/**
 * useConversationDetails — data for the details panel of a channel (issue #435),
 * an ad-hoc group (issue #441) or a 1:1 DM's profile (issue #443).
 *
 * Two independent requests behind one hook: the conversation projection from
 * chat-service and the recent attachments from file-service. They are separate
 * sections of the panel and separate services, so a failure in one leaves the
 * other rendered — a single combined state would let a file-service outage
 * blank out the conversation's own metadata.
 *
 * A direct target issues only the first: the profile panel has no files section
 * (issue #443), and requesting attachments nobody will render would be a
 * request for data the user did not ask for.
 *
 * Correctness properties this hook owns:
 *  - every request is keyed by the *pair* (kind, id), not by the id alone: a
 *    channel and a conversation are separate id spaces, so keying on the id
 *    could hold a stale result across a channel → group switch;
 *  - a target switch aborts the in-flight request *and* resets to loading, so
 *    the previous conversation's data is never shown under the new one's name;
 *  - a response that arrives after the key changed is dropped by the abort
 *    signal, so a slow reply for A cannot overwrite B;
 *  - unmount aborts too, so nothing dispatches into a dead component.
 *
 * State lives in a reducer so the load effect can dispatch synchronously
 * without tripping react-hooks/set-state-in-effect, mirroring usePins.
 *
 * Security: no tokens are handled here; the server enforces channel access and
 * answers 404 for anything the caller cannot read.
 */

import { useCallback, useEffect, useReducer, useRef } from "react";

import { fetchChannelDetails, fetchDirectProfile, fetchGroupDetails } from "./chatApi";
import { fetchConversationAttachments } from "./filesApi";
import { canShowPreview, isPreviewWorkPending } from "./useAttachmentPreview";
import type { ChannelAttachment, ConversationDetails } from "./chatTypes";

/**
 * What the panel is being opened for.
 *
 * `kind` is the domain discriminant — "channel" for chat.channels, "group" and
 * "direct" for a chat.dm_conversations row of the matching type — and never
 * something derived from the URL or from how many people are in the
 * conversation.
 */
export interface ConversationDetailsTarget {
  kind: "channel" | "group" | "direct";
  id: string;
}

/** How many files the panel previews. The server clamps anything larger. */
export const channelFilesPreviewLimit = 5;

// ── Preview reconciliation policy (RF-31, issue #464) ────────────────────────
//
// The panel re-reads its attachment list while an upload is still moving
// through the server's lifecycle: awaiting the malware scan, then awaiting the
// render. The two waits are not the same shape, and the policy has to hold for
// the worse one.
//
// A render is bounded: the worker claims within ten seconds and finishes in
// about one. A *scan* is not — a scanner that is stopped, unreachable, or not
// deployed at all never rules, and the attachment sits in pending_scan
// indefinitely. Polling that at a fixed five-second cadence would be a request
// every five seconds, forever, for every open panel.
//
// So the cadence starts at the render's timescale and backs off to the
// scanner's, and the whole window is finite.

/**
 * The first wait, matched to the server's own cadence: the preview worker polls
 * every ten seconds and renders in about one, so a thumbnail becomes available
 * somewhere inside a ten-second window and this halves the wait to notice it.
 */
export const previewReconcileIntervalMs = 5_000;

/**
 * The ceiling the delay doubles up to. Beyond this the panel is no longer
 * waiting for something that was about to happen — it is watching a queue that
 * has stalled, and thirty seconds is often enough for that.
 */
export const previewReconcileMaxIntervalMs = 30_000;

/**
 * How many consecutive polls may see no change at all before the panel stops.
 *
 * With the schedule below this is roughly five minutes of watching, which
 * comfortably covers a scan and a render and does not cover a scanner that is
 * down. Reaching it is not an error: the fallback icon is already what the user
 * is looking at, and the file is still listed, still downloadable once cleared,
 * and one explicit reload — or reopening the panel — opens a fresh window.
 *
 * The counter is per *observed state*, not per panel: any change in the list
 * restarts it, so a long wait for the scan does not eat the budget for the
 * render that follows it.
 */
