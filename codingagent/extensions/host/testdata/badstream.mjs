export default function (pi) {
	pi.registerProvider({
		id: "bad-stream-provider",
		name: "Bad Stream Provider",
		auth: {
			apiKey: {
				name: "Bad stream provider API key",
				async resolve() {
					return { auth: { apiKey: "fixture-key" }, source: "fixture" };
				},
			},
		},
		getModels() {
			return [{
				id: "bad-stream-model",
				name: "Bad Stream Model",
				api: "openai-responses",
				provider: "bad-stream-provider",
				baseUrl: "https://provider.invalid/v1",
				reasoning: false,
				input: ["text"],
				cost: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0 },
				contextWindow: 8192,
				maxTokens: 2048,
			}];
		},
		stream: async function* () {},
		streamSimple: async function* () {
			yield { type: "bogus_event" };
			yield { type: "text_delta", contentIndex: 0, delta: "after" };
		},
	});
}
