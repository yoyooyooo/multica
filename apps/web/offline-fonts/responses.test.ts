import { createRequire } from "node:module";
import { existsSync, readFileSync, statSync } from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";

const bundleDir = path.dirname(fileURLToPath(import.meta.url));
const repoRoot = path.resolve(bundleDir, "../../..");
const require = createRequire(import.meta.url);
const requests = require("./font-requests.json") as {
  sourceFiles: string[];
  calls: Array<{
    functionName: string;
    options: Record<string, unknown>;
  }>;
};
const {
  validateGoogleFontFunctionCall,
} = require("next/dist/compiled/@next/font/dist/google/validate-google-font-function-call") as {
  validateGoogleFontFunctionCall: (
    functionName: string,
    options: Record<string, unknown>,
  ) => {
    fontFamily: string;
    weights: string[];
    styles: string[];
    selectedVariableAxes?: string[];
    display: string;
  };
};
const {
  getFontAxes,
} = require("next/dist/compiled/@next/font/dist/google/get-font-axes") as {
  getFontAxes: (
    fontFamily: string,
    weights: string[],
    styles: string[],
    selectedVariableAxes?: string[],
  ) => unknown;
};
const {
  getGoogleFontsUrl,
} = require("next/dist/compiled/@next/font/dist/google/get-google-fonts-url") as {
  getGoogleFontsUrl: (
    fontFamily: string,
    axes: unknown,
    display: string,
  ) => string;
};

function importedGoogleFonts(relPath: string): string[] {
  const source = readFileSync(path.join(repoRoot, relPath), "utf8");
  const match = source.match(
    /import\s+\{([^}]+)\}\s+from\s+["']next\/font\/google["']/,
  );
  if (!match) {
    return [];
  }
  const importedNames = match[1];
  if (!importedNames) {
    return [];
  }
  return importedNames
    .split(",")
    .map((part) => part.trim())
    .filter(Boolean);
}

function expectedCssUrls(): string[] {
  return requests.calls.map((request) => {
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
    return getGoogleFontsUrl(
      validated.fontFamily,
      axes,
      validated.display,
    );
  });
}

describe("offline next/font/google mock", () => {
  it("covers every apps/web next/font/google family", () => {
    const imported = requests.sourceFiles.flatMap(importedGoogleFonts);
    expect(new Set(imported)).toEqual(
      new Set(requests.calls.map((request) => request.functionName)),
    );
  });

  it("maps current Next Google CSS URLs to existing local files", () => {
    const mocked = require("./responses.cjs") as Record<string, string>;
    const cssUrls = expectedCssUrls();

    expect(Object.keys(mocked).sort()).toEqual([...cssUrls].sort());

    const files = new Set<string>();
    for (const cssUrl of cssUrls) {
      const css = mocked[cssUrl];
      expect(css).toBeTruthy();
      if (!css) {
        throw new Error(`missing offline font response for ${cssUrl}`);
      }
      for (const match of css.matchAll(/src: url\(([^)]+)\)/g)) {
        const filePath = match[1];
        if (filePath) {
          files.add(filePath);
        }
      }
    }

    expect(files.size).toBeGreaterThan(0);
    for (const filePath of files) {
      expect(filePath.startsWith("/")).toBe(true);
      expect(existsSync(filePath)).toBe(true);
      expect(statSync(filePath).size).toBeGreaterThan(0);
    }
  });

  it("is the default Dockerfile.web build path", () => {
    const dockerfile = readFileSync(
      path.join(repoRoot, "Dockerfile.web"),
      "utf8",
    );
    expect(dockerfile).toContain(
      "NEXT_FONT_GOOGLE_MOCKED_RESPONSES=/app/apps/web/offline-fonts/responses.cjs",
    );
    expect(dockerfile).toContain("test -d /app/apps/web/.next/standalone");
  });
});
