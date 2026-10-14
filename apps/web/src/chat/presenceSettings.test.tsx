import { act, renderHook } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const fetchMock = vi.fn();
vi.mock("../lib/authClient", () => ({
  authenticatedFetch: (...args: unknown[]) => fetchMock(...args),
}));

let socketListener: {
  onOpen?: (generation: number) => void;
  onMessage?: (frame: Record<string, unknown>) => void;
} = {};
const release = vi.fn();
vi.mock("./chatSocket", () => ({
  acquireChatSocket: (listener: typeof socketListener) => {
    socketListener = listener;
    return { release, send: () => false, isOpen: () => true, generation: () => 1 };
  },
}));

import {
  automaticPresence,
  formatPresenceUntil,
  MIN_PRESENCE_DURATION_MS,
  parseLocalDateTime,
  parsePresenceSettings,
  presenceExpiry,
  summarizeSelfPresence,
  usePresenceSettings,
} from "./presenceSettings";

function envelope(state: string | null, expiresAt: string | null, writable = true) {
  return { data: { state, expires_at: expiresAt, writable } };
}

/** Far enough ahead that no timer of the controller fires during a test. */
const EXPIRES = "2099-10-01T21:00:00.000Z";

function deferred() {
  let resolve: (value: unknown) => void = () => {};
  let reject: (reason: unknown) => void = () => {};
  const promise = new Promise((res, rej) => {
    resolve = res;
    reject = rej;
  });
  return { promise, resolve, reject };
}

function methods(): string[] {
  return fetchMock.mock.calls.map((call) => (call[1] as RequestInit).method ?? "");
}

/** Lets every settled promise run its callbacks inside act. */
async function flush() {
  await act(async () => {});
}

beforeEach(() => {
  fetchMock.mockReset();
  release.mockReset();
  socketListener = {};
});

afterEach(() => {
  vi.useRealTimers();
});

describe("parsePresenceSettings", () => {
  it("reads a manual state and its end", () => {
    expect(parsePresenceSettings(envelope("dnd", EXPIRES))).toEqual({
      state: "dnd",
      expiresAt: Date.parse(EXPIRES),
      writable: true,
    });
  });

  it("offers a change only when the server says it accepts one", () => {
    expect(parsePresenceSettings(envelope("dnd", EXPIRES, false)).writable).toBe(false);
    expect(parsePresenceSettings({ data: { state: null, expires_at: null } }).writable).toBe(false);
    expect(parsePresenceSettings(envelope(null, null))).toEqual(automaticPresence);
  });

  it("reads anything it cannot vouch for as automatic", () => {
    for (const body of [
      envelope(null, null),
      envelope("invisible", EXPIRES),
      envelope("busy", "soon"),
      null,
      {},
    ]) {
      expect(parsePresenceSettings(body)).toMatchObject({ state: null, expiresAt: null });
    }
  });
});

describe("presenceExpiry", () => {
  const now = new Date(2026, 9, 1, 10, 0, 0).getTime(); // a Thursday

  it("turns each duration into a concrete local instant", () => {
    expect(presenceExpiry("1h", now)).toBe(now + 3_600_000);
    expect(presenceExpiry("4h", now)).toBe(now + 4 * 3_600_000);
    expect(presenceExpiry("today", now)).toBe(new Date(2026, 9, 1, 23, 59, 59, 999).getTime());
    expect(presenceExpiry("week", now)).toBe(new Date(2026, 9, 4, 23, 59, 59, 999).getTime());
  });

  it("ends this week on Sunday itself, and never asks for less than the minimum", () => {
    const sunday = new Date(2026, 9, 4, 12, 0, 0).getTime();
    expect(presenceExpiry("week", sunday)).toBe(new Date(2026, 9, 4, 23, 59, 59, 999).getTime());
    const lateNight = new Date(2026, 9, 1, 23, 58, 0).getTime();
    expect(presenceExpiry("today", lateNight)).toBe(lateNight + MIN_PRESENCE_DURATION_MS);
  });
});

