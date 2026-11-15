// Package platforms is the catalog of chat platforms an agent runs on, shared
// by the assemblies that run agents (orb chat, the team agent image): one row
// per platform maps its environment, and its section of an agent file, onto
// the platform package's options. Removing a platform is deleting its package
// and its row.
package platforms

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
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

// AllowedSenders is orb chat's sender allowlist, shared by the platforms
// that do not gate their senders themselves; Buzz takes its pubkeys as its
// allowlist.
const AllowedSenders = "ORB_CHAT_ALLOWED_SENDERS"

// DataDir is where orb chat keeps the gateway's state, a platform's included.
const DataDir = "ORB_CHAT_DATA_DIR"

// Platform is one row of the catalog.
type Platform struct {
	Name string
	// Env names the variables the platform reads (a name, or a prefix ending
	// in *), and About says what it does, for help.
	Env   []string
	About string
	// Open builds the platform's adapter, logging to logger, and how its
	// messages come in.
	Open func(env Env, logger *slog.Logger) (chat.Adapter, chat.Inbound, error)
	// Configure maps the platform's section of an agent file onto the
	// environment it reads; nil takes no settings.
	Configure func(agent chat.Identity, section map[string]any) (map[string]string, error)
	// Setup onboards the agent on the platform (orb chat setup <platform>):
	// given its arguments and the variables saved so far, talking with the
	// user on in and out, it returns the variables to save, or nil for none.
	Setup func(ctx context.Context, args []string, saved map[string]string, in io.Reader, out io.Writer) (map[string]string, error)
}

