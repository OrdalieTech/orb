package host

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/OrdalieTech/orb/agent/config"
	"github.com/OrdalieTech/orb/agent/extensions"
	"github.com/OrdalieTech/orb/agent/session"
	"github.com/OrdalieTech/orb/ai"
)

// contextModelRuntimeHandle is the reserved model_runtime_v1 handle naming the
// requesting extension's own ctx.modelRegistry view (the same registry
// captureModelRegistry snapshots into state_v1).
const contextModelRuntimeHandle = "context"

// servicesHost dispatches the capability-gated SDK service protocol: sdk_v1,
// agent_session_v1, model_runtime_v1. Handle convention (all services): string
// handle ids minted here; every handle has explicit dispose; events carry a
// per-handle monotonically increasing seq; errors are structured
// {code,message} and never tear the channel; callbacks emitted while an
// operation runs are written before that operation's terminal response.
type servicesHost struct {
	options Options

	mu           sync.Mutex
	sessions     AgentSessionService
	handles      map[string]*serviceHandle
	nextHandleID uint64
	// cancels maps in-flight service request ids to their context cancel;
	// service_cancel events (the JS AbortSignal mirror) fire them.
	cancels map[string]context.CancelFunc
	// cancelledEarly remembers a service_cancel that raced ahead of its
	// request's dispatch goroutine (readline can coalesce both into one stdin
	// chunk). Entries for requests that already completed are swept by reset.
	cancelledEarly map[string]bool
}

type serviceHandle struct {
	id          string
	extensionID string

	// mu orders the event stream: seq is per-handle monotonic and a chunked
	// transfer must not interleave with other events for the same handle.
	mu             sync.Mutex
	seq            uint64
	nextTransferID uint64
	disposed       bool

	session  AgentSessionHandle
	registry extensions.ModelRegistry
}

func newServicesHost(options Options) *servicesHost {
	return &servicesHost{
		options:        options,
		sessions:       stubAgentSessionService{},
		handles:        make(map[string]*serviceHandle),
		cancels:        make(map[string]context.CancelFunc),
		cancelledEarly: make(map[string]bool),
	}
}

// SetAgentSessionService installs the runtime behind agent_session_v1 and
// sdk_v1 resource reloads. nil restores the not-yet-wired stub. Sessions
// created earlier keep the service they were created with.
func (manager *Manager) SetAgentSessionService(service AgentSessionService) {
	if service == nil {
		service = stubAgentSessionService{}
	}
	manager.services.setService(service)
}

func (services *servicesHost) setService(service AgentSessionService) {
	services.mu.Lock()
	services.sessions = service
	services.mu.Unlock()
}

func (services *servicesHost) service() AgentSessionService {
	services.mu.Lock()
	defer services.mu.Unlock()
	return services.sessions
}

// servicesHello is the sdk_v1 bootstrap payload in the handshake response.
type servicesHello struct {
	// SessionsRoot is the absolute sessions root (<agentDir>/sessions). The
	// SDK's SessionManager composes and mkdirs <root>/--<cwd-dashed>-- locally
	// (mirroring session.DefaultSessionDirPath) so getSessionDir() returns a
	// real writable directory without a wire round-trip: the workflow plugin
	// write-probes it synchronously right after create.
	SessionsRoot string `json:"sessionsRoot,omitempty"`
}

func (services *servicesHost) helloValue() servicesHello {
	dir, err := session.DefaultSessionDirPath(services.options.CWD, services.options.AgentDir)
	if err != nil {
		return servicesHello{}
	}
	return servicesHello{SessionsRoot: filepath.Dir(dir)}
}

// asyncRequest reports service methods, all of which run outside the read
// loop: prompts block for whole child turns.
func (services *servicesHost) asyncRequest(method string) bool {
	switch method {
	case "sdk_resource_reload",
		"model_runtime_create", "model_runtime_get_available", "model_runtime_dispose",
		"agent_session_create", "agent_session_prompt",
		"agent_session_abort", "agent_session_subscribe",
		"agent_session_set_active_tools", "agent_session_append_info", "agent_session_dispose":
		return true
	default:
		return false
	}
}

func (services *servicesHost) handleRequest(manager *Manager, generation *generation, value frame) (any, *protocolError, bool) {
	if !services.asyncRequest(value.Method) {
		return nil, nil, false
	}
	result, err := services.dispatch(manager, generation, value)
	if err != nil {
		return nil, serviceProtocolError(err), true
	}
	return result, nil, true
}