describe("formatting", () => {
  const now = new Date(2026, 9, 1, 10, 0, 0).getTime();

  it("says until when, with a date only when it is not today", () => {
    expect(formatPresenceUntil(new Date(2026, 9, 1, 18, 0).getTime(), now)).toBe("até 18:00");
    expect(formatPresenceUntil(new Date(2026, 9, 3, 9, 30).getTime(), now)).toBe("até 03/10 09:30");
  });

  it("reads a local date-time input", () => {
    expect(parseLocalDateTime("2026-10-01T18:00")).toBe(new Date(2026, 9, 1, 18, 0).getTime());
    expect(parseLocalDateTime("")).toBeNaN();
  });
});

describe("summarizeSelfPresence", () => {
  it("tells a hidden viewer that they are hidden", () => {
    expect(
      summarizeSelfPresence(
        { state: "appear_offline", expiresAt: 1, writable: true },
        { state: "offline" },
        false,
      ),
    ).toEqual({
      dot: "offline",
      label: "Aparecer offline",
    });
  });

  it("shows the choice until the server has answered, then what others see", () => {
    const busy = { state: "busy" as const, expiresAt: 1, writable: true };
    expect(summarizeSelfPresence(busy, { state: "online" }, true)).toEqual({
      dot: "busy",
      label: "Ocupado",
    });
    expect(summarizeSelfPresence(busy, { state: "unknown" }, false)).toEqual({
      dot: "busy",
      label: "Ocupado",
    });
    expect(summarizeSelfPresence(busy, { state: "busy", activity: "in_call" }, false)).toEqual({
      dot: "busy",
      label: "Ocupado · Em chamada",
    });
    expect(summarizeSelfPresence(null, { state: "unknown" }, false)).toEqual({
      dot: "unknown",
      label: "Status",
    });
  });
});

