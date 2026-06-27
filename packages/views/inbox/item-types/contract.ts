import type { ComponentType } from "react";
import type { InboxItem } from "@multica/core/types";

/**
 * Inbox is a *typed feed*: one list whose entries each declare a high-level
 * `kind`, and a registered renderer decides how that kind looks in the list,
 * how its detail pane behaves, and which actions it offers. The envelope
 * (actor, time, read state, work anchor) and the list/triage machinery are
 * shared across kinds; only the row + detail + actions are polymorphic.
 *
 * See MUL-3788 for the design. Today every server-issued `InboxItem` is an
 * issue notification; `conversation` / `approval` / `digest` land as those
 * item types are introduced — each just registers another renderer, without
 * touching the list, filtering, sorting, or read machinery.
 */
export type InboxItemKind =
  | "issue_notification"
  | "conversation"
  | "approval"
  | "digest";

/** Props every kind's list-row component receives. */
export interface InboxItemRowProps {
  item: InboxItem;
  isSelected: boolean;
  onSelect: () => void;
  onArchive: () => void;
}

/** Props every kind's detail-pane component receives. */
export interface InboxItemDetailProps {
  item: InboxItem;
  onArchive: () => void;
}

/**
 * A renderer is the per-kind half of the contract. `Row` is required (a kind
 * must be listable); `Detail` is optional because some kinds (e.g. an issue
 * notification that points at an issue) defer their detail pane to a shared
 * surface rather than rendering their own.
 */
export interface InboxItemRenderer {
  kind: InboxItemKind;
  Row: ComponentType<InboxItemRowProps>;
  Detail?: ComponentType<InboxItemDetailProps>;
}

const registry = new Map<InboxItemKind, InboxItemRenderer>();

/** Register a renderer for an item kind. Last registration wins. */
export function registerInboxItemType(renderer: InboxItemRenderer): void {
  registry.set(renderer.kind, renderer);
}

export function hasInboxItemRenderer(kind: InboxItemKind): boolean {
  return registry.has(kind);
}

/**
 * Resolve the renderer for a kind. Throws if none is registered — a missing
 * renderer is a wiring bug (the registering module was not imported), not a
 * runtime condition to handle.
 */
export function getInboxItemRenderer(kind: InboxItemKind): InboxItemRenderer {
  const renderer = registry.get(kind);
  if (!renderer) {
    throw new Error(`No inbox item renderer registered for kind "${kind}"`);
  }
  return renderer;
}

/**
 * Map a raw `InboxItem` to its high-level kind. This is the single place that
 * decides which renderer an item dispatches to. Every current item type is an
 * issue notification; future kinds add their discriminant here as the data
 * model grows.
 */
export function inboxItemKind(_item: InboxItem): InboxItemKind {
  return "issue_notification";
}
