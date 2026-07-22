import { createInterface } from "node:readline";
import { pathToFileURL } from "node:url";

const PROTOCOL = "pigo-extension-host";
const VERSION = 1;
const MAX_FRAME_SIZE = 4 * 1024 * 1024;

let nextRequestId = 1;
let agent = {};
let shuttingDown = false;
let finishHandshake;
const handshakeReady = new Promise((resolve) => {
	finishHandshake = resolve;
});
const pending = new Map();
const entries = new Map();
const extensions = new Map();
const hostSections = [];

function registerHostSection(section) {
	hostSections.push(section);
}

async function dispatchHostSectionRequest(frame) {
	for (const section of hostSections) {
		if (typeof section.handleRequest !== "function") continue;
		const result = await section.handleRequest(frame);
		if (result?.handled === true) return result;
	}
	return { handled: false };
}

function dispatchHostSectionEvent(frame) {
	for (const section of hostSections) section.handleEvent?.(frame);
}

function write(frame) {
	const encoded = JSON.stringify({ protocol: PROTOCOL, version: VERSION, ...frame });
	if (Buffer.byteLength(encoded) > MAX_FRAME_SIZE) {
		throw new Error("extension host frame exceeds 4 MiB");
	}
	process.stdout.write(`${encoded}\n`);
}

function request(method, params) {
	if (shuttingDown && method !== "handshake") {
		return Promise.reject(new Error("extension host is shutting down"));
	}
	const id = `host-${nextRequestId++}`;
	return new Promise((resolve, reject) => {
		pending.set(id, { resolve, reject });
		try {
			write({ kind: "request", id, method, params });
		} catch (error) {
			pending.delete(id);
			reject(error);
		}
	});
}

function emit(method, params) {
	write({ kind: "event", method, params });
}

function registerWithPigo(state, method, params) {
	const registration = { method, params };
	if (!state.loaded) {
		state.registrations.push(registration);
		return registration;
	}
	state.registrationTail = state.registrationTail
		.then(() => request(method, params))
		.catch((error) => log("error", [`${method} failed:`, errorValue(error).message], state.id));
	return registration;
}

function errorValue(error) {
	if (error instanceof Error) {
		return { message: error.message, stack: error.stack };
	}
	return { message: String(error) };
}

function log(level, values, extensionId) {
	const message = values
		.map((value) => {
			if (typeof value === "string") return value;
			try {
				return JSON.stringify(value);
			} catch {
				return String(value);
			}
		})
		.join(" ");
	try {
		emit("log", { level, message, ...(extensionId ? { extensionId } : {}) });
	} catch {
		process.stderr.write(`${message}\n`);
	}
}

globalThis.console = Object.freeze({
	debug: (...values) => log("debug", values),
	log: (...values) => log("info", values),
	info: (...values) => log("info", values),
	warn: (...values) => log("warn", values),
	error: (...values) => log("error", values),
});

function makeContext(value = {}, state) {
	const context = {
		cwd: value.cwd ?? agent.cwd ?? process.cwd(),
		mode: value.mode ?? "print",
		hasUI: value.hasUI === true,
	};
	for (const section of hostSections) section.extendContext?.(context, value, state);
	return Object.freeze(context);
}


function restoreSourcePaths(value, state) {
	if (!state.runtimeRoot || !state.sourceRoot) return value;
	if (typeof value === "string") return value.split(state.runtimeRoot).join(state.sourceRoot);
	if (Array.isArray(value)) return value.map((item) => restoreSourcePaths(item, state));
	if (value && typeof value === "object") {
		return Object.fromEntries(Object.entries(value).map(([key, item]) => [key, restoreSourcePaths(item, state)]));
	}
	return value;
}

function serializableTool(tool, state) {
	return {
		name: tool.name,
		label: tool.label ?? "",
		description: restoreSourcePaths(tool.description ?? "", state),
		...(tool.promptSnippet === undefined ? {} : { promptSnippet: restoreSourcePaths(tool.promptSnippet, state) }),
		...(tool.promptGuidelines === undefined ? {} : { promptGuidelines: restoreSourcePaths(tool.promptGuidelines, state) }),
		parameters: restoreSourcePaths(tool.parameters ?? {}, state),
		...(tool.renderShell === undefined ? {} : { renderShell: tool.renderShell }),
		...(tool.executionMode === undefined ? {} : { executionMode: tool.executionMode }),
	};
}

function createAPI(state) {
	const api = {
		registerTool(tool) {
			if (!tool || typeof tool !== "object" || typeof tool.name !== "string" || typeof tool.execute !== "function") {
				throw new TypeError("registerTool requires a named tool with an execute function");
			}
			state.tools.set(tool.name, tool);
			registerWithPigo(state, "register_tool", { extensionId: state.id, definition: serializableTool(tool, state) });
		},
		registerCommand(name, options) {
			if (typeof name !== "string" || !options || typeof options.handler !== "function") {
				throw new TypeError("registerCommand requires a name and handler");
			}
			state.commands.set(name, options);
			registerWithPigo(state, "register_command", {
				extensionId: state.id,
				name,
				options: { description: restoreSourcePaths(options.description ?? "", state) },
			});
		},
		registerShortcut(shortcut, options) {
			if (typeof shortcut !== "string" || shortcut === "" || !options || typeof options.handler !== "function") {
				throw new TypeError("registerShortcut requires a key and handler");
			}
			state.shortcuts.set(shortcut.toLowerCase(), options);
			registerWithPigo(state, "register_shortcut", {
				extensionId: state.id,
				shortcut,
				options: { description: options.description ?? "" },
			});
		},
		on(event, handler) {
			if (typeof event !== "string" || typeof handler !== "function") {
				throw new TypeError("on requires an event name and handler");
			}
			const subscriptionId = `${state.id}-sub-${state.nextSubscriptionId++}`;
			state.subscriptions.set(subscriptionId, handler);
			registerWithPigo(state, "subscribe_event", { extensionId: state.id, subscriptionId, event });
		},
	};
	for (const section of hostSections) section.extendAPI?.(api, state);
	return Object.freeze(api);
}

async function loadExtension(params) {
	const entry = entries.get(params.extensionId);
	if (!entry || entry.path !== params.path) {
		throw Object.assign(new Error(`unknown extension entry ${params.extensionId}`), { code: "invalid_extension" });
	}
	const state = {
		id: entry.id,
		path: entry.path,
		sourceRoot: entry.sourceRoot,
		runtimeRoot: entry.runtimeRoot,
		tools: new Map(),
		commands: new Map(),
		shortcuts: new Map(),
		subscriptions: new Map(),
		registrations: [],
		registrationTail: Promise.resolve(),
		loaded: false,
		nextSubscriptionId: 1,
	};
	try {
		const moduleURL = pathToFileURL(entry.runtimePath ?? entry.path);
		moduleURL.searchParams.set("pigoHostGeneration", `${process.pid}`);
		const imported = await import(moduleURL.href);
		if (typeof imported.default !== "function") {
			throw new Error(`Extension does not export a valid factory function: ${entry.path}`);
		}
		await imported.default(createAPI(state));
		for (const registration of state.registrations) {
			await request(registration.method, registration.params);
		}
		extensions.set(state.id, state);
		state.loaded = true;
		return { extensionId: entry.id, path: entry.path, loaded: true };
	} catch (error) {
		extensions.delete(state.id);
		const detail = errorValue(error);
		throw Object.assign(new Error(`Failed to load extension: ${detail.message}`), {
			code: "extension_load_error",
			data: detail.stack ? { stack: detail.stack } : undefined,
		});
	}
}

