# Team agent container

One Ordalie team agent per container. `orb chat buzz telegram --tools` is the agent: one process,
one memory, reachable on Buzz and Telegram with the same identity and tools. It starts `buzz-acp`,
which holds the agent's Buzz identity and reaches the agent over ACP.

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
| `BUZZ_AUTH_TAG` | Buzz | NIP-OA tag attesting the owner's authorization |
| `BUZZ_RELAY_URL` | Buzz | `wss://chat.ordalie.com` |
| `BUZZ_ACP_RESPOND_TO`, `BUZZ_ACP_RESPOND_TO_ALLOWLIST` | Buzz | who the agent answers: `allowlist` and team pubkeys |
| `BUZZ_ACP_SUBSCRIBE`, `BUZZ_ACP_IDLE_TIMEOUT`, other `BUZZ_ACP_*` | Buzz | as for any buzz-acp agent |
| `TELEGRAM_BOT_TOKEN` | Telegram | the bot's token |
| `ORB_CHAT_ALLOWED_SENDERS` | Telegram | Telegram user ids allowed to talk to it |
| provider key, such as `OPENAI_API_KEY` or `OPENROUTER_API_KEY` | both | the model's |

The image sets `BUZZ_ACP_MCP_COMMAND` (buzz-dev-mcp) and `BUZZ_ACP_NO_MEMORY=true`: the agent's
memory is Orb's `memory` plugin, shared by both fronts, so Buzz's own is off.

## The volume

`/agent` holds everything the agent keeps: `config/` (Orb's agent dir: `settings.json`,
`AGENTS.md` for its persona, `skills/`), `state/` (sessions and memory) and `workspace/` (its
working directory). Turn the memory plugin on in `config/settings.json`:

```json
{ "defaultProvider": "openrouter", "defaultModel": "…", "plugins": { "memory": true } }
```
