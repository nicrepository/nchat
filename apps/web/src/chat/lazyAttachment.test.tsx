/**
 * Lazy attachment hydration tests (issue #675).
 *
 * Two things are under test and they fail in different ways:
 *
 * - **how the observers are configured.** The timeline scrolls in its own box,
 *   so an observer left on the default root measures against the window and the
 *   prefetch margin describes a distance nobody scrolls through. A test that
 *   only invoked the callback by hand would pass with that bug still in place,
 *   so the options each observer was constructed with are asserted directly.
 * - **what proximity does to work.** Far means no new request, a dropped queue
 *   entry and no animation — not merely "was hydrated once".
 */

import { act, render, screen } from "@testing-library/react";
import { useState, type ReactNode } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import {
  AttachmentHydrationContext,
  NEAR_DEBOUNCE_MS,
  PREVIEW_PREFETCH_MARGIN_PX,
  TimelineScrollRootContext,
  useLazyAttachment,
} from "./lazyAttachment";

interface ObserverDouble {
  callback: IntersectionObserverCallback;
  options: IntersectionObserverInit | undefined;
  targets: Set<Element>;
  disconnected: boolean;
}

const observers: ObserverDouble[] = [];

const prefetchMargin = `${PREVIEW_PREFETCH_MARGIN_PX}px 0px`;

function live(): ObserverDouble[] {
  return observers.filter((observer) => !observer.disconnected);
}

function observerFor(region: "near" | "visible"): ObserverDouble {
  const wanted = region === "near" ? prefetchMargin : undefined;
  const found = live().filter((observer) => observer.options?.rootMargin === wanted);
  expect(found).toHaveLength(1);
  return found[0];
}

/** Reports an intersection through the observer that really covers the region. */
function report(region: "near" | "visible", intersecting: boolean) {
  const observer = observerFor(region);
  const entries = [...observer.targets].map(
    (target) => ({ target, isIntersecting: intersecting }) as IntersectionObserverEntry,
  );
  if (entries.length > 0) {
    act(() => observer.callback(entries, {} as IntersectionObserver));
  }
}

/** Renders the gate as text, which is the whole observable contract. */
function Probe() {
  const { ref, gate } = useLazyAttachment();
  return (
    <AttachmentHydrationContext.Provider value={gate}>
      <div ref={ref} data-testid="row">
        <span data-testid="state">
          {gate.proximity}:{gate.active ? "active" : "idle"}:{gate.priority}
        </span>
      </div>
    </AttachmentHydrationContext.Provider>
  );
}

function state(): string {
  return screen.getByTestId("state").textContent ?? "";
}

/** The timeline's own scrollport, published exactly as ChatMessageArea does. */
function WithScrollRoot({ children }: { children: ReactNode }) {
  const [root, setRoot] = useState<HTMLDivElement | null>(null);
  return (
    <div ref={setRoot} data-testid="scrollport">
      <TimelineScrollRootContext.Provider value={root}>
        {children}
      </TimelineScrollRootContext.Provider>
    </div>
  );
}

beforeEach(() => {
  vi.useFakeTimers({ shouldAdvanceTime: true });
  observers.length = 0;
  class MockIntersectionObserver {
    private readonly entry: ObserverDouble;
    constructor(callback: IntersectionObserverCallback, options?: IntersectionObserverInit) {
      this.entry = { callback, options, targets: new Set<Element>(), disconnected: false };
      observers.push(this.entry);
    }
    observe(target: Element) {
      this.entry.targets.add(target);
    }
    unobserve(target: Element) {
      this.entry.targets.delete(target);
    }
    disconnect() {
      this.entry.targets.clear();
      this.entry.disconnected = true;
    }
  }
  vi.stubGlobal("IntersectionObserver", MockIntersectionObserver);
});

afterEach(() => {
  vi.useRealTimers();
  vi.unstubAllGlobals();
});

