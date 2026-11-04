import { describe, expect, it, vi } from "vitest";

import { followFocusReturn, type FocusReturn } from "./useListBoundaries";

/** A return armed on `?message=<askedFrom>` (request `askedIn`) toward `messageId`. */
function armed(messageId: string, askedFrom: string, askedIn: string) {
  const release = vi.fn();
  const ref: { current: FocusReturn | null } = {
    current: { messageId, askedFrom, askedIn, trip: null, owner: null, release },
  };
  return { ref, release };
}

describe("followFocusReturn (#1099)", () => {
  it("keeps the return through the arming commit and the trip, and drops it on the way back", () => {
    const { ref, release } = armed("original", "", "");

    expect(followFocusReturn(ref, "", "")).toBeNull();
    expect(ref.current).not.toBeNull();
    expect(followFocusReturn(ref, "original", "k2")).toBe(ref.current);
    expect(followFocusReturn(ref, "original", "k2")).toBe(ref.current);

    expect(followFocusReturn(ref, "", "")).toBeNull();
    expect(ref.current).toBeNull();
    expect(release).toHaveBeenCalledTimes(1);
  });

  it("tells a repeated request for the very message on screen from going back to it", () => {
    const { ref, release } = armed("original", "original", "k1");

    expect(followFocusReturn(ref, "original", "k1")).toBeNull();
    expect(followFocusReturn(ref, "original", "k2")).not.toBeNull();

    expect(followFocusReturn(ref, "original", "k1")).toBeNull();
    expect(release).toHaveBeenCalledTimes(1);
  });

  it("drops a return whose route moved anywhere but its target", () => {
    const { ref, release } = armed("original", "", "");

    expect(followFocusReturn(ref, "near", "k2")).toBeNull();
    expect(ref.current).toBeNull();
    expect(release).toHaveBeenCalledTimes(1);
  });
});
