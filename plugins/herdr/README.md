# Herdr

Inside a Herdr pane, Orb's hidden native adapter reports its interactive lifecycle
as `custom:orb`, agent `orb`. Headless sessions never claim the pane. JavaScript
is not needed, and Herdr's managed Pi extension is omitted when discovered by
Orb, without modifying its file or affecting an installed Pi.

State reports include the command that restores the current persisted session:

- Native SQLite sessions: `orb --session <id>`.
- File-backed sessions: `orb --pi-files --session <absolute-file>`.

Session starts, switches and reloads refresh the report. Ephemeral sessions have
no resume command. A persisted-to-ephemeral or invalid-command switch releases
and immediately reclaims ownership to clear Herdr's old restore command. Other
session handoffs do not release; a real quit releases permanently.
Reports are background, bounded, latest-pending-state-wins and sequenced. A
resume argument containing an apostrophe or control character is omitted because
Herdr rejects such argv; lifecycle reporting still works.

Custom resume commands shipped in **Herdr 0.9.2** (0.9.3 includes that support).
The integration guide's reference to 0.10.0 is incorrect. Older Herdr CLIs fall
back to state-only reporting when they reject the trailing resume command; Orb
never impersonates Pi as a fallback.

Herdr restores the pane's working directory. The CLI explicitly preserves `--auto`,
tool restrictions, offline mode, resource-discovery disable flags, explicit resources
and the Bridge profile/instance. Absolute `--agent-dir` and `--state-home` prefixes
travel together; an explicit `ORB_BRIDGE_HOME` travels as `--bridge-home`. These
global options (and `--pi-files`) must precede runtime options or a subcommand.
This preserves the store even when the original roots came from inline environment
assignments rather than the restored shell. No credentials, prompt text, transient
project approval or unknown extension flags are copied into the resume command.
