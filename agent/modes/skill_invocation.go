package modes

import (
	"fmt"
	"slices"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/OrdalieTech/orb/agent"
	"github.com/OrdalieTech/orb/agent/extensions"
	"github.com/OrdalieTech/orb/agent/modes/theme"
	"github.com/OrdalieTech/orb/agent/session/exporthtml"
	"github.com/OrdalieTech/orb/tui"
)

// Skill invocations stay canonical `/skill:name` text on every kernel surface
// (editor text, history, session JSONL, RPC). Interactive mode only draws them
// as chips, and lets the token sit anywhere in the message.

// skillSubmission rewrites an interactive message so the kernel's
// start-of-message expansion sees an inline invocation. A message already
// starting with the token passes through for upstream's exact expansion;
// otherwise `/skill:name ` is prepended to the untouched text, so the envelope
// carries the user's full message, token in place, after the skill block.
// Several distinct skills are refused: the envelope holds one.
func skillSubmission(text string, known func(string) bool) (string, error) {
	var names []string
	tokens := exporthtml.FindSkillTokens(text)
	for _, token := range tokens {
		if known(token.Name) && !slices.Contains(names, token.Name) {
			names = append(names, token.Name)
		}
	}
	switch len(names) {
	case 0:
		return text, nil
	case 1:
	default:
		chips := make([]string, len(names))
		for index, name := range names {
			chips[index] = "◆ " + name
		}
		return "", fmt.Errorf("one skill per message: %s; send them separately", strings.Join(chips, ", "))
	}
	if tokens[0].Start == 0 && tokens[0].Name == names[0] {
		return text, nil
	}
	return exporthtml.SkillTokenPrefix + names[0] + " " + text, nil
}

// skillChip draws an invocation; the no-break space keeps glyph and name on
// one wrapped line.
func skillChip(name string) string {
	return theme.FG("accent", theme.Bold("◆\u00a0"+name))
}

// skillPreview is the one-line form of a user message for selectors and the
// queue: a skill envelope reads as the message with its chip, never raw XML.
func skillPreview(text string) string {
	skill, ok := agent.ParseSkillBlock(text)
	if !ok {
		return text
	}
	return exporthtml.ReplaceSkillTokens(skill.InvocationText(), skill.Name, func(name string) string { return "◆ " + name })
}

// skillDisplayTokens chips every known skill token in an editor line.
func skillDisplayTokens(known func(string) bool) func(string) []tui.DisplayToken {
	return func(line string) []tui.DisplayToken {
		if !strings.Contains(line, exporthtml.SkillTokenPrefix) {
			return nil
		}
		var tokens []tui.DisplayToken
		for _, token := range exporthtml.FindSkillTokens(line) {
			if !known(token.Name) {
				continue
			}
			start := utf8.RuneCountInString(line[:token.Start])
			tokens = append(tokens, tui.DisplayToken{
				Start: start, End: start + utf8.RuneCountInString(line[token.Start:token.End]),
				Display: skillChip(token.Name),
			})
		}
		return tokens
	}
}

// newSkillUserMessageComponent renders a skill envelope as the user's message
// with the invocation chipped in place and one footer line naming the skill;
// the expand action (or a click on the footer) reveals the skill body.
func newSkillUserMessageComponent(skill agent.ParsedSkillBlock, description string, mdTheme tui.MarkdownTheme, outputPad int, transformers []extensions.MarkdownTransformer) *UserMessageComponent {
	transform := newMarkdownTransform("user", false, transformers)
	chipped := func(markdown string, width int) string {
		if transform != nil {
			markdown = transform(markdown, width)
		}
		// The chip closes its own color; reopen the message color after it.
		return exporthtml.ReplaceSkillTokens(markdown, skill.Name, func(name string) string {
			return skillChip(name) + theme.FGANSI("userMessageText")
		})
	}
	body := &skillMessageBody{
		name:        skill.Name,
		description: strings.Join(strings.Fields(description), " "),
		message:     newUserMarkdown(skill.InvocationText(), mdTheme, chipped),
		content: tui.NewMarkdown(skill.Content, 0, 0, mdTheme, &tui.DefaultTextStyle{
			Color: func(text string) string { return theme.FG("muted", text) },
		}, nil),
	}
	component := newUserMessageBand(body, outputPad)
	component.skill = body
	return component
}

// skillMessageBody is the inside of a skill user band: message, footer, and
// the skill body when expanded.
type skillMessageBody struct {
	mu          sync.Mutex
	name        string
	description string
	message     *tui.Markdown
	content     *tui.Markdown
	expanded    bool
	footerRow   int
}

func (body *skillMessageBody) footer(width int) string {
	detail := body.description
	if detail == "" {
		detail = "skill"
	}
	line := theme.FG("accent", "◆") + theme.FG("dim", " "+body.name+" · "+detail)
	return tui.TruncateToWidth(line, width, theme.FG("dim", "…"), false)
}

func (body *skillMessageBody) Render(width int) []string {
	lines := append([]string(nil), body.message.Render(width)...)
	body.mu.Lock()
	defer body.mu.Unlock()
	body.footerRow = len(lines)
	lines = append(lines, body.footer(width))
	if body.expanded {
		lines = append(lines, "")
		lines = append(lines, body.content.Render(width)...)
	}
	return lines
}

func (body *skillMessageBody) Invalidate() {
	body.message.Invalidate()
	body.content.Invalidate()
}

func (body *skillMessageBody) setExpanded(expanded bool) {
	body.mu.Lock()
	body.expanded = expanded
	body.mu.Unlock()
}

// toggleAt flips the body when row (body-relative) is the footer or below.
func (body *skillMessageBody) toggleAt(row int) bool {
	body.mu.Lock()
	defer body.mu.Unlock()
	if row < body.footerRow {
		return false
	}
	body.expanded = !body.expanded
	return true
}

func (mode *InteractiveMode) skillDescription(name string) string {
	if mode.session == nil {
		return ""
	}
	if loader := mode.session.ResourceLoader(); loader != nil {
		for _, skill := range loader.GetSkills().Skills {
			if skill.Name == name {
				return skill.Description
			}
		}
		return ""
	}
	for _, command := range mode.session.Commands() {
		if command.Source == agent.SlashCommandSkill && command.Name == "skill:"+name {
			return command.Description
		}
	}
	return ""
}

// runeBoundary backs a byte cut off to the start of the rune it would split,
// so byte-budget previews never tear the chip glyph.
func runeBoundary(text string, cut int) int {
	for cut > 0 && cut < len(text) && !utf8.RuneStart(text[cut]) {
		cut--
	}
	return cut
}
