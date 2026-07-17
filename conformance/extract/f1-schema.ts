import { mkdir, writeFile } from "node:fs/promises";
import path from "node:path";
import { pathToFileURL } from "node:url";

export async function generateF1Schema(
	upstreamRoot: string,
	outputRoot: string,
	_upstreamCommit: string,
): Promise<void> {
	const typeboxURL = pathToFileURL(path.join(upstreamRoot, "node_modules/typebox/build/index.mjs")).href;
	const helpersURL = pathToFileURL(
		path.join(upstreamRoot, "packages/ai/src/utils/typebox-helpers.ts"),
	).href;
	const [{ Type }, { StringEnum }] = await Promise.all([import(typeboxURL), import(helpersURL)]);

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
				source: "typebox@1.1.38 + packages/ai/src/utils/typebox-helpers.ts",
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
}
