package extension

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/OrdalieTech/orb/agent/extensions"
	"github.com/OrdalieTech/orb/ai"
	"github.com/OrdalieTech/orb/engine"
	memorysdk "github.com/OrdalieTech/orb/plugins/memory"
	agentmemory "github.com/OrdalieTech/orb/plugins/memory/agent"
)

// changesTimeout bounds the cross-session memory delta read during BeforeAgentStart
// so a temporarily locked or checkpointing SQLite store cannot stall a user prompt.
var changesTimeout = 2 * time.Second

// Options configures the bundled memory extension.
type Options struct {
	ViewBytes      int
	CompactorModel string
	Peer           string
	StreamFn       engine.StreamFn
	Summarizer     memorysdk.Summarizer
	OnWrite        func()
}

// Extension attaches OptChat timeline memory to an agent using an explicit,
// instance-owned store. opts is variadic so 1-arg callers Extension(store)
// continue to work with sensible defaults.
func Extension(store memorysdk.Store, opts ...Options) extensions.Factory {
	var cfg Options
	if len(opts) > 0 {
		cfg = opts[0]
	}

	return func(api extensions.API) error {
		var rtOpts []agentmemory.Option
		if cfg.ViewBytes > 0 {
			rtOpts = append(rtOpts, agentmemory.WithViewBytes(cfg.ViewBytes))
		}
		if cfg.Peer != "" {
			rtOpts = append(rtOpts, agentmemory.WithPeer(cfg.Peer))
		}
		if cfg.Summarizer != nil {
			rtOpts = append(rtOpts, agentmemory.WithSummarizer(cfg.Summarizer))
		}
		if cfg.OnWrite != nil {
			rtOpts = append(rtOpts, agentmemory.WithOnWrite(cfg.OnWrite))
		}

		runtime, err := agentmemory.New(store, rtOpts...)
		if err != nil {
			return err
		}

		// Register the single unified `memory` tool.
		for _, tool := range runtime.Tools() {
			spec := tool.Spec()
			api.RegisterTool(extensions.ToolDefinition{
				Name: spec.Name, Label: spec.Label, Description: spec.Description,
				Parameters: spec.Parameters, ConstrainedSampling: spec.ConstrainedSampling,
				PrepareArguments: spec.PrepareArguments, ExecutionMode: spec.ExecutionMode,
				Execute: func(ctx context.Context, callID string, raw any, onUpdate engine.AgentToolUpdateCallback, _ extensions.Context) (engine.AgentToolResult, error) {
					return tool.Execute(ctx, callID, raw, onUpdate)
				},
			})
		}

		// Register the `/memory` slash command.
		api.RegisterCommand("memory", extensions.Command{
			Name:        "memory",
			Description: "Inspect or compact OptChat memory (status, view, compact, search, zoom)",
			Handler: func(ctx context.Context, args string, cmdCtx extensions.CommandContext) error {
				out, err := handleMemoryCommand(ctx, store, runtime, args)
				if err != nil {
					notifyCommand(cmdCtx, "memory: "+err.Error(), extensions.NotifyError)
					return err
				}
				notifyCommand(cmdCtx, out, extensions.NotifyInfo)
				return nil
			},
		})

		api.On(extensions.EventSessionStart, func(ctx context.Context, _ extensions.Event, extCtx extensions.Context) (any, error) {
			if extCtx != nil && extCtx.SessionManager() != nil {
				runtime.SetSession(extCtx.SessionManager().GetSessionID())
			}
			if cfg.Summarizer == nil && cfg.StreamFn != nil && strings.TrimSpace(cfg.CompactorModel) != "" && extCtx != nil {
				if sum := buildStreamSummarizer(cfg.StreamFn, extCtx.ModelRegistry(), cfg.CompactorModel); sum != nil {
					runtime.SetSummarizer(sum)
				}
			}
			runtime.StartCompactor(context.Background())
			return nil, runtime.Load(ctx)
		})

		// A compaction rewrites the context anyway: reload the memory view fresh.
		api.On(extensions.EventSessionCompact, func(ctx context.Context, _ extensions.Event, _ extensions.Context) (any, error) {
			return nil, runtime.Load(ctx)
		})

		// Keep the frozen system prompt prefix cache-stable; inject cross-session
		// deltas as a custom message with a bounded fail-open timeout.
		api.On(extensions.EventBeforeAgentStart, func(ctx context.Context, raw extensions.Event, _ extensions.Context) (any, error) {
			event := raw.(extensions.BeforeAgentStartEvent)
			var result extensions.BeforeAgentStartResult
			if prompt := runtime.SystemPrompt(event.SystemPrompt); prompt != event.SystemPrompt {
				result.SystemPrompt = &prompt
			}
			changesCtx, cancel := context.WithTimeout(ctx, changesTimeout)
			changes, err := runtime.Changes(changesCtx)
			cancel()
			if err != nil {
				if ctx.Err() != nil {
					return nil, ctx.Err()
				}
				if !errors.Is(err, context.DeadlineExceeded) && !errors.Is(changesCtx.Err(), context.DeadlineExceeded) {
					return nil, err
				}
				// Fail open on timeout (e.g. SQLite temporarily locked/checkpointing).
				changes = ""
			}
			if changes != "" {
				result.Message = &extensions.CustomMessage{CustomType: "orb.memory", Content: changes}
			}
			if result.SystemPrompt == nil && result.Message == nil {
				return nil, nil
			}
			return result, nil
		})

		// Record the completed turn asynchronously without blocking the agent loop.
		api.On(extensions.EventAgentEnd, func(_ context.Context, raw extensions.Event, _ extensions.Context) (any, error) {
			event, ok := raw.(extensions.AgentEndEvent)
			if !ok || len(event.Messages) == 0 {
				return nil, nil
			}
			msgs := extractAIMessages(event.Messages)
			if len(msgs) > 0 {
				runtime.RecordTurnAsync(msgs)
			}
			return nil, nil
		})

		api.On(extensions.EventSessionShutdown, func(_ context.Context, _ extensions.Event, _ extensions.Context) (any, error) {
			runtime.Close()
			return nil, nil
		})

		return nil
	}
}

