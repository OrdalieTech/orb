import { mkdir, mkdtemp, rm, writeFile } from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import { pathToFileURL } from "node:url";

import { withOfflineGeneratedCatalog } from "./f3-agent.ts";

type ArgumentCase = { name: string; input: string; expected?: string[] };
type SubstitutionCase = { name: string; content: string; args: string[]; expected?: string };
type TemplateCase = {
  name: string;
  text: string;
  templates: Array<{ name: string; description: string; content: string }>;
  expected?: string;
};

type FixtureFile = { path: string; content: string };

const discoveryFiles: FixtureFile[] = [
  {
    path: "skills/inspect/SKILL.md",
    content:
      "---\nname: inspect\ndescription: Inspect files & report findings.\nallowed-tools: read bash\n---\nUse inspection tools.",
  },
  {
    path: "skills/hidden/SKILL.md",
    content:
      "---\nname: hidden\ndescription: Explicit invocation only.\ndisable-model-invocation: true\n---\nHidden body.",
  },
  { path: "skills/root.md", content: "---\nname: root\ndescription: Root markdown skill.\n---\nRoot body." },
  {
    path: "prompts/review.md",
    content: "---\ndescription: Review a path\nargument-hint: \"<path>\"\n---\nReview $1 with ${@:2}",
  },
  { path: "prompts/skill:missing.md", content: "Fallback template: $1" },
  { path: "prompts/unicode.md", content: `${"a".repeat(59)}🎉z` },
  { path: "prompts/nested/ignored.md", content: "Must not load" },
];

const argumentCases: ArgumentCase[] = [
  { name: "plain", input: "a b c" },
  { name: "quoted", input: 'Button "onClick handler" \'disabled support\'' },
  { name: "empty-quotes", input: '\"\" " "' },
  { name: "newlines", input: "label-2\n\nHere is some description #2." },
  { name: "unescaped-quotes", input: '"quoted \\"text\\""' },
  { name: "unicode", input: "日本語 🎉 café" },
];

const substitutionCases: SubstitutionCase[] = [
  { name: "all-forms", content: "$1: $@ ($ARGUMENTS)", args: ["first", "second", "third"] },
  { name: "missing-positionals", content: "$1 $2 $3 $4", args: ["a", "b"] },
  { name: "multi-digit", content: "$10 $12 $15", args: Array.from({ length: 15 }, (_, index) => `val${index}`) },
  { name: "positional-defaults", content: "${1:-7} ${2:-brief}", args: [] },
  { name: "all-default", content: "${@:-default}\n${ARGUMENTS:-default}", args: [] },
  { name: "non-recursive-argument", content: "$ARGUMENTS", args: ["$1", "$ARGUMENTS"] },
  { name: "non-recursive-default", content: "${3:-$ARGUMENTS}", args: ["a", "b"] },
  { name: "slice-tail", content: "${@:2}", args: ["a", "b", "c", "d"] },
  { name: "slice-length", content: "${@:2:2}", args: ["a", "b", "c", "d"] },
  { name: "slice-zero-start", content: "${@:0}", args: ["a", "b", "c"] },
  { name: "slice-zero-length", content: "${@:2:0}", args: ["a", "b", "c"] },
  { name: "escaped-dollar-is-not-special", content: "Price: \\$100", args: [] },
  { name: "unicode", content: "$ARGUMENTS", args: ["日本語", "🎉", "café"] },
];

const templateCases: TemplateCase[] = [
  {
    name: "quoted-args",
    text: '/component Button "onClick handler" "disabled support"',
    templates: [{ name: "component", description: "Create component", content: "Create $1 with ${@:2}" }],
  },
  {
    name: "newline-separator",
    text: "/review\nfile.go",
    templates: [{ name: "review", description: "Review", content: "Review $1" }],
  },
  {
    name: "unknown",
    text: "/missing value",
    templates: [{ name: "review", description: "Review", content: "Review $1" }],
  },
  {
    name: "not-command",
    text: "review file.go",
    templates: [{ name: "review", description: "Review", content: "Review $1" }],
  },
];

