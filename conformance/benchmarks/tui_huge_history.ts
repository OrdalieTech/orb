import { performance } from "node:perf_hooks";
import { Container, TUI, type Component } from "../../.upstream/packages/tui/src/tui.ts";
import type { Terminal } from "../../.upstream/packages/tui/src/terminal.ts";

class Line implements Component {
	private lines: string[];
	constructor(value: string) {
		this.lines = [value];
	}
	set value(value: string) {
		this.lines[0] = value;
	}
	render(_width: number): string[] {
		return this.lines;
	}
	invalidate(): void {}
}

class SinkTerminal implements Terminal {
	columns = 120;
	rows = 40;
	kittyProtocolActive = false;
	writes = 0;
	bytes = 0;
	lastBytes = 0;
	start(): void {}
	stop(): void {}
	async drainInput(): Promise<void> {}
	write(data: string): void {
		this.writes++;
		this.lastBytes = data.length;
		this.bytes += data.length;
	}
	moveBy(): void {}
	hideCursor(): void {}
	showCursor(): void {}
	clearLine(): void {}
	clearFromCursor(): void {}
	clearScreen(): void {}
	setTitle(): void {}
	setProgress(): void {}
}

function median(values: number[]): number {
	const sorted = [...values].sort((left, right) => left - right);
	return sorted[Math.floor(sorted.length / 2)]!;
}

function measure(count: number, samples: number): void {
	const fixed = new Line("history");
	const tail = new Line("tail-a");
	const chat = new Container();
	chat.children = Array(count - 1).fill(fixed);
	chat.addChild(tail);

	for (let index = 0; index < 3; index++) chat.render(120);
	const flatten: number[] = [];
	let consumed = 0;
	for (let index = 0; index < samples; index++) {
		const start = performance.now();
		consumed += chat.render(120).length;
		flatten.push(performance.now() - start);
	}

	const terminal = new SinkTerminal();
	const tui = new TUI(terminal, false);
	tui.addChild(chat);
	tui.addChild(new Line("input"));
	const render = (tui as unknown as { doRender(): void }).doRender.bind(tui);
	const initialStart = performance.now();
	render();
	const initialMs = performance.now() - initialStart;
	const initialBytes = terminal.lastBytes;

	const steady: number[] = [];
	for (let index = 0; index < samples; index++) {
		tail.value = index % 2 === 0 ? "tail-b" : "tail-a";
		const start = performance.now();
		render();
		steady.push(performance.now() - start);
	}

	const memory = process.memoryUsage();
	console.log(JSON.stringify({
		lines: count,
		viewport: `${terminal.columns}x${terminal.rows}`,
		container_flatten_median_ms: +median(flatten).toFixed(3),
		initial_full_render_ms: +initialMs.toFixed(3),
		initial_write_bytes: initialBytes,
		steady_tail_change_median_ms: +median(steady).toFixed(3),
		steady_tail_change_min_ms: +Math.min(...steady).toFixed(3),
		steady_tail_change_max_ms: +Math.max(...steady).toFixed(3),
		steady_write_bytes: terminal.lastBytes,
		rss_mib: +(memory.rss / 1048576).toFixed(1),
		heap_used_mib: +(memory.heapUsed / 1048576).toFixed(1),
		consumed,
	}));
}

measure(100_000, 15);
measure(1_000_000, 7);
