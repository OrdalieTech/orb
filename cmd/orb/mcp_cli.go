package main

import (
	"bufio"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/OrdalieTech/orb/agent"
	"github.com/OrdalieTech/orb/plugins/mcp"
)

const mcpCommandUsage = `Usage:
  orb mcp add <server> [options] -- <command> [args...]
  orb mcp add <server> [options] --url <url>
  orb mcp remove <server> [-l]
  orb mcp list [--json]
  orb mcp login <server> [--timeout <seconds>]
  orb mcp logout <server>

Configure and check MCP servers and sign in to OAuth servers without starting
a session. Reads ~/.pi/agent/mcp.json and, in trusted projects, .pi/mcp.json.

Commands:
  add <server>            Add or replace a server in mcp.json
  remove <server>         Remove a server from mcp.json
  list                    Show state, tools, and errors (exits 1 on failure)
  login <server>          Sign in through the browser
  logout <server>         Delete the stored OAuth credentials

Options for add and remove:
  -l, --local             Use .pi/mcp.json in the current project instead of the global file

Options for add:
  --url <url>             Streamable HTTP server URL (instead of a command)
  --env <KEY=VALUE>       Environment variable for a stdio server (repeatable)
  --cwd <dir>             Working directory for a stdio server
  --header <KEY=VALUE>    HTTP header (repeatable)
  --bearer-token-env-var <NAME>
                          Send "Authorization: Bearer ${NAME}"
  --oauth-client-id <id>  Pre-registered OAuth client id
  --oauth-client-secret <secret>
                          OAuth client secret (may be ${NAME} or !command)
  --oauth-callback-port <port>
                          Fixed OAuth callback port
  --oauth-client-name <name>
                          Client name sent when registering with the OAuth server
  --exposure <mode>       codemode (default, loaded through tool_search), deferred, direct, or hidden
  --description <text>    What the server offers, shown in the system prompt

Other options:
  --json                  Print the list as JSON
  --timeout <seconds>     How long login waits for the browser (default: 300)`

const mcpHelpHint = `Use "orb mcp --help" for usage.`

type mcpOptions struct {
	positional []string
	values     map[string]string
	lists      map[string][]string
}

// parseMCPOptions reads --name value options of the given kinds ("flag",
// "value" or "list"). "--", or reaching maxPositionals positional arguments,
// ends the options, so a server command's own flags pass through.
func parseMCPOptions(args []string, known map[string]string, maxPositionals int) (mcpOptions, error) {
	parsed := mcpOptions{values: map[string]string{}, lists: map[string][]string{}}
	for index := 0; index < len(args); index++ {
		arg := args[index]
		if arg == "-l" {
			arg = "--local"
		}
		if arg == "--" || len(parsed.positional) >= maxPositionals {
			if arg == "--" {
				index++
			}
			parsed.positional = append(parsed.positional, args[index:]...)
			break
		}
		if !strings.HasPrefix(arg, "--") {
			parsed.positional = append(parsed.positional, arg)
			continue
		}
		name := arg[2:]
		switch known[name] {
		case "flag":
			parsed.values[name] = "true"
		case "value", "list":
			if index+1 >= len(args) {
				return parsed, fmt.Errorf("%s needs a value", arg)
			}
			index++
			if known[name] == "list" {
				parsed.lists[name] = append(parsed.lists[name], args[index])
			} else {
				parsed.values[name] = args[index]
			}
		default:
			return parsed, fmt.Errorf("unknown option %s\n%s", arg, mcpHelpHint)
		}
	}
	return parsed, nil
}

