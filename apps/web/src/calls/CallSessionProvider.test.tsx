import { StrictMode } from "react";
import { act, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter, Outlet, Route, Routes, useNavigate } from "react-router";
import { beforeEach, describe, expect, it, vi } from "vitest";

import { acquireChatSocket, type ChatSocketListener } from "../chat/chatSocket";
import type { Call } from "../chat/callState";
import { avatarColorFor, initialsFrom } from "../chat/messageDisplay";
import { resolveCall, syncResourceCall } from "../chat/resourceCallSignaling";
import { useCallMedia } from "../chat/useCallMedia";
import { useCallSignaling } from "../chat/useCallSignaling";
import { useResourceCallSession } from "../chat/useResourceCallSession";
import { compareParticipationTokens, createOwnershipCoordinator } from "./callOwnership";
import CallSessionProvider, { useCallSession } from "./CallSessionProvider";
import { _resetSelfProfile } from "../profile/selfProfile";
import type { SelfProfile } from "../profile/profileApi";

vi.mock("../chat/useCallMedia", () => ({ useCallMedia: vi.fn() }));
vi.mock("../chat/useCallSignaling", () => ({ useCallSignaling: vi.fn() }));
vi.mock("../chat/useResourceCallSession", () => ({ useResourceCallSession: vi.fn() }));
vi.mock("../chat/chatSocket", () => ({
  acquireChatSocket: vi.fn(),
  setConsumerSubscriptions: vi.fn(),
  releaseConsumerSubscriptions: vi.fn(),
}));
// Only syncResourceCall is stubbed — issue #622 round 2's route/reconnect
// rediscovery effects call it directly, and the provider tests below need to
// assert exactly which target it was called with and control when it
// resolves, rather than driving a real request/response round trip through
// the already-mocked chatSocket.
vi.mock("../chat/resourceCallSignaling", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../chat/resourceCallSignaling")>()),
  // Default resolution (never call:null-observed forever) so every describe
  // block that never touches this mock directly stays unaffected —
  // vi.clearAllMocks() clears calls/results but not this factory-level
  // implementation, so no other beforeEach needs to know this mock exists.
  syncResourceCall: vi.fn(async () => ({ call: null, observedAt: "1970-01-01T00:00:00Z" })),
  resolveCall: vi.fn(),
}));
vi.mock("./callOwnership", async (importOriginal) => ({
  ...(await importOriginal<typeof import("./callOwnership")>()),
  createOwnershipCoordinator: vi.fn(),
}));

// ── Mock profileApi (the local participant's identity source, issue #612) ────

const { mockFetchMyProfile } = vi.hoisted(() => ({
  mockFetchMyProfile: vi.fn<(signal?: AbortSignal) => Promise<SelfProfile>>(),
}));

vi.mock("../profile/profileApi", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../profile/profileApi")>();
  return { ...actual, fetchMyProfile: (signal?: AbortSignal) => mockFetchMyProfile(signal) };
});

const callId = "00000000-0000-4000-8000-000000000546";
const userA = "00000000-0000-4000-8000-000000000547";
const userB = "00000000-0000-4000-8000-000000000548";
const channelId = "00000000-0000-4000-8000-000000000549";
const channelYId = "00000000-0000-4000-8000-000000000550";
const lease = {
  v: 1,
  callId,
  tabId: "tab-main",
  epoch: 2,
  role: "main",
  expiresAt: 999999,
} as const;

let ownershipListener: (message: never) => void;
let ownershipLost: () => void;
let socketListener: ChatSocketListener;
// Stands in for the real coordinator's persisted, PER-WRITER-KEY storage
// (issue #570 follow-up): production's nchat.call.participation.v2.
// <callId>.<writerId> scheme, one independent slot per (callId, writerId)
// pair — never a single shared slot a stale writer could regress. Shape:
// callId -> writerId -> generation. Deliberately NOT reset by
// vi.clearAllMocks() — only an explicit `participationStorage = {}` (in
// each describe's own beforeEach) represents "fresh browser storage";
// leaving it untouched within a single test is what lets that test simulate
// a reload, a second freshly-mounted tab, or a genuinely concurrent writer
// still sharing the same persisted floor.
let participationStorage: Record<string, Record<string, number>> = {};
function maxParticipationToken(id: string): { generation: number; writerId: string } | null {
  const writers = participationStorage[id];
  if (!writers) return null;
  let best: { generation: number; writerId: string } | null = null;
  for (const [writerId, generation] of Object.entries(writers)) {
    const candidate = { generation, writerId };
    if (!best || compareParticipationTokens(candidate, best) > 0) best = candidate;
  }
  return best;
}
// Issue #610 (privacy blocker follow-up): a small real implementation, not
// a dumb stub — the pending/confirmed write-ahead algorithm's revision
// fencing (expectedRevision) needs realistic revision bookkeeping for most
// tests to behave sensibly without each one hand-managing revision numbers.
// Reset in each describe's own beforeEach, exactly like participationStorage.
let mediaIntentStorage: Record<
  string,
  { revision: number; phase: "confirmed" | "pending"; microphone: boolean; camera: boolean }
> = {};
type WriteMediaIntentOutcome =
  | { ok: true; revision: number }
  | { ok: false; reason: "stale" | "storage-error" };
function defaultWriteMediaIntent(
  id: string,
  _capturedLease: unknown,
  intent: { microphone: boolean; camera: boolean },
  phase: "confirmed" | "pending",
  options?: { expectedRevision?: number },
): WriteMediaIntentOutcome {
  const previous = mediaIntentStorage[id];
  if (options?.expectedRevision !== undefined && previous?.revision !== options.expectedRevision) {
    return { ok: false, reason: "stale" };
  }
  const revision = (previous?.revision ?? 0) + 1;
  mediaIntentStorage[id] = { revision, phase, ...intent };
  return { ok: true, revision };
}
function defaultReadMediaIntentForLease(id: string, currentLease?: unknown) {
  void currentLease;
  const entry = mediaIntentStorage[id];
  if (!entry) return null;
  if (entry.phase === "pending") return { microphone: false, camera: false };
  return { microphone: entry.microphone, camera: entry.camera };
}
const ownership = {
  tabId: "tab-main",
  claim: vi.fn<() => Promise<typeof lease | null>>(async () => lease),
  getLease: vi.fn<() => typeof lease | null>(() => null),
  getOwner: vi.fn<() => typeof lease | null>(() => null),
  release: vi.fn(),
  post: vi.fn(),
  subscribe: vi.fn((listener: typeof ownershipListener) => {
    ownershipListener = listener;
    return vi.fn();
  }),
  onOwnershipLost: vi.fn((listener: typeof ownershipLost) => {
    ownershipLost = listener;
    return vi.fn();
  }),
  // Mirrors production: reads the max across every writer's own key, then
  // writes ONLY this tab's own key ("tab-main") — never any other writer's.
  allocateParticipationGeneration: vi.fn((id: string) => {
    const generation = (maxParticipationToken(id)?.generation ?? 0) + 1;
    participationStorage = {
      ...participationStorage,
      [id]: { ...participationStorage[id], "tab-main": generation },
    };
    return { generation, writerId: "tab-main" };
  }),
  getParticipationToken: vi.fn((id: string) => maxParticipationToken(id)),
  // Issue #610: mirrors production's expectedRevision fencing and
  // pending-means-OFF/OFF-on-read semantics against the in-memory
  // mediaIntentStorage above — never the real per-writer-key storage or
  // lease-scoped eligibility rules (those are exhaustively covered at the
  // callOwnership.ts level); tests that specifically exercise a distinct
  // outcome override these per-case via mockReturnValueOnce/mockImplementationOnce.
  // Re-armed (not just cleared) in every describe's own beforeEach — see
  // that reset for why a plain vi.clearAllMocks() isn't enough here.
  writeMediaIntent: vi.fn(defaultWriteMediaIntent),
  readMediaIntentForLease: vi.fn(defaultReadMediaIntentForLease),
  close: vi.fn(),
};

const media = {
  status: "connected",
  prepare: vi.fn(async () => undefined),
  startAudio: vi.fn(async () => undefined),
  connect: vi.fn(
    async (): Promise<{ microphone: boolean; camera: boolean } | undefined> => ({
      microphone: true,
      camera: true,
    }),
  ),
  stop: vi.fn(async () => undefined),
  participants: [] as Array<{
    identity: string;
    displayName: string;
    hasVideo: boolean;
    bindVideo: ReturnType<typeof vi.fn>;
  }>,
  activeSpeakerId: null as string | null,
  remoteScreenShare: null as null | { identity: string; bindMedia: ReturnType<typeof vi.fn> },
  hasRemoteVideo: true,
  hasLocalVideo: true,
  microphoneEnabled: false,
  cameraEnabled: false,
  screenShareEnabled: false,
  pendingControl: null,
  error: null as string | null,
  bindLocalMedia: vi.fn(),
  bindRemoteMedia: vi.fn(),
  bindRemoteAudio: vi.fn(),
  toggleMicrophone: vi.fn(),
  toggleCamera: vi.fn(),
  toggleScreenShare: vi.fn(),
};
const calls = {
  call: null as null | Record<string, unknown>,
  pending: false,
  // Distinct from `pending` on purpose (issue #615 blocker follow-up): the
  // real hook's `cancelling` is true only while THIS call's own cancel() is
  // pending, never merely because `pending` is (which also covers reconnect/
  // call.sync reconciliation) — tests below drive them independently to
  // prove the provider never conflates the two.
  cancelling: false,
  start: vi.fn(),
  accept: vi.fn(),
  decline: vi.fn(),
  cancel: vi.fn(() => true),
  end: vi.fn(),
  activateMedia: vi.fn(async () => undefined),
  retryMedia: vi.fn(async () => undefined),
  mediaActivationRequired: false,
  error: null as string | null,
};
const resource = {
  active: null as null | { kind: "channel" | "dm"; id: string; name: string },
  callId: null as string | null,
  status: "idle",
  error: null as string | null,
  // Mirrors the real hook: resolves with the joined call_id (target.callId
  // is always supplied by every real call site), never rejects.
  join: vi.fn(
    async (
      target: { callId?: string },
      mode: "fresh" | "recovery",
      onCallIdResolved?: (callId: string) => void,
    ) => {
      void mode;
      void onCallIdResolved;
      return target.callId;
    },
  ),
  leave: vi.fn(async () => true),
  reconnect: vi.fn(async () => undefined),
  convergeRemoteLeave: vi.fn(),
};
// Mutable target for "Known join probe" below — issue #622 round 2 removed
// the resource IncomingCallPopup that used to be these tests' only way to
// drive a real joinResourceParticipation() with a specific call_id, so the
// probe reads this instead of a popup's own click. Tests set it right
// before clicking, exactly like the retired joinViaPopup(targetCallId) did.
let knownJoinProbeCallId = "";

function Probe() {
  const navigate = useNavigate();
  const session = useCallSession();
  return (
    <>
      <span data-testid="owner">{session.ownerState}</span>
      <span data-testid="presentation">{session.presentation.mode}</span>
      <span data-testid="dedicated-recovery-failed">{String(session.dedicatedRecoveryFailed)}</span>
      <span data-testid="discovery-channel-x">
        {session.getResourceCall("channel", channelId)?.status ?? "none"}
      </span>
      <span data-testid="discovery-channel-y">
        {session.getResourceCall("channel", channelYId)?.status ?? "none"}
      </span>
      <span data-testid="discovery-dm-1">
        {session.getResourceCall("dm", "dm-1")?.status ?? "none"}
      </span>
      <span data-testid="resource-presentation-call">
        {session.resourcePresentationCall?.call_id ?? "none"}
      </span>
      <button type="button" onClick={() => navigate("/profile")}>
        Perfil
      </button>
      <button type="button" onClick={() => navigate("/chat/channel/example")}>
        Canal
      </button>
      <button type="button" onClick={() => navigate(`/chat/channel/${channelId}`)}>
        Canal X
      </button>
      <button type="button" onClick={() => navigate(`/chat/channel/${channelYId}`)}>
        Canal Y
      </button>
      <button type="button" onClick={() => navigate("/chat/dm/dm-1")}>
        DM direta
      </button>
      <button type="button" onClick={() => navigate("/chat/dm/dm-group-1")}>
        DM grupo
      </button>
      <button
        type="button"
        onClick={() =>
          session.registerDirectory({
            currentUserId: userB,
            channels: [
              { id: channelId, name: "Produto", type: "public", canWrite: true },
              { id: channelYId, name: "Suporte", type: "public", canWrite: true },
            ],
            dms: [
              {
                id: "dm-1",
                name: "Ana",
                type: "1:1",
                participants: [],
                counterpart: { userId: userA, displayName: "Ana" },
              },
              {
                id: "dm-group-1",
                name: "Squad",
                type: "group",
                participants: [],
              },
            ],
          })
        }
      >
        Diretório
      </button>
      <button
        type="button"
        onClick={() =>
          session.registerDirectory({
            currentUserId: userB,
            channels: [{ id: channelId, name: "Produto", type: "public", canWrite: true }],
            dms: [
              {
                id: "dm-1",
                name: "Ana",
                type: "1:1",
                participants: [],
                counterpart: { userId: userA, displayName: "Ana" },
              },
            ],
          })
        }
      >
        Diretório (sem Y)
      </button>
      <button
        type="button"
        onClick={() =>
          session.registerDirectory({
            currentUserId: userB,
            channels: [{ id: channelId, name: "Produto", type: "public", canWrite: true }],
            dms: [
              {
                id: "dm-1",
                name: "Ana",
                type: "1:1",
                participants: [],
                counterpart: { userId: userA, displayName: "Ana", avatarUrl: "https://x/peer.png" },
              },
            ],
          })
        }
      >
        Diretório (com avatar do par)
      </button>
      <button
        type="button"
        onClick={() => session.registerIdentity("ready", async () => undefined)}
      >
        Identidade
      </button>
      <button type="button" onClick={() => session.expand()}>
        Expandir probe
      </button>
      <button type="button" onClick={() => void session.takeOver()}>
        Takeover probe
      </button>
      <button type="button" onClick={() => session.announceDedicated(callId)}>
        Ready probe
      </button>
      <button type="button" onClick={() => session.acknowledgeDedicated(callId, true)}>
        Ack probe
      </button>
      <button type="button" onClick={() => session.acknowledgeDedicated(callId, false)}>
        Fail probe
      </button>
      <button type="button" onClick={() => session.releaseDedicated(callId)}>
        Release probe
      </button>
      <button type="button" onClick={() => session.releaseDirectTerminal(callId)}>
        Direct terminal release probe
      </button>
      <button type="button" onClick={() => void session.leaveDedicated(callId)}>
        Leave dedicated probe
      </button>
      <button
        type="button"
        // Mirrors every real call site (ChatShell's onLeave, issue #642
        // review blocker 5): leaveResourceParticipation deliberately
        // rethrows on failure, and the caller is always the one that must
        // swallow it — never left unhandled here either.
        onClick={() => void session.leaveResourceParticipation().catch(() => undefined)}
      >
        Leave resource participation probe
      </button>
      <button type="button" onClick={() => void session.beginResourceParticipation(callId)}>
        Begin participation probe
      </button>
      <button type="button" onClick={() => void session.media.toggleMicrophone()}>
        Toggle microphone probe
      </button>
      <button type="button" onClick={() => void session.media.toggleCamera()}>
        Toggle camera probe
      </button>
      <button type="button" onClick={() => void session.media.toggleScreenShare()}>
        Toggle screen share probe
      </button>
      <button
        type="button"
        onClick={() =>
          void session.joinResourceParticipation({ kind: "channel", id: channelId, name: "Geral" })
        }
      >
        Fresh join probe (no callId)
      </button>
      <button
        type="button"
        onClick={() =>
          void session.joinResourceParticipation({
            kind: "channel",
            id: channelId,
            name: "Geral",
            callId: knownJoinProbeCallId,
          })
        }
      >
        Known join probe
      </button>
      <button
        type="button"
        onClick={() =>
          void session.activateResourceParticipation({
            kind: "channel",
            id: channelId,
            name: "Geral",
            callId,
          })
        }
      >
        Activate participation probe
      </button>
      <Outlet />
    </>
  );
}

function providerTree(path = "/chat") {
  return (
    <MemoryRouter initialEntries={[path]}>
      <Routes>
        <Route
          element={
            <CallSessionProvider>
              <Probe />
            </CallSessionProvider>
          }
        >
          <Route path="/chat" element={<p>Chat</p>} />
          <Route path="/chat/channel/:id" element={<p>Canal</p>} />
          <Route path="/chat/dm/:id" element={<p>DM</p>} />
          <Route path="/profile" element={<p>Perfil</p>} />
          <Route path="/call/:id" element={<p>Dedicated</p>} />
        </Route>
      </Routes>
    </MemoryRouter>
  );
}

function renderProvider(path = "/chat") {
  return render(providerTree(path));
}

function activeDirect() {
  return {
    call_id: callId,
    request_id: "request",
    caller_id: userA,
    callee_id: userB,
    target_type: "user",
    call_type: "video",
    status: "active",
    version: 1,
    created_at: "2026-08-18T12:00:00Z",
    occurred_at: "2026-08-18T12:00:00Z",
    expires_at: "2026-08-18T13:00:00Z",
  } satisfies Call;
}

function mockActiveDirectResolution(id = callId) {
  return { ...activeDirect(), call_id: id };
}

// The CURRENT user (userB, per the "Diretório" probe button) as CALLER —
// activeDirect()'s roles swapped, ringing — for issue #615's outgoing popup.
// Ana (userA) is the existing "dm-1" counterpart fixture, so the peer lookup
// resolves exactly like every other direct-call test in this file.
function outgoingRinging() {
  return {
    ...activeDirect(),
    caller_id: userB,
    callee_id: userA,
    status: "ringing",
  } satisfies Call;
}

vi.mocked(resolveCall).mockImplementation(async (id) => mockActiveDirectResolution(id));

