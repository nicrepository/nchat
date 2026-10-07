import { act, renderHook } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { ApiRequestError } from "../lib/api";

const { mockSearchCategory } = vi.hoisted(() => ({ mockSearchCategory: vi.fn() }));

vi.mock("./searchApi", async () => {
  const actual = await vi.importActual<typeof import("./searchApi")>("./searchApi");
  return { ...actual, searchCategory: (...args: unknown[]) => mockSearchCategory(...args) };
});

import { linkResult, messageResult, resultPage, userResult } from "./searchFixtures";
import type { SearchCategory, SearchResultPage } from "./searchTypes";
import {
  OVERVIEW_LIMIT,
  PAGE_LIMIT,
  useGlobalSearch,
  type RestoredSearch,
} from "./useGlobalSearch";

type Call = [SearchCategory, string, { limit: number; cursor?: string; signal: AbortSignal }];

function calls(): Call[] {
  return mockSearchCategory.mock.calls as Call[];
}

function deferred<T>() {
  let resolve!: (value: T) => void;
  let reject!: (error: unknown) => void;
  const promise = new Promise<T>((res, rej) => {
    resolve = res;
    reject = rej;
  });
  return { promise, resolve, reject };
}

async function flush() {
  await act(async () => {
    await Promise.resolve();
    await Promise.resolve();
  });
}

function setup(restored?: RestoredSearch) {
  const hook = renderHook(() => useGlobalSearch(restored));
  const commit = async (query: string) => {
    act(() => hook.result.current.setQuery(query));
    await act(async () => {
      vi.advanceTimersByTime(300);
    });
    await flush();
  };
  return { ...hook, commit };
}

beforeEach(() => {
  vi.useFakeTimers();
  mockSearchCategory.mockReset().mockResolvedValue(resultPage([]));
});

afterEach(() => {
  vi.useRealTimers();
});

