package host

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"sync"

	"github.com/OrdalieTech/pigo/agent"
	"github.com/OrdalieTech/pigo/ai"
	"github.com/OrdalieTech/pigo/codingagent/extensions"
	"github.com/OrdalieTech/pigo/codingagent/session"
	"github.com/OrdalieTech/pigo/codingagent/tools"
)

type stateHost struct {
	mu sync.RWMutex

	base           stateSnapshot
	registrations  map[string]*stateRegistrations
	apis           map[string]extensions.API
	snapshots      map[string]stateSnapshot
	contexts       map[string][]extensions.Context
	lastContexts   map[string]extensions.Context
	busUnsubscribe map[string]map[string]func()
	execCancels    map[string]context.CancelFunc
	execCancelled  map[string]bool
	environment    []string
	nextSignalID   uint64
	nextTransferID uint64

	sessionCacheKey      string
	sessionCacheRevision uint64
	sessionCache         *stateSessionSnapshot
	sentSessionRevisions map[string]bool
}

type stateRegistrations struct {
	flags map[string]wireFlag
	bus   map[string]wireBusSubscription
}

type wireFlag struct {
	Name        string              `json:"name"`
	Description string              `json:"description,omitempty"`
	Type        extensions.FlagType `json:"type"`
	Default     json.RawMessage     `json:"default,omitempty"`
}

type wireBusSubscription struct {
	ID      string `json:"subscriptionId"`
	Channel string `json:"channel"`
}

type stateSnapshot struct {
	Flags           map[string]any                `json:"flags"`
	SessionName     *string                       `json:"sessionName"`
	ActiveTools     []string                      `json:"activeTools"`
	AllTools        []extensions.ToolInfo         `json:"allTools"`
	Commands        []extensions.SlashCommandInfo `json:"commands"`
	ThinkingLevel   agent.ThinkingLevel           `json:"thinkingLevel"`
	Context         stateContextSnapshot          `json:"context"`
	Session         *stateSessionSnapshot         `json:"session"`
	SessionRevision string                        `json:"sessionRevision,omitempty"`
	Models          *stateModelRegistrySnapshot   `json:"modelRegistry"`
}

type stateContextSnapshot struct {
	CWD                string             `json:"cwd"`
	Mode               extensions.Mode    `json:"mode"`
	HasUI              bool               `json:"hasUI"`
	Model              *ai.Model          `json:"model"`
	Idle               bool               `json:"idle"`
	ProjectTrusted     bool               `json:"projectTrusted"`
	HasPendingMessages bool               `json:"hasPendingMessages"`
	ContextUsage       *stateContextUsage `json:"contextUsage"`
	SystemPrompt       string             `json:"systemPrompt"`
}

type stateContextUsage struct {
	Tokens        *int64   `json:"tokens"`
	ContextWindow int64    `json:"contextWindow"`
	Percent       *float64 `json:"percent"`
}

type wireSignal struct {
	ID      string `json:"id"`
	Aborted bool   `json:"aborted"`
	Reason  string `json:"reason,omitempty"`
}

type stateSessionSnapshot struct {
	Persisted   bool                   `json:"persisted"`
	CWD         string                 `json:"cwd"`
	SessionDir  string                 `json:"sessionDir"`
	SessionID   string                 `json:"sessionId"`
	SessionFile *string                `json:"sessionFile"`
	LeafID      *string                `json:"leafId"`
	Entries     []session.SessionEntry `json:"entries"`
	Header      *session.SessionHeader `json:"header"`
	SessionName *string                `json:"sessionName"`
}

type stateModelRegistrySnapshot struct {
	Error                 string                           `json:"error"`
	All                   []ai.Model                       `json:"all"`
	Available             []ai.Model                       `json:"available"`
	RegisteredProviderIDs []string                         `json:"registeredProviderIds"`
	Providers             map[string]stateProviderSnapshot `json:"providers"`
}

type stateProviderSnapshot struct {
	ID               string                `json:"id"`
	Name             string                `json:"name"`
	BaseURL          string                `json:"baseUrl,omitempty"`
	Headers          map[string]string     `json:"headers,omitempty"`
	DisplayName      string                `json:"displayName"`
	AuthStatus       extensions.AuthStatus `json:"authStatus"`
	UsingOAuth       bool                  `json:"usingOAuth"`
	RegisteredConfig bool                  `json:"registeredConfig"`
	RegisteredNative bool                  `json:"registeredNative"`
}

type stateBusEnvelope struct {
	Source string `json:"source"`
	Data   any    `json:"data"`
}

func newStateHost(options Options) *stateHost {
	base := stateSnapshot{
		Flags:         map[string]any{},
		ActiveTools:   []string{},
		AllTools:      []extensions.ToolInfo{},
		Commands:      []extensions.SlashCommandInfo{},
		ThinkingLevel: agent.ThinkingOff,
		Context: stateContextSnapshot{
			CWD: options.CWD, Mode: extensions.ModePrint, Idle: true, ProjectTrusted: true,
		},
	}
	return &stateHost{
		base: base, registrations: make(map[string]*stateRegistrations), apis: make(map[string]extensions.API),
		snapshots: make(map[string]stateSnapshot), contexts: make(map[string][]extensions.Context),
		lastContexts:         make(map[string]extensions.Context),
		busUnsubscribe:       make(map[string]map[string]func()),
		execCancels:          make(map[string]context.CancelFunc),
		execCancelled:        make(map[string]bool),
		sentSessionRevisions: make(map[string]bool),
	}
}

