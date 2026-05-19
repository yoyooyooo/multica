/**
 * `markdownStyle` prop value for `EnrichedMarkdownText`. Driven by RNR
 * theme tokens (`apps/mobile/lib/theme.ts`, mirroring CSS variables in
 * `apps/mobile/global.css`) so colors track light/dark automatically.
 *
 * Why a hook instead of a static object: enriched-markdown is a native
 * (md4c → NSAttributedString / Spannable) layer that only accepts an
 * imperative style object — it can NOT consume NativeWind classNames.
 * The hook is the bridge: it reads the current colorScheme via the same
 * `useColorScheme` everything else in the app uses, and rebuilds the
 * style object whenever the theme flips.
 *
 * Sizing follows the mobile typography scale documented in
 * `apps/mobile/docs/markdown-renderer-research.md` → "Mobile typography
 * scale" (calibrated against Apple HIG; one tier below shadcn web defaults
 * because markdown headings inside an issue card are structural, not
 * screen titles). HIG values are encoded in `MD_FONT` / `MD_LINE` /
 * `MD_GAP` constants — these are NOT RNR tokens to replace; they are
 * mobile-specific design constants validated by the 2026-05-09 inline-
 * code incident.
 */
import { useMemo } from "react";
import { Platform } from "react-native";
import { THEME } from "@/lib/theme";
import { useColorScheme } from "@/lib/use-color-scheme";

/**
 * Monospace font family for inline code. enriched-markdown's default is
 * `''` (inherit) on native — so without this override, inline code on
 * iOS/Android renders in the same proportional font as the surrounding
 * paragraph, losing its only remaining visual identification once we
 * remove the chip background.
 */
const MONO_FONT = Platform.select({
  ios: "Menlo",
  android: "monospace",
  default: "monospace",
});

/**
 * Typography scale — Apple HIG-calibrated, one tier below shadcn web.
 * See `docs/markdown-renderer-research.md` "Mobile typography scale".
 */
const MD_FONT = {
  body: 14,
  h1: 20,
  h2: 18,
  h3: 16,
  h4: 14,
  h5: 14,
  h6: 12,
  codeBlock: 13,
} as const;

const MD_LINE = {
  // Body lineHeight 24 on fontSize 14 = ratio 1.71. Generous for CJK
  // paragraph readability (PingFang SC glyphs are taller than SF and
  // benefit from ≥1.5 leading).
  //
  // A 2026-05-19 attempt to reduce this to 20 (ratio 1.43) — to "fix"
  // the inline-code chip's top-heavy padding — was REVERTED on the same
  // day after evidence ruled out lineHeight as the actual root cause.
  // Real root cause: enriched-markdown has hardcoded inline-code padding
  // (upstream issue #255, maintainer unresponsive as of 2026-05). The
  // chip artifact is library-specific, not RN-platform-wide. Discord /
  // Slack / Telegram / Mattermost mobile all use background + mono with
  // no visible top-heavy issue, confirming this is enriched's bug, not
  // an RN+iOS structural limitation. See:
  //   - docs/markdown-rendering-adr.md "Known limitations"
  //   - docs/markdown-renderer-research.md decision log 2026-05-19
  body: 24,
  // Heading lineHeights match each heading's fontSize × ~1.3. We MUST
  // pass these explicitly: enriched-markdown's heading defaults are
  // (h1: 36, h2: 30, h3: 26, ...) calibrated for THEIR default fontSize
  // (30/24/20/...). Mobile uses smaller heading fontSizes (20/18/16/...)
  // but if we don't override lineHeight, enriched keeps its huge defaults
  // — h1 at 20pt fontSize + 36pt lineHeight reads as wildly over-spaced.
  h1: 28,
  h2: 24,
  h3: 22,
  h4: 20,
  h5: 20,
  h6: 18,
} as const;

const MD_GAP = {
  paragraph: 12,
  headingTopLarge: 16,
  headingTopSmall: 12,
  headingBottomLarge: 8,
  headingBottomSmall: 6,
} as const;

// Inline code style — see the `code:` entry in the style object below
// for the full history (2026-05-19 transparent + brand tint workaround
// → 2026-05-19 reverted to subtle surface-2 chip + foreground text).

