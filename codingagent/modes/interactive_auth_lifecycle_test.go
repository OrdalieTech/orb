package modes

import (
	"context"
	"testing"
	"time"

	aiauth "github.com/OrdalieTech/pi-go/ai/auth"
	"github.com/OrdalieTech/pi-go/tui"
)

type cancellingAuthHost struct {
	InteractiveSessionHost
	started chan struct{}
	stopped chan struct{}
}

func (host *cancellingAuthHost) Login(ctx context.Context, _ string, _ aiauth.AuthType, _ aiauth.AuthInteraction) error {
	close(host.started)
	<-ctx.Done()
	close(host.stopped)
	return ctx.Err()
}

type authMutationHost struct {
	InteractiveSessionHost
	loginCalls  int
	logoutCalls int
	reloadCalls int
}

func (host *authMutationHost) Login(context.Context, string, aiauth.AuthType, aiauth.AuthInteraction) error {
	host.loginCalls++
	return nil
}

func (host *authMutationHost) Logout(context.Context, string) error {
	host.logoutCalls++
	return nil
}

func (host *authMutationHost) Reload(context.Context) error {
	host.reloadCalls++
	return nil
}

func TestInteractiveAuthMutatesCredentialsWithoutReloadingSession(t *testing.T) {
	host := &authMutationHost{}
	mode := &InteractiveMode{
		ui:      tui.NewTUI(newFakeTerminal(80, 24)),
		chat:    &tui.Container{},
		options: InteractiveModeOptions{Host: host},
	}

	mode.runAuthentication("groq", aiauth.AuthTypeAPIKey, false)
	mode.runAuthentication("groq", aiauth.AuthTypeAPIKey, true)

	if host.loginCalls != 1 || host.logoutCalls != 1 {
		t.Fatalf("login calls=%d logout calls=%d", host.loginCalls, host.logoutCalls)
	}
	if host.reloadCalls != 0 {
		t.Fatalf("auth triggered %d session reloads", host.reloadCalls)
	}
}

func TestInteractiveAuthStopsWithModeContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	host := &cancellingAuthHost{started: make(chan struct{}), stopped: make(chan struct{})}
	mode := &InteractiveMode{
		ui:          tui.NewTUI(newFakeTerminal(80, 24)),
		chat:        &tui.Container{},
		options:     InteractiveModeOptions{Host: host},
		authContext: ctx,
	}

	done := make(chan struct{})
	go func() {
		mode.runAuthentication("blocking", aiauth.AuthTypeAPIKey, false)
		close(done)
	}()
	select {
	case <-host.started:
	case <-time.After(time.Second):
		t.Fatal("login did not start")
	}
	cancel()
	select {
	case <-host.stopped:
	case <-time.After(time.Second):
		t.Fatal("login outlived the interactive mode context")
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("authentication goroutine did not return after cancellation")
	}
}