func (host *stateHost) handshakeSnapshot() stateSnapshot {
	host.mu.RLock()
	defer host.mu.RUnlock()
	return cloneStateSnapshot(host.base)
}

func (host *stateHost) setEnvironment(environment []string) {
	host.mu.Lock()
	host.environment = append([]string(nil), environment...)
	host.mu.Unlock()
}

func (host *stateHost) reset(entries []extensionEntry) {
	host.mu.Lock()
	for _, subscriptions := range host.busUnsubscribe {
		for _, unsubscribe := range subscriptions {
			unsubscribe()
		}
	}
	for _, cancel := range host.execCancels {
		cancel()
	}
	host.registrations = make(map[string]*stateRegistrations, len(entries))
	host.busUnsubscribe = make(map[string]map[string]func())
	host.execCancels = make(map[string]context.CancelFunc)
	host.execCancelled = make(map[string]bool)
	host.contexts = make(map[string][]extensions.Context)
	host.lastContexts = make(map[string]extensions.Context)
	host.sentSessionRevisions = make(map[string]bool)
	for _, entry := range entries {
		host.registrations[entry.ID] = &stateRegistrations{flags: make(map[string]wireFlag), bus: make(map[string]wireBusSubscription)}
		if _, exists := host.snapshots[entry.ID]; !exists {
			host.snapshots[entry.ID] = cloneStateSnapshot(host.base)
		}
	}
	host.mu.Unlock()
}

func (host *stateHost) close() {
	host.mu.Lock()
	for _, subscriptions := range host.busUnsubscribe {
		for _, unsubscribe := range subscriptions {
			unsubscribe()
		}
	}
	for _, cancel := range host.execCancels {
		cancel()
	}
	host.busUnsubscribe = make(map[string]map[string]func())
	host.execCancels = make(map[string]context.CancelFunc)
	host.execCancelled = make(map[string]bool)
	host.contexts = make(map[string][]extensions.Context)
	host.lastContexts = make(map[string]extensions.Context)
	host.mu.Unlock()
}

func (host *stateHost) bind(manager *Manager, extensionID string, api extensions.API) error {
	host.mu.Lock()
	host.apis[extensionID] = api
	registration := cloneStateRegistrations(host.registrations[extensionID])
	host.mu.Unlock()
	if registration == nil {
		return fmt.Errorf("extension host: unknown state extension %s", extensionID)
	}
	for _, flag := range sortedFlags(registration.flags) {
		var defaultValue any
		if len(flag.Default) != 0 {
			if err := json.Unmarshal(flag.Default, &defaultValue); err != nil {
				return fmt.Errorf("extension host: decode flag %s default: %w", flag.Name, err)
			}
		}
		if err := callStateAPI(func() {
			api.RegisterFlag(flag.Name, extensions.Flag{Description: flag.Description, Type: flag.Type, Default: defaultValue})
		}); err != nil {
			return err
		}
	}
	for _, subscription := range sortedBusSubscriptions(registration.bus) {
		if err := host.bindBus(manager, extensionID, api, subscription); err != nil {
			return err
		}
	}
	host.refreshCurrent(manager, extensionID, nil)
	return nil
}

func (host *stateHost) rebindCaptured(manager *Manager) {
	host.mu.RLock()
	ids := make([]string, 0, len(host.apis))
	for id := range host.apis {
		ids = append(ids, id)
	}
	apis := make(map[string]extensions.API, len(host.apis))
	for id, api := range host.apis {
		apis[id] = api
	}
	host.mu.RUnlock()
	sort.Strings(ids)
	for _, id := range ids {
		if err := host.bind(manager, id, apis[id]); err != nil {
			manager.report(extensions.Diagnostic{Type: "error", Message: err.Error(), Path: id})
		}
	}
}

func (host *stateHost) bindBus(manager *Manager, extensionID string, api extensions.API, subscription wireBusSubscription) error {
	var bus extensions.EventBus
	if err := callStateAPI(func() { bus = api.Events() }); err != nil {
		return err
	}
	if bus == nil {
		return errors.New("extension host: extension event bus is unavailable")
	}
	unsubscribe := bus.On(subscription.Channel, func(ctx context.Context, data any) error {
		if envelope, ok := data.(stateBusEnvelope); ok {
			if envelope.Source == extensionID {
				return nil
			}
			data = envelope.Data
		}
		_, err := manager.request(ctx, "event_bus_dispatch", struct {
			ExtensionID    string `json:"extensionId"`
			SubscriptionID string `json:"subscriptionId"`
			Channel        string `json:"channel"`
			Data           any    `json:"data"`
		}{extensionID, subscription.ID, subscription.Channel, data}, nil)
		return err
	})
	host.mu.Lock()
	if host.busUnsubscribe[extensionID] == nil {
		host.busUnsubscribe[extensionID] = make(map[string]func())
	}
	if previous := host.busUnsubscribe[extensionID][subscription.ID]; previous != nil {
		previous()
	}
	host.busUnsubscribe[extensionID][subscription.ID] = unsubscribe
	host.mu.Unlock()
	return nil
}

func (host *stateHost) beforeCallback(manager *Manager, extensionID string, contextValue extensions.Context) func() {
	host.refreshCurrent(manager, extensionID, contextValue)
	if contextValue == nil {
		return func() {}
	}
	host.mu.Lock()
	host.contexts[extensionID] = append(host.contexts[extensionID], contextValue)
	host.lastContexts[extensionID] = contextValue
	host.mu.Unlock()
	return func() {
		host.mu.Lock()
		contexts := host.contexts[extensionID]
		for index := len(contexts) - 1; index >= 0; index-- {
			if contexts[index] == contextValue {
				contexts = append(contexts[:index], contexts[index+1:]...)
				break
			}
		}
		if len(contexts) == 0 {
			delete(host.contexts, extensionID)
		} else {
			host.contexts[extensionID] = contexts
		}
		host.mu.Unlock()
	}
}

