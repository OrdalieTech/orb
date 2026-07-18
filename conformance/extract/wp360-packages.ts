import { realpathSync } from "node:fs";
import { mkdir, mkdtemp, readFile, rm, symlink, writeFile } from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import { pathToFileURL } from "node:url";

import { withUpstreamModelData } from "./upstream-model-data.ts";

// WP-360 fixtures: package source parsing (git URLs), package/resource
// resolution, settings persistence, and project trust (options, store,
// trust.json format, trust-requiring detection).

type FileSpec = { path: string; content: string };
type SymlinkSpec = { link: string; target: string };

type GitUrlCase = {
  input: string;
  expected?: { repo: string; host: string; path: string; ref?: string; pinned: boolean } | null;
};

type ResolvedResourceFixture = {
  path: string;
  enabled: boolean;
  metadata: { source: string; scope: string; origin: string; baseDir?: string };
};

type ResolveCase = {
  name: string;
  files?: FileSpec[];
  dirs?: string[];
  symlinks?: SymlinkSpec[];
  globalSettings?: Record<string, unknown>;
  projectSettings?: Record<string, unknown>;
  projectTrusted: boolean;
  expected?: Record<"extensions" | "skills" | "prompts" | "themes", ResolvedResourceFixture[]>;
};

type SettingsOp = { op: "add" | "remove"; source: string; local?: boolean };

type SettingsCase = {
  name: string;
  dirs?: string[];
  initialGlobal?: Record<string, unknown>;
  initialProject?: Record<string, unknown>;
  ops: SettingsOp[];
  expected?: { changed: boolean[]; globalPackages: unknown[]; projectPackages: unknown[] };
};

type TrustOptionCase = {
  name: string;
  cwd: string;
  dirs?: string[];
  includeSessionOnly: boolean;
  expected?: Array<{
    label: string;
    trusted: boolean;
    updates: Array<{ path: string; decision: boolean | null }>;
    savedPath?: string;
  }>;
};

type TrustStoreCase = {
  name: string;
  dirs?: string[];
  ops: Array<Array<{ path: string; decision: boolean | null }>>;
  queries: string[];
  expected?: { decisions: Array<boolean | null>; file: string };
};

type HasTrustRequiringCase = {
  name: string;
  files?: FileSpec[];
  dirs?: string[];
  cwd: string;
  expected?: boolean;
};

const gitUrlCases: GitUrlCase[] = [
  { input: "https://github.com/user/repo" },
  { input: "https://github.com/user/repo.git" },
  { input: "https://github.com/user/repo@v2.0" },
  { input: "http://github.com/user/repo" },
  { input: "git://github.com/user/repo" },
  { input: "https://www.github.com/user/repo" },
  { input: "https://github.com/user" },
  { input: "https://github.com/user/repo/tree/v1" },
  { input: "https://github.com/user/repo#v1" },
  { input: "git:github.com/user/repo" },
  { input: "git:github.com/user/repo@v1" },
  { input: "git:git@github.com:user/repo" },
  { input: "git:git@github.com:user/repo@v1.0.0" },
  { input: "git:user/repo" },
  { input: "git:user/repo@v1" },
  { input: "git@github.com:user/repo" },
  { input: "github.com/user/repo" },
  { input: "ssh://git@github.com/user/repo" },
  { input: "ssh://git@github.com/user/repo@v1" },
  { input: "https://gitlab.com/group/sub/repo" },
  { input: "https://gitlab.com/user/repo@v1" },
  { input: "https://bitbucket.org/user/repo" },
  { input: "https://codeberg.org/user/repo" },
  { input: "git:codeberg.org/user/repo@main" },
  { input: "git:localhost/user/repo" },
  { input: "git:noext/user/repo" },
  { input: "git:github.com/../repo" },
  { input: "git:github.com/user/repo/../../etc" },
  { input: "git+https://github.com/user/repo" },
  { input: "./relative/path" },
  { input: "/absolute/path" },
];

const manifestPackageJson = JSON.stringify({
  name: "manifest-pkg",
  pi: {
    extensions: ["./ext/clip.ts", "./ext/glob-*.ts", "!ext/glob-legacy.ts"],
    skills: ["./skillset"],
    prompts: ["./docs/prompt-one.md", "./docs/more"],
    themes: ["./looks"],
  },
});

