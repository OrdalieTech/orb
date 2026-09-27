//go:build !windows

package jobs

import (
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
	"unicode/utf8"
)

func TestMain(m *testing.M) {
	tick = 20 * time.Millisecond
	os.Exit(m.Run())
}

var startedRE = regexp.MustCompile(`^Started background job (\d+) \(pid (\d+)\)\. Output: (.+?)\. A message`)

// launch starts a background job and returns its ID, process group and log.
func launch(h *harness, command string, monitor bool) (id string, group int, log string, err error) {
	mode := "run_in_background"
	if monitor {
		mode = "monitor"
	}
	text, err := call(h.bash, map[string]any{"command": command, mode: true})
	if err != nil {
		return "", 0, "", err
	}
	match := startedRE.FindStringSubmatch(text)
	if match == nil {
		return "", 0, "", fmt.Errorf("unexpected start message %q", text)
	}
	group, _ = strconv.Atoi(match[2])
	return match[1], group, match[3], nil
}

// gone waits for a process group to end.
func gone(group int) bool {
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		if syscall.Kill(-group, 0) != nil {
			return true
		}
	}
	return false
}

// report is what the messages say about one job.
type report struct {
	printed []string // monitored lines, with clipped ones as ""
	clipped []bool   // which printed lines were clipped out of a message
	end     string   // the end message
	late    int      // messages after the end
	count   int      // end messages
}

var clipRE = regexp.MustCompile(`^… (\d+) more lines in the log$`)

// reports sorts messages by job; a message about no known job fails the test.
func reports(t *testing.T, messages []string) map[string]*report {
	t.Helper()
	all := map[string]*report{}
	for _, message := range messages {
		id, rest, ok := strings.Cut(strings.TrimPrefix(message, "Job "), " ")
		if !ok || !strings.HasPrefix(message, "Job ") {
			t.Fatalf("stray message %q", message)
		}
		r := all[id]
		if r == nil {
			r = &report{}
			all[id] = r
		}
		if r.count > 0 {
			r.late++
		}
		body, printed := strings.CutPrefix(rest, "printed:\n")
		if !printed {
			r.end, r.count = message, r.count+1
			continue
		}
		lines := strings.Split(body, "\n")
		if len(lines) == maxLines+1 {
			if clip := clipRE.FindStringSubmatch(lines[maxLines]); clip != nil {
				hidden, _ := strconv.Atoi(clip[1])
				lines = lines[:maxLines]
				r.printed = append(r.printed, lines...)
				r.clipped = append(r.clipped, make([]bool, maxLines)...)
				for range hidden {
					r.printed, r.clipped = append(r.printed, ""), append(r.clipped, true)
				}
				continue
			}
		}
		r.printed = append(r.printed, lines...)
		r.clipped = append(r.clipped, make([]bool, len(lines))...)
	}
	return all
}

// matches reports whether the monitored lines are exactly want, in order,
// allowing lines a message clipped out.
func (r *report) matches(want []string) bool {
	if len(r.printed) != len(want) {
		return false
	}
	for i, line := range r.printed {
		if !r.clipped[i] && line != want[i] {
			return false
		}
	}
	return true
}

// expectLines is what monitor reports for output: its lines, each over the
// read buffer replaced by a placeholder.
func expectLines(output string) []string {
	if output == "" {
		return nil
	}
	lines := strings.Split(strings.TrimSuffix(output, "\n"), "\n")
	for i, line := range lines {
		if len(line) >= 64<<10 {
			lines[i] = "(a line over 64 KB, see the log)"
		}
	}
	return lines
}