// handleEvent consumes service_cancel, the JS-side AbortSignal mirror.
func (services *servicesHost) handleEvent(_ *generation, value frame) bool {
	if value.Method != "service_cancel" {
		return false
	}
	var params struct {
		RequestID string `json:"requestId"`
	}
	if json.Unmarshal(value.Params, &params) != nil || params.RequestID == "" {
		return true
	}
	services.mu.Lock()
	cancel := services.cancels[params.RequestID]
	if cancel == nil {
		services.cancelledEarly[params.RequestID] = true
	}
	services.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	return true
}

func (services *servicesHost) dispatch(manager *Manager, generation *generation, value frame) (any, error) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	services.mu.Lock()
	if services.cancelledEarly[value.ID] {
		delete(services.cancelledEarly, value.ID)
		services.mu.Unlock()
		cancel()
	} else {
		services.cancels[value.ID] = cancel
		services.mu.Unlock()
		defer func() {
			services.mu.Lock()
			delete(services.cancels, value.ID)
			delete(services.cancelledEarly, value.ID)
			services.mu.Unlock()
		}()
	}

	switch value.Method {
	case "sdk_resource_reload":
		var params AgentSessionResourceLoader
		if err := json.Unmarshal(value.Params, &params); err != nil {
			return nil, invalidServiceRequest(err)
		}
		if err := services.service().ReloadResources(ctx, params); err != nil {
			return nil, err
		}
		return map[string]bool{"reloaded": true}, nil

	case "model_runtime_create":
		return services.createModelRuntime(value.Params)

	case "model_runtime_get_available":
		var params struct {
			ExtensionID string `json:"extensionId"`
			Handle      string `json:"handle"`
		}
		if err := json.Unmarshal(value.Params, &params); err != nil {
			return nil, invalidServiceRequest(err)
		}
		registry, err := services.resolveModelRegistry(manager, params.ExtensionID, params.Handle)
		if err != nil {
			return nil, err
		}
		if _, err := registry.AvailableWithError(nil); err != nil {
			return nil, &ServiceError{Code: "model_runtime_error", Message: err.Error()}
		}
		// One catalog snapshot serves both consumers: ModelRuntime.getAvailable()
		// reads catalog.available, the transport's modelRuntimeRefresh rebuilds
		// the sync facade snapshot from the rest.
		return struct {
			Catalog *stateModelRegistrySnapshot `json:"catalog"`
		}{captureModelRegistry(registry)}, nil

	case "model_runtime_dispose":
		var params struct {
			Handle string `json:"handle"`
		}
		if err := json.Unmarshal(value.Params, &params); err != nil {
			return nil, invalidServiceRequest(err)
		}
		// Idempotent; the reserved "context" handle is not disposable state.
		if params.Handle != contextModelRuntimeHandle {
			services.mu.Lock()
			delete(services.handles, params.Handle)
			services.mu.Unlock()
		}
		return map[string]bool{"disposed": true}, nil

	case "agent_session_create":
		return services.createAgentSession(ctx, manager, generation, value.Params)

	case "agent_session_prompt":
		var params struct {
			Handle  string          `json:"handle"`
			Text    string          `json:"text"`
			Options json.RawMessage `json:"options"`
		}
		handle, err := services.sessionParams(value.Params, &params, &params.Handle)
		if err != nil {
			return nil, err
		}
		if err := handle.session.Prompt(ctx, params.Text, params.Options); err != nil {
			return nil, err
		}
		return map[string]bool{"completed": true}, nil

	case "agent_session_abort":
		var params struct {
			Handle string `json:"handle"`
		}
		handle, err := services.sessionParams(value.Params, &params, &params.Handle)
		if err != nil {
			return nil, err
		}
		if err := handle.session.Abort(ctx); err != nil {
			return nil, err
		}
		return map[string]bool{"aborted": true}, nil

	case "agent_session_subscribe":
		var params struct {
			Handle string `json:"handle"`
		}
		handle, err := services.sessionParams(value.Params, &params, &params.Handle)
		if err != nil {
			return nil, err
		}
		handle.mu.Lock()
		seq := handle.seq
		handle.mu.Unlock()
		return struct {
			Subscribed bool   `json:"subscribed"`
			Seq        uint64 `json:"seq"`
		}{true, seq}, nil

	case "agent_session_set_active_tools":
		var params struct {
			Handle string   `json:"handle"`
			Names  []string `json:"names"`
		}
		handle, err := services.sessionParams(value.Params, &params, &params.Handle)
		if err != nil {
			return nil, err
		}
		if err := handle.session.SetActiveToolsByName(ctx, params.Names); err != nil {
			return nil, err
		}
		return map[string]bool{"updated": true}, nil

	case "agent_session_append_info":
		var params struct {
			Handle string `json:"handle"`
			Name   string `json:"name"`
		}
		handle, err := services.sessionParams(value.Params, &params, &params.Handle)
		if err != nil {
			return nil, err
		}
		if err := handle.session.AppendSessionInfo(ctx, params.Name); err != nil {
			return nil, err
		}
		return map[string]bool{"appended": true}, nil

	case "agent_session_dispose":
		var params struct {
			Handle string `json:"handle"`
		}
		if err := json.Unmarshal(value.Params, &params); err != nil {
			return nil, invalidServiceRequest(err)
		}
		services.mu.Lock()
		handle := services.handles[params.Handle]
		delete(services.handles, params.Handle)
		services.mu.Unlock()
		// Idempotent: the SDK dedupes but a restart may replay a dispose.
		if handle == nil {
			return map[string]bool{"disposed": true}, nil
		}
		handle.markDisposed()
		if handle.session != nil {
			if err := handle.session.Dispose(ctx); err != nil {
				return nil, err
			}
		}
		return map[string]bool{"disposed": true}, nil
	}
	return nil, &ServiceError{Code: "method_not_found", Message: "unknown service method " + value.Method}
}

