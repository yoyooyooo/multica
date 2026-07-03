import Mention from "@tiptap/extension-mention";
import { mergeAttributes } from "@tiptap/core";
import { ReactNodeViewRenderer } from "@tiptap/react";
import { MentionView } from "./mention-view";

const MENTION_LINK_MARKER = "](mention://";

function isEscaped(text: string, index: number): boolean {
  let slashCount = 0;
  for (let i = index - 1; i >= 0 && text[i] === "\\"; i--) {
    slashCount++;
  }
  return slashCount % 2 === 1;
}

function findUnescapedMentionMarker(src: string, from = 0): number {
  let marker = src.indexOf(MENTION_LINK_MARKER, from);

  while (marker !== -1) {
    if (!isEscaped(src, marker)) return marker;
    marker = src.indexOf(MENTION_LINK_MARKER, marker + MENTION_LINK_MARKER.length);
  }

  return -1;
}

function findMentionStart(src: string): number {
  let marker = findUnescapedMentionMarker(src);

  while (marker !== -1) {
    for (let i = marker - 1; i >= 0; i--) {
      const char = src[i];
      if (char === "\n" || char === "\r") break;
      if (char === "[" && !isEscaped(src, i)) return i;
    }

    marker = findUnescapedMentionMarker(src, marker + MENTION_LINK_MARKER.length);
  }

  return -1;
}

export const BaseMentionExtension = Mention.extend({
  addNodeView() {
    return ReactNodeViewRenderer(MentionView);
  },
  renderHTML({ node, HTMLAttributes }) {
    const type = node.attrs.type ?? "member";
    const prefix = type === "issue" || type === "project" ? "" : "@";
    return [
      "span",
      mergeAttributes(
        { "data-type": "mention" },
        this.options.HTMLAttributes,
        HTMLAttributes,
        {
          "data-mention-type": node.attrs.type ?? "member",
          "data-mention-id": node.attrs.id,
        },
      ),
      `${prefix}${node.attrs.label ?? node.attrs.id}`,
    ];
  },
  addAttributes() {
    return {
      ...this.parent?.(),
      type: {
        default: "member",
        parseHTML: (el: HTMLElement) =>
          el.getAttribute("data-mention-type") ?? "member",
        renderHTML: () => ({}),
      },
    };
  },
  markdownTokenizer: {
    name: "mention",
    level: "inline" as const,
    start(src: string) {
      // Anchor on Multica's mention href first. Scanning forward from every
      // "[" backtracks badly on escaped stacktrace markers like \~\[...\].
      return findMentionStart(src);
    },
    tokenize(src: string) {
      // Label accepts escaped chars (\\[ \\]) or any non-] non-backslash
      // character. Excluding backslash from the char class keeps the two
      // alternatives disjoint — otherwise "\x" runs backtrack in 2^n ways
      // (ReDoS, GitHub #4881) — while still supporting bracket-containing
      // names like "David\[TF\]".
      const match = src.match(
        /^\[@?((?:\\.|[^\]\\])+)\]\(mention:\/\/(\w+)\/([^)]+)\)/,
      );
      if (!match) return undefined;
      // Unescape backslash-escaped brackets that renderMarkdown may produce.
      const rawLabel = match[1]?.replace(/\\\[/g, "[").replace(/\\\]/g, "]");
      return {
        type: "mention",
        raw: match[0],
        attributes: { label: rawLabel, type: match[2] ?? "member", id: match[3] },
      };
    },
  },
  parseMarkdown: (token: any, helpers: any) => {
    return helpers.createNode("mention", token.attributes);
  },
  renderMarkdown: (node: any) => {
    const { id, label, type = "member" } = node.attrs || {};
    const prefix = type === "issue" || type === "project" ? "" : "@";
    // Escape square brackets in the label so the markdown link syntax
    // is not broken when the name contains [ or ] (e.g. "David[TF]").
    const safeLabel = (label ?? id).replace(/\[/g, "\\[").replace(/\]/g, "\\]");
    return `[${prefix}${safeLabel}](mention://${type}/${id})`;
  },
});