describe("CallSessionProvider", () => {
  beforeEach(() => {
    vi.useRealTimers();
    vi.clearAllMocks();
    _resetSelfProfile();
    mockFetchMyProfile.mockResolvedValue({ id: userB, displayName: "" });
    _resetSelfProfile();
    mockFetchMyProfile.mockResolvedValue({ id: userB, displayName: "" });
    participationStorage = {};
    calls.call = null;
    calls.pending = false;
    calls.cancelling = false;
    calls.error = null;
    calls.mediaActivationRequired = false;
    resource.active = null;
    resource.callId = null;
    resource.status = "idle";
    resource.error = null;
    media.status = "connected";
    media.error = null;
    media.activeSpeakerId = null;
    media.participants = [];
    ownership.getLease.mockReturnValue(null);
    ownership.getOwner.mockReturnValue(null);
    ownership.claim.mockResolvedValue(lease);
    mediaIntentStorage = {};
    ownership.writeMediaIntent.mockReset();
    ownership.writeMediaIntent.mockImplementation(defaultWriteMediaIntent);
    ownership.readMediaIntentForLease.mockReset();
    ownership.readMediaIntentForLease.mockImplementation(defaultReadMediaIntentForLease);
    vi.mocked(resolveCall).mockReset();
    vi.mocked(resolveCall).mockImplementation(async (id) => mockActiveDirectResolution(id));
    media.connect.mockResolvedValue({ microphone: true, camera: true });
    vi.mocked(createOwnershipCoordinator).mockReturnValue(ownership as never);
    vi.mocked(useCallMedia).mockReturnValue(media as never);
    vi.mocked(useCallSignaling).mockReturnValue(calls as never);
    vi.mocked(useResourceCallSession).mockReturnValue(resource as never);
    vi.mocked(acquireChatSocket).mockImplementation((listener) => {
      socketListener = listener;
      return { send: vi.fn(), isOpen: vi.fn(), generation: vi.fn(), release: vi.fn() };
    });
  });

  async function handoffActiveDirectToDedicated() {
    calls.call = activeDirect();
    ownership.getLease.mockReturnValue(lease);
    const view = renderProvider();
    await screen.findByTestId("floating-call-window");
    act(() =>
      ownershipListener({ v: 1, type: "ready", callId, tabId: "tab-dedicated", epoch: 2 } as never),
    );
    await waitFor(() => expect(screen.getByTestId("owner")).toHaveTextContent("remote"));
    act(() =>
      ownershipListener({ v: 1, type: "ack", callId, tabId: "tab-dedicated", epoch: 3 } as never),
    );
    return view;
  }

  it("keeps one media/signaling controller across authenticated route navigation", () => {
    renderProvider();
    fireEvent.click(screen.getByRole("button", { name: "Perfil" }));
    fireEvent.click(screen.getByRole("button", { name: "Canal" }));
    expect(screen.getByText("Canal", { selector: "p" })).toBeInTheDocument();
    expect(media.stop).not.toHaveBeenCalled();
  });

  it("claims media once, reuses its lease, and releases on connect failure", async () => {
    renderProvider();
    const owned = vi.mocked(useResourceCallSession).mock.calls[0]![0];
    await act(() => owned.connect(activeDirect() as never, "token", "wss://livekit", "fresh"));
    expect(ownership.claim).toHaveBeenCalledWith(callId, "main");
    expect(screen.getByTestId("owner")).toHaveTextContent("local");

    ownership.getLease.mockReturnValue(lease);
    await act(() => owned.connect(activeDirect() as never, "token", "wss://livekit", "fresh"));
    expect(ownership.claim).toHaveBeenCalledOnce();

    media.connect.mockRejectedValueOnce(new Error("connect"));
    await expect(
      owned.connect(activeDirect() as never, "token", "wss://livekit", "fresh"),
    ).rejects.toThrow("connect");
    expect(ownership.release).toHaveBeenCalledWith(callId);
    await waitFor(() => expect(screen.getByTestId("owner")).toHaveTextContent("none"));
  });

  it("refuses media when another tab owns it and cleans tracks through the shared bridge", async () => {
    ownership.claim.mockResolvedValueOnce(null);
    renderProvider();
    const owned = vi.mocked(useResourceCallSession).mock.calls[0]![0];
    await act(async () => {
      await expect(
        owned.connect(activeDirect() as never, "token", "wss://livekit", "fresh"),
      ).rejects.toThrow("another tab");
    });
    expect(screen.getByTestId("owner")).toHaveTextContent("remote");
    await act(() => owned.stop());
    expect(media.stop).toHaveBeenCalledOnce();
  });

  describe("media-intent recovery/persistence at the ownedMedia choke point (issue #610)", () => {
    it("fresh connect never reads stored media intent", async () => {
      renderProvider();
      const owned = vi.mocked(useResourceCallSession).mock.calls[0]![0];
      await act(() => owned.connect(activeDirect() as never, "token", "wss://livekit", "fresh"));
      expect(ownership.readMediaIntentForLease).not.toHaveBeenCalled();
      expect(media.connect).toHaveBeenCalledWith(
        expect.anything(),
        "token",
        "wss://livekit",
        undefined,
      );
    });

    it.each([
      { microphone: true, camera: true },
      { microphone: true, camera: false },
      { microphone: false, camera: true },
      { microphone: false, camera: false },
    ])("recovery connect applies the stored snapshot %o exactly (§17/§18 2x2)", async (intent) => {
      ownership.readMediaIntentForLease.mockReturnValueOnce(intent);
      renderProvider();
      const owned = vi.mocked(useResourceCallSession).mock.calls[0]![0];
      await act(() => owned.connect(activeDirect() as never, "token", "wss://livekit", "recovery"));
      expect(ownership.readMediaIntentForLease).toHaveBeenCalledWith(callId);
      expect(media.connect).toHaveBeenCalledWith(
        expect.anything(),
        "token",
        "wss://livekit",
        intent,
      );
    });

    it("recovery connect with no valid snapshot degrades to OFF/OFF, never a call_type fallback (§16)", async () => {
      ownership.readMediaIntentForLease.mockReturnValueOnce(null);
      renderProvider();
      const owned = vi.mocked(useResourceCallSession).mock.calls[0]![0];
      await act(() => owned.connect(activeDirect() as never, "token", "wss://livekit", "recovery"));
      expect(media.connect).toHaveBeenCalledWith(expect.anything(), "token", "wss://livekit", {
        microphone: false,
        camera: false,
      });
    });

    it('persists the applied snapshot via writeMediaIntent(..., "confirmed") using the acquired lease, only then declares success', async () => {
      renderProvider();
      const owned = vi.mocked(useResourceCallSession).mock.calls[0]![0];
      media.connect.mockResolvedValueOnce({ microphone: true, camera: false });
      await act(() => owned.connect(activeDirect() as never, "token", "wss://livekit", "fresh"));
      expect(ownership.writeMediaIntent).toHaveBeenCalledWith(
        callId,
        lease,
        { microphone: true, camera: false },
        "confirmed",
      );
    });

    // §12 audit conclusion (documented): connect never risks resurrecting a
    // stale snapshot as WRONG the way a mid-call toggle can — a fresh
    // connect has no prior confirmed baseline for this device intent to
    // contradict, and a recovery connect only ever re-persists the SAME
    // value it just read back from a causally-valid predecessor (or
    // nothing). A failed write-back here just leaves that predecessor (or
    // nothing) in storage, which stays safe to read later — so connect
    // uses the plain §11 fail-closed teardown, never the toggle's
    // write-ahead pending/confirmed protocol.
    it("write failure: stops media, releases ownership (no write-ahead protocol needed), rejects (§11)", async () => {
      ownership.writeMediaIntent.mockReturnValueOnce({ ok: false, reason: "storage-error" });
      renderProvider();
      const owned = vi.mocked(useResourceCallSession).mock.calls[0]![0];
      await expect(
        owned.connect(activeDirect() as never, "token", "wss://livekit", "fresh"),
      ).rejects.toThrow();
      expect(media.stop).toHaveBeenCalled();
      expect(ownership.writeMediaIntent).toHaveBeenCalledOnce();
      expect(ownership.release).toHaveBeenCalledWith(callId);
      await waitFor(() => expect(screen.getByTestId("owner")).toHaveTextContent("none"));
    });

    it("stop happens strictly before release on write failure (ordering)", async () => {
      ownership.writeMediaIntent.mockReturnValueOnce({ ok: false, reason: "storage-error" });
      renderProvider();
      const owned = vi.mocked(useResourceCallSession).mock.calls[0]![0];
      await expect(
        owned.connect(activeDirect() as never, "token", "wss://livekit", "fresh"),
      ).rejects.toThrow();
      const stopOrder = media.stop.mock.invocationCallOrder[0]!;
      const releaseOrder = ownership.release.mock.invocationCallOrder[0]!;
      expect(stopOrder).toBeLessThan(releaseOrder);
    });

    it("rejects and releases ownership without writing when media.connect resolves undefined (superseded)", async () => {
      media.connect.mockResolvedValueOnce(undefined);
      renderProvider();
      const owned = vi.mocked(useResourceCallSession).mock.calls[0]![0];
      await expect(
        owned.connect(activeDirect() as never, "token", "wss://livekit", "fresh"),
      ).rejects.toThrow();
      expect(ownership.writeMediaIntent).not.toHaveBeenCalled();
      expect(ownership.release).toHaveBeenCalledWith(callId);
    });
  });

  describe("toggle wrappers: write-ahead pending/confirmed protocol (issue #610 privacy blocker follow-up)", () => {
    beforeEach(() => {
      ownership.getLease.mockReturnValue(lease);
    });

    it("wrappedToggleMicrophone: writes PENDING before the SDK call, then CONFIRMED after it applies", async () => {
      media.toggleMicrophone.mockResolvedValueOnce(true);
      media.microphoneEnabled = false;
      media.cameraEnabled = false;
      renderProvider();
      await act(async () => {
        fireEvent.click(screen.getByRole("button", { name: "Toggle microphone probe" }));
      });
      expect(ownership.writeMediaIntent).toHaveBeenNthCalledWith(
        1,
        callId,
        lease,
        { microphone: true, camera: false },
        "pending",
      );
      expect(ownership.writeMediaIntent).toHaveBeenNthCalledWith(
        2,
        callId,
        lease,
        { microphone: true, camera: false },
        "confirmed",
        { expectedRevision: 1 },
      );
      // Pending was written BEFORE the SDK call, not after.
      const pendingOrder = ownership.writeMediaIntent.mock.invocationCallOrder[0]!;
      const sdkOrder = media.toggleMicrophone.mock.invocationCallOrder[0]!;
      expect(pendingOrder).toBeLessThan(sdkOrder);
    });

    it("wrappedToggleCamera: writes PENDING before the SDK call, then CONFIRMED after it applies", async () => {
      media.toggleCamera.mockResolvedValueOnce(true);
      media.microphoneEnabled = true;
      media.cameraEnabled = false;
      renderProvider();
      await act(async () => {
        fireEvent.click(screen.getByRole("button", { name: "Toggle camera probe" }));
      });
      expect(ownership.writeMediaIntent).toHaveBeenNthCalledWith(
        1,
        callId,
        lease,
        { microphone: true, camera: true },
        "pending",
      );
      expect(ownership.writeMediaIntent).toHaveBeenNthCalledWith(
        2,
        callId,
        lease,
        { microphone: true, camera: true },
        "confirmed",
        { expectedRevision: 1 },
      );
    });

    it("1. pending pre-write failure: the SDK is NEVER called, device stays at its last confirmed state", async () => {
      ownership.writeMediaIntent.mockReturnValueOnce({ ok: false, reason: "storage-error" });
      renderProvider();
      await act(async () => {
        fireEvent.click(screen.getByRole("button", { name: "Toggle microphone probe" }));
      });
      expect(media.toggleMicrophone).not.toHaveBeenCalled();
      expect(ownership.writeMediaIntent).toHaveBeenCalledOnce();
    });

    it("a stale toggle (SDK resolves undefined after pending was durably written) leaves the pending marker as-is and confirms nothing", async () => {
      media.toggleMicrophone.mockResolvedValueOnce(undefined);
      renderProvider();
      await act(async () => {
        fireEvent.click(screen.getByRole("button", { name: "Toggle microphone probe" }));
      });
      expect(ownership.writeMediaIntent).toHaveBeenCalledOnce(); // pending only
      expect(mediaIntentStorage[callId]?.phase).toBe("pending");
    });

    describe("2/3. confirmed write fails after a successful SDK toggle — recovery must see OFF/OFF, never promote either direction", () => {
      it("2. mic durable ON -> toggle OFF -> SDK applies OFF -> confirmed write fails -> recovery reads OFF/OFF", async () => {
        media.toggleMicrophone.mockResolvedValueOnce(false);
        media.microphoneEnabled = true;
        media.cameraEnabled = false;
        renderProvider();
        // Force ONLY the second (confirmed) write to fail; pending succeeds.
        ownership.writeMediaIntent
          .mockImplementationOnce(defaultWriteMediaIntent) // pending
          .mockReturnValueOnce({ ok: false, reason: "storage-error" }); // confirmed

        await act(async () => {
          fireEvent.click(screen.getByRole("button", { name: "Toggle microphone probe" }));
        });

        expect(media.stop).toHaveBeenCalledOnce(); // defense-in-depth teardown
        expect(ownership.readMediaIntentForLease(callId, lease)).toEqual({
          microphone: false,
          camera: false,
        });
      });

      it("3. mic durable OFF -> toggle ON -> SDK applies ON -> confirmed write fails -> recovery reads OFF/OFF (never promotes ON)", async () => {
        media.toggleMicrophone.mockResolvedValueOnce(true);
        media.microphoneEnabled = false;
        media.cameraEnabled = false;
        renderProvider();
        ownership.writeMediaIntent
          .mockImplementationOnce(defaultWriteMediaIntent) // pending
          .mockReturnValueOnce({ ok: false, reason: "storage-error" }); // confirmed

        await act(async () => {
          fireEvent.click(screen.getByRole("button", { name: "Toggle microphone probe" }));
        });

        expect(media.stop).toHaveBeenCalledOnce();
        expect(ownership.readMediaIntentForLease(callId, lease)).toEqual({
          microphone: false,
          camera: false,
        });
      });
    });

    it("4. ownership moves on between pending and confirmed — confirmed is rejected as stale, no teardown escalation, recovery still OFF/OFF", async () => {
      media.toggleMicrophone.mockResolvedValueOnce(true);
      media.microphoneEnabled = false;
      media.cameraEnabled = false;
      renderProvider();
      // The pending write captured `lease`; before the SDK resolves,
      // ownership moves on — the confirmed write's fencing (a lease/epoch
      // mismatch in real production) naturally rejects it as "stale", not
      // "storage-error", so no stop/release escalation is warranted here.
      ownership.writeMediaIntent
        .mockImplementationOnce(defaultWriteMediaIntent) // pending, succeeds
        .mockReturnValueOnce({ ok: false, reason: "stale" }); // confirmed, ownership moved on

      await act(async () => {
        fireEvent.click(screen.getByRole("button", { name: "Toggle microphone probe" }));
      });

      expect(media.stop).not.toHaveBeenCalled();
      expect(ownership.release).not.toHaveBeenCalled();
      // The pending entry from before the loss is what a future recovery
      // (now under whoever actually owns it) would see — OFF/OFF either way.
      expect(defaultReadMediaIntentForLease(callId)).toEqual({ microphone: false, camera: false });
    });

    it("7. full pending -> confirmed success path updates confirmedIntentRef for the NEXT toggle's merge", async () => {
      media.toggleMicrophone.mockResolvedValueOnce(true);
      media.microphoneEnabled = false;
      media.cameraEnabled = false;
      renderProvider();
      await act(async () => {
        fireEvent.click(screen.getByRole("button", { name: "Toggle microphone probe" }));
      });
      expect(mediaIntentStorage[callId]).toMatchObject({
        phase: "confirmed",
        microphone: true,
        camera: false,
      });

      // The next toggle (camera) merges confirmedIntentRef's mic value
      // (true), not a stale media.microphoneEnabled read.
      media.toggleCamera.mockResolvedValueOnce(true);
      media.microphoneEnabled = false; // deliberately stale/wrong at the SDK layer
      await act(async () => {
        fireEvent.click(screen.getByRole("button", { name: "Toggle camera probe" }));
      });
      expect(ownership.writeMediaIntent).toHaveBeenCalledWith(
        callId,
        lease,
        { microphone: true, camera: true },
        "pending",
      );
    });

    it("does not touch the device at all when there is no current ownership lease", async () => {
      media.toggleMicrophone.mockResolvedValueOnce(true);
      ownership.getLease.mockReturnValue(null);
      renderProvider();
      await act(async () => {
        fireEvent.click(screen.getByRole("button", { name: "Toggle microphone probe" }));
      });
      expect(media.toggleMicrophone).not.toHaveBeenCalled();
      expect(ownership.writeMediaIntent).not.toHaveBeenCalled();
    });

    it("a server/SDK mute that flips media state WITHOUT going through the wrapper never persists (§20)", async () => {
      renderProvider();
      // Simulates onMicrophoneStateChanged/server-mute flipping the raw
      // media object's own state directly — never wrappedToggleMicrophone.
      media.microphoneEnabled = false;
      expect(ownership.writeMediaIntent).not.toHaveBeenCalled();

      // A subsequent REAL user toggle still behaves normally afterward.
      media.toggleMicrophone.mockResolvedValueOnce(true);
      await act(async () => {
        fireEvent.click(screen.getByRole("button", { name: "Toggle microphone probe" }));
      });
      expect(ownership.writeMediaIntent).toHaveBeenCalledTimes(2); // pending + confirmed
    });
  });

  it("keeps direct media from stopping an active resource room and releases it before direct media", async () => {
    resource.active = { kind: "channel", id: channelId, name: "Produto" };
    renderProvider();
    const direct = vi.mocked(useCallSignaling).mock.calls[0]![0]!;
    const releaseResource = vi.mocked(useCallSignaling).mock.calls[0]![2]!;
    await act(() => direct.stop());
    expect(media.stop).not.toHaveBeenCalled();
    await act(() => releaseResource(activeDirect() as never));
    expect(resource.leave).toHaveBeenCalledOnce();
  });

  it("shows direct incoming controls, identity retry, and a persistent floating call", async () => {
    calls.call = { ...activeDirect(), status: "ringing" };
    const view = renderProvider();
    fireEvent.click(screen.getByRole("button", { name: "Diretório" }));
    fireEvent.click(screen.getByRole("button", { name: "Identidade" }));
    expect(await screen.findByRole("dialog", { name: "Chamada recebida" })).toBeInTheDocument();
    fireEvent.click(
      screen.getByRole("button", { name: "Atender com câmera a chamada de vídeo de Ana" }),
    );
    expect(calls.accept).toHaveBeenCalledOnce();
    fireEvent.click(screen.getByRole("button", { name: "Recusar chamada de Ana" }));
    expect(calls.decline).toHaveBeenCalledOnce();

    calls.call = activeDirect();
    view.rerender(
      <MemoryRouter initialEntries={["/chat"]}>
        <CallSessionProvider>
          <Probe />
        </CallSessionProvider>
      </MemoryRouter>,
    );
    expect(await screen.findByTestId("floating-call-window")).toBeInTheDocument();
  });

  // Issue #615: the CALLER's own global, non-modal surface for a direct
  // call.ringing it started — IncomingCallPopup's counterpart for the other
  // end of the exact same lifecycle state, gated ENTIRELY on the
  // authoritative calls.call (never on calls.pending, never a second
  // lifecycle/local model).
  describe("outgoing call popup (issue #615)", () => {
    it("A: renders the outgoing popup (caller=current user, callee=peer), never the incoming one", async () => {
      calls.call = outgoingRinging();
      renderProvider();
      fireEvent.click(screen.getByRole("button", { name: "Diretório" }));

      expect(await screen.findByRole("region", { name: "Ligando para Ana" })).toBeInTheDocument();
      expect(screen.getByText("Ana")).toBeInTheDocument();
      expect(screen.queryByRole("dialog", { name: "Chamada recebida" })).not.toBeInTheDocument();
    });

    it("B: an incoming ringing call keeps its existing behavior and never renders the outgoing popup", async () => {
      calls.call = { ...activeDirect(), status: "ringing" };
      renderProvider();
      fireEvent.click(screen.getByRole("button", { name: "Diretório" }));

      expect(await screen.findByRole("dialog", { name: "Chamada recebida" })).toBeInTheDocument();
      expect(screen.queryByRole("region", { name: /Ligando para/ })).not.toBeInTheDocument();
    });

    it("C: accept transitions ringing outgoing -> active; the outgoing popup disappears and floating takes over", async () => {
      calls.call = outgoingRinging();
      const view = renderProvider();
      fireEvent.click(screen.getByRole("button", { name: "Diretório" }));
      expect(await screen.findByRole("region", { name: "Ligando para Ana" })).toBeInTheDocument();

      calls.call = { ...outgoingRinging(), status: "active", version: 2 };
      view.rerender(providerTree());

      expect(screen.queryByRole("region", { name: "Ligando para Ana" })).not.toBeInTheDocument();
      expect(await screen.findByTestId("floating-call-window")).toBeInTheDocument();
    });

    it("D: caller cancel calls calls.cancel() only — never calls.end, decline, or resource lifecycle", async () => {
      calls.call = outgoingRinging();
      renderProvider();
      fireEvent.click(screen.getByRole("button", { name: "Diretório" }));
      await screen.findByRole("region", { name: "Ligando para Ana" });

      fireEvent.click(screen.getByRole("button", { name: "Cancelar chamada para Ana" }));

      expect(calls.cancel).toHaveBeenCalledOnce();
      expect(calls.end).not.toHaveBeenCalled();
      expect(calls.decline).not.toHaveBeenCalled();
      expect(resource.join).not.toHaveBeenCalled();
      expect(resource.leave).not.toHaveBeenCalled();
    });

    it("emits the cancelled technical event only when calls.cancel() actually sent a command", async () => {
      const events: string[] = [];
      const listener = (event: Event) =>
        events.push((event as CustomEvent<{ event: string }>).detail.event);
      window.addEventListener("nchat:call-technical-event", listener);
      calls.call = outgoingRinging();
      calls.cancel.mockReturnValueOnce(false);
      renderProvider();
      fireEvent.click(screen.getByRole("button", { name: "Diretório" }));
      await screen.findByRole("region", { name: "Ligando para Ana" });

      fireEvent.click(screen.getByRole("button", { name: "Cancelar chamada para Ana" }));
      expect(calls.cancel).toHaveBeenCalledOnce();
      expect(events).not.toContain("cancelled");

      window.removeEventListener("nchat:call-technical-event", listener);
    });

    it("emits the cancelled technical event when calls.cancel() actually sends a command", async () => {
      const events: string[] = [];
      const listener = (event: Event) =>
        events.push((event as CustomEvent<{ event: string }>).detail.event);
      window.addEventListener("nchat:call-technical-event", listener);
      calls.call = outgoingRinging();
      calls.cancel.mockReturnValueOnce(true);
      renderProvider();
      fireEvent.click(screen.getByRole("button", { name: "Diretório" }));
      await screen.findByRole("region", { name: "Ligando para Ana" });

      fireEvent.click(screen.getByRole("button", { name: "Cancelar chamada para Ana" }));
      expect(events).toContain("cancelled");

      window.removeEventListener("nchat:call-technical-event", listener);
    });

    it("shows the normal ringing status when nothing is pending", async () => {
      calls.call = outgoingRinging();
      renderProvider();
      fireEvent.click(screen.getByRole("button", { name: "Diretório" }));

      await screen.findByRole("region", { name: "Ligando para Ana" });
      expect(screen.getByRole("status")).toHaveTextContent("Ligando…");
      expect(screen.getByRole("button", { name: "Cancelar chamada para Ana" })).toBeEnabled();
    });

    // Issue #615 blocker follow-up: useCallSignaling's `pending` also covers
    // reconnect/call.sync reconciliation (every reconnect sets pending=true
    // regardless of any real command in flight — see useCallSignaling.ts's
    // onOpen). The popup must key off the hook's dedicated `cancelling`
    // field, never off `pending` directly, or a mere reconnect would show
    // "Cancelando…"/disable the button for a cancel the user never asked
    // for.
    it("does not show a cancelling state during reconnect/call.sync reconciliation (calls.pending=true, calls.cancelling=false)", async () => {
      calls.call = outgoingRinging();
      calls.pending = true;
      calls.cancelling = false;
      renderProvider();
      fireEvent.click(screen.getByRole("button", { name: "Diretório" }));

      await screen.findByRole("region", { name: "Ligando para Ana" });
      expect(screen.getByRole("status")).toHaveTextContent("Ligando…");
      expect(screen.queryByText("Cancelando…")).not.toBeInTheDocument();
      expect(screen.getByRole("button", { name: "Cancelar chamada para Ana" })).toBeEnabled();
    });

    it("shows a disabled, cancelling-labeled button only while calls.cancelling reflects this call's own cancel in flight", async () => {
      calls.call = outgoingRinging();
      calls.pending = true;
      calls.cancelling = true;
      renderProvider();
      fireEvent.click(screen.getByRole("button", { name: "Diretório" }));

      const button = await screen.findByRole("button", { name: "Cancelar chamada para Ana" });
      expect(button).toBeDisabled();
      expect(screen.getByRole("status")).toHaveTextContent("Cancelando…");
    });

    it.each([
      ["E", "declined"],
      ["F", "timed_out"],
      ["G", "cancelled"],
    ] as const)("%s: a %s terminal clears the outgoing popup", async (_, status) => {
      calls.call = outgoingRinging();
      const view = renderProvider();
      fireEvent.click(screen.getByRole("button", { name: "Diretório" }));
      await screen.findByRole("region", { name: "Ligando para Ana" });

      calls.call = { ...outgoingRinging(), status, version: 2 };
      view.rerender(providerTree());

      expect(screen.queryByRole("region", { name: "Ligando para Ana" })).not.toBeInTheDocument();
    });

    it("H: a stale/duplicate ringing push never renders a second outgoing popup", async () => {
      calls.call = outgoingRinging();
      const view = renderProvider();
      fireEvent.click(screen.getByRole("button", { name: "Diretório" }));
      await screen.findByRole("region", { name: "Ligando para Ana" });

      // A duplicate/stale re-delivery of the exact same ringing snapshot —
      // this component derives the popup purely from calls.call, so there is
      // structurally only ever at most one to find, never a second one
      // accumulated from the redundant push.
      calls.call = { ...outgoingRinging() };
      view.rerender(providerTree());
      expect(screen.getAllByRole("region", { name: "Ligando para Ana" })).toHaveLength(1);
    });

    it("I: stays global and unique across a route change while the same direct call keeps ringing", async () => {
      calls.call = outgoingRinging();
      const view = renderProvider("/chat");
      fireEvent.click(screen.getByRole("button", { name: "Diretório" }));
      await screen.findByRole("region", { name: "Ligando para Ana" });

      view.rerender(providerTree(`/chat/channel/${channelId}`));

      expect(screen.getAllByRole("region", { name: "Ligando para Ana" })).toHaveLength(1);
    });

    it("J: a resource (channel/group DM) call never renders the outgoing popup", async () => {
      resource.active = { kind: "channel", id: channelId, name: "Produto" };
      resource.callId = callId;
      resource.status = "connecting";
      renderProvider();
      fireEvent.click(screen.getByRole("button", { name: "Diretório" }));

      expect(screen.queryByRole("region", { name: /Ligando para/ })).not.toBeInTheDocument();
    });
  });

  // Issue #622 round 2: resource calls are joinable sessions, never a
  // ringing call — there is no resource IncomingCallPopup anymore. A
  // broadcast only ever converges the discovery store; it never shows a
  // dialog and never calls resource.join on its own.
  it("converges resource discovery from broadcasts without ever showing a popup or auto-joining", async () => {
    renderProvider();
    fireEvent.click(screen.getByRole("button", { name: "Diretório" }));
    const incoming = {
      ...activeDirect(),
      call_id: callId,
      target_type: "channel",
      target_id: channelId,
      status: "active",
      version: 1,
    };
    act(() =>
      socketListener.onMessage?.(
        {
          type: "call.accepted",
          event_id: "event",
          target_type: "channel",
          target_id: channelId,
          call: incoming,
        },
        1,
      ),
    );

    expect(screen.queryByRole("dialog", { name: "Chamada recebida" })).not.toBeInTheDocument();
    expect(resource.join).not.toHaveBeenCalled();
    expect(screen.getByTestId("discovery-channel-x")).toHaveTextContent("active");

    // A higher-version terminal event for the SAME call_id hides the
    // indicator (status is no longer "active") without ever popping a
    // dialog either.
    act(() =>
      socketListener.onMessage?.(
        {
          type: "call.ended",
          event_id: "event-2",
          target_type: "channel",
          target_id: channelId,
          call: { ...incoming, status: "ended", version: 2 },
        },
        1,
      ),
    );
    expect(screen.queryByRole("dialog", { name: "Chamada recebida" })).not.toBeInTheDocument();
    expect(screen.getByTestId("discovery-channel-x")).toHaveTextContent("ended");
  });

  it("discovers resource Y while participating in X — observing is never participating, and X stays untouched", async () => {
    resource.active = { kind: "channel", id: channelId, name: "Produto" };
    resource.callId = callId;
    renderProvider();
    fireEvent.click(screen.getByRole("button", { name: "Diretório" }));

    act(() =>
      socketListener.onMessage?.(
        {
          type: "call.accepted",
          event_id: "event-resource-y",
          target_type: "channel",
          target_id: channelYId,
          call: {
            ...activeDirect(),
            call_id: "00000000-0000-4000-8000-000000000551",
            target_type: "channel",
            target_id: channelYId,
            status: "active",
          },
        },
        1,
      ),
    );

    // Y is discovered (never suppressed by this user's own participation in
    // X), but discovery alone never joins/starts anything and X's own
    // participation is untouched.
    expect(screen.getByTestId("discovery-channel-y")).toHaveTextContent("active");
    expect(resource.join).not.toHaveBeenCalled();
    expect(resource.active).toEqual({ kind: "channel", id: channelId, name: "Produto" });

    fireEvent.click(screen.getByRole("button", { name: "Fresh join probe (no callId)" }));
    await waitFor(() => expect(resource.join).toHaveBeenCalledOnce());
  });

  it("discovers a resource call even while a direct call is active — #609 blocks join/start only, never discovery", () => {
    calls.call = activeDirect();
    renderProvider();
    fireEvent.click(screen.getByRole("button", { name: "Diretório" }));

    act(() =>
      socketListener.onMessage?.(
        {
          type: "call.accepted",
          event_id: "event-resource-during-direct",
          target_type: "channel",
          target_id: channelYId,
          call: {
            ...activeDirect(),
            call_id: "00000000-0000-4000-8000-000000000552",
            target_type: "channel",
            target_id: channelYId,
            status: "active",
          },
        },
        1,
      ),
    );

    expect(screen.getByTestId("discovery-channel-y")).toHaveTextContent("active");
    expect(resource.join).not.toHaveBeenCalled();
  });

  // Issue #622 round 2 section 5: rediscovery on navigation/reload. Landing
  // on (or navigating to) a channel/group-DM route must resync that one
  // target — recovering whatever this tab missed while it wasn't open.
  it("navigating to a channel route triggers call.resource.sync for that channel", async () => {
    renderProvider();
    fireEvent.click(screen.getByRole("button", { name: "Diretório" }));
    fireEvent.click(screen.getByRole("button", { name: "Canal X" }));
    await waitFor(() =>
      expect(syncResourceCall).toHaveBeenCalledWith({ kind: "channel", id: channelId }),
    );
  });

  it("reloading directly on a group-DM route triggers call.resource.sync for it", async () => {
    // Directory registers AFTER the route is already showing — simulates a
    // reload/deep-link where the route is known before the directory has
    // loaded; the effect must still fire once the target becomes resolvable.
    renderProvider("/chat/dm/dm-group-1");
    fireEvent.click(screen.getByRole("button", { name: "Diretório" }));
    await waitFor(() =>
      expect(syncResourceCall).toHaveBeenCalledWith({ kind: "dm", id: "dm-group-1" }),
    );
  });

  it("a direct 1:1 DM route never triggers call.resource.sync", async () => {
    renderProvider();
    fireEvent.click(screen.getByRole("button", { name: "Diretório" }));
    fireEvent.click(screen.getByRole("button", { name: "DM direta" }));
    // Give any (incorrect) async sync a chance to fire before asserting its
    // absence — awaiting a microtask flush rather than a fixed timer.
    await act(async () => {
      await Promise.resolve();
    });
    expect(syncResourceCall).not.toHaveBeenCalledWith({ kind: "dm", id: "dm-1" });
  });

  it("a socket reconnect (generation change) resyncs the current route's target", async () => {
    renderProvider();
    fireEvent.click(screen.getByRole("button", { name: "Diretório" }));
    fireEvent.click(screen.getByRole("button", { name: "Canal X" }));
    await waitFor(() =>
      expect(syncResourceCall).toHaveBeenCalledWith({ kind: "channel", id: channelId }),
    );
    // Flush the mocked sync's own resolution before proceeding — the
    // provider tracks one in-flight sync per target and would otherwise
    // still consider this target "syncing", silently swallowing the
    // reconnect-triggered call this test is actually asserting on.
    await act(async () => {
      await Promise.resolve();
      await Promise.resolve();
    });
    vi.mocked(syncResourceCall).mockClear();

    // First onOpen only records the generation — it's the initial
    // connection, already covered by the route effect above, so it must NOT
    // fire a second, redundant sync on its own.
    act(() => socketListener.onOpen?.(1));
    expect(syncResourceCall).not.toHaveBeenCalled();

    // A generation CHANGE means the socket actually reopened — that's the
    // one that must resync the current route's target.
    act(() => socketListener.onOpen?.(2));
    await waitFor(() =>
      expect(syncResourceCall).toHaveBeenCalledWith({ kind: "channel", id: channelId }),
    );
  });

  it("performs main-to-dedicated handoff, ACK, timeout rollback, and failure rollback", async () => {
    vi.useFakeTimers();
    calls.call = activeDirect();
    ownership.getLease.mockReturnValue(lease);
    renderProvider();
    act(() =>
      ownershipListener({ v: 1, type: "ready", callId, tabId: "tab-dedicated", epoch: 2 } as never),
    );
    await act(async () => undefined);
    expect(ownership.post).toHaveBeenCalledWith(expect.objectContaining({ type: "handoff" }));
    act(() =>
      ownershipListener({ v: 1, type: "ack", callId, tabId: "tab-dedicated", epoch: 3 } as never),
    );
    expect(screen.getByTestId("presentation")).toHaveTextContent("active_dedicated_tab");

    act(() =>
      ownershipListener({ v: 1, type: "ready", callId, tabId: "tab-dedicated", epoch: 3 } as never),
    );
    await act(async () => vi.advanceTimersByTime(6000));
    expect(ownership.claim).toHaveBeenCalledWith(callId, "main", 2, undefined);
    act(() =>
      ownershipListener({
        v: 1,
        type: "failure",
        callId,
        tabId: "tab-dedicated",
        epoch: 3,
      } as never),
    );
    vi.useRealTimers();
  });

  it("claims a targeted handoff only in a dedicated tab and reports claim failure", async () => {
    // A healthy owner exists, so announceDedicated waits for a real handoff
    // reply instead of claiming immediately (achado #1's recovery path).
    ownership.getOwner.mockReturnValue(lease);
    renderProvider(`/call/${callId}`);
    fireEvent.click(screen.getByRole("button", { name: "Ready probe" }));
    act(() =>
      ownershipListener({
        v: 1,
        type: "handoff",
        callId,
        tabId: "tab-main",
        targetTabId: "other-tab",
        epoch: 2,
      } as never),
    );
    expect(ownership.claim).not.toHaveBeenCalled();
    act(() =>
      ownershipListener({
        v: 1,
        type: "handoff",
        callId,
        tabId: "tab-main",
        targetTabId: ownership.tabId,
        epoch: 2,
      } as never),
    );
    await waitFor(() =>
      expect(ownership.claim).toHaveBeenCalledWith(callId, "dedicated", 2, {
        tabId: "tab-main",
        epoch: 2,
      }),
    );
    ownership.claim.mockResolvedValueOnce(null);
    act(() =>
      ownershipListener({
        v: 1,
        type: "handoff",
        callId,
        tabId: "tab-main",
        targetTabId: ownership.tabId,
        epoch: 3,
      } as never),
    );
    await waitFor(() =>
      expect(ownership.post).toHaveBeenCalledWith(expect.objectContaining({ type: "failure" })),
    );
  });

  it("supports explicit coordinator actions and ownership-loss cleanup", async () => {
    calls.call = activeDirect();
    ownership.getOwner.mockReturnValue(lease);
    renderProvider();
    fireEvent.click(screen.getByRole("button", { name: "Ready probe" }));
    fireEvent.click(screen.getByRole("button", { name: "Ack probe" }));
    fireEvent.click(screen.getByRole("button", { name: "Fail probe" }));
    fireEvent.click(screen.getByRole("button", { name: "Release probe" }));
    expect(ownership.post).toHaveBeenCalledWith(
      expect.objectContaining({ type: "ready", epoch: 2 }),
    );
    expect(ownership.release).toHaveBeenCalledWith(callId);
    act(() => ownershipLost());
    await waitFor(() => expect(media.stop).toHaveBeenCalled());
  });

  it("covers action guards, floating controls, reconnect lifecycle, and terminal cleanup", async () => {
    const opened = vi.spyOn(window, "open").mockReturnValue(null);
    const view = renderProvider();
    fireEvent.click(screen.getByRole("button", { name: "Diretório" }));
    fireEvent.click(screen.getByRole("button", { name: "Expandir probe" }));
    fireEvent.click(screen.getByRole("button", { name: "Takeover probe" }));
    expect(ownership.claim).not.toHaveBeenCalled();

    calls.call = activeDirect();
    calls.mediaActivationRequired = true;
    calls.error = "Falha de mídia";
    media.participants = [
      { identity: userA, displayName: "Ana", hasVideo: true, bindVideo: vi.fn() },
    ];
    media.activeSpeakerId = userB;
    media.remoteScreenShare = { identity: userA, bindMedia: vi.fn() };
    view.rerender(providerTree());
    const owned = vi.mocked(useResourceCallSession).mock.calls.at(-1)![0];
    await act(() => owned.connect(activeDirect() as never, "token", "wss://livekit", "fresh"));
    await screen.findByTestId("floating-call-window");
    expect(document.querySelector(".floating-call__local")).toHaveClass(
      "call-speaker-surface--active",
    );
    expect(screen.getByLabelText("Você está falando")).toBeInTheDocument();

    fireEvent.click(screen.getByRole("button", { name: "Expandir probe" }));
    opened.mockReturnValue({} as Window);
    fireEvent.click(screen.getByRole("button", { name: "Expandir em nova aba" }));
    expect(opened).toHaveBeenCalledWith(`/call/${callId}`, "_blank", "noopener");
    fireEvent.click(screen.getByRole("button", { name: "Permitir câmera e microfone" }));
    fireEvent.click(screen.getByRole("button", { name: "Tentar mídia novamente" }));
    fireEvent.click(screen.getByRole("button", { name: "Encerrar chamada" }));
    expect(calls.activateMedia).toHaveBeenCalled();
    expect(calls.retryMedia).toHaveBeenCalled();
    expect(calls.end).toHaveBeenCalled();

    media.status = "reconnecting";
    view.rerender(providerTree());
    await waitFor(() =>
      expect(screen.getByTestId("presentation")).toHaveTextContent("reconnecting"),
    );
    media.status = "connected";
    view.rerender(providerTree());
    calls.call = { ...activeDirect(), status: "ended" };
    view.rerender(providerTree());
    await waitFor(() => expect(screen.getByTestId("presentation")).toHaveTextContent("ended"));
  });

  it("reconciles before recovery when released arrives while main still sees the direct call as active", async () => {
    vi.mocked(resolveCall).mockResolvedValueOnce({
      ...activeDirect(),
      status: "ended",
      version: 2,
    });
    const view = await handoffActiveDirectToDedicated();
    expect(screen.getByText("Chamada aberta em outra aba")).toBeInTheDocument();
    ownership.claim.mockClear();
    calls.retryMedia.mockClear();
    calls.activateMedia.mockClear();
    media.connect.mockClear();

    // Dedicated already observed the authoritative terminal and releases;
    // this main tab has deliberately NOT received its own call.ended yet.
    act(() =>
      ownershipListener({
        v: 1,
        type: "released",
        callId,
        tabId: "tab-dedicated",
        epoch: 3,
      } as never),
    );
    await act(async () => undefined);

    expect(ownership.claim).not.toHaveBeenCalled();
    expect(calls.retryMedia).not.toHaveBeenCalled();
    expect(calls.activateMedia).not.toHaveBeenCalled();
    expect(media.connect).not.toHaveBeenCalled();
    expect(screen.queryByTestId("floating-call-window")).not.toBeInTheDocument();
    expect(screen.queryByText("Chamada aberta em outra aba")).not.toBeInTheDocument();

    calls.call = { ...activeDirect(), status: "ended", version: 2 };
    view.rerender(providerTree());
    await waitFor(() => expect(screen.getByTestId("presentation")).toHaveTextContent("ended"));
  });

  it("fences the owner-loss poll while main still locally sees a server-terminal direct call as active", async () => {
    vi.useFakeTimers();
    vi.mocked(resolveCall).mockResolvedValueOnce({
      ...activeDirect(),
      status: "ended",
      version: 2,
    });
    calls.call = activeDirect();
    ownership.getLease.mockReturnValue(lease);
    renderProvider();
    await act(async () => undefined);
    expect(screen.getByTestId("floating-call-window")).toBeInTheDocument();
    act(() =>
      ownershipListener({ v: 1, type: "ready", callId, tabId: "tab-dedicated", epoch: 2 } as never),
    );
    await act(async () => undefined);
    expect(screen.getByTestId("owner")).toHaveTextContent("remote");
    act(() =>
      ownershipListener({ v: 1, type: "ack", callId, tabId: "tab-dedicated", epoch: 3 } as never),
    );
    ownership.claim.mockClear();
    calls.retryMedia.mockClear();
    calls.activateMedia.mockClear();
    ownership.getOwner.mockReturnValue(null);

    await act(async () => vi.advanceTimersByTime(1_500));

    expect(ownership.claim).not.toHaveBeenCalled();
    expect(calls.retryMedia).not.toHaveBeenCalled();
    expect(calls.activateMedia).not.toHaveBeenCalled();
    expect(screen.queryByTestId("floating-call-window")).not.toBeInTheDocument();
    expect(screen.queryByText("Chamada aberta em outra aba")).not.toBeInTheDocument();
    vi.useRealTimers();
  });

  it("still lets the owner-loss poll recover a direct call after manual dedicated close", async () => {
    vi.useFakeTimers();
    calls.call = activeDirect();
    ownership.getLease.mockReturnValue(lease);
    renderProvider();
    await act(async () => undefined);
    act(() =>
      ownershipListener({ v: 1, type: "ready", callId, tabId: "tab-dedicated", epoch: 2 } as never),
    );
    await act(async () => undefined);
    act(() =>
      ownershipListener({ v: 1, type: "ack", callId, tabId: "tab-dedicated", epoch: 3 } as never),
    );
    ownership.claim.mockClear();
    calls.retryMedia.mockClear();
    ownership.getOwner.mockReturnValue(null);

    await act(async () => vi.advanceTimersByTime(1_500));

    expect(resolveCall).toHaveBeenCalledWith(callId);
    expect(ownership.claim).toHaveBeenCalledWith(callId, "main", 2, undefined);
    expect(calls.retryMedia).toHaveBeenCalledOnce();
    expect(screen.getByTestId("floating-call-window")).toBeInTheDocument();
    vi.useRealTimers();
  });

  it("still recovers a direct call after manual dedicated close when authoritative sync says active", async () => {
    await handoffActiveDirectToDedicated();
    ownership.claim.mockClear();
    calls.retryMedia.mockClear();

    act(() =>
      ownershipListener({
        v: 1,
        type: "released",
        callId,
        tabId: "tab-dedicated",
        epoch: 3,
      } as never),
    );

    await waitFor(() => expect(resolveCall).toHaveBeenCalledWith(callId));
    await waitFor(() =>
      expect(ownership.claim).toHaveBeenCalledWith(callId, "main", 3, {
        tabId: "tab-dedicated",
        epoch: 3,
      }),
    );
    expect(calls.retryMedia).toHaveBeenCalledOnce();
    expect(await screen.findByTestId("floating-call-window")).toBeInTheDocument();
  });

  it("fails direct recovery closed while sync is offline and allows a later authoritative retry", async () => {
    vi.mocked(resolveCall).mockRejectedValueOnce(new Error("offline"));
    await handoffActiveDirectToDedicated();
    ownership.claim.mockClear();
    calls.retryMedia.mockClear();
    const released = {
      v: 1,
      type: "released",
      callId,
      tabId: "tab-dedicated",
      epoch: 3,
    } as const;

    act(() => ownershipListener(released as never));
    await waitFor(() => expect(resolveCall).toHaveBeenCalledOnce());
    await act(async () => undefined);
    expect(ownership.claim).not.toHaveBeenCalled();
    expect(calls.retryMedia).not.toHaveBeenCalled();

    act(() => ownershipListener(released as never));
    await waitFor(() => expect(resolveCall).toHaveBeenCalledTimes(2));
    await waitFor(() => expect(ownership.claim).toHaveBeenCalledOnce());
    expect(calls.retryMedia).toHaveBeenCalledOnce();
  });

  it("releases a just-claimed lease instead of reconnecting when main becomes terminal during claim", async () => {
    let finishClaim!: () => void;
    ownership.claim.mockImplementationOnce(
      () =>
        new Promise((resolve) => {
          finishClaim = () => resolve(lease);
        }),
    );
    const view = await handoffActiveDirectToDedicated();
    ownership.release.mockClear();
    calls.retryMedia.mockClear();

    act(() =>
      ownershipListener({
        v: 1,
        type: "released",
        callId,
        tabId: "tab-dedicated",
        epoch: 3,
      } as never),
    );
    await waitFor(() => expect(ownership.claim).toHaveBeenCalled());

    calls.call = { ...activeDirect(), status: "ended", version: 2 };
    view.rerender(providerTree());
    await waitFor(() => expect(screen.getByTestId("presentation")).toHaveTextContent("ended"));
    await act(async () => finishClaim());

    expect(ownership.release).toHaveBeenCalledWith(callId);
    expect(calls.retryMedia).not.toHaveBeenCalled();
    expect(screen.getByTestId("owner")).not.toHaveTextContent("local");
  });

  it("does not let a terminal fence for call X block recovery of active call Y", async () => {
    const callY = "00000000-0000-4000-8000-000000000999";
    vi.mocked(resolveCall)
      .mockResolvedValueOnce({ ...activeDirect(), status: "ended", version: 2 })
      .mockResolvedValueOnce(mockActiveDirectResolution(callY));
    const view = await handoffActiveDirectToDedicated();

    act(() =>
      ownershipListener({
        v: 1,
        type: "released",
        callId,
        tabId: "tab-dedicated",
        epoch: 3,
      } as never),
    );
    await waitFor(() => expect(resolveCall).toHaveBeenCalledWith(callId));
    expect(ownership.claim).not.toHaveBeenCalled();

    calls.call = { ...activeDirect(), call_id: callY };
    view.rerender(providerTree());
    await act(async () => undefined);
    act(() =>
      ownershipListener({
        v: 1,
        type: "released",
        callId: callY,
        tabId: "tab-dedicated-y",
        epoch: 8,
      } as never),
    );

    await waitFor(() => expect(resolveCall).toHaveBeenCalledWith(callY));
    await waitFor(() =>
      expect(ownership.claim).toHaveBeenCalledWith(callY, "main", 8, {
        tabId: "tab-dedicated-y",
        epoch: 8,
      }),
    );
    expect(calls.retryMedia).toHaveBeenCalledOnce();
  });

  it("does not recover or render a stale indicator when a dedicated owner releases after direct terminal", async () => {
    calls.call = activeDirect();
    const view = renderProvider();
    await screen.findByTestId("floating-call-window");

    calls.call = { ...activeDirect(), status: "ended", version: 2 };
    view.rerender(providerTree());
    await waitFor(() => expect(screen.getByTestId("presentation")).toHaveTextContent("ended"));
    expect(screen.queryByTestId("floating-call-window")).not.toBeInTheDocument();
    ownership.claim.mockClear();

    act(() =>
      ownershipListener({
        v: 1,
        type: "released",
        callId,
        tabId: "tab-dedicated",
        epoch: 3,
      } as never),
    );

    expect(ownership.claim).not.toHaveBeenCalled();
    expect(screen.queryByText("Chamada aberta em outra aba")).not.toBeInTheDocument();
  });

  it("does not treat a popup-blocked window.open as a loss of ownership", async () => {
    const opened = vi.spyOn(window, "open").mockReturnValue(null);
    const view = renderProvider();
    calls.call = activeDirect();
    view.rerender(providerTree());
    const owned = vi.mocked(useResourceCallSession).mock.calls.at(-1)![0];
    await act(() => owned.connect(activeDirect() as never, "token", "wss://livekit", "fresh"));
    await screen.findByTestId("floating-call-window");
    expect(screen.getByTestId("owner")).toHaveTextContent("local");

    fireEvent.click(screen.getByRole("button", { name: "Expandir em nova aba" }));

    expect(opened).toHaveBeenCalledWith(`/call/${callId}`, "_blank", "noopener");
    // A blocked/opaque popup must never look like ownership was lost: no
    // release, no owner-state change — only the ready/handoff/ack protocol
    // (exercised in the recovery tests below) ever does that.
    expect(ownership.release).not.toHaveBeenCalled();
    expect(screen.getByTestId("owner")).toHaveTextContent("local");
  });

  it("floats a direct call's remote fallback avatar with the peer's real identity", async () => {
    const view = renderProvider();
    fireEvent.click(screen.getByRole("button", { name: "Diretório" }));
    calls.call = activeDirect();
    media.hasRemoteVideo = false;
    view.rerender(providerTree());
    const owned = vi.mocked(useResourceCallSession).mock.calls.at(-1)![0];
    await act(() => owned.connect(activeDirect() as never, "token", "wss://livekit", "fresh"));
    await screen.findByTestId("floating-call-window");

    // Peer is "Ana" (userA) per the registered directory — never an index or
    // a made-up identity.
    const avatar = document.querySelector(".floating-call__avatar")!;
    expect(avatar).toHaveTextContent(initialsFrom("Ana"));
    expect(avatar).toHaveClass(`call-avatar--${avatarColorFor(userA)}`);
  });

  it("models a direct remote active speaker from the canonical peer id", async () => {
    const view = renderProvider();
    fireEvent.click(screen.getByRole("button", { name: "Diretório" }));
    calls.call = activeDirect();
    media.activeSpeakerId = userA;
    view.rerender(providerTree());
    const owned = vi.mocked(useResourceCallSession).mock.calls.at(-1)![0];
    await act(() => owned.connect(activeDirect() as never, "token", "wss://livekit", "fresh"));
    await screen.findByTestId("floating-call-window");

    expect(document.querySelector(".floating-call__remote-participant")).toHaveClass(
      "call-speaker-surface--active",
    );
    expect(screen.getByLabelText("Ana está falando")).toBeInTheDocument();
  });

  it("does not infer a direct remote speaker before the current-user identity is known", async () => {
    calls.call = activeDirect();
    media.activeSpeakerId = userA;
    renderProvider();
    await screen.findByTestId("floating-call-window");

    expect(document.querySelectorAll(".call-speaker-surface--active")).toHaveLength(0);
    expect(document.querySelector(".floating-call__speaker")).not.toBeInTheDocument();
  });

  it("floats the local fallback avatar with the current user's real id", async () => {
    const view = renderProvider();
    fireEvent.click(screen.getByRole("button", { name: "Diretório" }));
    calls.call = activeDirect();
    media.hasLocalVideo = false;
    view.rerender(providerTree());
    const owned = vi.mocked(useResourceCallSession).mock.calls.at(-1)![0];
    await act(() => owned.connect(activeDirect() as never, "token", "wss://livekit", "fresh"));
    await screen.findByTestId("floating-call-window");

    const avatar = document.querySelector(".floating-call__local-avatar")!;
    // Empty/loading profile name falls back to "Você" for initials too
    // (issue #612 blocker) — same visual fallback as the display label,
    // never "?" and never derived from a "(você)"-suffixed string.
    expect(avatar).toHaveTextContent(initialsFrom("Você"));
    // currentUserId (userB) — the registered directory's own id, never a
    // fetched profile just for this fallback.
    expect(avatar).toHaveClass(`call-avatar--${avatarColorFor(userB)}`);
  });

  it("shows the local participant's real profile name with (você), never a bare Você replacing it", async () => {
    mockFetchMyProfile.mockResolvedValue({ id: userB, displayName: "Ana Souza" });
    const view = renderProvider();
    fireEvent.click(screen.getByRole("button", { name: "Diretório" }));
    calls.call = activeDirect();
    view.rerender(providerTree());
    const owned = vi.mocked(useResourceCallSession).mock.calls.at(-1)![0];
    await act(() => owned.connect(activeDirect() as never, "token", "wss://livekit", "fresh"));
    await screen.findByTestId("floating-call-window");

    await waitFor(() => {
      expect(document.querySelector(".floating-call__local-avatar")).toHaveAttribute(
        "aria-label",
        "Ana Souza (você)",
      );
    });
  });

  it("derives the local floating fallback's initials from the raw one-word name, never 'A(' from the (você) suffix (issue #612 blocker)", async () => {
    mockFetchMyProfile.mockResolvedValue({ id: userB, displayName: "Ana" });
    const view = renderProvider();
    fireEvent.click(screen.getByRole("button", { name: "Diretório" }));
    calls.call = activeDirect();
    media.hasLocalVideo = false;
    view.rerender(providerTree());
    const owned = vi.mocked(useResourceCallSession).mock.calls.at(-1)![0];
    await act(() => owned.connect(activeDirect() as never, "token", "wss://livekit", "fresh"));
    await screen.findByTestId("floating-call-window");

    await waitFor(() => {
      const avatar = document.querySelector(".floating-call__local-avatar")!;
      expect(avatar).toHaveTextContent(initialsFrom("Ana"));
      expect(avatar.textContent).not.toContain("(");
    });
  });

  it("passes the direct peer's avatarUrl through to FloatingCallWindow's remote fallback", async () => {
    const view = renderProvider();
    fireEvent.click(screen.getByRole("button", { name: "Diretório (com avatar do par)" }));
    calls.call = activeDirect();
    media.hasRemoteVideo = false;
    view.rerender(providerTree());
    const owned = vi.mocked(useResourceCallSession).mock.calls.at(-1)![0];
    await act(() => owned.connect(activeDirect() as never, "token", "wss://livekit", "fresh"));
    await screen.findByTestId("floating-call-window");

    const img = document.querySelector(".floating-call__avatar img");
    expect(img).toHaveAttribute("src", "https://x/peer.png");
  });

  it("never uses the resource-level avatar as an individual's identity in the floating window", async () => {
    resource.active = { kind: "channel", id: channelId, name: "Produto" };
    resource.callId = callId;
    media.hasRemoteVideo = false;
    media.participants = [
      { identity: userA, displayName: "Ana", hasVideo: false, bindVideo: vi.fn() },
    ];
    renderProvider();
    await screen.findByTestId("resource-call-panel");

    // No avatarUrl exists for a resource/group target — the fallback must stay
    // on initials, never borrow a channel/group picture as if it belonged to
    // one person.
    expect(document.querySelector(".floating-call__avatar img")).not.toBeInTheDocument();
  });

  it("floats a resource call's remote fallback using the room's own identity, never a specific participant", async () => {
    resource.active = { kind: "channel", id: channelId, name: "Produto" };
    resource.callId = callId;
    media.hasRemoteVideo = false;
    // Multiple participants exist, but the fallback must represent the room
    // — not pick one of them as if it were a 1:1 call.
    media.participants = [
      { identity: userA, displayName: "Ana", hasVideo: false, bindVideo: vi.fn() },
    ];
    renderProvider();
    await screen.findByTestId("resource-call-panel");

    const avatar = document.querySelector(".floating-call__avatar")!;
    expect(avatar).toHaveTextContent(initialsFrom("Produto"));
    expect(avatar).toHaveClass(`call-avatar--${avatarColorFor(channelId)}`);
  });

  it("models a resource speaker as a compact participant cue without highlighting the room", async () => {
    resource.active = { kind: "channel", id: channelId, name: "Produto" };
    resource.callId = callId;
    media.activeSpeakerId = userA;
    media.participants = [
      { identity: userA, displayName: "Ana", hasVideo: false, bindVideo: vi.fn() },
    ];
    renderProvider();
    await screen.findByTestId("resource-call-panel");

    expect(document.querySelector(".floating-call__remote-participant")).not.toHaveClass(
      "call-speaker-surface--active",
    );
    expect(screen.getByLabelText("Ana está falando")).toBeInTheDocument();
  });

  it("never masks a resource participant's real video with the group fallback", async () => {
    resource.active = { kind: "channel", id: channelId, name: "Produto" };
    resource.callId = callId;
    // hasRemoteVideo mirrors whether ANY remote element (direct peer or
    // resource participant, same onRemoteElement stream in useCallMedia)
    // currently renders — a participant's real video already fills
    // bindRemoteMedia's flat container, so the fallback must stay hidden.
    media.hasRemoteVideo = true;
    media.participants = [
      { identity: userA, displayName: "Ana", hasVideo: true, bindVideo: vi.fn() },
    ];
    renderProvider();
    await screen.findByTestId("resource-call-panel");

    expect(document.querySelector(".floating-call__avatar")).toBeNull();
  });

  it("recovers resource ownership, including activation and failed-claim paths", async () => {
    vi.useFakeTimers();
    resource.active = { kind: "channel", id: channelId, name: "Produto" };
    resource.callId = callId;
    resource.status = "connecting";
    ownership.getLease.mockReturnValue(lease);
    renderProvider();
    act(() =>
      ownershipListener({ v: 1, type: "ready", callId, tabId: "tab-dedicated", epoch: 2 } as never),
    );
    await act(async () => undefined);
    ownership.getOwner.mockReturnValue(lease);
    await act(async () => vi.advanceTimersByTime(1500));
    ownership.getOwner.mockReturnValue(null);
    await act(async () => vi.advanceTimersByTime(1500));
    expect(ownership.claim).toHaveBeenCalledWith(callId, "main", 2, undefined);
    expect(resource.reconnect).toHaveBeenCalled();

    ownership.claim.mockResolvedValueOnce(null);
    fireEvent.click(screen.getByRole("button", { name: "Takeover probe" }));
    await act(async () => undefined);
    vi.useRealTimers();
  });

  it("converges global resource events into discovery, ignores malformed frames, and never shows a popup for them", async () => {
    renderProvider();
    fireEvent.click(screen.getByRole("button", { name: "Diretório" }));
    fireEvent.click(screen.getByRole("button", { name: "Diretório" }));
    fireEvent.click(screen.getByRole("button", { name: "Identidade" }));
    fireEvent.click(screen.getByRole("button", { name: "Identidade" }));
    act(() => socketListener.onMessage?.({}, 1));
    act(() =>
      socketListener.onMessage?.(
        {
          type: "call.ringing",
          event_id: "direct",
          target_type: "user",
          target_id: userB,
          call: { ...activeDirect(), status: "ringing" },
        },
        1,
      ),
    );
    const incoming = { ...activeDirect(), target_type: "dm", target_id: "dm-1", status: "active" };
    act(() =>
      socketListener.onMessage?.(
        {
          type: "call.accepted",
          event_id: "resource",
          target_type: "dm",
          target_id: "dm-1",
          call: incoming,
        },
        1,
      ),
    );
    expect(screen.queryByRole("dialog", { name: "Chamada recebida" })).not.toBeInTheDocument();
    expect(screen.getByTestId("discovery-dm-1")).toHaveTextContent("active");
    act(() =>
      socketListener.onMessage?.(
        {
          type: "call.ended",
          event_id: "ended",
          target_type: "dm",
          target_id: "dm-1",
          call: { ...incoming, status: "ended", version: 2 },
        },
        1,
      ),
    );
    expect(screen.queryByRole("dialog", { name: "Chamada recebida" })).not.toBeInTheDocument();
    expect(screen.getByTestId("discovery-dm-1")).toHaveTextContent("ended");
  });

  it("recovers direct media through retry, activation, failure, and exception paths", async () => {
    calls.call = activeDirect();
    renderProvider();

    fireEvent.click(screen.getByRole("button", { name: "Takeover probe" }));
    await waitFor(() => expect(calls.retryMedia).toHaveBeenCalledOnce());

    calls.mediaActivationRequired = true;
    fireEvent.click(screen.getByRole("button", { name: "Takeover probe" }));
    await waitFor(() => expect(calls.activateMedia).toHaveBeenCalledOnce());

    media.status = "error";
    fireEvent.click(screen.getByRole("button", { name: "Takeover probe" }));
    await waitFor(() => expect(screen.getByTestId("presentation")).toHaveTextContent("failed"));

    media.status = "connected";
    calls.mediaActivationRequired = false;
    calls.retryMedia.mockRejectedValueOnce(new Error("retry"));
    fireEvent.click(screen.getByRole("button", { name: "Takeover probe" }));
    await waitFor(() => expect(calls.retryMedia).toHaveBeenCalledTimes(2));
  });

  it("ignores unrelated ready messages and stale ack/failure with no attempt in flight (achado #3)", async () => {
    calls.call = activeDirect();
    renderProvider();
    act(() =>
      ownershipListener({
        v: 1,
        type: "ready",
        callId: "another-call",
        tabId: "tab-dedicated",
        epoch: 2,
      } as never),
    );
    act(() =>
      ownershipListener({ v: 1, type: "ready", callId, tabId: "tab-dedicated", epoch: 2 } as never),
    );
    expect(media.stop).not.toHaveBeenCalled();

    // No handoff attempt is in flight (getLease() is null), so these
    // ack/failure messages belong to no current attempt and must be
    // ignored rather than triggering a claim/recovery.
    act(() =>
      ownershipListener({ v: 1, type: "ack", callId, tabId: "tab-dedicated", epoch: 2 } as never),
    );
    act(() =>
      ownershipListener({
        v: 1,
        type: "failure",
        callId,
        tabId: "tab-dedicated",
        epoch: 2,
      } as never),
    );
    expect(ownership.claim).not.toHaveBeenCalled();
  });

  it("renders resource failure controls and performs resource-only retry and end", async () => {
    resource.active = { kind: "channel", id: channelId, name: "Produto" };
    resource.callId = callId;
    resource.status = "connecting";
    resource.error = "Falha no canal";
    media.status = "permission-denied";
    renderProvider();

    expect(await screen.findByTestId("resource-call-panel")).toBeInTheDocument();
    expect(screen.getByRole("status")).toHaveTextContent("Falha");
    fireEvent.click(screen.getByRole("button", { name: "Tentar mídia novamente" }));
    fireEvent.click(screen.getByRole("button", { name: "Encerrar chamada" }));
    await waitFor(() => expect(resource.reconnect).toHaveBeenCalled());
    expect(resource.leave).toHaveBeenCalled();
    // Issue #570: the main tab's own resource "leave" must announce the
    // same leaving/left participation signal a dedicated tab's leave does,
    // so a stale dedicated tab (if one exists) never reconnects this call.
    await waitFor(() =>
      expect(ownership.post).toHaveBeenCalledWith(
        expect.objectContaining({ type: "leaving", callId }),
      ),
    );
    await waitFor(() =>
      expect(ownership.post).toHaveBeenCalledWith(
        expect.objectContaining({ type: "left", callId }),
      ),
    );
  });

  it("uses the callee peer and empty participant fallback in a dedicated route", async () => {
    calls.call = { ...activeDirect(), caller_id: userB, callee_id: userA };
    media.participants = undefined as never;
    const view = renderProvider(`/call/${callId}`);
    fireEvent.click(screen.getByRole("button", { name: "Diretório" }));
    const owned = vi.mocked(useResourceCallSession).mock.calls.at(-1)![0];
    await act(() => owned.connect(calls.call as never, "token", "wss://livekit", "fresh"));
    view.rerender(providerTree(`/call/${callId}`));
    await waitFor(() =>
      expect(screen.getByTestId("presentation")).toHaveTextContent("active_dedicated_tab"),
    );
  });

  it("a stale server leave never publishes a false cross-tab left", async () => {
    resource.active = { kind: "channel", id: channelId, name: "Produto" };
    resource.callId = callId;
    resource.status = "active";
    resource.leave.mockResolvedValueOnce(false);
    renderProvider();

    fireEvent.click(screen.getByRole("button", { name: "Encerrar chamada" }));

    await waitFor(() => expect(resource.leave).toHaveBeenCalledOnce());
    expect(ownership.post).toHaveBeenCalledWith(
      expect.objectContaining({ type: "leaving", callId }),
    );
    expect(ownership.post).toHaveBeenCalledWith(
      expect.objectContaining({ type: "leave-cancelled", callId }),
    );
    expect(ownership.post).not.toHaveBeenCalledWith(
      expect.objectContaining({ type: "left", callId }),
    );
  });

  it("shows start failures globally and labels restored audio activation accurately", () => {
    calls.error = "Permissão negada";
    const view = renderProvider();
    expect(screen.getByRole("alert")).toHaveTextContent("Permissão negada");

    calls.call = { ...activeDirect(), call_type: "audio" };
    calls.mediaActivationRequired = true;
    view.rerender(providerTree());
    expect(screen.getByRole("button", { name: "Permitir microfone" })).toBeInTheDocument();
    expect(document.querySelector(".call-global-error")).toBeNull();
  });

  // Issue #622 round 2 adversarial audit (section 20): beginResourceParticipation
  // is designed to never throw (callOwnership.ts fails open to a safe local
  // fallback), but joinResourceParticipation must not silently assume that
  // forever — if it ever did throw, resource.join() already created a real
  // server-side lease (and possibly connected media) that no tab would then
  // know to converge or release. Forces the failure via the one function
  // that could theoretically throw (allocateParticipationGeneration) to
  // prove the defensive catch actually triggers real cleanup.
  it("a beginResourceParticipation failure triggers real resource.leave cleanup, never a silently orphaned lease", async () => {
    renderProvider();
    fireEvent.click(screen.getByRole("button", { name: "Diretório" }));
    knownJoinProbeCallId = callId;
    ownership.allocateParticipationGeneration.mockImplementationOnce(() => {
      throw new Error("storage exploded");
    });

    fireEvent.click(screen.getByRole("button", { name: "Known join probe" }));

    await waitFor(() => expect(resource.join).toHaveBeenCalled());
    await waitFor(() => expect(resource.leave).toHaveBeenCalledOnce());
    // The participation was never actually registered — no "participating"
    // broadcast for a lease this tab does not actually believe it holds.
    expect(ownership.post).not.toHaveBeenCalledWith(
      expect.objectContaining({ type: "participating", callId }),
    );
  });

  // Issue #622 round 2 adversarial audit (section 22): a known-call join
  // that the server refuses (call_not_found/call_invalid_state, or simply
  // busy) must resync that target afterward, so a stale pre-click discovery
  // guess converges to whatever the server now says.
  it("a failed known-call join resyncs that target's discovery afterward", async () => {
    resource.join.mockResolvedValueOnce(undefined);
    renderProvider();
    fireEvent.click(screen.getByRole("button", { name: "Diretório" }));
    knownJoinProbeCallId = callId;

    fireEvent.click(screen.getByRole("button", { name: "Known join probe" }));

    await waitFor(() =>
      expect(syncResourceCall).toHaveBeenCalledWith({ kind: "channel", id: channelId }),
    );
  });

  // Issue #622 round 2 adversarial audit (sections 11/27): repeated
  // navigation between two already-known routes must not accumulate a new
  // acquireChatSocket consumer (and its paired setConsumerSubscriptions
  // call) per navigation — the global subscription effect's deps
  // (applyResourceCallEvent, directory, getOwnership, requestResourceCallSync)
  // are all stable/unchanged across a route change alone.
  it("repeated navigation between known routes never accumulates a second global socket subscription", async () => {
    renderProvider();
    fireEvent.click(screen.getByRole("button", { name: "Diretório" }));
    const initialAcquireCalls = vi.mocked(acquireChatSocket).mock.calls.length;

    fireEvent.click(screen.getByRole("button", { name: "Canal X" }));
    fireEvent.click(screen.getByRole("button", { name: "DM grupo" }));
    fireEvent.click(screen.getByRole("button", { name: "Canal X" }));
    fireEvent.click(screen.getByRole("button", { name: "DM grupo" }));

    expect(vi.mocked(acquireChatSocket).mock.calls.length).toBe(initialAcquireCalls);
  });

  // Issue #622 round 2 adversarial audit (section 27): removing a target
  // from the directory purges its discovery observation while an unrelated
  // target already known stays intact — proven here through registerDirectory
  // itself (not just the reducer in isolation, see resourceCallDiscovery.test.ts).
  it("directory-driven pruning at the provider level: removing Y purges its discovery while X stays intact", async () => {
    renderProvider();
    fireEvent.click(screen.getByRole("button", { name: "Diretório" }));
    act(() =>
      socketListener.onMessage?.(
        {
          type: "call.accepted",
          event_id: "event-prune-x",
          target_type: "channel",
          target_id: channelId,
          call: {
            ...activeDirect(),
            target_type: "channel",
            target_id: channelId,
            status: "active",
          },
        },
        1,
      ),
    );
    act(() =>
      socketListener.onMessage?.(
        {
          type: "call.accepted",
          event_id: "event-prune-y",
          target_type: "channel",
          target_id: channelYId,
          call: {
            ...activeDirect(),
            call_id: "00000000-0000-4000-8000-000000000560",
            target_type: "channel",
            target_id: channelYId,
            status: "active",
          },
        },
        1,
      ),
    );
    expect(screen.getByTestId("discovery-channel-x")).toHaveTextContent("active");
    expect(screen.getByTestId("discovery-channel-y")).toHaveTextContent("active");

    fireEvent.click(screen.getByRole("button", { name: "Diretório (sem Y)" }));

    expect(screen.getByTestId("discovery-channel-y")).toHaveTextContent("none");
    expect(screen.getByTestId("discovery-channel-x")).toHaveTextContent("active");
  });

  // ── #642 active resource call bar vs FloatingCallWindow (review fixes) ───

  function activeResourceCall(overrides: Partial<Call> = {}): Call {
    return {
      call_id: callId,
      request_id: "req-resource",
      caller_id: userA,
      callee_id: "",
      target_type: "channel",
      target_id: channelId,
      call_type: "audio",
      status: "active",
      version: 1,
      created_at: "2026-08-18T12:00:00Z",
      occurred_at: "2026-08-18T12:00:00Z",
      expires_at: "2026-08-18T13:00:00Z",
      ...overrides,
    } satisfies Call;
  }

  // Satisfies EVERY ingredient resourcePresentationCall requires: local
  // participation, matching discovery, resource+media settled, local
  // ownership — the full atomic predicate, never just resource.active alone
  // (issue #642 review, blockers 1-3). Broadcasts discovery the same way
  // the real server would (a resource lifecycle event, never a direct
  // field write) and drives ownership through the real ownedMedia.connect
  // bridge (via the mocked useResourceCallSession's own `owned` bridge arg)
  // so ownerState genuinely becomes "local", not just asserted.
  async function makeResourcePresentationReady(targetChannelId = channelId, targetCallId = callId) {
    resource.active = { kind: "channel", id: targetChannelId, name: "Produto" };
    resource.callId = targetCallId;
    resource.status = "active";
    media.status = "connected";
    act(() =>
      socketListener.onMessage?.(
        {
          type: "call.accepted",
          event_id: `discovery-${targetCallId}`,
          target_type: "channel",
          target_id: targetChannelId,
          call: activeResourceCall({ call_id: targetCallId, target_id: targetChannelId }),
        },
        1,
      ),
    );
    const owned = vi.mocked(useResourceCallSession).mock.calls.at(-1)![0];
    await act(() =>
      owned.connect(
        { call_id: targetCallId, call_type: "audio" } as never,
        "token",
        "wss://livekit",
        "fresh",
      ),
    );
  }

  it("never suppresses the resource FloatingCallWindow even when resourcePresentationCall is fully ready (issue #657 regression fix)", async () => {
    renderProvider();
    fireEvent.click(screen.getByRole("button", { name: "Diretório" }));
    fireEvent.click(screen.getByRole("button", { name: "Canal X" }));
    await makeResourcePresentationReady();

    await waitFor(() =>
      expect(screen.getByTestId("resource-presentation-call")).toHaveTextContent(callId),
    );
    // #657: FloatingCallWindow coexists with the bar, it is never suppressed.
    // It must still provide the full surface controls (camera, screen share, open in new tab).
    const panel = screen.getByTestId("resource-call-panel");
    expect(panel).toBeInTheDocument();

    // Prove it still offers camera, screen share, and "Expandir em nova aba"
    const cameraBtn = within(panel).getByRole("button", { name: "Ativar câmera" });
    const screenShareBtn = within(panel).getByRole("button", { name: "Compartilhar tela" });
    expect(within(panel).getByRole("button", { name: "Expandir em nova aba" })).toBeInTheDocument();

    expect(cameraBtn).toBeEnabled();
    expect(screenShareBtn).toBeEnabled();
  });

  it("keeps the floating window when discovery still shows a DIFFERENT call_id for the same target (call.admitted/call.accepted have no ordering guarantee)", async () => {
    resource.active = { kind: "channel", id: channelId, name: "Produto" };
    resource.callId = callId;
    resource.status = "active";
    media.status = "connected";
    renderProvider();
    fireEvent.click(screen.getByRole("button", { name: "Diretório" }));
    fireEvent.click(screen.getByRole("button", { name: "Canal X" }));
    // Discovery still holds the OLD call for this exact target — a stale
    // observation that has not yet converged to this participation's callId.
    act(() =>
      socketListener.onMessage?.(
        {
          type: "call.accepted",
          event_id: "discovery-old",
          target_type: "channel",
          target_id: channelId,
          call: activeResourceCall({ call_id: "00000000-0000-4000-8000-000000000999" }),
        },
        1,
      ),
    );
    const owned = vi.mocked(useResourceCallSession).mock.calls.at(-1)![0];
    await act(() =>
      owned.connect(
        { call_id: callId, call_type: "audio" } as never,
        "token",
        "wss://livekit",
        "fresh",
      ),
    );

    expect(screen.getByTestId("resource-presentation-call")).toHaveTextContent("none");
    expect(await screen.findByTestId("resource-call-panel")).toBeInTheDocument();
  });

  it("keeps the floating window when discovery for this target is terminal (never active)", async () => {
    resource.active = { kind: "channel", id: channelId, name: "Produto" };
    resource.callId = callId;
    resource.status = "active";
    media.status = "connected";
    renderProvider();
    fireEvent.click(screen.getByRole("button", { name: "Diretório" }));
    fireEvent.click(screen.getByRole("button", { name: "Canal X" }));
    act(() =>
      socketListener.onMessage?.(
        {
          type: "call.ended",
          event_id: "discovery-ended",
          target_type: "channel",
          target_id: channelId,
          call: activeResourceCall({ status: "ended" }),
        },
        1,
      ),
    );
    const owned = vi.mocked(useResourceCallSession).mock.calls.at(-1)![0];
    await act(() =>
      owned.connect(
        { call_id: callId, call_type: "audio" } as never,
        "token",
        "wss://livekit",
        "fresh",
      ),
    );

    expect(screen.getByTestId("resource-presentation-call")).toHaveTextContent("none");
    expect(await screen.findByTestId("resource-call-panel")).toBeInTheDocument();
  });

  it("keeps the floating window while this target has no discovery observation at all yet", async () => {
    resource.active = { kind: "channel", id: channelId, name: "Produto" };
    resource.callId = callId;
    resource.status = "active";
    media.status = "connected";
    renderProvider();
    fireEvent.click(screen.getByRole("button", { name: "Diretório" }));
    fireEvent.click(screen.getByRole("button", { name: "Canal X" }));
    const owned = vi.mocked(useResourceCallSession).mock.calls.at(-1)![0];
    await act(() =>
      owned.connect(
        { call_id: callId, call_type: "audio" } as never,
        "token",
        "wss://livekit",
        "fresh",
      ),
    );

    expect(screen.getByTestId("resource-presentation-call")).toHaveTextContent("none");
    expect(await screen.findByTestId("resource-call-panel")).toBeInTheDocument();
  });

  it("keeps the floating window while resource.status is connecting, even at the matching route with valid discovery", async () => {
    resource.active = { kind: "channel", id: channelId, name: "Produto" };
    resource.callId = callId;
    resource.status = "connecting";
    media.status = "connected";
    renderProvider();
    fireEvent.click(screen.getByRole("button", { name: "Diretório" }));
    fireEvent.click(screen.getByRole("button", { name: "Canal X" }));
    act(() =>
      socketListener.onMessage?.(
        {
          type: "call.accepted",
          event_id: "discovery-connecting",
          target_type: "channel",
          target_id: channelId,
          call: activeResourceCall(),
        },
        1,
      ),
    );

    expect(screen.getByTestId("resource-presentation-call")).toHaveTextContent("none");
    expect(await screen.findByTestId("resource-call-panel")).toBeInTheDocument();
  });

  it("keeps the floating window (showing its own status) while media is reconnecting, never handing off to the bar mid-reconnect", async () => {
    renderProvider();
    fireEvent.click(screen.getByRole("button", { name: "Diretório" }));
    fireEvent.click(screen.getByRole("button", { name: "Canal X" }));
    await makeResourcePresentationReady();
    await waitFor(() => expect(screen.getByTestId("resource-call-panel")).toBeInTheDocument());

    // Media drops to reconnecting — the bar has no UI for this state.
    media.status = "reconnecting";
    fireEvent.click(screen.getByRole("button", { name: "Diretório" }));

    expect(screen.getByTestId("resource-presentation-call")).toHaveTextContent("none");
    expect(await screen.findByTestId("resource-call-panel")).toBeInTheDocument();
    expect(within(screen.getByTestId("resource-call-panel")).getByRole("status")).toHaveTextContent(
      "Reconectando",
    );
  });

  it("keeps the floating window (with its own error/retry UI) on a resource error, never hiding the retry affordance behind the bar", async () => {
    resource.active = { kind: "channel", id: channelId, name: "Produto" };
    resource.callId = callId;
    resource.status = "error";
    resource.error = "Falha no canal";
    media.status = "connected";
    renderProvider();
    fireEvent.click(screen.getByRole("button", { name: "Diretório" }));
    fireEvent.click(screen.getByRole("button", { name: "Canal X" }));
    act(() =>
      socketListener.onMessage?.(
        {
          type: "call.accepted",
          event_id: "discovery-error",
          target_type: "channel",
          target_id: channelId,
          call: activeResourceCall(),
        },
        1,
      ),
    );

    expect(screen.getByTestId("resource-presentation-call")).toHaveTextContent("none");
    const panel = await screen.findByTestId("resource-call-panel");
    // resource.error surfaces through FloatingCallWindow's own role="alert"
    // recovery text (never floatingStatus's role="status" header, which
    // tracks media.status, left "connected" here on purpose) — the retry
    // affordance the bar has no UI for.
    expect(within(panel).getByRole("alert")).toHaveTextContent("Falha no canal");
    expect(within(panel).getByRole("button", { name: "Tentar mídia novamente" })).toBeEnabled();
  });

  it("keeps the floating window when navigating away and on return, never duplicated (issue #657)", async () => {
    renderProvider();
    fireEvent.click(screen.getByRole("button", { name: "Diretório" }));
    fireEvent.click(screen.getByRole("button", { name: "Canal X" }));
    await makeResourcePresentationReady();
    await waitFor(() => expect(screen.getByTestId("resource-call-panel")).toBeInTheDocument());

    fireEvent.click(screen.getByRole("button", { name: "Canal Y" }));
    expect(await screen.findByTestId("resource-call-panel")).toBeInTheDocument();
    expect(screen.getAllByTestId("resource-call-panel")).toHaveLength(1);

    fireEvent.click(screen.getByRole("button", { name: "Canal X" }));
    await waitFor(() => expect(screen.getByTestId("resource-call-panel")).toBeInTheDocument());
    // Navigating A -> B -> A never reconnected media.
    expect(media.stop).not.toHaveBeenCalled();
  });

  it("keeps GlobalCallIndicator (never FloatingCallWindow, never the bar) when ownerState is remote, even at the matching route", async () => {
    renderProvider();
    fireEvent.click(screen.getByRole("button", { name: "Diretório" }));
    fireEvent.click(screen.getByRole("button", { name: "Canal X" }));
    await makeResourcePresentationReady();
    await waitFor(() => expect(screen.getByTestId("resource-call-panel")).toBeInTheDocument());

    act(() => ownershipLost());
    await waitFor(() => expect(screen.getByTestId("owner")).toHaveTextContent("remote"));
    expect(screen.getByTestId("resource-presentation-call")).toHaveTextContent("none");
    expect(screen.queryByTestId("resource-call-panel")).not.toBeInTheDocument();
    expect(screen.getByText("Chamada aberta em outra aba")).toBeInTheDocument();
  });

  it("clears resourcePresentationCall the instant leaving starts — before endResourceParticipation's own server round trip resolves", async () => {
    let resolveLeave!: (released: boolean) => void;
    resource.leave.mockReturnValueOnce(
      new Promise<boolean>((resolve) => {
        resolveLeave = resolve;
      }),
    );
    renderProvider();
    fireEvent.click(screen.getByRole("button", { name: "Diretório" }));
    fireEvent.click(screen.getByRole("button", { name: "Canal X" }));
    await makeResourcePresentationReady();
    await waitFor(() =>
      expect(screen.getByTestId("resource-presentation-call")).toHaveTextContent(callId),
    );

    fireEvent.click(screen.getByRole("button", { name: "Leave resource participation probe" }));
    // Still unresolved — resource.leave()'s own promise is deliberately
    // deferred — yet the participation authority already reflects "leaving"
    // synchronously (continueResourceParticipation runs before any await),
    // so resourcePresentationCall must already be null right now.
    await waitFor(() =>
      expect(screen.getByTestId("resource-presentation-call")).toHaveTextContent("none"),
    );
    expect(resource.leave).toHaveBeenCalled();

    resolveLeave(true);
    await waitFor(() =>
      expect(ownership.post).toHaveBeenCalledWith(
        expect.objectContaining({ type: "left", callId }),
      ),
    );
    // Stays removed once the leave actually succeeds.
    expect(screen.getByTestId("resource-presentation-call")).toHaveTextContent("none");
  });

  it("a leave failure restores participation but keeps the bar ineligible while the resource error is presented by the floating window", async () => {
    // Models the REAL useResourceCallSession.leave() rejection branch
    // exactly (issue #642 review follow-up): a genuine rejection sets
    // resource.status "error" and resource.error alongside throwing — never
    // just a bare throw with resource.status left untouched. The mutation
    // happens synchronously inside the mock so it's already in place by the
    // time endResourceParticipation's own "leave-cancelled" participation
    // update (a real setState) triggers the re-render that reads it — no
    // extra artificial test-only rerender trigger needed.
    resource.leave.mockImplementationOnce(async () => {
      resource.status = "error";
      resource.error = "Não foi possível sair da chamada. Tente novamente.";
      throw new Error("leave failed");
    });
    renderProvider();
    fireEvent.click(screen.getByRole("button", { name: "Diretório" }));
    fireEvent.click(screen.getByRole("button", { name: "Canal X" }));
    await makeResourcePresentationReady();
    // 1. resourcePresentationCall existed before leaving.
    await waitFor(() =>
      expect(screen.getByTestId("resource-presentation-call")).toHaveTextContent(callId),
    );

    fireEvent.click(screen.getByRole("button", { name: "Leave resource participation probe" }));
    // 2. Gone immediately once leaving starts, Promise still in flight.
    await waitFor(() =>
      expect(screen.getByTestId("resource-presentation-call")).toHaveTextContent("none"),
    );

    // 3 & 4. The rejection converges participation back to "participating"
    // — continueResourceParticipation("leave-cancelled") — purely via the
    // SAME participationRecords authority activeCallId already reads, never
    // a second local "isLeaving"/error flag.
    await waitFor(() =>
      expect(ownership.post).toHaveBeenCalledWith(
        expect.objectContaining({ type: "leave-cancelled", callId }),
      ),
    );
    // 5. resourcePresentationCall stays "none": participation is
    // reconnectable again, but resource.status is now "error", not
    // "active" — the predicate's own resource.status === "active"
    // requirement is exactly what keeps the bar from wrongly reappearing
    // over a call that just failed to leave cleanly.
    expect(screen.getByTestId("resource-presentation-call")).toHaveTextContent("none");
    // 6, 7 & 8. FloatingCallWindow is what actually presents this failure —
    // its own role="alert" and retry affordance, the existing authority,
    // never anything reinvented in the bar.
    const panel = await screen.findByTestId("resource-call-panel");
    expect(within(panel).getByRole("alert")).toHaveTextContent(
      "Não foi possível sair da chamada. Tente novamente.",
    );
    expect(within(panel).getByRole("button", { name: "Tentar mídia novamente" })).toBeEnabled();
  });

  it("a direct call's FloatingCallWindow is unaffected by the #642 route-suppression condition", async () => {
    calls.call = activeDirect();
    renderProvider();
    fireEvent.click(screen.getByRole("button", { name: "Diretório" }));
    fireEvent.click(screen.getByRole("button", { name: "Canal X" }));
    // resourceTarget is null whenever a direct call is active — the new
    // suppression clause can never apply to it, route or no route.
    expect(await screen.findByTestId("floating-call-window")).toBeInTheDocument();
  });
});