// sessionParams decodes raw into params (whose handle field is handleID) and
// resolves the live session handle.
func (services *servicesHost) sessionParams(raw json.RawMessage, params any, handleID *string) (*serviceHandle, error) {
	if err := json.Unmarshal(raw, params); err != nil {
		return nil, invalidServiceRequest(err)
	}
	services.mu.Lock()
	handle := services.handles[*handleID]
	services.mu.Unlock()
	if handle == nil || handle.session == nil {
		return nil, unknownHandleError(*handleID)
	}
	return handle, nil
}

func (services *servicesHost) createModelRuntime(raw json.RawMessage) (any, error) {
	var params struct {
		ExtensionID       string `json:"extensionId"`
		AuthPath          string `json:"authPath"`
		ModelsPath        string `json:"modelsPath"`
		AllowModelNetwork bool   `json:"allowModelNetwork"`
	}
	if err := json.Unmarshal(raw, &params); err != nil {
		return nil, invalidServiceRequest(err)
	}
	// Upstream ModelRuntime.create reads auth.json/models.json from one agent
	// dir; the SDK passes both paths, so their directory is the registry root.
	dir := services.options.AgentDir
	if params.ModelsPath != "" {
		dir = filepath.Dir(params.ModelsPath)
	} else if params.AuthPath != "" {
		dir = filepath.Dir(params.AuthPath)
	}
	var registry *config.ModelRegistry
	var err error
	if params.AllowModelNetwork {
		registry, err = config.NewModelRegistry(dir)
	} else {
		// Upstream CreateModelRuntimeOptions defaults allowModelNetwork false.
		registry, err = config.NewOfflineModelRegistry(dir)
	}
	if err != nil {
		return nil, &ServiceError{Code: "model_runtime_error", Message: err.Error()}
	}
	handle := services.mintHandle("orb-model-runtime", params.ExtensionID)
	handle.registry = registry
	services.retain(handle)
	// The catalog snapshot backs the SDK ModelRegistry facade's synchronous
	// getAll/getAvailable/hasConfiguredAuth, in the same shape state_v1 serves
	// for ctx.modelRegistry.
	return struct {
		Handle  string                      `json:"handle"`
		Catalog *stateModelRegistrySnapshot `json:"catalog"`
	}{handle.id, captureModelRegistry(registry)}, nil
}