// All is the catalog.
var All = []Platform{
	{
		Name: "buzz", Env: []string{"BUZZ_PRIVATE_KEY", "BUZZ_RELAY_URL", "BUZZ_AUTH_TAG", "BUZZ_RESPOND_TO", "BUZZ_CHANNELS", "BUZZ_NAME", "BUZZ_ABOUT", "BUZZ_AVATAR"},
		About: "the agent's Nostr key, its relay and its owner's NIP-OA tag; BUZZ_RESPOND_TO is owner-only (default), allowlist or anyone",
		Open: func(env Env, logger *slog.Logger) (chat.Adapter, chat.Inbound, error) {
			cursor := ""
			if dir := env.Get(DataDir); dir != "" {
				cursor = filepath.Join(dir, "buzz.cursor")
			}
			adapter, err := buzz.New(buzz.Options{
				Key: env.Get("BUZZ_PRIVATE_KEY"), Relay: env.Get("BUZZ_RELAY_URL"), AuthTag: env.Get("BUZZ_AUTH_TAG"),
				RespondTo: env.Get("BUZZ_RESPOND_TO"), Channels: list(env.Get("BUZZ_CHANNELS")),
				// The shared allowlist's pubkeys: other platforms' ids are not 64 characters.
				Allow:   slices.DeleteFunc(list(env.Get(AllowedSenders)), func(id string) bool { return len(id) != 64 }),
				Profile: chat.Identity{Name: env.Get("BUZZ_NAME"), About: env.Get("BUZZ_ABOUT"), Avatar: env.Get("BUZZ_AVATAR")},
				Cursor:  cursor, Logger: logger,
			})
			if err != nil {
				return nil, chat.Inbound{}, err
			}
			return adapter, chat.Inbound{Poll: adapter.Poll, Authorize: adapter.Authorize}, nil
		},
		Configure: configureBuzz,
		Setup:     buzz.Setup,
	},
	{
		Name: "discord", Env: []string{"DISCORD_BOT_TOKEN"}, About: "Discord bot token",
		Open: func(env Env, logger *slog.Logger) (chat.Adapter, chat.Inbound, error) {
			token, err := required(env, "DISCORD_BOT_TOKEN")
			if err != nil {
				return nil, chat.Inbound{}, err
			}
			return polled(discord.New(discord.Options{Token: token, Logger: logger}))
		},
	},
	{
		Name: "googlechat", Env: []string{"GOOGLE_CHAT_PROJECT_NUMBER", "GOOGLE_CHAT_CREDENTIALS_FILE"},
		Open: func(env Env, logger *slog.Logger) (chat.Adapter, chat.Inbound, error) {
			credentials, err := os.ReadFile(env.Get("GOOGLE_CHAT_CREDENTIALS_FILE"))
			if err != nil {
				return nil, chat.Inbound{}, fmt.Errorf("read GOOGLE_CHAT_CREDENTIALS_FILE: %w", err)
			}
			return webhook(googlechat.New(googlechat.Options{ProjectNumber: env.Get("GOOGLE_CHAT_PROJECT_NUMBER"), CredentialsJSON: credentials, Logger: logger}))
		},
	},
	{
		Name: "messenger", Env: []string{"MESSENGER_TOKEN", "MESSENGER_PAGE_ID", "MESSENGER_APP_SECRET", "MESSENGER_VERIFY_TOKEN"},
		Open: func(env Env, logger *slog.Logger) (chat.Adapter, chat.Inbound, error) {
			return webhook(messenger.New(messenger.Options{
				Token: env.Get("MESSENGER_TOKEN"), PageID: env.Get("MESSENGER_PAGE_ID"),
				AppSecret: env.Get("MESSENGER_APP_SECRET"), VerifyToken: env.Get("MESSENGER_VERIFY_TOKEN"), Logger: logger,
			}))
		},
	},
	{
		Name: "slack", Env: []string{"SLACK_BOT_TOKEN", "SLACK_SIGNING_SECRET", "SLACK_BOT_USER_ID"},
		Open: func(env Env, logger *slog.Logger) (chat.Adapter, chat.Inbound, error) {
			return webhook(slack.New(slack.Options{
				Token: env.Get("SLACK_BOT_TOKEN"), SigningSecret: env.Get("SLACK_SIGNING_SECRET"), BotUserID: env.Get("SLACK_BOT_USER_ID"), Logger: logger,
			}))
		},
	},
	{
		Name: "teams", Env: []string{"TEAMS_APP_ID", "TEAMS_APP_PASSWORD", "TEAMS_TENANT_ID"},
		Open: func(env Env, logger *slog.Logger) (chat.Adapter, chat.Inbound, error) {
			return webhook(teams.New(teams.Options{
				AppID: env.Get("TEAMS_APP_ID"), AppPassword: env.Get("TEAMS_APP_PASSWORD"), TenantID: env.Get("TEAMS_TENANT_ID"), Logger: logger,
			}))
		},
	},
	{
		Name: "telegram", Env: []string{"TELEGRAM_BOT_TOKEN"}, About: "Telegram bot token",
		Open: func(env Env, logger *slog.Logger) (chat.Adapter, chat.Inbound, error) {
			token, err := required(env, "TELEGRAM_BOT_TOKEN")
			if err != nil {
				return nil, chat.Inbound{}, err
			}
			return polled(telegram.New(telegram.Options{Token: token, Logger: logger}))
		},
	},
	{
		Name: "whatsapp", Env: []string{"WHATSAPP_TOKEN", "WHATSAPP_PHONE_NUMBER_ID", "WHATSAPP_APP_SECRET", "WHATSAPP_VERIFY_TOKEN"},
		Open: func(env Env, logger *slog.Logger) (chat.Adapter, chat.Inbound, error) {
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

// list splits a comma-separated list, dropping empty items.
func list(text string) []string {
	var items []string
	for _, item := range strings.Split(text, ",") {
		if item = strings.TrimSpace(item); item != "" {
			items = append(items, item)
		}
	}
	return items
}

// configureBuzz maps the agent's identity and its buzz section (respond_to,
// channels) onto the environment the buzz row reads; its allow is the shared
// allowlist.
func configureBuzz(agent chat.Identity, section map[string]any) (map[string]string, error) {
	env := map[string]string{}
	for name, value := range map[string]string{"BUZZ_NAME": agent.Name, "BUZZ_ABOUT": agent.About, "BUZZ_AVATAR": agent.Avatar} {
		if value != "" {
			env[name] = value
		}
	}
	for key, value := range section {
		switch value := value.(type) {
		case string:
			if key == "respond_to" {
				env["BUZZ_RESPOND_TO"] = value
				continue
			}
		case []any:
			if key == "channels" {
				items := make([]string, len(value))
				for i, item := range value {
					items[i] = fmt.Sprint(item)
				}
				env["BUZZ_CHANNELS"] = strings.Join(items, ",")
				continue
			}
		}
		return nil, fmt.Errorf("buzz: settings are respond_to (owner-only, allowlist or anyone), allow and channels (lists), got %s: %v", key, value)
	}
	return env, nil
}
