/**
 * Staleness tests for the admin users query (issue #425).
 *
 * These exercise observable behaviour only — what ends up in `state`,
 * `nextCursor`, `hasMore`, `loadingMore` and `loadMoreError`. Nothing here
 * knows how the hook distinguishes a stale response from a live one.
 */

import { act, cleanup, renderHook, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { ApiRequestError } from "../lib/api";
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

// Scope keys are literals the test owns. Nothing here reads or writes shared
// auth state, so cases cannot influence one another and order does not matter.
const SCOPE_A = "session-a:workspace-a:user-a";
const SCOPE_B = "session-b:workspace-b:user-b";

beforeEach(() => {
  mockListAdminUsers.mockReset();
});

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
});

/** Renders the hook with a first page already loaded and a next page available. */
async function renderWithFirstPage(cursor: string | null = "cursor-1") {
  mockListAdminUsers.mockResolvedValueOnce(pageOf(FIRST, cursor));
  const view = renderHook(({ scope }: { scope: string | null }) => useAdminUsers(scope), {
    initialProps: { scope: SCOPE_A as string | null },
  });
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
    const { result } = renderHook(() => useAdminUsers(SCOPE_A));

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

    // Wait on the appended page, not on the error clearing: loadMore clears the
    // error the moment it starts, so waiting on that would resolve before the
    // request finished and race the append.
    await waitFor(() => {
      expect(result.current.state).toEqual({ kind: "success", users: [...FIRST, user("c1")] });
    });
    expect(result.current.loadMoreError).toBeNull();
    expect(result.current.loadingMore).toBe(false);
    expect(result.current.hasMore).toBe(false);
  });
});

describe("useAdminUsers — unmount", () => {
  // The hook aborts on unmount; without a test, a regression in that cleanup
  // would pass unnoticed.
  it("applies nothing after unmounting with both requests pending", async () => {
    const slowFirst = deferred<ReturnType<typeof pageOf>>();
    mockListAdminUsers.mockReturnValueOnce(slowFirst.promise);
    const { result, unmount } = renderHook(() => useAdminUsers(SCOPE_A));

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

describe("useAdminUsers — session scope", () => {
  /** Renders with a scope the test can change, like the page does on re-login. */
  function renderScoped(scope: string | null) {
    return renderHook(({ s }: { s: string | null }) => useAdminUsers(s), {
      initialProps: { s: scope },
    });
  }

  // The listing is administrative PII scoped to a workspace. A response issued
  // under the previous scope must never land in the table after the identity
  // behind it has changed.
  it("discards a page issued before the scope changed", async () => {
    const slowFirst = deferred<ReturnType<typeof pageOf>>();
    mockListAdminUsers.mockReturnValueOnce(slowFirst.promise);
    const { result, rerender } = renderScoped(SCOPE_A);

    // Scope A's request is in flight when the session becomes somebody else's.
    mockListAdminUsers.mockResolvedValueOnce(pageOf(RELOADED, null));
    act(() => rerender({ s: SCOPE_B }));

    // Scope A's page arrives late.
    await act(async () => {
      slowFirst.resolve(pageOf(FIRST, "cursor-1"));
      await slowFirst.promise;
    });

    await waitFor(() => {
      expect(result.current.state).toEqual({ kind: "success", users: RELOADED });
    });
  });

  it("discards a pending loadMore when the scope changes", async () => {
    const stale = deferred<ReturnType<typeof pageOf>>();
    mockListAdminUsers.mockResolvedValueOnce(pageOf(FIRST, "cursor-1"));
    const { result, rerender } = renderScoped(SCOPE_A);
    await waitFor(() => expect(result.current.state.kind).toBe("success"));

    mockListAdminUsers.mockReturnValueOnce(stale.promise);
    act(() => result.current.loadMore());

    mockListAdminUsers.mockResolvedValueOnce(pageOf(RELOADED, null));
    act(() => rerender({ s: SCOPE_B }));

    await act(async () => {
      stale.resolve(pageOf(STALE_SECOND, "cursor-stale"));
      await stale.promise;
    });

    await waitFor(() => {
      expect(result.current.state).toEqual({ kind: "success", users: RELOADED });
    });
    expect(result.current.loadMoreError).toBeNull();
  });

  // The data is cleared in the same commit as the scope change, not a frame
  // later, so the previous session's rows are never rendered under the new one.
  it("clears the table immediately when the scope changes", async () => {
    mockListAdminUsers.mockResolvedValueOnce(pageOf(FIRST, "cursor-1"));
    const { result, rerender } = renderScoped(SCOPE_A);
    await waitFor(() => expect(result.current.state.kind).toBe("success"));

    mockListAdminUsers.mockReturnValueOnce(new Promise(() => {}));
    act(() => rerender({ s: SCOPE_B }));

    expect(result.current.state).toEqual({ kind: "loading" });
    expect(result.current.hasMore).toBe(false);
    expect(result.current.loadMoreError).toBeNull();
    expect(result.current.loadingMore).toBe(false);
  });

  // A null scope is a logged-out page: clear everything and ask for nothing.
  it("clears the table and issues no request when the scope becomes null", async () => {
    mockListAdminUsers.mockResolvedValueOnce(pageOf(FIRST, "cursor-1"));
    const { result, rerender } = renderScoped(SCOPE_A);
    await waitFor(() => expect(result.current.state.kind).toBe("success"));
    const callsBefore = mockListAdminUsers.mock.calls.length;

    act(() => rerender({ s: null }));

    expect(result.current.state).toEqual({ kind: "loading" });
    expect(result.current.hasMore).toBe(false);
    expect(mockListAdminUsers.mock.calls.length).toBe(callsBefore);
  });

  it("fetches the first page when the scope goes from null to a session", async () => {
    const { result, rerender } = renderScoped(null);
    expect(mockListAdminUsers).not.toHaveBeenCalled();

    mockListAdminUsers.mockResolvedValueOnce(pageOf(FIRST, null));
    act(() => rerender({ s: SCOPE_A }));

    await waitFor(() => {
      expect(result.current.state).toEqual({ kind: "success", users: FIRST });
    });
  });

  // Re-rendering with the same scope is not an identity change: it must not
  // refetch or blank the table. This is what a silent token refresh looks like.
  it("does not restart the query when the scope is unchanged", async () => {
    mockListAdminUsers.mockResolvedValueOnce(pageOf(FIRST, "cursor-1"));
    const { result, rerender } = renderScoped(SCOPE_A);
    await waitFor(() => expect(result.current.state.kind).toBe("success"));
    const callsBefore = mockListAdminUsers.mock.calls.length;

    act(() => rerender({ s: SCOPE_A }));

    expect(result.current.state).toEqual({ kind: "success", users: FIRST });
    expect(result.current.hasMore).toBe(true);
    expect(mockListAdminUsers.mock.calls.length).toBe(callsBefore);
  });
});
