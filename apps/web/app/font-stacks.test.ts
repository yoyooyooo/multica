// @vitest-environment node
import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import { describe, expect, it } from "vitest";

const read = (path: string) => readFileSync(resolve(__dirname, path), "utf8");
const webCss = read("./globals.css");
const desktopCss = read("../../desktop/src/renderer/src/globals.css");

const GENERIC = /^(?:serif|sans-serif|monospace|cursive|fantasy|system-ui|math|emoji|fangsong|ui-[a-z-]+)$/;

function rootVariable(css: string, name: string): string {
  const match = css.match(new RegExp(`^\\s*${name}:\\s*([^;]+);`, "m"));
  if (!match?.[1]) throw new Error(`${name} is not declared`);
  return match[1];
}

function families(css: string, stack: string): string[] {
  return stack.split(",").flatMap((part) => {
    const family = part.trim().replace(/^["']|["']$/g, "");
    const variable = family.match(/^var\((--[\w-]+)\)$/)?.[1];
    return variable ? families(css, rootVariable(css, variable)) : [family];
  });
}

describe("Appearance Latin faces", () => {
  // The CJK choice follows these in --font-sans and --font-content, so a
  // generic family here would draw Han before the CJK font is reached.
  const names = ["--font-ui-inter", "--font-ui-serif", "--font-ui-mono"];

  it.each(names)("web %s names families only", (name) => {
    expect(families(webCss, rootVariable(webCss, name)).filter((f) => GENERIC.test(f))).toEqual([]);
  });

  it.each(names)("desktop %s names families only", (name) => {
    expect(families(desktopCss, rootVariable(desktopCss, name)).filter((f) => GENERIC.test(f))).toEqual([]);
  });

  it("keeps the onboarding serif ending with its generic fallbacks", () => {
    expect(families(webCss, rootVariable(webCss, "--font-serif")).slice(-2)).toEqual(["Times New Roman", "serif"]);
    expect(families(webCss, rootVariable(webCss, "--font-serif"))).toContain("ui-serif");
  });
});
