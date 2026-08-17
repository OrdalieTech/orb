package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/OrdalieTech/orb/agent"
	"github.com/OrdalieTech/orb/agent/config"
	"github.com/OrdalieTech/orb/ai"
	aiauth "github.com/OrdalieTech/orb/ai/auth"
	"github.com/OrdalieTech/orb/ai/providers"
)

type credentialPrintKind string

type credentialPrintError string

func (err credentialPrintError) Error() string { return string(err) }

const (
	credentialPrintAPIKey credentialPrintKind = "api_key"
	credentialPrintBearer credentialPrintKind = "bearer_token"

	defaultBearerTokenMinExpiry = 30 * time.Minute
)

var (
	credentialDurationPattern = regexp.MustCompile(`(?i)^(\d+)(ms|s|m|h)$`)
	bearerTokenPattern        = regexp.MustCompile(`(?i)^Bearer\s+(.+)$`)
)

type credentialPrintCommand struct {
	kind      credentialPrintKind
	args      []string
	minExpiry *time.Duration
}

const credentialPrintHelp = `Usage:
  orb auth print-api-key --model <model> [--provider <provider>]
  orb auth print-bearer-token --model <model> [--provider <provider>] [--min-expiry <duration>]

Prints the configured credential alone on stdout. Provider inference uses configured credentials; specify --provider to select explicitly. Bearer tokens have a 30-minute minimum expiry by default. --min-expiry accepts ms, s, m, or h (for example, 30m).
`

func handleCredentialPrintCommand(ctx context.Context, argv []string, streams cliStreams) (bool, int) {
	if len(argv) == 0 || argv[0] != "auth" {
		return false, 0
	}
	if len(argv) == 1 || argv[1] == "help" || argv[1] == "--help" || argv[1] == "-h" {
		_, _ = io.WriteString(streams.Stdout, credentialPrintHelp)
		return true, 0
	}
	command, err := parseCredentialPrintCommand(argv)
	if err != nil {
		return true, reportCredentialPrintError(streams.Stderr, err, "Failed to parse auth command")
	}
	args := ParseArgs(command.args)
	if len(args.Diagnostics) > 0 {
		for _, diagnostic := range args.Diagnostics {
			_, _ = fmt.Fprintln(streams.Stderr, "Error: "+diagnostic.Message)
		}
		return true, 1
	}
	if err := validateCredentialPrintArgs(args); err != nil {
		return true, reportCredentialPrintError(streams.Stderr, err, "Failed to resolve credential")
	}
	agentDir, err := config.GetAgentDir()
	if err != nil {
		return true, reportCredentialPrintError(streams.Stderr, err, "Failed to resolve credential")
	}
	if _, err := config.MigrateAuthToAuthJSON(agentDir); err != nil {
		return true, reportCredentialPrintError(streams.Stderr, err, "Failed to resolve credential")
	}
	storage, err := config.NewAuthStorage(filepath.Join(agentDir, "auth.json"))
	if err != nil {
		return true, reportCredentialPrintError(streams.Stderr, err, "Failed to resolve credential")
	}
	registry, err := config.NewOfflineModelRegistry(agentDir)
	if err != nil {
		return true, reportCredentialPrintError(streams.Stderr, err, "Failed to resolve credential")
	}
	value, err := resolveCredentialForPrint(ctx, args, registry, storage, command)
	if err != nil {
		return true, reportCredentialPrintError(streams.Stderr, err, "Failed to resolve credential")
	}
	_, _ = fmt.Fprintln(streams.Stdout, value)
	return true, 0
}

