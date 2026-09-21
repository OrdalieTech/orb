package agent

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"strconv"
	"sync"

	"github.com/OrdalieTech/orb/agent/extensions"
)

var ErrControlStale = errors.New("stale_target")
var ErrControlBusy = errors.New("busy")

type ControlTarget struct {
	SessionID   string `json:"session_id"`
	Revision    string `json:"session_revision"`
	ExecutionID string `json:"execution_id,omitempty"`
}
type controlTargetKey struct{}
type controlReservationKey struct{}

func WithControlTarget(ctx context.Context, target ControlTarget) context.Context {
	return context.WithValue(ctx, controlTargetKey{}, target)
}

// SessionControl coordinates optional non-owning controllers with local runtime
// transitions. Its locks protect admission only, never model or extension work.
type SessionControl struct {
	mu        sync.Mutex
	session   func() *SessionRuntime
	revision  uint64
	execution string
	changing  bool
}

func (h *AgentSessionRuntime) EnableControl() (*SessionControl, error) {
	h.opMu.Lock()
	defer h.opMu.Unlock()
	if c := h.control.Load(); c != nil {
		return c, nil
	}
	s := h.Session()
	if s == nil {
		return nil, ErrSessionDisposed
	}
	if !s.agent.IsIdle() {
		return nil, ErrControlBusy
	}
	c := &SessionControl{session: h.Session, revision: 1}
	s.control.Store(c)
	h.control.Store(c)
	return c, nil
}
func (c *SessionControl) targetLocked() ControlTarget {
	t := ControlTarget{Revision: strconv.FormatUint(c.revision, 10), ExecutionID: c.execution}
	if s := c.session(); s != nil {
		t.SessionID = s.Manager().GetSessionID()
	}
	return t
}
func (c *SessionControl) Target() ControlTarget {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.targetLocked()
}
func (c *SessionControl) checkLocked(target ControlTarget) error {
	current := c.targetLocked()
	if c.changing || current.SessionID != target.SessionID || current.Revision != target.Revision {
		return ErrControlStale
	}
	return nil
}
func (c *SessionControl) beginTransition(ctx context.Context) (func(), error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if c == nil {
		return func() {}, nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if target, ok := ctx.Value(controlTargetKey{}).(ControlTarget); ok {
		if err := c.checkLocked(target); err != nil {
			return nil, err
		}
	}
	owner, _ := ctx.Value(controlReservationKey{}).(string)
	if c.changing || c.execution != "" && owner != c.execution {
		return nil, ErrControlBusy
	}
	if c.revision == ^uint64(0) {
		return nil, errors.New("resource_exhausted")
	}
	previous := c.session()
	previousLeaf := controlLeaf(previous)
	c.changing = true
	var once sync.Once
	return func() {
		once.Do(func() {
			c.mu.Lock()
			if next := c.session(); next != previous || controlLeaf(next) != previousLeaf {
				c.revision++
				if next != nil {
					next.control.Store(c)
				}
			}
			c.changing = false
			c.mu.Unlock()
		})
	}, nil
}
func (s *SessionRuntime) beginControlRun(ctx context.Context) (func(), error) {
	c := s.control.Load()
	if c == nil {
		return func() {}, nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.session() != s || c.changing {
		return nil, ErrControlStale
	}
	if owner, _ := ctx.Value(controlReservationKey{}).(string); owner != "" && owner == c.execution {
		return func() {}, nil
	}
	if target, ok := ctx.Value(controlTargetKey{}).(ControlTarget); ok {
		if err := c.checkLocked(target); err != nil {
			return nil, err
		}
	}
	if c.execution != "" {
		return nil, ErrControlBusy
	}
	var id [16]byte
	_, _ = rand.Read(id[:])
	execution := base64.RawURLEncoding.EncodeToString(id[:])
	c.execution = execution
	return func() {
		c.mu.Lock()
		if c.execution == execution {
			c.execution = ""
		}
		c.mu.Unlock()
	}, nil
}

// PromptControlled preserves input/tool policy but excludes slash-command dispatch.
func (s *SessionRuntime) PromptControlled(ctx context.Context, text string) error {
	ctx, finish, err := s.reserveControl(ctx)
	if err != nil {
		return err
	}
	defer finish()
	return s.promptExtensionInput(ctx, text, nil, extensions.InputRPC, false, nil, true, nil)
}

func (c *SessionControl) Execution(target ControlTarget, method, text string) error {
	c.mu.Lock()
	defer func() {
		c.mu.Unlock()
		if s := c.session(); s != nil && (method == "steer" || method == "follow_up") {
			s.emitQueueUpdate()
		}
	}()
	if err := c.checkLocked(target); err != nil {
		return err
	}
	if target.ExecutionID == "" || target.ExecutionID != c.execution {
		return ErrControlStale
	}
	s := c.session()
	switch method {
	case "cancel":
		s.Abort()
		return nil
	case "steer":
		return s.queueControlled(text, false)
	case "follow_up":
		return s.queueControlled(text, true)
	default:
		return errors.New("unsupported execution method")
	}
}

// ObserveSessions adds a non-owning observer without replacing the assembly's
// rebind callback. The callback must not initiate another session transition.
func (h *AgentSessionRuntime) ObserveSessions(observe func(*AgentSession)) func() {
	if observe == nil {
		return func() {}
	}
	h.opMu.Lock()
	h.mu.Lock()
	h.nextObserver++
	id := h.nextObserver
	if h.sessionObservers == nil {
		h.sessionObservers = map[uint64]func(*AgentSession){}
	}
	h.sessionObservers[id] = observe
	current := h.session
	h.mu.Unlock()
	if current != nil {
		observe(current)
	}
	h.opMu.Unlock()
	return func() { h.mu.Lock(); delete(h.sessionObservers, id); h.mu.Unlock() }
}

// NewSessionControl attaches to a host with an existing concrete session lifecycle.
// The host must bracket every replacement with BeginTransition.
func NewSessionControl(current func() *SessionRuntime) (*SessionControl, error) {
	if current == nil || current() == nil {
		return nil, ErrSessionDisposed
	}
	s := current()
	if !s.agent.IsIdle() {
		return nil, ErrControlBusy
	}
	c := &SessionControl{session: current, revision: 1}
	s.control.Store(c)
	return c, nil
}
func (c *SessionControl) BeginTransition(ctx context.Context) (func(), error) {
	return c.beginTransition(ctx)
}

// Controlled input is literal; queued slash expansion is a local-only surface.
func (s *SessionRuntime) queueControlled(text string, follow bool) error {
	if err := s.checkLive(); err != nil {
		return err
	}
	message := userMessageWithImagesAt(text, nil, s.clock())
	s.mu.Lock()
	if follow {
		s.followUps = append(s.followUps, text)
	} else {
		s.steering = append(s.steering, text)
	}
	s.mu.Unlock()
	if follow {
		s.agent.FollowUp(message)
	} else {
		s.agent.Steer(message)
	}
	return nil
}

func (s *SessionRuntime) reserveControl(ctx context.Context) (context.Context, func(), error) {
	if ctx == nil {
		ctx = context.Background()
	}
	c := s.control.Load()
	if c == nil {
		return ctx, func() {}, nil
	}
	finish, err := s.beginControlRun(ctx)
	if err != nil {
		return ctx, nil, err
	}
	c.mu.Lock()
	id := c.execution
	c.mu.Unlock()
	return context.WithValue(ctx, controlReservationKey{}, id), finish, nil
}

func controlLeaf(s *SessionRuntime) string {
	if s != nil {
		if leaf := s.Manager().GetLeafID(); leaf != nil {
			return *leaf
		}
	}
	return ""
}
