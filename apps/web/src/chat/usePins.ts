/**
 * usePins — RF-05 pinned-messages state.
 *
 * Owns the pin list for the active target: initial fetch, a pinnedIds set for
 * O(1) per-message lookup, a togglePin action (REST round-trip, then reload),
 * and a reload() the caller wires to the pin.updated WebSocket event so pins
 * stay live for every readable channel/DM member. Pass null and the hook stays idle.
 *
 * It is the only owner of pins on screen: the pinned bar, the timeline and the
 * details panel's "Mensagens fixadas" section (issue #896) all read this one
 * instance, so a pin/unpin converges on every surface through one refetch.
 *
 * Security: no tokens handled here (authenticatedFetch owns auth); the server
 * enforces target read access. A rejected toggle surfaces a transient defensive
 * error and leaves the list untouched.
 *
 * State lives in a reducer (not useState) so the load effect can dispatch
 * synchronously without the cascading-render lint rule, mirroring useChatSidebar.
 */

import { useCallback, useEffect, useMemo, useReducer, useRef, useState } from "react";

import { fetchPins, pinMessage, unpinMessage, type PinTarget } from "./chatApi";
import type { PinnedItem } from "./chatTypes";

/**
 * The collection as one discriminated value (issue #896): "still loading",
 * "failed to load" and "loaded, possibly empty" are different answers, and an
 * empty list is only ever the third one.
 *
 * `refreshFailed` is the fourth answer: a list is held, but the latest read
 * meant to confirm it failed — after a pin/unpin, or a realtime invalidation —
 * so what is shown may be stale. The list stays; the caller says so and offers
 * a retry. It is not the initial-load `error`, which has no list to keep.
 */
export type PinsCollection =
  | { status: "loading" }
  | { status: "error" }
  | { status: "ready"; pins: PinnedItem[]; refreshFailed?: true };

interface PinsState {
  /** The target `collection` describes; see targetKey. */
  key: string;
  collection: PinsCollection;
  error: string | null;
}

type Action =
  | { type: "start"; key: string }
  | { type: "loaded"; pins: PinnedItem[] }
  | { type: "failed" }
  | { type: "error"; error: string }
  | { type: "clear_error" };

const loading: PinsCollection = { status: "loading" };
const noPins: PinnedItem[] = [];
const noPending: ReadonlySet<string> = new Set();

const initialState: PinsState = { key: "", collection: loading, error: null };

function reducer(state: PinsState, action: Action): PinsState {
  switch (action.type) {
    case "start":
      // A new target starts from nothing. A refetch of the same one keeps its
      // list visible until the new one arrives (no blink), except a failed
      // load, which a retry turns back into loading.
      if (action.key !== state.key) return { key: action.key, collection: loading, error: null };
      return state.collection.status === "error" ? { ...state, collection: loading } : state;
    case "loaded":
      return { ...state, collection: { status: "ready", pins: action.pins } };
    case "failed":
      // A refetch that fails does not destroy a list already loaded — it marks
      // it unconfirmed; only a load that never produced one becomes an error.
      if (state.collection.status === "ready") {
        return { ...state, collection: { ...state.collection, refreshFailed: true } };
      }
      return { ...state, collection: { status: "error" } };
    case "error":
      return { ...state, error: action.error };
    case "clear_error":
      return { ...state, error: null };
  }
}

/** Channel and DM ids are separate id spaces, so the kind is part of the key. */
function targetKey(target: PinTarget | null): string {
  return target ? `${target.kind}:${target.id}` : "";
}

/** A read of one target's pins, and the promise that settles when it is done. */
interface ActiveRead {
  key: string;
  settled: Promise<void>;
}

const idleRead: ActiveRead = { key: "", settled: Promise.resolve() };

/**
 * What a waiter on the read `own` of `key` must keep waiting for once that read
 * has ended: the newer read that replaced it, if it reads the same target —
 * and nothing otherwise.
 *
 * Only a read of the same target can answer for this one. When the reader
 * switched conversations (or the hook went idle), the new read is about
 * someone else; waiting on it would tie this target's mutation to another
 * conversation's network, and there is nothing left here to reconcile.
 */
function successorOf(
  latest: ActiveRead,
  own: Promise<void>,
  key: string,
): Promise<void> | undefined {
  return latest.key === key && latest.settled !== own ? latest.settled : undefined;
}

/**
 * How one pin/unpin call ended, for the caller that issued it.
 *
 * "persisted" means the server accepted the write — even if the read meant to
 * confirm it then failed, which is the collection's refreshFailed, not this.
 * "rejected" means the write itself was refused. "skipped" means nothing was
 * sent: no target, or the same message already had a mutation in flight.
 */
export type PinMutationOutcome = "persisted" | "rejected" | "skipped";

export interface UsePinsResult {
  /** The current target's collection; never another target's. */
  collection: PinsCollection;
  /** The loaded pins, or [] while loading or failed. */
  pins: PinnedItem[];
  pinnedIds: Set<string>;
  /** Messages with a pin/unpin in flight; a repeat for one of them is ignored. */
  pendingIds: ReadonlySet<string>;
  /** Transient defensive error for a rejected pin/unpin. */
  error: string | null;
  togglePin: (messageId: string, pin: boolean) => Promise<PinMutationOutcome>;
  reload: () => void;
}

