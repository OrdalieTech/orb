// orb-extension-sdk: host-service seam.
//
// The three protocol-backed exports (coding-agent#createAgentSession,
// coding-agent#ModelRuntime, coding-agent#ModelRegistry) call through this
// module. The SDK owns the classes and mirrors; the extension host (host.mjs,
// SERVICES lane) owns the transport: it calls bindTransport() once at startup
// with an object implementing the contract below, wired to the Go runtime over
// the capability-negotiated protocol (agent_session_v1, model_runtime_v1).
//
// ── Transport contract ───────────────────────────────────────────────────────
//
// transport.capabilities: Iterable<string>
//   Capability ids negotiated in host_hello (e.g. "agent_session_v1").
//   A service method may only be called when its capability is present.
//
// All methods return Promises and reject with structured errors
// (Error with a `.code` string); a rejection never tears the channel.
// Handle ids are strings minted by Go. Every handle has an explicit dispose.
// Event callbacks are delivered in per-session `seq` order, and always before
// the settle of the operation that produced them.
//
// capability "agent_session_v1":
//
//   agentSessionCreate(options, events) → Promise<{sessionId: string, modelFallbackMessage?: string}>
//     `options` is the untouched CreateAgentSessionOptions object from the
//     extension: cwd?, agentDir?, modelRuntime? (SDK ModelRuntime instance —
//     read .runtimeId), model? (possibly an off-catalog synthesized Model
//     object; route by its fields), thinkingLevel?, scopedModels?, noTools?,
//     tools?, excludeTools?, customTools? (LIVE ToolDefinition objects with
//     execute closures — keep in-process, serialize only
//     name/description/parameters; run prepareArguments and JSON-Schema
//     validation before execute; honor result.terminate), resourceLoader?
//     (SDK DefaultResourceLoader — read .options, honor noExtensions),
//     sessionManager? (SDK SessionManager — read .kind/.cwd/.sessionDir/
//     .persist), settingsManager? (SDK SettingsManager — read .cwd/.agentDir),
//     sessionStartEvent?.
//     `events` (all optional; SDK always passes all of them):
//       onEvent(event)                  raw AgentSessionEvent mirror; drives subscribe()
//       onMessagesSnapshot(messages)    replace the session.messages mirror
//       onMessageAppended(message)      append one message to the mirror
//       onMessageUpdated(index, message) replace mirror[index] (delta updates)
//       onStats(stats)                  update the getSessionStats() mirror
//     Provider limit errors must land in the mirrored assistant message as
//     stopReason "error" + verbatim errorMessage — never as a rejection.
//
//   agentSessionPrompt(sessionId, text, options?) → Promise<void>
//     Resolves when the turn settles (matching AgentSession.prompt). The bound
//     transport additionally honors an AbortSignal at options.signal: it
//     mirrors into orb as a service_cancel event cancelling the Go-side
//     context, and the promise still settles from orb's response (rejection
//     code "cancelled") so cancellation is deterministic, never local-only.
//   agentSessionAbort(sessionId) → Promise<void>
//   agentSessionSetActiveTools(sessionId, names: string[]) → Promise<void>
//   agentSessionDispose(sessionId) → Promise<void>
//     Idempotent on the orb side; releases the retained tool closures.
//
// capability "model_runtime_v1":
//
//   modelRuntimeCreate(options) → Promise<{runtimeId: string, catalog: OrbModelCatalog}>
//     options: {authPath?, modelsPath?, allowModelNetwork?, refreshOnCreate?, ...}
//     (CreateModelRuntimeOptions fields; unknown fields ignored;
//     allowModelNetwork defaults false, matching upstream).
//   modelRuntimeRefresh(runtimeId, providerId?) → Promise<OrbModelCatalog>
//     The bound transport returns the full catalog regardless of providerId
//     (the SDK filters afterwards). ctx.modelRegistry.runtime handles are
//     "context:<extensionId>" and resolve to that extension's live registry.
//   modelRuntimeDispose(runtimeId) → Promise<void>
//     Idempotent on the orb side.
//
//   OrbModelCatalog = {
//     models: Model[],                  // full catalog (Model per pi-ai types)
//     available: Model[],              // models with usable auth, orb order
//     authenticatedProviders: string[] // provider ids with configured auth
//   }
//
// optional (no capability; SDK degrades silently when absent):
//
//   resourceLoaderReload(loader) → Promise<void>
//     `loader` is the SDK DefaultResourceLoader instance (read .options).
//     Bound to the sdk_v1 sdk_resource_reload request; the Go side treats it
//     as a reload/prewarm of the resource set the next session create
//     observes (a no-op success until the session runtime lands).
//   sessionInfoAppend(sessionManager, name) → void
//     Best-effort session naming, called after createAgentSession. The bound
//     transport resolves the manager instance to its session handle; names
//     appended before the session exists cross inside agentSessionCreate as
//     session.sessionInfoNames.
//
// ─────────────────────────────────────────────────────────────────────────────

import { unsupportedError } from "./unsupported.mjs";

let transport = null;

/** Called once by the extension host before any extension code runs. */
export function bindTransport(value) {
	transport = value;
}

// e2e seam: the host's service-protocol fixture (testdata/services.mjs)
// reaches the raw transport through this; no product path reads it.
export function boundTransport() {
	return transport;
}

function hasCapability(capability) {
	if (!transport || !transport.capabilities) return false;
	for (const entry of transport.capabilities) if (entry === capability) return true;
	return false;
}

/**
 * Invoke a required transport method, gated on its capability.
 * Throws the precise OrbUnsupportedCapability diagnostic when the transport is
 * absent, the capability was not negotiated, or the method is missing — this is
 * how future Orbs degrade older hosts deliberately.
 */
export function serviceCall(capability, method, pkg, exportName, supported, args) {
	if (!transport || !hasCapability(capability) || typeof transport[method] !== "function") {
		throw unsupportedError(pkg, exportName, supported, `requires host capability ${capability}`);
	}
	return transport[method](...args);
}

/** Invoke an optional transport method; undefined when unbound. */
export function optionalServiceCall(method, args) {
	if (!transport || typeof transport[method] !== "function") return undefined;
	return transport[method](...args);
}
