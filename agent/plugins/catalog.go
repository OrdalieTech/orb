// Package plugins contains orb's default-off first-party extensions.
package plugins

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/OrdalieTech/orb/agent/config"
	"github.com/OrdalieTech/orb/agent/extensions"
	"github.com/OrdalieTech/orb/engine"
	"github.com/OrdalieTech/orb/sandbox"
)

// Options supplies explicit runtime seams so bundled plugins remain instance-scoped.
type Options struct {
	StreamFn   engine.StreamFn
	HTTPClient *http.Client
	Settings   *config.SettingsManager
	Policy     *Policy
	AgentDir   string
}

var names = []string{"tasks", "websearch", "subagents", "permissions", "memory"}

var descriptions = map[string]string{
	"tasks":       "Live session task list and todo tool",
	"websearch":   "Web search and readable page fetching",
	"subagents":   "Single or parallel child agents, including configured external CLIs",
	"permissions": "Permissive audit and tool-call permission rules (bash is matched by command text only)",
	"memory":      "Bounded persistent remember, recall, replace, and forget tools",
}

// Names returns the stable first-party plugin order.
func Names() []string { return append([]string(nil), names...) }

// Description returns the one-line description used by the CLI and TUI.
func Description(name string) string { return descriptions[name] }

// Catalog returns fresh extension factories for embedders to select per instance.
func Catalog(option ...Options) map[string]extensions.Factory {
	if len(option) > 1 {
		panic("plugins: Catalog accepts at most one Options value")
	}
	var options Options
	if len(option) == 1 {
		options = option[0]
	}
	policy, inheritPolicy := options.Policy, options.Policy
	if policy == nil && options.Settings != nil && options.Settings.GetPlugins()["permissions"] {
		var err error
		policy, err = policyFromSettings(options.Settings.GetPluginSettings("permissions"))
		if err != nil {
			policy = &Policy{Mode: "enforce", Guards: []func(context.Context, ToolCallInfo) string{func(context.Context, ToolCallInfo) string { return err.Error() }}}
		}
		inheritPolicy = policy
	}
	if policy == nil {
		policy = &Policy{}
	}
	return map[string]extensions.Factory{
		"tasks":       tasksExtension(),
		"websearch":   websearchExtension(options.HTTPClient),
		"subagents":   subagentsExtension(options.StreamFn, inheritPolicy, options.Settings),
		"permissions": permissionsExtension(policy, options.Settings, nil),
		"memory":      memoryExtension(nil, options.AgentDir),
	}
}

// Control registers /plugins independently of the default-off plugin catalog.
func Control(settings *config.SettingsManager) extensions.Factory {
	return func(api extensions.API) error {
		api.RegisterCommand("plugins", extensions.Command{
			Description: "Enable or disable bundled plugins",
			Handler: func(ctx context.Context, _ string, command extensions.CommandContext) error {
				if !command.HasUI() {
					return fmt.Errorf("/plugins requires interactive mode")
				}
				dirty := false
				for {
					enabled := settings.GetPlugins()
					choices := make([]string, 0, len(names)+1)
					choiceNames := make(map[string]string, len(names))
					for _, name := range names {
						mark := " "
						if enabled[name] {
							mark = "x"
						}
						label := fmt.Sprintf("[%s] %s — %s", mark, name, descriptions[name])
						choices = append(choices, label)
						choiceNames[label] = name
					}
					choices = append(choices, "Done")
					selected, ok, err := command.UI().Select(ctx, "Bundled plugins", choices, nil)
					if err != nil {
						return err
					}
					if !ok || selected == "Done" {
						break
					}
					name := choiceNames[selected]
					if name == "" {
						continue
					}
					settings.SetPluginEnabled(name, !enabled[name])
					dirty = true
				}
				if !dirty {
					return nil
				}
				for _, settingsError := range settings.DrainErrors() {
					if strings.TrimSpace(settingsError.Error()) != "" {
						return settingsError
					}
				}
				return command.Reload(ctx)
			},
		})
		return nil
	}
}