describe("useGlobalSearch", () => {
  it("starts on Tudo and never requests an empty query", async () => {
    const { result, commit } = setup();
    expect(result.current.state.activeTab).toBe("all");
    await commit("   ");
    expect(mockSearchCategory).not.toHaveBeenCalled();
  });

  it("debounces typing, then loads every overview section with a small limit", async () => {
    const { result, commit } = setup();
    act(() => result.current.setQuery("ba"));
    act(() => vi.advanceTimersByTime(100));
    await commit("backup");

    expect(calls().map(([category]) => category)).toEqual([
      "messages",
      "users",
      "channels",
      "groups",
      "files",
      "links",
    ]);
    expect(
      calls().every(([, query, options]) => query === "backup" && options.limit === OVERVIEW_LIMIT),
    ).toBe(true);
    expect(result.current.state.overview.messages.status).toBe("ready");
  });

  it("loads a tab's own list on first visit only, and keeps the overview", async () => {
    const { result, commit } = setup();
    await commit("backup");
    mockSearchCategory.mockClear();

    act(() => result.current.setActiveTab("groups"));
    await flush();
    expect(calls()).toHaveLength(1);
    expect(calls()[0][0]).toBe("groups");
    expect(calls()[0][2].limit).toBe(PAGE_LIMIT);

    act(() => result.current.setActiveTab("all"));
    act(() => result.current.setActiveTab("groups"));
    await flush();
    expect(calls()).toHaveLength(1);
  });

  it("never lets a late answer for an old query replace the new one", async () => {
    const late = deferred<SearchResultPage<unknown>>();
    mockSearchCategory.mockImplementation((category: SearchCategory, query: string) =>
      category === "messages" && query === "bac"
        ? late.promise
        : Promise.resolve(resultPage([messageResult({ id: `for-${query}` })])),
    );
    const { result, commit } = setup();
    await commit("bac");
    const firstSignal = calls()[0][2].signal;
    await commit("backup");

    expect(firstSignal.aborted).toBe(true);
    late.resolve(resultPage([messageResult({ id: "stale" })]));
    await flush();

    expect(result.current.state.activeQuery).toBe("backup");
    expect(result.current.state.overview.messages.items.map((m) => m.id)).toEqual(["for-backup"]);
  });

  it("Links: a late page of query A never lands on query B, in the tab or in Tudo", async () => {
    const late = deferred<SearchResultPage<unknown>>();
    mockSearchCategory.mockImplementation((category: SearchCategory, query: string) =>
      category === "links" && query === "docs"
        ? late.promise
        : Promise.resolve(resultPage([linkResult({ id: `for-${query}` })])),
    );
    const { result, commit } = setup({ query: "", tab: "links" });
    await commit("docs");
    const [first] = calls().filter(([category]) => category === "links");
    expect(first[2].limit).toBe(PAGE_LIMIT);
    await commit("docs.example.com");

    expect(first[2].signal.aborted).toBe(true);
    late.resolve(resultPage([linkResult({ id: "stale" })]));
    await flush();
    expect(result.current.state.tabs.links.items.map((item) => item.id)).toEqual([
      "for-docs.example.com",
    ]);

    act(() => result.current.setActiveTab("all"));
    await flush();
    expect(result.current.state.overview.links.items.map((item) => item.id)).toEqual([
      "for-docs.example.com",
    ]);
  });

  it("pages a tab by cursor, skipping rows an earlier page already showed", async () => {
    mockSearchCategory.mockImplementation(
      (_c: SearchCategory, _q: string, options: { cursor?: string }) =>
        Promise.resolve(
          options.cursor
            ? resultPage([userResult({ id: "u2" }), userResult({ id: "u3" })])
            : resultPage([userResult({ id: "u1" }), userResult({ id: "u2" })], "c1"),
        ),
    );
    const { result, commit } = setup({ query: "", tab: "users" });
    await commit("ana");
    act(() => result.current.loadMore("users"));
    await flush();

    expect(calls().at(-1)?.[2].cursor).toBe("c1");
    expect(result.current.state.tabs.users.items.map((u) => u.id)).toEqual(["u1", "u2", "u3"]);
    expect(result.current.state.tabs.users.hasMore).toBe(false);

    mockSearchCategory.mockClear();
    act(() => result.current.loadMore("users"));
    expect(mockSearchCategory).not.toHaveBeenCalled();
  });

  it("drops a load-more page that answers after the query changed", async () => {
    const more = deferred<SearchResultPage<unknown>>();
    mockSearchCategory.mockImplementation(
      (_c: SearchCategory, query: string, options: { cursor?: string }) => {
        if (options.cursor) return more.promise;
        return Promise.resolve(
          resultPage([userResult({ id: `${query}-1` })], query === "ana" ? "c1" : null),
        );
      },
    );
    const { result, commit } = setup({ query: "", tab: "users" });
    await commit("ana");
    act(() => result.current.loadMore("users"));
    await commit("bia");

    more.resolve(resultPage([userResult({ id: "ana-2" })]));
    await flush();
    expect(result.current.state.tabs.users.items.map((u) => u.id)).toEqual(["bia-1"]);
  });

  // ── The debounce window (Code Quality Review, #900) ─────────────────────────
  // Between the keystroke that makes the field differ from the searched query
  // and the commit 300 ms later, nothing from the old query may stay in state
  // or land in it.

  it("drops the old results the moment the field diverges, without requesting yet", async () => {
    mockSearchCategory.mockResolvedValue(resultPage([userResult({ id: "ana-1" })]));
    const { result, commit } = setup({ query: "", tab: "users" });
    await commit("ana");
    mockSearchCategory.mockClear();

    act(() => result.current.setQuery("bia"));

    expect(result.current.state.typing).toBe(true);
    expect(result.current.state.tabs.users.items).toEqual([]);
    expect(mockSearchCategory).not.toHaveBeenCalled();
  });

  it("aborts and ignores a first page of the old query that answers during the window", async () => {
    const late = deferred<SearchResultPage<unknown>>();
    mockSearchCategory.mockReturnValue(late.promise);
    const { result, commit } = setup({ query: "", tab: "users" });
    await commit("ana");
    const signal = calls()[0][2].signal;

    act(() => result.current.setQuery("bia"));
    expect(signal.aborted).toBe(true);
    late.resolve(resultPage([userResult({ id: "ana-1" })]));
    await flush();
    expect(result.current.state.tabs.users.items).toEqual([]);

    mockSearchCategory.mockResolvedValue(resultPage([userResult({ id: "bia-1" })]));
    await commit("bia");
    expect(result.current.state.tabs.users.items.map((u) => u.id)).toEqual(["bia-1"]);
  });

  it("never appends an old load-more page, whether it lands during the window or after", async () => {
    const more = deferred<SearchResultPage<unknown>>();
    mockSearchCategory.mockImplementation(
      (_c: SearchCategory, query: string, options: { cursor?: string }) =>
        options.cursor
          ? more.promise
          : Promise.resolve(resultPage([userResult({ id: `${query}-1` })], "c1")),
    );
    const { result, commit } = setup({ query: "", tab: "users" });
    await commit("ana");
    act(() => result.current.loadMore("users"));
    const moreSignal = calls().at(-1)![2].signal;

    act(() => result.current.setQuery("bia"));
    expect(moreSignal.aborted).toBe(true);
    more.resolve(resultPage([userResult({ id: "ana-2" })]));
    await flush();
    expect(result.current.state.tabs.users.items).toEqual([]);

    await commit("bia");
    expect(result.current.state.tabs.users.items.map((u) => u.id)).toEqual(["bia-1"]);
  });

  it("typing back to the searched query searches it again rather than showing nothing", async () => {
    const { result, commit } = setup({ query: "", tab: "users" });
    await commit("ana");
    mockSearchCategory.mockClear();
    act(() => result.current.setQuery("anab"));
    act(() => result.current.setQuery("ana "));
    await flush();
    expect(result.current.state.typing).toBe(false);
    expect(calls().map(([category, query]) => [category, query])).toEqual([["users", "ana"]]);
  });

  it("keeps rows on a failed load-more and reports it", async () => {
    mockSearchCategory.mockImplementation(
      (_c: SearchCategory, _q: string, options: { cursor?: string }) =>
        options.cursor
          ? Promise.reject(new ApiRequestError(503, "internal", "down"))
          : Promise.resolve(resultPage([userResult()], "c1")),
    );
    const { result, commit } = setup({ query: "", tab: "users" });
    await commit("ana");
    act(() => result.current.loadMore("users"));
    await flush();
    expect(result.current.state.tabs.users).toMatchObject({
      loadMoreError: "server_error",
      loadingMore: false,
    });
    expect(result.current.state.tabs.users.items).toHaveLength(1);
  });

  it("fails one category on its own and retries only that one", async () => {
    mockSearchCategory.mockImplementation((category: SearchCategory) =>
      category === "files"
        ? Promise.reject(new ApiRequestError(500, "internal", "x"))
        : Promise.resolve(resultPage([])),
    );
    const { result, commit } = setup();
    await commit("backup");
    expect(result.current.state.overview.files).toMatchObject({
      status: "error",
      errorKind: "server_error",
    });
    expect(result.current.state.overview.messages.status).toBe("ready");

    mockSearchCategory.mockClear().mockResolvedValue(resultPage([]));
    act(() => result.current.retry("overview", "files"));
    await flush();
    expect(calls().map(([category]) => category)).toEqual(["files"]);
    expect(result.current.state.overview.files.status).toBe("ready");
  });

  it("restores a search left for a result without waiting for the debounce", async () => {
    const { result } = setup({ query: " backup ", tab: "channels" });
    await flush();
    expect(result.current.state.query).toBe("backup");
    expect(calls()).toHaveLength(1);
    expect(calls()[0].slice(0, 2)).toEqual(["channels", "backup"]);
  });

  it("aborts what is in flight on unmount", async () => {
    mockSearchCategory.mockReturnValue(new Promise(() => {}));
    const { commit, unmount } = setup();
    await commit("backup");
    unmount();
    expect(calls().every(([, , options]) => options.signal.aborted)).toBe(true);
  });
});
