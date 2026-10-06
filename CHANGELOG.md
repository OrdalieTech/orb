# Changelog

Orb's own release history (independent 0.x semver; upstream parity target recorded per release),
shown by `/changelog`.

## [Unreleased]

- A Bridge instance answers what `@` completes to (`complete`: its skills, then the files in its
  folder, as the TUI offers them; a folder listing where fd cannot run) and describes its
  provider's plan limits (`usage`: Claude from each turn, Codex and OpenCode Go read behind it).
  Images travel by reference: events and snapshots name each one, and a client fetches it fitted
  to the size it shows (`image`), so a screenshot no longer outgrows a frame or rides every reload.
- Bridge sends less and sooner: a describe leaves out the models and commands a client already
  holds (`catalog`), a message update no longer repeats its message, a replay skips the updates a
  later event replaces, and long tool output keeps its ends. A conversation past 8 MiB or with a
  message larger than a page opens at its end instead of not at all. The Android app reads its
  machines' Orbs only while on screen and wakes a conversation as soon as it is shown.
- The Android app swipes between Home and its tabs, keeps the conversations seen last streaming
  off screen (as many as its memory allows), completes `@` like the TUI, shows the plan window
  nearest its limit beside the context (all windows and their resets a tap away), shows the images
  a message or tool carried (decoded only on screen, never stored), lets you select text, and
  follows the screen's curve with its prompt box and sheets. Conversations stream without jumps
  and follow only a reader at the bottom, with room under the last message; quotes, nested ones included, read as one block and code
  wraps. A message that invoked a skill reads as typed, and an empty tab no longer comes back
  stuck after a restart.

## [0.19.0] - 2026-10-06

Buzz turns run tools again; chat platforms compose from one catalog, and layers are enforced.

- Buzz turns run tools again in the team agent image: 0.18.0 started buzz-acp in its own home,
  which buzz-acp makes every session's working directory and the agent cannot enter, so each
  tool failed with `spawn /bin/bash EACCES`. Sidecars run in the agent's workspace, an ACP
  session's working directory is the one its client gives (a stored session's included), and one
  the agent cannot enter is refused with its reason when the session opens.
- Chat platforms take their configuration as options and register nothing; one catalog,
  `chat/platforms`, maps each platform's environment and agent-file section onto them, for both
  `orb chat` and `orb-agent`. Layers are enforced by one map in `internal/layering`.

## [0.18.0] - 2026-10-06

Team agents are set up from one file, in a smaller image with optional browsers.

- Team agents are set up from one file: the image's new entrypoint, `orb-agent`, validates
  `/agent/agent.yaml` (identity, model, persona, skills, MCP servers, browser, each platform's
  settings and allowlist) and renders it into Orb's files and each platform's settings, with
  secrets still in the env file; `orb-agent check` validates a file before deploying. Platforms
  declare their part (`Configure`, `Sidecar`), so the entrypoint names none. An image without a
  file runs as before.
- The team agent image shrinks to 220 MB (Debian without apt, Perl or documentation; git and curl
  kept), and gains `browser` variants: agent-browser with Lightpanda, and headless Chromium with
  `CHROMIUM=1`, chosen per agent with `browser:`, logins kept in the volume.
- Scheduled turns run from a host timer through a running agent's ACP socket; the image's README
  shows how.

## [0.17.6] - 2026-10-06

Chat platforms reach Orb only through declared interfaces.

- Chat platforms reach Orb only through declared interfaces: a front gets the agent as one
  `chat.Agent` value, webhook platforms are served by `orb chat` with its own settings, help text
  comes from each platform's declared variables, and Buzz hands its socket to the agent's tools
  with `toolenv.Export` instead of editing `ORB_TOOL_ENV`, so its shim works whichever starts
  first. No behaviour change.
- Over-wide TUI lines are clipped safely instead of crashing the session, including during redraws and terminal resizes.
- Harden UI recovery: faulty tool renderers fall back to built-in output, invalid or stale
  completions leave the editor intact, negative list sizes and padding are clamped, and debug-log
  failures are shown instead of panicking. Long unbroken text no longer causes quadratic
  tokenization allocations.

## [0.17.5] - 2026-10-06

Buzz is one self-contained chat platform package among the others.

- `orb chat` platforms register themselves from their own packages, and all of Orb's Buzz code
  lives in `chat/buzz`; a build without it keeps every other platform. No behaviour change.

## [0.17.4] - 2026-10-06

A team agent shows on Buzz under its name and in its agent directory.

- A team agent shows on Buzz under its name and in Buzz's agent directory: `orb chat buzz`
  publishes its profile at start (`BUZZ_ACP_DISPLAY_NAME`, `ORB_BUZZ_ABOUT`, `ORB_BUZZ_AVATAR`)
  signed with its owner's tag, and `platforms/agent/buzz-owner.py`, which replaces
  `sign-auth-tag.py`, also publishes the owner's record of the agent from the owner's machine.

## [0.17.3] - 2026-10-06

An agent's live conversations share its memory as it changes.

- An agent's live conversations share its memory as it changes: what one saves, replaces or
  forgets reaches the others at their next turn, on any front, while each keeps the prompt it
  started with so provider caches hold. Before, a conversation saw only the memory it began with.

## [0.17.2] - 2026-10-06

A team agent with an OAuth login answers again from its first start.

- A team agent started on a new volume reads its OAuth login again: 0.17.1 wrapped the
  `ORB_AUTH_FD` descriptor twice while migrating its first start, and the collected copy closed
  it, so every turn failed with "read ORB_AUTH_FD: illegal seek".

## [0.17.1] - 2026-10-06

Fixes from the team agent pilot: credentials out of every tool's reach, the model set by the
mounted settings, and a quicker, lighter Buzz start.

- A team agent's credentials stay out of every tool's reach, its in-process read tool included:
  the image's entrypoint hands them over on descriptors (`ORB_SECRETS_FD`, and `ORB_AUTH_FD` for
  OAuth logins in `/agent/secrets/auth.json`), so neither its environment nor any file it can open
  holds one. In 0.17.0 the read tool could open `/proc/self/environ`.
- A team agent reads `settings.json` and `models.json` from its agent dir on every start, so
  editing them and restarting changes its model.
- The team agent image starts buzz-acp once the agent's socket is up, instead of failing its first
  start, and relays it with `nc` instead of a 45 MB Orb process.

## [0.17.0] - 2026-10-06

Orb runs Ordalie's team agents: `orb --mode acp` speaks the Agent Client Protocol natively, and
`orb chat buzz telegram --tools` is one agent with one memory on Buzz and Telegram, in one small
container whose tools never reach its credentials.

- A team agent's tools never see its credentials: `ORB_TOOL_ENV` lists the only variables bash,
  MCP servers, extension hosts and external agents inherit, `orb chat` sets one by default, and an
  Orb with one hides its own environment from them. With `orb chat buzz` the shell's `buzz` is Orb,
  which has the agent run the real CLI with the Buzz key; the image no longer runs buzz-dev-mcp,
  and runs buzz-acp as a second user whose key and keyfile the agent's tools cannot read.
- `orb --mode acp` speaks the Agent Client Protocol natively: Zed, Buzz Desktop or Buzz's
  `buzz-acp` drive any number of Orb sessions in one process, with the client's MCP servers and
  harness prompt, model and reasoning selectors, `session/load` and usage reports.
- `orb chat` runs several platforms as one agent with one memory: `orb chat buzz telegram --tools`
  starts `buzz-acp` for Buzz and reaches it over ACP, answers on Telegram, and with `--tools` gives
  chat conversations the agent's full sessions (tools, plugins, skills). `platforms/agent` builds
  one container per team agent.
- MCP servers started as processes connect again when built on SDKs that predate the 2026-07-28
  protocol, such as Rust's rmcp; Orb opens them with `initialize`.

## [0.16.0] - 2026-10-05

Turns cost the same at the eightieth message as at the first, natively and on the Worker host,
and the Worker bundle is half its size: native turns take less than half the CPU of pi's CLI
and a third of its memory. A message can invoke several skills, and memtree is marked experimental.

- Turns no longer slow down as a conversation grows: the session keeps the model context decoded
  and extends it as entries are appended, instead of re-reading the whole journal on every append
  and request, and hot paths encode and decode JSON without reflection. A native turn stays at
  3-4 ms of CPU over 80 turns where it grew from 14 to 82 ms; on the Worker host a warm turn
  retires 24M instructions instead of 1.27G and allocates 266 KB instead of 6.6 MB.
- The Worker runs every Durable Object of an isolate in one Go runtime, compiles its regular
  expressions on first use, and keeps large functions off the stacks that wait on JavaScript, so a
  fresh isolate's first turns spend less time in V8's compiler.
- Provider requests go through one small stdlib HTTP client instead of the OpenAI and Anthropic
  Go SDKs, which only sent bodies Orb had built, and Bedrock no longer uses the AWS SDK's runtime
  client: its SigV4 signing and event streams are Orb's own, and only native hosts read the AWS
  shared config. Anthropic workload-identity federation exchanges its token as upstream's
  TypeScript SDK does, with its errors, so a token response without `expires_in` now fails. The
  Worker bundle shrinks from 55.4 MB to 28.1 MB (7.0 MB gzip) and the CLI from 57.6 MB to 46.4 MB.
  Go embedders: `AnthropicMessagesOptions.Client` is removed.
- User messages have a padded line above and below their text, and an open group of tool calls
  (Ran, Explored, Worked) drops its down chevron and lines up with the calls under it.
- A message can invoke several skills: `/skill:a … /skill:b` sends both, each skill's block
  after the other, instead of being refused. The transcript chips each one in place, with a
  footer line per skill, and the session list, queue and HTML export show them all.
- `memtree` (experimental, off by default) now follows OptChat when enabled: each prompt starts
  from the view, the system prompt explains it, turns wait for every earlier message to be
  summarized (Escape ends the wait), and a long history alone no longer compacts. Summaries live
  in the session as hidden entries, so the plugin needs no files and runs in SDK embeddings and on
  the Worker and Celld host (`plugins.memtree` in its settings). Go embedders: `memtree.Extension`
  takes `memtree.Options`.

## [0.15.0] - 2026-10-04

The Android app becomes a daily driver: every conversation runs through Bridge, the phone's own
included, tabs survive restarts, a notification says when a turn ends, and Markdown tables and
Mermaid diagrams render. Bridge followers long-poll, open long conversations at their end, and get
`/compact` and `!commands`; `orb mermaid` draws a diagram for any client.

- `orb mermaid < diagram.mmd` draws a Mermaid diagram as the TUI shows it, for clients with no
  renderer of their own. A Bridge snapshot can start at a conversation's last `tail` messages and
  says where it starts, so a follower opens a long conversation at its end and pages back.
- The Android app renders Markdown tables and Mermaid diagrams (through `orb mermaid`, with the
  box and shape glyphs the diagrams use drawn into its font), opens long conversations at their
  last 80 messages with earlier ones on demand, and starts a conversation's Orb without waiting
  for every paired machine to answer first.
- Bridge followers long-poll: `events.subscribe` with `wait` answers as soon as there are events
  or the instance's state moves, instead of every poll answering at once, so remote conversations
  stream smoothly. Instances also report their reasoning level, usage and slash commands, and
  take `session.compact` and `shell`. The owner reaches its own machine with the calls it makes on
  peers, and opening a thread another Orb on Bridge has open joins that Orb.
- The Android app drives every conversation through Bridge, the phone's own included: each phone
  conversation is an Orb its Bridge launches, so several run at once, and server conversations
  get `/compact`, `!commands` and every slash command. Sign-ins, the terminal and the thread list
  work the same on the phone as on a server; a sign-in ends that machine's idle Orbs so they
  reopen with the new account. The app is about a sixth smaller, more compact, with calmer corners,
  and what you write sits at right on a soft ground.
- Pairing a machine (`orb bridge pair`, `join`, `trust`, `connect-ssh`) turns its `bridge` plugin
  on unless its owner set it, so the Orbs opened there in a terminal can be followed and driven
  from the paired devices too.

## [0.14.1] - 2026-10-04

The Android app is simpler: one bar with a tab per open session, monospace type with real weights,
and a terminal that follows the open conversation, onto the machine it runs on. `orb bridge shell`
opens that terminal from a computer, and a thread already open elsewhere on a machine says so.

- `orb bridge shell <peer> [folder]` opens a terminal on a paired machine that lets this Orb
  start Orb there (`host.terminal.*`): its owner's login shell, in that folder. The Android app's
  terminal follows the open conversation, on the phone's Linux or on the device it runs on.
- Opening a thread from a paired device while another Orb on that machine has it open (a terminal
  one, say) now says so, instead of failing as unavailable.
- The Android app is simpler and easier to move around: one bar holds the wordmark (Home), a tab
  per open session once several are open on the phone or paired devices, and the menu; a
  conversation shows its context use and cost under it. Text is set in the variable Ubuntu Sans
  Mono, slightly tighter and with real weights, and rules have more contrast. Keys the app kept
  in its own preferences move into Orb's store; the custom endpoint form and the text-size pinch
  are gone.
- New `memtree` plugin, off by default: OptChat's memory for a session. A cheap model keeps a
  zoomable tree of one-line summaries over the whole history, and every prompt starts a new
  context from a fixed-size view of it, so long sessions neither rot nor lose detail; any message
  stays a few `zoom` calls away. In `compaction` mode the view replaces Orb's compaction summary
  instead.

## [0.14.0] - 2026-10-04

The CLI's first request declares every tool again, context estimates and compaction count the
system prompt, and `--mode json`/`rpc` exit cleanly on SIGTERM and SIGHUP.
Claude Code sessions open with their reasoning and images, Codex CLI threads open as Orb
conversations, every account can be renamed, and dragging selects any text, dialogs and lists
included. Inside Herdr, Orb panes report their state and accept `herdr agent prompt`. Go embedders:
`SetSystemPrompt`, harness session v4, image generation and the process RPC client are removed.

Orb now tracks Pi **v1.0.0** (codemode and what only it uses stay out for now).

- Context edits: a retried or recovered response stays in the session's history but leaves the
  model's context, recorded as a `context_edit` entry (in the native SQLite sessions too), and
  compaction summarizes and measures what the model sees, ignoring usage reported before an edit; each request is now built from the session
  itself, so a bootstrap prompt the session never recorded no longer reaches the provider twice.
- Sessions are written to disk at the first message you send, not the first reply, so a prompt
  survives quitting before the model answers.
- New models: Claude Opus 5.5 and Sonnet 5.5, GPT-6 Sol, GPT-6 Luna and GPT-6.1 Sol (OpenAI,
  Azure, Codex, Copilot), and Meta's Muse Spark with `META_API_KEY` or `orb login meta`. OpenAI
  prices follow the current list, and xAI models price long contexts.
- Sign in with ChatGPT on the OpenAI provider (`orb login openai`); the Codex provider is now
  "OpenAI Codex (legacy)".
- OpenAI-compatible endpoints receive strict tool schemas only when the model says it supports
  them; built-in models keep them.
- RPC `prompt`, `steer` and `follow_up` responses say what happened to the input (`started`,
  `queued` or `handled`); the Go RPC client returns it and takes `streamingBehavior`, and the
  Android app notes when a message is queued or steering.
- A command that exits non-zero gives the model the same error, and extensions now also receive
  its full output, exit code and run time.
- Theme files accept `#rgb`, `oklch()` and `okhsl()` colors and an `appearance`; HTML export uses
  Pi 1.0's palettes and its show/hide toggle for hidden messages.
- A prompt template with broken frontmatter is reported instead of skipped silently.
- Split-turn compaction asks for its summary in a way Claude Fable 5.1 accepts.
- `defaultTools` accepts `+name` and `-name` entries that adjust the inherited selection (project on
  top of user), and `/reload` turns on tools newly added to it.
- Images from tools, `read` and prompt attachments are resized to the model's image limits; images a
  tool produced itself are normalized too, so one oversized screenshot no longer breaks a session.
- `samplingParams` (and image `inputLimits`) can be set per model in `models.json`, in
  `modelOverrides` and by extension providers; OpenAI-compatible requests send them last.
