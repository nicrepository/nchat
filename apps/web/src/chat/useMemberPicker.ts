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
 * groups. Those differ between the callers, and folding them in is how a
 * shared hook turns into a component with a mode flag.
 *
 * It is two layers (issue #1023). `useMemberSearch` is the search alone; the
 * "Nova conversa" flows use it directly — Pessoa needs no selection at all, and
 * Grupo keeps its own toggle selection (dmGroupForm) so a picked person stays
 * visible in the results. `useMemberPicker` is that search plus the add/remove
 * selection the add-members dialog uses.
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

export interface MemberSearch {
  query: string;
  setQuery: (value: string) => void;
  status: MemberSearchStatus;
  /** Search results with the excluded IDs removed. */
  results: DMCandidate[];
  /**
   * What the last failed request threw, meaningful only while status is
   * "error". Handed over raw so each caller maps it to its own copy; it must
   * never be rendered as-is, because it can carry server detail.
   */
  error: unknown;
  /** Re-runs the current query; used by the error state's retry control. */
  retry: () => void;
}

export interface MemberPicker extends MemberSearch {
  /** Search results with the excluded IDs and the current selection removed. */
  results: DMCandidate[];
  selected: DMCandidate[];
  select: (candidate: DMCandidate) => void;
  remove: (userId: string) => void;
  atCapacity: boolean;
}

export interface MemberSearchOptions {
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
  /**
   * Whether this search may touch the network. Defaults to true. While false
   * no debounce starts, a pending one is cancelled and an in-flight request is
   * aborted and ignored; query, results and status are kept as they are, so a
   * search interrupted mid-flight resumes for the current query once enabled
   * again, and a settled one is not re-run.
   */
  enabled?: boolean;
}

// `enabled` stays on the search alone: no picker caller suspends its search,
// and an option the picker accepted but did not forward would be a lie.
export interface MemberPickerOptions extends Omit<MemberSearchOptions, "enabled"> {
  /** Server's per-request batch cap. Selection stops here rather than dropping. */
  maxSelection: number;
}

export function useMemberSearch({
  search,
  excludedUserIds,
  enabled = true,
}: MemberSearchOptions): MemberSearch {
  const [query, setQueryState] = useState("");
  const [candidates, setCandidates] = useState<DMCandidate[]>([]);
  const [status, setStatus] = useState<MemberSearchStatus>("idle");
  const [error, setError] = useState<unknown>(null);
  const [attempt, setAttempt] = useState(0);

  const normalizedQuery = query.trim();
  // "loading" is the one state that still owes the user an answer. Keying the
  // effect on it is what lets a disabled search resume exactly once on
  // re-enable, while a ready or failed one stays as it was.
  const awaitingResults = status === "loading";

  useEffect(() => {
    if (!enabled || !awaitingResults) return;
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
        (reason: unknown) => {
          if (!active) return;
          setError(reason);
          setStatus("error");
        },
      );
    }, memberSearchDebounceMs);

    return () => {
      active = false;
      window.clearTimeout(timer);
      controller.abort();
    };
  }, [normalizedQuery, attempt, search, enabled, awaitingResults]);

  const setQuery = useCallback((value: string) => {
    setQueryState(value);
    // Clearing on every keystroke is what prevents the previous query's results
    // from being shown under the new one while its request is still in flight.
    setCandidates([]);
    setStatus(value.trim().length >= memberSearchMinLength ? "loading" : "idle");
  }, []);

  const retry = useCallback(() => {
    setStatus("loading");
    setAttempt((value) => value + 1);
  }, []);

  // Excluded people are removed from the rendered list rather than shown
  // disabled: the list is a search result, not the roster, so a name that
  // cannot be picked would just be noise the user has to read past.
  // Recomputed every render rather than memoised: a search page is tens of
  // people, and a memo's dependency bookkeeping is how a stale exclusion list
  // starts offering someone who already participates.
  const excluded = new Set(excludedUserIds);
  const results = candidates.filter((candidate) => !excluded.has(candidate.userId));

  return { query, setQuery, status, results, error, retry };
}

export function useMemberPicker({
  search,
  excludedUserIds,
  maxSelection,
}: MemberPickerOptions): MemberPicker {
  const memberSearch = useMemberSearch({ search, excludedUserIds });
  const [selected, setSelected] = useState<DMCandidate[]>([]);

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

  // Whoever is already selected is not repeated in the results — they are
  // visible as a chip, which is also where they are removed from.
  const chosen = new Set(selected.map((member) => member.userId));
  const results = memberSearch.results.filter((candidate) => !chosen.has(candidate.userId));

  return {
    ...memberSearch,
    results,
    selected,
    select,
    remove,
    atCapacity: selected.length >= maxSelection,
  };
}
