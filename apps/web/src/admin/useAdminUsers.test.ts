/**
 * Staleness tests for the admin users query (issue #425).
 *
 * These exercise observable behaviour only — what ends up in `state`,
 * `nextCursor`, `hasMore`, `loadingMore` and `loadMoreError`. Nothing here
 * knows how the hook distinguishes a stale response from a live one.
 */

import { act, renderHook, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";

import { ApiRequestError } from "../lib/api";
import { _resetListeners, clearTokens, setTokens } from "../lib/authSession";
import type { AdminUser } from "./adminUsersApi";
import { useAdminUsers } from "./useAdminUsers";

const { mockListAdminUsers } = vi.hoisted(() => ({ mockListAdminUsers: vi.fn() }));

vi.mock("./adminUsersApi", async () => {
  const actual = await vi.importActual<typeof import("./adminUsersApi")>("./adminUsersApi");
  return {
    ADMIN_USERS_PAGE_SIZE: actual.ADMIN_USERS_PAGE_SIZE,
    classifyAdminError: actual.classifyAdminError,
    listAdminUsers: (...args: unknown[]) => mockListAdminUsers(...args),
  };
});

function user(id: string): AdminUser {
  return {
    id,
    email: `${id}@example.com`,
    displayName: id.toUpperCase(),
    status: "active",
    authSource: "manual",
    createdAt: "2024-01-01T00:00:00Z",
  };
}

function pageOf(users: AdminUser[], nextCursor: string | null = null) {
  return { users, nextCursor, hasMore: nextCursor !== null };
}

/** A promise plus the handles to settle it later. */
function deferred<T>() {
  let resolve!: (v: T) => void;
  let reject!: (e: unknown) => void;
  const promise = new Promise<T>((res, rej) => {
    resolve = res;
    reject = rej;
  });
  return { promise, resolve, reject };
}

const FIRST = [user("a1"), user("a2")];
const STALE_SECOND = [user("s1"), user("s2")];
const RELOADED = [user("b1")];

beforeEach(() => {
  mockListAdminUsers.mockReset();
  _resetListeners();
  sessionStorage.clear();
  setTokens("session-A");
});

/** Renders the hook with a first page already loaded and a next page available. */
async function renderWithFirstPage(cursor: string | null = "cursor-1") {
  mockListAdminUsers.mockResolvedValueOnce(pageOf(FIRST, cursor));
  const view = renderHook(() => useAdminUsers());
  await waitFor(() => expect(view.result.current.state.kind).toBe("success"));
  return view;
}

describe("useAdminUsers — stale page invalidation", () => {
  // The bug: loadMore is in flight, the user hits retry (or an invite triggers
  // a refresh), and the old page resolves afterwards. Without invalidation it
  // appends rows from the discarded list and moves the cursor backwards.
  it("discards a loadMore page that resolves after a reload", async () => {
    const stale = deferred<ReturnType<typeof pageOf>>();
    const { result } = await renderWithFirstPage();

    mockListAdminUsers.mockReturnValueOnce(stale.promise);
    act(() => result.current.loadMore());

    // Reload lands first and brings a different list.
    mockListAdminUsers.mockResolvedValueOnce(pageOf(RELOADED, "cursor-reloaded"));
    act(() => result.current.reload());
    await waitFor(() => {
      expect(result.current.state).toEqual({ kind: "success", users: RELOADED });
    });

    // Now the abandoned page arrives.
    await act(async () => {
      stale.resolve(pageOf(STALE_SECOND, "cursor-stale"));
      await stale.promise;
    });

    // The list is exactly the reload's, with nothing appended.
    expect(result.current.state).toEqual({ kind: "success", users: RELOADED });
    // And paging continues from the reload's position, not the stale one.
    expect(result.current.hasMore).toBe(true);
    expect(result.current.loadMoreError).toBeNull();
    expect(result.current.loadingMore).toBe(false);
  });

  it("ignores a loadMore failure that arrives after a reload", async () => {
    const stale = deferred<ReturnType<typeof pageOf>>();
    const { result } = await renderWithFirstPage();

    mockListAdminUsers.mockReturnValueOnce(stale.promise);
    act(() => result.current.loadMore());

    mockListAdminUsers.mockResolvedValueOnce(pageOf(RELOADED, null));
    act(() => result.current.reload());
    await waitFor(() => {
      expect(result.current.state).toEqual({ kind: "success", users: RELOADED });
    });

    await act(async () => {
      stale.reject(new ApiRequestError(500, "internal_error", "boom"));
      await stale.promise.catch(() => undefined);
    });

    // No error surfaces for a request nobody is waiting for, and the list and
    // paging state are untouched.
    expect(result.current.loadMoreError).toBeNull();
    expect(result.current.state).toEqual({ kind: "success", users: RELOADED });
    expect(result.current.hasMore).toBe(false);
    expect(result.current.loadingMore).toBe(false);
  });

  // The invite flow reloads on success; a page in flight at that moment must
  // not resurrect rows from before the invite.
  it("discards a loadMore page when a reload follows a completed invite", async () => {
    const stale = deferred<ReturnType<typeof pageOf>>();
    const { result } = await renderWithFirstPage();

    mockListAdminUsers.mockReturnValueOnce(stale.promise);
    act(() => result.current.loadMore());

    // This is what AdminUsersPage does after createAdminInvite resolves.
    mockListAdminUsers.mockResolvedValueOnce(pageOf(RELOADED, null));
    act(() => result.current.reload());
    await waitFor(() => expect(result.current.hasMore).toBe(false));

    await act(async () => {
      stale.resolve(pageOf(STALE_SECOND, "cursor-stale"));
      await stale.promise;
    });

    expect(result.current.state).toEqual({ kind: "success", users: RELOADED });
    expect(result.current.hasMore).toBe(false);
  });

  // A first-page response abandoned by a second reload must not win either.
  it("discards an initial page superseded by a newer reload", async () => {
    const slowFirst = deferred<ReturnType<typeof pageOf>>();
    mockListAdminUsers.mockReturnValueOnce(slowFirst.promise);
    const { result } = renderHook(() => useAdminUsers());

    mockListAdminUsers.mockResolvedValueOnce(pageOf(RELOADED, null));
    act(() => result.current.reload());
    await waitFor(() => {
      expect(result.current.state).toEqual({ kind: "success", users: RELOADED });
    });

    await act(async () => {
      slowFirst.resolve(pageOf(FIRST, "cursor-1"));
      await slowFirst.promise;
    });

    expect(result.current.state).toEqual({ kind: "success", users: RELOADED });
    expect(result.current.hasMore).toBe(false);
  });
});

describe("useAdminUsers — normal paging", () => {
  it("appends a page and advances the cursor", async () => {
    const { result } = await renderWithFirstPage();

    mockListAdminUsers.mockResolvedValueOnce(pageOf([user("c1")], null));
    act(() => result.current.loadMore());
    await waitFor(() => expect(result.current.loadingMore).toBe(false));

    expect(result.current.state).toEqual({
      kind: "success",
      users: [...FIRST, user("c1")],
    });
    expect(result.current.hasMore).toBe(false);
    // The second call carried the cursor from the first page.
    expect(mockListAdminUsers.mock.calls[1][0]).toMatchObject({ cursor: "cursor-1" });
  });

  it("does not start a second page while one is in flight", async () => {
    const pending = deferred<ReturnType<typeof pageOf>>();
    const { result } = await renderWithFirstPage();

    mockListAdminUsers.mockReturnValue(pending.promise);
    act(() => result.current.loadMore());
    act(() => result.current.loadMore());
    act(() => result.current.loadMore());

    // One initial page plus exactly one loadMore.
    expect(mockListAdminUsers).toHaveBeenCalledTimes(2);

    await act(async () => {
      pending.resolve(pageOf([user("c1")], null));
      await pending.promise;
    });
  });

  it("does nothing when there is no further page", async () => {
    const { result } = await renderWithFirstPage(null);

    act(() => result.current.loadMore());

    expect(mockListAdminUsers).toHaveBeenCalledTimes(1);
    expect(result.current.loadingMore).toBe(false);
  });

  // A live failure still surfaces, and keeps the rows already on screen.
  it("reports a current loadMore failure without dropping loaded rows", async () => {
    const { result } = await renderWithFirstPage();

    mockListAdminUsers.mockRejectedValueOnce(new ApiRequestError(429, "rate_limited", "slow down"));
    act(() => result.current.loadMore());
    await waitFor(() => expect(result.current.loadMoreError).toBe("rate-limited"));

    expect(result.current.state).toEqual({ kind: "success", users: FIRST });
    expect(result.current.loadingMore).toBe(false);
    // No automatic retry.
    expect(mockListAdminUsers).toHaveBeenCalledTimes(2);
  });

  it("clears a previous loadMore error when a retry succeeds", async () => {
    const { result } = await renderWithFirstPage();

    mockListAdminUsers.mockRejectedValueOnce(new ApiRequestError(500, "internal_error", "boom"));
    act(() => result.current.loadMore());
    await waitFor(() => expect(result.current.loadMoreError).toBe("error"));

    mockListAdminUsers.mockResolvedValueOnce(pageOf([user("c1")], null));
    act(() => result.current.loadMore());
    await waitFor(() => expect(result.current.loadMoreError).toBeNull());

    expect(result.current.state).toEqual({ kind: "success", users: [...FIRST, user("c1")] });
  });
});

describe("useAdminUsers — unmount", () => {
  // The hook aborts on unmount; without a test, a regression in that cleanup
  // would pass unnoticed.
  it("applies nothing after unmounting with both requests pending", async () => {
    const slowFirst = deferred<ReturnType<typeof pageOf>>();
    mockListAdminUsers.mockReturnValueOnce(slowFirst.promise);
    const { result, unmount } = renderHook(() => useAdminUsers());

    const before = result.current.state;
    unmount();

    // Resolving after unmount must not throw, warn, or update anything.
    await act(async () => {
      slowFirst.resolve(pageOf(FIRST, "cursor-1"));
      await slowFirst.promise;
    });

    expect(result.current.state).toEqual(before);
  });

  it("applies nothing when a pending loadMore resolves after unmount", async () => {
    const stale = deferred<ReturnType<typeof pageOf>>();
    const { result, unmount } = await renderWithFirstPage();

    mockListAdminUsers.mockReturnValueOnce(stale.promise);
    act(() => result.current.loadMore());

    const before = result.current.state;
    unmount();

    await act(async () => {
      stale.resolve(pageOf(STALE_SECOND, "cursor-stale"));
      await stale.promise;
    });

    expect(result.current.state).toEqual(before);
  });

  it("swallows a rejection that arrives after unmount", async () => {
    const stale = deferred<ReturnType<typeof pageOf>>();
    const { result, unmount } = await renderWithFirstPage();

    mockListAdminUsers.mockReturnValueOnce(stale.promise);
    act(() => result.current.loadMore());
    unmount();

    await act(async () => {
      stale.reject(new ApiRequestError(500, "internal_error", "boom"));
      await stale.promise.catch(() => undefined);
    });

    expect(result.current.loadMoreError).toBeNull();
  });
});

describe("useAdminUsers — session change", () => {
  // The listing is administrative PII scoped to a workspace. A response issued
  // under the previous session must never land in the table after the identity
  // behind that session has changed.
  it("discards a page issued before a re-login as someone else", async () => {
    const slowFirst = deferred<ReturnType<typeof pageOf>>();
    mockListAdminUsers.mockReturnValueOnce(slowFirst.promise);
    const { result } = renderHook(() => useAdminUsers());

    // Session A's request is in flight; the operator logs out and back in.
    mockListAdminUsers.mockResolvedValueOnce(pageOf(RELOADED, null));
    act(() => {
      clearTokens();
      setTokens("session-B");
    });

    // Session A's page arrives late.
    await act(async () => {
      slowFirst.resolve(pageOf(FIRST, "cursor-1"));
      await slowFirst.promise;
    });

    await waitFor(() => {
      expect(result.current.state).toEqual({ kind: "success", users: RELOADED });
    });
    // Nothing from the previous session's workspace is on screen.
    expect(result.current.state).not.toEqual({ kind: "success", users: FIRST });
  });

  it("discards a pending loadMore when the session changes", async () => {
    const stale = deferred<ReturnType<typeof pageOf>>();
    const { result } = await renderWithFirstPage();

    mockListAdminUsers.mockReturnValueOnce(stale.promise);
    act(() => result.current.loadMore());

    mockListAdminUsers.mockResolvedValueOnce(pageOf(RELOADED, null));
    act(() => {
      clearTokens();
      setTokens("session-B");
    });

    await act(async () => {
      stale.resolve(pageOf(STALE_SECOND, "cursor-stale"));
      await stale.promise;
    });

    await waitFor(() => {
      expect(result.current.state).toEqual({ kind: "success", users: RELOADED });
    });
    expect(result.current.loadMoreError).toBeNull();
  });

  // After a logout there is no session to fetch under: the table is emptied and
  // no request is issued.
  it("clears the table on logout without refetching", async () => {
    const { result } = await renderWithFirstPage();
    const callsBefore = mockListAdminUsers.mock.calls.length;

    act(() => clearTokens());

    expect(result.current.state).toEqual({ kind: "loading" });
    expect(result.current.hasMore).toBe(false);
    expect(mockListAdminUsers.mock.calls.length).toBe(callsBefore);
  });

  // Re-notifying with the same token is not an identity change.
  it("ignores an auth notification that does not change the token", async () => {
    const { result } = await renderWithFirstPage();
    const callsBefore = mockListAdminUsers.mock.calls.length;

    act(() => setTokens("session-A"));

    expect(result.current.state).toEqual({ kind: "success", users: FIRST });
    expect(mockListAdminUsers.mock.calls.length).toBe(callsBefore);
  });
});
