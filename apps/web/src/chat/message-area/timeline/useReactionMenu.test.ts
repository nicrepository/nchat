/**
 * One menu, two ways in (issue #1152): hover/focus and a long press share the
 * single-message state, and a long-pressed menu is marked as such.
 */

import { act, renderHook } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { useReactionMenu } from "./useReactionMenu";

beforeEach(() => {
  vi.useFakeTimers();
});

afterEach(() => {
  vi.useRealTimers();
});

describe("useReactionMenu long press (issue #1152)", () => {
  it("opens the long-pressed message's menu, marked touch-opened", () => {
    const { result } = renderHook(() => useReactionMenu());
    act(() => result.current.onReactionMenuLongPress("a"));
    expect(result.current.hoveredMessageId).toBe("a");
    expect(result.current.touchMessageId).toBe("a");
    expect(result.current.touchSurfaceOpen).toBe(true);
  });

  it("reports no touch surface for a hover menu, and one for any open picker", () => {
    const { result } = renderHook(() => useReactionMenu());
    act(() => result.current.onReactionMenuVisibleChange("a", true));
    expect(result.current.touchSurfaceOpen).toBe(false);
    act(() => result.current.onPickerOpenChange("a", true));
    expect(result.current.touchSurfaceOpen).toBe(true);
  });

  it("replaces another message's menu and closes its picker: one surface at a time", () => {
    const { result } = renderHook(() => useReactionMenu());
    act(() => result.current.onPickerOpenChange("a", true));
    act(() => result.current.onReactionMenuLongPress("b"));
    expect(result.current.openPickerMessageId).toBeNull();
    expect(result.current.hoveredMessageId).toBe("b");
    expect(result.current.touchMessageId).toBe("b");
  });

  it("keeps the same message's picker open on a repeated long press", () => {
    const { result } = renderHook(() => useReactionMenu());
    act(() => result.current.onReactionMenuLongPress("a"));
    act(() => result.current.onPickerOpenChange("a", true));
    act(() => result.current.onReactionMenuLongPress("a"));
    expect(result.current.openPickerMessageId).toBe("a");
  });

  it("stays touch-opened when focus moving into it re-shows the same message", () => {
    const { result } = renderHook(() => useReactionMenu());
    act(() => result.current.onReactionMenuLongPress("a"));
    act(() => result.current.onReactionMenuVisibleChange("a", true));
    expect(result.current.touchMessageId).toBe("a");
  });

  it("becomes a plain hover menu when another message is revealed", () => {
    const { result } = renderHook(() => useReactionMenu());
    act(() => result.current.onReactionMenuLongPress("a"));
    act(() => result.current.onReactionMenuVisibleChange("b", true));
    expect(result.current.hoveredMessageId).toBe("b");
    expect(result.current.touchMessageId).toBeNull();
  });

  it("clears both after the leave delay, and a long press cancels a pending close", () => {
    const { result } = renderHook(() => useReactionMenu());
    act(() => result.current.onReactionMenuLongPress("a"));
    act(() => result.current.onReactionMenuVisibleChange("a", false));
    act(() => result.current.onReactionMenuLongPress("a"));
    act(() => vi.advanceTimersByTime(500));
    expect(result.current.hoveredMessageId).toBe("a");

    act(() => result.current.onReactionMenuVisibleChange("a", false));
    act(() => vi.advanceTimersByTime(500));
    expect(result.current.hoveredMessageId).toBeNull();
    expect(result.current.touchMessageId).toBeNull();
  });

  it("never closes another message's picker when a late close names this one", () => {
    const { result } = renderHook(() => useReactionMenu());
    act(() => result.current.onPickerOpenChange("b", true));
    act(() => result.current.onPickerOpenChange("a", false));
    expect(result.current.openPickerMessageId).toBe("b");
    act(() => result.current.onPickerOpenChange("b", false));
    expect(result.current.openPickerMessageId).toBeNull();
  });

  it("releases everything a leaving message owned, at once, and frees swipes", () => {
    const { result } = renderHook(() => useReactionMenu());
    act(() => result.current.onReactionMenuLongPress("a"));
    act(() => result.current.onPickerOpenChange("a", true));
    act(() => result.current.onReactionMenuRelease("a"));
    expect(result.current.hoveredMessageId).toBeNull();
    expect(result.current.touchMessageId).toBeNull();
    expect(result.current.openPickerMessageId).toBeNull();
    expect(result.current.touchSurfaceOpen).toBe(false);
  });

  it("ignores a release for a message that no longer owns anything", () => {
    const { result } = renderHook(() => useReactionMenu());
    act(() => result.current.onReactionMenuLongPress("b"));
    act(() => result.current.onPickerOpenChange("b", true));
    act(() => result.current.onReactionMenuRelease("a"));
    expect(result.current.touchMessageId).toBe("b");
    expect(result.current.openPickerMessageId).toBe("b");
  });

  it("leaves another message's touch menu alone when an old close fires", () => {
    const { result } = renderHook(() => useReactionMenu());
    act(() => result.current.onReactionMenuVisibleChange("a", true));
    act(() => result.current.onReactionMenuVisibleChange("a", false));
    act(() => result.current.onReactionMenuLongPress("b"));
    act(() => vi.advanceTimersByTime(500));
    expect(result.current.touchMessageId).toBe("b");
  });
});
