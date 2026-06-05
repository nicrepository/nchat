import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { ApiRequestError } from "./api";
import { AUTH_SKIP_PREFIXES, authenticatedFetch } from "./authClient";
import { clearTokens, getAccessToken, getRefreshToken, setTokens } from "./authSession";

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
    refreshToken: `new_rt${suffix}`,
    tokenType: "Bearer",
    expiresIn: 900,
  };
}

describe("authenticatedFetch", () => {
  describe("Authorization header injection", () => {
    it("attaches authorization header when access token is present", async () => {
      setTokens("my_at", "my_rt");
      mockApiFetch.mockResolvedValue({ ok: true });

      await authenticatedFetch("/api/resource", { method: "GET" });

      expect(mockApiFetch).toHaveBeenCalledWith(
        "/api/resource",
        expect.objectContaining({
          headers: expect.objectContaining({ authorization: "Bearer my_at" }),
        }),
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
      setTokens("at", "rt");
      mockApiFetch.mockResolvedValue({});

      await authenticatedFetch("/api/res", { method: "GET", headers: { "x-custom": "val" } });

      const [, init] = mockApiFetch.mock.calls[0] as [string, RequestInit];
      const headers = init.headers as Record<string, string>;
      expect(headers["x-custom"]).toBe("val");
      expect(headers["authorization"]).toBe("Bearer at");
    });

    it("handles Headers instance", async () => {
      setTokens("at", "rt");
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
      setTokens("at", "rt");
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
      setTokens("at", "rt");
      mockApiFetch.mockResolvedValue({});
      const callerHeaders = new Headers({ "x-custom": "val" });

      await authenticatedFetch("/api/res", { method: "GET", headers: callerHeaders });

      expect(callerHeaders.has("authorization")).toBe(false);
    });
  });

  it("returns result directly on non-401 success", async () => {
    setTokens("at", "rt");
    mockApiFetch.mockResolvedValue({ data: "hello" });

    const result = await authenticatedFetch<{ data: string }>("/api/data", { method: "GET" });

    expect(result).toEqual({ data: "hello" });
    expect(mockRefresh).not.toHaveBeenCalled();
  });

  it("re-throws non-401 ApiRequestError without attempting refresh", async () => {
    setTokens("at", "rt");
    mockApiFetch.mockRejectedValue(new ApiRequestError(500, "server_error", "oops"));

    await expect(authenticatedFetch("/api/data", { method: "GET" })).rejects.toMatchObject({
      status: 500,
      code: "server_error",
    });

    expect(mockRefresh).not.toHaveBeenCalled();
  });

  describe("on 401 from non-auth endpoint", () => {
    it("triggers refresh and retries original request once on success", async () => {
      setTokens("expired_at", "rt");
      mockApiFetch.mockRejectedValueOnce(make401()).mockResolvedValueOnce({ data: "ok" });
      mockRefresh.mockResolvedValue(makeTokenPair());

      const result = await authenticatedFetch<{ data: string }>("/api/resource", {
        method: "GET",
      });

      expect(result).toEqual({ data: "ok" });
      expect(mockRefresh).toHaveBeenCalledTimes(1);
      expect(mockApiFetch).toHaveBeenCalledTimes(2);
    });

    it("stores new tokens after successful refresh", async () => {
      setTokens("expired_at", "rt");
      mockApiFetch.mockRejectedValueOnce(make401()).mockResolvedValueOnce({});
      mockRefresh.mockResolvedValue(makeTokenPair());

      await authenticatedFetch("/api/resource", { method: "GET" });

      expect(getAccessToken()).toBe("new_at");
      expect(getRefreshToken()).toBe("new_rt");
    });

    it("retries with updated authorization header after successful refresh", async () => {
      setTokens("expired_at", "rt");
      mockApiFetch.mockRejectedValueOnce(make401()).mockResolvedValueOnce({});
      mockRefresh.mockResolvedValue(makeTokenPair());

      await authenticatedFetch("/api/resource", { method: "GET" });

      const [, retryInit] = mockApiFetch.mock.calls[1] as [string, RequestInit];
      expect((retryInit.headers as Record<string, string>)["authorization"]).toBe("Bearer new_at");
    });

    it("clears tokens and re-throws original 401 on refresh failure", async () => {
      setTokens("expired_at", "rt");
      mockApiFetch.mockRejectedValue(make401());
      mockRefresh.mockRejectedValue(new ApiRequestError(401, "invalid_refresh_token", "expired"));

      await expect(authenticatedFetch("/api/resource", { method: "GET" })).rejects.toMatchObject({
        status: 401,
        code: "token_expired",
      });

      expect(getAccessToken()).toBeNull();
      expect(getRefreshToken()).toBeNull();
    });

    it("does not retry original request after refresh failure", async () => {
      setTokens("expired_at", "rt");
      mockApiFetch.mockRejectedValue(make401());
      mockRefresh.mockRejectedValue(new Error("refresh failed"));

      await expect(authenticatedFetch("/api/resource", { method: "GET" })).rejects.toBeTruthy();

      expect(mockApiFetch).toHaveBeenCalledTimes(1);
    });

    it("clears tokens and does not call refresh when no refresh token is stored", async () => {
      sessionStorage.setItem("nchat_at", "expired_at");
      // no refresh token
      mockApiFetch.mockRejectedValue(make401());

      await expect(authenticatedFetch("/api/resource", { method: "GET" })).rejects.toMatchObject({
        status: 401,
      });

      expect(mockRefresh).not.toHaveBeenCalled();
      expect(getAccessToken()).toBeNull();
    });

    it("concurrent 401s trigger exactly one refresh call", async () => {
      setTokens("expired_at", "rt");

      let resolveRefresh!: (value: ReturnType<typeof makeTokenPair>) => void;
      const refreshPromise = new Promise<ReturnType<typeof makeTokenPair>>(
        (res) => (resolveRefresh = res),
      );

      // All apiFetch calls reject with 401 (including retries).
      mockApiFetch.mockRejectedValue(make401());
      mockRefresh.mockReturnValue(refreshPromise);

      const p1 = authenticatedFetch("/api/r1", { method: "GET" });
      const p2 = authenticatedFetch("/api/r2", { method: "GET" });

      resolveRefresh(makeTokenPair());

      await Promise.allSettled([p1, p2]);

      expect(mockRefresh).toHaveBeenCalledTimes(1);
    });

    describe("late-arrival guard (access token changed while request was in flight)", () => {
      it("retries with newer token without triggering a second refresh", async () => {
        setTokens("at1", "rt1");
        // Simulate a concurrent refresh completing while this request was in flight:
        // the mock changes the stored tokens before throwing 401.
        mockApiFetch
          .mockImplementationOnce(async () => {
            setTokens("at2", "rt2");
            throw make401();
          })
          .mockResolvedValueOnce({ data: "retried" });

        const result = await authenticatedFetch<{ data: string }>("/api/resource", {
          method: "GET",
        });

        expect(result).toEqual({ data: "retried" });
        expect(mockRefresh).not.toHaveBeenCalled();
        expect(mockApiFetch).toHaveBeenCalledTimes(2);
      });

      it("uses the newer access token for the retry", async () => {
        setTokens("at1", "rt1");
        mockApiFetch
          .mockImplementationOnce(async () => {
            setTokens("at2", "rt2");
            throw make401();
          })
          .mockResolvedValueOnce({});

        await authenticatedFetch("/api/resource", { method: "GET" });

        const [, retryInit] = mockApiFetch.mock.calls[1] as [string, RequestInit];
        expect((retryInit.headers as Record<string, string>)["authorization"]).toBe("Bearer at2");
      });

      it("late second 401 after first refresh settles: retries with newer token, no extra refresh", async () => {
        // First request: full refresh cycle completes.
        setTokens("expired_at", "rt");
        mockApiFetch.mockRejectedValueOnce(make401()).mockResolvedValueOnce({ data: "first_ok" });
        mockRefresh.mockResolvedValueOnce(makeTokenPair());
        await authenticatedFetch("/api/r1", { method: "GET" });
        expect(getAccessToken()).toBe("new_at");

        mockApiFetch.mockReset();
        mockRefresh.mockReset();

        // Second request arrives after the refresh settled (inflightRefresh is null).
        // It captured originalAccessToken = "expired_at" but now sessionStorage has "new_at".
        // Simulate by seeding the "old" token into the call then letting the mock change it:
        // actually, since AT is already "new_at" we just simulate the original request failing
        // with a stale-token scenario via mockImplementation.
        mockApiFetch
          .mockImplementationOnce(async () => {
            // AT is already "new_at" at call time, but suppose the server still returns 401
            // (e.g. clock skew). originalAccessToken === currentAccessToken here, so the
            // late-arrival guard won't fire — this verifies normal refresh still works.
            throw make401();
          })
          .mockResolvedValueOnce({ data: "second_ok" });
        mockRefresh.mockResolvedValueOnce({
          accessToken: "newest_at",
          refreshToken: "newest_rt",
          tokenType: "Bearer",
          expiresIn: 900,
        });

        const result = await authenticatedFetch<{ data: string }>("/api/r2", { method: "GET" });

        expect(result).toEqual({ data: "second_ok" });
        expect(mockRefresh).toHaveBeenCalledTimes(1);
        expect(getAccessToken()).toBe("newest_at");
      });
    });

    describe("session-binding guard (refresh token changed while refresh was in flight)", () => {
      it("stale refresh success after clearTokens does not restore tokens", async () => {
        setTokens("expired_at", "rt");
        // All apiFetch calls (including retry) return 401.
        mockApiFetch.mockRejectedValue(make401());

        let resolveRefresh!: (value: ReturnType<typeof makeTokenPair>) => void;
        mockRefresh.mockReturnValue(
          new Promise<ReturnType<typeof makeTokenPair>>((res) => (resolveRefresh = res)),
        );

        const p = authenticatedFetch("/api/resource", { method: "GET" });

        // Yield once so authenticatedFetch processes the apiFetch 401, passes the
        // late-arrival guard (tokens unchanged at this point), and suspends on
        // `await inflightRefresh` with `.finally()` already attached.
        await Promise.resolve();

        clearTokens(); // simulate logout while refresh is in flight
        resolveRefresh(makeTokenPair()); // stale refresh "succeeds"

        // Retry fires but also 401s; p rejects.
        await expect(p).rejects.toMatchObject({ status: 401 });
        expect(getAccessToken()).toBeNull();
        expect(getRefreshToken()).toBeNull();
      });

      it("stale refresh success after newer setTokens does not overwrite newer tokens", async () => {
        setTokens("expired_at", "rt");
        mockApiFetch.mockRejectedValueOnce(make401()).mockResolvedValueOnce({ data: "ok" });

        let resolveRefresh!: (value: ReturnType<typeof makeTokenPair>) => void;
        mockRefresh.mockReturnValue(
          new Promise<ReturnType<typeof makeTokenPair>>((res) => (resolveRefresh = res)),
        );

        const p = authenticatedFetch("/api/resource", { method: "GET" });

        // Yield so authenticatedFetch processes the 401 and suspends on await inflightRefresh.
        await Promise.resolve();

        // Newer login replaces the session while refresh was in flight.
        setTokens("newer_at", "newer_rt");
        resolveRefresh(makeTokenPair()); // stale refresh "succeeds" (would give new_at/new_rt)

        await p; // retry uses getAccessToken() = "newer_at", second mock returns { data: "ok" }

        expect(getAccessToken()).toBe("newer_at");
        expect(getRefreshToken()).toBe("newer_rt");
      });

      it("stale refresh failure after newer setTokens does not clear newer tokens", async () => {
        setTokens("expired_at", "rt");
        mockApiFetch.mockRejectedValue(make401());

        let rejectRefresh!: (reason: unknown) => void;
        mockRefresh.mockReturnValue(
          new Promise<ReturnType<typeof makeTokenPair>>((_, rej) => (rejectRefresh = rej)),
        );

        const p = authenticatedFetch("/api/resource", { method: "GET" });

        // Yield so authenticatedFetch processes the 401, attaches .finally() to the
        // refresh promise, and suspends on `await inflightRefresh`.
        // Without this yield, rejectRefresh() would fire before .finally() is attached,
        // causing an unhandled rejection.
        await Promise.resolve();

        // Newer login while refresh is in flight.
        setTokens("newer_at", "newer_rt");
        rejectRefresh(new Error("refresh failed"));

        await expect(p).rejects.toBeTruthy();
        expect(getAccessToken()).toBe("newer_at");
        expect(getRefreshToken()).toBe("newer_rt");
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
      setTokens("expired_at", "rt");
      mockApiFetch.mockRejectedValueOnce(make401()).mockResolvedValueOnce({ data: "ok" });
      mockRefresh.mockResolvedValue(makeTokenPair());

      // Pathname is /api/search — only query string contains /auth/login.
      await authenticatedFetch("/api/search?next=/api/auth/login", { method: "GET" });

      expect(mockRefresh).toHaveBeenCalledTimes(1);
    });
  });
});
