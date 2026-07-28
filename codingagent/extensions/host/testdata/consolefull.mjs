export default function (pi) {
	console.table([{ a: 1 }]);
	console.group("group-label");
	console.groupEnd();
	console.time("timer-label");
	console.timeEnd("timer-label");
	console.trace("trace-label");
	console.dir({ nested: true });
	console.count("count-label");
	console.assert(false, "assert-label");
	console.clear();
	console.profile("profile-label");
	console.profileEnd("profile-label");
	console.timeStamp("stamp-label");
	console.createTask("task-label").run(() => {});
	new console.Console(process.stdout).log("sub-console-label");
	pi.registerTool({
		name: "host_console",
		label: "Console probe",
		description: "Registered after exercising the console surface",
		parameters: { type: "object", properties: {} },
		async execute() {
			return { content: [{ type: "text", text: "ok" }] };
		},
	});
}
