// F12 mermaid (#7624): grok-mermaid engine output plus the Mermaid markdown
// transformer, recorded as goldens for internal/mermaid and codingagent/modes.
import { mkdir, rm, writeFile } from "node:fs/promises";
import path from "node:path";
import { pathToFileURL } from "node:url";

type Span = { text: string; cls: string };
type MermaidArt = { plain: string[]; styled: Span[][]; width: number; warnings: string[] };

const d = (...lines: string[]) => lines.join("\n");

const engineCases: { name: string; source: string }[] = [
	// Flowchart: directions, shapes, edges, groups, statement forms.
	{ name: "flow-minimal", source: d("flowchart LR", "  A[Start] --> B[Done]") },
	{ name: "flow-single-node", source: d("flowchart TD", "  A") },
	{ name: "flow-direction-td", source: d("graph TD", "  A --> B", "  B --> C") },
	{ name: "flow-direction-rl", source: d("flowchart RL", "  A[Right] --> B[Left]") },
	{ name: "flow-direction-bt", source: d("flowchart BT", "  low --> high") },
	{
		name: "flow-shapes",
		source: d(
			"graph TD",
			"  A[Rect] --> B(Round)",
			"  B --> C{Diamond}",
			"  C --> D([Stadium])",
			"  D --> E[[Subroutine]]",
			"  E --> F((Circle))",
			"  F --> G>Flag]",
		),
	},
	{
		name: "flow-edge-labels",
		source: d("flowchart LR", "  A -->|yes| B[OK]", "  A -->|no| C[Retry]", "  C -- try again --> A"),
	},
	{ name: "flow-edge-styles", source: d("flowchart LR", "  A --- B", "  B -.-> C", "  C ==> D", "  D <--> E") },
	{ name: "flow-edge-heads", source: d("flowchart LR", "  A o--o B", "  B x--x C", "  C --o D", "  D --x E") },
	{ name: "flow-fanout", source: d("graph LR", "  A & B --> C & D") },
	{ name: "flow-semicolons", source: "graph LR; A-->B; B-->C" },
	{
		name: "flow-subgraph",
		source: d("flowchart TD", "  subgraph one[Group One]", "    A --> B", "  end", "  subgraph two", "    C", "  end", "  A --> C"),
	},
	{
		name: "flow-nested-subgraph",
		source: d(
			"flowchart LR",
			"  subgraph outer[Outer]",
			"    subgraph inner[Inner]",
			"      A --> B",
			"    end",
			"    B --> C",
			"  end",
			"  C --> D",
		),
	},
	{ name: "flow-quoted-labels", source: d("flowchart LR", '  A["a ] b"] --> B[5" pipe]') },
	{
		name: "flow-comments-styles",
		source: d("flowchart LR", "  %% a comment", "  classDef hot fill:#f00", "  style A fill:#00f", "  A[Styled] --> B", "  click A callback"),
	},
	{
		name: "flow-long-label",
		source: d("flowchart LR", "  A[This is an extremely long node label that stretches the box wide] --> B[tiny]"),
	},
	// Backtick-bearing labels feed the transformer's code-span fencing.
	{ name: "flow-backtick-single", source: d("flowchart LR", '  A["plain ` tick"] --> B["two `` ticks"]') },
	{ name: "flow-backtick-edges", source: d("flowchart LR", '  A["`lead"] --> B["trail`"]') },
	{ name: "flow-backtick-run", source: d("flowchart LR", '  A["```triple"] --> B["` `"]') },
	// Display width: CJK and emoji.
	{ name: "flow-cjk", source: d("flowchart LR", "  A[你好世界] --> B[再见]") },
	{ name: "flow-emoji", source: d("flowchart LR", "  A[🚀 Launch] --> B[🌕 Moon]") },
	{ name: "seq-cjk", source: d("sequenceDiagram", "  参加者->>相手: こんにちは世界") },
	// Flowchart warnings: dropped statements keep the rest of the art.
	{ name: "flow-warning-classdef", source: d("flowchart LR", "  A[Foo]:::highlight --> B[Bar]") },
	{
		name: "flow-warning-multi",
		source: d("flowchart LR", "  A[Foo]:::highlight --> B[Bar]", "  C[Baz]:::other --> D[Qux]"),
	},
	{ name: "flow-warning-unclosed", source: d("flowchart LR", "  A[unclosed --> B") },
	{ name: "flow-warning-no-target", source: d("flowchart LR", "  A --> B", "  B --") },
	{ name: "flow-warning-not-node", source: d("flowchart LR", "  A --> B", "  --> C") },
	// State.
	{ name: "state-minimal", source: d("stateDiagram-v2", "  [*] --> Idle", "  Idle --> [*]") },
	{
		name: "state-labels-notes",
		source: d(
			"stateDiagram-v2",
			'  state "Waiting for input" as W',
			"  note right of W: patience",
			"  Idle --> W: start",
			"  W --> Done: finish",
			"  Done --> [*]",
		),
	},
	{
		name: "state-choice-chain",
		source: d("stateDiagram-v2", "  state c <<choice>>", "  A --> c", "  c --> B: yes", "  c --> C: no", "  C --> D --> E"),
	},
	{ name: "state-v1-desc", source: d("stateDiagram", "  direction LR", "  A --> B", "  B: In progress") },
	// Class.
	{ name: "class-minimal", source: d("classDiagram", "  class Animal") },
	{
		name: "class-members",
		source: d("classDiagram", "  class Animal {", "    +String name", "    -int age", "    +run() void", "  }", "  Animal <|-- Dog"),
	},
	{
		name: "class-relations",
		source: d("classDiagram", "  A <|-- B", "  C *-- D", "  E o-- F", "  G --> H : uses", "  I ..> J", "  K -- L"),
	},
	{
		name: "class-generics-cardinality",
		source: d("classDiagram", "  class Box {", "    <<interface>>", "    +items List~int~", "  }", '  Customer "1" --> "0..*" Order : places'),
	},
	// ER.
	{ name: "er-minimal", source: d("erDiagram", "  CUSTOMER ||--o{ ORDER : places") },
	{
		name: "er-attributes",
		source: d("erDiagram", "  CUSTOMER {", "    string name", '    int id PK "the key"', "  }", "  CUSTOMER ||--|{ LINE_ITEM : contains"),
	},
	{
		name: "er-cardinalities",
		source: d("erDiagram", "  A |o..o| B : maybe", "  C }|..|{ D : many", "  p[Person] ||--o{ CAR : owns"),
	},
	// Sequence.
	{ name: "seq-minimal", source: d("sequenceDiagram", "  Alice->>Bob: Hello") },
	{
		name: "seq-participants-notes",
		source: d(
			"sequenceDiagram",
			"  participant A as Alice",
			"  actor B as Bob",
			"  A->>B: Hi",
			"  Note over A,B: Greeting",
			"  Note right of B: thinks",
		),
	},
	{
		name: "seq-blocks-autonumber",
		source: d(
			"sequenceDiagram",
			"  autonumber",
			"  Alice->>Bob: hi",
			"  loop Every day",
			"    Bob-->>Alice: reply",
			"  end",
			"  alt ok",
			"    Alice-)Bob: async",
			"  else fail",
			"    Alice-xBob: dead",
			"  end",
		),
	},
	{ name: "seq-self-message", source: d("sequenceDiagram", "  A->>A: think", "  A-->>A: again") },
	// Strict grammars salvage by dropping an unreadable final line.
	{ name: "state-salvage-final-line", source: d("stateDiagram-v2", "  A --> B", "  garbage here") },
	{ name: "seq-salvage-final-line", source: d("sequenceDiagram", "  Alice->>Bob: hi", "  Bob->") },
	// Null: unsupported kinds, non-mermaid text, blank and empty bodies.
	{ name: "null-pie", source: d("pie", "  title Pets", '  "Dogs" : 4') },
	{ name: "null-gantt", source: d("gantt", "  title Timeline", "  section One") },
	{ name: "null-not-mermaid", source: "this is not mermaid at all" },
	{ name: "null-blank", source: "   \n\t\n" },
	{ name: "null-header-only", source: "flowchart LR" },
	{ name: "null-broken-class", source: d("classDiagram", "  ???", "  !!!") },
];

