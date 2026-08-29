import { describe, expect, it, vi } from "vitest";

import { authenticatedFetch } from "../lib/authClient";
import { ApiRequestError } from "../lib/api";
import {
  listSessions,
  revokeAllOtherSessions,
  revokeSession,
  SessionsApiError,
} from "./sessionsApi";

vi.mock("../lib/authClient");

describe("sessionsApi", () => {
  it("listSessions maps the envelope into Session[]", async () => {
    vi.mocked(authenticatedFetch).mockResolvedValueOnce({
      data: [
        {
          id: "s1",
          device_id: null,
          created_at: "2026-08-01T00:00:00Z",
          last_seen_at: "2026-08-27T00:00:00Z",
          idle_expires_at: "2026-08-27T01:00:00Z",
          absolute_expires_at: null,
          revoked_at: null,
          ip_address: "187.10.x.x",
          user_agent: "Mozilla/5.0 (X11; Linux x86_64) Firefox",
          current: true,
        },
      ],
      pagination: { limit: 50 },
    });
    const sessions = await listSessions();
    expect(sessions).toEqual([
      {
        id: "s1",
        createdAt: "2026-08-01T00:00:00Z",
        lastSeenAt: "2026-08-27T00:00:00Z",
        ipAddress: "187.10.x.x",
        userAgent: "Mozilla/5.0 (X11; Linux x86_64) Firefox",
        current: true,
        revokedAt: undefined,
      },
    ]);
    expect(authenticatedFetch).toHaveBeenCalledWith("/api/auth/me/sessions", {
      method: "GET",
      signal: undefined,
    });
  });

  it("listSessions falls back to empty strings when ip_address/user_agent are absent", async () => {
    vi.mocked(authenticatedFetch).mockResolvedValueOnce({
      data: [
        {
          id: "s2",
          device_id: null,
          created_at: "2026-08-01T00:00:00Z",
          last_seen_at: "2026-08-27T00:00:00Z",
          idle_expires_at: "2026-08-27T01:00:00Z",
          absolute_expires_at: null,
          revoked_at: "2026-08-20T00:00:00Z",
          current: false,
        },
      ],
      pagination: { limit: 50 },
    });
    const sessions = await listSessions();
    expect(sessions).toEqual([
      expect.objectContaining({
        id: "s2",
        ipAddress: "",
        userAgent: "",
        revokedAt: "2026-08-20T00:00:00Z",
      }),
    ]);
  });

  it("revokeSession calls DELETE on the session's own path", async () => {
    vi.mocked(authenticatedFetch).mockResolvedValueOnce(undefined);
    await revokeSession("s2");
    expect(authenticatedFetch).toHaveBeenCalledWith("/api/auth/me/sessions/s2", {
      method: "DELETE",
      signal: undefined,
    });
  });

  it("revokeAllOtherSessions calls DELETE on the collection endpoint", async () => {
    vi.mocked(authenticatedFetch).mockResolvedValueOnce(undefined);
    await revokeAllOtherSessions();
    expect(authenticatedFetch).toHaveBeenCalledWith("/api/auth/me/sessions", {
      method: "DELETE",
      signal: undefined,
    });
  });

  it("maps a 401 on revokeAllOtherSessions to SessionsApiError('unauthorized')", async () => {
    vi.mocked(authenticatedFetch).mockRejectedValueOnce(
      new ApiRequestError(401, "unauthorized", "no current session"),
    );
    await expect(revokeAllOtherSessions()).rejects.toMatchObject({ reason: "unauthorized" });
    expect(SessionsApiError).toBeDefined();
  });

  it("maps a 403 on revokeSession to SessionsApiError('forbidden')", async () => {
    vi.mocked(authenticatedFetch).mockRejectedValueOnce(
      new ApiRequestError(403, "forbidden", "not your session"),
    );
    await expect(revokeSession("s3")).rejects.toMatchObject({
      reason: "forbidden",
      message: "Você não tem permissão para esta ação.",
    });
  });

  it("treats a stale 404 revoke as an idempotent success", async () => {
    vi.mocked(authenticatedFetch).mockRejectedValueOnce(
      new ApiRequestError(404, "not_found", "already gone"),
    );

    await expect(revokeSession("stale-session")).resolves.toBeUndefined();
  });

  it("maps an ApiRequestError with an unhandled status to SessionsApiError('unknown')", async () => {
    vi.mocked(authenticatedFetch).mockRejectedValueOnce(
      new ApiRequestError(500, "server_error", "boom"),
    );
    await expect(revokeAllOtherSessions()).rejects.toMatchObject({
      reason: "unknown",
      message: "Não foi possível concluir a operação.",
    });
  });

  it("maps a non-ApiRequestError failure on listSessions to SessionsApiError('unknown')", async () => {
    vi.mocked(authenticatedFetch).mockRejectedValueOnce(new TypeError("network down"));
    await expect(listSessions()).rejects.toMatchObject({
      reason: "unknown",
      message: "Não foi possível concluir a operação.",
    });
  });
});
