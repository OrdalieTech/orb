# Team agent container

One Ordalie team agent per container. `orb chat buzz telegram --tools` is the agent: one process,
one memory, reachable on Buzz and Telegram with the same identity and tools. The image's
entrypoint, `orb-agent`, sets the agent up from one file, `/agent/agent.yaml`, and runs it: Orb as
the `agent` user, and as the `sidecar` user each process its platforms declare, such as Buzz's
`buzz-acp`, which holds the agent's Buzz identity and reaches the agent over ACP. The agent's
tools can read neither the sidecars' environment nor any of the agent's credentials.

Adding an agent is one file and its secrets:

```sh
docker build --platform linux/amd64 --build-arg ORB_VERSION=<release> -t orb-agent platforms/agent
docker volume create agent-sales
docker run --rm -v agent-sales:/agent -v ./sales.yaml:/sales.yaml:ro --entrypoint cp orb-agent /sales.yaml /agent/agent.yaml
docker run -d --name agent-sales --restart on-failure --memory 256m --env-file /secure/agent-sales.env \
  -v agent-sales:/agent orb-agent
```

`--restart on-failure` keeps an owner's `!shutdown` (a clean exit) final.

## Images

| Target | Adds | Size | Memory cap |
|---|---|---|---|
| default | Orb, Buzz's CLI and buzz-acp, bash, git, curl | 220 MB | 256 MB |
| `--target browser` | [agent-browser](https://github.com/vercel-labs/agent-browser) and [Lightpanda](https://lightpanda.io) | 312 MB | 512 MB |
| `--target browser --build-arg CHROMIUM=1` | and headless Chromium | 855 MB | 1.5 GB, with `--shm-size=256m` |

The base is Debian 13 (Buzz's binaries need glibc 2.39), without apt, Perl or documentation; git's
few Perl commands (`send-email`, `svn`) are missing. Memory caps leave room over what a pilot
measured: 12 to 27 MiB for an agent without a browser, about 130 MiB more with Lightpanda on a
page and about 750 MiB more with Chromium on a news site (amd64 under emulation, so high). Chromium
crashes tabs in Docker's default 64 MB of shared memory, hence `--shm-size`.

## The agent file

```yaml
name: Sales                      # how it shows on its platforms
about: Answers the sales team
avatar: https://…/sales.png
model: openai-codex/gpt-5.4      # <provider>/<model>
thinking: medium                 # off, minimal, low, medium, high or xhigh
persona: |                       # its AGENTS.md
  You are the sales team's agent…
skills: [skills/revops]          # skill directories, relative to /agent
mcp:                             # MCP servers, as in mcp.json
  notion: {command: notion-mcp, args: [--stdio]}
providers: {}                    # custom model providers, as in models.json
browser: lightpanda              # or chromium; needs the browser image
plugins:                         # bundled plugins (orb plugins list --all): true, false or
  websearch: true                # the plugin's settings, on unless enabled: false; memory
  subagents:                     # is on unless set false
    models: [openai-codex/gpt-6-luna]
platforms:
  buzz:
    respond_to: allowlist        # who it answers: owner-only, allowlist or anyone
    allow: [f59bcde6…, 97303c06…]  # team pubkeys
    subscribe: mentions          # also channels, kinds, idle_timeout, turn_timeout,
                                 # heartbeat_interval, heartbeat_prompt
  telegram:
    allow: [7311893094]          # user ids allowed to talk to it
```

On every start `orb-agent` validates the file, refusing unknown keys and settings, and writes
`config/settings.json`, `config/AGENTS.md`, `config/mcp.json`, `config/models.json` and the
browser's settings from it, so edit the file, not those, and restart. The agent never trusts its
workspace's `.orb/`, which its tools can write, and every `orb` in the container reads those files,
so `docker exec … orb plugins list` shows what the agent runs. Each platform's section is
what its package declares (`orb chat --help` lists the platforms); buzz-acp settings it does not
take stay available as `BUZZ_ACP_*` environment. Check a file before deploying it:

```sh
docker run --rm -v ./sales.yaml:/a.yaml:ro --entrypoint orb-agent orb-agent check /a.yaml
```

Without `agent.yaml` the image runs as before 0.18: the platforms are its arguments (`buzz
telegram --tools` by default) and the settings environment variables.

## Secrets

Only secrets go in the env file, and the agent's tools see none of them: `orb-agent` hands them to
Orb on a pipe, not in its environment, Orb gives its tools only `PATH`, `HOME` and a few locale
variables, and each sidecar gets only the variables its platform declares.

| Variable | Platform | |
|---|---|---|
| `BUZZ_PRIVATE_KEY` | Buzz | the agent's Nostr key |
| `BUZZ_AUTH_TAG` | Buzz | NIP-OA tag attesting the owner's authorization, from `buzz-owner.py` (below) |
| `BUZZ_RELAY_URL` | Buzz | the relay, such as `wss://buzz.example.com` |
| `TELEGRAM_BOT_TOKEN` | Telegram | the bot's token |
| provider key, such as `OPENAI_API_KEY` or `OPENROUTER_API_KEY` | all | the model's |

Model logins that need a refresh token (OAuth, such as `openai-codex`) go in
`/agent/secrets/auth.json`: `orb-agent` makes it root's (0600) and hands the agent an open
descriptor on it, so the agent reads and refreshes it while its tools cannot open it. An
`auth.json` left in `config/` is moved there on start; if one ever sat in `config/` while the agent
ran, the store under `state/` may hold a copy, so start such an agent on a fresh volume.

The agent posts on Buzz with `buzz messages send` as Buzz's prompt tells it; the `buzz` its shell
runs is Orb, which has the agent run the real CLI with the Buzz key.

## On Buzz

Buzz shows an agent by its profile and lists it in its agent directory only when the profile
carries its owner's tag and the owner has published a record of the agent. The agent signs its
profile: on every start it publishes its `name`, `about` and `avatar` with `BUZZ_AUTH_TAG`, merged
into the profile already on the relay, and retries until the relay takes it. The owner signs the
rest on their own machine, since the owner's key never goes into the container:

```sh
python3 buzz-owner.py <agent pubkey> --relay wss://buzz.example.com --name Sales --allow <pubkey>,…
```

It asks for the owner's key without echoing it, prints the `BUZZ_AUTH_TAG=…` line for the env
file, and publishes the owner's record of the agent: its name and whom it answers, which should
match the file's `respond_to` (`--allow` for an allowlist, `--anyone`, or neither for the owner
alone). Without `--relay` it only prints the tag. Run it again to change the record. The agent
still has to be a member of the relay and of its channels, as any Buzz identity.

## Browser

With `browser:` set, the agent drives a headless browser from its shell with `agent-browser`, as
its skill (added to the agent's skills) explains, like any other tool: it runs as the agent's
user with the tools' environment, so no secret is within its reach. Lightpanda, the default, is a
small headless engine without rendering: no screenshots or PDFs, and heavy single-page apps may
break. `chromium` renders like a desktop browser, at several times the memory. Logins persist in
the volume, Lightpanda's cookies and storage under `.agent-browser/sessions/` and Chromium's
profile under `browser/`. To give an agent a login, export it from a Chrome where someone signed
in (`agent-browser --auto-connect state save auth.json` on their machine), copy the file into the
workspace and have the agent load it once, before it opens a page (`agent-browser state load
auth.json`). Chromium
runs without its own sandbox, which needs user namespaces containers deny: the container is the
sandbox.

## Scheduled turns

Orb has no scheduler: a timer on the host starts a turn in the running agent through its ACP
socket, in a session of its own, with the agent's memory, tools and identity. This script runs one
turn and prints the agent's reply (bash and jq on the host):

```bash
#!/usr/bin/env bash
# agent-turn <container> <prompt>
set -euo pipefail
dir=$(mktemp -d)
trap 'rm -rf "$dir"' EXIT
mkfifo "$dir/in" "$dir/out"
docker exec -i -u agent "$1" orb chat connect /run/orb/acp.sock <"$dir/in" >"$dir/out" &
exec 3>"$dir/in" 4<"$dir/out"
call() { # call <id> <method> <params>: sends a request and leaves its response in $reply
  printf '{"jsonrpc":"2.0","id":%s,"method":"%s","params":%s}\n' "$1" "$2" "$3" >&3
  while IFS= read -r reply <&4; do
    [[ $(jq -r '.id // empty' <<<"$reply") == "$1" ]] && return
    jq -j 'select(.params.update.sessionUpdate == "agent_message_chunk") | .params.update.content.text // empty' <<<"$reply"
  done
}
call 1 initialize '{"protocolVersion":1}'
call 2 session/new '{"cwd":"/agent/workspace","mcpServers":[]}'
call 3 session/prompt "$(jq -nc --arg s "$(jq -r .result.sessionId <<<"$reply")" --arg t "$2" '{sessionId: $s, prompt: [{type: "text", text: $t}]}')"
echo
exec 3>&-
wait
```

A prompt that asks the agent to post its result on Buzz (`… and post it in channel <id>`) makes it
post as itself with `buzz messages send`. For Telegram, the timer sends the printed reply with
the bot's token, which the host has in the env file:

```sh
reply=$(agent-turn agent-sales "Write the daily sales recap.")
curl -s https://api.telegram.org/bot$TELEGRAM_BOT_TOKEN/sendMessage -d chat_id=<chat> --data-urlencode text="$reply"
```

A systemd timer runs it on schedule, missed runs included (`Persistent=true`):

```ini
# /etc/systemd/system/sales-recap.timer
[Timer]
OnCalendar=*-*-* 20:00 Europe/Paris
Persistent=true
[Install]
WantedBy=timers.target
# /etc/systemd/system/sales-recap.service
[Service]
Type=oneshot
EnvironmentFile=/secure/agent-sales.env
ExecStart=/usr/local/bin/sales-recap
```

Not `docker exec … orb -p`: that starts a second Orb in the container whose tools inherit every
secret in its environment, cannot use the `buzz` shim and cannot read an OAuth login.

## The volume

`/agent` holds everything the agent keeps: `agent.yaml`, `config/` (Orb's agent dir, rendered from
it, and `skills/`), `state/` (sessions and memory), `workspace/` (its working directory) and the
browser's state.
