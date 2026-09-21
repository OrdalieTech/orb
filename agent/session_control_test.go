package agent

import (
	"context"
	"errors"
	"testing"

	sessionstore "github.com/OrdalieTech/orb/agent/session"
)

func TestControlFencesLocalSessionChangesAndPreservesRebind(t *testing.T) {
	ctx := context.Background()
	cwd := t.TempDir()
	manager, err := sessionstore.InMemory(cwd)
	if err != nil {
		t.Fatal(err)
	}
	p := testFaux(10000)
	host, err := NewAgentSessionRuntime(ctx, AgentSessionOptions{CWD: cwd, AgentDir: t.TempDir(), SessionManager: manager, Model: p.GetModel(), StreamFn: p.StreamSimple})
	if err != nil {
		t.Fatal(err)
	}
	defer host.Dispose(ctx)
	rebound := 0
	host.SetRebindSession(func(*AgentSession) error { rebound++; return nil })
	control, err := host.EnableControl()
	if err != nil {
		t.Fatal(err)
	}
	before := control.Target()
	if _, err = host.NewSession(ctx, nil); err != nil {
		t.Fatal(err)
	}
	if rebound != 1 || control.Target().SessionID == before.SessionID {
		t.Fatal("rebind lost")
	}
	if _, err = host.NewSession(WithControlTarget(ctx, before), nil); !errors.Is(err, ErrControlStale) {
		t.Fatal(err)
	}
	if err = control.Execution(before, "cancel", ""); !errors.Is(err, ErrControlStale) {
		t.Fatal(err)
	}
}

func TestControlObserverDoesNotReplaceOwnerCallback(t *testing.T) {
	ctx := context.Background()
	cwd := t.TempDir()
	manager, _ := sessionstore.InMemory(cwd)
	p := testFaux(1000)
	host, err := NewAgentSessionRuntime(ctx, AgentSessionOptions{CWD: cwd, AgentDir: t.TempDir(), SessionManager: manager, Model: p.GetModel(), StreamFn: p.StreamSimple})
	if err != nil {
		t.Fatal(err)
	}
	defer host.Dispose(ctx)
	n := 0
	unsubscribe := host.ObserveSessions(func(*AgentSession) { n++ })
	if n != 1 {
		t.Fatal(n)
	}
	if _, err = host.NewSession(ctx, nil); err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatal(n)
	}
	unsubscribe()
	if _, err = host.NewSession(ctx, nil); err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatal(n)
	}
}

func TestControlReservationFencesTransitionsAndQueueCallbacks(t *testing.T) {
	ctx := context.Background()
	cwd := t.TempDir()
	manager, _ := sessionstore.InMemory(cwd)
	p := testFaux(1000)
	h, err := NewAgentSessionRuntime(ctx, AgentSessionOptions{CWD: cwd, AgentDir: t.TempDir(), SessionManager: manager, Model: p.GetModel(), StreamFn: p.StreamSimple})
	if err != nil {
		t.Fatal(err)
	}
	defer h.Dispose(ctx)
	control, err := h.EnableControl()
	if err != nil {
		t.Fatal(err)
	}
	reserved, finish, err := h.Session().reserveControl(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer finish()
	nested, end, err := h.Session().reserveControl(reserved)
	_ = nested
	if err != nil {
		t.Fatal(err)
	}
	end()
	if _, err = h.NewSession(ctx, nil); !errors.Is(err, ErrControlBusy) {
		t.Fatal(err)
	}
	target := control.Target()
	called := false
	stop := h.Session().Subscribe(func(any) { _ = control.Target(); called = true })
	defer stop()
	if err = control.Execution(target, "steer", "literal /bridge"); err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Fatal("queue callback missing")
	}
}
