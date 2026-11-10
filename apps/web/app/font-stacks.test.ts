// @vitest-environment node
import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import { describe, expect, it } from "vitest";

const read = (path: string) => readFileSync(resolve(__dirname, path), "utf8");
const webCss = read("./globals.css");
const desktopCss = read("../../desktop/src/renderer/src/globals.css");
const layout = read("./layout.tsx");

const GENERIC = /^(?:serif|sans-serif|monospace|cursive|fantasy|system-ui|math|emoji|fangsong|ui-[a-z-]+)$/;

function rootVariable(css: string, name: string): string {
  const match = css.match(new RegExp(`^\\s*${name}:\\s*([^;]+);`, "m"));
  if (!match?.[1]) throw new Error(`${name} is not declared`);
  return match[1];
}

function families(stack: string, css: string): string[] {
  return stack.split(",").flatMap((part) => {
    const family = part.trim().replace(/^["']|["']$/g, "");
    const variable = family.match(/^var\((--[\w-]+)\)$/)?.[1];
    return variable ? families(rootVariable(css, variable), css) : [family];
  });
}

describe("Appearance Latin faces", () => {
  // The CJK choice follows these in --font-sans and --font-content, so a
  // generic family here would draw Han before the CJK font is reached.
  const names = ["--font-ui-inter", "--font-ui-serif", "--font-ui-mono"];

  it.each(names)("web %s names families only", (name) => {
    expect(families(rootVariable(webCss, name), webCss).filter((f) => GENERIC.test(f))).toEqual([]);
  });

  it.each(names)("desktop %s names families only", (name) => {
    expect(families(rootVariable(desktopCss, name), desktopCss).filter((f) => GENERIC.test(f))).toEqual([]);
  });

  it("keeps the onboarding serif ending with its generic fallbacks", () => {
    expect(families(rootVariable(webCss, "--font-serif"), webCss).slice(-2)).toEqual(["Times New Roman", "serif"]);
    expect(families(rootVariable(webCss, "--font-serif"), webCss)).toContain("ui-serif");
  });

  it("does not load web faces from next/font", () => {
    expect(layout).not.toMatch(/from ["']next\/font\/google["']/);
    expect(layout).toContain("@fontsource-variable/inter/opsz.css");
  });
});
