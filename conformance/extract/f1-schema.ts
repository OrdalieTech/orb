import { mkdir, readFile, writeFile } from "node:fs/promises";
import path from "node:path";
import { pathToFileURL } from "node:url";

export async function generateF1Schema(
	upstreamRoot: string,
	outputRoot: string,
	_upstreamCommit: string,
): Promise<void> {
	const typeboxURL = pathToFileURL(path.join(upstreamRoot, "node_modules/typebox/build/index.mjs")).href;
	const helpersURL = pathToFileURL(
		path.join(upstreamRoot, "packages/ai/src/utils/string-enum.ts"),
	).href;
	const validationURL = pathToFileURL(path.join(upstreamRoot, "packages/ai/src/utils/validation.ts")).href;
	const [{ Type }, { StringEnum }, { validateToolArguments }] = await Promise.all([
		import(typeboxURL),
		import(helpersURL),
		import(validationURL),
	]);

	const nestedObject = Type.Object({
		path: Type.String({ description: "Path to inspect <>&" }),
		children: Type.Array(
			Type.Object({
				name: Type.String({ description: "Child name" }),
				note: Type.Optional(Type.String({ description: "Optional note" })),
			}),
			{ description: "Nested children" },
		),
		labels: Type.Optional(Type.Array(Type.String(), { description: "Optional labels" })),
		mode: StringEnum(["read", "write"], { description: "Operation mode" }),
		filter: Type.Optional(
			Type.Object({
				pattern: Type.String(),
			}),
		),
	});
	const requiredOptional = Type.Object({
		required: Type.String(),
		optional: Type.Optional(Type.String({ description: "one, two" })),
	});
	const stringEnum = StringEnum(["add", "subtract", "multiply", "divide"]);

	const familyDir = path.join(outputRoot, "F1");
	await mkdir(familyDir, { recursive: true });
	await writeFile(
		path.join(familyDir, "schema.json"),
		`${JSON.stringify(
			{
				source: "typebox@1.3.7 + packages/ai/src/utils/string-enum.ts",
				cases: [
					{ name: "nested-object", schema: nestedObject },
					{ name: "required-optional", schema: requiredOptional },
					{ name: "string-enum", schema: stringEnum },
				],
			},
			null,
			2,
		)}\n`,
	);

	const cases = await validationCases(Type, StringEnum);
	const names = new Set<string>();
	const results = cases.map((validationCase) => {
		if (names.has(validationCase.name)) throw new Error(`duplicate validation case ${validationCase.name}`);
		names.add(validationCase.name);
		// The golden carries plain JSON Schema, as Orb receives it; TypeBox schemas compile the same way.
		const schema = JSON.parse(JSON.stringify(validationCase.schema));
		const tool = validationCase.tool ?? "t";
		const toolCall = {
			type: "toolCall",
			id: "call",
			name: tool,
			arguments: JSON.parse(validationCase.arguments),
			...(validationCase.argumentsError === undefined ? {} : { argumentsError: validationCase.argumentsError }),
		};
		const outcome: Record<string, unknown> = {};
		try {
			outcome.value = validateToolArguments({ name: tool, description: "", parameters: schema }, toolCall);
		} catch (error) {
			outcome.error = (error as Error).message;
		}
		return { ...validationCase, tool, schema, ...outcome };
	});
	await writeFile(
		path.join(familyDir, "validation.json"),
		`${JSON.stringify({ source: "packages/ai/src/utils/validation.ts validateToolArguments", cases: results }, null, 2)}\n`,
	);
}

interface ValidationCase {
	name: string;
	tool?: string;
	schema: unknown;
	/** The provider's argument JSON text; the tool call carries JSON.parse of it. */
	arguments: string;
	argumentsError?: string;
}

const repoRoot = path.resolve(import.meta.dirname, "../..");

/** The first backtick-quoted JSON literal after anchor in an Orb Go source file: the schema Orb runs. */
async function goSchema(file: string, anchor: string): Promise<any> {
	const source = await readFile(path.join(repoRoot, file), "utf8");
	const at = source.indexOf(anchor);
	if (at < 0) throw new Error(`${file}: ${anchor} not found`);
	const start = source.indexOf("`", at) + 1;
	return JSON.parse(source.slice(start, source.indexOf("`", start)));
}

/** value as Go's json.Marshal writes a decoded map: keys sorted at every level. */
function goMarshalled(value: any): any {
	if (Array.isArray(value)) return value.map(goMarshalled);
	if (typeof value !== "object" || value === null) return value;
	return Object.fromEntries(
		Object.keys(value)
			.sort()
			.map((key) => [key, goMarshalled(value[key])]),
	);
}

const j = (value: unknown) => JSON.stringify(value);

/** A case for one subschema at the required property v. */
function at(name: string, schema: unknown, value: unknown): ValidationCase {
	return { name, schema: { type: "object", properties: { v: schema }, required: ["v"] }, arguments: j({ v: value }) };
}

/** A case for one subschema at the required property v, with v's JSON text written out. */
function atText(name: string, schema: unknown, valueText: string): ValidationCase {
	return { name, schema: { type: "object", properties: { v: schema }, required: ["v"] }, arguments: `{"v":${valueText}}` };
}

