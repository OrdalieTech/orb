// F13 Orb replay harness. One harness serves every scenario, in extractor
// order, over a single extension-host process:
//
//   - A fixed-length temp root (/tmp/orb-f13-XXXXXX, matching the extractor's
//     mkdtemp pattern — faux token accounting counts the cwd embedded in the
//     child-session system prompt, so path LENGTH is part of the goldens).
//   - The plugin installed hermetically by the same integrity-pinned lockfile
//     the extractor embeds (npm ci; typebox is linked from the pinned
//     .upstream tree like the extractor does, acorn installs from the lock).
//   - A driver extension (testdata/f13-driver.mjs) loaded through the real
//     host; it imports the plugin sources — whose @earendil-works/pi-*
//     imports resolve to the materialized orb-extension-sdk via loader.mjs —
//     and replays the extractor's scenario logic with the JS clock pinned.
//   - The Go faux provider (Now pinned, 64-token chunks, same scripted
//     response plans) streams every child session through
//     ExtensionAgentSessionService with a pinned Go clock; provider calls are
//     recorded Go-side with the extractor's projection and spliced into the
//     driver's JSON.
//   - Canonicalization mirrors the extractor byte for byte (temp roots,
//     dashed cwd encodings, the workflow project-key hash, first-seen UUID
//     aliasing over the serialized JSON in document order).
package runner

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/OrdalieTech/orb/ai"
	"github.com/OrdalieTech/orb/ai/providers/faux"
	"github.com/OrdalieTech/orb/codingagent"
	"github.com/OrdalieTech/orb/codingagent/config"
	"github.com/OrdalieTech/orb/codingagent/extensions"
	extensionhost "github.com/OrdalieTech/orb/codingagent/extensions/host"
)

const (
	f13FixedNow        = int64(1700000200321)
	f13PluginName      = "@quintinshaw/pi-dynamic-workflows"
	f13PluginVersion   = "3.5.1"
	f13PluginIntegrity = "sha512-YeIIJQpYpF5hPCQm2dlW6zsonZ82BTdelIh1J7TKmaoZkatiTKwM/yBSby7bZpatWQ2YbYdj+RfGEll+cXT8Hg=="
	f13AcornVersion    = "8.16.0"
	f13AcornIntegrity  = "sha512-UVJyE9MttOsBQIDKw1skb9nAwQuR5wuGD3+82K6JgJlm/Y+KI92oNsMNGZCYdDsVtRHSak0pcV5Dno5+4jh9sw=="
)

type f13ProviderCall struct {
	Model     string   `json:"model"`
	ToolNames []string `json:"toolNames"`
	Messages  []any    `json:"messages"`
}

type f13Harness struct {
	root       string
	home       string
	agentDir   string
	project    string
	pluginRoot string
	signalDir  string
	catalogs   map[string]string
	cwdHash    string

	provider *faux.Provider
	aux      *faux.Provider

	mu        sync.Mutex
	auxMarker string
	calls     []f13ProviderCall

	manager *extensionhost.Manager
	runner  *extensions.Runner
	driver  extensions.ToolDefinition
}

func f13RepoRoot() string {
	return filepath.Dir(filepath.Dir(FixtureRoot()))
}

// startF13Harness boots the shared replay environment or skips when the
// machine cannot host it (no Node runtime, no npm, no pinned .upstream tree,
// or an unreachable npm registry with a cold cache).
func startF13Harness(t *testing.T) *f13Harness {
	t.Helper()
	runtime, err := extensionhost.DiscoverRuntime(context.Background())
	if err != nil {
		t.Skip("F13 orb replay requires Node.js >=22.6 on PATH")
	}
	if _, err := exec.LookPath("npm"); err != nil {
		t.Skip("F13 orb replay requires npm on PATH")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("F13 orb replay requires git on PATH")
	}
	typebox := filepath.Join(f13RepoRoot(), ".upstream", "node_modules", "typebox")
	if _, err := os.Stat(filepath.Join(typebox, "package.json")); err != nil {
		t.Skip("F13 orb replay requires the pinned .upstream tree (make ensure-upstream-fixture-tools)")
	}

	// Fixed-length root: the extractor's mkdtemp(join(tmpdir(), "orb-f13-"))
	// appends exactly six characters under /tmp; faux input-token counts embed
	// the cwd's length via the system prompt, so the replay must match it.
	suffix := make([]byte, 3)
	if _, err := rand.Read(suffix); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join("/tmp", "orb-f13-"+hex.EncodeToString(suffix))
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })

	harness := &f13Harness{
		root:      root,
		home:      filepath.Join(root, "home"),
		project:   filepath.Join(root, "project"),
		signalDir: filepath.Join(root, "signals"),
		catalogs:  map[string]string{},
	}
	harness.agentDir = filepath.Join(harness.home, ".pi", "agent")
	hash := sha256.Sum256([]byte(harness.project))
	harness.cwdHash = hex.EncodeToString(hash[:])[:12]
	for _, dir := range []string{harness.agentDir, harness.project, harness.signalDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	harness.writeProjectFixture(t)
	harness.writeAgentDir(t)
	harness.writeCatalogs(t)
	harness.installPlugin(t, typebox)
	harness.startProviders()
	harness.startHost(t, runtime)
	return harness
}