const skillMd = "---\nname: NAME\ndescription: A skill.\n---\nBody.";

const resolveCases: ResolveCase[] = [
  {
    name: "manifest-package-with-globs",
    projectTrusted: true,
    files: [
      { path: "agent/settings.json", content: "{}" },
      { path: "pkg/package.json", content: manifestPackageJson },
      { path: "pkg/ext/clip.ts", content: "1" },
      { path: "pkg/ext/glob-a.ts", content: "1" },
      { path: "pkg/ext/glob-legacy.ts", content: "1" },
      { path: "pkg/ext/unlisted.ts", content: "1" },
      { path: "pkg/skillset/one/SKILL.md", content: skillMd.replace("NAME", "one") },
      { path: "pkg/skillset/nested/two/SKILL.md", content: skillMd.replace("NAME", "two") },
      { path: "pkg/docs/prompt-one.md", content: "P1" },
      { path: "pkg/docs/more/prompt-two.md", content: "P2" },
      { path: "pkg/looks/dark.json", content: "{}" },
    ],
    globalSettings: { packages: ["../pkg"] },
  },
  {
    name: "convention-package-discovery",
    projectTrusted: true,
    files: [
      { path: "pkg/extensions/top.ts", content: "1" },
      { path: "pkg/extensions/tool/index.ts", content: "1" },
      { path: "pkg/extensions/tool/helper.ts", content: "1" },
      { path: "pkg/extensions/manifested/package.json", content: '{"pi":{"extensions":["./main.ts"]}}' },
      { path: "pkg/extensions/manifested/main.ts", content: "1" },
      { path: "pkg/extensions/manifested/other.ts", content: "1" },
      { path: "pkg/extensions/plain-dir/readme.txt", content: "no entry" },
      { path: "pkg/extensions/node_modules/dep/index.ts", content: "1" },
      { path: "pkg/skills/root-file.md", content: skillMd.replace("NAME", "root-file") },
      { path: "pkg/skills/folder/SKILL.md", content: skillMd.replace("NAME", "folder") },
      { path: "pkg/skills/folder/extra.md", content: "ignored sibling of SKILL.md" },
      { path: "pkg/skills/.gitignore", content: "skipped/\n" },
      { path: "pkg/skills/skipped/SKILL.md", content: skillMd.replace("NAME", "skipped") },
      { path: "pkg/prompts/prompt.md", content: "P" },
      { path: "pkg/prompts/nested/deep.md", content: "P" },
      { path: "pkg/themes/light.json", content: "{}" },
    ],
    globalSettings: { packages: ["../pkg"] },
  },
  {
    name: "package-filters-and-empty-array",
    projectTrusted: true,
    files: [
      { path: "pkg/extensions/keep.ts", content: "1" },
      { path: "pkg/extensions/legacy.ts", content: "1" },
      { path: "pkg/extensions/other.ts", content: "1" },
      { path: "pkg/themes/dark.json", content: "{}" },
      { path: "pkg/prompts/a.md", content: "P" },
    ],
    globalSettings: {
      packages: [
        {
          source: "../pkg",
          extensions: ["extensions/*.ts", "!extensions/legacy.ts", "-extensions/other.ts", "+extensions/legacy.ts"],
          themes: [],
        },
      ],
    },
  },
  {
    name: "autoload-delta-over-global",
    projectTrusted: true,
    files: [
      { path: "pkg/extensions/bar.ts", content: "1" },
      { path: "pkg/extensions/baz.ts", content: "1" },
    ],
    globalSettings: { packages: ["../pkg"] },
    projectSettings: {
      packages: [{ source: "../../pkg", autoload: false, extensions: ["-extensions/bar.ts"] }],
    },
  },
  {
    name: "dedupe-project-wins",
    projectTrusted: true,
    files: [{ path: "pkg/extensions/ext.ts", content: "1" }],
    globalSettings: { packages: ["../pkg"] },
    projectSettings: { packages: [{ source: "../../pkg", extensions: ["!extensions/ext.ts"] }] },
  },
  {
    name: "top-level-entries-with-patterns",
    projectTrusted: true,
    files: [
      { path: "agent/my-prompts/auto.md", content: "A" },
      { path: "agent/my-prompts/keep.md", content: "K" },
      { path: "project/.pi/exts/one.ts", content: "1" },
      { path: "project/.pi/exts/two.ts", content: "1" },
    ],
    globalSettings: { prompts: ["my-prompts", "!my-prompts/auto.md"] },
    projectSettings: { extensions: ["exts", "!exts/two.ts"] },
  },
  {
    name: "auto-discovery-trusted",
    projectTrusted: true,
    files: [
      { path: "agent/extensions/user-ext.ts", content: "1" },
      { path: "agent/skills/user-skill/SKILL.md", content: skillMd.replace("NAME", "user-skill") },
      { path: "agent/prompts/user.md", content: "U" },
      { path: "agent/themes/user.json", content: "{}" },
      { path: "home/.agents/skills/home-skill/SKILL.md", content: skillMd.replace("NAME", "home-skill") },
      { path: "parent/.agents/skills/parent-skill/SKILL.md", content: skillMd.replace("NAME", "parent-skill") },
      { path: "parent/project/.agents/skills/proj-agents/SKILL.md", content: skillMd.replace("NAME", "proj-agents") },
      { path: "parent/project/.pi/extensions/proj-ext.ts", content: "1" },
      { path: "parent/project/.pi/skills/proj-skill/SKILL.md", content: skillMd.replace("NAME", "proj-skill") },
      { path: "parent/project/.pi/prompts/proj.md", content: "P" },
      { path: "parent/project/.pi/themes/proj.json", content: "{}" },
    ],
    dirs: ["parent/project/.git"],
    globalSettings: { prompts: ["!prompts/user.md"] },
    projectSettings: {},
  },
  {
    name: "auto-discovery-untrusted",
    projectTrusted: false,
    files: [
      { path: "agent/prompts/user.md", content: "U" },
      { path: "parent/project/.agents/skills/proj-agents/SKILL.md", content: skillMd.replace("NAME", "proj-agents") },
      { path: "parent/project/.pi/prompts/proj.md", content: "P" },
      { path: "parent/project/.pi/themes/proj.json", content: "{}" },
    ],
    dirs: ["parent/project/.git"],
    globalSettings: {},
    projectSettings: {},
  },
  {
    name: "local-file-and-bare-dir-sources",
    projectTrusted: true,
    files: [
      { path: "single/tool.ts", content: "1" },
      { path: "bare-dir/readme.txt", content: "no resources" },
    ],
    globalSettings: { packages: ["../single/tool.ts", "../bare-dir"] },
  },
  {
    name: "installed-npm-package",
    projectTrusted: true,
    files: [
      {
        path: "agent/npm/node_modules/@scope/pkg/package.json",
        content: '{"name":"@scope/pkg","version":"1.0.0"}',
      },
      { path: "agent/npm/node_modules/@scope/pkg/prompts/greet.md", content: "G" },
      { path: "agent/npm/node_modules/@scope/pkg/extensions/e.ts", content: "1" },
    ],
    globalSettings: { packages: ["npm:@scope/pkg"] },
  },
  {
    name: "missing-npm-package-offline",
    projectTrusted: true,
    globalSettings: { packages: ["npm:never-installed-pkg"] },
  },
  {
    name: "installed-git-package",
    projectTrusted: true,
    files: [
      { path: "agent/git/github.com/user/repo/prompts/from-git.md", content: "G" },
      { path: "agent/git/github.com/user/repo/themes/git.json", content: "{}" },
    ],
    globalSettings: { packages: ["git:github.com/user/repo@v1"] },
  },
  {
    name: "symlinked-project-and-user-prompts",
    projectTrusted: true,
    files: [{ path: "shared/prompts/shared.md", content: "S" }],
    dirs: ["project/.pi"],
    symlinks: [
      { link: "agent/prompts", target: "shared/prompts" },
      { link: "project/.pi/prompts", target: "shared/prompts" },
    ],
    globalSettings: {},
    projectSettings: {},
  },
];

