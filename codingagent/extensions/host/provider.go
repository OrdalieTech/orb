package host

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/OrdalieTech/pigo/agent"
	"github.com/OrdalieTech/pigo/ai"
	aiauth "github.com/OrdalieTech/pigo/ai/auth"
	"github.com/OrdalieTech/pigo/codingagent/extensions"
)

const (
	wireProviderNative = "native"
	wireProviderConfig = "config"
)

// ProviderInvokeError distinguishes extension failures from transport failures.
// A retryable error means the process generation disappeared or was replaced;
// callers may retry against the next generation without rebuilding the registry.
type ProviderInvokeError struct {
	ExtensionID string
	ProviderID  string
	Method      string
	CanRetry    bool
	Cause       error
}

func (err *ProviderInvokeError) Error() string {
	if err == nil {
		return ""
	}
	return fmt.Sprintf("extension host provider %s %s: %v", err.ProviderID, err.Method, err.Cause)
}

func (err *ProviderInvokeError) Unwrap() error { return err.Cause }

func (err *ProviderInvokeError) Retryable() bool { return err != nil && err.CanRetry }

type wireProviderRegistration struct {
	Kind         string                        `json:"kind"`
	ID           string                        `json:"id"`
	Name         string                        `json:"name,omitempty"`
	BaseURL      string                        `json:"baseUrl,omitempty"`
	Headers      map[string]string             `json:"headers,omitempty"`
	Models       []ai.Model                    `json:"models,omitempty"`
	Auth         wireProviderAuth              `json:"auth,omitempty"`
	Stream       string                        `json:"stream,omitempty"`
	StreamSimple string                        `json:"streamSimple,omitempty"`
	Config       *wireProviderConfigDefinition `json:"config,omitempty"`
}

type wireProviderAuth struct {
	APIKey *wireAPIKeyAuth `json:"apiKey,omitempty"`
	OAuth  *wireOAuthAuth  `json:"oauth,omitempty"`
}

type wireAPIKeyAuth struct {
	Name    string `json:"name"`
	Login   string `json:"login,omitempty"`
	Check   string `json:"check,omitempty"`
	Resolve string `json:"resolve"`
}

type wireOAuthAuth struct {
	Name       string `json:"name"`
	LoginLabel string `json:"loginLabel,omitempty"`
	Login      string `json:"login"`
	Refresh    string `json:"refresh"`
	ToAuth     string `json:"toAuth"`
}

type wireProviderConfigDefinition struct {
	Name         string                           `json:"name,omitempty"`
	BaseURL      string                           `json:"baseUrl,omitempty"`
	APIKey       string                           `json:"apiKey,omitempty"`
	API          ai.API                           `json:"api,omitempty"`
	Headers      map[string]string                `json:"headers,omitempty"`
	AuthHeader   *bool                            `json:"authHeader,omitempty"`
	Models       []extensions.ProviderModelConfig `json:"models,omitempty"`
	Defined      map[string]bool                  `json:"defined"`
	StreamSimple string                           `json:"streamSimple,omitempty"`
}

type providerBridge struct {
	mu     sync.Mutex
	nextID uint64
	active map[string]providerInvocation
}

type providerInvocation struct {
	ctx         context.Context
	authContext aiauth.AuthContext
	interaction aiauth.AuthInteraction
}

type providerInvokeResult struct {
	Present bool            `json:"present"`
	Value   json.RawMessage `json:"value,omitempty"`
}

type providerInteractionEvent struct {
	InvocationID string          `json:"invocationId"`
	CallID       string          `json:"callId,omitempty"`
	Operation    string          `json:"operation"`
	Value        json.RawMessage `json:"value,omitempty"`
}

func (bridge *providerBridge) begin(invocation providerInvocation) string {
	bridge.mu.Lock()
	defer bridge.mu.Unlock()
	bridge.nextID++
	id := fmt.Sprintf("provider-invoke-%d", bridge.nextID)
	if bridge.active == nil {
		bridge.active = make(map[string]providerInvocation)
	}
	bridge.active[id] = invocation
	return id
}

func (bridge *providerBridge) end(id string) {
	if id == "" {
		return
	}
	bridge.mu.Lock()
	delete(bridge.active, id)
	bridge.mu.Unlock()
}

