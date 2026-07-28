import { writeFileSync } from "node:fs";
import { join } from "node:path";

export default function (pi) {
	pi.registerProvider({
		id: "infinite-provider",
		name: "Infinite Provider",
		auth: {
			apiKey: {
				name: "Infinite provider API key",
				async resolve() {
					return { auth: { apiKey: "fixture-key" }, source: "fixture" };
				},
			},
		},
		getModels() {
			return [{
				id: "infinite-model",
				name: "Infinite Model",
				api: "openai-responses",
				provider: "infinite-provider",
				baseUrl: "https://provider.invalid/v1",
				reasoning: false,
				input: ["text"],
				cost: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0 },
				contextWindow: 8192,
				maxTokens: 2048,
			}];
		},
		stream: async function* () {},
		// Never ends on its own; the finally marker proves the host terminated
		// the generator after pigo abandoned the stream.
		streamSimple: async function* () {
			try {
				while (true) {
					yield { type: "text_delta", contentIndex: 0, delta: "tick" };
					await new Promise((resolve) => setTimeout(resolve, 10));
				}
			} finally {
				writeFileSync(join(process.cwd(), "stream-stopped"), "stopped");
			}
		},
	});
}
