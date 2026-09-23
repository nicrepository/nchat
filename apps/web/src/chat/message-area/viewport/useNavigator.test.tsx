/**
 * The navigation controller's ownership rules (#880), driven directly.
 *
 * The timeline's own suites exercise these through a rendered conversation,
 * which is where the interesting geometry lives. What cannot be reached from
 * there is the part that is about *time*: a conversation left while a trip was
 * still in flight, and the observer from it that fires afterwards. Rendering
 * that race is a race; driving the controller is not.
 */

import { act, renderHook } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { useInstantPositioning } from "./useOpenPosition";
import { useNavigator } from "./useNavigator";
import { useViewportCore, type ViewportCore } from "./useViewportCore";

// Without one the tail degrades to "the geometry answers alone" (the same
// capability check the rest of the viewport carries), and these cases are
// precisely about the sentinel having a say.
beforeEach(() => {
  class SilentIntersectionObserver {
    observe() {}
    unobserve() {}
    disconnect() {}
  }
  vi.stubGlobal("IntersectionObserver", SilentIntersectionObserver);
});

afterEach(() => {
  vi.unstubAllGlobals();
});

interface Geometry {
  scrollHeight: number;
  clientHeight: number;
  scrollTop: number;
  /** The border-box width, when a test needs an overlay scrollbar (= 720). */
  clientWidth_?: number;
}

/** A scrollport with the geometry jsdom lays out for nobody. */
function scrollport(geometry: Geometry): HTMLDivElement {
  const el = document.createElement("div");
  Object.defineProperty(el, "scrollHeight", {
    configurable: true,
    get: () => geometry.scrollHeight,
  });
  Object.defineProperty(el, "clientHeight", {
    configurable: true,
    get: () => geometry.clientHeight,
  });
  // Without the scrollbar, which is what makes a pointer past it identifiable.
  Object.defineProperty(el, "clientWidth", { configurable: true, value: 720 });
  // A classic scrollbar by default: the border box is wider than the content
  // box by its width, which is also what makes an animation interruptible.
  Object.defineProperty(el, "offsetWidth", {
    configurable: true,
    get: () => geometry.clientWidth_ ?? 735,
  });
  Object.defineProperty(el, "scrollTop", {
    configurable: true,
    get: () => geometry.scrollTop,
    set: (value: number) => {
      geometry.scrollTop = Math.max(
        0,
        Math.min(value, geometry.scrollHeight - geometry.clientHeight),
      );
    },
  });
  return el;
}

function setup(geometry: Geometry, key = "channel:a") {
  const onTailArrived = vi.fn();
  const el = scrollport(geometry);
  const rendered = renderHook(
    ({ conversationKey }: { conversationKey: string }) => {
      const { core } = useViewportCore();
      core.listRef.current = el;
      const navigator = useNavigator({ core, conversationKey, onTailArrived });
      return { core, navigator };
    },
    { initialProps: { conversationKey: key } },
  );
  return { ...rendered, el, onTailArrived, current: () => rendered.result.current };
}

/** The sentinel's report, which is half of the tail's arrival condition. */
function sentinelVisible(core: ViewportCore, visible: boolean) {
  core.noteTailSentinel(visible);
}

describe("a tail navigation", () => {
  it("keeps travelling to where the end is now, not to where it was when asked", () => {
    const geometry = { scrollHeight: 10_000, clientHeight: 600, scrollTop: 0 };
    const { current, el } = setup(geometry);

    act(() => current().navigator.navigateToTail("button"));
    expect(el.scrollTop).toBe(9_400);

    // A row measured for the first time: the end moved, and the trip is still
    // responsible for getting there.
    geometry.scrollHeight = 12_000;
    act(() => current().navigator.requestPass());
    expect(el.scrollTop).toBe(11_400);
  });

  it("does not finish on a sentinel that reports arrival before the geometry does", () => {
    const geometry = { scrollHeight: 10_000, clientHeight: 600, scrollTop: 0 };
    const { current, el, onTailArrived } = setup(geometry);

    act(() => current().navigator.navigateToTail("button"));
    // The canvas has not caught up with a measured row: the sentinel is on
    // screen with real content still below the fold (#880's DEV capture).
    sentinelVisible(current().core, true);
    geometry.scrollHeight = 10_214;
    act(() => current().navigator.requestPass());

    expect(onTailArrived).not.toHaveBeenCalled();
    expect(current().core.navigationRef.current).not.toBeNull();

    // And once the two agree, it is over.
    act(() => current().navigator.requestPass());
    expect(el.scrollTop).toBe(9_614);
    act(() => current().navigator.requestPass());
    expect(onTailArrived).toHaveBeenCalledTimes(1);
    expect(current().core.navigationRef.current).toBeNull();
  });

  it("takes the scrollport from a prepend restoration rather than sharing it", () => {
    const geometry = { scrollHeight: 10_000, clientHeight: 600, scrollTop: 4_000 };
    const { current } = setup(geometry);
    act(() => current().core.armPrependRestore({ messageId: "m-1", offsetPx: 40 }));
    expect(current().core.prependRestoreRef.current).not.toBeNull();

    act(() => current().navigator.navigateToTail("button"));

    // #880 item 16: an explicit destination outranks putting an old reading
    // position back, and the handoff is a release rather than a race.
    expect(current().core.prependRestoreRef.current).toBeNull();
  });
});

