package daemon

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestParseSSHConfig(t *testing.T) {
	cfg := `
# Global defaults
Host *
    ServerAliveInterval 60

Host ordalie-lab-3 lab3 *.internal !excluded
    HostName 10.0.0.3
    User ubuntu
    Port 2222

host=gpu-box
    hostname=192.168.1.50
    user = admin
    port=2200

Match host foo
    User ignored

Host ordalie-lab-3
    HostName 10.0.0.99
    User other
`
	got := ParseSSHConfig(cfg)
	want := []SSHHost{
		{Alias: "ordalie-lab-3", HostName: "10.0.0.3", User: "ubuntu", Port: 2222},
		{Alias: "lab3", HostName: "10.0.0.3", User: "ubuntu", Port: 2222},
		{Alias: "gpu-box", HostName: "192.168.1.50", User: "admin", Port: 2200},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ParseSSHConfig() = %#v, want %#v", got, want)
	}
}

func TestDiscoverSSHHostsIncludesAndCap(t *testing.T) {
	home := t.TempDir()
	sshDir := filepath.Join(home, ".ssh")
	confD := filepath.Join(sshDir, "config.d")
	if err := os.MkdirAll(confD, 0o700); err != nil {
		t.Fatal(err)
	}
	// Outside ~/.ssh/ should be ignored for safety.
	outside := filepath.Join(home, "outside.conf")
	if err := os.WriteFile(outside, []byte("Host secret\n  HostName 1.2.3.4\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	mainCfg := "Include config.d/*.conf\nInclude ../outside.conf\nHost alpha\n  HostName 10.0.0.1\n"
	if err := os.WriteFile(filepath.Join(sshDir, "config"), []byte(mainCfg), 0o600); err != nil {
		t.Fatal(err)
	}
	incCfg := "Host beta\n  HostName 10.0.0.2\n  User dev\nHost alpha\n  HostName 9.9.9.9\n"
	if err := os.WriteFile(filepath.Join(confD, "extra.conf"), []byte(incCfg), 0o600); err != nil {
		t.Fatal(err)
	}

	got := DiscoverSSHHosts(home)
	want := []SSHHost{
		{Alias: "alpha", HostName: "10.0.0.1"},
		{Alias: "beta", HostName: "10.0.0.2", User: "dev"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("DiscoverSSHHosts() = %#v, want %#v", got, want)
	}
}

func TestDetectLLMClients(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".claude"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(home, ".config", "opencode"), 0o700); err != nil {
		t.Fatal(err)
	}
	lookPath := func(name string) (string, error) {
		if name == "codex" || name == "aider" {
			return "/usr/local/bin/" + name, nil
		}
		return "", errors.New("not found")
	}

	got := DetectLLMClients(home, lookPath)
	want := []DetectedClient{
		{ID: "claude", Name: "Claude Code", Found: true},
		{ID: "codex", Name: "Codex", Found: true},
		{ID: "opencode", Name: "OpenCode", Found: true},
		{ID: "aider", Name: "Aider", Found: true},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("DetectLLMClients() = %#v, want %#v", got, want)
	}
}

func TestParseSSHTarget(t *testing.T) {
	valid := []struct {
		raw        string
		wantArgs   []string
		wantTarget string
	}{
		{
			raw:        "ordalie-lab-3",
			wantArgs:   []string{"ordalie-lab-3"},
			wantTarget: "ordalie-lab-3",
		},
		{
			raw:        "ubuntu@10.0.0.5",
			wantArgs:   []string{"ubuntu@10.0.0.5"},
			wantTarget: "ubuntu@10.0.0.5",
		},
		{
			raw:        "ssh -p 2222 -i ~/.ssh/id_ed25519 ubuntu@vps",
			wantArgs:   []string{"-p", "2222", "-i", "~/.ssh/id_ed25519", "ubuntu@vps"},
			wantTarget: "ubuntu@vps",
		},
		{
			raw:        "ssh -4 -A -C -J bastion host",
			wantArgs:   []string{"-4", "-A", "-C", "-J", "bastion", "host"},
			wantTarget: "host",
		},
		{
			raw:        "-l deploy -p2200 -o ForwardAgent=yes box",
			wantArgs:   []string{"-l", "deploy", "-p", "2200", "-o", "ForwardAgent=yes", "box"},
			wantTarget: "box",
		},
	}
	for _, tc := range valid {
		args, target, err := ParseSSHTarget(tc.raw)
		if err != nil {
			t.Errorf("ParseSSHTarget(%q) unexpected error: %v", tc.raw, err)
			continue
		}
		if !reflect.DeepEqual(args, tc.wantArgs) || target != tc.wantTarget {
			t.Errorf("ParseSSHTarget(%q) = (%#v, %q), want (%#v, %q)", tc.raw, args, target, tc.wantArgs, tc.wantTarget)
		}
	}

	rejected := []string{
		"",
		"   ",
		"ssh",
		"host; rm -rf /",
		"host | cat /etc/passwd",
		"host && id",
		"$(whoami)@host",
		"`id`@host",
		"host\nwhoami",
		"ssh -D 8080 host",
		"ssh -L 8080:localhost:80 host",
		"ssh -p abc host",
		"ssh -p",
		"host1 host2",
	}
	for _, raw := range rejected {
		if _, _, err := ParseSSHTarget(raw); err == nil {
			t.Errorf("ParseSSHTarget(%q) expected error, got nil", raw)
		}
	}
}

func TestBuildSpreadRemoteCommand(t *testing.T) {
	cmd := BuildSpreadRemoteCommand("abc'def")
	if !strings.Contains(cmd, "$HOME/.local/bin:/opt/homebrew/bin:/usr/local/bin") {
		t.Fatalf("missing PATH export in %q", cmd)
	}
	if !strings.Contains(cmd, "https://raw.githubusercontent.com/OrdalieTech/orb/main/scripts/install.sh") {
		t.Fatalf("missing install script in %q", cmd)
	}
	if !strings.Contains(cmd, `orb mitosis join 'abc'\''def'`) {
		t.Fatalf("missing single-quoted join command in %q", cmd)
	}
}
