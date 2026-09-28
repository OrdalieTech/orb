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

	"github.com/OrdalieTech/orb/bridge"
	"github.com/OrdalieTech/orb/bridge/protocol"
	"github.com/OrdalieTech/orb/internal/qr"
)

// runBridgePair is pairing in one command: a QR code a phone photographs, then an explicit
// yes on this terminal for the exact fingerprint that claimed it. Nothing is trusted before.
func runBridgePair(ctx context.Context, profile string, streams cliStreams) int {
	client, err := startedBridgeAdmin(ctx, profile)
	if err != nil {
		return reportCLIError(streams.Stderr, err)
	}
	defer func() { _ = client.Close() }()
	var inv bridge.Invitation
	if err = client.Call(ctx, "invite", map[string]any{"grants": []bridge.Grant{fullBridgeGrant("")}}, &inv); err != nil {
		return reportCLIError(streams.Stderr, err)
	}
	code := bridgeInvitationCode(inv)
	if q, err := qr.Encode([]byte(code)); err == nil {
		_, _ = fmt.Fprint(streams.Stdout, q.Terminal())
	}
	_, _ = fmt.Fprintf(streams.Stdout, "\nScan with the Orb app, or on another machine run:\n  orb bridge join %s\n\nThis Orb   %s\nExpires in 10 minutes · Ctrl-C cancels\n", code, inv.PeerID)
	claimed, err := pollBridgePairing(ctx, inv.Expires, func(ctx context.Context) (bridge.Invitation, bool, error) {
		var status bridgeSettingsStatus
		if err := client.Call(ctx, "status", struct{}{}, &status); err != nil {
			return bridge.Invitation{}, false, err
		}
		for _, i := range status.Pending {
			if i.ID == inv.ID {
				return i, i.Claimant != "", nil
			}
		}
		return bridge.Invitation{}, false, errors.New("invitation expired; run orb bridge pair again")
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
		if bridge.Code(err) == "identity_conflict" {
			err = errors.New("two devices used this code, so someone else saw it: not paired.\nRun orb bridge pair again where only you can see the code")
		}
		return reportCLIError(streams.Stderr, err)
	}
	_, _ = fmt.Fprintln(streams.Stdout, "Paired. The device reconnects on its own from now on; orb bridge block <fingerprint> revokes it.")
	// On a server, pairing is only half the job: the Bridge has to outlive this SSH session.
	if runtime.GOOS == "linux" && !bridgeServiceInstalled(profile) {
		if _, err := exec.LookPath("systemctl"); err == nil {
			_, _ = fmt.Fprint(streams.Stdout, "Keep Bridge running after you log out and across reboots (systemd user service)? [Y/n] ")
			if yes(in, true) {
				note, err := installBridgeService(ctx, profile)
				if err != nil {
					return reportCLIError(streams.Stderr, err)
				}
				_, _ = fmt.Fprintln(streams.Stdout, strings.TrimSpace("Bridge runs as a service now.\n"+note))
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

// runBridgeJoin claims an invitation, waits for the inviter's yes, then trusts it back. Joining
// hands the inviter this Orb's conversations, so it is never done without the owner's yes here:
// an invitation is only someone else's claim of who they are.
func runBridgeJoin(ctx context.Context, profile string, args []string, streams cliStreams) int {
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
	inv, err := parseBridgeInvitation(text)
	if err != nil {
		return reportCLIError(streams.Stderr, err)
	}
	client, err := startedBridgeAdmin(ctx, profile)
	if err != nil {
		return reportCLIError(streams.Stderr, err)
	}
	defer func() { _ = client.Close() }()
	var status bridgeSettingsStatus
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
		if bridge.Code(err) == "identity_conflict" {
			err = errors.New("another device already used this code: ask for a new one")
		}
		return reportCLIError(streams.Stderr, err)
	}
	_, _ = fmt.Fprintf(streams.Stdout, "Waiting for approval on %s\nThis Orb   %s\n", inv.PeerID, status.PeerID)
	_, err = pollBridgePairing(ctx, inv.Expires, func(ctx context.Context) (bridge.Invitation, bool, error) {
		var i bridge.Invitation
		err := client.Call(ctx, "remote", map[string]any{"peer_id": inv.PeerID, "method": "pair.status", "params": map[string]string{"invitation_id": inv.ID}}, &i)
		return i, i.Status == "approved", err
	})
	if err == nil {
		err = trustBridgePeer(ctx, client, inv.PeerID, false)
	}
	if err != nil {
		return reportCLIError(streams.Stderr, err)
	}
	_, _ = fmt.Fprintf(streams.Stdout, "Paired with %s\n", inv.PeerID)
	return 0
}

func startedBridgeAdmin(ctx context.Context, profile string) (*protocol.Conn, error) {
	if err := startBridge(ctx, profile, true); err != nil {
		return nil, err
	}
	return bridgeAdmin(ctx, profile)
}

// pollBridgePairing checks once a second until done reports true or the invitation expires.
func pollBridgePairing(ctx context.Context, expires int64, poll func(context.Context) (bridge.Invitation, bool, error)) (bridge.Invitation, error) {
	ctx, cancel := context.WithDeadline(ctx, time.Unix(expires, 0))
	defer cancel()
	for {
		i, done, err := poll(ctx)
		if done || err != nil {
			return i, err
		}
		select {
		case <-ctx.Done():
			return bridge.Invitation{}, errors.New("invitation expired; run orb bridge pair again")
		case <-time.After(time.Second):
		}
	}
}
