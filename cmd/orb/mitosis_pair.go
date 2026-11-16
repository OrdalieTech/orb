package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"runtime"
	"slices"
	"strings"
	"time"

	"github.com/OrdalieTech/orb/internal/qr"
	"github.com/OrdalieTech/orb/mitosis"
	"github.com/OrdalieTech/orb/mitosis/protocol"
	"github.com/OrdalieTech/orb/platforms/native/mitosis/daemon"
)

// runMitosisPair is pairing in one command: a QR code a phone photographs, then an explicit
// yes on this terminal for the exact fingerprint that claimed it. Nothing is trusted before.
func runMitosisPair(ctx context.Context, profile string, streams cliStreams) int {
	client, err := startedMitosisAdmin(ctx, profile)
	if err != nil {
		return reportCLIError(streams.Stderr, err)
	}
	defer func() { _ = client.Close() }()
	var inv mitosis.Invitation
	if err = client.Call(ctx, "invite", map[string]any{"grants": []mitosis.Grant{mitosis.FullGrant("")}}, &inv); err != nil {
		return reportCLIError(streams.Stderr, err)
	}
	code := mitosis.InvitationCode(inv)
	if q, err := qr.Encode([]byte(code)); err == nil {
		_, _ = fmt.Fprint(streams.Stdout, q.Terminal())
	}
	_, _ = fmt.Fprintf(streams.Stdout, "\nScan with the Orb app, or on another machine run:\n  orb mitosis join %s\n\nThis Orb   %s\nExpires in 10 minutes · Ctrl-C cancels\n", code, inv.PeerID)
	claimed, err := pollMitosisPairing(ctx, inv.Expires, func(ctx context.Context) (mitosis.Invitation, bool, error) {
		var status mitosisSettingsStatus
		if err := client.Call(ctx, "status", struct{}{}, &status); err != nil {
			return mitosis.Invitation{}, false, err
		}
		for _, i := range status.Pending {
			if i.ID == inv.ID {
				return i, i.Claimant != "", nil
			}
		}
		return mitosis.Invitation{}, false, errors.New("invitation expired; run orb mitosis pair again")
	})
	if err != nil {
		return reportCLIError(streams.Stderr, err)
	}
	in := bufio.NewReader(io.LimitReader(streams.Stdin, 256))
	_, _ = fmt.Fprintf(streams.Stdout, "\nA device claimed the code:\n  %s\nCheck the device shows the same fingerprint.\nGive it full control of this Orb's conversations, and starting Orb in any folder here? [y/N] ", claimed.Claimant)
	if !yes(in, false) {
		_, _ = fmt.Fprintln(streams.Stdout, "Not paired.")
		return 1
	}
	if err = client.Call(ctx, "approve", map[string]string{"invitation_id": claimed.ID, "claimant": claimed.Claimant}, nil); err != nil {
		if mitosis.Code(err) == "identity_conflict" {
			err = errors.New("two devices used this code, so someone else saw it: not paired.\nRun orb mitosis pair again where only you can see the code")
		}
		return reportCLIError(streams.Stderr, err)
	}
	_, _ = fmt.Fprintln(streams.Stdout, "Paired. The device reconnects on its own from now on; orb mitosis block <fingerprint> revokes it.")
	shareOrbsOnMitosis(ctx, profile)
	// On a server, pairing is only half the job: the Mitosis has to outlive this SSH session.
	if runtime.GOOS == "linux" && !daemon.Installed(profile) {
		if _, err := exec.LookPath("systemctl"); err == nil {
			_, _ = fmt.Fprint(streams.Stdout, "Keep Mitosis running after you log out and across reboots (systemd user service)? [Y/n] ")
			if yes(in, true) {
				note, err := daemon.Install(ctx, stateFromContext(ctx), profile)
				if err != nil {
					return reportCLIError(streams.Stderr, err)
				}
				_, _ = fmt.Fprintln(streams.Stdout, strings.TrimSpace("Mitosis runs as a service now.\n"+note))
			}
		}
	}
	return 0
}

// yes reads one answer line; an empty line (or none) is the default.
func yes(in *bufio.Reader, def bool) bool {
	line, _ := in.ReadString('\n')
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return true
	case "":
		return def && line != ""
	}
	return false
}

