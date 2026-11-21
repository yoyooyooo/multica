import fs from "node:fs";

const [path, baselinePath] = process.argv.slice(2);
if (!path) throw new Error("usage: validate-census.mjs <path> [baseline-path]");
const census = JSON.parse(fs.readFileSync(path, "utf8"));
const expected = census.summary?.live;
if (census.schema !== "multica.external-pr-census.v1") throw new Error("unexpected census schema");
if (!Number.isSafeInteger(expected) || expected < 0) throw new Error("invalid live row count");
if (!Array.isArray(census.rows) || census.rows.length !== expected) throw new Error("census row coverage mismatch");
const strict = census.rows.filter((row) => row.disposition === "keep_strict").length;
const preserved = census.rows.filter((row) => row.disposition === "preserve_read_only").length;
if (strict !== census.summary.keep_strict || preserved !== census.summary.preserve_read_only) {
  throw new Error("census summary does not match rows");
}
for (const row of census.rows) {
  if (!row.id || !row.workspace_id || !row.issue_id) throw new Error("census row is missing explicit identity");
  if (row.disposition === "preserve_read_only" && row.reason !== "missing_merge_authority") {
    throw new Error(`unsupported preserved disposition for ${row.id}`);
  }
}
if (new Set(census.rows.map((row) => row.id)).size !== expected) throw new Error("duplicate census row identity");
if (baselinePath) {
  const baseline = JSON.parse(fs.readFileSync(baselinePath, "utf8"));
  if (baseline.schema !== census.schema) throw new Error("baseline census schema mismatch");
  const canonicalRows = (rows) => rows.map(({ id, workspace_id, issue_id, state, disposition, reason }) =>
    [id, workspace_id, issue_id, state, disposition, reason]).sort((a, b) => a[0].localeCompare(b[0]));
  if (JSON.stringify(canonicalRows(baseline.rows)) !== JSON.stringify(canonicalRows(census.rows))) {
    throw new Error("External PR identities or recorded state changed since the deployment baseline");
  }
  if (baseline.summary.excluded_orphan_historical !== census.summary.excluded_orphan_historical) {
    throw new Error("historical orphan count changed since the deployment baseline");
  }
  console.log("baseline_match=ok");
}
console.log(`live=${expected}`);
console.log(`keep_strict=${strict}`);
console.log(`preserve_read_only=${preserved}`);
console.log(`excluded_orphan_historical=${census.summary.excluded_orphan_historical}`);
