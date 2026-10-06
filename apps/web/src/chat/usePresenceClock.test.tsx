import { act, render, renderHook, screen } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import PresenceDot from "./PresenceDot";
import { PRESENCE_CLOCK_MS, presenceNeedsClock, usePresenceClock } from "./usePresenceClock";

describe("usePresenceClock", () => {
  beforeEach(() => {
    vi.useFakeTimers();
    vi.setSystemTime(new Date(2026, 9, 1, 12, 0, 0));
  });
  afterEach(() => vi.useRealTimers());

  it("shares one timer between every surface and stops it with the last", () => {
    const setInterval = vi.spyOn(window, "setInterval");
    const clearInterval = vi.spyOn(window, "clearInterval");
    const first = renderHook(() => usePresenceClock(true));
    const second = renderHook(() => usePresenceClock(true));
    expect(setInterval).toHaveBeenCalledTimes(1);

    const start = first.result.current;
    act(() => vi.advanceTimersByTime(PRESENCE_CLOCK_MS));
    expect(first.result.current).toBe(start + PRESENCE_CLOCK_MS);
    expect(second.result.current).toBe(first.result.current);

    first.unmount();
    expect(clearInterval).not.toHaveBeenCalled();
    second.unmount();
    expect(clearInterval).toHaveBeenCalledTimes(1);
  });

  it("starts no timer while nothing on screen depends on elapsed time", () => {
    const setInterval = vi.spyOn(window, "setInterval");
    renderHook(() => usePresenceClock(false));
    expect(setInterval).not.toHaveBeenCalled();
  });

  it("only the states that say how long ago need a clock", () => {
    expect(["away", "brb", "offline"].every(presenceNeedsClock)).toBe(true);
    expect(["online", "busy", "dnd", "unknown"].some(presenceNeedsClock)).toBe(false);
  });
});

describe("PresenceDot (issue #798)", () => {
  it("draws every state with its own class and a word", () => {
    for (const [state, label] of [
      ["busy", "Ocupado"],
      ["dnd", "Não perturbe"],
      ["brb", "Volto já"],
    ] as const) {
      const view = render(<PresenceDot state={state} />);
      const dot = screen.getByTestId("presence-dot");
      expect(dot).toHaveAttribute("data-presence", state);
      expect(dot.className).toContain(`presence-dot--${state}`);
      expect(dot).toHaveAttribute("title", label);
      view.unmount();
    }
  });

  it("takes a richer hover text and an inline placement", () => {
    render(<PresenceDot state="busy" inline title="Ocupado · Em chamada" />);
    const dot = screen.getByTestId("presence-dot");
    expect(dot).toHaveAttribute("title", "Ocupado · Em chamada");
    expect(dot.className).toContain("presence-dot--inline");
    expect(dot).toHaveAttribute("aria-hidden", "true");
  });
});