const bashCaveat = "Bash is matched by its command text only: a tool-scoped rule does not stop an equivalent shell command."

// Action is the ecosystem-standard three-state permission result.
type Action string

const (
	Allow Action = "allow"
	Deny  Action = "deny"
	Ask   Action = "ask"
)

// Rule is one ordered permission rule. Empty Tool means "*".
type Rule struct {
	Tool    string `json:"tool,omitempty"`
	Command string `json:"command,omitempty"`
	Path    string `json:"path,omitempty"`
	Action  Action `json:"action"`
}

// ToolCallInfo is the low-level SDK authorization input.
type ToolCallInfo struct {
	Tool      string
	Args      any
	CWD       string
	SessionID string
}

// Decision records the matched policy and its runtime resolution.
type Decision struct {
	Time       int64  `json:"time"`
	Tool       string `json:"tool"`
	Action     Action `json:"action"`
	Resolved   Action `json:"resolved"`
	Mode       string `json:"mode"`
	Rule       int    `json:"rule,omitempty"`
	Matcher    string `json:"matcher,omitempty"`
	Input      string `json:"input,omitempty"`
	Resolution string `json:"resolution,omitempty"`
}

// Policy is constructible by SDK embedders and shared with in-process children.
type Policy struct {
	Preset      string       `json:"preset,omitempty"`
	Sandbox     sandbox.Mode `json:"sandbox,omitempty"`
	Mode        string       `json:"mode,omitempty"`
	AskFallback Action       `json:"askFallback,omitempty"`
	Rules       []Rule       `json:"rules,omitempty"`

	Authorizer func(context.Context, ToolCallInfo) (Action, error) `json:"-"`
	Guards     []func(context.Context, ToolCallInfo) string        `json:"-"`

	mu        sync.Mutex
	askMu     sync.Mutex
	approved  map[string]struct{}
	decisions []Decision
}

// SandboxMode returns the filesystem sandbox selected by the enabled permissions plugin.
func SandboxMode(settings *config.SettingsManager) (sandbox.Mode, error) {
	if settings == nil || !settings.GetPlugins()["permissions"] {
		return sandbox.ModeDangerFullAccess, nil
	}
	policy, err := policyFromSettings(settings.GetPluginSettings("permissions"))
	if err != nil {
		return "", err
	}
	if policy.Sandbox == "" {
		return sandbox.ModeDangerFullAccess, nil
	}
	return policy.Sandbox, nil
}

