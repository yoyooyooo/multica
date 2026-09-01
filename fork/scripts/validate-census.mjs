import fs from "node:fs";

const [path, expectedRaw] = process.argv.slice(2);
if (!path || !expectedRaw) throw new Error("usage: validate-census.mjs <path> <expected-live>");
const census = JSON.parse(fs.readFileSync(path, "utf8"));
const expected = Number(expectedRaw);
if (census.schema !== "multica.external-pr-census.v1") throw new Error("unexpected census schema");
if (census.summary.live !== expected) throw new Error(`live rows ${census.summary.live}, expected ${expected}`);
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
console.log(`live=${expected}`);
console.log(`keep_strict=${strict}`);
console.log(`preserve_read_only=${preserved}`);
console.log(`excluded_orphan_historical=${census.summary.excluded_orphan_historical}`);