func (host *stateHost) currentContext(extensionID string) extensions.Context {
	host.mu.RLock()
	defer host.mu.RUnlock()
	contexts := host.contexts[extensionID]
	if len(contexts) == 0 {
		return host.lastContexts[extensionID]
	}
	return contexts[len(contexts)-1]
}

func (host *stateHost) bindContextSignal(generation *generation, extensionID string, signal context.Context, target *wireContext) func() {
	if generation == nil || signal == nil || target == nil {
		return func() {}
	}
	host.mu.Lock()
	host.nextSignalID++
	signalID := fmt.Sprintf("%s-signal-%d", extensionID, host.nextSignalID)
	host.mu.Unlock()
	target.Signal = &wireSignal{ID: signalID, Aborted: signal.Err() != nil, Reason: contextSignalReason(signal)}

	released := make(chan struct{})
	var releaseOnce sync.Once
	push := func(method string, reason string) {
		value, err := eventFrame(method, struct {
			ExtensionID string `json:"extensionId"`
			SignalID    string `json:"signalId"`
			Reason      string `json:"reason,omitempty"`
		}{ExtensionID: extensionID, SignalID: signalID, Reason: reason})
		if err != nil {
			generation.manager.report(extensions.Diagnostic{Type: "error", Message: err.Error(), Path: extensionID})
			return
		}
		if err := generation.codec.write(value); err != nil {
			generation.manager.report(extensions.Diagnostic{Type: "error", Message: err.Error(), Path: extensionID})
		}
	}
	if signal.Done() != nil && signal.Err() == nil {
		go func() {
			select {
			case <-signal.Done():
				push("state_signal_abort", contextSignalReason(signal))
			case <-released:
			}
		}()
	}
	return func() {
		releaseOnce.Do(func() {
			close(released)
			push("state_signal_release", "")
		})
	}
}

func callbackSignal(event extensions.Event, contextValue extensions.Context) context.Context {
	switch typed := event.(type) {
	case extensions.SessionBeforeCompactEvent:
		return typed.Signal
	case extensions.SessionBeforeTreeEvent:
		return typed.Signal
	}
	if contextValue == nil {
		return nil
	}
	var signal context.Context
	if callStateAPI(func() { signal = contextValue.Signal() }) != nil {
		return nil
	}
	return signal
}

func contextSignalReason(signal context.Context) string {
	if signal == nil {
		return ""
	}
	if cause := context.Cause(signal); cause != nil {
		return cause.Error()
	}
	if err := signal.Err(); err != nil {
		return err.Error()
	}
	return ""
}