export function usePins(target: PinTarget | null): UsePinsResult {
  const [state, dispatch] = useReducer(reducer, initialState);
  const abortRef = useRef<AbortController | null>(null);
  // The target on screen, for completions that outlive the one they began on.
  const currentTargetRef = useRef<PinTarget | null>(null);
  const inFlightRef = useRef(new Set<string>());
  // The read that currently speaks for the target on screen; see fetch.
  const latestReadRef = useRef<ActiveRead>(idleRead);
  const [pendingIds, setPendingIds] = useState<ReadonlySet<string>>(noPending);
  const targetKind = target?.kind ?? "";
  const targetId = target?.id ?? "";
  const resolvedTarget = useMemo<PinTarget | null>(() => {
    if (!targetId || (targetKind !== "channel" && targetKind !== "dm")) return null;
    return { kind: targetKind, id: targetId };
  }, [targetKind, targetId]);
  const key = targetKey(resolvedTarget);

  // Loads the target's pins. Every call aborts the one before it — including a
  // switch to another target — and a result is applied only while its request
  // is still the latest, so a late answer for A can never populate B and an
  // older reload can never overwrite a newer one.
  //
  // The promise returned settles when the collection has actually been
  // reconciled, not when this particular request ended: a read superseded by a
  // newer read of the same target (a pin.updated arriving mid-reload) hands
  // its waiters to that one — "my request was replaced" is not "the list
  // converged". A read superseded by another target's, or by going idle, just
  // ends: see successorOf.
  const fetch = useCallback((nextTarget: PinTarget | null): Promise<void> => {
    abortRef.current?.abort();
    const readKey = targetKey(nextTarget);
    dispatch({ type: "start", key: readKey });
    if (!nextTarget) {
      latestReadRef.current = idleRead;
      return idleRead.settled;
    }
    const ctrl = new AbortController();
    abortRef.current = ctrl;
    const settled: Promise<void> = fetchPins(nextTarget, ctrl.signal)
      .then(
        (pins) => {
          if (!ctrl.signal.aborted) dispatch({ type: "loaded", pins });
        },
        (err: unknown) => {
          if (ctrl.signal.aborted || (err instanceof Error && err.name === "AbortError")) return;
          dispatch({ type: "failed" });
        },
      )
      .then(() => successorOf(latestReadRef.current, settled, readKey));
    latestReadRef.current = { key: readKey, settled };
    return settled;
  }, []);

  useEffect(() => {
    currentTargetRef.current = resolvedTarget;
    void fetch(resolvedTarget);
    return () => {
      currentTargetRef.current = null;
      abortRef.current?.abort();
    };
  }, [resolvedTarget, fetch]);

  useEffect(() => {
    if (!state.error) return;
    const timer = window.setTimeout(() => dispatch({ type: "clear_error" }), 5_000);
    return () => window.clearTimeout(timer);
  }, [state.error]);

  const reload = useCallback(() => void fetch(resolvedTarget), [resolvedTarget, fetch]);

  // ponytail: no optimistic update; the REST call is cheap and the reload
  // reflects the authoritative order/cap. The lock is per message and lasts
  // until the collection is reconciled — through any reload that supersedes
  // the mutation's own — so a repeat press for the same message sends nothing
  // while another message stays actionable. A reconciliation that fails still
  // releases it, and surfaces as the collection's refreshFailed.
  const togglePin = useCallback(
    (messageId: string, pin: boolean): Promise<PinMutationOutcome> => {
      const inFlight = inFlightRef.current;
      if (!resolvedTarget || inFlight.has(messageId)) return Promise.resolve("skipped");
      const startedFor = resolvedTarget;
      const stillCurrent = () => currentTargetRef.current === startedFor;
      inFlight.add(messageId);
      setPendingIds(new Set(inFlight));
      const apply = pin ? pinMessage : unpinMessage;
      return apply(startedFor, messageId)
        .then(
          async (): Promise<PinMutationOutcome> => {
            if (stillCurrent()) await fetch(startedFor);
            return "persisted";
          },
          (): PinMutationOutcome => {
            if (stillCurrent()) {
              dispatch({
                type: "error",
                error: pin
                  ? "Não foi possível fixar a mensagem."
                  : "Não foi possível desafixar a mensagem.",
              });
            }
            return "rejected";
          },
        )
        .finally(() => {
          inFlight.delete(messageId);
          setPendingIds(new Set(inFlight));
        });
    },
    [resolvedTarget, fetch],
  );

  // Until the effect has retargeted the reducer, the state still describes the
  // previous target; that one render reads as loading, never as its pins.
  const collection = state.key === key ? state.collection : loading;
  const pins = collection.status === "ready" ? collection.pins : noPins;
  const pinnedIds = useMemo(() => new Set(pins.map((p) => p.message.id)), [pins]);

  return { collection, pins, pinnedIds, pendingIds, error: state.error, togglePin, reload };
}
