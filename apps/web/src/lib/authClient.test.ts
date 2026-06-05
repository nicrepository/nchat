import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { ApiRequestError } from "./api";
import { AUTH_SKIP_PATHS, authenticatedFetch } from "./authClient";
import { getAccessToken, getRefreshToken, setTokens } from "./authSession";

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
  it("attaches Authorization header when access token is present", async () => {
    setTokens("my_at", "my_rt");
    mockApiFetch.mockResolvedValue({ ok: true });

    await authenticatedFetch("/api/resource", { method: "GET" });

    expect(mockApiFetch).toHaveBeenCalledWith(
      "/api/resource",
      expect.objectContaining({
        headers: expect.objectContaining({ Authorization: "Bearer my_at" }),
      }),
    );
  });

  it("omits Authorization header when no access token", async () => {
    mockApiFetch.mockResolvedValue({ ok: true });

    await authenticatedFetch("/api/resource", { method: "GET" });

    const [, init] = mockApiFetch.mock.calls[0] as [string, RequestInit];
    expect((init.headers as Record<string, string>)["Authorization"]).toBeUndefined();
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

    it("retries with new Authorization header after successful refresh", async () => {
      setTokens("expired_at", "rt");
      mockApiFetch.mockRejectedValueOnce(make401()).mockResolvedValueOnce({});
      mockRefresh.mockResolvedValue(makeTokenPair());

      await authenticatedFetch("/api/resource", { method: "GET" });

      const [, retryInit] = mockApiFetch.mock.calls[1] as [string, RequestInit];
      expect((retryInit.headers as Record<string, string>)["Authorization"]).toBe("Bearer new_at");
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
  });

  describe("auth endpoint exclusion", () => {
    it.each(AUTH_SKIP_PATHS.map((p) => [p] as const))(
      "does not trigger refresh for auth path %s",
      async (path) => {
        mockApiFetch.mockRejectedValue(make401());

        await expect(
          authenticatedFetch(`/api${path}`, { method: "POST", body: "{}" }),
        ).rejects.toMatchObject({ status: 401 });

        expect(mockRefresh).not.toHaveBeenCalled();
      },
    );
  });
});
