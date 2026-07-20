package exporthtml

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/OrdalieTech/pi-go/codingagent/session"
)

// ExportSessionMarkdown writes the active session branch as portable Markdown.
func ExportSessionMarkdown(manager *session.SessionManager, outputPath string) (string, error) {
	if manager == nil {
		return "", errors.New("session manager is required")
	}
	sessionFile := manager.GetSessionFile()
	if sessionFile == "" {
		return "", errors.New("Cannot export in-memory session to Markdown") //nolint:staticcheck // User-facing compatibility error.
	}
	if _, err := os.Stat(sessionFile); err != nil {
		return "", errors.New("Nothing to export yet - start a conversation first") //nolint:staticcheck // User-facing compatibility error.
	}
	if outputPath == "" {
		base := strings.TrimSuffix(filepath.Base(sessionFile), ".jsonl")
		outputPath = "pi-session-" + base + ".md"
	}
	outputPath, err := normalizePath(outputPath)
	if err != nil {
		return "", err
	}
	contents, err := renderMarkdown(manager)
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(outputPath, []byte(contents), 0o666); err != nil {
		return "", err
	}
	return outputPath, nil
}

func ExportMarkdownFromFile(inputPath, outputPath string) (string, error) {
	resolvedInput, err := resolvePath(inputPath)
	if err != nil {
		return "", err
	}
	if _, err := os.Stat(resolvedInput); err != nil {
		return "", fmt.Errorf("File not found: %s", resolvedInput) //nolint:staticcheck // User-facing compatibility error.
	}
	manager, err := session.Open(resolvedInput, "")
	if err != nil {
		return "", err
	}
	if outputPath == "" {
		base := strings.TrimSuffix(filepath.Base(resolvedInput), ".jsonl")
		outputPath = "pi-session-" + base + ".md"
	}
	return ExportSessionMarkdown(manager, outputPath)
}

func renderMarkdown(manager *session.SessionManager) (string, error) {
	var output strings.Builder
	title := "Session " + manager.GetSessionID()
	if name := manager.GetSessionName(); name != nil {
		title = *name
	}
	output.WriteString("# " + title + "\n\n")
	if header := manager.GetHeader(); header != nil {
		output.WriteString("- Session ID: `" + header.ID + "`\n")
		output.WriteString("- Working directory: `" + header.CWD + "`\n")
		output.WriteString("- Created: " + header.Timestamp + "\n\n")
	}
	for _, entry := range manager.GetBranch() {
		switch entry.Type {
		case "message":
			if err := renderMessageMarkdown(&output, entry.Message); err != nil {
				return "", err
			}
		case "custom_message":
			if !entry.Display {
				continue
			}
			output.WriteString("## " + entry.CustomType + "\n\n")
			output.WriteString(renderContentMarkdown(entry.Content))
			output.WriteString("\n\n")
		case "model_change":
			output.WriteString("## Model change\n\nSwitched to model: ")
			output.WriteString(inlineCode(entry.Provider + "/" + entry.ModelID))
			output.WriteString("\n\n")
		case "compaction":
			output.WriteString("## Compaction summary\n\n")
			output.WriteString(blockquote(entry.Summary) + "\n\n")
		case "branch_summary":
			output.WriteString("## Branch summary\n\n")
			output.WriteString(blockquote(entry.Summary) + "\n\n")
		}
	}
	return output.String(), nil
}

func renderMessageMarkdown(output *strings.Builder, raw json.RawMessage) error {
	var message struct {
		Role       string          `json:"role"`
		Content    json.RawMessage `json:"content"`
		Command    string          `json:"command"`
		Output     string          `json:"output"`
		ToolName   string          `json:"toolName"`
		IsError    bool            `json:"isError"`
		CustomType string          `json:"customType"`
	}
	if err := json.Unmarshal(raw, &message); err != nil {
		return err
	}
	switch message.Role {
	case "user":
		output.WriteString("## User\n\n")
		output.WriteString(renderUserContentMarkdown(message.Content))
	case "assistant":
		output.WriteString("## Assistant\n\n")
		output.WriteString(renderAssistantContentMarkdown(message.Content))
	case "toolResult":
		title := "## Tool result"
		if message.ToolName != "" {
			title += ": " + message.ToolName
		}
		if message.IsError {
			title += " (error)"
		}
		output.WriteString(title + "\n\n")
		output.WriteString(renderContentMarkdown(message.Content))
	case "bashExecution":
		output.WriteString("## Bash\n\n" + fencedBlock("sh", message.Command) + "\n\n" + fencedBlock("text", message.Output))
	case "custom":
		output.WriteString("## " + message.CustomType + "\n\n")
		output.WriteString(renderContentMarkdown(message.Content))
	default:
		return nil
	}
	output.WriteString("\n\n")
	return nil
}

