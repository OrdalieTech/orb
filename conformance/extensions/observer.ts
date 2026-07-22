const MARKER = "PI_EXTENSION_MATRIX:";

function text(value: unknown): string {
	return typeof value === "string" ? value : "";
}

function compare(left: string, right: string): number {
	return left < right ? -1 : left > right ? 1 : 0;
}

function canonical(value: unknown): unknown {
	if (Array.isArray(value)) return value.map(canonical);
	if (value && typeof value === "object") {
		return Object.fromEntries(
			Object.entries(value as Record<string, unknown>)
				.sort(([left], [right]) => compare(left, right))
				.map(([key, item]) => [key, canonical(item)]),
		);
	}
	return value;
}

export default function extensionMatrixObserver(pi: any) {
	pi.registerCommand("__extension_matrix_probe", {
		description: "Emit the extension compatibility probe",
		handler: async (_args: string, ctx: any) => {
			const snapshot = {
				activeTools: pi.getActiveTools().map(String).sort(compare),
				allTools: pi
					.getAllTools()
					.map((tool: any) => ({
						name: text(tool.name),
						description: text(tool.description),
						parameters: canonical(tool.parameters),
						promptGuidelines: Array.isArray(tool.promptGuidelines) ? tool.promptGuidelines.map(text) : [],
					}))
					.sort((left: any, right: any) => compare(left.name, right.name)),
				commands: pi
					.getCommands()
					.map((command: any) => ({ name: text(command.name), description: text(command.description) }))
					.sort((left: any, right: any) => compare(left.name, right.name) || compare(left.description, right.description)),
			};
			ctx.ui.notify(MARKER + JSON.stringify(snapshot), "info");
		},
	});
}