// FuzzMonitoredOutput runs arbitrary output through a monitored job: the log
// holds it byte for byte and the messages report every line once, in order.
func FuzzMonitoredOutput(f *testing.F) {
	for _, seed := range []string{
		"", "\n", "plain", "two\nlines\n", "no newline at the end", "\n\n\nblank lines\n\n",
		`quotes ' " and \ backslashes \n $HOME $(echo no) ` + "`echo no`",
		"EOF\n'EOF'\n)\n# not a comment\n", "é 😀 日本語 \u202e rtl", "\r\ncarriage\rreturn\t\x1b[31mansi\x1b[0m",
		strings.Repeat("x", 64<<10-1) + "\n" + strings.Repeat("y", 64<<10) + "\nshort\n" + strings.Repeat("z", 200_000),
		strings.Repeat("many lines\n", 57),
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, output string) {
		if strings.IndexByte(output, 0) >= 0 || !utf8.ValidString(output) {
			t.Skip("tool arguments are JSON text, and a shell word cannot hold NUL")
		}
		h := newHarness(t)
		id, _, log, err := launch(h, "printf '%s' "+quote(output), true)
		if err != nil {
			t.Fatal(err)
		}
		h.wait(t, "Job "+id+" (")
		messages := h.snapshot()
		r := reports(t, messages)[id]
		if got, _ := os.ReadFile(log); string(got) != output {
			t.Fatalf("log = %q, want %q", got, output)
		}
		if r.count != 1 || r.late != 0 || !strings.Contains(r.end, "exited with code 0 after ") {
			t.Fatalf("end = %q (%d ends, %d late)", r.end, r.count, r.late)
		}
		if want := expectLines(output); !r.matches(want) {
			t.Fatalf("printed %q, want %q", r.printed, want)
		}
		for _, message := range messages {
			if !utf8.ValidString(message) {
				t.Fatalf("invalid UTF-8 in %q", message)
			}
		}
	})
}

// A command for the randomized run, with what it must report.
type command struct {
	script string
	code   string   // the exit code the end message gives
	lines  []string // every line it prints
	slow   bool     // runs long enough for stop_job and shutdown to catch it
	loose  bool     // its lines are the shell's, not checked
}

var commands = []command{
	{script: "exit 7", code: "7"},
	{script: "for i in $(seq 1 50); do echo n$i; done", code: "0", lines: numbered("n", 50)},
	{script: "printf 'no newline'", code: "0", lines: []string{"no newline"}},
	{script: "sleep 0.3; echo late", code: "0", lines: []string{"late"}},
	{script: "echo out; echo err >&2; exit 3", code: "3", lines: []string{"out", "err"}},
	{script: "for i in 1 2 3 4 5; do echo tick$i; sleep 0.05; done", code: "0", lines: numbered("tick", 5)},
	{script: "yes | head -n 5000 | wc -l | tr -d ' '", code: "0", lines: []string{"5000"}},
	{script: `echo 'é 😀 日本'; echo "$((6*7))"`, code: "0", lines: []string{"é 😀 日本", "42"}},
	{script: "echo bye; kill -KILL 0", code: "none (the job was killed)", lines: []string{"bye"}},
	{script: "kill -TERM $$", code: "143", loose: true}, // the log says why
	{script: "trap '' TERM; echo stubborn; sleep 2", code: "0", lines: []string{"stubborn"}, slow: true},
	{script: "sleep 2 & sleep 2", code: "0", slow: true},
	{script: "while :; do echo spin; sleep 0.01; done", slow: true},
}

func numbered(prefix string, n int) []string {
	lines := make([]string, n)
	for i := range lines {
		lines[i] = prefix + strconv.Itoa(i+1)
	}
	return lines
}

type step struct{ kind, command, pick int }

const (
	startBackground = iota
	startMonitor
	stopOne
	stopMissing
	foreground
	kinds
)

type tracked struct {
	command
	monitor           bool
	group             int
	stopping, stopped bool
	stopFailed        bool
}

// TestRandomOperations starts, monitors, stops and runs jobs from concurrent
// callers in a random order, then checks every report. Replay a failure with
// JOBS_SEED; raise JOBS_ROUNDS for a longer run.
func TestRandomOperations(t *testing.T) {
	seed := uint64(time.Now().UnixNano())
	if value := os.Getenv("JOBS_SEED"); value != "" {
		seed, _ = strconv.ParseUint(value, 10, 64)
	}
	rounds, _ := strconv.Atoi(os.Getenv("JOBS_ROUNDS"))
	if rounds == 0 {
		rounds = 4
	}
	if testing.Short() {
		rounds = 1
	}
	t.Logf("JOBS_SEED=%d", seed)
	for round := range rounds {
		t.Run(strconv.Itoa(round), func(t *testing.T) {
			t.Parallel()
			randomRound(t, rand.New(rand.NewPCG(seed, uint64(round))))
		})
	}
}