func renderContentMarkdown(raw json.RawMessage) string {
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return text
	}
	var blocks []struct {
		Type     string `json:"type"`
		Text     string `json:"text"`
		Data     string `json:"data"`
		MimeType string `json:"mimeType"`
	}
	if json.Unmarshal(raw, &blocks) != nil {
		var formatted bytes.Buffer
		if json.Indent(&formatted, raw, "", "  ") == nil {
			return fencedBlock("json", formatted.String())
		}
		return ""
	}
	parts := make([]string, 0, len(blocks))
	for _, block := range blocks {
		switch block.Type {
		case "text":
			parts = append(parts, block.Text)
		case "image":
			parts = append(parts, fmt.Sprintf("![image](data:%s;base64,%s)", block.MimeType, block.Data))
		}
	}
	return strings.Join(parts, "\n\n")
}

func renderUserContentMarkdown(raw json.RawMessage) string {
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return renderUserTextMarkdown(text, nil)
	}
	var blocks []struct {
		Type     string `json:"type"`
		Text     string `json:"text"`
		Data     string `json:"data"`
		MimeType string `json:"mimeType"`
	}
	if json.Unmarshal(raw, &blocks) != nil {
		return renderContentMarkdown(raw)
	}
	var textParts, images []string
	for _, block := range blocks {
		switch block.Type {
		case "text":
			textParts = append(textParts, block.Text)
		case "image":
			images = append(images, fmt.Sprintf("![image](data:%s;base64,%s)", block.MimeType, block.Data))
		}
	}
	return renderUserTextMarkdown(strings.Join(textParts, "\n"), images)
}

func renderUserTextMarkdown(text string, images []string) string {
	skill, ok := ParseSkillBlock(text)
	if !ok {
		parts := make([]string, 0, len(images)+1)
		if text != "" {
			parts = append(parts, text)
		}
		parts = append(parts, images...)
		return strings.Join(parts, "\n\n")
	}
	parts := []string{"**Skill: " + inlineCode(skill.Name) + "**", skill.Content}
	parts = append(parts, images...)
	if skill.UserMessage != "" {
		parts = append(parts, skill.UserMessage)
	}
	return strings.Join(parts, "\n\n")
}

// ParsedSkillBlock is an upstream skill invocation embedded in a user message.
type ParsedSkillBlock struct {
	Name        string
	Location    string
	Content     string
	UserMessage string
}

var skillBlockPattern = regexp.MustCompile(`(?s)^<skill name="([^"]+)" location="([^"]+)">\n(.*?)\n</skill>(?:\n\n(.+))?$`)

// ParseSkillBlock parses the exact upstream skill-message envelope.
func ParseSkillBlock(text string) (ParsedSkillBlock, bool) {
	match := skillBlockPattern.FindStringSubmatch(text)
	if match == nil {
		return ParsedSkillBlock{}, false
	}
	return ParsedSkillBlock{Name: match[1], Location: match[2], Content: match[3], UserMessage: strings.TrimSpace(match[4])}, true
}

func renderAssistantContentMarkdown(raw json.RawMessage) string {
	var blocks []struct {
		Type      string          `json:"type"`
		Text      string          `json:"text"`
		Thinking  string          `json:"thinking"`
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if json.Unmarshal(raw, &blocks) != nil {
		return renderContentMarkdown(raw)
	}
	parts := make([]string, 0, len(blocks))
	for _, block := range blocks {
		switch block.Type {
		case "text":
			parts = append(parts, block.Text)
		case "thinking":
			parts = append(parts, "<details><summary>Thinking</summary>\n\n"+block.Thinking+"\n\n</details>")
		case "toolCall":
			var formatted bytes.Buffer
			if json.Indent(&formatted, block.Arguments, "", "  ") != nil {
				formatted.Write(block.Arguments)
			}
			parts = append(parts, "**Tool call: "+block.Name+"**\n\n"+fencedBlock("json", formatted.String()))
		}
	}
	return strings.Join(parts, "\n\n")
}

func fencedBlock(info, content string) string {
	longest := 0
	for _, run := range strings.FieldsFunc(content, func(character rune) bool { return character != '`' }) {
		if len(run) > longest {
			longest = len(run)
		}
	}
	if longest < 3 {
		longest = 3
	} else {
		longest++
	}
	fence := strings.Repeat("`", longest)
	ending := "\n"
	if strings.HasSuffix(content, "\n") {
		ending = ""
	}
	return fence + info + "\n" + content + ending + fence
}

func inlineCode(value string) string {
	longest := 0
	for _, run := range strings.FieldsFunc(value, func(character rune) bool { return character != '`' }) {
		if len(run) > longest {
			longest = len(run)
		}
	}
	fence := strings.Repeat("`", longest+1)
	padding := ""
	if strings.HasPrefix(value, "`") || strings.HasSuffix(value, "`") || strings.HasPrefix(value, " ") || strings.HasSuffix(value, " ") {
		padding = " "
	}
	return fence + padding + value + padding + fence
}

func blockquote(value string) string {
	lines := strings.Split(value, "\n")
	for index := range lines {
		lines[index] = "> " + lines[index]
	}
	return strings.Join(lines, "\n")
}
