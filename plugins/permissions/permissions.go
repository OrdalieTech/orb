package permissions

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/bmatcuk/doublestar/v4"

	"github.com/OrdalieTech/orb/agent/config"
	"github.com/OrdalieTech/orb/agent/extensions"
	"github.com/OrdalieTech/orb/sandbox"
)

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
// Configure fields before attaching; SetMode is the only concurrent mutation API.
type Policy struct {
	// Sandbox is selected by native hosts; SDK callers attach native.ToolOptions
	// explicitly or supply their own contained tool operations.
	Sandbox     sandbox.Mode `json:"sandbox,omitempty"`
	Mode        string       `json:"mode,omitempty"`
	AskFallback Action       `json:"askFallback,omitempty"`
	Rules       []Rule       `json:"rules,omitempty"`

	Authorizer func(context.Context, ToolCallInfo) (Action, error) `json:"-"`
	// Guards are hard deny-only constraints: any non-empty reason denies,
	// even in log mode.
	Guards []func(context.Context, ToolCallInfo) string `json:"-"`

	mu        sync.Mutex
	ask       chan struct{}
	approved  map[string]struct{}
	decisions []Decision
}

// SandboxMode is a host constraint, independent of extension enablement.
func SandboxMode(settings *config.SettingsManager) (sandbox.Mode, error) {
	if settings == nil {
		return sandbox.ModeDangerFullAccess, nil
	}
	policy, err := FromSettings(settings.GetPluginSettings("permissions"))
	if err != nil {
		return "", err
	}
	if policy.Sandbox == "" {
		return sandbox.ModeDangerFullAccess, nil
	}
	return policy.Sandbox, nil
}

func FromSettings(value map[string]any) (*Policy, error) {
	policy := &Policy{Mode: "enforce"}
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
	if mode != "log" {
		mode = "enforce"
	}
	fallback := policy.AskFallback
	if fallback != Allow {
		fallback = Deny
	}
	return mode, fallback, append([]Rule(nil), policy.Rules...), policy.Authorizer, append([]func(context.Context, ToolCallInfo) string(nil), policy.Guards...)
}

func (policy *Policy) SetMode(mode string) {
	if mode != "log" {
		mode = "enforce"
	}
	policy.mu.Lock()
	policy.Mode = mode
	clear(policy.approved)
	policy.mu.Unlock()
}