describe("dedicated tab reload/reopen recovery (achado #1)", () => {
  beforeEach(() => {
    vi.useRealTimers();
    vi.clearAllMocks();
    participationStorage = {};
    calls.call = null;
    resource.active = null;
    resource.callId = null;
    ownership.getLease.mockReturnValue(null);
    ownership.getOwner.mockReturnValue(null);
    ownership.claim.mockResolvedValue(lease);
    mediaIntentStorage = {};
    ownership.writeMediaIntent.mockReset();
    ownership.writeMediaIntent.mockImplementation(defaultWriteMediaIntent);
    ownership.readMediaIntentForLease.mockReset();
    ownership.readMediaIntentForLease.mockImplementation(defaultReadMediaIntentForLease);
    media.connect.mockResolvedValue({ microphone: true, camera: true });
    vi.mocked(createOwnershipCoordinator).mockReturnValue(ownership as never);
    vi.mocked(useCallMedia).mockReturnValue(media as never);
    vi.mocked(useCallSignaling).mockReturnValue(calls as never);
    vi.mocked(useResourceCallSession).mockReturnValue(resource as never);
    vi.mocked(acquireChatSocket).mockImplementation((listener) => {
      socketListener = listener;
      return { send: vi.fn(), isOpen: vi.fn(), generation: vi.fn(), release: vi.fn() };
    });
  });

  it("dedicated aberta diretamente: claims immediately when there is no owner at all", async () => {
    renderProvider(`/call/${callId}`);
    fireEvent.click(screen.getByRole("button", { name: "Ready probe" }));
    await waitFor(() => expect(ownership.claim).toHaveBeenCalledWith(callId, "dedicated", 0));
    await waitFor(() => expect(screen.getByTestId("owner")).toHaveTextContent("local"));
  });

  it("owner inexistente: never depends on another tab replying", async () => {
    renderProvider(`/call/${callId}`);
    fireEvent.click(screen.getByRole("button", { name: "Ready probe" }));
    await waitFor(() => expect(ownership.claim).toHaveBeenCalledTimes(1));
    expect(ownership.claim).toHaveBeenCalledWith(callId, "dedicated", 0);
  });

  it("timeout: waits the full bounded window before claiming when a lease is present", async () => {
    vi.useFakeTimers();
    ownership.getOwner.mockReturnValue(lease);
    renderProvider(`/call/${callId}`);
    fireEvent.click(screen.getByRole("button", { name: "Ready probe" }));
    await act(async () => vi.advanceTimersByTime(4_999));
    expect(ownership.claim).not.toHaveBeenCalled();
    await act(async () => vi.advanceTimersByTime(1));
    expect(ownership.claim).toHaveBeenCalledWith(callId, "dedicated", lease.epoch);
  });

  it("reload da dedicated owner: a stale lease from this tab's earlier life is reclaimed", async () => {
    vi.useFakeTimers();
    ownership.getOwner.mockReturnValue(lease);
    renderProvider(`/call/${callId}`);
    fireEvent.click(screen.getByRole("button", { name: "Ready probe" }));
    await act(async () => vi.advanceTimersByTime(5_000));
    expect(ownership.claim).toHaveBeenCalledWith(callId, "dedicated", lease.epoch);
    expect(screen.getByTestId("owner")).toHaveTextContent("local");
  });

  it("ready sem resposta: no handoff message ever arrives, recovery still completes", async () => {
    vi.useFakeTimers();
    ownership.getOwner.mockReturnValue(lease);
    renderProvider(`/call/${callId}`);
    fireEvent.click(screen.getByRole("button", { name: "Ready probe" }));
    await act(async () => vi.advanceTimersByTime(2_000));
    expect(ownership.claim).not.toHaveBeenCalled();
    await act(async () => vi.advanceTimersByTime(3_000));
    expect(ownership.claim).toHaveBeenCalledWith(callId, "dedicated", lease.epoch);
  });

  it("owner saudável existente: a real handoff reply wins and cancels the recovery timer", async () => {
    vi.useFakeTimers();
    ownership.getOwner.mockReturnValue(lease);
    renderProvider(`/call/${callId}`);
    fireEvent.click(screen.getByRole("button", { name: "Ready probe" }));
    act(() =>
      ownershipListener({
        v: 1,
        type: "handoff",
        callId,
        tabId: "tab-main",
        targetTabId: ownership.tabId,
        epoch: lease.epoch,
      } as never),
    );
    await act(async () => undefined);
    expect(ownership.claim).toHaveBeenCalledWith(callId, "dedicated", lease.epoch, {
      tabId: "tab-main",
      epoch: lease.epoch,
    });
    expect(ownership.claim).toHaveBeenCalledTimes(1);
    // The recovery timer must have been cancelled: advancing past its
    // window never produces a second, redundant claim.
    await act(async () => vi.advanceTimersByTime(5_000));
    expect(ownership.claim).toHaveBeenCalledTimes(1);
  });

  it("recovery bem-sucedido: sets local ownership and never surfaces a recovery failure", async () => {
    vi.useFakeTimers();
    ownership.getOwner.mockReturnValue(lease);
    renderProvider(`/call/${callId}`);
    fireEvent.click(screen.getByRole("button", { name: "Ready probe" }));
    await act(async () => vi.advanceTimersByTime(5_000));
    expect(screen.getByTestId("owner")).toHaveTextContent("local");
    expect(screen.getByTestId("dedicated-recovery-failed")).toHaveTextContent("false");
  });

  it("recovery impossível: surfaces a visible, terminal fallback instead of spinning forever", async () => {
    vi.useFakeTimers();
    ownership.getOwner.mockReturnValue(lease);
    ownership.claim.mockResolvedValueOnce(null);
    renderProvider(`/call/${callId}`);
    fireEvent.click(screen.getByRole("button", { name: "Ready probe" }));
    await act(async () => vi.advanceTimersByTime(5_000));
    expect(screen.getByTestId("dedicated-recovery-failed")).toHaveTextContent("true");
    expect(screen.getByTestId("owner")).not.toHaveTextContent("local");
  });

  it("cleans up the recovery timer on unmount instead of firing a late claim", async () => {
    vi.useFakeTimers();
    ownership.getOwner.mockReturnValue(lease);
    const view = renderProvider(`/call/${callId}`);
    fireEvent.click(screen.getByRole("button", { name: "Ready probe" }));
    view.unmount();
    await act(async () => vi.advanceTimersByTime(5_000));
    expect(ownership.claim).not.toHaveBeenCalled();
  });
});