const settingsCases: SettingsCase[] = [
  {
    name: "replace-ref-preserves-filters",
    initialGlobal: {
      packages: [{ source: "git:github.com/user/repo@v1", skills: ["skills/one.md"] }],
    },
    ops: [
      { op: "add", source: "git:github.com/user/repo@v2" },
      { op: "add", source: "git:github.com/user/repo@v2" },
    ],
  },
  {
    name: "ssh-and-https-share-identity",
    initialGlobal: { packages: ["git:git@github.com:user/repo@v1"] },
    ops: [{ op: "add", source: "https://github.com/user/repo@v2" }],
  },
  {
    name: "local-paths-relative-to-scope-base",
    dirs: ["project/packages/local-package", "shared-package"],
    ops: [
      { op: "add", source: "./packages/local-package" },
      { op: "add", source: "../shared-package", local: true },
      { op: "remove", source: "./packages/local-package/" },
    ],
  },
  {
    name: "remove-npm-by-name",
    initialGlobal: { packages: ["npm:pkg@1.0.0", "npm:other"] },
    ops: [{ op: "remove", source: "npm:pkg" }],
  },
];

const trustOptionCases: TrustOptionCase[] = [
  { name: "nested-project-with-session-options", cwd: "parent/project", dirs: ["parent/project"], includeSessionOnly: true },
  { name: "nested-project-persistent-only", cwd: "parent/project", dirs: ["parent/project"], includeSessionOnly: false },
];

