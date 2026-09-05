import { cp, mkdtemp, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import path from "node:path";
import { pathToFileURL } from "node:url";
import { writeProviderModelData } from "./upstream-model-data.ts";

export async function extractGitHubCopilotAuthF2(upstreamRoot: string) {
	const directory = await mkdtemp(path.join(tmpdir(), "orb-copilot-fixture-"));
	try {
		const packageRoot = path.join(directory, "ai");
		await cp(path.join(upstreamRoot, "packages/ai"), packageRoot, {
			recursive: true,
		});
		const knownModelIds = [
			"fixture-enabled",
			"fixture-unconfigured",
			"fixture-no-tools",
		];
		await writeProviderModelData(
			path.join(packageRoot, "src/providers"),
			"github-copilot",
			Object.fromEntries(
				knownModelIds.map((id) => [id, { id, api: "openai-responses" }]),
			),
		);
		const { githubCopilotOAuth } = await import(
			pathToFileURL(path.join(packageRoot, "src/auth/oauth/github-copilot.ts"))
				.href
		);
		const item = (
			id: string,
			picker: boolean,
			state: string,
			tools = true,
		) => ({
			id,
			model_picker_enabled: picker,
			policy: { state },
			capabilities: { supports: { tool_calls: tools } },
		});
		const cases = [];
		for (const name of [
			"selective-policy",
			"individual-policy-fallback",
			"business-no-fallback",
			"rate-limited-policy",
		]) {
			const fallback = name.includes("fallback");
			const host = name.startsWith("business") ? "business" : "individual";
			const models = [
				item(knownModelIds[0], !fallback, "enabled"),
				item(knownModelIds[1], !fallback, "unconfigured"),
				item(knownModelIds[2], true, "unconfigured", false),
				item("remote-only-model", !fallback, "unconfigured"),
				item("disabled-model", true, "disabled"),
			];
			const requests: string[] = [];
			const progress: string[] = [];
			const previous = globalThis.fetch;
			let catalogAttempts = 0;
			try {
				globalThis.fetch = async (input, init) => {
					const url = String(input);
					const pathname = new URL(url).pathname;
					requests.push(`${init?.method ?? "GET"} ${pathname}`);
					const response = (body: unknown, status = 200) =>
						new Response(JSON.stringify(body), {
							status,
							headers: {
								"content-type": "application/json",
								"retry-after": "0",
							},
						});
					if (pathname === "/login/device/code")
						return response({
							device_code: "device",
							user_code: "ABCD",
							verification_uri: "https://github.com/login/device",
							interval: 0,
							expires_in: 900,
						});
					if (pathname === "/login/oauth/access_token")
						return response({ access_token: "refresh" });
					if (pathname === "/copilot_internal/v2/token")
						return response({
							token: `tid=fixture;proxy-ep=proxy.${host}.githubcopilot.com`,
							expires_at: 1800000000,
						});
					if (pathname === "/models") {
						if (name === "rate-limited-policy" && catalogAttempts++ === 0)
							return response({ error: "rate limit" }, 429);
						return response({ data: models });
					}
					if (pathname.endsWith("/policy"))
						return response({}, name === "rate-limited-policy" ? 429 : 200);
					throw new Error(`Unexpected Copilot URL ${url}`);
				};
				const credential = await githubCopilotOAuth.login({
					signal: new AbortController().signal,
					prompt: async () => "",
					notify: (event) => {
						if (event.type === "progress") progress.push(event.message);
					},
				});
				cases.push({
					name,
					knownModelIds,
					models,
					host,
					requests,
					progress,
					credential,
				});
			} finally {
				globalThis.fetch = previous;
			}
		}
		return { cases };
	} finally {
		await rm(directory, { recursive: true, force: true });
	}
}
