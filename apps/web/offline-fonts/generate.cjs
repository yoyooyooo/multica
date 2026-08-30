"use strict";

const crypto = require("node:crypto");
const fs = require("node:fs");
const path = require("node:path");
const { createRequire } = require("node:module");

const bundleDir = __dirname;
const filesDir = path.join(bundleDir, "files");
const requests = require("./font-requests.json");
const webRequire = createRequire(path.join(__dirname, "../package.json"));
const {
  validateGoogleFontFunctionCall,
} = webRequire("next/dist/compiled/@next/font/dist/google/validate-google-font-function-call");
const {
  getFontAxes,
} = webRequire("next/dist/compiled/@next/font/dist/google/get-font-axes");
const {
  getGoogleFontsUrl,
} = webRequire("next/dist/compiled/@next/font/dist/google/get-google-fonts-url");

const urlPattern =
  /https:\/\/fonts\.gstatic\.com\/[^)\s]+\.(?:woff2|woff|ttf|otf|eot)/g;

async function fetchText(url) {
  const response = await fetch(url, {
    headers: {
      "user-agent":
        "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/122.0.0.0 Safari/537.36",
    },
  });
  if (!response.ok) {
    throw new Error(`GET ${url} failed: ${response.status}`);
  }
  return response.text();
}

async function fetchBuffer(url) {
  const response = await fetch(url);
  if (!response.ok) {
    throw new Error(`GET ${url} failed: ${response.status}`);
  }
  return Buffer.from(await response.arrayBuffer());
}

async function main() {
  fs.mkdirSync(filesDir, { recursive: true });
  const cssByUrl = {};
  const downloads = new Map();

  for (const request of requests.calls) {
    const validated = validateGoogleFontFunctionCall(
      request.functionName,
      request.options,
    );
    const axes = getFontAxes(
      validated.fontFamily,
      validated.weights,
      validated.styles,
      validated.selectedVariableAxes,
    );
    const cssUrl = getGoogleFontsUrl(
      validated.fontFamily,
      axes,
      validated.display,
    );
    let css = await fetchText(cssUrl);
    css = css.replace(urlPattern, (remoteUrl) => {
      const extension = path.extname(new URL(remoteUrl).pathname);
      const filename = `${crypto.createHash("sha256").update(remoteUrl).digest("hex").slice(0, 24)}${extension}`;
      downloads.set(remoteUrl, filename);
      return `__NEXT_FONT_FILES__/${filename}`;
    });
    cssByUrl[cssUrl] = css;
  }

  for (const [remoteUrl, filename] of downloads) {
    const dest = path.join(filesDir, filename);
    if (!fs.existsSync(dest)) {
      fs.writeFileSync(dest, await fetchBuffer(remoteUrl));
    }
  }

  const manifest = [...downloads].map(([url, filename]) => ({ url, filename }));
  fs.writeFileSync(
    path.join(bundleDir, "download-manifest.json"),
    `${JSON.stringify(manifest, null, 2)}\n`,
  );
  fs.writeFileSync(
    path.join(bundleDir, "css-by-url.json"),
    `${JSON.stringify(cssByUrl, null, 2)}\n`,
  );
  console.log(
    JSON.stringify(
      {
        cssResponses: Object.keys(cssByUrl).length,
        fontFiles: manifest.length,
      },
      null,
      2,
    ),
  );
}

main().catch((error) => {
  console.error(error);
  process.exit(1);
});
