package plugins

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/OrdalieTech/pigo/agent"
	"github.com/OrdalieTech/pigo/ai"
	"github.com/OrdalieTech/pigo/codingagent"
	"github.com/OrdalieTech/pigo/codingagent/config"
	"github.com/OrdalieTech/pigo/codingagent/extensions"
	sessionstore "github.com/OrdalieTech/pigo/codingagent/session"
	memorysdk "github.com/OrdalieTech/pigo/memory"
)

const (
	memoryIndexBytes       = 8 << 10
	distillTranscriptBytes = 12 << 10
	distillMessageLimit    = 40
	distillItemLimit       = 20
	// ponytail: one fixed shutdown deadline is enough; add configuration only
	// when a real provider needs a different distillation budget.
	distillTimeout       = 30 * time.Second
	defaultDistillPrompt = "Extract durable facts, preferences, decisions, and reusable lessons from the transcript. Return one concise memory per line, or NONE."
)

var (
	rememberSchema = ai.JSONSchema(`{"type":"object","required":["content"],"properties":{"content":{"type":"string"},"tags":{"type":"array","items":{"type":"string"}}}}`)
	recallSchema   = ai.JSONSchema(`{"type":"object","properties":{"query":{"type":"string"},"tags":{"type":"array","items":{"type":"string"}},"limit":{"type":"integer","minimum":1,"maximum":100}}}`)
)

type memoryPluginSettings struct {
	inject        string
	indexLimit    int
	distill       bool
	distillPrompt string
}

// MemoryWithStore returns the dormant memory plugin with a caller-supplied SDK
// backend. Registering the factory is the opt-in.
func MemoryWithStore(store memorysdk.Store) extensions.Factory {
	return memoryExtension(store, nil, nil, "")
}

func memoryExtension(store memorysdk.Store, stream agent.StreamFn, settings *config.SettingsManager, agentDir string) extensions.Factory {
	return func(api extensions.API) error {
		activeStore := store
		if activeStore == nil {
			if agentDir == "" {
				var err error
				agentDir, err = config.GetAgentDir()
				if err != nil {
					return err
				}
			}
			var err error
			activeStore, err = memorysdk.NewFileStore(filepath.Join(agentDir, "memory"))
			if err != nil {
				return err
			}
		}
		if activeStore == nil {
			return fmt.Errorf("memory: store is required")
		}
		options := loadMemoryPluginSettings(settings)

		// ponytail: v1 injects one startup index and has no per-turn RAG; add
		// retrieval hooks only when measured sessions outgrow the index.
		// ponytail: v1 searches durable items only, not sessions; add a separate
		// session index when a consumer needs conversation-history search.
		// ponytail: v1 stores caller content verbatim with no secret scanner; add
		// a pre-append policy hook when a deployment crosses that trust boundary.
		// ponytail: v1 has no widget; add UI only when users need memory browsing.
		api.RegisterTool(extensions.ToolDefinition{
			Name: "remember", Label: "Remember", Description: "Save a durable memory", Parameters: rememberSchema,
			Execute: func(ctx context.Context, _ string, raw any, _ agent.AgentToolUpdateCallback, _ extensions.Context) (agent.AgentToolResult, error) {
				var input struct {
					Content string   `json:"content"`
					Tags    []string `json:"tags"`
				}
				if err := decode(raw, &input); err != nil {
					return agent.AgentToolResult{}, err
				}
				input.Content = strings.TrimSpace(input.Content)
				if input.Content == "" {
					return agent.AgentToolResult{}, fmt.Errorf("remember: content is required")
				}
				id, err := activeStore.Append(ctx, memorysdk.Item{Content: input.Content, Tags: normalizeMemoryTags(input.Tags)})
				if err != nil {
					return agent.AgentToolResult{}, err
				}
				return textResult("Remembered " + id + "."), nil
			},
		})
		api.RegisterTool(extensions.ToolDefinition{
			Name: "recall", Label: "Recall", Description: "Search durable memories", Parameters: recallSchema,
			Execute: func(ctx context.Context, _ string, raw any, _ agent.AgentToolUpdateCallback, _ extensions.Context) (agent.AgentToolResult, error) {
				var input struct {
					Query string   `json:"query"`
					Tags  []string `json:"tags"`
					Limit int      `json:"limit"`
				}
				if err := decode(raw, &input); err != nil {
					return agent.AgentToolResult{}, err
				}
				items, err := recallItems(ctx, activeStore, strings.TrimSpace(input.Query), normalizeMemoryTags(input.Tags), input.Limit)
				if err != nil {
					return agent.AgentToolResult{}, err
				}
				if len(items) == 0 {
					return textResult("No memories found."), nil
				}
				lines := make([]string, len(items))
				for index := range items {
					lines[index] = renderMemoryItem(items[index])
				}
				return textResult(strings.Join(lines, "\n")), nil
			},
		})

		api.On(extensions.EventSessionStart, func(ctx context.Context, _ extensions.Event, _ extensions.Context) (any, error) {
			if options.inject == "none" {
				return nil, nil
			}
			items, err := activeStore.Query(ctx, memorysdk.Filter{Limit: options.indexLimit})
			if err != nil || len(items) == 0 {
				return nil, err
			}
			index := renderMemoryIndex(items, memoryIndexBytes)
			if index == "" {
				return nil, nil
			}
			return nil, api.SendMessage(ctx, extensions.CustomMessage{
				CustomType: "pigo.memory.index", Content: index, Display: false,
			}, nil)
		})

		api.On(extensions.EventSessionShutdown, func(ctx context.Context, _ extensions.Event, extensionContext extensions.Context) (any, error) {
			if !options.distill {
				return nil, nil
			}
			messages := extensionContext.SessionManager().BuildSessionContext().Messages
			if len(messages) > distillMessageLimit {
				messages = messages[len(messages)-distillMessageLimit:]
			}
			transcript := forkTranscript(messages)
			if len(transcript) > distillTranscriptBytes {
				transcript = trimLeadingBytes(transcript, distillTranscriptBytes)
			}
			if strings.TrimSpace(transcript) == "" {
				return nil, nil
			}
			model := extensionContext.Model()
			modelRegistry := extensionContext.ModelRegistry()
			cwd := extensionContext.CWD()
			return nil, runDistillWithShutdownDeadline(ctx, func(distillContext context.Context) error {
				return distillMemory(
					distillContext, activeStore, stream, modelRegistry, model, cwd, options.distillPrompt, transcript,
				)
			})
		})
		return nil
	}
}

