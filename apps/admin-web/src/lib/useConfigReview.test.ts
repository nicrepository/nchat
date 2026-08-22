import { act, renderHook, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";

import { _resetCSRFToken } from "../api/client";
import type { ConfigValue } from "../api/configApi";
import { useConfigReview } from "./useConfigReview";

/**
 * The review flow's one guarantee, tested where it lives: what is confirmed is
 * exactly what was previewed, at the revision it was previewed against.
 *
 * These are hook tests rather than page tests because the page deliberately
 * locks the form while a review is open — so through the UI the draft *cannot*
 * move underneath a confirm. The guarantee must hold anyway: a reload, a second
 * tab or a future screen that does not lock must not be able to turn a reviewed
 * diff into a different write.
 */

const PLAN = {
  document: "auth.policy",
  revision: 3,
  stale: false,
  superseded: false,
  changes: [],
  dangerous: false,
  required_capability: "admin.config.manage",
  authorized: true,
  reason_required: false,
  warnings: [],
  errors: [],
  affected_services: ["auth-service"],
  apply: "runtime",
};

function jsonResponse(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "content-type": "application/json" },
  });
}

function applyResponse(revision: number) {
  return jsonResponse({
    data: {
      applied: true,
      document: "auth.policy",
      revision,
      values: {},
      plan: PLAN,
      version: {
        id: "9",
        document: "auth.policy",
        revision,
        applied_at: "2026-08-22T12:00:00Z",
        actor_user_id: "u1",
        actor_email: "admin@example.test",
        correlation_id: "req-1",
        reason: "",
        reverts_revision: 0,
        rollbackable: true,
        changes: [],
      },
    },
  });
}

/** A response the test releases when it wants it to arrive. */
function deferredResponse() {
  let release: (value: Response) => void = () => undefined;
  const promise = new Promise<Response>((resolve) => {
    release = resolve;
  });
  return { promise, release };
}

function routedFetch(handlers: Record<string, () => Response | Promise<Response>>) {
  const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    void init;
    const url = String(input);
    const match = Object.keys(handlers)
      .sort((a, b) => b.length - a.length)
      .find((route) => url.includes(route));
    if (match === undefined) throw new Error(`unstubbed request: ${url}`);
    return handlers[match]();
  });
  vi.stubGlobal("fetch", fetchMock);
  return fetchMock;
}

function bodyOf(fetchMock: ReturnType<typeof vi.fn>, fragment: string) {
  const call = fetchMock.mock.calls.find(([url]) => String(url).includes(fragment));
  return JSON.parse(String(call?.[1]?.body));
}

afterEach(() => {
  vi.unstubAllGlobals();
  _resetCSRFToken();
});

