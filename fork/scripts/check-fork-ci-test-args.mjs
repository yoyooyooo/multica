// Fork CI must keep running every non-mobile workspace test script, but
// Vitest-only passthrough flags cannot be forwarded to `node --test`.
// Turbo's package selection is taken from `turbo test --dry-run`, not reimplemented.

import { spawnSync } from "node:child_process";
import fs from "node:fs";
import path from "node:path";

const root = path.resolve(import.meta.dirname, "../..");
const workflowPath = path.join(root, ".github/workflows/fork-ci.yml");
const STEP = "Typecheck and test frontend workspaces";
const SCOPE_EXCLUSIONS = new Set(["@multica/mobile"]);

function fail(messages) {
  console.error("fork_ci_test_args=fail");
  for (const message of messages) console.error(message);
  process.exit(1);
}

function logicalLines(script) {
  const lines = [];
  let current = "";
  let continued = false;
  for (const raw of script.split("\n")) {
    const trimmed = raw.trim();
    if (!trimmed || trimmed.startsWith("#")) continue;
    continued = trimmed.endsWith("\\");
    const piece = continued ? trimmed.slice(0, -1).trim() : trimmed;
    current = `${current} ${piece}`.trim();
    if (!continued) {
      lines.push(current);
      current = "";
    }
  }
  if (current) lines.push(current.trim());
  return lines;
}

function tokenize(line) {
  const tokens = [];
  let current = "";
  let quote = "";
  for (const char of line) {
    if (quote) {
      if (char === quote) quote = "";
      else current += char;
      continue;
    }
    if (char === "'" || char === '"') {
      quote = char;
      continue;
    }
    if (/\s/.test(char)) {
      if (current) tokens.push(current);
      current = "";
      continue;
    }
    current += char;
  }
  if (quote) fail([`unclosed quote in fork CI command: ${line}`]);
  if (current) tokens.push(current);
  return tokens;
}

function frontendStep(workflow) {
  const marker = `- name: ${STEP}`;
  const start = workflow.indexOf(marker);
  if (start < 0) fail([`missing workflow step: ${STEP}`]);
  const rest = workflow.slice(start + marker.length);
  const next = rest.search(/\n {6}- name:/);
  const block = next < 0 ? rest : rest.slice(0, next);
  const run = block.match(/run:\s*\|\n([\s\S]*)$/);
  if (!run) fail([`workflow step has no literal run block: ${STEP}`]);
  return run[1];
}

function turboTestCommands(script) {
  const commands = [];
  for (const line of logicalLines(script)) {
    const tokens = tokenize(line);
    const index = tokens.findIndex((token, position) => token === "turbo" && tokens[position + 1] === "test");
    if (index < 0) continue;
    const after = tokens.slice(index + 2);
    const separator = after.indexOf("--");
    commands.push({
      turboArgs: separator < 0 ? after : after.slice(0, separator),
      passthrough: separator < 0 ? [] : after.slice(separator + 1),
    });
  }
  return commands;
}

function workspaceTests() {
  const tests = new Map();
  for (const parent of ["apps", "packages"]) {
    const directory = path.join(root, parent);
    for (const entry of fs.readdirSync(directory, { withFileTypes: true })) {
      if (!entry.isDirectory()) continue;
      const manifestPath = path.join(directory, entry.name, "package.json");
      if (!fs.existsSync(manifestPath)) continue;
      const manifest = JSON.parse(fs.readFileSync(manifestPath, "utf8"));
      if (typeof manifest.scripts?.test === "string" && manifest.name) {
        tests.set(manifest.name, manifest.scripts.test);
      }
    }
  }
  return tests;
}

function hasVitestPassthrough(args) {
  return args.some((arg) => /^(?:--maxWorkers|--retry)(?:=|$)/.test(arg));
}

function rejectsVitestPassthrough(script) {
  return script.includes("node --test") || !/\bvitest\b/.test(script);
}

function selectedTestPackages(turboArgs) {
  const result = spawnSync("pnpm", ["exec", "turbo", "test", "--dry-run=json", ...turboArgs], {
    cwd: root,
    encoding: "utf8",
    maxBuffer: 32 * 1024 * 1024,
  });
  if (result.error) fail([`turbo dry-run failed to start: ${result.error.message}`]);
  if (result.status !== 0) {
    fail([
      `turbo dry-run exited ${result.status}`,
      (result.stderr || result.stdout || "").slice(-2000),
    ]);
  }
  const start = result.stdout.indexOf("{");
  if (start < 0) fail(["turbo dry-run returned no JSON"]);
  const parsed = JSON.parse(result.stdout.slice(start));
  return [...new Set((parsed.tasks ?? []).filter((task) => task.task === "test").map((task) => task.package))];
}

const tests = workspaceTests();
const commands = turboTestCommands(frontendStep(fs.readFileSync(workflowPath, "utf8")));
if (commands.length === 0) fail([`no turbo test command in ${STEP}`]);

const failures = [];
const scheduled = new Map();
commands.forEach((command, index) => {
  const packages = selectedTestPackages(command.turboArgs);
  const passthrough = command.passthrough.join(" ") || "none";
  for (const name of packages) {
    const script = tests.get(name);
    if (!script) continue;
    const runs = scheduled.get(name) ?? [];
    runs.push(passthrough);
    scheduled.set(name, runs);
    if (hasVitestPassthrough(command.passthrough) && rejectsVitestPassthrough(script)) {
      failures.push(
        `command ${index + 1} forwards [${command.passthrough.join(" ")}] to ${name} (${script})`,
      );
    }
  }
});

for (const name of tests.keys()) {
  if (SCOPE_EXCLUSIONS.has(name)) continue;
  if (!scheduled.has(name)) failures.push(`workspace test script is not scheduled: ${name}`);
}

if (failures.length > 0) fail(failures);

const summary = [...scheduled.keys()].sort().map((name) => `${name}[${scheduled.get(name).join(" | ")}]`).join(",");
console.log("fork_ci_test_args=ok");
console.log(`scheduled=${summary}`);