// Evaluate applies the authorizer, deny-only guards, then last-match-wins rules.
func (policy *Policy) Evaluate(ctx context.Context, info ToolCallInfo) Decision {
	mode, _, rules, authorizer, guards := policy.snapshot()
	input, _ := json.Marshal(info.Args)
	decision := Decision{Time: time.Now().UnixMilli(), Tool: info.Tool, Action: Allow, Resolved: Allow, Mode: mode, Input: string(input)}
	if authorizer != nil {
		action, err := authorizer(ctx, info)
		if err != nil || !validAction(action) {
			decision.Action, decision.Matcher, decision.Resolution = Deny, "guard", "authorizer failed"
			if err != nil {
				decision.Resolution = err.Error()
			}
			return decision
		}
		decision.Action, decision.Matcher = action, "authorizer"
		if action == Deny && mode != "log" {
			return decision
		}
	}
	for _, guard := range guards {
		if guard == nil {
			continue
		}
		if reason := guard(ctx, info); reason != "" {
			if reason = strings.TrimSpace(reason); reason == "" {
				reason = "denied by guard"
			}
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
	if info.Tool == "bash" && decision.Action == Allow {
		command, _ := commandArgument(info.Args)
		if strings.ContainsAny(command, ";&|<>$`\\\"'()\n\r") {
			scoped := false
			for _, rule := range rules {
				if matchGlob(ruleTool(rule), "bash", false) && (rule.Command != "" || rule.Path != "") {
					scoped = true
				}
			}
			if decision.Rule > 0 {
				rule := rules[decision.Rule-1]
				if rule.Path == "" && (rule.Command == "" || rule.Command == command) {
					scoped = false
				}
			}
			if scoped {
				decision.Action, decision.Matcher, decision.Resolution = Ask, "shell syntax", "command requires approval because scoped rules cannot resolve shell syntax"
			}
		}
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
		matched := false
		for _, candidate := range candidates {
			hit := matchesPath(rule.Path, candidate, info.CWD, rule.Action == Allow)
			if rule.Action == Allow && !hit {
				return false
			}
			matched = matched || hit
		}
		return matched
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

func matchesPath(pattern, raw, cwd string, allow bool) bool {
	if matched, err := doublestar.Match(filepath.ToSlash(pattern), filepath.ToSlash(raw)); err == nil && matched && !allow {
		return true
	}
	canonicalPattern := canonicalPath(cwd, pattern)
	canonical := canonicalPath(cwd, raw)
	matched, err := doublestar.Match(filepath.ToSlash(canonicalPattern), filepath.ToSlash(canonical))
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
	for ancestor := value; ; ancestor = filepath.Dir(ancestor) {
		if resolved, err := filepath.EvalSymlinks(ancestor); err == nil {
			suffix, _ := filepath.Rel(ancestor, value)
			return filepath.Join(resolved, suffix)
		}
		if filepath.Dir(ancestor) == ancestor {
			break
		}
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
	paths := pathArguments(info.Args)
	for i := range paths {
		paths[i] = canonicalPath(info.CWD, paths[i])
	}
	// Sorting makes map traversal irrelevant to the consent scope.
	slices.Sort(paths)
	scope, _ := json.Marshal([]any{info.SessionID, info.Tool, canonicalPath(info.CWD, "."), paths, decision.Rule, decision.Matcher, decision.Input})
	return fmt.Sprintf("%x", sha256.Sum256(scope))
}

func (policy *Policy) approvedForSession(info ToolCallInfo, decision Decision) bool {
	if info.SessionID == "" {
		return false
	}
	policy.mu.Lock()
	defer policy.mu.Unlock()
	_, ok := policy.approved[permissionKey(info, decision)]
	return ok
}

func (policy *Policy) approveForSession(info ToolCallInfo, decision Decision) {
	if info.SessionID == "" {
		return
	}
	policy.mu.Lock()
	if policy.approved == nil {
		policy.approved = make(map[string]struct{})
	}
	if len(policy.approved) >= 1024 {
		clear(policy.approved)
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

// Extension requires an explicit policy; child agents may share their parent policy.
func Extension(policy *Policy, settings *config.SettingsManager, parent extensions.Context) extensions.Factory {
	return func(api extensions.API) error {
		if policy == nil {
			return fmt.Errorf("permissions: policy is required")
		}
		var hiddenMu sync.Mutex
		hidden := make(map[string]struct{})
		record := func(ctx context.Context, decision Decision) {
			// Tool arguments already belong to the transcript; do not duplicate
			// file bodies or credentials into the permission audit.
			decision.Input = ""
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
			if err := ctx.Err(); err != nil {
				return extensions.ToolCallResult{Block: true, Reason: err.Error()}, nil
			}
			call := event.(extensions.ToolCallEvent)
			info := ToolCallInfo{Tool: call.ToolName, Args: call.Input, CWD: extensionContext.CWD(), SessionID: permissionScope(extensionContext, parent)}
			decision := policy.Evaluate(ctx, info)
			mode, fallback, _, _, _ := policy.snapshot()
			// Guard denials are invariants, not approval-policy outcomes:
			// log mode never downgrades them (this also keeps the deny-all
			// guard for invalid settings unescapable via /permissions).
			if mode == "log" && decision.Matcher != "guard" {
				decision.Resolved, decision.Resolution = Allow, "would-"+string(decision.Action)
				record(ctx, decision)
				return nil, nil
			}
			switch decision.Action {
			case Allow:
				decision.Resolved = Allow
				record(ctx, decision)
				if decision.Rule == 0 && decision.Matcher == "" {
					return nil, nil
				}
				return extensions.ToolCallResult{Approved: true}, nil
			case Deny:
				decision.Resolved = Deny
				record(ctx, decision)
				return extensions.ToolCallResult{Block: true, Reason: permissionDenied(decision, decision.Resolution)}, nil
			}
			if policy.approvedForSession(info, decision) {
				decision.Resolved, decision.Resolution = Allow, "session approval"
				record(ctx, decision)
				return extensions.ToolCallResult{Approved: true}, nil
			}
			ui, interactive := permissionUI(extensionContext, parent)
			request := extensions.InputHandlerFromContext(ctx)
			if !interactive && request == nil {
				decision.Resolved, decision.Resolution = fallback, "askFallback"
				record(ctx, decision)
				if fallback == Deny {
					return extensions.ToolCallResult{Block: true, Reason: permissionDenied(decision, "ask resolved by askFallback")}, nil
				}
				return extensions.ToolCallResult{Approved: true}, nil
			}
			release, err := policy.acquirePrompt(ctx)
			if err != nil {
				return extensions.ToolCallResult{Block: true, Reason: err.Error()}, nil
			}
			defer release()
			if policy.approvedForSession(info, decision) {
				decision.Resolved, decision.Resolution = Allow, "session approval"
				record(ctx, decision)
				return extensions.ToolCallResult{Approved: true}, nil
			}
			if request == nil {
				request = func(ctx context.Context, title string, choices []string) (string, error) {
					var value string
					var ok bool
					var err error
					if len(choices) == 0 {
						value, ok, err = ui.Input(ctx, title, nil, nil)
					} else {
						value, ok, err = ui.Select(ctx, title, choices, nil)
					}
					if err == nil && !ok {
						err = context.Canceled
					}
					return value, err
				}
			}
			selected, err := request(ctx, permissionPrompt(info, decision), []string{
				"y approve once", "s approve for this session", "n deny", "r deny with a reason",
			})
			if err != nil || ctx.Err() != nil {
				decision.Resolved, decision.Resolution = Deny, "approval cancelled or unavailable"
				record(ctx, decision)
				return extensions.ToolCallResult{Block: true, Reason: permissionDenied(decision, decision.Resolution)}, nil
			}
			switch selected {
			case "y approve once":
				decision.Resolved, decision.Resolution = Allow, "approved once"
			case "s approve for this session":
				policy.approveForSession(info, decision)
				decision.Resolved, decision.Resolution = Allow, "session approval"
			case "r deny with a reason":
				reason, _ := request(ctx, "Why deny this tool call?", nil)
				decision.Resolved, decision.Resolution = Deny, strings.TrimSpace(reason)
			default:
				decision.Resolved, decision.Resolution = Deny, "denied"
			}
			record(ctx, decision)
			if decision.Resolved == Deny {
				return extensions.ToolCallResult{Block: true, Reason: permissionDenied(decision, decision.Resolution)}, nil
			}
			return extensions.ToolCallResult{Approved: true}, nil
		})
		api.RegisterCommand("permissions", extensions.Command{
			Description: "Show or toggle the permissions policy",
			Handler: func(ctx context.Context, _ string, command extensions.CommandContext) error {
				if command.Mode() != extensions.ModeTUI || !command.HasUI() {
					return fmt.Errorf("/permissions requires interactive mode")
				}
				before, _, _, _, _ := policy.snapshot()
				if err := permissionsWindow(ctx, command, policy, settings); err != nil {
					return err
				}
				if after, _, _, _, _ := policy.snapshot(); after != before {
					return applyMode(ctx, command)
				}
				return nil
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

func permissionPrompt(info ToolCallInfo, decision Decision) string {
	matched := "default"
	if decision.Rule > 0 {
		matched = fmt.Sprintf("rule %d (%s)", decision.Rule, decision.Matcher)
	} else if decision.Matcher != "" {
		matched = decision.Matcher
	}
	return fmt.Sprintf("Permission requested for %s\nWorking directory: %s\nArguments: %s\nMatched %s", decision.Tool, info.CWD, decision.Input, matched)
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

func (policy *Policy) acquirePrompt(ctx context.Context) (func(), error) {
	policy.mu.Lock()
	if policy.ask == nil {
		policy.ask = make(chan struct{}, 1)
	}
	gate := policy.ask
	policy.mu.Unlock()
	select {
	case gate <- struct{}{}:
		if err := ctx.Err(); err != nil {
			<-gate
			return nil, err
		}
		return func() { <-gate }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}