func loadMemoryPluginSettings(settings *config.SettingsManager) memoryPluginSettings {
	result := memoryPluginSettings{inject: "index", indexLimit: 20, distillPrompt: defaultDistillPrompt}
	if settings == nil {
		return result
	}
	values := settings.GetPluginSettings("memory")
	if inject, ok := values["inject"].(string); ok && inject == "none" {
		result.inject = "none"
	}
	if limit, ok := numberSetting(values["indexLimit"]); ok && limit > 0 {
		if limit > 100 {
			limit = 100
		}
		result.indexLimit = limit
	}
	if distill, ok := values["distill"].(bool); ok {
		result.distill = distill
	}
	if prompt, ok := values["distillPrompt"].(string); ok && strings.TrimSpace(prompt) != "" {
		result.distillPrompt = strings.TrimSpace(prompt)
	}
	return result
}

func numberSetting(value any) (int, bool) {
	switch value := value.(type) {
	case int:
		return value, true
	case float64:
		return int(value), value == float64(int(value))
	default:
		return 0, false
	}
}

func normalizeMemoryTags(tags []string) []string {
	result := make([]string, 0, len(tags))
	seen := make(map[string]struct{}, len(tags))
	for _, tag := range tags {
		tag = strings.ToLower(strings.TrimSpace(tag))
		if tag == "" {
			continue
		}
		if _, exists := seen[tag]; exists {
			continue
		}
		seen[tag] = struct{}{}
		result = append(result, tag)
	}
	return result
}

func recallItems(ctx context.Context, store memorysdk.Store, query string, tags []string, limit int) ([]memorysdk.Item, error) {
	if limit <= 0 || limit > 100 {
		limit = 100
	}
	if semantic, ok := store.(memorysdk.SemanticSearcher); ok && query != "" {
		scored, err := semantic.Search(ctx, query, limit)
		if err != nil {
			return nil, err
		}
		items := make([]memorysdk.Item, 0, len(scored))
		for _, item := range scored {
			if hasMemoryTags(item.Tags, tags) {
				items = append(items, item.Item)
			}
		}
		return items, nil
	}
	return store.Query(ctx, memorysdk.Filter{Tags: tags, Contains: query, Limit: limit})
}

func hasMemoryTags(itemTags, required []string) bool {
	set := make(map[string]struct{}, len(itemTags))
	for _, tag := range itemTags {
		set[tag] = struct{}{}
	}
	for _, tag := range required {
		if _, ok := set[tag]; !ok {
			return false
		}
	}
	return true
}

func renderMemoryItem(item memorysdk.Item) string {
	content, _, _ := strings.Cut(strings.TrimSpace(item.Content), "\n")
	return fmt.Sprintf("%s [%s] %s", item.Time.UTC().Format("2006-01-02T15:04:05Z"), strings.Join(item.Tags, ","), strings.TrimSpace(content))
}

