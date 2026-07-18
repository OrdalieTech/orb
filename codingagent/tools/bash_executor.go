package tools

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"sync"
	"unicode"
)

type BashResult struct {
	Output         string `json:"output"`
	ExitCode       *int   `json:"exitCode,omitempty"`
	Cancelled      bool   `json:"cancelled"`
	Truncated      bool   `json:"truncated"`
	FullOutputPath string `json:"fullOutputPath,omitempty"`
}

var rpcANSISequence = regexp.MustCompile(`(?:\x1b\][\s\S]*?(?:\x07|\x1b\\|\x{009c}))|(?:[\x1b\x{009b}][[\]()#;?]*(?:[0-9]{1,4}(?:[;:][0-9]{0,4})*)?[0-9A-PR-TZcf-nq-uy=><~])`)

// ExecuteBash mirrors the session-level bash executor rather than the bash
// tool: a non-zero exit is data in RPC mode, while cancellation is reported in
// the result instead of becoming a tool error.
func ExecuteBash(ctx context.Context, command, cwd, prefix, shellPath string) (BashResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	resolvedCommand := command
	if prefix != "" {
		resolvedCommand = prefix + "\n" + command
	}
	environment, err := GetShellEnv()
	if err != nil {
		return BashResult{}, err
	}
	tempPrefix := "pi-bash"
	output := NewOutputAccumulator(OutputAccumulatorOptions{TempFilePrefix: &tempPrefix})
	var decoder streamingUTF8Decoder
	operations := NewLocalBashOperations(LocalBashOperationsOptions{ShellPath: shellPath})
	var appendMu sync.Mutex
	var appendErr error
	result, executeErr := operations.Exec(ctx, resolvedCommand, cwd, BashExecOptions{
		Env: environment,
		OnData: func(chunk []byte) {
			appendMu.Lock()
			defer appendMu.Unlock()
			if appendErr == nil {
				appendErr = output.appendTransformed(len(chunk), sanitizeBashOutput(decoder.Decode(chunk, false)))
			}
		},
	})
	appendMu.Lock()
	streamErr := appendErr
	appendMu.Unlock()
	if streamErr != nil {
		return BashResult{}, errors.Join(streamErr, output.CloseTempFile())
	}
	if finishErr := output.Finish(); finishErr != nil {
		_ = output.CloseTempFile()
		return BashResult{}, finishErr
	}
	snapshot, snapshotErr := output.Snapshot(OutputSnapshotOptions{PersistIfTruncated: true})
	closeErr := output.CloseTempFile()
	if snapshotErr != nil {
		return BashResult{}, snapshotErr
	}
	if closeErr != nil {
		return BashResult{}, closeErr
	}
	response := BashResult{
		Output: snapshot.Content, ExitCode: result.ExitCode,
		Truncated: snapshot.Truncation.Truncated, FullOutputPath: snapshot.FullOutputPath,
	}
	if executeErr == nil {
		return response, nil
	}
	if errors.Is(ctx.Err(), context.Canceled) || executeErr.Error() == "aborted" {
		response.ExitCode = nil
		response.Cancelled = true
		return response, nil
	}
	return BashResult{}, executeErr
}

func sanitizeBashOutput(value string) string {
	value = rpcANSISequence.ReplaceAllString(value, "")
	value = strings.ReplaceAll(value, "\r", "")
	return strings.Map(func(character rune) rune {
		switch {
		case character == '\t' || character == '\n':
			return character
		case character <= 0x1f:
			return -1
		case character >= 0xfff9 && character <= 0xfffb:
			return -1
		case character == unicode.ReplacementChar:
			return character
		default:
			return character
		}
	}, value)
}