func (bridge *providerBridge) invocation(id string) (providerInvocation, bool) {
	bridge.mu.Lock()
	defer bridge.mu.Unlock()
	value, ok := bridge.active[id]
	return value, ok
}

func (manager *Manager) handleProviderHostRequest(generation *generation, value frame) (any, *protocolError, bool) {
	if value.Method != "register_provider" {
		return nil, nil, false
	}
	var params struct {
		ExtensionID string                   `json:"extensionId"`
		Provider    wireProviderRegistration `json:"provider"`
	}
	if err := json.Unmarshal(value.Params, &params); err != nil {
		return nil, invalidRegistration(err), true
	}
	state := generation.registration(params.ExtensionID)
	if state == nil {
		return nil, invalidRegistration(errors.New("unknown extension id")), true
	}
	if err := validateWireProvider(params.Provider); err != nil {
		return nil, invalidRegistration(err), true
	}
	for index := range state.Providers {
		if state.Providers[index].ID == params.Provider.ID {
			state.Providers[index] = cloneWireProvider(params.Provider)
			if api := manager.stateHost.api(params.ExtensionID); api != nil {
				if err := callStateAPI(func() {
					manager.registerProviders(api, params.ExtensionID, []wireProviderRegistration{params.Provider})
				}); err != nil {
					return nil, invalidRegistration(err), true
				}
			}
			return map[string]bool{"accepted": true}, nil, true
		}
	}
	state.Providers = append(state.Providers, cloneWireProvider(params.Provider))
	if api := manager.stateHost.api(params.ExtensionID); api != nil {
		if err := callStateAPI(func() {
			manager.registerProviders(api, params.ExtensionID, []wireProviderRegistration{params.Provider})
		}); err != nil {
			return nil, invalidRegistration(err), true
		}
	}
	return map[string]bool{"accepted": true}, nil, true
}

func validateWireProvider(provider wireProviderRegistration) error {
	if provider.ID == "" {
		return errors.New("provider requires an id")
	}
	switch provider.Kind {
	case wireProviderNative:
		if provider.Name == "" {
			return fmt.Errorf("native provider %q requires name", provider.ID)
		}
		if provider.Auth.APIKey == nil && provider.Auth.OAuth == nil {
			return fmt.Errorf("native provider %q requires auth", provider.ID)
		}
		if provider.Auth.APIKey != nil && provider.Auth.APIKey.Resolve == "" {
			return fmt.Errorf("native provider %q apiKey auth requires resolve", provider.ID)
		}
		if oauth := provider.Auth.OAuth; oauth != nil && (oauth.Login == "" || oauth.Refresh == "" || oauth.ToAuth == "") {
			return fmt.Errorf("native provider %q oauth auth requires login, refresh, and toAuth", provider.ID)
		}
	case wireProviderConfig:
		if provider.Config == nil {
			return fmt.Errorf("provider config %q is missing config", provider.ID)
		}
	default:
		return fmt.Errorf("provider %q has unknown registration kind %q", provider.ID, provider.Kind)
	}
	return nil
}

func (manager *Manager) registerProviders(api extensions.API, extensionID string, registrations []wireProviderRegistration) {
	for _, registration := range registrations {
		switch registration.Kind {
		case wireProviderNative:
			api.RegisterProvider(manager.nativeProvider(extensionID, registration.ID))
		case wireProviderConfig:
			api.RegisterProviderConfig(registration.ID, manager.providerConfig(extensionID, registration.ID))
		}
	}
}

func (manager *Manager) nativeProvider(extensionID, providerID string) extensions.Provider {
	registration, _ := manager.providerRegistration(extensionID, providerID)
	provider := extensions.Provider{
		ID:      providerID,
		Name:    registration.Name,
		BaseURL: registration.BaseURL,
		Headers: cloneProviderStringMap(registration.Headers),
	}
	if registration.Auth.APIKey != nil {
		provider.Auth.APIKey = &hostAPIKeyAuth{manager: manager, extensionID: extensionID, providerID: providerID}
	}
	if registration.Auth.OAuth != nil {
		provider.Auth.OAuth = &hostOAuthAuth{manager: manager, extensionID: extensionID, providerID: providerID}
	}
	provider.GetModels = func() ([]ai.Model, error) {
		current, ok := manager.providerRegistration(extensionID, providerID)
		if !ok {
			return nil, fmt.Errorf("extension host: provider %s is unavailable", providerID)
		}
		return append([]ai.Model(nil), current.Models...), nil
	}
	if registration.Stream != "" {
		provider.Stream = manager.providerStream(extensionID, providerID, "stream")
	}
	if registration.StreamSimple != "" {
		provider.StreamSimple = manager.providerStream(extensionID, providerID, "streamSimple")
	}
	return provider
}