- Hitting the ChatGPT subscription's usage limit stops retrying and links to the usage page.
- Anthropic: copy-code login (the code shows on Anthropic's page, for a browser on another machine),
  workload identity federation from `ANTHROPIC_FEDERATION_RULE_ID`, `ANTHROPIC_ORGANIZATION_ID` and
  `ANTHROPIC_IDENTITY_TOKEN_FILE`, and tools whose schemas use keywords Anthropic's strict mode
  rejects (`minimum`, `maxItems`, …) are sent non-strict instead of failing the request.
- A paired phone signs a headless Orb in to providers over Bridge (`host.login.*`, device →
  providers in the Android app): the sign-in link, device code and questions come to the phone.
- Browser sign-in (Anthropic, ChatGPT, Codex, OpenRouter) shares one callback server: a provider
  error ends sign-in with its description, a busy callback port falls back to pasting the redirect
  URL, and the browser page is Orb's.
- `--provider` without `--model`, and an invalid `--mode`, are now errors instead of being ignored.
- `quietStartup: "header"` keeps the startup logo while hiding startup details.
- Extensions: `turn_end` and the new `agent_before_settle` can persist entries (custom, custom
  message, context edit, compaction, including one that keeps nothing before it) and ask for one
  more model request; runs started from `agent_settled` wait until every handler has finished.
  `context` handlers no longer see system messages, so pruning cannot drop the prompt or tools, and
  the new `context_with_system` sees the full transcript.
- Bundled plugins and built-in tools are named `builtin:<name>`; `-e builtin:<name>` loads one even
  with `--no-extensions`, and the MCP plugin steps aside (with a warning) for an extension that
  registers the same tool, command or flag. Extension commands without a name fail to load.
- Extension tools declare an `exposure` (`direct`, `model-only`, `deferred`, `hidden`), a
  `namespace`, `annotations`, an `outputSchema` and `defaultActive`; a `prepareLoadout` hook can
  rewrite declared descriptions and hide declarations from requests. Tools run other tools with
  `ctx.executeTool()`: nested calls go through validation and the `tool_call`/`tool_result` hooks,
  carry `parentToolCallId` in their events, and are recorded with their usage on the caller's result
  (`nestedCalls`).
- Extensions can watch raw provider stream events (`provider_stream_event`), style text with
  `theme.style()`, and read `theme.colors` and `theme.appearance`; pi-tui's color helpers
  (`rgbColor`, `mixColors`, `styleText`, …) work, and the SDK declares pi 1.0's full export surface
  including `@earendil-works/pi-ai/models`.
- MCP servers move to `mcp.json` (`~/.pi/agent/mcp.json`, and `.pi/mcp.json` in a trusted project);
  `mcpServers` in `settings.json` is no longer read. Servers connect in the background, a short
  `mcp_servers` section tells the model what they offer (`description` per server), and their tools
  are deferred by default: the new `tool_search` loads the ones a task needs, and loaded tools come
  back on resume and `/reload`. `exposure`/`toolExposure` make tools direct or hidden instead. Tools
  are named `mcp__<server>__<tool>` (a hash only on collisions), carry MCP annotations and the full
  result as structured content, and `isError` results reach the model as errors. `orb mcp add`
  takes `--exposure`, `--description`, `--bearer-token-env-var`, OAuth client options and `-l`, and
  `orb mcp list` connects each server and reports its tools (`--json`); `get`, `enable` and
  `disable` are gone.
- MCP servers sign in with OAuth: `/mcp login` or `orb mcp login` runs the browser flow (discovery,
  client registration with `oauth.clientName`, PKCE, the authorization server's `iss` checked, a
  pasted redirect URL when the browser is elsewhere), tokens refresh on their own, a server asking
  for more scope keeps what it had, and `oauth.authServerMetadataUrl` overrides discovery.
  Credentials are stored per server name and URL in `mcp-auth.json`, shared with pi. A server can
  instead use a provider's `orb login` token with `"auth": {"provider": "…"}`.
- Go embedders: `engine.Agent.SetSystemPrompt` is gone, as in Pi 1.0: an agent's prompt is its
  transcript's (`State().SystemPrompt`), changed by adding a system message, and the session's
  `State()` reports the prompt it will send next. `extensions.API` includes `OnWithUnsubscribe`, and
  `harness.SessionV4Storage` includes `ClearName`. `agent.FormatSkillsForPrompt` takes the name of
  the tool that reads skill files.
- A project with only `.pi/mcp.json` asks for trust before its servers load.
- `--mode json` and `--mode rpc` exit promptly on SIGTERM and SIGHUP even when nothing reads their output, instead of hanging until a forced kill; normal completion still writes every frame.
- The CLI's first model request declares every active tool; it used to carry only bash and extension
  tools (no tools at all on OpenRouter), so read, edit and write were unavailable until turn two.
- With bash but no read tool, the skills section tells the model to load skill files with bash, as
  pi 1.0 does.
- OpenRouter turns record the cost OpenRouter reports as their total instead of the catalog
  estimate; per-component costs stay estimates. The OpenRouter catalog is refreshed (400 models,
  including DeepSeek V4.1 Flash).
- Inside Herdr, an interactive Orb lets Herdr's pi integration own its lifecycle and shows itself as
  Orb, so `herdr agent prompt` works on Orb panes; without that integration Orb reports its own
  status as before, and headless runs never claim the pane.
- Inside Herdr, a Claude conversation (or Orb asking Claude for its models at startup) no longer
  lets Claude Code's own Herdr hook claim Orb's pane, which froze the pane's status at idle.
- The working indicator comes back when automatic compaction continues a run.
- The system prompt's documentation section appears only when the README, docs and examples it
  points to exist, so standalone installs and dev builds no longer send the model to missing paths.
- The footer's hover label disappears on its own after two seconds, since terminals never report the
  pointer leaving the pane.
- Compaction no longer sends an extra summary request for an empty conversation when the only
  history before the first user message is the system prompt; it summarizes as pi 1.0 does.
- Claude Code sessions opened in Orb keep their reasoning, images, usage and timestamps, and show
  compactions, background tasks and commands as Orb does live; catching up turns added in Claude Code
  keeps the model chosen in Orb.
- Sign-in shows its link once, with the code field in the same dialog, and a drag over a link or any
  wrapped text in a dialog copies exactly that text, without padding, borders or wrap breaks. Adding a
  Claude account asks for the code it always needs and ignores an empty submit.
- Context estimates count the system prompt and declared tools, as pi does, so the footer and
  auto-compaction see the real context size instead of leaving the prompt out.
- A model that can't reason starts with thinking off instead of the default medium, as in pi.
- An explicitly empty tool selection stays empty instead of falling back to the default tools.
- Go embedders: the process RPC client (`agent/modes.NewRPCClient` and its types) is removed;
  drive `orb --mode rpc` over stdio, or use hosted RPC.
- Borders read more clearly: the input field's border with thinking off and dialog rules use a darker
  gray in the light theme and a lighter one in the dark theme.
- Go embedders: Bridge, its operation ledger and model-catalog persistence take `host.Document`
  (Read/Update), now defined once in `internal/document`; `bridge.Store`, `models.StoreDocument`,
  `platforms/worker/peer.DocumentStore`, `nativebridge.OpenStoreWithDocument`,
  `(*nativebridge.Store).Remove`, `bridge.Snapshot` and `(*bridge.Stream).Snapshot` are removed,
  `nativebridge.OpenStore` takes an optional document, and `agent/bridge.Descriptor` names the
  `instances.describe` shape.
- Subagents started with `context: "fork"` continue from the parent's real conversation branch
  instead of a text summary of its last 20 messages, and children no longer create a temporary
  settings directory.
- Long pasted inputs submit promptly: typing no longer copies the whole line for every character.
- Esc puts an unanswered prompt and queued messages back in the editor at once, without waiting for
  the abort to finish or overwriting what you type meanwhile.
- Every account in `/login` can be renamed, including ambient logins and command-line API keys; names
  persist, and an empty name restores the default label.
- Dragging over any text selects it, in lists and dialogs too, and copies it without padding, borders
  or wrap breaks; a click still picks or copies, now on release.
- Codex CLI threads open as Orb conversations with `orb --session <thread-id>` (codex-sessions plugin,
  on by default): text, images, reasoning, tools and compaction come along, and turns added in Codex
  since are taken in when reopened.
- Go embedders: harness session v4 is removed, as in Pi 1.0 (`harness.SessionV4*`, its JSONL storage,
  repository, transactions, fork and migration), along with `harness.LoadSkills`,
  `LoadPromptTemplates`, `FormatSkillInvocation`, `FormatPromptTemplateInvocation`, `Skill`,
  `PromptTemplate`, `ResourceFileSystem`, `InMemorySessionRepo` and `ContextMessages`; also
  `extensions.{Bash,Read,Edit,Write,Grep,Find,Ls}ToolCall`/`…ToolResult`,
  `config.ReadStoredCredential`, `agent.LoadSkillsFromDir`, `tools.ExpandPath` and
  `session.WithParentSession`.
- Go embedders: image generation is removed (`ai.CreateImagesModels`, `ai.CreateImagesProvider`,
  `ai.ImagesOptions`/`ImagesRequest`/`ImagesFunction`/`ImagesContext`, `api.GenerateImages`,
  `api.GenerateOpenRouterImages`, `models.BuiltinImages`, `providers.BuiltinImages*`,
  `providers.OpenRouterImages`), as are `api.StreamAnthropicMessages`,
  `api.StreamSimpleAnthropicMessagesWithClient`, `api.StreamOpenAICompletions`,
  `api.StreamOpenAIResponses` (use the `…WithOptions` forms), `api.Registry.Has`, the Codex
  WebSocket debug-stats API and the per-provider constructors such as `providers.Anthropic()`
  (use `providers.Get`).
- Fixes from Pi 1.0: image-only messages no longer carry an empty text part, Responses streams that
  end with an unfinished tool call fail instead of running it, replayed grammar tool calls drop
  mismatched item ids, Mistral GLM models keep one thinking block and receive the requested effort,
  Z.AI overflow messages trigger compaction, OpenAI Fast tier and Vercel 1-hour cache writes are
  priced correctly, and an unparseable `Retry-After` backs off instead of retrying at once.

## [0.13.3] - 2026-09-29

The first signed release: `orb update` checks Orb's signature from now on. Claude sessions stop
prompting for what Orb already allowed, any text in the TUI can be selected, dialogs stay clear
of images and warnings, and conversations can be deleted. The compatibility target remains Pi
**v0.86.0** on Go **1.27.1**.

- Claude sessions stop asking for permissions Orb already gave: Claude Code could still ask after
  Orb's permission hook allowed a call, and a second prompt now follows Orb's answer (your own
  Claude ask rules and plan mode still prompt). With the permissions plugin off, Claude's tools
  run unasked, like Orb's.
- Any text on screen can be selected and copied: a drag inside a dialog, the editor or the footer
  selects the cells as drawn (it used to select the transcript behind a dialog, or nothing), and
  a click in the login dialog copies its link.
- Images in the transcript leave the screen while a dialog is open: terminals draw them above
  text, so they covered the dialog.
- The TUI no longer lists skill and prompt warnings at startup (a skill name other tools accept, a
  YAML slip in another tool's skill); the skill loads or is skipped as before.
- Releases are signed: `checksums.txt.sig` is an Ed25519 signature over `checksums.txt`, and
  `orb update` (and a paired phone's update of a machine) refuses a release that Orb's key,
  built into the binary, does not vouch for. HTTPS to GitHub alone no longer decides.
- `orb storage delete <session>` deletes a stored conversation by the ID `orb storage sessions`
  lists. In the Android app, the long-press sheet of a phone session deletes it; the open one is
  left for a new session first.

## [0.13.2] - 2026-09-28

The phone's agent gets a real Linux: the Android app installs Termux's base system on first start
and runs every command there, with a terminal on the same system and the phone's files within
reach. Pairing is hardened after a security audit, and the TUI folds quiet tool calls into one
line. The compatibility target remains Pi **v0.86.0** on Go **1.27.1**.

- The Android app carries its own Linux: on first start it installs Termux's base system (checked
  against the sha256 GitHub publishes) and runs it through proot, which ships in the APK. The
  agent's commands run there, with `pkg`/`apt` to install what it needs, and the menu opens a
  terminal on the same system. Nothing of Termux shows, and the Termux app is not needed. Once
  allowed from Home, the phone's files are `~/storage/shared`.
- A new `titles` plugin names each session after its first exchange: a few words chosen by the
  session's own model, set once and never over a name the owner gave. It is on by default in the
  Android app and one toggle away in `/plugins` elsewhere.
- Claude sessions (the `claude-sessions` plugin) get titles too: Claude through Claude Code only
  runs whole conversations, so the plugin names them with the cheapest other model signed in.
- A Bridge retires the throwaway registrations crashed or killed Orbs left behind, two minutes
  after it starts: they only retired on a clean exit, and a long-used machine listed hundreds.
- The Android app names a remote conversation by its device, not its folder, drops the raw
  "succeeded" left under the prompt after a call, and keeps the model name readable next to a
  long device name.
- Sessions on a paired machine can be renamed: the Bridge gains `session.name`, under the same
  grant as switching sessions. In the Android app, a long press on any session, on the phone or
  another device, renames it; `/name` works in remote conversations too.
- The Android app's conversations run `!command` in the phone's Linux, as the TUI does, and open
  the terminal from their header.
- The Android app's Home is reduced to the wordmark, one line per session (its title, then where
  it lives and how long ago) and the prompt box: the readouts, the sessions header, the device
  pills' borders, the rules between rows and inside the prompt box are gone, and status notices
  share a single line.
- The Android app's model picker is a compact sheet rising from the bottom instead of a side
  rolodex: every model grouped by provider with search, and reasoning pinned at the bottom within
  thumb's reach, one segmented control that says what each level means ("balanced", "thorough").
- The Android app's conversations fold each stretch of work into one line, as the TUI folds
  exploration: "worked · 6 thoughts · 5 commands · 3 failed", which opens to the thoughts and tool
  calls in order, each still expandable; a running action stays in view. Thoughts now read as
  actions on the same grid as tool calls instead of a separate tag.
- The Android app remembers the model and reasoning last chosen: every new session on the phone
  starts with them, and threads the phone starts on a paired machine take the choices last made
  for that machine.
- The Android app keeps the condensed display type for the Home wordmark, its readouts and the
  one-word interrupts; screen titles, the pairing screens and the model sheet use the regular
  type.

- `orb bridge join` asks before pairing and says what the other side gets: it reads and drives
  this Orb's conversations, not starting or updating Orb here. Joining now grants conversations
  only; starting Orb on a machine stays with `orb bridge trust` on that machine. A code on stdin
  needs `--yes`.
- Pairing refuses a code that two devices presented: someone else saw it, so neither is approved
  and the owner is told to pair again out of sight. A joining Orb keeps an inviter as a peer
  only once it trusts it, so one that never approves is not dialled at every start.
- Devices a Bridge does not know yet (someone pairing) connect through a small pool of their
  own, dropped after ten minutes unless paired: throwaway identities from a leaked invitation can
  no longer take the connection slots paired machines use.
- The TUI draws names, folders and first messages from paired machines without their escape
  sequences, so a peer cannot write the clipboard or forge links through them.
- Starting Orb for a peer (`host.launch`) waits a minute at most for it to come up, and fails at
  once if it exits first.
- Anthropic sign-in also holds `[::1]` on its callback port, and refuses to start when another
  program listens there: the browser may send the code to either loopback address.
- The Bridge's systemd unit keeps `%` and `$` in paths literal.
- The Android release is built without the signing key present and signed in a separate step,
  with the password read from a file rather than the command line.

- Conversations left without a message are not kept: quitting, `/new` or switching away from
  one removes it, those earlier versions left behind are cleared at start, and the session
  pickers list no empty rows. A session started with a skill shows the skill's name.
- Quiet tool calls in a row share one line: reads, searches, commands, web searches and fetched
  pages read as "Searched the web · 3 web searches · 4 pages", the reasoning between them folds
  inside, and while it works the line says the current step; a click opens the list. Reasoning
  sits right on the steps it leads to, and title-only summaries stack. Long URLs keep their host
  and long paths their file name on one line.
- Settled tool calls sit back at reduced opacity so the conversation reads first; hovering or
  expanding one, a running tool and a failed one show at full strength.

## [0.13.1] - 2026-09-28

A small follow-up to 0.13.0 that also exercises the new update paths: the Android app updates
itself to it from Home, and a paired machine updates to it from the app's Bridge screen. The
compatibility target remains Pi **v0.86.0** on Go **1.27.1**.

- The Android app's Bridge screen shows each paired machine's Orb version, and offers to update
  it, as soon as it opens; before, both appeared only once Home had refreshed.
- The Android release attaches only the app and its sha256: the APK signer no longer adds an
  `.idsig` file, which is for incremental installs from a store.

## [0.13.0] - 2026-09-27

Orb on Android, and Bridge made for servers and phones. The Android app runs the full Orb core on
the phone and is a Bridge peer: pair it by photographing the QR code `orb bridge pair` prints, see
every device's threads in one list, open any of them or start one in any folder of a paired
machine, and update the app and paired machines from inside it. Bridge survives restarts, crashes
and network cuts in seconds, and runs as a systemd service on Linux servers. The compatibility
target remains Pi **v0.86.0** on Go **1.27.1**.

- Orb for Android (`platforms/android`, preview): a native app that runs the full Orb core on the
  phone — its own sessions, tools and plugins — and is a full Bridge peer. It drives the
  unmodified `orb` binary over RPC mode and the new Bridge pipe, pairs both ways (invite, join,
  approve), shows peers' live sessions, and raises approvals, questions and pairing as
  interrupts. It pairs with a computer by photographing the QR code `orb bridge pair` prints,
  lists this phone's past sessions first on Home, sets reasoning per session (local or on a
  peer), and offers the TUI's commands through a `/` palette next to the core's own extension
  commands, prompt templates and skills. Tested standalone and bridged on a physical device.
  Releases now carry it (`orb_<version>_android_arm64.apk` and its `.sha256`), and it updates
  itself: Home offers a newer release and installs it through Android's installer.