func (harness *f13Harness) writeProjectFixture(t *testing.T) {
	t.Helper()
	write := func(rel, content string) {
		path := filepath.Join(harness.project, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("marker.txt", "marker-from-repo\n")
	write(".pi/agents/reader.md",
		"---\nname: reader\ndescription: read-only scout\ntools: read\n---\nOnly read files; never modify anything.\n")
	write(".pi/agents/isolated.md",
		"---\nname: isolated\ndescription: worktree-isolated worker\ntools: read, bash\ndisallowedTools: bash\nisolation: worktree\n---\nWork inside your isolated worktree only.\n")
	git := func(args ...string) {
		command := exec.Command("git", append([]string{"-C", harness.project}, args...)...)
		command.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=fixture", "GIT_AUTHOR_EMAIL=fixture@example.invalid",
			"GIT_COMMITTER_NAME=fixture", "GIT_COMMITTER_EMAIL=fixture@example.invalid",
			"GIT_AUTHOR_DATE=2026-02-03T04:05:06Z", "GIT_COMMITTER_DATE=2026-02-03T04:05:06Z",
			"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null",
		)
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, output)
		}
	}
	git("init", "--quiet", "--initial-branch=main")
	git("add", "marker.txt")
	git("commit", "--quiet", "-m", "fixture")
}

func (harness *f13Harness) writeAgentDir(t *testing.T) {
	t.Helper()
	settings := "{\n  \"defaultProvider\": \"faux\",\n  \"defaultModel\": \"faux-model\"\n}\n"
	if err := os.WriteFile(filepath.Join(harness.agentDir, "settings.json"), []byte(settings), 0o644); err != nil {
		t.Fatal(err)
	}
}

// Catalogs back the SDK's ModelRuntime.create({authPath, modelsPath}) handles
// (model_runtime_v1 resolves the directory of modelsPath as the registry
// root). Model shapes mirror the faux worlds the extractor registered —
// upstream faux defaults contextWindow 128000 / maxTokens 16384 on every
// model definition.
func (harness *f13Harness) writeCatalogs(t *testing.T) {
	t.Helper()
	model := func(id, name string, cost bool) string {
		costJSON := ""
		if cost {
			costJSON = `,"cost":{"input":0,"output":0,"cacheRead":0,"cacheWrite":0}`
		}
		return `{"id":"` + id + `","name":"` + name + `","reasoning":false,"input":["text"]` + costJSON +
			`,"contextWindow":128000,"maxTokens":16384}`
	}
	catalog := func(models ...string) string {
		return `{"providers":{"faux":{"name":"faux","baseUrl":"http://127.0.0.1:1","api":"faux","apiKey":"faux-key","models":[` +
			strings.Join(models, ",") + `]}}}` + "\n"
	}
	catalogs := map[string]string{
		"default": catalog(model("faux-model", "Faux Model", true)),
		"routing": catalog(model("faux-model", "Faux Model", false), model("faux-mini", "Faux Mini", true)),
		"models":  catalog(model("faux-model", "Faux Model", false), model("faux-mini", "Faux Mini", false)),
	}
	for name, content := range catalogs {
		dir := filepath.Join(harness.root, "catalog", name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "models.json"), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		harness.catalogs[name] = dir
	}
}

