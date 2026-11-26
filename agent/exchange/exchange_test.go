package exchange

import "testing"

func TestFormatAndParseIncomingRoundTrip(t *testing.T) {
	wire := FormatIncoming("orb:ed25519:kmUVCF6YvJHHyEIy90yc0t6PqIN6qr_89q-1zybjRqU", "sess-1", "Refactor, v2", "MacBook", "Is deploy green?")
	want := "Message from Orb agent orb:ed25519:kmUVCF6YvJHHyEIy90yc0t6PqIN6qr_89q-1zybjRqU, conversation sess-1 (Refactor, v2, MacBook):\n\nIs deploy green?"
	if wire != want {
		t.Fatalf("wire = %q, want %q", wire, want)
	}
	msg, ok := ParseIncoming(wire)
	if !ok {
		t.Fatal("ParseIncoming returned false")
	}
	if msg.Dir != DirIn || msg.Scope != ScopePeer || msg.Who != "MacBook · Refactor, v2" ||
		msg.Peer != "orb:ed25519:kmUVCF6YvJHHyEIy90yc0t6PqIN6qr_89q-1zybjRqU" || msg.Session != "sess-1" || msg.Text != "Is deploy green?" {
		t.Fatalf("parsed = %+v", msg)
	}

	short, ok := ParseIncoming("Message from an Orb conversation (fedora):\n\nHello there")
	if !ok || short.Who != "fedora" || short.Text != "Hello there" {
		t.Fatalf("short parsed = %+v, ok=%v", short, ok)
	}

	if _, ok := ParseIncoming("Just a normal user message"); ok {
		t.Fatal("plain user message parsed as peer exchange")
	}
}

func TestFromToolAgentsAndSubagent(t *testing.T) {
	// Exploration calls stay out of peer exchanges.
	if _, ok := FromTool("agents", map[string]any{"action": "list"}, "", nil, false, false); ok {
		t.Fatal("agents list should not become an exchange")
	}
	if _, ok := FromTool("agents", map[string]any{"action": "read", "agent": "fedora"}, "", nil, false, false); ok {
		t.Fatal("agents read should not become an exchange")
	}

	// Outgoing peer send uses enriched details when present and degrades cleanly without them.
	sent, ok := FromTool("agents",
		map[string]any{"action": "send", "agent": "ordalie-lab-3", "text": "Check staging"},
		"Queued: conversation s2 reads it after its current turn.",
		map[string]any{"who": "ordalie-lab-3 · Deploy", "peer": "orb:ed25519:abc", "session": "s2", "status": "queued"},
		false, false,
	)
	if !ok || sent.Dir != DirOut || sent.Scope != ScopePeer || sent.Who != "ordalie-lab-3 · Deploy" || sent.Status != "queued" || sent.Text != "Check staging" {
		t.Fatalf("sent = %+v, ok=%v", sent, ok)
	}

	// Single local subagent.
	sub, ok := FromTool("subagent",
		map[string]any{"mode": "single", "agent": "reviewer", "task": "Check race conditions"},
		"All clean.",
		map[string]any{"steps": "read a.go\nrun tests"},
		false, false,
	)
	if !ok || sub.Dir != DirOut || sub.Scope != ScopeSubagent || sub.Who != "reviewer" || sub.Status != "2 steps" || sub.Reply != "All clean." {
		t.Fatalf("sub = %+v, ok=%v", sub, ok)
	}

	// Parallel local subagents.
	par, ok := FromTool("subagent",
		map[string]any{
			"mode": "parallel",
			"tasks": []any{
				map[string]any{"agent": "scout", "task": "Find callers"},
				map[string]any{"agent": "reviewer", "task": "Audit locks"},
			},
		},
		"", nil, true, false,
	)
	if !ok || par.Who != "scout, reviewer" || par.Status != "working" || !par.Live {
		t.Fatalf("par = %+v, ok=%v", par, ok)
	}
}