func renderMemoryIndex(items []memorysdk.Item, maxBytes int) string {
	// ponytail: the startup index is capped at 8 KiB; make the cap configurable
	// only when real profiles need a different prompt budget.
	var builder strings.Builder
	for _, item := range items {
		line := renderMemoryItem(item)
		remaining := maxBytes - builder.Len()
		if builder.Len() > 0 {
			remaining--
		}
		if remaining <= 0 {
			break
		}
		if len(line) > remaining {
			line = trimTrailingBytes(line, remaining)
		}
		if builder.Len() > 0 {
			builder.WriteByte('\n')
		}
		builder.WriteString(line)
		if len(line) == remaining {
			break
		}
	}
	return builder.String()
}

func distillMemory(
	ctx context.Context,
	store memorysdk.Store,
	injected agent.StreamFn,
	modelRegistry extensions.ModelRegistry,
	model *ai.Model,
	cwd string,
	prompt string,
	transcript string,
) error {
	if model == nil {
		return fmt.Errorf("memory: distillation requires a model")
	}
	agentDir, err := os.MkdirTemp("", "pigo-memory-distill-")
	if err != nil {
		return fmt.Errorf("memory: prepare distillation: %w", err)
	}
	defer func() { _ = os.RemoveAll(agentDir) }()
	settings, err := config.NewSettingsManager(cwd, config.WithAgentDir(agentDir), config.WithProjectTrusted(false))
	if err != nil {
		return fmt.Errorf("memory: prepare distillation: %w", err)
	}
	manager, err := sessionstore.InMemory(cwd)
	if err != nil {
		return fmt.Errorf("memory: prepare distillation: %w", err)
	}
	sessionOptions := codingagent.AgentSessionOptions{
		CWD: cwd, AgentDir: agentDir, Model: model, ThinkingLevel: ai.ModelThinkingOff,
		NoTools: "all", SessionManager: manager, Settings: settings,
		Resources: &codingagent.Resources{SystemPrompt: &prompt},
	}
	if injected != nil {
		sessionOptions.StreamFn = injected
	} else {
		registry, ok := modelRegistry.(*config.ModelRegistry)
		if !ok {
			return fmt.Errorf("memory: unsupported model registry %T", modelRegistry)
		}
		sessionOptions.ModelRegistry = registry
	}
	result, err := codingagent.NewAgentSession(sessionOptions)
	if err != nil {
		return fmt.Errorf("memory: prepare distillation: %w", err)
	}
	defer result.Session.Dispose()
	distillAgent := result.Session.Agent()
	distillAgent.SetSystemPrompt(prompt)
	// ponytail: one bounded shutdown turn over the last 40 messages; add
	// batching only when real transcripts produce missed durable facts.
	if err := distillAgent.Prompt(ctx, "Transcript:\n"+transcript); err != nil {
		return fmt.Errorf("memory: distillation failed: %w", err)
	}
	if err := distillAgent.WaitForIdle(ctx); err != nil {
		return fmt.Errorf("memory: distillation failed: %w", err)
	}
	state := result.Session.State()
	if len(state.Messages) > 0 {
		if message, ok := state.Messages[len(state.Messages)-1].(*ai.AssistantMessage); ok &&
			(message.StopReason == ai.StopReasonError || message.StopReason == ai.StopReasonAborted) {
			if message.ErrorMessage != nil {
				return fmt.Errorf("memory: distillation failed: %s", *message.ErrorMessage)
			}
			return fmt.Errorf("memory: distillation failed: %s", message.StopReason)
		}
	}
	text := result.Session.GetLastAssistantText()
	if text == nil {
		return nil
	}
	count := 0
	for _, line := range strings.Split(*text, "\n") {
		line = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "- "))
		if line == "" || strings.EqualFold(line, "none") {
			continue
		}
		if _, err := store.Append(ctx, memorysdk.Item{Content: line, Tags: []string{"distilled"}}); err != nil {
			return err
		}
		count++
		if count == distillItemLimit {
			break
		}
	}
	return nil
}

func runDistillWithShutdownDeadline(ctx context.Context, distill func(context.Context) error) error {
	if ctx == nil {
		ctx = context.Background()
	}
	distillContext, cancel := context.WithTimeout(ctx, distillTimeout)
	defer cancel()
	result := make(chan error, 1)
	go func() {
		var err error
		defer func() {
			if recovered := recover(); recovered != nil {
				err = fmt.Errorf("memory: distillation panicked: %v", recovered)
			}
			result <- err
		}()
		err = distill(distillContext)
	}()
	select {
	case err := <-result:
		return err
	case <-distillContext.Done():
		return fmt.Errorf("memory: distillation did not finish before shutdown deadline: %w", distillContext.Err())
	}
}

func trimTrailingBytes(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	for limit > 0 && !utf8.RuneStart(value[limit]) {
		limit--
	}
	return value[:limit]
}

func trimLeadingBytes(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	start := len(value) - limit
	for start < len(value) && !utf8.RuneStart(value[start]) {
		start++
	}
	return value[start:]
}