describe("stale handoff replies never move ownership after HANDOFF_TIMEOUT (achado #3)", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    participationStorage = {};
    calls.call = activeDirect();
    resource.active = null;
    resource.callId = null;
    ownership.getLease.mockReturnValue(lease);
    ownership.getOwner.mockReturnValue(null);
    ownership.claim.mockResolvedValue(lease);
    mediaIntentStorage = {};
    ownership.writeMediaIntent.mockReset();
    ownership.writeMediaIntent.mockImplementation(defaultWriteMediaIntent);
    ownership.readMediaIntentForLease.mockReset();
    ownership.readMediaIntentForLease.mockImplementation(defaultReadMediaIntentForLease);
    media.connect.mockResolvedValue({ microphone: true, camera: true });
    vi.mocked(createOwnershipCoordinator).mockReturnValue(ownership as never);
    vi.mocked(useCallMedia).mockReturnValue(media as never);
    vi.mocked(useCallSignaling).mockReturnValue(calls as never);
    vi.mocked(useResourceCallSession).mockReturnValue(resource as never);
    vi.mocked(acquireChatSocket).mockImplementation((listener) => {
      socketListener = listener;
      return { send: vi.fn(), isOpen: vi.fn(), generation: vi.fn(), release: vi.fn() };
    });
  });

  function startHandoff() {
    act(() =>
      ownershipListener({ v: 1, type: "ready", callId, tabId: "tab-dedicated", epoch: 2 } as never),
    );
  }

  it("an ACK arriving immediately after the timeout is ignored: recovery already started", async () => {
    vi.useFakeTimers();
    renderProvider();
    startHandoff();
    await act(async () => undefined);
    await act(async () => vi.advanceTimersByTime(6_000));
    expect(ownership.claim).toHaveBeenCalledWith(callId, "main", 2, undefined);
    const claimsBeforeAck = ownership.claim.mock.calls.length;

    // The dedicated tab's epoch for this (now-abandoned) attempt.
    act(() =>
      ownershipListener({ v: 1, type: "ack", callId, tabId: "tab-dedicated", epoch: 3 } as never),
    );
    expect(ownership.claim).toHaveBeenCalledTimes(claimsBeforeAck);
    expect(screen.getByTestId("presentation")).not.toHaveTextContent("active_dedicated_tab");
    vi.useRealTimers();
  });

  it("a very late ACK (long after timeout/recovery) is still ignored", async () => {
    vi.useFakeTimers();
    renderProvider();
    startHandoff();
    await act(async () => undefined);
    await act(async () => vi.advanceTimersByTime(6_000));
    await act(async () => vi.advanceTimersByTime(30_000));
    const claimsBeforeAck = ownership.claim.mock.calls.length;

    act(() =>
      ownershipListener({ v: 1, type: "ack", callId, tabId: "tab-dedicated", epoch: 3 } as never),
    );
    expect(ownership.claim).toHaveBeenCalledTimes(claimsBeforeAck);
    vi.useRealTimers();
  });

  it("a late failure after timeout/recovery never re-triggers recovery", async () => {
    vi.useFakeTimers();
    renderProvider();
    startHandoff();
    await act(async () => undefined);
    await act(async () => vi.advanceTimersByTime(6_000));
    const claimsBeforeFailure = ownership.claim.mock.calls.length;

    act(() =>
      ownershipListener({
        v: 1,
        type: "failure",
        callId,
        tabId: "tab-dedicated",
        epoch: 3,
      } as never),
    );
    expect(ownership.claim).toHaveBeenCalledTimes(claimsBeforeFailure);
  });

  it("recovery starting before the ACK arrives still converges to a single owner", async () => {
    vi.useFakeTimers();
    renderProvider();
    startHandoff();
    await act(async () => undefined);
    // Recovery (timeout-triggered restoreOwnership) begins first...
    await act(async () => vi.advanceTimersByTime(6_000));
    expect(ownership.claim).toHaveBeenCalledWith(callId, "main", 2, undefined);
    // ...then the ACK for the original attempt finally shows up. It must
    // not flip presentation back to "active_dedicated_tab" once the main
    // tab has already moved into recovery for this call.
    act(() =>
      ownershipListener({ v: 1, type: "ack", callId, tabId: "tab-dedicated", epoch: 3 } as never),
    );
    expect(screen.getByTestId("presentation")).not.toHaveTextContent("active_dedicated_tab");
    vi.useRealTimers();
  });

  it("a fresh claim after recovery always uses a higher epoch than the abandoned attempt", async () => {
    vi.useFakeTimers();
    renderProvider();
    startHandoff();
    await act(async () => undefined);
    await act(async () => vi.advanceTimersByTime(6_000));
    expect(ownership.claim).toHaveBeenCalledWith(callId, "main", 2, undefined);
    vi.useRealTimers();
  });
});

