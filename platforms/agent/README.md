# Team agent container

One Ordalie team agent per container. `orb chat buzz telegram --tools` is the agent: one process,
one memory, reachable on Buzz and Telegram with the same identity and tools. The image's
entrypoint, `orb-agent`, sets the agent up from one file, `/agent/agent.yaml`, and runs it as the
`agent` user. Orb speaks every platform itself, Buzz included (its relay's Nostr protocol, in Go),
so the container runs one process, and the agent's tools can read none of its credentials.

Adding an agent is one file and its secrets:

```sh
docker build --platform linux/amd64 --build-arg ORB_VERSION=<release> -t orb-agent platforms/agent
docker volume create agent-sales
docker run --rm -v agent-sales:/agent -v ./sales.yaml:/sales.yaml:ro --entrypoint cp orb-agent /sales.yaml /agent/agent.yaml
docker run -d --name agent-sales --restart on-failure --memory 256m --env-file /secure/agent-sales.env \
  -v agent-sales:/agent orb-agent
```

`--restart on-failure` restarts an agent that crashed, not one stopped on purpose.

## Images

| Target | Adds | Size | Memory cap |
|---|---|---|---|
| default | Orb, bash, git, curl | about 190 MB | 256 MB |
| `--target browser` | [agent-browser](https://github.com/vercel-labs/agent-browser) and [Lightpanda](https://lightpanda.io) | 312 MB | 512 MB |
| `--target browser --build-arg CHROMIUM=1` | and headless Chromium | 855 MB | 1.5 GB, with `--shm-size=256m` |

The base is Debian 13, without apt, Perl or documentation; git's
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
    allow: [f59bcde6…, 97303c06…]  # team pubkeys, besides the owner (its auth tag's)
    channels: [4de8c05e-…]       # where every message, not only mentions, is for it
  telegram:
    allow: [7311893094]          # user ids allowed to talk to it
```

On every start `orb-agent` validates the file, refusing unknown keys and settings, and writes
`config/settings.json`, `config/AGENTS.md`, `config/mcp.json`, `config/models.json` and the
browser's settings from it, so edit the file, not those, and restart. The agent never trusts its
workspace's `.orb/`, which its tools can write, and every `orb` in the container reads those files,
so `docker exec … orb plugins list` shows what the agent runs. Each platform's section is
what its package declares (`orb chat --help` lists the platforms). Check a file before deploying it:

```sh
docker run --rm -v ./sales.yaml:/a.yaml:ro --entrypoint orb-agent orb-agent check /a.yaml
```

Without `agent.yaml` the image runs as before 0.18: the platforms are its arguments (`buzz
telegram --tools` by default) and the settings environment variables.

## Secrets

Only secrets go in the env file, and the agent's tools see none of them: `orb-agent` hands them to
Orb on a pipe, not in its environment, Orb gives its tools only `PATH`, `HOME` and a few locale
variables.

| Variable | Platform | |
|---|---|---|
| `BUZZ_PRIVATE_KEY` | Buzz | the agent's Nostr key |
| `BUZZ_AUTH_TAG` | Buzz | NIP-OA tag attesting the owner's authorization, from `orb chat setup buzz` (below) |
| `BUZZ_RELAY_URL` | Buzz | the relay, such as `wss://buzz.example.com` |
| `TELEGRAM_BOT_TOKEN` | Telegram | the bot's token |
| provider key, such as `OPENAI_API_KEY` or `OPENROUTER_API_KEY` | all | the model's |

Model logins that need a refresh token (OAuth, such as `openai-codex`) go in
`/agent/secrets/auth.json`: `orb-agent` makes it root's (0600) and hands the agent an open
descriptor on it, so the agent reads and refreshes it while its tools cannot open it.

On Buzz the agent hears the messages that mention it in its channels (added later included), its direct messages
and every message of its `channels`, each thread one conversation (a direct message is one), and
answers in the thread. Mentioned mid-thread, it first gets what was said there since it last spoke.
What arrived while it was down reaches it when it is back. Two tools, `buzz_query` and
`buzz_publish`, read any event and publish any as the agent (messages elsewhere, reactions, edits,
canvases), in its chats and in turns started from outside alike.

## On Buzz

Buzz shows an agent by its profile and lists it in its agent directory only when the profile
carries its owner's tag and the owner has published a record of the agent. `orb chat setup buzz`
does all of it, on any machine with `orb`, the agent's half there and its owner's wherever their
key is:

```sh
ORB_CHAT_DATA_DIR=./sales orb chat setup buzz --relay wss://buzz.example.com --name Sales --respond-to allowlist
# the owner, on their machine: signs, publishes their record of it, prints the --auth to give back
orb chat setup buzz --attest <agent pubkey> --relay wss://buzz.example.com --name Sales --owner-key -
ORB_CHAT_DATA_DIR=./sales orb chat setup buzz --auth '<tag>'
```

The agent's key is made there and never leaves; the owner's key never reaches the agent (an owner
on the same machine can approve at once with `--owner-key`, or by answering the hidden prompt).
The last step checks the attestation and publishes the agent's profile. `./sales/buzz.env` is then
the container's `--env-file` (add the model's key). On a closed relay the agent needs no membership
of its own, the relay admits it through its owner; add it to channels from Buzz (Add people and
agents), as a bot.

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

An agent schedules its own turns with the `wake` plugin, on unless the agent file turns it off
(`plugins: {wake: false}`): asked "post the sales recap here every evening at 20:00", it calls
`schedule_wake` with a cron schedule and a time zone, and each evening the prompt comes back in
that conversation, a Buzz thread or a Telegram chat, where the agent answers as it does any message.
`list_wakes` and `cancel_wake` manage them. Wakes live in the volume's state, so a restart keeps
them, and one missed while the agent was down arrives once when it is back.

The same plugin holds a permission ask the agent cannot put to anyone at the time: it asks in the
conversation, and the next message there approves (yes, oui, ok) or denies it; without an answer
in 30 minutes the call does not run.

## The volume

`/agent` holds everything the agent keeps: `agent.yaml`, `config/` (Orb's agent dir, rendered from
it, and `skills/`), `state/` (sessions and memory), `workspace/` (its working directory) and the
browser's state.
