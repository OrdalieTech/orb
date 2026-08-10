// orb-extension-sdk: unsupported-export machinery.
// Every upstream export name that orb does not implement resolves to a stub
// from here: importable (module init never throws), precise on use.
import manifest from "../sdk.json" with { type: "json" };

export const SDK_VERSION = manifest.version;

export class OrbUnsupportedCapability extends Error {
	constructor(message) {
		super(message);
		this.name = "OrbUnsupportedCapability";
		this.code = "ORB_UNSUPPORTED_CAPABILITY";
	}
}

/**
 * Build the diagnostic for an unimplemented export or method.
 * Text shape is pinned by the design brief; do not reword.
 *
 * @param {string} pkg - short package name ("coding-agent" | "ai" | "tui")
 * @param {string} name - export (or "Export.method") the caller touched
 * @param {readonly string[]} supported - implemented export names of that module
 * @param {string} [detail] - optional trailing hint (e.g. missing capability)
 */
export function unsupportedError(pkg, name, supported, detail) {
	let message =
		`${pkg}#${name} is not implemented by orb-extension-sdk ${SDK_VERSION}; ` +
		`supported exports: ${supported.join(", ")}`;
	if (detail) message += ` (${detail})`;
	return new OrbUnsupportedCapability(message);
}

// Symbols the runtime may probe during innocuous operations (inspection,
// promise-resolution checks, primitive coercion attempts). Returning undefined
// for them keeps stubs init-safe; every string-keyed access still throws.
function isProbeKey(key) {
	return typeof key === "symbol" || key === "then" || key === "constructor" || key === "prototype";
}

/**
 * Create an importable stand-in for one unimplemented upstream export.
 * Calling it, constructing it, or reading any property throws the precise
 * OrbUnsupportedCapability diagnostic.
 */
export function unsupported(pkg, name, supported) {
	const throwNow = () => {
		throw unsupportedError(pkg, name, supported);
	};
	const target = function orbUnsupportedExport() {};
	Object.defineProperty(target, "name", { value: name, configurable: true });
	return new Proxy(target, {
		apply: throwNow,
		construct: throwNow,
		get(currentTarget, key, receiver) {
			if (isProbeKey(key)) return Reflect.get(currentTarget, key, receiver);
			throwNow();
		},
		set: throwNow,
	});
}

/** Attach throwing methods for the unimplemented part of a supported class. */
export function attachUnsupportedMethods(prototype, pkg, className, methodNames, supported) {
	for (const method of methodNames) {
		if (Object.hasOwn(prototype, method)) continue;
		Object.defineProperty(prototype, method, {
			value: function orbUnsupportedMethod() {
				throw unsupportedError(pkg, `${className}.${method}`, supported);
			},
			writable: true,
			configurable: true,
		});
	}
}