type TransformDoc = { name: string; markdown: string; tight: number };

const transformDocs: TransformDoc[] = [
	{ name: "basic", markdown: "Before\n\n```mermaid\nflowchart LR\n  A[Start] --> B[Done]\n```\nAfter", tight: 10 },
	{ name: "warning-single", markdown: "```mermaid\nflowchart LR\n  A[Foo]:::highlight --> B[Bar]\n```", tight: 10 },
	{
		name: "warning-multi",
		markdown: "```mermaid\nflowchart LR\n  A[Foo]:::highlight --> B[Bar]\n  C[Baz]:::other --> D[Qux]\n```\nFollowing text",
		tight: 10,
	},
	{ name: "unsupported-pie", markdown: '```mermaid\npie\n  title Pets\n  "Dogs" : 4\n```', tight: 10 },
	{ name: "broken", markdown: "```mermaid\nclassDiagram\n  ???\n  !!!\n```", tight: 10 },
	{ name: "partial-stream", markdown: "```mermaid\nflowchart LR\n  A --> B", tight: 10 },
	{
		name: "backticks",
		markdown: '```mermaid\nflowchart LR\n  A["plain ` tick"] --> B["two `` ticks"]\n  B --> C["`lead"]\n  C --> D["trail`"]\n```',
		tight: 10,
	},
	{ name: "cjk-emoji", markdown: "```mermaid\nflowchart LR\n  A[你好世界] --> B[🚀 emoji]\n```", tight: 12 },
	{ name: "indented-code", markdown: "    flowchart LR\n      A --> B\n", tight: 10 },
	{ name: "nested-fence-list", markdown: "- item\n\n  ```mermaid\n  flowchart LR\n    A --> B\n  ```\n", tight: 10 },
	{
		name: "two-blocks",
		markdown:
			"```mermaid\nflowchart LR\n  A --> B\n```\n\nMiddle prose\n\n```js\nconst x = 1;\n```\n\n```mermaid\nsequenceDiagram\n  Alice->>Bob: Hello\n```\n",
		tight: 10,
	},
	{
		name: "lang-variants",
		markdown: "```Mermaid extra-info\nflowchart LR\n  A --> B\n```\n\n~~~mermaid\ngraph LR\n  C --> D\n~~~\n",
		tight: 10,
	},
];

