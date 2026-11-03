package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/egoist/mygo"
)

// Orb is the app's side of `orb app`, the view every Orb app draws (bridge/view): what it shows,
// kept as the view sends it, and the intents it sends back. Its fields are read and written on the
// main thread only.
type Orb struct {
	win     *mygo.Window
	state   State
	home    Home
	rows    map[string][]Row // each followed conversation's rows, as the view last patched them
	alert   func(tab, title, text string)
	visible bool
	shown   []string
	began   time.Time // when the app started
	running string    // the orb `orb app` runs, once started

	mu      sync.Mutex // guards what the reader and the askers share
	in      *os.File
	asked   int
	replies map[string]chan reply
}

type reply struct {
	Result json.RawMessage `json:"result"`
	Error  string          `json:"error"`
}

// device is what the view calls this machine.
const device = "this computer"

func newOrb(win *mygo.Window, alert func(tab, title, text string)) *Orb {
	o := &Orb{win: win, alert: alert, rows: map[string][]Row{}, replies: map[string]chan reply{}, began: time.Now()}
	o.state.Summary = "starting Bridge…"
	go o.run()
	return o
}

// binary is $ORB, else the orb this app carries, else the one on PATH.
func binary() string {
	if p := os.Getenv("ORB"); p != "" {
		return p
	}
	name := "orb"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	if dir, err := mygo.App.Path(mygo.PathResources); err == nil {
		if p := filepath.Join(dir, "bin", name); executable(p) {
			return p
		}
	}
	if p, err := exec.LookPath(name); err == nil {
		return p
	}
	return name
}

func executable(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir() && (runtime.GOOS == "windows" || st.Mode()&0o111 != 0)
}

// run keeps `orb app` running for as long as the app lives; one that ends starts again, hears what
// this side shows, and sends everything again.
func (o *Orb) run() {
	wait := time.Second
	for {
		began := time.Now()
		err := o.serve()
		o.mu.Lock()
		o.in = nil
		for id, r := range o.replies {
			r <- reply{Error: "Orb restarted"}
			delete(o.replies, id)
		}
		o.mu.Unlock()
		// One that keeps failing is started less and less often, and says why.
		if time.Since(began) > 30*time.Second {
			wait = time.Second
		}
		o.win.Update(func() { o.state.Up, o.state.Summary = false, "orb app ended: "+err.Error() })
		time.Sleep(wait)
		wait = min(wait*2, 30*time.Second)
	}
}

// tail keeps the end of what a process wrote, to say why it ended.
type tail struct {
	mu sync.Mutex
	b  []byte
}

func (t *tail) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.b = append(t.b, p...)
	t.b = t.b[max(len(t.b)-512, 0):]
	return len(p), nil
}

func (t *tail) last() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	lines := strings.Split(strings.TrimSpace(string(t.b)), "\n")
	return lines[len(lines)-1]
}

func (o *Orb) serve() error {
	cmd := exec.Command(binary(), "app", "--name", device)
	cmd.Env = environment()
	cmd.Dir, _ = os.UserHomeDir()
	errs := &tail{}
	cmd.Stderr = errs
	in, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	// On the main thread, so a `show` the view sends meanwhile cannot overtake this one.
	mygo.RunOnMain(func() {
		o.mu.Lock()
		o.in = in.(*os.File)
		o.mu.Unlock()
		o.send("hello", "budget", 4)
		o.send("visible", "on", o.visible)
		o.send("show", "tabs", o.shown)
		o.running = cmd.Path
		o.win.Invalidate()
	})
	// A line is as long as what it carries: a whole conversation, after hello or earlier messages.
	lines := bufio.NewReaderSize(out, 64<<10)
	for {
		line, err := lines.ReadBytes('\n')
		if len(line) > 0 {
			o.heard(line)
		}
		if err != nil {
			break
		}
	}
	_ = cmd.Process.Kill()
	err = cmd.Wait()
	if why := errs.last(); why != "" {
		return errors.New(why)
	}
	if err == nil {
		err = errors.New("it stopped")
	}
	return err
}

