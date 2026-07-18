import { access, mkdir, mkdtemp, rm, writeFile } from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import { pathToFileURL } from "node:url";

import { withUpstreamModelData } from "./upstream-model-data.ts";

type ContextFile = { path: string; content: string };
type PromptSkill = {
  name: string;
  description: string;
  filePath: string;
  baseDir: string;
  disableModelInvocation: boolean;
};

const contextFileCandidates = ["AGENTS.md", "AGENTS.MD", "CLAUDE.md", "CLAUDE.MD"];

type PromptCase = {
  name: string;
  input: {
    customPrompt?: string;
    selectedTools?: string[];
    toolSnippets?: Record<string, string>;
    promptGuidelines?: string[];
    appendSystemPrompt?: string;
    cwd: string;
    contextFiles?: ContextFile[];
    skills?: PromptSkill[];
  };
  expected?: string;
};

type DiscoveryCase = {
  name: string;
  cwd: string;
  agentDir: string;
  files: Array<{ path: string; content: string }>;
  noContextFiles?: boolean;
  projectTrusted: boolean;
  systemPromptSet?: boolean;
  systemPrompt?: string;
  appendSystemPromptSet?: boolean;
  appendSystemPrompt?: string[];
  expected?: {
    contextFiles: ContextFile[];
    systemPrompt: string | null;
    appendSystemPrompt: string[];
    assembledPrompt: string;
  };
};

const promptCases: PromptCase[] = [
  {
    name: "default-tools-context-and-guidelines",
    input: {
      selectedTools: ["read", "bash", "edit", "write", "hidden"],
      toolSnippets: {
        read: "Read file contents",
        bash: "Execute bash commands",
        edit: "Make surgical edits",
        write: "Create or overwrite files",
        hidden: "",
      },
      promptGuidelines: ["  Prefer small patches.  ", "Prefer small patches.", "   "],
      appendSystemPrompt: "Appended guidance.",
      cwd: "/fixture/project",
      contextFiles: [
        { path: "/fixture/AGENTS.md", content: "Global <rules> & raw." },
        { path: "/fixture/project/CLAUDE.md", content: "Project rules." },
      ],
    },
  },
  {
    name: "explicit-empty-tools",
    input: {
      selectedTools: [],
      toolSnippets: { read: "Read file contents" },
      cwd: "/fixture/empty-tools",
      contextFiles: [],
    },
  },
  {
    name: "default-tool-selection-without-snippets",
    input: {
      cwd: "/fixture/no-snippets",
      contextFiles: [],
    },
  },
  {
    name: "custom-prompt-order-and-windows-cwd",
    input: {
      customPrompt: "Custom base.",
      selectedTools: ["read"],
      appendSystemPrompt: "First append.\n\nSecond append.",
      cwd: "C:\\work\\repo",
      contextFiles: [{ path: "C:\\work\\repo\\AGENTS.md", content: "Do <x> & y." }],
    },
  },
  {
    name: "empty-custom-falls-back-to-default",
    input: {
      customPrompt: "",
      selectedTools: ["bash"],
      toolSnippets: { bash: "Execute bash commands" },
      cwd: "/fixture/empty-custom",
      contextFiles: [],
    },
  },
  {
    name: "skills-progressive-disclosure",
    input: {
      selectedTools: ["read"],
      toolSnippets: { read: "Read file contents" },
      cwd: "/fixture/skills",
      contextFiles: [],
      skills: [
        {
          name: "review<&\"'",
          description: "Review <files> & report \"findings\".",
          filePath: "/fixture/skills/review<&\"'/SKILL.md",
          baseDir: "/fixture/skills/review<&\"'",
          disableModelInvocation: false,
        },
        {
          name: "hidden",
          description: "Explicit invocation only.",
          filePath: "/fixture/skills/hidden/SKILL.md",
          baseDir: "/fixture/skills/hidden",
          disableModelInvocation: true,
        },
      ],
    },
  },
];