func (host *stateHost) handleRequest(manager *Manager, generation *generation, value frame) (any, *protocolError, bool) {
	switch value.Method {
	case "register_flag":
		var params struct {
			ExtensionID string   `json:"extensionId"`
			Definition  wireFlag `json:"definition"`
		}
		if err := json.Unmarshal(value.Params, &params); err != nil {
			return nil, invalidRegistration(err), true
		}
		if params.Definition.Name == "" || params.Definition.Type != extensions.FlagBoolean && params.Definition.Type != extensions.FlagString {
			return nil, invalidRegistration(errors.New("flag requires a name and boolean or string type")), true
		}
		host.mu.Lock()
		registration := host.registrations[params.ExtensionID]
		if registration != nil {
			registration.flags[params.Definition.Name] = params.Definition
		}
		host.mu.Unlock()
		if registration == nil {
			return nil, invalidRegistration(errors.New("unknown extension id")), true
		}
		if api := host.api(params.ExtensionID); api != nil {
			var defaultValue any
			if len(params.Definition.Default) != 0 {
				if err := json.Unmarshal(params.Definition.Default, &defaultValue); err != nil {
					return nil, invalidRegistration(err), true
				}
			}
			if err := callStateAPI(func() {
				api.RegisterFlag(params.Definition.Name, extensions.Flag{
					Description: params.Definition.Description, Type: params.Definition.Type, Default: defaultValue,
				})
			}); err != nil {
				return nil, invalidRegistration(err), true
			}
		}
		return map[string]bool{"accepted": true}, nil, true
	case "event_bus_subscribe":
		var params struct {
			ExtensionID    string `json:"extensionId"`
			SubscriptionID string `json:"subscriptionId"`
			Channel        string `json:"channel"`
		}
		if err := json.Unmarshal(value.Params, &params); err != nil || params.SubscriptionID == "" || params.Channel == "" {
			if err == nil {
				err = errors.New("event bus subscription requires id and channel")
			}
			return nil, invalidRegistration(err), true
		}
		host.mu.Lock()
		registration := host.registrations[params.ExtensionID]
		if registration != nil {
			registration.bus[params.SubscriptionID] = wireBusSubscription{ID: params.SubscriptionID, Channel: params.Channel}
		}
		host.mu.Unlock()
		if registration == nil {
			return nil, invalidRegistration(errors.New("unknown extension id")), true
		}
		if api := host.api(params.ExtensionID); api != nil {
			if err := host.bindBus(manager, params.ExtensionID, api, wireBusSubscription{ID: params.SubscriptionID, Channel: params.Channel}); err != nil {
				return nil, invalidRegistration(err), true
			}
		}
		return map[string]bool{"accepted": true}, nil, true
	case "event_bus_unsubscribe":
		var params struct {
			ExtensionID    string `json:"extensionId"`
			SubscriptionID string `json:"subscriptionId"`
		}
		if err := json.Unmarshal(value.Params, &params); err != nil {
			return nil, &protocolError{Code: "invalid_state_action", Message: err.Error()}, true
		}
		host.mu.Lock()
		if registration := host.registrations[params.ExtensionID]; registration != nil {
			delete(registration.bus, params.SubscriptionID)
		}
		unsubscribe := host.busUnsubscribe[params.ExtensionID][params.SubscriptionID]
		delete(host.busUnsubscribe[params.ExtensionID], params.SubscriptionID)
		host.mu.Unlock()
		if unsubscribe != nil {
			unsubscribe()
		}
		return map[string]bool{"accepted": true}, nil, true
	case "event_bus_emit":
		var params struct {
			ExtensionID string          `json:"extensionId"`
			Channel     string          `json:"channel"`
			Data        json.RawMessage `json:"data"`
		}
		if err := json.Unmarshal(value.Params, &params); err != nil || params.Channel == "" {
			if err == nil {
				err = errors.New("event bus emit requires a channel")
			}
			return nil, &protocolError{Code: "invalid_state_action", Message: err.Error()}, true
		}
		api := host.api(params.ExtensionID)
		if api == nil {
			return nil, &protocolError{Code: "extension_error", Message: "extension API is not bound"}, true
		}
		var data any
		if len(params.Data) != 0 {
			if err := json.Unmarshal(params.Data, &data); err != nil {
				return nil, &protocolError{Code: "invalid_state_action", Message: err.Error()}, true
			}
		}
		var bus extensions.EventBus
		if err := callStateAPI(func() { bus = api.Events() }); err != nil {
			return nil, &protocolError{Code: "extension_error", Message: err.Error()}, true
		}
		errorsSeen := bus.Emit(context.Background(), params.Channel, stateBusEnvelope{Source: params.ExtensionID, Data: data})
		if len(errorsSeen) > 0 {
			return nil, &protocolError{Code: "extension_error", Message: errorsSeen[0].Error()}, true
		}
		return map[string]bool{"emitted": true}, nil, true
	case "state_exec_cancel":
		var params struct {
			ExtensionID string `json:"extensionId"`
			OperationID string `json:"operationId"`
		}
		if err := json.Unmarshal(value.Params, &params); err != nil || params.ExtensionID == "" || params.OperationID == "" {
			if err == nil {
				err = errors.New("exec cancellation requires extension and operation ids")
			}
			return nil, &protocolError{Code: "invalid_state_action", Message: err.Error()}, true
		}
		key := stateExecKey(params.ExtensionID, params.OperationID)
		host.mu.Lock()
		cancel := host.execCancels[key]
		if cancel == nil {
			host.execCancelled[key] = true
		}
		host.mu.Unlock()
		if cancel != nil {
			cancel()
		}
		return map[string]bool{"cancelled": true}, nil, true
	case "state_action":
		result, err := host.runAction(manager, generation, value.Params)
		if err != nil {
			return nil, &protocolError{Code: "extension_error", Message: err.Error()}, true
		}
		return result, nil, true
	default:
		return nil, nil, false
	}
}

func (*stateHost) asyncRequest(method string) bool {
	switch method {
	case "state_action", "event_bus_emit", "event_bus_unsubscribe":
		return true
	default:
		return false
	}
}

func stateExecKey(extensionID, operationID string) string {
	return extensionID + "\x00" + operationID
}

