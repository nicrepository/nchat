import { act, renderHook } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { meetingCandidates } from "./meetingApi";
import { createMeetingSession } from "./meetingSession";
import { useMeetingCandidates } from "./useMeetingCandidates";
import type { Candidate } from "./meetingTypes";
vi.mock("./meetingApi", async (original) => ({
  ...(await original<typeof import("./meetingApi")>()),
  meetingCandidates: vi.fn(),
}));
const ana: Candidate = { user_id: "ana", display_name: "Ana" };
const me: Candidate = { user_id: "me", display_name: "Eu" };
const create = (direct = false, id = "ch") =>
  createMeetingSession({ id, kind: direct ? "dm" : "channel", name: "Team", direct }, "me");
const flush = async (ms = 300) => {
  await act(async () => {
    await vi.advanceTimersByTimeAsync(ms);
  });
};
beforeEach(() => {
  vi.useFakeTimers();
  vi.mocked(meetingCandidates)
    .mockReset()
    .mockResolvedValue({ items: [ana, me], next_cursor: "ana" });
});
afterEach(() => vi.useRealTimers());

describe("contextual paginated candidates", () => {
  it("loads one bounded page, excludes organizer and seeds only an eligible direct peer", async () => {
    const session = create(true);
    const { result } = renderHook(() => useMeetingCandidates(session, "me", true));
    expect(result.current.page.status).toBe("loading");
    await flush(0);
    expect(result.current.page.items).toEqual([ana]);
    expect(session.getSnapshot().draft.participants).toEqual([ana]);
    expect(session.getSnapshot().baseline.participants).toEqual([ana]);
    expect(meetingCandidates).toHaveBeenCalledOnce();
  });
  it("does not preselect a channel and preserves selection between pages/searches", async () => {
    const session = create();
    const { result } = renderHook(() => useMeetingCandidates(session, "me", true));
    await flush(0);
    expect(session.getSnapshot().draft.participants).toEqual([]);
    session.update({ draft: { ...session.getSnapshot().draft, participants: [ana] } });
    act(() => result.current.next());
    await flush(0);
    expect(vi.mocked(meetingCandidates).mock.calls.at(-1)?.slice(1, 3)).toEqual(["", "ana"]);
    act(() => result.current.search("Be"));
    await flush(299);
    expect(meetingCandidates).toHaveBeenCalledTimes(2);
    await flush(1);
    expect(vi.mocked(meetingCandidates).mock.calls.at(-1)?.slice(1, 3)).toEqual(["Be", ""]);
    expect(session.getSnapshot().draft.participants).toEqual([ana]);
  });
  it("aborts superseded requests and ignores their late responses", async () => {
    let resolve!: (page: { items: Candidate[] }) => void;
    vi.mocked(meetingCandidates).mockImplementationOnce(
      () =>
        new Promise((done) => {
          resolve = done;
        }),
    );
    const session = create();
    const { result, unmount } = renderHook(() => useMeetingCandidates(session, "me", true));
    await flush(0);
    const signal = vi.mocked(meetingCandidates).mock.calls[0][3];
    act(() => result.current.search("Ana"));
    expect(signal.aborted).toBe(true);
    await flush();
    await act(async () => resolve({ items: [{ user_id: "stale", display_name: "Stale" }] }));
    expect(result.current.page.items).toEqual([ana]);
    const latest = vi.mocked(meetingCandidates).mock.calls[1][3];
    unmount();
    expect(latest.aborted).toBe(true);
  });
  it("cancels debounce on query change, supports empty DM and no repeated preselection", async () => {
    vi.mocked(meetingCandidates).mockResolvedValue({ items: [] });
    const session = create(true);
    const { result } = renderHook(() => useMeetingCandidates(session, "me", true));
    act(() => {
      result.current.search("A");
      result.current.search("An");
    });
    await flush();
    expect(meetingCandidates).toHaveBeenCalledOnce();
    expect(session.getSnapshot().seeded).toBe(false);
    act(() => result.current.search(""));
    await flush(0);
    expect(session.getSnapshot().seeded).toBe(true);
    expect(session.getSnapshot().draft.participants).toEqual([]);
    vi.mocked(meetingCandidates).mockResolvedValue({ items: [ana] });
    act(() => result.current.retry());
    await flush(0);
    expect(session.getSnapshot().draft.participants).toEqual([]);
    act(() => result.current.next());
    expect(meetingCandidates).toHaveBeenCalledTimes(3);
  });
  it("handles failure and retries only the current page", async () => {
    vi.mocked(meetingCandidates).mockRejectedValueOnce(new Error("private"));
    const session = create();
    const { result } = renderHook(() => useMeetingCandidates(session, "me", true));
    await flush(0);
    expect(result.current.page.status).toBe("error");
    expect(result.current.page.error).not.toContain("private");
    act(() => result.current.retry());
    await flush(0);
    expect(result.current.page.status).toBe("ready");
  });
  it("does not load when locked and does not overwrite explicit selection with prefill", async () => {
    const session = create(true);
    session.update({ seeded: true });
    const { rerender } = renderHook(({ enabled }) => useMeetingCandidates(session, "me", enabled), {
      initialProps: { enabled: false },
    });
    await flush();
    expect(meetingCandidates).not.toHaveBeenCalled();
    rerender({ enabled: true });
    await flush(0);
    expect(session.getSnapshot().draft.participants).toEqual([]);
  });
});

it("retains a settled page while disabled and resumed, but resumes an interrupted request", async () => {
  const session = create();
  const { result, rerender } = renderHook(
    ({ enabled }) => useMeetingCandidates(session, "me", enabled),
    { initialProps: { enabled: true } },
  );
  await flush(0);
  const page = result.current.page;
  rerender({ enabled: false });
  rerender({ enabled: true });
  await flush(0);
  expect(meetingCandidates).toHaveBeenCalledOnce();
  expect(result.current.page).toBe(page);
  let resolve!: (value: { items: Candidate[] }) => void;
  vi.mocked(meetingCandidates).mockImplementationOnce(
    () =>
      new Promise((done) => {
        resolve = done;
      }),
  );
  act(() => result.current.search("An"));
  await flush();
  const interrupted = vi.mocked(meetingCandidates).mock.calls.at(-1)![3];
  rerender({ enabled: false });
  expect(interrupted.aborted).toBe(true);
  rerender({ enabled: true });
  await flush();
  expect(result.current.page.status).toBe("ready");
  expect(meetingCandidates).toHaveBeenCalledTimes(3);
  await act(async () => resolve({ items: [{ user_id: "stale", display_name: "Stale" }] }));
  expect(result.current.page.items).toEqual([ana]);
});

it("invalidates the page, query and cursor for another conversation or organizer", async () => {
  const first = create();
  const second = create(false, "other");
  const { result, rerender } = renderHook(
    ({ session, organizer }) => useMeetingCandidates(session, organizer, true),
    { initialProps: { session: first, organizer: "me" } },
  );
  await flush(0);
  act(() => result.current.search("An"));
  await flush();
  rerender({ session: second, organizer: "me" });
  expect(result.current.query).toBe("");
  expect(result.current.page.status).toBe("loading");
  await flush(0);
  expect(vi.mocked(meetingCandidates).mock.calls.at(-1)?.slice(0, 3)).toEqual([
    second.target,
    "",
    "",
  ]);
  rerender({ session: second, organizer: "ana" });
  await flush(0);
  expect(result.current.page.items).toEqual([me]);
});