const trustStoreCases: TrustStoreCase[] = [
  {
    name: "inheritance-and-null-deletion",
    dirs: ["parent/project"],
    ops: [
      [{ path: "parent", decision: true }],
      [{ path: "parent/project", decision: false }],
      [{ path: "parent/project", decision: null }],
    ],
    queries: ["parent/project", "parent", "elsewhere"],
  },
  {
    name: "trust-parent-updates",
    dirs: ["parent/project", "other"],
    ops: [
      [{ path: "parent/project", decision: false }],
      [
        { path: "parent", decision: true },
        { path: "parent/project", decision: null },
      ],
      [{ path: "other", decision: false }],
    ],
    queries: ["parent/project", "other"],
  },
];

const hasTrustRequiringCases: HasTrustRequiringCase[] = [
  { name: "plain-project", dirs: ["home", "project"], cwd: "project" },
  {
    name: "home-resources-are-user-scoped",
    dirs: ["home/.pi/agent", "home/.agents/skills"],
    cwd: "home",
  },
  {
    name: "project-settings",
    files: [{ path: "project/.pi/settings.json", content: "{}" }],
    dirs: ["home"],
    cwd: "project",
  },
  { name: "project-extensions-dir", dirs: ["home", "project/.pi/extensions"], cwd: "project" },
  { name: "empty-pi-dir", dirs: ["home", "project/.pi"], cwd: "project" },
  {
    name: "project-system-md",
    files: [{ path: "project/.pi/SYSTEM.md", content: "s" }],
    dirs: ["home"],
    cwd: "project",
  },
  { name: "project-agents-skills", dirs: ["home", "project/.agents/skills"], cwd: "project" },
  { name: "ancestor-agents-skills", dirs: ["home", "parent/.agents/skills", "parent/project"], cwd: "parent/project" },
];

function replaceFixture(value: string, fixtureRoot: string): string {
  return value.split(fixtureRoot).join("<fixture>");
}

async function writeCaseTree(root: string, files?: FileSpec[], dirs?: string[], symlinks?: SymlinkSpec[]): Promise<void> {
  for (const dir of dirs ?? []) {
    await mkdir(path.join(root, dir), { recursive: true });
  }
  for (const file of files ?? []) {
    const filePath = path.join(root, file.path);
    await mkdir(path.dirname(filePath), { recursive: true });
    await writeFile(filePath, file.content);
  }
  for (const link of symlinks ?? []) {
    const linkPath = path.join(root, link.link);
    await mkdir(path.dirname(linkPath), { recursive: true });
    await symlink(path.join(root, link.target), linkPath, "dir");
  }
}

async function withCaseRoot<T>(run: (root: string) => Promise<T>): Promise<T> {
  const root = await mkdtemp(path.join(realpathSync(os.tmpdir()), "pi-go-wp360-"));
  try {
    return await run(root);
  } finally {
    await rm(root, { recursive: true, force: true });
  }
}