func (manager *Manager) providerConfig(extensionID, providerID string) extensions.ProviderConfig {
	registration, _ := manager.providerRegistration(extensionID, providerID)
	definition := registration.Config
	if definition == nil {
		return extensions.ProviderConfig{}
	}
	config := extensions.ProviderConfig{
		Name:       definition.Name,
		BaseURL:    definition.BaseURL,
		APIKey:     definition.APIKey,
		API:        definition.API,
		Headers:    cloneProviderStringMap(definition.Headers),
		AuthHeader: cloneProviderBool(definition.AuthHeader),
		Models:     append([]extensions.ProviderModelConfig(nil), definition.Models...),
		Defined:    cloneProviderBoolMap(definition.Defined),
	}
	if definition.StreamSimple != "" {
		config.Stream = manager.providerStream(extensionID, providerID, "streamSimple")
	}
	return config
}

func (manager *Manager) providerRegistration(extensionID, providerID string) (wireProviderRegistration, bool) {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	state := manager.states[extensionID]
	if state == nil {
		return wireProviderRegistration{}, false
	}
	for _, provider := range state.Providers {
		if provider.ID == providerID {
			return cloneWireProvider(provider), true
		}
	}
	return wireProviderRegistration{}, false
}

func (manager *Manager) providerHandle(extensionID, providerID, method string) (string, bool) {
	provider, ok := manager.providerRegistration(extensionID, providerID)
	if !ok {
		return "", false
	}
	switch method {
	case "apiKey.login":
		return optionalAPIKeyHandle(provider.Auth.APIKey, func(auth *wireAPIKeyAuth) string { return auth.Login })
	case "apiKey.check":
		return optionalAPIKeyHandle(provider.Auth.APIKey, func(auth *wireAPIKeyAuth) string { return auth.Check })
	case "apiKey.resolve":
		return optionalAPIKeyHandle(provider.Auth.APIKey, func(auth *wireAPIKeyAuth) string { return auth.Resolve })
	case "oauth.login":
		return optionalOAuthHandle(provider.Auth.OAuth, func(auth *wireOAuthAuth) string { return auth.Login })
	case "oauth.refresh":
		return optionalOAuthHandle(provider.Auth.OAuth, func(auth *wireOAuthAuth) string { return auth.Refresh })
	case "oauth.toAuth":
		return optionalOAuthHandle(provider.Auth.OAuth, func(auth *wireOAuthAuth) string { return auth.ToAuth })
	case "stream":
		return provider.Stream, provider.Stream != ""
	case "streamSimple":
		if provider.Kind == wireProviderConfig && provider.Config != nil {
			return provider.Config.StreamSimple, provider.Config.StreamSimple != ""
		}
		return provider.StreamSimple, provider.StreamSimple != ""
	default:
		return "", false
	}
}

func optionalAPIKeyHandle(auth *wireAPIKeyAuth, get func(*wireAPIKeyAuth) string) (string, bool) {
	if auth == nil {
		return "", false
	}
	handle := get(auth)
	return handle, handle != ""
}

func optionalOAuthHandle(auth *wireOAuthAuth, get func(*wireOAuthAuth) string) (string, bool) {
	if auth == nil {
		return "", false
	}
	handle := get(auth)
	return handle, handle != ""
}

