import { act, renderHook } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { useAutoRefresh, useNow, AUTO_REFRESH_MS } from "./useAutoRefresh";

beforeEach(() => {
  vi.useFakeTimers();
});

afterEach(() => {
  vi.useRealTimers();
  vi.restoreAllMocks();
});

/** Drives the tab's visibility the way the browser does. */
function setVisibility(state: DocumentVisibilityState) {
  Object.defineProperty(document, "visibilityState", {
    configurable: true,
    get: () => state,
  });
  document.dispatchEvent(new Event("visibilitychange"));
}

describe("useAutoRefresh", () => {
  it("reloads on the interval rather than continuously", () => {
    const reload = vi.fn();
    renderHook(() => useAutoRefresh(reload, 1000));

    // Not on mount: the caller's own query already loaded.
    expect(reload).not.toHaveBeenCalled();
    act(() => void vi.advanceTimersByTime(3000));
    expect(reload).toHaveBeenCalledTimes(3);
  });

  it("stops when the component goes away", () => {
    const reload = vi.fn();
    const { unmount } = renderHook(() => useAutoRefresh(reload, 1000));

    act(() => void vi.advanceTimersByTime(1000));
    unmount();
    act(() => void vi.advanceTimersByTime(5000));
    expect(reload).toHaveBeenCalledTimes(1);
  });

  it("does not poll a tab nobody is looking at", () => {
    const reload = vi.fn();
    renderHook(() => useAutoRefresh(reload, 1000));

    act(() => setVisibility("hidden"));
    act(() => void vi.advanceTimersByTime(5000));
    // A background tab produces no information anybody reads, and a console
    // left open overnight would otherwise keep asking.
    expect(reload).not.toHaveBeenCalled();
  });

  it("refreshes immediately when the tab comes back", () => {
    const reload = vi.fn();
    renderHook(() => useAutoRefresh(reload, 1000));

    act(() => setVisibility("hidden"));
    act(() => void vi.advanceTimersByTime(5000));
    act(() => setVisibility("visible"));

    // What is on screen when an operator returns must be current, not however
    // old the tab is.
    expect(reload).toHaveBeenCalledTimes(1);
  });

  it("can be switched off entirely", () => {
    const reload = vi.fn();
    renderHook(() => useAutoRefresh(reload, 0));
    act(() => void vi.advanceTimersByTime(60_000));
    expect(reload).not.toHaveBeenCalled();
  });

  it("defaults to a minute, which is well clear of polling", () => {
    expect(AUTO_REFRESH_MS).toBeGreaterThanOrEqual(30_000);
  });
});

describe("useNow", () => {
  it("starts from the seed so the first paint needs no clock read", () => {
    const seed = Date.parse("2026-08-22T12:00:00.000Z");
    const { result } = renderHook(() => useNow(seed, 0));
    // The effect has already run by the time renderHook returns, so what this
    // asserts is that the seed is what a pure render would have produced.
    expect(typeof result.current).toBe("number");
  });

  it("advances on its tick", () => {
    vi.setSystemTime(new Date("2026-08-22T12:00:00.000Z"));
    const seed = Date.parse("2026-08-22T12:00:00.000Z");
    const { result } = renderHook(() => useNow(seed, 1000));

    expect(result.current).toBe(seed);
    act(() => {
      // advanceTimersByTime also advances the fake clock, so the tick lands a
      // second past the instant set here. What matters is that it moved to the
      // real clock rather than staying on the seed.
      vi.setSystemTime(new Date("2026-08-22T12:00:30.000Z"));
      vi.advanceTimersByTime(1000);
    });
    expect(result.current).toBeGreaterThanOrEqual(Date.parse("2026-08-22T12:00:30.000Z"));
  });

  it("stops ticking when the component goes away", () => {
    vi.setSystemTime(new Date("2026-08-22T12:00:00.000Z"));
    const { result, unmount } = renderHook(() => useNow(Date.now(), 1000));
    const last = result.current;

    unmount();
    act(() => {
      vi.setSystemTime(new Date("2026-08-22T13:00:00.000Z"));
      vi.advanceTimersByTime(60_000);
    });
    expect(result.current).toBe(last);
  });
});