func notifyCommand(cmdCtx extensions.CommandContext, message string, level extensions.NotificationType) {
	if cmdCtx == nil || cmdCtx.UI() == nil {
		return
	}
	cmdCtx.UI().Notify(message, level)
}

func handleMemoryCommand(ctx context.Context, store memorysdk.Store, runtime *agentmemory.Runtime, args string) (string, error) {
	fields := strings.Fields(strings.TrimSpace(args))
	if len(fields) == 0 {
		st, err := runtime.Status(ctx)
		if err != nil {
			return "", err
		}
		return formatUsageAndStatus(st), nil
	}

	sub := strings.ToLower(fields[0])
	rest := fields[1:]

	switch sub {
	case "status":
		st, err := runtime.Status(ctx)
		if err != nil {
			return "", err
		}
		return formatStatus(st), nil

	case "view":
		return runtime.ViewText(ctx)

	case "compact":
		written, err := runtime.CompactNow(ctx)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("Compacted memory (%d nodes written).", written), nil

	case "search":
		query := strings.TrimSpace(strings.Join(rest, " "))
		if query == "" {
			return "", fmt.Errorf("usage: /memory search <query>")
		}
		hits, err := memorysdk.Search(ctx, store, query, 20)
		if err != nil {
			return "", err
		}
		if len(hits) == 0 {
			return "No memory matches found.", nil
		}
		lines := make([]string, len(hits))
		for i, h := range hits {
			lines[i] = fmt.Sprintf("%s %s", h.Ref, h.Snippet)
		}
		return strings.Join(lines, "\n"), nil

	case "zoom":
		if len(rest) == 0 {
			return "", fmt.Errorf("usage: /memory zoom <id> [n] or /memory zoom <id>+<n>")
		}
		id, n, err := parseZoomArgs(rest)
		if err != nil {
			return "", err
		}
		return memorysdk.Zoom(ctx, store, id, n, 1, 0)

	default:
		st, err := runtime.Status(ctx)
		if err != nil {
			return "", err
		}
		return formatUsageAndStatus(st), nil
	}
}

func parseZoomArgs(args []string) (int, int, error) {
	if id, n, ok := memorysdk.ParseRef(args[0]); ok {
		return id, n, nil
	}
	id, err := strconv.Atoi(args[0])
	if err != nil || id < 0 {
		return 0, 0, fmt.Errorf("invalid id %q", args[0])
	}
	n := 1
	if len(args) > 1 {
		nVal, err := strconv.Atoi(args[1])
		if err != nil || nVal < 1 {
			return 0, 0, fmt.Errorf("invalid span %q", args[1])
		}
		n = nVal
	}
	return id, n, nil
}

func formatStatus(st agentmemory.Status) string {
	return fmt.Sprintf("Memory status: entries=%d nodes=%d profile=%d view=%d/%dB (low=%dB) peer=%s session=%s",
		st.Entries, st.Nodes, st.ProfileItems, st.ViewBytes, st.HighBytes, st.LowBytes, st.Peer, st.Session)
}

func formatUsageAndStatus(st agentmemory.Status) string {
	return formatStatus(st) + "\nUsage: /memory [status | view | compact | search <query> | zoom <id> [n]]"
}

func extractAIMessages(messages engine.AgentMessages) []ai.Message {
	out := make([]ai.Message, 0, len(messages))
	for _, raw := range messages {
		if m, ok := raw.(ai.Message); ok && m != nil {
			out = append(out, m)
		}
	}
	return out
}

func buildStreamSummarizer(streamFn engine.StreamFn, registry extensions.ModelRegistry, modelSpec string) memorysdk.Summarizer {
	if streamFn == nil || registry == nil {
		return nil
	}
	model := resolveCompactorModel(registry, modelSpec)
	if model == nil {
		return nil
	}
	m := *model
	return func(ctx context.Context, systemPrompt, userPrompt string) (string, error) {
		reqCtx := ai.Context{
			SystemPrompt: &systemPrompt,
			Messages: []ai.Message{
				&ai.UserMessage{
					Content:   ai.UserContent{Text: &userPrompt},
					Timestamp: time.Now().UnixMilli(),
				},
			},
		}
		stream, err := streamFn(ctx, &m, reqCtx, nil)
		if err != nil {
			return "", err
		}
		msg, err := ai.Collect(stream)
		if err != nil {
			return "", err
		}
		if msg == nil {
			return "", nil
		}
		var parts []string
		for _, block := range msg.Content {
			if tb, ok := block.(*ai.TextContent); ok && tb != nil {
				if t := strings.TrimSpace(tb.Text); t != "" {
					parts = append(parts, t)
				}
			}
		}
		return strings.Join(parts, "\n"), nil
	}
}

func resolveCompactorModel(registry extensions.ModelRegistry, spec string) *ai.Model {
	spec = strings.TrimSpace(spec)
	if spec == "" || registry == nil {
		return nil
	}
	if provider, id, ok := strings.Cut(spec, "/"); ok {
		if m, found := registry.Find(strings.TrimSpace(provider), strings.TrimSpace(id)); found {
			return &m
		}
	}
	for _, m := range registry.Models() {
		if m.ID == spec {
			cp := m
			return &cp
		}
	}
	return nil
}
