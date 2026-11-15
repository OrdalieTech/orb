package buzz

import (
	"bytes"
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"
)

// The agent sets itself up and waits for its owner, who approves it on their
// own machine; the attestation it gets back is checked, then its profile goes
// out, and the owner's record of it is as Buzz's app writes it.
func TestSetupAcrossTwoMachines(t *testing.T) {
	r := newRelay(t)
	relay := strings.Replace(r.server.URL, "http", "ws", 1)
	var out bytes.Buffer
	saved, err := Setup(context.Background(), []string{"--relay", relay, "--name", "Sales", "--respond-to", "allowlist"}, nil, strings.NewReader(""), &out)
	if err != nil {
		t.Fatal(err)
	}
	agent, err := parseKey(saved["BUZZ_PRIVATE_KEY"])
	if err != nil || saved["BUZZ_RELAY_URL"] != relay || saved["BUZZ_NAME"] != "Sales" || saved["BUZZ_AUTH_TAG"] != "" || !strings.Contains(out.String(), "--attest "+agent.public) {
		t.Fatalf("agent side saved %v, said\n%s", saved, out.String())
	}

	out.Reset()
	owner := "B7E151628AED2A6ABF7158809CF4F3C762E7160F38B4DA56A784D9045190CFEF"
	if none, err := Setup(context.Background(), []string{"--attest", agent.public, "--relay", relay, "--name", "Sales", "--respond-to", "allowlist", "--owner-key", "-"}, nil, strings.NewReader(owner+"\n"), &out); err != nil || none != nil {
		t.Fatalf("owner side: %v, %v", none, err)
	}
	_, tag, _ := strings.Cut(strings.TrimSpace(out.String()), "--auth '")
	tag = strings.TrimSuffix(tag, "'")
	r.mu.Lock()
	record := r.published[len(r.published)-1]
	r.mu.Unlock()
	var content map[string]any
	_ = json.Unmarshal([]byte(record.Content), &content)
	if record.Kind != 30177 || record.PubKey != ownerKey.public || record.tag("d") != agent.public || record.tag("runtime") != "orb" ||
		content["name"] != "Sales" || content["respond_to"] != "allowlist" || content["parallelism"] != float64(1) {
		t.Fatalf("owner's record %+v", record)
	}

	someone, _ := parseKey(newKey())
	if _, err := Setup(context.Background(), []string{"--auth", attestation(ownerKey, someone.public)}, saved, strings.NewReader(""), &out); err == nil {
		t.Fatal("an attestation of another key was taken")
	}
	out.Reset()
	saved, err = Setup(context.Background(), []string{"--auth", tag}, saved, strings.NewReader(""), &out)
	if err != nil || saved["BUZZ_AUTH_TAG"] != tag || !strings.Contains(out.String(), "Ready") {
		t.Fatalf("with its attestation: %v, %v\n%s", saved, err, out.String())
	}
	r.mu.Lock()
	profile := r.published[len(r.published)-1]
	r.mu.Unlock()
	if profile.Kind != 0 || profile.PubKey != agent.public || !slices.ContainsFunc(profile.Tags, func(tag []string) bool { return tag[0] == "auth" && tag[1] == ownerKey.public }) {
		t.Fatalf("profile %+v", profile)
	}
}

// An owner on the agent's machine approves it at once.
func TestSetupWithTheOwnersKeyHere(t *testing.T) {
	r := newRelay(t)
	var out bytes.Buffer
	saved, err := Setup(context.Background(), []string{"--relay", strings.Replace(r.server.URL, "http", "ws", 1), "--name", "Boss", "--owner-key", "-", "--json"}, nil,
		strings.NewReader("B7E151628AED2A6ABF7158809CF4F3C762E7160F38B4DA56A784D9045190CFEF\n"), &out)
	if err != nil {
		t.Fatal(err)
	}
	var status map[string]any
	if json.Unmarshal(out.Bytes(), &status) != nil || status["approved"] != true || status["owner"] != ownerKey.public || status["error"] != nil {
		t.Fatalf("status %s", out.String())
	}
	if _, err := New(Options{Key: saved["BUZZ_PRIVATE_KEY"], Relay: saved["BUZZ_RELAY_URL"], AuthTag: saved["BUZZ_AUTH_TAG"]}); err != nil {
		t.Fatalf("the saved settings do not run: %v", err)
	}
}