func policyFromSettings(value map[string]any) (*Policy, error) {
	policy := &Policy{}
	for key, configured := range value {
		switch key {
		case "enabled":
			if _, ok := configured.(bool); !ok {
				return nil, fmt.Errorf("plugins: permissions.enabled must be true or false")
			}
		case "preset", "sandbox", "mode", "askFallback", "rules":
			text, stringValue := configured.(string)
			if configured == nil || stringValue && text == "" {
				return nil, fmt.Errorf("plugins: permissions.%s must not be empty", key)
			}
		default:
			return nil, fmt.Errorf("plugins: permissions.%s is unknown", key)
		}
	}
	switch value["preset"] {
	case nil:
	case "workspace-write":
		policy.Sandbox, policy.Mode = sandbox.ModeWorkspaceWrite, "enforce"
	case "danger-full-access":
		policy.Sandbox, policy.Mode = sandbox.ModeDangerFullAccess, "log"
	default:
		return nil, fmt.Errorf("plugins: permissions.preset must be workspace-write or danger-full-access")
	}
	encoded, err := json.Marshal(value)
	var rawPolicy struct {
		Rules []map[string]json.RawMessage `json:"rules"`
	}
	if err == nil {
		err = json.Unmarshal(encoded, policy)
	}
	if err == nil {
		err = json.Unmarshal(encoded, &rawPolicy)
	}
	if err != nil {
		if field, ok := err.(*json.UnmarshalTypeError); ok {
			return nil, fmt.Errorf("plugins: permissions.%s has invalid type", field.Field)
		}
		return nil, fmt.Errorf("plugins: invalid permissions settings: %w", err)
	}
	for index, rule := range rawPolicy.Rules {
		for key, configured := range rule {
			if key != "action" && key != "tool" && key != "command" && key != "path" {
				return nil, fmt.Errorf("plugins: permissions.rules[%d].%s is unknown", index, key)
			}
			if key != "action" && string(configured) == "null" {
				return nil, fmt.Errorf("plugins: permissions.rules[%d].%s must not be null", index, key)
			}
		}
	}
	for _, setting := range []struct {
		name  string
		valid bool
	}{{"sandbox", policy.Sandbox == "" || policy.Sandbox == sandbox.ModeReadOnly || policy.Sandbox == sandbox.ModeWorkspaceWrite || policy.Sandbox == sandbox.ModeDangerFullAccess}, {"mode", policy.Mode == "" || policy.Mode == "log" || policy.Mode == "enforce"}, {"askFallback", policy.AskFallback == "" || policy.AskFallback == Allow || policy.AskFallback == Deny}} {
		if !setting.valid {
			return nil, fmt.Errorf("plugins: permissions.%s is invalid", setting.name)
		}
	}
	for index, rule := range policy.Rules {
		if !validAction(rule.Action) {
			return nil, fmt.Errorf("plugins: permissions.rules[%d].action must be allow, deny, or ask", index)
		}
		for _, pattern := range []string{ruleTool(rule), strings.ReplaceAll(rule.Command, "/", "\ue000"), filepath.ToSlash(rule.Path)} {
			if pattern == "" {
				continue
			}
			if _, err := path.Match(pattern, ""); err != nil {
				return nil, fmt.Errorf("plugins: permissions.rules[%d] contains an invalid glob", index)
			}
		}
	}
	return policy, nil
}

func validAction(action Action) bool { return action == Allow || action == Deny || action == Ask }

func (policy *Policy) snapshot() (string, Action, []Rule, func(context.Context, ToolCallInfo) (Action, error), []func(context.Context, ToolCallInfo) string) {
	if policy == nil {
		return "log", Allow, nil, nil, nil
	}
	policy.mu.Lock()
	defer policy.mu.Unlock()
	mode := policy.Mode
	if mode != "enforce" {
		mode = "log"
	}
	fallback := policy.AskFallback
	if fallback != Deny {
		fallback = Allow
	}
	return mode, fallback, append([]Rule(nil), policy.Rules...), policy.Authorizer, append([]func(context.Context, ToolCallInfo) string(nil), policy.Guards...)
}

func (policy *Policy) SetMode(mode string) {
	if mode != "enforce" {
		mode = "log"
	}
	policy.mu.Lock()
	policy.Mode = mode
	policy.mu.Unlock()
}

// Evaluate applies the authorizer, deny-only guards, then last-match-wins rules.
func (policy *Policy) Evaluate(ctx context.Context, info ToolCallInfo) Decision {
	mode, _, rules, authorizer, guards := policy.snapshot()
	input, _ := json.Marshal(info.Args)
	decision := Decision{Time: time.Now().UnixMilli(), Tool: info.Tool, Action: Allow, Resolved: Allow, Mode: mode, Input: string(input)}
	if authorizer != nil {
		action, err := authorizer(ctx, info)
		if err != nil {
			decision.Action, decision.Matcher, decision.Resolution = Ask, "authorizer", err.Error()
			return decision
		}
		if validAction(action) {
			decision.Action, decision.Matcher = action, "authorizer"
			if action != Allow {
				return decision
			}
		}
	}
	for _, guard := range guards {
		if guard == nil {
			continue
		}
		if reason := strings.TrimSpace(guard(ctx, info)); reason != "" {
			decision.Action, decision.Matcher, decision.Resolution = Deny, "guard", reason
			return decision
		}
	}
	if decision.Matcher == "authorizer" {
		return decision
	}
	if info.Tool == "bash" {
		if command, ok := commandArgument(info.Args); !ok || strings.TrimSpace(command) == "" {
			for _, rule := range rules {
				if rule.Action != Allow && validAction(rule.Action) && matchGlob(ruleTool(rule), "bash", false) {
					decision.Action, decision.Matcher, decision.Resolution = Ask, "restrictive bash rule", "unparseable bash command"
					return decision
				}
			}
		}
	}
	for index, rule := range rules {
		if !validAction(rule.Action) || !ruleMatches(rule, info) {
			continue
		}
		decision.Action = rule.Action
		decision.Rule = index + 1
		decision.Matcher = formatRule(rule)
	}
	return decision
}

