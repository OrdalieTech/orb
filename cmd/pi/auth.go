package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"sync"

	aiauth "github.com/OrdalieTech/pi-go/ai/auth"
	"github.com/OrdalieTech/pi-go/ai/auth/oauth"
	"github.com/OrdalieTech/pi-go/codingagent/config"
)

func runAuthCommand(ctx context.Context, args CLIArgs, streams cliStreams) int {
	if args.Command == "logout" && len(args.CommandArgs) == 0 {
		args.CommandArgs = []string{"anthropic"}
	}
	if len(args.CommandArgs) != 1 {
		return reportCLIError(streams.Stderr, fmt.Errorf("usage: pi %s anthropic", args.Command))
	}
	provider := strings.ToLower(args.CommandArgs[0])
	if args.Command != "logout" && provider != "anthropic" {
		return reportCLIError(streams.Stderr, fmt.Errorf("provider %q does not support headless login yet", provider))
	}
	agentDir, err := config.GetAgentDir()
	if err != nil {
		return reportCLIError(streams.Stderr, err)
	}
	if _, err := config.MigrateAuthToAuthJSON(agentDir); err != nil {
		return reportCLIError(streams.Stderr, err)
	}
	storage, err := config.NewAuthStorage(filepath.Join(agentDir, "auth.json"))
	if err != nil {
		return reportCLIError(streams.Stderr, err)
	}
	if args.Command == "logout" {
		if err := storage.Delete(ctx, provider); err != nil {
			return reportCLIError(streams.Stderr, err)
		}
		_, _ = fmt.Fprintf(streams.Stdout, "Logged out of %s.\n", provider)
		return 0
	}

	interaction := newHeadlessAuthInteraction(streams.Stdin, streams.Stdout, streams.Stderr)
	credential, err := oauth.NewAnthropic(nil).Login(ctx, interaction)
	if err != nil {
		return reportCLIError(streams.Stderr, err)
	}
	if _, err := storage.Modify(ctx, provider, func(*aiauth.Credential) (*aiauth.Credential, error) {
		return credential, nil
	}); err != nil {
		return reportCLIError(streams.Stderr, err)
	}
	_, _ = fmt.Fprintf(streams.Stdout, "Logged in to %s. Credentials saved to %s.\n", provider, storage.Path())
	return 0
}

type headlessAuthInteraction struct {
	reader *bufio.Reader
	out    io.Writer
	err    io.Writer
	mu     sync.Mutex
}

func newHeadlessAuthInteraction(input io.Reader, output, errorOutput io.Writer) *headlessAuthInteraction {
	return &headlessAuthInteraction{reader: bufio.NewReader(input), out: output, err: errorOutput}
}

func (interaction *headlessAuthInteraction) Prompt(ctx context.Context, prompt aiauth.AuthPrompt) (string, error) {
	interaction.mu.Lock()
	defer interaction.mu.Unlock()
	_, _ = fmt.Fprintln(interaction.err, prompt.Message)
	result := make(chan struct {
		value string
		err   error
	}, 1)
	go func() {
		value, err := interaction.reader.ReadString('\n')
		if err != nil && err != io.EOF {
			result <- struct {
				value string
				err   error
			}{err: err}
			return
		}
		result <- struct {
			value string
			err   error
		}{value: strings.TrimRight(value, "\r\n")}
	}()
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case resolved := <-result:
		return resolved.value, resolved.err
	}
}

func (interaction *headlessAuthInteraction) Notify(event aiauth.AuthEvent) {
	switch event.Type {
	case aiauth.EventAuthURL:
		if event.Instructions != "" {
			_, _ = fmt.Fprintln(interaction.out, event.Instructions)
		}
		_, _ = fmt.Fprintln(interaction.out, event.URL)
	case aiauth.EventProgress, aiauth.EventInfo:
		_, _ = fmt.Fprintln(interaction.out, event.Message)
	case aiauth.EventDeviceCode:
		_, _ = fmt.Fprintf(interaction.out, "%s\n%s\n", event.VerificationURI, event.UserCode)
	}
}
