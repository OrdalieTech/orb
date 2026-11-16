package main

import (
	"context"
	"fmt"
	"slices"

	"github.com/OrdalieTech/orb/ai/auth"
)

// runMitosisSync shares this device's credentials with the profile's peers and
// applies theirs: it reads the local auth store, sends it to the Mitosis service to
// seal to the scope, and merges back whatever the service has sealed to this device,
// keeping local-only logins. Secrets are sealed to each peer's key and never copied
// in the clear. Pass --pull to only receive, --push to only send.
func runMitosisSync(ctx context.Context, profile string, args []string, streams cliStreams) int {
	pull := slices.Contains(args, "--pull")
	push := slices.Contains(args, "--push")
	if !pull && !push {
		pull, push = true, true // default: both directions
	}

	_, agentDir, err := packageCommandDirs()
	if err != nil {
		return reportCLIError(streams.Stderr, err)
	}
	authStore, err := stateFromContext(ctx).Auth(agentDir)
	if err != nil {
		return reportCLIError(streams.Stderr, err)
	}

	var local []byte
	if push {
		if local, err = authStore.Export(ctx); err != nil {
			return reportCLIError(streams.Stderr, err)
		}
	}

	client, err := startedBridgeAdmin(ctx, profile)
	if err != nil {
		return reportCLIError(streams.Stderr, err)
	}
	defer func() { _ = client.Close() }()

	var resp struct {
		Found       bool   `json:"found"`
		Credentials []byte `json:"credentials"`
	}
	if err := client.Call(ctx, "mitosis.sync", map[string]any{"credentials": local}, &resp); err != nil {
		return reportCLIError(streams.Stderr, err)
	}

	if push {
		_, _ = fmt.Fprintln(streams.Stdout, "Shared this device's credentials with the profile's peers.")
	}
	if !pull || !resp.Found || len(resp.Credentials) == 0 {
		if pull && !resp.Found {
			_, _ = fmt.Fprintln(streams.Stdout, "No credentials are sealed to this device yet. Run sync on your other devices too.")
		}
		return 0
	}

	before, _ := authStore.Export(ctx)
	if err := authStore.MergeIn(ctx, resp.Credentials); err != nil {
		return reportCLIError(streams.Stderr, err)
	}
	_, _ = fmt.Fprintf(streams.Stdout, "Applied %s from the profile.\n", describeProviders(before, resp.Credentials))
	return 0
}

// describeProviders names the providers an overlay adds or changes relative to what
// was already stored, for a human-readable sync summary.
func describeProviders(before, overlay []byte) string {
	prev, _ := auth.ParseCredentials(before)
	next, _ := auth.ParseCredentials(overlay)
	var added, updated []string
	for name := range next {
		if _, had := prev[name]; had {
			updated = append(updated, name)
		} else {
			added = append(added, name)
		}
	}
	slices.Sort(added)
	slices.Sort(updated)
	switch {
	case len(added) == 0 && len(updated) == 0:
		return "no credentials"
	case len(updated) == 0:
		return fmt.Sprintf("%d credential(s): %v", len(added), added)
	case len(added) == 0:
		return fmt.Sprintf("%d updated credential(s): %v", len(updated), updated)
	default:
		return fmt.Sprintf("%d new %v and %d updated %v", len(added), added, len(updated), updated)
	}
}
