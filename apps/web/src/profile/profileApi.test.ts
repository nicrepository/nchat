import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { ApiRequestError } from "../lib/api";
import {
  AvatarUploadError,
  fetchMyProfile,
  removeAvatar,
  updateProfile,
  uploadAvatar,
} from "./profileApi";

const { mockAuthFetch } = vi.hoisted(() => ({ mockAuthFetch: vi.fn() }));

vi.mock("../lib/authClient", () => ({
  authenticatedFetch: (url: string, init: RequestInit) => mockAuthFetch(url, init),
}));

beforeEach(() => vi.clearAllMocks());
afterEach(() => vi.clearAllMocks());

describe("uploadAvatar", () => {
  it("posts multipart form data with the avatar field and returns the url", async () => {
    mockAuthFetch.mockResolvedValue({ data: { avatar_url: "/api/auth/avatars/abc.png" } });
    const file = new File([new Uint8Array([1, 2, 3])], "me.png", { type: "image/png" });

    const url = await uploadAvatar(file);

    expect(url).toBe("/api/auth/avatars/abc.png");
    const [calledUrl, init] = mockAuthFetch.mock.calls[0];
    expect(calledUrl).toContain("/me/avatar");
    expect(init.method).toBe("POST");
    expect(init.body).toBeInstanceOf(FormData);
    const body = init.body as FormData;
    const sent = body.get("avatar");
    expect(sent).toBeInstanceOf(File);
    // The client never sends a user_id — identity is the session's.
    expect(body.get("user_id")).toBeNull();
  });

  it("maps 413 to a too_large reason", async () => {
    mockAuthFetch.mockRejectedValue(new ApiRequestError(413, "payload_too_large", "big"));
    const file = new File([new Uint8Array([1])], "me.png", { type: "image/png" });
    await expect(uploadAvatar(file)).rejects.toMatchObject({
      name: "AvatarUploadError",
      reason: "too_large",
    });
  });

  it("maps 415 to unsupported", async () => {
    mockAuthFetch.mockRejectedValue(new ApiRequestError(415, "unsupported_media_type", "bad"));
    const file = new File([new Uint8Array([1])], "x.gif", { type: "image/gif" });
    await expect(uploadAvatar(file)).rejects.toMatchObject({ reason: "unsupported" });
  });

  it("maps 403 to forbidden", async () => {
    mockAuthFetch.mockRejectedValue(new ApiRequestError(403, "forbidden", "no"));
    const file = new File([new Uint8Array([1])], "x.png", { type: "image/png" });
    await expect(uploadAvatar(file)).rejects.toMatchObject({ reason: "forbidden" });
  });

  it("maps an unknown failure to a generic reason", async () => {
    mockAuthFetch.mockRejectedValue(new Error("boom"));
    const file = new File([new Uint8Array([1])], "x.png", { type: "image/png" });
    await expect(uploadAvatar(file)).rejects.toBeInstanceOf(AvatarUploadError);
  });
});

describe("fetchMyProfile", () => {
  // jsdom serves tests from http://localhost:3000.
  it("maps the profile and keeps a same-origin avatar", async () => {
    mockAuthFetch.mockResolvedValue({
      data: { id: "u1", display_name: "Ana", avatar_url: "/api/auth/avatars/x.png" },
    });
    const profile = await fetchMyProfile();
    expect(profile).toEqual({
      id: "u1",
      displayName: "Ana",
      avatarUrl: "/api/auth/avatars/x.png",
      jobTitle: "",
      bio: "",
      timezone: "",
      customStatus: "",
    });
    const [url, init] = mockAuthFetch.mock.calls[0];
    expect(url).toContain("/me");
    expect(init.method).toBe("GET");
  });

  it("normalises the display name and reports '' when there is none usable", async () => {
    for (const [raw, expected] of [
      ["  Ana Souza  ", "Ana Souza"],
      ["   ", ""],
      ["", ""],
      [null, ""],
      [undefined, ""],
      [42, ""],
    ] as const) {
      mockAuthFetch.mockResolvedValue({ data: { id: "u1", display_name: raw } });
      expect((await fetchMyProfile()).displayName).toBe(expected);
    }
  });

  it("forwards an abort signal to the transport", async () => {
    mockAuthFetch.mockResolvedValue({ data: { id: "u1", display_name: "Ana" } });
    const controller = new AbortController();
    await fetchMyProfile(controller.signal);
    expect(mockAuthFetch.mock.calls[0][1].signal).toBe(controller.signal);
  });

  it("drops a cross-origin avatar from the profile", async () => {
    mockAuthFetch.mockResolvedValue({
      data: { id: "u1", display_name: "Ana", avatar_url: "https://evil.example.test/a.png" },
    });
    const profile = await fetchMyProfile();
    expect(profile.avatarUrl).toBeUndefined();
  });

  it("drops a dangerous-scheme avatar from the profile", async () => {
    mockAuthFetch.mockResolvedValue({
      data: { id: "u1", display_name: "Ana", avatar_url: "javascript:alert(1)" },
    });
    expect((await fetchMyProfile()).avatarUrl).toBeUndefined();
  });

  it("returns undefined avatar when the server sends none", async () => {
    mockAuthFetch.mockResolvedValue({ data: { id: "u1", display_name: "Ana" } });
    expect((await fetchMyProfile()).avatarUrl).toBeUndefined();
  });

  it("drops same-host URLs carrying credentials, protocol-relative and malformed values", async () => {
    for (const avatar of [
      "http://user:pass@localhost:3000/a.png",
      "//evil.example.test/a.png",
      "http://localhost:3001/a.png", // different port
      "   ",
    ]) {
      mockAuthFetch.mockResolvedValue({
        data: { id: "u1", display_name: "Ana", avatar_url: avatar },
      });
      expect((await fetchMyProfile()).avatarUrl).toBeUndefined();
    }
  });

  it("drops a malformed URL that new URL() cannot parse at all", async () => {
    mockAuthFetch.mockResolvedValue({
      data: { id: "u1", display_name: "Ana", avatar_url: "http://[not-valid-host" },
    });
    expect((await fetchMyProfile()).avatarUrl).toBeUndefined();
  });

  it("drops every avatar URL when window.location.origin is the opaque-origin sentinel 'null'", async () => {
    // A sandboxed iframe (or a file:// document) reports "null" as its
    // origin — the same value a browser really produces, not a test-only
    // fabrication. sameOriginAvatarUrl must treat that as "no safe origin
    // to compare against" and drop the URL rather than risk a same-origin
    // check that can never be satisfied honestly.
    const originalLocation = window.location;
    Object.defineProperty(window, "location", {
      configurable: true,
      value: { ...originalLocation, origin: "null" },
    });
    try {
      mockAuthFetch.mockResolvedValue({
        data: { id: "u1", display_name: "Ana", avatar_url: "/api/auth/avatars/x.png" },
      });
      expect((await fetchMyProfile()).avatarUrl).toBeUndefined();
    } finally {
      Object.defineProperty(window, "location", {
        configurable: true,
        value: originalLocation,
      });
    }
  });

  it("accepts an absolute same-origin URL unchanged", async () => {
    mockAuthFetch.mockResolvedValue({
      data: {
        id: "u1",
        display_name: "Ana",
        avatar_url: "http://localhost:3000/api/auth/avatars/a.png",
      },
    });
    expect((await fetchMyProfile()).avatarUrl).toBe("http://localhost:3000/api/auth/avatars/a.png");
  });
});