export const previewReconcileMaxAttempts = 12;

/**
 * The cadence of the *other* reconciliation: watching a thumbnail that is
 * already on screen for the moment it stops being servable.
 *
 * This is a different problem from waiting for a preview to appear, and it does
 * not end. A rescan can condemn a file that already has a preview; the bytes
 * are in the page by then, and the server refusing the next request does not
 * take back the one it already answered. Only the client can remove what it is
 * already showing, and it can only do that if it keeps asking.
 *
 * So this cycle has no attempt budget — bounding it would mean a panel left
 * open long enough goes back to displaying content that was revoked, which is
 * the bug this exists to prevent. What keeps it cheap instead is the cadence
 * and the fact that it stops entirely while the tab is hidden: it matches the
 * revalidation useMessages already runs for referenced messages, doubled,
 * because a rescan is far rarer than an edit.
 */
export const previewRevalidateIntervalMs = 30_000;

/**
 * Doubling backoff, capped. Deterministic on purpose — unlike the socket's
 * reconnect (chatSocket.computeBackoffDelay), this is not a herd of clients
 * racing back to one server after a restart, so jitter would buy nothing and
 * cost every test a stubbed random.
 */
export function previewReconcileDelayMs(attempt: number): number {
  const doubled = previewReconcileIntervalMs * 2 ** Math.max(attempt, 0);
  return Math.min(doubled, previewReconcileMaxIntervalMs);
}

export type AsyncSection<T> =
  | { status: "loading" }
  | { status: "ready"; data: T }
  | { status: "error" };

export interface ConversationDetailsState {
  details: AsyncSection<ConversationDetails>;
  files: AsyncSection<ChannelAttachment[]>;
  /**
   * Refetches the panel for the target it is currently showing (issue #398).
   *
   * The single reconciliation path after a membership change. Both triggers —
   * the local add's HTTP response and a members.added event from anyone else —
   * call this rather than merging a response into the rendered list, so the
   * roster and both counters always come from one authority and two triggers
   * arriving together cannot double a member or a count.
   *
   * It re-reads the current target rather than taking one as an argument, so a
   * caller holding a stale closure cannot make the panel load a conversation
   * that is no longer open. With the panel closed there is no target and this
   * is a no-op.
   */
  reload: () => void;
}

type Action =
  | { type: "reset" }
  | { type: "details_ready"; details: ConversationDetails }
  | { type: "details_error" }
  | { type: "files_ready"; files: ChannelAttachment[] }
  | { type: "files_error" };

/**
 * The reducer owns the two sections only; `reload` is attached by the hook.
 *
 * Keeping the callback out of reducer state is what stops a dispatch from ever
 * replacing it with a stale identity.
 */
type Sections = Omit<ConversationDetailsState, "reload">;

const initialState: Sections = {
  details: { status: "loading" },
  files: { status: "loading" },
};

function reducer(state: Sections, action: Action): Sections {
  switch (action.type) {
    case "reset":
      return initialState;
    case "details_ready":
      return { ...state, details: { status: "ready", data: action.details } };
    case "details_error":
      return { ...state, details: { status: "error" } };
    case "files_ready":
      return { ...state, files: { status: "ready", data: action.files } };
    case "files_error":
      return { ...state, files: { status: "error" } };
  }
}

function isAbort(error: unknown): boolean {
  return error instanceof Error && error.name === "AbortError";
}

/**
 * A compact description of everything the reconciliation watches.
 *
 * Only the three fields the server can move on its own are in it: the id, the
 * scan status and the preview status. A change in any of them is progress and
 * restarts the backoff; a poll that returns the same list is what the budget
 * counts.
 */
function attachmentProgressKey(files: ChannelAttachment[]): string {
  return files.map((file) => `${file.id}:${file.status}:${file.previewStatus}`).join("|");
}

