import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { ApiRequestError } from "../lib/api";
import {
  AvatarUploadError,
  fetchMyProfile,
  removeAvatar,
  updateDisplayName,
  UpdateDisplayNameError,
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
    expect(profile).toEqual({ id: "u1", displayName: "Ana", avatarUrl: "/api/auth/avatars/x.png" });
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

describe("updateDisplayName", () => {
  it("sends a PATCH with only display_name and returns the persisted profile", async () => {
    mockAuthFetch.mockResolvedValue({
      data: { id: "u1", display_name: "Ana Lima", avatar_url: "/api/auth/avatars/x.png" },
    });

    const profile = await updateDisplayName("Ana Lima");

    expect(profile).toEqual({
      id: "u1",
      displayName: "Ana Lima",
      avatarUrl: "/api/auth/avatars/x.png",
    });
    const [calledUrl, init] = mockAuthFetch.mock.calls[0];
    expect(calledUrl).toContain("/me");
    expect(calledUrl).not.toContain("/me/avatar");
    expect(init.method).toBe("PATCH");
    expect(JSON.parse(init.body as string)).toEqual({ display_name: "Ana Lima" });
    // The client never sends an id, user_id or any other field — identity is
    // the session's, and nothing else is editable through this call.
    expect(Object.keys(JSON.parse(init.body as string))).toEqual(["display_name"]);
  });

  it("forwards an abort signal to the transport", async () => {
    mockAuthFetch.mockResolvedValue({ data: { id: "u1", display_name: "Ana" } });
    const controller = new AbortController();
    await updateDisplayName("Ana", controller.signal);
    expect(mockAuthFetch.mock.calls[0][1].signal).toBe(controller.signal);
  });

  it("maps 400 to an invalid reason", async () => {
    mockAuthFetch.mockRejectedValue(new ApiRequestError(400, "bad_request", "bad"));
    await expect(updateDisplayName("")).rejects.toMatchObject({
      name: "UpdateDisplayNameError",
      reason: "invalid",
    });
  });

  it("maps 403 to forbidden", async () => {
    mockAuthFetch.mockRejectedValue(new ApiRequestError(403, "forbidden", "no"));
    await expect(updateDisplayName("Ana")).rejects.toMatchObject({ reason: "forbidden" });
  });

  it("maps an unknown failure to a generic reason", async () => {
    mockAuthFetch.mockRejectedValue(new Error("boom"));
    await expect(updateDisplayName("Ana")).rejects.toBeInstanceOf(UpdateDisplayNameError);
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