async function executeTool(frame) {
	const state = extensions.get(frame.params.extensionId);
	const tool = state?.tools.get(frame.params.toolName);
	if (!tool) throw new Error(`unknown tool ${frame.params.toolName}`);
	const controller = new AbortController();
	const onUpdate = (partial) => emit("tool_update", { requestId: frame.id, partial: partial ?? { content: [] } });
	const result = await tool.execute(
		frame.params.toolCallId,
		frame.params.params,
		controller.signal,
		onUpdate,
		makeContext(frame.params.context, state),
	);
	await state.registrationTail;
	return result ?? { content: [] };
}

async function executeCommand(frame) {
	const state = extensions.get(frame.params.extensionId);
	const command = state?.commands.get(frame.params.commandName);
	if (!command) throw new Error(`unknown command ${frame.params.commandName}`);
	await command.handler(frame.params.arguments ?? "", makeContext(frame.params.context, state));
	await state.registrationTail;
	return { completed: true };
}

async function executeShortcut(frame) {
	const state = extensions.get(frame.params.extensionId);
	const shortcut = state?.shortcuts.get(String(frame.params.shortcut).toLowerCase());
	if (!shortcut) throw new Error(`unknown shortcut ${frame.params.shortcut}`);
	await shortcut.handler(makeContext(frame.params.context, state));
	await state.registrationTail;
	return { completed: true };
}

async function emitEvent(frame) {
	const state = extensions.get(frame.params.extensionId);
	const handler = state?.subscriptions.get(frame.params.subscriptionId);
	if (!handler) throw new Error(`unknown event subscription ${frame.params.subscriptionId}`);
	const context = makeContext(frame.params.context, state);
	const payload = { type: frame.params.event, ...frame.params.payload };
	if (Object.hasOwn(payload, "signal") && context.signal !== undefined) payload.signal = context.signal;
	const value = await handler(payload, context);
	await state.registrationTail;
	return value === undefined ? { payload } : { value, payload };
}

async function handleRequest(frame) {
	try {
		await handshakeReady;
		let result;
		const sectionResult = await dispatchHostSectionRequest(frame);
		if (sectionResult.handled) {
			result = sectionResult.result;
		} else switch (frame.method) {
			case "load_extension":
				result = await loadExtension(frame.params);
				break;
			case "execute_tool":
				result = await executeTool(frame);
				break;
			case "execute_command":
				result = await executeCommand(frame);
				break;
			case "execute_shortcut":
				result = await executeShortcut(frame);
				break;
			case "emit_event":
				result = await emitEvent(frame);
				break;
			case "shutdown":
				shuttingDown = true;
				result = { stopped: true };
				break;
			default:
				throw Object.assign(new Error(`unknown method ${frame.method}`), { code: "method_not_found" });
		}
		write({ kind: "response", id: frame.id, result });
		if (frame.method === "shutdown") {
			setTimeout(() => process.exit(0), 0);
		}
	} catch (error) {
		const detail = errorValue(error);
		write({
			kind: "response",
			id: frame.id,
			error: {
				code: error?.code ?? "extension_error",
				message: detail.message,
				...(error?.data ? { data: error.data } : detail.stack ? { data: { stack: detail.stack } } : {}),
			},
		});
	}
}

function handleFrame(frame) {
	if (frame?.protocol !== PROTOCOL || frame?.version !== VERSION) {
		throw new Error("invalid extension host protocol frame");
	}
	if (frame.kind === "response") {
		const waiter = pending.get(frame.id);
		if (!waiter) return;
		pending.delete(frame.id);
		if (frame.error) {
			const error = Object.assign(new Error(frame.error.message), { code: frame.error.code, data: frame.error.data });
			waiter.reject(error);
		} else {
			waiter.resolve(frame.result);
		}
		return;
	}
	if (frame.kind === "event" && typeof frame.method === "string") {
		dispatchHostSectionEvent(frame);
		return;
	}
	if (frame.kind === "request" && typeof frame.id === "string" && typeof frame.method === "string") {
		void handleRequest(frame);
	}
}

const lines = createInterface({ input: process.stdin, crlfDelay: Infinity });
lines.on("line", (line) => {
	try {
		if (Buffer.byteLength(line) > MAX_FRAME_SIZE) throw new Error("extension host frame exceeds 4 MiB");
		handleFrame(JSON.parse(line));
	} catch (error) {
		process.stderr.write(`${errorValue(error).message}\n`);
		process.exitCode = 1;
		lines.close();
	}
});
lines.on("close", () => {
	for (const waiter of pending.values()) waiter.reject(new Error("pigo closed the extension host transport"));
	pending.clear();
	for (const section of hostSections) section.onClose?.();
});