describe("a navigation the reader overrides", () => {
  /** A short trip that animates, and so is still in flight when interrupted. */
  function tripInFlight(geometry: Geometry) {
    const rendered = setup(geometry);
    const el = rendered.el as HTMLDivElement & { scrollTo: (options: ScrollToOptions) => void };
    el.scrollTo = vi.fn();
    act(() => rendered.current().navigator.navigateToTail("button"));
    expect(rendered.current().core.phaseRef.current).toBe("SCROLLING_TO_BOTTOM");
    return rendered;
  }

  /** A pointer going down where only the scrollbar can be. */
  function scrollbarPointerDown(el: HTMLDivElement, offsetX = 724) {
    const event = new Event("pointerdown");
    Object.defineProperty(event, "offsetX", { value: offsetX });
    el.dispatchEvent(event);
  }

  it("hands the viewport back when the reader takes hold of the scrollbar", () => {
    const geometry = { scrollHeight: 10_000, clientHeight: 600, scrollTop: 0 };
    const { current, el } = tripInFlight(geometry);
    // The drag has taken the reader up into the history; the trip that was
    // still travelling to the end must not pull them back out of it.
    geometry.scrollTop = 2_000;

    act(() => scrollbarPointerDown(el));

    const { core } = current();
    expect(core.navigationRef.current).toBeNull();
    expect(core.phaseRef.current).toBe("READING_HISTORY");
    expect(core.isNearBottomRef.current).toBe(false);
    expect(core.followTailRef.current).toBe(false);
  });

  it("keeps the position the drag chose when the layout moves afterwards", () => {
    const geometry = { scrollHeight: 10_000, clientHeight: 600, scrollTop: 0 };
    const { current, el } = tripInFlight(geometry);
    geometry.scrollTop = 2_000;
    act(() => scrollbarPointerDown(el));

    // A row measured after the drag: the dead navigation has no say in it.
    geometry.scrollHeight = 14_000;
    act(() => current().navigator.requestPass());

    expect(el.scrollTop).toBe(2_000);
    expect(current().core.navigationRef.current).toBeNull();
  });

  it("cannot be revived by a pass belonging to the generation the drag ended", () => {
    const geometry = { scrollHeight: 10_000, clientHeight: 600, scrollTop: 0 };
    const { current, el } = tripInFlight(geometry);
    const cancelled = current().core.navigationRef.current!;
    geometry.scrollTop = 2_000;
    act(() => scrollbarPointerDown(el));

    // The callback an old observer is holding: its generation is gone, and
    // neither ending it nor passing on it may write to the scrollport.
    expect(current().core.endNavigation(cancelled)).toBe(false);
    act(() => current().navigator.requestPass());
    expect(el.scrollTop).toBe(2_000);
    expect(current().core.phaseRef.current).toBe("READING_HISTORY");
  });

  it("settles on AT_BOTTOM when the drag ends at the end of the conversation", () => {
    const geometry = { scrollHeight: 10_000, clientHeight: 600, scrollTop: 0 };
    const { current, el } = tripInFlight(geometry);
    geometry.scrollTop = 9_400;

    act(() => scrollbarPointerDown(el));

    const { core } = current();
    expect(core.navigationRef.current).toBeNull();
    expect(core.phaseRef.current).toBe("AT_BOTTOM");
    expect(core.isNearBottomRef.current).toBe(true);
    expect(core.followTailRef.current).toBe(true);
  });

  it("ignores a pointer that went down on the timeline rather than on the scrollbar", () => {
    const geometry = { scrollHeight: 10_000, clientHeight: 600, scrollTop: 0 };
    const { current, el } = tripInFlight(geometry);

    // Clicking a message, selecting text, opening a reaction: all inside the
    // content box, and none of them a reason to abandon the trip.
    act(() => scrollbarPointerDown(el, 300));
    // And an event that came from a row rather than from the scrollport, even
    // at an offset its own box makes look like the edge.
    const row = document.createElement("div");
    el.appendChild(row);
    act(() => {
      const event = new Event("pointerdown", { bubbles: true });
      Object.defineProperty(event, "offsetX", { value: 900 });
      row.dispatchEvent(event);
    });

    expect(current().core.navigationRef.current).not.toBeNull();
    expect(current().core.phaseRef.current).toBe("SCROLLING_TO_BOTTOM");
  });

  for (const input of ["wheel", "touchstart", "keydown"]) {
    it(`leaves SCROLLING_TO_BOTTOM for the reader's real position on ${input}`, () => {
      const geometry = { scrollHeight: 2_000, clientHeight: 600, scrollTop: 400 };
      const { current, el } = tripInFlight(geometry);

      act(() => {
        el.dispatchEvent(new Event(input));
      });

      // One transition, complete: the trip is over, and the phase and the
      // follow-the-tail refs describe where the scrollport actually is.
      const { core } = current();
      expect(core.navigationRef.current).toBeNull();
      expect(core.phaseRef.current).toBe("READING_HISTORY");
      expect(core.isNearBottomRef.current).toBe(false);
      expect(core.followTailRef.current).toBe(false);
    });
  }

  it("settles on AT_BOTTOM when the reader takes over at the end", () => {
    const geometry = { scrollHeight: 2_000, clientHeight: 600, scrollTop: 400 };
    const { current, el } = tripInFlight(geometry);
    // The animation got there; the sentinel has not said so yet.
    geometry.scrollTop = 1_400;

    act(() => {
      el.dispatchEvent(new Event("wheel"));
    });

    const { core } = current();
    expect(core.navigationRef.current).toBeNull();
    expect(core.phaseRef.current).toBe("AT_BOTTOM");
    expect(core.isNearBottomRef.current).toBe(true);
    expect(core.followTailRef.current).toBe(true);
  });

  it("gives the scrollport back the moment they scroll for themselves", () => {
    const geometry = { scrollHeight: 10_000, clientHeight: 600, scrollTop: 0 };
    const { current, el } = setup(geometry);
    act(() => current().navigator.navigateToTail("button"));
    const landedOn = el.scrollTop;

    // A wheel, a touch or a key is an intent of their own, and it outranks a
    // trip they asked for a moment ago — correcting against it would be the
    // viewport fighting the person using it.
    act(() => {
      el.dispatchEvent(new Event("wheel"));
    });
    expect(current().core.navigationRef.current).toBeNull();

    geometry.scrollHeight = 20_000;
    act(() => current().navigator.requestPass());
    expect(el.scrollTop).toBe(landedOn);
  });

  it("leaves a trip that cannot finish in the phase the geometry describes", () => {
    // Nowhere to go — the scrollport refuses every write — so the operation
    // ends rather than holding the viewport in a state nothing owns, and the
    // control stays on screen because the reader really is up in the history.
    const geometry = { scrollHeight: 10_000, clientHeight: 600, scrollTop: 0 };
    const { current } = setup(geometry);
    // A scrollport that will not take a write, the way a physical limit does:
    // every pass produces no movement, so no pass has a successor.
    Object.defineProperty(current().core.listRef.current!, "scrollTop", {
      configurable: true,
      get: () => 0,
      set: () => {},
    });

    act(() => current().navigator.navigateToTail("button"));
    act(() => current().navigator.requestPass());

    expect(current().core.navigationRef.current).toBeNull();
    expect(current().core.phaseRef.current).toBe("READING_HISTORY");
  });
});