func (harness *f13Harness) installPlugin(t *testing.T, typebox string) {
	t.Helper()
	pluginFixture := filepath.Join(harness.root, "plugin")
	if err := os.MkdirAll(pluginFixture, 0o755); err != nil {
		t.Fatal(err)
	}
	packageJSON := fmt.Sprintf(`{
  "name": "orb-f13-plugin-fixture",
  "version": "1.0.0",
  "private": true,
  "dependencies": {
    %q: %q
  }
}
`, f13PluginName, f13PluginVersion)
	lock := fmt.Sprintf(`{
  "name": "orb-f13-plugin-fixture",
  "version": "1.0.0",
  "lockfileVersion": 3,
  "requires": true,
  "packages": {
    "": {
      "name": "orb-f13-plugin-fixture",
      "version": "1.0.0",
      "dependencies": { %q: %q }
    },
    "node_modules/%s": {
      "version": %q,
      "resolved": "https://registry.npmjs.org/%s/-/pi-dynamic-workflows-%s.tgz",
      "integrity": %q,
      "license": "MIT",
      "dependencies": { "acorn": "^8.16.0" },
      "peerDependencies": {
        "@earendil-works/pi-coding-agent": ">=0.80.8",
        "@earendil-works/pi-tui": ">=0.80.6",
        "typebox": "*"
      }
    },
    "node_modules/acorn": {
      "version": %q,
      "resolved": "https://registry.npmjs.org/acorn/-/acorn-%s.tgz",
      "integrity": %q,
      "license": "MIT",
      "bin": { "acorn": "bin/acorn" },
      "engines": { "node": ">=0.4.0" }
    }
  }
}
`, f13PluginName, f13PluginVersion, f13PluginName, f13PluginVersion, f13PluginName, f13PluginVersion,
		f13PluginIntegrity, f13AcornVersion, f13AcornVersion, f13AcornIntegrity)
	if err := os.WriteFile(filepath.Join(pluginFixture, "package.json"), []byte(packageJSON), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pluginFixture, "package-lock.json"), []byte(lock), 0o644); err != nil {
		t.Fatal(err)
	}
	command := exec.Command("npm", "ci", "--ignore-scripts", "--no-audit", "--no-fund",
		"--legacy-peer-deps", "--prefer-offline")
	command.Dir = pluginFixture
	command.Env = append(os.Environ(), "npm_config_update_notifier=false")
	if output, err := command.CombinedOutput(); err != nil {
		t.Skipf("F13 orb replay: npm ci for the pinned plugin failed (registry unreachable and cache cold?): %v\n%s", err, output)
	}
	// The plugin value-imports typebox (a peer dependency the lock does not
	// install); resolve it to the pinned .upstream copy like the extractor.
	if err := os.Symlink(typebox, filepath.Join(pluginFixture, "node_modules", "typebox")); err != nil {
		t.Fatal(err)
	}
	harness.pluginRoot = filepath.Join(pluginFixture, "node_modules", f13PluginName)
}

func (harness *f13Harness) startProviders() {
	now := func() int64 { return f13FixedNow }
	models := []faux.ModelDefinition{{ID: "faux-model", Name: f13String("Faux Model")}}
	harness.provider = faux.New(faux.Options{
		API: "faux", Provider: "faux", Models: models, TokenSize: faux.FixedTokenSize(64), Now: now,
	})
	harness.aux = faux.New(faux.Options{
		API: "faux", Provider: "faux", Models: models, TokenSize: faux.FixedTokenSize(64), Now: now,
	})
}

func f13String(value string) *string { return &value }

// f13IdentityToUpstream inverts the D30 product-identity substitutions the F9
// runner applies to its goldens (f9OrbPromptReplacer): the F13 goldens bake
// upstream faux token accounting — which counts the system prompt — so the
// child session's Orb-branded prompt is mapped back to the upstream bytes
// before the faux provider sees it. This is the same sanctioned D30 mapping,
// applied in the opposite direction; nothing else is rewritten.
var f13IdentityToUpstream = strings.NewReplacer(
	"You are an expert problem-solving assistant operating inside Orb, a general-purpose agent harness for work and software development. You help users investigate, plan, create, and complete tasks using the available tools, including working with files, executing commands, and editing code or documents.",
	"You are an expert coding assistant operating inside pi, a coding agent harness. You help users by reading files, executing commands, editing code, and writing new files.",
	"Orb documentation (read only when the user asks about Orb itself, its SDK, extensions, themes, skills, or TUI):",
	"Pi documentation (read only when the user asks about pi itself, its SDK, extensions, themes, skills, or TUI):",
	"When reading Orb docs or examples", "When reading pi docs or examples",
	"When working on Orb topics", "When working on pi topics",
	"Always read Orb documentation files completely", "Always read pi .md files completely",
)