describe("releaseDedicated stops media before releasing ownership (achado #4)", () => {
  beforeEach(() => {
    vi.useRealTimers();
    vi.clearAllMocks();
    participationStorage = {};
    calls.call = null;
    resource.active = null;
    resource.callId = null;
    ownership.getLease.mockReturnValue(null);
    ownership.getOwner.mockReturnValue(null);
    ownership.claim.mockResolvedValue(lease);
    mediaIntentStorage = {};
    ownership.writeMediaIntent.mockReset();
    ownership.writeMediaIntent.mockImplementation(defaultWriteMediaIntent);
    ownership.readMediaIntentForLease.mockReset();
    ownership.readMediaIntentForLease.mockImplementation(defaultReadMediaIntentForLease);
    media.connect.mockResolvedValue({ microphone: true, camera: true });
    vi.mocked(createOwnershipCoordinator).mockReturnValue(ownership as never);
    vi.mocked(useCallMedia).mockReturnValue(media as never);
    vi.mocked(useCallSignaling).mockReturnValue(calls as never);
    vi.mocked(useResourceCallSession).mockReturnValue(resource as never);
    vi.mocked(acquireChatSocket).mockImplementation((listener) => {
      socketListener = listener;
      return { send: vi.fn(), isOpen: vi.fn(), generation: vi.fn(), release: vi.fn() };
    });
  });

  it("awaits media.stop() before ownership.release(), leaving no residual media in this tab", async () => {
    const order: string[] = [];
    media.stop.mockImplementationOnce(async () => {
      order.push("stop");
    });
    ownership.release.mockImplementationOnce(() => {
      order.push("release");
    });
    renderProvider(`/call/${callId}`);
    fireEvent.click(screen.getByRole("button", { name: "Release probe" }));
    await waitFor(() => expect(ownership.release).toHaveBeenCalledWith(callId));
    expect(order).toEqual(["stop", "release"]);
    await waitFor(() => expect(screen.getByTestId("owner")).toHaveTextContent("remote"));
  });

  it("is safe to call twice (idempotent cleanup)", async () => {
    renderProvider(`/call/${callId}`);
    fireEvent.click(screen.getByRole("button", { name: "Release probe" }));
    fireEvent.click(screen.getByRole("button", { name: "Release probe" }));
    await waitFor(() => expect(ownership.release).toHaveBeenCalled());
    expect(media.stop).toHaveBeenCalled();
  });

  it("lets the main tab recover ownership normally once the dedicated tab releases", async () => {
    resource.active = { kind: "channel", id: channelId, name: "Produto" };
    resource.callId = callId;
    resource.status = "connecting";
    ownership.getLease.mockReturnValue(lease);
    vi.useFakeTimers();
    renderProvider();
    act(() =>
      ownershipListener({ v: 1, type: "ready", callId, tabId: "tab-dedicated", epoch: 2 } as never),
    );
    await act(async () => undefined);
    // Simulate the dedicated tab releasing the lease: the main tab's
    // recovery interval must reclaim ownership on its own next tick.
    ownership.getOwner.mockReturnValue(null);
    await act(async () => vi.advanceTimersByTime(1_500));
    expect(ownership.claim).toHaveBeenCalledWith(callId, "main", 2, undefined);
    expect(resource.reconnect).toHaveBeenCalled();
  });
});