describe("a trip the browser cannot be interrupted during", () => {
  /** An overlay scrollbar: browser chrome, so its width is nobody's. */
  const overlay = { scrollHeight: 2_000, clientHeight: 600, scrollTop: 0, clientWidth_: 720 };

  it("never animates where a drag would reach the page as nothing at all", () => {
    const geometry = { ...overlay };
    const { current, el } = setup(geometry);
    const animated = vi.fn();
    (el as HTMLDivElement & { scrollTo: unknown }).scrollTo = animated;

    // Short enough for #675 to allow an animation, and refused all the same.
    act(() => current().navigator.navigateToTail("button"));

    expect(animated).not.toHaveBeenCalled();
    expect(el.scrollTop).toBe(1_400);
  });

  it("keeps animating where the reader can still take the scrollbar", () => {
    const geometry = { scrollHeight: 2_000, clientHeight: 600, scrollTop: 0 };
    const { current, el } = setup(geometry);
    const animated = vi.fn();
    (el as HTMLDivElement & { scrollTo: unknown }).scrollTo = animated;

    act(() => current().navigator.navigateToTail("button"));

    expect(animated).toHaveBeenCalledWith({ top: 1_400, behavior: "smooth" });
  });
});

describe("opening at the tail", () => {
  it("is handed to the navigator, never scrolled once and forgotten", () => {
    const navigateToTail = vi.fn();
    const navigateToFirstUnread = vi.fn();
    const scrollIntoView = vi.fn();
    const rendered = renderHook(() => {
      const { core } = useViewportCore();
      useInstantPositioning(
        core,
        { messageId: null },
        "TAIL",
        navigateToTail,
        navigateToFirstUnread,
      );
      return core;
    });
    rendered.result.current.bottomRef.current = Object.assign(document.createElement("div"), {
      scrollIntoView,
    });

    // #880: the one-shot scroll this replaced is exactly what left an opening
    // stranded when the layout moved after it.
    expect(navigateToTail).toHaveBeenCalledWith("open");
    expect(navigateToFirstUnread).not.toHaveBeenCalled();
    expect(scrollIntoView).not.toHaveBeenCalled();
  });

  it("keeps travelling through reflows without a smooth scroll or a flash of the control", () => {
    const geometry = { scrollHeight: 10_000, clientHeight: 600, scrollTop: 0 };
    const { current, el, onTailArrived } = setup(geometry);
    const animated = vi.fn();
    (el as HTMLDivElement & { scrollTo: unknown }).scrollTo = animated;

    act(() => current().navigator.navigateToTail("open"));
    // Still establishing the position: not a trip the control would announce,
    // and not AT_BOTTOM before anything has confirmed it.
    expect(current().core.phaseRef.current).toBe("RESTORING_POSITION");
    expect(el.scrollTop).toBe(9_400);

    // Rows at the end replace their estimates, three times over.
    for (const height of [10_400, 11_100, 11_500]) {
      geometry.scrollHeight = height;
      act(() => current().navigator.requestPass());
      expect(el.scrollTop).toBe(height - 600);
      expect(current().core.navigationRef.current).not.toBeNull();
    }

    sentinelVisible(current().core, true);
    act(() => current().navigator.requestPass());

    expect(onTailArrived).toHaveBeenCalledTimes(1);
    expect(current().core.navigationRef.current).toBeNull();
    // Instant from the first write to the last, even for a short opening.
    expect(animated).not.toHaveBeenCalled();
  });
});

