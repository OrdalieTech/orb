package buzz

import (
	"bufio"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"maps"
	"os"
	"strings"

	"golang.org/x/term"

	"github.com/OrdalieTech/orb/chat"
)

// Setup onboards a Buzz agent from args, given the settings saved before,
// and returns the settings to save, or nil for none. The agent gets its key
// and profile itself; its owner approves it by signing its NIP-OA
// attestation, here (--owner-key) or on their own machine (--attest there,
// then --auth here), since the owner's key never needs to reach the agent.
func Setup(ctx context.Context, args []string, saved map[string]string, in io.Reader, out io.Writer) (map[string]string, error) {
	flags := flag.NewFlagSet("orb chat setup buzz", flag.ContinueOnError)
	flags.SetOutput(out)
	relay := flags.String("relay", saved["BUZZ_RELAY_URL"], "the relay, such as wss://buzz.example.com")
	name := flags.String("name", saved["BUZZ_NAME"], "the agent's name on Buzz")
	about := flags.String("about", saved["BUZZ_ABOUT"], "what it is for")
	avatar := flags.String("avatar", saved["BUZZ_AVATAR"], "its picture, an HTTPS image URL")
	respondTo := flags.String("respond-to", cmp.Or(saved["BUZZ_RESPOND_TO"], "owner-only"), "whom it answers: owner-only, allowlist or anyone")
	auth := flags.String("auth", "", "its owner's attestation, the tag --attest printed on the owner's machine")
	ownerKey := flags.String("owner-key", "", "the owner approves it here: a file holding the owner's secret key, or - to read it")
	attest := flags.String("attest", "", "as its owner, approve the agent with this pubkey, which is set up elsewhere")
	report := flags.Bool("json", false, "report as JSON")
	flags.Usage = func() {
		_, _ = fmt.Fprint(out, `Usage: orb chat setup buzz [flags]

Sets this agent up on a Buzz relay, saving its settings for orb chat buzz:
  orb chat setup buzz --relay wss://buzz.example.com --name Sales
makes its key and, once its owner approved it, publishes its profile. Its owner
approves it here with --owner-key, or on their own machine with
  orb chat setup buzz --attest <agent pubkey> --relay … --name Sales --owner-key -
which prints the --auth to give it here. Run it again to change a setting.

`)
		flags.PrintDefaults()
	}
	if err := flags.Parse(args); err != nil {
		return nil, err
	}
	if *relay == "" {
		return nil, errors.New("buzz: --relay is required, such as --relay wss://buzz.example.com")
	}
	if !strings.Contains(*relay, "://") {
		*relay = "wss://" + *relay
	}
	profile := chat.Identity{Name: *name, About: *about, Avatar: *avatar}

	if *attest != "" {
		agent := strings.ToLower(strings.TrimSpace(*attest))
		if !isHex32(agent) {
			return nil, errors.New("buzz: --attest takes the agent's pubkey, 64 hex characters")
		}
		secret, err := readSecret(*ownerKey, in, out)
		if err == nil && secret == "" {
			err = errors.New("buzz: --attest needs the owner's key, --owner-key <file> or -")
		}
		if err != nil {
			return nil, err
		}
		tag, err := approve(ctx, secret, agent, *relay, profile, *respondTo)
		if err != nil {
			return nil, err
		}
		_, _ = fmt.Fprintf(out, "Approved %s. On the agent's machine, run:\n  orb chat setup buzz --auth '%s'\n", npub(agent), tag)
		return nil, nil
	}

	settings := maps.Clone(saved)
	if settings == nil {
		settings = map[string]string{}
	}
	if settings["BUZZ_PRIVATE_KEY"] == "" {
		settings["BUZZ_PRIVATE_KEY"] = newKey()
	}
	agent, err := parseKey(settings["BUZZ_PRIVATE_KEY"])
	if err != nil {
		return nil, err
	}
	for variable, value := range map[string]string{"BUZZ_RELAY_URL": *relay, "BUZZ_NAME": *name, "BUZZ_ABOUT": *about, "BUZZ_AVATAR": *avatar, "BUZZ_RESPOND_TO": *respondTo} {
		if value != "" {
			settings[variable] = value
		}
	}
	interactive := *auth == "" && *ownerKey == "" && settings["BUZZ_AUTH_TAG"] == "" && !*report && isTerminal(in)
	if interactive {
		_, _ = fmt.Fprintf(out, "Agent %s needs its owner's approval.\n", npub(agent.public))
		*ownerKey = "-"
	}
	switch {
	case *auth != "":
		var tag []string
		_ = json.Unmarshal([]byte(*auth), &tag)
		if err := checkAttestation(tag, agent.public); err != nil {
			return nil, err
		}
		settings["BUZZ_AUTH_TAG"] = *auth
	case *ownerKey != "":
		secret, err := readSecret(*ownerKey, in, out)
		if err == nil && secret == "" && !interactive {
			err = errors.New("buzz: no owner key given")
		}
		if err != nil {
			return nil, err
		}
		if secret != "" {
			if settings["BUZZ_AUTH_TAG"], err = approve(ctx, secret, agent.public, *relay, profile, settings["BUZZ_RESPOND_TO"]); err != nil {
				return nil, err
			}
		}
	}

	status := map[string]any{"pubkey": agent.public, "npub": npub(agent.public), "relay": *relay, "approved": settings["BUZZ_AUTH_TAG"] != ""}
	if tag := settings["BUZZ_AUTH_TAG"]; tag != "" {
		adapter, err := New(Options{Key: settings["BUZZ_PRIVATE_KEY"], Relay: *relay, AuthTag: tag, RespondTo: "anyone", Profile: profile})
		if err != nil {
			return nil, err
		}
		status["owner"] = adapter.owner
		if err := adapter.publishProfile(ctx); err != nil {
			status["error"] = err.Error()
		}
	}
	if *report {
		data, _ := json.Marshal(status)
		_, _ = fmt.Fprintln(out, string(data))
		return settings, nil
	}
	_, _ = fmt.Fprintf(out, "Buzz agent %s on %s\n  pubkey %s\n", cmp.Or(*name, "(unnamed)"), *relay, agent.public)
	switch {
	case status["error"] != nil:
		_, _ = fmt.Fprintf(out, "  owner  %s\nThe relay did not take its profile: %s\n", status["owner"], status["error"])
	case status["approved"] == true:
		_, _ = fmt.Fprintf(out, "  owner  %s\nReady: its profile is published. Run it with orb chat buzz --tools, and add it to\nchannels in Buzz (Add people and agents) or mention it where it may read.\n", status["owner"])
	default:
		_, _ = fmt.Fprintf(out, "Waiting for its owner. They approve it on their own machine with\n  orb chat setup buzz --attest %s --relay %s --name '%s' --owner-key -\nand give you the --auth line it prints to run here.\n", agent.public, *relay, *name)
	}
	return settings, nil
}

