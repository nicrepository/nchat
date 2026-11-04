import { beforeEach, describe, expect, it, vi } from "vitest";
import { ApiRequestError } from "../lib/api";
import { createMeeting, inviteAll } from "./meetingApi";
import { createMeetingSession, meetingBusy, meetingDirty, meetingLocked } from "./meetingSession";
import type { MeetingResult, MeetingOperation } from "./meetingTypes";
vi.mock("./meetingApi", async (importOriginal) => ({
  ...(await importOriginal<typeof import("./meetingApi")>()),
  createMeeting: vi.fn(),
  inviteAll: vi.fn(),
}));
const meeting: MeetingResult = {
  id: "meeting-1",
  status: "scheduled",
  version: 1,
  starts_at: "2026-10-30T10:00:00Z",
  ends_at: "2026-10-30T10:30:00Z",
  timezone: "UTC",
};
const setup = () =>
  createMeetingSession({ kind: "channel", id: "ch-1", name: "Equipe", direct: false }, "me");
beforeEach(() => {
  vi.resetAllMocks();
  vi.mocked(createMeeting).mockResolvedValue({ meeting, replayed: false });
  vi.mocked(inviteAll).mockResolvedValue(12);
});

describe("meeting intention and two transactions", () => {
  it("preserves a dirty draft and notifies subscribers until explicit disposal", () => {
    const session = setup();
    const callback = vi.fn();
    const off = session.subscribe(callback);
    expect(meetingDirty(session.getSnapshot())).toBe(false);
    session.update({ draft: { ...session.getSnapshot().draft, title: "Changed" } });
    expect(meetingDirty(session.getSnapshot())).toBe(true);
    expect(callback).toHaveBeenCalledOnce();
    off();
    session.update({ seeded: true });
    expect(callback).toHaveBeenCalledOnce();
  });
  it("creates once even for concurrent submits", async () => {
    let finish!: (value: { meeting: MeetingResult; replayed: boolean }) => void;
    vi.mocked(createMeeting).mockReturnValue(
      new Promise((resolve) => {
        finish = resolve;
      }),
    );
    const session = setup();
    const pending = session.submit();
    await session.submit();
    expect(session.getSnapshot().operation.stage).toBe("creating");
    expect(createMeeting).toHaveBeenCalledOnce();
    finish({ meeting, replayed: false });
    await pending;
    await session.submit();
    expect(createMeeting).toHaveBeenCalledOnce();
    expect(inviteAll).not.toHaveBeenCalled();
    expect(session.getSnapshot().operation).toEqual({
      stage: "complete",
      meeting,
      replayed: false,
    });
  });
  it("recovers a lost create response with exactly the same key and payload", async () => {
    vi.mocked(createMeeting).mockRejectedValueOnce(
      new ApiRequestError(0, "network_error", "private"),
    );
    const session = setup();
    await session.submit();
    expect(session.getSnapshot().operation).toMatchObject({
      stage: "create-error",
      uncertain: true,
    });
    vi.mocked(createMeeting).mockResolvedValue({ meeting, replayed: true });
    await session.submit();
    const calls = vi.mocked(createMeeting).mock.calls;
    expect(calls[0]).toEqual(calls[1]);
    expect(calls[0][1]).toMatch(/^[A-Za-z0-9._:-]{1,128}$/);
    expect(session.getSnapshot().operation).toMatchObject({ stage: "complete", replayed: true });
  });
  it.each([400, 401, 403, 404, 409, 413, 429, 500, 503])(
    "preserves draft on HTTP %s",
    async (status) => {
      vi.mocked(createMeeting).mockRejectedValueOnce(
        new ApiRequestError(status, "code", "private account information"),
      );
      const session = setup();
      const before = session.getSnapshot().draft;
      await session.submit();
      expect(session.getSnapshot().draft).toEqual(before);
      expect(session.getSnapshot().operation).toMatchObject({
        stage: "create-error",
        uncertain: status >= 500,
      });
      expect(JSON.stringify(session.getSnapshot().operation)).not.toContain(
        "private account information",
      );
    },
  );
  it("keeps key for definite retry and changes key only after material payload change", async () => {
    vi.mocked(createMeeting).mockRejectedValue(new ApiRequestError(429, "rate_limited", ""));
    const session = setup();
    await session.submit();
    await session.submit();
    expect(vi.mocked(createMeeting).mock.calls[0][1]).toBe(
      vi.mocked(createMeeting).mock.calls[1][1],
    );
    session.update({ draft: { ...session.getSnapshot().draft, title: "Other" } });
    await session.submit();
    expect(vi.mocked(createMeeting).mock.calls[2][1]).not.toBe(
      vi.mocked(createMeeting).mock.calls[0][1],
    );
  });
  it("invites all only after persisted create, and retries only bulk after partial failure", async () => {
    const session = setup();
    session.update({ draft: { ...session.getSnapshot().draft, inviteAll: true } });
    vi.mocked(inviteAll).mockRejectedValueOnce(new ApiRequestError(503, "unavailable", ""));
    await session.submit();
    expect(session.getSnapshot().operation).toMatchObject({ stage: "invite-error", meeting });
    expect(createMeeting).toHaveBeenCalledOnce();
    expect(inviteAll).toHaveBeenCalledWith(meeting.id);
    await session.submit();
    expect(createMeeting).toHaveBeenCalledOnce();
    expect(inviteAll).toHaveBeenCalledTimes(2);
    expect(session.getSnapshot().operation).toMatchObject({ stage: "complete", added: 12 });
  });
  it("can explicitly complete an existing meeting with the original partial selection", async () => {
    const session = setup();
    session.finishWithoutBulk();
    expect(session.getSnapshot().operation.stage).toBe("editing");
    session.update({ draft: { ...session.getSnapshot().draft, inviteAll: true } });
    vi.mocked(inviteAll).mockRejectedValueOnce(new ApiRequestError(400, "bad_request", ""));
    await session.submit();
    session.finishWithoutBulk();
    expect(session.getSnapshot().operation).toMatchObject({ stage: "complete", meeting });
    expect(createMeeting).toHaveBeenCalledOnce();
    expect(inviteAll).toHaveBeenCalledOnce();
  });
  it("does not invite members to a terminal replay", async () => {
    const session = setup();
    session.update({ draft: { ...session.getSnapshot().draft, inviteAll: true } });
    vi.mocked(createMeeting).mockResolvedValue({
      meeting: { ...meeting, status: "cancelled" },
      replayed: true,
    });
    await session.submit();
    expect(inviteAll).not.toHaveBeenCalled();
    expect(session.getSnapshot().operation).toMatchObject({
      stage: "complete",
      meeting: { status: "cancelled" },
    });
  });
  it("does not submit until the shell confirms identity", async () => {
    const confirmed = vi.fn(() => false);
    const session = createMeetingSession(
      { kind: "channel", id: "ch", name: "Team", direct: false },
      "me",
      confirmed,
    );
    await session.submit();
    expect(createMeeting).not.toHaveBeenCalled();
    confirmed.mockReturnValue(true);
    await session.submit();
    expect(createMeeting).toHaveBeenCalledOnce();
  });
  it("preserves the created ID if identity becomes pending before bulk", async () => {
    const confirmed = vi.fn(() => true);
    const session = createMeetingSession(
      { kind: "channel", id: "ch", name: "Team", direct: false },
      "me",
      confirmed,
    );
    session.update({ draft: { ...session.getSnapshot().draft, inviteAll: true } });
    vi.mocked(createMeeting).mockImplementationOnce(async () => {
      confirmed.mockReturnValue(false);
      return { meeting, replayed: false };
    });
    await session.submit();
    expect(inviteAll).not.toHaveBeenCalled();
    expect(session.getSnapshot().operation).toMatchObject({ stage: "invite-error", meeting });
    confirmed.mockReturnValue(true);
    await session.submit();
    expect(inviteAll).toHaveBeenCalledWith(meeting.id);
    expect(createMeeting).toHaveBeenCalledOnce();
  });
  it("classifies unknown failures conservatively", async () => {
    vi.mocked(createMeeting).mockRejectedValueOnce(new Error("parse failed"));
    const session = setup();
    await session.submit();
    expect(session.getSnapshot().operation).toMatchObject({
      stage: "create-error",
      uncertain: true,
    });
  });
  it.each(["editing", "creating", "inviting", "invite-error", "complete", "create-error"])(
    "classifies operation %s",
    (stage) => {
      const operation = { stage, uncertain: false } as MeetingOperation;
      expect(meetingBusy(operation)).toBe(["creating", "inviting"].includes(stage));
      expect(meetingLocked(operation)).toBe(!["editing", "create-error"].includes(stage));
    },
  );
});
