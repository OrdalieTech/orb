package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/OrdalieTech/orb/mitosis"
	"github.com/OrdalieTech/orb/mitosis/protocol"
)

const (
	maxSSHHosts   = 64
	spreadTimeout = 90 * time.Second
)

// SSHHost is one concrete Host entry discovered from ~/.ssh/config.
type SSHHost struct {
	Alias    string `json:"alias"`
	HostName string `json:"hostname,omitempty"`
	User     string `json:"user,omitempty"`
	Port     int    `json:"port,omitempty"`
}

// DetectedClient is a local AI/LLM CLI discovered on this machine.
type DetectedClient struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Found bool   `json:"found"`
}

// ParseSSHConfig parses OpenSSH Host, HostName, User, and Port blocks.
// It skips wildcard patterns (*, ?, leading !) and deduplicates by alias.
func ParseSSHConfig(content string) []SSHHost {
	hosts, _ := parseSSHConfigWithIncludes(content)
	return hosts
}

func parseSSHConfigWithIncludes(content string) ([]SSHHost, []string) {
	var (
		order    []string
		byAlias  = map[string]*SSHHost{}
		current  []*SSHHost
		includes []string
	)
	for line := range strings.SplitSeq(content, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, val, ok := splitSSHDirective(line)
		if !ok {
			continue
		}
		switch strings.ToLower(key) {
		case "include":
			includes = append(includes, strings.Fields(val)...)
		case "match":
			current = nil
		case "host":
			current = nil
			for _, pattern := range strings.Fields(val) {
				if strings.ContainsAny(pattern, "*?") || strings.HasPrefix(pattern, "!") {
					continue
				}
				h, exists := byAlias[pattern]
				if !exists {
					h = &SSHHost{Alias: pattern}
					byAlias[pattern] = h
					order = append(order, pattern)
				}
				current = append(current, h)
			}
		case "hostname":
			for _, h := range current {
				if h.HostName == "" {
					h.HostName = val
				}
			}
		case "user":
			for _, h := range current {
				if h.User == "" {
					h.User = val
				}
			}
		case "port":
			if port, err := strconv.Atoi(val); err == nil && port > 0 && port <= 65535 {
				for _, h := range current {
					if h.Port == 0 {
						h.Port = port
					}
				}
			}
		}
	}
	out := make([]SSHHost, 0, len(order))
	for _, alias := range order {
		out = append(out, *byAlias[alias])
	}
	return out, includes
}

func splitSSHDirective(line string) (string, string, bool) {
	// OpenSSH accepts "Key Value", "Key=Value", or "Key = Value".
	sep := strings.IndexAny(line, " \t=")
	if sep <= 0 {
		return "", "", false
	}
	key := strings.TrimSpace(line[:sep])
	rest := strings.TrimSpace(line[sep:])
	rest = strings.TrimLeft(rest, "=")
	rest = strings.TrimSpace(rest)
	if key == "" || rest == "" {
		return "", "", false
	}
	return key, rest, true
}

// DiscoverSSHHosts reads ~/.ssh/config and follows simple Include globs within ~/.ssh/,
// capped at 64 hosts.
func DiscoverSSHHosts(home string) []SSHHost {
	if home == "" {
		var err error
		home, err = os.UserHomeDir()
		if err != nil || home == "" {
			return nil
		}
	}
	sshDir := filepath.Clean(filepath.Join(home, ".ssh"))
	visited := map[string]bool{}
	seen := map[string]bool{}
	var out []SSHHost

	var readFile func(path string)
	readFile = func(path string) {
		if len(out) >= maxSSHHosts {
			return
		}
		clean := filepath.Clean(path)
		if visited[clean] {
			return
		}
		visited[clean] = true
		data, err := os.ReadFile(clean)
		if err != nil {
			return
		}
		hosts, includes := parseSSHConfigWithIncludes(string(data))
		for _, h := range hosts {
			if len(out) >= maxSSHHosts {
				return
			}
			if !seen[h.Alias] {
				seen[h.Alias] = true
				out = append(out, h)
			}
		}
		for _, inc := range includes {
			if len(out) >= maxSSHHosts {
				return
			}
			pattern := inc
			if strings.HasPrefix(pattern, "~/") {
				pattern = filepath.Join(home, pattern[2:])
			} else if !filepath.IsAbs(pattern) {
				pattern = filepath.Join(sshDir, pattern)
			}
			matches, err := filepath.Glob(pattern)
			if err != nil {
				continue
			}
			for _, m := range matches {
				rel, err := filepath.Rel(sshDir, filepath.Clean(m))
				if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
					continue
				}
				readFile(m)
			}
		}
	}

	readFile(filepath.Join(sshDir, "config"))
	return out
}

// DetectLLMClients checks common local AI/LLM CLIs via lookPath or existing config dirs,
// returning all found ones.
func DetectLLMClients(home string, lookPath func(string) (string, error)) []DetectedClient {
	if home == "" {
		home, _ = os.UserHomeDir()
	}
	if lookPath == nil {
		lookPath = exec.LookPath
	}
	candidates := []struct {
		id, name, configDir string
	}{
		{"claude", "Claude Code", ".claude"},
		{"codex", "Codex", ".codex"},
		{"opencode", "OpenCode", filepath.Join(".config", "opencode")},
		{"pi", "Pi", ".pi"},
		{"gemini", "Gemini CLI", ".gemini"},
		{"cursor", "Cursor", ".cursor"},
		{"aider", "Aider", ""},
	}
	var found []DetectedClient
	for _, c := range candidates {
		ok := false
		if _, err := lookPath(c.id); err == nil {
			ok = true
		} else if home != "" && c.configDir != "" {
			if info, err := os.Stat(filepath.Join(home, c.configDir)); err == nil && info.IsDir() {
				ok = true
			}
		}
		if ok {
			found = append(found, DetectedClient{ID: c.id, Name: c.name, Found: true})
		}
	}
	return found
}