// ===== SECTION: providers (agent-c) =====
registerHostSection((() => {
	let nextInteractionID = 1;
	const interactionPending = new Map();

	function staticValue(value) {
		return JSON.parse(JSON.stringify(value, (_key, item) => {
			if (typeof item === "function" || typeof item === "undefined") return undefined;
			return item;
		}));
	}

	function retainCallback(state, providerID, method, owner, callback) {
		if (typeof callback !== "function") return undefined;
		const handle = `${state.id}-provider-${state.nextProviderHandle++}`;
		state.providerCallbacks.set(handle, { providerID, method, owner, callback });
		return handle;
	}

	function interactionCall(invocationID, operation, value) {
		const callId = `provider-interaction-${nextInteractionID++}`;
		return new Promise((resolve, reject) => {
			interactionPending.set(callId, { resolve, reject });
			try {
				emit("provider_interaction", { invocationId: invocationID, callId, operation, value });
			} catch (error) {
				interactionPending.delete(callId);
				reject(error);
			}
		});
	}

	function authContext(invocationID) {
		return Object.freeze({
			env: (name) => interactionCall(invocationID, "env", { name }),
			fileExists: (path) => interactionCall(invocationID, "fileExists", { path }),
		});
	}

	function authInteraction(invocationID) {
		const controller = new AbortController();
		return Object.freeze({
			signal: controller.signal,
			prompt: (prompt) => interactionCall(invocationID, "prompt", prompt),
			notify(event) {
				emit("provider_interaction", { invocationId: invocationID, operation: "notify", value: event });
			},
		});
	}

	function registerNativeProvider(state, provider) {
		if (!provider || typeof provider !== "object" || typeof provider.id !== "string" || provider.id === "") {
			throw new TypeError("registerProvider requires a provider with an id");
		}
		if (typeof provider.name !== "string" || typeof provider.getModels !== "function") {
			throw new TypeError(`native provider ${provider.id} requires name and getModels`);
		}
		const models = provider.getModels();
		if (models && typeof models.then === "function") {
			throw new TypeError(`native provider ${provider.id} getModels must return synchronously`);
		}
		const auth = provider.auth;
		if (!auth || (auth.apiKey === undefined && auth.oauth === undefined)) {
			throw new TypeError(`native provider ${provider.id} requires auth`);
		}
		const registration = {
			kind: "native",
			id: provider.id,
			name: provider.name,
			...(provider.baseUrl === undefined ? {} : { baseUrl: provider.baseUrl }),
			...(provider.headers === undefined ? {} : { headers: staticValue(provider.headers) }),
			models: staticValue(Array.from(models ?? [])),
			auth: {},
		};
		if (auth.apiKey !== undefined) {
			if (typeof auth.apiKey.resolve !== "function") {
				throw new TypeError(`native provider ${provider.id} apiKey auth requires resolve`);
			}
			registration.auth.apiKey = {
				name: auth.apiKey.name ?? "",
				resolve: retainCallback(state, provider.id, "apiKey.resolve", auth.apiKey, auth.apiKey.resolve),
				...(typeof auth.apiKey.login === "function" ? { login: retainCallback(state, provider.id, "apiKey.login", auth.apiKey, auth.apiKey.login) } : {}),
				...(typeof auth.apiKey.check === "function" ? { check: retainCallback(state, provider.id, "apiKey.check", auth.apiKey, auth.apiKey.check) } : {}),
			};
		}
		if (auth.oauth !== undefined) {
			for (const name of ["login", "refresh", "toAuth"]) {
				if (typeof auth.oauth[name] !== "function") {
					throw new TypeError(`native provider ${provider.id} oauth auth requires login, refresh, and toAuth`);
				}
			}
			registration.auth.oauth = {
				name: auth.oauth.name ?? "",
				...(auth.oauth.loginLabel === undefined ? {} : { loginLabel: auth.oauth.loginLabel }),
				login: retainCallback(state, provider.id, "oauth.login", auth.oauth, auth.oauth.login),
				refresh: retainCallback(state, provider.id, "oauth.refresh", auth.oauth, auth.oauth.refresh),
				toAuth: retainCallback(state, provider.id, "oauth.toAuth", auth.oauth, auth.oauth.toAuth),
			};
		}
		if (typeof provider.stream === "function") {
			registration.stream = retainCallback(state, provider.id, "stream", provider, provider.stream);
		}
		if (typeof provider.streamSimple === "function") {
			registration.streamSimple = retainCallback(state, provider.id, "streamSimple", provider, provider.streamSimple);
		}
		return registration;
	}

	function registerProviderConfig(state, providerID, config) {
		if (typeof providerID !== "string" || providerID === "" || !config || typeof config !== "object") {
			throw new TypeError("registerProvider requires a provider id and config");
		}
		const defined = {};
		for (const name of ["name", "baseUrl", "apiKey", "api", "headers", "authHeader", "models", "streamSimple"]) {
			if (config[name] !== undefined) defined[name] = true;
		}
		const serializable = {
			...(config.name === undefined ? {} : { name: config.name }),
			...(config.baseUrl === undefined ? {} : { baseUrl: config.baseUrl }),
			...(config.apiKey === undefined ? {} : { apiKey: config.apiKey }),
			...(config.api === undefined ? {} : { api: config.api }),
			...(config.headers === undefined ? {} : { headers: staticValue(config.headers) }),
			...(config.authHeader === undefined ? {} : { authHeader: config.authHeader }),
			...(config.models === undefined ? {} : { models: staticValue(config.models) }),
			defined,
		};
		if (typeof config.streamSimple === "function") {
			serializable.streamSimple = retainCallback(state, providerID, "streamSimple", config, config.streamSimple);
		}
		return { kind: "config", id: providerID, config: serializable };
	}

	function addProviderRegistration(state, providerOrID, config) {
		const provider = typeof providerOrID === "string"
			? registerProviderConfig(state, providerOrID, config)
			: registerNativeProvider(state, providerOrID);
		registerWithPigo(state, "register_provider", { extensionId: state.id, provider });
	}

	async function collectProviderStream(callback, owner, args) {
		const controller = new AbortController();
		const options = args.options === undefined ? undefined : { ...args.options, signal: controller.signal };
		const source = await callback.call(owner, args.model, args.context, options);
		const iterator = typeof source?.[Symbol.asyncIterator] === "function" ? source[Symbol.asyncIterator]() : source;
		if (!iterator || typeof iterator.next !== "function") throw new TypeError("provider stream is not async iterable");
		const events = [];
		while (true) {
			const next = await iterator.next();
			if (next.done) break;
			events.push(next.value);
		}
		return { events };
	}

	async function invokeProvider(frame) {
		const state = extensions.get(frame.params.extensionId);
		const retained = state?.providerCallbacks.get(frame.params.handle);
		if (!retained || retained.providerID !== frame.params.providerId || retained.method !== frame.params.method) {
			throw new Error(`unknown provider callback ${frame.params.providerId}/${frame.params.method}`);
		}
		const args = frame.params.args ?? {};
		let value;
		switch (retained.method) {
			case "apiKey.resolve":
			case "apiKey.check":
				value = await retained.callback.call(retained.owner, {
					ctx: authContext(frame.params.invocationId),
					...(args.credential === undefined ? {} : { credential: args.credential }),
				});
				break;
			case "apiKey.login":
			case "oauth.login":
				value = await retained.callback.call(retained.owner, authInteraction(frame.params.invocationId));
				break;
			case "oauth.refresh": {
				const controller = new AbortController();
				value = await retained.callback.call(retained.owner, args.credential, controller.signal);
				break;
			}
			case "oauth.toAuth":
				value = await retained.callback.call(retained.owner, args.credential);
				break;
			case "stream":
			case "streamSimple":
				value = await collectProviderStream(retained.callback, retained.owner, args);
				break;
			default:
				throw new Error(`unsupported provider callback ${retained.method}`);
		}
		return value === undefined ? { present: false } : { present: true, value };
	}

	return {
		extendAPI(api, state) {
			state.providerCallbacks = new Map();
			state.nextProviderHandle = 1;
			api.registerProvider = (providerOrID, config) => addProviderRegistration(state, providerOrID, config);
		},
		async handleRequest(frame) {
			if (frame.method !== "provider_invoke") return { handled: false };
			return { handled: true, result: await invokeProvider(frame) };
		},
		handleEvent(frame) {
			if (frame.method !== "provider_interaction_result") return;
			const waiter = interactionPending.get(frame.params.callId);
			if (!waiter) return;
			interactionPending.delete(frame.params.callId);
			if (frame.params.error) {
				waiter.reject(Object.assign(new Error(frame.params.error.message), { code: frame.params.error.code }));
			} else {
				waiter.resolve(frame.params.present ? frame.params.value : undefined);
			}
		},
		onClose() {
			for (const waiter of interactionPending.values()) waiter.reject(new Error("provider interaction transport closed"));
			interactionPending.clear();
		},
	};
})());
// ===== END SECTION =====