describe("useConfigReview", () => {
  // The values are copied into the snapshot, not referenced. A caller that
  // keeps mutating the object it passed — which is what a live form does —
  // must not change what gets written.
  it("confirms the values the review was opened with, not the caller's object", async () => {
    const fetchMock = routedFetch({
      "/config/preview": () => jsonResponse({ data: { plan: PLAN } }),
      "/config/apply": () => applyResponse(4),
    });
    const { result } = renderHook(() =>
      useConfigReview({ revision: 3, onApplied: () => undefined }),
    );

    const values: Record<string, ConfigValue> = { "auth.password.min_length": 16 };
    act(() => result.current.open(values));
    await waitFor(() => expect(result.current.review).not.toBeNull());

    // The caller's object moves on after the review opened.
    values["auth.password.min_length"] = 99;
    values["auth.device.max_per_user"] = 50;

    act(() => result.current.confirm(""));
    await waitFor(() => expect(result.current.feedback).not.toBeNull());

    expect(bodyOf(fetchMock, "/config/apply").changes).toEqual({
      "auth.password.min_length": 16,
    });
  });

  // The revision is frozen with the values. Confirming against a newer one
  // would let a diff reviewed under revision N be written after N moved, which
  // is exactly what optimistic locking exists to refuse.
  it("confirms against the revision the review was opened at", async () => {
    const fetchMock = routedFetch({
      "/config/preview": () => jsonResponse({ data: { plan: PLAN } }),
      "/config/apply": () => applyResponse(9),
    });
    const { result, rerender } = renderHook(
      ({ revision }) => useConfigReview({ revision, onApplied: () => undefined }),
      { initialProps: { revision: 3 } },
    );

    act(() => result.current.open({ "auth.password.min_length": 16 }));
    await waitFor(() => expect(result.current.review).not.toBeNull());

    // The document moves under the open dialog.
    rerender({ revision: 8 });

    act(() => result.current.confirm(""));
    await waitFor(() => expect(result.current.feedback).not.toBeNull());

    expect(bodyOf(fetchMock, "/config/apply").expected_revision).toBe(3);
  });

  it("sends a rollback against the snapshot's version and revision", async () => {
    const fetchMock = routedFetch({
      "/rollback/preview": () => jsonResponse({ data: { plan: PLAN } }),
      "/rollback": () => applyResponse(4),
    });
    const { result } = renderHook(() =>
      useConfigReview({ revision: 3, onApplied: () => undefined }),
    );

    act(() => result.current.openRollback("7"));
    await waitFor(() => expect(result.current.review).not.toBeNull());

    act(() => result.current.confirm("reverter"));
    await waitFor(() => expect(result.current.feedback).not.toBeNull());

    // The preview names the version, and carries no values: the server derives
    // what a rollback of it would restore.
    const previewCall = fetchMock.mock.calls.find(([url]) =>
      String(url).includes("/rollback/preview"),
    );
    expect(String(previewCall?.[0])).toBe("/api/admin/config/versions/7/rollback/preview");
    expect(JSON.parse(String(previewCall?.[1]?.body))).toEqual({ expected_revision: 3 });

    const applyCall = fetchMock.mock.calls.find(
      ([url]) => String(url).includes("/rollback") && !String(url).includes("/preview"),
    );
    expect(String(applyCall?.[0])).toBe("/api/admin/config/versions/7/rollback");
    expect(JSON.parse(String(applyCall?.[1]?.body))).toEqual({
      expected_revision: 3,
      reason: "reverter",
    });
  });

  // A slow first preview answering after a second one must not replace the
  // review the operator is looking at.
  it("keeps the newest review when an earlier preview answers late", async () => {
    const slow = deferredResponse();
    let previews = 0;
    routedFetch({
      "/config/preview": () => {
        previews += 1;
        if (previews === 1) return slow.promise;
        return jsonResponse({ data: { plan: { ...PLAN, revision: 20 } } });
      },
    });
    const { result } = renderHook(() =>
      useConfigReview({ revision: 3, onApplied: () => undefined }),
    );

    act(() => result.current.open({ "auth.password.min_length": 8 }));
    act(() => result.current.open({ "auth.password.min_length": 20 }));

    await waitFor(() => expect(result.current.review?.plan.revision).toBe(20));

    slow.release(jsonResponse({ data: { plan: { ...PLAN, revision: 99 } } }));
    await new Promise((resolve) => setTimeout(resolve, 0));

    expect(result.current.review?.plan.revision).toBe(20);
    const snapshot = result.current.review?.request;
    expect(snapshot?.kind === "apply" && snapshot.changes).toEqual({
      "auth.password.min_length": 20,
    });
  });

  // A cancelled review must stay cancelled, however late its preview answers.
  it("does not reopen a cancelled review when its preview answers late", async () => {
    const slow = deferredResponse();
    routedFetch({ "/config/preview": () => slow.promise });
    const { result } = renderHook(() =>
      useConfigReview({ revision: 3, onApplied: () => undefined }),
    );

    act(() => result.current.open({ "auth.password.min_length": 16 }));
    expect(result.current.previewing).toBe(true);
    act(() => result.current.cancel());

    slow.release(jsonResponse({ data: { plan: PLAN } }));
    await new Promise((resolve) => setTimeout(resolve, 0));

    expect(result.current.review).toBeNull();
    expect(result.current.previewing).toBe(false);
  });

  it("sends one mutation however many times confirm is called", async () => {
    const slow = deferredResponse();
    const fetchMock = routedFetch({
      "/config/preview": () => jsonResponse({ data: { plan: PLAN } }),
      "/config/apply": () => slow.promise,
    });
    const { result } = renderHook(() =>
      useConfigReview({ revision: 3, onApplied: () => undefined }),
    );

    act(() => result.current.open({ "auth.password.min_length": 16 }));
    await waitFor(() => expect(result.current.review).not.toBeNull());

    act(() => result.current.confirm(""));
    act(() => result.current.confirm(""));
    act(() => result.current.confirm(""));

    slow.release(applyResponse(4));
    await waitFor(() => expect(result.current.feedback).not.toBeNull());

    expect(
      fetchMock.mock.calls.filter(([url]) => String(url).includes("/config/apply")),
    ).toHaveLength(1);
  });

  it("does nothing when confirm is called with no open review", async () => {
    const fetchMock = routedFetch({ "/config/apply": () => applyResponse(4) });
    const { result } = renderHook(() =>
      useConfigReview({ revision: 3, onApplied: () => undefined }),
    );

    act(() => result.current.confirm(""));

    expect(fetchMock).not.toHaveBeenCalled();
    expect(result.current.applying).toBe(false);
  });

  it("reports a refused write without clearing the review", async () => {
    routedFetch({
      "/config/preview": () => jsonResponse({ data: { plan: PLAN } }),
      "/config/apply": () =>
        jsonResponse({ error: { code: "conflict", message: "conflict" } }, 409),
    });
    const applied = vi.fn();
    const { result } = renderHook(() => useConfigReview({ revision: 3, onApplied: applied }));

    act(() => result.current.open({ "auth.password.min_length": 16 }));
    await waitFor(() => expect(result.current.review).not.toBeNull());

    act(() => result.current.confirm(""));
    await waitFor(() => expect(result.current.failure).not.toBeNull());

    // The dialog stays open with its message, and nothing was reloaded as if a
    // write had happened.
    expect(result.current.review).not.toBeNull();
    expect(applied).not.toHaveBeenCalled();
  });

  it("surfaces a failed preview without opening a review", async () => {
    routedFetch({
      "/config/preview": () => jsonResponse({ error: { code: "forbidden", message: "no" } }, 403),
    });
    const { result } = renderHook(() =>
      useConfigReview({ revision: 3, onApplied: () => undefined }),
    );

    act(() => result.current.open({ "auth.password.min_length": 16 }));
    await waitFor(() => expect(result.current.failure).not.toBeNull());

    expect(result.current.review).toBeNull();
    expect(result.current.previewing).toBe(false);
  });
});
