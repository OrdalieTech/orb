export default function (pi) {
	setInterval(() => {}, 1000000);
	pi.registerTool({
		name: "host_keepalive",
		label: "Keepalive",
		description: "Holds a live timer handle",
		parameters: { type: "object", properties: {} },
		async execute() {
			return { content: [{ type: "text", text: "ok" }] };
		},
	});
}
