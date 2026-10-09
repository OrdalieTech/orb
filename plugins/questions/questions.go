// Package questions is Orb's shared human-question capability and native tool.
package questions

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/OrdalieTech/orb/agent/extensions"
	"github.com/OrdalieTech/orb/ai"
	"github.com/OrdalieTech/orb/engine"
	"github.com/OrdalieTech/orb/plugins/internal/toolutil"
)

const Kind = "questions"
const ToolName = "ask_user_question"

type Option struct {
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
	Preview     string `json:"preview,omitempty"`
}
type Question struct {
	ID          string   `json:"id"`
	Question    string   `json:"question"`
	Header      string   `json:"header,omitempty"`
	Options     []Option `json:"options,omitempty"`
	MultiSelect bool     `json:"multi_select,omitempty"`
}
type Request struct {
	Questions []Question `json:"questions"`
}
type Answer struct {
	ID       string   `json:"id"`
	Selected []string `json:"selected"`
	Custom   string   `json:"custom,omitempty"`
}
type Result struct {
	Answers   []Answer `json:"answers,omitempty"`
	Cancelled bool     `json:"cancelled,omitempty"`
}

var schema = ai.JSONSchema(`{"type":"object","required":["questions"],"additionalProperties":false,"properties":{"questions":{"type":"array","minItems":1,"maxItems":4,"items":{"type":"object","required":["id","question"],"additionalProperties":false,"properties":{"id":{"type":"string","description":"Stable question ID echoed in the answer."},"question":{"type":"string"},"header":{"type":"string"},"multi_select":{"type":"boolean"},"options":{"type":"array","maxItems":8,"items":{"type":"object","required":["label"],"additionalProperties":false,"properties":{"label":{"type":"string"},"description":{"type":"string"},"preview":{"type":"string"}}}}}}}}}`)

func (r Request) Validate() error {
	if len(r.Questions) < 1 || len(r.Questions) > 4 {
		return errors.New("ask_user_question requires one to four questions")
	}
	seen := map[string]bool{}
	for _, q := range r.Questions {
		if strings.TrimSpace(q.ID) == "" || len(q.ID) > 64 || seen[q.ID] || strings.TrimSpace(q.Question) == "" || len(q.Question) > 4096 || len(q.Header) > 128 || len(q.Options) > 8 {
			return errors.New("invalid question ID, text or options")
		}
		seen[q.ID] = true
		labels := map[string]bool{}
		for _, o := range q.Options {
			if strings.TrimSpace(o.Label) == "" || len(o.Label) > 256 || labels[o.Label] || len(o.Description) > 4096 || len(o.Preview) > 8192 {
				return errors.New("invalid or duplicate question option")
			}
			labels[o.Label] = true
		}
	}
	data, _ := json.Marshal(r)
	if len(data) > 64<<10 {
		return errors.New("questions exceed 64 KiB")
	}
	return nil
}

func (r Request) ValidateReply(raw string) error {
	var result Result
	if len(raw) > 64<<10 || json.Unmarshal([]byte(raw), &result) != nil {
		return errors.New("invalid question reply")
	}
	if result.Cancelled {
		if len(result.Answers) != 0 {
			return errors.New("dismissed questions cannot contain answers")
		}
		return nil
	}
	if len(result.Answers) != len(r.Questions) {
		return errors.New("answer every question before submitting")
	}
	for i, q := range r.Questions {
		a := result.Answers[i]
		if a.Selected == nil || a.ID != q.ID || len(a.Custom) > 16384 || (!q.MultiSelect && len(a.Selected) > 1) || (!q.MultiSelect && a.Custom != "" && len(a.Selected) > 0) {
			return errors.New("answer does not match its question")
		}
		if len(a.Selected) == 0 && strings.TrimSpace(a.Custom) == "" {
			return errors.New("choose an option or write an answer")
		}
		seen := map[string]bool{}
		for _, label := range a.Selected {
			if seen[label] || !slices.ContainsFunc(q.Options, func(o Option) bool { return o.Label == label }) {
				return errors.New("invalid selected answer")
			}
			seen[label] = true
		}
	}
	return nil
}

// Panel draws questions in a terminal and calls done with their answers.
type Panel func(request Request, theme extensions.Theme, height func() int, invalidate func(), done func(Result)) extensions.Component

