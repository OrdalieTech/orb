package tools

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/OrdalieTech/pi-go/agent"
	"github.com/OrdalieTech/pi-go/ai"
	"github.com/OrdalieTech/pi-go/internal/jsonschema"
	"github.com/OrdalieTech/pi-go/internal/truncate"
)

const bashUpdateThrottle = 100 * time.Millisecond

var bashSchema = jsonschema.Schema(`{"type":"object","required":["command"],"properties":{"command":{"type":"string","description":"Bash command to execute"},"timeout":{"type":"number","description":"Timeout in seconds (optional, no default timeout)"}}}`)

type BashToolInput struct {
	Command string   `json:"command"`
	Timeout *float64 `json:"timeout,omitempty"`
}

type BashToolDetails struct {
	Truncation     *truncate.Result `json:"truncation,omitempty"`
	FullOutputPath string           `json:"fullOutputPath,omitempty"`
}

type BashExecOptions struct {
	OnData  func([]byte)
	Timeout *float64
	Env     map[string]string
}

type BashExecResult struct {
	ExitCode *int
}

// BashOperations is the command-execution delegation seam used by local and
// remote backends.
type BashOperations interface {
	Exec(context.Context, string, string, BashExecOptions) (BashExecResult, error)
}

type LocalBashOperationsOptions struct {
	ShellPath string
}

type BashSpawnContext struct {
	Command string
	Cwd     string
	Env     map[string]string
}

type BashSpawnHook func(BashSpawnContext) BashSpawnContext

type BashToolOptions struct {
	Operations    BashOperations
	CommandPrefix string
	ShellPath     string
	SpawnHook     BashSpawnHook
}

type bashTool struct {
	cwd           string
	operations    BashOperations
	commandPrefix string
	spawnHook     BashSpawnHook
}

func NewBashTool(cwd string, options *BashToolOptions) agent.AgentTool {
	operations := NewLocalBashOperations()
	commandPrefix := ""
	var spawnHook BashSpawnHook
	if options != nil {
		if options.Operations != nil {
			operations = options.Operations
		} else {
			operations = NewLocalBashOperations(LocalBashOperationsOptions{ShellPath: options.ShellPath})
		}
		commandPrefix = options.CommandPrefix
		spawnHook = options.SpawnHook
	}
	return &bashTool{
		cwd:           cwd,
		operations:    operations,
		commandPrefix: commandPrefix,
		spawnHook:     spawnHook,
	}
}

func (tool *bashTool) Spec() agent.AgentToolSpec {
	return agent.AgentToolSpec{
		Name:        "bash",
		Label:       "bash",
		Description: fmt.Sprintf("Execute a bash command in the current working directory. Returns stdout and stderr. Output is truncated to last %d lines or %dKB (whichever is hit first). If truncated, full output is saved to a temp file. Optionally provide a timeout in seconds.", truncate.DefaultMaxLines, truncate.DefaultMaxBytes/1024),
		Parameters:  bashSchema,
	}
}