func parseCredentialPrintCommand(argv []string) (credentialPrintCommand, error) {
	command := credentialPrintCommand{}
	switch argv[1] {
	case "print-api-key":
		command.kind = credentialPrintAPIKey
	case "print-bearer-token":
		command.kind = credentialPrintBearer
	default:
		return command, credentialPrintError(fmt.Sprintf(
			`Unknown auth command %q. Use "orb auth print-api-key" or "orb auth print-bearer-token".`,
			argv[1],
		))
	}
	for index := 2; index < len(argv); index++ {
		if argv[index] != "--min-expiry" {
			command.args = append(command.args, argv[index])
			continue
		}
		if command.kind != credentialPrintBearer {
			return command, credentialPrintError("--min-expiry is only supported by print-bearer-token")
		}
		index++
		if index == len(argv) {
			return command, credentialPrintError("--min-expiry must use a duration such as 30m or 1h")
		}
		duration, err := parseCredentialDuration(argv[index])
		if err != nil {
			return command, err
		}
		command.minExpiry = &duration
	}
	return command, nil
}

func parseCredentialDuration(value string) (time.Duration, error) {
	match := credentialDurationPattern.FindStringSubmatch(value)
	if match == nil {
		return 0, credentialPrintError("--min-expiry must use a duration such as 30m or 1h")
	}
	amount, err := strconv.ParseUint(match[1], 10, 63)
	if err != nil {
		return 0, credentialPrintError("--min-expiry must use a duration such as 30m or 1h")
	}
	unit := time.Millisecond
	switch strings.ToLower(match[2]) {
	case "s":
		unit = time.Second
	case "m":
		unit = time.Minute
	case "h":
		unit = time.Hour
	}
	if amount > uint64((1<<63-1)/unit) {
		return 0, credentialPrintError("--min-expiry must use a duration such as 30m or 1h")
	}
	return time.Duration(amount) * unit, nil
}

func validateCredentialPrintArgs(args CLIArgs) error {
	if args.Model == nil || strings.TrimSpace(*args.Model) == "" {
		return credentialPrintError("Credential printing requires --model <model>")
	}
	if args.APIKey != nil {
		return credentialPrintError("Credential printing reads configured credentials; --api-key is not supported")
	}
	if len(args.Messages) > 0 || len(args.FileArgs) > 0 || len(args.UnknownFlags) > 0 {
		return credentialPrintError("Credential printing only accepts --provider and --model")
	}
	return nil
}

func reportCredentialPrintError(writer io.Writer, err error, fallback string) int {
	var expected credentialPrintError
	if errors.As(err, &expected) {
		return reportCLIError(writer, expected)
	}
	return reportCLIError(writer, errors.New(fallback))
}

func resolveCredentialForPrint(
	ctx context.Context,
	args CLIArgs,
	registry *config.ModelRegistry,
	storage *config.AuthStorage,
	command credentialPrintCommand,
) (string, error) {
	stored, err := storage.List(ctx)
	if err != nil {
		return "", err
	}
	credentialTypes := make(map[string]aiauth.CredentialType, len(stored))
	for _, credential := range stored {
		credentialTypes[credential.ProviderID] = credential.Type
	}
	models, err := credentialPrintModels(args, registry.Models(), credentialTypes)
	if err != nil {
		return "", err
	}
	type resolvedCredential struct {
		provider string
		value    string
	}
	resolved := make([]resolvedCredential, 0, len(models))
	for _, model := range models {
		credentialType := credentialTypes[string(model.Provider)]
		if command.kind == credentialPrintAPIKey && credentialType == aiauth.CredentialOAuth {
			continue
		}
		if command.kind == credentialPrintBearer && credentialType != aiauth.CredentialOAuth {
			continue
		}
		var overrides *aiauth.ResolutionOverrides
		if command.kind == credentialPrintBearer {
			minExpiry := defaultBearerTokenMinExpiry
			if command.minExpiry != nil {
				minExpiry = *command.minExpiry
			}
			overrides = &aiauth.ResolutionOverrides{MinOAuthValidity: &minExpiry}
		}
		authResult, authErr := registry.ResolveProviderAuthWithOverrides(
			ctx,
			string(model.Provider),
			nil,
			overrides,
		)
		if authErr != nil {
			return "", authErr
		}
		value := credentialValue(command.kind, authResult)
		if value != "" {
			resolved = append(resolved, resolvedCredential{provider: string(model.Provider), value: value})
		}
	}
	if len(resolved) == 1 {
		return resolved[0].value, nil
	}
	provider := string(models[0].Provider)
	if len(resolved) == 0 {
		credentialType := credentialTypes[provider]
		if args.Provider != nil && *args.Provider != "" && command.kind == credentialPrintAPIKey && credentialType == aiauth.CredentialOAuth {
			return "", credentialPrintError(fmt.Sprintf("Provider %q is configured with OAuth, not an API key", provider))
		}
		if args.Provider != nil && *args.Provider != "" && command.kind == credentialPrintBearer && credentialType != aiauth.CredentialOAuth {
			return "", credentialPrintError(fmt.Sprintf("Provider %q is not configured with an OAuth bearer token", provider))
		}
		if command.kind == credentialPrintAPIKey {
			return "", credentialPrintError("No usable API key is configured")
		}
		return "", credentialPrintError("No usable OAuth bearer token is configured")
	}
	providers := make([]string, 0, len(resolved))
	for _, credential := range resolved {
		providers = append(providers, credential.provider)
	}
	return "", credentialPrintError(fmt.Sprintf(
		"Model %q has multiple configured providers (%s). Specify --provider.",
		*args.Model,
		strings.Join(providers, ", "),
	))
}