// approve signs the owner's attestation of agent and publishes the owner's
// kind:30177 record of it, which Buzz's agent directory requires, as Buzz's
// own app writes it.
func approve(ctx context.Context, secret, agent, relay string, profile chat.Identity, respondTo string) (string, error) {
	adapter, err := New(Options{Key: secret, Relay: relay, RespondTo: "anyone"})
	if err != nil {
		return "", err
	}
	if adapter.key.public == agent {
		return "", errors.New("buzz: the agent's key must differ from its owner's")
	}
	record := map[string]any{"parallelism": 1, "respond_to": cmp.Or(respondTo, "owner-only"), "name": cmp.Or(profile.Name, npub(agent)[:12])}
	if strings.HasPrefix(profile.Avatar, "https://") {
		record["picture"] = profile.Avatar
	}
	content, _ := json.Marshal(record)
	if _, err := adapter.publish(ctx, 30177, [][]string{{"d", agent}, {"runtime", "orb"}}, string(content)); err != nil {
		return "", fmt.Errorf("buzz: publishing the owner's record of the agent: %w", err)
	}
	tag, _ := json.Marshal(adapter.key.attest(agent))
	return string(tag), nil
}

// readSecret reads a secret key from a file, or for "-" from in: without
// echo when in is a terminal, where an empty answer is no key.
func readSecret(path string, in io.Reader, out io.Writer) (string, error) {
	if path != "-" {
		data, err := os.ReadFile(path)
		return strings.TrimSpace(string(data)), err
	}
	if file, ok := in.(*os.File); ok && isTerminal(in) {
		_, _ = fmt.Fprint(out, "Owner's secret key (nsec… or hex; Enter to approve elsewhere): ")
		secret, err := term.ReadPassword(int(file.Fd()))
		_, _ = fmt.Fprintln(out)
		return strings.TrimSpace(string(secret)), err
	}
	line, err := bufio.NewReader(in).ReadString('\n')
	if errors.Is(err, io.EOF) {
		err = nil
	}
	return strings.TrimSpace(line), err
}

func isTerminal(in io.Reader) bool {
	file, ok := in.(*os.File)
	return ok && term.IsTerminal(int(file.Fd()))
}