func ruleTool(rule Rule) string {
	if strings.TrimSpace(rule.Tool) == "" {
		return "*"
	}
	return rule.Tool
}

// ponytail: bash rules match text, so only command rules or the sandbox cover shell effects.
func ruleMatches(rule Rule, info ToolCallInfo) bool {
	if !matchGlob(ruleTool(rule), info.Tool, false) {
		return false
	}
	command, hasCommand := commandArgument(info.Args)
	if rule.Command != "" {
		if info.Tool != "bash" || !hasCommand || !matchGlob(rule.Command, command, true) {
			return false
		}
	}
	if rule.Path != "" {
		candidates := pathArguments(info.Args)
		// Only restrictions scan words; widening a last-match-wins allow could override a deny.
		if info.Tool == "bash" && hasCommand && rule.Action != Allow {
			candidates = append(candidates, commandPaths(command)...)
		}
		for _, candidate := range candidates {
			if matchesPath(rule.Path, candidate, info.CWD) {
				return true
			}
		}
		return false
	}
	return true
}

// commandPaths sees bare words, not quoted paths, variables, or substitutions; use only to restrict.
func commandPaths(command string) []string {
	fields := strings.FieldsFunc(command, func(letter rune) bool {
		return unicode.IsSpace(letter) || strings.ContainsRune("|&;<>()", letter)
	})
	candidates := make([]string, 0, len(fields))
	for _, field := range fields {
		if field = strings.Trim(field, `"'`); field != "" && !strings.HasPrefix(field, "-") {
			candidates = append(candidates, field)
		}
	}
	return candidates
}

func matchGlob(pattern, value string, command bool) bool {
	if command {
		pattern = strings.ReplaceAll(pattern, "/", "\ue000")
		value = strings.ReplaceAll(value, "/", "\ue000")
	}
	matched, err := path.Match(pattern, value)
	return err == nil && matched
}

func commandArgument(raw any) (string, bool) {
	arguments, ok := raw.(map[string]any)
	if !ok {
		return "", false
	}
	command, ok := arguments["command"].(string)
	return command, ok
}

func pathArguments(raw any) []string {
	var result []string
	var walk func(any)
	walk = func(value any) {
		switch typed := value.(type) {
		case map[string]any:
			for key, item := range typed {
				lower := strings.ToLower(key)
				isPath := lower == "path" || lower == "paths" || lower == "file" || lower == "files" || lower == "filename" || lower == "filenames" || strings.HasSuffix(lower, "_path")
				if isPath {
					appendPathValues(&result, item)
				}
				walk(item)
			}
		case []any:
			for _, item := range typed {
				walk(item)
			}
		}
	}
	walk(raw)
	return result
}

func appendPathValues(target *[]string, value any) {
	switch typed := value.(type) {
	case string:
		*target = append(*target, typed)
	case []string:
		*target = append(*target, typed...)
	case []any:
		for _, item := range typed {
			if text, ok := item.(string); ok {
				*target = append(*target, text)
			}
		}
	}
}

func matchesPath(pattern, raw, cwd string) bool {
	if matched, err := path.Match(filepath.ToSlash(pattern), filepath.ToSlash(raw)); err == nil && matched {
		return true
	}
	canonicalPattern := canonicalPath(cwd, pattern)
	canonical := canonicalPath(cwd, raw)
	matched, err := path.Match(filepath.ToSlash(canonicalPattern), filepath.ToSlash(canonical))
	return err == nil && matched
}

