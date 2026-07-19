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
type HarnessSubstitutionCase = { name: string; content: string; args: string[]; expected?: string };

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
  {
    path: "skills/broken/SKILL.md",
    content: "---\nname: broken\n---\nThis skill is missing its required description.",
  },
  { path: "skills/root.md", content: "---\nname: root\ndescription: Root markdown skill.\n---\nRoot body." },
  {
    path: "prompts/review.md",
    content: "---\ndescription: Review a path\nargument-hint: \"<path>\"\n---\nReview $1 with ${@:2}",
  },
  { path: "prompts/skill:missing.md", content: "Fallback template: $1" },
  { path: "prompts/empty.md", content: "" },
  { path: "prompts/unicode.md", content: `${"a".repeat(59)}🎉z` },
  { path: "prompts/nested/ignored.md", content: "Must not load" },
];

const resourceFiles: FixtureFile[] = [
  { path: "resource/project/.git/.keep", content: "" },
  {
    path: "resource/project/.pi/skills/direct-wins/SKILL.md",
    content: "---\nname: direct-wins\ndescription: Project direct-wins skill\n---\nProject direct body.",
  },
  {
    path: "resource/project/.pi/skills/project-wins/SKILL.md",
    content: "---\nname: project-wins\ndescription: Project project-wins skill\n---\nProject winner body.",
  },
  {
    path: "resource/project/.pi/skills/broken/SKILL.md",
    content: "---\nname: broken\n---\nMissing description.",
  },
  {
    path: "resource/agent/skills/direct-wins/SKILL.md",
    content: "---\nname: direct-wins\ndescription: User direct-wins skill\n---\nUser direct body.",
  },
  {
    path: "resource/agent/skills/project-wins/SKILL.md",
    content: "---\nname: project-wins\ndescription: User project-wins skill\n---\nUser losing body.",
  },
  {
    path: "resource/agent/skills/user-wins/SKILL.md",
    content: "---\nname: user-wins\ndescription: User user-wins skill\n---\nUser winner body.",
  },
  {
    path: "resource/direct/skills/direct-wins/SKILL.md",
    content: "---\nname: direct-wins\ndescription: CLI direct-wins skill\n---\nCLI winner body.",
  },
  {
    path: "resource/direct/skills/direct-only/SKILL.md",
    content: "---\nname: direct-only\ndescription: Direct-only skill\n---\nDirect-only body.",
  },
  {
    path: "resource/package/skills/direct-wins/SKILL.md",
    content: "---\nname: direct-wins\ndescription: Package direct-wins skill\n---\nPackage direct body.",
  },
  {
    path: "resource/package/skills/project-wins/SKILL.md",
    content: "---\nname: project-wins\ndescription: Package project-wins skill\n---\nPackage project body.",
  },
  {
    path: "resource/package/skills/user-wins/SKILL.md",
    content: "---\nname: user-wins\ndescription: Package user-wins skill\n---\nPackage user body.",
  },
  {
    path: "resource/package/skills/package-only/SKILL.md",
    content: "---\nname: package-only\ndescription: Package-only skill\n---\nPackage-only body.",
  },
  { path: "resource/project/.pi/prompts/x.md", content: "Project x prompt." },
  { path: "resource/project/.pi/prompts/project-wins.md", content: "Project prompt winner." },
  { path: "resource/agent/prompts/x.md", content: "User x prompt." },
  { path: "resource/agent/prompts/project-wins.md", content: "User project prompt." },
  { path: "resource/agent/prompts/user-wins.md", content: "User prompt winner." },
  { path: "resource/direct/prompts/x.md", content: "" },
  { path: "resource/direct/prompts/repeat.md", content: "Repeated direct prompt." },
  { path: "resource/package/prompts/x.md", content: "Package x prompt." },
  { path: "resource/package/prompts/project-wins.md", content: "Package project prompt." },
  { path: "resource/package/prompts/user-wins.md", content: "Package user prompt." },
  { path: "resource/package/prompts/package-only.md", content: "Package-only prompt." },
  {
    path: "resource/package/package.json",
    content: JSON.stringify({
      name: "pi-go-f8-package",
      version: "1.0.0",
      pi: { skills: ["skills"], prompts: ["prompts"] },
    }),
  },
  { path: "resource/agent/settings.json", content: JSON.stringify({ packages: ["../package"] }) },
];

