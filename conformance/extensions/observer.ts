const MARKER = "PI_EXTENSION_MATRIX:";

function text(value: unknown): string {
	return typeof value === "string" ? value : "";
}

function compare(left: string, right: string): number {
	return left < right ? -1 : left > right ? 1 : 0;
}

export default function extensionMatrixObserver(pi: any) {
	pi.registerCommand("__extension_matrix_probe", {
		description: "Emit the extension compatibility probe",
		handler: async (_args: string, ctx: any) => {
			const snapshot = {
				activeTools: pi.getActiveTools().map(String).sort(compare),
				allTools: pi.getAllTools().map((tool: any) => text(tool.name)).sort(compare),
				commands: pi
					.getCommands()
					.map((command: any) => ({ name: text(command.name), description: text(command.description) }))
					.sort((left: any, right: any) => compare(left.name, right.name) || compare(left.description, right.description)),
			};
			ctx.ui.notify(MARKER + JSON.stringify(snapshot), "info");
		},
	});
}