func randomRound(t *testing.T, rng *rand.Rand) {
	const callers, steps = 3, 6
	plans := make([][]step, callers)
	for c := range plans {
		for range steps {
			plans[c] = append(plans[c], step{rng.IntN(kinds), rng.IntN(len(commands)), rng.IntN(1 << 20)})
		}
	}
	// Half the rounds end the session while jobs still run.
	early := rng.IntN(2) == 0
	h := newHarness(t)
	var mu sync.Mutex
	jobs := map[string]*tracked{}
	var dir string
	var wg sync.WaitGroup
	for c, plan := range plans {
		wg.Go(func() {
			for n, s := range plan {
				switch s.kind {
				case startBackground, startMonitor:
					cmd := commands[s.command]
					id, group, log, err := launch(h, cmd.script, s.kind == startMonitor)
					if err != nil {
						if !strings.Contains(err.Error(), "background jobs already run") {
							t.Errorf("start %q: %v", cmd.script, err)
						}
						continue
					}
					mu.Lock()
					if jobs[id] != nil {
						t.Errorf("job ID %s given twice", id)
					}
					jobs[id], dir = &tracked{command: cmd, monitor: s.kind == startMonitor, group: group}, filepath.Dir(log)
					mu.Unlock()
				case stopOne:
					mu.Lock()
					var ids []string
					for id, j := range jobs {
						if !j.stopping {
							ids = append(ids, id)
						}
					}
					slices.Sort(ids)
					if len(ids) == 0 {
						mu.Unlock()
						continue
					}
					j := jobs[ids[s.pick%len(ids)]]
					j.stopping = true
					mu.Unlock()
					_, err := call(h.stop, map[string]any{"job": ids[s.pick%len(ids)]})
					mu.Lock()
					j.stopped, j.stopFailed = err == nil, err != nil
					mu.Unlock()
					if err == nil && !gone(j.group) {
						t.Errorf("process group %d survived stop_job", j.group)
					}
				case stopMissing:
					if _, err := call(h.stop, map[string]any{"job": "no-such-job"}); err == nil {
						t.Error("stop_job accepted an unknown job")
					}
				case foreground:
					want := fmt.Sprintf("fg-%d-%d", c, n)
					if got, err := call(h.bash, map[string]any{"command": "echo " + want}); err != nil || strings.TrimSpace(got) != want {
						t.Errorf("foreground = %q, %v", got, err)
					}
				}
			}
		})
	}
	wg.Wait()
	if t.Failed() {
		return
	}

	if early {
		h.shutdown()
		after := len(h.snapshot())
		for _, j := range jobs {
			if !gone(j.group) {
				t.Errorf("process group %d (%q) survived the session", j.group, j.script)
			}
		}
		if dir != "" {
			if _, err := os.Stat(dir); !os.IsNotExist(err) {
				t.Errorf("job directory %s left behind: %v", dir, err)
			}
		}
		time.Sleep(10 * tick)
		messages := h.snapshot()
		if len(messages) != after {
			t.Errorf("messages after shutdown: %q", messages[after:])
		}
		for id, r := range reports(t, messages) {
			j := jobs[id]
			if r.count > 1 || r.count == 1 && (j.stopped || !strings.Contains(r.end, "exited with code "+j.code+" after ")) {
				t.Errorf("job %s (%q, stopped %v): %d ends, last %q", id, j.script, j.stopped, r.count, r.end)
			}
		}
		return
	}

	// A spinning job ends only when stopped.
	for id, j := range jobs {
		if j.code == "" && !j.stopping {
			_, err := call(h.stop, map[string]any{"job": id})
			if j.stopped = err == nil; err != nil || !gone(j.group) {
				t.Errorf("stop spinning job %s: %v", id, err)
			}
		}
	}
	// Every job that was not stopped reports its end; the slow ones within seconds.
	for deadline := time.Now().Add(20 * time.Second); ; time.Sleep(tick) {
		done := reports(t, h.snapshot())
		waiting := 0
		for id, j := range jobs {
			if r := done[id]; !j.stopped && (r == nil || r.count == 0) {
				waiting++
			}
		}
		if waiting == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("%d jobs never reported their end", waiting)
		}
	}
	time.Sleep(10 * tick)
	all := reports(t, h.snapshot())
	for id, j := range jobs {
		r := all[id]
		if r == nil {
			r = &report{}
		}
		switch {
		case j.stopped:
			if r.count != 0 {
				t.Errorf("stopped job %s (%q) reported %q", id, j.script, r.end)
			}
			continue
		case r.count != 1 || r.late != 0:
			t.Errorf("job %s (%q): %d ends, %d messages after", id, j.script, r.count, r.late)
		case !strings.Contains(r.end, "exited with code "+j.code+" after "):
			t.Errorf("job %s (%q) end = %q, want code %s", id, j.script, r.end, j.code)
		}
		if j.loose {
			continue
		}
		if j.monitor && !r.matches(j.lines) {
			t.Errorf("monitored job %s (%q) printed %q, want %q", id, j.script, r.printed, j.lines)
		}
		if !j.monitor {
			if len(r.printed) != 0 {
				t.Errorf("unmonitored job %s printed %q", id, r.printed)
			}
			_, tail, _ := strings.Cut(r.end, "\nLast lines:\n")
			if want := strings.Join(j.lines[max(0, len(j.lines)-10):], "\n"); tail != want {
				t.Errorf("job %s (%q) last lines = %q, want %q", id, j.script, tail, want)
			}
		}
	}
}

