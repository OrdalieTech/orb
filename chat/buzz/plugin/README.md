# Orb in the Buzz app

A Buzz app plugin (Buzz 1.0, `block/buzz-app`) that makes Orb an agent type: Buzz hosts the agent
and Orb runs it. It is the other way round from `orb chat buzz`, where Orb hosts the agent and
speaks the relay itself (the Go package above); pick that one for an agent that runs without the
app, such as a team agent in a container.

- One `orb --mode acp` per agent, one Orb session per Buzz thread or Solo chat, kept across
  restarts; Orb's last message of a turn is posted as the agent's reply there.
- Orb's session updates drive the live step, the Activity tab, session status and usage, and a
  message sent while a turn runs steers it.
- Model and thinking are Orb's own ACP options, in the agent's settings and per mention.
- The agent's `buzz` CLI (bundled with the app) acts as it: the plugin passes the relay and NIP-OA
  tag Buzz saved, and the key in `~/.config/buzz-orb/<agent pubkey>` (mode 600), since the app
  keeps agent keys out of plugins' reach.

Install: in Buzz, Settings → Plugins → Manage plugins → Add → Browse, pick this folder, then set an
agent's type to Orb. `orb` must be on the app's `PATH`. The plugin API is a preview, so a Buzz
update can break it.