// streamFn routes child-session streaming onto the scripted faux providers,
// recording each primary-provider call with the extractor's projection.
func (harness *f13Harness) streamFn(ctx context.Context, model *ai.Model, requestContext ai.Context, options *ai.SimpleStreamOptions) (ai.AssistantMessageEventStream, error) {
	if requestContext.SystemPrompt != nil {
		mapped := f13IdentityToUpstream.Replace(*requestContext.SystemPrompt)
		requestContext.SystemPrompt = &mapped
	}
	if os.Getenv("ORB_F13_DEBUG") != "" {
		dumpDir := "/tmp/f13-debug"
		_ = os.MkdirAll(dumpDir, 0o755)
		var dump strings.Builder
		if requestContext.SystemPrompt != nil {
			dump.WriteString("=== SYSTEM ===\n" + *requestContext.SystemPrompt + "\n")
		}
		if requestContext.Tools != nil {
			encoded, _ := ai.Marshal(*requestContext.Tools)
			dump.WriteString("=== TOOLS ===\n" + string(encoded) + "\n")
		}
		for _, message := range requestContext.Messages {
			encoded, _ := ai.Marshal(message)
			dump.WriteString("=== MSG ===\n" + string(encoded) + "\n")
		}
		harness.mu.Lock()
		index := len(harness.calls)
		harness.mu.Unlock()
		_ = os.WriteFile(fmt.Sprintf("%s/call-%d.txt", dumpDir, index), []byte(dump.String()), 0o644)
	}
	harness.mu.Lock()
	marker := harness.auxMarker
	harness.mu.Unlock()
	provider := harness.provider
	record := true
	if marker != "" && strings.Contains(f13LastUserText(requestContext), marker) {
		provider = harness.aux
		record = false
	}
	if record {
		call, err := f13ProjectCall(model, requestContext)
		if err != nil {
			return nil, err
		}
		harness.mu.Lock()
		harness.calls = append(harness.calls, call)
		harness.mu.Unlock()
	}
	return provider.StreamSimple(ctx, model, requestContext, options)
}

func f13LastUserText(requestContext ai.Context) string {
	for index := len(requestContext.Messages) - 1; index >= 0; index-- {
		if user, ok := requestContext.Messages[index].(*ai.UserMessage); ok {
			if user.Content.Text != nil {
				return *user.Content.Text
			}
			var parts []string
			for _, block := range user.Content.Blocks {
				if text, ok := block.(*ai.TextContent); ok {
					parts = append(parts, text.Text)
				}
			}
			return strings.Join(parts, "\n")
		}
	}
	return ""
}

// f13ProjectCall mirrors the extractor's projectMessage over the request the
// provider received.
func f13ProjectCall(model *ai.Model, requestContext ai.Context) (f13ProviderCall, error) {
	call := f13ProviderCall{
		Model:     fmt.Sprintf("%s/%s", model.Provider, model.ID),
		ToolNames: []string{},
		Messages:  []any{},
	}
	if requestContext.Tools != nil {
		for _, tool := range *requestContext.Tools {
			call.ToolNames = append(call.ToolNames, tool.Name)
		}
	}
	for _, message := range requestContext.Messages {
		encoded, err := ai.Marshal(message)
		if err != nil {
			return call, err
		}
		var generic map[string]any
		if err := json.Unmarshal(encoded, &generic); err != nil {
			return call, err
		}
		call.Messages = append(call.Messages, f13ProjectMessage(generic))
	}
	return call, nil
}