func (services *servicesHost) resolveModelRegistry(manager *Manager, extensionID, handleID string) (extensions.ModelRegistry, error) {
	if handleID == "" {
		return nil, &ServiceError{Code: "invalid_service_request", Message: "model_runtime requires a handle"}
	}
	// "context:<extensionId>" self-identifies so the handle stays resolvable
	// when only the handle string travels (transport modelRuntimeRefresh).
	if handleID == contextModelRuntimeHandle || strings.HasPrefix(handleID, contextModelRuntimeHandle+":") {
		if extensionID == "" {
			extensionID = strings.TrimPrefix(strings.TrimPrefix(handleID, contextModelRuntimeHandle), ":")
		}
		if extensionID == "" {
			return nil, &ServiceError{Code: "invalid_service_request", Message: `model_runtime handle "context" requires an extensionId`}
		}
		registry, err := manager.stateHost.contextModelRegistry(extensionID)
		if err != nil {
			return nil, &ServiceError{Code: "model_runtime_error", Message: err.Error()}
		}
		return registry, nil
	}
	services.mu.Lock()
	handle := services.handles[handleID]
	services.mu.Unlock()
	if handle == nil || handle.registry == nil {
		return nil, unknownHandleError(handleID)
	}
	return handle.registry, nil
}

func (services *servicesHost) createAgentSession(ctx context.Context, manager *Manager, generation *generation, raw json.RawMessage) (any, error) {
	var params struct {
		ExtensionID string              `json:"extensionId"`
		Options     AgentSessionOptions `json:"options"`
	}
	if err := json.Unmarshal(raw, &params); err != nil {
		return nil, invalidServiceRequest(err)
	}
	handle := services.mintHandle("orb-session", params.ExtensionID)
	callbacks := AgentSessionCallbacks{
		OnEvent: func(payload any) {
			services.emitSessionEvent(generation, handle, SessionEventGeneric, payload)
		},
		OnMessagesSnapshot: func(messages any) {
			services.emitSessionEvent(generation, handle, SessionEventMessagesSnapshot, struct {
				Messages any `json:"messages"`
			}{messages})
		},
		OnMessageAppended: func(message any) {
			services.emitSessionEvent(generation, handle, SessionEventMessageAppended, struct {
				Message any `json:"message"`
			}{message})
		},
		OnMessageUpdated: func(index int, message any) {
			services.emitSessionEvent(generation, handle, SessionEventMessageUpdated, struct {
				Index   int `json:"index"`
				Message any `json:"message"`
			}{index, message})
		},
		OnStats: func(stats AgentSessionStats) {
			services.emitSessionEvent(generation, handle, SessionEventStats, struct {
				Stats AgentSessionStats `json:"stats"`
			}{stats})
		},
	}
	request := AgentSessionCreateRequest{
		ExtensionID: params.ExtensionID,
		Options:     params.Options,
		ExecuteTool: func(toolCtx context.Context, toolName, toolCallID string, toolParams any, onUpdate func(json.RawMessage)) (json.RawMessage, error) {
			return manager.request(toolCtx, "execute_session_tool", struct {
				Handle     string `json:"handle"`
				ToolName   string `json:"toolName"`
				ToolCallID string `json:"toolCallId"`
				Params     any    `json:"params"`
			}{handle.id, toolName, toolCallID, toolParams}, onUpdate)
		},
	}
	if ref := params.Options.ModelRuntime; ref != nil {
		extensionID := ref.ExtensionID
		if extensionID == "" {
			extensionID = params.ExtensionID
		}
		handleID := ref.Handle
		request.ResolveModelRuntime = func() (extensions.ModelRegistry, error) {
			return services.resolveModelRegistry(manager, extensionID, handleID)
		}
	}
	created, result, err := services.service().CreateSession(ctx, request, callbacks)
	if err != nil {
		return nil, err
	}
	handle.session = created
	services.retain(handle)
	return struct {
		Handle               string    `json:"handle"`
		ModelFallbackMessage string    `json:"modelFallbackMessage,omitempty"`
		Model                *ai.Model `json:"model,omitempty"`
	}{handle.id, result.ModelFallbackMessage, result.Model}, nil
}

func (services *servicesHost) mintHandle(prefix, extensionID string) *serviceHandle {
	services.mu.Lock()
	services.nextHandleID++
	id := fmt.Sprintf("%s-%d", prefix, services.nextHandleID)
	services.mu.Unlock()
	return &serviceHandle{id: id, extensionID: extensionID}
}

func (services *servicesHost) retain(handle *serviceHandle) {
	services.mu.Lock()
	services.handles[handle.id] = handle
	services.mu.Unlock()
}

