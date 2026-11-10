// Reproduce the fork web deps stage without building an image.
// The stage copies a narrow input set and then runs `pnpm install --frozen-lockfile`.
// Root patchedDependencies is resolved from that input set, so a missing patch
// fails while pnpm is reading local files. `--ignore-scripts --offline` keeps the
// check on that input read and does not perform a production image build.

import { spawnSync } from "node:child_process";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";

const root = path.resolve(import.meta.dirname, "../..");
const dockerfilePath = path.join(root, "fork/web.Dockerfile");

function fail(message) {
  console.error("web_deps_inputs=fail");
  console.error(message);
  process.exit(1);
}

function depsStage(text) {
  const lines = text.split("\n");
  let active = false;
  const stage = [];
  for (const line of lines) {
    if (/^FROM\s+\S+\s+AS\s+deps\s*$/.test(line.trim())) {
      active = true;
      continue;
    }
    if (active && line.startsWith("FROM ")) break;
    if (active) stage.push(line);
  }
  if (!active) fail("deps stage not found in fork/web.Dockerfile");
  return stage;
}

function logicalLines(lines) {
  const out = [];
  let current = "";
  for (const raw of lines) {
    const trimmed = raw.trim();
    if (!current && (!trimmed || trimmed.startsWith("#"))) continue;
    const continued = trimmed.endsWith("\\");
    current = `${current} ${continued ? trimmed.slice(0, -1).trim() : trimmed}`.trim();
    if (!continued) {
      if (current && !current.startsWith("#")) out.push(current);
      current = "";
    }
  }
  if (current) out.push(current);
  return out;
}

function tokenize(line) {
  return line.split(/\s+/).filter(Boolean);
}

function materialize(stageDir, instruction) {
  const tokens = tokenize(instruction);
  if (tokens[0] !== "COPY") return false;
  if (tokens.slice(1, -1).some((token) => token.startsWith("--"))) {
    fail(`unsupported COPY flags: ${instruction}`);
  }
  const destToken = tokens.at(-1);
  const sources = tokens.slice(1, -1);
  if (sources.length === 0) fail(`COPY has no source: ${instruction}`);
  const destIsDir = destToken.endsWith("/") || destToken === "." || destToken === "./" || sources.length > 1;
  for (const sourceToken of sources) {
    const relativeSource = sourceToken.replace(/^\.\//, "").replace(/\/$/, "");
    const absoluteSource = path.join(root, relativeSource);
    if (!fs.existsSync(absoluteSource)) fail(`COPY source missing from repo: ${relativeSource}`);
    const sourceStat = fs.statSync(absoluteSource);
    if (destIsDir) {
      const destRelative = destToken === "." || destToken === "./" ? "" : destToken.replace(/^\.\//, "").replace(/\/$/, "");
      const destDir = path.join(stageDir, destRelative);
      fs.mkdirSync(destDir, { recursive: true });
      if (sourceStat.isDirectory()) {
        for (const name of fs.readdirSync(absoluteSource)) {
          fs.cpSync(path.join(absoluteSource, name), path.join(destDir, name), { recursive: true });
        }
      } else {
        fs.copyFileSync(absoluteSource, path.join(destDir, path.basename(absoluteSource)));
      }
    } else if (sourceStat.isDirectory()) {
      fail(`directory COPY destination must be a directory: ${instruction}`);
    } else {
      const output = path.join(stageDir, destToken.replace(/^\.\//, ""));
      fs.mkdirSync(path.dirname(output), { recursive: true });
      fs.copyFileSync(absoluteSource, output);
    }
  }
  return true;
}

const copies = logicalLines(depsStage(fs.readFileSync(dockerfilePath, "utf8"))).filter((line) => line.startsWith("COPY "));
if (copies.length === 0) fail("deps stage has no COPY instructions");

const stageDir = fs.mkdtempSync(path.join(os.tmpdir(), "fork-web-deps-"));
const temporaryRoot = path.resolve(os.tmpdir());
if (path.relative(temporaryRoot, stageDir).startsWith("..")) fail(`refusing temp dir outside tmp: ${stageDir}`);

try {
  for (const instruction of copies) materialize(stageDir, instruction);
  const result = spawnSync(
    "pnpm",
    ["install", "--frozen-lockfile", "--ignore-scripts", "--offline"],
    {
      cwd: stageDir,
      encoding: "utf8",
      maxBuffer: 32 * 1024 * 1024,
      timeout: 300000,
      env: { ...process.env, CI: "1" },
    },
  );
  if (result.error) fail(result.error.message);
  if (result.status !== 0) {
    const output = `${result.stdout ?? ""}\n${result.stderr ?? ""}`.trim();
    fail(output.slice(-4000) || `pnpm install exited ${result.status}`);
  }
  console.log("web_deps_inputs=ok");
  console.log(`copies=${copies.length}`);
  console.log("install=frozen-lockfile-ignore-scripts-offline");
} finally {
  fs.rmSync(stageDir, { recursive: true, force: true });
}
