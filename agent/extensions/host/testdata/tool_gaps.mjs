// e2e fixture for the register_tool wire gap closures: lazy promptGuidelines
// getter, live renderCall/renderResult functions, and terminate in the tool
// result crossing the wire intact.
let guidelineReads = 0;

export default function activate(pi) {
	pi.registerTool({
		name: "gap_tool",
		description: "lazy guidelines, live renderers, terminate",
		parameters: { type: "object", properties: { key: { type: "string" } }, required: ["key"] },
		// Lazy contract: each read reflects current state (upstream re-reads
		// this when building the system prompt, e.g. an available-model list).
		get promptGuidelines() {
			guidelineReads++;
			return [`guideline-read-${guidelineReads}`];
		},
		renderCall(args, _theme, context) {
			return { render: (width) => [`call:${args?.key ?? ""}:${width}:${context.toolCallId}`] };
		},
		renderResult(result, options, _theme, _context) {
			const text = result?.content?.[0]?.text ?? "";
			return { render: (width) => [`result:${text}:${options.expanded}:${width}`] };
		},
		async execute(_toolCallId, params) {
			return { content: [{ type: "text", text: `ran:${params.key}` }], terminate: true };
		},
	});
}
