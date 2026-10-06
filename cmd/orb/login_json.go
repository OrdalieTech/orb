package main

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/OrdalieTech/orb/agent/config"
	aiauth "github.com/OrdalieTech/orb/ai/auth"
)

// runLoginJSON is /login for apps that draw their own screens, as JSON lines on stdout.
//
//	orb login --json                              one line per sign-in method, with its status
//	orb login --json <provider> [oauth|api_key]   runs that method: events and prompts out,
//	                                              one answer line in per prompt; closing stdin cancels
func runLoginJSON(ctx context.Context, args []string, streams cliStreams) int {
	out := aiauth.NewJSONLines(streams.Stdout)
	fail := func(err error) int { out.Emit(map[string]string{"type": "error", "message": err.Error()}); return 1 }
	agentDir, err := config.GetAgentDir()
	if err != nil {
		return fail(err)
	}
	if _, err = migrateAuthForContext(ctx, agentDir); err != nil {
		return fail(err)
	}
	storage, err := stateFromContext(ctx).auth(agentDir)
	if err != nil {
		return fail(err)
	}
	registry, err := stateFromContext(ctx).models(agentDir, storage, true)
	if err != nil {
		return fail(err)
	}
	options, err := authOptions(ctx, storage, registry, nil)
	if err != nil {
		return fail(err)
	}
	if len(args) == 0 {
		models := map[string]int{}
		for _, model := range registry.Available(nil) {
			models[string(model.Provider)]++
		}
		for _, option := range options.Login {
			label := "Sign in with an API key" // the labels /login shows
			if option.AuthType == aiauth.AuthTypeOAuth {
				label = cmp.Or(option.LoginLabel, "Sign in with an account")
			}
			row := map[string]any{"id": option.ID, "name": option.Name, "auth": option.AuthType, "method": option.MethodName, "label": label, "login": option.LoginAvailable, "models": models[option.ID]}
			if option.Status != nil {
				row["status"] = map[string]any{"type": option.Status.Type, "source": option.Status.Source}
			}
			out.Emit(row)
		}
		return 0
	}
	provider, authType := strings.ToLower(args[0]), aiauth.AuthType("")
	if len(args) > 1 {
		authType = aiauth.AuthType(args[1])
	}
	for _, option := range options.Login {
		if option.ID == provider && option.LoginAvailable && (authType == "" || authType == option.AuthType) {
			authType = option.AuthType // an account sign-in is listed first, as /login offers it first
			break
		}
	}
	if authType == "" {
		return fail(fmt.Errorf("provider %q has no sign-in here; configure it with its environment variables or models.json", provider))
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	interaction := aiauth.NewJSONInteraction(ctx, streams.Stdin, out)
	credential, err := loginCredential(ctx, registry, provider, authType, interaction)
	if err == nil {
		_, err = storage.Modify(ctx, provider, func(*aiauth.Credential) (*aiauth.Credential, error) { return credential, nil })
	}
	if err != nil {
		if errors.Is(err, context.Canceled) {
			err = errors.New("sign-in cancelled")
		}
		return fail(err)
	}
	out.Emit(map[string]any{"type": "done", "provider": provider, "auth": authType})
	return 0
}
