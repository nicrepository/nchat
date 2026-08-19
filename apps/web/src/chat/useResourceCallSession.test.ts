import { act, renderHook } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";

import { issueCallToken } from "./callApi";
import type { Call } from "./callState";
import { acquireChatSocket } from "./chatSocket";
import { leaveResourceCall, startResourceCall } from "./resourceCallSignaling";
import type { CallMediaBridge } from "./useCallSignaling";
import { useResourceCallSession, type ResourceCallTarget } from "./useResourceCallSession";

vi.mock("./callApi", () => ({
  issueCallToken: vi.fn(),
}));
vi.mock("./resourceCallSignaling", () => ({
  startResourceCall: vi.fn(),
  leaveResourceCall: vi.fn(),
}));
vi.mock("./chatSocket", () => ({
  acquireChatSocket: vi.fn(() => ({
    send: vi.fn(() => true),
    isOpen: vi.fn(() => true),
    generation: vi.fn(() => 1),
    release: vi.fn(),
  })),
}));

function fakeMedia(): CallMediaBridge & {
  startAudio: ReturnType<typeof vi.fn>;
  connect: ReturnType<typeof vi.fn>;
  stop: ReturnType<typeof vi.fn>;
} {
  return {
    startAudio: vi.fn(async () => undefined),
    connect: vi.fn(async () => undefined),
    stop: vi.fn(async () => undefined),
  };
}

const channelTarget: ResourceCallTarget = {
  kind: "channel",
  id: "00000000-0000-4000-8000-000000000701",
  name: "geral",
};
const resourceCall = {
  call_id: "00000000-0000-4000-8000-000000000705",
  request_id: "00000000-0000-4000-8000-000000000706",
  caller_id: "00000000-0000-4000-8000-000000000707",
  callee_id: "",
  target_type: "channel" as const,
  target_id: channelTarget.id,
  call_type: "audio" as const,
  status: "active" as const,
  version: 1,
  created_at: "2026-08-18T12:00:00Z",
  occurred_at: "2026-08-18T12:00:00Z",
  expires_at: "2026-08-18T12:00:30Z",
};

beforeEach(() => {
  vi.mocked(startResourceCall).mockReset();
  vi.mocked(startResourceCall).mockResolvedValue(resourceCall);
  vi.mocked(issueCallToken).mockReset();
  vi.mocked(issueCallToken).mockResolvedValue({
    token: "resource-token",
    expiresAt: "2026-01-01T00:00:00Z",
    serverUrl: "wss://livekit-dev.nic-labs.com",
  });
  vi.mocked(leaveResourceCall).mockReset();
  vi.mocked(leaveResourceCall).mockResolvedValue({ ...resourceCall, status: "ended" });
});