const resolutionTemplateSpecs = [
  { name: "review", description: "Review", content: "Template review: $1", fileName: "review.md" },
  {
    name: "skill:inspect",
    description: "Collision template",
    content: "WRONG TEMPLATE: $1",
    fileName: "skill:inspect.md",
  },
  {
    name: "skill:missing",
    description: "Unknown-skill fallback",
    content: "Fallback template: $1",
    fileName: "skill:missing.md",
  },
];

function replaceFixture(value: string, fixtureRoot: string): string {
  return value.split(fixtureRoot).join("<fixture>");
}

function normalizeDeep(value: unknown, fixtureRoot: string): unknown {
  if (typeof value === "string") return replaceFixture(value, fixtureRoot);
  if (Array.isArray(value)) return value.map((item) => normalizeDeep(item, fixtureRoot));
  if (value && typeof value === "object") {
    return Object.fromEntries(Object.entries(value).map(([key, item]) => [key, normalizeDeep(item, fixtureRoot)]));
  }
  return value;
}

async function writeFixtureTree(root: string): Promise<{ skillsDir: string; promptsDir: string; inspectPath: string }> {
  const skillsDir = path.join(root, "skills");
  const promptsDir = path.join(root, "prompts");
  const inspectPath = path.join(skillsDir, "inspect", "SKILL.md");
  for (const file of discoveryFiles) {
    const filePath = path.join(root, file.path);
    await mkdir(path.dirname(filePath), { recursive: true });
    await writeFile(filePath, file.content);
  }
  return { skillsDir, promptsDir, inspectPath };
}

async function generateResolutionCases(
  upstreamRoot: string,
  fixtureRoot: string,
  inspectPath: string,
): Promise<unknown[]> {
  return withOfflineGeneratedCatalog(upstreamRoot, async () => {
    const harnessModule = await import(
      `${pathToFileURL(path.join(upstreamRoot, "packages/coding-agent/test/suite/harness.ts")).href}?f8`
    );
    const utilities = await import(
      `${pathToFileURL(path.join(upstreamRoot, "packages/coding-agent/test/utilities.ts")).href}?f8`
    );
    const sourceInfoModule = await import(
      `${pathToFileURL(path.join(upstreamRoot, "packages/coding-agent/src/core/source-info.ts")).href}?f8`
    );
    const faux = await import(
      `${pathToFileURL(path.join(upstreamRoot, "packages/ai/src/providers/faux.ts")).href}?f8`
    );
    const skill = {
      name: "inspect",
      description: "Inspect files & report findings.",
      filePath: inspectPath,
      baseDir: path.dirname(inspectPath),
      disableModelInvocation: false,
      sourceInfo: sourceInfoModule.createSyntheticSourceInfo(inspectPath, {
        source: "local",
        scope: "project",
        origin: "top-level",
        baseDir: path.dirname(inspectPath),
      }),
    };
    const templates = resolutionTemplateSpecs.map((template) => {
      const filePath = path.join(fixtureRoot, "prompts", template.fileName);
      return {
        name: template.name,
        description: template.description,
        content: template.content,
        filePath,
        sourceInfo: sourceInfoModule.createSyntheticSourceInfo(filePath, {
          source: "local",
          scope: "project",
          origin: "top-level",
        }),
      };
    });
    const definitions = [
      { name: "extension-first", text: "/ext hello" },
      { name: "input-transform-before-template", text: "/alias file.go" },
      { name: "input-transform-before-skill", text: "/choose details" },
      { name: "skill-before-colliding-template", text: "/skill:inspect details" },
      { name: "unknown-skill-falls-through-template", text: "/skill:missing details" },
      { name: "input-handled-before-expansion", text: "/consume" },
    ];
    const results: unknown[] = [];
    for (const definition of definitions) {
      const trace: string[] = [];
      const extensionsResult = await utilities.createTestExtensionsResult([
        (pi: any) => {
          pi.registerCommand("ext", {
            description: "Handled extension command",
            handler: async (args: string) => trace.push(`extension:${args}`),
          });
          pi.on("input", async (event: any) => {
            trace.push(`input:${event.text}`);
            if (event.text.startsWith("/alias ")) {
              return { action: "transform", text: `/review ${event.text.slice(7)}` };
            }
            if (event.text.startsWith("/choose ")) {
              return { action: "transform", text: `/skill:inspect ${event.text.slice(8)}` };
            }
            if (event.text === "/consume") return { action: "handled" };
            return undefined;
          });
        },
      ], fixtureRoot);
      const resourceLoader = {
        ...utilities.createTestResourceLoader({ extensionsResult }),
        getSkills: () => ({ skills: [skill], diagnostics: [] }),
        getPrompts: () => ({ prompts: templates, diagnostics: [] }),
      };
      const harness = await harnessModule.createHarness({ resourceLoader });
      harness.setResponses([faux.fauxAssistantMessage("ok")]);
      try {
        await harness.session.prompt(definition.text);
        const userTexts = harnessModule.getUserTexts(harness);
        results.push({
          ...definition,
          handled: userTexts.length === 0,
          expanded: userTexts.at(-1) ?? null,
          trace,
        });
      } finally {
        harness.cleanup();
      }
    }
    return normalizeDeep(results, fixtureRoot) as unknown[];
  });
}

