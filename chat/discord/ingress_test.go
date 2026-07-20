package discord

import (
	"strings"
	"testing"
	"time"
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

func TestNormalizeFields(t *testing.T) {
	adapter := newIngressAdapter(t)
	msg := gwMessage{
		ID:        "111222333",
		ChannelID: "chan9",
		GuildID:   "guild1",
		Content:   "<@999> summarize the doc",
		Timestamp: "2026-07-19T10:30:00.123000+00:00",
		Author:    &gwUser{ID: "42", Username: "lea", GlobalName: "Léa G"},
		Member:    &gwMember{Nick: "Léa"},
		Mentions:  []gwUser{{ID: "999"}},
		ReferencedMessage: &gwMessage{
			ID:     "111000000",
			Author: author("999", "pibot", true),
		},
		Attachments: []gwAttachment{
			{ID: "a1", Filename: "chart.png", ContentType: "image/png", Size: 1234,
				URL: "https://cdn.example.com/attachments/1/a1/chart.png?ex=1&is=2&hm=3"},
			{ID: "a2", Filename: "notes.pdf", ContentType: "application/pdf", Size: 999,
				URL: "https://cdn.example.com/attachments/1/a2/notes.pdf?ex=1&is=2&hm=3"},
			{ID: "a3", Filename: "voice.ogg", ContentType: "audio/ogg", Size: 555,
				URL: "https://cdn.example.com/attachments/1/a3/voice.ogg?ex=1&is=2&hm=3"},
		},
	}
	m, ok := adapter.normalize(&msg)
	if !ok {
		t.Fatal("normalize dropped the message")
	}
	if m.EventID != "dc:chan9:111222333" {
		t.Errorf("EventID = %q, want dc:chan9:111222333", m.EventID)
	}
	if m.Platform != "discord" || m.Account != "999" {
		t.Errorf("Platform/Account = %q/%q, want discord/999", m.Platform, m.Account)
	}
	if m.ChatID != "chan9" || m.ThreadID != "" || m.ChatType != "group" {
		t.Errorf("ChatID/ThreadID/ChatType = %q/%q/%q, want chan9//group", m.ChatID, m.ThreadID, m.ChatType)
	}
	if m.SenderID != "42" || m.SenderName != "Léa" {
		t.Errorf("Sender = %q/%q, want 42/Léa (nick wins)", m.SenderID, m.SenderName)
	}
	if m.Text != "summarize the doc" {
		t.Errorf("Text = %q", m.Text)
	}
	if m.ReplyToID != "dc:chan9:111000000" {
		t.Errorf("ReplyToID = %q, want dc:chan9:111000000", m.ReplyToID)
	}
	want := time.Date(2026, 7, 19, 10, 30, 0, 123000000, time.UTC)
	if !m.SentAt.Equal(want) {
		t.Errorf("SentAt = %v, want %v", m.SentAt, want)
	}
	if len(m.Attachments) != 3 {
		t.Fatalf("attachments = %d, want 3", len(m.Attachments))
	}
	kinds := []string{m.Attachments[0].Kind, m.Attachments[1].Kind, m.Attachments[2].Kind}
	if kinds[0] != "photo" || kinds[1] != "document" || kinds[2] != "audio" {
		t.Errorf("attachment kinds = %v, want [photo document audio]", kinds)
	}
	first := m.Attachments[0]
	if !strings.HasPrefix(first.ID, "https://") || first.Name != "chart.png" ||
		first.MIME != "image/png" || first.Size != 1234 {
		t.Errorf("attachment ref = %+v", first)
	}
}

func TestNormalizeSenderNameFallbacks(t *testing.T) {
	adapter := newIngressAdapter(t)
	m, ok := adapter.normalize(&gwMessage{
		ID: "m1", ChannelID: "d1", Content: "hi",
		Author: &gwUser{ID: "42", Username: "lea", GlobalName: "Léa G"},
	})
	if !ok || m.SenderName != "Léa G" {
		t.Errorf("SenderName = %q, want global name fallback", m.SenderName)
	}
	m, ok = adapter.normalize(&gwMessage{
		ID: "m2", ChannelID: "d1", Content: "hi again",
		Author: &gwUser{ID: "42", Username: "lea"},
	})
	if !ok || m.SenderName != "lea" {
		t.Errorf("SenderName = %q, want username fallback", m.SenderName)
	}
}

func TestAccountFromToken(t *testing.T) {
	// "OTk5" is base64("999") — the id segment of a bot token is public.
	if got := accountFromToken("OTk5.secret.part"); got != "999" {
		t.Errorf("accountFromToken = %q, want 999", got)
	}
	if got := accountFromToken("no-dots-here"); got != "" {
		t.Errorf("accountFromToken on malformed token = %q, want empty", got)
	}
	if got := accountFromToken("!!!.a.b"); got != "" {
		t.Errorf("accountFromToken on undecodable token = %q, want empty", got)
	}
}