// ParseSSHTarget parses user input like "ordalie-lab-3", "ubuntu@10.0.0.5",
// "ssh -p 2222 -i ~/.ssh/id_ed25519 ubuntu@vps", or "ssh -J bastion host"
// into safe ssh argv and a display target name.
func ParseSSHTarget(raw string) ([]string, string, error) {
	if strings.ContainsAny(raw, ";|&$`\n\r") {
		return nil, "", errors.New("ssh target contains disallowed shell characters")
	}
	tokens := strings.Fields(strings.TrimSpace(raw))
	if len(tokens) == 0 {
		return nil, "", errors.New("ssh target is required")
	}
	if tokens[0] == "ssh" {
		tokens = tokens[1:]
		if len(tokens) == 0 {
			return nil, "", errors.New("ssh target is required")
		}
	}

	var args []string
	var target string
	for i := 0; i < len(tokens); i++ {
		tok := tokens[i]
		if !strings.HasPrefix(tok, "-") {
			if target != "" {
				return nil, "", fmt.Errorf("unexpected extra ssh argument %q", tok)
			}
			target = tok
			continue
		}
		if len(tok) < 2 {
			return nil, "", fmt.Errorf("invalid ssh flag %q", tok)
		}
		flag := tok[:2]
		switch flag {
		case "-4", "-6", "-A", "-C":
			if len(tok) != 2 {
				return nil, "", fmt.Errorf("unsupported ssh flag %q", tok)
			}
			args = append(args, flag)
		case "-p", "-i", "-J", "-l", "-F", "-o":
			var val string
			if len(tok) > 2 {
				val = tok[2:]
			} else {
				i++
				if i >= len(tokens) {
					return nil, "", fmt.Errorf("ssh flag %s requires a value", flag)
				}
				val = tokens[i]
			}
			if val == "" || strings.HasPrefix(val, "-") {
				return nil, "", fmt.Errorf("ssh flag %s requires a value", flag)
			}
			if flag == "-p" {
				if p, err := strconv.Atoi(val); err != nil || p <= 0 || p > 65535 {
					return nil, "", fmt.Errorf("invalid ssh port %q", val)
				}
			}
			args = append(args, flag, val)
		default:
			return nil, "", fmt.Errorf("unsupported ssh flag %q", tok)
		}
	}
	if target == "" {
		return nil, "", errors.New("ssh target host is required")
	}
	args = append(args, target)
	return args, target, nil
}

// BuildSpreadRemoteCommand returns a POSIX sh script that ensures orb is installed
// and runs `orb mitosis join` with the single-quoted invitation code.
func BuildSpreadRemoteCommand(code string) string {
	quoted := "'" + strings.ReplaceAll(code, "'", `'\''`) + "'"
	return `export PATH="$HOME/.local/bin:/opt/homebrew/bin:/usr/local/bin:$PATH"; ` +
		`command -v orb >/dev/null 2>&1 || curl -fsSL https://raw.githubusercontent.com/OrdalieTech/orb/main/scripts/install.sh | sh; ` +
		`orb mitosis join ` + quoted
}

func (s *Service) hostSpreadInfo() (json.RawMessage, error) {
	home, _ := os.UserHomeDir()
	out := struct {
		SSHHosts []SSHHost        `json:"ssh_hosts"`
		Clients  []DetectedClient `json:"clients"`
	}{
		SSHHosts: DiscoverSSHHosts(home),
		Clients:  DetectLLMClients(home, exec.LookPath),
	}
	if out.SSHHosts == nil {
		out.SSHHosts = []SSHHost{}
	}
	if out.Clients == nil {
		out.Clients = []DetectedClient{}
	}
	return mitosis.JSON(out), nil
}

func (s *Service) hostSpreadSSH(ctx context.Context, params json.RawMessage) (json.RawMessage, error) {
	var q struct {
		Target string `json:"target"`
		Code   string `json:"code"`
	}
	if err := protocol.Decode(params, &q); err != nil {
		return nil, err
	}
	sshArgs, target, err := ParseSSHTarget(q.Target)
	if err != nil {
		return nil, mitosis.Fail(err.Error())
	}
	code := strings.TrimSpace(q.Code)
	if code == "" {
		return nil, mitosis.Fail("invitation code is required")
	}
	remoteCmd := BuildSpreadRemoteCommand(code)
	argv := append([]string{
		"-o", "BatchMode=yes",
		"-o", "ConnectTimeout=10",
		"-o", "StrictHostKeyChecking=accept-new",
	}, append(sshArgs, remoteCmd)...)

	runCtx, cancel := context.WithTimeout(ctx, spreadTimeout)
	defer cancel()
	cmd := exec.CommandContext(runCtx, "ssh", argv...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = strings.TrimSpace(stdout.String())
		}
		if msg == "" {
			msg = err.Error()
		}
		return nil, mitosis.Fail(fmt.Sprintf("ssh %s failed: %s", target, msg))
	}
	return mitosis.JSON(map[string]any{
		"ok":     true,
		"target": target,
		"output": strings.TrimSpace(stdout.String()),
	}), nil
}