// Issue #611: only the current owner of the ACTIVE call may start screen
// share; stop must always be allowed through so an active share can never
// get stuck when the lease vanishes; and no ownership transition may ever
// auto-resume a share on the new/recovered owner (screen share is never
// persisted, unlike #610's mic/camera MediaIntent).
describe("screen-share ownership fence and non-resumption (issue #611)", () => {
  beforeEach(() => {
    vi.useRealTimers();
    vi.clearAllMocks();
    participationStorage = {};
    calls.call = null;
    calls.pending = false;
    calls.cancelling = false;
    calls.error = null;
    calls.mediaActivationRequired = false;
    resource.active = null;
    resource.callId = null;
    resource.status = "idle";
    resource.error = null;
    media.status = "connected";
    media.error = null;
    media.activeSpeakerId = null;
    media.participants = [];
    media.screenShareEnabled = false;
    ownership.getLease.mockReturnValue(null);
    ownership.getOwner.mockReturnValue(null);
    ownership.claim.mockResolvedValue(lease);
    mediaIntentStorage = {};
    ownership.writeMediaIntent.mockReset();
    ownership.writeMediaIntent.mockImplementation(defaultWriteMediaIntent);
    ownership.readMediaIntentForLease.mockReset();
    ownership.readMediaIntentForLease.mockImplementation(defaultReadMediaIntentForLease);
    media.connect.mockResolvedValue({ microphone: true, camera: true });
    vi.mocked(createOwnershipCoordinator).mockReturnValue(ownership as never);
    vi.mocked(useCallMedia).mockReturnValue(media as never);
    vi.mocked(useCallSignaling).mockReturnValue(calls as never);
    vi.mocked(useResourceCallSession).mockReturnValue(resource as never);
    vi.mocked(acquireChatSocket).mockImplementation((listener) => {
      socketListener = listener;
      return { send: vi.fn(), isOpen: vi.fn(), generation: vi.fn(), release: vi.fn() };
    });
  });

  it("a tab with no current lease cannot start screen share", () => {
    renderProvider();
    fireEvent.click(screen.getByRole("button", { name: "Toggle screen share probe" }));
    expect(media.toggleScreenShare).not.toHaveBeenCalled();
  });

  it("a lease for a different call cannot authorize screen-share START", async () => {
    renderProvider();
    const owned = vi.mocked(useResourceCallSession).mock.calls[0]![0];
    await act(() => owned.connect(activeDirect() as never, "token", "wss://livekit", "fresh"));
    expect(screen.getByTestId("owner")).toHaveTextContent("local");

    // This tab's own active call is now a DIFFERENT call than the lease it
    // holds (e.g. a stale lease from a prior call) — force a re-render so
    // activeCallId is recomputed before probing. ownership.getLease is set
    // to the ORIGINAL lease (matching the fixture `callId`), so this
    // specifically exercises the callId-mismatch branch, never "no lease".
    ownership.getLease.mockReturnValue(lease);
    calls.call = { ...activeDirect(), call_id: "00000000-0000-4000-8000-000000000999" };
    fireEvent.click(screen.getByRole("button", { name: "Perfil" }));
    fireEvent.click(screen.getByRole("button", { name: "Toggle screen share probe" }));

    expect(media.toggleScreenShare).not.toHaveBeenCalled();
  });

  it("the current owner can start screen share on a direct call", async () => {
    renderProvider();
    const owned = vi.mocked(useResourceCallSession).mock.calls[0]![0];
    await act(() => owned.connect(activeDirect() as never, "token", "wss://livekit", "fresh"));
    expect(screen.getByTestId("owner")).toHaveTextContent("local");

    // ownedMedia.connect() only claims the lease — it never drives
    // useCallSignaling's own `calls.call`/getLease() mocks, which the test
    // must keep in sync itself (same pattern as the "claims media once..."
    // test above) so activeCallId/getLease() reflect this tab's real call.
    calls.call = activeDirect();
    ownership.getLease.mockReturnValue(lease);
    fireEvent.click(screen.getByRole("button", { name: "Perfil" }));
    fireEvent.click(screen.getByRole("button", { name: "Toggle screen share probe" }));

    await waitFor(() => expect(media.toggleScreenShare).toHaveBeenCalledOnce());
  });

  it("the current owner can start screen share on a resource/group call", async () => {
    renderProvider();
    const owned = vi.mocked(useResourceCallSession).mock.calls[0]![0];
    await act(() => owned.connect(activeDirect() as never, "token", "wss://livekit", "fresh"));
    expect(screen.getByTestId("owner")).toHaveTextContent("local");

    // Switch this tab's active call to the resource-call fixture (same
    // callId as the held lease) so activeCallId is derived from the
    // resource branch instead of the direct-call branch.
    calls.call = null;
    resource.active = { kind: "channel", id: channelId, name: "Produto" };
    resource.callId = callId;
    resource.status = "active";
    ownership.getLease.mockReturnValue(lease);
    fireEvent.click(screen.getByRole("button", { name: "Perfil" }));
    fireEvent.click(screen.getByRole("button", { name: "Toggle screen share probe" }));

    await waitFor(() => expect(media.toggleScreenShare).toHaveBeenCalledOnce());
  });

  it("stop is allowed through even when ownership/lease has just been lost", async () => {
    media.screenShareEnabled = true;
    renderProvider();
    fireEvent.click(screen.getByRole("button", { name: "Toggle screen share probe" }));
    await waitFor(() => expect(media.toggleScreenShare).toHaveBeenCalledOnce());
  });

  it("ownership loss stops media, tearing down an active screen share", async () => {
    media.screenShareEnabled = true;
    renderProvider();
    act(() => ownershipLost());
    await waitFor(() => expect(media.stop).toHaveBeenCalledOnce());
    expect(screen.getByTestId("owner")).toHaveTextContent("remote");
  });

  it("floating -> dedicated handoff stops media and never restarts screen share on the outgoing tab", async () => {
    media.screenShareEnabled = true;
    calls.call = activeDirect();
    ownership.getLease.mockReturnValue(lease);
    renderProvider();

    act(() =>
      ownershipListener({ v: 1, type: "ready", callId, tabId: "tab-dedicated", epoch: 2 } as never),
    );

    await waitFor(() => expect(media.stop).toHaveBeenCalledOnce());
    expect(media.toggleScreenShare).not.toHaveBeenCalled();
    await waitFor(() => expect(screen.getByTestId("owner")).toHaveTextContent("remote"));
  });

  it("dedicated minimize (releaseDedicated) stops media and never restarts screen share", async () => {
    media.screenShareEnabled = true;
    renderProvider(`/call/${callId}`);

    fireEvent.click(screen.getByRole("button", { name: "Release probe" }));

    await waitFor(() => expect(media.stop).toHaveBeenCalledOnce());
    expect(media.toggleScreenShare).not.toHaveBeenCalled();
  });

  it("reports one track cleanup when direct terminal ownership release follows signaling cleanup", async () => {
    const events: string[] = [];
    const listener = (event: Event) =>
      events.push((event as CustomEvent<{ event: string }>).detail.event);
    window.addEventListener("nchat:call-technical-event", listener);
    ownership.getLease.mockReturnValue(lease);
    renderProvider(`/call/${callId}`);
    const directMedia = vi.mocked(useCallSignaling).mock.calls.at(-1)![0]!;

    await act(() => directMedia.stop());
    expect(media.stop).toHaveBeenCalledOnce();
    expect(events.filter((event) => event === "track-cleanup")).toHaveLength(1);

    fireEvent.click(screen.getByRole("button", { name: "Direct terminal release probe" }));
    await waitFor(() => expect(media.stop).toHaveBeenCalledTimes(2));
    expect(ownership.release).toHaveBeenCalledWith(callId);
    expect(events.filter((event) => event === "track-cleanup")).toHaveLength(1);
    window.removeEventListener("nchat:call-technical-event", listener);
  });

  it("reports the eventual track cleanup when direct signaling cleanup failed first", async () => {
    const events: string[] = [];
    const listener = (event: Event) =>
      events.push((event as CustomEvent<{ event: string }>).detail.event);
    window.addEventListener("nchat:call-technical-event", listener);
    ownership.getLease.mockReturnValue(lease);
    media.stop.mockRejectedValueOnce(new Error("disconnect failed"));
    renderProvider(`/call/${callId}`);
    const directMedia = vi.mocked(useCallSignaling).mock.calls.at(-1)![0]!;

    await expect(directMedia.stop()).rejects.toThrow("disconnect failed");
    expect(events.filter((event) => event === "track-cleanup")).toHaveLength(0);

    fireEvent.click(screen.getByRole("button", { name: "Direct terminal release probe" }));
    await waitFor(() => expect(media.stop).toHaveBeenCalledTimes(2));
    await waitFor(() => expect(ownership.release).toHaveBeenCalledWith(callId));
    expect(events.filter((event) => event === "track-cleanup")).toHaveLength(1);
    window.removeEventListener("nchat:call-technical-event", listener);
  });

  // Adversarial case flagged by #614's review of this newly-added dedup
  // mechanism: a connect() failure racing the terminal-triggered stop BEFORE
  // confirmedIntentRef is ever set. ownedMedia.connect's own catch releases
  // the lease it just (re)used the instant media.connect() rejects — so by
  // the time useCallSignaling's terminal handling calls stopMedia(), neither
  // getLease() nor confirmedIntentRef has this call's id anymore, and
  // ownedMedia.stop()'s callId derivation must not silently fall through to
  // "" and defeat releaseDirectTerminal's own dedup below it.
  it("never double-reports track-cleanup when a connect failure releases the lease before confirmedIntent is ever set", async () => {
    const events: string[] = [];
    const listener = (event: Event) =>
      events.push((event as CustomEvent<{ event: string }>).detail.event);
    window.addEventListener("nchat:call-technical-event", listener);
    ownership.getLease.mockReturnValue(lease);
    media.connect.mockRejectedValueOnce(new Error("connect failed"));
    renderProvider(`/call/${callId}`);
    const owned = vi.mocked(useResourceCallSession).mock.calls[0]![0];
    const directMedia = vi.mocked(useCallSignaling).mock.calls.at(-1)![0]!;

    // ownedMedia.connect() reuses the already-held lease (getLease already
    // matches call_id), then media.connect() itself fails: its own catch
    // releases that lease and never reaches the writeMediaIntent that would
    // set confirmedIntentRef — exactly like a connect failure racing a
    // terminal event before media ever fully established.
    await expect(
      owned.connect(activeDirect() as never, "token", "wss://livekit", "fresh"),
    ).rejects.toThrow("connect failed");
    expect(ownership.release).toHaveBeenCalledWith(callId);
    // This fake ownership mock doesn't track its own release() call —
    // reflect what a real coordinator would now report.
    ownership.getLease.mockReturnValue(null);

    // useCallSignaling's own terminal handling now runs its stopMedia() —
    // this is the one real cleanup for this call.
    await act(() => directMedia.stop());
    expect(media.stop).toHaveBeenCalledOnce();
    expect(events.filter((event) => event === "track-cleanup")).toHaveLength(1);

    // The dedicated tab's terminal-close effect converges next; it must
    // recognize the cleanup above already happened for THIS call, not emit
    // a second one just because neither lease nor confirmedIntent survived
    // to say so.
    fireEvent.click(screen.getByRole("button", { name: "Direct terminal release probe" }));
    await waitFor(() => expect(media.stop).toHaveBeenCalledTimes(2));
    expect(events.filter((event) => event === "track-cleanup")).toHaveLength(1);
    window.removeEventListener("nchat:call-technical-event", listener);
  });

  it("ownership recovery reconnects media without auto-resuming screen share", async () => {
    resource.active = { kind: "channel", id: channelId, name: "Produto" };
    resource.callId = callId;
    resource.status = "connecting";
    ownership.getLease.mockReturnValue(lease);
    vi.useFakeTimers();
    renderProvider();
    act(() =>
      ownershipListener({ v: 1, type: "ready", callId, tabId: "tab-dedicated", epoch: 2 } as never),
    );
    await act(async () => undefined);
    ownership.getOwner.mockReturnValue(null);
    await act(async () => vi.advanceTimersByTime(1_500));

    expect(ownership.claim).toHaveBeenCalledWith(callId, "main", 2, undefined);
    expect(resource.reconnect).toHaveBeenCalled();
    expect(media.toggleScreenShare).not.toHaveBeenCalled();
  });

  it("ordinary chat route navigation does not stop an active screen share", () => {
    media.screenShareEnabled = true;
    renderProvider();
    fireEvent.click(screen.getByRole("button", { name: "Perfil" }));
    fireEvent.click(screen.getByRole("button", { name: "Canal" }));
    expect(screen.getByText("Canal", { selector: "p" })).toBeInTheDocument();
    expect(media.stop).not.toHaveBeenCalled();
    expect(media.screenShareEnabled).toBe(true);
  });
});

// Issue #570: a dedicated tab's own "sair" must converge — real participant
// leave (#569), release ownership, and tell every other tab this
// participation is over — never just a minimize-style release, and never a
// resource-call call.end.
describe("dedicated participant leave converges ownership and main tab state (issue #570)", () => {
  beforeEach(() => {
    vi.useRealTimers();
    vi.clearAllMocks();
    participationStorage = {};
    calls.call = null;
    resource.active = { kind: "channel", id: channelId, name: "Produto" };
    resource.callId = callId;
    resource.status = "active";
    ownership.getLease.mockReturnValue(lease);
    ownership.getOwner.mockReturnValue(null);
    ownership.claim.mockResolvedValue(lease);
    mediaIntentStorage = {};
    ownership.writeMediaIntent.mockReset();
    ownership.writeMediaIntent.mockImplementation(defaultWriteMediaIntent);
    ownership.readMediaIntentForLease.mockReset();
    ownership.readMediaIntentForLease.mockImplementation(defaultReadMediaIntentForLease);
    media.connect.mockResolvedValue({ microphone: true, camera: true });
    vi.mocked(createOwnershipCoordinator).mockReturnValue(ownership as never);
    vi.mocked(useCallMedia).mockReturnValue(media as never);
    vi.mocked(useCallSignaling).mockReturnValue(calls as never);
    vi.mocked(useResourceCallSession).mockReturnValue(resource as never);
    vi.mocked(acquireChatSocket).mockImplementation((listener) => {
      socketListener = listener;
      return { send: vi.fn(), isOpen: vi.fn(), generation: vi.fn(), release: vi.fn() };
    });
  });

  it("leaveDedicated runs the real participant leave, releases ownership, and broadcasts leaving then left — never a resource call.end", async () => {
    renderProvider(`/call/${callId}`);
    fireEvent.click(screen.getByRole("button", { name: "Leave dedicated probe" }));

    await waitFor(() =>
      expect(ownership.post).toHaveBeenCalledWith(
        expect.objectContaining({ type: "leaving", callId }),
      ),
    );
    await waitFor(() => expect(resource.leave).toHaveBeenCalledOnce());
    expect(calls.end).not.toHaveBeenCalled();
    expect(ownership.release).toHaveBeenCalledWith(callId);
    await waitFor(() =>
      expect(ownership.post).toHaveBeenCalledWith(
        expect.objectContaining({ type: "left", callId }),
      ),
    );
    await waitFor(() => expect(screen.getByTestId("owner")).toHaveTextContent("remote"));
    // This tab's own leave already cleared its own resource session via
    // resource.leave() above — convergeRemoteLeave (issue #594) is for OTHER
    // tabs that only ever observe this "left" cross-tab, never this one.
    expect(resource.convergeRemoteLeave).not.toHaveBeenCalled();
  });

  it("main tab converges once it observes the dedicated tab's 'left' broadcast: stops showing the call as open elsewhere and never reconnects (leave confirmado)", async () => {
    vi.useFakeTimers();
    // Main's own resource session is the stale one from before the handoff
    // (issue #594) — the real production shape: a handoff never nulls it.
    resource.callId = callId;
    renderProvider();
    act(() =>
      ownershipListener({ v: 1, type: "ready", callId, tabId: "tab-dedicated", epoch: 2 } as never),
    );
    await act(async () => undefined);
    expect(screen.getByTestId("owner")).toHaveTextContent("remote");
    expect(screen.getByText("Chamada aberta em outra aba")).toBeInTheDocument();

    // The dedicated tab really left: it broadcasts "left", not just a
    // released lease.
    act(() =>
      ownershipListener({
        v: 1,
        type: "left",
        callId,
        tabId: "tab-dedicated",
        epoch: 3,
        generation: 1,
        writerId: "tab-dedicated",
        sequence: 1,
      } as never),
    );

    expect(screen.queryByText("Chamada aberta em outra aba")).not.toBeInTheDocument();

    // Main's own (stale) resource-call session converges to idle locally —
    // never a second call.leave, the server-side leave already happened in
    // the dedicated tab (issue #594).
    expect(resource.convergeRemoteLeave).toHaveBeenCalledExactlyOnceWith(callId);
    expect(resource.leave).not.toHaveBeenCalled();

    // Neither the automatic recovery interval nor an explicit "Abrir aqui"
    // click may reconnect a resource call whose participation is already
    // confirmed over.
    ownership.getOwner.mockReturnValue(null);
    await act(async () => vi.advanceTimersByTime(1_500));
    expect(resource.reconnect).not.toHaveBeenCalled();

    fireEvent.click(screen.getByRole("button", { name: "Takeover probe" }));
    await act(async () => undefined);
    expect(resource.reconnect).not.toHaveBeenCalled();
    expect(ownership.claim).not.toHaveBeenCalled();
    vi.useRealTimers();
  });

  it("a resource call's 'left' broadcast never touches an unrelated, concurrently active direct 1:1 call (issue #594, no regression)", async () => {
    calls.call = activeDirect();
    resource.callId = callId;
    renderProvider();

    act(() =>
      ownershipListener({
        v: 1,
        type: "left",
        callId,
        tabId: "tab-dedicated",
        epoch: 3,
        generation: 1,
        writerId: "tab-dedicated",
        sequence: 1,
      } as never),
    );

    expect(resource.convergeRemoteLeave).toHaveBeenCalledExactlyOnceWith(callId);
    expect(calls.end).not.toHaveBeenCalled();
    expect(screen.getByTestId("floating-call-window")).toBeInTheDocument();
  });

  it("a plain 'released' message (minimize, not leave) never blocks a legitimate reclaim", async () => {
    vi.useFakeTimers();
    renderProvider();
    act(() =>
      ownershipListener({ v: 1, type: "ready", callId, tabId: "tab-dedicated", epoch: 2 } as never),
    );
    await act(async () => undefined);

    act(() =>
      ownershipListener({
        v: 1,
        type: "released",
        callId,
        tabId: "tab-dedicated",
        epoch: 2,
      } as never),
    );
    ownership.getOwner.mockReturnValue(null);
    await act(async () => vi.advanceTimersByTime(1_500));
    // The "released" message itself (issue #610 audit) already carries
    // dedicated's own {tabId, epoch} as a provable predecessor hint, so
    // the eager message-triggered reclaim (not the slower 1.5s poll,
    // deduped away by restoreOwnership's own in-flight guard) is what
    // actually claims here.
    expect(ownership.claim).toHaveBeenCalledWith(callId, "main", 2, {
      tabId: "tab-dedicated",
      epoch: 2,
    });
    expect(ownership.claim).toHaveBeenCalledTimes(1);
    expect(resource.reconnect).toHaveBeenCalled();
    // Minimize/handoff is never a leave (issue #594): it must never
    // converge/clear resource.active or resource.callId.
    expect(resource.convergeRemoteLeave).not.toHaveBeenCalled();
    vi.useRealTimers();
  });

  it("a leave in flight ('leaving', no ack yet) blocks any tab from reclaiming/reconnecting (corrida leave × Abrir aqui)", async () => {
    vi.useFakeTimers();
    renderProvider();
    act(() =>
      ownershipListener({ v: 1, type: "ready", callId, tabId: "tab-dedicated", epoch: 2 } as never),
    );
    await act(async () => undefined);
    expect(screen.getByText("Chamada aberta em outra aba")).toBeInTheDocument();

    // Dedicated started call.leave but the server hasn't confirmed yet.
    act(() =>
      ownershipListener({
        v: 1,
        type: "leaving",
        callId,
        tabId: "tab-dedicated",
        epoch: 2,
        generation: 1,
        writerId: "tab-dedicated",
        sequence: 1,
      } as never),
    );

    // The indicator itself must stop offering a reclaim while leaving is in
    // flight — reconnecting now would race the server-side leave and could
    // resurrect a participation the user is actively ending.
    expect(screen.queryByText("Chamada aberta em outra aba")).not.toBeInTheDocument();

    ownership.getOwner.mockReturnValue(null);
    await act(async () => vi.advanceTimersByTime(1_500));
    expect(resource.reconnect).not.toHaveBeenCalled();
    expect(ownership.claim).not.toHaveBeenCalled();

    fireEvent.click(screen.getByRole("button", { name: "Takeover probe" }));
    await act(async () => undefined);
    expect(resource.reconnect).not.toHaveBeenCalled();
    expect(ownership.claim).not.toHaveBeenCalled();
    // "leaving" alone must never converge/clear the local resource session —
    // only its matching "left" may (issue #594).
    expect(resource.convergeRemoteLeave).not.toHaveBeenCalled();
    vi.useRealTimers();
  });

  it("a failed leave ('leave-cancelled') restores this participation to reconnectable everywhere (leave falhou permite recovery)", async () => {
    vi.useFakeTimers();
    renderProvider();
    act(() =>
      ownershipListener({ v: 1, type: "ready", callId, tabId: "tab-dedicated", epoch: 2 } as never),
    );
    await act(async () => undefined);
    act(() =>
      ownershipListener({
        v: 1,
        type: "leaving",
        callId,
        tabId: "tab-dedicated",
        epoch: 2,
        generation: 1,
        writerId: "tab-dedicated",
        sequence: 1,
      } as never),
    );
    expect(screen.queryByText("Chamada aberta em outra aba")).not.toBeInTheDocument();

    // The leave attempt itself failed server-side; the same participation
    // is announced as participating/recoverable again.
    act(() =>
      ownershipListener({
        v: 1,
        type: "leave-cancelled",
        callId,
        tabId: "tab-dedicated",
        epoch: 2,
        generation: 1,
        writerId: "tab-dedicated",
        sequence: 2,
      } as never),
    );
    expect(screen.getByText("Chamada aberta em outra aba")).toBeInTheDocument();

    ownership.getOwner.mockReturnValue(null);
    await act(async () => vi.advanceTimersByTime(1_500));
    expect(ownership.claim).toHaveBeenCalledWith(callId, "main", 2, undefined);
    expect(resource.reconnect).toHaveBeenCalled();
    // "leave-cancelled" restores the participation — it must never converge
    // the local resource session (issue #594): there is nothing to undo.
    expect(resource.convergeRemoteLeave).not.toHaveBeenCalled();
    vi.useRealTimers();
  });

  it("a real join resolving to the same callId after a confirmed leave registers a fresh participation, even though resource.callId never observably changed (rejoin real)", async () => {
    // Main's own resource.callId is X from the very start and stays X
    // through the whole handoff -> dedicated-leave -> rejoin flow — the real
    // production shape: a handoff never nulls it, and useResourceCallSession
    // .join() resolving to the exact same call_id produces no React state
    // transition on it (Object.is bails), so nothing watching resource.callId
    // could ever detect this rejoin.
    // The original participation's generation (1) was itself allocated from
    // shared storage when the dedicated tab joined — seeding it here mirrors
    // that same shared floor a real allocateParticipationGeneration() call
    // would have produced, so the later real rejoin's own allocation is
    // exercised against realistic, not merely locally-injected, state.
    participationStorage = { [callId]: { "tab-dedicated": 1 } };
    renderProvider();
    fireEvent.click(screen.getByRole("button", { name: "Diretório" }));

    act(() =>
      ownershipListener({ v: 1, type: "ready", callId, tabId: "tab-dedicated", epoch: 2 } as never),
    );
    await act(async () => undefined);
    expect(screen.getByTestId("owner")).toHaveTextContent("remote");
    act(() =>
      ownershipListener({
        v: 1,
        type: "left",
        callId,
        tabId: "tab-dedicated",
        epoch: 3,
        generation: 1,
        writerId: "tab-dedicated",
        sequence: 2,
      } as never),
    );
    // While ownerState stays "remote" (this test never re-establishes local
    // ownership — a separate concern from participation, already covered
    // elsewhere), the resource-call indicator is what activeCallId gates.
    expect(screen.queryByText("Chamada aberta em outra aba")).not.toBeInTheDocument();

    // Others are still in call X: discovery still shows it active (a fresh
    // "active" event for the very same call_id arrives). resource.callId is
    // still X — untouched this whole time — yet the affordance must still
    // offer to join, because THIS participation already ended.
    const incoming = {
      ...activeDirect(),
      call_id: callId,
      target_type: "channel",
      target_id: channelId,
      status: "active",
    };
    act(() =>
      socketListener.onMessage?.(
        {
          type: "call.accepted",
          event_id: "resource-rejoin",
          target_type: "channel",
          target_id: channelId,
          call: incoming,
        },
        1,
      ),
    );
    expect(screen.getByTestId("discovery-channel-x")).toHaveTextContent("active");

    // The explicit, real join — resource.join() resolves with call_id X,
    // exactly like the real hook does when target.callId is supplied
    // (issue #622 round 2: ChatMessageArea's "Entrar na chamada", driven
    // here through the same probe joinViaPopup() uses).
    knownJoinProbeCallId = callId;
    fireEvent.click(screen.getByRole("button", { name: "Known join probe" }));
    expect(resource.join).toHaveBeenCalledWith(
      expect.objectContaining({ callId }),
      "fresh",
      expect.any(Function),
    );

    // The new participation lands with a fresh generation (2, one past the
    // old participation's 1) and activeCallId/UI converge back to X.
    await waitFor(() =>
      expect(ownership.post).toHaveBeenCalledWith(
        expect.objectContaining({ type: "participating", callId, generation: 2, sequence: 0 }),
      ),
    );
    await waitFor(() =>
      expect(screen.getByText("Chamada aberta em outra aba")).toBeInTheDocument(),
    );

    // Reconnect/control is genuinely restored, never refusing just because
    // this exact callId once appeared in a "left" message.
    ownership.getLease.mockReturnValue(lease);
    ownership.getOwner.mockReturnValue(null);
    fireEvent.click(screen.getByRole("button", { name: "Takeover probe" }));
    await waitFor(() => expect(resource.reconnect).toHaveBeenCalled());
  });

  it("dedicated crashing mid-leave leaves main stuck on 'leaving', but an explicit real join for the same callId still recovers it (dedicated morre durante leave)", async () => {
    // Mirrors the shared floor a real allocateParticipationGeneration() call
    // would have produced when the (now-crashed) dedicated tab originally
    // joined and posted "leaving" at generation 1.
    participationStorage = { [callId]: { "tab-dedicated": 1 } };
    renderProvider();
    fireEvent.click(screen.getByRole("button", { name: "Diretório" }));

    // Dedicated starts leaving but crashes before ever confirming — no
    // "left" or "leave-cancelled" ever arrives.
    act(() =>
      ownershipListener({
        v: 1,
        type: "leaving",
        callId,
        tabId: "tab-dedicated",
        epoch: 2,
        generation: 1,
        writerId: "tab-dedicated",
        sequence: 1,
      } as never),
    );
    expect(screen.queryByTestId("resource-call-panel")).not.toBeInTheDocument();

    // Automatic recovery stays blocked forever — that's expected, no ack ever
    // arrives to clear it — but this is NOT the thing under test.
    ownership.getOwner.mockReturnValue(null);
    fireEvent.click(screen.getByRole("button", { name: "Takeover probe" }));
    await act(async () => undefined);
    expect(resource.reconnect).not.toHaveBeenCalled();

    // Other participants are still in call X: a fresh "active" event for the
    // same call_id arrives. The affordance must reappear even though the
    // stuck record is "leaving", not merely "left".
    const incoming = {
      ...activeDirect(),
      call_id: callId,
      target_type: "channel",
      target_id: channelId,
      status: "active",
    };
    act(() =>
      socketListener.onMessage?.(
        {
          type: "call.accepted",
          event_id: "resource-crash-recovery",
          target_type: "channel",
          target_id: channelId,
          call: incoming,
        },
        1,
      ),
    );
    expect(screen.getByTestId("discovery-channel-x")).toHaveTextContent("active");

    // No automatic recovery is required for this case — only that the
    // user's explicit join (issue #622 round 2: ChatMessageArea's "Entrar na
    // chamada", driven here through the same probe joinViaPopup() uses)
    // fully recovers the system.
    knownJoinProbeCallId = callId;
    fireEvent.click(screen.getByRole("button", { name: "Known join probe" }));
    await waitFor(() => expect(screen.getByTestId("resource-call-panel")).toBeInTheDocument());

    ownership.getLease.mockReturnValue(lease);
    ownership.getOwner.mockReturnValue(null);
    fireEvent.click(screen.getByRole("button", { name: "Takeover probe" }));
    await waitFor(() => expect(resource.reconnect).toHaveBeenCalled());
  });
});

