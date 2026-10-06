// Package platforms is the catalog of chat platforms an agent runs on, shared
// by the assemblies that run agents (orb chat, the team agent image): one row
// per platform maps its environment, and its section of an agent file, onto
// the platform package's options. Removing a platform is deleting its package
// and its row.
package platforms

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"slices"
	"strings"

	"github.com/OrdalieTech/orb/chat"
	"github.com/OrdalieTech/orb/chat/buzz"
	"github.com/OrdalieTech/orb/chat/discord"
	"github.com/OrdalieTech/orb/chat/googlechat"
	"github.com/OrdalieTech/orb/chat/messenger"
	"github.com/OrdalieTech/orb/chat/slack"
	"github.com/OrdalieTech/orb/chat/teams"
	"github.com/OrdalieTech/orb/chat/telegram"
	"github.com/OrdalieTech/orb/chat/whatsapp"
)

// AllowedSenders is orb chat's sender allowlist, shared by the platforms it
// routes (those with Open).
const AllowedSenders = "ORB_CHAT_ALLOWED_SENDERS"

// Platform is one row of the catalog.
type Platform struct {
	Name string
	// Env names the variables the platform reads (a name, or a prefix ending
	// in *), and About says what it does, for help.
	Env   []string
	About string
	// Open builds the adapter of a platform orb chat routes, and how its
	// messages come in.
	Open func(env Env) (chat.Adapter, chat.Inbound, error)
	// Front runs a platform that drives the agent's sessions itself.
	Front func(ctx context.Context, agent chat.Agent, env Env) error
	// Configure maps the platform's section of an agent file onto the
	// environment it reads; nil takes no settings.
	Configure func(agent chat.Identity, section map[string]any) (map[string]string, error)
	// Sidecar is a process the platform needs beside the agent, run as another
	// user: given relay, a command connecting its stdio to the agent's ACP
	// socket, it returns the command and its settings. It also gets the
	// variables Env names.
	Sidecar func(relay []string) (command, settings []string)
	// Alias is what Orb runs when started under the platform's name, through a
	// link in the agent's shell.
	Alias func(env Env, args []string, stdin io.Reader, stdout, stderr io.Writer) int
}

