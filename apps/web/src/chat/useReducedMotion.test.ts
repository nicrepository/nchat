import { act, renderHook } from "@testing-library/react";
import { afterEach, describe, expect, it } from "vitest";

import { usePrefersReducedMotion } from "./useReducedMotion";

/** A minimal MediaQueryList stand-in: jsdom does not implement matchMedia. */
function stubMatchMedia(initialMatches: boolean) {
  let matches = initialMatches;
  let listener: (() => void) | null = null;
  const mql = {
    get matches() {
      return matches;
    },
    media: "(prefers-reduced-motion: reduce)",
    addEventListener: (_event: string, cb: () => void) => {
      listener = cb;
    },
    removeEventListener: () => {
      listener = null;
    },
  };
  window.matchMedia = () => mql as unknown as MediaQueryList;
  return {
    change(next: boolean) {
      matches = next;
      listener?.();
    },
  };
}

afterEach(() => {
  // @ts-expect-error -- jsdom does not define this by default; restore that.
  delete window.matchMedia;
});

describe("usePrefersReducedMotion", () => {
  it("reflects the initial media query state", () => {
    stubMatchMedia(true);
    const { result } = renderHook(() => usePrefersReducedMotion());
    expect(result.current).toBe(true);
  });

  it("reacts to a live change event", () => {
    const media = stubMatchMedia(false);
    const { result } = renderHook(() => usePrefersReducedMotion());
    expect(result.current).toBe(false);

    act(() => media.change(true));

    expect(result.current).toBe(true);
  });

  it("defaults to false when matchMedia is unavailable", () => {
    // @ts-expect-error -- simulating an environment without matchMedia.
    delete window.matchMedia;
    const { result } = renderHook(() => usePrefersReducedMotion());
    expect(result.current).toBe(false);
  });
});