func (host *stateHost) runAction(manager *Manager, generation *generation, raw json.RawMessage) (result any, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			result = nil
			err = fmt.Errorf("extension host state: %v", recovered)
		}
	}()
	var request struct {
		ExtensionID string          `json:"extensionId"`
		Action      string          `json:"action"`
		Args        json.RawMessage `json:"args"`
	}
	if err := json.Unmarshal(raw, &request); err != nil {
		return nil, err
	}
	api := host.api(request.ExtensionID)
	if api == nil {
		return nil, errors.New("extension API is not bound")
	}
	ctx, cancel := manager.timeoutContext(context.Background())
	defer cancel()
	result = map[string]bool{"accepted": true}
	switch request.Action {
	case "send_message":
		var args struct {
			Message extensions.CustomMessage       `json:"message"`
			Options *extensions.SendMessageOptions `json:"options"`
		}
		if err := json.Unmarshal(request.Args, &args); err != nil {
			return nil, err
		}
		if err := api.SendMessage(ctx, args.Message, args.Options); err != nil {
			return nil, err
		}
	case "send_user_message":
		var args struct {
			Content json.RawMessage                    `json:"content"`
			Options *extensions.SendUserMessageOptions `json:"options"`
		}
		if err := json.Unmarshal(request.Args, &args); err != nil {
			return nil, err
		}
		var content ai.UserContent
		if err := json.Unmarshal(args.Content, &content); err != nil {
			return nil, err
		}
		if err := api.SendUserMessage(ctx, content, args.Options); err != nil {
			return nil, err
		}
	case "append_entry":
		var args struct {
			CustomType string          `json:"customType"`
			Data       json.RawMessage `json:"data"`
		}
		if err := json.Unmarshal(request.Args, &args); err != nil {
			return nil, err
		}
		var data any
		if len(args.Data) != 0 {
			if err := json.Unmarshal(args.Data, &data); err != nil {
				return nil, err
			}
		}
		if err := api.AppendEntry(ctx, args.CustomType, data); err != nil {
			return nil, err
		}
	case "set_session_name":
		var args struct {
			Name string `json:"name"`
		}
		if err := json.Unmarshal(request.Args, &args); err != nil {
			return nil, err
		}
		if err := api.SetSessionName(ctx, args.Name); err != nil {
			return nil, err
		}
	case "set_label":
		var args struct {
			EntryID string  `json:"entryId"`
			Label   *string `json:"label"`
		}
		if err := json.Unmarshal(request.Args, &args); err != nil {
			return nil, err
		}
		if err := api.SetLabel(ctx, args.EntryID, args.Label); err != nil {
			return nil, err
		}
	case "exec":
		var args struct {
			Command     string                  `json:"command"`
			Args        []string                `json:"args"`
			Options     *extensions.ExecOptions `json:"options"`
			OperationID string                  `json:"operationId"`
		}
		if err := json.Unmarshal(request.Args, &args); err != nil {
			return nil, err
		}
		if args.Options == nil {
			args.Options = &extensions.ExecOptions{}
		}
		host.mu.RLock()
		args.Options.Env = append([]string(nil), host.environment...)
		host.mu.RUnlock()
		if resolved, resolveErr := lookPathInEnvironment(args.Command, args.Options.Env); resolveErr == nil {
			args.Command = resolved
		}
		execContext := ctx
		if args.OperationID != "" {
			var cancel context.CancelFunc
			execContext, cancel = context.WithCancel(ctx)
			key := stateExecKey(request.ExtensionID, args.OperationID)
			host.mu.Lock()
			host.execCancels[key] = cancel
			cancelled := host.execCancelled[key]
			delete(host.execCancelled, key)
			host.mu.Unlock()
			if cancelled {
				cancel()
			}
			defer func() {
				host.mu.Lock()
				delete(host.execCancels, key)
				delete(host.execCancelled, key)
				host.mu.Unlock()
				cancel()
			}()
		}
		value, err := api.Exec(execContext, args.Command, args.Args, args.Options)
		if err != nil {
			return nil, err
		}
		result = value
	case "set_active_tools":
		var args struct {
			Names []string `json:"names"`
		}
		if err := json.Unmarshal(request.Args, &args); err != nil {
			return nil, err
		}
		if err := api.SetActiveTools(args.Names); err != nil {
			return nil, err
		}
	case "set_model":
		var args struct {
			Model ai.Model `json:"model"`
		}
		if err := json.Unmarshal(request.Args, &args); err != nil {
			return nil, err
		}
		selected, err := api.SetModel(ctx, &args.Model)
		if err != nil {
			return nil, err
		}
		result = selected
	case "set_thinking_level":
		var args struct {
			Level agent.ThinkingLevel `json:"level"`
		}
		if err := json.Unmarshal(request.Args, &args); err != nil {
			return nil, err
		}
		if err := api.SetThinkingLevel(args.Level); err != nil {
			return nil, err
		}
	case "abort":
		contextValue := host.currentContext(request.ExtensionID)
		if contextValue == nil {
			return nil, errors.New("extension context is not active")
		}
		if err := callStateAPI(func() { contextValue.Abort() }); err != nil {
			return nil, err
		}
	case "shutdown":
		contextValue := host.currentContext(request.ExtensionID)
		if contextValue == nil {
			return nil, errors.New("extension context is not active")
		}
		if err := callStateAPI(func() { contextValue.Shutdown() }); err != nil {
			return nil, err
		}
	case "compact":
		contextValue := host.currentContext(request.ExtensionID)
		if contextValue == nil {
			return nil, errors.New("extension context is not active")
		}
		var args struct {
			CustomInstructions string `json:"customInstructions"`
		}
		if err := json.Unmarshal(request.Args, &args); err != nil {
			return nil, err
		}
		if err := callStateAPI(func() {
			contextValue.Compact(&extensions.CompactOptions{CustomInstructions: args.CustomInstructions})
		}); err != nil {
			return nil, err
		}
	default:
		return nil, fmt.Errorf("unknown state action %q", request.Action)
	}
	host.refreshAndPush(manager, generation, request.ExtensionID, nil)
	return result, nil
}

func (host *stateHost) api(extensionID string) extensions.API {
	host.mu.RLock()
	defer host.mu.RUnlock()
	return host.apis[extensionID]
}

func (host *stateHost) refreshCurrent(manager *Manager, extensionID string, contextValue extensions.Context) {
	manager.mu.Lock()
	generation := manager.current
	manager.mu.Unlock()
	if generation == nil || !generation.ready.Load() {
		return
	}
	host.refreshAndPush(manager, generation, extensionID, contextValue)
}

func (host *stateHost) refreshAndPush(manager *Manager, generation *generation, extensionID string, contextValue extensions.Context) {
	api := host.api(extensionID)
	if api == nil {
		return
	}
	snapshot := host.refreshSnapshot(extensionID, api, contextValue)
	if err := host.writeStateDelta(generation, extensionID, snapshot); err != nil {
		manager.report(extensions.Diagnostic{Type: "error", Message: err.Error(), Path: extensionID})
	}
}

