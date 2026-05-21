/**
 * Per-comment text-selection mode. When the user taps "Select Text" in the
 * comment's UIContextMenu (built by `components/issue/comment-context-menu.
 * tsx`), the targeted comment id is parked here. `CommentBody` reads this
 * store and, when matched, (a) drops its `<CommentContextMenu>` wrapper so
 * UIKit no longer owns long-press, and (b) sets `selectable={true}` on the
 * Markdown body — the next long-press inside the bubble fires UIKit's
 * native selection magnifier + handles + Copy/Look Up callout.
 *
 * Why a separate Zustand store (not props / context):
 *   - Only one comment can be in selection mode at a time across the app —
 *     selecting comment B implicitly clears comment A by id replacement.
 *   - The flip happens from inside the context-menu callback, which lives
 *     in a different component tree than the comment list — easier to wire
 *     via a global store than to thread callbacks through every parent.
 *
 * Why this exists at all (iOS / Android constraint):
 *   - Long-press on a single native view can be routed to exactly one
 *     gesture recognizer — either UIKit's UIContextMenuInteraction (the
 *     menu) OR UITextInteraction (text selection). The two cannot fire in
 *     parallel. Mirrors the iOS 26 iMessage pattern: the context menu has
 *     a "Select" entry that transitions the bubble into selection mode
 *     rather than trying to run both gestures at once.
 *
 * Lifecycle: cleared when the issue-detail screen unmounts so each fresh
 * navigation into an issue starts with no comment in selection mode.
 */
import { create } from "zustand";

interface State {
  selectingId: string | null;
  setSelecting: (commentId: string) => void;
  clear: () => void;
}

export const useCommentSelectStore = create<State>((set) => ({
  selectingId: null,
  setSelecting: (commentId) => set({ selectingId: commentId }),
  clear: () => set({ selectingId: null }),
}));
