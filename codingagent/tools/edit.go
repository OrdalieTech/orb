package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strconv"
	"strings"
	"syscall"
	"unicode/utf16"

	"github.com/OrdalieTech/pi-go/agent"
	"github.com/OrdalieTech/pi-go/ai"
	"github.com/OrdalieTech/pi-go/internal/jsonschema"
)

var editSchema = jsonschema.Schema(`{"type":"object","required":["path","edits"],"properties":{"path":{"type":"string","description":"Path to the file to edit (relative or absolute)"},"edits":{"type":"array","items":{"type":"object","required":["oldText","newText"],"properties":{"oldText":{"type":"string","description":"Exact text for one targeted replacement. It must be unique in the original file and must not overlap with any other edits[].oldText in the same call."},"newText":{"type":"string","description":"Replacement text for this targeted edit."}}},"description":"One or more targeted replacements. Each edit is matched against the original file, not incrementally. Do not include overlapping or nested edits. If two changes touch the same block or nearby lines, merge them into one edit instead."}}}`)

type EditToolInput struct {
	Path  string `json:"path"`
	Edits []Edit `json:"edits"`
}

type EditToolDetails struct {
	Diff             string `json:"diff"`
	Patch            string `json:"patch"`
	FirstChangedLine *int   `json:"firstChangedLine,omitempty"`
}

// EditOperations is the delegation seam for the edit tool.
type EditOperations interface {
	ReadFile(context.Context, string) ([]byte, error)
	WriteFile(context.Context, string, string) error
	Access(context.Context, string) error
}

type EditToolOptions struct {
	Operations EditOperations
}

type editTool struct {
	cwd        string
	operations EditOperations
}

type localEditOperations struct{}

func (localEditOperations) ReadFile(_ context.Context, path string) ([]byte, error) {
	data, err := os.ReadFile(path)
	return data, asNodeFilesystemError("open", path, err)
}

func (localEditOperations) WriteFile(_ context.Context, path, content string) error {
	return asNodeFilesystemError("open", path, os.WriteFile(path, []byte(encodeNodeUTF8(content)), 0o666))
}

func (localEditOperations) Access(_ context.Context, path string) error {
	return syscall.Access(path, accessRead|accessWrite)
}

func NewEditTool(cwd string, options *EditToolOptions) agent.AgentTool {
	operations := EditOperations(localEditOperations{})
	if options != nil && options.Operations != nil {
		operations = options.Operations
	}
	return &editTool{cwd: cwd, operations: operations}
}

func (tool *editTool) Spec() agent.AgentToolSpec {
	return agent.AgentToolSpec{
		Name:        "edit",
		Label:       "edit",
		Description: "Edit a single file using exact text replacement. Every edits[].oldText must match a unique, non-overlapping region of the original file. If two changes affect the same block or nearby lines, merge them into one edit instead of emitting overlapping edits. Do not include large unchanged regions just to connect distant changes.",
		Parameters:  editSchema,
		PrepareArguments: func(input any) (any, error) {
			return prepareEditArguments(input), nil
		},
	}
}

func (tool *editTool) Execute(
	ctx context.Context,
	_ string,
	params any,
	_ agent.AgentToolUpdateCallback,
) (agent.AgentToolResult, error) {
	input, err := editInput(params)
	if err != nil {
		return agent.AgentToolResult{}, err
	}
	if len(input.Edits) == 0 {
		return agent.AgentToolResult{}, upstreamToolError("Edit tool input is invalid. edits must contain at least one replacement.")
	}
	absolutePath, err := ResolveToCwd(input.Path, tool.cwd)
	if err != nil {
		return agent.AgentToolResult{}, err
	}
	return withFileMutationQueueContext(ctx, absolutePath, func() (agent.AgentToolResult, error) {
		if err := checkAborted(ctx); err != nil {
			return agent.AgentToolResult{}, err
		}
		if err := tool.operations.Access(ctx, absolutePath); err != nil {
			if abortErr := checkAborted(ctx); abortErr != nil {
				return agent.AgentToolResult{}, abortErr
			}
			return agent.AgentToolResult{}, editAccessError(input.Path, err)
		}
		if err := checkAborted(ctx); err != nil {
			return agent.AgentToolResult{}, err
		}

		data, err := tool.operations.ReadFile(ctx, absolutePath)
		if err != nil {
			return agent.AgentToolResult{}, err
		}
		rawContent := decodeNodeUTF8(data)
		if err := checkAborted(ctx); err != nil {
			return agent.AgentToolResult{}, err
		}

		bom, content := StripBOM(rawContent)
		lineEnding := DetectLineEnding(content)
		applied, err := ApplyEditsToNormalizedContent(NormalizeToLF(content), input.Edits, input.Path)
		if err != nil {
			return agent.AgentToolResult{}, err
		}
		if err := checkAborted(ctx); err != nil {
			return agent.AgentToolResult{}, err
		}

		finalContent := bom + RestoreLineEndings(applied.NewContent, lineEnding)
		if err := tool.operations.WriteFile(ctx, absolutePath, finalContent); err != nil {
			return agent.AgentToolResult{}, err
		}
		if err := checkAborted(ctx); err != nil {
			return agent.AgentToolResult{}, err
		}

		diff := GenerateDiffString(applied.BaseContent, applied.NewContent, 4)
		patch, err := GenerateUnifiedPatch(input.Path, applied.BaseContent, applied.NewContent, 4)
		if err != nil {
			return agent.AgentToolResult{}, err
		}
		return agent.AgentToolResult{
			Content: ai.ToolResultContent{&ai.TextContent{Text: fmt.Sprintf(
				"Successfully replaced %d block(s) in %s.", len(input.Edits), input.Path,
			)}},
			Details: EditToolDetails{Diff: diff.Diff, Patch: patch, FirstChangedLine: diff.FirstChangedLine},
		}, nil
	})
}