// ===== SECTION: ui (agent-d) =====
registerHostSection((() => {
	const themeMarker = "\u0000pigo-theme-text\u0000";
	const factories = new Map();
	const components = new Map();
	const handlers = new Map();
	const customCalls = new Map();
	const autocompleteProviders = new Map();
	const rendererComponents = new Map();
	let nextRendererComponentID = 1;

	function nextHandle(state, kind) {
		const handle = `${state.id}-ui-${kind}-${state.nextUIHandle++}`;
		return handle;
	}

	function retainFactory(state, kind, factory, context) {
		if (typeof factory !== "function") return undefined;
		const handle = nextHandle(state, kind);
		factories.set(handle, { state, kind, factory, context });
		return handle;
	}

	function sendUIEvent(context, method, fields = {}) {
		emit("ui_request", {
			extensionId: context.state.id,
			contextId: context.value.uiContextId,
			method,
			...fields,
		});
	}

	function sendUIRequest(context, method, fields = {}) {
		return request("ui_request", {
			extensionId: context.state.id,
			contextId: context.value.uiContextId,
			method,
			...fields,
		});
	}

	function applyTheme(rendered, text) {
		return String(rendered ?? themeMarker).split(themeMarker).join(String(text));
	}

	function createTheme(snapshot, name) {
		const value = snapshot ?? {};
		const theme = {
			fg: (color, text) => applyTheme(value.fg?.[color], text),
			bg: (color, text) => applyTheme(value.bg?.[color], text),
			bold: (text) => applyTheme(value.bold, text),
			italic: (text) => applyTheme(value.italic, text),
			underline: (text) => applyTheme(value.underline, text),
			inverse: (text) => applyTheme(value.inverse, text),
			strikethrough: (text) => applyTheme(value.strikethrough, text),
			getFgAnsi: (color) => value.fgAnsi?.[color] ?? "",
			getBgAnsi: (color) => value.bgAnsi?.[color] ?? "",
			getColorMode: () => value.colorMode ?? "",
			getThinkingBorderColor: (level) => (text) => applyTheme(value.thinkingBorder?.[level], text),
			getBashModeBorderColor: () => (text) => applyTheme(value.bashModeBorder, text),
		};
		Object.defineProperty(theme, "__pigoThemeName", { value: name, enumerable: false });
		Object.defineProperty(theme, "__pigoHostTheme", { value: true, enumerable: false });
		return Object.freeze(theme);
	}

	function registerRenderer(state, kind, customType, renderer) {
		if (typeof customType !== "string" || customType === "" || typeof renderer !== "function") {
			throw new TypeError(`register${kind === "message" ? "Message" : "Entry"}Renderer requires a custom type and renderer`);
		}
		state.renderers.set(`${kind}:${customType}`, renderer);
		registerWithPigo(state, "register_renderer", { extensionId: state.id, kind, customType });
	}

	function createRegisteredRendererComponent(params) {
		const state = extensions.get(params.extensionId);
		const renderer = state?.renderers.get(`${params.kind}:${params.customType}`);
		if (!renderer) throw new Error(`unknown ${params.kind} renderer ${params.customType}`);
		const component = renderer(params.value, { expanded: params.expanded === true }, createTheme(params.theme));
		if (component && typeof component.then === "function") throw new TypeError("extension renderer must return synchronously");
		if (component == null) return { present: false };
		if (typeof component.render !== "function") throw new TypeError("extension renderer component has no render function");
		const handle = `${state.id}-renderer-${nextRendererComponentID++}`;
		rendererComponents.set(handle, component);
		return { present: true, handle };
	}

	function renderRegisteredRendererComponent(params) {
		const component = rendererComponents.get(params.handle);
		if (!component) throw new Error(`unknown renderer component ${params.handle}`);
		const lines = component.render(params.width);
		if (lines && typeof lines.then === "function") throw new TypeError("extension component render must return synchronously");
		if (!Array.isArray(lines) || !lines.every((line) => typeof line === "string")) {
			throw new TypeError("extension component render must return string[]");
		}
		return { lines };
	}

	function disposeRegisteredRendererComponent(params) {
		const component = rendererComponents.get(params.handle);
		rendererComponents.delete(params.handle);
		try { component?.dispose?.(); } catch { /* ignore dispose errors */ }
		return { disposed: component !== undefined };
	}

	function createKeybindings(snapshot) {
		const resolved = snapshot?.resolved ?? {};
		return Object.freeze({
			matches(input, binding) {
				return Array.isArray(resolved[binding]) && resolved[binding].includes(input);
			},
			keys(binding) {
				return Array.isArray(resolved[binding]) ? [...resolved[binding]] : [];
			},
		});
	}

	function createFooterData(snapshot) {
		const statuses = Object.entries(snapshot?.statuses ?? {});
		return Object.freeze({
			getGitBranch: () => snapshot?.gitBranch || null,
			getExtensionStatuses: () => new Map(statuses),
			getAvailableProviderCount: () => 0,
			onBranchChange: () => () => {},
		});
	}

	function createTUI(record, width, height) {
		return Object.freeze({
			requestRender() {
				sendUIEvent(record.context, "component_request_render", { componentHandle: record.handle });
				scheduleRender(record, record.lastWidth || width);
			},
			terminal: Object.freeze({ columns: width || 0, rows: height || 0 }),
		});
	}

	function staticOverlayOptions(options) {
		if (options === undefined) return undefined;
		const resolved = typeof options === "function" ? options() : options;
		if (!resolved || typeof resolved !== "object") return undefined;
		const copy = {};
		for (const name of ["width", "minWidth", "maxHeight", "anchor", "offsetX", "offsetY", "row", "col", "margin", "nonCapturing"]) {
			if (resolved[name] !== undefined) copy[name] = resolved[name];
		}
		if (typeof resolved.visible === "function") {
			copy.visible = Boolean(resolved.visible(process.stdout.columns ?? 80, process.stdout.rows ?? 24));
		}
		return copy;
	}

	function createOverlayHandle(record) {
		let hidden = false;
		let focused = true;
		const action = (name, fields = {}) => sendUIEvent(record.context, "overlay_action", {
			componentHandle: record.handle,
			action: name,
			...fields,
		});
		return Object.freeze({
			hide() { hidden = true; action("hide"); },
			setHidden(value) { hidden = Boolean(value); action("setHidden", { visible: hidden }); },
			isHidden: () => hidden,
			focus() { focused = true; action("focus"); },
			unfocus() { focused = false; action("unfocus"); },
			isFocused: () => focused,
		});
	}

	function createAutocompleteProvider(snapshot) {
		return Object.freeze({
			triggerCharacters: [...(snapshot?.triggerCharacters ?? [])],
			getSuggestions: async () => null,
			applyCompletion(lines, cursorLine, cursorCol) { return { lines, cursorLine, cursorCol }; },
			shouldTriggerFileCompletion: () => false,
		});
	}

	function componentText(component) {
		return typeof component?.getText === "function" ? String(component.getText()) : undefined;
	}

	function pushRender(record, width) {
		if (!record || record.disposed) return Promise.resolve();
		let rendered;
		try {
			rendered = record.component.render(width);
			if (rendered && typeof rendered.then === "function") {
				throw new TypeError("extension component render must return synchronously");
			}
			if (!Array.isArray(rendered) || !rendered.every((line) => typeof line === "string")) {
				throw new TypeError("extension component render must return string[]");
			}
			const text = componentText(record.component);
			emit("ui_component_render", {
				componentHandle: record.handle,
				lines: rendered,
				width,
				...(text === undefined ? {} : { text }),
			});
		} catch (error) {
			log("error", [`UI component ${record.handle} render failed:`, errorValue(error).message], record.context.state.id);
		}
		return Promise.resolve();
	}

	function scheduleRender(record, width) {
		if (!record || record.disposed) return;
		record.lastWidth = width || record.lastWidth || 80;
		if (record.renderScheduled) return;
		record.renderScheduled = true;
		queueMicrotask(() => {
			record.renderScheduled = false;
			void pushRender(record, record.lastWidth);
		});
	}

	async function mountComponent(params) {
		const retained = factories.get(params.factoryHandle);
		if (!retained) throw new Error(`unknown UI factory ${params.factoryHandle}`);
		const record = {
			handle: params.componentHandle,
			factoryHandle: params.factoryHandle,
			context: retained.context,
			kind: params.kind,
			lastWidth: params.width || 80,
			renderScheduled: false,
			disposed: false,
		};
		const tui = createTUI(record, params.width, params.height);
		const theme = createTheme(params.theme);
		const keybindings = createKeybindings(params.keybindings);
		let component;
		if (retained.kind === "custom") {
			const done = (value) => sendUIEvent(retained.context, "custom_done", {
				componentHandle: params.componentHandle,
				...(value === undefined ? {} : { value }),
			});
			component = await retained.factory(tui, theme, keybindings, done);
		} else if (retained.kind === "footer") {
			component = retained.factory(tui, theme, createFooterData(params.footerData));
		} else if (retained.kind === "editor") {
			component = retained.factory(tui, theme, keybindings);
		} else {
			component = retained.factory(tui, theme);
		}
		if (!component || typeof component.render !== "function") {
			throw new TypeError("extension component has no render function");
		}
		if (retained.kind === "editor" && (
			typeof component.getText !== "function"
			|| typeof component.setText !== "function"
			|| typeof component.handleInput !== "function"
		)) {
			throw new TypeError("editor component requires getText, setText, and handleInput");
		}
		record.component = component;
		components.set(record.handle, record);
		scheduleRender(record, record.lastWidth);
		const text = componentText(component);
		return {
			...(text === undefined ? {} : { text }),
			handlesInput: typeof component.handleInput === "function",
			tracksFocus: "focused" in component,
			wantsKeyRelease: component.wantsKeyRelease === true,
		};
	}

	async function processComponentEvent(params) {
		if (params.event === "mount") return mountComponent(params);
		if (params.event === "terminal_input") {
			const handler = handlers.get(params.componentHandle);
			if (!handler) return {};
			const result = handler.callback(params.data);
			return result === undefined ? {} : { terminalResult: result };
		}
		const record = components.get(params.componentHandle);
		if (!record) return {};
		switch (params.event) {
			case "render":
				scheduleRender(record, params.width);
				break;
			case "input":
				if (typeof record.component.handleInput === "function") record.component.handleInput(params.data);
				scheduleRender(record, record.lastWidth);
				break;
			case "focus":
				if ("focused" in record.component) record.component.focused = params.focused === true;
				scheduleRender(record, record.lastWidth);
				break;
			case "set_text":
				if (typeof record.component.setText === "function") record.component.setText(params.text ?? "");
				scheduleRender(record, record.lastWidth);
				break;
			case "set_autocomplete_provider":
				if (typeof record.component.setAutocompleteProvider === "function") {
					record.component.setAutocompleteProvider(createAutocompleteProvider(params.provider));
				}
				break;
			case "dispose":
				record.disposed = true;
				components.delete(record.handle);
				if (record.kind === "custom") factories.delete(record.factoryHandle);
				try { record.component.dispose?.(); } catch { /* ignore dispose errors */ }
				break;
			case "overlay_handle": {
				const custom = customCalls.get(record.handle);
				custom?.options?.onHandle?.(createOverlayHandle(record));
				break;
			}
		}
		const text = componentText(record.component);
		return text === undefined ? {} : { text };
	}

	function autocompleteRequest(params) {
		return {
			lines: [...(params.lines ?? [])],
			cursorLine: params.cursorLine ?? 0,
			cursorCol: params.cursorCol ?? 0,
			signal: new AbortController().signal,
			force: params.force === true,
		};
	}

	async function processAutocomplete(params) {
		if (params.factoryHandle) {
			const retained = factories.get(params.factoryHandle);
			if (!retained || retained.kind !== "autocomplete") throw new Error(`unknown autocomplete factory ${params.factoryHandle}`);
			const provider = retained.factory(createAutocompleteProvider(params.current));
			if (provider && typeof provider.then === "function") throw new TypeError("autocomplete provider factory must return synchronously");
			if (!provider || typeof provider !== "object") throw new TypeError("autocomplete provider factory returned no provider");
			autocompleteProviders.set(params.providerHandle, provider);
			return { triggerCharacters: [...(provider.triggerCharacters ?? [])] };
		}
		const provider = autocompleteProviders.get(params.providerHandle);
		if (!provider) throw new Error(`unknown autocomplete provider ${params.providerHandle}`);
		const value = autocompleteRequest(params);
		switch (params.operation) {
			case "getSuggestions": {
				const result = await provider.getSuggestions(value.lines, value.cursorLine, value.cursorCol, { signal: value.signal, force: value.force });
				if (result == null) return { present: false };
				return { present: true, prefix: result.prefix, items: result.items ?? [] };
			}
			case "applyCompletion": {
				const result = provider.applyCompletion(value.lines, value.cursorLine, value.cursorCol, params.item, params.prefix ?? "");
				if (result && typeof result.then === "function") throw new TypeError("autocomplete applyCompletion must return synchronously");
				return result;
			}
			case "shouldTriggerFileCompletion":
				return { triggered: typeof provider.shouldTriggerFileCompletion === "function" && provider.shouldTriggerFileCompletion(value.lines, value.cursorLine, value.cursorCol) === true };
			default:
				throw new Error(`unknown autocomplete operation ${params.operation}`);
		}
	}

	function startUIRequest(context, method, fields) {
		if (shuttingDown) return { id: "", promise: Promise.reject(new Error("extension host is shutting down")) };
		const id = `host-${nextRequestId++}`;
		const promise = new Promise((resolve, reject) => {
			pending.set(id, { resolve, reject });
			try {
				write({
					kind: "request",
					id,
					method: "ui_request",
					params: {
						extensionId: context.state.id,
						contextId: context.value.uiContextId,
						method,
						...fields,
					},
				});
			} catch (error) {
				pending.delete(id);
				reject(error);
			}
		});
		return { id, promise };
	}

	function dialog(context, method, fields, options, fallback, decode) {
		if (options?.signal?.aborted) return Promise.resolve(fallback);
		const pendingDialog = startUIRequest(context, method, {
			...fields,
			...(options?.timeout === undefined ? {} : { timeout: options.timeout }),
		});
		const onAbort = () => sendUIEvent(context, "cancelDialog", { requestId: pendingDialog.id });
		options?.signal?.addEventListener("abort", onAbort, { once: true });
		return pendingDialog.promise
			.then((response) => response?.cancelled ? fallback : decode(response))
			.catch((error) => error?.code === "ui_cancelled" ? fallback : Promise.reject(error))
			.finally(() => options?.signal?.removeEventListener("abort", onAbort));
	}

	function createUI(value, state) {
		const context = { value, state };
		const snapshot = value.ui ?? { editorText: "", toolsExpanded: false, themes: [] };
		const themeByName = new Map((snapshot.themes ?? []).map((entry) => [entry.name, entry]));
		const ui = {
			notify(message, type) { sendUIEvent(context, "notify", { message: String(message), ...(type === undefined ? {} : { notifyType: type }) }); },
			select(title, options, dialogOptions) {
				return dialog(context, "select", { title: String(title), options: Array.from(options, String) }, dialogOptions, undefined, (response) => response?.value);
			},
			confirm(title, message, dialogOptions) {
				return dialog(context, "confirm", { title: String(title), message: String(message) }, dialogOptions, false, (response) => Boolean(response?.confirmed));
			},
			input(title, placeholder, dialogOptions) {
				return dialog(context, "input", { title: String(title), ...(placeholder === undefined ? {} : { placeholder: String(placeholder) }) }, dialogOptions, undefined, (response) => response?.value);
			},
			editor(title, prefill) {
				return dialog(context, "editor", { title: String(title), ...(prefill === undefined ? {} : { prefill: String(prefill) }) }, undefined, undefined, (response) => response?.value);
			},
			onTerminalInput(handler) {
				if (typeof handler !== "function") throw new TypeError("terminal input handler is not a function");
				const handlerHandle = nextHandle(state, "terminal");
				handlers.set(handlerHandle, { callback: handler, context });
				sendUIEvent(context, "onTerminalInput", { handlerHandle });
				return () => {
					handlers.delete(handlerHandle);
					sendUIEvent(context, "unsubscribeTerminalInput", { handlerHandle });
				};
			},
			setStatus(statusKey, statusText) { sendUIEvent(context, "setStatus", { statusKey: String(statusKey), ...(statusText === undefined ? {} : { statusText: String(statusText) }) }); },
			setWorkingMessage(text) { sendUIEvent(context, "setWorkingMessage", text === undefined ? {} : { text: String(text) }); },
			setWorkingVisible(visible) { sendUIEvent(context, "setWorkingVisible", { visible: Boolean(visible) }); },
			setWorkingIndicator(workingIndicator) { sendUIEvent(context, "setWorkingIndicator", workingIndicator === undefined ? {} : { workingIndicator }); },
			setHiddenThinkingLabel(text) { sendUIEvent(context, "setHiddenThinkingLabel", text === undefined ? {} : { text: String(text) }); },
			setWidget(widgetKey, content, options) {
				const fields = { widgetKey: String(widgetKey), ...(options?.placement === undefined ? {} : { widgetPlacement: options.placement }) };
				if (typeof content === "function") fields.factoryHandle = retainFactory(state, "widget", content, context);
				else if (content !== undefined) fields.widgetLines = Array.from(content, String);
				sendUIEvent(context, "setWidget", fields);
			},
			setFooter(factory) { sendUIEvent(context, "setFooter", typeof factory === "function" ? { factoryHandle: retainFactory(state, "footer", factory, context) } : {}); },
			setHeader(factory) { sendUIEvent(context, "setHeader", typeof factory === "function" ? { factoryHandle: retainFactory(state, "header", factory, context) } : {}); },
			setTitle(title) { sendUIEvent(context, "setTitle", { title: String(title) }); },
			async custom(factory, options) {
				if (typeof factory !== "function") throw new TypeError("custom component factory is not a function");
				const factoryHandle = retainFactory(state, "custom", factory, context);
				const componentHandle = nextHandle(state, "component");
				const overlayOptions = staticOverlayOptions(options?.overlayOptions);
				customCalls.set(componentHandle, { options, context, factoryHandle });
				const response = await sendUIRequest(context, "custom", {
					factoryHandle,
					componentHandle,
					customOptions: {
						overlay: options?.overlay === true,
						...(overlayOptions === undefined ? {} : { overlayOptions }),
						onHandle: typeof options?.onHandle === "function",
					},
				});
				customCalls.delete(componentHandle);
				return response?.cancelled ? undefined : response?.value;
			},
			pasteToEditor(text) { sendUIEvent(context, "pasteToEditor", { text: String(text) }); },
			setEditorText(text) { snapshot.editorText = String(text); sendUIEvent(context, "setEditorText", { text: snapshot.editorText }); },
			getEditorText: () => snapshot.editorText ?? "",
			addAutocompleteProvider(factory) {
				if (typeof factory !== "function") throw new TypeError("autocomplete provider factory is not a function");
				sendUIEvent(context, "addAutocompleteProvider", { factoryHandle: retainFactory(state, "autocomplete", factory, context) });
			},
			setEditorComponent(factory) {
				state.uiEditorFactory = typeof factory === "function" ? factory : undefined;
				sendUIEvent(context, "setEditorComponent", state.uiEditorFactory ? { factoryHandle: retainFactory(state, "editor", state.uiEditorFactory, context) } : {});
			},
			getEditorComponent: () => state.uiEditorFactory,
			getAllThemes: () => (snapshot.themes ?? []).map(({ name, path }) => ({ name, path })),
			getTheme(name) {
				const found = themeByName.get(String(name));
				return found?.theme ? createTheme(found.theme, found.name) : undefined;
			},
			setTheme(theme) {
				const name = typeof theme === "string" ? theme : theme?.__pigoThemeName;
				if (!name && theme?.__pigoHostTheme === true) return { success: true };
				if (!name || !themeByName.has(name)) return { success: false, error: `Theme not found: ${name ?? "unknown"}` };
				snapshot.theme = themeByName.get(name).theme;
				sendUIEvent(context, "setTheme", { themeName: name });
				return { success: true };
			},
			getToolsExpanded: () => snapshot.toolsExpanded === true,
			setToolsExpanded(expanded) { snapshot.toolsExpanded = Boolean(expanded); sendUIEvent(context, "setToolsExpanded", { expanded: snapshot.toolsExpanded }); },
		};
		Object.defineProperty(ui, "theme", { enumerable: true, get: () => createTheme(snapshot.theme) });
		return Object.freeze(ui);
	}

	return {
		extendAPI(api, state) {
			state.nextUIHandle = 1;
			state.uiEditorFactory = undefined;
			state.renderers = new Map();
			api.registerMessageRenderer = (customType, renderer) => registerRenderer(state, "message", customType, renderer);
			api.registerEntryRenderer = (customType, renderer) => registerRenderer(state, "entry", customType, renderer);
		},
		extendContext(context, value, state) {
			context.ui = createUI(value, state);
		},
		async handleRequest(frame) {
			if (frame.method === "ui_component_event") return { handled: true, result: await processComponentEvent(frame.params) };
			if (frame.method === "ui_autocomplete") return { handled: true, result: await processAutocomplete(frame.params) };
			if (frame.method === "create_registered_renderer_component") return { handled: true, result: createRegisteredRendererComponent(frame.params) };
			if (frame.method === "render_registered_renderer_component") return { handled: true, result: renderRegisteredRendererComponent(frame.params) };
			if (frame.method === "dispose_registered_renderer_component") return { handled: true, result: disposeRegisteredRendererComponent(frame.params) };
			return { handled: false };
		},
		handleEvent(frame) {
			if (frame.method !== "ui_component_event") return;
			void processComponentEvent(frame.params);
		},
		onClose() {
			factories.clear();
			components.clear();
			handlers.clear();
			customCalls.clear();
			autocompleteProviders.clear();
			rendererComponents.clear();
		},
	};
})());
// ===== END SECTION =====

