package main

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/OrdalieTech/orb/agent/config"
	"github.com/OrdalieTech/orb/agent/mcp"
)

const mcpCommandUsage = `orb mcp <command>

Commands:
  orb mcp list                          List configured MCP servers
  orb mcp get <name>                    Print one server's configuration as JSON
  orb mcp add <name> --url <url> [--header k=v]...
  orb mcp add <name> [--env K=V]... [--cwd dir] -- <command> [args...]
  orb mcp remove <name>                 Delete a server from the global settings
  orb mcp enable|disable <name>         Toggle a server without deleting it

Options for add: --timeout <ms>, --disabled. Servers are written to the global
settings (~/.pi/agent/settings.json, "mcpServers"); project settings overlay
per server name. See docs/plugins.md.`

// handleMCPCommand configures MCP servers from outside a session. It only
// reads and edits settings — no server is ever spawned here.
func handleMCPCommand(ctx context.Context, argv []string, streams cliStreams) (bool, int) {
	if len(argv) == 0 || argv[0] != "mcp" {
		return false, 0
	}
	if len(argv) == 1 || argv[1] == "--help" || argv[1] == "-h" || argv[1] == "help" {
		_, _ = fmt.Fprintln(streams.Stdout, mcpCommandUsage)
		return true, 0
	}
	cwd, agentDir, err := packageCommandDirs()
	if err != nil {
		return true, reportCLIError(streams.Stderr, err)
	}
	settings, trustWarnings, err := createCommandSettingsManager(ctx, cwd, agentDir, nil, false)
	if err != nil {
		return true, reportCLIError(streams.Stderr, err)
	}
	for _, warning := range trustWarnings {
		_, _ = fmt.Fprintln(streams.Stderr, "Warning: "+warning)
	}
	action, rest := argv[1], argv[2:]
	switch action {
	case "list":
		if len(rest) != 0 {
			break
		}
		return true, listMCPServers(settings, streams)
	case "get":
		if len(rest) != 1 {
			break
		}
		server, exists := settings.GetMCPServers()[rest[0]]
		if !exists {
			_, _ = fmt.Fprintf(streams.Stderr, "Unknown MCP server %q.\n", rest[0])
			return true, 1
		}
		encoded, err := json.MarshalIndent(server, "", "  ")
		if err != nil {
			return true, reportCLIError(streams.Stderr, err)
		}
		_, _ = fmt.Fprintln(streams.Stdout, string(encoded))
		return true, 0
	case "add":
		if len(rest) < 1 {
			break
		}
		return true, addMCPServer(settings, rest[0], rest[1:], streams)
	case "remove":
		if len(rest) != 1 {
			break
		}
		return true, removeMCPServer(settings, rest[0], streams)
	case "enable", "disable":
		if len(rest) != 1 {
			break
		}
		name := rest[0]
		if _, exists := settings.GetMCPServers()[name]; !exists {
			_, _ = fmt.Fprintf(streams.Stderr, "Unknown MCP server %q.\n", name)
			return true, 1
		}
		settings.SetMCPServerEnabled(name, action == "enable")
		if errors := settings.DrainErrors(); len(errors) > 0 {
			return true, reportCLIError(streams.Stderr, errors[0])
		}
		_, _ = fmt.Fprintf(streams.Stdout, "%sd %s\n", strings.ToUpper(action[:1])+action[1:], name)
		return true, 0
	}
	_, _ = fmt.Fprintln(streams.Stderr, "Usage: "+mcpCommandUsage)
	return true, 1
}

func mcpServerEnabled(server map[string]any) bool {
	if value, ok := server["enabled"].(bool); ok && !value {
		return false
	}
	if value, ok := server["disabled"].(bool); ok && value {
		return false
	}
	return true
}

func listMCPServers(settings *config.SettingsManager, streams cliStreams) int {
	servers := settings.GetMCPServers()
	_, warnings, err := mcp.ParseSettingsWithWarnings(settings.GetSettings())
	if err != nil {
		_, _ = fmt.Fprintln(streams.Stderr, "Warning: "+err.Error())
	}
	for _, warning := range warnings {
		_, _ = fmt.Fprintln(streams.Stderr, "Warning: "+warning)
	}
	names := make([]string, 0, len(servers))
	for name := range servers {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		server := servers[name]
		state := "off"
		if mcpServerEnabled(server) {
			state = "on"
		}
		transport, target := "stdio", ""
		if url, ok := server["url"].(string); ok && url != "" {
			transport, target = "http", url
		} else if command, ok := server["command"].(string); ok {
			target = command
			if args, ok := server["args"].([]any); ok {
				for _, arg := range args {
					if text, ok := arg.(string); ok {
						target += " " + text
					}
				}
			}
		}
		_, _ = fmt.Fprintf(streams.Stdout, "%s\t%s\t%s\t%s\n", name, transport, state, target)
	}
	return 0
}

