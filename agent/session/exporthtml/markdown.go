package exporthtml

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/OrdalieTech/orb/agent/session"
	"github.com/OrdalieTech/orb/internal/lazyregexp"
)

// ExportSessionMarkdown writes the active session branch as portable Markdown.
func ExportSessionMarkdown(manager *session.SessionManager, outputPath string) (string, error) {
	if manager == nil {
		return "", errors.New("session manager is required")
	}
	sessionFile := manager.GetSessionFile()
	if !manager.IsPersisted() {
		return "", errors.New("Cannot export in-memory session to Markdown") //nolint:staticcheck // User-facing compatibility error.
	}
	if sessionFile != "" {
		if _, err := os.Stat(sessionFile); err != nil {
			return "", errors.New("Nothing to export yet - start a conversation first") //nolint:staticcheck // User-facing compatibility error.
		}
	}
	if outputPath == "" {
		base := manager.GetSessionID()
		if sessionFile != "" {
			base = strings.TrimSuffix(filepath.Base(sessionFile), ".jsonl")
		}
		outputPath = "orb-session-" + base + ".md"
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
		outputPath = "orb-session-" + base + ".md"
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
	// The message reads as typed, the invocation marked in place; the skill
	// body folds below it like thinking does.
	message := ReplaceSkillTokens(skill.InvocationText(), skill.Names(), func(name string) string { return "**◆ " + name + "**" })
	parts := append([]string{message}, images...)
	for _, each := range skill.Skills() {
		parts = append(parts, "<details><summary>◆ "+each.Name+" skill</summary>\n\n"+each.Content+"\n\n</details>")
	}
	return strings.Join(parts, "\n\n")
}

// ParsedSkillBlock is an upstream skill invocation embedded in a user message;
// More holds the skills the same message invoked after it, envelope after envelope.
type ParsedSkillBlock struct {
	Name        string
	Location    string
	Content     string
	UserMessage string
	More        []ParsedSkillBlock
}

var skillBlockPattern = lazyregexp.New(`(?s)^<skill name="([^"]+)" location="([^"]+)">\n(.*?)\n</skill>(?:\n\n(.+))?$`)

// ParseSkillBlock parses the exact upstream skill-message envelope.
func ParseSkillBlock(text string) (ParsedSkillBlock, bool) {
	match := skillBlockPattern().FindStringSubmatch(text)
	if match == nil {
		return ParsedSkillBlock{}, false
	}
	skill := ParsedSkillBlock{Name: match[1], Location: match[2], Content: match[3], UserMessage: strings.TrimSpace(match[4])}
	if next, ok := ParseSkillBlock(skill.UserMessage); ok {
		skill.UserMessage, skill.More = next.UserMessage, next.Skills()
	}
	return skill, true
}

// Skills is every skill the message invoked, in order.
func (skill ParsedSkillBlock) Skills() []ParsedSkillBlock {
	first := skill
	first.More = nil
	return append([]ParsedSkillBlock{first}, skill.More...)
}

// Names is the invoked skills' names, in order.
func (skill ParsedSkillBlock) Names() []string {
	names := []string{skill.Name}
	for _, more := range skill.More {
		names = append(names, more.Name)
	}
	return names
}

// SkillTokenPrefix starts a skill invocation in message text.
const SkillTokenPrefix = "/skill:"

// SkillToken is a whitespace-delimited `/skill:name` run; offsets are bytes.
type SkillToken struct {
	Start, End int
	Name       string
}

func isSkillTokenSpace(b byte) bool { return b == ' ' || b == '\t' || b == '\r' || b == '\n' }

// FindSkillTokens returns the skill invocations in text, in order.
func FindSkillTokens(text string) []SkillToken {
	var tokens []SkillToken
	for from := 0; from < len(text); {
		at := strings.Index(text[from:], SkillTokenPrefix)
		if at < 0 {
			break
		}
		at += from
		end := at + len(SkillTokenPrefix)
		for end < len(text) && !isSkillTokenSpace(text[end]) {
			end++
		}
		if (at == 0 || isSkillTokenSpace(text[at-1])) && end > at+len(SkillTokenPrefix) {
			tokens = append(tokens, SkillToken{Start: at, End: end, Name: text[at+len(SkillTokenPrefix) : end]})
		}
		from = end
	}
	return tokens
}

// SkillSubmission rewrites an interactive message so the kernel's
// start-of-message expansion sees an inline invocation. A message already
// starting with the token passes through for upstream's exact expansion;
// otherwise `/skill:name ` is prepended for each skill, in order, to the untouched
// text, so the envelopes carry the user's full message, tokens in place, after
// the skill blocks.
func SkillSubmission(text string, known func(string) bool) string {
	var names []string
	tokens := FindSkillTokens(text)
	for _, token := range tokens {
		if known(token.Name) && !slices.Contains(names, token.Name) {
			names = append(names, token.Name)
		}
	}
	if len(names) == 0 || len(names) == 1 && tokens[0].Start == 0 && tokens[0].Name == names[0] {
		return text
	}
	return SkillTokenPrefix + strings.Join(names, " "+SkillTokenPrefix) + " " + text
}

// ReplaceSkillTokens substitutes every invocation of the named skills.
func ReplaceSkillTokens(text string, names []string, chip func(string) string) string {
	var out strings.Builder
	position := 0
	for _, token := range FindSkillTokens(text) {
		if !slices.Contains(names, token.Name) {
			continue
		}
		out.WriteString(text[position:token.Start])
		out.WriteString(chip(token.Name))
		position = token.End
	}
	out.WriteString(text[position:])
	return out.String()
}

// InvocationText is the user's text with the invocations at their original
// position. Orb's inline form keeps the tokens in the text after the blocks;
// upstream's `/skill:name args` form moved them out, so they lead.
func (skill ParsedSkillBlock) InvocationText() string {
	text := skill.UserMessage
	names := skill.Names()
	for index := len(names) - 1; index >= 0; index-- {
		if !slices.ContainsFunc(FindSkillTokens(text), func(token SkillToken) bool { return token.Name == names[index] }) {
			text = strings.TrimSpace(SkillTokenPrefix + names[index] + " " + text)
		}
	}
	return text
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
