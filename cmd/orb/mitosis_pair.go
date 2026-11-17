package main

import (
	"bufio"
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"slices"
	"strings"
	"syscall"
	"time"

	"golang.org/x/term"

	"github.com/OrdalieTech/orb/internal/qr"
	"github.com/OrdalieTech/orb/mitosis"
	"github.com/OrdalieTech/orb/mitosis/protocol"
	transport "github.com/OrdalieTech/orb/platforms/native/tailcat"
)

// runMitosisPair is `orb mitosis`: a code (and, on a terminal, a QR code a phone photographs),
// then one yes for the exact fingerprint that claimed it, and the device joins this Orb's sync
// group, which this Orb owns if it is in none. Nothing is trusted before the yes; with auto (an
// agent running it, say) the first device to claim the code gets it.
func runMitosisPair(ctx context.Context, profile string, auto bool, streams cliStreams) int {
	client, err := startedMitosisAdmin(ctx, profile)
	if err != nil {
		return reportCLIError(streams.Stderr, err)
	}
	defer func() { _ = client.Close() }()
	var inv mitosis.Invitation
	if err = client.Call(ctx, "invite", map[string]any{"grants": []mitosis.Grant{mitosis.FullGrant("")}}, &inv); err != nil {
		return reportCLIError(streams.Stderr, err)
	}
	// Interrupted, the invitation is withdrawn: its full code, printed below, must not stay usable.
	ctx, cancel := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	defer cancel()
	defer func() {
		done, stop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()
		_ = client.Call(done, "uninvite", map[string]string{"invitation_id": inv.ID}, nil) // an approved one stays
	}()
	code := mitosis.InvitationCode(inv)
	// The code, fingerprint and expiry below must stay on screen: the QR shows only on a terminal
	// it fits.
	scan := ""
	if q, err := qr.Encode([]byte(code)); err == nil {
		f, _ := streams.Stdout.(*os.File)
		if cols, rows, err := term.GetSize(int(f.Fd())); err == nil && cols >= len(q)+2 && rows >= (len(q)+3)/2+8 {
			_, _ = fmt.Fprint(streams.Stdout, q.Terminal())
			scan = "scan the QR code or "
		}
	}
	// A short code for a terminal, when the relay can carry it; apps take the full one.
	join := code
	if short, stop, err := offerShortCode(ctx, code); err == nil {
		defer stop()
		join = short
		_, _ = fmt.Fprintf(streams.Stdout, "\nIn an app or a browser tab, %spaste:\n%s\n", scan, code)
	}
	_, _ = fmt.Fprintf(streams.Stdout, "\nOn the other machine run:\n  orb mitosis join %s\n\nThis Orb   %s\nExpires in 10 minutes · Ctrl-C cancels\n", join, inv.PeerID)
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
		return mitosis.Invitation{}, false, errors.New("invitation expired; run orb mitosis again")
	})
	if err != nil && ctx.Err() != nil {
		_, _ = fmt.Fprintln(streams.Stderr, "Cancelled; the invitation is withdrawn.")
		return 130
	} else if err != nil {
		return reportCLIError(streams.Stderr, err)
	}
	if auto {
		_, _ = fmt.Fprintf(streams.Stdout, "\n%s claimed the code and joins unchecked (--yes); orb mitosis block %s cuts it off.\n", claimed.Claimant, claimed.Claimant)
	} else if _, _ = fmt.Fprintf(streams.Stdout, "\n%s\nis the fingerprint the other machine shows? It gets this Orb's conversations, logins and memory. [y/N] ", claimed.Claimant); !yes(bufio.NewReader(io.LimitReader(streams.Stdin, 256))) {
		_, _ = fmt.Fprintln(streams.Stdout, "Not paired.")
		return 1
	}
	if err = client.Call(ctx, "approve", map[string]string{"invitation_id": claimed.ID, "claimant": claimed.Claimant}, nil); err != nil {
		if mitosis.Code(err) == "identity_conflict" {
			err = errors.New("two devices used this code, so someone else saw it: not paired.\nRun orb mitosis again where only you can see the code")
		}
		return reportCLIError(streams.Stderr, err)
	}
	shareOrbsOnMitosis(ctx, profile)
	return reportInSync(ctx, client, streams, claimed.Claimant, client.Call(ctx, "sync", map[string]string{"add": claimed.Claimant}, nil))
}