func (tool *editTool) PrepareParallelExecution(ctx context.Context, params any) (context.Context, func(), error) {
	if ctx == nil {
		ctx = context.Background()
	}
	input, err := editInput(params)
	if err != nil {
		return ctx, nil, err
	}
	if len(input.Edits) == 0 {
		return ctx, nil, upstreamToolError("Edit tool input is invalid. edits must contain at least one replacement.")
	}
	absolutePath, err := ResolveToCwd(input.Path, tool.cwd)
	if err != nil {
		return ctx, nil, err
	}
	reservation, err := reserveFileMutation(absolutePath)
	if err != nil {
		return ctx, nil, err
	}
	return context.WithValue(ctx, mutationReservationContextKey{}, reservation), reservation.cancel, nil
}

func prepareEditArguments(input any) any {
	args, ok := input.(map[string]any)
	if !ok || args == nil {
		return input
	}
	if encoded, ok := args["edits"].(string); ok {
		if parsed, ok := parseStringifiedEdits(encoded); ok {
			args["edits"] = parsed
		}
	}
	oldText, hasOldText := args["oldText"].(string)
	newText, hasNewText := args["newText"].(string)
	if !hasOldText || !hasNewText {
		return args
	}

	edits := make([]any, 0)
	switch existing := args["edits"].(type) {
	case []any:
		edits = append(edits, existing...)
	case []Edit:
		for _, edit := range existing {
			edits = append(edits, map[string]any{"oldText": edit.OldText, "newText": edit.NewText})
		}
	}
	edits = append(edits, map[string]any{"oldText": oldText, "newText": newText})
	prepared := make(map[string]any, len(args)-1)
	for name, value := range args {
		if name != "oldText" && name != "newText" {
			prepared[name] = value
		}
	}
	prepared["edits"] = edits
	return prepared
}

func editInput(params any) (EditToolInput, error) {
	if input, ok := params.(EditToolInput); ok {
		return input, nil
	}
	if input, ok := params.(*EditToolInput); ok && input != nil {
		return *input, nil
	}
	object, err := toolParams(params)
	if err != nil {
		return EditToolInput{}, err
	}
	path, err := requiredString(object, "path")
	if err != nil {
		return EditToolInput{}, err
	}
	rawEdits, ok := object["edits"]
	if !ok {
		return EditToolInput{Path: path}, nil
	}
	edits, err := decodeEdits(rawEdits)
	if err != nil {
		return EditToolInput{}, fmt.Errorf("invalid tool arguments: edits must be an array")
	}
	return EditToolInput{Path: path, Edits: edits}, nil
}

func parseStringifiedEdits(encoded string) ([]any, bool) {
	var rawItems []json.RawMessage
	if err := json.Unmarshal([]byte(encoded), &rawItems); err != nil || rawItems == nil {
		return nil, false
	}
	items := make([]any, len(rawItems))
	for index, rawItem := range rawItems {
		if err := json.Unmarshal(rawItem, &items[index]); err != nil {
			return nil, false
		}
		var rawObject map[string]json.RawMessage
		if err := json.Unmarshal(rawItem, &rawObject); err != nil || rawObject == nil {
			continue
		}
		object, ok := items[index].(map[string]any)
		if !ok {
			continue
		}
		for _, name := range []string{"oldText", "newText"} {
			rawString, exists := rawObject[name]
			if !exists {
				continue
			}
			value, err := decodeJavaScriptJSONString(rawString)
			if err == nil {
				object[name] = value
			}
		}
	}
	return items, true
}

func decodeEdits(rawEdits any) ([]Edit, error) {
	switch values := rawEdits.(type) {
	case []Edit:
		return append([]Edit(nil), values...), nil
	case []any:
		edits := make([]Edit, len(values))
		for index, value := range values {
			if edit, ok := value.(Edit); ok {
				edits[index] = edit
				continue
			}
			object, err := toolParams(value)
			if err != nil {
				return nil, err
			}
			oldText, err := requiredString(object, "oldText")
			if err != nil {
				return nil, err
			}
			newText, err := requiredString(object, "newText")
			if err != nil {
				return nil, err
			}
			edits[index] = Edit{OldText: oldText, NewText: newText}
		}
		return edits, nil
	default:
		encoded, err := json.Marshal(rawEdits)
		if err != nil {
			return nil, err
		}
		var edits []Edit
		if err := json.Unmarshal(encoded, &edits); err != nil {
			return nil, err
		}
		return edits, nil
	}
}

