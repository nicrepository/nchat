import { act, renderHook } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";

import { issueCallToken } from "./callApi";
import type { Call } from "./callState";
import { acquireChatSocket } from "./chatSocket";
import {
  joinResourceCall,
  leaveResourceCall,
  ResourceCallSignalingError,
  startResourceCall,
  type ResourceCallAdmission,
  type ResourceCallLeaveResult,
} from "./resourceCallSignaling";
import type { CallMediaBridge } from "./useCallSignaling";
import { useResourceCallSession, type ResourceCallTarget } from "./useResourceCallSession";

vi.mock("./callApi", () => ({
  issueCallToken: vi.fn(),
}));
vi.mock("./resourceCallSignaling", async (importOriginal) => ({
  ...(await importOriginal<typeof import("./resourceCallSignaling")>()),
  startResourceCall: vi.fn(),
  joinResourceCall: vi.fn(),
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
const participationID = "00000000-0000-4000-8000-000000000708";
const resourceAdmission = (call: Call = resourceCall, participationId = participationID) => ({
  call,
  participationId,
});

const knownCallTarget: ResourceCallTarget = { ...channelTarget, callId: resourceCall.call_id };

beforeEach(() => {
  vi.mocked(startResourceCall).mockReset();
  vi.mocked(startResourceCall).mockResolvedValue(resourceAdmission());
  vi.mocked(joinResourceCall).mockReset();
  vi.mocked(joinResourceCall).mockResolvedValue(resourceAdmission());
  vi.mocked(issueCallToken).mockReset();
  vi.mocked(issueCallToken).mockResolvedValue({
    token: "resource-token",
    expiresAt: "2026-01-01T00:00:00Z",
    serverUrl: "wss://livekit-dev.nic-labs.com",
  });
  vi.mocked(leaveResourceCall).mockReset();
  vi.mocked(leaveResourceCall).mockResolvedValue({
    released: true,
    call: { ...resourceCall, status: "ended" },
  });
});

describe("useResourceCallSession", () => {
  it("joins a channel room: unlocks audio, fetches a resource token, connects, goes active", async () => {
    const media = fakeMedia();
    const view = renderHook(() => useResourceCallSession(media));

    await act(() => view.result.current.join(channelTarget, "fresh"));

    expect(media.startAudio).toHaveBeenCalledOnce();
    expect(startResourceCall).toHaveBeenCalledExactlyOnceWith(channelTarget);
    expect(issueCallToken).toHaveBeenCalledExactlyOnceWith(resourceCall.call_id, participationID);
    expect(media.connect).toHaveBeenCalledExactlyOnceWith(
      { call_id: resourceCall.call_id, call_type: "audio" },
      "resource-token",
      "wss://livekit-dev.nic-labs.com",
      "fresh",
    );
    expect(view.result.current.status).toBe("active");
    expect(view.result.current.active).toEqual(channelTarget);
    expect(view.result.current.error).toBeNull();
  });

  it("joins a group DM room with resource_kind dm", async () => {
    const media = fakeMedia();
    const view = renderHook(() => useResourceCallSession(media));
    const groupTarget: ResourceCallTarget = { ...channelTarget, kind: "dm" };

    await act(() => view.result.current.join(groupTarget, "fresh"));

    expect(startResourceCall).toHaveBeenCalledExactlyOnceWith(groupTarget);
    expect(issueCallToken).toHaveBeenCalledExactlyOnceWith(resourceCall.call_id, participationID);
    expect(media.connect).toHaveBeenCalledExactlyOnceWith(
      { call_id: resourceCall.call_id, call_type: "audio" },
      "resource-token",
      "wss://livekit-dev.nic-labs.com",
      "fresh",
    );
  });

  it("never lets the camera auto-enable: call_type stays 'audio' regardless of target (RF-24 follow-up)", async () => {
    const media = fakeMedia();
    const view = renderHook(() => useResourceCallSession(media));

    await act(() => view.result.current.join(channelTarget, "fresh"));

    expect(media.connect).toHaveBeenCalledExactlyOnceWith(
      expect.objectContaining({ call_type: "audio" }),
      "resource-token",
      "wss://livekit-dev.nic-labs.com",
      "fresh",
    );
  });

  it("reports an error and leaves status idle-adjacent when the token request fails", async () => {
    const media = fakeMedia();
    vi.mocked(issueCallToken).mockRejectedValueOnce(new Error("unauthorized"));
    const view = renderHook(() => useResourceCallSession(media));

    await act(() => view.result.current.join(channelTarget, "fresh"));

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

    let first!: Promise<string | undefined>;
    let second!: Promise<string | undefined>;
    act(() => {
      first = view.result.current.join(channelTarget, "fresh");
      second = view.result.current.join(channelTarget, "fresh");
    });
    await act(async () => {
      resolveToken({ token: "t", expiresAt: "2026-01-01T00:00:00Z", serverUrl: "wss://x" });
      await Promise.all([first, second]);
    });

    expect(issueCallToken).toHaveBeenCalledOnce();
  });

  it("does not dedup a concurrent join for the same target across mismatched modes — no caller silently inherits another's snapshot mode (issue #610 audit)", async () => {
    const media = fakeMedia();
    const view = renderHook(() => useResourceCallSession(media));

    let recovery!: Promise<string | undefined>;
    let fresh!: Promise<string | undefined>;
    act(() => {
      recovery = view.result.current.join(channelTarget, "recovery");
      fresh = view.result.current.join(channelTarget, "fresh");
    });

    // Deduping on target alone (the pre-fix behavior) would return the
    // exact same promise to both callers here.
    expect(recovery).not.toBe(fresh);

    await act(async () => {
      await Promise.all([recovery, fresh]);
    });

    // The superseded "recovery" attempt never reaches its own connect() —
    // only the current ("fresh") attempt does, and with its own mode. If
    // dedup had merged them, this would instead be called once with
    // "recovery", and `fresh` would have resolved to whatever the
    // recovery attempt resolved to.
    expect(media.connect).toHaveBeenCalledExactlyOnceWith(
      expect.anything(),
      expect.anything(),
      expect.anything(),
      "fresh",
    );
    expect(await recovery).toBeUndefined();
    expect(await fresh).toBe(resourceCall.call_id);
  });

  it("rejects a different local join without touching the active resource session", async () => {
    vi.useFakeTimers();
    try {
      const media = fakeMedia();
      const view = renderHook(() => useResourceCallSession(media));
      await act(() => view.result.current.join(channelTarget, "fresh"));
      const originalCallId = view.result.current.callId;
      const acquireCount = vi.mocked(acquireChatSocket).mock.results.length;
      const presenceHandle = vi.mocked(acquireChatSocket).mock.results.at(-1)!.value as {
        send: ReturnType<typeof vi.fn>;
      };
      presenceHandle.send.mockClear();
      vi.mocked(startResourceCall).mockClear();
      media.startAudio.mockClear();
      media.connect.mockClear();
      media.stop.mockClear();

      await act(() =>
        view.result.current.join(
          {
            kind: "channel",
            id: "00000000-0000-4000-8000-000000000702",
            name: "outro",
          },
          "fresh",
        ),
      );

      expect(startResourceCall).not.toHaveBeenCalled();
      expect(media.startAudio).not.toHaveBeenCalled();
      expect(media.connect).not.toHaveBeenCalled();
      expect(media.stop).not.toHaveBeenCalled();
      expect(vi.mocked(acquireChatSocket).mock.results.length).toBe(acquireCount);
      expect(view.result.current.active).toEqual(channelTarget);
      expect(view.result.current.callId).toBe(originalCallId);
      expect(view.result.current.status).toBe("active");

      act(() => vi.advanceTimersByTime(10_000));
      expect(presenceHandle.send).toHaveBeenCalledWith({
        type: "call.presence",
        call_id: originalCallId,
        participation_id: participationID,
      });
    } finally {
      vi.useRealTimers();
    }
  });

  it("stops stale presence and clears only the local fenced session", async () => {
    const media = fakeMedia();
    const view = renderHook(() => useResourceCallSession(media));
    await act(() => view.result.current.join(channelTarget, "fresh"));
    const listener = vi.mocked(acquireChatSocket).mock.calls.at(-1)![0];
    media.stop.mockClear();

    act(() =>
      listener.onMessage?.(
        {
          type: "call.error",
          operation: "call.presence",
          call_id: resourceCall.call_id,
          code: "call_participation_stale",
        },
        1,
      ),
    );

    expect(media.stop).toHaveBeenCalledOnce();
    expect(view.result.current.status).toBe("idle");
    expect(view.result.current.callId).toBeNull();
  });

  it("keeps a same-target rejoin legitimate", async () => {
    const media = fakeMedia();
    const view = renderHook(() => useResourceCallSession(media));
    await act(() => view.result.current.join(channelTarget, "fresh"));
    vi.mocked(startResourceCall).mockClear();

    await act(() => view.result.current.join({ ...channelTarget }, "fresh"));

    expect(startResourceCall).toHaveBeenCalledExactlyOnceWith({ ...channelTarget });
    expect(view.result.current.active).toEqual(channelTarget);
    expect(view.result.current.callId).toBe(resourceCall.call_id);
  });

  it("rolls back only a stale fresh attempt rejected as participant busy", async () => {
    const media = fakeMedia();
    vi.mocked(startResourceCall).mockRejectedValueOnce(
      new ResourceCallSignalingError("call.start", "call_participant_busy"),
    );
    const view = renderHook(() => useResourceCallSession(media));

    await act(() => view.result.current.join(channelTarget, "fresh"));

    expect(view.result.current.active).toBeNull();
    expect(view.result.current.callId).toBeNull();
    expect(view.result.current.status).toBe("error");
    expect(view.result.current.error).toBe("Você já está em outra chamada.");
    expect(media.connect).not.toHaveBeenCalled();
    expect(media.stop).not.toHaveBeenCalled();
  });

  it("restores an existing same-target session when stale server state rejects its rejoin as busy", async () => {
    const media = fakeMedia();
    const view = renderHook(() => useResourceCallSession(media));
    await act(() => view.result.current.join(channelTarget, "fresh"));
    const originalCallId = view.result.current.callId;
    vi.mocked(startResourceCall).mockRejectedValueOnce(
      new ResourceCallSignalingError("call.start", "call_participant_busy"),
    );
    media.connect.mockClear();
    media.stop.mockClear();

    await act(() => view.result.current.join({ ...channelTarget }, "fresh"));

    expect(view.result.current.active).toEqual(channelTarget);
    expect(view.result.current.callId).toBe(originalCallId);
    expect(view.result.current.status).toBe("active");
    expect(view.result.current.error).toBe("Você já está em outra chamada.");
    expect(media.connect).not.toHaveBeenCalled();
    expect(media.stop).not.toHaveBeenCalled();
  });

  // ── issue #569: leave() must release this participant's server-side
  // participation (a call.leave signal), never just clean up locally and
  // never end the resource call for anyone else. ─────────────────────────

  it("leave() stops media locally and releases this participant's own server-side presence", async () => {
    const media = fakeMedia();
    const view = renderHook(() => useResourceCallSession(media));
    await act(() => view.result.current.join(channelTarget, "fresh"));

    await act(() => view.result.current.leave());

    expect(media.stop).toHaveBeenCalledOnce();
    expect(leaveResourceCall).toHaveBeenCalledExactlyOnceWith(
      resourceCall.call_id,
      participationID,
    );
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

    let joining!: Promise<string | undefined>;
    act(() => {
      joining = view.result.current.join(channelTarget, "fresh");
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
    await act(() => view.result.current.join(channelTarget, "fresh"));

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
    await act(() => view.result.current.join(channelTarget, "fresh"));

    await act(() => view.result.current.leave());

    // The hook's only server-facing calls are startResourceCall (join) and
    // leaveResourceCall (leave) — there is no call.end/global-end affordance
    // in this hook's surface at all, so a passing join+leave already proves
    // leave() never reached for one.
    expect(startResourceCall).toHaveBeenCalledOnce();
    expect(leaveResourceCall).toHaveBeenCalledExactlyOnceWith(
      resourceCall.call_id,
      participationID,
    );
  });

  it("a duplicated/retried leave() is safe: both settle and never leave a stuck pending state", async () => {
    const media = fakeMedia();
    const view = renderHook(() => useResourceCallSession(media));
    await act(() => view.result.current.join(channelTarget, "fresh"));

    let first!: Promise<boolean>;
    let second!: Promise<boolean>;
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
      await act(() => view.result.current.join(channelTarget, "fresh"));
      expect(view.result.current.status).toBe("active");

      const acquireCallsBeforeLeave = vi.mocked(acquireChatSocket).mock.results.length;
      const presenceHandle = vi.mocked(acquireChatSocket).mock.results.at(-1)!.value as {
        send: ReturnType<typeof vi.fn>;
      };
      presenceHandle.send.mockClear();

      let resolveAck!: (value: ResourceCallLeaveResult) => void;
      vi.mocked(leaveResourceCall).mockReturnValueOnce(
        new Promise((resolve) => {
          resolveAck = resolve;
        }),
      );

      let leaving!: Promise<boolean>;
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
        resolveAck({ released: true, call: { ...resourceCall, status: "ended" } });
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
      await act(() => view.result.current.join(channelTarget, "fresh"));
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
      vi.mocked(startResourceCall).mockResolvedValueOnce(resourceAdmission(otherCall));
      await act(() => view.result.current.join(otherTarget, "fresh"));
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
        participation_id: participationID,
      });
    } finally {
      vi.useRealTimers();
    }
  });

  it("unmount stops media without requiring an explicit leave()", async () => {
    const media = fakeMedia();
    const view = renderHook(() => useResourceCallSession(media));
    await act(() => view.result.current.join(channelTarget, "fresh"));
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

    let joining!: Promise<string | undefined>;
    act(() => {
      joining = view.result.current.join(channelTarget, "fresh");
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
    await act(() => view.result.current.join(channelTarget, "fresh"));
    media.connect.mockClear();

    act(() => {
      view.result.current.leave();
    });
    let rejoining!: Promise<string | undefined>;
    act(() => {
      rejoining = view.result.current.join(channelTarget, "fresh");
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
    await act(() => view.result.current.join(channelTarget, "fresh"));
    media.connect.mockClear();

    act(() => {
      view.result.current.leave();
    });
    act(() => {
      // Stale: superseded by the switch below before cleanup ever resolves.
      void view.result.current.join(channelTarget, "fresh");
    });
    const otherTarget: ResourceCallTarget = {
      ...channelTarget,
      id: "00000000-0000-4000-8000-000000000702",
    };
    let switching!: Promise<string | undefined>;
    act(() => {
      switching = view.result.current.join(otherTarget, "fresh");
    });

    await act(async () => {
      resolveStop();
      await switching;
    });

    expect(media.connect).toHaveBeenCalledExactlyOnceWith(
      { call_id: resourceCall.call_id, call_type: "audio" },
      "resource-token",
      "wss://livekit-dev.nic-labs.com",
      "fresh",
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

    let joining!: Promise<string | undefined>;
    act(() => {
      joining = view.result.current.join(channelTarget, "fresh");
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

    let joining!: Promise<string | undefined>;
    act(() => {
      joining = view.result.current.join(channelTarget, "fresh");
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

    let joining!: Promise<string | undefined>;
    act(() => {
      joining = view.result.current.join(channelTarget, "fresh");
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
    await act(() => view.result.current.join(channelTarget, "fresh"));
    expect(view.result.current.status).toBe("error");

    await act(() => view.result.current.join(channelTarget, "fresh"));

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

    let joining!: Promise<string | undefined>;
    act(() => {
      joining = view.result.current.join(channelTarget, "fresh");
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
    // §14: resource.reconnect() is unconditionally "recovery" — never a
    // parameter, never inferred from anything else.
    expect(media.connect).toHaveBeenCalledWith(
      expect.anything(),
      expect.anything(),
      expect.anything(),
      "recovery",
    );
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

  it("re-admits on every reclaim and uses the new P2/P3 before each token", async () => {
    const media = fakeMedia();
    const view = renderHook(() => useResourceCallSession(media));
    await act(() => view.result.current.join(knownCallTarget, "fresh"));

    const participationP2 = "00000000-0000-4000-8000-000000000721";
    const participationP3 = "00000000-0000-4000-8000-000000000722";
    vi.mocked(joinResourceCall).mockResolvedValueOnce(
      resourceAdmission(resourceCall, participationP2),
    );
    await act(() => view.result.current.reconnect());
    vi.mocked(joinResourceCall).mockResolvedValueOnce(
      resourceAdmission(resourceCall, participationP3),
    );
    await act(() => view.result.current.reconnect());

    expect(joinResourceCall).toHaveBeenCalledTimes(3);
    expect(issueCallToken).toHaveBeenNthCalledWith(2, resourceCall.call_id, participationP2);
    expect(issueCallToken).toHaveBeenNthCalledWith(3, resourceCall.call_id, participationP3);
  });

  it("preserves the reconnect admission P when its compensation leave fails", async () => {
    const media = fakeMedia();
    const view = renderHook(() => useResourceCallSession(media));
    await act(() => view.result.current.join(knownCallTarget, "fresh"));

    const participationP2 = "00000000-0000-4000-8000-000000000723";
    vi.mocked(joinResourceCall).mockResolvedValueOnce(
      resourceAdmission(resourceCall, participationP2),
    );
    vi.mocked(issueCallToken).mockRejectedValueOnce(new Error("token unavailable"));
    vi.mocked(leaveResourceCall).mockRejectedValueOnce(new Error("compensation failed"));

    await act(() => view.result.current.reconnect().catch(() => undefined));

    expect(view.result.current.status).toBe("error");
    expect(view.result.current.errorOperation).toBe("leave");
    expect(view.result.current.callId).toBe(resourceCall.call_id);
    expect(view.result.current.active).toEqual(knownCallTarget);

    vi.mocked(leaveResourceCall).mockResolvedValueOnce({
      released: true,
      call: { ...resourceCall, status: "ended" },
    });
    await act(() => view.result.current.leave());
    expect(leaveResourceCall).toHaveBeenLastCalledWith(resourceCall.call_id, participationP2);
    expect(view.result.current.status).toBe("idle");
  });

  it("a stale explicit leave clears only this local session and reports released=false", async () => {
    const media = fakeMedia();
    const view = renderHook(() => useResourceCallSession(media));
    await act(() => view.result.current.join(channelTarget, "fresh"));
    vi.mocked(leaveResourceCall).mockResolvedValueOnce({ released: false });

    let released = true;
    await act(async () => {
      released = await view.result.current.leave();
    });

    expect(released).toBe(false);
    expect(leaveResourceCall).toHaveBeenCalledWith(resourceCall.call_id, participationID);
    expect(media.stop).toHaveBeenCalled();
    expect(view.result.current.status).toBe("idle");
    expect(view.result.current.callId).toBeNull();
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
    await act(() => view.result.current.join(channelTarget, "fresh"));

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
    await act(() => view.result.current.join(channelTarget, "fresh"));
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
    await act(() => view.result.current.join(channelTarget, "fresh"));
    media.connect.mockClear();

    let leaving!: Promise<boolean | undefined>;
    act(() => {
      leaving = view.result.current.leave().catch(() => undefined);
    });
    let rejoining!: Promise<string | undefined>;
    act(() => {
      rejoining = view.result.current.join(channelTarget, "fresh");
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
      void view.result.current.join(channelTarget, "fresh");
    });
    act(() => {
      void view.result.current.leave();
    });
    let rejoining!: Promise<string | undefined>;
    act(() => {
      rejoining = view.result.current.join(channelTarget, "fresh");
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
      void view.result.current.join(channelTarget, "fresh");
    });
    act(() => {
      void view.result.current.leave();
    });
    const sameTargetNewObject: ResourceCallTarget = { ...channelTarget };
    let rejoining!: Promise<string | undefined>;
    act(() => {
      rejoining = view.result.current.join(sameTargetNewObject, "fresh");
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

    let staleJoin!: Promise<string | undefined>;
    act(() => {
      staleJoin = view.result.current.join(channelTarget, "fresh");
    });
    act(() => {
      void view.result.current.leave();
    });
    let latestJoin!: Promise<string | undefined>;
    act(() => {
      latestJoin = view.result.current.join(channelTarget, "fresh");
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

      await act(() => view.result.current.join(channelTarget, "fresh"));
      expect(view.result.current.status).toBe("error");
      expect(view.result.current.errorOperation).toBe("join");

      await act(() => view.result.current.join(channelTarget, "fresh"));
      expect(view.result.current.status).toBe("active");
      expect(view.result.current.errorOperation).toBeNull();
    });

    it("is 'leave' after a leave failure and clears once leave() succeeds", async () => {
      const media = fakeMedia();
      media.stop.mockRejectedValueOnce(new Error("stop failed"));
      const view = renderHook(() => useResourceCallSession(media));
      await act(() => view.result.current.join(channelTarget, "fresh"));

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

  // ── issue #594: converging a participation another tab already ended
  // server-side must clear this session locally, without ever sending
  // another call.leave — and must never let a stale in-flight join/reconnect
  // resurrect it afterward. ────────────────────────────────────────────────

  describe("convergeRemoteLeave", () => {
    it("clears active/callId/status to idle without ever calling leaveResourceCall or stopping media again (already-settled participation)", async () => {
      const media = fakeMedia();
      const view = renderHook(() => useResourceCallSession(media));
      await act(() => view.result.current.join(channelTarget, "fresh"));
      expect(view.result.current.status).toBe("active");
      media.stop.mockClear();

      act(() => view.result.current.convergeRemoteLeave(resourceCall.call_id));

      expect(view.result.current.active).toBeNull();
      expect(view.result.current.callId).toBeNull();
      expect(view.result.current.status).toBe("idle");
      expect(view.result.current.error).toBeNull();
      expect(leaveResourceCall).not.toHaveBeenCalled();
      // Nothing was in flight for this hook at convergence time — a settled
      // "active" participation's media is either already torn down by
      // whatever made it stale (e.g. an ownership handoff) or, if this tab
      // still legitimately owned it, is someone else's responsibility to
      // stop — convergeRemoteLeave must not reach for the shared media
      // bridge just because it once had a session.
      expect(media.stop).not.toHaveBeenCalled();
    });

    it("no-ops for a callId that doesn't match this hook's own current participation", async () => {
      const media = fakeMedia();
      const view = renderHook(() => useResourceCallSession(media));
      await act(() => view.result.current.join(channelTarget, "fresh"));

      act(() => view.result.current.convergeRemoteLeave("00000000-0000-4000-8000-000000000999"));

      expect(view.result.current.active).toEqual(channelTarget);
      expect(view.result.current.callId).toBe(resourceCall.call_id);
      expect(view.result.current.status).toBe("active");
    });

    it("no-ops when nothing is active", () => {
      const media = fakeMedia();
      const view = renderHook(() => useResourceCallSession(media));

      act(() => view.result.current.convergeRemoteLeave(resourceCall.call_id));

      expect(view.result.current.active).toBeNull();
      expect(view.result.current.status).toBe("idle");
      expect(media.stop).not.toHaveBeenCalled();
    });

    // ── adversarial follow-up: convergeRemoteLeave must use a synchronous
    // ref, never the `callId` React state closure, so it can never observe
    // a stale/old callId at the exact moment a cross-tab "left" for THIS
    // attempt's own, just-claimed callId is accepted — and it must actually
    // tear down a still-connecting media session, never just the outer
    // active/status/callId bookkeeping, so no LiveKit Room is ever left
    // running in the background. ──────────────────────────────────────────

    it("a 'left' that arrives before callId is known (startAudio still pending) never converges and never blocks the join from completing normally", async () => {
      const media = fakeMedia();
      let resolveStartAudio!: () => void;
      media.startAudio.mockReturnValueOnce(
        new Promise<void>((resolve) => {
          resolveStartAudio = resolve;
        }),
      );
      const view = renderHook(() => useResourceCallSession(media));

      let joining!: Promise<string | undefined>;
      act(() => {
        joining = view.result.current.join(channelTarget, "fresh");
      });
      expect(view.result.current.callId).toBeNull();

      // Nothing this hook has claimed yet — a "left" for the callId this
      // join() is ABOUT to claim must be a pure no-op, never interfering
      // with the attempt already under way.
      act(() => view.result.current.convergeRemoteLeave(resourceCall.call_id));
      expect(view.result.current.status).toBe("connecting");

      await act(async () => {
        resolveStartAudio();
        await joining;
      });

      expect(view.result.current.status).toBe("active");
      expect(view.result.current.callId).toBe(resourceCall.call_id);
    });

    it("a 'left' accepted the INSTANT callId becomes authoritative (same tick as setCallId, before startResourceCall's caller could ever re-render) still converges — proving the ref, not the callId state closure, is what's read", async () => {
      const media = fakeMedia();
      let resolveToken!: (value: { token: string; expiresAt: string; serverUrl: string }) => void;
      vi.mocked(issueCallToken).mockReturnValueOnce(
        new Promise((resolve) => {
          resolveToken = resolve;
        }),
      );
      const view = renderHook(() => useResourceCallSession(media));

      let joining!: Promise<string | undefined>;
      act(() => {
        joining = view.result.current.join(channelTarget, "fresh");
      });
      // Only startAudio() (an already-resolved mock) has had a chance to
      // settle — callId was just claimed synchronously in that same
      // continuation, and the attempt is now genuinely stalled awaiting
      // issueCallToken(), the very next step.
      await act(async () => {
        await Promise.resolve();
      });

      // A single accepted "left" here must fully converge — not merely
      // "eventually, once a duplicate redelivers it": the ordering guard
      // upstream (CallSessionProvider's commitParticipation) already
      // consumes the message on this first delivery, so if this call missed
      // it (e.g. by reading a stale `callId` closure instead of a ref), the
      // session would be stuck active forever with no second chance.
      act(() => view.result.current.convergeRemoteLeave(resourceCall.call_id));
      expect(view.result.current.status).toBe("idle");
      expect(view.result.current.active).toBeNull();
      expect(view.result.current.callId).toBeNull();

      await act(async () => {
        resolveToken({ token: "late", expiresAt: "2026-01-01T00:00:00Z", serverUrl: "wss://x" });
        await joining;
      });

      // The late token/connect continuation must never resurrect it.
      expect(view.result.current.status).toBe("idle");
      expect(view.result.current.active).toBeNull();
      expect(view.result.current.callId).toBeNull();
      expect(media.connect).not.toHaveBeenCalled();
    });

    it("invalidates a join still in flight, tears down its still-pending media.connect() (no ghost Room), and never resurrects on late resolution", async () => {
      const media = fakeMedia();
      let resolveConnect!: () => void;
      media.connect.mockReturnValueOnce(
        new Promise<void>((resolve) => {
          resolveConnect = resolve;
        }),
      );
      const view = renderHook(() => useResourceCallSession(media));

      let joining!: Promise<string | undefined>;
      act(() => {
        joining = view.result.current.join(channelTarget, "fresh");
      });
      // Let startResourceCall/issueCallToken (already-resolved mocks) settle
      // so callId is known and the attempt is genuinely stalled at connect().
      await act(async () => {
        await Promise.resolve();
        await Promise.resolve();
        await Promise.resolve();
      });
      expect(view.result.current.callId).toBe(resourceCall.call_id);
      media.stop.mockClear();

      act(() => view.result.current.convergeRemoteLeave(resourceCall.call_id));
      expect(view.result.current.status).toBe("idle");
      // The in-flight media.connect() is torn down immediately, not merely
      // ignored once it eventually settles — this is what actually prevents
      // a live LiveKit Room from being left connected in the background.
      expect(media.stop).toHaveBeenCalledOnce();

      await act(async () => {
        resolveConnect();
        await joining;
      });

      expect(view.result.current.status).toBe("idle");
      expect(view.result.current.active).toBeNull();
      expect(view.result.current.callId).toBeNull();
    });

    it("invalidates a reconnect() still in flight, tears down its still-pending media.connect() (no ghost Room), and never resurrects on late resolution", async () => {
      const media = fakeMedia();
      const view = renderHook(() => useResourceCallSession(media));
      await act(() => view.result.current.join(channelTarget, "fresh"));

      let resolveConnect!: () => void;
      media.connect.mockReturnValueOnce(
        new Promise<void>((resolve) => {
          resolveConnect = resolve;
        }),
      );
      let reconnecting!: Promise<void>;
      act(() => {
        reconnecting = view.result.current.reconnect().catch(() => undefined);
      });
      media.stop.mockClear();

      act(() => view.result.current.convergeRemoteLeave(resourceCall.call_id));
      expect(view.result.current.status).toBe("idle");
      expect(media.stop).toHaveBeenCalled();

      await act(async () => {
        resolveConnect();
        await reconnecting;
      });

      expect(view.result.current.status).toBe("idle");
      expect(view.result.current.active).toBeNull();
      expect(view.result.current.callId).toBeNull();
    });

    it("a second join() for the SAME callId after convergence starts a real new attempt, unaffected by the prior convergence's now-superseded generation", async () => {
      const media = fakeMedia();
      const view = renderHook(() => useResourceCallSession(media));
      await act(() => view.result.current.join(channelTarget, "fresh"));
      act(() => view.result.current.convergeRemoteLeave(resourceCall.call_id));
      expect(view.result.current.status).toBe("idle");
      media.connect.mockClear();

      await act(() => view.result.current.join(channelTarget, "fresh"));

      expect(view.result.current.status).toBe("active");
      expect(view.result.current.callId).toBe(resourceCall.call_id);
      expect(media.connect).toHaveBeenCalledOnce();
    });
  });

  // ── issue #622 round 2: a known call_id is obligatory admission via
  // call.join — never a locally-synthesised Call, never a shortcut around
  // the lease media-service's resource token now requires. ───────────────

  describe("known call_id admission (issue #622 round 2)", () => {
    it("1. a fresh target (no call_id) uses call.start", async () => {
      const media = fakeMedia();
      const view = renderHook(() => useResourceCallSession(media));
      await act(() => view.result.current.join(channelTarget, "fresh"));
      expect(startResourceCall).toHaveBeenCalledExactlyOnceWith(channelTarget);
      expect(joinResourceCall).not.toHaveBeenCalled();
    });

    it("2. a known call_id uses call.join, never call.start", async () => {
      const media = fakeMedia();
      const view = renderHook(() => useResourceCallSession(media));
      await act(() => view.result.current.join(knownCallTarget, "fresh"));
      expect(joinResourceCall).toHaveBeenCalledExactlyOnceWith(
        resourceCall.call_id,
        knownCallTarget,
      );
      expect(startResourceCall).not.toHaveBeenCalled();
      expect(view.result.current.status).toBe("active");
      expect(view.result.current.callId).toBe(resourceCall.call_id);
    });

    it("3. mode is passed through as given, independent of whether target.callId is known (issue #610)", async () => {
      // A known target.callId (the common re-entry case: "Chamada"/"Entrar
      // na chamada" on a resource that already has an active call) must
      // still be classified FRESH when the caller says so — never inferred
      // from target.callId's mere presence.
      const freshMedia = fakeMedia();
      const freshView = renderHook(() => useResourceCallSession(freshMedia));
      await act(() => freshView.result.current.join(knownCallTarget, "fresh"));
      expect(freshMedia.connect).toHaveBeenCalledExactlyOnceWith(
        { call_id: resourceCall.call_id, call_type: "audio" },
        "resource-token",
        "wss://livekit-dev.nic-labs.com",
        "fresh",
      );

      // Conversely, a dedicated-tab activation passes "recovery" for the
      // exact same knownCallTarget.
      const recoveryMedia = fakeMedia();
      const recoveryView = renderHook(() => useResourceCallSession(recoveryMedia));
      await act(() => recoveryView.result.current.join(knownCallTarget, "recovery"));
      expect(recoveryMedia.connect).toHaveBeenCalledExactlyOnceWith(
        { call_id: resourceCall.call_id, call_type: "audio" },
        "resource-token",
        "wss://livekit-dev.nic-labs.com",
        "recovery",
      );
    });

    it("3. a known call_id never skips admission — a token is never requested before call.join actually resolves", async () => {
      const media = fakeMedia();
      let resolveJoin!: (admission: ResourceCallAdmission) => void;
      vi.mocked(joinResourceCall).mockReturnValueOnce(
        new Promise((resolve) => {
          resolveJoin = resolve;
        }),
      );
      const view = renderHook(() => useResourceCallSession(media));
      const joining = act(() => view.result.current.join(knownCallTarget, "fresh"));

      expect(issueCallToken).not.toHaveBeenCalled();
      resolveJoin(resourceAdmission());
      await joining;
      expect(issueCallToken).toHaveBeenCalledExactlyOnceWith(resourceCall.call_id, participationID);
    });

    it("4. startAudio runs before the network admission round trip, for both fresh and known targets", async () => {
      const media = fakeMedia();
      const order: string[] = [];
      media.startAudio.mockImplementation(async () => {
        order.push("startAudio");
      });
      vi.mocked(joinResourceCall).mockImplementationOnce(async () => {
        order.push("call.join");
        return resourceAdmission();
      });
      const view = renderHook(() => useResourceCallSession(media));
      await act(() => view.result.current.join(knownCallTarget, "fresh"));
      expect(order).toEqual(["startAudio", "call.join"]);
    });

    it("5. a token is requested only after call.admitted (call.join) resolves", async () => {
      const media = fakeMedia();
      const order: string[] = [];
      vi.mocked(joinResourceCall).mockImplementationOnce(async () => {
        order.push("call.join");
        return resourceAdmission();
      });
      vi.mocked(issueCallToken).mockImplementationOnce(async () => {
        order.push("token");
        return { token: "t", expiresAt: "2026-01-01T00:00:00Z", serverUrl: "wss://x" };
      });
      const view = renderHook(() => useResourceCallSession(media));
      await act(() => view.result.current.join(knownCallTarget, "fresh"));
      expect(order).toEqual(["call.join", "token"]);
    });

    it("6. media.connect runs only after the token resolves", async () => {
      const media = fakeMedia();
      const order: string[] = [];
      vi.mocked(issueCallToken).mockImplementationOnce(async () => {
        order.push("token");
        return { token: "t", expiresAt: "2026-01-01T00:00:00Z", serverUrl: "wss://x" };
      });
      media.connect.mockImplementationOnce(async () => {
        order.push("connect");
      });
      const view = renderHook(() => useResourceCallSession(media));
      await act(() => view.result.current.join(knownCallTarget, "fresh"));
      expect(order).toEqual(["token", "connect"]);
    });

    it("7. a token failure after admission compensates with call.leave for the admitted call_id", async () => {
      const media = fakeMedia();
      vi.mocked(issueCallToken).mockRejectedValueOnce(new Error("token unavailable"));
      const view = renderHook(() => useResourceCallSession(media));

      await act(() => view.result.current.join(knownCallTarget, "fresh"));

      expect(leaveResourceCall).toHaveBeenCalledExactlyOnceWith(
        resourceCall.call_id,
        participationID,
      );
      expect(view.result.current.status).toBe("error");
      expect(view.result.current.errorOperation).toBe("join");
      expect(view.result.current.callId).toBeNull();
      expect(view.result.current.active).toBeNull();
    });

    it("8. a connect failure after admission compensates with call.leave and stops local media", async () => {
      const media = fakeMedia();
      media.connect.mockRejectedValueOnce(new Error("connect failed"));
      const view = renderHook(() => useResourceCallSession(media));

      await act(() => view.result.current.join(knownCallTarget, "fresh"));

      expect(leaveResourceCall).toHaveBeenCalledExactlyOnceWith(
        resourceCall.call_id,
        participationID,
      );
      expect(view.result.current.status).toBe("error");
      expect(view.result.current.callId).toBeNull();
      expect(view.result.current.active).toBeNull();
    });

    it("9. a successful compensation leave never leaves a false active/callId behind", async () => {
      const media = fakeMedia();
      vi.mocked(issueCallToken).mockRejectedValueOnce(new Error("token unavailable"));
      const view = renderHook(() => useResourceCallSession(media));

      await act(() => view.result.current.join(knownCallTarget, "fresh"));

      expect(view.result.current.callId).toBeNull();
      expect(view.result.current.active).toBeNull();
      expect(view.result.current.status).not.toBe("active");
    });

    it("10. a failed compensation leave preserves callId/active for a retryable leave() — never fakes idle", async () => {
      const media = fakeMedia();
      vi.mocked(issueCallToken).mockRejectedValueOnce(new Error("token unavailable"));
      vi.mocked(leaveResourceCall).mockRejectedValueOnce(new Error("compensation failed"));
      const view = renderHook(() => useResourceCallSession(media));

      await act(() => view.result.current.join(knownCallTarget, "fresh"));

      expect(view.result.current.status).toBe("error");
      expect(view.result.current.errorOperation).toBe("leave");
      expect(view.result.current.callId).toBe(resourceCall.call_id);
      expect(view.result.current.active).toEqual(knownCallTarget);

      // The preserved errorOperation="leave" lets the existing retry
      // affordance call leave() again — idempotent server-side — instead of
      // silently pretending the lease is gone.
      vi.mocked(leaveResourceCall).mockResolvedValueOnce({
        released: true,
        call: { ...resourceCall, status: "ended" },
      });
      await act(() => view.result.current.leave());
      expect(leaveResourceCall).toHaveBeenLastCalledWith(resourceCall.call_id, participationID);
      expect(view.result.current.status).toBe("idle");
    });

    it("11. a pre-admission busy rejection never calls call.leave — no lease was ever created", async () => {
      const media = fakeMedia();
      vi.mocked(joinResourceCall).mockRejectedValueOnce(
        new ResourceCallSignalingError("call.join", "call_participant_busy"),
      );
      const view = renderHook(() => useResourceCallSession(media));

      await act(() => view.result.current.join(knownCallTarget, "fresh"));

      expect(leaveResourceCall).not.toHaveBeenCalled();
      expect(view.result.current.error).toBe("Você já está em outra chamada.");
    });

    it("11b. a pre-admission busy rejection on a same-target rejoin never stops the previously active session", async () => {
      const media = fakeMedia();
      const view = renderHook(() => useResourceCallSession(media));
      await act(() => view.result.current.join(channelTarget, "fresh"));
      const originalCallId = view.result.current.callId;
      vi.mocked(joinResourceCall).mockRejectedValueOnce(
        new ResourceCallSignalingError("call.join", "call_participant_busy"),
      );
      media.stop.mockClear();

      // Same kind/id as the already-active session (so the hook's own
      // pre-#622 different-target guard does not short-circuit before
      // reaching admission), but rejoining via the known-callId path — e.g.
      // the discovery indicator handed back a callId while server-side
      // state now disagrees.
      await act(() => view.result.current.join(knownCallTarget, "fresh"));

      expect(joinResourceCall).toHaveBeenCalledOnce();
      expect(view.result.current.callId).toBe(originalCallId);
      expect(view.result.current.status).toBe("active");
      expect(leaveResourceCall).not.toHaveBeenCalled();
      expect(media.stop).not.toHaveBeenCalled();
    });

    it("13. a failure after this attempt was already superseded by a newer generation never touches the newer attempt's state", async () => {
      const media = fakeMedia();
      const participationP1 = "00000000-0000-4000-8000-000000000731";
      const participationP2 = "00000000-0000-4000-8000-000000000732";
      let resolveFirstJoin!: (admission: ResourceCallAdmission) => void;
      vi.mocked(joinResourceCall).mockReturnValueOnce(
        new Promise((resolve) => {
          resolveFirstJoin = resolve;
        }),
      );
      const view = renderHook(() => useResourceCallSession(media));
      let firstJoin!: Promise<string | undefined>;
      act(() => {
        firstJoin = view.result.current.join(knownCallTarget, "fresh");
      });
      // Let startAudio() (an already-resolved mock) actually settle, so the
      // first attempt is genuinely stalled awaiting call.join's admission —
      // not still behind an earlier step — before it gets superseded.
      // Mirrors the existing "leaving while ... still pending" tests above.
      await act(async () => {
        await Promise.resolve();
        await Promise.resolve();
      });
      expect(joinResourceCall).toHaveBeenCalledOnce();

      // Supersede via leave() only now that admission is genuinely in
      // flight — bumps attemptGenerationRef past the first attempt.
      const secondCallId = "00000000-0000-4000-8000-0000000008aa";
      await act(() => view.result.current.leave());
      vi.mocked(joinResourceCall).mockResolvedValueOnce(
        resourceAdmission({ ...resourceCall, call_id: secondCallId }, participationP2),
      );
      const secondTarget: ResourceCallTarget = { ...channelTarget, callId: secondCallId };
      await act(() => view.result.current.join(secondTarget, "fresh"));
      expect(view.result.current.callId).toBe(secondCallId);

      // Now let the FIRST (long-superseded) join's admission finally
      // resolve — it must compensate for its own orphaned lease and must
      // never touch the second attempt's now-current state.
      await act(async () => {
        resolveFirstJoin(resourceAdmission(resourceCall, participationP1));
        await firstJoin;
      });

      expect(leaveResourceCall).toHaveBeenCalledWith(resourceCall.call_id, participationP1);
      expect(view.result.current.callId).toBe(secondCallId);
      expect(view.result.current.status).toBe("active");
    });

    // Issue #622 round 2 adversarial audit (section 18, "MAIOR RISCO"): the
    // exact scenario is a REJOIN of the SAME call_id, not a different one
    // (test 13 above uses secondCallId, deliberately distinct). Attempt A
    // admits X, then stalls post-admission (token pending). A newer,
    // legitimate rejoin of the SAME X (attempt B) supersedes A and reaches
    // "active" on its own. Only THEN does A's stalled token request finally
    // reject. The generation guard must be re-checked immediately before
    // A's own compensating call.leave — never merely at the top of the
    // catch handler — so A's now-stale failure never sends a second,
    // redundant (and destructive) leave against the lease B currently holds.
    it("18. a stale attempt's post-admission failure never compensates against a newer rejoin of the SAME call_id", async () => {
      const media = fakeMedia();
      let rejectTokenA!: (error: unknown) => void;
      vi.mocked(issueCallToken).mockImplementationOnce(
        () =>
          new Promise((_resolve, reject) => {
            rejectTokenA = reject;
          }),
      );
      const view = renderHook(() => useResourceCallSession(media));

      // Attempt A: admits X immediately, then stalls at issueCallToken.
      let firstJoin!: Promise<string | undefined>;
      act(() => {
        firstJoin = view.result.current.join(knownCallTarget, "fresh");
      });
      await act(async () => {
        await Promise.resolve();
        await Promise.resolve();
      });
      expect(view.result.current.callId).toBe(resourceCall.call_id);
      expect(view.result.current.status).toBe("connecting");

      // Supersede via leave() — this is the ONLY leaveResourceCall call this
      // test expects to ever see for X.
      await act(() => view.result.current.leave());
      expect(leaveResourceCall).toHaveBeenCalledExactlyOnceWith(
        resourceCall.call_id,
        participationID,
      );

      // Attempt B: a legitimate rejoin of the exact same call_id X, which
      // the server allows (the call is still active) — reaches "active" on
      // its own, fully independent of A's still-dangling promise chain.
      vi.mocked(issueCallToken).mockResolvedValueOnce({
        token: "token-for-b",
        expiresAt: "2026-01-01T00:00:00Z",
        serverUrl: "wss://livekit-dev.nic-labs.com",
      });
      await act(() => view.result.current.join(knownCallTarget, "fresh"));
      expect(view.result.current.callId).toBe(resourceCall.call_id);
      expect(view.result.current.status).toBe("active");

      // Only NOW does A's long-stalled token request finally reject. A's own
      // generation is long superseded (by leave(), then by B's join()), so
      // its post-admission catch must skip compensation entirely — never a
      // second call.leave against the lease B currently holds.
      await act(async () => {
        rejectTokenA(new Error("token unavailable, far too late"));
        await firstJoin;
      });

      expect(leaveResourceCall).toHaveBeenCalledOnce();
      expect(view.result.current.callId).toBe(resourceCall.call_id);
      expect(view.result.current.status).toBe("active");
    });
  });
});