export async function generateWP360(upstreamRoot: string, outputRoot: string, upstreamCommit: string): Promise<void> {
  const gitSource = "packages/coding-agent/src/utils/git.ts";
  const packageManagerSource = "packages/coding-agent/src/core/package-manager.ts";
  const settingsSource = "packages/coding-agent/src/core/settings-manager.ts";
  const trustSource = "packages/coding-agent/src/core/trust-manager.ts";

  const { gitModule, packageManagerModule, settingsModule, trustModule } = await withUpstreamModelData(
    upstreamRoot,
    async () => ({
      gitModule: (await import(pathToFileURL(path.join(upstreamRoot, gitSource)).href)) as typeof import(
        "../../.upstream/packages/coding-agent/src/utils/git.ts"
      ),
      packageManagerModule: (await import(
        pathToFileURL(path.join(upstreamRoot, packageManagerSource)).href
      )) as typeof import("../../.upstream/packages/coding-agent/src/core/package-manager.ts"),
      settingsModule: (await import(pathToFileURL(path.join(upstreamRoot, settingsSource)).href)) as typeof import(
        "../../.upstream/packages/coding-agent/src/core/settings-manager.ts"
      ),
      trustModule: (await import(pathToFileURL(path.join(upstreamRoot, trustSource)).href)) as typeof import(
        "../../.upstream/packages/coding-agent/src/core/trust-manager.ts"
      ),
    }),
  );

  for (const fixtureCase of gitUrlCases) {
    const parsed = gitModule.parseGitUrl(fixtureCase.input);
    fixtureCase.expected = parsed
      ? {
          repo: parsed.repo,
          host: parsed.host,
          path: parsed.path,
          ...(parsed.ref !== undefined ? { ref: parsed.ref } : {}),
          pinned: parsed.pinned,
        }
      : null;
  }

  const previousHome = process.env.HOME;
  const previousOffline = process.env.PI_OFFLINE;
  try {
    process.env.PI_OFFLINE = "1";

    for (const fixtureCase of resolveCases) {
      await withCaseRoot(async (root) => {
        process.env.HOME = path.join(root, "home");
        await mkdir(path.join(root, "home"), { recursive: true });
        await mkdir(path.join(root, "agent"), { recursive: true });
        const cwd = path.join(root, fixtureCase.name.startsWith("auto-discovery") ? "parent/project" : "project");
        await mkdir(cwd, { recursive: true });
        await writeCaseTree(root, fixtureCase.files, fixtureCase.dirs, fixtureCase.symlinks);

        const storage = new settingsModule.InMemorySettingsStorage();
        storage.withLock("global", () => JSON.stringify(fixtureCase.globalSettings ?? {}));
        storage.withLock("project", () => JSON.stringify(fixtureCase.projectSettings ?? {}));
        const settingsManager = settingsModule.SettingsManager.fromStorage(storage, {
          projectTrusted: fixtureCase.projectTrusted,
        });
        const packageManager = new packageManagerModule.DefaultPackageManager({
          cwd,
          agentDir: path.join(root, "agent"),
          settingsManager,
        });
        const resolved = await packageManager.resolve(async () => "skip");
        const normalize = (resources: Array<{ path: string; enabled: boolean; metadata: Record<string, unknown> }>) =>
          resources
            .map((resource) => ({
              path: replaceFixture(resource.path, root),
              enabled: resource.enabled,
              metadata: {
                source: replaceFixture(String(resource.metadata.source), root),
                scope: String(resource.metadata.scope),
                origin: String(resource.metadata.origin),
                ...(resource.metadata.baseDir !== undefined
                  ? { baseDir: replaceFixture(String(resource.metadata.baseDir), root) }
                  : {}),
              },
            }))
            .sort((a, b) => (a.path < b.path ? -1 : a.path > b.path ? 1 : 0));
        fixtureCase.expected = {
          extensions: normalize(resolved.extensions),
          skills: normalize(resolved.skills),
          prompts: normalize(resolved.prompts),
          themes: normalize(resolved.themes),
        };
      });
    }

    for (const fixtureCase of settingsCases) {
      await withCaseRoot(async (root) => {
        process.env.HOME = path.join(root, "home");
        await mkdir(path.join(root, "home"), { recursive: true });
        await mkdir(path.join(root, "agent"), { recursive: true });
        const cwd = path.join(root, "project");
        await mkdir(cwd, { recursive: true });
        await writeCaseTree(root, undefined, fixtureCase.dirs);

        const storage = new settingsModule.InMemorySettingsStorage();
        storage.withLock("global", () => JSON.stringify(fixtureCase.initialGlobal ?? {}));
        storage.withLock("project", () => JSON.stringify(fixtureCase.initialProject ?? {}));
        const settingsManager = settingsModule.SettingsManager.fromStorage(storage, { projectTrusted: true });
        const packageManager = new packageManagerModule.DefaultPackageManager({
          cwd,
          agentDir: path.join(root, "agent"),
          settingsManager,
        });
        const changed: boolean[] = [];
        for (const op of fixtureCase.ops) {
          if (op.op === "add") {
            changed.push(packageManager.addSourceToSettings(op.source, { local: op.local }));
          } else {
            changed.push(packageManager.removeSourceFromSettings(op.source, { local: op.local }));
          }
        }
        fixtureCase.expected = {
          changed,
          globalPackages: (settingsManager.getGlobalSettings().packages ?? []) as unknown[],
          projectPackages: (settingsManager.getProjectSettings().packages ?? []) as unknown[],
        };
      });
    }

    for (const fixtureCase of trustOptionCases) {
      await withCaseRoot(async (root) => {
        await writeCaseTree(root, undefined, fixtureCase.dirs);
        const options = trustModule.getProjectTrustOptions(path.join(root, fixtureCase.cwd), {
          includeSessionOnly: fixtureCase.includeSessionOnly,
        });
        fixtureCase.expected = options.map((option) => ({
          label: replaceFixture(option.label, root),
          trusted: option.trusted,
          updates: option.updates.map((update) => ({
            path: replaceFixture(update.path, root),
            decision: update.decision === null ? null : update.decision === true,
          })),
          ...(option.savedPath !== undefined ? { savedPath: replaceFixture(option.savedPath, root) } : {}),
        }));
      });
    }

    for (const fixtureCase of trustStoreCases) {
      await withCaseRoot(async (root) => {
        await mkdir(path.join(root, "agent"), { recursive: true });
        await writeCaseTree(root, undefined, fixtureCase.dirs);
        const store = new trustModule.ProjectTrustStore(path.join(root, "agent"));
        for (const op of fixtureCase.ops) {
          store.setMany(op.map((update) => ({ path: path.join(root, update.path), decision: update.decision })));
        }
        const decisions = fixtureCase.queries.map((query) => store.get(path.join(root, query)));
        const file = await readFile(path.join(root, "agent", "trust.json"), "utf-8");
        fixtureCase.expected = { decisions, file: replaceFixture(file, root) };
      });
    }

    for (const fixtureCase of hasTrustRequiringCases) {
      await withCaseRoot(async (root) => {
        process.env.HOME = path.join(root, "home");
        await mkdir(path.join(root, "home"), { recursive: true });
        await writeCaseTree(root, fixtureCase.files, fixtureCase.dirs);
        await mkdir(path.join(root, fixtureCase.cwd), { recursive: true });
        fixtureCase.expected = trustModule.hasTrustRequiringProjectResources(path.join(root, fixtureCase.cwd));
      });
    }
  } finally {
    if (previousHome === undefined) {
      delete process.env.HOME;
    } else {
      process.env.HOME = previousHome;
    }
    if (previousOffline === undefined) {
      delete process.env.PI_OFFLINE;
    } else {
      process.env.PI_OFFLINE = previousOffline;
    }
  }

  const familyDir = path.join(outputRoot, "WP360");
  await mkdir(familyDir, { recursive: true });
  const manifest = {
    family: "WP360",
    upstreamCommit,
    generator: "conformance/extract/wp360-packages.ts",
    source: packageManagerSource,
    additionalSources: [gitSource, settingsSource, trustSource],
    files: ["cases.json"],
  };
  await writeFile(path.join(familyDir, "manifest.json"), `${JSON.stringify(manifest, null, 2)}\n`);
  await writeFile(
    path.join(familyDir, "cases.json"),
    `${JSON.stringify(
      {
        schemaVersion: 1,
        gitUrlCases,
        resolveCases,
        settingsCases,
        trustOptionCases,
        trustStoreCases,
        hasTrustRequiringCases,
      },
      null,
      2,
    )}\n`,
  );
}