// emitSessionEvent mirrors one session event to the host process. Events for a
// handle are seq-ordered under the handle mutex; an event whose frame exceeds
// the 4 MiB cap is mirrored as base64 agent_session_chunk frames (precedent:
// state_session_chunk) referenced by a chunkless terminal event. Emissions
// made while a service operation runs are written before that operation's
// response, which respondHostRequest only writes after dispatch returns.
func (services *servicesHost) emitSessionEvent(generation *generation, handle *serviceHandle, kind string, payload any) {
	encoded, err := ai.Marshal(payload)
	if err != nil {
		services.reportEventFailure(generation, handle, kind, err)
		return
	}
	handle.mu.Lock()
	defer handle.mu.Unlock()
	if handle.disposed {
		return
	}
	handle.seq++
	envelope := struct {
		Handle     string          `json:"handle"`
		Seq        uint64          `json:"seq"`
		Kind       string          `json:"kind"`
		Event      json.RawMessage `json:"event,omitempty"`
		TransferID string          `json:"transferId,omitempty"`
	}{Handle: handle.id, Seq: handle.seq, Kind: kind, Event: encoded}
	value, err := eventFrame("agent_session_event", envelope)
	if err == nil {
		if err = generation.codec.write(value); err == nil {
			return
		}
	}
	if !errors.Is(err, ErrFrameTooLarge) {
		services.reportEventFailure(generation, handle, kind, err)
		return
	}
	handle.nextTransferID++
	transferID := fmt.Sprintf("%s-transfer-%d", handle.id, handle.nextTransferID)
	const chunkSize = 2 << 20
	total := (len(encoded) + chunkSize - 1) / chunkSize
	for index, offset := 0, 0; offset < len(encoded); index, offset = index+1, offset+chunkSize {
		end := min(offset+chunkSize, len(encoded))
		chunk, chunkErr := eventFrame("agent_session_chunk", struct {
			Handle     string `json:"handle"`
			TransferID string `json:"transferId"`
			Index      int    `json:"index"`
			Total      int    `json:"total"`
			Data       string `json:"data"`
		}{handle.id, transferID, index, total, base64.StdEncoding.EncodeToString(encoded[offset:end])})
		if chunkErr == nil {
			chunkErr = generation.codec.write(chunk)
		}
		if chunkErr != nil {
			services.reportEventFailure(generation, handle, kind, chunkErr)
			return
		}
	}
	envelope.Event = nil
	envelope.TransferID = transferID
	value, err = eventFrame("agent_session_event", envelope)
	if err == nil {
		err = generation.codec.write(value)
	}
	if err != nil {
		services.reportEventFailure(generation, handle, kind, err)
	}
}

func (services *servicesHost) reportEventFailure(generation *generation, handle *serviceHandle, kind string, err error) {
	generation.manager.report(extensions.Diagnostic{
		Type:    "error",
		Message: fmt.Sprintf("agent_session event %s for %s: %v", kind, handle.id, err),
		Path:    handle.extensionID,
	})
}

// reset tears down every service handle: generations own their handles, so a
// restart or Close must not leave child sessions running against a host
// process that no longer exists.
func (services *servicesHost) reset() {
	services.mu.Lock()
	handles := services.handles
	services.handles = make(map[string]*serviceHandle)
	cancels := services.cancels
	services.cancels = make(map[string]context.CancelFunc)
	services.cancelledEarly = make(map[string]bool)
	services.mu.Unlock()
	for _, cancel := range cancels {
		cancel()
	}
	for _, handle := range handles {
		handle.markDisposed()
		if handle.session == nil {
			continue
		}
		disposeCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		_ = handle.session.Dispose(disposeCtx)
		cancel()
	}
}

func (handle *serviceHandle) markDisposed() {
	handle.mu.Lock()
	handle.disposed = true
	handle.mu.Unlock()
}

func unknownHandleError(id string) error {
	return &ServiceError{Code: "unknown_handle", Message: fmt.Sprintf("unknown service handle %q", id)}
}

func invalidServiceRequest(err error) error {
	return &ServiceError{Code: "invalid_service_request", Message: err.Error()}
}

func serviceProtocolError(err error) *protocolError {
	var service *ServiceError
	if errors.As(err, &service) {
		return &protocolError{Code: service.Code, Message: service.Message}
	}
	var wire *protocolError
	if errors.As(err, &wire) {
		return wire
	}
	switch {
	case errors.Is(err, context.Canceled):
		return &protocolError{Code: "cancelled", Message: err.Error()}
	case errors.Is(err, context.DeadlineExceeded):
		return &protocolError{Code: "deadline_exceeded", Message: err.Error()}
	}
	return &protocolError{Code: "service_error", Message: err.Error()}
}