func (host *stateHost) writeStateDelta(generation *generation, extensionID string, snapshot stateSnapshot) error {
	host.mu.RLock()
	sessionSent := host.sentSessionRevisions[snapshot.SessionRevision]
	host.mu.RUnlock()
	if sessionSent {
		snapshot.Session = nil
	}
	markSessionSent := func() {
		if snapshot.SessionRevision == "" {
			return
		}
		host.mu.Lock()
		host.sentSessionRevisions[snapshot.SessionRevision] = true
		host.mu.Unlock()
	}
	frameFor := func(transferID string) (frame, error) {
		return eventFrame("state_delta", struct {
			ExtensionID       string        `json:"extensionId"`
			Snapshot          stateSnapshot `json:"stateSnapshot"`
			SessionTransferID string        `json:"sessionTransferId,omitempty"`
		}{extensionID, snapshot, transferID})
	}
	value, err := frameFor("")
	if err != nil {
		return err
	}
	if err = generation.codec.write(value); err == nil {
		markSessionSent()
		return nil
	}
	if !errors.Is(err, ErrFrameTooLarge) || snapshot.Session == nil {
		return err
	}
	encoded, err := ai.Marshal(snapshot.Session)
	if err != nil {
		return err
	}
	host.mu.Lock()
	host.nextTransferID++
	transferID := fmt.Sprintf("%s-session-%d", extensionID, host.nextTransferID)
	host.mu.Unlock()
	const chunkSize = 2 << 20
	total := (len(encoded) + chunkSize - 1) / chunkSize
	for index, offset := 0, 0; offset < len(encoded); index, offset = index+1, offset+chunkSize {
		end := min(offset+chunkSize, len(encoded))
		chunk, frameErr := eventFrame("state_session_chunk", struct {
			TransferID string `json:"transferId"`
			Index      int    `json:"index"`
			Total      int    `json:"total"`
			Data       string `json:"data"`
		}{transferID, index, total, base64.StdEncoding.EncodeToString(encoded[offset:end])})
		if frameErr != nil {
			return frameErr
		}
		if frameErr = generation.codec.write(chunk); frameErr != nil {
			return frameErr
		}
	}
	snapshot.Session = nil
	value, err = frameFor(transferID)
	if err != nil {
		return err
	}
	if err = generation.codec.write(value); err != nil {
		return err
	}
	markSessionSent()
	return nil
}

func (host *stateHost) refreshSnapshot(extensionID string, api extensions.API, contextValue extensions.Context) stateSnapshot {
	host.mu.RLock()
	snapshot, exists := host.snapshots[extensionID]
	registration := cloneStateRegistrations(host.registrations[extensionID])
	host.mu.RUnlock()
	if !exists {
		snapshot = host.handshakeSnapshot()
	} else {
		snapshot = cloneStateSnapshot(snapshot)
	}
	if registration != nil {
		for _, flag := range sortedFlags(registration.flags) {
			var value any
			var ok bool
			if callStateAPI(func() { value, ok = api.GetFlag(flag.Name) }) == nil && ok {
				snapshot.Flags[flag.Name] = value
			}
		}
	}
	var sessionName *string
	if err := callStateAPIError(func() error {
		var err error
		sessionName, err = api.GetSessionName(context.Background())
		return err
	}); err == nil {
		snapshot.SessionName = cloneStringPointer(sessionName)
	}
	var active []string
	if err := callStateAPIError(func() error {
		var err error
		active, err = api.GetActiveTools()
		return err
	}); err == nil {
		snapshot.ActiveTools = append([]string(nil), active...)
	}
	var all []extensions.ToolInfo
	if err := callStateAPIError(func() error {
		var err error
		all, err = api.GetAllTools()
		return err
	}); err == nil {
		snapshot.AllTools = append([]extensions.ToolInfo(nil), all...)
	}
	var commands []extensions.SlashCommandInfo
	if err := callStateAPIError(func() error {
		var err error
		commands, err = api.GetCommands()
		return err
	}); err == nil {
		snapshot.Commands = append([]extensions.SlashCommandInfo(nil), commands...)
	}
	var thinking agent.ThinkingLevel
	if err := callStateAPIError(func() error {
		var err error
		thinking, err = api.GetThinkingLevel()
		return err
	}); err == nil && thinking != "" {
		snapshot.ThinkingLevel = thinking
	}
	if contextValue != nil {
		host.updateContextSnapshot(&snapshot, contextValue)
	}
	host.mu.Lock()
	host.snapshots[extensionID] = cloneStateSnapshot(snapshot)
	host.mu.Unlock()
	return snapshot
}

func (host *stateHost) updateContextSnapshot(snapshot *stateSnapshot, contextValue extensions.Context) {
	_ = callStateAPI(func() {
		snapshot.Context.CWD = contextValue.CWD()
		snapshot.Context.Mode = contextValue.Mode()
		snapshot.Context.HasUI = contextValue.HasUI()
		if model := contextValue.Model(); model != nil {
			copy := *model
			snapshot.Context.Model = &copy
		} else {
			snapshot.Context.Model = nil
		}
		snapshot.Context.Idle = contextValue.IsIdle()
		snapshot.Context.ProjectTrusted = contextValue.IsProjectTrusted()
		snapshot.Context.HasPendingMessages = contextValue.HasPendingMessages()
		if usage := contextValue.GetContextUsage(); usage != nil {
			snapshot.Context.ContextUsage = &stateContextUsage{Tokens: cloneInt64Pointer(usage.Tokens), ContextWindow: usage.ContextWindow, Percent: cloneFloat64Pointer(usage.Percent)}
		} else {
			snapshot.Context.ContextUsage = nil
		}
		snapshot.Context.SystemPrompt = contextValue.GetSystemPrompt()
		snapshot.Session, snapshot.SessionRevision = host.captureSession(contextValue.SessionManager())
		snapshot.Models = captureModelRegistry(contextValue.ModelRegistry())
	})
}

