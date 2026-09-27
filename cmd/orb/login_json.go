package main

import (
	"bufio"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"

	"github.com/OrdalieTech/orb/agent/config"
	aiauth "github.com/OrdalieTech/orb/ai/auth"
)

// runLoginJSON is /login for apps that draw their own screens, as JSON lines on stdout.
//
//	orb login --json                              one line per sign-in method, with its status
//	orb login --json <provider> [oauth|api_key]   runs that method: events and prompts out,
//	                                              one answer line in per prompt; closing stdin cancels
func runLoginJSON(ctx context.Context, args []string, streams cliStreams) int {
	out := &jsonLines{enc: json.NewEncoder(streams.Stdout)}
	fail := func(err error) int { out.emit(map[string]string{"type": "error", "message": err.Error()}); return 1 }
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
			out.emit(row)
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
	interaction := &jsonAuthInteraction{out: out, answers: make(chan string), closed: make(chan struct{})}
	go func() {
		// Once stdin closes, prompts fail: an app that goes away leaves no listener behind.
		defer close(interaction.closed)
		lines := bufio.NewScanner(io.LimitReader(streams.Stdin, 1<<20))
		lines.Buffer(make([]byte, 64<<10), 64<<10)
		for lines.Scan() {
			select {
			case interaction.answers <- lines.Text():
			case <-ctx.Done():
				return
			}
		}
	}()
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
	out.emit(map[string]any{"type": "done", "provider": provider, "auth": authType})
	return 0
}

type jsonLines struct {
	mu  sync.Mutex
	enc *json.Encoder
}

func (o *jsonLines) emit(v any) {
	o.mu.Lock()
	defer o.mu.Unlock()
	_ = o.enc.Encode(v)
}

// jsonAuthInteraction forwards a sign-in's events and prompts as JSON lines. Flows with a
// browser callback also prompt for a pasted code meanwhile; the callback winning cancels it.
type jsonAuthInteraction struct {
	out     *jsonLines
	answers chan string
	closed  chan struct{}
}

func (i *jsonAuthInteraction) Notify(event aiauth.AuthEvent) {
	row := map[string]any{"type": event.Type}
	for key, value := range map[string]any{"message": event.Message, "url": event.URL, "instructions": event.Instructions, "code": event.UserCode, "uri": event.VerificationURI, "expires": event.ExpiresInSeconds} {
		if value != "" && value != 0 {
			row[key] = value
		}
	}
	if len(event.Links) > 0 {
		links := make([]map[string]string, len(event.Links))
		for n, link := range event.Links {
			links[n] = map[string]string{"url": link.URL, "label": link.Label}
		}
		row["links"] = links
	}
	i.out.emit(row)
}

func (i *jsonAuthInteraction) Prompt(ctx context.Context, prompt aiauth.AuthPrompt) (string, error) {
	options := make([]map[string]string, len(prompt.Options))
	for n, option := range prompt.Options {
		options[n] = map[string]string{"id": option.ID, "label": option.Label, "description": option.Description}
	}
	i.out.emit(map[string]any{"type": "prompt", "kind": prompt.Type, "message": prompt.Message, "placeholder": prompt.Placeholder, "options": options})
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case <-i.closed:
		return "", context.Canceled
	case answer := <-i.answers:
		if prompt.Type == aiauth.PromptSelect {
			return resolveSelectAnswer(prompt.Options, answer)
		}
		return strings.TrimSpace(answer), nil
	}
}