func (manager *Manager) invokeProvider(
	ctx context.Context,
	extensionID, providerID, method string,
	args any,
	invocation providerInvocation,
) (json.RawMessage, bool, error) {
	invokeContext, cancel := manager.timeoutContext(ctx)
	defer cancel()
	invocation.ctx = invokeContext
	handle, ok := manager.providerHandle(extensionID, providerID, method)
	if !ok {
		return nil, false, &ProviderInvokeError{
			ExtensionID: extensionID, ProviderID: providerID, Method: method,
			Cause: fmt.Errorf("callback is unavailable"),
		}
	}
	invocationID := ""
	if invocation.authContext != nil || invocation.interaction != nil {
		invocationID = manager.providers.begin(invocation)
		defer manager.providers.end(invocationID)
	}
	request := struct {
		ExtensionID  string `json:"extensionId"`
		ProviderID   string `json:"providerId"`
		Handle       string `json:"handle"`
		Method       string `json:"method"`
		InvocationID string `json:"invocationId,omitempty"`
		Args         any    `json:"args,omitempty"`
	}{extensionID, providerID, handle, method, invocationID, args}
	raw, err := manager.request(invokeContext, "provider_invoke", request, nil)
	if err != nil {
		manager.reportProviderInvokeError(extensionID, err)
		var remote *protocolError
		retryable := !errors.As(err, &remote) && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded)
		return nil, false, &ProviderInvokeError{
			ExtensionID: extensionID, ProviderID: providerID, Method: method, CanRetry: retryable, Cause: err,
		}
	}
	var result providerInvokeResult
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil, false, &ProviderInvokeError{
			ExtensionID: extensionID, ProviderID: providerID, Method: method,
			Cause: fmt.Errorf("decode response: %w", err),
		}
	}
	return result.Value, result.Present, nil
}

func (manager *Manager) reportProviderInvokeError(extensionID string, err error) {
	var remote *protocolError
	if !errors.As(err, &remote) {
		return
	}
	message := remote.Message
	if len(remote.Data) != 0 {
		var data struct {
			Stack string `json:"stack"`
		}
		if json.Unmarshal(remote.Data, &data) == nil && data.Stack != "" && !strings.Contains(message, data.Stack) {
			message += "\n" + data.Stack
		}
	}
	path := extensionID
	if state := manager.state(extensionID); state != nil && state.Path != "" {
		path = state.Path
	}
	manager.report(extensions.Diagnostic{Type: "error", Message: message, Path: path})
}

type hostAPIKeyAuth struct {
	manager     *Manager
	extensionID string
	providerID  string
}

func (method *hostAPIKeyAuth) Name() string {
	provider, _ := method.manager.providerRegistration(method.extensionID, method.providerID)
	if provider.Auth.APIKey == nil {
		return ""
	}
	return provider.Auth.APIKey.Name
}