describe("usePresenceSettings", () => {
  async function loaded(body: unknown = envelope(null, null)) {
    fetchMock.mockResolvedValueOnce(body);
    const hook = renderHook(() => usePresenceSettings());
    await flush();
    return hook;
  }

  it("loads the server's settings and releases the socket on unmount", async () => {
    const { result, unmount } = await loaded(envelope("brb", EXPIRES));
    expect(result.current.settings).toEqual({
      state: "brb",
      expiresAt: Date.parse(EXPIRES),
      writable: true,
    });
    expect(fetchMock).toHaveBeenCalledWith(
      "/api/chat/presence/me",
      expect.objectContaining({ method: "GET" }),
    );
    unmount();
    expect(release).toHaveBeenCalled();
  });

  it("reads again when the socket reconnects — a hint sent while it was down is lost", async () => {
    const { result } = await loaded();
    fetchMock.mockResolvedValueOnce(envelope("away", EXPIRES));
    act(() => socketListener.onOpen?.(2));
    await flush();
    expect(result.current.settings?.state).toBe("away");
  });

  it("shows a choice at once and keeps the server's answer", async () => {
    const { result } = await loaded();
    const put = deferred();
    fetchMock.mockReturnValueOnce(put.promise);
    const expiresAt = Date.parse(EXPIRES);
    act(() => result.current.set("dnd", expiresAt));
    expect(result.current.settings).toEqual({ state: "dnd", expiresAt, writable: true });
    expect(result.current.pending).toBe(true);
    const [url, init] = fetchMock.mock.calls.at(-1) as [string, RequestInit];
    expect(url).toBe("/api/chat/presence/me");
    expect(init.method).toBe("PUT");
    expect(JSON.parse(init.body as string)).toEqual({ state: "dnd", expires_at: EXPIRES });

    await act(async () => put.resolve(envelope("dnd", EXPIRES)));
    expect(result.current.pending).toBe(false);
    expect(result.current.failed).toBe(false);
    expect(methods()).toEqual(["GET", "PUT"]);
  });

  // The server sends the hint before the write answers (issue #798).
  it("is not left pending by its own write's hint", async () => {
    const { result } = await loaded();
    const put = deferred();
    fetchMock.mockReturnValueOnce(put.promise);
    act(() => result.current.set("busy", Date.parse(EXPIRES)));

    act(() => socketListener.onMessage?.({ type: "presence.settings_changed" }));
    expect(methods()).toEqual(["GET", "PUT"]); // the read waits for the write

    fetchMock.mockResolvedValueOnce(envelope("busy", EXPIRES));
    await act(async () => put.resolve(envelope("busy", EXPIRES)));
    expect(result.current.pending).toBe(false);
    expect(result.current.settings?.state).toBe("busy");
    await flush();
    expect(methods()).toEqual(["GET", "PUT", "GET"]);
    expect(result.current.settings?.state).toBe("busy");
  });

  it("does not let a read that started before a write overwrite it", async () => {
    const read = deferred();
    fetchMock.mockReturnValueOnce(read.promise);
    const { result } = renderHook(() => usePresenceSettings());

    fetchMock.mockResolvedValueOnce(envelope("dnd", EXPIRES));
    await act(async () => result.current.set("dnd", Date.parse(EXPIRES)));
    await act(async () => read.resolve(envelope("busy", EXPIRES)));
    expect(result.current.settings?.state).toBe("dnd");
  });

  it("puts the server's state back after a refused write, reads it again, and retries", async () => {
    const { result } = await loaded(envelope("busy", EXPIRES));

    fetchMock.mockRejectedValueOnce(new Error("503"));
    fetchMock.mockResolvedValueOnce(envelope("busy", EXPIRES));
    await act(async () => result.current.clear());
    await flush();
    expect(result.current.failed).toBe(true);
    expect(result.current.pending).toBe(false);
    expect(result.current.settings?.state).toBe("busy");
    expect(methods()).toEqual(["GET", "DELETE", "GET"]);

    fetchMock.mockResolvedValueOnce(envelope(null, null));
    await act(async () => result.current.retry());
    expect(fetchMock.mock.calls.at(-1)?.[1]).toMatchObject({ method: "DELETE" });
    expect(result.current.failed).toBe(false);
    expect(result.current.settings).toEqual(automaticPresence);

    // Nothing left to retry.
    const calls = fetchMock.mock.calls.length;
    act(() => result.current.retry());
    expect(fetchMock.mock.calls.length).toBe(calls);
  });

  it("lets the newest of two writes decide, and reads again when the older answers late", async () => {
    const { result } = await loaded();
    const first = deferred();
    const second = deferred();
    fetchMock.mockReturnValueOnce(first.promise).mockReturnValueOnce(second.promise);
    act(() => result.current.set("busy", Date.parse(EXPIRES)));
    act(() => result.current.set("dnd", Date.parse(EXPIRES)));

    await act(async () => second.resolve(envelope("dnd", EXPIRES)));
    expect(result.current.settings?.state).toBe("dnd");
    expect(result.current.pending).toBe(false);

    fetchMock.mockResolvedValueOnce(envelope("dnd", EXPIRES));
    await act(async () => first.resolve(envelope("busy", EXPIRES)));
    expect(result.current.settings?.state).toBe("dnd");
    await flush();
    expect(methods()).toEqual(["GET", "PUT", "PUT", "GET"]);
    expect(result.current.settings?.state).toBe("dnd");
  });

  it("does not report a superseded write's failure", async () => {
    const { result } = await loaded();
    const first = deferred();
    const second = deferred();
    fetchMock.mockReturnValueOnce(first.promise).mockReturnValueOnce(second.promise);
    act(() => result.current.set("busy", Date.parse(EXPIRES)));
    act(() => result.current.set("dnd", Date.parse(EXPIRES)));

    await act(async () => first.reject(new Error("503")));
    expect(result.current.failed).toBe(false);
    expect(result.current.pending).toBe(true);
    fetchMock.mockResolvedValueOnce(envelope("dnd", EXPIRES));
    await act(async () => second.resolve(envelope("dnd", EXPIRES)));
    await flush();
    expect(result.current.pending).toBe(false);
    expect(result.current.settings?.state).toBe("dnd");
  });

  it("re-reads when another session changes the settings, and ignores other frames", async () => {
    const { result } = await loaded();
    act(() => socketListener.onMessage?.({ type: "presence.updated" }));
    expect(fetchMock).toHaveBeenCalledTimes(1);

    fetchMock.mockResolvedValueOnce(envelope("away", EXPIRES));
    act(() => socketListener.onMessage?.({ type: "presence.settings_changed" }));
    await flush();
    expect(result.current.settings?.state).toBe("away");
  });

  it("keeps what it knows when a read fails", async () => {
    fetchMock.mockRejectedValueOnce(new Error("offline"));
    const { result } = renderHook(() => usePresenceSettings());
    await flush();
    expect(result.current.settings).toBeNull();
  });

  it("does nothing once unmounted, whatever answers afterwards", async () => {
    const { result, unmount } = await loaded();
    const put = deferred();
    fetchMock.mockReturnValueOnce(put.promise);
    act(() => result.current.set("busy", Date.parse(EXPIRES)));
    act(() => socketListener.onMessage?.({ type: "presence.settings_changed" }));
    unmount();
    await act(async () => put.resolve(envelope("busy", EXPIRES)));
    expect(methods()).toEqual(["GET", "PUT"]);
  });
});