/**
 * The reconciliation window: which target and which observed state the current
 * backoff belongs to, and how many polls have seen no change.
 *
 * `round` exists only to re-arm the effect after a completed request — it is
 * what makes the next timer start from the previous one *finishing* rather than
 * from a fixed interval, which is what stops requests from overlapping.
 */
interface ReconcileWindow {
  round: number;
  attempt: number;
  target: string;
  progressKey: string;
}

type ReconcileAction =
  | { type: "polled"; target: string; progressKey: string }
  | { type: "restart" }
  | { type: "resumed" };

const initialReconcile: ReconcileWindow = {
  round: 0,
  attempt: 0,
  target: "",
  progressKey: "",
};

function reconcileReducer(state: ReconcileWindow, action: ReconcileAction): ReconcileWindow {
  switch (action.type) {
    case "polled": {
      // A window belongs to one target and one observed state. A poll that
      // found the same state costs a step of backoff; anything else is progress
      // and the next scheduling starts over, because the fields no longer match.
      const sameWindow = state.target === action.target && state.progressKey === action.progressKey;
      return {
        round: state.round + 1,
        attempt: sameWindow ? state.attempt + 1 : 1,
        target: action.target,
        progressKey: action.progressKey,
      };
    }
    case "restart":
      return { ...initialReconcile, round: state.round + 1 };
    case "resumed":
      // The tab came back. Re-arm the timer without touching the backoff: the
      // window is unchanged, it was simply not being watched while hidden.
      return { ...state, round: state.round + 1 };
  }
}

/**
 * Loads the panel's data for `target`. Pass null (panel closed, or an unknown
 * target) and the hook stays idle and issues no request.
 */
