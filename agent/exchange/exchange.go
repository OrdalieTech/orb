// Package exchange defines the lightweight, UI-agnostic representation of
// inter-agent messages (Mitosis peer conversations and local subagents).
//
// It layers cleanly over standard pi-compatible user messages and tool calls
// without changing session JSONL or wire schemas, so older Orb versions and
// external SDK embedders stay interoperable.
package exchange

import (
	"fmt"
	"slices"
	"strings"
)

const (
	DirOut = "out"
	DirIn  = "in"

	ScopePeer     = "peer"
	ScopeSubagent = "subagent"
)

// Message is one lightweight inter-agent exchange rendered as a dedicated
// multiplayer primitive across the TUI, web, desktop, and mobile apps.
type Message struct {
	Dir     string `json:"dir"`               // "out" (sent/delegated) or "in" (received)
	Scope   string `json:"scope"`             // "peer" (Mitosis) or "subagent" (local)
	Who     string `json:"who"`               // human-readable peer/conversation or subagent role
	Peer    string `json:"peer,omitempty"`    // Mitosis peer ID when known
	Session string `json:"session,omitempty"` // Mitosis conversation ID when known
	Text    string `json:"text"`              // message body without transport envelope
	Status  string `json:"status,omitempty"`  // "sent", "queued", "sending", "working", "done", "failed"
	Reply   string `json:"reply,omitempty"`   // subagent answer preview when completed
	Steps   string `json:"steps,omitempty"`   // optional subagent step log
	Live    bool   `json:"live,omitempty"`
	Failed  bool   `json:"failed,omitempty"`
}

// FormatIncoming wraps a message sent from one Orb conversation to another in
// the human- and machine-readable envelope persisted in standard user messages.
func FormatIncoming(peer, session, title, machine, text string) string {
	header := "Message from an Orb conversation"
	if session != "" {
		header = "Message from Orb agent " + peer + ", conversation " + session
	}
	if about := slices.DeleteFunc([]string{strings.TrimSpace(title), strings.TrimSpace(machine)}, func(s string) bool { return s == "" }); len(about) > 0 {
		header += " (" + strings.Join(about, ", ") + ")"
	}
	return header + ":\n\n" + text
}

// ParseIncoming extracts an incoming Mitosis peer exchange from a user message
// written by FormatIncoming (including historical messages from earlier Orb versions).
func ParseIncoming(raw string) (Message, bool) {
	head, body, ok := strings.Cut(raw, ":\n\n")
	if !ok {
		return Message{}, false
	}
	var peer, session, about string
	switch {
	case strings.HasPrefix(head, "Message from Orb agent "):
		rest := strings.TrimPrefix(head, "Message from Orb agent ")
		peerPart, afterPeer, ok := strings.Cut(rest, ", conversation ")
		if !ok {
			return Message{}, false
		}
		peer = strings.TrimSpace(peerPart)
		if sessPart, paren, hasParen := strings.Cut(afterPeer, " ("); hasParen && strings.HasSuffix(paren, ")") {
			session = strings.TrimSpace(sessPart)
			about = strings.TrimSuffix(paren, ")")
		} else {
			session = strings.TrimSpace(afterPeer)
		}
	case strings.HasPrefix(head, "Message from an Orb conversation"):
		rest := strings.TrimPrefix(head, "Message from an Orb conversation")
		if rest != "" {
			if !strings.HasPrefix(rest, " (") || !strings.HasSuffix(rest, ")") {
				return Message{}, false
			}
			about = rest[2 : len(rest)-1]
		}
	default:
		return Message{}, false
	}
	return Message{
		Dir:     DirIn,
		Scope:   ScopePeer,
		Who:     formatPeerWho(about, peer),
		Peer:    peer,
		Session: session,
		Text:    strings.TrimSpace(body),
	}, true
}

// formatPeerWho turns "(Title, Machine)" into "Machine · Title" (matching Orb's
// machine-first label across Mitosis), falling back to a short peer ID.
func formatPeerWho(about, peer string) string {
	about = strings.TrimSpace(about)
	if about != "" {
		// Split on the last ", " so a conversation title containing a comma stays intact.
		if idx := strings.LastIndex(about, ", "); idx > 0 {
			title := strings.TrimSpace(about[:idx])
			machine := strings.TrimSpace(about[idx+2:])
			if machine != "" && title != "" {
				return machine + " · " + title
			}
		}
		return about
	}
	return shortPeer(peer)
}

