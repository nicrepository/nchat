/**
 * useMemberPicker — search + multi-select for a workspace people picker.
 *
 * Extracted from the one behaviour NewConversationDialog and the add-members
 * dialog genuinely share: debounce a query, cancel the request the previous
 * keystroke started, drop a reply that arrives after the query moved on, and
 * keep a selection that survives the result list changing under it.
 *
 * Deliberately narrow. It owns the search and the selection and nothing else —
 * no submit, no error copy, no dialog chrome, no knowledge of channels or
 * groups. Those differ between the two callers, and folding them in is how a
 * shared hook turns into a component with a mode flag. NewConversationDialog is
 * intentionally left alone by this change: it also drives a 1:1 open, a group
 * title and a channel form off the same query state, and rewriting that flow to
 * adopt this hook would be a refactor of conversation creation, which is not
 * what this issue is for.
 *
 * The caller supplies `search`, which is what makes this reusable without a
 * mode flag: a channel picker passes the channel-scoped endpoint, a group
 * picker the group-scoped one, and everything else here — debounce, in-flight
 * cancellation, out-of-order rejection, selection, capacity — is identical and
 * lives in one place.
 *
 * `excludedUserIds` is a *presentation* filter for people the caller already
 * knows about locally. It is deliberately not the eligibility rule: the search
 * endpoint excludes current members in SQL, because the panel's member list is
 * a capped, presence-filtered preview and never was a complete roster.
 */

import { useCallback, useEffect, useState } from "react";

import type { DMCandidate } from "./chatTypes";

/** Matches chat-service, which rejects a query shorter than two characters. */
export const memberSearchMinLength = 2;

/** Long enough to skip the intermediate keystrokes of a typed word. */
export const memberSearchDebounceMs = 150;

export type MemberSearchStatus = "idle" | "loading" | "ready" | "error";

export interface MemberPicker {
  query: string;
  setQuery: (value: string) => void;
  status: MemberSearchStatus;
  /** Search results with the excluded IDs and the current selection removed. */
  results: DMCandidate[];
  selected: DMCandidate[];
  select: (candidate: DMCandidate) => void;
  remove: (userId: string) => void;
  /** Re-runs the current query; used by the error state's retry control. */
  retry: () => void;
  atCapacity: boolean;
}

export interface MemberPickerOptions {
  /**
   * The conversation-scoped search this picker runs.
   *
   * Passed in rather than chosen here, so the hook never learns what a channel
   * or a group is. Its identity must be stable for a given target — the effect
   * below re-runs when it changes, which is exactly what should happen when the
   * conversation changes.
   */
  search: (query: string, signal: AbortSignal) => Promise<DMCandidate[]>;
  /** Locally-known people to hide from results. Never the eligibility rule. */
  excludedUserIds: readonly string[];
  /** Server's per-request batch cap. Selection stops here rather than dropping. */
  maxSelection: number;
}

export function useMemberPicker({
  search,
  excludedUserIds,
  maxSelection,
}: MemberPickerOptions): MemberPicker {
  const [query, setQueryState] = useState("");
  const [candidates, setCandidates] = useState<DMCandidate[]>([]);
  const [status, setStatus] = useState<MemberSearchStatus>("idle");
  const [selected, setSelected] = useState<DMCandidate[]>([]);
  const [attempt, setAttempt] = useState(0);

  const normalizedQuery = query.trim();

  useEffect(() => {
    if (normalizedQuery.length < memberSearchMinLength) return;

    const controller = new AbortController();
    // `active` and the abort signal guard different things: the signal stops the
    // network work, this stops a response that already resolved from writing
    // state after the effect was torn down.
    let active = true;

    const timer = window.setTimeout(() => {
      void search(normalizedQuery, controller.signal).then(
        (results) => {
          if (!active) return;
          setCandidates(results);
          setStatus("ready");
        },
        () => {
          if (!active) return;
          setStatus("error");
        },
      );
    }, memberSearchDebounceMs);

    return () => {
      active = false;
      window.clearTimeout(timer);
      controller.abort();
    };
  }, [normalizedQuery, attempt, search]);

  const setQuery = useCallback((value: string) => {
    setQueryState(value);
    // Clearing on every keystroke is what prevents the previous query's results
    // from being shown under the new one while its request is still in flight.
    setCandidates([]);
    setStatus(value.trim().length >= memberSearchMinLength ? "loading" : "idle");
  }, []);

  /**
   * Adds one person to the selection.
   *
   * Deliberately not a toggle: a selected person is filtered out of `results`,
   * so the only control that could deselect them is their own chip, which calls
   * `remove`. A toggle branch here would be unreachable code standing in for a
   * second way to do something there is exactly one way to do.
   *
   * The duplicate guard stays, because it is a correctness invariant rather than
   * a UI assumption: two rapid clicks on the same row before the re-render must
   * not select the same person twice.
   */
  const select = useCallback(
    (candidate: DMCandidate) => {
      setSelected((current) => {
        if (current.some((member) => member.userId === candidate.userId)) return current;
        // At capacity the addition is refused rather than silently evicting
        // someone the user already chose.
        if (current.length >= maxSelection) return current;
        return [...current, candidate];
      });
    },
    [maxSelection],
  );

  const remove = useCallback((userId: string) => {
    setSelected((current) => current.filter((member) => member.userId !== userId));
  }, []);

  const retry = useCallback(() => {
    setStatus("loading");
    setAttempt((value) => value + 1);
  }, []);

  // Excluded people are removed from the rendered list rather than shown
  // disabled: the list is a search result, not the roster, so a name that
  // cannot be picked would just be noise the user has to read past. Whoever is
  // already selected is likewise not repeated here — they are visible as a chip,
  // which is also where they are removed from.
  // Recomputed every render rather than memoised. Both inputs are a search
  // page's worth of people — tens, not thousands — so two Sets and one filter
  // cost less than the dependency bookkeeping a memo would need to get right,
  // and getting that wrong is how a stale exclusion list starts offering
  // someone who already participates.
  const excluded = new Set(excludedUserIds);
  const chosen = new Set(selected.map((member) => member.userId));
  const results = candidates.filter(
    (candidate) => !excluded.has(candidate.userId) && !chosen.has(candidate.userId),
  );

  return {
    query,
    setQuery,
    status,
    results,
    selected,
    select,
    remove,
    retry,
    atCapacity: selected.length >= maxSelection,
  };
}
