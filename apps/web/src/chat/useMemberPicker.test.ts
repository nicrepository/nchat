import { act, renderHook } from "@testing-library/react";
import { afterEach, beforeEach, expect, expectTypeOf, it, vi } from "vitest";

import { ApiRequestError } from "../lib/api";
import type { DMCandidate } from "./chatTypes";
import {
  memberSearchDebounceMs,
  type MemberPickerOptions,
  type MemberSearchOptions,
  useMemberPicker,
  useMemberSearch,
} from "./useMemberPicker";

// Debounce, cancellation and stale-response handling are proved through the
// dialogs that use the hooks. These cover the contract issue #1023 added: the
// search layer on its own, and what each layer filters.

const people: DMCandidate[] = [
  { userId: "me", displayName: "Eu" },
  { userId: "u-2", displayName: "Joana" },
  { userId: "u-3", displayName: "Marcos" },
];

beforeEach(() => vi.useFakeTimers());
afterEach(() => vi.useRealTimers());

async function flushSearch() {
  await act(async () => {
    vi.advanceTimersByTime(memberSearchDebounceMs);
    await Promise.resolve();
  });
}

it("exposes the raw failure for the caller to map, and recovers on retry", async () => {
  const failure = new ApiRequestError(429, "rate_limited", "detail");
  const search = vi.fn().mockRejectedValueOnce(failure).mockResolvedValueOnce(people);
  const { result } = renderHook(() => useMemberSearch({ search, excludedUserIds: ["me"] }));

  act(() => result.current.setQuery("jo"));
  await flushSearch();
  expect(result.current.status).toBe("error");
  expect(result.current.error).toBe(failure);

  act(() => result.current.retry());
  expect(result.current.status).toBe("loading");
  await flushSearch();
  expect(result.current.status).toBe("ready");
  expect(result.current.results.map((person) => person.userId)).toEqual(["u-2", "u-3"]);
});

it("keeps selected people out of the picker's results but not out of the search", async () => {
  const search = vi.fn().mockResolvedValue(people);
  const { result } = renderHook(() =>
    useMemberPicker({ search, excludedUserIds: ["me"], maxSelection: 5 }),
  );

  act(() => result.current.setQuery("jo"));
  await flushSearch();
  act(() => result.current.select(people[1]));

  expect(result.current.selected).toEqual([people[1]]);
  expect(result.current.results.map((person) => person.userId)).toEqual(["u-3"]);
  expect(result.current.error).toBeNull();
});

it("does nothing while disabled and resumes the interrupted query once enabled", async () => {
  const search = vi.fn<(query: string, signal: AbortSignal) => Promise<DMCandidate[]>>();
  search.mockResolvedValue(people);
  const { result, rerender } = renderHook(
    ({ enabled }) => useMemberSearch({ search, excludedUserIds: ["me"], enabled }),
    { initialProps: { enabled: true } },
  );

  act(() => result.current.setQuery("jo"));
  rerender({ enabled: false });
  await flushSearch();
  expect(search).not.toHaveBeenCalled();
  expect(result.current.query).toBe("jo");
  expect(result.current.status).toBe("loading");

  rerender({ enabled: true });
  await flushSearch();
  expect(search).toHaveBeenCalledTimes(1);
  expect(result.current.status).toBe("ready");

  rerender({ enabled: false });
  rerender({ enabled: true });
  await flushSearch();
  expect(search).toHaveBeenCalledTimes(1);
  expect(result.current.results).toHaveLength(2);
});

// Checked by tsc (`pnpm typecheck`): only the search can be suspended.
it("offers `enabled` on the search contract only", () => {
  expectTypeOf<MemberSearchOptions>().toHaveProperty("enabled");
  expectTypeOf<MemberPickerOptions>().not.toHaveProperty("enabled");
});