func expandHome(value string) string {
	if value != "~" && !strings.HasPrefix(value, "~/") {
		return value
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return value
	}
	if value == "~" {
		return home
	}
	return filepath.Join(home, filepath.FromSlash(strings.TrimPrefix(value, "~/")))
}

func canonicalPath(cwd, raw string) string {
	value := expandHome(raw)
	if !filepath.IsAbs(value) {
		value = filepath.Join(cwd, value)
	}
	value = filepath.Clean(value)
	if resolved, err := filepath.EvalSymlinks(value); err == nil {
		return resolved
	}
	if parent, err := filepath.EvalSymlinks(filepath.Dir(value)); err == nil {
		return filepath.Join(parent, filepath.Base(value))
	}
	return value
}

func formatRule(rule Rule) string {
	parts := make([]string, 0, 3)
	if rule.Tool != "" {
		parts = append(parts, fmt.Sprintf("tool=%q", rule.Tool))
	}
	if rule.Command != "" {
		parts = append(parts, fmt.Sprintf("command=%q", rule.Command))
	}
	if rule.Path != "" {
		parts = append(parts, fmt.Sprintf("path=%q", rule.Path))
	}
	if len(parts) == 0 {
		return "tool=\"*\""
	}
	return strings.Join(parts, ", ")
}

// ponytail: static denies hide tools; the tool_call hook still blocks tools re-added later.
func (policy *Policy) staticDeny(info ToolCallInfo) (Decision, bool) {
	mode, _, rules, authorizer, _ := policy.snapshot()
	if mode != "enforce" || authorizer != nil {
		return Decision{}, false
	}
	candidate := -1
	conditionalAfter := false
	for index, rule := range rules {
		if !validAction(rule.Action) || !matchGlob(ruleTool(rule), info.Tool, false) {
			continue
		}
		if rule.Command != "" || rule.Path != "" {
			if candidate >= 0 {
				conditionalAfter = true
			}
			continue
		}
		candidate = index
		conditionalAfter = false
	}
	if candidate < 0 || conditionalAfter || rules[candidate].Action != Deny {
		return Decision{}, false
	}
	rule := rules[candidate]
	return Decision{
		Time: time.Now().UnixMilli(), Tool: info.Tool, Action: Deny, Resolved: Deny,
		Mode: mode, Rule: candidate + 1, Matcher: formatRule(rule), Resolution: "hidden from active tools",
	}, true
}

func permissionKey(info ToolCallInfo, decision Decision) string {
	return info.SessionID + "\x00" + fmt.Sprint(decision.Rule) + "\x00" + decision.Matcher + "\x00" + decision.Input
}

func (policy *Policy) approvedForSession(info ToolCallInfo, decision Decision) bool {
	policy.mu.Lock()
	defer policy.mu.Unlock()
	_, ok := policy.approved[permissionKey(info, decision)]
	return ok
}

func (policy *Policy) approveForSession(info ToolCallInfo, decision Decision) {
	policy.mu.Lock()
	if policy.approved == nil {
		policy.approved = make(map[string]struct{})
	}
	policy.approved[permissionKey(info, decision)] = struct{}{}
	policy.mu.Unlock()
}

func (policy *Policy) record(decision Decision) {
	policy.mu.Lock()
	policy.decisions = append(policy.decisions, decision)
	// ponytail: memory keeps the last 100; durable session entries keep the full audit trail.
	if len(policy.decisions) > 100 {
		policy.decisions = append([]Decision(nil), policy.decisions[len(policy.decisions)-100:]...)
	}
	policy.mu.Unlock()
}

func (policy *Policy) recent(limit int) []Decision {
	policy.mu.Lock()
	defer policy.mu.Unlock()
	if limit <= 0 || limit > len(policy.decisions) {
		limit = len(policy.decisions)
	}
	return append([]Decision(nil), policy.decisions[len(policy.decisions)-limit:]...)
}