func (host *stateHost) captureSession(manager extensions.ReadonlySessionManager) (*stateSessionSnapshot, string) {
	if manager == nil {
		return nil, ""
	}
	leafID := manager.GetLeafID()
	leaf := "\x00"
	if leafID != nil {
		leaf = *leafID
	}
	key := fmt.Sprintf("%T:%p\x00%s\x00%s\x00%s", manager, manager, manager.GetSessionID(), manager.GetSessionFile(), leaf)
	host.mu.RLock()
	if key == host.sessionCacheKey {
		snapshot, revision := host.sessionCache, host.sessionCacheRevision
		host.mu.RUnlock()
		return snapshot, fmt.Sprintf("session-%d", revision)
	}
	host.mu.RUnlock()
	entries := manager.GetEntries()
	var sessionFile *string
	if value := manager.GetSessionFile(); value != "" {
		sessionFile = &value
	}
	snapshot := &stateSessionSnapshot{
		Persisted: manager.IsPersisted(), CWD: manager.GetCWD(), SessionDir: manager.GetSessionDir(), SessionID: manager.GetSessionID(),
		SessionFile: sessionFile, LeafID: cloneStringPointer(leafID), Entries: append([]session.SessionEntry(nil), entries...),
		Header: manager.GetHeader(), SessionName: cloneStringPointer(manager.GetSessionName()),
	}
	host.mu.Lock()
	if key != host.sessionCacheKey {
		host.sessionCacheKey = key
		host.sessionCacheRevision++
		host.sessionCache = snapshot
	}
	snapshot, revision := host.sessionCache, host.sessionCacheRevision
	host.mu.Unlock()
	return snapshot, fmt.Sprintf("session-%d", revision)
}

func captureModelRegistry(registry extensions.ModelRegistry) *stateModelRegistrySnapshot {
	if registry == nil {
		return nil
	}
	result := &stateModelRegistrySnapshot{
		Error: registry.Error(), All: registry.Models(), RegisteredProviderIDs: append([]string(nil), registry.RegisteredProviderIDs()...),
		Providers: make(map[string]stateProviderSnapshot),
	}
	if available, err := registry.AvailableWithError(nil); err == nil {
		result.Available = available
	}
	ids := append([]string(nil), result.RegisteredProviderIDs...)
	seen := make(map[string]bool, len(ids))
	for _, id := range ids {
		seen[id] = true
	}
	for _, model := range result.All {
		providerID := string(model.Provider)
		if !seen[providerID] {
			ids = append(ids, providerID)
			seen[providerID] = true
		}
	}
	sort.Strings(ids)
	for _, id := range ids {
		value := stateProviderSnapshot{ID: id, DisplayName: registry.ProviderDisplayName(id), AuthStatus: registry.GetProviderAuthStatus(id, nil), UsingOAuth: registry.IsUsingOAuth(id)}
		if provider, ok := registry.Provider(id); ok {
			value.Name, value.BaseURL = provider.Name, provider.BaseURL
			value.Headers = cloneStringMap(provider.Headers)
		}
		_, value.RegisteredConfig = registry.RegisteredProviderConfig(id)
		_, value.RegisteredNative = registry.RegisteredNativeProvider(id)
		result.Providers[id] = value
	}
	return result
}

func (host *stateHost) applyEventMutation(event extensions.Event, raw json.RawMessage) error {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	switch typed := event.(type) {
	case extensions.ToolCallEvent:
		var payload struct {
			Input map[string]any `json:"input"`
		}
		if err := json.Unmarshal(raw, &payload); err != nil {
			return err
		}
		clear(typed.Input)
		for key, value := range payload.Input {
			typed.Input[key] = value
		}
	case extensions.BeforeProviderHeadersEvent:
		var payload struct {
			Headers map[string]*string `json:"headers"`
		}
		if err := json.Unmarshal(raw, &payload); err != nil {
			return err
		}
		clear(typed.Headers)
		for key, value := range payload.Headers {
			typed.Headers[key] = value
		}
	}
	return nil
}

func (host *stateHost) decodeEventResult(manager *Manager, extensionID string, event extensions.EventType, raw json.RawMessage) (any, error) {
	switch event {
	case extensions.EventBeforeProviderRequest:
		var payload any
		if err := json.Unmarshal(raw, &payload); err != nil {
			return nil, err
		}
		return extensions.ProviderRequestResult{Payload: payload, Replace: true}, nil
	case extensions.EventContext:
		var value struct {
			Messages []json.RawMessage `json:"messages"`
		}
		if err := json.Unmarshal(raw, &value); err != nil {
			return nil, err
		}
		messages, err := decodeAgentMessages(value.Messages)
		return extensions.ContextResult{Messages: messages}, err
	case extensions.EventMessageEnd:
		var value struct {
			Message json.RawMessage `json:"message"`
		}
		if err := json.Unmarshal(raw, &value); err != nil || len(value.Message) == 0 {
			return nil, err
		}
		message, err := decodeAgentMessage(value.Message)
		if err != nil {
			return nil, err
		}
		return extensions.MessageEndResult{Message: message}, nil
	case extensions.EventToolResult:
		return decodeToolResultResult(raw)
	case extensions.EventUserBash:
		var value struct {
			Result     *extensions.BashResult `json:"result"`
			Operations *struct {
				HostOperationID string `json:"hostOperationId"`
			} `json:"operations"`
		}
		if err := json.Unmarshal(raw, &value); err != nil {
			return nil, err
		}
		result := extensions.UserBashResult{Result: value.Result}
		if value.Operations != nil && value.Operations.HostOperationID != "" {
			result.Operations = &hostBashOperations{manager: manager, extensionID: extensionID, operationID: value.Operations.HostOperationID}
		}
		return result, nil
	default:
		return decodeEventResult(event, raw)
	}
}

