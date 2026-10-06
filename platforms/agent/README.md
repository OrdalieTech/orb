# Team agent container

One Ordalie team agent per container. `orb chat buzz telegram --tools` is the agent: one process,
one memory, reachable on Buzz and Telegram with the same identity and tools. Next to it runs
`buzz-acp`, which holds the agent's Buzz identity and reaches the agent over ACP. The entrypoint
starts as root only to run the agent as `agent` and buzz-acp as `buzz`, so the agent's tools can
read neither buzz-acp's environment nor its signing key.

```sh
docker build --platform linux/amd64 --build-arg ORB_VERSION=<release> -t orb-agent platforms/agent
docker volume create agent-sales
docker run -d --name agent-sales --restart on-failure --env-file /secure/agent-sales.env \
  -v agent-sales:/agent orb-agent
```

`--restart on-failure` keeps an owner's `!shutdown` (a clean exit) final. Run `buzz` alone
(`docker run … orb-agent buzz`) for an agent that is only on Buzz, or `telegram --tools` for one
only on Telegram.

## Environment

Only the agent's own secrets, as environment:

| Variable | Front | |
|---|---|---|
| `BUZZ_PRIVATE_KEY` | Buzz | the agent's Nostr key |
| `BUZZ_AUTH_TAG` | Buzz | NIP-OA tag attesting the owner's authorization, from `buzz-owner.py` (below) |
| `BUZZ_ACP_DISPLAY_NAME` | Buzz | the agent's name |
| `BUZZ_RELAY_URL` | Buzz | the relay, such as `wss://buzz.example.com` |
| `BUZZ_ACP_RESPOND_TO`, `BUZZ_ACP_RESPOND_TO_ALLOWLIST` | Buzz | who the agent answers: `allowlist` and team pubkeys |
| `BUZZ_ACP_SUBSCRIBE`, `BUZZ_ACP_IDLE_TIMEOUT`, other `BUZZ_ACP_*` | Buzz | as for any buzz-acp agent |
| `TELEGRAM_BOT_TOKEN` | Telegram | the bot's token |
| `ORB_CHAT_ALLOWED_SENDERS` | Telegram | Telegram user ids allowed to talk to it |
| provider key, such as `OPENAI_API_KEY` or `OPENROUTER_API_KEY` | both | the model's |
| `ORB_BUZZ_ABOUT`, `ORB_BUZZ_AVATAR` | Buzz | the agent's description and picture URL, optional |

The entrypoint hands these to the agent on a pipe, not in its environment, and the agent's tools
(bash and the rest) see none of them: `orb chat` gives them only `PATH`, `HOME`
and a few locale variables (`ORB_TOOL_ENV` widens that) and hides its own environment. The agent
posts on Buzz with `buzz messages send` as Buzz's prompt tells it; the `buzz` its shell runs is Orb,
which has the agent run the real CLI with the Buzz key. The image sets `BUZZ_ACP_NO_MEMORY=true`: the agent's memory
is Orb's `memory` plugin, shared by both fronts, so Buzz's own is off.

## On Buzz

Buzz shows an agent by its profile and lists it in its agent directory only when the profile
carries its owner's tag and the owner has published a record of the agent. The agent signs its
profile: on every start it publishes `BUZZ_ACP_DISPLAY_NAME`, `ORB_BUZZ_ABOUT` and
`ORB_BUZZ_AVATAR` with `BUZZ_AUTH_TAG`, merged into the profile already on the relay, and retries
until the relay takes it. The owner signs the rest on their own machine, since the owner's key
never goes into the container:

```sh
python3 buzz-owner.py <agent pubkey> --relay wss://buzz.example.com --name Sales --allow <pubkey>,…
```

It asks for the owner's key without echoing it, prints the `BUZZ_AUTH_TAG=…` line for the env
file, and publishes the owner's record of the agent: its name and whom it answers, which should
match `BUZZ_ACP_RESPOND_TO` (`--allow` for an allowlist, `--anyone`, or neither for the owner
alone). Without `--relay` it only prints the tag. Run it again to change the record. The agent
still has to be a member of the relay and of its channels, as any Buzz identity.

## The volume

`/agent` holds everything the agent keeps: `config/` (Orb's agent dir: `settings.json`,
`AGENTS.md` for its persona, `skills/`), `state/` (sessions and memory) and `workspace/` (its
working directory). Turn the memory plugin on in `config/settings.json`:

```json
{ "defaultProvider": "openrouter", "defaultModel": "…", "plugins": { "memory": true } }
```

Model logins that need a refresh token (OAuth, such as `openai-codex`) go in
`/agent/secrets/auth.json`: the entrypoint makes it root's (0600) and hands the agent an open
descriptor on it, so the agent reads and refreshes it while its tools cannot open it. An
`auth.json` left in `config/` is moved there on start; if one ever sat in `config/` while the agent
ran, the store under `state/` may hold a copy, so start such an agent on a fresh volume.

`config/settings.json` and `config/models.json` are read on every start: edit them and restart to
change the model.

Measured on a pilot agent (0.17.2, Buzz and Telegram, Codex model): 12 to 27 MiB for the whole
container.

