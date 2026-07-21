package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/OrdalieTech/pigo/ai"
	aiauth "github.com/OrdalieTech/pigo/ai/auth"
	"github.com/OrdalieTech/pigo/ai/providers"
	"github.com/OrdalieTech/pigo/codingagent/config"
)

func runAuthCommand(ctx context.Context, args CLIArgs, streams cliStreams) int {
	if len(args.CommandArgs) > 1 || (args.Command != "logout" && len(args.CommandArgs) == 0) {
		return reportCLIError(streams.Stderr, fmt.Errorf("usage: pigo %s <provider>", args.Command))
	}
	provider := ""
	if len(args.CommandArgs) == 1 {
		provider = strings.ToLower(args.CommandArgs[0])
	}
	var method aiauth.OAuth
	if args.Command != "logout" {
		definition, known := providers.Get(ai.ProviderID(provider))
		if !known || definition.Methods.OAuth == nil {
			return reportCLIError(streams.Stderr, fmt.Errorf("provider %q does not support headless login yet", provider))
		}
		method = definition.Methods.OAuth
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
	if args.Command == "logout" && provider == "" {
		// Bare `pigo logout` lists the stored credentials instead of silently
		// picking a provider; removal always names its target explicitly.
		stored, err := storage.List(ctx)
		if err != nil {
			return reportCLIError(streams.Stderr, err)
		}
		names := make([]string, 0, len(stored))
		for _, credential := range stored {
			names = append(names, credential.ProviderID)
		}
		_, _ = fmt.Fprintln(streams.Stderr, "usage: pigo logout <provider>")
		if len(names) == 0 {
			_, _ = fmt.Fprintln(streams.Stderr, "No stored credentials.")
		} else {
			_, _ = fmt.Fprintf(streams.Stderr, "Stored credentials: %s\n", strings.Join(names, ", "))
		}
		return 1
	}
	if args.Command == "logout" {
		if err := storage.Delete(ctx, provider); err != nil {
			return reportCLIError(streams.Stderr, err)
		}
		_, _ = fmt.Fprintf(streams.Stdout, "Logged out of %s.\n", provider)
		return 0
	}

	interaction := newHeadlessAuthInteraction(streams.Stdin, streams.Stdout, streams.Stderr)
	credential, err := method.Login(ctx, interaction)
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
	if prompt.Type == aiauth.PromptSelect {
		for index, option := range prompt.Options {
			label := option.Label
			if option.Description != "" {
				label += " — " + option.Description
			}
			_, _ = fmt.Fprintf(interaction.err, "  %d) %s\n", index+1, label)
		}
	}
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
		if resolved.err != nil || prompt.Type != aiauth.PromptSelect {
			return resolved.value, resolved.err
		}
		return resolveSelectAnswer(prompt.Options, resolved.value)
	}
}

// resolveSelectAnswer maps a numbered choice (or a literal option id) typed on
// stdin to the option id expected by auth flows.
func resolveSelectAnswer(options []aiauth.PromptOption, answer string) (string, error) {
	trimmed := strings.TrimSpace(answer)
	if number, err := strconv.Atoi(trimmed); err == nil && number >= 1 && number <= len(options) {
		return options[number-1].ID, nil
	}
	for _, option := range options {
		if strings.EqualFold(option.ID, trimmed) {
			return option.ID, nil
		}
	}
	return "", fmt.Errorf("invalid selection %q", trimmed)
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