- A paired machine updates from another device: `host.update` makes its Orb replace itself with
  the latest release, as `orb update` does, and restart its Bridge on the new binary, so peers see
  it back within seconds. The Android app's Bridge screen shows each device's version and updates
  the ones behind.
- `orb bridge pair` pairs a device in one command: it prints a QR code and a
  `orb bridge join <code>` line, waits for the claim, and approves only after you answer `y` for
  the exact fingerprint that claimed it. `orb bridge join <code>` is the other side: it claims,
  waits for that approval, then trusts the inviter back. Bridge → Create an invitation in the TUI
  can show the same QR code. Invitation codes no longer carry grants, which the joiner never
  used, so they fit a QR code a phone reads off an 80-column terminal.
- Bridge opens any thread on a paired machine, not only the Orbs already running there. With the
  new `host.launch` grant, a peer lists the machine's stored threads across folders
  (`host.sessions`) and starts Orb in a folder, on a new thread or resuming one (`host.launch`).
  Such an Orb is ephemeral: it ends after thirty minutes without activity or with its Bridge,
  and its thread reopens with the next message (the Android app does this by itself). The TUI's device
  panel gains "Open a folder…", and the Android app lists every device's threads in one list, newest
  first. Pairings made from now on include the grant; for an existing pair, run
  `orb bridge trust <peer>` on the machine to add it. `orb bridge view` lists a session's threads
  readably with `/sessions` and opens one with `/switch <number>`.
- `orb bridge service install|remove` keeps a Linux server's Bridge running across logouts and
  reboots as a systemd user service (restarted if it crashes, not after `orb bridge stop`), and
  `orb bridge pair` offers it right after pairing. `orb bridge start` starts the service when one
  is installed.
- An Orb could not attach to a running Bridge after an earlier `orb bridge stop`: the stop marker,
  meant only to keep Orb from starting a Bridge by itself, also kept it from reaching one that ran
  (a systemd service, for one).
- Bridge recovers from restarts and network cuts in seconds. Every peer connection is pinged
  every four seconds and dropped when the peer stops answering; a restarted Bridge dials its
  peers at once and calls move to the fresh channel; a dial toward a peer gives way to the peer
  dialling back. Measured on a phone: under a second after the other machine's Bridge restarts,
  five to nine seconds after a thirty-second network cut (it was twenty to thirty). Calls that are
  unsafe to repeat (`host.launch`) are not retried. A Bridge service started by a long-lived
  process is reaped when it exits instead of lingering as a zombie.
- Provider calls retry when the device has no network at all: Go's own wording for it ("network is
  unreachable", "no route to host", "connection reset by peer", and Android's resolver failure)
  now counts as transient, like Node's "fetch failed" does upstream.
- `orb login --json` is `/login` for apps that draw their own screens: it lists every sign-in
  method with its status and model count, and `orb login --json <provider> [oauth|api_key]` runs
  that method as the TUI does (browser callback, device code, menu, pasted code or API key), its
  events and prompts as JSON lines with one answer line per prompt. The Android app now signs in
  to every provider this way, with the browser redirect landing on Orb's own listener on the phone.
- `orb storage sessions` lists this project's stored sessions as JSON lines (id, name, working
  directory, times, message count, first message) for apps that show session history.
- Bridge reconnects a peer that restarted: a new connection now replaces its oldest channel
  instead of being refused once four stale ones had piled up, and calls use the newest channel.
  A phone restarting its app was locked out of a paired Mac after a few restarts.
- `orb bridge pipe` serves Bridge's owner API as JSON lines on stdin/stdout over one long-lived
  connection, for apps and scripts that would otherwise start a process per call.
- `orb plugins set <plugin> <key> <json>` writes one plugin setting (for example
  `orb plugins set permissions mode '"enforce"'`) without the interactive `/plugins` screen.
- New `jobs` plugin: Orb's own models run commands in the background as Claude Code does. `bash`
  gains `run_in_background` (the call returns at once and a message reports the job's end,
  waking the model) and `monitor` (each line the command prints is reported as it comes);
  `stop_job` ends a job. Jobs go through the same bash, sandbox and permission rules.

## [0.12.1] - 2026-09-27

Fixes from a round of hands-on testing: Orb shows up in Herdr again after Herdr updates itself,
the first launch after an update offers to stop old Orbs instead of refusing, Bridge stops
accumulating dead instances, and typing, exports and the model picker get faster or fixed. The
compatibility target remains Pi **v0.86.0** on Go **1.27.1**.

- Orb shows up as an agent in Herdr again after Herdr updates itself in place: the pane then names
  Herdr's replaced binary (`… (deleted)`), and every state report failed silently. Orb now
  reports through the binary at that path, or the `herdr` on `PATH`.
- The first launch after updating from a version that kept conversations in files no longer just
  refuses while another Orb runs: it names that Orb, offers to stop it, and migrates.
- Bridge no longer keeps a registration for every Orb ever started: an Orb started without
  `--instance-alias` retires its throwaway instance and its state when it exits, and
  `orb bridge prune` removes those left by earlier versions or crashes. Enrolling also prunes
  them before refusing at the 4096-instance limit, which such leftovers would have reached.
- Orb reattaches to a restarted Bridge within a second instead of up to fifteen.
- A Bridge profile whose socket path exceeds the system limit (a deep `PI_CODING_AGENT_DIR`)
  says so and suggests `ORB_BRIDGE_HOME`, instead of failing with "bind: invalid argument".
- `/export` works with the default terminal theme, `/export file.md` writes Markdown, and
  `/import` of an export whose conversation went on since opens it as a copy.
- Typing a long text keystroke by keystroke (a paste without bracketed paste) stays fast:
  430 KB now takes seconds instead of minutes, without growing to a gigabyte.
- The model picker opens at once with Claude Sessions enabled: it waited up to two seconds for
  Claude's model catalog and login check, which now refresh in the background while the last
  reading answers.
- A print or RPC run that ends mid-turn no longer prints "Extension error … ctx is stale" for each
  extension, and an RPC `prompt` without a message is refused instead of starting an empty turn.
- `orb mcp list` says when no server is configured instead of printing nothing.
- Teams accepts a token signed by a freshly rotated key on Windows, where the forced key refetch
  could be skipped.

## [0.12.0] - 2026-09-26

Claude becomes a provider like the others: one conversation moves between Claude and any model,
Claude accounts switch like other accounts, and an Orb conversation and its Claude Code session
stay one conversation in both directions. The session tree is redesigned, and large conversations
open faster in less memory. The compatibility target remains Pi **v0.86.0** on Go **1.27.1**.

- Claude is a provider like the others once Claude Sessions is enabled. `/login` lists **Claude**
  with its accounts: your Claude Code login and any account added with **+ Add account**, which
  runs the official CLI's sign-in. Orb never reads Claude's credentials. Each account shows its
  plan limits in the account switcher and under **Usage and reset times**, like other providers.
- One conversation now moves between Claude and any other model: pick one in `/model` and continue.
  Orb holds the transcript, so Claude reads the turns other models answered, other models read
  Claude's, and switching Claude accounts mid-conversation continues it. The `/claude` menu is gone.
- `orb --resume <claude-code-session-id>` opens a Claude Code session as an Orb conversation, and
  both stay one conversation under one ID: Orb appends to that session instead of copying it, so
  `claude --resume <id>` continues it in Claude Code, and turns added there appear in Orb when it
  opens the conversation again. An import follows the session's latest branch, not rewound ones.
- `orb -p` with a Claude model no longer waits forever when Claude asks to approve a tool: as with
  Orb's own approvals, a print or JSON run resolves the ask by the permissions fallback.
- Accounts stored for a provider an extension registers now resolve at request time.
- Each turn now ends with a dim footer naming the model and how long the turn took
  (`claude-opus-5-5 · 1m 12s`), live and when a session is reopened.
- The terminal theme separates text into clearer levels: replies in full ink, reasoning and tool
  output muted, hints, times and footers dimmer, rules and rails fainter still.
