"use strict";

const path = require("node:path");
const cssByUrl = require("./css-by-url.json");

const filesDir = path.join(__dirname, "files").split(path.sep).join("/");

module.exports = Object.fromEntries(
  Object.entries(cssByUrl).map(([url, css]) => [
    url,
    css.replaceAll("__NEXT_FONT_FILES__", filesDir),
  ]),
);