describe("usePresenceSettings — the end of a manual state", () => {
  const NOW = Date.parse("2026-10-01T12:00:00.000Z");
  const END = NOW + 60 * 60_000;

  beforeEach(() => {
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout", "Date"] });
    vi.setSystemTime(NOW);
  });

  async function hidden(end = END) {
    fetchMock.mockResolvedValueOnce(envelope("appear_offline", new Date(end).toISOString()));
    const hook = renderHook(() => usePresenceSettings());
    await flush();
    return hook;
  }

  it("shows automatic when the end comes, and asks the server, with no hint at all", async () => {
    const { result } = await hidden();
    expect(result.current.settings?.state).toBe("appear_offline");
    expect(summarizeSelfPresence(result.current.settings, { state: "offline" }, false).label).toBe(
      "Aparecer offline",
    );

    fetchMock.mockResolvedValueOnce(envelope(null, null));
    await act(async () => vi.advanceTimersByTime(END - NOW));
    expect(result.current.settings).toMatchObject({ state: null, expiresAt: null });
    expect(summarizeSelfPresence(result.current.settings, { state: "online" }, false)).toEqual({
      dot: "online",
      label: "Disponível",
    });
    expect(methods()).toEqual(["GET", "GET"]);
  });

  it("asks once when the server's clock still holds the state", async () => {
    const { result } = await hidden();
    fetchMock.mockResolvedValue(envelope("appear_offline", new Date(END).toISOString()));
    await act(async () => vi.advanceTimersByTime(END - NOW));
    await act(async () => vi.advanceTimersByTime(60_000));
    expect(result.current.settings?.state).toBeNull();
    expect(methods()).toEqual(["GET", "GET"]);

    // The server's own expiry hint settles it.
    fetchMock.mockResolvedValueOnce(envelope(null, null));
    act(() => socketListener.onMessage?.({ type: "presence.settings_changed" }));
    await flush();
    expect(result.current.settings).toEqual(automaticPresence);
  });

  it("forgets the end of a state that was replaced before it came", async () => {
    const { result } = await hidden();
    const later = END + 3 * 60 * 60_000;
    fetchMock.mockResolvedValueOnce(envelope("dnd", new Date(later).toISOString()));
    await act(async () => result.current.set("dnd", later));

    await act(async () => vi.advanceTimersByTime(END - NOW));
    expect(result.current.settings?.state).toBe("dnd");
    expect(methods()).toEqual(["GET", "PUT"]);
  });

  const MAX_TIMER_MS = 2 ** 31 - 1;
  const MONTH = 31 * 24 * 60 * 60_000;

  it("does not take a timer's ceiling for the end of a long state", async () => {
    const { result } = await hidden(NOW + MONTH);
    await act(async () => vi.advanceTimersByTime(MAX_TIMER_MS));
    expect(result.current.settings?.state).toBe("appear_offline");
    expect(methods()).toEqual(["GET"]);
    fetchMock.mockResolvedValueOnce(envelope(null, null));
    await act(async () => vi.advanceTimersByTime(MONTH - MAX_TIMER_MS));
    expect(result.current.settings?.state).toBeNull();
    expect(methods()).toEqual(["GET", "GET"]);
  });

  // MEDIUM-F: a read that fails while a long state is counted down must not
  // stop the count.
  it("keeps counting when a read fails at the timer's ceiling", async () => {
    const { result } = await hidden(NOW + MONTH);
    await act(async () => vi.advanceTimersByTime(MAX_TIMER_MS));
    fetchMock.mockRejectedValueOnce(new Error("500"));
    act(() => socketListener.onOpen?.(2));
    await flush();
    expect(result.current.settings?.state).toBe("appear_offline");

    fetchMock.mockRejectedValueOnce(new Error("500"));
    await act(async () => vi.advanceTimersByTime(MONTH - MAX_TIMER_MS));
    expect(result.current.settings?.state).toBeNull();
    expect(
      summarizeSelfPresence(result.current.settings, { state: "online" }, result.current.pending),
    ).toEqual({
      dot: "online",
      label: "Disponível",
    });
    expect(result.current.pending).toBe(false);
    expect(methods()).toEqual(["GET", "GET", "GET"]);
  });

  it("re-arms once when a read succeeds in the middle of a long count", async () => {
    const { result } = await hidden(NOW + MONTH);
    await act(async () => vi.advanceTimersByTime(MAX_TIMER_MS / 2));
    fetchMock.mockResolvedValueOnce(
      envelope("appear_offline", new Date(NOW + MONTH).toISOString()),
    );
    act(() => socketListener.onMessage?.({ type: "presence.settings_changed" }));
    await flush();
    expect(vi.getTimerCount()).toBe(1);

    fetchMock.mockResolvedValueOnce(envelope(null, null));
    await act(async () => vi.advanceTimersByTime(MONTH - MAX_TIMER_MS / 2));
    expect(result.current.settings?.state).toBeNull();
    expect(methods()).toEqual(["GET", "GET", "GET"]);
  });

  it("drops the old count when the state is replaced half-way", async () => {
    const { result } = await hidden(NOW + MONTH);
    await act(async () => vi.advanceTimersByTime(MAX_TIMER_MS));
    const later = NOW + MONTH + 7 * 24 * 60 * 60_000;
    fetchMock.mockResolvedValueOnce(envelope("dnd", new Date(later).toISOString()));
    await act(async () => result.current.set("dnd", later));
    expect(vi.getTimerCount()).toBe(1);
    await act(async () => vi.advanceTimersByTime(MONTH - MAX_TIMER_MS));
    expect(result.current.settings?.state).toBe("dnd");
    expect(methods()).toEqual(["GET", "PUT"]);
  });

  it("stops a long count on unmount", async () => {
    const { unmount } = await hidden(NOW + MONTH);
    await act(async () => vi.advanceTimersByTime(MAX_TIMER_MS));
    unmount();
    expect(vi.getTimerCount()).toBe(0);
    await act(async () => vi.advanceTimersByTime(MONTH));
    expect(methods()).toEqual(["GET"]);
  });

  it("stops its timer on unmount", async () => {
    const { unmount } = await hidden();
    unmount();
    await act(async () => vi.advanceTimersByTime(END - NOW));
    expect(methods()).toEqual(["GET"]);
  });
});