- Code blocks render as a subtle panel with the language as a dim label instead of literal
  ```` ``` ```` fences; long lines wrap inside the panel, and copying a block yields just the code.
- A Claude turn now shows everything Claude does: it no longer ended early, with Claude working
  unseen, when a resumed session first answered a leftover task notification, or when a background
  task finished after Claude's reply. The turn stays open until Claude has read the task's result,
  and a message typed while Claude waits on a task reaches it at once.
- Claude's context meter updates after each step of a long turn, and the footer no longer shows
  "Claude limits stale" after five idle minutes. The model picker shows a price only when the model
  has one.
- `/compact` says why it did nothing ("Nothing to compact", "Already compacted") instead of staying silent.
- `/changelog` shows Orb's own release notes instead of pi's, and pi's hidden `/arminsayshi` and
  `/dementedelves` easter eggs are gone, with the 528 KB image the latter shipped.
- Opening and reading a large conversation is faster and lighter: each stored entry is parsed once
  (a 128 MB conversation opens with half the CPU and a sixth less memory), Claude Sessions reads
  only what Claude appended after each turn, and the SQLite log no longer keeps the size of its
  largest write.
- Claude Sessions no longer ends a turn with "cannot unmarshal string into … tool_use_result" when a
  tool fails: a failed tool reports its result as text, which Orb now accepts.
- Claude Sessions no longer stops with "write |1: file already closed" on the first prompt after
  ten idle minutes: the idle native host has exited, and Orb now resumes the session in a new one.

- Skills show as `◆ name` chips instead of `/skill:name` text, in the composer (deleted whole by
  backspace) and in the transcript, where the invocation stays inline in your message with one
  footer line that expands to the skill; an inline invocation now reaches the model in place instead
  of being moved to the front, and a message naming two different skills is refused with a warning.
- The session tree (double Escape, `/tree`) is redesigned as a modal that reads like the
  conversation: one numbered row per prompt along a single history, with the current position
  marked and turns outside it muted. Where the history forks, the row that differs shows its
  version (`2/3`) and `←→` switches to another, so every branch is reachable without drawing the
  tree. The selected turn's reply shows below the list. `enter` continues after a turn, `e` edits
  its prompt, `f` forks it, `/` searches every branch, `tab` lists every message, and `?` shows
  the remaining keys; folding is gone.

## [0.11.1] - 2026-09-24

Claude Sessions behaves like Claude Code in the terminal: no injected context files and an effort
level that survives restarts. Redundant tests are pruned. The compatibility target remains Pi
**v0.86.0** on Go **1.27.1**.

### Claude Sessions

- Claude Sessions: Orb no longer injects its AGENTS.md or other context files into Claude, which loads
  its own CLAUDE.md; custom and appended system prompts still reach it. A new Claude session opens at
  the thinking level last chosen for its model instead of the global default, and new sessions of any
  provider honor per-model thinking levels as pi does. Task notices appear only for subagents and
  background work, no longer duplicating a foreground command's tool row.

### Maintenance

- Remove 43 redundant tests and fold 14 repetitive ones into table-driven tests, with coverage
  unchanged in every touched package.

## [0.11.0] - 2026-09-24

Every Durable Object or Celld Orb is now a full Bridge peer, paired with and called from a native
Orb in both directions, and the Bridge moves into the core. The Windows suite runs green and blocks
every commit (binaries are not yet released). The repository takes its final shape: core in `ai`,
`engine`, `agent`, `bridge` and `host`, platforms under `platforms/`. The compatibility target
remains Pi **v0.86.0** on Go **1.27.1**.

### Deployments and Bridge

- A Durable Object or Celld Orb is a full Bridge peer at `/agents/<name>/bridge`: pair a native Orb
  with `orb bridge pair join`, administer it through `/bridge/admin`, and let either side call the
  other under its own grants.

- Deploy Orb to Cloudflare Durable Objects or self-hosted Celld cells (`platforms/worker`,
  `make worker-deploy`, `make worker-celld-dev`): each object is a full Orb whose workspace,
  settings and session persist in object storage, driven by RPC frames over WebSocket or
  streamed HTTP. `docs/deployments.md` catalogues every target with its status, capabilities
  and Bridge role.

- SDK: the Bridge is core and the tree is reorganized. `connect` and `plugins/bridge` are now
  `bridge` (with `bridge/protocol`); `connect/agent` and `plugins/bridge/extension` are
  `agent/bridge`, and the `bridge_call` tool is `agent/bridge/tool`. `storage.Document` is
  `host.Document`; `storage/sqlite`, `sandbox` and the Tailcat transport and native Bridge host
  live under `platforms/native/`, the WebSocket transport under `platforms/websocket`, and the
  browser runtime under `platforms/browser`. `accounts` splits into `ai/auth/accounts`
  (document-backed) and `platforms/native/accounts` (file-backed). Update imports; behavior is
  unchanged.

### Windows

- Windows: the Bridge IPC socket is owned by and restricted to the current user, and both ends
  accept only that user; chat previews on Discord and Telegram throttle from the completed call,
  like Slack.

- Several Orb processes writing to the same state database take turns instead of starving each
  other on slow disks (notably Windows): writes queue on a kernel lock next to the database.

- Windows: `!command` config values return their output, Bridge IPC accepts the local owner from
  both ends, first-launch migration ignores unrelated `orb-*` processes, and Node-style file URLs,
  Git Bash paths and virtual-host session paths resolve correctly.

- Windows: Node extension runtime discovery (PATHEXT, `node.exe`, version managers), file URLs
  and drive-rooted paths as Node resolves them on win32, `!command` config values through Git
  Bash or `cmd.exe`, external CLI subagents in a kill-on-close job object, and file-lock
  contention on delete-pending directories. Checkouts keep LF line endings (`.gitattributes`).

### Interface

- Skip Claude Code's `skills/synced` folder, which holds one copy of the claude.ai skills per
  signed-in account: it made the home screen warn about name collisions (`docx`, `pdf`, …) and gave
  Orb claude.ai-only skills. Your own `~/.claude/skills` still load.

- List the ChatGPT subscription models your account actually offers, fetched at startup: new
  releases such as GPT-6 Sol appear without waiting for an Orb update.

- Report Orb as blocked to Herdr while it waits on runtime questions and Claude Sessions approvals,
  with the prompt title as the pane message.

- Stop warning about skill and prompt name collisions at startup: the first definition already
  wins and the agent sees one entry per name. `/reload` still lists them.

- Show each model's provider as a right-aligned column in the model picker at every width, so the
  same model offered by several providers is easy to tell apart.

- Show Bridge as one dot at the far right of the footer, green when on and hollow when off; click it
  to open `/bridge`. Hovering a footer item brightens it slightly and floats its label just above it.
  The footer's directory now shortens to its last folder past 32 characters.

## [0.10.0] - 2026-09-23

Orb's core now builds for every target behind host ports: Linux, macOS and Windows, 32-bit
Linux (iSH), Android (Termux), browser and Worker Wasm, and WASI. One scripted session produces
identical results natively, in browser Wasm and under WASI. Windows builds run in CI but are not
shipped until their test suite is green. Claude Sessions keeps one live Claude process per session
with mid-turn steering. The compatibility target remains Pi **v0.86.0** on Go **1.27.1**.

### Portable core

- Run the agent on any platform through `host.Host` ports (files, processes, documents, credentials, sessions): browser Wasm, WASI and native hosts produce identical sessions. The RPC mode moves to the headless `agent/rpc` package (`rpc.Serve`) as the embedding protocol for every host. Provider families register through `api.Registry` (`ai/api/all` for all of them), so light builds link only what they use; `engine.SetDefaultStreamFn` is removed.

- SDK: the `agent` package no longer links the TUI or syntax highlighting (full AgentSession
  js/wasm build ~12.0 MB → ~9.5 MB gzip). `ResourceThemesResult.Themes` now holds
  `*agent.ResourceTheme` (parsed theme files); `agent/modes/theme.FromFile` renders them, and
  `SessionRuntime.ExportHTMLWithThemes` lets a UI driver supply its active theme to HTML export.

- Build and test Orb on every portable target: Windows (Git Bash tools, process-tree cleanup, native console input, Bridge peer checks), 32-bit Linux for iSH, Android/Termux, iOS type-checking, and the core suites under browser (`js/wasm`) and WASI runtimes. `make check` now includes `make portability`.

- Add outbound-only Bridge clients and an opt-in WebSocket listener, allowing browser Wasm to pair, browse and control remote sessions without starting a local Bridge. Local agent calls require separate remote grants.

- Add an opt-in browser Wasm debug screen with a shared headless assembly, isolated virtual files,
  streamed engine events, cancellation and a responsive chat with direct model API configuration
  (OpenRouter/GPT-5.6 Luna by default), Enter to send and collapsible debugging controls.

- Orb on Linux reaches HTTPS providers from minimal containers that ship without a CA bundle
  (for example `oven/bun` images): the CLI falls back to built-in Mozilla roots, as pi does
  through Node. A system bundle, when present, still wins.

- Orb starts in slim Linux images without `ps` (such as `python:*-slim` and `node:*-slim`): before
  moving existing settings files into SQLite, it now checks for other running Orb processes through
  `/proc` instead of refusing to start.

### Claude Sessions

- Claude Sessions: keep one live Claude process per session, so turns after the first start in about
  a second and messages sent while Claude works join the running turn. Headless runs approve what
  Orb itself would run, Orb's AGENTS.md and system-prompt additions reach Claude, Edit shows Orb's
  diff, unknown tools show their main argument, and `/claude` offers every permission mode. A
  disabled plugin now says how to enable it.

- Claude Sessions: stream every block of a reply as one message with correct token counts, continue
  `/tree` branches (including before a compaction) and interrupted or withdrawn prompts from the
  history Orb shows, write branch summaries natively, end interrupted replies like Orb, clamp
  unsupported effort levels, route typed `/compact`, show tool rows during approval with Orb's
  approval choices (session approvals persist across turns), and keep Orb's `ANTHROPIC_API_KEY` out
  of subscription sessions. The plugin is now a default-off `/plugins` row on SDK 0.3.280, installed
  without the unused bundled CLI (47 MB instead of 255 MB).

- Add discoverable `/claude:models`, `:usage`, `:new`, `:exit`, `:plan`, `:normal` and `:compact` shortcuts to open Claude actions directly.

- Complete Claude SDK background-task draining and cancellation, recoverable versioned setup,
  MCP questions, native plan/compaction controls and bounded lifecycle progress. Clear stale context
  readings; keep all adaptation in the plugin and reuse existing client/Bridge contracts.

- Show native Claude quota locally and through Bridge, with the limiting window, reset times and
  freshness in `/claude usage`. Display used context tokens after the path in the shared footer.

### Interface

- TUI: a "↓ Jump to bottom" pill appears on the last transcript row while scrolled up; click it
  (or press ctrl+end) to reattach live follow.

- Keep filenames visible in collapsed Read calls by shortening long paths from the left, and show command/search details without losing them to word wrapping.

- Fix viewport crashes when collapsing tool output exposes evicted rows; keep cache refills,
  resizing and concurrent transcript updates consistent.

- Give tool actions distinct bold colors and compact titles; hide completed output until expanded,
  keep live command output and errors visible, and shorten native Claude paths inside the project.

- Standardize built-in modals at an 80-column maximum with one-cell outer margins; tighten inner padding below 60 columns and keep mouse targets aligned on resize.

- Batch consecutive reads and searches into expandable activity rows, keep running actions and
  failures visible, and use consistent spacing without per-tool rails or background fills.

## [0.9.0] - 2026-09-22

Orb adds optional native Claude Sessions, a shared question interface and rule-based permission
auto mode. Streaming now uses fewer JSON passes and retains completed Markdown render caches.
The compatibility target remains Pi **v0.86.0** on Go **1.27.1**.

### Claude Sessions

- Add an optional plugin using the official Claude Agent SDK. Start from `/claude`; Orb prepares
  the SDK on first use, while authentication stays with the native Claude CLI. The executing host
  needs Node, npm and Claude Code; remote Bridge clients need none of them.
- Stream native replies and tool activity, resume and fork sessions, and cancel active work locally
  or through Bridge. Save the transcript projection and native checkpoint metadata in SQLite;
  resume also requires Claude's native history on the execution host.
- Discover Claude's native models and supported effort levels, allow model/effort changes, and
  identify Claude sessions in the bottom bar. **Switch to Orb** preserves the saved conversation;
  ordinary launches never implicitly start Claude.
- Apply the enabled Orb Permissions plugin to native Claude tools, sharing rules, approval reuse
  and audit while retaining Claude's native restrictions. Orb's Bash sandbox does not extend to
  Claude's native executable.

### Shared questions

- Add the optional Questions plugin for native Orb and reuse its interface for Claude's native
  questions and remote Bridge conversations. Questions replace the composer while conversation
  history stays scrollable, then restore the editor draft.
- Offer numbered choices with descriptions, custom answers, multiple selections, question tabs
  and a final review. Support single clicks and hover feedback without shifting the layout;
  dragging cannot submit an answer. Dismissal supplies no invented answer.
- Validate replies before consuming them, reject stale dialogs, and keep tool/question summaries
  readable in the transcript. All controllers share the existing execution-bound input path.

### Modularity and maintenance

- Group optional Go capabilities under `plugins/`, split bundled plugins into independent packages,
  and isolate memory file storage and quota footer adapters. Go import paths change; plugin IDs
  and stored data remain unchanged.
- Remove bundled demo extensions, share footer/account quota requests, and keep quota display in
  Providers. Separate native tool execution from Wasm builds so injected tools can run without a
  host filesystem.
- `orb upgrade` is an alias for `orb update`, with the same routes and flags.

### Permissions and login reliability

- Permissions remains opt-in. When enabled, its default mode is now `auto`: resolve asks through
  rules without an AI model or approval prompt, while preserving explicit denials, guards,
  cancellation and configured filesystem containment. Existing explicit mode settings are retained.
- Use `--auto` for a per-run override, or `/permissions` to choose the saved mode. Set
  `permissions.mode` to `enforce` for manual approval prompts; `log` remains audit-only.
  `--auto` refuses `--no-extensions` because the permissions plugin must be available.
- Refuse unresolved compound or expanding shell syntax under scoped restrictions in auto mode;
  explicit whole-tool or exact-command allows remain available. Rules match command text and
  cannot infer everything an invoked program will do.
- Keep manual consent scoped to the tool, arguments, directory and session. Missing UI,
  cancellation and dismissal deny; native Claude restrictions remain intact. Configured native
  filesystem containment covers file tools and child agents even with extensions disabled.
- Finish the local Anthropic OAuth callback response before shutting down its server, preventing
  the browser from receiving a truncated success page after a fast token exchange.

### Streaming performance

- Decode ordinary unescaped JSON strings directly and reuse normalized partial tool arguments
  across Anthropic, Mistral Conversations and Pi Messages. Preserve property order, number spelling
  and lone surrogates; keep the public arguments map available.
- Release canceled session selectors without waiting for stopped status timers to leave the
  runtime timer heap, so their loaders and session lists can be collected promptly.
- Prepare assistant components at the render boundary and retain completed Markdown blocks.
  Own pending presentation data so later provider updates cannot race with rendering. A single
  growing Markdown block still needs a full parse.
- Preallocate merged model catalogs, reuse encoded Bridge partials and restore compiler inlining
  in three JSON packages while keeping the 55 MB release-binary budget.
- Local Apple M4 benchmarks measured plain streamed arguments at **6.22 → 2.88 ms**, escaped code
  at **7.18 → 4.69 ms**, and **40–44% fewer allocated bytes**. Registry allocations fell **42%**.
  Updating a small tail after a completed 256 KiB Markdown block fell from **9.77 ms to about
  19 µs** per frame. Attached Bridge workloads used about **10% fewer allocated bytes**, with
  a **5–15% runtime improvement** across repeated runs. These are workload-specific measurements
  of runtime and allocation churn, not total process RAM or model-generation speed.

### Upgrade and verification

Go embedders must update moved capability imports: `bridge/`, `memory/`, `agent/mcp/` and
`agent/extensions/herdr/` now live under `plugins/`; CLI plugin assembly moved from `agent/plugins/`
to `agent/assembly/`. Plugin IDs and stored data are unchanged. Native SQLite storage remains as
introduced in 0.8.0; no new state migration is required for this release.

Release gates cover build, vet/lint, race tests, byte conformance, Linux fixture regeneration and
the upstream RPC suite. Packaging includes four static Linux/macOS binaries, checksums and a
source archive that rebuilds without Git metadata. The release candidate's largest binary is
**54.51 MB**; all four remain below **55 MB**. On an Apple M4, 20 measured warm-cache launches
had medians of **12.04 ms** for `--version` and **15.40 ms** for `--help`. Isolated macOS candidate
checks cover migration, original-file preservation, export, backup and restore. The inherited
provider/OAuth and real-terminal coverage deferrals remain documented in the release criteria.

## [0.8.0] - 2026-09-21

Orb now stores native application state in SQLite and connects conversations across devices through
an optional, built-in Bridge. This release also reduces long-conversation rendering costs and
simplifies everyday navigation. The compatibility target remains Pi **v0.86.0** on Go **1.27.1**.

### Native SQLite and migration

- Use one native database for conversations, global settings, provider accounts and credentials,
  trust, model catalogs, keybindings, memory, chat delivery state, Bridge identities and receipts.
  Project configuration, installed extensions, exports and process files remain on disk.
- Migrate legacy state on first launch with resumable, transactional imports. Preserve original
  files, session IDs and conversation trees; reject changed sources, damaged trees and unsupported
  versions before cutover. Close older Orb and Bridge processes before upgrading a state root.
- Import and export Pi JSONL, and export HTML and Markdown through the existing codecs. Native sessions have
  stable IDs rather than live JSONL paths. SDK constructors and file-backed defaults stay compatible;
  `orb --pi-files ...` requires a separate state root after native migration.
- Create private, consistent snapshots with `orb storage backup <path>`. Recover conversations with
  `orb storage restore <backup>` without rolling back current credentials, grants, operation receipts
  or chat delivery markers. Conflicting conversation histories are rejected.
- Import or export global configuration explicitly with `orb storage config import|export
  <name.json> <path>`. Retained legacy files are recovery copies and no longer update native state.
- Index session catalogs and title/directory search, paginate without OFFSET scans, and append only
  new journal entries. Keep WAL with FULL durability, reject stale writers, and acquire destination
  ownership before replacing a live conversation. Concurrent schema initialization is serialized;
  normal opens take no write lock.

### Bridge: connect devices and conversations

- Enable Bridge directly from Settings or Ctrl+P. One executable includes the service, pairing,
  local administration and a focused remote conversation view; no separate plugin installation.
- Add devices through copy/paste invitations or existing SSH access. SSH pairing installs or updates
  remote Orb when needed, then conversation traffic uses authenticated Bridge over Tailcat.
- Grant mutual conversation access with one trust confirmation. Pinned identities, durable operation
  receipts, immediate revocation and execution targeting protect remote control and reconnects.
- Show paired devices separately from current availability, refresh conversations dynamically, and
  reconnect attached instances after service restarts. Turning Bridge off stops access while retaining
  saved pairings. Report connection failures in a readable two-line message.
- Cache recently visited foreign conversations separately from owned sessions: at most eight visible
  messages and 32 KiB per preview, with expiry and profile/peer bounds. Offline previews stay read-only;
  reopening checks remote authority, and blocking a peer purges its cached content.
- Retain scoped discovery and optional agent calls with explicit grants. Managed remote conversation
  hosting and a unified multi-Bridge Sessions page are **not included** in this release.

### A smaller, faster conversation interface

- Open Plugins and Bridge pages directly from the command palette. Search individual settings there;
  keep skill suggestions in the composer, including skill completion after `@` and `/` within drafts.
- Limit tool previews to three rows with per-result expansion. Bound live reasoning previews, retain
  full completed text on expansion, and evict offscreen rendered lines from long conversations.
- Restore evicted lines before selection to prevent the reported slice-bounds crash. Preserve logical
  lines when copying wrapped text, paragraphs and wide characters; confirm successful copies briefly
  beside the composer.
- Support word/paragraph selection and composer selection, modifier-arrow navigation and undoable
  replacements. Treat image tags as single editable units, and attach pasted clipboard images or
  dropped files as bounded, resized model-readable content.
- Show the session directory, compact context usage and account quota reset times. Click the reasoning
  indicator to change levels. Keep searchable model favorites and option dialogs stable at narrow sizes.
- Recover missed terminal-appearance replies and refresh cached colors after theme changes. Preserve
  detected colors across resource reloads and prevent stale modal backdrops.
- Report interactive lifecycle to Herdr only when launched in its environment. Refresh reviewed Go
  dependencies and release tooling while retaining compatibility-sensitive edit output.

### Upgrade and verification

Native SQLite replaces live Pi/Orb file sharing. Keep the retained originals for recovery, stop
legacy writers before cutover, and use explicit export when another application needs session files.
Native imports accept product session formats v1–v3; the separate harness v4 SDK remains unchanged.

The release candidate passes the complete macOS and Linux build, vet/lint, race and conformance
gates, regenerated Linux fixtures, and all 29 upstream RPC tests. Repeated SQLite tests on lab-3
and edge cover concurrent processes, interrupted migration, corruption, full-storage rollback and
recovery. Live SSH/Bridge checks exercise 20 instances, restarts, durable receipts and revocation.
All four packaged binaries pass migration and recovery smokes; archive checksums, extracted source
builds and installation from the candidate archives are verified.

On an Apple M4, 30 warm launches measured **13.34 ms** median for `--version` and **16.58 ms** for
native `--help`, with a separate help process peaking at **37.1 MB RSS**. A 100k-session catalog page
measured **0.138 ms**, title/directory search **0.199 ms**, and a durable append after 10k entries
**0.096 ms** in 1,000-iteration microbenchmarks. A million-line viewport allocates about **2.6 KB
per rendered frame**. The largest static-release executable is **54.25 MB**, below the 55 MB cap.
These measurements describe the tested workload and hardware, not saturation guarantees.

See the [storage and upgrade guide](https://github.com/OrdalieTech/orb/blob/v0.8.0/docs/sdk.md#session-management)
and [verification record](https://github.com/OrdalieTech/orb/blob/v0.8.0/docs/plan/PROGRESS.md). Live Bridge
checks use isolated state and faux models; provider/OAuth and real-terminal coverage remain the
previously deferred release checks.

## [0.7.1] - 2026-09-21

- Make Ctrl+P the main command palette, with direct Ctrl+M model selection, Ctrl+N new session, and Ctrl+R rename shortcuts. Keep slash and skill suggestions above the composer while preserving extension and skill compatibility.
- Make menus compact and responsive at narrow widths. A single click confirms an option, and clicking outside a modal cancels it without activating the controls underneath.
- Follow the terminal's light or dark background with readable text, restrained accents, neutral selections, and a gently darkened modal backdrop. Appearance detection runs asynchronously and existing custom themes remain available.
- Add a unified Providers menu with multiple named accounts per provider, per-provider Add account actions, and a pinned Connect provider button. Keep account storage independent of the agent engine.
- Add an optional provider-usage module for Codex and OpenCode Go. Show remaining quota in the footer; click it to compare accounts and switch. Keep usage requests bounded and cached.
- Simplify the footer while retaining clickable reasoning-level bars and context usage. Ctrl+S in the model picker selects and saves the default model; Enter changes only the current session. Compact rendering no longer probes Git metadata.
- Give Shift+Enter newline insertion priority over app and extension shortcuts, including legacy Escape+Return encodings. Explicit Alt+Enter remains available for queued follow-ups.
- Replace the browser login callback's pi branding with Orb branding while preserving compatibility-facing HTML helpers.

## [0.7.0] - 2026-09-20

- Upgrade the Go baseline to 1.27.1 and golangci-lint to 2.13.2; CI and release builds follow `go.mod`, and cached lint tooling rebuilds when its pin or the Go baseline changes.
- Adopt Pi v0.86 transcript-backed system prompts and tool declarations, provider replay/cache/reasoning fixes, extension dispatch and unsubscribe updates, compaction controls, and v4 storage fork/migration corrections.
- **Headless consumer migration:** system messages now appear in normal agent message events by default. Filter them before forwarding events to clients: they can contain system prompts and tool declarations. Existing exported Go signatures and legacy session reads are retained.
- Improve CJK file completion, quoted-directory ordering, and skill-name matching; verify Wayland clipboard command completion and provide actionable clipboard errors.
- Built-in read, bash, edit, and write tools prefer strict JSON-schema sampling without `PI_EXPERIMENTAL`; signal-terminated shell commands and custom shell operations without an exit code report failure instead of success.
- Speed up recent-session and exact-ID discovery with header-only reads, and show progressively loaded resume results while preserving selection and cancelling abandoned scans. Existing session-listing and selector APIs remain available.

## [0.6.0] - 2026-09-12

- Fix Gemini 3 fallback after another model's tool calls: send Google's documented cross-model thought-signature sentinel without modifying stored history or genuine Gemini signatures.
- Update pi compatibility to released v0.85.0: provider wire fixes, refreshed models, safer compaction, session-scoped model controls, queue clearing, and transactional v4 session storage. The product remains pure Go with no new dependencies.
- The released v4 harness format uses transactional entries rather than the previous lane format; use the transaction storage API for v4. Coding-agent sessions continue to use v3.

## [0.5.0] - 2026-08-18

### Changed

- SDK import paths renamed: `codingagent` is now `agent` (the full-featured agent runtime) and the
  former `agent` package is now `engine` (loop + Agent + harness). Types and behavior are
  unchanged; on-disk `~/.pi/agent/` locations, wire formats, and `PI_*` variables are untouched.
- Invalid `plugins.permissions` settings now fail closed instead of being silently tolerated:
  the CLI refuses to start with the validation error, and SDK embedders get a deny-all policy
  whose guard denials hold even in log mode. `--no-extensions` disables the permissions plugin
  and with it the sandbox.
- `orb update` now opens with a smaller mark, then Orb, then update, and runs one full-width process line under it — steps linked by faint rules that recede behind the text, with extra space below. The reveal is eased rather than metronomic: the wordmark fades in, the rule sweeps early and glides into the edge behind a normal-weight tip, and the outcome docks bold.

### Removed

- The unused SDK bundle constructors `tools.NewCodingTools` and `tools.NewReadOnlyTools`;
  construct the tool slice directly (see `docs/sdk.md`).

### Added

- The dormant subagents plugin can expose explicitly configured external CLIs as child roles, feeding each task on stdin and returning stdout under the existing parallel limits and a bounded runtime.
- The permissions plugin adds deny-only SDK guards and named `workspace-write`/`danger-full-access` presets, and can wrap only the built-in bash tool in an opt-in filesystem sandbox: Linux Landlock enforcement is partial because it cannot mediate metadata mutations, macOS uses `sandbox-exec`, and an unavailable sandbox refuses the command with an actionable message instead of running it unsandboxed. Both restrictive modes keep `/dev` and the temp directory writable so ordinary shell idioms (`2>/dev/null`, `mktemp`) keep working, and a `/plugins` reload re-resolves the sandbox mode without a restart.
- Every menu now floats: extension dialogs (selects, confirms, inputs, editors, permission asks),
  `/model`, and `/settings` open as centered framed windows over the veiled page — the same
  window language as `/plugins`, `/permissions`, and `/mcp` — instead of replacing the editor at
  the bottom. Keyboard shortcuts, mnemonics, mouse semantics, and dialog timeouts are unchanged.
- The `/plugins` window becomes the configuration hub: an External CLIs section lists the
  configured sub-agent CLIs and auto-detects known ones on PATH (claude, codex, gemini) — one
  keypress configures or toggles them without losing their command (`plugins.subagents.external`
  accepts `"command"` or `{"command", "enabled"}`) — plus an install-package action
  (npm:/git:/path) that downloads, persists, and applies on close. Type to filter the window.
- Configuration windows in the TUI: `/plugins`, `/permissions`, and `/mcp` open fixed-size framed
  windows floating over a dimmed page, on an aligned invisible grid with a reserved detail area
  (hovering never resizes or moves the window) — plugins show their structured settings (external CLIs,
  preset/sandbox/mode) inline, the permissions window lays out policy, rules, and recent decisions
  with the sandbox mode visible, and the MCP window shows live server state, targets, and
  registered tools with in-place reconnection. The `/model` picker gains aligned context,
  $/Mtok cost, and think/img capability columns.
- `orb mcp <list|get|add|remove|enable|disable>` configures MCP servers from the shell without a
  session and without spawning any server; `add` validates through the exact parser the session
  uses. New reference: `docs/plugins.md`.
- `orb plugins list --all` prints the resolved composition — every compiled extension, bundled
  plugin, and MCP row with its id, source, on/off state, and the settings layer that decided it,
  plus the discovered JS extensions — through the same code path the real boot uses.
- Upstream parity moves to pi v0.84.2.
- `--use-theme <name>` sets the interactive theme for a single run without persisting it, and `/export` now follows the active theme. Themes may define `searchMatchBg`/`searchMatchText` (optional, falling back to `selectedBg`/`text`).
- A `defaultTools` setting chooses the initial built-in tool selection; extension and SDK custom tools stay enabled alongside it.
- Harness sessions expose a run-lifecycle event bus (direct per-type listeners plus buffered watches pairing a consistent snapshot with every later event), can durably clear their name, and session search ships as a standalone `engine/search` service with filters, limits, cursor paging, and cancellation.
- Tools that ask for JSON-schema constrained sampling now send a strict provider schema, falling back when a schema cannot be expressed strictly; under `PI_EXPERIMENTAL=1` the read, bash, edit, and write tools request strict schemas too.
- OpenAI Responses tool-call namespaces survive replay, and models declaring support receive deferred tools through `additional_tools`.
- The OpenRouter image catalog resyncs to upstream's 45 models (adds Seedream 5.0 Lite/Pro, Grok Imagine Image 2.0, MAI-Image-2.5 Pro, Qwen Image 3/3 Pro).
- Orb sets `AI_AGENT=orb` and `PI_CODING_AGENT=true` at CLI and RPC entry so child processes can detect the launching agent.

### Fixed

- A cancelled OpenRouter login reports "Login cancelled" consistently instead of sometimes
  surfacing a raw context error, and external-subagent cleanup on macOS no longer reports a
  spurious failure when the process group is already gone.
- Streaming `message_update` events carry cumulative token usage again in JSON and RPC output.
- Mistral requests go out in the native Chat Completions wire shape, and an empty error body reports the HTTP status text instead of an SDK placeholder.
- Google no longer reports a truncated response as a tool-use stop; Bedrock replays tool arguments without the empty property names its encoder rejects; DeepSeek endpoints are detected regardless of URL casing and receive `max_tokens`.
- A tool argument sent as `null` for an optional non-nullable property is treated as omitted instead of failing validation.
- Corrupt session files no longer fail the whole session listing; a schema-valid but invalid final line is reported as corruption instead of silently truncated; concurrent same-id session creations can no longer both succeed.
- Split `Alt+Enter` over SSH is no longer misread as Escape: the lone-Escape wait is separate from the incomplete-sequence wait, defaults to 100 ms over SSH, and is overridable with `PI_TUI_ESC_TIMEOUT`.
- Focused overlays receive `Ctrl+PageUp`, `Ctrl+PageDown`, and `Ctrl+End`; the transcript viewport no longer claims them.
- Extension `sendMessage` with `triggerTurn: false` no longer starts a turn mid-stream, and `sendUserMessage` accepts `expandPromptTemplates`.
- Concurrent model catalog refreshes share one fetch while each caller keeps its own cancellation, and custom system prompts end with a newline after the working-directory line.

## [0.4.16] - 2026-08-13

### Changed

- `orb update` now installs verified releases directly; package-manager installs remain package-manager-owned.
- The interactive UI now has a rounded composer, quieter conversation spacing, an inset telemetry footer, and a persistent top-left Orb lockup with a subtle unfold.

### Fixed

- Runtime catalog refreshes now expose new models when models.dev supplies an ETag without a Last-Modified header.
- Long model-selection confirmations now wrap to the terminal width instead of crashing the TUI after selection.
- On macOS, inherited empty or `0` `MallocStackLogging*` settings are removed before Orb starts children, preventing Apple's allocator warning from leaking into the TUI while preserving deliberately enabled logging.

## [0.4.15] - 2026-08-11

### Changed

- `--help` and `--list-models` now load extension registrations live instead of replaying an approximate persistent snapshot, so extension order, environment-dependent registrations, and current provider metadata are reflected on every run.
- Text selection is now constrained to the transcript and anchored to content instead of the screen: presses on the editor or chrome start nothing, drags that stray clamp to the thread, selections span multiple messages and extract as clean content (gutter bars, band padding, and the scrollbar column are stripped while relative code indentation is kept), the viewport auto-scrolls when a drag reaches its edges (rate scaling with overshoot), and scrolling under a held selection keeps the highlight glued to its text.
- Tool results sit on one neutral panel instead of a green-tinted success band (opencode's one-panel language — status lives in the glyph, not the band color), and that panel now recedes toward the page background so tool output reads as secondary to user messages: `toolPendingBg`/`toolSuccessBg` are `#1f1f22` dark / `#f1f1f1` light, the diff line-number gutter drops its blue cast (`#26262a` dark, `#e2e2e2` light), and genuine errors keep their faint red band.
- Edit-tool diffs now carry opencode's signature look in both layouts (D35): added and removed rows render on restrained dark-green/dark-red background tints spanning the full row width (`diffAddedBg`/`diffRemovedBg` theme roles, with `diffGutterBg` for the line-number gutter band; user themes missing them inherit the tool-band backgrounds), line numbers sit in that darker gutter with muted foreground, the `+`/`-` signs render bright, and content is syntax-highlighted by the edited file's language with token colors layered over the tints — context rows stay untinted on the tool band. The unified (narrow) layout adopts the same gutter/sign/tint language as the split view plus the `+N -N` header, and wraps long rows onto tinted continuation lines. Tint spans re-open the surrounding band background instead of resetting, so interior highlighter resets can't drop the tint mid-line and nothing bleeds past the row.