func credentialPrintModels(
	args CLIArgs,
	available []ai.Model,
	credentialTypes map[string]aiauth.CredentialType,
) ([]ai.Model, error) {
	if args.Provider != nil && *args.Provider != "" {
		resolution := agent.ResolveCLIModel(*args.Provider, *args.Model, nil, available)
		if resolution.Error != "" || resolution.Model == nil {
			if resolution.Error != "" {
				return nil, credentialPrintError(resolution.Error)
			}
			return nil, credentialPrintError("Unable to resolve the requested provider/model")
		}
		return []ai.Model{*resolution.Model}, nil
	}
	seen := make(map[string]struct{})
	models := make([]ai.Model, 0)
	for _, candidate := range available {
		provider := string(candidate.Provider)
		if _, ok := seen[provider]; ok {
			continue
		}
		seen[provider] = struct{}{}
		if _, ok := credentialTypes[provider]; !ok {
			continue
		}
		resolution := agent.ResolveCLIModel(provider, *args.Model, nil, available)
		if resolution.Model == nil || resolution.Error != "" || strings.Contains(resolution.Warning, "Using custom model id") {
			continue
		}
		models = append(models, *resolution.Model)
	}
	if len(models) == 0 {
		return nil, credentialPrintError(fmt.Sprintf(
			"Model %q not found. Use --list-models to see available models.",
			*args.Model,
		))
	}
	return models, nil
}

func credentialValue(kind credentialPrintKind, result *aiauth.AuthResult) string {
	if result == nil {
		return ""
	}
	if result.Auth.APIKey != nil {
		return *result.Auth.APIKey
	}
	if kind != credentialPrintBearer {
		return ""
	}
	for name, value := range result.Auth.Headers {
		if !strings.EqualFold(name, "authorization") || value == nil {
			continue
		}
		if match := bearerTokenPattern.FindStringSubmatch(*value); match != nil {
			return match[1]
		}
	}
	return ""
}

func runAuthCommand(ctx context.Context, args CLIArgs, streams cliStreams) int {
	if len(args.CommandArgs) > 1 || (args.Command != "logout" && len(args.CommandArgs) == 0) {
		return reportCLIError(streams.Stderr, fmt.Errorf("usage: orb %s <provider>", args.Command))
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
		// Bare `orb logout` lists the stored credentials instead of silently
		// picking a provider; removal always names its target explicitly.
		stored, err := storage.List(ctx)
		if err != nil {
			return reportCLIError(streams.Stderr, err)
		}
		names := make([]string, 0, len(stored))
		for _, credential := range stored {
			names = append(names, credential.ProviderID)
		}
		_, _ = fmt.Fprintln(streams.Stderr, "usage: orb logout <provider>")
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
