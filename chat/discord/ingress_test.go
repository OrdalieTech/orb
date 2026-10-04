package discord

import (
	"testing"
)

func newIngressAdapter(t *testing.T) *Adapter {
	t.Helper()
	adapter, err := New(Options{Token: "OTk5.fake.token"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	adapter.setIdentity("999")
	return adapter
}

func author(id, username string, bot bool) *gwUser {
	return &gwUser{ID: id, Username: username, Bot: bot}
}

func TestNormalizeGatingMatrix(t *testing.T) {
	adapter := newIngressAdapter(t)
	tests := []struct {
		name     string
		msg      gwMessage
		wantOK   bool
		wantText string
	}{
		{
			name:     "dm always triggers",
			msg:      gwMessage{ID: "m1", ChannelID: "d1", Content: "hello", Author: author("42", "lea", false)},
			wantOK:   true,
			wantText: "hello",
		},
		{
			name:   "guild without mention is dropped",
			msg:    gwMessage{ID: "m2", ChannelID: "c1", GuildID: "g1", Content: "hello", Author: author("42", "lea", false)},
			wantOK: false,
		},
		{
			name: "guild mention token triggers and is stripped",
			msg: gwMessage{ID: "m3", ChannelID: "c1", GuildID: "g1", Content: "<@999> do the thing",
				Author: author("42", "lea", false), Mentions: []gwUser{{ID: "999"}}},
			wantOK:   true,
			wantText: "do the thing",
		},
		{
			name: "guild nickname mention token triggers and is stripped",
			msg: gwMessage{ID: "m4", ChannelID: "c1", GuildID: "g1", Content: "before <@!999> after",
				Author: author("42", "lea", false), Mentions: []gwUser{{ID: "999"}}},
			wantOK:   true,
			wantText: "before after",
		},
		{
			name:   "plain username text is not a mention",
			msg:    gwMessage{ID: "m5", ChannelID: "c1", GuildID: "g1", Content: "@PiBot status", Author: author("42", "lea", false)},
			wantOK: false,
		},
		{
			name: "guild mentions array without token (suppressed embed) triggers",
			msg: gwMessage{ID: "m6", ChannelID: "c1", GuildID: "g1", Content: "check this",
				Author: author("42", "lea", false), Mentions: []gwUser{{ID: "999"}}},
			wantOK:   true,
			wantText: "check this",
		},
		{
			name: "guild reply to the bot triggers",
			msg: gwMessage{ID: "m7", ChannelID: "c1", GuildID: "g1", Content: "and this?",
				Author:            author("42", "lea", false),
				ReferencedMessage: &gwMessage{ID: "m0", Author: author("999", "pibot", true)}},
			wantOK:   true,
			wantText: "and this?",
		},
		{
			name: "guild mention of someone else is dropped",
			msg: gwMessage{ID: "m8", ChannelID: "c1", GuildID: "g1", Content: "<@777> hey",
				Author: author("42", "lea", false), Mentions: []gwUser{{ID: "777"}}},
			wantOK: false,
		},
		{
			name:   "bot author is dropped",
			msg:    gwMessage{ID: "m9", ChannelID: "d1", Content: "beep", Author: author("555", "otherbot", true)},
			wantOK: false,
		},
		{
			name:   "own echo is dropped",
			msg:    gwMessage{ID: "m10", ChannelID: "d1", Content: "echo", Author: author("999", "pibot", true)},
			wantOK: false,
		},
		{
			name:   "missing author is dropped",
			msg:    gwMessage{ID: "m11", ChannelID: "d1", Content: "ghost"},
			wantOK: false,
		},
		{
			name:   "empty content without attachments is dropped",
			msg:    gwMessage{ID: "m12", ChannelID: "d1", Content: "", Author: author("42", "lea", false)},
			wantOK: false,
		},
		{
			name: "mention-only guild message is dropped (nothing left to say)",
			msg: gwMessage{ID: "m13", ChannelID: "c1", GuildID: "g1", Content: "<@999>",
				Author: author("42", "lea", false), Mentions: []gwUser{{ID: "999"}}},
			wantOK: false,
		},
		{
			name: "command with mention normalizes to bare command",
			msg: gwMessage{ID: "m14", ChannelID: "c1", GuildID: "g1", Content: "<@999> /status",
				Author: author("42", "lea", false), Mentions: []gwUser{{ID: "999"}}},
			wantOK:   true,
			wantText: "/status",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m, ok := adapter.normalize(&tt.msg)
			if ok != tt.wantOK {
				t.Fatalf("ok = %t, want %t", ok, tt.wantOK)
			}
			if !ok {
				return
			}
			if m.Text != tt.wantText {
				t.Errorf("Text = %q, want %q", m.Text, tt.wantText)
			}
		})
	}
}