func (o *Orb) heard(line []byte) {
	var m struct {
		T    string `json:"t"`
		ID   string `json:"id"`
		Tab  string `json:"tab"`
		At   int    `json:"at"`
		Rows []Row  `json:"rows"`
	}
	if json.Unmarshal(line, &m) != nil {
		return
	}
	switch m.T {
	case "reply":
		var r reply
		_ = json.Unmarshal(line, &r)
		o.mu.Lock()
		c := o.replies[m.ID]
		delete(o.replies, m.ID)
		o.mu.Unlock()
		if c != nil {
			c <- r
		}
	case "state":
		var s State
		_ = json.Unmarshal(line, &s)
		o.win.Update(func() {
			o.state = s
			for id := range o.rows {
				if o.tab(id) == nil {
					delete(o.rows, id)
				}
			}
		})
	case "home":
		// Whole each time, and never decoded into what the window draws: the view leaves out what is false.
		var home Home
		_ = json.Unmarshal(line, &home)
		o.win.Update(func() { o.home = home })
	case "rows":
		o.win.Update(func() {
			rows := o.rows[m.Tab]
			o.rows[m.Tab] = append(rows[:min(m.At, len(rows)):min(m.At, len(rows))], m.Rows...)
		})
	case "alert":
		var a struct{ Tab, Title, Text string }
		_ = json.Unmarshal(line, &a)
		o.win.Update(func() { o.alert(a.Tab, a.Title, a.Text) })
	}
}

// intent is an intent's fields, given as name, value pairs.
func intent(name string, args []any) map[string]any {
	m := map[string]any{"do": name}
	for i := 0; i+1 < len(args); i += 2 {
		m[args[i].(string)] = args[i+1]
	}
	return m
}

func (o *Orb) write(m map[string]any) error {
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.in == nil {
		return errors.New("Orb is not running yet")
	}
	_, err = o.in.Write(append(b, '\n'))
	return err
}

// send asks the view something it does at once or on its own time; nothing comes back.
func (o *Orb) send(name string, args ...any) { _ = o.write(intent(name, args)) }

// ask asks the view something and waits for its reply; never on the main thread.
func (o *Orb) ask(name string, args ...any) (json.RawMessage, error) {
	o.mu.Lock()
	o.asked++
	id := strconv.Itoa(o.asked)
	c := make(chan reply, 1)
	o.replies[id] = c
	o.mu.Unlock()
	m := intent(name, args)
	m["id"] = id
	if err := o.write(m); err != nil {
		o.mu.Lock()
		delete(o.replies, id)
		o.mu.Unlock()
		return nil, err
	}
	select {
	case r := <-c:
		if r.Error != "" {
			return nil, errors.New(r.Error)
		}
		return r.Result, nil
	case <-time.After(2 * time.Minute):
		o.mu.Lock()
		delete(o.replies, id)
		o.mu.Unlock()
		return nil, errors.New("no answer")
	}
}

// then asks on a goroutine of its own and hands the reply, decoded into a T, to done on the main thread.
func then[T any](o *Orb, done func(T, error), name string, args ...any) {
	go func() {
		var v T
		raw, err := o.ask(name, args...)
		if err == nil && len(raw) > 0 && string(raw) != "null" {
			err = json.Unmarshal(raw, &v)
		}
		o.win.Update(func() { done(v, err) })
	}()
}

func (o *Orb) setVisible(on bool) {
	if on != o.visible {
		o.visible = on
		o.send("visible", "on", on)
	}
}

func (o *Orb) show(tabs []string) {
	if !slices.Equal(tabs, o.shown) {
		o.shown = tabs
		o.send("show", "tabs", tabs)
	}
}

func (o *Orb) tab(id string) *Tab {
	for i := range o.state.Tabs {
		if o.state.Tabs[i].ID == id {
			return &o.state.Tabs[i]
		}
	}
	return nil
}

func (o *Orb) machine(id string) *Machine {
	for i := range o.home.Machines {
		if o.home.Machines[i].ID == id {
			return &o.home.Machines[i]
		}
	}
	return nil
}

// hue is what a machine's sessions are told apart by: 0 for this computer, 1 to 6 for a peer, as
// the view assigns them.
func (o *Orb) hue(peer string) int {
	if peer == "" || peer == o.state.Self {
		return 0
	}
	if m := o.machine(peer); m != nil {
		return max(m.Hue, 1)
	}
	return 1
}

// environment is the login shell's: an app opened from the Finder or a desktop launcher gets a bare
// PATH, and the Orbs this machine's Bridge starts, when this app starts it, run with what the
// terminal has.
var environment = sync.OnceValue(func() []string {
	env := os.Environ()
	shell := os.Getenv("SHELL")
	if runtime.GOOS == "windows" || shell == "" {
		return env
	}
	cmd := exec.Command(shell, "-ilc", "printf '\\0\\0'; env -0")
	cmd.WaitDelay = time.Second
	done := time.AfterFunc(5*time.Second, func() { _ = cmd.Process.Kill() })
	out, err := cmd.Output()
	done.Stop()
	// What the shell's startup printed comes before the marker.
	_, vars, ok := strings.Cut(string(out), "\x00\x00")
	if err != nil || !ok {
		return env
	}
	return strings.Split(strings.TrimSuffix(vars, "\x00"), "\x00")
})
