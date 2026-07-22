export default function (pi) {
	const providerId = "host-provider";
	const emptyStream = async function* () {};

	pi.registerProvider({
		id: providerId,
		name: "Host Provider",
		baseUrl: "https://provider.invalid/v1",
		headers: { "X-Provider-Host": "fixture" },
		auth: {
			apiKey: {
				name: "Host provider API key",
				async resolve({ ctx, credential }) {
					if (credential?.key === "crash") process.exit(73);
					if (credential?.key === "throw") throw new Error("wire resolve failed");
					const key = credential?.key ?? await ctx.env("HOST_PROVIDER_KEY");
					if (!key) return undefined;
					return { auth: { apiKey: key }, source: credential ? "stored credential" : "HOST_PROVIDER_KEY" };
				},
				async check({ ctx, credential }) {
					const key = credential?.key ?? await ctx.env("HOST_PROVIDER_KEY");
					return key ? { source: credential ? "stored credential" : "HOST_PROVIDER_KEY", type: "api_key" } : undefined;
				},
				async login(interaction) {
					const key = await interaction.prompt({ type: "secret", message: "Paste API key" });
					return { type: "api_key", key };
				},
			},
			oauth: {
				name: "Host provider OAuth",
				loginLabel: "Sign in to Host Provider",
				async login(interaction) {
					interaction.notify({
						type: "auth_url",
						url: "https://provider.invalid/oauth",
						instructions: "Open the URL and paste the code",
					});
					const code = await interaction.prompt({ type: "manual_code", message: "Paste authorization code" });
					return { type: "oauth", refresh: `refresh:${code}`, access: `access:${code}`, expires: 4102444800000 };
				},
				async refresh(credential) {
					return { ...credential, access: `${credential.access}:refreshed`, expires: 4102444800000 };
				},
				async toAuth(credential) {
					return { apiKey: `oauth:${credential.access}`, baseUrl: "https://oauth.provider.invalid/v1" };
				},
			},
		},
		getModels() {
			return [{
				id: "host-model",
				name: "Host Model",
				api: "openai-responses",
				provider: providerId,
				baseUrl: "https://provider.invalid/v1",
				reasoning: false,
				input: ["text"],
				cost: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0 },
				contextWindow: 8192,
				maxTokens: 2048,
			}];
		},
		stream: emptyStream,
		streamSimple: emptyStream,
	});
}
