"use client";

import { useCallback, useEffect, useState } from "react";

/**
 * Defers mounting a heavy child until just after the first paint of the
 * surrounding tree. Returns `ready: false` on the initial commit (and on the
 * first commit after `resetKey` changes) and flips to `true` on the next
 * animation frame.
 *
 * The inbox detail pane remounts the whole `IssueDetail` subtree on every issue
 * switch (it is keyed by `issue_id`). Creating the description's Tiptap editor
 * inside that synchronous commit is one of the costliest pieces of work on the
 * critical switch path — a fresh ProseMirror `EditorView` with ~20 extensions.
 * Gating it on this hook keeps the editor out of the switch commit, so the new
 * issue's content paints first and the editor hydrates a frame later. UX is
 * unchanged; only the timing moves.
 *
 * `resetKey` re-arms the deferral when it changes. The reset is derived during
 * render (the tracked key lives in state and is reconciled in the render body),
 * NOT in an effect — so the very first render after a key change already
 * reports `ready: false`. That matters on routes where the host component is
 * NOT remounted on key change (e.g. the full-page issue route): an
 * effect-based reset would let the heavy child mount synchronously on that
 * first render and then immediately unmount, which is the opposite of the goal.
 *
 * `mountNow` forces an immediate mount, for when the user interacts with the
 * placeholder before the deferred frame lands.
 */
export function useDeferredMount(resetKey?: unknown): {
  ready: boolean;
  mountNow: () => void;
} {
  const [ready, setReady] = useState(false);
  const [trackedKey, setTrackedKey] = useState(resetKey);

  // Reconcile during render (React's "adjust state when a prop changes"
  // pattern): on a key change, reset the tracked state for subsequent renders.
  const keyChanged = trackedKey !== resetKey;
  if (keyChanged) {
    setTrackedKey(resetKey);
    setReady(false);
  }

  useEffect(() => {
    const raf = requestAnimationFrame(() => setReady(true));
    return () => cancelAnimationFrame(raf);
  }, [resetKey]);

  const mountNow = useCallback(() => setReady(true), []);

  // On the render where the key just changed, `ready` still holds the previous
  // key's value. Derive the returned value from the comparison so even that
  // pass reports false — the heavy child is never mountable for a stale key.
  return { ready: keyChanged ? false : ready, mountNow };
}
