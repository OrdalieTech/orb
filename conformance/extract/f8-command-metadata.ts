import { copyFile, mkdir, readFile, readdir, writeFile } from "node:fs/promises";
import path from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";

// Only these upstream-owned command values change; Orb render snapshots stay intact.
export async function generateCommandMetadata(upstreamRoot: string, outputRoot: string): Promise<void> {
 const baseline = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "../fixtures/F12-commands/commands.json");
 const fixture = JSON.parse(await readFile(baseline,"utf8"));
 const slash = await import(pathToFileURL(path.join(upstreamRoot,"packages/coding-agent/src/core/slash-commands.ts")).href);
 const source = await readFile(path.join(upstreamRoot,"packages/coding-agent/src/modes/interactive/interactive-mode.ts"),"utf8");
 const ts = await import(pathToFileURL(path.join(upstreamRoot,"node_modules/typescript/lib/typescript.js")).href);
 const method = source.slice(source.indexOf("private handleThinkingCommand("), source.indexOf("private selectThinkingLevel("));
 if (!method.startsWith("private handleThinkingCommand(")) throw new Error("upstream thinking handler missing");
 const emitted = ts.transpileModule(`class Probe {${method}}`,{compilerOptions:{target:ts.ScriptTarget.ES2022}}).outputText;
 const Probe = new Function(`${emitted}; return Probe;`)();
 fixture.visible = slash.BUILTIN_SLASH_COMMANDS.map((command: any) => ({name:command.name, description:command.description, argumentHint:command.argumentHint ?? null, visible:true}));
 fixture.dispatch = [...source.matchAll(/if \(text === "\/([a-z-]+)"/g)].map((match)=>match[1]);
 if (new Set(fixture.dispatch).size !== fixture.dispatch.length || !fixture.dispatch.includes("thinking")) throw new Error("unexpected upstream slash dispatch shape");
 fixture.thinkingCommands = ["", "HiGh", "invalid"].map((input) => {
  const trace: unknown[] = [];
  Probe.prototype.handleThinkingCommand.call({
   session:{getAvailableThinkingLevels:()=>["off","low","medium","high"]},
   showThinkingSelector:()=>trace.push({selector:true}),
   selectThinkingLevel:(level:string,persist:boolean)=>trace.push({level,persist}),
   showError:(error:string)=>trace.push({error}),
  },input);
  return {input,trace};
 });
 const destination = path.resolve(outputRoot,"F12-commands");
 await mkdir(destination,{recursive:true});
 for (const name of await readdir(path.dirname(baseline))) {
  const sourceFile = path.join(path.dirname(baseline), name);
  const targetFile = path.join(destination, name);
  if (sourceFile !== targetFile) await copyFile(sourceFile, targetFile);
 }
 await writeFile(path.join(destination,"commands.json"),`${JSON.stringify(fixture,null,2)}\n`);
}
