---
name: orb
description: "Understand, drive and configure Orb, the pure-Go agent runtime behind the `orb` CLI. Use when the user mentions Orb or asks you to run Orb headlessly, script it, or change its settings, models, logins, plugins, MCP servers, skills, extensions, sessions, chat platforms, team agents or Bridge peers. Do not use merely because you are running inside Orb, and not for pi itself."
---

# Orb

Orb is an agent runtime: the `orb` binary and the Go module it is built from. The TUI, print mode, RPC, ACP, chat platforms and the Orb apps are peer drivers of one core. Orb keeps pi's file and wire formats and runs pi's extensions, skills, prompt templates and packages unchanged.

Inside an Orb session, your shell's `ORB_SESSION_ID`, `ORB_PROVIDER` and `ORB_MODEL` name the session and model you run in. `orb skill` prints this file for the installed version; reprint copies after an update.

## Learn the current CLI

The installed binary is the authority for syntax; this file states no flags. Start with:

```bash
orb --help
```

Then print the relevant command's help:

```bash
orb bridge --help
orb chat --help
orb plugins --help
orb mcp --help
orb auth --help
orb storage --help
orb login --help
orb logout --help
orb install --help
orb remove --help
orb update --help
orb list --help
orb config --help
```

These print and change nothing. Bare `orb` and `orb config` open a TUI, and bare `orb update` replaces the running binary. `orb chat <platform>`, `orb app` (which also starts Bridge) and the RPC and ACP modes run until stopped. Do not probe a nested command by omitting its arguments: `orb storage migrate`, `orb bridge start` and `orb bridge pair` act at once.

## Core and hosts

The core is the same on every target: the model layer, the agent loop and its tools, sessions, settings, extensions and Bridge. A host supplies its ports: files, processes, a document store, network and environment. Capabilities follow the ports, so a host without processes, such as a browser or Cloudflare worker, has no bash, process MCP servers or JavaScript extensions; those tools are absent rather than failing.

## State and configuration

- The CLI keeps sessions and global state in one SQLite store, `~/.orb/state/orb.db` (`ORB_STATE_HOME`). Sessions have IDs, not files, and belong to the directory they ran in; `orb storage` lists them and moves them in and out as pi JSONL. Pi-file mode keeps pi's files instead, in a root of its own.
- The agent directory is `~/.orb/agent` (`ORB_AGENT_DIR`). Its `settings.json`, `models.json`, `auth.json`, `trust.json` and `keybindings.json` keep pi's formats but live in the store, so editing those files changes nothing outside pi-file mode. Change them through Orb (`/settings`, `/login` and `/model` in the TUI, `orb login`, `orb plugins`) or export, edit and import them with `orb storage`.
- Providers take an API key from the environment or a login. Custom providers and model overrides go in `models.json`.
- A project's `.orb/` (settings, skills, prompts, extensions, `mcp.json`) loads only once the project is trusted. Its `settings.json` merges over the global settings one level deep.
- Bundled plugins are off by default. `plugins.<name>` in settings is `true`, `false` or the plugin's settings object; `/plugins` in the TUI and `orb plugins` from a shell list, toggle and set them.
- MCP servers live in `mcp.json`, in the agent directory or a trusted project; `orb mcp` edits and checks them.
- Skills and prompt templates come from the agent directory, trusted projects, installed pi packages (`orb install`) and the skill directories of Claude Code, Codex and other agents. Extensions are pi's TypeScript or JavaScript extensions, run on the user's Node or Bun; without one, extensions are off and everything else works.
- A session reads all of this when it starts. After changing it from a shell, run `/reload` in the session or start a new one.
- A team agent is one file, `agent.yaml`, which its container's `orb-agent` validates and renders into the agent's settings, `AGENTS.md`, `mcp.json` and `models.json` on every start: edit the agent file, never those, and restart. Its secrets come from the environment only.

## Modes

- `orb` is the interactive TUI.
- `orb -p` runs prompts and exits; `--mode json` streams the same run's events as JSON lines.
- `orb --mode rpc` speaks pi's RPC protocol on stdio.
- `orb --mode acp` serves the Agent Client Protocol on stdio, many sessions in one process.
- `orb chat <platform>...` runs one agent on chat platforms as one process with one memory. Give its conversations tools only when the agent runs isolated, one container per agent.

## Bridge

Every Orb has a peer identity and registers its conversations as instances. Two Orbs become peers through an invitation each owner approves, or over SSH. Grants are explicit and of two kinds that never merge. Controllers are people: they list, watch and drive another Orb's conversations, which keep running there with that Orb's tools and credentials, and full controller access also launches Orb in a folder on that machine (`host.launch`). Agents are Orbs acting on their own: an agent reaches another Orb only under its own grant, and with agent access on, its owner's other conversations with the owner's reach.

## Deployments

The same core runs as this CLI on desktops, servers and containers, embedded in Go programs, and in browser and Cloudflare workers. Each deployment runs its own models, tools and state and reaches the others as a Bridge peer; `orb bridge --help` shows how to connect them.

## Rules

- Read state from Orb's output, never from this file or memory: `orb plugins list` and `orb mcp list` show what is configured.
- Sign in or out, install or remove packages, update Orb, and pair, grant or start Bridge only when the user asks; Bridge changes decide who can reach this machine.
- Never edit `orb.db` by hand.