func f13ProjectMessage(message map[string]any) any {
	role, _ := message["role"].(string)
	if role == "toolResult" {
		text := ""
		if content, ok := message["content"].([]any); ok {
			for _, rawPart := range content {
				part, _ := rawPart.(map[string]any)
				if part["type"] == "text" {
					value, _ := part["text"].(string)
					text += value
				}
			}
		} else if value, ok := message["content"].(string); ok {
			text = value
		}
		isError, _ := message["isError"].(bool)
		return map[string]any{"role": role, "toolName": message["toolName"], "isError": isError, "text": text}
	}
	if text, ok := message["content"].(string); ok {
		return map[string]any{"role": role, "text": text}
	}
	if content, ok := message["content"].([]any); ok {
		parts := make([]any, 0, len(content))
		for _, rawPart := range content {
			part, _ := rawPart.(map[string]any)
			switch part["type"] {
			case "text":
				parts = append(parts, map[string]any{"type": "text", "text": part["text"]})
			case "toolCall":
				parts = append(parts, map[string]any{"type": "toolCall", "name": part["name"], "arguments": part["arguments"]})
			default:
				partType, ok := part["type"]
				if !ok {
					partType = "unknown"
				}
				parts = append(parts, map[string]any{"type": partType})
			}
		}
		projected := map[string]any{"role": role, "parts": parts}
		if reason, ok := message["stopReason"].(string); ok && reason != "" {
			projected["stopReason"] = reason
		}
		if errorMessage, ok := message["errorMessage"].(string); ok && errorMessage != "" {
			projected["errorMessage"] = errorMessage
		}
		return projected
	}
	return map[string]any{"role": role}
}

func (harness *f13Harness) startHost(t *testing.T, runtime extensionhost.Runtime) {
	t.Helper()
	// The host process and every Go-side path resolution live inside the
	// harness home: the plugin reads ~/.pi/workflows/* through homedir().
	t.Setenv("HOME", harness.home)
	t.Setenv(config.EnvAgentDir, harness.agentDir)
	// The extractor cleared XDG_CONFIG_HOME so no machine-level config leaks
	// into resource discovery.
	if value, ok := os.LookupEnv("XDG_CONFIG_HOME"); ok {
		_ = os.Unsetenv("XDG_CONFIG_HOME")
		t.Cleanup(func() { _ = os.Setenv("XDG_CONFIG_HOME", value) })
	}
	// Upstream built its docs section from the pinned .upstream package dir;
	// point Orb's prompt builder at the same directory so the docs paths — and
	// with them the faux token accounting — carry identical byte lengths.
	t.Setenv("PI_PACKAGE_DIR", filepath.Join(f13RepoRoot(), ".upstream", "packages", "coding-agent"))
	manager := extensionhost.NewManager(extensionhost.Options{
		AgentDir: harness.agentDir, CWD: harness.project, Version: "test", Runtime: &runtime,
		RequestTimeout: 5 * time.Minute, ShutdownTimeout: 2 * time.Second,
		BackoffBase: 10 * time.Millisecond, BackoffMax: 50 * time.Millisecond,
	})
	t.Cleanup(func() {
		if err := manager.Close(); err != nil {
			t.Errorf("close manager: %v", err)
		}
	})
	manager.SetAgentSessionService(codingagent.NewExtensionAgentSessionService(codingagent.ExtensionAgentSessionServiceOptions{
		CWD:      harness.project,
		AgentDir: harness.agentDir,
		StreamFn: harness.streamFn,
		Clock:    func() int64 { return f13FixedNow },
	}))
	modelRegistry, err := config.NewOfflineModelRegistry(harness.agentDir)
	if err != nil {
		t.Fatal(err)
	}
	registry := extensions.NewRegistry(harness.project)
	driverPath := filepath.Join(f13RepoRoot(), "conformance", "runner", "testdata", "f13-driver.mjs")
	result := manager.RegisterInto(context.Background(), registry, []string{driverPath})
	if len(result.Errors) != 0 || len(result.Diagnostics) != 0 {
		t.Fatalf("driver load result = %#v", result)
	}
	harness.manager = manager
	harness.runner = extensions.NewRunner(registry, extensions.RunnerOptions{CWD: harness.project, ModelRegistry: modelRegistry})
	definition := harness.runner.ToolDefinition("f13_scenario")
	if definition == nil {
		t.Fatal("f13_scenario driver tool was not registered")
	}
	harness.driver = *definition
}