// Issue #570 follow-up: participation events are ordered by
// (generation, sequence), never Date.now() — two independent tabs can
// legitimately collide on the same wall-clock millisecond, and delivery
// order is not causal order.
describe("participation ordering is causal, not wall-clock (issue #570 follow-up)", () => {
  beforeEach(() => {
    vi.useRealTimers();
    vi.clearAllMocks();
    participationStorage = {};
    calls.call = null;
    resource.active = { kind: "channel", id: channelId, name: "Produto" };
    resource.callId = callId;
    resource.status = "active";
    ownership.getLease.mockReturnValue(lease);
    ownership.getOwner.mockReturnValue(null);
    ownership.claim.mockResolvedValue(lease);
    mediaIntentStorage = {};
    ownership.writeMediaIntent.mockReset();
    ownership.writeMediaIntent.mockImplementation(defaultWriteMediaIntent);
    ownership.readMediaIntentForLease.mockReset();
    ownership.readMediaIntentForLease.mockImplementation(defaultReadMediaIntentForLease);
    media.connect.mockResolvedValue({ microphone: true, camera: true });
    vi.mocked(createOwnershipCoordinator).mockReturnValue(ownership as never);
    vi.mocked(useCallMedia).mockReturnValue(media as never);
    vi.mocked(useCallSignaling).mockReturnValue(calls as never);
    vi.mocked(useResourceCallSession).mockReturnValue(resource as never);
    vi.mocked(acquireChatSocket).mockImplementation((listener) => {
      socketListener = listener;
      return { send: vi.fn(), isOpen: vi.fn(), generation: vi.fn(), release: vi.fn() };
    });
  });

  it("a new participation's higher generation always beats an old, delayed 'left' from a superseded participation, even at identical wall-clock time", async () => {
    vi.useFakeTimers();
    vi.setSystemTime(new Date("2026-08-19T12:00:00.000Z"));
    try {
      renderProvider();

      // The old participation (generation 1) already ended.
      act(() =>
        ownershipListener({
          v: 1,
          type: "left",
          callId,
          tabId: "tab-dedicated",
          epoch: 2,
          generation: 1,
          writerId: "tab-dedicated",
          sequence: 5,
        } as never),
      );
      expect(screen.queryByTestId("resource-call-panel")).not.toBeInTheDocument();

      // A brand-new participation begins (generation 2) — e.g. a fresh
      // dedicated tab reopening the same call.
      act(() =>
        ownershipListener({
          v: 1,
          type: "participating",
          callId,
          tabId: "tab-dedicated-2",
          epoch: 2,
          generation: 2,
          writerId: "tab-dedicated-2",
          sequence: 0,
        } as never),
      );
      // Synchronous: the ownershipListener call above already ran inside
      // act() and committed the re-render — no async wait needed.
      expect(screen.getByTestId("resource-call-panel")).toBeInTheDocument();

      // The OLD "left" (generation 1) is redelivered late, at the exact same
      // system time as everything above — it must never outrank generation 2.
      act(() =>
        ownershipListener({
          v: 1,
          type: "left",
          callId,
          tabId: "tab-dedicated",
          epoch: 2,
          generation: 1,
          writerId: "tab-dedicated",
          sequence: 6,
        } as never),
      );
      expect(screen.getByTestId("resource-call-panel")).toBeInTheDocument();
      // The old, superseded "left" must never converge/clear the newer
      // rejoin's resource session (issue #594) — only the FIRST "left"
      // above (which had no newer participation to contend with) may have.
      expect(resource.convergeRemoteLeave).toHaveBeenCalledTimes(1);
    } finally {
      vi.useRealTimers();
    }
  });

  it("within one participation, 'left' always wins over a reordered, later-arriving 'leaving' for an earlier step — even at identical wall-clock time", async () => {
    vi.useFakeTimers();
    vi.setSystemTime(new Date("2026-08-19T12:00:00.000Z"));
    try {
      renderProvider();

      act(() =>
        ownershipListener({
          v: 1,
          type: "participating",
          callId,
          tabId: "tab-dedicated",
          epoch: 2,
          generation: 1,
          writerId: "tab-dedicated",
          sequence: 0,
        } as never),
      );
      act(() =>
        ownershipListener({
          v: 1,
          type: "left",
          callId,
          tabId: "tab-dedicated",
          epoch: 2,
          generation: 1,
          writerId: "tab-dedicated",
          sequence: 2,
        } as never),
      );
      expect(screen.queryByTestId("resource-call-panel")).not.toBeInTheDocument();

      // "leaving" (an earlier step of the SAME participation) is redelivered
      // late, after "left" already applied — it must not resurrect the call.
      act(() =>
        ownershipListener({
          v: 1,
          type: "leaving",
          callId,
          tabId: "tab-dedicated",
          epoch: 2,
          generation: 1,
          writerId: "tab-dedicated",
          sequence: 1,
        } as never),
      );
      expect(screen.queryByTestId("resource-call-panel")).not.toBeInTheDocument();
    } finally {
      vi.useRealTimers();
    }
  });

  it("participating -> leaving -> leave-cancelled ends recoverable/participating, even at identical wall-clock time", async () => {
    vi.useFakeTimers();
    vi.setSystemTime(new Date("2026-08-19T12:00:00.000Z"));
    try {
      renderProvider();

      act(() =>
        ownershipListener({
          v: 1,
          type: "participating",
          callId,
          tabId: "tab-dedicated",
          epoch: 2,
          generation: 1,
          writerId: "tab-dedicated",
          sequence: 0,
        } as never),
      );
      act(() =>
        ownershipListener({
          v: 1,
          type: "leaving",
          callId,
          tabId: "tab-dedicated",
          epoch: 2,
          generation: 1,
          writerId: "tab-dedicated",
          sequence: 1,
        } as never),
      );
      expect(screen.queryByTestId("resource-call-panel")).not.toBeInTheDocument();

      act(() =>
        ownershipListener({
          v: 1,
          type: "leave-cancelled",
          callId,
          tabId: "tab-dedicated",
          epoch: 2,
          generation: 1,
          writerId: "tab-dedicated",
          sequence: 2,
        } as never),
      );
      expect(screen.getByTestId("resource-call-panel")).toBeInTheDocument();
    } finally {
      vi.useRealTimers();
    }
  });
});