// ===== SECTION: state (agent-e) =====
registerHostSection((() => {
	let baseSnapshot = normalizeSnapshot();

	function clone(value) {
		if (value === undefined) return undefined;
		return structuredClone(value);
	}

	function normalizeSnapshot(value = {}) {
		return {
			...clone(value),
			flags: { ...(value.flags ?? {}) },
			sessionName: value.sessionName ?? null,
			activeTools: [...(value.activeTools ?? [])],
			allTools: clone(value.allTools ?? []),
			commands: clone(value.commands ?? []),
			thinkingLevel: value.thinkingLevel ?? "off",
			context: {
				cwd: value.context?.cwd ?? agent.cwd ?? process.cwd(),
				mode: value.context?.mode ?? "print",
				hasUI: value.context?.hasUI === true,
				model: clone(value.context?.model),
				idle: value.context?.idle !== false,
				projectTrusted: value.context?.projectTrusted !== false,
				hasPendingMessages: value.context?.hasPendingMessages === true,
				contextUsage: clone(value.context?.contextUsage),
				systemPrompt: value.context?.systemPrompt ?? "",
			},
			session: value.session == null ? null : clone(value.session),
			modelRegistry: value.modelRegistry == null ? null : clone(value.modelRegistry),
		};
	}

	function reportAsyncError(state, operation, error) {
		log("error", [`${operation} failed:`, errorValue(error).message], state.id);
	}

	function stateAction(state, action, args = {}) {
		const invoke = () => request("state_action", { extensionId: state.id, action, args });
		const pendingAction = state.stateActionTail.then(invoke, invoke);
		state.stateActionTail = pendingAction.then(() => undefined, () => undefined);
		return pendingAction;
	}

	function fireStateAction(state, action, args = {}) {
		const pendingAction = stateAction(state, action, args);
		pendingAction.catch((error) => reportAsyncError(state, action, error));
		return pendingAction;
	}

	function registeredFlagValue(state, name) {
		const definition = state.stateFlags.get(name);
		if (!definition) return undefined;
		if (Object.hasOwn(state.stateSnapshot.flags, name)) return clone(state.stateSnapshot.flags[name]);
		return clone(definition.default);
	}

	function invokeBusHandler(state, handler, data) {
		try {
			const result = handler(data);
			if (result && typeof result.then === "function") {
				result.catch((error) => reportAsyncError(state, "event handler", error));
			}
		} catch (error) {
			reportAsyncError(state, "event handler", error);
		}
	}

	function createEventBus(state) {
		return Object.freeze({
			on(channel, handler) {
				if (typeof channel !== "string" || channel === "" || typeof handler !== "function") {
					throw new TypeError("events.on requires a channel and handler");
				}
				const subscriptionId = `${state.id}-bus-${state.nextStateBusID++}`;
				state.stateBus.set(subscriptionId, { channel, handler });
				const registration = registerWithPigo(state, "event_bus_subscribe", {
					extensionId: state.id, subscriptionId, channel,
				});
				let active = true;
				return () => {
					if (!active) return;
					active = false;
					state.stateBus.delete(subscriptionId);
					const pendingIndex = state.registrations.indexOf(registration);
					if (pendingIndex >= 0) state.registrations.splice(pendingIndex, 1);
					else request("event_bus_unsubscribe", { extensionId: state.id, subscriptionId })
						.catch((error) => reportAsyncError(state, "events.unsubscribe", error));
				};
			},
			emit(channel, data) {
				if (typeof channel !== "string" || channel === "") throw new TypeError("events.emit requires a channel");
				for (const subscription of state.stateBus.values()) {
					if (subscription.channel === channel) invokeBusHandler(state, subscription.handler, data);
				}
				request("event_bus_emit", { extensionId: state.id, channel, data })
					.catch((error) => reportAsyncError(state, "events.emit", error));
			},
		});
	}

	function sessionSnapshot(state) {
		return state.stateSnapshot.session;
	}

	function sessionEntry(state, id) {
		return sessionSnapshot(state)?.entries?.find((entry) => entry.id === id);
	}

	function createSessionManager(state) {
		return Object.freeze({
			isPersisted: () => sessionSnapshot(state)?.persisted === true,
			getCwd: () => sessionSnapshot(state)?.cwd ?? state.stateSnapshot.context.cwd,
			getSessionDir: () => sessionSnapshot(state)?.sessionDir ?? "",
			getSessionId: () => sessionSnapshot(state)?.sessionId ?? "",
			getSessionFile: () => sessionSnapshot(state)?.sessionFile ?? undefined,
			getLeafId: () => sessionSnapshot(state)?.leafId ?? null,
			getLeafEntry: () => clone(sessionEntry(state, sessionSnapshot(state)?.leafId)),
			getEntry: (id) => clone(sessionEntry(state, id)),
			getEntries: () => clone(sessionSnapshot(state)?.entries ?? []),
			getHeader: () => clone(sessionSnapshot(state)?.header ?? null),
			getSessionName: () => sessionSnapshot(state)?.sessionName ?? undefined,
			getLabel: (id) => sessionSnapshot(state)?.labels?.[id] ?? undefined,
			getChildren(parentId) {
				return clone((sessionSnapshot(state)?.entries ?? []).filter((entry) => (entry.parentId ?? null) === (parentId ?? null)));
			},
			getBranch(fromId) {
				let current = fromId ?? sessionSnapshot(state)?.leafId;
				const branch = [];
				const seen = new Set();
				while (current != null && !seen.has(current)) {
					seen.add(current);
					const entry = sessionEntry(state, current);
					if (!entry) break;
					branch.push(entry);
					current = entry.parentId ?? null;
				}
				return clone(branch.reverse());
			},
			getTree: () => clone(sessionSnapshot(state)?.tree ?? []),
			buildContextEntries: () => clone(sessionSnapshot(state)?.contextEntries ?? []),
			buildSessionContext: () => clone(sessionSnapshot(state)?.sessionContext ?? {
				messages: [],
				thinkingLevel: state.stateSnapshot.thinkingLevel,
				model: state.stateSnapshot.context.model,
				activeToolNames: state.stateSnapshot.activeTools,
			}),
		});
	}

	function providerName(value) {
		return typeof value === "string" ? value : value?.provider;
	}

	function providerView(state, name) {
		const snapshot = state.stateSnapshot.modelRegistry;
		const provider = snapshot?.providers?.[name];
		if (!provider) return undefined;
		return Object.freeze({
			id: provider.id,
			name: provider.name,
			...(provider.baseUrl ? { baseUrl: provider.baseUrl } : {}),
			...(provider.headers ? { headers: clone(provider.headers) } : {}),
			getModels: () => clone((snapshot.all ?? []).filter((model) => model.provider === name)),
		});
	}

	function createModelRegistry(state) {
		return Object.freeze({
			refresh: async () => {},
			getError: () => state.stateSnapshot.modelRegistry?.error || undefined,
			getAll: () => clone(state.stateSnapshot.modelRegistry?.all ?? []),
			getAvailable: () => clone(state.stateSnapshot.modelRegistry?.available ?? []),
			find(provider, id) {
				return clone((state.stateSnapshot.modelRegistry?.all ?? []).find((model) => model.provider === provider && model.id === id));
			},
			hasConfiguredAuth(value) {
				return state.stateSnapshot.modelRegistry?.providers?.[providerName(value)]?.authStatus?.configured === true;
			},
			getProviderAuthStatus(provider) {
				return clone(state.stateSnapshot.modelRegistry?.providers?.[provider]?.authStatus ?? { configured: false });
			},
			async getApiKeyAndHeaders() {
				return { ok: false, error: "credentials are unavailable in the extension host snapshot" };
			},
			getApiKeyForProvider: async () => undefined,
			getProviderAuth: async () => undefined,
			getProvider: (provider) => providerView(state, provider),
			getProviderDisplayName(provider) {
				return state.stateSnapshot.modelRegistry?.providers?.[provider]?.displayName ?? provider;
			},
			isUsingOAuth(value) {
				return state.stateSnapshot.modelRegistry?.providers?.[providerName(value)]?.usingOAuth === true;
			},
			getRegisteredProviderConfig(provider) {
				return state.stateSnapshot.modelRegistry?.providers?.[provider]?.registeredConfig ? providerView(state, provider) : undefined;
			},
			getRegisteredNativeProvider(provider) {
				return state.stateSnapshot.modelRegistry?.providers?.[provider]?.registeredNative ? providerView(state, provider) : undefined;
			},
			getRegisteredProviderIds: () => [...(state.stateSnapshot.modelRegistry?.registeredProviderIds ?? [])],
		});
	}

	function compact(state, options) {
		const compacting = stateAction(state, "compact", { customInstructions: options?.customInstructions ?? "" });
		compacting
			.then((result) => options?.onComplete?.(result))
			.catch((error) => {
				if (typeof options?.onError === "function") options.onError(error);
				else reportAsyncError(state, "compact", error);
			});
	}

	function extendContext(context, value, state) {
		const current = state.stateSnapshot.context;
		context.model = clone(current.model);
		const signalValue = value.signal;
		if (signalValue?.id) {
			const controller = new AbortController();
			if (signalValue.aborted) controller.abort(signalValue.reason || undefined);
			state.contextSignals.set(signalValue.id, controller);
			context.signal = controller.signal;
		} else {
			context.signal = undefined;
		}
		context.sessionManager = createSessionManager(state);
		context.modelRegistry = createModelRegistry(state);
		context.isIdle = () => state.stateSnapshot.context.idle === true;
		context.isProjectTrusted = () => state.stateSnapshot.context.projectTrusted === true;
		context.abort = () => { void fireStateAction(state, "abort"); };
		context.hasPendingMessages = () => state.stateSnapshot.context.hasPendingMessages === true;
		context.shutdown = () => { void fireStateAction(state, "shutdown"); };
		context.getContextUsage = () => clone(state.stateSnapshot.context.contextUsage);
		context.compact = (options) => compact(state, options);
		context.getSystemPrompt = () => state.stateSnapshot.context.systemPrompt ?? "";
		if (value.cwd === undefined) context.cwd = current.cwd;
		if (value.mode === undefined) context.mode = current.mode;
		if (value.hasUI === undefined) context.hasUI = current.hasUI === true;
	}

	function retainBashOperations(state, value) {
		if (!value || typeof value !== "object" || typeof value.operations?.exec !== "function") return value;
		const operationId = `${state.id}-bash-${state.nextBashOperationID++}`;
		state.bashOperations.set(operationId, value.operations.exec.bind(value.operations));
		return { ...value, operations: { hostOperationId: operationId } };
	}

	function normalizeEventResult(state, event, payload, value) {
		if (event === "context" && (value == null || typeof value !== "object" || !Object.hasOwn(value, "messages"))) {
			return { messages: payload.messages };
		}
		if (event === "user_bash") return retainBashOperations(state, value);
		return value;
	}

	function executeBashOperation(frame) {
		const state = extensions.get(frame.params.extensionId);
		const operation = state?.bashOperations.get(frame.params.operationId);
		if (!operation) throw new Error(`unknown bash operation ${frame.params.operationId}`);
		const controller = new AbortController();
		const options = {
			signal: controller.signal,
			...(frame.params.timeout === undefined ? {} : { timeout: frame.params.timeout }),
			...(frame.params.env === undefined ? {} : { env: frame.params.env }),
			onData(data) {
				emit("tool_update", { requestId: frame.id, partial: { data: Buffer.from(data).toString("base64") } });
			},
		};
		return operation(frame.params.command, frame.params.cwd, options);
	}

	function extendAPI(api, state) {
		state.stateSnapshot = normalizeSnapshot(baseSnapshot);
		state.stateActionTail = Promise.resolve();
		state.stateFlags = new Map();
		state.stateBus = new Map();
		state.nextStateBusID = 1;
		state.nextExecOperationID = 1;
		state.bashOperations = new Map();
		state.nextBashOperationID = 1;
		state.contextSignals = new Map();

		const registerOn = api.on.bind(api);
		api.on = (event, handler) => {
			if (typeof handler !== "function") throw new TypeError("on requires an event name and handler");
			registerOn(event, async (payload, context) => normalizeEventResult(state, event, payload, await handler(payload, context)));
		};
		api.registerFlag = (name, definition) => {
			if (typeof name !== "string" || name === "" || !definition || !["boolean", "string"].includes(definition.type)) {
				throw new TypeError("registerFlag requires a name and boolean or string definition");
			}
			const copied = {
				name,
				description: definition.description ?? "",
				type: definition.type,
				...(definition.default === undefined ? {} : { default: clone(definition.default) }),
			};
			state.stateFlags.set(name, copied);
			if (!Object.hasOwn(state.stateSnapshot.flags, name) && definition.default !== undefined) {
				state.stateSnapshot.flags[name] = clone(definition.default);
			}
			registerWithPigo(state, "register_flag", { extensionId: state.id, definition: copied });
		};
		api.getFlag = (name) => registeredFlagValue(state, String(name));
		api.sendMessage = (message, options) => { void fireStateAction(state, "send_message", { message, options }); };
		api.sendUserMessage = (content, options) => { void fireStateAction(state, "send_user_message", { content, options }); };
		api.appendEntry = (customType, data) => { void fireStateAction(state, "append_entry", { customType, data }); };
		api.setSessionName = (name) => {
			state.stateSnapshot.sessionName = String(name);
			if (state.stateSnapshot.session) state.stateSnapshot.session.sessionName = String(name);
			void fireStateAction(state, "set_session_name", { name: String(name) });
		};
		api.getSessionName = () => state.stateSnapshot.sessionName ?? undefined;
		api.setLabel = (entryId, label) => {
			if (state.stateSnapshot.session) {
				state.stateSnapshot.session.labels ??= {};
				if (label === undefined) delete state.stateSnapshot.session.labels[entryId];
				else state.stateSnapshot.session.labels[entryId] = String(label);
			}
			void fireStateAction(state, "set_label", { entryId: String(entryId), label: label === undefined ? null : String(label) });
		};
		api.exec = (command, args = [], options = {}) => {
			const operationId = `${state.id}-exec-${state.nextExecOperationID++}`;
			const executing = stateAction(state, "exec", {
				command: String(command),
				args: Array.from(args, String),
				operationId,
				options: {
					...(options.cwd === undefined ? {} : { cwd: String(options.cwd) }),
					...(options.timeout === undefined ? {} : { timeout: Number(options.timeout) }),
				},
			});
			if (!options.signal) return executing;
			const cancel = () => {
				request("state_exec_cancel", { extensionId: state.id, operationId })
					.catch((error) => reportAsyncError(state, "exec cancellation", error));
			};
			options.signal.addEventListener("abort", cancel, { once: true });
			if (options.signal.aborted) cancel();
			return executing.finally(() => options.signal.removeEventListener("abort", cancel));
		};
		api.getActiveTools = () => [...state.stateSnapshot.activeTools];
		api.getAllTools = () => clone(state.stateSnapshot.allTools);
		api.setActiveTools = (names) => {
			state.stateSnapshot.activeTools = Array.from(names, String);
			void fireStateAction(state, "set_active_tools", { names: state.stateSnapshot.activeTools });
		};
		api.getCommands = () => clone(state.stateSnapshot.commands);
		api.setModel = (model) => stateAction(state, "set_model", { model });
		api.getThinkingLevel = () => state.stateSnapshot.thinkingLevel;
		api.setThinkingLevel = (level) => {
			state.stateSnapshot.thinkingLevel = String(level);
			void fireStateAction(state, "set_thinking_level", { level: state.stateSnapshot.thinkingLevel });
		};
		api.events = createEventBus(state);
	}

	return {
		onHandshake(value) {
			baseSnapshot = normalizeSnapshot(value.stateSnapshot);
		},
		extendAPI,
		extendContext,
		async handleRequest(frame) {
			if (frame.method === "event_bus_dispatch") {
				const state = extensions.get(frame.params.extensionId);
				const subscription = state?.stateBus.get(frame.params.subscriptionId);
				if (!subscription || subscription.channel !== frame.params.channel) {
					throw new Error(`unknown event bus subscription ${frame.params.subscriptionId}`);
				}
				invokeBusHandler(state, subscription.handler, frame.params.data);
				return { handled: true, result: { delivered: true } };
			}
			if (frame.method === "execute_bash_operation") {
				return { handled: true, result: await executeBashOperation(frame) };
			}
			return { handled: false };
		},
		handleEvent(frame) {
			const state = extensions.get(frame.params.extensionId);
			if (!state) return;
			if (frame.method === "state_signal_abort") {
				const controller = state.contextSignals.get(frame.params.signalId);
				if (controller && !controller.signal.aborted) controller.abort(frame.params.reason || undefined);
				return;
			}
			if (frame.method === "state_signal_release") {
				state.contextSignals.delete(frame.params.signalId);
				return;
			}
			if (frame.method !== "state_delta") return;
			state.stateSnapshot = normalizeSnapshot(frame.params.stateSnapshot);
			for (const [name, definition] of state.stateFlags) {
				if (!Object.hasOwn(state.stateSnapshot.flags, name) && definition.default !== undefined) {
					state.stateSnapshot.flags[name] = clone(definition.default);
				}
			}
		},
		onClose() {
			for (const state of extensions.values()) {
				state.stateBus?.clear();
				state.bashOperations?.clear();
				state.contextSignals?.clear();
			}
		},
	};
})());
// ===== END SECTION =====

const runtime = typeof globalThis.Bun === "object"
	? { name: "bun", version: globalThis.Bun.version }
	: { name: "node", version: process.versions.node };
const handshake = await request("handshake", { runtime, capabilities: ["tool_updates", "providers", "ui", "state_v1"] });
agent = handshake.agent ?? {};
for (const section of hostSections) section.onHandshake?.(handshake);
for (const entry of handshake.extensionEntries ?? []) entries.set(entry.id, entry);
finishHandshake();
