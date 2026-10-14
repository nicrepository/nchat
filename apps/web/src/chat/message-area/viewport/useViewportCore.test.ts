/**
 * scrollToMessage and the prepend restoration (#1088).
 *
 * A deep link to an older message is reached by paging back, so the page that
 * brings the target in also arms a restoration of the position the reader had.
 * The jump that follows in the same commit has to win: the restoration is the
 * weaker intent, exactly as it is against a #880 navigation.
 */

import { renderHook } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import type { Virtualizer } from "@tanstack/react-virtual";

import { useViewportCore } from "./useViewportCore";

function setup() {
  const { result } = renderHook(() => useViewportCore());
  const core = result.current.core;
  const scrollToIndex = vi.fn();
  const virtualizer = { scrollToIndex } as unknown as Virtualizer<HTMLDivElement, Element>;
  core.setRowModel(new Map([["target", 7]]), virtualizer);
  core.armPrependRestore({ messageId: "reading", offsetPx: 12 });
  return { core, scrollToIndex };
}

describe("scrollToMessage", () => {
  it("abandons an armed prepend restoration when it travels to an unmounted row", () => {
    const { core, scrollToIndex } = setup();

    expect(core.scrollToMessage("target")).toBe(true);

    expect(scrollToIndex).toHaveBeenCalledWith(7, { align: "center" });
    expect(core.prependRestoreRef.current).toBeNull();
  });

  it("abandons it when it travels to a mounted row too", () => {
    const { core, scrollToIndex } = setup();
    const row = document.createElement("div");
    row.scrollIntoView = vi.fn();
    core.setMessageRef("target", row);

    expect(core.scrollToMessage("target")).toBe(true);

    expect(row.scrollIntoView).toHaveBeenCalledWith({ behavior: "smooth", block: "center" });
    expect(scrollToIndex).not.toHaveBeenCalled();
    expect(core.prependRestoreRef.current).toBeNull();
  });

  it("leaves the restoration alone when the message is not loaded, since nothing moved", () => {
    const { core, scrollToIndex } = setup();

    expect(core.scrollToMessage("elsewhere")).toBe(false);

    expect(scrollToIndex).not.toHaveBeenCalled();
    expect(core.prependRestoreRef.current).toMatchObject({ messageId: "reading" });
  });
});
