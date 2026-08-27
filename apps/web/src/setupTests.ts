import "@testing-library/jest-dom/vitest";
import { cleanup } from "@testing-library/react";
import { afterEach } from "vitest";

// JSDOM has no layout, but ProseMirror reads geometry while pasting and scrolling.
const emptyDOMRect = () => new DOMRect();
Range.prototype.getClientRects = () => [] as unknown as DOMRectList;
Range.prototype.getBoundingClientRect = emptyDOMRect;
Element.prototype.getBoundingClientRect = emptyDOMRect;
// Same reason: jsdom has no viewport to scroll, so its window.scrollTo only logs
// "Not implemented". ChatShell resets the document scroll when it mounts, which
// is a real browser behaviour and not something a test should have to silence.
window.scrollTo = () => undefined;

/**
 * jsdom implements no layout, and therefore no ResizeObserver.
 *
 * The reaction picker uses one to re-place itself when its lazily-loaded
 * content changes size (issue #496), so a test needs an observer it can drive
 * rather than one that silently never fires: this records every live
 * observation, and flushResizeObservers() plays the browser's part by invoking
 * their callbacks. Observations disappear on disconnect, which is also what
 * lets a test assert that the picker cleaned up after itself.
 */
const resizeObservations = new Map<ResizeObserver, Set<Element>>();
const resizeCallbacks = new Map<ResizeObserver, ResizeObserverCallback>();

class TestResizeObserver implements ResizeObserver {
  constructor(callback: ResizeObserverCallback) {
    resizeObservations.set(this, new Set());
    resizeCallbacks.set(this, callback);
  }

  observe(target: Element): void {
    resizeObservations.get(this)?.add(target);
  }

  unobserve(target: Element): void {
    resizeObservations.get(this)?.delete(target);
  }

  disconnect(): void {
    resizeObservations.delete(this);
    resizeCallbacks.delete(this);
  }
}

window.ResizeObserver = TestResizeObserver;

/** Elements currently being observed, across every live observer. */
export function observedElements(): Element[] {
  return [...resizeObservations.values()].flatMap((targets) => [...targets]);
}

/** Invokes every live observer's callback, as a real resize would. */
export function flushResizeObservers(): void {
  for (const [observer, callback] of resizeCallbacks) {
    const targets = resizeObservations.get(observer) ?? new Set<Element>();
    const entries = [...targets].map((target) => ({ target }) as ResizeObserverEntry);
    if (entries.length > 0) callback(entries, observer);
  }
}

afterEach(() => {
  cleanup();
  resizeObservations.clear();
  resizeCallbacks.clear();
});