func permissionsExtension(policy *Policy, settings *config.SettingsManager, parent extensions.Context) extensions.Factory {
	return func(api extensions.API) error {
		var hiddenMu sync.Mutex
		hidden := make(map[string]struct{})
		record := func(ctx context.Context, decision Decision) {
			policy.record(decision)
			_ = api.AppendEntry(ctx, "orb.permissions.decision", decision)
		}
		applyMode := func(ctx context.Context, extensionContext extensions.Context) error {
			mode, _, _, _, _ := policy.snapshot()
			hiddenMu.Lock()
			if mode == "log" && len(hidden) == 0 {
				hiddenMu.Unlock()
				return nil
			}
			hiddenMu.Unlock()
			active, err := api.GetActiveTools()
			if err != nil {
				return err
			}
			hiddenMu.Lock()
			defer hiddenMu.Unlock()
			if mode == "log" {
				for name := range hidden {
					active = append(active, name)
				}
				clear(hidden)
				return api.SetActiveTools(uniquePluginNames(active))
			}
			filtered := active[:0]
			for _, name := range active {
				info := ToolCallInfo{Tool: name, CWD: extensionContext.CWD(), SessionID: permissionScope(extensionContext, parent)}
				if decision, denied := policy.staticDeny(info); denied {
					hidden[name] = struct{}{}
					record(ctx, decision)
					continue
				}
				filtered = append(filtered, name)
			}
			return api.SetActiveTools(uniquePluginNames(filtered))
		}

		api.On(extensions.EventSessionStart, func(ctx context.Context, _ extensions.Event, extensionContext extensions.Context) (any, error) {
			return nil, applyMode(ctx, extensionContext)
		})
		api.On(extensions.EventToolCall, func(ctx context.Context, event extensions.Event, extensionContext extensions.Context) (any, error) {
			call := event.(extensions.ToolCallEvent)
			info := ToolCallInfo{Tool: call.ToolName, Args: call.Input, CWD: extensionContext.CWD(), SessionID: permissionScope(extensionContext, parent)}
			decision := policy.Evaluate(ctx, info)
			mode, fallback, _, _, _ := policy.snapshot()
			if mode == "log" {
				decision.Resolved, decision.Resolution = Allow, "would-"+string(decision.Action)
				record(ctx, decision)
				return nil, nil
			}
			switch decision.Action {
			case Allow:
				decision.Resolved = Allow
				record(ctx, decision)
				return nil, nil
			case Deny:
				decision.Resolved = Deny
				record(ctx, decision)
				return extensions.ToolCallResult{Block: true, Reason: permissionDenied(decision, decision.Resolution)}, nil
			}
			if policy.approvedForSession(info, decision) {
				decision.Resolved, decision.Resolution = Allow, "session approval"
				record(ctx, decision)
				return nil, nil
			}
			ui, interactive := permissionUI(extensionContext, parent)
			if !interactive {
				decision.Resolved, decision.Resolution = fallback, "askFallback"
				record(ctx, decision)
				if fallback == Deny {
					return extensions.ToolCallResult{Block: true, Reason: permissionDenied(decision, "ask resolved by askFallback")}, nil
				}
				return nil, nil
			}
			policy.askMu.Lock()
			defer policy.askMu.Unlock()
			if policy.approvedForSession(info, decision) {
				decision.Resolved, decision.Resolution = Allow, "session approval"
				record(ctx, decision)
				return nil, nil
			}
			selected, ok, err := ui.Select(ctx, permissionPrompt(decision), []string{
				"y approve once", "s approve for this session", "n deny", "r deny with a reason",
			}, nil)
			if err != nil || !ok {
				decision.Resolved, decision.Resolution = fallback, "askFallback"
				record(ctx, decision)
				if fallback == Deny {
					return extensions.ToolCallResult{Block: true, Reason: permissionDenied(decision, "ask was cancelled")}, nil
				}
				return nil, nil
			}
			choice := byte('n')
			if selected != "" {
				choice = selected[0]
			}
			switch choice {
			case 'y':
				decision.Resolved, decision.Resolution = Allow, "approved once"
			case 's':
				policy.approveForSession(info, decision)
				decision.Resolved, decision.Resolution = Allow, "session approval"
			case 'r':
				reason, _, _ := ui.Input(ctx, "Why deny this tool call?", nil, nil)
				decision.Resolved, decision.Resolution = Deny, strings.TrimSpace(reason)
			default:
				decision.Resolved, decision.Resolution = Deny, "denied"
			}
			record(ctx, decision)
			if decision.Resolved == Deny {
				return extensions.ToolCallResult{Block: true, Reason: permissionDenied(decision, decision.Resolution)}, nil
			}
			return nil, nil
		})
		api.RegisterCommand("permissions", extensions.Command{
			Description: "Show or toggle the permissions policy",
			Handler: func(ctx context.Context, _ string, command extensions.CommandContext) error {
				if command.Mode() != extensions.ModeTUI || !command.HasUI() {
					return fmt.Errorf("/permissions requires interactive mode")
				}
				mode, _, rules, _, _ := policy.snapshot()
				next := "enforce"
				if mode == "enforce" {
					next = "log"
				}
				selected, ok, err := command.UI().Select(ctx, permissionSummary(mode, rules, policy.recent(10)), []string{"Keep " + mode, "Switch to " + next}, nil)
				if err != nil || !ok || selected != "Switch to "+next {
					return err
				}
				policy.SetMode(next)
				if settings != nil {
					settings.SetPluginSetting("permissions", "mode", next)
					if errors := settings.DrainErrors(); len(errors) > 0 {
						return errors[0]
					}
				}
				return applyMode(ctx, command)
			},
		})
		return nil
	}
}