const discoveryCases: DiscoveryCase[] = [
  {
    name: "global-root-ancestor-cwd-order-and-case-priority",
    cwd: "project/parent/cwd",
    agentDir: "agent",
    projectTrusted: true,
    files: [
      { path: "agent/AGENTS.md", content: "global" },
      { path: "project/AGENTS.MD", content: "root upper agents" },
      { path: "project/CLAUDE.md", content: "must lose" },
      { path: "project/parent/CLAUDE.md", content: "ancestor claude" },
      { path: "project/parent/cwd/AGENTS.md", content: "cwd agents" },
    ],
  },
  {
    name: "project-system-and-append-win",
    cwd: "project",
    agentDir: "agent",
    projectTrusted: true,
    files: [
      { path: "agent/SYSTEM.md", content: "global system" },
      { path: "agent/APPEND_SYSTEM.md", content: "global append" },
      { path: "project/.pi/SYSTEM.md", content: "project system" },
      { path: "project/.pi/APPEND_SYSTEM.md", content: "project append" },
      { path: "project/AGENTS.md", content: "project context" },
    ],
  },
  {
    name: "no-context-does-not-disable-system-files",
    cwd: "project",
    agentDir: "agent",
    projectTrusted: true,
    noContextFiles: true,
    files: [
      { path: "project/.pi/SYSTEM.md", content: "project system" },
      { path: "project/.pi/APPEND_SYSTEM.md", content: "project append" },
      { path: "project/AGENTS.md", content: "hidden context" },
    ],
  },
  {
    name: "untrusted-project-falls-back-to-global-prompts",
    cwd: "project",
    agentDir: "agent",
    projectTrusted: false,
    files: [
      { path: "agent/SYSTEM.md", content: "global system" },
      { path: "agent/APPEND_SYSTEM.md", content: "global append" },
      { path: "project/.pi/SYSTEM.md", content: "project system" },
      { path: "project/.pi/APPEND_SYSTEM.md", content: "project append" },
    ],
  },
  {
    name: "cli-file-and-literal-overrides-replace-discovery",
    cwd: "project",
    agentDir: "agent",
    projectTrusted: true,
    systemPromptSet: true,
    systemPrompt: "<fixture>/prompts/system.txt",
    appendSystemPromptSet: true,
    appendSystemPrompt: ["<fixture>/prompts/append.txt", "literal append"],
    files: [
      { path: "agent/SYSTEM.md", content: "global system" },
      { path: "project/.pi/SYSTEM.md", content: "project system" },
      { path: "prompts/system.txt", content: "cli system file" },
      { path: "prompts/append.txt", content: "cli append file" },
    ],
  },
  {
    name: "explicit-empty-overrides-suppress-discovery",
    cwd: "project",
    agentDir: "agent",
    projectTrusted: true,
    systemPromptSet: true,
    systemPrompt: "",
    appendSystemPromptSet: true,
    appendSystemPrompt: [],
    files: [
      { path: "agent/SYSTEM.md", content: "global system" },
      { path: "agent/APPEND_SYSTEM.md", content: "global append" },
    ],
  },
];

function replaceFixture(value: string, fixtureRoot: string): string {
  return value.split(fixtureRoot).join("<fixture>");
}

function materializeFixture(value: string | undefined, fixtureRoot: string): string | undefined {
  return value?.split("<fixture>").join(fixtureRoot);
}

async function writeTree(root: string, files: DiscoveryCase["files"]): Promise<void> {
  for (const file of files) {
    const filePath = path.join(root, file.path);
    await mkdir(path.dirname(filePath), { recursive: true });
    await writeFile(filePath, file.content);
  }
}

async function assertNoAmbientContextFiles(): Promise<void> {
  const tempRoot = path.resolve(os.tmpdir());
  for (let current = tempRoot; ; current = path.dirname(current)) {
    for (const candidate of contextFileCandidates) {
      const filePath = path.join(current, candidate);
      try {
        await access(filePath);
      } catch {
        continue;
      }
      throw new Error(
        `F9 fixture isolation requires no ambient context file at ${filePath}; move it before regenerating fixtures`,
      );
    }
    const parent = path.dirname(current);
    if (parent === current) break;
  }
}