export function useMarkdownStyle() {
  const { isDarkColorScheme } = useColorScheme();
  const t = isDarkColorScheme ? THEME.dark : THEME.light;

  return useMemo(
    () => ({
      // Body / paragraph — text-sm + leading-6 ≈ 1.71. Generous for CJK.
      paragraph: {
        fontSize: MD_FONT.body,
        lineHeight: MD_LINE.body,
        color: t.foreground,
        marginBottom: MD_GAP.paragraph,
      },
      // Headings — Apple HIG-calibrated, one tier below shadcn web defaults.
      h1: {
        fontSize: MD_FONT.h1,
        lineHeight: MD_LINE.h1,
        fontWeight: "700" as const,
        color: t.foreground,
        marginTop: MD_GAP.headingTopLarge,
        marginBottom: MD_GAP.headingBottomLarge,
      },
      h2: {
        fontSize: MD_FONT.h2,
        lineHeight: MD_LINE.h2,
        fontWeight: "600" as const,
        color: t.foreground,
        marginTop: MD_GAP.headingTopLarge,
        marginBottom: MD_GAP.headingBottomLarge,
      },
      h3: {
        fontSize: MD_FONT.h3,
        lineHeight: MD_LINE.h3,
        fontWeight: "600" as const,
        color: t.foreground,
        marginTop: MD_GAP.headingTopSmall,
        marginBottom: MD_GAP.headingBottomSmall,
      },
      h4: {
        fontSize: MD_FONT.h4,
        lineHeight: MD_LINE.h4,
        fontWeight: "600" as const,
        color: t.foreground,
        marginTop: MD_GAP.headingTopSmall,
        marginBottom: MD_GAP.headingBottomSmall,
      },
      h5: {
        fontSize: MD_FONT.h5,
        lineHeight: MD_LINE.h5,
        fontWeight: "600" as const,
        color: t.foreground,
        marginTop: MD_GAP.headingTopSmall,
        marginBottom: MD_GAP.headingBottomSmall,
      },
      h6: {
        fontSize: MD_FONT.h6,
        lineHeight: MD_LINE.h6,
        fontWeight: "600" as const,
        color: t.mutedForeground,
        marginTop: MD_GAP.headingTopSmall,
        marginBottom: MD_GAP.headingBottomSmall,
      },
      strong: {
        // md4c restricts inline `fontWeight` to "bold" | "normal" — it adds
        // the bold trait on top of the inherited block font. We can't pin
        // a 600 weight here the way we can on headings.
        fontWeight: "bold" as const,
        color: t.foreground,
      },
      em: {
        color: t.foreground,
      },
      strikethrough: {
        color: t.mutedForeground,
      },
      underline: {
        color: t.foreground,
      },
      link: {
        color: t.brand,
        underline: true,
      },
      // Inline code — monospace font + subtle surface-2 chip + foreground
       // text. Matches GitHub mobile / Slack / Notion / Discord / Apple
       // Notes inline-code rendering.
       //
       // History:
       //   - 2026-05-19 (a):  switched to `transparent` bg + `t.brand`
       //     color as a workaround for enriched-markdown upstream issue
       //     #255 (hardcoded internal padding renders a top-heavy chip
       //     when bg is non-transparent — top:bottom padding ~4:1).
       //   - 2026-05-19 (b):  REVERTED. Refactoring UI #1 — "use color
       //     semantically, not decoratively" — `t.brand` is the link
       //     color (and the WS-/iOS-wide blue-link convention), so
       //     tinting code blue was confusing users into tapping it as
       //     if it were a link. The padding artifact is the lesser
       //     evil: it's a few pixels of asymmetry on a subtle chip,
       //     not a semantic miscue.
       //
       // Why this looks acceptable now (vs the earlier attempt):
       //   - Earlier chip used `t.muted` (L 96.1%) on a `bg-secondary`
       //     parent (L 96.1%) — same color, so the chip needed all the
       //     contrast it could get; the top-heavy padding stood out.
       //   - Current chip uses `t.surface2` (L 90%) on a `bg-surface-1`
       //     parent (L 98%) — 8% L step, chip is clearly framed but
       //     temperate, padding asymmetry is hardly noticeable.
       //
       // borderColor matches bg so the library's default outline
       // (enriched ships a pink `#F8D7DA` border that renders harshly
       // on dark) collapses into the chip fill.
       //
       // Revisit: if upstream #255 ever ships a padding control, drop
       // borderRadius / padding fields here to get pixel-perfect chip.
      code: {
        color: t.foreground,
        backgroundColor: t.surface2,
        borderColor: t.surface2,
        fontFamily: MONO_FONT,
      },
      // Block code — bigger box, surface-2 background (one tonal tier
       // above secondary so the box stays visible when the markdown
       // renders inside a bg-secondary parent like a comment bubble),
       // mono font. (When the splitter detects a fenced code block it
       // routes to the in-house `CodeBlock` component instead — this
       // style is the fallback for any code that stays inside the
       // enriched prose stream, e.g. code nested in a list item.)
       // `borderColor` REQUIRED: enriched defaults to `#374151` which
       // clashes with our background.
      codeBlock: {
        fontSize: MD_FONT.codeBlock,
        color: t.foreground,
        backgroundColor: t.surface2,
        borderColor: t.border,
        padding: 12,
        borderRadius: 8,
        marginBottom: MD_GAP.paragraph,
      },
      // Blockquote — subtle left bar in border tone. `color` is REQUIRED:
      // enriched's default is a hardcoded #4B5563 mid-gray that disappears
      // on dark backgrounds.
      blockquote: {
        color: t.mutedForeground,
        fontSize: MD_FONT.body,
        lineHeight: MD_LINE.body,
        borderColor: t.border,
        borderWidth: 2,
        backgroundColor: "transparent",
        marginBottom: MD_GAP.paragraph,
      },
      // List — bullets in muted-foreground so they don't compete with content.
      // `color` is REQUIRED: enriched's default text color does NOT track
      // dark mode, so list items render in hardcoded near-black and are
      // invisible on dark backgrounds. This was the visible bug in #MUL-2395
      // dark-mode screenshot (2026-05-19).
      list: {
        color: t.foreground,
        fontSize: MD_FONT.body,
        lineHeight: MD_LINE.body,
        bulletColor: t.mutedForeground,
        bulletSize: 4,
        markerColor: t.mutedForeground,
        gapWidth: 8,
        marginLeft: 16,
      },
      image: {
        borderRadius: 8,
        marginBottom: MD_GAP.paragraph,
      },
      // Task lists. `checkedTextColor` REQUIRED: enriched default is `#000000`,
      // making completed items invisible in dark mode.
      taskList: {
        checkedColor: t.brand,
        borderColor: t.border,
        checkmarkColor: t.brandForeground,
        checkedTextColor: t.mutedForeground,
        checkboxSize: 16,
      },
      // GFM tables. Every color field below is required — enriched defaults
      // are all hardcoded light values (#FFFFFF row even, #F9FAFB row odd,
      // #111827 header text), all invisible / clashing in dark mode.
      // headerBackgroundColor uses `surface-2` (one tier above secondary)
      // so the header stays distinct when the table renders inside a
      // bg-secondary parent like a comment bubble.
      table: {
        color: t.foreground,
        fontSize: MD_FONT.body,
        lineHeight: MD_LINE.body,
        borderColor: t.border,
        borderRadius: 6,
        headerBackgroundColor: t.surface2,
        headerTextColor: t.foreground,
        // Transparent rows let the page background show through — works in
        // both light (white page) and dark (near-black page) without a
        // jarring inner panel.
        rowEvenBackgroundColor: "transparent",
        rowOddBackgroundColor: "transparent",
        cellPaddingHorizontal: 10,
        cellPaddingVertical: 6,
      },
      thematicBreak: {
        color: t.border,
      },
      // LaTeX math (free with this engine — was V3 deferred under the walker).
      math: {
        fontSize: 16,
        color: t.foreground,
        backgroundColor: t.muted,
        padding: 12,
        marginBottom: MD_GAP.paragraph,
        textAlign: "center" as const,
      },
      inlineMath: {
        color: t.foreground,
      },
    }),
    [t],
  );
}