func decodeJavaScriptJSONString(raw json.RawMessage) (string, error) {
	var validation string
	if err := json.Unmarshal(raw, &validation); err != nil {
		return "", err
	}
	encoded := bytes.TrimSpace(raw)
	if len(encoded) < 2 || encoded[0] != '"' || encoded[len(encoded)-1] != '"' {
		return "", errors.New("value is not a JSON string")
	}
	var output strings.Builder
	for index := 1; index < len(encoded)-1; {
		if encoded[index] != '\\' {
			output.WriteByte(encoded[index])
			index++
			continue
		}
		escape := encoded[index+1]
		index += 2
		switch escape {
		case '"', '\\', '/':
			output.WriteByte(escape)
		case 'b':
			output.WriteByte('\b')
		case 'f':
			output.WriteByte('\f')
		case 'n':
			output.WriteByte('\n')
		case 'r':
			output.WriteByte('\r')
		case 't':
			output.WriteByte('\t')
		case 'u':
			unit, err := parseJSONUTF16Unit(encoded[index : index+4])
			if err != nil {
				return "", err
			}
			index += 4
			if unit >= 0xd800 && unit <= 0xdbff && index+6 <= len(encoded)-1 && encoded[index] == '\\' && encoded[index+1] == 'u' {
				next, err := parseJSONUTF16Unit(encoded[index+2 : index+6])
				if err == nil && next >= 0xdc00 && next <= 0xdfff {
					output.WriteRune(utf16.DecodeRune(rune(unit), rune(next)))
					index += 6
					continue
				}
			}
			writeJavaScriptUTF16Unit(&output, unit)
		}
	}
	return output.String(), nil
}

func parseJSONUTF16Unit(encoded []byte) (uint16, error) {
	value, err := strconv.ParseUint(string(encoded), 16, 16)
	return uint16(value), err
}

func writeJavaScriptUTF16Unit(output *strings.Builder, unit uint16) {
	if unit >= 0xd800 && unit <= 0xdfff {
		output.Write([]byte{byte(0xe0 | unit>>12), byte(0x80 | unit>>6&0x3f), byte(0x80 | unit&0x3f)})
		return
	}
	output.WriteRune(rune(unit))
}

func editAccessError(path string, err error) error {
	if code := filesystemErrorCode(err); code != "" {
		return upstreamToolErrorf("Could not edit file: %s. Error code: %s.", path, code)
	}
	return upstreamToolErrorf("Could not edit file: %s. Error: %s.", path, err)
}

func filesystemErrorCode(err error) string {
	type codedError interface{ Code() string }
	var coded codedError
	if errors.As(err, &coded) && coded.Code() != "" {
		return coded.Code()
	}
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return "ENOENT"
	case errors.Is(err, syscall.EPERM):
		return "EPERM"
	case errors.Is(err, syscall.EACCES):
		return "EACCES"
	case errors.Is(err, fs.ErrPermission):
		return "EACCES"
	case errors.Is(err, syscall.ENOTDIR):
		return "ENOTDIR"
	case errors.Is(err, syscall.ELOOP):
		return "ELOOP"
	case errors.Is(err, syscall.EROFS):
		return "EROFS"
	case errors.Is(err, syscall.ENAMETOOLONG):
		return "ENAMETOOLONG"
	case errors.Is(err, syscall.EIO):
		return "EIO"
	case errors.Is(err, syscall.ENOMEM):
		return "ENOMEM"
	case errors.Is(err, syscall.ETXTBSY):
		return "ETXTBSY"
	case errors.Is(err, syscall.EINVAL):
		return "EINVAL"
	case errors.Is(err, syscall.ENOSPC):
		return "ENOSPC"
	case errors.Is(err, syscall.EISDIR):
		return "EISDIR"
	case errors.Is(err, syscall.EEXIST):
		return "EEXIST"
	}
	return ""
}

func ComputeEditsDiff(path string, edits []Edit, cwd string) (DiffResult, error) {
	absolutePath, err := ResolveToCwd(path, cwd)
	if err != nil {
		return DiffResult{}, err
	}
	if err := nodeNullPathError(absolutePath); err != nil {
		return DiffResult{}, editAccessError(path, err)
	}
	if err := syscall.Access(absolutePath, accessRead); err != nil {
		return DiffResult{}, editAccessError(path, err)
	}
	data, err := (localEditOperations{}).ReadFile(context.Background(), absolutePath)
	if err != nil {
		return DiffResult{}, err
	}
	_, content := StripBOM(decodeNodeUTF8(data))
	applied, err := ApplyEditsToNormalizedContent(NormalizeToLF(content), edits, path)
	if err != nil {
		return DiffResult{}, err
	}
	return GenerateDiffString(applied.BaseContent, applied.NewContent, 4), nil
}

func ComputeEditDiff(path, oldText, newText, cwd string) (DiffResult, error) {
	return ComputeEditsDiff(path, []Edit{{OldText: oldText, NewText: newText}}, cwd)
}