// All is the catalog.
var All = []Platform{
	{
		Name:  "buzz",
		Env:   []string{"BUZZ_PRIVATE_KEY", "BUZZ_RELAY_URL", "BUZZ_AUTH_TAG", "BUZZ_API_TOKEN", "BUZZ_ACP_*", "RUST_LOG"},
		About: "starts buzz-acp (or ORB_BUZZ_ACP), which holds the agent's Buzz identity and reaches it over ACP",
		Front: func(ctx context.Context, agent chat.Agent, env Env) error {
			return buzz.Front(ctx, agent, buzz.Options{
				CLI:         cmp.Or(env.Get("ORB_BUZZ_CLI"), "buzz"),
				Credentials: env.Matching("BUZZ_PRIVATE_KEY", "BUZZ_AUTH_TAG", "BUZZ_RELAY_URL"),
				Profile:     chat.Identity{Name: env.Get("BUZZ_ACP_DISPLAY_NAME"), About: env.Get("ORB_BUZZ_ABOUT"), Avatar: env.Get("ORB_BUZZ_AVATAR")},
				Socket:      env.Get("ORB_ACP_SOCKET"),
				Harness:     cmp.Or(env.Get("ORB_BUZZ_ACP"), "buzz-acp"),
				HarnessEnv:  env.Matching("BUZZ_*", "RUST_LOG"),
			})
		},
		Configure: configureBuzz,
		Sidecar: func(relay []string) ([]string, []string) {
			return []string{"buzz-acp"}, buzz.HarnessSettings(relay)
		},
		Alias: func(env Env, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
			dir, _ := os.Getwd()
			return buzz.Shim(env.Get("ORB_BUZZ"), dir, args, stdin, stdout, stderr)
		},
	},
	{
		Name: "discord", Env: []string{"DISCORD_BOT_TOKEN"}, About: "Discord bot token",
		Open: func(env Env) (chat.Adapter, chat.Inbound, error) {
			token, err := required(env, "DISCORD_BOT_TOKEN")
			if err != nil {
				return nil, chat.Inbound{}, err
			}
			return polled(discord.New(discord.Options{Token: token}))
		},
	},
	{
		Name: "googlechat", Env: []string{"GOOGLE_CHAT_PROJECT_NUMBER", "GOOGLE_CHAT_CREDENTIALS_FILE"},
		Open: func(env Env) (chat.Adapter, chat.Inbound, error) {
			credentials, err := os.ReadFile(env.Get("GOOGLE_CHAT_CREDENTIALS_FILE"))
			if err != nil {
				return nil, chat.Inbound{}, fmt.Errorf("read GOOGLE_CHAT_CREDENTIALS_FILE: %w", err)
			}
			return webhook(googlechat.New(googlechat.Options{ProjectNumber: env.Get("GOOGLE_CHAT_PROJECT_NUMBER"), CredentialsJSON: credentials}))
		},
	},
	{
		Name: "messenger", Env: []string{"MESSENGER_TOKEN", "MESSENGER_PAGE_ID", "MESSENGER_APP_SECRET", "MESSENGER_VERIFY_TOKEN"},
		Open: func(env Env) (chat.Adapter, chat.Inbound, error) {
			return webhook(messenger.New(messenger.Options{
				Token: env.Get("MESSENGER_TOKEN"), PageID: env.Get("MESSENGER_PAGE_ID"),
				AppSecret: env.Get("MESSENGER_APP_SECRET"), VerifyToken: env.Get("MESSENGER_VERIFY_TOKEN"),
			}))
		},
	},
	{
		Name: "slack", Env: []string{"SLACK_BOT_TOKEN", "SLACK_SIGNING_SECRET", "SLACK_BOT_USER_ID"},
		Open: func(env Env) (chat.Adapter, chat.Inbound, error) {
			return webhook(slack.New(slack.Options{
				Token: env.Get("SLACK_BOT_TOKEN"), SigningSecret: env.Get("SLACK_SIGNING_SECRET"), BotUserID: env.Get("SLACK_BOT_USER_ID"),
			}))
		},
	},
	{
		Name: "teams", Env: []string{"TEAMS_APP_ID", "TEAMS_APP_PASSWORD", "TEAMS_TENANT_ID"},
		Open: func(env Env) (chat.Adapter, chat.Inbound, error) {
			return webhook(teams.New(teams.Options{
				AppID: env.Get("TEAMS_APP_ID"), AppPassword: env.Get("TEAMS_APP_PASSWORD"), TenantID: env.Get("TEAMS_TENANT_ID"),
			}))
		},
	},
	{
		Name: "telegram", Env: []string{"TELEGRAM_BOT_TOKEN"}, About: "Telegram bot token",
		Open: func(env Env) (chat.Adapter, chat.Inbound, error) {
			token, err := required(env, "TELEGRAM_BOT_TOKEN")
			if err != nil {
				return nil, chat.Inbound{}, err
			}
			return polled(telegram.New(telegram.Options{Token: token}))
		},
	},
	{
		Name: "whatsapp", Env: []string{"WHATSAPP_TOKEN", "WHATSAPP_PHONE_NUMBER_ID", "WHATSAPP_APP_SECRET", "WHATSAPP_VERIFY_TOKEN"},
		Open: func(env Env) (chat.Adapter, chat.Inbound, error) {
			return webhook(whatsapp.New(whatsapp.Options{
				Token: env.Get("WHATSAPP_TOKEN"), PhoneNumberID: env.Get("WHATSAPP_PHONE_NUMBER_ID"),
				AppSecret: env.Get("WHATSAPP_APP_SECRET"), VerifyToken: env.Get("WHATSAPP_VERIFY_TOKEN"),
			}))
		},
	},
}

