package modes

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// modesTestHelperEnv turns the test binary into a fake child process (fd, an
// external editor, an RPC agent) so those fixtures run on every platform
// without a POSIX shell.
const modesTestHelperEnv = "ORB_MODES_TEST_HELPER"

const modesTestStdoutEnv = "ORB_MODES_TEST_STDOUT"

func TestMain(m *testing.M) {
	if helper := os.Getenv(modesTestHelperEnv); helper != "" {
		os.Exit(runModesTestHelper(helper, os.Args[1:]))
	}
	os.Exit(m.Run())
}

func runModesTestHelper(helper string, args []string) int {
	switch helper {
	case "stdout":
		_, _ = io.WriteString(os.Stdout, os.Getenv(modesTestStdoutEnv))
		return 0
	case "sleep":
		time.Sleep(time.Second)
		return 0
	case "editor":
		return fakeExternalEditorProcess(args)
	}
	if scenario, ok := strings.CutPrefix(helper, "rpc-"); ok {
		return fakeRPCAgent(scenario)
	}
	_, _ = fmt.Fprintf(os.Stderr, "unknown test helper %q\n", helper)
	return 2
}

// fakeExternalEditorProcess mirrors upstream test/fixtures/fake-external-editor.mjs:
// arguments are the capture path, optional flags, then the prompt file.
func fakeExternalEditorProcess(args []string) int {
	capture, file := args[0], args[len(args)-1]
	content, _ := os.ReadFile(file)
	entries, _ := os.ReadDir(filepath.Dir(file))
	var listing strings.Builder
	for _, entry := range entries {
		listing.WriteString(entry.Name() + " ")
	}
	record := "file:" + file + "\ncontent:" + strings.TrimRight(string(content), "\n") + "\nentries:" + listing.String() + "\n"
	if err := os.WriteFile(capture, []byte(record), 0o600); err != nil {
		return 3
	}
	flags := strings.Join(args[1:len(args)-1], " ")
	switch {
	case strings.Contains(flags, "--fail"):
		return 1
	case strings.Contains(flags, "--empty"):
		_ = os.WriteFile(file, nil, 0o600)
	default:
		_ = os.WriteFile(file, []byte("edited\n"), 0o600)
	}
	return 0
}

const fakeRPCGetState = `{"id":"req_1","type":"response","command":"get_state","success":true,"data":{"thinkingLevel":"off","isStreaming":false,"isCompacting":false,"steeringMode":"all","followUpMode":"all","sessionId":"session","autoCompactionEnabled":true,"messageCount":2,"pendingMessageCount":0}}`

func fakeRPCAgent(scenario string) int {
	input := bufio.NewReader(os.Stdin)
	readLine := func() (string, bool) {
		line, err := input.ReadString('\n')
		return line, err == nil
	}
	drain := func() {
		for {
			if _, ok := readLine(); !ok {
				return
			}
		}
	}
	write := func(lines ...string) {
		for _, line := range lines {
			_, _ = io.WriteString(os.Stdout, line)
		}
	}
	switch scenario {
	case "lifecycle":
		readLine()
		write("not-json\n", `{"type":"queue_update","steering":["a`+"\u2028b\u2029c"+`"],"followUp":[]}`+"\r\n", fakeRPCGetState+"\n")
		drain()
	case "exit":
		readLine()
		_, _ = io.WriteString(os.Stderr, "child diagnostic")
		return 43
	case "stderr":
		readLine()
		_, _ = io.WriteString(os.Stderr, "waiting")
		drain()
	case "descendant":
		signal.Ignore(syscall.SIGTERM)
		descendant := exec.Command(os.Args[0])
		descendant.Env = append(os.Environ(), modesTestHelperEnv+"=sleep")
		descendant.Stdout, descendant.Stderr = os.Stdout, os.Stderr
		if err := descendant.Start(); err != nil {
			return 3
		}
		for {
			time.Sleep(time.Second)
		}
	case "typed":
		for {
			line, ok := readLine()
			if !ok {
				return 0
			}
			switch {
			case containsInOrder(line, `"type":"prompt"`, `"message":"hello"`):
				write(`{"id":"req_1","type":"response","command":"prompt","success":true}` + "\n")
			case containsInOrder(line, `"type":"set_model"`, `"provider":"openai"`, `"modelId":"gpt-test"`):
				write(`{"id":"req_2","type":"response","command":"set_model","success":true,"data":{"id":"gpt-test","name":"Test","api":"openai-responses","provider":"openai","baseUrl":"https://example.test","reasoning":false,"input":["text"],"cost":{"input":0,"output":0,"cacheRead":0,"cacheWrite":0},"contextWindow":1000,"maxTokens":100}}` + "\n")
			case containsInOrder(line, `"type":"get_available_thinking_levels"`):
				write(`{"id":"req_3","type":"response","command":"get_available_thinking_levels","success":true,"data":{"levels":["off","high"]}}` + "\n")
			case containsInOrder(line, `"type":"clone"`):
				write(`{"id":"req_4","type":"response","command":"clone","success":true,"data":{"cancelled":false}}` + "\n")
			default:
				write(`{"id":"unknown","type":"response","command":"unknown","success":false,"error":"bad command"}` + "\n")
			}
		}
	case "reentrant":
		for {
			line, ok := readLine()
			if !ok {
				return 0
			}
			switch {
			case strings.Contains(line, `"type":"prompt"`):
				write(`{"type":"queue_update","steering":["one"],"followUp":[]}`+"\n", `{"type":"response","command":"prompt","success":true,"id":"req_1"}`+"\n")
			case strings.Contains(line, `"type":"get_state"`):
				write(`{"type":"response","command":"get_state","success":true,"data":{"thinkingLevel":"off","isStreaming":false,"isCompacting":false,"steeringMode":"all","followUpMode":"all","sessionId":"reentrant","autoCompactionEnabled":true,"messageCount":0,"pendingMessageCount":0},"id":"req_2"}` + "\n")
			}
		}
	case "panic":
		readLine()
		write(`{"type":"queue_update","steering":["one"],"followUp":[]}`+"\n",
			`{"type":"response","command":"prompt","success":true,"id":"req_1"}`+"\n",
			`{"type":"queue_update","steering":["two"],"followUp":[]}`+"\n")
		drain()
	case "event":
		readLine()
		write(`{"type":"queue_update","steering":[],"followUp":[]}` + "\n")
		drain()
	case "idle":
		drain()
	default:
		_, _ = fmt.Fprintf(os.Stderr, "unknown RPC scenario %q\n", scenario)
		return 2
	}
	return 0
}

func containsInOrder(line string, parts ...string) bool {
	for _, part := range parts {
		index := strings.Index(line, part)
		if index < 0 {
			return false
		}
		line = line[index+len(part):]
	}
	return true
}

// rpcClientHelper points an RPC client at the test binary acting as the named
// fake agent scenario.
func rpcClientHelper(scenario string) RPCClientOptions {
	return RPCClientOptions{CLIPath: os.Args[0], Env: map[string]string{modesTestHelperEnv: "rpc-" + scenario}}
}