// The ninth job is refused until one of the eight ends.
func TestJobLimit(t *testing.T) {
	h := newHarness(t)
	for range maxJobs {
		if _, _, _, err := launch(h, "sleep 30", false); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, _, err := launch(h, "sleep 30", false); err == nil || !strings.Contains(err.Error(), "already run") {
		t.Fatalf("ninth job: %v", err)
	}
	h.run(t, h.stop, map[string]any{"job": "3"})
	if _, _, _, err := launch(h, "sleep 30", true); err != nil {
		t.Fatalf("job after a stop: %v", err)
	}
}

// Ending the session ends every job at once, those that ignore TERM included.
func TestShutdownEndsEveryJob(t *testing.T) {
	h := newHarness(t)
	var groups []int
	var dir string
	for i := range 6 {
		script := "sleep 30 & sleep 30"
		if i%2 == 0 {
			script = "trap '' TERM; sleep 30"
		}
		_, group, log, err := launch(h, script, i%3 == 0)
		if err != nil {
			t.Fatal(err)
		}
		groups, dir = append(groups, group), filepath.Dir(log)
	}
	time.Sleep(5 * tick)
	start := time.Now()
	h.shutdown()
	for _, group := range groups {
		if !gone(group) {
			t.Errorf("process group %d survived the session", group)
		}
	}
	if took := time.Since(start); took > 4*time.Second {
		t.Errorf("shutdown took %v", took)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("job directory left behind: %v", err)
	}
	if messages := h.snapshot(); len(messages) != 0 {
		t.Errorf("messages after shutdown: %q", messages)
	}
}

// oneLine keeps a job's command readable in its report, whatever it holds.
func FuzzOneLine(f *testing.F) {
	for _, seed := range []string{"", "ls", "a\nb", strings.Repeat("é", 100), strings.Repeat("x", 79) + "😀"} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, command string) {
		got := oneLine(command)
		if strings.Contains(got, "\n") || utf8.RuneCountInString(got) > 81 {
			t.Fatalf("oneLine(%q) = %q", command, got)
		}
		if utf8.ValidString(command) && !utf8.ValidString(got) {
			t.Fatalf("oneLine(%q) = %q, invalid UTF-8", command, got)
		}
	})
}