describe("observer configuration", () => {
  it("roots both observers on the timeline's scrollport, not on the window", () => {
    render(
      <WithScrollRoot>
        <Probe />
      </WithScrollRoot>,
    );

    const scrollport = screen.getByTestId("scrollport");
    expect(observerFor("near").options?.root).toBe(scrollport);
    expect(observerFor("visible").options?.root).toBe(scrollport);
  });

  it("gives the prefetch observer the configured margin and the viewport observer none", () => {
    render(
      <WithScrollRoot>
        <Probe />
      </WithScrollRoot>,
    );

    expect(observerFor("near").options?.rootMargin).toBe(prefetchMargin);
    // No margin: this one answers "is it on screen", which is what P0 and GIF
    // playback are allowed to depend on.
    expect(observerFor("visible").options?.rootMargin).toBeUndefined();
  });

  it("shares one pair of observers across every attachment in the same root", () => {
    render(
      <WithScrollRoot>
        <Probe />
        <Probe />
        <Probe />
      </WithScrollRoot>,
    );

    expect(live()).toHaveLength(2);
    expect(observerFor("near").targets.size).toBe(3);
  });

  it("falls back to the window when there is no timeline around the attachment", () => {
    render(<Probe />);

    expect(observerFor("near").options?.root).toBeNull();
  });

  it("disconnects the pair once the last attachment in that root unmounts", () => {
    const { unmount } = render(
      <WithScrollRoot>
        <Probe />
      </WithScrollRoot>,
    );
    expect(live()).toHaveLength(2);

    unmount();

    expect(live()).toHaveLength(0);
  });
});

describe("proximity", () => {
  function renderProbe() {
    return render(
      <WithScrollRoot>
        <Probe />
      </WithScrollRoot>,
    );
  }

  it("starts far, with no work allowed", () => {
    renderProbe();

    expect(state()).toBe("far:idle:1");
  });

  it("reaches near before visible, as a real scroll does", () => {
    renderProbe();

    report("near", true);
    expect(state()).toBe("near:idle:1");

    report("visible", true);
    expect(state()).toBe("visible:active:0");
  });

  it("waits out the debounce before letting a merely-near attachment work", () => {
    renderProbe();

    report("near", true);
    expect(state()).toBe("near:idle:1");

    act(() => {
      vi.advanceTimersByTime(NEAR_DEBOUNCE_MS);
    });
    expect(state()).toBe("near:active:1");
  });

  it("allows nothing when a fast scroll crosses the prefetch region", () => {
    renderProbe();

    report("near", true);
    act(() => {
      vi.advanceTimersByTime(NEAR_DEBOUNCE_MS - 20);
    });
    report("near", false);
    act(() => {
      vi.advanceTimersByTime(NEAR_DEBOUNCE_MS * 4);
    });

    expect(state()).toBe("far:idle:1");
  });

  it("acts immediately, at top priority, for something already on screen", () => {
    renderProbe();

    report("near", true);
    report("visible", true);

    // No timer advance: an artificial delay in front of the reader's eyes is
    // exactly what the debounce must not become.
    expect(state()).toBe("visible:active:0");
  });

  it("keeps working, at prefetch priority, when it leaves the viewport but stays near", () => {
    renderProbe();
    report("near", true);
    report("visible", true);

    report("visible", false);

    // Still inside the useful region, and it has already proven it is being
    // scrolled toward — making it serve the debounce again would be a stall.
    expect(state()).toBe("near:active:1");
  });

  it("stops allowing work as soon as it goes far, however long it was active", () => {
    renderProbe();
    report("near", true);
    report("visible", true);
    expect(state()).toBe("visible:active:0");

    report("visible", false);
    report("near", false);

    expect(state()).toBe("far:idle:1");
  });

  it("serves the debounce again on the next approach after going far", () => {
    renderProbe();
    report("near", true);
    report("visible", true);
    report("visible", false);
    report("near", false);

    report("near", true);
    expect(state()).toBe("near:idle:1");

    act(() => {
      vi.advanceTimersByTime(NEAR_DEBOUNCE_MS);
    });
    expect(state()).toBe("near:active:1");
  });

  it("stops observing when the attachment unmounts", () => {
    const { unmount } = renderProbe();
    expect(observerFor("near").targets.size).toBe(1);

    unmount();

    expect(live()).toHaveLength(0);
  });
});

describe("without IntersectionObserver", () => {
  it("treats everything as visible rather than withholding every preview", () => {
    vi.stubGlobal("IntersectionObserver", undefined);

    render(<Probe />);

    expect(state()).toBe("visible:active:0");
  });
});
