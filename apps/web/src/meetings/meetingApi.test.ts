import { refresh } from "../auth/authApi";
import { _resetState } from "../lib/authClient";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { setTokens } from "../lib/authSession";
import {
  createMeeting,
  inviteAll,
  meetingCandidates,
  meetingError,
  candidateError,
} from "./meetingApi";
import { ApiRequestError } from "../lib/api";
import type { CreateMeetingPayload } from "./meetingTypes";
vi.mock("../auth/authApi", async (original) => ({
  ...(await original<typeof import("../auth/authApi")>()),
  refresh: vi.fn(),
}));
const payload: CreateMeetingPayload = {
  title: "Equipe",
  description: "",
  starts_at: "2026-10-30T10:00:00Z",
  ends_at: "2026-10-30T10:30:00Z",
  timezone: "UTC",
  join_policy: "invited_only",
  conversation: { kind: "dm", id: "dm-1" },
  participants: [],
};
const network = vi.fn();
beforeEach(() => {
  _resetState();
  vi.mocked(refresh)
    .mockReset()
    .mockResolvedValue({ accessToken: "rotated-token", tokenType: "Bearer", expiresIn: 300 });
  setTokens("test-meeting-token");
  vi.stubGlobal("fetch", network);
  network.mockReset();
});
afterEach(() => vi.unstubAllGlobals());
const json = (data: unknown, status = 200) =>
  new Response(JSON.stringify({ data }), {
    status,
    headers: { "Content-Type": "application/json" },
  });
describe("meeting authenticated HTTP contracts", () => {
  it.each([201, 200])("reads create status %s without a followup GET", async (status) => {
    network.mockResolvedValue(json({ id: "meeting", status: "scheduled" }, status));
    expect(await createMeeting(payload, "intent-key")).toEqual({
      meeting: { id: "meeting", status: "scheduled" },
      replayed: status === 200,
    });
    expect(network).toHaveBeenCalledOnce();
    const [url, init] = network.mock.calls[0];
    expect(url).toBe("/api/chat/meetings");
    expect(init.method).toBe("POST");
    expect(JSON.parse(init.body)).toEqual(payload);
    const headers = new Headers(init.headers);
    expect(headers.get("Authorization")).toBe("Bearer test-meeting-token");
    expect(headers.get("Idempotency-Key")).toBe("intent-key");
    expect(headers.get("Content-Type")).toBe("application/json");
  });
  it.each(["create", "bulk"])(
    "refuses automatic %s replay until rotated identity is confirmed",
    async (operation) => {
      network.mockResolvedValue(
        new Response(JSON.stringify({ error: { code: "unauthorized", message: "expired" } }), {
          status: 401,
        }),
      );
      await expect(
        operation === "create" ? createMeeting(payload, "stable-key") : inviteAll("meeting"),
      ).rejects.toMatchObject({ status: 401, code: "identity_unconfirmed" });
      expect(refresh).toHaveBeenCalledOnce();
      expect(network).toHaveBeenCalledOnce();
      expect(meetingError(new ApiRequestError(401, "identity_unconfirmed", ""))).toContain(
        "confirmação da sessão",
      );
    },
  );
  it("sends paginated prefix search and an abort signal", async () => {
    network.mockResolvedValue(json({ items: [], next_cursor: "next" }));
    const controller = new AbortController();
    expect(
      await meetingCandidates(
        { kind: "channel", id: "ch /1", name: "Team", direct: false },
        "Ana%_",
        "cursor-1",
        controller.signal,
      ),
    ).toEqual({ items: [], next_cursor: "next" });
    const [url, init] = network.mock.calls[0];
    expect(url).toContain("/channel/ch%20%2F1?");
    const query = new URL(url, "https://nchat.test").searchParams;
    expect(Object.fromEntries(query)).toEqual({ q: "Ana%_", limit: "50", cursor: "cursor-1" });
    expect(init.signal).toBe(controller.signal);
  });
  it("omits an empty cursor and uses the DM contract", async () => {
    network.mockResolvedValue(json({ items: [] }));
    await meetingCandidates(
      { kind: "dm", id: "dm-1", name: "Team", direct: false },
      "",
      "",
      new AbortController().signal,
    );
    expect(network.mock.calls[0][0]).toBe("/api/chat/meeting-candidates/dm/dm-1?q=&limit=50");
  });
  it("posts empty JSON to invite-all on the persisted ID", async () => {
    network.mockResolvedValue(json({ added: 12 }));
    expect(await inviteAll("meeting/1")).toBe(12);
    expect(network.mock.calls[0][0]).toBe("/api/chat/meetings/meeting%2F1/participants/invite-all");
    expect(network.mock.calls[0][1].body).toBe("{}");
  });
  it.each([{}, { id: "x", status: "unknown" }])(
    "treats malformed create response as unconfirmed",
    async (data) => {
      network.mockResolvedValue(json(data, 201));
      await expect(createMeeting(payload, "same-key")).rejects.toMatchObject({
        status: 0,
        code: "invalid_response",
      });
    },
  );
  it("retains structured HTTP failures without repeating server messages", async () => {
    network.mockResolvedValue(
      new Response(
        JSON.stringify({ error: { code: "idempotency_key_reused", message: "private" } }),
        { status: 409 },
      ),
    );
    await expect(createMeeting(payload, "key")).rejects.toMatchObject({
      status: 409,
      code: "idempotency_key_reused",
    });
  });
  it.each([0, 400, 401, 403, 404, 409, 413, 429, 500, 503, 418])(
    "shows safe error copy for %s",
    (status) => {
      const error = new ApiRequestError(status, "code", "private metadata");
      expect(meetingError(error)).not.toContain("private metadata");
      expect(meetingError(error, true)).not.toContain("private metadata");
    },
  );
  it.each([{}, { added: -1 }, { added: "2" }])(
    "does not report bulk success from a malformed response",
    async (data) => {
      network.mockResolvedValue(json(data));
      await expect(inviteAll("meeting")).rejects.toMatchObject({ code: "invalid_response" });
    },
  );
  it.each([0, 401, 403, 404, 429, 500])("shows search-specific feedback for %s", (status) => {
    const message = candidateError(new ApiRequestError(status, "code", "private"));
    expect(message).not.toContain("private");
    expect(message).not.toContain("duplicar");
  });
  it("classifies non HTTP failures safely", () =>
    expect(meetingError(new Error("private metadata"))).not.toContain("private metadata"));
});