func (method *hostAPIKeyAuth) Resolve(
	ctx context.Context,
	authContext aiauth.AuthContext,
	credential *aiauth.Credential,
) (*aiauth.AuthResult, error) {
	raw, present, err := method.manager.invokeProvider(ctx, method.extensionID, method.providerID, "apiKey.resolve", struct {
		Credential *aiauth.Credential `json:"credential,omitempty"`
	}{credential}, providerInvocation{authContext: authContext})
	if err != nil || !present {
		return nil, err
	}
	var result aiauth.AuthResult
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

func (method *hostAPIKeyAuth) Check(
	ctx context.Context,
	authContext aiauth.AuthContext,
	credential *aiauth.Credential,
) (*aiauth.AuthCheck, error) {
	if _, ok := method.manager.providerHandle(method.extensionID, method.providerID, "apiKey.check"); !ok {
		resolved, err := method.Resolve(ctx, authContext, credential)
		if err != nil || resolved == nil {
			return nil, err
		}
		return &aiauth.AuthCheck{Source: resolved.Source, Type: aiauth.CredentialAPIKey}, nil
	}
	raw, present, err := method.manager.invokeProvider(ctx, method.extensionID, method.providerID, "apiKey.check", struct {
		Credential *aiauth.Credential `json:"credential,omitempty"`
	}{credential}, providerInvocation{authContext: authContext})
	if err != nil || !present {
		return nil, err
	}
	var result aiauth.AuthCheck
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

func (method *hostAPIKeyAuth) Login(ctx context.Context, interaction aiauth.AuthInteraction) (*aiauth.Credential, error) {
	raw, present, err := method.manager.invokeProvider(ctx, method.extensionID, method.providerID, "apiKey.login", nil, providerInvocation{interaction: interaction})
	if err != nil {
		return nil, err
	}
	if !present {
		return nil, errors.New("api-key login returned no credential")
	}
	var credential aiauth.Credential
	if err := json.Unmarshal(raw, &credential); err != nil {
		return nil, err
	}
	return &credential, nil
}

type hostOAuthAuth struct {
	manager     *Manager
	extensionID string
	providerID  string
}

func (method *hostOAuthAuth) Name() string {
	provider, _ := method.manager.providerRegistration(method.extensionID, method.providerID)
	if provider.Auth.OAuth == nil {
		return ""
	}
	return provider.Auth.OAuth.Name
}

func (method *hostOAuthAuth) LoginLabel() string {
	provider, _ := method.manager.providerRegistration(method.extensionID, method.providerID)
	if provider.Auth.OAuth == nil {
		return ""
	}
	return provider.Auth.OAuth.LoginLabel
}

func (method *hostOAuthAuth) Login(ctx context.Context, interaction aiauth.AuthInteraction) (*aiauth.Credential, error) {
	raw, present, err := method.manager.invokeProvider(ctx, method.extensionID, method.providerID, "oauth.login", nil, providerInvocation{interaction: interaction})
	if err != nil {
		return nil, err
	}
	if !present {
		return nil, errors.New("oauth login returned no credential")
	}
	var credential aiauth.Credential
	if err := json.Unmarshal(raw, &credential); err != nil {
		return nil, err
	}
	return &credential, nil
}

func (method *hostOAuthAuth) Refresh(ctx context.Context, credential *aiauth.Credential) (*aiauth.Credential, error) {
	raw, present, err := method.manager.invokeProvider(ctx, method.extensionID, method.providerID, "oauth.refresh", struct {
		Credential *aiauth.Credential `json:"credential"`
	}{credential}, providerInvocation{})
	if err != nil {
		return nil, err
	}
	if !present {
		return nil, errors.New("oauth refresh returned no credential")
	}
	var refreshed aiauth.Credential
	if err := json.Unmarshal(raw, &refreshed); err != nil {
		return nil, err
	}
	return &refreshed, nil
}

func (method *hostOAuthAuth) ToAuth(credential *aiauth.Credential) (aiauth.ModelAuth, error) {
	raw, present, err := method.manager.invokeProvider(context.Background(), method.extensionID, method.providerID, "oauth.toAuth", struct {
		Credential *aiauth.Credential `json:"credential"`
	}{credential}, providerInvocation{})
	if err != nil {
		return aiauth.ModelAuth{}, err
	}
	if !present {
		return aiauth.ModelAuth{}, errors.New("oauth toAuth returned no auth")
	}
	var auth aiauth.ModelAuth
	if err := json.Unmarshal(raw, &auth); err != nil {
		return aiauth.ModelAuth{}, err
	}
	return auth, nil
}

func (manager *Manager) providerStream(extensionID, providerID, method string) agent.StreamFn {
	return func(
		ctx context.Context,
		model *ai.Model,
		requestContext ai.Context,
		options *ai.SimpleStreamOptions,
	) (ai.AssistantMessageEventStream, error) {
		raw, present, err := manager.invokeProvider(ctx, extensionID, providerID, method, struct {
			Model   *ai.Model               `json:"model"`
			Context ai.Context              `json:"context"`
			Options *ai.SimpleStreamOptions `json:"options,omitempty"`
		}{model, requestContext, options}, providerInvocation{})
		if err != nil {
			return nil, err
		}
		if !present {
			return nil, errors.New("provider stream returned no event stream")
		}
		var result struct {
			Events []json.RawMessage `json:"events"`
		}
		if err := json.Unmarshal(raw, &result); err != nil {
			return nil, fmt.Errorf("extension host: decode provider stream: %w", err)
		}
		events := make([]ai.AssistantMessageEvent, 0, len(result.Events))
		for _, encoded := range result.Events {
			event, err := ai.UnmarshalAssistantMessageEvent(encoded)
			if err != nil {
				return nil, err
			}
			events = append(events, event)
		}
		return func(yield func(ai.AssistantMessageEvent, error) bool) {
			for _, event := range events {
				if !yield(event, nil) {
					return
				}
			}
		}, nil
	}
}

func (manager *Manager) handleProviderHostEvent(generation *generation, value frame) bool {
	if value.Method != "provider_interaction" {
		return false
	}
	var event providerInteractionEvent
	if json.Unmarshal(value.Params, &event) != nil || event.InvocationID == "" || event.Operation == "" {
		return true
	}
	invocation, ok := manager.providers.invocation(event.InvocationID)
	if event.Operation == "notify" {
		if ok && invocation.interaction != nil {
			var authEvent aiauth.AuthEvent
			if json.Unmarshal(event.Value, &authEvent) == nil {
				invocation.interaction.Notify(authEvent)
			}
		}
		return true
	}
	go manager.resolveProviderInteraction(generation, event, invocation, ok)
	return true
}

func (manager *Manager) resolveProviderInteraction(
	generation *generation,
	event providerInteractionEvent,
	invocation providerInvocation,
	found bool,
) {
	if !found {
		manager.writeProviderInteractionResult(generation, event.CallID, nil, false, errors.New("provider invocation is no longer active"))
		return
	}
	callbackContext := invocation.ctx
	if callbackContext == nil {
		callbackContext = context.Background()
	}
	switch event.Operation {
	case "env":
		var input struct {
			Name string `json:"name"`
		}
		if invocation.authContext == nil || json.Unmarshal(event.Value, &input) != nil {
			manager.writeProviderInteractionResult(generation, event.CallID, nil, false, errors.New("auth env callback is unavailable"))
			return
		}
		result, present := invocation.authContext.Env(callbackContext, input.Name)
		manager.writeProviderInteractionResult(generation, event.CallID, result, present, nil)
	case "fileExists":
		var input struct {
			Path string `json:"path"`
		}
		if invocation.authContext == nil || json.Unmarshal(event.Value, &input) != nil {
			manager.writeProviderInteractionResult(generation, event.CallID, nil, false, errors.New("auth fileExists callback is unavailable"))
			return
		}
		manager.writeProviderInteractionResult(generation, event.CallID, invocation.authContext.FileExists(callbackContext, input.Path), true, nil)
	case "prompt":
		var prompt aiauth.AuthPrompt
		if invocation.interaction == nil || json.Unmarshal(event.Value, &prompt) != nil {
			manager.writeProviderInteractionResult(generation, event.CallID, nil, false, errors.New("auth prompt callback is unavailable"))
			return
		}
		result, err := invocation.interaction.Prompt(callbackContext, prompt)
		manager.writeProviderInteractionResult(generation, event.CallID, result, err == nil, err)
	default:
		manager.writeProviderInteractionResult(generation, event.CallID, nil, false, fmt.Errorf("unknown provider interaction %q", event.Operation))
	}
}

func (manager *Manager) writeProviderInteractionResult(generation *generation, callID string, value any, present bool, err error) {
	if callID == "" {
		return
	}
	params := struct {
		CallID  string         `json:"callId"`
		Present bool           `json:"present"`
		Value   any            `json:"value,omitempty"`
		Error   *protocolError `json:"error,omitempty"`
	}{CallID: callID, Present: present, Value: value}
	if err != nil {
		params.Error = &protocolError{Code: "interaction_error", Message: err.Error()}
	}
	frame, frameErr := eventFrame("provider_interaction_result", params)
	if frameErr == nil {
		_ = generation.codec.write(frame)
	}
}

func cloneWireProvider(source wireProviderRegistration) wireProviderRegistration {
	cloned := source
	cloned.Headers = cloneProviderStringMap(source.Headers)
	cloned.Models = append([]ai.Model(nil), source.Models...)
	if source.Auth.APIKey != nil {
		value := *source.Auth.APIKey
		cloned.Auth.APIKey = &value
	}
	if source.Auth.OAuth != nil {
		value := *source.Auth.OAuth
		cloned.Auth.OAuth = &value
	}
	if source.Config != nil {
		value := *source.Config
		value.Headers = cloneProviderStringMap(source.Config.Headers)
		value.Models = append([]extensions.ProviderModelConfig(nil), source.Config.Models...)
		value.Defined = cloneProviderBoolMap(source.Config.Defined)
		value.AuthHeader = cloneProviderBool(source.Config.AuthHeader)
		cloned.Config = &value
	}
	return cloned
}

func cloneProviderStringMap(source map[string]string) map[string]string {
	if source == nil {
		return nil
	}
	result := make(map[string]string, len(source))
	for name, value := range source {
		result[name] = value
	}
	return result
}

func cloneProviderBoolMap(source map[string]bool) map[string]bool {
	if source == nil {
		return nil
	}
	result := make(map[string]bool, len(source))
	for name, value := range source {
		result[name] = value
	}
	return result
}

func cloneProviderBool(source *bool) *bool {
	if source == nil {
		return nil
	}
	value := *source
	return &value
}
