"use strict";

const { spawnSync } = require("node:child_process");
const path = require("node:path");

process.env.NEXT_FONT_GOOGLE_MOCKED_RESPONSES = path.join(
  __dirname,
  "responses.cjs",
);

const nextBin = require.resolve("next/dist/bin/next");
const result = spawnSync(process.execPath, [nextBin, ...process.argv.slice(2)], {
  stdio: "inherit",
  env: process.env,
});

process.exit(result.status ?? 1);
