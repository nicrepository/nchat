import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { ApiRequestError } from "./api";
import { AUTH_SKIP_PREFIXES, _resetState, authenticatedFetch } from "./authClient";
import { clearTokens, getAccessToken, setTokens } from "./authSession";

const mockApiFetch = vi.fn();
vi.mock("./api", async () => {
  const actual = await vi.importActual<typeof import("./api")>("./api");
  return {
    ...actual,
    apiFetch: (...args: unknown[]) => mockApiFetch(...args),
  };
});

const mockRefresh = vi.fn();
vi.mock("../auth/authApi", () => ({
  refresh: (...args: unknown[]) => mockRefresh(...args),
}));

beforeEach(() => {
  sessionStorage.clear();
  vi.clearAllMocks();
  _resetState();
});

afterEach(() => {
  sessionStorage.clear();
});

function make401(): ApiRequestError {
  return new ApiRequestError(401, "token_expired", "Token expired");
}

function makeTokenPair(suffix = "") {
  return {
    accessToken: `new_at${suffix}`,
    tokenType: "Bearer",
    expiresIn: 900,
  };
}

describe("authenticatedFetch", () => {
  describe("Authorization header injection", () => {
    it("attaches authorization header when access token is present", async () => {
      setTokens("my_at");
      mockApiFetch.mockResolvedValue({ ok: true });

      await authenticatedFetch("/api/resource", { method: "GET" });

      expect(mockApiFetch).toHaveBeenCalledWith(
        "/api/resource",
        expect.objectContaining({
          headers: expect.objectContaining({ authorization: "Bearer my_at" }),
        }),
        // The optional response parser, absent for an ordinary JSON call and
        // forwarded unchanged when a caller needs the raw body (RF-31 preview).
        undefined,
      );
    });

    it("omits authorization header when no access token", async () => {
      mockApiFetch.mockResolvedValue({ ok: true });

      await authenticatedFetch("/api/resource", { method: "GET" });

      const [, init] = mockApiFetch.mock.calls[0] as [string, RequestInit];
      expect((init.headers as Record<string, string>)["authorization"]).toBeUndefined();
    });
  });

  describe("HeadersInit normalization", () => {
    it("handles plain object headers", async () => {
      setTokens("at");
      mockApiFetch.mockResolvedValue({});

      await authenticatedFetch("/api/res", { method: "GET", headers: { "x-custom": "val" } });

      const [, init] = mockApiFetch.mock.calls[0] as [string, RequestInit];
      const headers = init.headers as Record<string, string>;
      expect(headers["x-custom"]).toBe("val");
      expect(headers["authorization"]).toBe("Bearer at");
    });

    it("handles Headers instance", async () => {
      setTokens("at");
      mockApiFetch.mockResolvedValue({});

      await authenticatedFetch("/api/res", {
        method: "GET",
        headers: new Headers({ "x-custom": "val" }),
      });

      const [, init] = mockApiFetch.mock.calls[0] as [string, RequestInit];
      const headers = init.headers as Record<string, string>;
      expect(headers["x-custom"]).toBe("val");
      expect(headers["authorization"]).toBe("Bearer at");
    });

    it("handles tuple-array headers", async () => {
      setTokens("at");
      mockApiFetch.mockResolvedValue({});

      await authenticatedFetch("/api/res", {
        method: "GET",
        headers: [["x-custom", "val"]],
      });

      const [, init] = mockApiFetch.mock.calls[0] as [string, RequestInit];
      const headers = init.headers as Record<string, string>;
      expect(headers["x-custom"]).toBe("val");
      expect(headers["authorization"]).toBe("Bearer at");
    });

    it("does not mutate a caller-provided Headers object", async () => {
      setTokens("at");
      mockApiFetch.mockResolvedValue({});
      const callerHeaders = new Headers({ "x-custom": "val" });

      await authenticatedFetch("/api/res", { method: "GET", headers: callerHeaders });

      expect(callerHeaders.has("authorization")).toBe(false);
    });
  });

  it("returns result directly on non-401 success", async () => {
    setTokens("at");
    mockApiFetch.mockResolvedValue({ data: "hello" });

    const result = await authenticatedFetch<{ data: string }>("/api/data", { method: "GET" });

    expect(result).toEqual({ data: "hello" });
    expect(mockRefresh).not.toHaveBeenCalled();
  });

  it("re-throws non-401 ApiRequestError without attempting refresh", async () => {
    setTokens("at");
    mockApiFetch.mockRejectedValue(new ApiRequestError(500, "server_error", "oops"));

    await expect(authenticatedFetch("/api/data", { method: "GET" })).rejects.toMatchObject({
      status: 500,
      code: "server_error",
    });

    expect(mockRefresh).not.toHaveBeenCalled();
  });

  describe("on 401 from non-auth endpoint", () => {
    it("triggers refresh and retries original request once on success", async () => {
      setTokens("expired_at");
      mockApiFetch.mockRejectedValueOnce(make401()).mockResolvedValueOnce({ data: "ok" });
      mockRefresh.mockResolvedValue(makeTokenPair());

      const result = await authenticatedFetch<{ data: string }>("/api/resource", {
        method: "GET",
      });

      expect(result).toEqual({ data: "ok" });
      expect(mockRefresh).toHaveBeenCalledTimes(1);
      expect(mockApiFetch).toHaveBeenCalledTimes(2);
    });

    it("stores new access token after successful refresh", async () => {
      setTokens("expired_at");
      mockApiFetch.mockRejectedValueOnce(make401()).mockResolvedValueOnce({});
      mockRefresh.mockResolvedValue(makeTokenPair());

      await authenticatedFetch("/api/resource", { method: "GET" });

      expect(getAccessToken()).toBe("new_at");
    });

    it("retries with updated authorization header after successful refresh", async () => {
      setTokens("expired_at");
      mockApiFetch.mockRejectedValueOnce(make401()).mockResolvedValueOnce({});
      mockRefresh.mockResolvedValue(makeTokenPair());

      await authenticatedFetch("/api/resource", { method: "GET" });

      const [, retryInit] = mockApiFetch.mock.calls[1] as [string, RequestInit];
      expect((retryInit.headers as Record<string, string>)["authorization"]).toBe("Bearer new_at");
    });

    it("clears access token and re-throws original 401 on refresh failure", async () => {
      setTokens("expired_at");
      mockApiFetch.mockRejectedValue(make401());
      mockRefresh.mockRejectedValue(new ApiRequestError(401, "invalid_refresh_token", "expired"));

      await expect(authenticatedFetch("/api/resource", { method: "GET" })).rejects.toMatchObject({
        status: 401,
        code: "token_expired",
      });

      expect(getAccessToken()).toBeNull();
    });

    it("does not retry original request after refresh failure", async () => {
      setTokens("expired_at");
      mockApiFetch.mockRejectedValue(make401());
      mockRefresh.mockRejectedValue(new Error("refresh failed"));

      await expect(authenticatedFetch("/api/resource", { method: "GET" })).rejects.toBeTruthy();

      expect(mockApiFetch).toHaveBeenCalledTimes(1);
    });

    it("retry returning 401 after successful refresh does not trigger a second refresh", async () => {
      setTokens("expired_at");
      mockApiFetch
        .mockRejectedValueOnce(make401()) // initial
        .mockRejectedValueOnce(make401()); // retry also returns 401
      mockRefresh.mockResolvedValue(makeTokenPair());

      await expect(authenticatedFetch("/api/resource", { method: "GET" })).rejects.toMatchObject({
        status: 401,
      });

      expect(mockRefresh).toHaveBeenCalledTimes(1);
      expect(mockApiFetch).toHaveBeenCalledTimes(2);
    });

    it("clears access token and rethrows 401 when refresh fails (simulates missing cookie)", async () => {
      sessionStorage.setItem("nchat_at", "expired_at");
      mockApiFetch.mockRejectedValue(make401());
      mockRefresh.mockRejectedValue(new ApiRequestError(401, "invalid_refresh_token", "no cookie"));

      await expect(authenticatedFetch("/api/resource", { method: "GET" })).rejects.toMatchObject({
        status: 401,
        code: "token_expired",
      });

      expect(getAccessToken()).toBeNull();
    });

    it("concurrent 401s trigger exactly one refresh call", async () => {
      setTokens("expired_at");

      let resolveRefresh!: (value: ReturnType<typeof makeTokenPair>) => void;
      const refreshPromise = new Promise<ReturnType<typeof makeTokenPair>>(
        (res) => (resolveRefresh = res),
      );

      mockApiFetch.mockRejectedValue(make401());
      mockRefresh.mockReturnValue(refreshPromise);

      const p1 = authenticatedFetch("/api/r1", { method: "GET" });
      const p2 = authenticatedFetch("/api/r2", { method: "GET" });

      resolveRefresh(makeTokenPair());

      await Promise.allSettled([p1, p2]);

      expect(mockRefresh).toHaveBeenCalledTimes(1);
    });

    it("concurrent 401s with no access token trigger exactly one refresh call", async () => {
      let resolveRefresh!: (value: ReturnType<typeof makeTokenPair>) => void;
      const refreshPromise = new Promise<ReturnType<typeof makeTokenPair>>(
        (res) => (resolveRefresh = res),
      );

      mockApiFetch.mockRejectedValue(make401());
      mockRefresh.mockReturnValue(refreshPromise);

      const p1 = authenticatedFetch("/api/r1", { method: "GET" });
      const p2 = authenticatedFetch("/api/r2", { method: "GET" });

      await Promise.resolve();

      expect(mockRefresh).toHaveBeenCalledTimes(1);

      resolveRefresh(makeTokenPair());

      await Promise.allSettled([p1, p2]);
    });

    it("concurrent waiters on the same refresh both retry once with the new access token", async () => {
      setTokens("expired_at");
      mockApiFetch
        .mockRejectedValueOnce(make401()) // p1 initial
        .mockRejectedValueOnce(make401()) // p2 initial
        .mockResolvedValueOnce({ data: "p1_ok" }) // p1 retry
        .mockResolvedValueOnce({ data: "p2_ok" }); // p2 retry (concurrent waiter case)

      let resolveRefresh!: (value: ReturnType<typeof makeTokenPair>) => void;
      mockRefresh.mockReturnValue(
        new Promise<ReturnType<typeof makeTokenPair>>((res) => (resolveRefresh = res)),
      );

      const p1 = authenticatedFetch<{ data: string }>("/api/r1", { method: "GET" });
      const p2 = authenticatedFetch<{ data: string }>("/api/r2", { method: "GET" });

      await Promise.resolve();

      resolveRefresh(makeTokenPair());

      const [r1, r2] = await Promise.all([p1, p2]);
      expect(r1).toEqual({ data: "p1_ok" });
      expect(r2).toEqual({ data: "p2_ok" });
      expect(mockRefresh).toHaveBeenCalledTimes(1);
    });

    describe("late-arrival guard (access token changed while request was in flight)", () => {
      it("same-generation late 401 retries once with the rotated token, no second refresh", async () => {
        // Step 1: run a complete refresh cycle to establish the rotation record.
        setTokens("expired_at");
        mockApiFetch.mockRejectedValueOnce(make401()).mockResolvedValueOnce({});
        mockRefresh.mockResolvedValueOnce(makeTokenPair()); // → { new_at }
        await authenticatedFetch("/api/setup", { method: "GET" });
        // lastAppliedRefreshRotation = { fromAT: "expired_at", toAT: "new_at" }

        // Step 2: simulate a request that was started before the rotation committed.
        setTokens("expired_at");
        mockApiFetch.mockReset();
        mockRefresh.mockReset();

        mockApiFetch
          .mockImplementationOnce(async () => {
            setTokens("new_at"); // same rotation already recorded
            throw make401();
          })
          .mockResolvedValueOnce({ data: "retried" });

        const result = await authenticatedFetch<{ data: string }>("/api/resource", {
          method: "GET",
        });

        expect(result).toEqual({ data: "retried" });
        expect(mockRefresh).not.toHaveBeenCalled();
        const [, retryInit] = mockApiFetch.mock.calls[1] as [string, RequestInit];
        expect((retryInit.headers as Record<string, string>)["authorization"]).toBe(
          "Bearer new_at",
        );
      });

      it("access token changed due to a newer login does not retry under the new session", async () => {
        setTokens("expired_at");
        mockApiFetch.mockImplementationOnce(async () => {
          setTokens("new_login_at");
          throw make401();
        });

        await expect(authenticatedFetch("/api/resource", { method: "GET" })).rejects.toMatchObject({
          status: 401,
        });

        expect(mockRefresh).not.toHaveBeenCalled();
        expect(getAccessToken()).toBe("new_login_at");
      });

      it("access token cleared due to logout does not retry", async () => {
        setTokens("expired_at");
        mockApiFetch.mockImplementationOnce(async () => {
          clearTokens(); // logout while request was in flight
          throw make401();
        });

        await expect(authenticatedFetch("/api/resource", { method: "GET" })).rejects.toMatchObject({
          status: 401,
        });

        expect(mockRefresh).not.toHaveBeenCalled();
        expect(getAccessToken()).toBeNull();
      });

      it("sequential server-side 401 on already-refreshed token triggers its own refresh, no cross-request interference", async () => {
        setTokens("expired_at");
        mockApiFetch.mockRejectedValueOnce(make401()).mockResolvedValueOnce({ data: "first_ok" });
        mockRefresh.mockResolvedValueOnce(makeTokenPair());
        await authenticatedFetch("/api/r1", { method: "GET" });
        expect(getAccessToken()).toBe("new_at");

        mockApiFetch.mockReset();
        mockRefresh.mockReset();

        mockApiFetch
          .mockImplementationOnce(async () => {
            throw make401();
          })
          .mockResolvedValueOnce({ data: "second_ok" });
        mockRefresh.mockResolvedValueOnce({
          accessToken: "newest_at",
          tokenType: "Bearer",
          expiresIn: 900,
        });

        const result = await authenticatedFetch<{ data: string }>("/api/r2", { method: "GET" });

        expect(result).toEqual({ data: "second_ok" });
        expect(mockRefresh).toHaveBeenCalledTimes(1);
        expect(getAccessToken()).toBe("newest_at");
      });
    });

    describe("session-binding guard (access token changed while refresh was in flight)", () => {
      it("stale refresh success after clearTokens does not restore tokens", async () => {
        setTokens("expired_at");
        mockApiFetch.mockRejectedValue(make401());

        let resolveRefresh!: (value: ReturnType<typeof makeTokenPair>) => void;
        mockRefresh.mockReturnValue(
          new Promise<ReturnType<typeof makeTokenPair>>((res) => (resolveRefresh = res)),
        );

        const p = authenticatedFetch("/api/resource", { method: "GET" });

        await Promise.resolve();

        clearTokens(); // simulate logout while refresh is in flight
        resolveRefresh(makeTokenPair()); // stale refresh "succeeds"

        await expect(p).rejects.toMatchObject({ status: 401 });
        expect(getAccessToken()).toBeNull();
      });

      it("stale refresh success after newer setTokens does not overwrite newer tokens and rethrows original 401", async () => {
        setTokens("expired_at");
        mockApiFetch.mockRejectedValue(make401());

        let resolveRefresh!: (value: ReturnType<typeof makeTokenPair>) => void;
        mockRefresh.mockReturnValue(
          new Promise<ReturnType<typeof makeTokenPair>>((res) => (resolveRefresh = res)),
        );

        const p = authenticatedFetch("/api/resource", { method: "GET" });

        await Promise.resolve();

        setTokens("newer_at");
        resolveRefresh(makeTokenPair()); // stale refresh gives new_at, but session already changed

        await expect(p).rejects.toMatchObject({ status: 401 });
        expect(getAccessToken()).toBe("newer_at");
      });

      it("stale refresh failure after newer setTokens does not clear newer tokens", async () => {
        setTokens("expired_at");
        mockApiFetch.mockRejectedValue(make401());

        let rejectRefresh!: (reason: unknown) => void;
        mockRefresh.mockReturnValue(
          new Promise<ReturnType<typeof makeTokenPair>>((_, rej) => (rejectRefresh = rej)),
        );

        const p = authenticatedFetch("/api/resource", { method: "GET" });

        await Promise.resolve();

        setTokens("newer_at");
        rejectRefresh(new Error("refresh failed"));

        await expect(p).rejects.toBeTruthy();
        expect(getAccessToken()).toBe("newer_at");
      });
    });

    describe("cross-session guard (different access token does not share in-flight refresh)", () => {
      it("newer session 401 with a different access token starts its own refresh call", async () => {
        setTokens("old_at");

        let resolveOldRefresh!: (value: ReturnType<typeof makeTokenPair>) => void;
        let resolveNewRefresh!: (value: ReturnType<typeof makeTokenPair>) => void;
        mockRefresh
          .mockReturnValueOnce(
            new Promise<ReturnType<typeof makeTokenPair>>((res) => (resolveOldRefresh = res)),
          )
          .mockReturnValueOnce(
            new Promise<ReturnType<typeof makeTokenPair>>((res) => (resolveNewRefresh = res)),
          );

        mockApiFetch
          .mockRejectedValueOnce(make401()) // old session initial
          .mockRejectedValueOnce(make401()) // new session initial
          .mockResolvedValueOnce({ data: "new_ok" }); // new session retry

        const oldRequest = authenticatedFetch("/api/r1", { method: "GET" });

        await Promise.resolve();

        // New login: new session with a different access token.
        setTokens("new_at");

        const newRequest = authenticatedFetch<{ data: string }>("/api/r2", { method: "GET" });

        await Promise.resolve();

        // Two separate refresh calls (one per session generation keyed by expired AT).
        expect(mockRefresh).toHaveBeenCalledTimes(2);

        resolveOldRefresh({
          accessToken: "old_refreshed_at",
          tokenType: "Bearer",
          expiresIn: 900,
        });

        resolveNewRefresh({
          accessToken: "newest_at",
          tokenType: "Bearer",
          expiresIn: 900,
        });

        await expect(oldRequest).rejects.toMatchObject({ status: 401 });
        const newResult = await newRequest;
        expect(newResult).toEqual({ data: "new_ok" });

        expect(getAccessToken()).toBe("newest_at");
      });
    });
  });

  describe("auth endpoint exclusion", () => {
    it.each(AUTH_SKIP_PREFIXES.map((p) => [p] as const))(
      "does not trigger refresh for auth path %s",
      async (path) => {
        mockApiFetch.mockRejectedValue(make401());

        await expect(
          authenticatedFetch(path, { method: "POST", body: "{}" }),
        ).rejects.toMatchObject({ status: 401 });

        expect(mockRefresh).not.toHaveBeenCalled();
      },
    );

    it("does not skip refresh for URL with auth path only in query string", async () => {
      setTokens("expired_at");
      mockApiFetch.mockRejectedValueOnce(make401()).mockResolvedValueOnce({ data: "ok" });
      mockRefresh.mockResolvedValue(makeTokenPair());

      await authenticatedFetch("/api/search?next=/api/auth/login", { method: "GET" });

      expect(mockRefresh).toHaveBeenCalledTimes(1);
    });

    it("does not skip refresh for path that merely starts with auth prefix without '/' boundary", async () => {
      setTokens("expired_at");
      mockApiFetch.mockRejectedValueOnce(make401()).mockResolvedValueOnce({ data: "ok" });
      mockRefresh.mockResolvedValue(makeTokenPair());

      await authenticatedFetch("/api/auth/loginExtra", { method: "GET" });

      expect(mockRefresh).toHaveBeenCalledTimes(1);
    });
  });
});
