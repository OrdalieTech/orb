package modes

import (
	"context"
	"testing"

	aiauth "github.com/OrdalieTech/pi-go/ai/auth"
	"github.com/OrdalieTech/pi-go/tui"
)

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
