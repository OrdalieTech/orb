package main

import (
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/OrdalieTech/orb/agent/config"
)

func TestEntryIdentityDoesNotMasqueradeAsPi(t *testing.T) {
	for _, hint := range []string{"pi", "other-agent", ""} {
		t.Run(hint, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			t.Setenv("USERPROFILE", home)
			t.Setenv("ORB_AGENT_DIR", "")
			t.Setenv("PI_CODING_AGENT_DIR", filepath.Join(home, ".pi", "agent"))
			t.Setenv("PI_CODING_AGENT", "true")
			t.Setenv("AI_AGENT", "pi")
			t.Setenv("ORB_CODING_AGENT", "")
			t.Setenv("HERDR_AGENT", hint)
			setEntryIdentity()
			if os.Getenv("AI_AGENT") != "orb" || os.Getenv("ORB_CODING_AGENT") != "true" {
				t.Fatal("entry did not identify as Orb")
			}
			if _, exists := os.LookupEnv("PI_CODING_AGENT"); exists {
				t.Fatal("inherited Pi marker survived")
			}
			wantHint := hint
			if hint == "pi" {
				wantHint = ""
			}
			if os.Getenv("HERDR_AGENT") != wantHint {
				t.Fatal("wrong Herdr identity hint")
			}
			dir, err := config.GetAgentDir()
			if err != nil || dir != filepath.Join(home, ".orb", "agent") {
				t.Fatalf("agent directory = %q, %v", dir, err)
			}
		})
	}
}

func TestStartupVersionCheckIgnoresPiControls(t *testing.T) {
	t.Setenv("PI_OFFLINE", "1")
	t.Setenv("PI_SKIP_VERSION_CHECK", "1")
	t.Setenv("ORB_OFFLINE", "")
	t.Setenv("ORB_SKIP_VERSION_CHECK", "")
	requests := 0
	client := &http.Client{Transport: versionRoundTrip(func(*http.Request) (*http.Response, error) {
		requests++
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"tag_name":"v1.2.4"}`))}, nil
	})}
	newStartupVersionCheck("1.2.3", client, latestReleaseURL, time.Second)(context.Background(), &versionNotificationUI{})
	if requests != 1 {
		t.Fatalf("Pi controls suppressed Orb version check: requests=%d", requests)
	}
}

func TestHelpDocumentsIndependentGlobalRoots(t *testing.T) {
	for _, text := range []string{"--agent-dir", "--state-home", "--bridge-home", "ORB_AGENT_DIR", "ORB_STATE_HOME", "ORB_BRIDGE_HOME", "~/.orb/agent", "ORB_OFFLINE=1", "before runtime options/subcommands"} {
		if !strings.Contains(helpText, text) {
			t.Errorf("help missing %q", text)
		}
	}
	if strings.Contains(helpText, "must be the first argument") || strings.Contains(helpText, "PI_OFFLINE") {
		t.Fatal("help still describes old startup controls")
	}
}