func (tool *bashTool) Execute(
	ctx context.Context,
	_ string,
	params any,
	onUpdate agent.AgentToolUpdateCallback,
) (agent.AgentToolResult, error) {
	input, err := bashInput(params)
	if err != nil {
		return agent.AgentToolResult{}, err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	command := input.Command
	if tool.commandPrefix != "" {
		command = tool.commandPrefix + "\n" + command
	}
	environment, err := GetShellEnv()
	if err != nil {
		return agent.AgentToolResult{}, err
	}
	spawnContext := BashSpawnContext{Command: command, Cwd: tool.cwd, Env: environment}
	if tool.spawnHook != nil {
		spawnContext = tool.spawnHook(spawnContext)
	}

	output := newBashOutputStream(onUpdate)
	if onUpdate != nil {
		onUpdate(agent.AgentToolResult{Content: ai.ToolResultContent{}})
	}
	result, executeErr := tool.operations.Exec(ctx, spawnContext.Command, spawnContext.Cwd, BashExecOptions{
		OnData:  output.Append,
		Timeout: input.Timeout,
		Env:     spawnContext.Env,
	})
	snapshot, finishErr := output.Finish()
	if finishErr != nil {
		return agent.AgentToolResult{}, finishErr
	}

	formatted, details := formatBashOutput(output.accumulator, snapshot, "(no output)")
	if executeErr != nil {
		message := executeErr.Error()
		switch {
		case message == "aborted":
			return agent.AgentToolResult{}, upstreamToolError(appendBashStatus(formatBashSnapshot(output.accumulator, snapshot, ""), "Command aborted"))
		case strings.HasPrefix(message, "timeout:"):
			timeoutSeconds := strings.Split(message, ":")[1]
			return agent.AgentToolResult{}, upstreamToolError(appendBashStatus(formatBashSnapshot(output.accumulator, snapshot, ""), "Command timed out after "+timeoutSeconds+" seconds"))
		default:
			return agent.AgentToolResult{}, executeErr
		}
	}
	if result.ExitCode != nil && *result.ExitCode != 0 {
		return agent.AgentToolResult{}, upstreamToolError(appendBashStatus(formatted, fmt.Sprintf("Command exited with code %d", *result.ExitCode)))
	}
	return agent.AgentToolResult{
		Content: ai.ToolResultContent{&ai.TextContent{Text: formatted}},
		Details: details,
	}, nil
}

func (tool *bashTool) RenderCall(args any) string {
	object := renderArgs(args)
	command, _ := object["command"].(string)
	if command == "" {
		command = "..."
	}
	text := "$ " + command
	if timeout, err := optionalNumber(object, "timeout"); err == nil && timeout != nil && *timeout != 0 {
		text += " (timeout " + formatJSNumber(*timeout) + "s)"
	}
	return text
}

func (*bashTool) RenderResult(result agent.AgentToolResult) string {
	return renderTextResult(result)
}

func bashInput(params any) (BashToolInput, error) {
	object, err := toolParams(params)
	if err != nil {
		return BashToolInput{}, err
	}
	command, err := requiredString(object, "command")
	if err != nil {
		return BashToolInput{}, err
	}
	timeout, err := optionalNumber(object, "timeout")
	if err != nil {
		return BashToolInput{}, err
	}
	return BashToolInput{Command: command, Timeout: timeout}, nil
}

func formatBashOutput(
	output *OutputAccumulator,
	snapshot OutputSnapshot,
	emptyText string,
) (string, any) {
	text := snapshot.Content
	if text == "" {
		text = emptyText
	}
	if !snapshot.Truncation.Truncated {
		return text, nil
	}
	truncation := snapshot.Truncation
	startLine := truncation.TotalLines - truncation.OutputLines + 1
	endLine := truncation.TotalLines
	switch {
	case truncation.LastLinePartial:
		text += fmt.Sprintf("\n\n[Showing last %s of line %d (line is %s). Full output: %s]", truncate.FormatSize(truncation.OutputBytes), endLine, truncate.FormatSize(output.LastLineBytes()), snapshot.FullOutputPath)
	case truncation.TruncatedBy != nil && *truncation.TruncatedBy == truncate.ReasonLines:
		text += fmt.Sprintf("\n\n[Showing lines %d-%d of %d. Full output: %s]", startLine, endLine, truncation.TotalLines, snapshot.FullOutputPath)
	default:
		text += fmt.Sprintf("\n\n[Showing lines %d-%d of %d (%s limit). Full output: %s]", startLine, endLine, truncation.TotalLines, truncate.FormatSize(truncate.DefaultMaxBytes), snapshot.FullOutputPath)
	}
	return text, BashToolDetails{Truncation: &truncation, FullOutputPath: snapshot.FullOutputPath}
}

func formatBashSnapshot(output *OutputAccumulator, snapshot OutputSnapshot, emptyText string) string {
	text, _ := formatBashOutput(output, snapshot, emptyText)
	return text
}

func appendBashStatus(text, status string) string {
	if text == "" {
		return status
	}
	return text + "\n\n" + status
}

type bashOutputStream struct {
	mu          sync.Mutex
	accumulator *OutputAccumulator
	onUpdate    agent.AgentToolUpdateCallback
	accepting   bool
	dirty       bool
	lastUpdate  time.Time
	timer       *time.Timer
	err         error
}

func newBashOutputStream(onUpdate agent.AgentToolUpdateCallback) *bashOutputStream {
	prefix := "pi-bash"
	return &bashOutputStream{
		accumulator: NewOutputAccumulator(OutputAccumulatorOptions{TempFilePrefix: &prefix}),
		onUpdate:    onUpdate,
		accepting:   true,
	}
}

func (stream *bashOutputStream) Append(data []byte) {
	stream.mu.Lock()
	defer stream.mu.Unlock()
	if !stream.accepting || stream.err != nil {
		return
	}
	if err := stream.accumulator.Append(data); err != nil {
		stream.err = err
		return
	}
	stream.scheduleUpdateLocked()
}

func (stream *bashOutputStream) Finish() (OutputSnapshot, error) {
	stream.mu.Lock()
	stream.accepting = false
	if stream.timer != nil {
		stream.timer.Stop()
		stream.timer = nil
	}
	finishErr := stream.accumulator.Finish()
	if stream.err == nil {
		stream.err = finishErr
	}
	if stream.err == nil && stream.dirty {
		stream.emitUpdateLocked()
	}
	var snapshot OutputSnapshot
	if stream.err == nil {
		snapshot, stream.err = stream.accumulator.Snapshot(OutputSnapshotOptions{PersistIfTruncated: true})
	}
	stream.mu.Unlock()

	closeErr := stream.accumulator.CloseTempFile()
	if stream.err != nil {
		return OutputSnapshot{}, stream.err
	}
	if closeErr != nil {
		return OutputSnapshot{}, closeErr
	}
	return snapshot, nil
}

func (stream *bashOutputStream) scheduleUpdateLocked() {
	if stream.onUpdate == nil {
		return
	}
	stream.dirty = true
	delay := bashUpdateThrottle - time.Since(stream.lastUpdate)
	if stream.lastUpdate.IsZero() || delay <= 0 {
		if stream.timer != nil {
			stream.timer.Stop()
			stream.timer = nil
		}
		stream.emitUpdateLocked()
		return
	}
	if stream.timer == nil {
		stream.timer = time.AfterFunc(delay, func() {
			stream.mu.Lock()
			stream.timer = nil
			if stream.accepting && stream.err == nil {
				stream.emitUpdateLocked()
			}
			stream.mu.Unlock()
		})
	}
}

func (stream *bashOutputStream) emitUpdateLocked() {
	if stream.onUpdate == nil || !stream.dirty {
		return
	}
	stream.dirty = false
	stream.lastUpdate = time.Now()
	snapshot, err := stream.accumulator.Snapshot(OutputSnapshotOptions{PersistIfTruncated: true})
	if err != nil {
		stream.err = err
		return
	}
	var truncation *truncate.Result
	if snapshot.Truncation.Truncated {
		value := snapshot.Truncation
		truncation = &value
	}
	stream.onUpdate(agent.AgentToolResult{
		Content: ai.ToolResultContent{&ai.TextContent{Text: snapshot.Content}},
		Details: BashToolDetails{Truncation: truncation, FullOutputPath: snapshot.FullOutputPath},
	})
}

var _ agent.AgentTool = (*bashTool)(nil)
var _ PlainTextRenderer = (*bashTool)(nil)