### Fixed

- Web fetches once again decode the full HTML5 legacy charset label set, including Shift_JIS, through the existing `x/text` decoder.
- A successful edit no longer renders the tool's "Could not find the exact text" mismatch error: the live preview was recomputed against the current file on every re-render, so once the edit had been applied (or when history was replayed after the fact) the stale recompute replaced or stacked above the recorded diff. The preview now computes once per argument set before execution (matching upstream `edit.ts`), and a final result renders exclusively from the recorded result details — success shows only the diff, failure only the real error.

## [0.4.14] - 2026-08-11

### Added

- Every selector and list overlay is now fully mouse-aware through one shared pointer implementation: hover moves the selection highlight in place (the list window never re-anchors under the cursor, so hover works on the full scrollable `/model`, `/login`, `/resume`, and `/tree` lists), click selects, double-click confirms, and the wheel scrolls with the keyboard's recentering behavior. The editor's autocomplete popup gets the same hover support by enabling any-motion tracking (`?1003`) only while the popup or a selector is on screen — normal typing never pays for a motion-event flood, leaving any-motion re-asserts button tracking in the same write (xterm-family terminals treat `1000/1002/1003` as one mutually exclusive mode, so a bare `?1003l` would kill wheel, clicks, and the scrollbar), Shift+drag native text selection keeps working, and an end-to-end suite drives raw SGR bytes through a tracking-mode-faithful terminal to pin transcript wheel scrolling, the scrollbar, clicks, and hover. `/settings` keeps click and wheel but stays hover-free: its per-item description panel changes the bottom-anchored dialog's height, which would shift rows under the cursor.

### Changed

- The default dark and light themes trade their warm heading/warning tones for a colder palette (D35): markdown headings and the `[Context]` header render steel blue (`#89b4fa` dark, `#35689e` light) instead of amber, the warning role drops from pure yellow to a restrained sand (`#d7ba7d` dark) and a deeper amber that now meets 4.5:1 contrast (`#8a6420` light), and the HTML-export info background swaps its warm olive/cream for cold slate/ice (`#28323c` dark, `#e6f0fa` light). Error red, success green, and diff colors are untouched, and the existing cold accents (teal accent bar and highlights, blue/cyan borders) are unchanged.
- Startup diagnostics render as one compact warning band instead of a full-width wrapped wall: one truncated line per warning behind a warning-colored bar, extension paths reduced to their basename with the "imported from …" tail dropped, and same-name collisions merged onto a single line. Full texts stay available from `/reload` output and stderr in print modes.

- Chat presentation polish (D35, opencode-inspired): user messages carry an accent-colored left bar with the band inset one column, tool and custom message bands reserve the same gutter column with two cells of interior padding, and assistant prose aligns to the shared indent ladder, so all chat content shares one left edge. Edit-tool diffs render side by side (old | new panes with line numbers, red/green marks, and a `+N -N` header) when more than 120 content columns are available, and keep the unified view unchanged below that. Repeated renders of a user message no longer accumulate duplicate OSC 133 zone markers.
- `--help` and `--list-models` now answer from a metadata snapshot cache written on every successful extension-host load (sha256-fingerprinted over the SDK, entry files, package trees, lock file, and trust state) instead of spawning a Node child per invocation: `--help` ~284ms → ~12ms and `--list-models` ~290ms → ~27ms on a machine with installed extensions, with byte-identical output. Any fingerprint mismatch, corruption, or native-provider registration falls back to the full spawn, and real sessions always spawn, so staleness self-heals on the next run. An extension whose load-time registrations depend on env or network may show stale flags/models in these two commands until that next run.
- The binary is ~3.2MB smaller and startup leaner: the syntax-highlighting lexer XML corpus, CJK segmentation dictionary, bundled upstream changelog, and generated model catalog now embed gzip-compressed and decode lazily on first use (each keeps its uncompressed source in-repo behind a byte-compare staleness test), websearch's charset decoding drops the CJK legacy-encoding tables (unknown charsets fall through to the existing UTF-8 scrub floor), and ~40 package-level regexp tables defer compilation behind `sync.OnceValue`. `orb --version` 9.2ms → 7.6ms, peak RSS 21.2MB → 19.3MB, init-phase allocations -35%.
- Hot paths allocate far less, identical bytes throughout: streamed Anthropic tool-call turns -67% wall time and -53% memory (streaming JSON is trusted as already normalized, with a fixed-point property test as the permanent gate), a prompt on a 1000-turn session -46% time and -59% memory (the session path is built and deep-cloned once instead of five times, and branch scans no longer clone every entry), session creation -41% memory, TUI steady frames 1,368B/3 allocs → 728B/2, and a cold 1M-line render -50% time.
- The F12 and WP450 render goldens are now Orb-owned snapshots regenerated from Orb's renderer (`make fixtures-tui`), landing the conversion D35 promised; behavior-shaped values in the same files stay frozen upstream captures, and every other conformance family remains an upstream-parity gate.

### Fixed

- `orb --help` no longer connects to configured MCP servers before printing (a slow server could stall help for its full connect timeout); it now takes the same metadata-only path as `--list-models`, and new guard tests pin that `--version` spawns nothing at all.

## [0.4.13] - 2026-08-10

### Changed