// handleMCPCommand configures and checks MCP servers outside a session.
func handleMCPCommand(ctx context.Context, argv []string, streams cliStreams) (bool, int) {
	if len(argv) == 0 || argv[0] != "mcp" {
		return false, 0
	}
	args := argv[1:]
	for _, arg := range args {
		if arg == "--help" || arg == "-h" {
			args = nil
		}
	}
	if len(args) == 0 || args[0] == "help" {
		_, _ = fmt.Fprintln(streams.Stdout, mcpCommandUsage)
		return true, 0
	}
	cwd, agentDir, err := packageCommandDirs()
	if err != nil {
		return true, reportCLIError(streams.Stderr, err)
	}
	fail := func(message string) (bool, int) {
		_, _ = fmt.Fprintln(streams.Stderr, message)
		return true, 1
	}
	command, rest := args[0], args[1:]
	switch command {
	case "add":
		return true, addMCPServer(ctx, cwd, agentDir, rest, streams)
	case "remove":
		parsed, err := parseMCPOptions(rest, map[string]string{"local": "flag"}, 1<<30)
		if err != nil {
			return fail(err.Error())
		}
		if len(parsed.positional) != 1 {
			return fail("Usage: orb mcp remove <server> [-l]\n" + mcpHelpHint)
		}
		return true, removeMCPServer(ctx, cwd, agentDir, parsed.positional[0], parsed.values["local"] != "", streams)
	case "list":
		parsed, err := parseMCPOptions(rest, map[string]string{"json": "flag"}, 1<<30)
		if err != nil {
			return fail(err.Error())
		}
		if len(parsed.positional) > 0 {
			return fail("Usage: orb mcp list [--json]\n" + mcpHelpHint)
		}
		return true, listMCPServers(ctx, cwd, agentDir, parsed.values["json"] != "", streams)
	case "login", "logout":
		known := map[string]string{}
		if command == "login" {
			known["timeout"] = "value"
		}
		parsed, err := parseMCPOptions(rest, known, 1<<30)
		if err != nil {
			return fail(err.Error())
		}
		if len(parsed.positional) != 1 {
			return fail("Usage: orb mcp " + command + " <server>\n" + mcpHelpHint)
		}
		return true, signInMCPServer(ctx, cwd, agentDir, command, parsed.positional[0], parsed.values["timeout"], streams)
	}
	return fail(fmt.Sprintf("Unknown mcp command %q.\n%s", command, mcpHelpHint))
}

func projectTrusted(ctx context.Context, cwd, agentDir string) bool {
	store, err := stateFromContext(ctx).trust(agentDir)
	if err != nil {
		return false
	}
	decision, err := store.Get(cwd)
	return err == nil && decision != nil && *decision
}

func parsePairs(option string, pairs []string) (map[string]string, error) {
	if len(pairs) == 0 {
		return nil, nil
	}
	record := make(map[string]string, len(pairs))
	for _, pair := range pairs {
		key, value, found := strings.Cut(pair, "=")
		if !found || key == "" {
			return nil, fmt.Errorf("--%s expects KEY=VALUE, got %q", option, pair)
		}
		record[key] = value
	}
	return record, nil
}