// reportInSync waits up to a minute for this Orb and peer to share the sync group, then says so
// in one line. added is how adding peer to the group went, nil from the device that joins.
func reportInSync(ctx context.Context, client *protocol.Conn, streams cliStreams, peer string, added error) int {
	if mitosis.Code(added) == "unauthorized" {
		_, _ = fmt.Fprintln(streams.Stdout, "Paired. This Orb follows another's sync group: run orb mitosis on that one to share logins and memory too.")
		return 0
	} else if added != nil {
		return reportCLIError(streams.Stderr, added)
	}
	var group struct {
		Self    string            `json:"self"`
		Owner   string            `json:"owner"`
		Members []string          `json:"members"`
		Names   map[string]string `json:"names"`
		InSync  bool              `json:"in_sync"`
	}
	for end := time.Now().Add(time.Minute); time.Now().Before(end); time.Sleep(time.Second) {
		err := client.Call(ctx, "sync", struct{}{}, &group)
		if _, published := group.Names[peer]; err == nil && group.InSync && slices.Contains(group.Members, peer) && published {
			name := func(p string) string { return cmp.Or(group.Names[p], "a device") }
			_, _ = fmt.Fprintf(streams.Stdout, "%s and %s (%s) in sync, %d devices.\n", name(group.Self), name(peer), peer, len(group.Members))
			return 0
		}
	}
	_, _ = fmt.Fprintln(streams.Stdout, "Paired. Logins and memory follow once both devices are online.")
	return 0
}

// yes reads one answer line; anything but y or yes is no.
func yes(in *bufio.Reader) bool {
	line, _ := in.ReadString('\n')
	return slices.Contains([]string{"y", "yes"}, strings.ToLower(strings.TrimSpace(line)))
}

// runMitosisJoin claims an invitation, waits for the inviter's yes, then trusts it back and, if
// this Orb is in no sync group yet, follows the inviter's. Typing the code is the yes here: the
// inviter is the one that checks a fingerprint.
func runMitosisJoin(ctx context.Context, profile string, args []string, streams cliStreams) int {
	if slices.ContainsFunc(args, func(a string) bool { return strings.HasPrefix(a, "-") }) {
		return reportCLIError(streams.Stderr, errors.New("join takes the code alone: orb mitosis join <code>"))
	}
	text := strings.Join(args, "")
	if text == "" {
		raw, err := io.ReadAll(io.LimitReader(streams.Stdin, protocol.MaxFrame+1))
		if err != nil {
			return reportCLIError(streams.Stderr, err)
		}
		text = string(raw)
	}
	inv, err := parseMitosisCode(ctx, text)
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
	if err = client.Call(ctx, "join", inv, nil); err != nil {
		if mitosis.Code(err) == "identity_conflict" {
			err = errors.New("another device already used this code: ask for a new one")
		}
		return reportCLIError(streams.Stderr, err)
	}
	_, _ = fmt.Fprintf(streams.Stdout, "This Orb   %s\nConfirm this fingerprint on the other machine.\n", status.PeerID)
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
	var group struct {
		Owner string `json:"owner"`
	}
	if err = client.Call(ctx, "sync", struct{}{}, &group); mitosis.Code(err) == "not_found" {
		err = client.Call(ctx, "sync", map[string]string{"join": inv.PeerID}, nil)
	} else if group.Owner != "" && group.Owner != inv.PeerID {
		_, _ = fmt.Fprintln(streams.Stdout, "Paired. This Orb keeps the sync group it follows: orb mitosis sync remove <owner> leaves it.")
		return 0
	}
	if mitosis.Code(err) == "unavailable" {
		err = nil // the owner's vault is on its way
	}
	return reportInSync(ctx, client, streams, inv.PeerID, err)
}

// offerShortCode is transport.Offer, which tests replace to stay off the network.
var offerShortCode = transport.Offer

// parseMitosisCode reads a short code, fetching the invitation it offers, or a full invitation.
func parseMitosisCode(ctx context.Context, text string) (mitosis.Invitation, error) {
	if full, short, err := transport.Fetch(ctx, text); short && err != nil {
		return mitosis.Invitation{}, err
	} else if short {
		text = full
	}
	return mitosis.ParseInvitation(text)
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
			return mitosis.Invitation{}, errors.New("invitation expired; run orb mitosis again")
		case <-time.After(time.Second):
		}
	}
}