describe("a navigation the reader has left behind", () => {
  it("moves nothing once the conversation has changed", () => {
    const geometry = { scrollHeight: 10_000, clientHeight: 600, scrollTop: 0 };
    const { current, el, rerender } = setup(geometry);
    act(() => current().navigator.navigateToTail("button"));
    const landedOn = el.scrollTop;

    rerender({ conversationKey: "channel:b" });
    // The timeline is now showing somebody else's messages, and the pass an
    // observer from the previous one delivers must not write into it.
    geometry.scrollHeight = 20_000;
    act(() => current().navigator.requestPass());

    expect(el.scrollTop).toBe(landedOn);
    expect(current().core.navigationRef.current).toBeNull();
  });

  it("cannot be ended by a callback holding the previous generation", () => {
    const geometry = { scrollHeight: 10_000, clientHeight: 600, scrollTop: 0 };
    const { current } = setup(geometry);

    act(() => current().navigator.navigateToTail("button"));
    const first = current().core.navigationRef.current!;
    // A second request — an own send, say — replaces the first outright. The
    // end has moved meanwhile, so it has somewhere to go and stays armed.
    geometry.scrollHeight = 12_000;
    act(() => current().navigator.navigateToTail("own-send"));
    const second = current().core.navigationRef.current!;

    expect(second).not.toBe(first);
    expect(second.reason).toBe("own-send");

    expect(second.generation).toBeGreaterThan(first.generation);
    // The stale one can neither end nor claim the live operation.
    expect(current().core.endNavigation(first)).toBe(false);
    expect(current().core.navigationRef.current).toBe(second);
    expect(current().core.endNavigation(second)).toBe(true);
  });
});