// Draw is how a terminal draws questions: the panel it asks them in, and their lines in the
// transcript. A host without a terminal leaves it zero: its controller (an app, a Bridge peer)
// answers them from the presentation.
type Draw struct {
	Panel Panel
	Text  func(text string, oneLine bool) extensions.Component
}

// Ask uses the runtime's execution-bound input seam; it owns no queue or history. A terminal asks
// in panel, when one is given.
func Ask(ctx context.Context, request Request, input extensions.InputHandler, panel Panel) (Result, error) {
	if err := request.Validate(); err != nil {
		return Result{}, err
	}
	if input == nil {
		return Result{}, errors.New("questions require an interactive session or an attached controller")
	}
	data, _ := json.Marshal(request)
	options := extensions.InputOptions{Presentation: &extensions.InputPresentation{Kind: Kind, Data: data}, Validate: request.ValidateReply}
	if panel != nil {
		options.Render = func(ctx context.Context, ui extensions.UI) (string, error) {
			value, ok, err := ui.Custom(ctx, func(host extensions.UIHost, theme extensions.Theme, _ extensions.Keybindings, done extensions.CustomDone) (extensions.Component, error) {
				return panel(request, theme, host.Height, host.Invalidate, func(result Result) { done(result) }), nil
			}, nil)
			if err != nil {
				return "", err
			}
			result, valid := value.(Result)
			if !ok || !valid {
				result = Result{Cancelled: true}
			}
			raw, err := json.Marshal(result)
			return string(raw), err
		}
	}
	title := request.Questions[0].Question
	raw, err := input(extensions.WithInputOptions(ctx, options), title, nil)
	if err != nil {
		return Result{}, err
	}
	if err = request.ValidateReply(raw); err != nil {
		return Result{}, err
	}
	var result Result
	err = json.Unmarshal([]byte(raw), &result)
	return result, err
}

func (request Request) Summary() string {
	lines := []string{"Question"}
	for _, q := range request.Questions {
		lines = append(lines, q.Question)
		for _, option := range q.Options {
			line := "  • " + option.Label
			if option.Description != "" {
				line += " — " + option.Description
			}
			lines = append(lines, line)
		}
	}
	return strings.Join(lines, "\n")
}

func Extension(draw Draw) extensions.Factory {
	return func(api extensions.API) error {
		tool := extensions.ToolDefinition{Name: ToolName, Label: "Question", Description: "Ask the user for missing information or a choice. Offer concise options with descriptions, or omit options for free text. The user can always write their own answer. Do not use this tool to bypass action permissions.", Parameters: schema, ExecutionMode: engine.ToolExecutionSequential,
			Execute: func(ctx context.Context, _ string, args any, _ engine.AgentToolUpdateCallback, _ extensions.Context) (engine.AgentToolResult, error) {
				var request Request
				if err := toolutil.Decode(args, &request); err != nil {
					return engine.AgentToolResult{}, err
				}
				result, err := Ask(ctx, request, extensions.InputHandlerFromContext(ctx), draw.Panel)
				if err != nil {
					return engine.AgentToolResult{}, err
				}
				raw, err := json.Marshal(result)
				return engine.AgentToolResult{Content: ai.ToolResultContent{&ai.TextContent{Text: string(raw)}}, Details: result}, err
			},
		}
		if draw.Text != nil {
			tool.RenderCall = func(args any, theme extensions.Theme, _ extensions.ToolRenderContext) extensions.Component {
				var request Request
				_ = toolutil.Decode(args, &request)
				return draw.Text(theme.FG("toolTitle", request.Summary()), false)
			}
			tool.RenderResult = func(result engine.AgentToolResult, _ extensions.ToolRenderResultOptions, _ extensions.Theme, _ extensions.ToolRenderContext) extensions.Component {
				var answers Result
				if toolutil.Decode(result.Details, &answers) != nil {
					return draw.Text(ai.ContentText(result.Content), false)
				}
				if answers.Cancelled {
					return draw.Text("Question dismissed — no answer supplied", false)
				}
				var lines []string
				for _, a := range answers.Answers {
					parts := append([]string{}, a.Selected...)
					if a.Custom != "" {
						parts = append(parts, a.Custom)
					}
					lines = append(lines, fmt.Sprintf("%s: %s", a.ID, strings.Join(parts, ", ")))
				}
				return draw.Text(strings.Join(lines, "\n"), false)
			}
		}
		api.RegisterTool(tool)
		return nil
	}
}
