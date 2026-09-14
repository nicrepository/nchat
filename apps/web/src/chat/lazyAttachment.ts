/**
 * Lazy hydration for attachments (issue #675).
 *
 * One place decides *when* an attachment is allowed to do work, for every
 * attachment type, instead of each component growing its own observer and its
 * own rules.
 *
 * # Two independent facts, never one boolean
 *
 * An attachment has a *proximity* and its resources have a *state*, and
 * collapsing them into a single "hydrated" flag is what makes a timeline keep
 * paying for things nobody is looking at:
 *
 *   proximity   far / near / visible — where the row is relative to the
 *               timeline's scrollport, right now. It goes both ways.
 *   active      whether work may run right now. Derived from proximity, and
 *               false again the moment a row goes far.
 *
 * Whether the bytes have already arrived is not tracked here at all — that is
 * useAttachmentBlobUrl's business. The division matters because the two answers
 * differ: a preview that finished loading may stay on screen while its row
 * drifts out of the useful region (dropping it would only buy a flicker and a
 * second request), while an animation, a queued task or an in-flight request
 * for that same row must stop.
 *
 *   far      no new request; a queued task is dropped; an in-flight one is
 *            aborted once nothing else wants it; no GIF keeps animating;
 *   near     eligible at P1 after a short debounce, so a fast scroll across a
 *            hundred messages does not leave a hundred requests behind it;
 *   visible  eligible immediately at P0, with no artificial delay.
 *
 * # The observers are rooted on the timeline, not on the window
 *
 * The message list is its own scrollport (`.chat-msg-area__list`,
 * `overflow-y: auto`). An observer with the default root measures against the
 * *window*, so a row can sit far down the timeline's own scroll while being
 * well inside the window — and the prefetch margin then describes a distance
 * nobody scrolls through. The root is therefore the scroll container, supplied
 * by the timeline through TimelineScrollRootContext.
 *
 * Observers are shared per root — one prefetch observer and one viewport
 * observer for the whole conversation, not two per attachment — and torn down
 * as soon as the last row using that root goes.
 *
 * # This is not an authorization boundary
 *
 * It decides when a request is *made*. Whether it is answered is file-service's
 * decision, re-taken on every request, and unchanged by anything here.
 */

import { createContext, useContext, useEffect, useMemo, useState } from "react";

import type { PreviewPriority } from "./previewScheduler";

/**
 * How far outside the scrollport an attachment starts preparing itself.
 *
 * Inside the issue's 600–1000 px suggestion: about one screen of chat on a
 * laptop, which is what makes a preview look "already there" at ordinary
 * scroll speeds without arming a whole conversation at once.
 */
export const PREVIEW_PREFETCH_MARGIN_PX = 800;

/**
 * How long an attachment must stay in the prefetch region before it counts.
 *
 * A flick of the wheel crosses the region in well under this; a real scroll
 * toward something does not. Never applied to something already on screen —
 * that would be an artificial delay in front of the user's eyes.
 */
export const NEAR_DEBOUNCE_MS = 150;

export type AttachmentProximity = "far" | "near" | "visible";

interface Registration {
  near: boolean;
  visible: boolean;
  notify: (proximity: AttachmentProximity) => void;
}

interface ObserverPair {
  prefetch: IntersectionObserver;
  viewport: IntersectionObserver;
  registrations: Map<Element, Registration>;
}

/**
 * One pair of observers per scroll root, shared by every attachment inside it.
 *
 * Weakly keyed so a conversation's container being discarded takes its entry
 * with it even if a cleanup were ever missed. `document` stands in for "no
 * container" — a thumbnail rendered outside the timeline, where the window is
 * genuinely the right root.
 */
const pairsByRoot = new WeakMap<Element | Document, ObserverPair>();

/**
 * The roots that currently have a pair, so the whole registry can be torn down
 * at once.
 *
 * A WeakMap cannot be enumerated, and one of its keys is `document` — which
 * outlives every component in a test file. Without this, a pair built around
 * one test's IntersectionObserver double would be handed to the next test,
 * which would then observe through a double nobody is driving any more and see
 * every attachment as permanently far away. The set is emptied by the same
 * cleanup that disconnects the pairs, so nothing accumulates in production.
 */
const rootsWithPairs = new Set<Element | Document>();

function proximityOf(registration: Registration): AttachmentProximity {
  if (registration.visible) return "visible";
  return registration.near ? "near" : "far";
}

function handleEntries(
  pair: ObserverPair,
  field: "near" | "visible",
): IntersectionObserverCallback {
  return (entries) => {
    for (const entry of entries) {
      const registration = pair.registrations.get(entry.target);
      if (!registration) continue;
      registration[field] = entry.isIntersecting;
      registration.notify(proximityOf(registration));
    }
  };
}

/**
 * Reports whether this environment can observe proximity at all.
 *
 * Without IntersectionObserver there is no way to tell far from visible, and
 * withholding every preview forever would be worse than loading them: the
 * fallback is "everything is visible", which is exactly the behaviour this
 * feature replaces.
 */
export function canObserveProximity(): boolean {
  return typeof IntersectionObserver !== "undefined";
}

