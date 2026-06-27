import { describe, expect, it } from "vitest";
import type { InboxItem } from "@multica/core/types";
import {
  getInboxItemRenderer,
  hasInboxItemRenderer,
  inboxItemKind,
} from "./index";

function item(overrides: Partial<InboxItem> = {}): InboxItem {
  return {
    id: "inbox-1",
    workspace_id: "workspace-1",
    recipient_type: "member",
    recipient_id: "member-1",
    actor_type: "agent",
    actor_id: "agent-1",
    type: "new_comment",
    severity: "info",
    issue_id: "issue-1",
    title: "Issue title",
    body: null,
    issue_status: null,
    read: false,
    archived: false,
    created_at: "2026-04-29T12:00:00Z",
    details: null,
    ...overrides,
  };
}

describe("inbox item-type registry", () => {
  it("maps every current server item to the issue_notification kind", () => {
    expect(inboxItemKind(item({ type: "new_comment" }))).toBe(
      "issue_notification",
    );
    expect(inboxItemKind(item({ type: "quick_create_failed" }))).toBe(
      "issue_notification",
    );
  });

  it("registers a renderer for issue_notification with a row and detail", () => {
    expect(hasInboxItemRenderer("issue_notification")).toBe(true);
    const renderer = getInboxItemRenderer("issue_notification");
    expect(renderer.kind).toBe("issue_notification");
    expect(typeof renderer.Row).toBe("function");
    expect(typeof renderer.Detail).toBe("function");
  });

  it("throws for a kind with no registered renderer", () => {
    expect(() => getInboxItemRenderer("conversation")).toThrow(
      /No inbox item renderer registered/,
    );
  });
});