// f13ScenarioPlan configures the faux providers for one scenario, mirroring
// the extractor's world.setResponses plans.
func (harness *f13Harness) prepareScenario(t *testing.T, scenario string) (catalog string) {
	t.Helper()
	now := f13FixedNow
	text := func(body string) faux.ResponseStep {
		return faux.AssistantMessage(body, faux.AssistantMessageOptions{Timestamp: &now})
	}
	toolUse := func(lead string, calls ...*ai.ToolCall) faux.ResponseStep {
		content := ai.AssistantContent{}
		if lead != "" {
			content = append(content, faux.Text(lead))
		}
		for _, call := range calls {
			content = append(content, call)
		}
		return faux.AssistantMessage(content, faux.AssistantMessageOptions{StopReason: ai.StopReasonToolUse, Timestamp: &now})
	}
	// call builds a scripted tool call from ordered JSON: upstream faux emits
	// arguments in object insertion order, which flows into mirrored history
	// texts and the JS shared store; SetToolCallArgumentsJSON preserves it.
	call := func(name, argumentsJSON, id string) *ai.ToolCall {
		toolCall := faux.ToolCall(name, nil, faux.ToolCallOptions{ID: id})
		if err := ai.SetToolCallArgumentsJSON(toolCall, []byte(argumentsJSON)); err != nil {
			t.Fatalf("tool call %s arguments: %v", name, err)
		}
		return toolCall
	}
	limit := func(message string) faux.ResponseStep {
		return faux.AssistantMessage(ai.AssistantContent{}, faux.AssistantMessageOptions{
			StopReason: ai.StopReasonError, ErrorMessage: &message, Timestamp: &now,
		})
	}
	// hang blocks until the request context is cancelled, then settles as the
	// aborted assistant message; it drops a signal file first so the driver
	// can abort or quit only once the call is truly in flight.
	hang := func(signal string) faux.ResponseStep {
		return faux.Factory(func(ctx context.Context, _ ai.Context, _ *ai.StreamOptions, _ faux.State, _ *ai.Model) (*ai.AssistantMessage, error) {
			if signal != "" {
				_ = os.WriteFile(filepath.Join(harness.signalDir, signal), []byte("1"), 0o644)
			}
			<-ctx.Done()
			aborted := "Request was aborted"
			return faux.AssistantMessage(ai.AssistantContent{}, faux.AssistantMessageOptions{
				StopReason: ai.StopReasonAborted, ErrorMessage: &aborted, Timestamp: &now,
			}), nil
		})
	}

	harness.mu.Lock()
	harness.auxMarker = ""
	harness.calls = nil
	harness.mu.Unlock()
	harness.provider.SetResponses(nil)
	harness.aux.SetResponses(nil)

	catalog = "default"
	switch scenario {
	case "foreground-basic":
		harness.provider.SetResponses([]faux.ResponseStep{
			text("The marker file says marker-from-repo."),
			text("Summary: the marker holds marker-from-repo."),
		})
	case "structured-output":
		harness.provider.SetResponses([]faux.ResponseStep{
			toolUse("", call("structured_output", `{"fruit":"apple","count":3}`, "structured-1")),
			text("I will not call the tool."),
			toolUse("", call("structured_output", `{"veg":"leek"}`, "structured-2")),
			text("Thinking about minerals..."),
			text("Still refusing the tool."),
			text("Here you go:\n```json\n{\"mineral\":\"quartz\"}\n```"),
		})
	case "store-tools":
		harness.provider.SetResponses([]faux.ResponseStep{
			toolUse("", call("store_put", `{"key":"finding","value":{"n":1,"kind":"seed"}}`, "store-1")),
			text("Stored the finding."),
			toolUse("", call("store_get", `{"key":"finding"}`, "store-2")),
			text("The finding was n=1 kind=seed."),
		})
	case "web-toolset":
		harness.provider.SetResponses([]faux.ResponseStep{
			toolUse("", call("web_search", `{"query":"orb conformance"}`, "web-1")),
			toolUse("", call("web_fetch", `{"url":"https://example.test/page"}`, "web-2")),
			text("Research done: example.test documents the topic."),
		})
	case "agent-types":
		harness.provider.SetResponses([]faux.ResponseStep{
			toolUse("", call("read", `{"path":"marker.txt"}`, "agents-1")),
			text("The marker says marker-from-repo."),
			toolUse("", call("read", `{"path":"marker.txt"}`, "agents-2")),
			text("Isolated read complete."),
			text("Ghost type fell back to defaults."),
		})
	case "nested-workflow":
		harness.provider.SetResponses([]faux.ResponseStep{
			text("Child one reporting on seeds."),
			text("Parent used the child result."),
		})
	case "cancellation":
		harness.provider.SetResponses([]faux.ResponseStep{
			text("First result before the abort."),
			hang("cancellation"),
		})
	case "model-routing":
		catalog = "routing"
		harness.provider.SetResponses([]faux.ResponseStep{
			text("Small tier answer."),
			text("Untagged answer."),
			text("Synthesized custom-id answer."),
		})
	case "background-lifecycle":
		harness.provider.SetResponses([]faux.ResponseStep{
			text("First succeeded."),
			limit("Codex usage limit reached (plus plan). Resets in ~3h."),
			// Queued for the resume leg (the extractor re-arms the same world
			// before resuming; calls are strictly sequential here).
			text("Second succeeded after the reset."),
		})
		harness.mu.Lock()
		harness.auxMarker = "Hangs until stopped"
		harness.mu.Unlock()
		harness.aux.SetResponses([]faux.ResponseStep{hang("background-stop")})
	case "persist-agent-sessions":
		harness.provider.SetResponses([]faux.ResponseStep{
			toolUse("", call("read", `{"path":"marker.txt"}`, "persist-1")),
			text("Kept the marker safe."),
		})
	case "extension-lifecycle":
		harness.provider.SetResponses([]faux.ResponseStep{
			text("Extension agent result."),
			hang("ext-hang"),
		})
	case "workflows-models":
		catalog = "models"
	case "export-surface":
		// No provider traffic.
	default:
		t.Fatalf("unknown scenario %q", scenario)
	}
	return catalog
}