async function validationCases(Type: any, StringEnum: any): Promise<ValidationCase[]> {
	const string = { type: "string" };
	const number = { type: "number" };
	const integer = { type: "integer" };
	const boolean = { type: "boolean" };
	const nul = { type: "null" };

	const coercion: ValidationCase[] = [
		// Pass 2, by type.
		at("string-from-integer", string, 42),
		at("string-from-boolean", string, true),
		at("string-from-null", string, null),
		at("string-from-large-number", string, 1e21),
		at("string-from-small-number", string, 0.000001),
		at("string-from-tiny-number", string, 1e-7),
		atText("string-from-negative-zero", string, "-0"),
		at("string-rejects-object", string, {}),
		at("string-rejects-array", string, [1]),
		at("number-from-string", number, "42"),
		at("number-from-empty-string", number, ""),
		at("number-from-padded-string", number, " 7\n"),
		at("number-from-nbsp-padded-string", number, " 5 "),
		at("number-from-hex", number, "0x1F"),
		at("number-from-binary", number, "0b101"),
		at("number-from-octal", number, "0o17"),
		at("number-from-exponent", number, "1e3"),
		at("number-from-leading-dot", number, ".5"),
		at("number-from-trailing-dot", number, "5."),
		at("number-from-plus", number, "+5"),
		at("number-from-negative-exponent", number, "-1.5e-3"),
		at("number-from-true-text", number, "TRUE"),
		at("number-from-false-text", number, "false"),
		at("number-from-boolean", number, true),
		at("number-from-null", number, null),
		at("number-from-bigint-text", number, "12n"),
		at("number-rejects-word", number, "abc"),
		at("number-rejects-infinity", number, "Infinity"),
		at("number-rejects-overflow", number, "1e400"),
		at("number-rejects-separator", number, "1_000"),
		at("number-rejects-signed-hex", number, "-0x10"),
		at("number-rejects-bare-prefix", number, "0x"),
		at("integer-truncates-text", integer, "3.5"),
		at("integer-truncates-number", integer, 3.5),
		at("integer-truncates-negative", integer, "-2.7"),
		at("integer-from-exponent", integer, "1e2"),
		at("integer-from-hex", integer, "0x10"),
		at("integer-from-boolean", integer, true),
		at("integer-from-null", integer, null),
		at("integer-rejects-word", integer, "x"),
		at("boolean-from-upper-text", boolean, "TRUE"),
		at("boolean-from-mixed-text", boolean, "False"),
		at("boolean-from-one-text", boolean, "1"),
		at("boolean-from-zero-text", boolean, "0"),
		at("boolean-from-one", boolean, 1),
		at("boolean-from-zero", boolean, 0),
		at("boolean-from-null", boolean, null),
		at("boolean-rejects-two", boolean, 2),
		at("boolean-rejects-yes", boolean, "yes"),
		at("boolean-rejects-empty", boolean, ""),
		at("null-from-text", nul, "null"),
		at("null-from-upper-text", nul, "NULL"),
		at("null-from-undefined-text", nul, "undefined"),
		at("null-from-empty", nul, ""),
		at("null-from-zero-text", nul, "0"),
		at("null-from-zero", nul, 0),
		at("null-from-false", nul, false),
		at("null-rejects-nil", nul, "nil"),
		at("null-rejects-one", nul, 1),
		at("array-wraps-string", { type: "array", items: string }, "a"),
		at("array-wraps-and-converts", { type: "array", items: number }, "5"),
		at("array-wraps-without-items", { type: "array" }, 5),
		at("array-wraps-object", { type: "array", items: { type: "object" } }, { a: 1 }),
		at("array-wraps-null", { type: "array", items: string }, null),
		at("array-converts-items", { type: "array", items: number }, ["1", 2, "x"]),
		at("tuple-never-wraps", { type: "array", prefixItems: [string] }, "a"),
		at("tuple-items-array", { type: "array", items: [string, number] }, [1, "2", "3"]),
		at("tuple-prefix-and-rest", { type: "array", prefixItems: [string], items: number }, ["1", "2", "3"]),
		at("array-typeless-items", { items: number }, "1"),
		at("object-properties", { type: "object", properties: { n: number, s: string } }, { n: "1", s: 2, x: "3" }),
		at(
			"object-pattern-properties",
			{ type: "object", patternProperties: { "^n_": number } },
			{ n_a: "1", s: "x" },
		),
		at(
			"object-additional-converts-undeclared-only",
			{ type: "object", properties: { a: string }, additionalProperties: number },
			{ a: 5, b: "6" },
		),
		at("object-typeless-properties", { properties: { x: number } }, { x: "1" }),
		at("const-number-from-text", { const: 5 }, "5"),
		at("const-text-from-number", { const: "5" }, 5),
		at("const-boolean-from-text", { const: true }, "true"),
		at("const-null-keeps-text", { const: null }, "null"),
		at("const-mismatch", { const: 5 }, "6"),
		at("const-object-mismatch", { const: { a: 1 } }, { a: 2 }),
		at("enum-number-from-text", { enum: [1, 2, 3] }, "2"),
		at("enum-text-from-number", { enum: ["1", "2"] }, 1),
		at("enum-boolean-from-text", { enum: [true, "x"] }, "true"),
		at("enum-null-member", { enum: [null, 1] }, "1"),
		at("enum-mismatch", { type: "string", enum: ["a", "b"] }, "c"),
		at(
			"enum-long-list",
			{ enum: Array.from({ length: 25 }, (_, index) => `v${index}`) },
			"nope",
		),
		at("any-of-first-converting-branch", { anyOf: [number, boolean] }, "true"),
		at("any-of-boolean-first", { anyOf: [boolean, number] }, "true"),
		at("any-of-keeps-accepted", { anyOf: [string, number] }, 5),
		at("any-of-no-branch", { anyOf: [integer, boolean] }, "x"),
		at("one-of-both-match", { oneOf: [number, integer] }, 5),
		at("one-of-ambiguous-conversion", { oneOf: [number, integer] }, "5"),
		at("all-of-converts-then-checks", { allOf: [string, { maxLength: 3 }] }, 1234),
		at(
			"union-node-with-properties",
			{ anyOf: [{ required: ["a"] }, { required: ["b"] }], properties: { a: number } },
			{ a: "5" },
		),
		// Pass 3, by type arrays.
		at("type-array-integer-from-text", { type: ["integer", "null"] }, "5"),
		at("type-array-null-from-empty", { type: ["integer", "null"] }, ""),
		at("type-array-rejects-word", { type: ["integer", "null"] }, "x"),
		at("type-array-string-from-boolean", { type: ["string", "number"] }, true),
		at("type-array-keeps-member", { type: ["number", "string"] }, "5"),
		at("type-array-boolean-from-text", { type: ["boolean", "null"] }, "false"),
		at("type-array-null-to-empty-string", { type: ["string", "boolean"] }, null),
		at("type-array-keeps-null", { type: ["number", "null"] }, null),
		at("type-array-string-from-fraction", { type: ["integer", "string"] }, 2.5),
		at("type-array-in-union", { anyOf: [{ type: ["integer", "null"] }, boolean] }, "5"),
		// Pass 1, optional nulls.
		{ name: "optional-null-dropped", schema: { type: "object", properties: { a: string } }, arguments: j({ a: null, b: null }) },
		{
			name: "required-null-becomes-text",
			schema: { type: "object", properties: { a: string }, required: ["a"] },
			arguments: j({ a: null }),
		},
		{
			name: "nullable-null-kept",
			schema: { type: "object", properties: { a: { anyOf: [string, nul] } } },
			arguments: j({ a: null }),
		},
		{
			name: "type-array-null-kept",
			schema: { type: "object", properties: { a: { type: ["string", "null"] } } },
			arguments: j({ a: null }),
		},
		{
			name: "openapi-nullable-ignored",
			schema: { type: "object", properties: { a: { type: "string", nullable: true }, b: { type: "string", nullable: true } }, required: ["b"] },
			arguments: j({ a: null, b: null }),
		},
		{
			name: "optional-null-unconstrained-kept",
			schema: { type: "object", properties: { a: {} } },
			arguments: j({ a: null }),
		},
		{
			name: "optional-null-object-dropped",
			schema: { type: "object", properties: { opts: { type: "object" }, e: { enum: ["x"] }, c: { const: 1 } } },
			arguments: j({ opts: null, e: null, c: null }),
		},
		{
			name: "optional-null-in-items",
			schema: { type: "object", properties: { list: { type: "array", items: { type: "object", properties: { a: string, b: number } } } } },
			arguments: j({ list: [{ a: null, b: 1 }] }),
		},
		{
			name: "null-item-converted",
			schema: { type: "object", properties: { list: { type: "array", items: string } } },
			arguments: j({ list: ["a", null] }),
		},
		{
			name: "optional-null-in-additional-value",
			schema: { type: "object", additionalProperties: { type: "object", properties: { o: number } } },
			arguments: j({ k: { o: null } }),
		},
		{
			name: "optional-null-in-pattern-value",
			schema: { type: "object", patternProperties: { "^x": { type: "object", properties: { o: number } } } },
			arguments: j({ x1: { o: null } }),
		},
		{
			name: "additional-null-key-kept",
			schema: { type: "object", additionalProperties: number },
			arguments: j({ k: null }),
		},
		{
			name: "optional-null-ref-property",
			schema: { type: "object", properties: { a: { $ref: "#/$defs/s" } }, $defs: { s: string } },
			arguments: j({ a: null }),
		},
		{
			name: "optional-null-behind-ref",
			schema: {
				type: "object",
				properties: { a: { $ref: "#/$defs/A" } },
				$defs: { A: { type: "object", properties: { x: string } } },
			},
			arguments: j({ a: { x: null } }),
		},
		// Recursion through $ref.
		{
			name: "recursive-wrap-cut",
			schema: { $defs: { n: { type: "array", items: { $ref: "#/$defs/n" } } }, $ref: "#/$defs/n" },
			arguments: "5",
		},
	];

	const keywords: ValidationCase[] = [
		{ name: "root-not-object", schema: { type: "object", properties: { a: string } }, arguments: "[1]" },
		at("type-integer-got-number", integer, 1.5),
		at("type-list-got-object", { type: ["string", "array"] }, { a: 1 }),
		at("type-unknown-name", { type: "date" }, "2026-01-01"),
		at("min-length-code-points", { type: "string", minLength: 3 }, "\u{1F600}\u{1F600}"),
		at("max-length", { type: "string", maxLength: 1 }, "ab"),
		at("max-length-code-points-ok", { type: "string", maxLength: 2 }, "\u{1F600}\u{1F600}"),
		at("pattern", { type: "string", pattern: "^[a-z]+$" }, "ABC"),
		at("pattern-unanchored-ok", { type: "string", pattern: "[0-9]" }, "ab1"),
		at("pattern-unicode-property", { type: "string", pattern: "^\\p{Lu}" }, "abc"),
		at("minimum", { type: "number", minimum: 1 }, 0),
		at("maximum", { type: "number", maximum: 10.5 }, 11),
		at("exclusive-minimum", { type: "number", exclusiveMinimum: 0 }, 0),
		at("exclusive-maximum", { type: "number", exclusiveMaximum: 1 }, 1),
		at("multiple-of", { type: "number", multipleOf: 3 }, 10),
		at("multiple-of-decimal-ok", { type: "number", multipleOf: 0.01 }, 0.07),
		at("multiple-of-decimal", { type: "number", multipleOf: 0.01 }, 0.075),
		at("bounds-without-type", { minimum: 5, maxLength: 1 }, "ab"),
		{
			name: "required-in-order",
			schema: { type: "object", properties: { a: string, b: string, c: string }, required: ["c", "a"] },
			arguments: j({ b: "x" }),
		},
		{
			name: "required-undeclared",
			schema: { type: "object", required: ["z"] },
			arguments: j({}),
		},
		{
			name: "additional-false-in-key-order",
			schema: { type: "object", properties: { a: string }, additionalProperties: false },
			arguments: `{"z":1,"10":2,"a":"x","2":3,"y":4}`,
		},
		{
			name: "additional-schema",
			schema: { type: "object", properties: { a: string }, additionalProperties: { type: "number", minimum: 0 } },
			arguments: j({ a: "x", b: -1, c: "y" }),
		},
		{
			name: "additional-true",
			schema: { type: "object", properties: { a: string }, additionalProperties: true },
			arguments: j({ a: 1, b: { c: 2 } }),
		},
		{
			name: "additional-with-patterns",
			schema: { type: "object", patternProperties: { "^x-": string }, additionalProperties: false },
			arguments: j({ "x-a": 1, y: 2 }),
		},
		{
			name: "property-names",
			schema: { type: "object", propertyNames: { pattern: "^[a-z]+$" } },
			arguments: j({ ok: 1, Bad: 2 }),
		},
		{ name: "min-properties", schema: { type: "object", minProperties: 2 }, arguments: j({ a: 1 }) },
		{ name: "max-properties", schema: { type: "object", maxProperties: 1 }, arguments: j({ a: 1, b: 2 }) },
		{
			name: "dependent-required",
			schema: { type: "object", dependentRequired: { card: ["billing", "name"] } },
			arguments: j({ card: "1", name: "x" }),
		},
		{
			name: "dependencies-draft7",
			schema: { type: "object", dependencies: { card: ["billing"], ship: { required: ["address"] } } },
			arguments: j({ card: "1", ship: true }),
		},
		{
			name: "dependent-schemas",
			schema: { type: "object", dependentSchemas: { a: { properties: { b: number } } } },
			arguments: j({ a: 1, b: "x" }),
		},
		at("min-items", { type: "array", minItems: 2 }, [1]),
		at("max-items", { type: "array", maxItems: 1 }, [1, 2]),
		at("unique-items", { type: "array", uniqueItems: true }, [{ a: 1 }, { a: 1 }]),
		at("contains", { type: "array", contains: string }, [1, 2]),
		at("min-contains", { type: "array", contains: string, minContains: 2 }, ["a", 1]),
		at("max-contains", { type: "array", contains: string, maxContains: 1 }, ["a", "b"]),
		at("min-max-contains", { type: "array", contains: string, minContains: 2, maxContains: 3 }, ["a", 1]),
		at("not", { not: string }, "x"),
		at("if-then", { if: { type: "string" }, then: { minLength: 2 }, else: { minimum: 5 } }, "a"),
		at("if-else", { if: { type: "string" }, then: { minLength: 2 }, else: { minimum: 5 } }, 1),
		at("false-schema", false, 1),
		{
			name: "false-property",
			schema: { type: "object", properties: { a: false, b: true } },
			arguments: j({ a: 1, b: 2 }),
		},
		at(
			"any-of-blames-declared-branch",
			{
				anyOf: [
					{ type: "object", properties: { a: string }, required: ["a"] },
					{ type: "object", properties: { b: number, c: number }, required: ["b", "c"] },
				],
			},
			{ b: "x", c: 1 },
		),
		at(
			"any-of-blames-deepest-error",
			{
				anyOf: [
					{ type: "object", required: ["a"] },
					{ type: "object", properties: { list: { type: "array", items: string } } },
				],
			},
			{ list: [1, { b: 1 }] },
		),
		at("any-of-literals", Type.Union([Type.Literal("a"), Type.Literal("b")]), "c"),
		at("string-enum-helper", StringEnum(["add", "subtract"]), "divide"),
		at(
			"tagged-union-wrong-tag",
			{
				anyOf: [
					{ type: "object", properties: { kind: { const: "text" }, text: string }, required: ["kind", "text"] },
					{ type: "object", properties: { kind: { const: "image" }, width: integer }, required: ["kind", "width"] },
				],
			},
			{ kind: "video" },
		),
		at(
			"tagged-union-missing-tag",
			{
				oneOf: [
					{ type: "object", properties: { kind: { const: "text" }, text: string }, required: ["kind", "text"] },
					{ type: "object", properties: { kind: { const: "image" }, width: integer }, required: ["kind", "width"] },
				],
			},
			{ width: 3 },
		),
		at(
			"tagged-union-branch-error",
			{
				oneOf: [
					{ type: "object", properties: { kind: { const: "text" }, text: string }, required: ["kind", "text"] },
					{
						type: "object",
						properties: { kind: { const: "image" }, width: { type: "integer", minimum: 0 } },
						required: ["kind", "width"],
					},
				],
			},
			{ kind: "image", width: -1 },
		),
		{
			name: "nested-path",
			schema: {
				type: "object",
				properties: { a: { type: "object", properties: { b: { type: "array", items: { type: "object", properties: { c: number }, required: ["c", "d"] } } } } },
			},
			arguments: j({ a: { b: [{ c: 1, d: 1 }, { c: "x" }] } }),
		},
		{
			name: "escaped-pointer-segments",
			schema: {
				type: "object",
				properties: { "a/b": number, "m~n": number, "x.y": number, "": number },
				required: ["", "q/r"],
			},
			arguments: j({ "a/b": "x", "m~n": "y", "x.y": "z" }),
		},
		{
			name: "tool-name-verbatim",
			tool: "mcp__srv__dé\"l",
			schema: { type: "object", required: ["a"] },
			arguments: j({}),
		},
		{
			name: "arguments-error",
			schema: { type: "object", properties: { a: string }, required: ["a"] },
			arguments: j({}),
			argumentsError: "Unexpected end of JSON input",
		},
		{ name: "schema-true", schema: true, arguments: j({ a: [1, "2"] }) },
		{ name: "malformed-subschema", schema: { type: "object", properties: { a: { type: "string" }, b: 5 } }, arguments: j({ a: 1 }) },
		{ name: "malformed-root", schema: null, arguments: j({}) },
		{
			name: "received-arguments-rendering",
			schema: { type: "object", required: ["zzz"] },
			arguments: `{"b":1,"2":"x","1":true,"a":1.50,"e":1E2,"s":"\\u00e9<>&\\u2028\\n\\u0001\\"","n":-0,"big":12345678901234567890,"tiny":0.0000001,"dup":1,"o":{},"l":[],"nested":{"k":[1,{"z":null}]},"dup":2}`,
		},
	];

	const node = Type.Cyclic(
		{ Node: Type.Object({ name: Type.String(), children: Type.Array(Type.Ref("Node")) }) },
		"Node",
	);
	const refs: ValidationCase[] = [
		{
			name: "ref-defs-converts",
			schema: { type: "object", properties: { a: { $ref: "#/$defs/n" } }, $defs: { n: number } },
			arguments: j({ a: "5" }),
		},
		{
			name: "ref-definitions",
			schema: { type: "object", properties: { a: { $ref: "#/definitions/n" } }, definitions: { n: { type: "number", minimum: 10 } } },
			arguments: j({ a: "5" }),
		},
		{ name: "ref-typebox-cyclic", schema: node, arguments: j({ name: "root", children: [{ name: 1, children: [{ name: "c" }] }] }) },
		{
			name: "ref-recursive-root",
			schema: { type: "object", properties: { name: string, children: { type: "array", items: { $ref: "#" } } }, required: ["name"] },
			arguments: j({ name: "a", children: [{ children: [{ name: 5 }] }] }),
		},
		{
			name: "ref-anchor",
			schema: { type: "object", properties: { a: { $ref: "#num" } }, $defs: { n: { $anchor: "num", type: "number" } } },
			arguments: j({ a: "x" }),
		},
		{
			name: "ref-relative-id",
			schema: {
				$id: "https://example.com/root.json",
				type: "object",
				properties: { a: { $ref: "item.json" } },
				$defs: { item: { $id: "item.json", type: "integer" } },
			},
			arguments: j({ a: "4.2" }),
		},
		{
			name: "ref-dangling-local",
			schema: { type: "object", properties: { cfg: { $ref: "#/$defs/Missing" }, q: string }, required: ["q"] },
			arguments: j({ cfg: { a: 1 }, q: 5 }),
		},
		{
			name: "ref-dangling-absent-ok",
			schema: { type: "object", properties: { cfg: { $ref: "#/$defs/Missing" }, q: string } },
			arguments: j({ q: 5 }),
		},
		{
			name: "ref-dangling-remote",
			schema: { type: "object", properties: { a: { $ref: "https://example.com/schemas/x.json" } } },
			arguments: j({ a: 1 }),
		},
		{
			name: "ref-dangling-anchor",
			schema: { type: "object", properties: { a: { $ref: "#nowhere" } } },
			arguments: j({ a: 1 }),
		},
		{
			name: "ref-percent-encoded",
			schema: { type: "object", properties: { a: { $ref: "#/$defs/a%20b" } }, $defs: { "a b": number } },
			arguments: j({ a: "x" }),
		},
		{
			name: "ref-escaped-pointer",
			schema: { type: "object", properties: { a: { $ref: "#/$defs/a~1b~0c" } }, $defs: { "a/b~c": number } },
			arguments: j({ a: "1" }),
		},
		{
			name: "ref-to-false",
			schema: { type: "object", properties: { a: { $ref: "#/$defs/never" } }, $defs: { never: false } },
			arguments: j({ a: 1 }),
		},
		{
			name: "ref-with-siblings",
			schema: { type: "object", properties: { a: { $ref: "#/$defs/s", maxLength: 2 } }, $defs: { s: string } },
			arguments: j({ a: 1234 }),
		},
		{
			name: "ref-root-to-defs",
			schema: { $ref: "#/$defs/args", $defs: { args: { type: "object", properties: { n: integer, o: string }, required: ["n"] } } },
			arguments: j({ n: "7", o: null }),
		},
		{
			name: "ref-in-union-branch",
			schema: {
				type: "object",
				properties: { v: { anyOf: [{ $ref: "#/$defs/obj" }, string] } },
				$defs: { obj: { type: "object", properties: { k: number }, required: ["k"] } },
			},
			arguments: j({ v: { k: "x" } }),
		},
	];

	const formatCases: [string, string, string][] = [
		["date-time", "2026-11-02T10:00:00Z", "ok"],
		["date-time", "2026-11-02t10:00:00.123+01:00", "ok-lower"],
		["date-time", "2026-11-02 10:00:00Z", "space"],
		["date-time", "2026-11-02T10:00:00", "no-offset"],
		["date", "2024-02-29", "leap"],
		["date", "2023-02-29", "not-leap"],
		["time", "23:59:60Z", "leap-second"],
		["time", "12:00:00", "no-offset"],
		["time", "24:00:00Z", "hour"],
		["email", "a.b+c@example.com", "ok"],
		["email", "not-an-email", "bad"],
		["uri", "https://example.com/a?b=c#d", "ok"],
		["uri", "mailto:x@y.z", "mailto"],
		["uri", "example.com/a", "relative"],
		["uri-reference", "../a/b?c", "ok"],
		["uri-reference", "a b", "space"],
		["url", "https://example.com/x", "ok"],
		["url", "http:foo", "lenient"],
		["url", "not a url", "bad"],
		["url", "http://exa mple.com", "space-host"],
		["url", "https://[::1]:8080/x", "ipv6-port"],
		["url", "http://example.com:99999", "bad-port"],
		["url", "ftp://", "empty-host"],
		["url", "urn:isbn:0451450523", "urn"],
		["uuid", "123e4567-e89b-12d3-a456-426614174000", "ok"],
		["uuid", "123e4567e89b12d3a456426614174000", "bad"],
		["ipv4", "192.168.0.1", "ok"],
		["ipv4", "256.1.1.1", "bad"],
		["ipv6", "2001:db8::1", "ok"],
		["ipv6", "2001:db8::g", "bad"],
		["hostname", "api.example.com", "ok"],
		["hostname", "-bad.example.com", "hyphen"],
		["hostname", "a..b", "empty-label"],
		["hostname", `${"a".repeat(64)}.com`, "long-label"],
		["hostname", "ab--c.com", "reserved-hyphens"],
		["duration", "P3DT4H", "ok"],
		["duration", "PT", "bad"],
		["json-pointer", "/a/~1b", "ok"],
		["json-pointer", "a/b", "bad"],
		["json-pointer-uri-fragment", "#/a%20b", "ok"],
		["json-pointer-uri-fragment", "#a", "bad"],
		["relative-json-pointer", "1/a", "ok"],
		["relative-json-pointer", "/a", "bad"],
		["regex", "^a+$", "ok"],
		["regex", "(", "bad"],
		["uri-template", "https://x.com/{id}{?q}", "ok"],
		["uri-template", "https://x.com/{id", "bad"],
		["iri", "https://ex.com/ü", "ok"],
		["iri", "ü", "bad"],
		["iri-reference", "ü/path", "ok"],
		["iri-reference", "a\\b", "bad"],
		["custom-thing", "anything", "unknown"],
	];
	const formats: ValidationCase[] = [
		...formatCases.map(([format, value, label]) => at(`format-${format}-${label}`, { type: "string", format }, value)),
		at("format-on-number-ignored", { format: "date-time" }, 5),
		at("format-after-conversion", { type: "string", format: "date" }, 20260101),
	];

	const readSchema = await goSchema("agent/tools/read.go", "readSchema = ");
	const bashSchema = await goSchema("agent/tools/bash.go", "bashSchema = ");
	const editSchema = await goSchema("agent/tools/edit.go", "editSchema = ");
	const writeSchema = await goSchema("agent/tools/write.go", "writeSchema = ");
	const grepSchema = await goSchema("agent/tools/grep.go", "grepSchema = ");
	const findSchema = await goSchema("agent/tools/find.go", "findSchema = ");
	const lsSchema = await goSchema("agent/tools/ls.go", "lsSchema = ");
	const rememberSchema = await goSchema("plugins/memory/agent/memory.go", "rememberSchema = ");
	const recallSchema = await goSchema("plugins/memory/agent/memory.go", "recallSchema   = ");
	const replaceSchema = await goSchema("plugins/memory/agent/memory.go", "replaceSchema  = ");
	const forgetSchema = await goSchema("plugins/memory/agent/memory.go", "forgetSchema   = ");
	const todoSchema = await goSchema("plugins/tasks/tasks.go", "todoSchema = ");
	const webSearchSchema = await goSchema("plugins/websearch/websearch.go", "webSearchSchema = ");
	const fetchSchema = await goSchema("plugins/websearch/websearch.go", "fetchSchema     = ");
	const stopSchema = await goSchema("plugins/jobs/jobs.go", "stopSchema = ");
	const subagentSchema = await goSchema("plugins/subagents/subagents.go", "subagentSchema = ");
	const zoomSchema = await goSchema("plugins/memtree/memtree.go", "zoomSchema = ");
	const dateSchema = await goSchema("plugins/memtree/memtree.go", "dateSchema = ");
	const questionSchema = await goSchema("plugins/questions/questions.go", "var schema = ");
	const agentsSchema = await goSchema("plugins/bridgeagents/bridgeagents.go", "var schema = ");
	const sendFileSchema = await goSchema("chat/attachment.go", 'Name: "send_file"');
	const bridgeCallSchema = await goSchema("agent/bridge/tool/tool.go", 'Name: "bridge_call"');
	const toolSearchSchema = await goSchema("plugins/mcp/tool_search.go", "Parameters:");
	// plugins/jobs extends bash's schema through a decoded map, so Go re-encodes it with sorted keys.
	const jobsBashSchema = goMarshalled({
		...bashSchema,
		properties: {
			...bashSchema.properties,
			run_in_background: { type: "boolean", description: "Run as a background job and return at once with its ID and log file." },
			monitor: { type: "boolean", description: "Like run_in_background, and also report the lines the command prints." },
		},
	});
	// plugins/subagents schemaWithExternal with offered models.
	const model = { description: "Model for the child; defaults to yours.", enum: ["lab/small", "lab/big"], type: "string" };
	const subagentWithModels = JSON.parse(JSON.stringify(subagentSchema));
	subagentWithModels.properties = Object.fromEntries(
		Object.entries(subagentWithModels.properties).flatMap(([key, value]) => (key === "tasks" ? [["model", model], [key, value]] : [[key, value]])),
	);
	subagentWithModels.properties.tasks.items.properties = { model, ...subagentWithModels.properties.tasks.items.properties };

	const orb: ValidationCase[] = [
		{ name: "read-valid", tool: "read", schema: readSchema, arguments: j({ path: "a.txt", offset: 10, limit: 20 }) },
		{ name: "read-numeric-strings", tool: "read", schema: readSchema, arguments: j({ path: "a.txt", offset: "10", limit: "20" }) },
		{ name: "read-null-optionals", tool: "read", schema: readSchema, arguments: j({ path: "a.txt", offset: null, limit: null }) },
		{ name: "read-bad-offset", tool: "read", schema: readSchema, arguments: j({ path: "a.txt", offset: "ten" }) },
		{ name: "read-missing-path", tool: "read", schema: readSchema, arguments: j({ file: "a.txt" }) },
		{ name: "read-path-number", tool: "read", schema: readSchema, arguments: j({ path: 123 }) },
		{ name: "bash-timeout-string", tool: "bash", schema: bashSchema, arguments: j({ command: "ls", timeout: "30" }) },
		{ name: "bash-timeout-null", tool: "bash", schema: bashSchema, arguments: j({ command: "ls", timeout: null }) },
		{ name: "bash-missing-command", tool: "bash", schema: bashSchema, arguments: j({ cmd: "ls" }) },
		{ name: "bash-command-array", tool: "bash", schema: bashSchema, arguments: j({ command: ["ls", "-la"] }) },
		{ name: "jobs-bash-background-text", tool: "bash", schema: jobsBashSchema, arguments: j({ command: "make", run_in_background: "true" }) },
		{ name: "jobs-bash-bad-monitor", tool: "bash", schema: jobsBashSchema, arguments: j({ command: "make", monitor: "yes", timeout: "x" }) },
		{
			name: "edit-valid",
			tool: "edit",
			schema: editSchema,
			arguments: j({ path: "a.go", edits: [{ oldText: "a", newText: "b" }] }),
		},
		{ name: "edit-single-edit-object", tool: "edit", schema: editSchema, arguments: j({ path: "a.go", edits: { oldText: "a", newText: "b" } }) },
		{
			name: "edit-stringified-edits",
			tool: "edit",
			schema: editSchema,
			arguments: j({ path: "a.go", edits: j([{ oldText: "a", newText: "b" }]) }),
		},
		{ name: "edit-missing-old-text", tool: "edit", schema: editSchema, arguments: j({ path: "a.go", edits: [{ newText: "b" }, { oldText: "c" }] }) },
		{ name: "edit-number-new-text", tool: "edit", schema: editSchema, arguments: j({ path: "a.go", edits: [{ oldText: "1", newText: 2 }] }) },
		{ name: "write-number-content", tool: "write", schema: writeSchema, arguments: j({ path: "n.txt", content: 42 }) },
		{ name: "write-null-content", tool: "write", schema: writeSchema, arguments: j({ path: "n.txt", content: null }) },
		{ name: "write-object-content", tool: "write", schema: writeSchema, arguments: j({ path: "n.json", content: { a: 1 } }) },
		{
			name: "grep-coercions",
			tool: "grep",
			schema: grepSchema,
			arguments: j({ pattern: "x", ignoreCase: "true", context: "2", literal: "0", limit: null }),
		},
		{ name: "grep-bad-literal", tool: "grep", schema: grepSchema, arguments: j({ pattern: "x", literal: "yes" }) },
		{ name: "find-limit-string", tool: "find", schema: findSchema, arguments: j({ pattern: "*.go", limit: "50" }) },
		{ name: "ls-empty", tool: "ls", schema: lsSchema, arguments: j({}) },
		{ name: "ls-null-path", tool: "ls", schema: lsSchema, arguments: j({ path: null, limit: "5" }) },
		{ name: "remember-tag-string", tool: "remember", schema: rememberSchema, arguments: j({ content: "x", tags: "pref" }) },
		{ name: "remember-bad-target", tool: "remember", schema: rememberSchema, arguments: j({ content: "x", target: "USER" }) },
		{ name: "remember-missing-content", tool: "remember", schema: rememberSchema, arguments: j({ target: "user" }) },
		{ name: "recall-limit-string", tool: "recall", schema: recallSchema, arguments: j({ query: "q", limit: "5" }) },
		{ name: "recall-limit-zero", tool: "recall", schema: recallSchema, arguments: j({ limit: 0 }) },
		{ name: "recall-limit-too-large", tool: "recall", schema: recallSchema, arguments: j({ limit: 150 }) },
		{ name: "recall-limit-fraction", tool: "recall", schema: recallSchema, arguments: j({ limit: 2.7 }) },
		{ name: "recall-limit-word", tool: "recall", schema: recallSchema, arguments: j({ limit: "all" }) },
		{ name: "replace-missing", tool: "replace", schema: replaceSchema, arguments: j({ target: "memory", content: "x" }) },
		{ name: "forget-tag-string", tool: "forget", schema: forgetSchema, arguments: j({ query: "q", tags: "x" }) },
		{ name: "todo-valid", tool: "todo", schema: todoSchema, arguments: j({ items: [{ text: "a", status: "pending" }] }) },
		{ name: "todo-single-item", tool: "todo", schema: todoSchema, arguments: j({ items: { text: "a", status: "done" } }) },
		{ name: "todo-bad-status", tool: "todo", schema: todoSchema, arguments: j({ items: [{ text: "a", status: "in-progress" }] }) },
		{ name: "web-search-number", tool: "web_search", schema: webSearchSchema, arguments: j({ query: 2026 }) },
		{ name: "fetch-missing-url", tool: "fetch_content", schema: fetchSchema, arguments: j({ link: "https://x" }) },
		{ name: "stop-job-number", tool: "stop_job", schema: stopSchema, arguments: j({ job: 12 }) },
		{
			name: "subagent-single-task-object",
			tool: "subagent",
			schema: subagentSchema,
			arguments: j({ mode: "parallel", tasks: { task: "a", agent: "scout" } }),
		},
		{
			name: "subagent-too-many-tasks",
			tool: "subagent",
			schema: subagentSchema,
			arguments: j({ mode: "parallel", tasks: Array.from({ length: 33 }, () => ({ task: "a" })) }),
		},
		{ name: "subagent-bad-agent", tool: "subagent", schema: subagentSchema, arguments: j({ mode: "single", task: "a", agent: "Worker" }) },
		{ name: "subagent-tools-string", tool: "subagent", schema: subagentSchema, arguments: j({ mode: "single", task: "a", tools: "read" }) },
		{
			name: "subagent-model-outside-offer",
			tool: "subagent",
			schema: subagentWithModels,
			arguments: j({ mode: "single", task: "answer", model: "lab/huge" }),
		},
		{ name: "memtree-zoom-strings", tool: "zoom", schema: zoomSchema, arguments: j({ id: "3", n: "2" }) },
		{ name: "memtree-zoom-bounds", tool: "zoom", schema: zoomSchema, arguments: j({ id: -1, n: 0 }) },
		{ name: "memtree-date-fraction", tool: "date", schema: dateSchema, arguments: j({ id: 1.5 }) },
		{
			name: "question-valid",
			tool: "question",
			schema: questionSchema,
			arguments: j({ questions: [{ id: "a", question: "Which?", multi_select: "false", options: [{ label: "x" }] }] }),
		},
		{ name: "question-single-object", tool: "question", schema: questionSchema, arguments: j({ questions: { id: "a", question: "Which?" } }) },
		{ name: "question-empty", tool: "question", schema: questionSchema, arguments: j({ questions: [] }) },
		{
			name: "question-unknown-fields",
			tool: "question",
			schema: questionSchema,
			arguments: j({ questions: [{ id: "a", question: "Q", options: [{ description: "d", value: 1 }], extra: true }], note: "n" }),
		},
		{
			name: "question-too-many",
			tool: "question",
			schema: questionSchema,
			arguments: j({ questions: Array.from({ length: 5 }, (_, index) => ({ id: `q${index}`, question: "Q" })) }),
		},
		{ name: "agents-valid", tool: "agents", schema: agentsSchema, arguments: j({ action: "send", id: "x", text: "hi" }) },
		{ name: "agents-bad-action", tool: "agents", schema: agentsSchema, arguments: j({ action: "LIST" }) },
		{ name: "send-file-number", tool: "send_file", schema: sendFileSchema, arguments: j({ path: 5 }) },
		{ name: "send-file-missing", tool: "send_file", schema: sendFileSchema, arguments: j({}) },
		{
			name: "bridge-call-valid",
			tool: "bridge_call",
			schema: bridgeCallSchema,
			arguments: j({ peer_id: "p", call: { instance_id: "i", service: "orb.instance/1", method: "inspect", args: {} } }),
		},
		{
			name: "bridge-call-errors",
			tool: "bridge_call",
			schema: bridgeCallSchema,
			arguments: j({
				peer_id: "p",
				call: { instance_id: "i", service: "orb.instance/2", method: "Prompt", expected: { registration_generation: "1" }, extra: 1 },
			}),
		},
		{ name: "tool-search-limit-string", tool: "tool_search", schema: toolSearchSchema, arguments: j({ query: "q", limit: "3" }) },
		{ name: "tool-search-missing-query", tool: "tool_search", schema: toolSearchSchema, arguments: j({ limit: 3 }) },
	];

	// Ordalie-back's agentengine tools (apps/api/app/features/ai/agentengine), descriptions shortened.
	const submitAbstract = {
		type: "object",
		required: ["found", "type", "value"],
		properties: {
			found: { type: "boolean", description: "true when the answer identifies a requested fact or explicit source finding." },
			type: { type: "string", enum: ["text", "boolean", "number", "date", "list"], description: "The value's type." },
			value: { type: "string", description: "Only the bare value matching type." },
		},
	};
	const execCode = {
		type: "object",
		properties: {
			title: { type: "string", description: "Step label shown to the user." },
			code: { type: "string", description: "JavaScript code to execute" },
		},
		required: ["code"],
		additionalProperties: false,
	};
	const requestUserInput = {
		type: "object",
		properties: {
			questions: {
				type: "array",
				description: "Questions to show the user. Prefer 1 and do not exceed 3.",
				items: {
					type: "object",
					properties: {
						id: { type: "string" },
						header: { type: "string" },
						question: { type: "string" },
						options: {
							type: "array",
							items: {
								type: "object",
								properties: { label: { type: "string" }, description: { type: "string" } },
								required: ["label", "description"],
								additionalProperties: false,
							},
						},
					},
					required: ["id", "header", "question", "options"],
					additionalProperties: false,
				},
			},
		},
		required: ["questions"],
		additionalProperties: false,
	};
	const submitDraft = {
		type: "object",
		properties: { completed: { type: "boolean" }, missing_information: { type: "array", items: { type: "string" } } },
		required: ["completed", "missing_information"],
		additionalProperties: false,
	};
	// template_fields_writer.go templateValuesSchema: json.Marshal of a map, keys sorted.
	const submitFields = goMarshalled({
		type: "object",
		additionalProperties: false,
		required: ["values", "missing_information"],
		properties: {
			values: {
				type: "object",
				additionalProperties: false,
				properties: {
					client_name: { type: "string", minLength: 1, description: "Client (text, inline)" },
					body: { type: "string", minLength: 1, description: "Corps (rich_text, block)" },
				},
				required: ["client_name", "body"],
			},
			missing_information: { type: "array", items: { type: "string" } },
		},
	});
	const ordalie: ValidationCase[] = [
		{ name: "ordalie-abstract-valid", tool: "submit_abstract", schema: submitAbstract, arguments: j({ found: true, type: "number", value: "42" }) },
		{ name: "ordalie-abstract-coerced", tool: "submit_abstract", schema: submitAbstract, arguments: j({ found: "false", type: "text", value: 42 }) },
		{ name: "ordalie-abstract-bad-type", tool: "submit_abstract", schema: submitAbstract, arguments: j({ found: true, type: "Text" }) },
		{ name: "ordalie-exec-null-title", tool: "exec_code", schema: execCode, arguments: j({ title: null, code: "1" }) },
		{ name: "ordalie-exec-errors", tool: "exec_code", schema: execCode, arguments: j({ title: "Lecture", language: "js" }) },
		{
			name: "ordalie-request-input-single",
			tool: "request_user_input",
			schema: requestUserInput,
			arguments: j({ questions: { id: "a", header: "H", question: "Q?", options: [{ label: "x", description: "d" }] } }),
		},
		{
			name: "ordalie-request-input-errors",
			tool: "request_user_input",
			schema: requestUserInput,
			arguments: j({ questions: [{ id: "a", header: "H", question: "Q?", options: [{ label: "x" }], multi: true }] }),
		},
		{ name: "ordalie-draft-coerced", tool: "submit_draft", schema: submitDraft, arguments: j({ completed: "true", missing_information: "none" }) },
		{ name: "ordalie-draft-null-list", tool: "submit_draft", schema: submitDraft, arguments: j({ completed: true, missing_information: null }) },
		{
			name: "ordalie-fields-valid",
			tool: "submit_draft",
			schema: submitFields,
			arguments: j({ values: { client_name: "A", body: "B" }, missing_information: [] }),
		},
		{
			name: "ordalie-fields-errors",
			tool: "submit_draft",
			schema: submitFields,
			arguments: j({ values: { body: "", date: "2026" }, missing_information: [] }),
		},
	];

	// Typical MCP server shapes: plain JSON Schema from zod, pydantic and hand-written servers.
	const githubIssues = {
		$schema: "http://json-schema.org/draft-07/schema#",
		type: "object",
		properties: {
			owner: { type: "string" },
			repo: { type: "string" },
			state: { type: "string", enum: ["open", "closed", "all"] },
			labels: { type: "array", items: { type: "string" } },
			since: { type: "string", format: "date-time" },
			per_page: { type: "integer", minimum: 1, maximum: 100 },
		},
		required: ["owner", "repo"],
		additionalProperties: false,
	};
	const fetchTool = {
		type: "object",
		properties: {
			url: { type: "string", format: "uri", description: "URL to fetch" },
			max_length: { type: "integer", exclusiveMinimum: 0, exclusiveMaximum: 1000000, default: 5000 },
			raw: { type: "boolean", default: false },
		},
		required: ["url"],
	};
	const calendar = {
		type: "object",
		properties: {
			summary: { type: "string", minLength: 1 },
			start: { type: "string", format: "date-time" },
			attendees: { type: "array", items: { type: "string", format: "email" } },
			reminders: { type: "array", items: { type: "integer" }, maxItems: 5 },
		},
		required: ["summary", "start"],
	};
	const pydantic = {
		properties: {
			query: { title: "Query", type: "string" },
			limit: { anyOf: [{ type: "integer" }, { type: "null" }], default: null, title: "Limit" },
			fields: { anyOf: [{ type: "array", items: { type: "string" } }, { type: "null" }], default: null, title: "Fields" },
		},
		required: ["query"],
		title: "searchArguments",
		type: "object",
	};
	const zodRefs = {
		type: "object",
		properties: {
			filter: { $ref: "#/definitions/Filter" },
			sort: { type: "string", enum: ["asc", "desc"] },
		},
		definitions: {
			Filter: {
				type: "object",
				properties: { field: { type: "string" }, value: { type: ["string", "number", "boolean"] } },
				required: ["field", "value"],
				additionalProperties: false,
			},
		},
		additionalProperties: false,
		$schema: "http://json-schema.org/draft-07/schema#",
	};
	const browserActions = {
		type: "object",
		properties: {
			action: {
				oneOf: [
					{ type: "object", properties: { type: { const: "click" }, selector: { type: "string" } }, required: ["type", "selector"] },
					{ type: "object", properties: { type: { const: "scroll" }, dy: { type: "number" } }, required: ["type", "dy"] },
				],
			},
		},
		required: ["action"],
	};
	const mcp: ValidationCase[] = [
		{
			name: "mcp-github-valid",
			tool: "github__list_issues",
			schema: githubIssues,
			arguments: j({ owner: "o", repo: "r", state: "open", labels: ["bug"], since: "2026-01-01T00:00:00Z", per_page: 30 }),
		},
		{
			name: "mcp-github-coerced",
			tool: "github__list_issues",
			schema: githubIssues,
			arguments: j({ owner: "o", repo: "r", labels: "bug", per_page: "30" }),
		},
		{
			name: "mcp-github-errors",
			tool: "github__list_issues",
			schema: githubIssues,
			arguments: j({ owner: "o", state: "OPEN", since: "yesterday", per_page: 500, page: 2 }),
		},
		{ name: "mcp-fetch-valid", tool: "fetch__fetch", schema: fetchTool, arguments: j({ url: "https://example.com", max_length: "1000", raw: "false" }) },
		{ name: "mcp-fetch-bad-url", tool: "fetch__fetch", schema: fetchTool, arguments: j({ url: "example.com", max_length: 0 }) },
		{
			name: "mcp-calendar-errors",
			tool: "calendar__create_event",
			schema: calendar,
			arguments: j({ summary: "", start: "2026-11-02T10:00", attendees: ["a@example.com", "bob"], reminders: ["10", 30] }),
		},
		{ name: "mcp-pydantic-nulls", tool: "search", schema: pydantic, arguments: j({ query: "q", limit: null, fields: "title" }) },
		{ name: "mcp-pydantic-limit-string", tool: "search", schema: pydantic, arguments: j({ query: 5, limit: "10" }) },
		{ name: "mcp-zod-ref-valid", tool: "db__query", schema: zodRefs, arguments: j({ filter: { field: "age", value: "42" }, sort: "asc" }) },
		{ name: "mcp-zod-ref-errors", tool: "db__query", schema: zodRefs, arguments: j({ filter: { field: "age", op: "gt" }, sort: "up" }) },
		{ name: "mcp-tagged-action", tool: "browser__act", schema: browserActions, arguments: j({ action: { type: "scroll", dy: "100" } }) },
		{ name: "mcp-tagged-action-wrong", tool: "browser__act", schema: browserActions, arguments: j({ action: { type: "hover" } }) },
	];

	return [...coercion, ...keywords, ...refs, ...formats, ...orb, ...ordalie, ...mcp];
}
