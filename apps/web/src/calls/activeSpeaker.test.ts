import { describe, expect, it } from "vitest";

import { nextStableSpeaker } from "./activeSpeaker";

describe("active speaker stabilization", () => {
  it("waits before exposing a new candidate", () => {
    expect(nextStableSpeaker(null, "a", 1000, 400)).toEqual({
      candidate: "a",
      since: 1000,
      visible: null,
    });
  });

  it("exposes a candidate after the stabilization delay", () => {
    const previous = { candidate: "a", since: 1000, visible: null };

    expect(nextStableSpeaker(previous, "a", 1400, 400).visible).toBe("a");
  });

  it("keeps the visible speaker while a replacement stabilizes", () => {
    const previous = { candidate: "a", since: 1000, visible: "a" };

    expect(nextStableSpeaker(previous, "b", 1500, 400)).toEqual({
      candidate: "b",
      since: 1500,
      visible: "a",
    });
  });

  it("stabilizes silence before clearing the visible speaker", () => {
    const pending = nextStableSpeaker(
      { candidate: "a", since: 1000, visible: "a" },
      null,
      1500,
      400,
    );

    expect(pending).toEqual({ candidate: null, since: 1500, visible: "a" });
    expect(nextStableSpeaker(pending, null, 1900, 400).visible).toBeNull();
  });

  it("keeps the same state before the delay and after the visible candidate already matches", () => {
    const pending = { candidate: "a", since: 1000, visible: null };
    expect(nextStableSpeaker(pending, "a", 1200, 400)).toBe(pending);
    const visible = { candidate: "a", since: 1000, visible: "a" };
    expect(nextStableSpeaker(visible, "a", 1500, 400)).toBe(visible);
  });
});
