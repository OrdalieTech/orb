// Package titles names a session after its first exchange: a few words the session's own model
// picks, set once, never over a name the owner gave. Until then a session reads as its first
// message, which is rarely a good name.
package titles

import (
	"cmp"
	"context"
	"maps"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/OrdalieTech/orb/agent/extensions"
	"github.com/OrdalieTech/orb/ai"
)

const prompt = "Name this conversation for a list of conversations: at most six words, in the language " +
	"it is written in, no quotes, no final period. Reply with the title only."

// Extension names each unnamed session once, when its first agent run ends.
func Extension() extensions.Factory {
	return func(api extensions.API) error {
		var tried sync.Map // session IDs already named or attempted
		api.On(extensions.EventAgentEnd, func(_ context.Context, raw extensions.Event, ec extensions.Context) (any, error) {
			event, ok := raw.(extensions.AgentEndEvent)
			sessions, model := ec.SessionManager(), ec.Model()
			if !ok || sessions == nil || model == nil || sessions.GetSessionName() != nil {
				return nil, nil
			}
			id := sessions.GetSessionID()
			if _, done := tried.LoadOrStore(id, true); done {
				return nil, nil
			}
			exchange := firstExchange(event.Messages)
			if exchange == "" {
				return nil, nil
			}
			// Off the turn's path: the owner sees the answer at once and the title a moment later.
			go func() {
				ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
				defer cancel()
				title := ask(ctx, ec.ModelRegistry(), model, exchange)
				// The owner may have named it, or moved to another session, in the meantime.
				if title != "" && sessions.GetSessionID() == id && sessions.GetSessionName() == nil {
					_ = api.SetSessionName(ctx, title)
				}
			}()
			return nil, nil
		})
		return nil
	}
}

// firstExchange is the first request and the first answer to it, trimmed to what names it.
func firstExchange(messages []any) string {
	var request, answer string
	for _, message := range messages {
		switch m := message.(type) {
		case *ai.UserMessage:
			if request == "" {
				request = userText(m.Content)
			}
		case *ai.AssistantMessage:
			if request != "" && answer == "" {
				answer = ai.ContentText(m.Content)
			}
		}
	}
	if strings.TrimSpace(request) == "" {
		return ""
	}
	return "Request:\n" + clip(request, 2000) + "\n\nAnswer:\n" + clip(answer, 1500)
}

// ask names the exchange with the session's model or, when that model only runs whole
// conversations (Claude through Claude Code), with the cheapest other model signed in.
func ask(ctx context.Context, registry extensions.ModelRegistry, model *ai.Model, exchange string) string {
	if registry == nil {
		return ""
	}
	if title, ok := askWith(ctx, registry, model, exchange); ok {
		return title
	}
	others := slices.DeleteFunc(registry.Available(nil), func(m ai.Model) bool { return m.Provider == model.Provider })
	slices.SortStableFunc(others, func(a, b ai.Model) int { return cmp.Compare(a.Cost.Input+a.Cost.Output, b.Cost.Input+b.Cost.Output) })
	for _, other := range others[:min(len(others), 3)] {
		if title, ok := askWith(ctx, registry, &other, exchange); ok {
			return title
		}
	}
	return ""
}

// askWith reports false when the model could not be asked at all, so another can be.
func askWith(ctx context.Context, registry extensions.ModelRegistry, model *ai.Model, exchange string) (string, bool) {
	request := *model
	options := &ai.SimpleStreamOptions{}
	if resolved, err := registry.ResolveProviderAuth(ctx, string(model.Provider), nil); err == nil && resolved != nil {
		options.APIKey, options.Headers, options.Env = resolved.Auth.APIKey, ai.ProviderHeaders(resolved.Auth.Headers), ai.ProviderEnv(resolved.Env)
		if resolved.Auth.BaseURL != nil {
			request.BaseURL = *resolved.Auth.BaseURL
		}
	}
	if headers, err := registry.ResolveModelHeaders(ctx, request, map[string]string(options.Env), options.APIKey); err == nil && headers != nil {
		merged := map[string]string{}
		if request.Headers != nil {
			maps.Copy(merged, *request.Headers)
		}
		maps.Copy(merged, *headers)
		request.Headers = &merged
	}
	if model.Reasoning {
		low := ai.ThinkingLow
		options.Reasoning = &low
	}
	system := prompt
	stream, err := registry.StreamSimple(ctx, &request, ai.Context{SystemPrompt: &system, Messages: ai.MessageList{&ai.UserMessage{Content: ai.NewUserText(exchange), Timestamp: time.Now().UnixMilli()}}}, options)
	if err != nil {
		return "", false
	}
	reply, err := ai.Collect(stream)
	if err != nil || reply == nil || reply.StopReason == ai.StopReasonError || reply.StopReason == ai.StopReasonAborted {
		return "", false
	}
	title := tidy(ai.ContentText(reply.Content))
	return title, title != ""
}

func userText(content ai.UserContent) string {
	if content.Text != nil {
		return *content.Text
	}
	return ai.ContentText(content.Blocks)
}

// tidy keeps the first line, without the label, emphasis, quotes or period models add anyway.
func tidy(text string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(text), "\n")
	const marks = "\"'`*“”«». "
	line = strings.Trim(strings.ReplaceAll(line, "**", ""), marks)
	if len(line) > 6 && strings.EqualFold(line[:6], "title:") {
		line = line[6:]
	}
	return clip(strings.Trim(line, marks), 80)
}

func clip(text string, limit int) string {
	runes := []rune(strings.TrimSpace(text))
	if len(runes) <= limit {
		return string(runes)
	}
	return string(runes[:limit]) + "…"
}