export async function generateF9(upstreamRoot: string, outputRoot: string, upstreamCommit: string): Promise<void> {
  await assertNoAmbientContextFiles();
  process.env.PI_PACKAGE_DIR = "/pi-package";
  const promptSource = "packages/coding-agent/src/core/system-prompt.ts";
  const resourceSource = "packages/coding-agent/src/core/resource-loader.ts";
  const settingsSource = "packages/coding-agent/src/core/settings-manager.ts";
  const { promptModule, resourceModule, settingsModule } = await withUpstreamModelData(upstreamRoot, async () => ({
    promptModule: await import(pathToFileURL(path.join(upstreamRoot, promptSource)).href) as typeof import(
      "../../.upstream/packages/coding-agent/src/core/system-prompt.ts"
    ),
    resourceModule: await import(pathToFileURL(path.join(upstreamRoot, resourceSource)).href) as typeof import(
      "../../.upstream/packages/coding-agent/src/core/resource-loader.ts"
    ),
    settingsModule: await import(pathToFileURL(path.join(upstreamRoot, settingsSource)).href) as typeof import(
      "../../.upstream/packages/coding-agent/src/core/settings-manager.ts"
    ),
  }));

  for (const fixtureCase of promptCases) {
    fixtureCase.expected = promptModule.buildSystemPrompt(fixtureCase.input);
  }

  for (const fixtureCase of discoveryCases) {
    const fixtureRoot = await mkdtemp(path.join(os.tmpdir(), "pi-go-f9-"));
    try {
      await writeTree(fixtureRoot, fixtureCase.files);
      const cwd = path.join(fixtureRoot, fixtureCase.cwd);
      const agentDir = path.join(fixtureRoot, fixtureCase.agentDir);
      await mkdir(cwd, { recursive: true });
      await mkdir(agentDir, { recursive: true });
      const settingsManager = settingsModule.SettingsManager.create(cwd, agentDir, {
        projectTrusted: fixtureCase.projectTrusted,
      });
      const loaderOptions: ConstructorParameters<typeof resourceModule.DefaultResourceLoader>[0] = {
        cwd,
        agentDir,
        settingsManager,
        noExtensions: true,
        noSkills: true,
        noPromptTemplates: true,
        noThemes: true,
        noContextFiles: fixtureCase.noContextFiles,
      };
      if (fixtureCase.systemPromptSet) {
        loaderOptions.systemPrompt = materializeFixture(fixtureCase.systemPrompt, fixtureRoot);
      }
      if (fixtureCase.appendSystemPromptSet) {
        loaderOptions.appendSystemPrompt = (fixtureCase.appendSystemPrompt ?? []).map(
          (value) => materializeFixture(value, fixtureRoot)!,
        );
      }
      const loader = new resourceModule.DefaultResourceLoader(loaderOptions);
      await loader.reload();
      const contextFiles = loader.getAgentsFiles().agentsFiles.map((file) => ({
        path: replaceFixture(file.path, fixtureRoot),
        content: file.content,
      }));
      const systemPrompt = loader.getSystemPrompt() ?? null;
      const appendSystemPrompt = loader.getAppendSystemPrompt();
      const assembledPrompt = promptModule.buildSystemPrompt({
        customPrompt: systemPrompt ?? undefined,
        selectedTools: ["read", "bash", "edit", "write"],
        toolSnippets: {
          read: "Read file contents",
          bash: "Execute bash commands",
          edit: "Make surgical edits",
          write: "Create or overwrite files",
        },
        appendSystemPrompt: appendSystemPrompt.join("\n\n") || undefined,
        cwd: replaceFixture(cwd, fixtureRoot),
        contextFiles,
        skills: [],
      });
      fixtureCase.expected = { contextFiles, systemPrompt, appendSystemPrompt, assembledPrompt };
    } finally {
      await rm(fixtureRoot, { recursive: true, force: true });
    }
  }

  const familyDir = path.join(outputRoot, "F9");
  await mkdir(familyDir, { recursive: true });
  const manifest = {
    family: "F9",
    upstreamCommit,
    generator: "conformance/extract/f9-system-prompt.ts",
    source: promptSource,
    additionalSources: [resourceSource, settingsSource],
    files: ["cases.json"],
  };
  await writeFile(path.join(familyDir, "manifest.json"), `${JSON.stringify(manifest, null, 2)}\n`);
  await writeFile(
    path.join(familyDir, "cases.json"),
    `${JSON.stringify({ schemaVersion: 1, packageDir: "/pi-package", promptCases, discoveryCases }, null, 2)}\n`,
  );
}
