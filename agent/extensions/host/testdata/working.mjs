import { writeFile } from "node:fs/promises";
import { join } from "node:path";

let executions = 0;
let dynamicRegistered = false;

export default function (pi) {
	pi.registerTool({
		name: "host_echo",
		label: "Host Echo",
		description: "Echo through the extension host",
		parameters: {
			type: "object",
			properties: { text: { type: "string" } },
			required: ["text"],
		},
		async execute(_toolCallId, params, _signal, onUpdate) {
			onUpdate({ content: [{ type: "text", text: `partial:${params.text}` }], details: { partial: true } });
			executions += 1;
			return {
				content: [{ type: "text", text: `final:${params.text}` }],
				details: { executions, pid: process.pid },
			};
		},
	});

	pi.registerTool({
		name: "host_crash",
		label: "Host Crash",
		description: "Exit the fixture host",
		parameters: { type: "object", properties: {} },
		async execute() {
			process.exit(17);
		},
	});

	pi.registerCommand("host-command", {
		description: "Write command arguments",
		async handler(args, ctx) {
			await writeFile(join(ctx.cwd, "host-command.txt"), args, "utf8");
		},
	});

	pi.on("before_agent_start", (event) => {
		if (!dynamicRegistered) {
			dynamicRegistered = true;
			pi.registerTool({
				name: "host_dynamic",
				label: "Host Dynamic",
				description: "Registered after the extension factory",
				parameters: { type: "object", properties: {} },
				async execute() { return { content: [{ type: "text", text: "dynamic" }] }; },
			});
		}
		return { systemPrompt: `${event.systemPrompt} host-event` };
	});
}