- Faster startup and session resume, identical behavior: the syntax-highlighting lexer registry now builds on first highlight instead of at process start (`orb --version` wall time roughly halves, peak RSS drops ~5MB), the built-in model catalog is decoded once per process and merged without redundant deep clones (offline registry construction ~17ms → ~1.5ms, 29k → 4k allocations), and the session/harness JSONL loaders skip redundant validation scans, per-line UTF-8 transforms, and oversized read buffers (1000-turn v3 session load ~37ms → ~28ms, allocations -26%).

- Extensions importing the pi SDK (`@earendil-works/pi-*` or the legacy `@mariozechner/pi-*` names) are now served entirely by Orb's embedded `orb-extension-sdk`: `pi-dynamic-workflows` runs unchanged — child agent sessions, model catalogs, shared-store/web tools, structured output, worktree isolation, background runs, pause/resume, and persisted transcripts included — with zero Pi-SDK code on the machine. The previous on-demand `npm install` of the real SDK is gone, and `ORB_PI_SDK_ROOT` with it.
- An extension that imports a pi SDK surface Orb does not implement now gets a precise diagnostic — `OrbUnsupportedCapability: <package>#<export> is not implemented by orb-extension-sdk <version>; supported exports: …` — at the point of use, instead of a version-dependent resolution or missing-export failure; any import that would reach a real installed pi SDK is refused at load with the full import chain named.

### Fixed

- `orb install npm:<package>` now materializes the package's required peer dependencies natively from the registry (respecting `peerDependenciesMeta.optional`), so extensions that peer-depend on ordinary packages — e.g. `pi-dynamic-workflows` on `typebox` — load instead of failing with "Cannot find package". Upstream pi serves such peers in-process, which Orb's out-of-process extension host cannot; the `@earendil-works/pi-*` / `@mariozechner/pi-*` SDK peers are deliberately never fetched (the embedded orb-extension-sdk serves that surface), and the dependency `npm install` for managed npm packages now passes upstream's peer-resolution opt-out flags (`--legacy-peer-deps` / bun `--omit=peer` / pnpm config) so no package manager materializes a real pi SDK either.
- The bash tool's system-prompt guideline matches upstream v0.84.1's wording ("You can inspect PI_* environment variables…").

### Changed

- Orb's default system prompt now presents it as a general-purpose problem-solving harness for work and software development, while retaining its coding capabilities and pi-compatible prompt assembly.
- The upstream compatibility target is now pi v0.84.1. Harness session repos write the JSONL v4 format (product session files remain v3, matching upstream), JSON/RPC `message_update` events carry only deltas, Gemini-3 tool-call ids and structured Bedrock failure diagnostics ride the provider wire, OpenAI Responses ending incomplete without a provider reason surface as errors, and `/scoped-models` opens its selector from cached models immediately.

### Added

- Orb automatically discovers Agent Skills installed for Claude Code, Codex, OpenCode, Gemini CLI, Cursor, and GitHub Copilot; project skills remain trust-gated and duplicate external copies collapse deterministically.
- Interactive `@` autocomplete now presents loaded skills as clearly badged, themed entries alongside files; accepting one inserts the canonical `/skill:name` command, ready to submit.
- Built-in Baseten and Qwen Token Plan Individual providers, the `scrollbarThumb` theme color, and iTerm image size metadata, matching upstream v0.84.1.
- Mermaid code blocks in assistant and user messages now render as Unicode terminal diagrams (flowchart, sequence, state, class, and ER), with a "Mermaid diagrams" `/settings` toggle (`markdown.mermaid`: off/final/streaming, default streaming).

## [0.4.11] - 2026-08-03

### Fixed

- Provider turns that declare tool use without emitting a call now receive up to three bounded internal retries without persisting the recovery scaffold, and duplicate tool-call IDs are made deterministic before execution so results remain unambiguous.

## [0.4.10] - 2026-07-30

### Changed

- The project’s public identity is now **Orb**: repository and module path `github.com/OrdalieTech/orb`, `orb` executable and release artifacts, `ORB_*` environment variables, and `orb.*` private namespaces. Upstream compatibility names such as `.pi`, `PI_*`, the JavaScript `pi` API, and session/RPC wire formats remain unchanged.
- The upstream compatibility target is now pi v0.83.0. Stored OAuth credentials refresh with less than five minutes remaining, and tool-schema conformance follows TypeBox 1.3.7, including nullable arrays.
- Sending a message snaps the transcript back to the live tail, so a view scrolled up for reading shows the message and its reply. Scrolling away still holds position against streaming frames.
- When enabled, the memory plugin now has one Hermes-inspired behavior for local and SDK users: a frozen, character-bounded `USER PROFILE`/`MEMORY`, model-led consolidation through `replace`, and `remember`/`recall`/`forget` over the existing `memory.Store`. The former injection and shutdown-distillation options were removed.
- Memory plugin instances no longer share one process-wide store lock: each `MemoryWithStore` instance serializes only its own operations, so multi-tenant embedders' stores can serve concurrent queries across instances. Cross-instance and cross-process coherence remains the `memory.Store`'s responsibility, as durable adapters already require.
- Plain `agent.Agent` SDK users can attach the same bounded memory through `memory/agent`; concurrent same-tenant sessions can make compound mutations atomic through `memory.TransactionalStore`.

### Added

- `orb auth print-api-key` and `orb auth print-bearer-token` export configured credentials without mixing diagnostics into stdout; bearer export refreshes tokens to a configurable minimum validity.
- Streaming messages expose the upstream `pending` and raw provider stop reasons, supported provider requests accept an injected HTTP client, OpenRouter login accepts a pasted redirect URL or code, and extensions receive live `ctx.scopedModels`.
- Interactive startup lists file-backed `SYSTEM.md` and `APPEND_SYSTEM.md` inputs alongside project context files, and `ResourceLoader` exposes their source paths to embedders.

### Fixed

- `/model <query>` opens a searchable model picker with all/scoped tabs and selects the best fuzzy match; session replacement and shutdown cancel the picker before tearing down its runtime.
- Provider requests now use Qwen Token Plan thinking controls, Z.AI `max_tokens`, configured Bedrock profiles, GitHub Copilot Claude Opus 5 metadata, and valid OpenAI function arguments when malformed deltas also carry an empty custom payload.
- Active session replacement and tree navigation settle aborted turns, concurrent bash commands all remain cancellable, and RPC bash commands pass through extension `user_bash` handlers.
- Nested linked worktrees no longer load the same context file twice, failed Git package installs clean their partial checkout, tool-output toggles report their state, and image fallbacks shorten, link, and clamp long paths.

## [0.4.9] - 2026-07-28

### Fixed

- Anthropic streams are no longer hard-killed at `timeoutMs` (5 minutes by default): the timeout bounds only the header phase, matching the pinned `@anthropic-ai/sdk` (10-minute default when unset), so long streaming turns complete. Bedrock no longer applies `timeoutMs` as a whole-stream deadline at all (upstream applies none) and can no longer misreport an internal timeout as a user abort, and OpenRouter image generation no longer races the response body read.
- Aborting a stream mid-flight records upstream's `Request was aborted` in every adapter (openai-responses, openai-completions, Azure, Google, Mistral) instead of raw Go error text such as `context canceled`; aborted provider-retry requests and aborted OpenRouter image body reads return upstream's `Request aborted`, and HTTP-error paths no longer leak the header-timeout wrapper's context.
- JS extension callbacks (tools, commands, shortcuts, event handlers, providers, renderers, dialogs, state actions) are no longer capped at 30 seconds; only host startup RPCs stay bounded, matching upstream's untimed awaits. Extension tools and provider streams now receive a live positional `AbortSignal` that fires when the agent-side context is cancelled, extension-registered provider streams reach the agent incrementally instead of buffering the whole response, extensions get the full Node `console` surface, and the Node host exits when its transport closes so it cannot be orphaned by a hard Orb crash. A cancel arriving in the same stdin chunk as its request still aborts it, abandoning a provider stream mid-iteration terminates the host-side generator instead of buffering unboundedly, abort reasons arrive as Error values, undecodable streamed events fail the stream like the buffered path, and the console bridge adds profile/profileEnd/timeStamp/createTask and a constructible `console.Console`.
- `extensions.Exec` sends SIGTERM and only SIGKILLs after a 5-second grace on abort or timeout, letting children trap TERM and clean up, and a successful command that leaves a background grandchild holding the stdio pipes keeps its own exit code after the bounded wait instead of reporting 1.
- The `project_trust` extension event fires during startup project-trust resolution — for sessions and package commands alike — consulted ahead of the trust store and the interactive prompt as upstream does.
- A panic on any TUI-spawned goroutine (render timer, input reader, loader ticker, autocomplete, colour-scheme timers, stdin flush) restores the terminal — cooked mode, main screen, cursor, bracketed paste off, kitty keyboard protocol popped — before printing the panic and exiting 1, instead of leaving the terminal raw with a hidden cursor. A split escape sequence whose second chunk arrives exactly at the stdin flush deadline keeps its full completion window (the same stale-timer guard now covers the capability-negotiation fragment buffer), astral-plane characters echoed after their kitty CSI-u printable report are no longer suppressed, and the crash restore path can no longer deadlock on a mutex the panicking goroutine holds.
- Terminal cell widths match upstream for keycap emoji, East-Asian-Wide text-presentation symbols, and halfwidth voiced sound marks, fixing wrap and truncation drift on lines containing them.
- A 0-byte `auth.json` (for example after a crash between create and first write) self-heals on the next credential write instead of failing every login with `EOF`.
- Session-file writes no longer leave permanent `.jsonl.lock` files: session locking uses the proper-lockfile-compatible directory lock, which self-cleans on release, is stolen when stale, and interoperates with a concurrently running upstream pi.
- Session files with an explicit empty header id keep it on load, `trust.json` keys sort in JS UTF-16 code-unit order, `models-store.json` and the extension provider store are byte-identical to upstream `JSON.stringify(value, null, 2)` instead of HTML-escaping `<`, `>` and `&`, `harness.CompactionResult` wire JSON routes through jsonwire, extension-host OAuth credentials serialize in upstream member order, and `jsonwire.Marshal` emits `0` for negative zero.
- Harness `PrepareCompaction`/`PrepareTreeCompaction` use upstream harness's own cut-point algorithm, while `FindCutPoint`/`PrepareLegacyCompaction` keep the coding-agent algorithm and treat empty-summary `branch_summary` entries as invisible metadata in cut-point selection — while branch summarization still projects them unconditionally — matching upstream on both sides.
- RPC frames with a missing or non-string `type` answer upstream's untyped `Unknown command` response with the id and type echoed in upstream's `JSON.stringify` canonical form, the untyped path honors a pending extension shutdown like every command, and `--mode rpc` with `@file` arguments fails up front with upstream's error instead of silently dropping them.
- Startup `Error:`/`Warning:` diagnostics are coloured red and yellow when stderr is a TTY (`NO_COLOR` and `TERM=dumb` respected), and unstamped dev builds report `orb dev` instead of a stale version number.
- Interrupting a run restores queued messages to the editor instead of discarding them, and the dequeue binding keeps the current draft after them and reports what it restored, matching upstream.
- chat: the Telegram webhook caps request bodies at 1 MB, and image attachments larger than 20 MB fall back to a textual attachment note instead of being base64-inlined into the prompt.

### Added

- Interrupting a turn that has not shown anything yet now takes its prompt back: the branch rewinds to before it and the text returns to the editor, so you can edit and resend instead of leaving a stranded message (the abandoned attempt stays reachable in the session tree). Once any output has appeared, interrupting aborts as before.
- Upstream's `--verbose` flag forces verbose startup instead of being rejected as an unknown option.
- `--export <session> <output>.md` writes the session as portable Markdown; every other output path keeps the HTML export.
- Exported provider constructors such as `providers.GitHubCopilot()` return complete registry metadata (APIs, base URL, env-key lists) instead of a partially populated struct.

## [0.4.8] - 2026-07-27

### Fixed

- JavaScript extension components can use SDK helpers such as `BorderedLoader` without an uninitialized-theme failure, and `ctx.modelRegistry` now resolves request-time credentials through the owning Go context so account-usage extensions no longer report `auth unavailable` for an authenticated provider.
- Extension credential reads no longer rebuild and transmit the full state snapshot; interactive rendering moves Git/provider metadata off the render thread, reuses the editor’s rendered scroll state for border decoration, and caches stable task-widget lines.

## [0.4.7] - 2026-07-27

### Added

- Anthropic simple streams accept an optional upstream client, preserving Orb's tool and reasoning mapping for hosts that use AnthropicVertex or another client-owned transport.
- Embedders can collect `CompleteSimple` directly and use portable `auto`, `none`, or `required` tool choice through `SimpleStreamOptions`; forcing a named tool stays compositionally small by advertising only that tool with `required`.
- Interactive sessions place transient working/retry/compaction status in the built-in editor’s top border when it fits, with a right-aligned, truncated session-name badge; dialogs, scrolled drafts, and extension-provided editors retain the standard status lane and all existing UI/keybinding contracts.
- The optional tasks plugin now keeps its persistent widget and collapsed tool results to a one-row current/progress/queue summary, lets the widget expand or collapse on click with dimmed, inset details, and exposes the full branch-aware list through `/tasks` and Ctrl+O expansion; retry statuses count down, and queued messages use one-row truncation with their count and configured dequeue-key hint.
- 0.82.1 port, first waves: `ANTHROPIC_AUTH_TOKEN` resolves to bearer headers ahead of the other anthropic credentials; `orb login openrouter` (PKCE) and `orb login kimi-coding` (device flow) mint credentials; bash commands see `PI_SESSION_ID`, `PI_SESSION_FILE`, `PI_PROVIDER`, `PI_MODEL` and `PI_REASONING_LEVEL`, and RPC clients receive streamed `bash_execution_update` events; `/models` lists configured-but-missing ids as `[unavailable]` and picks up `models.json` edits on open; custom renderers receive the live `outputPad`; the external editor works out of its own temp directory with the resolved `$VISUAL`/`$EDITOR`/nano chain; DNS failures retry; scroll borders survive narrow terminals; harness paths expand `~` and `file://` and children are reaped on cleanup.

- Embedders can parse and marshal individual harness session-tree entries without constructing a JSONL file.
- Embedders can prepare compaction directly from canonical harness session-tree entries.
- A loose extension that imports the pi SDK without declaring it — the shape upstream permits because pi bundles its SDK — now installs the pinned `@earendil-works/pi-coding-agent` into orb's own npm root automatically on the launch that first needs it, from the npm registry, never from an installed pi. One line announces the install; `ORB_PI_SDK_ROOT`, `PI_OFFLINE` and a missing npm all skip it, leaving the existing guidance message. Extensions that declare their dependencies are untouched.
- `ORB_NODE` names the Node executable to use, for installs no search reaches; `ORB_NODE=none` disables JavaScript extensions.
- TypeScript published inside `node_modules` on Node 22.6-22.12 now reports the file, the running version and the fix instead of failing opaquely.

### Removed

- The staged-entry mechanism and its `packages/` mirror. Supplying transpiled source from the load hook covers the entry as well as its dependencies, so resolution is now exactly what the package manager laid out.

### Fixed