// replayScenario drives one scenario through the driver extension and returns
// the canonicalized observation JSON.
func (harness *f13Harness) replayScenario(t *testing.T, scenario string) []byte {
	t.Helper()
	catalog := harness.prepareScenario(t, scenario)
	params := map[string]any{
		"scenario":   scenario,
		"fixedNow":   f13FixedNow,
		"pluginRoot": harness.pluginRoot,
		"project":    harness.project,
		"home":       harness.home,
		"agentDir":   harness.agentDir,
		"catalogDir": harness.catalogs[catalog],
		"signalDir":  harness.signalDir,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	final, err := harness.driver.Execute(ctx, "call-f13-"+scenario, params, nil, harness.runner.CreateContext())
	if err != nil {
		t.Fatalf("driver scenario %s failed: %v", scenario, err)
	}
	payload := ""
	for _, block := range final.Content {
		if textBlock, ok := block.(*ai.TextContent); ok {
			payload += textBlock.Text
		}
	}
	var envelope struct {
		OK    bool            `json:"ok"`
		Error string          `json:"error"`
		Value json.RawMessage `json:"value"`
	}
	if err := json.Unmarshal([]byte(payload), &envelope); err != nil {
		t.Fatalf("driver scenario %s returned unparseable payload: %v\n%s", scenario, err, payload)
	}
	if !envelope.OK {
		t.Fatalf("driver scenario %s failed in-host:\n%s", scenario, envelope.Error)
	}

	harness.mu.Lock()
	calls := append([]f13ProviderCall(nil), harness.calls...)
	harness.mu.Unlock()
	callsJSON, err := json.Marshal(calls)
	if err != nil {
		t.Fatal(err)
	}
	if calls == nil {
		callsJSON = []byte("[]")
	}
	pending := harness.provider.PendingResponseCount() + harness.aux.PendingResponseCount()
	replayed := string(envelope.Value)
	replayed = strings.ReplaceAll(replayed, `"__F13_PROVIDER_CALLS__"`, string(callsJSON))
	replayed = strings.ReplaceAll(replayed, `"__F13_PENDING__"`, fmt.Sprintf("%d", pending))
	return harness.canonicalize([]byte(replayed))
}

var f13UUIDPattern = regexp.MustCompile(`[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}`)

// canonicalize mirrors the extractor's makeCanonicalizer over the serialized
// JSON: fixed replacements first, then first-seen UUID aliasing in document
// order (equivalent to the extractor's depth-first value traversal).
func (harness *f13Harness) canonicalize(document []byte) []byte {
	value := string(document)
	replacements := [][2]string{}
	roots := []string{harness.root}
	if canonical, err := filepath.EvalSymlinks(harness.root); err == nil && canonical != harness.root {
		roots = append(roots, canonical)
	}
	for _, root := range roots {
		dashed := strings.NewReplacer("/", "-", ".", "-").Replace(root)
		replacements = append(replacements, [2]string{root, "<fixture>"}, [2]string{dashed, "<fixture-dash>"})
	}
	replacements = append(replacements, [2]string{harness.cwdHash, "<cwdhash>"})
	for _, pair := range replacements {
		value = strings.ReplaceAll(value, pair[0], pair[1])
	}
	seen := map[string]string{}
	value = f13UUIDPattern.ReplaceAllStringFunc(value, func(match string) string {
		key := strings.ToLower(match)
		alias, ok := seen[key]
		if !ok {
			alias = fmt.Sprintf("<uuid-%d>", len(seen)+1)
			seen[key] = alias
		}
		return alias
	})
	return []byte(value)
}

// f13DiffCanonical structurally compares the canonicalized replay against the
// committed golden and renders the divergences path by path.
func f13DiffCanonical(golden, replayed []byte) string {
	var want, got any
	if err := json.Unmarshal(golden, &want); err != nil {
		return fmt.Sprintf("golden is not valid JSON: %v", err)
	}
	if err := json.Unmarshal(replayed, &got); err != nil {
		return fmt.Sprintf("replay is not valid JSON: %v", err)
	}
	var diffs []string
	f13Compare("$", want, got, &diffs)
	if len(diffs) == 0 {
		return ""
	}
	const cap = 40
	if len(diffs) > cap {
		diffs = append(diffs[:cap], fmt.Sprintf("... and %d more", len(diffs)-cap))
	}
	return strings.Join(diffs, "\n")
}

func f13Compare(path string, want, got any, diffs *[]string) {
	switch wantValue := want.(type) {
	case map[string]any:
		gotValue, ok := got.(map[string]any)
		if !ok {
			*diffs = append(*diffs, fmt.Sprintf("%s: want object, got %s", path, f13Render(got)))
			return
		}
		keys := make([]string, 0, len(wantValue)+len(gotValue))
		for key := range wantValue {
			keys = append(keys, key)
		}
		for key := range gotValue {
			if _, exists := wantValue[key]; !exists {
				keys = append(keys, key)
			}
		}
		sort.Strings(keys)
		for _, key := range keys {
			wantChild, wantOK := wantValue[key]
			gotChild, gotOK := gotValue[key]
			childPath := path + "." + key
			switch {
			case !wantOK:
				*diffs = append(*diffs, fmt.Sprintf("%s: unexpected key (got %s)", childPath, f13Render(gotChild)))
			case !gotOK:
				*diffs = append(*diffs, fmt.Sprintf("%s: missing (want %s)", childPath, f13Render(wantChild)))
			default:
				f13Compare(childPath, wantChild, gotChild, diffs)
			}
		}
	case []any:
		gotValue, ok := got.([]any)
		if !ok {
			*diffs = append(*diffs, fmt.Sprintf("%s: want array, got %s", path, f13Render(got)))
			return
		}
		if len(wantValue) != len(gotValue) {
			*diffs = append(*diffs, fmt.Sprintf("%s: want %d elements, got %d", path, len(wantValue), len(gotValue)))
		}
		for index := 0; index < len(wantValue) && index < len(gotValue); index++ {
			f13Compare(fmt.Sprintf("%s[%d]", path, index), wantValue[index], gotValue[index], diffs)
		}
	default:
		if !reflect.DeepEqual(want, got) {
			*diffs = append(*diffs, fmt.Sprintf("%s: want %s, got %s", path, f13Render(want), f13Render(got)))
		}
	}
}

func f13Render(value any) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		return fmt.Sprintf("%v", value)
	}
	if len(encoded) > 200 {
		encoded = append(encoded[:200], []byte("...")...)
	}
	return string(encoded)
}