func addMCPServer(ctx context.Context, cwd, agentDir string, args []string, streams cliStreams) int {
	fail := func(message string) int {
		_, _ = fmt.Fprintln(streams.Stderr, message)
		return 1
	}
	parsed, err := parseMCPOptions(args, map[string]string{
		"local": "flag", "url": "value", "env": "list", "cwd": "value", "header": "list", "bearer-token-env-var": "value",
		"oauth-client-id": "value", "oauth-client-secret": "value", "oauth-callback-port": "value", "oauth-client-name": "value",
		"exposure": "value", "description": "value",
	}, 2)
	if err != nil {
		return fail(err.Error())
	}
	_, hasURL := parsed.values["url"]
	if len(parsed.positional) == 0 || hasURL == (len(parsed.positional) > 1) {
		return fail("Usage: orb mcp add <server> [options] (--url <url> | -- <command> [args...])\n" + mcpHelpHint)
	}
	name, command := parsed.positional[0], parsed.positional[1:]
	misplaced := []string{"env", "cwd"}
	if !hasURL {
		misplaced = []string{"header", "bearer-token-env-var", "oauth-client-id", "oauth-client-secret", "oauth-callback-port", "oauth-client-name"}
	}
	for _, option := range misplaced {
		_, isValue := parsed.values[option]
		if isValue || len(parsed.lists[option]) > 0 {
			if hasURL {
				return fail("--" + option + " only applies to stdio servers.")
			}
			return fail("--" + option + " only applies to HTTP servers (--url).")
		}
	}
	server := mcp.ServerConfig{Exposure: mcp.Exposure(parsed.values["exposure"]), Description: parsed.values["description"]}
	if hasURL {
		server.URL = parsed.values["url"]
		if server.Headers, err = parsePairs("header", parsed.lists["header"]); err != nil {
			return fail(err.Error())
		}
		if variable, ok := parsed.values["bearer-token-env-var"]; ok {
			if server.Headers == nil {
				server.Headers = map[string]string{}
			}
			server.Headers["Authorization"] = "Bearer ${" + variable + "}"
		}
		oauth := mcp.OAuthConfig{ClientID: parsed.values["oauth-client-id"], ClientSecret: parsed.values["oauth-client-secret"], ClientName: parsed.values["oauth-client-name"]}
		if port, ok := parsed.values["oauth-callback-port"]; ok {
			if oauth.CallbackPort, err = strconv.Atoi(port); err != nil {
				return fail("--oauth-callback-port must be a port number.")
			}
		}
		if oauth != (mcp.OAuthConfig{}) {
			server.OAuth = &oauth
		}
	} else {
		server.Command, server.Args, server.CWD = command[0], command[1:], parsed.values["cwd"]
		if server.Env, err = parsePairs("env", parsed.lists["env"]); err != nil {
			return fail(err.Error())
		}
	}
	if err := mcp.Validate(name, &server); err != nil {
		return fail(err.Error())
	}
	local := parsed.values["local"] != ""
	path, scope := mcp.GlobalPath(agentDir), "global"
	if local {
		path, scope = mcp.ProjectPath(cwd), "project"
	}
	replaced, err := mcp.AddServer(path, name, server)
	if err != nil {
		return fail(fmt.Sprintf("Could not update %s: %v", path, err))
	}
	verb := "Added"
	if replaced {
		verb = "Replaced"
	}
	_, _ = fmt.Fprintf(streams.Stdout, "%s %s MCP server %q in %s.\n", verb, scope, name, path)
	if local && !projectTrusted(ctx, cwd, agentDir) {
		_, _ = fmt.Fprintf(streams.Stdout, "The project is not trusted, so %s is ignored until you start orb in the project and trust it.\n", path)
	}
	check := "Check it with: orb mcp list"
	if server.UsesOAuth() {
		check += ". If it requires sign-in: orb mcp login " + name
	}
	_, _ = fmt.Fprintln(streams.Stdout, check)
	return 0
}

func removeMCPServer(ctx context.Context, cwd, agentDir, name string, local bool, streams cliStreams) int {
	path, scope := mcp.GlobalPath(agentDir), "global"
	if local {
		path, scope = mcp.ProjectPath(cwd), "project"
	}
	removed, err := mcp.RemoveServer(path, name)
	if err != nil {
		_, _ = fmt.Fprintf(streams.Stderr, "Could not update %s: %v\n", path, err)
		return 1
	}
	if removed {
		_, _ = fmt.Fprintf(streams.Stdout, "Removed %s MCP server %q from %s.\n", scope, name, path)
		return 0
	}
	message := fmt.Sprintf("No %s MCP server named %q in %s.", scope, name, path)
	entries, _ := mcp.Load(agentDir, cwd, true)
	for _, entry := range entries {
		if entry.Name == name && entry.Scope != scope {
			hint := "; omit --local."
			if entry.Scope == "project" {
				hint = "; use --local."
			}
			message += " It is defined in " + entry.Source + hint
		}
	}
	_, _ = fmt.Fprintln(streams.Stderr, message)
	return 1
}