- Provider-side constrained sampling now survives the complete tool path, including Go agent tools and native or JavaScript extensions, instead of being dropped before the provider request.
- `SessionRuntime.ExecuteBash` retains its exact three-argument Go signature while the new `ExecuteBashWithID` carries an RPC correlation id; OpenRouter’s callback server now drains its response before shutdown instead of intermittently resetting the browser connection.
- Upstream fixture extraction validates and normalizes random summary session UUIDs, and the export/shutdown manifests now identify the 0.82.1 sources, restoring deterministic Linux CI.
- SDK auto-provisioning now also covers package-installed extensions: the ecosystem declares the SDK as a peerDependency (pi's bundling satisfied it implicitly), npm does not materialize absent peers, and conflicting peer ranges across installed packages are tolerated with --legacy-peer-deps. Resolvability up the entry's tree is now the only criterion.
- orb no longer resolves the extension SDK from an installed upstream pi. It searched `PATH` for the `pi` executable and pointed extensions at the npm package that owns it, so a machine without pi got a different result from one with it. The SDK is now taken from orb's own managed npm root, project scope first when the project is trusted, then the user scope. Reading pi's configuration files stays supported; borrowing its code does not. `ORB_PI_SDK_ROOT` remains as an explicit override for a checkout or vendored copy.
- JavaScript extensions now run on Node 22.6, where every TypeScript extension previously failed: the module loader returned a string source for a format that release requires a buffer for.
- The extension host no longer dies on Node 26, which removed `--experimental-transform-types`; an unknown flag aborts Node outright rather than warning. The flag set and the TypeScript transpiler options are now taken from what the Node build in hand accepts.
- pnpm-installed extensions resolve their dependencies: `--preserve-symlinks` made a package reached through the `.pnpm` store resolve from the link site instead of the store. The flag existed only for the staged-entry mechanism and is gone with it.
- An extension published as ESM without `"type": "module"` — the shape npm packs by default — no longer fails on `export`.
- Node is found under nvm, fnm, volta, asdf, mise, nodenv, n, Homebrew and system packages even when a spawned process inherits no `node` on `PATH`, and a broken or chatty shim no longer hides a working install behind it. The chosen runtime is added to the extension host's `PATH`.
- TypeScript published inside `node_modules` runs: Node refuses to type-strip any file under that path, which is where every installed extension and its dependencies live.

- JS extensions importing the pi SDK now resolve the surface upstream gives them. Node's type stripping kept type-only imports from package specifiers, so an extension importing a type such as `ApiKeyCredential` failed to load; and `@earendil-works/pi-ai`'s root entry no longer carries the global API, so imports of `complete`, `stream` or `getModel` resolved to a narrower module than upstream's jiti loader provides. The extension host now elides type-only imports from package specifiers and resolves the legacy surface from the same installed copy, leaving pinned installs untouched.
- Bun hosts resolve the SDK specifiers extensions import under their historical names (`@mariozechner/pi-tui`, `@earendil-works/pi-agent-core`, `@earendil-works/pi-ai/compat`), which previously only Node aliased. A package the extension installs itself still wins.
- Bun's implicit auto-install is disabled, so an unresolved import can no longer fetch a package from npm mid-session; dependencies come only from the explicit install step.
- Skill, prompt-template and slash-command frontmatter keeps the trailing newline that YAML clip chomping gives a closing `>` or `|` block scalar, matching upstream. Frontmatter consisting of an empty `---`/`---` block no longer panics.

### Changed

- orb now tracks upstream pi **0.82.1** (`b4f29368`); every item of the 0.81.1 → 0.82.1 delta is ported and the conformance goldens, embedded changelog, model catalog and version identity moved together. Second wave: `Tool.constrainedSampling` with OpenAI custom tool calls and strict/grammar flags across six providers; abortable provider retries owning the SDKs' backoff with an interruptible sleep; models-store ETag revalidation; compaction and branch summaries isolated with `cacheRetention: "none"` and fresh session ids; the Codex `previous_response_not_found` retry; OpenRouter cache breakpoints on tool results; and the catalog's reasoning-level derivation, at full ID-set parity with the published 0.82.1 package.
- Fixture extraction now scrubs the terminal-identity environment (`GHOSTTY_RESOURCES_DIR` alone flipped the theme to truecolor), fixing most of the documented macOS extraction irreproducibility.
- Conformance extraction now runs against upstream 0.82.x: it synthesizes the generated
  `providers/data/.manifest.json` that `providers/all.ts` began importing, writes synthesized
  provider catalogs in the flat or grouped-by-API shape the checked-out revision expects,
  records the Anthropic provider's resolved credential verbatim so a headers-only resolution is
  captured, derives subscription-provider APIs from the provider factory, and supplies the
  session scope the `/models` command now reads. `UPSTREAM.lock` and every committed manifest now
  pin 0.82.1, with Linux CI regenerating the complete fixture tree before release.

## [0.4.6] - 2026-07-25

### Added

- Native mouse support across interactive mode. Click a row in the session tree to select it, click the `⊞`/`⊟` marker to fold or unfold a branch, and double-click to open it. Clicking also works in `/resume`, `/settings`, `/model`, permission prompts and extension selectors — single click highlights, double click confirms — and clicking an autocomplete suggestion accepts it. Click anywhere in the input editor to place the cursor, and the wheel scrolls inside selectors as well as the transcript. Text selection is unchanged: hold shift to drag-select over any clickable surface, and the scrollbar, wheel-detach and `ctrl+end` reattach behave as before. Terminals without SGR mouse reporting stay keyboard-only.

### Fixed

- Web search and page fetching returned nothing for any page larger than roughly 50 KB while still reporting success; readable extraction now keeps block structure so truncation returns the head of the page instead of an empty result.
- `fetch_content` now rejects loopback, private, link-local and unresolvable destinations, and re-validates every redirect against the same rules.
- Permission `path` rules now also match paths named inside a bash command, so a rule that denies a file is no longer bypassed by reading it through the shell.
- Streaming responses no longer accumulate quadratically: a 64 KB tool call is about 28x faster and allocates about 28x less. Emitted bytes are unchanged.
- Lone UTF-16 surrogates split across streaming chunks are preserved instead of being replaced with U+FFFD, so emoji in tool arguments survive chunk boundaries.
- Editing a session label in a terminal narrower than the label no longer panics the renderer.
- The session tree no longer rebuilds in quadratic time; large sessions stay responsive per keystroke.
- `orb -p` no longer exits 0 with empty output when a prompt fails before producing a reply, such as on context overflow.
- Extension host shutdown can no longer block indefinitely when a grandchild process inherited the host's stderr.
- `orb --help` no longer waits for configured MCP servers to connect.
- `@file` completion now uses orb's managed `fd`, so it works without a system `fd` on `PATH`.
- Subagent progress widgets are cleared when a run ends, and a failing parallel child is reported as an error rather than a successful result.
- `recall` falls back to word overlap, so a query no longer has to be a literal substring of a stored memory.
- Web search honours an explicit `provider` in `web-search.json`, decodes non-UTF-8 pages, rejects binary responses, and no longer echoes provider error bodies that can contain an API key.
- Compaction cut-point selection now counts earlier compaction entries, so a second and later compaction retains the same history upstream retains instead of compacting too little and risking overflow.
- Unified patches now number the second and later hunks correctly; multi-edit patches previously reported the wrong new-file start line.
- Serializing a session containing an unprojectable custom message no longer panics.
- A legacy session that is too small to compact now reports "Nothing to compact" instead of failing.
- Tool execution updates are delivered in order and no longer overlap; concurrent delivery could make streaming output visibly regress. Updates still reach the sink without the tool waiting on it.
- Extensions handling `tool_call` and `tool_result` now receive the prepared arguments the tool actually executes, so documented argument rewriting works; previously the edit silently changed only the recorded call.
- `Dispose()` now cancels every `SubscribeChan` it handed out, so a long-running embedder no longer leaks a goroutine per subscription.
- A disposed session refuses further work instead of quietly calling the model and persisting the turn.
- Concurrent settings writes no longer lose updates, and a failed write no longer leaves the in-memory value ahead of what was persisted.
- The chat gateway's `/stop` is bounded by its own worker pool instead of spawning a goroutine per message, and a permanently failing handler now gives up instead of retrying forever.
- The chat gateway reuses a conversation's session manager between messages instead of re-reading and re-parsing the whole session file on every inbound message.
- Disposing a session or reloading plugins while subagents were running could terminate the host process; child work now fails that child instead.
- Parallel subagent runs are capped at 32 children per call, so one tool call can no longer fan out as wide as the model asks.
- A JavaScript extension host that dies with a fatal error no longer leaves the terminal staggered; its output is line-normalised while stderr is a terminal.
- `orb install` now declares the package it installed in the install root's `package.json`. Packages are still fetched natively, so npm remains optional, but an undeclared entry looked extraneous to npm and the next `pi install` deleted it while `settings.json` still listed it.
- `--list-models` now reports settings errors instead of silently listing against defaults when `settings.json` fails to parse.
- Compacting a disposed session is refused instead of summarizing through the model and rewriting history.
- Refreshing the model catalog left a zero-byte `models-store.json.lock` file behind, which permanently broke `pi update --models` for a coexisting upstream pi: upstream locks with `proper-lockfile`, which creates the lock as a directory and treats any existing path as held. Orb now uses the same directory protocol, removes the lock on release, and reclaims a leftover file from an older Orb release. Delete a stale `~/.pi/agent/models-store.json.lock` once to recover.
- Token costs recorded in sessions could differ from upstream by one unit in the last place in release builds. The compiler fused a multiply and an add on arm64, which the race-instrumented test builds did not, so the shipped binary wrote values the test suite never saw. `make check` now also verifies the byte-compared surfaces in the shipped build shape.

### Changed

- Bundled plugin tools now describe every schema property and declare their required arguments.
- Task list state moved into tool-result details, matching upstream's `todo` extension, so it survives resume and stays correct across branches.
- `make fixtures-check` runs the reciprocal TypeScript-reads-Go gates before the fixture diff; a fixture difference previously aborted the target and skipped them silently.

## [0.4.5] - 2026-07-25

### Fixed

- Terminal shutdown now stops the session picker's reader and restores keyboard mode on the same screen where it was enabled, preventing frozen input and CSI-u leakage into the shell on macOS.
- JavaScript extension reloads no longer block the host reader, change the parent terminal mode through inherited stderr, or crash on late stale-context UI events.
- Stale autocomplete results can no longer rewrite a complete slash command such as `/plugins` when Enter is pressed.
- Double-Escape now opens the session tree at the current leaf with pi-compatible search, filters, paging, branch folding, copy, and labels.
- Node caches compiled extension modules between runs, reducing repeat startup time with JavaScript extensions enabled.

## [0.4.4] - 2026-07-24

### Fixed

- Extension hosts shut down with Orb instead of becoming orphaned and crashing later with `EPIPE`.
- Large sessions remain available to extensions without duplicating the transcript across bridge snapshots, and stale asynchronous extension actions are isolated instead of panicking Orb.

## [0.4.3] - 2026-07-23

### Fixed

- Extensions retain access to hoisted transitive Node dependencies after staging.
- Typeless TypeScript extensions start quietly, and the custom-model example uses the current upstream shape.

## [0.4.2] - 2026-07-23

### Fixed

- Extensions can reuse SDK packages from an installed `pi`; incompatible extensions remain isolated warnings.

## [0.4.1] - 2026-07-23

### Fixed

- Loading frames reuse incremental session totals; Ctrl-C exits from an empty editor, and quit drains Ghostty/Kitty key releases before returning to the shell.
- The session tree keeps linear conversations flat and adds indentation and connectors only where branches exist.

## [0.4.0] - 2026-07-23

### Changed

- The memory SDK moved from `codingagent/memory` to root-level `memory` before external adoption, changing its import path to `github.com/OrdalieTech/orb/memory`.

### Fixed

- Memory distillation now derives provider authentication through the model registry and cannot block session shutdown beyond 30 seconds.
- OpenRouter Anthropic caching now anchors the latest tool result and enables cache controls for the `~anthropic/*-latest` aliases.

## [0.3.4] - 2026-07-23

### Added

- An Orb-original `codingagent/memory.Store` SDK seam ships with an append-only, locked JSONL file store and optional semantic-search interface.
- A fifth bundled-but-dormant memory plugin adds `remember`/`recall`, bounded startup index injection, opt-in session distillation, and custom-store injection.

### Changed

- Mouse selection keeps scrollbar drags captured and double-clicks copy the visible sentence.

## [0.3.3] - 2026-07-23

### Added

- Left-dragging in interactive mode highlights visible text, holds the viewport stable during streaming, and copies the selection on release.

## [0.3.2] - 2026-07-23

### Changed

- Interactive mode collapses the idle/working spacer and adds a one-column clickable scroll thumb.
- `orb update` now reports installed package versions dynamically and `--extensions` names every package that changed.

## [0.3.1] - 2026-07-22

### Changed

- `orb update` now reports whether the running release is current before showing reinstall instructions.

### Fixed

- Permission `path` rules canonicalize both the rule pattern and the candidate path, so rules on
  symlinked locations (e.g. macOS `/tmp`) match reliably.
- Extension-host dialog cancellations arriving before the handler registers are preserved instead
  of dropped, removing an intermittent hang in custom-component flows.

## [0.3.0] - 2026-07-22

### Added

- A bundled-but-dormant tasks plugin adds the `todo` tool and live session checklist, enabled through settings, `orb plugins`, or `/plugins`.
- A bundled-but-dormant websearch plugin adds Exa, Brave, and Tavily search plus lightweight HTML/text fetching.
- A bundled-but-dormant subagents plugin adds injectable in-process scout, worker, and reviewer child sessions with bounded parallel execution.
- A bundled-but-dormant permissions plugin adds last-match-wins allow, deny, and ask rules with permissive audit-only defaults and inherited subagent policy.
- `orb chat <platform>` runs every built-in chat adapter through one durable CLI gateway system.
- An out-of-process extension host runs the full JavaScript/TypeScript extension API through a
  local Node.js or Bun process, including providers, UI callbacks, state synchronization, package
  dependency materialization, and the PATH-to-orb compatibility shim.

### Changed

- JavaScript and TypeScript extensions now require local Node.js ≥22.6 or Bun. Without either
  runtime, orb reports one clear diagnostic while skills, prompt templates, MCP servers, and
  built-in tools continue to work.
- Interactive mode now keeps the status, extension widgets, input, and footer fixed at the bottom
  while the transcript scrolls independently. Mouse-wheel or `Ctrl+PageUp` scrolling pauses live
  follow, and scrolling down or pressing `Ctrl+End` returns to the latest loading or streamed output.
- Huge transcripts now cache stable message layout and render only the visible window plus a changed
  tail, keeping loading and streaming frame cost independent of conversation length after warm-up.

### Fixed

- Subagent children now inherit request authentication from the parent model registry and surface
  provider stream errors instead of reporting an empty final response.

### Removed

- The embedded Sobek JavaScript engine, esbuild transpiler, Node compatibility shims, vendored
  TypeBox runtime, and their bridge-only conformance fixtures.

## [0.2.1] - 2026-07-22

### Fixed

- `terminal.clearOnShrink` now erases vacated visible rows with the differential renderer instead
  of clearing and replaying the terminal, so streamed responses no longer destroy scrollback when
  their Markdown or loading layout becomes shorter.

## [0.1.3] - 2026-07-22

### Fixed

- `orb update --extensions` now reconciles installed Git packages pinned to abbreviated commit
  IDs from the existing clone instead of passing the abbreviation as an invalid remote fetch ref.
- Live TUI redraws disable xterm-compatible scroll-on-output mode while Orb is running, so
  supporting terminals keep a user's scrollback position during loading and streamed responses.

## [0.1.2] - 2026-07-22

### Added

- A reproducible public-extension compatibility harness locks the 44 most-downloaded valid Pi
  packages, compares stable load and registration behavior against Pi 0.81.1, audits each primary
  workflow, and measures seven offline command handlers plus Piolium's knowledge-base workflow.

### Changed

- Synchronized the complete in-scope upstream target to pi 0.81.1: compaction and branch-summary
  retries with lifecycle events, the restored default stream fallback, deferred interactive model
  refresh, Kimi K3 compatibility metadata, and regenerated Gemini catalogs and conformance fixtures.
- Releases now include a checksummed deterministic source archive that CI rebuilds before publish;
  the Homebrew publisher uses GoReleaser's current cask configuration.

### Fixed

- `--no-extensions` now disables discovery while preserving explicit `-e` extensions, and the
  upstream `--theme`/`--no-themes` resource-selection flags are available.
- JS extensions can import `buffer`/`node:buffer`, append transcript streams with
  `fs.createWriteStream`, resolve the pi-ai root as the upstream compat superset, and use
  `import.meta.dirname`/`filename` to locate bundled resources from the package directory.
- Popular extensions can use common `fs` realpath/copy/remove/access APIs, their promise
  counterparts, and synchronous argument-safe child processes through `execFileSync`.
- OpenAI and Azure Responses requests now match the pinned SDK's ten-minute header timeout and
  `X-Stainless-Timeout` wire format, while Codex error fallbacks stringify parsed events and drop
  non-string event types like upstream.
- Bedrock payload hooks preserve a deleted `inferenceConfig`, Vertex ADC reports unknown metadata
  detection modes verbatim, and Anthropic's subscription warning survives OAuth refresh failures.
- Model generation now requires an explicit NVIDIA NIM listing instead of an invented fallback,
  while concurrent and cancelled remote-catalog refreshes preserve upstream cache semantics.
- The extension runtime now supports Piolium child sessions and its filesystem, streaming decode,
  and cancellation workflows, alongside the Node/SDK surfaces used by more ecosystem packages.
- Bundled dependencies now keep module-local `import.meta` paths, Node-compatible UID scoping, and
  `Buffer.byteLength`; this restores exact `pi-subagents /subagents-doctor` discovery of all eight
  shipped agents and Piolium's bounded file-reading workflow.

## [0.1.1] - 2026-07-21

### Fixed

- JS extensions and bundled dependencies can import the Node process built-in as `process` or
  `node:process`; both resolve to the existing process global.

## [0.1.0] - 2026-07-21

### Added

- Current upstream SDK surface: image-model registry and OpenRouter catalog, typed RPC client,
  public retry/overflow and skill-block helpers, custom-theme HTML export, and notify-only update
  checks with orb and upstream version identity.
- Release hardening: immutable CI action SHAs, fixture regeneration at tag time, strict changelog
  notes, clean-macOS checksum support, and a 754 KB amd64 linker-alignment reduction.
- Upstream pi 0.80.10 sync to `3a40794e`: tool-result and summary usage accounting, Qwen Token
  Plan and refreshed provider catalogs, deferred model refresh with upstream's offline quirk,
  public text and UUIDv7 helpers, RPC thinking levels, editor paste history, cursor cleanup, and
  regenerated conformance fixtures.
- Chat gateway wave 2: stdlib-only Slack, Teams, Discord, Messenger, and Google Chat adapters,
  plus shared RFC 6455 and Meta Graph webhook helpers.
- jsbridge Node compatibility for real ecosystem extensions: `node:crypto` (randomUUID,
  randomBytes, createHash/createHmac with hex/base64/base64url digests), `node:http`/`node:https`
  (minimal server + client over Go net/http), `node:module` `createRequire`, and the
  `atob`/`btoa`/`TextDecoder`/`structuredClone` globals; fs shim errors are Node-shaped
  (`code`/`errno`/`syscall`/`path`, so `err.code === "ENOENT"` idioms work); `import.meta.url`
  is defined per bundle as the entry's `file://` URL; `.node` native addons and WebAssembly
  modules fail with explicit "not supported by the orb extension runtime" diagnostics.
- jsbridge pi-* module surface: `@earendil-works/pi-ai` exports `EventStream`,
  `AssistantMessageEventStream`, `createAssistantMessageEventStream` (upstream
  `utils/event-stream.ts` port) and `calculateCost`; `pi-coding-agent` exports `getAgentDir`,
  `getMarkdownTheme`, `VERSION`, `parseFrontmatter`/`stripFrontmatter`; `pi-tui` exports the
  full `Key` builder and `isKeyRelease`. Unknown imports from the pi-* shims now fail at first
  touch with a clear "not exported" error instead of resolving `undefined` and breaking later.
- Extensions from installed pi packages load in every session (`orb install` now delivers its
  main payload), and `-e npm:<pkg>` / `-e git:<repo>` performs upstream's temporary-install
  resolution instead of treating the spec as a literal path. npm/git package dependencies are
  installed through the settings `npmCommand` (default `npm install --omit=dev`), skipped when
  deps are absent or bundled, with a warning instead of a failure when npm is missing. The npm
  registry honors `npm_config_registry`, project and user `.npmrc` `registry=` lines, and
  nerf-darted `_authToken` bearer auth.
- Interactive extension shortcuts: `pi.registerShortcut` handlers now dispatch on keypress
  (matched before built-in keybindings, reserved bindings still win with a stored diagnostic),
  mirroring upstream interactive-mode dispatch and insertion order.
- RPC extension UI: the extension UI bridge is bound on every session rebind, so
  `extension_ui_request` events (notify, dialogs, status, widgets) stream to RPC clients and
  `ctx.hasUI` is true, matching upstream rpc-mode. MCP: `"disabled": true` on a server entry is
  honored as a disable switch (config portability from other MCP clients); one invalid
  `mcpServers` entry no longer disables the rest (per-entry warnings); explicit `maxRetries: 0`
  disables streamable-HTTP reconnect retries; startup connects run concurrently per server.

### Changed

- Synchronized the behavioral target to upstream pi 0.81.0 (`9c480b6a`): required stream injection,
  retained-tail session APIs, split public/coding compaction contracts, refreshed model and image
  catalogs, strict catalog validation, product assets, actions, and regenerated conformance goldens.
- Model generation now intersects NVIDIA NIM and consumes the live OpenRouter and Vercel catalogs;
  runtime catalog freshness follows upstream's `checkedAt`/`lastModified` rules.
- Interactive login now auto-opens OAuth URLs, uses the searchable fuzzy selector, reports exact
  completion/default-model outcomes, and warns once for Anthropic subscription extra usage.
- Renamed the repository, Go module, release artifacts, and CLI to `orb`, so it installs beside
  upstream `pi`; `orb update` now prints exact installer and Go routes.
- Releases, CI, and `go install` now pin Go 1.26.5. On identical source, the in-memory 1,000-turn
  Processor core and F12 renderer are each 2.8% faster; no-prompt startup is 1.7% slower, minimal
  session creation is 4.8% slower, and the stripped Linux binary is 0.9% larger than Go 1.25.0.

### Fixed

- Closed 52 provider, catalog, and login parity gaps, including Codex consumer cancellation and
  zstd transport, OpenAI/Azure timeout and pricing behavior, lossless unknown pi-message events,
  Bedrock payload hooks, Mistral streamed arguments, Cloudflare auth, and OAuth credential wire data.
- Turn refresh now carries prompt, tools, model, and thinking changes into the next provider call;
  custom and branch-summary entries count toward compaction; model/thinking mutations share
  persistence and extension events; provider-header hooks run before affinity headers.
- CI now pins the signed Node 24 `actions/checkout` v7.0.1 commit instead of the deprecated
  Node 20 action runtime.
- Hosted macOS verification now handles APFS realpath, case, and Unicode normalization without
  weakening Linux coverage; interactive session replacement is race-free and custom extension
  messages request their render deterministically.
- Session entry IDs no longer copy the complete ID index before every append, removing quadratic
  allocation growth from long sessions while preserving collision handling.
- Interactive history renders skill invocations as the upstream collapsible skill block plus an
  optional separate user message instead of exposing the raw `<skill>` envelope.
- Long-session compaction checks now walk directly from the active leaf to the latest compaction,
  avoiding a full cloned branch on every turn; the retained 20,000-entry benchmark is allocation-free.
- Resource discovery now deduplicates canonical paths in linear time and reuses package metadata,
  cutting minimal agent-session creation from about 49 ms to 32 ms on a 25-skill install.
- Chat gateway hot paths allocate less and wake only the worker needed, with wire, authentication,
  Unicode, recovery, and per-conversation ordering behavior unchanged.
- `make test` and the fixture race checks explicitly enable CGo for Go's development-only race
  runtime, so an inherited `CGO_ENABLED=0` no longer prevents the gate from starting; every product
  and release build remains static with CGo disabled.
- RPC state responses can no longer overtake the prompt acknowledgement that initiated a session
  replacement, while extension UI replies remain live during that replacement.
- Chat wave-2 transport hardening: WebSocket message limits cannot overflow,
  Slack file tokens stay on Slack hosts, Google Chat JWKS refreshes and
  per-space writes are throttled, Discord reconnect/heartbeat state is
  bounded per connection, and Teams conversation state is bounded.
- SECURITY: `orb --help` and unknown-flag invocations no longer load untrusted project settings.
  Previously those paths constructed settings without the project-trust gate, so an untrusted
  project's `mcpServers` could execute arbitrary commands and make network requests from the
  most innocuous invocations.
- RPC mode dispatches extension commands (`/mcp`, ...) before model/API-key preflight, matching
  upstream agent-session ordering — MCP diagnostics work on keyless installs.
- Extension factories ran twice per startup (duplicated side effects); the resource loader now
  adopts the pre-loaded registry once and only `Fresh()`es on real reloads.
- MCP tools survive session registry rebinds: re-running the MCP extension factory re-registers
  discovered tools on the new API instead of silently dropping all of them; `Start()` failures
  surface as warnings; child exit statuses no longer report as `session_shutdown` extension
  errors; a tool call failing with EOF deactivates that server's tools immediately.
- Interactive `/reload` leaked ~16 MB per reload (previous jsbridge loader VMs were never
  closed); RSS now plateaus.
- `registerEntryRenderer` receives the full custom session entry (`entry.data` works) instead
  of the bare data payload; `ctx.compact()` `onComplete`/`onError` fire even when the
  dispatching event's context is gone.
- Skills parity edges: nested ignore-file basename patterns scope to the ignore file's own
  directory and root-anchored `/patterns` match at any depth (upstream npm-ignore semantics,
  bug-for-bug); non-string frontmatter `name`/`description` reject the skill with upstream's
  type-error warning shape; collision diagnostics trail all warnings; headless (`-p`/RPC) runs
  no longer print per-skill validation warnings (interactive keeps them, with paths).
- `--list-models` creates the full runtime so extension-registered providers appear (but skips
  MCP servers, which contribute tools not models, so model enumeration no longer spawns and
  connects them); `--help` documents `--extension/-e` and the package subcommands; package git
  operations are quiet (`-q`, no detached-HEAD advice).
- RPC extensions see a live `ctx.ui` on `session_start`: the session defers its start until the
  RPC extension UI is bound, so startup `notify`/`setTitle`/`setWidget`/`setStatus` calls reach
  the client instead of firing against the headless noop UI.
- Ported upstream's `docs/providers.md` and `docs/models.md`, which the "No API key found"
  guidance and the system prompt reference; the guidance falls back to the hosted copies when no
  docs directory ships next to the binary.

- Streaming TUI flicker: long/streaming bash tool output is no longer rendered uncapped, which
  had pushed the block above the viewport and forced a full-screen clear (ESC[2J) on every
  streaming update (measured ~192 full clears over 260 tool-delta frames). Collapsed tool output
  now shows a bounded preview of the last visual lines with an "(N earlier lines, … to expand)"
  hint, mirroring upstream's bash renderer; `!` bash-mode output caps while still running, not
  only when complete. Ported upstream's `truncateToVisualLines`; guarded by a renderer-level test
  asserting zero full-screen clears during in-viewport streaming, plus a WP450 byte-parity golden.
  The concurrent tool-component render race (torn frames during rebuild) was fixed separately.

Full-parity port of upstream pi v0.80.10 (`3a40794e`). Release candidate: every locally
provable M1–M5 criterion is green; the owner-gated verification remainder is listed in
the M5 trim checklist (retired).

### Added

- Full TUI parity with upstream pi 0.80.10: components, application frames, all interactive
  commands, `ctx.ui` lifecycle, themes, terminal images, clipboard command paths (M3).
- Headless parity: print/JSON/RPC modes, upstream RPC suite compatibility, eight provider API
  shapes, Anthropic/ChatGPT-Codex/Copilot/xAI OAuth flows, MCP client, packages and project trust,
  JS extension bridge runtime with non-UI API and node shims (M1–M2 plus consolidated expansion).

- JS extension bridge `ctx.ui`: dialogs (select/confirm/input/editor), notifications, status,
  widgets, footer/header factories, hidden-thinking label, working indicator and message, title,
  theme access and switching, tools-expanded state, autocomplete providers, and AbortController —
  seventeen more upstream single-file examples run unmodified.
- JS extension bridge custom UI (gate G3): `ctx.ui.custom` with overlay options and
  `OverlayHandle`, focusable components, `setEditorComponent`/`getEditorComponent`, and the
  `CustomEditor` base class backed by the real built-in editor — modal-editor and six more
  custom-UI examples wired.
- JS extension bridge example matrix (M4): 61 of the 69 upstream single-file extension examples
  (88%) run unmodified — pi-tui `Text`/`Box`/`Container`/`Spacer`/`Loader`/`CancellableLoader`
  component classes, `BorderedLoader`/`DynamicBorder`, `convertToLlm`/`serializeConversation`,
  truncation utilities, `CONFIG_DIR_NAME`, a `node:readline` shim, live message/entry renderers,
  and Node-style `execSync` errors; superseded by `docs/sync/ecosystem-extension-matrix.md`.
- JS extensions load in the product: settings-configured and project extension paths plus the new
  `--extension`/`-e` flag route through the bridge loader into the shared registry; `/reload`
  rebuilds changed bundles and replaces per-path VMs.
- OpenRouter image-generation client (`openrouter-images` API shape): non-streaming Chat
  Completions request with image/text modalities, data-URL result decoding, and the `ai/api`
  `GenerateImages` dispatch entry point.
- SDK parity helpers mirroring upstream exports: `tools.NewCodingTools`/`NewReadOnlyTools`
  bundles and public `ai.CalculateCost`, `ai.SupportedThinkingLevels`, `ai.ClampThinkingLevel`,
  `ai.ModelsAreEqual`, `ai.HasAPI` (private duplicates removed).
- `settings.httpProxy` is honored: exported as HTTP(S)_PROXY for pi-managed clients unless the
  environment already sets them (upstream http-dispatcher semantics).
- Release machinery: goreleaser config for linux/darwin × amd64/arm64 with ldflags-injected
  version, a tag-triggered release workflow that re-runs the full gate and extracts notes from
  this changelog, a checksum-verifying curl install script, and CI running `make check` on every
  push. Update checks remain notify-only (gate G4 resolved).
- README newcomer path: install, first session, SDK embedding, and running upstream extensions.
- `/session` shows upstream's full cost panel: cached/uncached prompt split, per-model cost
  breakdown (`provider/responseModel`, sorted by cost), and "Cache Re-billed" totals from the
  ported cache-stats arithmetic (upstream unit cases included).
- `/settings` gains upstream's "HTTP idle timeout" entry (30 sec/1 min/2 min/5 min/disabled),
  persisted to `httpIdleTimeoutMs` and applied to the next request.
- `/export` HTML pre-renders custom extension tool calls/results through their TUI renderers
  with upstream's ANSI-to-HTML conversion, and embeds the active tool list.
- opencode models send `x-opencode-session`/`x-opencode-client` session-affinity headers on
  every request; the per-request stream session id now also reaches providers from the CLI
  runtime path (prompt-cache keys and affinity headers for Anthropic/OpenAI/Mistral/Codex).
- Tool headers `~`-shorten home paths and emit OSC 8 `file://` hyperlinks in terminals that
  support them (upstream render-utils).
- Six upstream numbered regression tests ported: message_end cost override (3982), explicit
  provider retry guidance (6019), pending tool renders surviving chat rebuilds (4167),
  session_start render/notify ordering (5943), queued extension slash follow-ups staying raw
  text (2023), and the extension factory cache (bundle cached, factories re-run).
- Typed per-tool event accessors in `codingagent/extensions` (`BashToolCall`/`BashToolResult`
  through `LsToolCall`/`LsToolResult`) — the Go analog of upstream's `isBashToolResult`-family
  type guards over the tool_call/tool_result union.
- `ai.ParseStreamingJSON` exports the streaming tool-call argument parser publicly, matching
  pi-ai's `parseStreamingJson` index export (delegates to the internal partial-JSON port).
- Extension UI kit exports from `codingagent/modes`: `ExtensionSelectorComponent`,
  `ExtensionInputComponent`, `ExtensionEditorComponent` (with constructors) and the
  `KeyText`/`KeyHint`/`RawKeyHint` hint helpers from upstream's "UI components for extensions"
  index block.

### Fixed

- Legacy app-scoped keybinding names (`interrupt`, `expandTools`, `tree`, ...) now migrate to
  their namespaced ids when `keybindings.json` loads, completing upstream's
  `KEYBINDING_NAME_MIGRATIONS` table; previously only the `tui.*` names migrated.

- Footer shows `detached` on a detached HEAD (was the literal `HEAD`), matching upstream's
  footer-data-provider.
- Live extension custom messages (`display: true`) render in the interactive transcript as they
  arrive; previously only the rebuild-from-entries path showed them.
- Selector lists use upstream's select-list palette (accent selection, muted descriptions);
  the previous unknown `selectedText` color crashed once a real theme was active.

### Changed

- Conformance extraction is environment-independent (COLORTERM pinned, deterministic fixture cwd).