const ROOMY_WIDTH = 100;

// Deterministic marker theme, as in upstream test/mermaid.test.ts.
const markerTheme = {
	fg: (color: string, text: string) => `<${color}>${text}</${color}>`,
	bold: (text: string) => `<b>${text}</b>`,
};

export async function generateF12Mermaid(
	upstreamRoot: string,
	outputRoot: string,
	upstreamCommit: string,
): Promise<void> {
	const load = async (rel: string) => import(pathToFileURL(path.join(upstreamRoot, rel)).href);
	const { render } = await load("node_modules/grok-mermaid/dist/index.js");
	const { createMermaidMarkdownTransformer } = await load(
		"packages/coding-agent/src/modes/interactive/components/mermaid.ts",
	);

	const engine = engineCases.map(({ name, source }) => {
		const art = render(source) as MermaidArt | null;
		return {
			name,
			source,
			art:
				art === null
					? null
					: {
							plain: art.plain,
							styled: art.styled.map((row) => row.map((span) => ({ cls: span.cls, text: span.text }))),
							width: art.width,
							warnings: art.warnings,
						},
		};
	});

	const transform = (
		doc: TransformDoc,
		suffix: string,
		mode: string,
		isStreaming: boolean,
		messageType: string,
		availableWidth: number,
		themed: boolean,
	) => {
		const transformer = createMermaidMarkdownTransformer({
			getMode: () => mode,
			...(themed ? { theme: markerTheme } : {}),
		});
		return {
			name: `${doc.name}/${suffix}`,
			markdown: doc.markdown,
			mode,
			isStreaming,
			messageType,
			availableWidth,
			themed,
			output: transformer(doc.markdown, { messageType, isStreaming, availableWidth }),
		};
	};

	const transformer = transformDocs.flatMap((doc) => [
		...["off", "final", "streaming"].flatMap((mode) =>
			[false, true].map((isStreaming) =>
				transform(doc, `${mode}-${isStreaming ? "stream" : "idle"}`, mode, isStreaming, "assistant", ROOMY_WIDTH, true),
			),
		),
		transform(doc, "thinking", "streaming", false, "assistant-thinking", ROOMY_WIDTH, true),
		transform(doc, "tight", "streaming", false, "assistant", doc.tight, true),
		transform(doc, "plain", "streaming", false, "assistant", ROOMY_WIDTH, false),
	]);

	const familyDir = path.join(outputRoot, "F12-mermaid");
	await rm(familyDir, { recursive: true, force: true });
	await mkdir(familyDir, { recursive: true });
	await writeFile(
		path.join(familyDir, "cases.json"),
		`${JSON.stringify({ schemaVersion: 1, engine, transformer }, null, 2)}\n`,
	);
	await writeFile(
		path.join(familyDir, "manifest.json"),
		`${JSON.stringify(
			{
				family: "F12-mermaid",
				upstreamCommit,
				generator: "conformance/extract/f12-mermaid.ts",
				sources: [
					"node_modules/grok-mermaid (v0.2.2)",
					"packages/coding-agent/src/modes/interactive/components/mermaid.ts",
					"packages/coding-agent/test/mermaid.test.ts",
				],
				files: ["cases.json"],
				metadata: {
					engine: "raw render(src) output per corpus diagram; null means unsupported input",
					transformer:
						"createMermaidMarkdownTransformer over mode x isStreaming x messageType x width, marker theme fg -> <color>text</color>, bold -> <b>text</b>",
				},
			},
			null,
			2,
		)}\n`,
	);
}