func permissionScope(current, parent extensions.Context) string {
	if parent != nil && parent.SessionManager() != nil {
		return parent.SessionManager().GetSessionID()
	}
	if current.SessionManager() != nil {
		return current.SessionManager().GetSessionID()
	}
	return ""
}

func permissionUI(current, parent extensions.Context) (extensions.UI, bool) {
	if parent != nil {
		return parent.UI(), parent.Mode() == extensions.ModeTUI && parent.HasUI()
	}
	return current.UI(), current.Mode() == extensions.ModeTUI && current.HasUI()
}

func permissionPrompt(decision Decision) string {
	matched := "default"
	if decision.Rule > 0 {
		matched = fmt.Sprintf("rule %d (%s)", decision.Rule, decision.Matcher)
	} else if decision.Matcher != "" {
		matched = decision.Matcher
	}
	return fmt.Sprintf("Permission requested for %s\nMatched %s", decision.Tool, matched)
}

func permissionDenied(decision Decision, reason string) string {
	matched := decision.Matcher
	if decision.Rule > 0 {
		matched = fmt.Sprintf("rule %d (%s)", decision.Rule, decision.Matcher)
	}
	if matched == "" {
		matched = "default policy"
	}
	if strings.TrimSpace(reason) != "" {
		return fmt.Sprintf("permissions: denied by %s: %s", matched, strings.TrimSpace(reason))
	}
	return "permissions: denied by " + matched
}

func permissionSummary(mode string, rules []Rule, decisions []Decision) string {
	lines := []string{"Permissions mode: " + mode, bashCaveat, "Rules:"}
	if len(rules) == 0 {
		lines = append(lines, "  (default allow)")
	}
	for index, rule := range rules {
		lines = append(lines, fmt.Sprintf("  %d. %s -> %s", index+1, formatRule(rule), rule.Action))
	}
	lines = append(lines, "Recent decisions:")
	if len(decisions) == 0 {
		lines = append(lines, "  (none)")
	}
	for _, decision := range decisions {
		lines = append(lines, fmt.Sprintf("  %s: %s -> %s (%s)", decision.Tool, decision.Action, decision.Resolved, decision.Resolution))
	}
	return strings.Join(lines, "\n")
}

func uniquePluginNames(names []string) []string {
	seen := make(map[string]struct{}, len(names))
	result := names[:0]
	for _, name := range names {
		if _, exists := seen[name]; exists {
			continue
		}
		seen[name] = struct{}{}
		result = append(result, name)
	}
	return result
}