type mcpServerReport struct {
	Name         string            `json:"name"`
	Scope        string            `json:"scope"`
	Source       string            `json:"source"`
	Enabled      bool              `json:"enabled"`
	Exposure     string            `json:"exposure"`
	Transport    string            `json:"transport"`
	State        string            `json:"state"`
	Tools        []string          `json:"tools"`
	ToolExposure map[string]string `json:"toolExposure,omitempty"`
	Error        string            `json:"error,omitempty"`
}

func listMCPServers(ctx context.Context, cwd, agentDir string, asJSON bool, streams cliStreams) int {
	trusted := projectTrusted(ctx, cwd, agentDir)
	entries, problems := mcp.Load(agentDir, cwd, trusted)
	note := ""
	if _, err := os.Stat(mcp.ProjectPath(cwd)); !trusted && err == nil {
		note = mcp.ProjectPath(cwd) + " is ignored because the project is not trusted. Start orb in the project to trust it."
	}
	status := map[string]mcp.ServerStatus{}
	for _, server := range mcp.Probe(ctx, cwd, agentDir, entries) {
		status[server.Name] = server
	}
	reports := make([]mcpServerReport, 0, len(entries))
	failed := len(problems) > 0
	for _, entry := range entries {
		server := status[entry.Name]
		report := mcpServerReport{
			Name: entry.Name, Scope: entry.Scope, Source: entry.Source, Enabled: entry.Config.IsEnabled(),
			Exposure: string(entry.Config.ExposureOf()), Transport: server.Target, State: string(server.State), Tools: server.Tools,
		}
		if report.Tools == nil {
			report.Tools = []string{}
		}
		for _, tool := range server.Tools {
			if exposure := entry.Config.ToolExposureOf(tool); exposure != entry.Config.ExposureOf() {
				if report.ToolExposure == nil {
					report.ToolExposure = map[string]string{}
				}
				report.ToolExposure[tool] = string(exposure)
			}
		}
		if server.State != mcp.ServerConnected {
			report.Error = server.Error
			failed = failed || report.Enabled
		}
		reports = append(reports, report)
	}
	code := 0
	if failed {
		code = 1
	}
	if asJSON {
		output := map[string]any{"servers": reports, "errors": problems}
		if problems == nil {
			output["errors"] = []string{}
		}
		if note != "" {
			output["note"] = note
		}
		encoded, _ := json.MarshalIndent(output, "", "  ")
		_, _ = fmt.Fprintln(streams.Stdout, string(encoded))
		return code
	}
	if len(reports) == 0 && len(problems) == 0 {
		_, _ = fmt.Fprintf(streams.Stdout, "No MCP servers configured. Add them with orb mcp add, or to %s or .pi/mcp.json.\n", mcp.GlobalPath(agentDir))
	}
	for _, report := range reports {
		state := report.State
		if report.State == string(mcp.ServerConnected) {
			state = fmt.Sprintf("connected, %d tool%s", len(report.Tools), map[bool]string{true: "", false: "s"}[len(report.Tools) == 1])
		}
		if report.State == string(mcp.ServerNeedsAuth) {
			state = "needs sign-in"
		}
		_, _ = fmt.Fprintf(streams.Stdout, "%s: %s (%s, %s)\n  %s\n", report.Name, state, report.Exposure, report.Scope, report.Transport)
		if report.State == string(mcp.ServerNeedsAuth) {
			_, _ = fmt.Fprintf(streams.Stdout, "  sign in with: orb mcp login %s\n", report.Name)
		}
		if len(report.Tools) > 0 {
			tools := make([]string, 0, len(report.Tools))
			for _, tool := range report.Tools {
				if exposure := report.ToolExposure[tool]; exposure != "" {
					tool += " [" + exposure + "]"
				}
				tools = append(tools, tool)
			}
			_, _ = fmt.Fprintf(streams.Stdout, "  tools: %s\n", strings.Join(tools, ", "))
		}
		if report.Error != "" {
			_, _ = fmt.Fprintf(streams.Stdout, "  %s\n", strings.ReplaceAll(report.Error, "\n", "\n  "))
		}
	}
	for _, problem := range problems {
		_, _ = fmt.Fprintln(streams.Stdout, "config error: "+problem)
	}
	if note != "" {
		_, _ = fmt.Fprintln(streams.Stdout, note)
	}
	return code
}