describe("updateProfile", () => {
  it("sends all five fields in one PATCH and maps the response", async () => {
    mockAuthFetch.mockResolvedValueOnce({
      data: {
        id: "u1",
        display_name: "Ana",
        job_title: "Eng",
        bio: "bio",
        timezone: "America/Sao_Paulo",
        custom_status: "🚀 Focada",
      },
    });
    const result = await updateProfile({
      displayName: "Ana",
      jobTitle: "Eng",
      bio: "bio",
      timezone: "America/Sao_Paulo",
      customStatus: "🚀 Focada",
    });
    expect(mockAuthFetch).toHaveBeenCalledWith(
      expect.stringContaining("/me"),
      expect.objectContaining({
        method: "PATCH",
        body: JSON.stringify({
          display_name: "Ana",
          job_title: "Eng",
          bio: "bio",
          timezone: "America/Sao_Paulo",
          custom_status: "🚀 Focada",
        }),
      }),
    );
    expect(result.displayName).toBe("Ana");
  });

  it("maps a 400 to an invalid UpdateProfileError", async () => {
    mockAuthFetch.mockRejectedValueOnce(new ApiRequestError(400, "bad", "bad"));
    await expect(
      updateProfile({ displayName: "x", jobTitle: "", bio: "", timezone: "", customStatus: "" }),
    ).rejects.toMatchObject({ reason: "invalid" });
  });

  it("maps a 403 to forbidden", async () => {
    mockAuthFetch.mockRejectedValueOnce(new ApiRequestError(403, "forbidden", "no"));
    await expect(
      updateProfile({ displayName: "x", jobTitle: "", bio: "", timezone: "", customStatus: "" }),
    ).rejects.toMatchObject({ reason: "forbidden" });
  });

  it("maps an unrecognized status and a non-ApiRequestError both to 'unknown'", async () => {
    mockAuthFetch.mockRejectedValueOnce(new ApiRequestError(500, "server_error", "boom"));
    await expect(
      updateProfile({ displayName: "x", jobTitle: "", bio: "", timezone: "", customStatus: "" }),
    ).rejects.toMatchObject({ reason: "unknown" });

    mockAuthFetch.mockRejectedValueOnce(new TypeError("network down"));
    await expect(
      updateProfile({ displayName: "x", jobTitle: "", bio: "", timezone: "", customStatus: "" }),
    ).rejects.toMatchObject({ reason: "unknown" });
  });
});

describe("removeAvatar", () => {
  it("issues a DELETE to the me-avatar endpoint", async () => {
    mockAuthFetch.mockResolvedValue(undefined);
    await removeAvatar();
    const [calledUrl, init] = mockAuthFetch.mock.calls[0];
    expect(calledUrl).toContain("/me/avatar");
    expect(init.method).toBe("DELETE");
  });

  it("wraps failures in AvatarUploadError", async () => {
    mockAuthFetch.mockRejectedValue(new ApiRequestError(500, "internal_error", "x"));
    await expect(removeAvatar()).rejects.toBeInstanceOf(AvatarUploadError);
  });
});
