import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import { act, render, renderHook } from "@testing-library/react";
import { useDeferredMount } from "./use-deferred-mount";

// Drive requestAnimationFrame deterministically: queue callbacks and flush
// them on demand so the test controls exactly when the deferred mount lands.
let rafCallbacks: FrameRequestCallback[] = [];

beforeEach(() => {
  rafCallbacks = [];
  vi.stubGlobal("requestAnimationFrame", (cb: FrameRequestCallback) => {
    rafCallbacks.push(cb);
    return rafCallbacks.length;
  });
  vi.stubGlobal("cancelAnimationFrame", (id: number) => {
    rafCallbacks[id - 1] = () => {};
  });
});

afterEach(() => {
  vi.unstubAllGlobals();
});

function flushRaf() {
  const pending = rafCallbacks;
  rafCallbacks = [];
  act(() => {
    pending.forEach((cb) => cb(0));
  });
}

describe("useDeferredMount", () => {
  it("starts not-ready and flips ready after the next animation frame", () => {
    const { result } = renderHook(() => useDeferredMount());

    expect(result.current.ready).toBe(false);

    flushRaf();

    expect(result.current.ready).toBe(true);
  });

  it("mountNow forces an immediate ready without waiting for the frame", () => {
    const { result } = renderHook(() => useDeferredMount());

    expect(result.current.ready).toBe(false);

    act(() => {
      result.current.mountNow();
    });

    expect(result.current.ready).toBe(true);
  });

  it("re-arms the deferral when resetKey changes", () => {
    const { result, rerender } = renderHook(
      ({ key }: { key: string }) => useDeferredMount(key),
      { initialProps: { key: "a" } },
    );

    flushRaf();
    expect(result.current.ready).toBe(true);

    rerender({ key: "b" });
    expect(result.current.ready).toBe(false);

    flushRaf();
    expect(result.current.ready).toBe(true);
  });

  it("never reports ready for the first render after a key change (no sync mount-then-unmount)", () => {
    // Captures the `ready` value on EVERY render, including the synchronous
    // render(s) React runs while a key change is reconciled — the window
    // renderHook's settled `result.current` hides. An effect-based reset would
    // leave this first post-change render ready=true (heavy child mounts, then
    // unmounts a tick later); the render-phase reset must keep it false.
    const readyLog: boolean[] = [];
    function Probe({ k }: { k: string }) {
      const { ready } = useDeferredMount(k);
      readyLog.push(ready);
      return <span>{ready ? "ready" : "pending"}</span>;
    }

    const { rerender } = render(<Probe k="a" />);
    flushRaf();
    // Now settled ready=true for key "a".
    readyLog.length = 0;

    act(() => {
      rerender(<Probe k="b" />);
    });

    // Every render between the key change and the next frame must be pending.
    expect(readyLog.length).toBeGreaterThan(0);
    expect(readyLog.some((r) => r === true)).toBe(false);

    flushRaf();
    expect(readyLog.at(-1)).toBe(true);
  });
});