function pairFor(root: Element | null): ObserverPair {
  const key: Element | Document = root ?? document;
  const existing = pairsByRoot.get(key);
  if (existing) return existing;
  const pair: Partial<ObserverPair> & { registrations: Map<Element, Registration> } = {
    registrations: new Map(),
  };
  pair.prefetch = new IntersectionObserver(handleEntries(pair as ObserverPair, "near"), {
    root,
    rootMargin: `${PREVIEW_PREFETCH_MARGIN_PX}px 0px`,
  });
  // The same root, deliberately with no margin: this one answers "is it on
  // screen", which is what P0 and GIF playback are allowed to depend on.
  pair.viewport = new IntersectionObserver(handleEntries(pair as ObserverPair, "visible"), {
    root,
  });
  const complete = pair as ObserverPair;
  pairsByRoot.set(key, complete);
  rootsWithPairs.add(key);
  return complete;
}

/**
 * Registers one element with the observers for its scroll root.
 *
 * Returns the unobserve. The last registration leaving a root disconnects both
 * of its observers, so switching conversations — which unmounts every row and
 * then supplies a new container — leaves nothing observing the old one.
 */
export function observeAttachmentProximity(
  root: Element | null,
  element: Element,
  notify: (proximity: AttachmentProximity) => void,
): () => void {
  if (!canObserveProximity()) return () => {};
  const pair = pairFor(root);
  pair.registrations.set(element, { near: false, visible: false, notify });
  pair.prefetch.observe(element);
  pair.viewport.observe(element);
  return () => {
    pair.registrations.delete(element);
    pair.prefetch.unobserve(element);
    pair.viewport.unobserve(element);
    if (pair.registrations.size === 0) {
      pair.prefetch.disconnect();
      pair.viewport.disconnect();
      pairsByRoot.delete(root ?? document);
      rootsWithPairs.delete(root ?? document);
    }
  };
}

/**
 * The element the timeline scrolls in, published to every attachment inside it.
 *
 * Null means "no timeline around me" — the details panel's file list, a card
 * rendered on its own — and the window is then the honest root.
 */
export const TimelineScrollRootContext = createContext<HTMLElement | null>(null);

export interface AttachmentGate {
  /** Where this attachment is, right now, relative to the scrollport. */
  proximity: AttachmentProximity;
  /**
   * Whether work may run right now. False while far, which is what stops a
   * queued preview, an in-flight request nobody else wants, and a GIF's
   * animation. It says nothing about whether bytes already arrived.
   */
  active: boolean;
  /** The priority its fetches enter the scheduler with. */
  priority: PreviewPriority;
}

/**
 * The gate an attachment's own components read.
 *
 * The default is open, deliberately: a thumbnail rendered outside a lazy
 * container behaves exactly as it always has.
 */
export const AttachmentHydrationContext = createContext<AttachmentGate>({
  proximity: "visible",
  active: true,
  priority: 0,
});

export function useAttachmentGate(): AttachmentGate {
  return useContext(AttachmentHydrationContext);
}

export interface LazyAttachment {
  /** Attach to the element whose position decides this attachment's fate. */
  ref: (element: HTMLElement | null) => void;
  /** Stable across renders that do not change it — safe as a context value. */
  gate: AttachmentGate;
}

/**
 * Tracks one attachment's proximity and turns it into a gate.
 */
export function useLazyAttachment(): LazyAttachment {
  const root = useContext(TimelineScrollRootContext);
  const [element, setElement] = useState<HTMLElement | null>(null);
  const [proximity, setProximity] = useState<AttachmentProximity>(() =>
    canObserveProximity() ? "far" : "visible",
  );
  /**
   * Whether the prefetch debounce has been served for the current stay in the
   * useful region. Set by the timer below, and by ever having been visible —
   * an attachment that scrolls from the viewport back into the prefetch region
   * has already proven it is being scrolled toward, and must not be made to
   * wait again. Cleared on going far, so the next approach pays the debounce.
   */
  const [debounceServed, setDebounceServed] = useState(false);

  // Adjusting state during render rather than in an effect: this codebase's
  // lint config forbids a state setter in an effect body, and neither of these
  // may wait a commit — "already on screen" must not be delayed, and "gone
  // far" must not leave a stale grant behind for one more frame.
  if (!debounceServed && proximity === "visible") setDebounceServed(true);
  if (debounceServed && proximity === "far") setDebounceServed(false);

  useEffect(() => {
    if (!element) return;
    return observeAttachmentProximity(root, element, setProximity);
  }, [element, root]);

  useEffect(() => {
    if (proximity !== "near" || debounceServed) return;
    const timer = window.setTimeout(() => setDebounceServed(true), NEAR_DEBOUNCE_MS);
    return () => window.clearTimeout(timer);
  }, [proximity, debounceServed]);

  const active = proximity === "visible" || (proximity === "near" && debounceServed);
  const priority: PreviewPriority = proximity === "visible" ? 0 : 1;
  const gate = useMemo(() => ({ proximity, active, priority }), [proximity, active, priority]);
  return { ref: setElement, gate };
}

/**
 * Test seam: disconnects every observer pair and forgets every root.
 *
 * The counterpart of resetPreviewScheduler. Both exist for the same reason —
 * module state shared by every attachment in a test file — and both are called
 * from the suite's global teardown.
 */
export function resetAttachmentProximityObservers(): void {
  for (const root of rootsWithPairs) {
    const pair = pairsByRoot.get(root);
    if (!pair) continue;
    pair.prefetch.disconnect();
    pair.viewport.disconnect();
    pair.registrations.clear();
    pairsByRoot.delete(root);
  }
  rootsWithPairs.clear();
}