export function useConversationDetails(
  target: ConversationDetailsTarget | null,
): ConversationDetailsState {
  const [state, dispatch] = useReducer(reducer, initialState);
  const abortRef = useRef<AbortController | null>(null);

  // The effect depends on the two primitives rather than on the object, so a
  // caller that rebuilds the target literal on every render does not refetch.
  const kind = target?.kind ?? "";
  const id = target?.id ?? "";

  /**
   * Fetches both sections for (nextKind, nextID).
   *
   * `reset` is the difference between the two callers, and it is not cosmetic.
   * A target *switch* must reset: showing the previous conversation's
   * participants and files under the new one's name would be wrong, and briefly
   * so in a way the user cannot detect. A *refetch* of the conversation already
   * displayed must not: the data is about to be replaced by a newer version of
   * itself, and blanking the section in between unmounts everything inside it —
   * including the control the user just activated, which drops keyboard focus
   * to <body> mid-flow.
   */
  const load = useCallback((nextKind: string, nextID: string, reset = true) => {
    abortRef.current?.abort();
    if (reset) {
      dispatch({ type: "reset" });
    }
    if (!nextID || (nextKind !== "channel" && nextKind !== "group" && nextKind !== "direct")) {
      return;
    }

    const controller = new AbortController();
    abortRef.current = controller;

    // Each kind has its own endpoint because each names a different aggregate:
    // a channel, a group conversation, and the person on the other end of a
    // direct one.
    //
    // The direct client returns an already-discriminated value and this hook
    // passes it through untouched. Re-tagging it here — or substituting the
    // requested ID for the one the server sent — would overwrite the two things
    // fetchDirectProfile validated, and a response for the wrong conversation
    // would arrive labelled as the right one.
    const details =
      nextKind === "channel"
        ? fetchChannelDetails(nextID, controller.signal).then(
            (channel): ConversationDetails => ({ kind: "channel", ...channel }),
          )
        : nextKind === "group"
          ? fetchGroupDetails(nextID, controller.signal).then(
              (group): ConversationDetails => ({ kind: "group", ...group }),
            )
          : fetchDirectProfile(nextID, controller.signal);

    details.then(
      (resolved) => {
        if (controller.signal.aborted) return;
        dispatch({ type: "details_ready", details: resolved });
      },
      (error: unknown) => {
        if (controller.signal.aborted || isAbort(error)) return;
        dispatch({ type: "details_error" });
      },
    );

    // A 1:1 profile has no files section, so no attachment request is issued
    // and `files` stays at its initial loading value, unread by the direct
    // variant of the panel.
    if (nextKind === "direct") return;

    // A group's attachments live under the DM destination, never the channel
    // one: the two are separate resources with separate authorization.
    fetchConversationAttachments(
      { kind: nextKind === "channel" ? "channel" : "dm", id: nextID },
      channelFilesPreviewLimit,
      controller.signal,
    ).then(
      (files) => {
        if (controller.signal.aborted) return;
        dispatch({ type: "files_ready", files });
      },
      (error: unknown) => {
        if (controller.signal.aborted || isAbort(error)) return;
        dispatch({ type: "files_error" });
      },
    );
  }, []);

  // The loaded target is mirrored into a ref, written by the same effect that
  // performs the load, so `reload` can read it without being re-created on every
  // switch. A caller that stored the callback — useMessages holds it for the
  // lifetime of the socket — therefore always refetches the conversation that is
  // open now, never the one that was open when it captured it.
  //
  // Written in the effect rather than during render: a render can be discarded,
  // and a ref updated by a discarded one would point at a target that was never
  // loaded.
  const targetRef = useRef({ kind, id });

  // The reconciliation window (RF-31). It is declared here, above `reload`,
  // because an explicit refresh restarts it: that is the documented way back
  // for a panel whose polling budget ran out.
  const [reconcile, dispatchReconcile] = useReducer(reconcileReducer, initialReconcile);

  // Refetch in place: same target, newer data, no blank frame and no unmount.
  //
  // It also restarts the reconciliation window. That is the documented way back
  // for a panel whose polling budget ran out: the user asked for fresh data, so
  // the panel is allowed to watch for a while again.
  const reload = useCallback(() => {
    const { kind: currentKind, id: currentID } = targetRef.current;
    dispatchReconcile({ type: "restart" });
    load(currentKind, currentID, false);
  }, [load]);

  useEffect(() => {
    targetRef.current = { kind, id };
    load(kind, id);
    return () => abortRef.current?.abort();
  }, [kind, id, load]);

  // ── Preview reconciliation (RF-31, issue #464) ─────────────────────────────
  //
  // An upload is not finished when its request answers. The malware scan has to
  // rule, and only then does the preview worker render — so a panel that is
  // already open shows `pending_scan` for a file whose thumbnail is a few
  // seconds away, and would keep showing it until the user closed and reopened
  // the panel.
  //
  // Only the attachment list is re-read: the conversation's own projection has
  // not changed, and `reload` — which refetches both sections — would put a
  // request on chat-service every few seconds for nothing.
  //
  // Three properties, and each is load-bearing:
  //
  //   - **one cycle per panel.** The timer lives in this hook, so a list of ten
  //     waiting attachments is one request, not ten timers;
  //   - **no overlap.** The next timer is armed from the *completion* of the
  //     previous request, so a poll slower than its own delay cannot stack up
  //     behind itself;
  //   - **one scheduler, two jobs.** See below.
  //
  // The two jobs are opposites and it matters that they are not confused:
  //
  //   *generation* — waiting for something to appear. Bounded: the delay doubles
  //   to a ceiling and the window ends after previewReconcileMaxAttempts
  //   unchanged polls, because waiting for a scan that may never come would
  //   otherwise be an unbounded request loop, and the cost of giving up is only
  //   that a thumbnail shows up late;
  //
  //   *revocation* — watching something already on screen stop being servable.
  //   Unbounded, on a slow cadence, because the cost of giving up is different
  //   in kind: a rescan can condemn a file whose bytes are already in the page,
  //   and nothing but this cycle will ever take them off it. A budget here would
  //   mean a panel left open long enough silently goes back to displaying
  //   revoked content.
  //
  // Generation wins while it has work, since a list with something pending is
  // re-read faster than the revocation cadence anyway.

  // What the panel currently believes about every attachment it is showing.
  //
  // It is the window's identity as well as its content: any change to it —
  // pending_scan becoming clean, pending becoming ready, an attachment leaving
  // the list — means the server made progress, which restarts the backoff. So a
  // long wait for the scan never eats the budget for the render after it.
  const progressKey = state.files.status === "ready" ? attachmentProgressKey(state.files.data) : "";

  // Both are recomputed from the rendered list rather than tracked separately,
  // so neither can disagree with what is on screen. They are booleans, so a list
  // rebuilt with identical contents does not restart the cycle.
  const awaitingPreview =
    state.files.status === "ready" && state.files.data.some(isPreviewWorkPending);

  // Something whose *bytes* are being displayed, or are about to be. This is the
  // same predicate AttachmentThumbnail loads on, so the set watched here is
  // exactly the set that can have a live object URL behind it.
  const showingPreview = state.files.status === "ready" && state.files.data.some(canShowPreview);

  const targetKey = `${kind}:${id}`;
  // The attempt count only accumulates while the target *and* the observed
  // state stay the same; anything else is a fresh window at the base delay.
  const attempt =
    reconcile.target === targetKey && reconcile.progressKey === progressKey ? reconcile.attempt : 0;

  // Which job the single timer is doing right now. Generation falls through to
  // revocation once its budget is spent, so a stalled `pending_scan` sharing the
  // list with a displayed thumbnail cannot switch the watching off.
  const reconcileMode: "generation" | "revocation" | "idle" =
    awaitingPreview && attempt < previewReconcileMaxAttempts
      ? "generation"
      : showingPreview
        ? "revocation"
        : "idle";

  // A hidden tab shows nothing, so there is nothing on screen to take away and
  // no reason to keep asking.
  //
  // The dispatch is unconditional — both directions of the transition matter.
  // Going hidden has to re-run the scheduling effect so its cleanup clears a
  // timer that is already armed; coming back has to re-arm one. Deciding which
  // is which belongs in the effect, which reads the visibility state anyway.
  useEffect(() => {
    if (reconcileMode === "idle") return;
    const onVisibilityChange = () => dispatchReconcile({ type: "resumed" });
    document.addEventListener("visibilitychange", onVisibilityChange);
    return () => document.removeEventListener("visibilitychange", onVisibilityChange);
  }, [reconcileMode]);

  useEffect(() => {
    // Nothing pending and nothing displayed: no timer exists at all.
    if (reconcileMode === "idle") return;
    if (!id || (kind !== "channel" && kind !== "group")) return;
    if (document.visibilityState === "hidden") return;

    const controller = new AbortController();
    const timer = window.setTimeout(
      () => {
        fetchConversationAttachments(
          { kind: kind === "channel" ? "channel" : "dm", id },
          channelFilesPreviewLimit,
          controller.signal,
        ).then(
          (files) => {
            // Aborted means the panel closed or the conversation changed while
            // this was in flight: the answer describes a target nobody is looking
            // at, and dispatching it would show the previous conversation's files
            // under the current one's name.
            if (controller.signal.aborted) return;
            dispatch({ type: "files_ready", files });
            // Recorded against the state this poll was made *against*, so the
            // next scheduling can tell "nothing changed" from "progress".
            dispatchReconcile({ type: "polled", target: targetKey, progressKey });
          },
          (error: unknown) => {
            if (controller.signal.aborted || isAbort(error)) return;
            // A transient failure leaves the list exactly as it is. Replacing it
            // with the error section would blank a working list because one
            // background poll missed, and the user did not ask for anything. The
            // retry is the next backoff step, never an immediate one.
            dispatchReconcile({ type: "polled", target: targetKey, progressKey });
          },
        );
      },
      reconcileMode === "generation"
        ? previewReconcileDelayMs(attempt)
        : previewRevalidateIntervalMs,
    );

    return () => {
      window.clearTimeout(timer);
      controller.abort();
    };
  }, [reconcileMode, attempt, kind, id, targetKey, progressKey, reconcile.round]);

  return { ...state, reload };
}