func shortPeer(peer string) string {
	peer = strings.TrimSpace(peer)
	if peer == "" {
		return "peer"
	}
	short := peer[strings.LastIndex(peer, ":")+1:]
	if len(short) > 10 {
		return short[:10]
	}
	return short
}

// FromTool projects an agents(action="send") or subagent tool call into a
// lightweight inter-agent Exchange. Exploration calls (agents list/read) and
// unrelated tools return false so they stay in normal tool runs.
func FromTool(name string, args map[string]any, resultText string, details map[string]any, live, failed bool) (Message, bool) {
	switch strings.ToLower(name) {
	case "agents":
		action, _ := args["action"].(string)
		if action != "send" {
			return Message{}, false
		}
		text, _ := args["text"].(string)
		agentArg, _ := args["agent"].(string)
		convArg, _ := args["conversation"].(string)
		who, _ := details["who"].(string)
		peer, _ := details["peer"].(string)
		session, _ := details["session"].(string)
		if peer == "" && strings.HasPrefix(agentArg, "orb:") {
			peer = agentArg
		}
		if session == "" {
			session = convArg
		}
		if who == "" {
			who = strings.TrimSpace(agentArg)
			if who == "" {
				who = shortPeer(session)
			} else if strings.HasPrefix(who, "orb:") {
				who = shortPeer(who)
			}
		}
		status, _ := details["status"].(string)
		switch {
		case live:
			status = "sending"
		case failed:
			status = "failed"
		case status == "":
			if strings.HasPrefix(resultText, "Queued") {
				status = "queued"
			} else {
				status = "sent"
			}
		}
		return Message{
			Dir:     DirOut,
			Scope:   ScopePeer,
			Who:     who,
			Peer:    peer,
			Session: session,
			Text:    strings.TrimSpace(text),
			Status:  status,
			Live:    live,
			Failed:  failed,
		}, true

	case "subagent":
		mode, _ := args["mode"].(string)
		who, text := subagentSummary(mode, args)
		steps, _ := details["steps"].(string)
		status := "done"
		switch {
		case live:
			status = "working"
		case failed:
			status = "failed"
		case strings.TrimSpace(steps) != "":
			n := countNonEmptyLines(steps)
			if n == 1 {
				status = "1 step"
			} else if n > 1 {
				status = fmt.Sprintf("%d steps", n)
			}
		}
		reply := ""
		if !live {
			reply = strings.TrimSpace(resultText)
		}
		return Message{
			Dir:    DirOut,
			Scope:  ScopeSubagent,
			Who:    who,
			Text:   text,
			Status: status,
			Reply:  reply,
			Steps:  strings.TrimSpace(steps),
			Live:   live,
			Failed: failed,
		}, true
	}
	return Message{}, false
}

func subagentSummary(mode string, args map[string]any) (who, text string) {
	if mode == "parallel" {
		if tasks, ok := args["tasks"].([]any); ok && len(tasks) > 0 {
			roles := make([]string, 0, len(tasks))
			lines := make([]string, 0, len(tasks))
			for _, raw := range tasks {
				entry, _ := raw.(map[string]any)
				role, _ := entry["agent"].(string)
				if strings.TrimSpace(role) == "" {
					role = "worker"
				}
				roles = append(roles, role)
				task, _ := entry["task"].(string)
				if line := firstNonEmptyLine(task); line != "" {
					lines = append(lines, role+": "+line)
				}
			}
			if len(roles) <= 3 {
				who = strings.Join(roles, ", ")
			} else {
				who = fmt.Sprintf("%d subagents", len(roles))
			}
			return who, strings.Join(lines, "\n")
		}
		return "subagents", ""
	}
	role, _ := args["agent"].(string)
	if strings.TrimSpace(role) == "" {
		role = "worker"
	}
	if model, _ := args["model"].(string); strings.TrimSpace(model) != "" {
		if idx := strings.LastIndex(model, "/"); idx >= 0 {
			model = model[idx+1:]
		}
		role += " · " + model
	}
	task, _ := args["task"].(string)
	return role, strings.TrimSpace(task)
}

func firstNonEmptyLine(s string) string {
	for line := range strings.SplitSeq(s, "\n") {
		if trimmed := strings.TrimSpace(line); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func countNonEmptyLines(s string) int {
	n := 0
	for line := range strings.SplitSeq(s, "\n") {
		if strings.TrimSpace(line) != "" {
			n++
		}
	}
	return n
}