// Issue #570 follow-up (HIGH): generation computed from this tab's own
// local participationRecordsRef is only monotonic WITHIN one
// CallSessionProvider instance. A brand-new tab, or the same tab after a
// reload, starts with that ref EMPTY and would restart at generation 1 —
// indistinguishable from, and rejected as older than, a real prior
// participation any other (or the same, pre-reload) tab still remembers.
// generation must instead come from the ownership coordinator's shared,
// persisted storage (allocateParticipationGeneration/
// getParticipationGeneration) — proven here via the `ownership` mock's
// `participationStorage`, which stands in for real localStorage: it is
// deliberately NOT cleared between renders within a single test, exactly
// like real storage survives a reload or a second tab opening.
describe("participation generation is cross-tab and reload safe (issue #570 follow-up, HIGH)", () => {
  beforeEach(() => {
    vi.useRealTimers();
    vi.clearAllMocks();
    participationStorage = {};
    calls.call = null;
    calls.pending = false;
    calls.cancelling = false;
    calls.error = null;
    calls.mediaActivationRequired = false;
    resource.active = null;
    resource.callId = null;
    resource.status = "idle";
    resource.error = null;
    media.status = "connected";
    media.error = null;
    media.activeSpeakerId = null;
    media.participants = [];
    ownership.getLease.mockReturnValue(null);
    ownership.getOwner.mockReturnValue(null);
    ownership.claim.mockResolvedValue(lease);
    mediaIntentStorage = {};
    ownership.writeMediaIntent.mockReset();
    ownership.writeMediaIntent.mockImplementation(defaultWriteMediaIntent);
    ownership.readMediaIntentForLease.mockReset();
    ownership.readMediaIntentForLease.mockImplementation(defaultReadMediaIntentForLease);
    media.connect.mockResolvedValue({ microphone: true, camera: true });
    vi.mocked(createOwnershipCoordinator).mockReturnValue(ownership as never);
    vi.mocked(useCallMedia).mockReturnValue(media as never);
    vi.mocked(useCallSignaling).mockReturnValue(calls as never);
    vi.mocked(useResourceCallSession).mockReturnValue(resource as never);
    vi.mocked(acquireChatSocket).mockImplementation((listener) => {
      socketListener = listener;
      return { send: vi.fn(), isOpen: vi.fn(), generation: vi.fn(), release: vi.fn() };
    });
  });

  // Drives a real resource.join() through joinResourceParticipation — the
  // same causal path production code uses (issue #622 round 2:
  // ChatMessageArea's "Entrar na chamada" action, never a resource
  // IncomingCallPopup click, which this issue retired), never a manual mock
  // mutation. targetCallId lets a test join a SECOND, distinct resource call
  // (Y) through the same directory-registered channel, to prove one
  // callId's activity never disturbs another's own generation history.
  async function joinViaPopup(targetCallId: string = callId) {
    fireEvent.click(screen.getByRole("button", { name: "Diretório" }));
    knownJoinProbeCallId = targetCallId;
    fireEvent.click(screen.getByRole("button", { name: "Known join probe" }));
    await waitFor(() => expect(resource.join).toHaveBeenCalled());
  }

  it("cross-tab fresh provider: a brand-new tab's real join allocates strictly past an old participation's generation, never restarting at 1 from its own empty local state", async () => {
    // This tab's own OLD participation: a real join (generation 1, from
    // empty shared storage), then a real leave (generation 1 stays, phase
    // -> left, sequence advances). Also proves "nova saída legítima": the
    // terminal leaving/left events of a real participation win normally.
    const first = renderProvider();
    await joinViaPopup();
    await waitFor(() =>
      expect(ownership.post).toHaveBeenCalledWith(
        expect.objectContaining({ type: "participating", callId, generation: 1, sequence: 0 }),
      ),
    );
    resource.active = { kind: "channel", id: channelId, name: "Produto" };
    resource.callId = callId;
    resource.status = "active";
    fireEvent.click(screen.getByRole("button", { name: "Leave dedicated probe" }));
    await waitFor(() =>
      expect(ownership.post).toHaveBeenCalledWith(
        expect.objectContaining({ type: "leaving", callId, generation: 1, sequence: 1 }),
      ),
    );
    await waitFor(() =>
      expect(ownership.post).toHaveBeenCalledWith(
        expect.objectContaining({ type: "left", callId, generation: 1, sequence: 2 }),
      ),
    );
    first.unmount();

    // A brand-new tab: fresh CallSessionProvider instance (fresh
    // participationRecordsRef, empty) — it never saw any of the events
    // above. Only the shared storage (participationStorage) survives, the
    // same way real localStorage would survive across a new tab opening.
    resource.active = null;
    resource.callId = null;
    resource.status = "idle";
    ownership.post.mockClear();
    renderProvider();
    await joinViaPopup();

    // The fresh tab's real join must allocate strictly past the OLD
    // participation's generation (1) — never restart at 1 from its own
    // empty local knowledge, which is exactly the HIGH finding's bug.
    await waitFor(() =>
      expect(ownership.post).toHaveBeenCalledWith(
        expect.objectContaining({ type: "participating", callId, generation: 2, sequence: 0 }),
      ),
    );
  });

  // ── issue #594 adversarial follow-up: a rejoin already under way — before
  // resource.join() has resolved and beginResourceParticipation could ever
  // register its real, higher-ranked token — must be protected from an old
  // "left" for the OLD participation it supersedes, which commitParticipation
  // would otherwise still consider "current" during that exact window. ─────

  it("a rejoin started before join() resolves is protected from an old 'left' for the participation it supersedes, still registers its own generation once it resolves, and a real left for THAT new generation converges normally afterward", async () => {
    // N: an existing participation this tab already knows about (generation
    // 1, real join through the popup).
    renderProvider();
    await joinViaPopup();
    await waitFor(() =>
      expect(ownership.post).toHaveBeenCalledWith(
        expect.objectContaining({ type: "participating", callId, generation: 1, sequence: 0 }),
      ),
    );
    ownership.post.mockClear();

    // The old participation enters "leaving" (e.g. announced by whatever
    // else is ending it server-side) — this is also what makes the incoming
    // popup eligible to reoffer this exact callId at all: it deliberately
    // refuses to reoffer a callId this tab still considers "participating".
    act(() =>
      ownershipListener({
        v: 1,
        type: "leaving",
        callId,
        tabId: "tab-dedicated",
        epoch: 2,
        generation: 1,
        writerId: "tab-main",
        sequence: 1,
      } as never),
    );

    // The user starts a legitimate rejoin of the SAME callId — resource.join()
    // is called (joinResourceParticipation's own protection window opens
    // the instant this happens, synchronously, before any await) but does
    // not resolve yet: beginResourceParticipation cannot have registered
    // generation 2 at this point.
    let resolveJoin!: (value: string | undefined) => void;
    resource.join.mockReturnValueOnce(
      new Promise<string | undefined>((resolve) => {
        resolveJoin = resolve;
      }),
    );
    fireEvent.click(screen.getByRole("button", { name: "Diretório" }));
    knownJoinProbeCallId = callId;
    fireEvent.click(screen.getByRole("button", { name: "Known join probe" }));
    expect(resource.join).toHaveBeenCalledTimes(2);

    // C: a redelivered/late "left" for generation 1 — the OLD participation,
    // still the only one commitParticipation knows about — arrives and is
    // legitimately accepted by the ordering guard.
    act(() =>
      ownershipListener({
        v: 1,
        type: "left",
        callId,
        tabId: "tab-dedicated",
        epoch: 3,
        generation: 1,
        writerId: "tab-main",
        sequence: 2,
      } as never),
    );

    // D: the in-flight rejoin must NOT be aborted by it.
    expect(resource.convergeRemoteLeave).not.toHaveBeenCalled();

    // E: join() now resolves for real.
    resolveJoin(callId);
    await act(async () => undefined);

    // F: the new participation is registered at generation 2, exactly as if
    // the old "left" above had never arrived.
    await waitFor(() =>
      expect(ownership.post).toHaveBeenCalledWith(
        expect.objectContaining({ type: "participating", callId, generation: 2, sequence: 0 }),
      ),
    );

    // G: the SAME old "left" (generation 1, sequence 2) redelivered again
    // after the rejoin completed must still never clear the new generation
    // 2 — rejected as stale by the ordinary ordering guard this time (no
    // longer even needs the pending-attempt protection, since generation 2
    // is now the registered current record).
    act(() =>
      ownershipListener({
        v: 1,
        type: "left",
        callId,
        tabId: "tab-dedicated",
        epoch: 3,
        generation: 1,
        writerId: "tab-main",
        sequence: 2,
      } as never),
    );
    expect(resource.convergeRemoteLeave).not.toHaveBeenCalled();

    // Inverse: a real "left" that actually corresponds to the NOW-current
    // generation (2) must still converge normally — the protection window
    // is gone once the rejoin registered for real, it must never linger.
    act(() =>
      ownershipListener({
        v: 1,
        type: "left",
        callId,
        tabId: "tab-dedicated",
        epoch: 4,
        generation: 2,
        writerId: "tab-main",
        sequence: 1,
      } as never),
    );
    expect(resource.convergeRemoteLeave).toHaveBeenCalledExactlyOnceWith(callId);
  });

  it("issue #594 adversarial follow-up (round 3): a fresh join with target.callId undefined (ChatShell's own shape) is protected the instant the server resolves it — before issueCallToken/media.connect ever settle — surviving an old left, and a late duplicate never clears the real registration", async () => {
    // The shared storage already holds a real, older generation for this
    // callId (mirrors what that OLD participation's own real
    // allocateParticipationGeneration() call would have written) — this is
    // what the fresh join's OWN real allocation reads from, independent of
    // any locally observed broadcast.
    participationStorage = { [callId]: { "tab-dedicated": 1 } };
    renderProvider();

    // resource.join() itself resolves the real call_id mid-flight (target
    // had none) and reports it via onCallIdResolved BEFORE settling —
    // exactly like the real hook's own placement, right before its own
    // issueCallToken/media.connect awaits.
    let resolveJoin!: (value: string | undefined) => void;
    let onCallIdResolved: ((resolvedCallId: string) => void) | undefined;
    resource.join.mockImplementationOnce(
      (_target: unknown, _mode: unknown, callback?: (resolvedCallId: string) => void) => {
        onCallIdResolved = callback;
        return new Promise<string | undefined>((resolve) => {
          resolveJoin = resolve;
        });
      },
    );

    fireEvent.click(screen.getByRole("button", { name: "Fresh join probe (no callId)" }));
    expect(resource.join).toHaveBeenCalledWith(
      expect.objectContaining({ kind: "channel", id: channelId }),
      "fresh",
      expect.any(Function),
    );
    expect((resource.join.mock.calls[0]![0] as { callId?: string }).callId).toBeUndefined();

    // The server resolves the real callId (the SAME, reused one) — reported
    // synchronously, before issueCallToken/media.connect would ever run in
    // the real hook.
    act(() => onCallIdResolved?.(callId));

    // An old "left" (generation 1 — the only one the ordering guard knows
    // about, since the fresh attempt hasn't registered its own generation
    // yet) arrives right in this window and is legitimately accepted.
    act(() =>
      ownershipListener({
        v: 1,
        type: "left",
        callId,
        tabId: "tab-dedicated",
        epoch: 3,
        generation: 1,
        writerId: "tab-dedicated",
        sequence: 1,
      } as never),
    );

    // The in-flight fresh join must NOT be aborted by it.
    expect(resource.convergeRemoteLeave).not.toHaveBeenCalled();

    // join() now resolves for real.
    resolveJoin(callId);
    await act(async () => undefined);

    // The new participation registers at generation 2.
    await waitFor(() =>
      expect(ownership.post).toHaveBeenCalledWith(
        expect.objectContaining({ type: "participating", callId, generation: 2, sequence: 0 }),
      ),
    );

    // A late/duplicate redelivery of the same old "left" (generation 1)
    // must never clear the new generation 2.
    act(() =>
      ownershipListener({
        v: 1,
        type: "left",
        callId,
        tabId: "tab-dedicated",
        epoch: 3,
        generation: 1,
        writerId: "tab-dedicated",
        sequence: 1,
      } as never),
    );
    expect(resource.convergeRemoteLeave).not.toHaveBeenCalled();
  });

  it("issue #594 adversarial follow-up (round 3): DedicatedCallPage's activateResourceParticipation never registers a fresh-join intent — a real, current 'left' arriving while it connects still converges normally, never masked", async () => {
    renderProvider();

    // This tab already genuinely participates in callId (generation 1,
    // real registration) — activateResourceParticipation is now just
    // reconnecting media for that SAME, already-current participation
    // (issue #570 problem 3's handoff-continuation shape), not starting a
    // new one.
    fireEvent.click(screen.getByRole("button", { name: "Begin participation probe" }));
    await waitFor(() =>
      expect(ownership.post).toHaveBeenCalledWith(
        expect.objectContaining({ type: "participating", callId, generation: 1, sequence: 0 }),
      ),
    );

    let resolveJoin!: (value: string | undefined) => void;
    resource.join.mockReturnValueOnce(
      new Promise<string | undefined>((resolve) => {
        resolveJoin = resolve;
      }),
    );
    fireEvent.click(screen.getByRole("button", { name: "Activate participation probe" }));
    expect(resource.join).toHaveBeenCalledWith(expect.objectContaining({ callId }), "recovery");

    // The REAL, current participation (generation 1) genuinely ends now,
    // while activateResourceParticipation's own join() is still connecting.
    act(() =>
      ownershipListener({
        v: 1,
        type: "left",
        callId,
        tabId: "tab-dedicated",
        epoch: 3,
        generation: 1,
        writerId: "tab-main",
        sequence: 1,
      } as never),
    );

    // This must converge normally — never masked just because
    // activateResourceParticipation happens to be connecting. It never
    // registered any fresh-join-intent to suppress it with.
    expect(resource.convergeRemoteLeave).toHaveBeenCalledExactlyOnceWith(callId);

    resolveJoin(callId);
    await act(async () => undefined);
  });

  it("reload: this tab's own earlier participation ended, the provider is unmounted and recreated, and a delayed message from before the reload never outranks the post-reload rejoin", async () => {
    const first = renderProvider();
    await joinViaPopup();
    await waitFor(() =>
      expect(ownership.post).toHaveBeenCalledWith(
        expect.objectContaining({ type: "participating", callId, generation: 1, sequence: 0 }),
      ),
    );
    resource.active = { kind: "channel", id: channelId, name: "Produto" };
    resource.callId = callId;
    resource.status = "active";
    fireEvent.click(screen.getByRole("button", { name: "Leave dedicated probe" }));
    await waitFor(() =>
      expect(ownership.post).toHaveBeenCalledWith(
        expect.objectContaining({ type: "left", callId, generation: 1 }),
      ),
    );
    first.unmount();

    // Simulates a reload: same browser, same shared storage, but a
    // brand-new CallSessionProvider instance with no memory of the above.
    resource.active = null;
    resource.callId = null;
    resource.status = "idle";
    const second = renderProvider();
    await joinViaPopup();
    await waitFor(() =>
      expect(ownership.post).toHaveBeenCalledWith(
        expect.objectContaining({ type: "participating", callId, generation: 2, sequence: 0 }),
      ),
    );
    resource.active = { kind: "channel", id: channelId, name: "Produto" };
    resource.callId = callId;
    resource.status = "active";
    second.rerender(providerTree());
    expect(await screen.findByTestId("resource-call-panel")).toBeInTheDocument();

    // An old message from before the reload — this tab's own pre-reload
    // "left" at generation 1 — finally arrives late. It must not undo the
    // post-reload rejoin.
    act(() =>
      ownershipListener({
        v: 1,
        type: "left",
        callId,
        tabId: "tab-pre-reload",
        epoch: 2,
        generation: 1,
        writerId: "tab-main",
        sequence: 3,
      } as never),
    );
    expect(screen.getByTestId("resource-call-panel")).toBeInTheDocument();
  });

  it("a tab that took over an existing participation via handoff (never its own join()) still tags its own leaving/left with the shared generation floor, not restarting at 0", async () => {
    // Someone else (a different tab, or this same tab's earlier life)
    // already joined X and reached generation 1 — recorded only in shared
    // storage. This tab's own participationRecordsRef has no entry for X:
    // it took over via handoff/reconnect, never resource.join().
    ownership.allocateParticipationGeneration(callId);
    ownership.post.mockClear();
    resource.active = { kind: "channel", id: channelId, name: "Produto" };
    resource.callId = callId;
    resource.status = "active";
    renderProvider(`/call/${callId}`);

    fireEvent.click(screen.getByRole("button", { name: "Leave dedicated probe" }));
    await waitFor(() =>
      expect(ownership.post).toHaveBeenCalledWith(
        expect.objectContaining({ type: "leaving", callId, generation: 1, sequence: 1 }),
      ),
    );
    await waitFor(() =>
      expect(ownership.post).toHaveBeenCalledWith(
        expect.objectContaining({ type: "left", callId, generation: 1, sequence: 2 }),
      ),
    );
  });

  // HIGH finding: PARTICIPATION_KEY used to be a single global slot — a
  // different call's activity in between silently forgot the first call's
  // own generation, so a legitimate rejoin of it collided with its own
  // history instead of outranking it.
  it("X1 -> Y1 -> X2: another call's real join in between never resets X's own generation history", async () => {
    const otherCallId = "00000000-0000-4000-8000-000000000999";

    // X1: real join (generation 1), then a real leave — this tab's own
    // participation in X actually ends before Y ever begins.
    const first = renderProvider();
    await joinViaPopup(callId);
    await waitFor(() =>
      expect(ownership.post).toHaveBeenCalledWith(
        expect.objectContaining({ type: "participating", callId, generation: 1, sequence: 0 }),
      ),
    );
    resource.active = { kind: "channel", id: channelId, name: "Produto" };
    resource.callId = callId;
    resource.status = "active";
    first.rerender(providerTree());
    fireEvent.click(screen.getByRole("button", { name: "Leave dedicated probe" }));
    await waitFor(() =>
      expect(ownership.post).toHaveBeenCalledWith(
        expect.objectContaining({ type: "left", callId, generation: 1 }),
      ),
    );
    ownership.post.mockClear();

    // Y1: a DIFFERENT resource call is joined next, in the SAME tab.
    resource.active = null;
    resource.callId = null;
    resource.status = "idle";
    first.rerender(providerTree());
    await joinViaPopup(otherCallId);
    await waitFor(() =>
      expect(ownership.post).toHaveBeenCalledWith(
        expect.objectContaining({
          type: "participating",
          callId: otherCallId,
          generation: 1,
          sequence: 0,
        }),
      ),
    );
    ownership.post.mockClear();

    // X2: X is still active for other participants; this tab rejoins it.
    // X's own history — untouched by Y's activity — is what decides X2's
    // generation, strictly past X1's.
    resource.active = null;
    resource.callId = null;
    resource.status = "idle";
    first.rerender(providerTree());
    await joinViaPopup(callId);
    await waitFor(() =>
      expect(ownership.post).toHaveBeenCalledWith(
        expect.objectContaining({ type: "participating", callId, generation: 2, sequence: 0 }),
      ),
    );
  });

  it("X1 termina, Y usa o storage compartilhado, X2 começa, e um left(X1) atrasado nunca afeta X2", async () => {
    const otherCallId = "00000000-0000-4000-8000-000000000999";

    // X1: real join + real leave (generation 1, phase -> left).
    const first = renderProvider();
    await joinViaPopup(callId);
    await waitFor(() =>
      expect(ownership.post).toHaveBeenCalledWith(
        expect.objectContaining({ type: "participating", callId, generation: 1, sequence: 0 }),
      ),
    );
    resource.active = { kind: "channel", id: channelId, name: "Produto" };
    resource.callId = callId;
    resource.status = "active";
    first.rerender(providerTree());
    fireEvent.click(screen.getByRole("button", { name: "Leave dedicated probe" }));
    await waitFor(() =>
      expect(ownership.post).toHaveBeenCalledWith(
        expect.objectContaining({ type: "left", callId, generation: 1, sequence: 2 }),
      ),
    );
    const x1Left = ownership.post.mock.calls.find(
      (postCall) => (postCall[0] as { type?: string }).type === "left",
    )![0] as { generation: number; writerId: string; sequence: number };
    ownership.post.mockClear();

    // Y: another call's real join and leave uses the SAME shared storage.
    resource.active = null;
    resource.callId = null;
    resource.status = "idle";
    first.rerender(providerTree());
    await joinViaPopup(otherCallId);
    resource.active = { kind: "channel", id: channelId, name: "Produto" };
    resource.callId = otherCallId;
    resource.status = "active";
    first.rerender(providerTree());
    fireEvent.click(screen.getByRole("button", { name: "Leave dedicated probe" }));
    await waitFor(() =>
      expect(ownership.post).toHaveBeenCalledWith(
        expect.objectContaining({ type: "left", callId: otherCallId }),
      ),
    );

    // X2: a real rejoin of X begins — its own history (unaffected by Y).
    resource.active = null;
    resource.callId = null;
    resource.status = "idle";
    first.rerender(providerTree());
    await joinViaPopup(callId);
    await waitFor(() =>
      expect(ownership.post).toHaveBeenCalledWith(
        expect.objectContaining({ type: "participating", callId, generation: 2, sequence: 0 }),
      ),
    );
    // leaveDedicated (used to end X1 above) also releases ownership, so this
    // tab stays "remote" for the rest of the test — a separate concern from
    // participation, already covered elsewhere. The resource-call indicator
    // is what activeCallId gates while ownerState === "remote".
    resource.active = { kind: "channel", id: channelId, name: "Produto" };
    resource.callId = callId;
    resource.status = "active";
    first.rerender(providerTree());
    expect(await screen.findByText("Chamada aberta em outra aba")).toBeInTheDocument();

    // The OLD X1 "left" — sent before Y or X2 ever happened — finally
    // arrives late. It must not undo X2.
    act(() =>
      ownershipListener({
        v: 1,
        type: "left",
        callId,
        tabId: "tab-x1",
        epoch: 1,
        generation: x1Left.generation,
        writerId: x1Left.writerId,
        sequence: x1Left.sequence,
      } as never),
    );
    expect(screen.getByText("Chamada aberta em outra aba")).toBeInTheDocument();
  });

  it("a tie between two independently-raced tokens for the same callId still converges to one deterministic winner, and a later real join outranks both", async () => {
    // Two tabs raced to generation 1 for X with no shared-storage
    // synchronization at all (proven directly in callOwnership.test.ts) —
    // this tab receives both, in arbitrary order.
    const tokenA = { generation: 1, writerId: "tab-a" };
    const tokenB = { generation: 1, writerId: "tab-b" };
    resource.active = { kind: "channel", id: channelId, name: "Produto" };
    resource.callId = callId;
    resource.status = "active";
    renderProvider();
    act(() =>
      ownershipListener({
        v: 1,
        type: "participating",
        callId,
        tabId: "tab-a",
        epoch: 1,
        ...tokenA,
        sequence: 0,
      } as never),
    );
    act(() =>
      ownershipListener({
        v: 1,
        type: "participating",
        callId,
        tabId: "tab-b",
        epoch: 1,
        ...tokenB,
        sequence: 0,
      } as never),
    );
    expect(screen.getByTestId("resource-call-panel")).toBeInTheDocument();

    // Whichever of the two this tab settled on as "current" (deterministic,
    // same rule everywhere — proven directly in callOwnership.test.ts), the
    // OTHER one's own terminal message must never undo it: it belongs to a
    // participation this tab has already decided lost the tie.
    act(() =>
      ownershipListener({
        v: 1,
        type: "left",
        callId,
        tabId: "tab-a",
        epoch: 1,
        generation: tokenA.generation,
        writerId: tokenA.writerId,
        sequence: 1,
      } as never),
    );
    act(() =>
      ownershipListener({
        v: 1,
        type: "left",
        callId,
        tabId: "tab-b",
        epoch: 1,
        generation: tokenB.generation,
        writerId: tokenB.writerId,
        sequence: 1,
      } as never),
    );
    // At most one of the two "left" messages (the one for whichever token
    // actually won) could have applied; even so, a genuinely newer
    // participation (generation 2) must still outrank whatever is current.
    act(() =>
      ownershipListener({
        v: 1,
        type: "participating",
        callId,
        tabId: "tab-c",
        epoch: 1,
        generation: 2,
        writerId: "tab-c",
        sequence: 0,
      } as never),
    );
    expect(screen.getByTestId("resource-call-panel")).toBeInTheDocument();
  });

  // Issue #570 problem 3: DedicatedCallPage's activation effect calls
  // resource.join() on every successful ownerState -> "local" transition,
  // including a plain handoff continuation, not only a genuinely fresh
  // join — see the comment on beginResourceParticipation in
  // CallSessionProvider.tsx for the chosen, documented semantics.
  it("problem 3: re-registering an already-active participation (as a handoff continuation's resource.join() would) never posts leaving/left, and reconnect/minimize keep working", async () => {
    // The popup's own affordance gate (participationRecordsRef.phase !==
    // "participating") deliberately can't reoffer a call this tab is
    // already in — so this drives the SAME function
    // (beginResourceParticipation) DedicatedCallPage's activation effect
    // calls directly, on every ownerState -> "local" transition, without
    // going through that gate — exactly how a real handoff continuation's
    // resource.join() reaches it.
    const view = renderProvider();
    fireEvent.click(screen.getByRole("button", { name: "Begin participation probe" }));
    await waitFor(() =>
      expect(ownership.post).toHaveBeenCalledWith(
        expect.objectContaining({ type: "participating", callId, generation: 1, sequence: 0 }),
      ),
    );

    // A handoff continuation's resource.join() resolving again for the
    // exact same, still-active participation.
    fireEvent.click(screen.getByRole("button", { name: "Begin participation probe" }));
    await waitFor(() =>
      expect(ownership.post).toHaveBeenCalledWith(
        expect.objectContaining({ type: "participating", callId, generation: 2, sequence: 0 }),
      ),
    );
    expect(ownership.post).not.toHaveBeenCalledWith(expect.objectContaining({ type: "leaving" }));
    expect(ownership.post).not.toHaveBeenCalledWith(expect.objectContaining({ type: "left" }));

    // Reconnect/minimize continue functioning normally afterward.
    resource.active = { kind: "channel", id: channelId, name: "Produto" };
    resource.callId = callId;
    resource.status = "active";
    ownership.getLease.mockReturnValue(lease);
    ownership.getOwner.mockReturnValue(null);
    view.rerender(providerTree());
    fireEvent.click(screen.getByRole("button", { name: "Takeover probe" }));
    await waitFor(() => expect(resource.reconnect).toHaveBeenCalled());
  });

  // HIGH finding (per-writer keys): A and B both raced to generation 4
  // without Web Locks. Under the OLD single-shared-key design, whichever
  // wrote last could regress storage back over the other. Under per-writer
  // keys, both A's and B's own keys persist independently — nothing to
  // regress — so a fresh/reload tab with no local participationRecordsRef
  // entry, falling back to getParticipationToken, reads the true causal
  // winner among BOTH, never "whichever physically wrote last".
  it("A/B collide at the same generation; a fresh/reload tab's own leave still uses exactly the causal winner among both persisted writer keys", async () => {
    const tokenA = { generation: 4, writerId: "writer-z" };
    const tokenB = { generation: 4, writerId: "writer-a" };
    const winner = compareParticipationTokens(tokenB, tokenA) > 0 ? tokenB : tokenA;
    const loser = winner === tokenB ? tokenA : tokenB;
    expect(winner).not.toEqual(loser);

    // Both A's and B's own writer keys are independently persisted — the
    // real outcome of two tabs racing with zero synchronization, neither
    // ever overwriting the other's key.
    participationStorage = {
      [callId]: { [tokenA.writerId]: tokenA.generation, [tokenB.writerId]: tokenB.generation },
    };
    expect(ownership.getParticipationToken(callId)).toEqual(winner);

    // A brand-new/reloaded tab: fresh CallSessionProvider instance, EMPTY
    // local participationRecordsRef — it never itself observed either
    // participation being established.
    resource.active = { kind: "channel", id: channelId, name: "Produto" };
    resource.callId = callId;
    resource.status = "active";
    renderProvider();
    ownership.post.mockClear();

    fireEvent.click(screen.getByRole("button", { name: "Leave dedicated probe" }));
    await waitFor(() => expect(ownership.post).toHaveBeenCalled());

    // The fresh tab's own leaving/left is tagged with EXACTLY the causal
    // winner — never the loser, and never reset to a fresh generation 1.
    const leavingCall = ownership.post.mock.calls.find(
      (postCall) => (postCall[0] as { type?: string }).type === "leaving",
    );
    expect(leavingCall?.[0]).toMatchObject({
      type: "leaving",
      callId,
      generation: winner.generation,
      writerId: winner.writerId,
    });
    await waitFor(() =>
      expect(ownership.post).toHaveBeenCalledWith(
        expect.objectContaining({
          type: "left",
          callId,
          generation: winner.generation,
          writerId: winner.writerId,
        }),
      ),
    );

    // Every tab that already knows the winner as current accepts these
    // terminal events: the posted messages carry the SAME winning token
    // with a strictly increasing sequence, which is exactly what
    // compareParticipationTokens plus the sequence guard in
    // commitParticipation accepts (proven directly by the
    // "left always wins over a reordered leaving" and
    // "delivers '%s' to listeners unconditionally" tests) — never rejected
    // as belonging to an unrelated, older participation.
    const leftCall = ownership.post.mock.calls.find(
      (postCall) => (postCall[0] as { type?: string }).type === "left",
    )![0] as { generation: number; writerId: string };
    expect(compareParticipationTokens(leftCall, loser)).toBeGreaterThan(0);
    expect(compareParticipationTokens(leftCall, winner)).toBe(0);
  });
});

describe("React StrictMode ownership coordinator lifecycle (CALLS-546 regression)", () => {
  // Each call to createOwnershipCoordinator() returns a fresh, independently
  // tracked instance — unlike the single shared `ownership` mock used by
  // every other describe block above — because this suite's whole point is
  // to prove the provider ends up with a *different*, *open* instance after
  // React StrictMode's dev-only mount -> cleanup -> mount probe, never a
  // reused closed one.
  function createMockCoordinator(label: string) {
    let closed = false;
    const unsubscribe = vi.fn();
    const unsubscribeLost = vi.fn();
    const instance = {
      label,
      tabId: `tab-${label}`,
      claim: vi.fn(async (claimCallId: string, role: "main" | "dedicated") =>
        closed
          ? null
          : {
              v: 1 as const,
              callId: claimCallId,
              tabId: `tab-${label}`,
              epoch: 1,
              role,
              expiresAt: 999_999,
            },
      ),
      getLease: vi.fn(() => null),
      getOwner: vi.fn(() => null),
      release: vi.fn(),
      post: vi.fn(),
      subscribe: vi.fn((listener: typeof ownershipListener) => {
        ownershipListener = listener;
        return unsubscribe;
      }),
      onOwnershipLost: vi.fn((listener: typeof ownershipLost) => {
        ownershipLost = listener;
        return unsubscribeLost;
      }),
      close: vi.fn(() => {
        closed = true;
      }),
      isClosed: vi.fn(() => closed),
    };
    return { instance, unsubscribe, unsubscribeLost };
  }

  let created: Array<ReturnType<typeof createMockCoordinator>>;

  beforeEach(() => {
    vi.useRealTimers();
    vi.clearAllMocks();
    participationStorage = {};
    calls.call = null;
    resource.active = null;
    resource.callId = null;
    created = [];
    vi.mocked(createOwnershipCoordinator).mockImplementation(() => {
      const made = createMockCoordinator(`c${created.length}`);
      created.push(made);
      return made.instance as never;
    });
    vi.mocked(useCallMedia).mockReturnValue(media as never);
    vi.mocked(useCallSignaling).mockReturnValue(calls as never);
    vi.mocked(useResourceCallSession).mockReturnValue(resource as never);
    vi.mocked(acquireChatSocket).mockImplementation((listener) => {
      socketListener = listener;
      return { send: vi.fn(), isOpen: vi.fn(), generation: vi.fn(), release: vi.fn() };
    });
  });

  it("survives the StrictMode probe: closes the first instance and keeps a second, open one active", () => {
    render(<StrictMode>{providerTree()}</StrictMode>);

    // The probe creates exactly two coordinators for this one provider
    // mount — never more (no duplicate live coordinators) and never just
    // one reused across the cleanup (that reused-after-close instance is
    // exactly CALLS-546's bug).
    expect(created).toHaveLength(2);
    const [first, second] = created;
    expect(first.instance).not.toBe(second.instance);

    // The probe's cleanup closed the first instance...
    expect(first.instance.close).toHaveBeenCalledOnce();
    expect(first.instance.isClosed()).toBe(true);
    // ...and the provider is left with a second, functional, open one.
    expect(second.instance.close).not.toHaveBeenCalled();
    expect(second.instance.isClosed()).toBe(false);
  });

  it("routes announceDedicated/claim to the active (second) instance, never the closed first one", async () => {
    render(<StrictMode>{providerTree(`/call/${callId}`)}</StrictMode>);
    const [first, second] = created;

    fireEvent.click(screen.getByRole("button", { name: "Ready probe" }));
    // No owner on the active instance -> immediate claim (achado #1).
    await waitFor(() => expect(second.instance.claim).toHaveBeenCalledWith(callId, "dedicated", 0));
    expect(second.instance.post).toHaveBeenCalledWith(expect.objectContaining({ type: "ready" }));

    // The closed, discarded first instance must never see any of this.
    expect(first.instance.claim).not.toHaveBeenCalled();
    expect(first.instance.post).not.toHaveBeenCalled();
    await waitFor(() => expect(screen.getByTestId("owner")).toHaveTextContent("local"));
  });

  it("exercises claim-without-owner, handoff, and release against the active instance after the probe", async () => {
    render(<StrictMode>{providerTree(`/call/${callId}`)}</StrictMode>);
    const [, second] = created;

    // claim sem owner (direct dedicated recovery uses the same path).
    fireEvent.click(screen.getByRole("button", { name: "Ready probe" }));
    await waitFor(() => expect(second.instance.claim).toHaveBeenCalledWith(callId, "dedicated", 0));

    // handoff: a targeted "handoff" message reaching this tab claims on
    // the active instance.
    second.instance.claim.mockClear();
    act(() =>
      ownershipListener({
        v: 1,
        type: "handoff",
        callId,
        tabId: "tab-main",
        targetTabId: second.instance.tabId,
        epoch: 4,
      } as never),
    );
    await waitFor(() =>
      expect(second.instance.claim).toHaveBeenCalledWith(callId, "dedicated", 4, {
        tabId: "tab-main",
        epoch: 4,
      }),
    );

    // release: releaseDedicated always resolves the active instance too.
    fireEvent.click(screen.getByRole("button", { name: "Release probe" }));
    await waitFor(() => expect(second.instance.release).toHaveBeenCalledWith(callId));
  });

  it("final unmount closes the active instance and tears down its listeners — no dangling handle", () => {
    const view = render(<StrictMode>{providerTree()}</StrictMode>);
    const [, second] = created;
    expect(second.instance.subscribe).toHaveBeenCalledOnce();
    expect(second.instance.onOwnershipLost).toHaveBeenCalledOnce();

    view.unmount();

    expect(second.instance.close).toHaveBeenCalledOnce();
    expect(second.instance.isClosed()).toBe(true);
    // subscribe()/onOwnershipLost() returned unsubscribe functions — React
    // calling an effect's cleanup is what invokes them.
    expect(created[1].unsubscribe).toHaveBeenCalledOnce();
    expect(created[1].unsubscribeLost).toHaveBeenCalledOnce();
    // No coordinator ever left open after the real unmount.
    expect(created.every((c) => c.instance.isClosed())).toBe(true);
    expect(created).toHaveLength(2);
  });
});

describe("useCallSession", () => {
  it("rejects consumers outside the provider", () => {
    function InvalidConsumer() {
      useCallSession();
      return null;
    }
    expect(() => render(<InvalidConsumer />)).toThrow("useCallSession must be used within");
  });
});
