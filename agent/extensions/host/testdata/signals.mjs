import { writeFileSync } from "node:fs";

export default function (pi) {
	pi.registerTool({
		name: "host_slow",
		label: "Slow tool",
		description: "Sleeps for params.ms milliseconds",
		parameters: { type: "object", properties: { ms: { type: "number" } } },
		async execute(_id, params) {
			await new Promise((resolve) => setTimeout(resolve, params.ms ?? 0));
			return { content: [{ type: "text", text: "slept" }] };
		},
	});
	pi.registerTool({
		name: "host_wait_abort",
		label: "Wait for abort",
		description: "Writes params.marker when the positional AbortSignal fires",
		parameters: { type: "object", properties: { marker: { type: "string" } } },
		async execute(_id, params, signal) {
			return await new Promise((resolve) => {
				const finish = () => {
					writeFileSync(params.marker, "aborted");
					resolve({ content: [{ type: "text", text: "aborted" }] });
				};
				// A request cancelled before it started carries an
				// already-aborted signal, which never fires "abort".
				if (signal.aborted) {
					finish();
					return;
				}
				signal.addEventListener("abort", finish, { once: true });
			});
		},
	});
}