describe("useResourceCallSession", () => {
  it("joins a channel room: unlocks audio, fetches a resource token, connects, goes active", async () => {
    const media = fakeMedia();
    const view = renderHook(() => useResourceCallSession(media));

    await act(() => view.result.current.join(channelTarget));

    expect(media.startAudio).toHaveBeenCalledOnce();
    expect(startResourceCall).toHaveBeenCalledExactlyOnceWith(channelTarget);
    expect(issueCallToken).toHaveBeenCalledExactlyOnceWith(resourceCall.call_id);
    expect(media.connect).toHaveBeenCalledExactlyOnceWith(
      { call_id: resourceCall.call_id, call_type: "audio" },
      "resource-token",
      "wss://livekit-dev.nic-labs.com",
    );
    expect(view.result.current.status).toBe("active");
    expect(view.result.current.active).toEqual(channelTarget);
    expect(view.result.current.error).toBeNull();
  });

  it("joins a group DM room with resource_kind dm", async () => {
    const media = fakeMedia();
    const view = renderHook(() => useResourceCallSession(media));
    const groupTarget: ResourceCallTarget = { ...channelTarget, kind: "dm" };

    await act(() => view.result.current.join(groupTarget));

    expect(startResourceCall).toHaveBeenCalledExactlyOnceWith(groupTarget);
    expect(issueCallToken).toHaveBeenCalledExactlyOnceWith(resourceCall.call_id);
    expect(media.connect).toHaveBeenCalledExactlyOnceWith(
      { call_id: resourceCall.call_id, call_type: "audio" },
      "resource-token",
      "wss://livekit-dev.nic-labs.com",
    );
  });

  it("never lets the camera auto-enable: call_type stays 'audio' regardless of target (RF-24 follow-up)", async () => {
    const media = fakeMedia();
    const view = renderHook(() => useResourceCallSession(media));

    await act(() => view.result.current.join(channelTarget));

    expect(media.connect).toHaveBeenCalledExactlyOnceWith(
      expect.objectContaining({ call_type: "audio" }),
      "resource-token",
      "wss://livekit-dev.nic-labs.com",
    );
  });

  it("reports an error and leaves status idle-adjacent when the token request fails", async () => {
    const media = fakeMedia();
    vi.mocked(issueCallToken).mockRejectedValueOnce(new Error("unauthorized"));
    const view = renderHook(() => useResourceCallSession(media));

    await act(() => view.result.current.join(channelTarget));

    expect(view.result.current.status).toBe("error");
    expect(view.result.current.error).not.toBeNull();
    expect(media.connect).not.toHaveBeenCalled();
  });

  it("dedups a second join for the same target while one is already in flight", async () => {
    const media = fakeMedia();
    let resolveToken!: (value: { token: string; expiresAt: string; serverUrl: string }) => void;
    vi.mocked(issueCallToken).mockReturnValueOnce(
      new Promise((resolve) => {
        resolveToken = resolve;
      }),
    );
    const view = renderHook(() => useResourceCallSession(media));

    let first!: Promise<void>;
    let second!: Promise<void>;
    act(() => {
      first = view.result.current.join(channelTarget);
      second = view.result.current.join(channelTarget);
    });
    await act(async () => {
      resolveToken({ token: "t", expiresAt: "2026-01-01T00:00:00Z", serverUrl: "wss://x" });
      await Promise.all([first, second]);
    });

    expect(issueCallToken).toHaveBeenCalledOnce();
  });

  // ── issue #569: leave() must release this participant's server-side
  // participation (a call.leave signal), never just clean up locally and
  // never end the resource call for anyone else. ─────────────────────────

  it("leave() stops media locally and releases this participant's own server-side presence", async () => {
    const media = fakeMedia();
    const view = renderHook(() => useResourceCallSession(media));
    await act(() => view.result.current.join(channelTarget));

    await act(() => view.result.current.leave());

    expect(media.stop).toHaveBeenCalledOnce();
    expect(leaveResourceCall).toHaveBeenCalledExactlyOnceWith(resourceCall.call_id);
    expect(view.result.current.active).toBeNull();
    expect(view.result.current.status).toBe("idle");
    expect(view.result.current.error).toBeNull();
  });

  it("leave() never sends any signal before a call_id is known: an abandoned in-flight join has nothing to release", async () => {
    const media = fakeMedia();
    let resolveStartAudio!: () => void;
    media.startAudio.mockReturnValueOnce(
      new Promise<void>((resolve) => {
        resolveStartAudio = resolve;
      }),
    );
    const view = renderHook(() => useResourceCallSession(media));

    let joining!: Promise<void>;
    act(() => {
      joining = view.result.current.join(channelTarget);
    });
    await act(() => view.result.current.leave());

    expect(leaveResourceCall).not.toHaveBeenCalled();

    await act(async () => {
      resolveStartAudio();
      await joining;
    });
  });

  it("a leave() whose server-side release fails surfaces a recoverable 'leave' error distinct from a media-cleanup failure", async () => {
    const media = fakeMedia();
    vi.mocked(leaveResourceCall).mockRejectedValueOnce(new Error("release failed"));
    const view = renderHook(() => useResourceCallSession(media));
    await act(() => view.result.current.join(channelTarget));

    let caught: unknown;
    await act(async () => {
      try {
        await view.result.current.leave();
      } catch (error) {
        caught = error;
      }
    });

    expect(caught).toBeInstanceOf(Error);
    expect((caught as Error).message).toBe("release failed");
    expect(media.stop).toHaveBeenCalledOnce();
    expect(view.result.current.active).toEqual(channelTarget);
    expect(view.result.current.status).toBe("error");
    expect(view.result.current.errorOperation).toBe("leave");

    // Retrying succeeds once the server-side release stops failing — the
    // same affordance, not a fresh join.
    await act(() => view.result.current.leave());
    expect(view.result.current.active).toBeNull();
    expect(view.result.current.status).toBe("idle");
    expect(leaveResourceCall).toHaveBeenCalledTimes(2);
  });

  it("never turns leave() into ending the call: nothing beyond call.leave's own signaling is ever sent", async () => {
    const media = fakeMedia();
    const view = renderHook(() => useResourceCallSession(media));
    await act(() => view.result.current.join(channelTarget));

    await act(() => view.result.current.leave());

    // The hook's only server-facing calls are startResourceCall (join) and
    // leaveResourceCall (leave) — there is no call.end/global-end affordance
    // in this hook's surface at all, so a passing join+leave already proves
    // leave() never reached for one.
    expect(startResourceCall).toHaveBeenCalledOnce();
    expect(leaveResourceCall).toHaveBeenCalledExactlyOnceWith(resourceCall.call_id);
  });

  it("a duplicated/retried leave() is safe: both settle and never leave a stuck pending state", async () => {
    const media = fakeMedia();
    const view = renderHook(() => useResourceCallSession(media));
    await act(() => view.result.current.join(channelTarget));

    let first!: Promise<void>;
    let second!: Promise<void>;
    await act(async () => {
      first = view.result.current.leave();
      second = view.result.current.leave();
      await Promise.all([first, second]);
    });

    expect(view.result.current.active).toBeNull();
    expect(view.result.current.status).toBe("idle");
    expect(view.result.current.error).toBeNull();
  });

  // ── issue #569 follow-up: a heartbeat tick that lands during leave()'s own
  // round trip must never resend call.presence and resurrect the lease
  // call.leave is releasing. The heartbeat has to stop the instant leave()
  // is called, not once it resolves. ──────────────────────────────────────

  it("stops the presence heartbeat synchronously when leave() starts, before call.leave's ack ever arrives", async () => {
    vi.useFakeTimers();
    try {
      const media = fakeMedia();
      const view = renderHook(() => useResourceCallSession(media));
      await act(() => view.result.current.join(channelTarget));
      expect(view.result.current.status).toBe("active");

      const acquireCallsBeforeLeave = vi.mocked(acquireChatSocket).mock.results.length;
      const presenceHandle = vi.mocked(acquireChatSocket).mock.results.at(-1)!.value as {
        send: ReturnType<typeof vi.fn>;
      };
      presenceHandle.send.mockClear();

      let resolveAck!: (value: Call) => void;
      vi.mocked(leaveResourceCall).mockReturnValueOnce(
        new Promise((resolve) => {
          resolveAck = resolve;
        }),
      );

      let leaving!: Promise<void>;
      act(() => {
        leaving = view.result.current.leave();
      });

      // Well past two whole presence intervals while call.leave's ack is
      // still pending: every tick that would have fired must have been
      // silenced by leave(), not merely deferred.
      act(() => {
        vi.advanceTimersByTime(25_000);
      });
      expect(presenceHandle.send).not.toHaveBeenCalled();
      // No replacement heartbeat was started either.
      expect(vi.mocked(acquireChatSocket).mock.results.length).toBe(acquireCallsBeforeLeave);

      await act(async () => {
        resolveAck({ ...resourceCall, status: "ended" });
        await leaving;
      });

      expect(view.result.current.active).toBeNull();
      expect(view.result.current.status).toBe("idle");
      expect(view.result.current.error).toBeNull();
    } finally {
      vi.useRealTimers();
    }
  });

  it("resumes the presence heartbeat normally for a new session after a completed leave() — the timer is never left permanently disabled", async () => {
    vi.useFakeTimers();
    try {
      const media = fakeMedia();
      const view = renderHook(() => useResourceCallSession(media));
      await act(() => view.result.current.join(channelTarget));
      await act(() => view.result.current.leave());
      expect(view.result.current.status).toBe("idle");

      const otherTarget: ResourceCallTarget = {
        ...channelTarget,
        id: "00000000-0000-4000-8000-000000000702",
      };
      const otherCall = {
        ...resourceCall,
        call_id: "00000000-0000-4000-8000-000000000799",
        target_id: otherTarget.id,
      };
      vi.mocked(startResourceCall).mockResolvedValueOnce(otherCall);
      await act(() => view.result.current.join(otherTarget));
      expect(view.result.current.status).toBe("active");

      const presenceHandle = vi.mocked(acquireChatSocket).mock.results.at(-1)!.value as {
        send: ReturnType<typeof vi.fn>;
      };
      presenceHandle.send.mockClear();

      act(() => {
        vi.advanceTimersByTime(10_000);
      });
      expect(presenceHandle.send).toHaveBeenCalledWith({
        type: "call.presence",
        call_id: otherCall.call_id,
      });
    } finally {
      vi.useRealTimers();
    }
  });

  it("unmount stops media without requiring an explicit leave()", async () => {
    const media = fakeMedia();
    const view = renderHook(() => useResourceCallSession(media));
    await act(() => view.result.current.join(channelTarget));
    media.stop.mockClear();

    view.unmount();

    expect(media.stop).toHaveBeenCalledOnce();
  });

  it("ignores a stale token/connect resolution after leave() switched targets", async () => {
    const media = fakeMedia();
    let resolveToken!: (value: { token: string; expiresAt: string; serverUrl: string }) => void;
    vi.mocked(issueCallToken).mockReturnValueOnce(
      new Promise((resolve) => {
        resolveToken = resolve;
      }),
    );
    const view = renderHook(() => useResourceCallSession(media));

    let joining!: Promise<void>;
    act(() => {
      joining = view.result.current.join(channelTarget);
    });
    act(() => {
      view.result.current.leave();
    });
    await act(async () => {
      resolveToken({ token: "late", expiresAt: "2026-01-01T00:00:00Z", serverUrl: "wss://x" });
      await joining;
    });

    expect(media.connect).not.toHaveBeenCalled();
    expect(view.result.current.status).toBe("idle");
  });

  it("a rejoin of the same target waits for the prior leave's cleanup before starting a new Room", async () => {
    const media = fakeMedia();
    let resolveStop!: () => void;
    media.stop.mockReturnValueOnce(
      new Promise<void>((resolve) => {
        resolveStop = resolve;
      }),
    );
    const view = renderHook(() => useResourceCallSession(media));
    await act(() => view.result.current.join(channelTarget));
    media.connect.mockClear();

    act(() => {
      view.result.current.leave();
    });
    let rejoining!: Promise<void>;
    act(() => {
      rejoining = view.result.current.join(channelTarget);
    });

    // The previous leave()'s stop() has not resolved yet: connect() must not
    // have run for the new attempt.
    await act(async () => {
      await Promise.resolve();
      await Promise.resolve();
    });
    expect(media.connect).not.toHaveBeenCalled();

    await act(async () => {
      resolveStop();
      await rejoining;
    });

    expect(media.connect).toHaveBeenCalledOnce();
    expect(view.result.current.status).toBe("active");
  });

  it("a rejoin superseded by a different target while cleanup is still pending never connects for the stale target", async () => {
    const media = fakeMedia();
    let resolveStop!: () => void;
    media.stop.mockReturnValueOnce(
      new Promise<void>((resolve) => {
        resolveStop = resolve;
      }),
    );
    const view = renderHook(() => useResourceCallSession(media));
    await act(() => view.result.current.join(channelTarget));
    media.connect.mockClear();

    act(() => {
      view.result.current.leave();
    });
    act(() => {
      // Stale: superseded by the switch below before cleanup ever resolves.
      void view.result.current.join(channelTarget);
    });
    const otherTarget: ResourceCallTarget = {
      ...channelTarget,
      id: "00000000-0000-4000-8000-000000000702",
    };
    let switching!: Promise<void>;
    act(() => {
      switching = view.result.current.join(otherTarget);
    });

    await act(async () => {
      resolveStop();
      await switching;
    });

    expect(media.connect).toHaveBeenCalledExactlyOnceWith(
      { call_id: resourceCall.call_id, call_type: "audio" },
      "resource-token",
      "wss://livekit-dev.nic-labs.com",
    );
    expect(view.result.current.active).toEqual(otherTarget);
  });

  it("leaving after startAudio() and the token request already settled, while connect() is still pending, never marks the abandoned target active", async () => {
    const media = fakeMedia();
    let resolveConnect!: () => void;
    media.connect.mockReturnValueOnce(
      new Promise<void>((resolve) => {
        resolveConnect = resolve;
      }),
    );
    const view = renderHook(() => useResourceCallSession(media));

    let joining!: Promise<void>;
    act(() => {
      joining = view.result.current.join(channelTarget);
    });
    // Let startAudio(), startResourceCall(), and issueCallToken() (all already-resolved
    // mocks) actually settle, so the attempt is genuinely stalled awaiting
    // connect() — not still behind an earlier step — before leave() runs.
    await act(async () => {
      await Promise.resolve();
      await Promise.resolve();
      await Promise.resolve();
      await Promise.resolve();
    });
    act(() => {
      view.result.current.leave();
    });

    await act(async () => {
      resolveConnect();
      await joining;
    });

    expect(view.result.current.status).toBe("idle");
    expect(view.result.current.active).toBeNull();
  });

  it("leaving while the resource token request is still in flight never lets the stale attempt proceed to connect()", async () => {
    const media = fakeMedia();
    let resolveStartAudio!: () => void;
    media.startAudio.mockReturnValueOnce(
      new Promise<void>((resolve) => {
        resolveStartAudio = resolve;
      }),
    );
    let resolveToken!: (value: { token: string; expiresAt: string; serverUrl: string }) => void;
    vi.mocked(issueCallToken).mockReturnValueOnce(
      new Promise((resolve) => {
        resolveToken = resolve;
      }),
    );
    const view = renderHook(() => useResourceCallSession(media));

    let joining!: Promise<void>;
    act(() => {
      joining = view.result.current.join(channelTarget);
    });
    await act(async () => {
      resolveStartAudio();
      await Promise.resolve();
      await Promise.resolve();
    });
    // startAudio() has settled and the attempt is now awaiting the token
    // request specifically — leave() here must be caught by that step's own
    // guard, distinct from the startAudio-in-flight case above.
    act(() => {
      view.result.current.leave();
    });

    await act(async () => {
      resolveToken({ token: "late", expiresAt: "2026-01-01T00:00:00Z", serverUrl: "wss://x" });
      await joining;
    });

    expect(media.connect).not.toHaveBeenCalled();
    expect(view.result.current.status).toBe("idle");
  });

  it("a leave() before an abandoned connect() rejects never surfaces that error", async () => {
    const media = fakeMedia();
    let rejectConnect!: (error: unknown) => void;
    media.connect.mockReturnValueOnce(
      new Promise<void>((_resolve, reject) => {
        rejectConnect = reject;
      }),
    );
    const view = renderHook(() => useResourceCallSession(media));

    let joining!: Promise<void>;
    act(() => {
      joining = view.result.current.join(channelTarget);
    });
    await act(async () => {
      await Promise.resolve();
      await Promise.resolve();
      await Promise.resolve();
      await Promise.resolve();
    });
    act(() => {
      view.result.current.leave();
    });

    await act(async () => {
      rejectConnect(new Error("connect failed for the abandoned attempt"));
      await joining;
    });

    // leave() already settled this hook into "idle" with no error; the
    // abandoned attempt's own rejection must not overwrite that.
    expect(view.result.current.status).toBe("idle");
    expect(view.result.current.error).toBeNull();
  });

  it("retry after an error performs a real reconnect, not a no-op", async () => {
    const media = fakeMedia();
    vi.mocked(issueCallToken).mockRejectedValueOnce(new Error("token unavailable"));
    const view = renderHook(() => useResourceCallSession(media));
    await act(() => view.result.current.join(channelTarget));
    expect(view.result.current.status).toBe("error");

    await act(() => view.result.current.join(channelTarget));

    expect(issueCallToken).toHaveBeenCalledTimes(2);
    expect(media.connect).toHaveBeenCalledOnce();
    expect(view.result.current.status).toBe("active");
  });

  it("reconnects only an established resource call and surfaces recovery failure", async () => {
    const media = fakeMedia();
    let resolveAudio!: () => void;
    media.startAudio.mockReturnValueOnce(
      new Promise<void>((resolve) => {
        resolveAudio = resolve;
      }),
    );
    const view = renderHook(() => useResourceCallSession(media));
    await act(() => view.result.current.reconnect());

    let joining!: Promise<void>;
    act(() => {
      joining = view.result.current.join(channelTarget);
    });
    await act(() => view.result.current.reconnect());
    await act(async () => {
      resolveAudio();
      await joining;
    });

    media.stop.mockClear();
    media.startAudio.mockClear();
    media.connect.mockClear();
    await act(() => view.result.current.reconnect());
    expect(media.stop).toHaveBeenCalledOnce();
    expect(media.startAudio).toHaveBeenCalledOnce();
    expect(media.connect).toHaveBeenCalledOnce();
    expect(view.result.current.status).toBe("active");

    media.stop.mockRejectedValueOnce(new Error("reconnect failed"));
    let reconnectError: unknown;
    await act(async () => {
      try {
        await view.result.current.reconnect();
      } catch (error) {
        reconnectError = error;
      }
    });
    expect(reconnectError).toBeInstanceOf(Error);
    expect(view.result.current.status).toBe("error");
    expect(view.result.current.error).toContain("recuperar");
  });

  // ── RF-24 × RF-23: a cleanup failure is never mistaken for a completed
  // leave, so a caller relying on "the Room is gone" never proceeds while it
  // actually isn't. ───────────────────────────────────────────────────────

  async function leaveExpectingFailure(view: {
    result: { current: ReturnType<typeof useResourceCallSession> };
  }) {
    let caught: unknown;
    await act(async () => {
      try {
        await view.result.current.leave();
      } catch (error) {
        caught = error;
      }
    });
    return caught;
  }

  it("keeps active set and surfaces a recoverable error when leave()'s cleanup itself fails", async () => {
    const media = fakeMedia();
    media.stop.mockRejectedValueOnce(new Error("stop failed"));
    const view = renderHook(() => useResourceCallSession(media));
    await act(() => view.result.current.join(channelTarget));

    const caught = await leaveExpectingFailure(view);

    expect(caught).toBeInstanceOf(Error);
    expect((caught as Error).message).toBe("stop failed");
    expect(view.result.current.active).toEqual(channelTarget);
    expect(view.result.current.status).toBe("error");
    expect(view.result.current.error).not.toBeNull();
  });

  it("lets the same leave() affordance retry and succeed after a failed cleanup", async () => {
    const media = fakeMedia();
    media.stop.mockRejectedValueOnce(new Error("stop failed"));
    const view = renderHook(() => useResourceCallSession(media));
    await act(() => view.result.current.join(channelTarget));
    await leaveExpectingFailure(view);
    expect(view.result.current.active).toEqual(channelTarget);

    await act(() => view.result.current.leave());

    expect(media.stop).toHaveBeenCalledTimes(2);
    expect(view.result.current.active).toBeNull();
    expect(view.result.current.status).toBe("idle");
    expect(view.result.current.error).toBeNull();
  });

  it("rejects a rejoin attempt while a prior leave's cleanup is still failing, instead of starting a second Room", async () => {
    const media = fakeMedia();
    let rejectStop!: (error: unknown) => void;
    media.stop.mockReturnValueOnce(
      new Promise<void>((_resolve, reject) => {
        rejectStop = reject;
      }),
    );
    const view = renderHook(() => useResourceCallSession(media));
    await act(() => view.result.current.join(channelTarget));
    media.connect.mockClear();

    let leaving!: Promise<void>;
    act(() => {
      leaving = view.result.current.leave().catch(() => undefined);
    });
    let rejoining!: Promise<void>;
    act(() => {
      rejoining = view.result.current.join(channelTarget);
    });

    await act(async () => {
      rejectStop(new Error("stop failed"));
      await Promise.all([leaving, rejoining]);
    });

    // The failed cleanup never resolved, so no new Room is ever started for
    // it — the rejoin attempt itself must also observe that same failure,
    // not silently connect as if the old Room were already gone.
    expect(media.connect).not.toHaveBeenCalled();
    expect(view.result.current.status).toBe("error");
  });

  // ── Pending join → leave → rejoin: a stale pending-join entry must never
  // be deduplicated once leave() has invalidated its generation. ──────────

  it("starts a real new join when the same target is rejoined while the previous join for it was still pending at leave() time", async () => {
    const media = fakeMedia();
    let resolveStartAudio!: () => void;
    media.startAudio.mockReturnValueOnce(
      new Promise<void>((resolve) => {
        resolveStartAudio = resolve;
      }),
    );
    const view = renderHook(() => useResourceCallSession(media));

    act(() => {
      void view.result.current.join(channelTarget);
    });
    act(() => {
      void view.result.current.leave();
    });
    let rejoining!: Promise<void>;
    act(() => {
      rejoining = view.result.current.join(channelTarget);
    });

    await act(async () => {
      resolveStartAudio();
      await rejoining;
    });

    expect(view.result.current.status).toBe("active");
    expect(view.result.current.active).toEqual(channelTarget);
    expect(media.connect).toHaveBeenCalledOnce();
  });

  it("starts a real new join when a rejoin uses a different object with the same kind/id while the previous join was still pending at leave() time", async () => {
    const media = fakeMedia();
    let resolveStartAudio!: () => void;
    media.startAudio.mockReturnValueOnce(
      new Promise<void>((resolve) => {
        resolveStartAudio = resolve;
      }),
    );
    const view = renderHook(() => useResourceCallSession(media));

    act(() => {
      void view.result.current.join(channelTarget);
    });
    act(() => {
      void view.result.current.leave();
    });
    const sameTargetNewObject: ResourceCallTarget = { ...channelTarget };
    let rejoining!: Promise<void>;
    act(() => {
      rejoining = view.result.current.join(sameTargetNewObject);
    });

    await act(async () => {
      resolveStartAudio();
      await rejoining;
    });

    expect(view.result.current.status).toBe("active");
    expect(view.result.current.active).toEqual(sameTargetNewObject);
    expect(media.connect).toHaveBeenCalledOnce();
  });

  it("never lets a stale, superseded join generation touch active/status/error/target once it finally settles", async () => {
    const media = fakeMedia();
    let resolveStartAudio!: () => void;
    media.startAudio.mockReturnValueOnce(
      new Promise<void>((resolve) => {
        resolveStartAudio = resolve;
      }),
    );
    const view = renderHook(() => useResourceCallSession(media));

    let staleJoin!: Promise<void>;
    act(() => {
      staleJoin = view.result.current.join(channelTarget);
    });
    act(() => {
      void view.result.current.leave();
    });
    let latestJoin!: Promise<void>;
    act(() => {
      latestJoin = view.result.current.join(channelTarget);
    });

    await act(async () => {
      await latestJoin;
    });
    expect(view.result.current.status).toBe("active");
    const activeAfterLatest = view.result.current.active;

    await act(async () => {
      resolveStartAudio();
      await staleJoin;
    });

    expect(view.result.current.active).toEqual(activeAfterLatest);
    expect(view.result.current.status).toBe("active");
    expect(view.result.current.error).toBeNull();
  });

  // ── errorOperation discriminates join failures from leave failures, so a
  // retry affordance replays the operation that actually failed. ──────────

  describe("errorOperation", () => {
    it("is 'join' after a join failure and clears on the next successful join", async () => {
      const media = fakeMedia();
      vi.mocked(issueCallToken).mockRejectedValueOnce(new Error("token unavailable"));
      const view = renderHook(() => useResourceCallSession(media));

      await act(() => view.result.current.join(channelTarget));
      expect(view.result.current.status).toBe("error");
      expect(view.result.current.errorOperation).toBe("join");

      await act(() => view.result.current.join(channelTarget));
      expect(view.result.current.status).toBe("active");
      expect(view.result.current.errorOperation).toBeNull();
    });

    it("is 'leave' after a leave failure and clears once leave() succeeds", async () => {
      const media = fakeMedia();
      media.stop.mockRejectedValueOnce(new Error("stop failed"));
      const view = renderHook(() => useResourceCallSession(media));
      await act(() => view.result.current.join(channelTarget));

      await act(async () => {
        await view.result.current.leave().catch(() => undefined);
      });
      expect(view.result.current.status).toBe("error");
      expect(view.result.current.errorOperation).toBe("leave");
      expect(view.result.current.active).toEqual(channelTarget);

      await act(() => view.result.current.leave());
      expect(view.result.current.status).toBe("idle");
      expect(view.result.current.errorOperation).toBeNull();
    });
  });
});
