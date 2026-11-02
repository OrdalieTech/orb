import { writeFile } from "node:fs/promises";
import path from "node:path";
import { pathToFileURL } from "node:url";

// OpenRouter usage accounting on Chat Completions: the catalog estimate (calculateCost), then
// applyOpenRouterReportedCost for an OpenRouter model, as parseChunkUsage does.
export async function generateF2OpenRouterUsage(upstreamRoot: string, outputRoot: string): Promise<void> {
	const { applyOpenRouterReportedCost, isOpenRouterModel } = await import(
		pathToFileURL(path.join(upstreamRoot, "packages/ai/src/utils/openrouter-cost.ts")).href
	);
	const rates = { input: 0.6, output: 1.4, cacheRead: 0.06, cacheWrite: 0.1 };
	const tokens = { input: 50, output: 5, cacheRead: 40, cacheWrite: 10 };
	const estimate = {
		input: (rates.input / 1e6) * tokens.input,
		output: (rates.output / 1e6) * tokens.output,
		cacheRead: (rates.cacheRead / 1e6) * tokens.cacheRead,
		cacheWrite: (rates.cacheWrite * tokens.cacheWrite) / 1e6,
	};
	const accounting = (cost: string, byok?: string, details?: string) =>
		`,"cost":${cost}${byok === undefined ? "" : `,"is_byok":${byok}`}${details === undefined ? "" : `,"cost_details":${details}`}`;
	const cases = [
		{ name: "reported", provider: "openrouter", fields: accounting("0.123", "false") },
		{ name: "free", provider: "openrouter", fields: accounting("0", "false") },
		{ name: "missing", provider: "openrouter", fields: "" },
		{ name: "null", provider: "openrouter", fields: accounting("null", "false") },
		{ name: "not-a-number", provider: "openrouter", fields: accounting('"0.123"', "false") },
		{ name: "overflow", provider: "openrouter", fields: accounting("1e9999", "false") },
		{ name: "negative", provider: "openrouter", fields: accounting("-1", "false") },
		{ name: "no-byok-flag", provider: "openrouter", fields: accounting("0.123") },
		{ name: "byok-upstream", provider: "openrouter", fields: accounting("0.01", "true", '{"upstream_inference_cost":0.2,"upstream_inference_prompt_cost":0.15,"upstream_inference_completions_cost":0.05}') },
		{ name: "byok-upstream-parts", provider: "openrouter", fields: accounting("0.01", "true", '{"upstream_inference_cost":null,"upstream_inference_prompt_cost":0.15,"upstream_inference_completions_cost":0.05}') },
		{ name: "byok-no-details", provider: "openrouter", fields: accounting("0.01", "true", "null") },
		{ name: "other-provider", provider: "openai", fields: accounting("0.123", "false") },
		{ name: "custom-openrouter-endpoint", provider: "custom", baseUrl: "https://openrouter.ai/api/v1", fields: accounting("0.123", "false") },
	].map(({ fields, ...entry }) => {
		const usage = `{"prompt_tokens":100,"completion_tokens":5,"prompt_tokens_details":{"cached_tokens":40,"cache_write_tokens":10},"completion_tokens_details":{"reasoning_tokens":2}${fields}}`;
		const result = {
			cost: { ...estimate, total: estimate.input + estimate.output + estimate.cacheRead + estimate.cacheWrite, totalFromProvider: false },
		};
		if (isOpenRouterModel({ provider: entry.provider, baseUrl: entry.baseUrl ?? "https://fixture.invalid/v1" })) {
			applyOpenRouterReportedCost(result, JSON.parse(usage));
		}
		return { ...entry, usage, expectedCost: result.cost };
	});
	await writeFile(
		path.join(outputRoot, "F2", "openrouter-usage.json"),
		`${JSON.stringify({ source: "packages/ai/src/utils/openrouter-cost.ts", modelCost: rates, cases }, null, 2)}\n`,
	);
}