type hostBashOperations struct {
	manager     *Manager
	extensionID string
	operationID string
}

func (operations *hostBashOperations) Exec(ctx context.Context, command, cwd string, options tools.BashExecOptions) (tools.BashExecResult, error) {
	request := struct {
		ExtensionID string            `json:"extensionId"`
		OperationID string            `json:"operationId"`
		Command     string            `json:"command"`
		CWD         string            `json:"cwd"`
		Timeout     *float64          `json:"timeout,omitempty"`
		Env         map[string]string `json:"env,omitempty"`
	}{operations.extensionID, operations.operationID, command, cwd, options.Timeout, options.Env}
	update := func(raw json.RawMessage) {
		if options.OnData == nil {
			return
		}
		var value struct {
			Data string `json:"data"`
		}
		if json.Unmarshal(raw, &value) == nil {
			if decoded, err := base64.StdEncoding.DecodeString(value.Data); err == nil {
				options.OnData(decoded)
			}
		}
	}
	raw, err := operations.manager.request(ctx, "execute_bash_operation", request, update)
	if err != nil {
		return tools.BashExecResult{}, err
	}
	var result tools.BashExecResult
	if err := json.Unmarshal(raw, &result); err != nil {
		return tools.BashExecResult{}, err
	}
	return result, nil
}

func decodeToolResultResult(raw json.RawMessage) (extensions.ToolResultResult, error) {
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil {
		return extensions.ToolResultResult{}, err
	}
	result := extensions.ToolResultResult{}
	if value, ok := object["content"]; ok {
		var content ai.ToolResultContent
		if err := json.Unmarshal(value, &content); err != nil {
			return result, err
		}
		result.Content = &content
	}
	if value, ok := object["details"]; ok {
		var details any
		if err := json.Unmarshal(value, &details); err != nil {
			return result, err
		}
		result.Details = &details
	}
	if value, ok := object["isError"]; ok {
		var isError bool
		if err := json.Unmarshal(value, &isError); err != nil {
			return result, err
		}
		result.IsError = &isError
	}
	if value, ok := object["usage"]; ok {
		var usage ai.Usage
		if err := json.Unmarshal(value, &usage); err != nil {
			return result, err
		}
		result.Usage = &usage
	}
	return result, nil
}

func decodeAgentMessages(values []json.RawMessage) (agent.AgentMessages, error) {
	result := make(agent.AgentMessages, 0, len(values))
	for _, raw := range values {
		message, err := decodeAgentMessage(raw)
		if err != nil {
			return nil, err
		}
		result = append(result, message)
	}
	return result, nil
}

func decodeAgentMessage(raw json.RawMessage) (agent.AgentMessage, error) {
	message, err := ai.UnmarshalMessage(raw)
	if err == nil {
		return message, nil
	}
	var custom any
	if customErr := json.Unmarshal(raw, &custom); customErr != nil {
		return nil, err
	}
	return custom, nil
}

func callStateAPI(call func()) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("extension host state: %v", recovered)
		}
	}()
	call()
	return nil
}

func callStateAPIError(call func() error) (err error) {
	if panicErr := callStateAPI(func() { err = call() }); panicErr != nil {
		return panicErr
	}
	return err
}

func cloneStateRegistrations(value *stateRegistrations) *stateRegistrations {
	if value == nil {
		return nil
	}
	result := &stateRegistrations{flags: make(map[string]wireFlag, len(value.flags)), bus: make(map[string]wireBusSubscription, len(value.bus))}
	for name, flag := range value.flags {
		flag.Default = append(json.RawMessage(nil), flag.Default...)
		result.flags[name] = flag
	}
	for id, subscription := range value.bus {
		result.bus[id] = subscription
	}
	return result
}

func cloneStateSnapshot(value stateSnapshot) stateSnapshot {
	result := value
	result.Flags = make(map[string]any, len(value.Flags))
	for key, item := range value.Flags {
		result.Flags[key] = item
	}
	result.SessionName = cloneStringPointer(value.SessionName)
	result.ActiveTools = append([]string(nil), value.ActiveTools...)
	result.AllTools = append([]extensions.ToolInfo(nil), value.AllTools...)
	result.Commands = append([]extensions.SlashCommandInfo(nil), value.Commands...)
	if value.Context.Model != nil {
		model := *value.Context.Model
		result.Context.Model = &model
	}
	if value.Context.ContextUsage != nil {
		usage := *value.Context.ContextUsage
		usage.Tokens = cloneInt64Pointer(usage.Tokens)
		usage.Percent = cloneFloat64Pointer(usage.Percent)
		result.Context.ContextUsage = &usage
	}
	return result
}

func sortedFlags(values map[string]wireFlag) []wireFlag {
	names := make([]string, 0, len(values))
	for name := range values {
		names = append(names, name)
	}
	sort.Strings(names)
	result := make([]wireFlag, 0, len(names))
	for _, name := range names {
		result = append(result, values[name])
	}
	return result
}

func sortedBusSubscriptions(values map[string]wireBusSubscription) []wireBusSubscription {
	ids := make([]string, 0, len(values))
	for id := range values {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	result := make([]wireBusSubscription, 0, len(ids))
	for _, id := range ids {
		result = append(result, values[id])
	}
	return result
}

func cloneStringPointer(value *string) *string {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func cloneInt64Pointer(value *int64) *int64 {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func cloneFloat64Pointer(value *float64) *float64 {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func cloneStringMap(value map[string]string) map[string]string {
	if value == nil {
		return nil
	}
	result := make(map[string]string, len(value))
	for key, item := range value {
		result[key] = item
	}
	return result
}