// runMitosisJoin claims an invitation, waits for the inviter's yes, then trusts it back. Joining
// hands the inviter this Orb's conversations, so it is never done without the owner's yes here:
// an invitation is only someone else's claim of who they are.
func runMitosisJoin(ctx context.Context, profile string, args []string, streams cliStreams) int {
	agreed := slices.Contains(args, "--yes")
	args = slices.DeleteFunc(slices.Clone(args), func(a string) bool { return a == "--yes" })
	text := strings.Join(args, "")
	if text == "" && !agreed {
		return reportCLIError(streams.Stderr, errors.New("pass the code as an argument to confirm the pairing here, or --yes to join without asking"))
	}
	if text == "" {
		raw, err := io.ReadAll(io.LimitReader(streams.Stdin, protocol.MaxFrame+1))
		if err != nil {
			return reportCLIError(streams.Stderr, err)
		}
		text = string(raw)
	}
	inv, err := mitosis.ParseInvitation(text)
	if err != nil {
		return reportCLIError(streams.Stderr, err)
	}
	client, err := startedMitosisAdmin(ctx, profile)
	if err != nil {
		return reportCLIError(streams.Stderr, err)
	}
	defer func() { _ = client.Close() }()
	var status mitosisSettingsStatus
	if err = client.Call(ctx, "status", struct{}{}, &status); err != nil {
		return reportCLIError(streams.Stderr, err)
	}
	if !agreed {
		_, _ = fmt.Fprintf(streams.Stdout, "Pair with %s?\nIt will read and drive this Orb's conversations, current and future (not start or update Orb here).\nOnce it approves this Orb, you get the same over its conversations. [y/N] ", inv.PeerID)
		if !yes(bufio.NewReader(io.LimitReader(streams.Stdin, 256)), false) {
			_, _ = fmt.Fprintln(streams.Stdout, "Not joined.")
			return 1
		}
	}
	if err = client.Call(ctx, "join", inv, nil); err != nil {
		if mitosis.Code(err) == "identity_conflict" {
			err = errors.New("another device already used this code: ask for a new one")
		}
		return reportCLIError(streams.Stderr, err)
	}
	_, _ = fmt.Fprintf(streams.Stdout, "Waiting for approval on %s\nThis Orb   %s\n", inv.PeerID, status.PeerID)
	_, err = pollMitosisPairing(ctx, inv.Expires, func(ctx context.Context) (mitosis.Invitation, bool, error) {
		var i mitosis.Invitation
		err := client.Call(ctx, "remote", map[string]any{"peer_id": inv.PeerID, "method": "pair.status", "params": map[string]string{"invitation_id": inv.ID}}, &i)
		return i, i.Status == "approved", err
	})
	if err == nil {
		err = trustMitosisPeer(ctx, client, profile, inv.PeerID, false)
	}
	if err != nil {
		return reportCLIError(streams.Stderr, err)
	}
	_, _ = fmt.Fprintf(streams.Stdout, "Paired with %s\n", inv.PeerID)
	return 0
}

// shareOrbsOnMitosis turns the mitosis plugin on once this machine pairs, unless its owner set it
// either way: a paired machine is one whose Orbs its owner means to reach from the others, the
// Orbs opened in a terminal there included. Only for the profile the plugin attaches to: pairing
// another profile must not start a second, unpaired Mitosis behind every later Orb.
func shareOrbsOnMitosis(ctx context.Context, profile string) {
	if profile != "personal" {
		return
	}
	cwd, agentDir, err := packageCommandDirs()
	if err != nil {
		return
	}
	settings, _, err := createCommandSettingsManager(ctx, cwd, agentDir, nil, false)
	if err != nil {
		return
	}
	if _, set := settings.GetPlugins()["mitosis"]; !set {
		settings.SetPluginEnabled("mitosis", true)
		_ = settings.DrainErrors()
	}
}

func startedMitosisAdmin(ctx context.Context, profile string) (*protocol.Conn, error) {
	if err := startMitosis(ctx, profile, true); err != nil {
		return nil, err
	}
	return mitosisAdmin(ctx, profile)
}

// pollMitosisPairing checks once a second until done reports true or the invitation expires.
func pollMitosisPairing(ctx context.Context, expires int64, poll func(context.Context) (mitosis.Invitation, bool, error)) (mitosis.Invitation, error) {
	ctx, cancel := context.WithDeadline(ctx, time.Unix(expires, 0))
	defer cancel()
	for {
		i, done, err := poll(ctx)
		if done || err != nil {
			return i, err
		}
		select {
		case <-ctx.Done():
			return mitosis.Invitation{}, errors.New("invitation expired; run orb mitosis pair again")
		case <-time.After(time.Second):
		}
	}
}
