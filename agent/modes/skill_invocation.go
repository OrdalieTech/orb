package modes

import (
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
		// A stored preview is clipped, often before the block closes.
		if rest, open := strings.CutPrefix(text, `<skill name="`); open {
			if end := strings.IndexByte(rest, '"'); end > 0 {
				return "◆ " + rest[:end]
			}
		}
		return text
	}
	return exporthtml.ReplaceSkillTokens(skill.InvocationText(), skill.Names(), func(name string) string { return "◆ " + name })
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
// with the invocations chipped in place and a footer line naming each skill;
// the expand action (or a click on the footers) reveals the skill bodies.
func newSkillUserMessageComponent(skill agent.ParsedSkillBlock, describe func(string) string, mdTheme tui.MarkdownTheme, outputPad int, transformers []extensions.MarkdownTransformer) *UserMessageComponent {
	transform := newMarkdownTransform("user", false, transformers)
	chipped := func(markdown string, width int) string {
		if transform != nil {
			markdown = transform(markdown, width)
		}
		// The chip closes its own color; reopen the message color after it.
		return exporthtml.ReplaceSkillTokens(markdown, skill.Names(), func(name string) string {
			return skillChip(name) + theme.FGANSI("userMessageText")
		})
	}
	body := &skillMessageBody{message: newUserMarkdown(skill.InvocationText(), mdTheme, chipped)}
	for _, each := range skill.Skills() {
		body.skills = append(body.skills, invokedSkill{
			name:        each.Name,
			description: strings.Join(strings.Fields(describe(each.Name)), " "),
			content: tui.NewMarkdown(each.Content, 0, 0, mdTheme, &tui.DefaultTextStyle{
				Color: func(text string) string { return theme.FG("muted", text) },
			}, nil),
		})
	}
	component := newUserMessageBand(body, outputPad)
	component.skill = body
	return component
}

// skillMessageBody is the inside of a skill user band: message, a footer per
// skill, and the skill bodies when expanded.
type skillMessageBody struct {
	mu        sync.Mutex
	message   *tui.Markdown
	skills    []invokedSkill
	expanded  bool
	footerRow int
}

type invokedSkill struct {
	name, description string
	content           *tui.Markdown
}

func (skill invokedSkill) footer(width int) string {
	detail := skill.description
	if detail == "" {
		detail = "skill"
	}
	line := theme.FG("accent", "◆") + theme.FG("dim", " "+skill.name+" · "+detail)
	return tui.TruncateToWidth(line, width, theme.FG("dim", "…"), false)
}

func (body *skillMessageBody) Render(width int) []string {
	lines := append([]string(nil), body.message.Render(width)...)
	body.mu.Lock()
	defer body.mu.Unlock()
	body.footerRow = len(lines)
	for _, skill := range body.skills {
		lines = append(lines, skill.footer(width))
	}
	if body.expanded {
		for _, skill := range body.skills {
			lines = append(lines, "")
			lines = append(lines, skill.content.Render(width)...)
		}
	}
	return lines
}

func (body *skillMessageBody) Invalidate() {
	body.message.Invalidate()
	for _, skill := range body.skills {
		skill.content.Invalidate()
	}
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
