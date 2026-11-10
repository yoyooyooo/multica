// The fork web image copies Fontsource license files from the installed web
// workspace. The copied package set must equal @multica/web's direct Fontsource
// dependencies, and each copied LICENSE must exist. A retired face is not
// reintroduced to satisfy an old copy command.

import fs from "node:fs";
import path from "node:path";

const root = path.resolve(import.meta.dirname, "../..");
const manifest = JSON.parse(fs.readFileSync(path.join(root, "apps/web/package.json"), "utf8"));
const dockerfile = fs.readFileSync(path.join(root, "fork/web.Dockerfile"), "utf8");

const declared = Object.keys({ ...manifest.dependencies, ...manifest.devDependencies })
  .filter((name) => name.startsWith("@fontsource/") || name.startsWith("@fontsource-variable/"))
  .sort();

const copied = [];
for (const raw of dockerfile.split("\n")) {
  const line = raw.trim().replace(/\\$/, "").replace(/&&$/, "").trim();
  if (!line.startsWith("cp ")) continue;
  const tokens = line.split(/\s+/);
  const source = tokens[1];
  const destination = tokens[2];
  if (!source || !destination) continue;
  const match = source.match(/^apps\/web\/node_modules\/(@fontsource(?:-variable)?\/[^/]+)\/LICENSE$/);
  if (!match) continue;
  copied.push({ name: match[1], source, destination });
}

const failures = [];
const copiedNames = copied.map((item) => item.name).sort();
if (copiedNames.join("\n") !== declared.join("\n")) {
  failures.push(`copied [${copiedNames.join(", ")}] != web Fontsource deps [${declared.join(", ")}]`);
}
const destinations = new Set();
for (const item of copied) {
  if (destinations.has(item.destination)) failures.push(`duplicate license destination: ${item.destination}`);
  destinations.add(item.destination);
  if (!item.destination.startsWith("/font-licenses/")) {
    failures.push(`license destination is outside /font-licenses: ${item.destination}`);
  }
  if (!fs.existsSync(path.join(root, item.source))) failures.push(`missing license file: ${item.source}`);
}
for (const name of declared) {
  const relative = path.join("apps/web/node_modules", name, "LICENSE");
  if (!fs.existsSync(path.join(root, relative))) failures.push(`installed Fontsource license missing: ${relative}`);
}

if (failures.length > 0) {
  console.error("web_font_licenses=fail");
  for (const failure of failures) console.error(failure);
  process.exit(1);
}

console.log("web_font_licenses=ok");
console.log(`packages=${declared.join(",")}`);