export async function generateF8(upstreamRoot: string, outputRoot: string, upstreamCommit: string): Promise<void> {
  const promptSource = "packages/coding-agent/src/core/prompt-templates.ts";
  const skillSource = "packages/coding-agent/src/core/skills.ts";
  const harnessSkillSource = "packages/agent/src/harness/skills.ts";
  const harnessPromptSource = "packages/agent/src/harness/prompt-templates.ts";
  const promptModule = await import(pathToFileURL(path.join(upstreamRoot, promptSource)).href) as typeof import(
    "../../.upstream/packages/coding-agent/src/core/prompt-templates.ts"
  );
  const skillModule = await import(pathToFileURL(path.join(upstreamRoot, skillSource)).href) as typeof import(
    "../../.upstream/packages/coding-agent/src/core/skills.ts"
  );
  const harnessSkillModule = await import(pathToFileURL(path.join(upstreamRoot, harnessSkillSource)).href) as typeof import(
    "../../.upstream/packages/agent/src/harness/skills.ts"
  );
  const harnessPromptModule = await import(pathToFileURL(path.join(upstreamRoot, harnessPromptSource)).href) as typeof import(
    "../../.upstream/packages/agent/src/harness/prompt-templates.ts"
  );

  for (const fixtureCase of argumentCases) fixtureCase.expected = promptModule.parseCommandArgs(fixtureCase.input);
  for (const fixtureCase of substitutionCases) {
    fixtureCase.expected = promptModule.substituteArgs(fixtureCase.content, fixtureCase.args);
  }
  for (const fixtureCase of templateCases) {
    fixtureCase.expected = promptModule.expandPromptTemplate(fixtureCase.text, fixtureCase.templates as any);
  }

  const fixtureRoot = await mkdtemp(path.join(os.tmpdir(), "pi-go-f8-"));
  try {
    const { skillsDir, promptsDir, inspectPath } = await writeFixtureTree(fixtureRoot);
    const skillResult = skillModule.loadSkills({
      cwd: fixtureRoot,
      agentDir: path.join(fixtureRoot, "agent"),
      skillPaths: [skillsDir],
      includeDefaults: false,
    });
    const templates = promptModule.loadPromptTemplates({
      cwd: fixtureRoot,
      agentDir: path.join(fixtureRoot, "agent"),
      promptPaths: [promptsDir],
      includeDefaults: false,
    });
    const envModule = await import(
      pathToFileURL(path.join(upstreamRoot, "packages/agent/src/harness/env/nodejs.ts")).href
    );
    const harnessEnv = new envModule.NodeExecutionEnv({ cwd: fixtureRoot });
    const harnessSkills = await harnessSkillModule.loadSkills(harnessEnv, skillsDir);
    const harnessPrompts = await harnessPromptModule.loadPromptTemplates(harnessEnv, promptsDir);
    const inspect = harnessSkills.skills.find((skill) => skill.name === "inspect");
    if (!inspect) throw new Error("F8 failed to load inspect skill through agent harness");
    const invocationCases = [
      { name: "without-extra", additionalInstructions: "", expected: harnessSkillModule.formatSkillInvocation(inspect) },
      {
        name: "with-extra",
        additionalInstructions: "Check errors.",
        expected: harnessSkillModule.formatSkillInvocation(inspect, "Check errors."),
      },
    ];
    const normalizedSkills = skillResult.skills.map((skill) => ({
      name: skill.name,
      description: skill.description,
      filePath: replaceFixture(skill.filePath, fixtureRoot),
      baseDir: replaceFixture(skill.baseDir, fixtureRoot),
      disableModelInvocation: skill.disableModelInvocation,
      sourceInfo: normalizeDeep(skill.sourceInfo, fixtureRoot),
    }));
    const normalizedTemplates = templates.map((template) => ({
      name: template.name,
      description: template.description,
      argumentHint: template.argumentHint ?? null,
      content: template.content,
      filePath: replaceFixture(template.filePath, fixtureRoot),
      sourceInfo: normalizeDeep(template.sourceInfo, fixtureRoot),
    }));
    const commands = [
      ...normalizedTemplates.map((template) => ({
        name: template.name,
        description: template.description,
        source: "prompt",
        sourceInfo: template.sourceInfo,
      })),
      ...normalizedSkills.map((skill) => ({
        name: `skill:${skill.name}`,
        description: skill.description,
        source: "skill",
        sourceInfo: skill.sourceInfo,
      })),
    ];
    const resolutionCases = await generateResolutionCases(upstreamRoot, fixtureRoot, inspectPath);
    const familyDir = path.join(outputRoot, "F8");
    await rm(familyDir, { recursive: true, force: true });
    await mkdir(familyDir, { recursive: true });
    const manifest = {
      family: "F8",
      upstreamCommit,
      generator: "conformance/extract/f8-slash-templates.ts",
      source: `${promptSource} + ${skillSource} + packages/coding-agent/src/core/agent-session.ts`,
      additionalSources: [
        harnessSkillSource,
        harnessPromptSource,
        "packages/coding-agent/src/core/slash-commands.ts",
      ],
      files: ["cases.json"],
    };
    const fixture = {
      schemaVersion: 1,
      argumentCases,
      substitutionCases,
      templateCases,
      invocationCases: normalizeDeep(invocationCases, fixtureRoot),
      harnessPrompts: {
        promptTemplates: harnessPrompts.promptTemplates,
        diagnostics: normalizeDeep(harnessPrompts.diagnostics, fixtureRoot),
        invocation: harnessPromptModule.formatPromptTemplateInvocation(
          harnessPrompts.promptTemplates.find((template) => template.name === "review")!,
          ["file.go", "focus", "errors"],
        ),
      },
      discovery: {
        files: discoveryFiles,
        skills: normalizedSkills,
        diagnostics: normalizeDeep(skillResult.diagnostics, fixtureRoot),
        templates: normalizedTemplates,
        commands,
      },
      resolutionTemplates: normalizeDeep(
        resolutionTemplateSpecs.map((template) => ({
          name: template.name,
          description: template.description,
          content: template.content,
          filePath: path.join(fixtureRoot, "prompts", template.fileName),
        })),
        fixtureRoot,
      ),
      resolutionCases,
    };
    await writeFile(path.join(familyDir, "manifest.json"), `${JSON.stringify(manifest, null, 2)}\n`);
    await writeFile(path.join(familyDir, "cases.json"), `${JSON.stringify(fixture, null, 2)}\n`);
  } finally {
    await rm(fixtureRoot, { recursive: true, force: true });
  }
}