// Lookup returns the platform named name.
func Lookup(name string) (Platform, bool) {
	at := slices.IndexFunc(All, func(platform Platform) bool { return platform.Name == name })
	if at < 0 {
		return Platform{}, false
	}
	return All[at], true
}

// Names are the catalog's platform names.
func Names() []string {
	names := make([]string, len(All))
	for i, platform := range All {
		names[i] = platform.Name
	}
	return names
}

// Env is a process environment as KEY=VALUE entries; for a name set twice,
// the last entry wins.
type Env []string

// Get returns the value of name, or "".
func (env Env) Get(name string) string {
	for i := len(env) - 1; i >= 0; i-- {
		if key, value, ok := strings.Cut(env[i], "="); ok && key == name {
			return value
		}
	}
	return ""
}

// Matching returns the entries whose names match a pattern: a name, or a
// prefix ending in *.
func (env Env) Matching(patterns ...string) []string {
	return slices.DeleteFunc(slices.Clone(env), func(entry string) bool {
		name, _, _ := strings.Cut(entry, "=")
		return !Matches(name, patterns)
	})
}

// Matches reports whether name matches one of patterns.
func Matches(name string, patterns []string) bool {
	return slices.ContainsFunc(patterns, func(pattern string) bool {
		prefix, wildcard := strings.CutSuffix(pattern, "*")
		return name == pattern || wildcard && strings.HasPrefix(name, prefix)
	})
}

// polled is an adapter that fetches its messages, or err.
func polled[A interface {
	chat.Adapter
	Poll(context.Context, func(chat.Message) error) error
}](adapter A, err error) (chat.Adapter, chat.Inbound, error) {
	if err != nil {
		return nil, chat.Inbound{}, err
	}
	return adapter, chat.Inbound{Poll: adapter.Poll}, nil
}

// webhook is an adapter its platform calls, or err.
func webhook[A interface {
	chat.Adapter
	Webhook(func(chat.Message) error) http.Handler
}](adapter A, err error) (chat.Adapter, chat.Inbound, error) {
	if err != nil {
		return nil, chat.Inbound{}, err
	}
	return adapter, chat.Inbound{Webhook: adapter.Webhook}, nil
}

func required(env Env, name string) (string, error) {
	if value := strings.TrimSpace(env.Get(name)); value != "" {
		return value, nil
	}
	return "", errors.New(name + " is required")
}

// buzzSettings are the buzz-acp settings an agent file may set, as
// BUZZ_ACP_<KEY>; allow is BUZZ_ACP_RESPOND_TO_ALLOWLIST. Others stay
// available as environment.
var buzzSettings = []string{"respond_to", "allow", "subscribe", "channels", "kinds", "idle_timeout", "turn_timeout", "heartbeat_interval", "heartbeat_prompt"}

// configureBuzz maps the agent's identity and its buzz section onto the
// environment the buzz row and buzz-acp read.
func configureBuzz(agent chat.Identity, section map[string]any) (map[string]string, error) {
	env := map[string]string{}
	for name, value := range map[string]string{"BUZZ_ACP_DISPLAY_NAME": agent.Name, "ORB_BUZZ_ABOUT": agent.About, "ORB_BUZZ_AVATAR": agent.Avatar} {
		if value != "" {
			env[name] = value
		}
	}
	for key, value := range section {
		if !slices.Contains(buzzSettings, key) {
			return nil, fmt.Errorf("buzz: unknown setting %q (known: %s)", key, strings.Join(buzzSettings, ", "))
		}
		name := "BUZZ_ACP_" + strings.ToUpper(key)
		if key == "allow" {
			name = "BUZZ_ACP_RESPOND_TO_ALLOWLIST"
		}
		switch value := value.(type) {
		case []any:
			items := make([]string, len(value))
			for i, item := range value {
				items[i] = fmt.Sprint(item)
			}
			env[name] = strings.Join(items, ",")
		case map[string]any:
			return nil, fmt.Errorf("buzz: %s takes a value or a list", key)
		default:
			env[name] = fmt.Sprint(value)
		}
	}
	return env, nil
}