func addMCPServer(settings *config.SettingsManager, name string, arguments []string, streams cliStreams) int {
	server := map[string]any{}
	env := map[string]any{}
	headers := map[string]any{}
	usage := func(message string) int {
		_, _ = fmt.Fprintln(streams.Stderr, message+"\nUsage: "+mcpCommandUsage)
		return 1
	}
	index := 0
	for index < len(arguments) {
		argument := arguments[index]
		value := func() (string, bool) {
			if index+1 >= len(arguments) {
				return "", false
			}
			index++
			return arguments[index], true
		}
		switch argument {
		case "--url":
			url, ok := value()
			if !ok {
				return usage("--url needs a value")
			}
			server["url"] = url
		case "--header":
			pair, ok := value()
			if !ok {
				return usage("--header needs k=v")
			}
			key, headerValue, found := strings.Cut(pair, "=")
			if !found {
				return usage("--header needs k=v")
			}
			headers[key] = headerValue
		case "--env":
			pair, ok := value()
			if !ok {
				return usage("--env needs K=V")
			}
			key, envValue, found := strings.Cut(pair, "=")
			if !found {
				return usage("--env needs K=V")
			}
			env[key] = envValue
		case "--cwd":
			dir, ok := value()
			if !ok {
				return usage("--cwd needs a directory")
			}
			server["cwd"] = dir
		case "--timeout":
			text, ok := value()
			if !ok {
				return usage("--timeout needs milliseconds")
			}
			timeout, err := strconv.Atoi(text)
			if err != nil || timeout < 0 {
				return usage("--timeout needs a non-negative integer")
			}
			server["timeoutMs"] = timeout
		case "--disabled":
			server["enabled"] = false
		case "--":
			command := arguments[index+1:]
			if len(command) == 0 {
				return usage("-- needs a command")
			}
			server["command"] = command[0]
			if len(command) > 1 {
				args := make([]any, 0, len(command)-1)
				for _, arg := range command[1:] {
					args = append(args, arg)
				}
				server["args"] = args
			}
			index = len(arguments)
			continue
		default:
			return usage(fmt.Sprintf("unknown argument %q", argument))
		}
		index++
	}
	if len(env) > 0 {
		server["env"] = env
	}
	if len(headers) > 0 {
		server["headers"] = headers
	}
	// Validate through the exact parser the session uses, so anything orb mcp
	// accepts is something the session will start.
	configs, warnings, err := mcp.ParseSettingsWithWarnings(map[string]any{"mcpServers": map[string]any{name: server}})
	if err != nil {
		return reportCLIError(streams.Stderr, err)
	}
	if len(warnings) > 0 {
		return usage(warnings[0])
	}
	if len(configs) == 0 && mcpServerEnabled(server) {
		return usage("the configuration was rejected")
	}
	if _, exists := settings.GetMCPServers()[name]; exists {
		_, _ = fmt.Fprintf(streams.Stderr, "Replacing existing MCP server %q.\n", name)
	}
	settings.SetMCPServer(name, server)
	if errors := settings.DrainErrors(); len(errors) > 0 {
		return reportCLIError(streams.Stderr, errors[0])
	}
	_, _ = fmt.Fprintf(streams.Stdout, "Added %s\n", name)
	return 0
}

func removeMCPServer(settings *config.SettingsManager, name string, streams cliStreams) int {
	if _, exists := settings.GetMCPServers()[name]; !exists {
		_, _ = fmt.Fprintf(streams.Stderr, "Unknown MCP server %q.\n", name)
		return 1
	}
	settings.RemoveMCPServer(name)
	if errors := settings.DrainErrors(); len(errors) > 0 {
		return reportCLIError(streams.Stderr, errors[0])
	}
	if _, exists := settings.GetMCPServers()[name]; exists {
		_, _ = fmt.Fprintf(streams.Stderr, "%q is defined in the project settings; edit %s/settings.json to remove it there.\n", name, config.ConfigDirName)
		return 1
	}
	_, _ = fmt.Fprintf(streams.Stdout, "Removed %s\n", name)
	return 0
}