func signInMCPServer(ctx context.Context, cwd, agentDir, command, name, timeout string, streams cliStreams) int {
	trusted := projectTrusted(ctx, cwd, agentDir)
	entries, _ := mcp.Load(agentDir, cwd, trusted)
	index := slices.IndexFunc(entries, func(entry mcp.Entry) bool { return entry.Name == name })
	if index < 0 {
		names := make([]string, 0, len(entries))
		for _, entry := range entries {
			names = append(names, entry.Name)
		}
		_, _ = fmt.Fprintf(streams.Stderr, "No MCP server named %q. Configured: %s.\n", name, cmp.Or(strings.Join(names, ", "), "none"))
		return 1
	}
	entry := entries[index]
	if !entry.Config.UsesOAuth() {
		_, _ = fmt.Fprintf(streams.Stderr, "MCP server %q does not use OAuth. Only HTTP servers without an Authorization header do.\n", name)
		return 1
	}
	if command == "logout" {
		removed, err := mcp.RemoveCredentials(agentDir, name, entry.Config.URL)
		if err != nil {
			return reportCLIError(streams.Stderr, err)
		}
		if removed {
			_, _ = fmt.Fprintf(streams.Stdout, "Signed out of MCP server %q.\n", name)
		} else {
			_, _ = fmt.Fprintf(streams.Stdout, "No stored credentials for MCP server %q.\n", name)
		}
		return 0
	}
	seconds := 300.0
	if timeout != "" {
		parsed, err := strconv.ParseFloat(timeout, 64)
		if err != nil || parsed <= 0 {
			_, _ = fmt.Fprintln(streams.Stderr, "--timeout must be a positive number of seconds.")
			return 1
		}
		seconds = parsed
	}
	loginCtx, cancel := context.WithTimeout(ctx, time.Duration(seconds*float64(time.Second)))
	defer cancel()
	tools, already, err := mcp.Login(loginCtx, cwd, agentDir, entry, func(target string) {
		_, _ = fmt.Fprintf(streams.Stdout, "Sign in to MCP server %q in your browser:\n%s\n", name, target)
		agent.OpenBrowser(target)
	}, func(pasteCtx context.Context) (string, error) {
		// Without a terminal only the browser callback can finish the sign-in.
		if !streams.StdinTTY {
			<-pasteCtx.Done()
			return "", nil
		}
		_, _ = fmt.Fprint(streams.Stderr, "If the browser cannot reach this machine, paste the URL it was redirected to: ")
		line := make(chan string, 1)
		go func() {
			text, _ := bufio.NewReader(streams.Stdin).ReadString('\n')
			line <- text
		}()
		select {
		case text := <-line:
			return text, nil
		case <-pasteCtx.Done():
			return "", nil
		}
	})
	switch {
	case errors.Is(err, mcp.ErrSignInCancelled):
		_, _ = fmt.Fprintf(streams.Stderr, "Sign-in to MCP server %q was cancelled or not completed within %g seconds.\n", name, seconds)
		return 1
	case err != nil:
		_, _ = fmt.Fprintf(streams.Stderr, "Sign-in to MCP server %q failed: %v\n", name, err)
		return 1
	case already:
		_, _ = fmt.Fprintf(streams.Stdout, "Already signed in to MCP server %q (%d tools).\n", name, tools)
	default:
		_, _ = fmt.Fprintf(streams.Stdout, "Signed in to MCP server %q (%d tools).\n", name, tools)
	}
	return 0
}
