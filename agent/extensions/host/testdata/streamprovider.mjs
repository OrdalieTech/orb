import { existsSync } from "node:fs";
import { join } from "node:path";

export default function (pi) {
	pi.registerProvider({
		id: "stream-provider",
		name: "Stream Provider",
		auth: {
			apiKey: {
				name: "Stream provider API key",
				async resolve() {
					return { auth: { apiKey: "fixture-key" }, source: "fixture" };
				},
			},
		},
		getModels() {
			return [{
				id: "stream-model",
				name: "Stream Model",
				api: "openai-responses",
				provider: "stream-provider",
				baseUrl: "https://provider.invalid/v1",
				reasoning: false,
				input: ["text"],
				cost: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0 },
				contextWindow: 8192,
				maxTokens: 2048,
			}];
		},
		stream: async function* () {},
		// The second event is gated on a file the Go test writes only after it
		// receives the first one, so this stream can complete only if events are
		// delivered incrementally.
		streamSimple: async function* () {
			yield { type: "text_delta", contentIndex: 0, delta: "first" };
			const gate = join(process.cwd(), "stream-gate");
			while (!existsSync(gate)) {
				await new Promise((resolve) => setTimeout(resolve, 20));
			}
			yield { type: "text_delta", contentIndex: 0, delta: "second" };
		},
	});
}