const resourceExtensionFiles: FixtureFile[] = [
  { path: "loader-extension/project/.git/.keep", content: "" },
  {
    path: "loader-extension/skills/instant/SKILL.md",
    content: "---\nname: instant\ndescription: Immediate extension skill\n---\nInstant skill body.",
  },
  { path: "loader-extension/prompts/first/shared.md", content: "First shared prompt." },
  { path: "loader-extension/prompts/second/shared.md", content: "Second shared prompt." },
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

const harnessSubstitutionCases: HarnessSubstitutionCase[] = [
  { name: "positional-slice-and-all", content: "$1 ${@:2} $ARGUMENTS", args: ["hello world", "test", "$1"] },
  { name: "default-syntax-is-literal", content: "${4:-fallback}", args: ["one", "two"] },
  { name: "sequential-reexpansion", content: "$1", args: ["$ARGUMENTS", "tail"] },
  { name: "slice-zero-length-quirk", content: "${@:2:0}", args: ["one", "two", "three"] },
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
  await writeFixtureFiles(root, discoveryFiles);
  return { skillsDir, promptsDir, inspectPath };
}

async function writeFixtureFiles(root: string, files: FixtureFile[]): Promise<void> {
  for (const file of files) {
    const filePath = path.join(root, file.path);
    await mkdir(path.dirname(filePath), { recursive: true });
    await writeFile(filePath, file.content);
  }
}

function normalizeSkill(skill: any, fixtureRoot: string): any {
  return {
    name: skill.name,
    description: skill.description,
    filePath: replaceFixture(skill.filePath, fixtureRoot),
    baseDir: replaceFixture(skill.baseDir, fixtureRoot),
    disableModelInvocation: skill.disableModelInvocation,
    sourceInfo: normalizeDeep(skill.sourceInfo, fixtureRoot),
  };
}

function normalizeTemplate(template: any, fixtureRoot: string): any {
  return {
    name: template.name,
    description: template.description,
    argumentHint: template.argumentHint ?? null,
    content: template.content,
    filePath: replaceFixture(template.filePath, fixtureRoot),
    sourceInfo: normalizeDeep(template.sourceInfo, fixtureRoot),
  };
}

async function generateResourceLoaderFixture(upstreamRoot: string, fixtureRoot: string): Promise<unknown> {
  return withOfflineGeneratedCatalog(upstreamRoot, async () => {
    const resourceModule = await import(
      `${pathToFileURL(path.join(upstreamRoot, "packages/coding-agent/src/core/resource-loader.ts")).href}?f8-resources`
    );
    await writeFixtureFiles(fixtureRoot, resourceFiles);
    const cwd = path.join(fixtureRoot, "resource", "project");
    const agentDir = path.join(fixtureRoot, "resource", "agent");
    const directSkill = path.join(fixtureRoot, "resource", "direct", "skills", "direct-wins", "SKILL.md");
    const repeatedSkill = path.join(fixtureRoot, "resource", "direct", "skills", "direct-only", "SKILL.md");
    const directPrompt = path.join(fixtureRoot, "resource", "direct", "prompts", "x.md");
    const repeatedPrompt = path.join(fixtureRoot, "resource", "direct", "prompts", "repeat.md");
    const missingSkill = path.join(fixtureRoot, "resource", "missing-skill");
    const missingPrompt = path.join(fixtureRoot, "resource", "missing-prompt");
    const originalHome = process.env.HOME;
    process.env.HOME = path.join(fixtureRoot, "home");
    try {
      const loader = new resourceModule.DefaultResourceLoader({
        cwd,
        agentDir,
        additionalSkillPaths: [directSkill, repeatedSkill, repeatedSkill, missingSkill],
        additionalPromptTemplatePaths: [directPrompt, repeatedPrompt, repeatedPrompt, missingPrompt],
        noExtensions: true,
        noThemes: true,
        noContextFiles: true,
      });
      await loader.reload();
      const skillResult = loader.getSkills();
      const promptResult = loader.getPrompts();
      return normalizeDeep(
        {
          files: resourceFiles,
          cwd,
          agentDir,
          skillPaths: [directSkill, repeatedSkill, repeatedSkill, missingSkill],
          promptPaths: [directPrompt, repeatedPrompt, repeatedPrompt, missingPrompt],
          packageSkillPaths: [path.join(fixtureRoot, "resource", "package", "skills")],
          packagePromptPaths: [path.join(fixtureRoot, "resource", "package", "prompts")],
          skills: skillResult.skills.map((skill: any) => normalizeSkill(skill, fixtureRoot)),
          templates: promptResult.prompts.map((template: any) => normalizeTemplate(template, fixtureRoot)),
          diagnostics: [...skillResult.diagnostics, ...promptResult.diagnostics],
        },
        fixtureRoot,
      );
    } finally {
      if (originalHome === undefined) delete process.env.HOME;
      else process.env.HOME = originalHome;
    }
  });
}

async function generateResourceLoaderExtensionFixture(upstreamRoot: string, fixtureRoot: string): Promise<unknown> {
  return withOfflineGeneratedCatalog(upstreamRoot, async () => {
    const resourceModule = await import(
      `${pathToFileURL(path.join(upstreamRoot, "packages/coding-agent/src/core/resource-loader.ts")).href}?f8-resource-extensions`
    );
    await writeFixtureFiles(fixtureRoot, resourceExtensionFiles);
    const cwd = path.join(fixtureRoot, "loader-extension", "project");
    const agentDir = path.join(fixtureRoot, "loader-extension", "agent");
    const skillDir = path.join(fixtureRoot, "loader-extension", "skills", "instant");
    const promptOne = path.join(fixtureRoot, "loader-extension", "prompts", "first", "shared.md");
    const promptTwo = path.join(fixtureRoot, "loader-extension", "prompts", "second", "shared.md");
    const missingSkill = path.join(fixtureRoot, "loader-extension", "missing-skill");
    const originalHome = process.env.HOME;
    process.env.HOME = path.join(fixtureRoot, "loader-extension", "home");
    try {
      const loader = new resourceModule.DefaultResourceLoader({
        cwd,
        agentDir,
        noExtensions: true,
        noSkills: true,
        noPromptTemplates: true,
        noThemes: true,
        noContextFiles: true,
      });
      await loader.reload();
      const skillPaths = [
        {
          path: path.relative(cwd, skillDir),
          metadata: {
            source: "extension:first",
            scope: "temporary",
            origin: "extension",
            baseDir: path.relative(cwd, path.dirname(skillDir)),
          },
        },
        {
          path: pathToFileURL(skillDir).href,
          metadata: {
            source: "extension:second",
            scope: "temporary",
            origin: "extension",
            baseDir: pathToFileURL(path.dirname(skillDir)).href,
          },
        },
        {
          path: path.relative(cwd, missingSkill),
          metadata: { source: "extension:missing", scope: "temporary", origin: "extension" },
        },
      ];
      const promptPaths = [
        {
          path: path.relative(cwd, promptOne),
          metadata: {
            source: "extension:prompt-one",
            scope: "temporary",
            origin: "extension",
            baseDir: path.relative(cwd, path.dirname(promptOne)),
          },
        },
        {
          path: pathToFileURL(promptTwo).href,
          metadata: {
            source: "extension:prompt-two",
            scope: "temporary",
            origin: "extension",
            baseDir: pathToFileURL(path.dirname(promptTwo)).href,
          },
        },
      ];
      loader.extendResources({ skillPaths, promptPaths });
      const skills = loader.getSkills();
      const prompts = loader.getPrompts();
      return normalizeDeep(
        {
          files: resourceExtensionFiles,
          cwd,
          agentDir,
          skillPaths,
          promptPaths,
          skills: skills.skills.map((skill: any) => normalizeSkill(skill, fixtureRoot)),
          skillDiagnostics: skills.diagnostics,
          templates: prompts.prompts.map((template: any) => normalizeTemplate(template, fixtureRoot)),
          promptDiagnostics: prompts.diagnostics,
        },
        fixtureRoot,
      );
    } finally {
      if (originalHome === undefined) delete process.env.HOME;
      else process.env.HOME = originalHome;
    }
  });
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
  const resourceSource = "packages/coding-agent/src/core/resource-loader.ts";
  const slashSource = "packages/coding-agent/src/core/slash-commands.ts";
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
  const slashModule = await import(pathToFileURL(path.join(upstreamRoot, slashSource)).href) as typeof import(
    "../../.upstream/packages/coding-agent/src/core/slash-commands.ts"
  );

  for (const fixtureCase of argumentCases) fixtureCase.expected = promptModule.parseCommandArgs(fixtureCase.input);
  for (const fixtureCase of substitutionCases) {
    fixtureCase.expected = promptModule.substituteArgs(fixtureCase.content, fixtureCase.args);
  }
  for (const fixtureCase of templateCases) {
    fixtureCase.expected = promptModule.expandPromptTemplate(fixtureCase.text, fixtureCase.templates as any);
  }
  for (const fixtureCase of harnessSubstitutionCases) {
    fixtureCase.expected = harnessPromptModule.formatPromptTemplateInvocation(
      { name: fixtureCase.name, description: "", content: fixtureCase.content },
      fixtureCase.args,
    );
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
    const harnessDirectPrompt = await harnessPromptModule.loadPromptTemplates(
      harnessEnv,
      path.join(promptsDir, "empty.md"),
    );
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
    const normalizedSkills = skillResult.skills.map((skill) => normalizeSkill(skill, fixtureRoot));
    const normalizedTemplates = templates.map((template) => normalizeTemplate(template, fixtureRoot));
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
    const resourceLoader = await generateResourceLoaderFixture(upstreamRoot, fixtureRoot);
    const resourceLoaderExtension = await generateResourceLoaderExtensionFixture(upstreamRoot, fixtureRoot);
    const resolutionCases = await generateResolutionCases(upstreamRoot, fixtureRoot, inspectPath);
    const familyDir = path.join(outputRoot, "F8");
    await rm(familyDir, { recursive: true, force: true });
    await mkdir(familyDir, { recursive: true });
    const manifest = {
      family: "F8",
      upstreamCommit,
      generator: "conformance/extract/f8-slash-templates.ts",
      source: `${promptSource} + ${skillSource} + ${resourceSource} + packages/coding-agent/src/core/agent-session.ts`,
      additionalSources: [
        harnessSkillSource,
        harnessPromptSource,
        slashSource,
        "packages/coding-agent/src/modes/rpc/rpc-mode.ts",
      ],
      files: ["cases.json"],
    };
    const fixture = {
      schemaVersion: 3,
      argumentCases,
      substitutionCases,
      templateCases,
      invocationCases: normalizeDeep(invocationCases, fixtureRoot),
      harnessSubstitutionCases,
      harnessPrompts: {
        promptTemplates: harnessPrompts.promptTemplates,
        diagnostics: normalizeDeep(harnessPrompts.diagnostics, fixtureRoot),
        directPrompt: harnessDirectPrompt,
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
        rpcCommandsWhenSkillCommandsDisabled: commands,
        builtinCommands: slashModule.BUILTIN_SLASH_COMMANDS.map((command) => ({
          name: command.name,
          description: command.description,
          argumentHint: command.argumentHint ?? null,
        })),
      },
      resourceLoader,
      resourceLoaderExtension,
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
