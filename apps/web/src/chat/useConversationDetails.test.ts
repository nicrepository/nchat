import { act, renderHook, waitFor } from "@testing-library/react";

import { ApiRequestError } from "../lib/api";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import {
  channelFilesPreviewLimit,
  previewReconcileDelayMs,
  previewReconcileIntervalMs,
  previewReconcileMaxAttempts,
  previewReconcileMaxIntervalMs,
  previewRevalidateIntervalMs,
  useConversationDetails,
  type ConversationDetailsTarget,
} from "./useConversationDetails";
import type { ChannelAttachment, ChannelDetails, DirectDetails, GroupDetails } from "./chatTypes";

const {
  mockFetchChannelDetails,
  mockFetchGroupDetails,
  mockFetchDirectProfile,
  mockFetchChannelAttachments,
} = vi.hoisted(() => ({
  mockFetchChannelDetails:
    vi.fn<(channelId: string, signal?: AbortSignal) => Promise<ChannelDetails>>(),
  mockFetchGroupDetails: vi.fn<(id: string, signal?: AbortSignal) => Promise<GroupDetails>>(),
  mockFetchDirectProfile: vi.fn<(id: string, signal?: AbortSignal) => Promise<DirectDetails>>(),
  mockFetchChannelAttachments:
    vi.fn<
      (
        target: { kind: "channel" | "dm"; id: string },
        limit: number,
        signal?: AbortSignal,
      ) => Promise<ChannelAttachment[]>
    >(),
}));

vi.mock("./chatApi", () => ({
  fetchChannelDetails: (channelId: string, signal?: AbortSignal) =>
    mockFetchChannelDetails(channelId, signal),
  fetchGroupDetails: (conversationId: string, signal?: AbortSignal) =>
    mockFetchGroupDetails(conversationId, signal),
  fetchDirectProfile: (conversationId: string, signal?: AbortSignal) =>
    mockFetchDirectProfile(conversationId, signal),
}));

vi.mock("./filesApi", () => ({
  fetchConversationAttachments: (
    target: { kind: "channel" | "dm"; id: string },
    limit: number,
    signal?: AbortSignal,
  ) => mockFetchChannelAttachments(target, limit, signal),
}));

function details(overrides: Partial<ChannelDetails> = {}): ChannelDetails {
  return {
    id: "ch-1",
    slug: "infra",
    name: "Infraestrutura",
    type: "public",
    createdAt: "2024-01-12T09:30:00Z",
    memberCount: 3,
    onlineCount: 0,
    canManageMembers: false,
    onlineMembers: [],
    ...overrides,
  };
}

function attachment(id: string): ChannelAttachment {
  return {
    id,
    filename: `${id}.pdf`,
    contentType: "application/pdf",
    size: 1024,
    status: "clean",
    previewStatus: "unsupported",
    createdAt: "2026-07-15T12:00:00.000Z",
  };
}

beforeEach(() => {
  mockFetchChannelDetails.mockResolvedValue(details());
  mockFetchChannelAttachments.mockResolvedValue([]);
});

afterEach(() => {
  vi.clearAllMocks();
});

describe("useConversationDetails", () => {
  it("issues no request while idle", () => {
    const { result } = renderHook(() => useConversationDetails(null));

    expect(mockFetchChannelDetails).not.toHaveBeenCalled();
    expect(mockFetchChannelAttachments).not.toHaveBeenCalled();
    expect(result.current.details.status).toBe("loading");
  });

  it("loads both sections for a channel", async () => {
    mockFetchChannelDetails.mockResolvedValue(details({ name: "Infra" }));
    mockFetchChannelAttachments.mockResolvedValue([attachment("a-1")]);

    const { result } = renderHook(() => useConversationDetails({ kind: "channel", id: "ch-1" }));

    await waitFor(() => expect(result.current.details.status).toBe("ready"));
    await waitFor(() => expect(result.current.files.status).toBe("ready"));

    expect(result.current.details).toEqual({
      status: "ready",
      data: { kind: "channel", ...details({ name: "Infra" }) },
    });
    expect(mockFetchChannelAttachments).toHaveBeenCalledWith(
      { kind: "channel", id: "ch-1" },
      channelFilesPreviewLimit,
      expect.any(AbortSignal),
    );
  });

  it("keeps one failed section from destroying the other", async () => {
    mockFetchChannelAttachments.mockRejectedValue(new Error("file-service down"));

    const { result } = renderHook(() => useConversationDetails({ kind: "channel", id: "ch-1" }));

    await waitFor(() => expect(result.current.files.status).toBe("error"));
    expect(result.current.details.status).toBe("ready");
  });

  it("resets to loading and refetches when the channel changes", async () => {
    mockFetchChannelDetails.mockResolvedValueOnce(details({ id: "ch-1", name: "Primeiro" }));
    const { result, rerender } = renderHook(
      ({ id }) => useConversationDetails({ kind: "channel", id }),
      {
        initialProps: { id: "ch-1" },
      },
    );
    await waitFor(() => expect(result.current.details.status).toBe("ready"));

    // A never-resolving second channel: the panel must show loading, not the
    // previous channel's data under the new channel's name.
    mockFetchChannelDetails.mockReturnValueOnce(new Promise<ChannelDetails>(() => {}));
    mockFetchChannelAttachments.mockReturnValueOnce(new Promise<ChannelAttachment[]>(() => {}));
    rerender({ id: "ch-2" });

    expect(result.current.details.status).toBe("loading");
    expect(result.current.files.status).toBe("loading");
    expect(mockFetchChannelDetails).toHaveBeenLastCalledWith("ch-2", expect.any(AbortSignal));
  });

  it("aborts the previous channel's request and ignores its late answer", async () => {
    let resolveFirst: (value: ChannelDetails) => void = () => {};
    let firstSignal: AbortSignal | undefined;
    mockFetchChannelDetails.mockImplementationOnce((_id, signal) => {
      firstSignal = signal;
      return new Promise<ChannelDetails>((resolve) => {
        resolveFirst = resolve;
      });
    });

    const { result, rerender } = renderHook(
      ({ id }) => useConversationDetails({ kind: "channel", id }),
      {
        initialProps: { id: "ch-1" },
      },
    );

    mockFetchChannelDetails.mockResolvedValueOnce(details({ id: "ch-2", name: "Segundo" }));
    rerender({ id: "ch-2" });
    await waitFor(() => expect(result.current.details.status).toBe("ready"));

    expect(firstSignal?.aborted).toBe(true);
    // The first channel finally answers — long after the switch. It must not
    // overwrite the channel the user is actually looking at.
    await act(async () => {
      resolveFirst(details({ id: "ch-1", name: "Primeiro" }));
    });

    expect(result.current.details).toEqual({
      status: "ready",
      data: { kind: "channel", ...details({ id: "ch-2", name: "Segundo" }) },
    });
  });

  it("aborts in flight requests on unmount", async () => {
    let signal: AbortSignal | undefined;
    mockFetchChannelDetails.mockImplementationOnce((_id, requestSignal) => {
      signal = requestSignal;
      return new Promise<ChannelDetails>(() => {});
    });

    const { unmount } = renderHook(() => useConversationDetails({ kind: "channel", id: "ch-1" }));
    unmount();

    expect(signal?.aborted).toBe(true);
  });

  it("treats an aborted request as neither ready nor error", async () => {
    const abortError = new Error("aborted");
    abortError.name = "AbortError";
    mockFetchChannelDetails.mockRejectedValueOnce(abortError);

    const { result } = renderHook(() => useConversationDetails({ kind: "channel", id: "ch-1" }));
    await waitFor(() => expect(result.current.files.status).toBe("ready"));

    expect(result.current.details.status).toBe("loading");
  });
});

// ── Perfil de DM 1:1 (issue #443) ────────────────────────────────────────────

function group(overrides: Partial<GroupDetails> = {}): GroupDetails {
  return {
    id: "conv-1",
    name: "Time de Infra",
    createdAt: "2024-03-04T15:00:00Z",
    participantCount: 4,
    participants: [],
    canManageMembers: false,
    ...overrides,
  };
}

function profile(
  overrides: Partial<DirectDetails["profile"]> = {},
  conversationId = "conv-dm-1",
): DirectDetails {
  // The client returns the tag; this fixture mirrors that, so a hook that
  // re-tagged the value would have to overwrite something already correct.
  return {
    kind: "direct",
    conversationId,
    profile: { userId: "user-other", displayName: "Juliane Lino", ...overrides },
  };
}

describe("useConversationDetails — perfil de DM 1:1", () => {
  it("loads the profile endpoint and tags the result direct", async () => {
    mockFetchDirectProfile.mockResolvedValue(profile({ email: "juliane@nic.test" }));

    const { result } = renderHook(() =>
      useConversationDetails({ kind: "direct", id: "conv-dm-1" }),
    );

    await waitFor(() => expect(result.current.details.status).toBe("ready"));
    expect(result.current.details).toEqual({
      status: "ready",
      data: profile({ email: "juliane@nic.test" }),
    });
    expect(mockFetchDirectProfile).toHaveBeenCalledWith("conv-dm-1", expect.any(AbortSignal));
    // A profile panel has no files section, so nothing is asked of file-service.
    expect(mockFetchChannelAttachments).not.toHaveBeenCalled();
    // Nor is the group endpoint touched: a 1:1 is not a small group.
    expect(mockFetchGroupDetails).not.toHaveBeenCalled();
  });

  it("shows the error state when the profile is refused", async () => {
    mockFetchDirectProfile.mockRejectedValue(new Error("404"));

    const { result } = renderHook(() =>
      useConversationDetails({ kind: "direct", id: "conv-dm-1" }),
    );

    await waitFor(() => expect(result.current.details.status).toBe("error"));
  });

  it("keys requests by kind and id, so a switch between types refetches", async () => {
    mockFetchDirectProfile.mockResolvedValue(profile());
    mockFetchGroupDetails.mockResolvedValue(group());
    mockFetchChannelDetails.mockResolvedValue(details({ id: "ch-1" }));

    const { result, rerender } = renderHook(
      ({ target }: { target: ConversationDetailsTarget }) => useConversationDetails(target),
      {
        initialProps: { target: { kind: "direct", id: "conv-dm-1" } as ConversationDetailsTarget },
      },
    );
    await waitFor(() => expect(result.current.details.status).toBe("ready"));

    // direct → group
    rerender({ target: { kind: "group", id: "conv-group-1" } });
    await waitFor(() =>
      expect(result.current.details).toEqual({
        status: "ready",
        data: { kind: "group", ...group() },
      }),
    );

    // group → direct
    rerender({ target: { kind: "direct", id: "conv-dm-2" } });
    await waitFor(() =>
      expect(result.current.details).toEqual({
        status: "ready",
        data: profile(),
      }),
    );

    // direct → channel
    rerender({ target: { kind: "channel", id: "ch-1" } });
    await waitFor(() =>
      expect(result.current.details).toEqual({
        status: "ready",
        data: { kind: "channel", ...details({ id: "ch-1" }) },
      }),
    );
  });

  it("resets to loading rather than showing the previous DM's profile", async () => {
    mockFetchDirectProfile.mockResolvedValueOnce(profile({ email: "primeira@nic.test" }));
    const { result, rerender } = renderHook(
      ({ id }) => useConversationDetails({ kind: "direct", id }),
      { initialProps: { id: "conv-dm-1" } },
    );
    await waitFor(() => expect(result.current.details.status).toBe("ready"));

    // The second DM never answers: the panel must be blank, not showing A's
    // name and e-mail under B's conversation.
    mockFetchDirectProfile.mockReturnValueOnce(new Promise<DirectDetails>(() => {}));
    rerender({ id: "conv-dm-2" });

    expect(result.current.details.status).toBe("loading");
  });

  it("drops a late answer for the DM the user already left", async () => {
    let resolveFirst: (value: DirectDetails) => void = () => {};
    let firstSignal: AbortSignal | undefined;
    mockFetchDirectProfile.mockImplementationOnce((_id, signal) => {
      firstSignal = signal;
      return new Promise<DirectDetails>((resolve) => {
        resolveFirst = resolve;
      });
    });

    const { result, rerender } = renderHook(
      ({ id }) => useConversationDetails({ kind: "direct", id }),
      { initialProps: { id: "conv-dm-1" } },
    );

    mockFetchDirectProfile.mockResolvedValueOnce(
      profile({ displayName: "Segunda Pessoa", email: "segunda@nic.test" }),
    );
    rerender({ id: "conv-dm-2" });
    await waitFor(() => expect(result.current.details.status).toBe("ready"));
    expect(firstSignal?.aborted).toBe(true);

    // The first DM finally answers. Showing it now would put one person's
    // e-mail under another person's conversation.
    await act(async () => {
      resolveFirst(profile({ displayName: "Primeira Pessoa", email: "primeira@nic.test" }));
    });

    expect(result.current.details).toEqual({
      status: "ready",
      data: profile({ displayName: "Segunda Pessoa", email: "segunda@nic.test" }),
    });
  });
});

describe("useConversationDetails — o hook não reescreve o contrato do cliente", () => {
  it("passes the client's variant through without re-tagging it", async () => {
    // A value the client would never produce: if the hook rebuilt the tag or
    // substituted the requested id, both of these would be silently corrected
    // and the corruption would become invisible.
    mockFetchDirectProfile.mockResolvedValue({
      kind: "direct",
      conversationId: "conv-dm-1",
      profile: { userId: "user-other", displayName: "Juliane Lino" },
    });

    const { result } = renderHook(() =>
      useConversationDetails({ kind: "direct", id: "conv-dm-1" }),
    );

    await waitFor(() => expect(result.current.details.status).toBe("ready"));
    // Exactly what the client returned — same keys, same values, nothing added.
    expect(result.current.details).toEqual({
      status: "ready",
      data: {
        kind: "direct",
        conversationId: "conv-dm-1",
        profile: { userId: "user-other", displayName: "Juliane Lino" },
      },
    });
  });

  it("propagates a contract violation as an error, keeping no stale profile", async () => {
    mockFetchDirectProfile.mockResolvedValueOnce(profile({ email: "primeira@nic.test" }));
    const { result, rerender } = renderHook(
      ({ id }) => useConversationDetails({ kind: "direct", id }),
      { initialProps: { id: "conv-dm-1" } },
    );
    await waitFor(() => expect(result.current.details.status).toBe("ready"));

    // The client rejects a misrouted or mislabelled payload; the hook must not
    // fall back to what it already had.
    mockFetchDirectProfile.mockRejectedValueOnce(
      new ApiRequestError(200, "invalid_response", "Invalid direct profile response: kind"),
    );
    rerender({ id: "conv-dm-2" });

    await waitFor(() => expect(result.current.details.status).toBe("error"));
    expect(result.current.details).toEqual({ status: "error" });
  });
});

// ── reload (issue #398) ──────────────────────────────────────────────────────

describe("useConversationDetails — reload", () => {
  it("refetches the open target without blanking what is on screen", async () => {
    mockFetchChannelDetails.mockResolvedValue(details({ memberCount: 3 }));
    mockFetchChannelAttachments.mockResolvedValue([]);
    const { result } = renderHook(() => useConversationDetails({ kind: "channel", id: "ch-1" }));
    await waitFor(() => expect(result.current.details.status).toBe("ready"));

    mockFetchChannelDetails.mockResolvedValue(details({ memberCount: 4 }));
    const statuses: string[] = [];
    mockFetchChannelDetails.mockImplementation(async () => {
      // Sampled while the request is in flight: the section must still be
      // "ready". Blanking it here unmounts the add-members control the user
      // just used and drops keyboard focus to <body> mid-flow.
      statuses.push(result.current.details.status);
      return details({ memberCount: 4 });
    });

    await act(async () => {
      result.current.reload();
    });

    await waitFor(() => {
      const section = result.current.details;
      expect(
        section.status === "ready" && section.data.kind === "channel" && section.data.memberCount,
      ).toBe(4);
    });
    expect(statuses).toEqual(["ready"]);
  });

  // A switch is the opposite case and must still reset, or the previous
  // conversation's roster would show under the new one's name.
  it("still blanks the panel on a target switch", async () => {
    mockFetchChannelDetails.mockResolvedValue(details());
    mockFetchChannelAttachments.mockResolvedValue([]);
    const { result, rerender } = renderHook(
      ({ id }: { id: string }) => useConversationDetails({ kind: "channel", id }),
      { initialProps: { id: "ch-1" } },
    );
    await waitFor(() => expect(result.current.details.status).toBe("ready"));

    let pending: (() => void) | undefined;
    mockFetchChannelDetails.mockImplementation(
      () =>
        new Promise<ChannelDetails>((resolve) => {
          pending = () => resolve(details({ id: "ch-2" }));
        }),
    );
    rerender({ id: "ch-2" });

    expect(result.current.details.status).toBe("loading");
    await act(async () => {
      pending?.();
    });
  });

  it("refetches the target that is open now, not the one it was created for", async () => {
    mockFetchChannelDetails.mockResolvedValue(details());
    mockFetchChannelAttachments.mockResolvedValue([]);
    const { result, rerender } = renderHook(
      ({ id }: { id: string }) => useConversationDetails({ kind: "channel", id }),
      { initialProps: { id: "ch-1" } },
    );
    await waitFor(() => expect(result.current.details.status).toBe("ready"));
    // Captured before the switch, the way useMessages holds it for the lifetime
    // of the socket.
    const captured = result.current.reload;

    rerender({ id: "ch-2" });
    await waitFor(() => expect(result.current.details.status).toBe("ready"));
    mockFetchChannelDetails.mockClear();

    await act(async () => {
      captured();
    });

    expect(mockFetchChannelDetails).toHaveBeenCalledWith("ch-2", expect.any(AbortSignal));
  });
});

// ── Preview reconciliation (RF-31, issue #464) ───────────────────────────────
//
// A preview is produced after the upload has answered, so a panel that is
// already open has to notice `pending` becoming `ready` on its own. These tests
// use fake timers throughout: the cycle is defined by when it fires and when it
// stops, and a real interval would make that either slow or flaky.

/** Cleared by the scan, still waiting for the render. */
function pendingAttachment(id: string): ChannelAttachment {
  return { ...attachment(id), status: "clean", previewStatus: "pending" };
}

/**
 * The state every upload starts in when the malware scan is on — which is the
 * default. The panel spends most of its waiting here, not in `clean`.
 */
function scanningAttachment(id: string): ChannelAttachment {
  return { ...attachment(id), status: "pending_scan", previewStatus: "pending" };
}

function readyAttachment(id: string): ChannelAttachment {
  return { ...attachment(id), status: "clean", previewStatus: "ready" };
}

describe("useConversationDetails — preview reconciliation", () => {
  beforeEach(() => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
  });

  afterEach(() => {
    vi.useRealTimers();
  });

  /** Renders and waits for the first load to settle. */
  async function renderLoaded(target: ConversationDetailsTarget = { kind: "channel", id: "ch-1" }) {
    const rendered = renderHook(
      (props: ConversationDetailsTarget) => useConversationDetails(props),
      {
        initialProps: target,
      },
    );
    await waitFor(() => expect(rendered.result.current.files.status).toBe("ready"));
    return rendered;
  }

  /** Advances past the base delay — the one used whenever the list just moved. */
  async function advancePastInterval() {
    await act(async () => {
      await vi.advanceTimersByTimeAsync(previewReconcileIntervalMs);
    });
  }

  /**
   * Advances past the delay that follows `attempt` polls which saw no change.
   *
   * Needed wherever a test polls twice without the list moving in between: the
   * cadence is not fixed, it doubles, so advancing by the base interval again
   * would land before the next timer and read as "the cycle stopped".
   */
  async function advancePastDelay(attempt: number) {
    await act(async () => {
      await vi.advanceTimersByTimeAsync(previewReconcileDelayMs(attempt));
    });
  }

  /** Runs the cycle until its budget is spent, with the list never changing. */
  async function exhaustBudget() {
    for (let attempt = 0; attempt < previewReconcileMaxAttempts; attempt += 1) {
      await advancePastDelay(attempt);
    }
  }

  /** Advances past the slow cadence that watches a displayed thumbnail. */
  async function advancePastRevalidate() {
    await act(async () => {
      await vi.advanceTimersByTimeAsync(previewRevalidateIntervalMs);
    });
  }

  /**
   * Sets the tab's visibility and fires the event the browser would.
   *
   * defineProperty rather than vi.spyOn: `visibilityState` is an accessor on
   * Document.prototype with no own descriptor to restore, and restoring a spy
   * on one leaves the property broken for every later test in the file.
   */
  async function setVisibility(value: "visible" | "hidden") {
    Object.defineProperty(document, "visibilityState", {
      configurable: true,
      get: () => value,
    });
    await act(async () => {
      document.dispatchEvent(new Event("visibilitychange"));
    });
  }

  afterEach(() => {
    // Back to the prototype's accessor, whatever the test did. Reflect rather
    // than `delete`: the property is readonly in the DOM types.
    Reflect.deleteProperty(document, "visibilityState");
  });

  it("schedules nothing when nothing is pending and nothing is displayed", async () => {
    // Neither attachment can change and neither has bytes on screen: one is
    // unsupported, the other failed.
    mockFetchChannelAttachments.mockResolvedValue([
      attachment("a-1"),
      { ...attachment("a-2"), previewStatus: "failed" as const },
    ]);

    await renderLoaded();
    expect(mockFetchChannelAttachments).toHaveBeenCalledTimes(1);

    await advancePastInterval();
    await advancePastRevalidate();

    expect(mockFetchChannelAttachments).toHaveBeenCalledTimes(1);
  });

  it("re-reads the list while a preview is pending", async () => {
    mockFetchChannelAttachments.mockResolvedValue([pendingAttachment("a-1")]);

    await renderLoaded();
    expect(mockFetchChannelAttachments).toHaveBeenCalledTimes(1);

    await advancePastInterval();

    expect(mockFetchChannelAttachments).toHaveBeenCalledTimes(2);
    // Only the attachment list is re-read: the conversation projection has not
    // changed and re-requesting it would be load nobody asked for.
    expect(mockFetchChannelDetails).toHaveBeenCalledTimes(1);
    expect(mockFetchChannelAttachments).toHaveBeenLastCalledWith(
      { kind: "channel", id: "ch-1" },
      channelFilesPreviewLimit,
      expect.any(AbortSignal),
    );
  });

  it("stops as soon as the preview turns ready", async () => {
    mockFetchChannelAttachments.mockResolvedValueOnce([pendingAttachment("a-1")]);
    mockFetchChannelAttachments.mockResolvedValue([readyAttachment("a-1")]);

    const { result } = await renderLoaded();
    await advancePastInterval();

    await waitFor(() => {
      const files = result.current.files;
      expect(files.status === "ready" && files.data[0].previewStatus).toBe("ready");
    });
    const callsAtReady = mockFetchChannelAttachments.mock.calls.length;

    await advancePastInterval();
    await advancePastInterval();

    expect(mockFetchChannelAttachments).toHaveBeenCalledTimes(callsAtReady);
  });

  // Terminal states end the cycle exactly like ready does: nothing about them
  // can change without a new upload.
  it.each(["failed", "unsupported"] as const)("stops when the preview turns %s", async (final) => {
    mockFetchChannelAttachments.mockResolvedValueOnce([pendingAttachment("a-1")]);
    mockFetchChannelAttachments.mockResolvedValue([
      { ...pendingAttachment("a-1"), previewStatus: final },
    ]);

    const { result } = await renderLoaded();
    await advancePastInterval();

    await waitFor(() => {
      const files = result.current.files;
      expect(files.status === "ready" && files.data[0].previewStatus).toBe(final);
    });
    const callsAtTerminal = mockFetchChannelAttachments.mock.calls.length;

    await advancePastInterval();

    expect(mockFetchChannelAttachments).toHaveBeenCalledTimes(callsAtTerminal);
  });

  // A rejected attachment is never claimed by the preview worker, so its
  // preview cannot move. Polling it would be a timer that never stops.
  it("does not reconcile an attachment the scan rejected", async () => {
    mockFetchChannelAttachments.mockResolvedValue([
      { ...pendingAttachment("a-1"), status: "rejected" },
    ]);

    await renderLoaded();
    await advancePastInterval();
    await advancePastDelay(1);

    expect(mockFetchChannelAttachments).toHaveBeenCalledTimes(1);
  });

  // The bug this cycle was rewritten for. Every upload starts in pending_scan
  // when scanning is on, and the panel used to treat that as finished — so the
  // thumbnail of a file the user had just sent only ever appeared if they
  // closed the panel and opened it again.
  it("reconciles an attachment that is still awaiting the malware scan", async () => {
    mockFetchChannelAttachments.mockResolvedValue([scanningAttachment("a-1")]);

    await renderLoaded();
    expect(mockFetchChannelAttachments).toHaveBeenCalledTimes(1);

    await advancePastInterval();

    expect(mockFetchChannelAttachments).toHaveBeenCalledTimes(2);
  });

  // The whole default path, in the order the server actually produces it: the
  // scan rules, and only then does the render happen. Both waits have to be
  // covered by one window, and the window has to end on its own at the end.
  it("follows an upload from pending_scan through clean to ready", async () => {
    mockFetchChannelAttachments.mockResolvedValueOnce([scanningAttachment("a-1")]);
    mockFetchChannelAttachments.mockResolvedValueOnce([pendingAttachment("a-1")]);
    mockFetchChannelAttachments.mockResolvedValue([readyAttachment("a-1")]);

    const { result } = await renderLoaded();

    // The scan clears: still nothing to render, still watching.
    await advancePastInterval();
    await waitFor(() => {
      const files = result.current.files;
      expect(files.status === "ready" && files.data[0].status).toBe("clean");
    });

    // The render lands. Progress reset the backoff, so this is the base delay
    // again, not a doubled one.
    await advancePastInterval();
    await waitFor(() => {
      const files = result.current.files;
      expect(files.status === "ready" && files.data[0].previewStatus).toBe("ready");
    });

    // The fast cycle is over. What continues from here is the slow revocation
    // watch, on its own cadence — see "watching a displayed thumbnail" below.
    const callsAtReady = mockFetchChannelAttachments.mock.calls.length;
    await advancePastDelay(1);
    expect(mockFetchChannelAttachments).toHaveBeenCalledTimes(callsAtReady);
  });

  // A rejection is the other way the scan ends, and it ends the cycle too: a
  // rejected attachment is never rendered.
  it("stops when the scan rejects the attachment", async () => {
    mockFetchChannelAttachments.mockResolvedValueOnce([scanningAttachment("a-1")]);
    mockFetchChannelAttachments.mockResolvedValue([
      { ...scanningAttachment("a-1"), status: "rejected", previewStatus: "unsupported" },
    ]);

    const { result } = await renderLoaded();
    await advancePastInterval();

    await waitFor(() => {
      const files = result.current.files;
      expect(files.status === "ready" && files.data[0].status).toBe("rejected");
    });
    const callsAtRejection = mockFetchChannelAttachments.mock.calls.length;

    await advancePastInterval();
    await advancePastDelay(1);

    expect(mockFetchChannelAttachments).toHaveBeenCalledTimes(callsAtRejection);
  });

  // Cleared, but nothing to draw. The panel keeps the fallback and stops
  // watching: no later poll can turn an unsupported type into a preview.
  it("stops when a cleared attachment turns out to be unsupported", async () => {
    mockFetchChannelAttachments.mockResolvedValueOnce([scanningAttachment("a-1")]);
    mockFetchChannelAttachments.mockResolvedValue([
      { ...pendingAttachment("a-1"), previewStatus: "unsupported" },
    ]);

    const { result } = await renderLoaded();
    await advancePastInterval();

    await waitFor(() => {
      const files = result.current.files;
      expect(files.status === "ready" && files.data[0].previewStatus).toBe("unsupported");
    });
    const callsAtTerminal = mockFetchChannelAttachments.mock.calls.length;

    await advancePastInterval();
    await advancePastDelay(1);

    expect(mockFetchChannelAttachments).toHaveBeenCalledTimes(callsAtTerminal);
  });

  it("runs one cycle for the panel, not one per pending attachment", async () => {
    mockFetchChannelAttachments.mockResolvedValue([
      pendingAttachment("a-1"),
      pendingAttachment("a-2"),
      pendingAttachment("a-3"),
    ]);

    await renderLoaded();
    await advancePastInterval();

    // Three pending attachments, one extra request.
    expect(mockFetchChannelAttachments).toHaveBeenCalledTimes(2);
  });

  it("keeps polling while only some attachments reach ready", async () => {
    mockFetchChannelAttachments.mockResolvedValueOnce([
      pendingAttachment("a-1"),
      pendingAttachment("a-2"),
    ]);
    mockFetchChannelAttachments.mockResolvedValue([
      readyAttachment("a-1"),
      pendingAttachment("a-2"),
    ]);

    await renderLoaded();
    await advancePastInterval();
    expect(mockFetchChannelAttachments).toHaveBeenCalledTimes(2);

    await advancePastInterval();
    expect(mockFetchChannelAttachments).toHaveBeenCalledTimes(3);
  });

  // The next request is scheduled when the previous one *finishes*, so a slow
  // response cannot be overtaken by its own timer.
  it("never overlaps requests when a response is slower than the interval", async () => {
    let releaseSecond: (files: ChannelAttachment[]) => void = () => {};
    mockFetchChannelAttachments.mockResolvedValueOnce([pendingAttachment("a-1")]);
    mockFetchChannelAttachments.mockImplementationOnce(
      () => new Promise<ChannelAttachment[]>((resolve) => (releaseSecond = resolve)),
    );
    mockFetchChannelAttachments.mockResolvedValue([readyAttachment("a-1")]);

    await renderLoaded();
    await advancePastInterval();
    expect(mockFetchChannelAttachments).toHaveBeenCalledTimes(2);

    // Several intervals pass while the second request is still in flight.
    await advancePastInterval();
    await advancePastInterval();
    expect(mockFetchChannelAttachments).toHaveBeenCalledTimes(2);

    await act(async () => {
      releaseSecond([pendingAttachment("a-1")]);
    });
    // That poll saw no change, so the next one is a doubled delay away.
    await advancePastDelay(1);

    expect(mockFetchChannelAttachments).toHaveBeenCalledTimes(3);
  });

  it("keeps the list and retries after a transient failure", async () => {
    mockFetchChannelAttachments.mockResolvedValueOnce([pendingAttachment("a-1")]);
    mockFetchChannelAttachments.mockRejectedValueOnce(
      new ApiRequestError(503, "unavailable", "no"),
    );
    mockFetchChannelAttachments.mockResolvedValue([readyAttachment("a-1")]);

    const { result } = await renderLoaded();
    await advancePastInterval();

    // The section is still ready and still shows what it had: a background poll
    // missing must not blank a working list.
    expect(result.current.files.status).toBe("ready");

    // A failed poll costs a step of backoff like any other unchanged one — the
    // retry is the next scheduled delay, never an immediate second request.
    await advancePastDelay(1);

    await waitFor(() => {
      const files = result.current.files;
      expect(files.status === "ready" && files.data[0].previewStatus).toBe("ready");
    });
  });

  it("cancels the cycle on unmount", async () => {
    mockFetchChannelAttachments.mockResolvedValue([pendingAttachment("a-1")]);

    const { unmount } = await renderLoaded();
    unmount();

    await advancePastInterval();
    await advancePastInterval();

    expect(mockFetchChannelAttachments).toHaveBeenCalledTimes(1);
  });

  it("cancels the previous conversation's cycle on a target switch", async () => {
    mockFetchChannelAttachments.mockResolvedValue([pendingAttachment("a-1")]);
    mockFetchGroupDetails.mockResolvedValue({
      id: "cv-9",
      kind: "group",
      name: "Grupo",
    } as unknown as GroupDetails);

    const { rerender, result } = await renderLoaded();

    mockFetchChannelAttachments.mockClear();
    mockFetchChannelAttachments.mockResolvedValue([readyAttachment("b-1")]);
    rerender({ kind: "group", id: "cv-9" });
    await waitFor(() => expect(result.current.files.status).toBe("ready"));

    const callsAfterSwitch = mockFetchChannelAttachments.mock.calls.length;
    await advancePastInterval();

    // The new target loaded a list with nothing pending, so no cycle is running
    // — and the old target's timer did not survive the switch.
    expect(mockFetchChannelAttachments).toHaveBeenCalledTimes(callsAfterSwitch);
    for (const [target] of mockFetchChannelAttachments.mock.calls) {
      expect(target).toEqual({ kind: "dm", id: "cv-9" });
    }
  });

  // A reconsulta still in flight when the conversation changes describes a
  // target nobody is looking at.
  it("ignores a reconciliation response that arrives after a target switch", async () => {
    let releaseStale: (files: ChannelAttachment[]) => void = () => {};
    mockFetchChannelAttachments.mockResolvedValueOnce([pendingAttachment("a-1")]);
    mockFetchChannelAttachments.mockImplementationOnce(
      () => new Promise<ChannelAttachment[]>((resolve) => (releaseStale = resolve)),
    );
    mockFetchGroupDetails.mockResolvedValue({
      id: "cv-9",
      kind: "group",
      name: "Grupo",
    } as unknown as GroupDetails);

    const { rerender, result } = await renderLoaded();
    await advancePastInterval();

    mockFetchChannelAttachments.mockResolvedValue([readyAttachment("b-1")]);
    rerender({ kind: "group", id: "cv-9" });
    await waitFor(() => {
      const files = result.current.files;
      expect(files.status === "ready" && files.data[0]?.id).toBe("b-1");
    });

    // The previous conversation's poll answers now.
    await act(async () => {
      releaseStale([pendingAttachment("a-1")]);
    });

    const files = result.current.files;
    expect(files.status === "ready" && files.data[0]?.id).toBe("b-1");
  });

  it("does not restart the cycle when the caller rebuilds the target literal", async () => {
    mockFetchChannelAttachments.mockResolvedValue([pendingAttachment("a-1")]);

    const { rerender } = await renderLoaded();
    rerender({ kind: "channel", id: "ch-1" });
    rerender({ kind: "channel", id: "ch-1" });

    expect(mockFetchChannelAttachments).toHaveBeenCalledTimes(1);

    await advancePastInterval();

    // One rerendered literal, one poll — not one per render.
    expect(mockFetchChannelAttachments).toHaveBeenCalledTimes(2);
  });

  // A group's attachments live under the DM destination, and the reconciliation
  // has to name the same resource the initial load did. Polling a group's id
  // against the channel destination would be a request for a different
  // resource with different authorization.
  it("reconciles a group panel against its dm destination", async () => {
    mockFetchGroupDetails.mockResolvedValue({
      id: "cv-9",
      kind: "group",
      name: "Grupo",
    } as unknown as GroupDetails);
    mockFetchChannelAttachments.mockResolvedValue([scanningAttachment("a-1")]);

    await renderLoaded({ kind: "group", id: "cv-9" });
    await advancePastInterval();

    expect(mockFetchChannelAttachments).toHaveBeenCalledTimes(2);
    for (const [target] of mockFetchChannelAttachments.mock.calls) {
      expect(target).toEqual({ kind: "dm", id: "cv-9" });
    }
  });

  // The abort itself surfaces as a rejection, and it must be indistinguishable
  // from silence: it describes a panel that is gone.
  it("swallows a poll that fails because it was aborted", async () => {
    let rejectPoll: (error: unknown) => void = () => {};
    mockFetchChannelAttachments.mockResolvedValueOnce([pendingAttachment("a-1")]);
    mockFetchChannelAttachments.mockImplementationOnce(
      () => new Promise<ChannelAttachment[]>((_, reject) => (rejectPoll = reject)),
    );

    const { unmount } = await renderLoaded();
    await advancePastInterval();
    expect(mockFetchChannelAttachments).toHaveBeenCalledTimes(2);

    unmount();
    const abort = new Error("aborted");
    abort.name = "AbortError";
    await act(async () => {
      rejectPoll(abort);
    });

    // No dispatch into a dead component, and no further scheduling.
    await advancePastDelay(1);
    expect(mockFetchChannelAttachments).toHaveBeenCalledTimes(2);
  });

  // ── Watching a displayed thumbnail (SEC-001) ───────────────────────────────
  //
  // The opposite job from everything above. A rescan can condemn a file that
  // already has a preview, and by then the bytes are in the page: the server
  // refusing the *next* request does not take back the one it already answered.
  // Only the panel noticing can remove what is on screen, and it can only
  // notice if it keeps asking.

  it("keeps watching an attachment whose preview is already displayed", async () => {
    mockFetchChannelAttachments.mockResolvedValue([readyAttachment("a-1")]);

    await renderLoaded();
    expect(mockFetchChannelAttachments).toHaveBeenCalledTimes(1);

    // Not on the fast cadence — there is nothing being generated.
    await advancePastInterval();
    expect(mockFetchChannelAttachments).toHaveBeenCalledTimes(1);

    await advancePastRevalidate();
    expect(mockFetchChannelAttachments).toHaveBeenCalledTimes(2);

    await advancePastRevalidate();
    expect(mockFetchChannelAttachments).toHaveBeenCalledTimes(3);
  });

  // The blocker, at this layer: the panel has to *see* the rejection.
  it("observes a rejection that lands after the preview was displayed", async () => {
    mockFetchChannelAttachments.mockResolvedValueOnce([readyAttachment("a-1")]);
    mockFetchChannelAttachments.mockResolvedValue([
      { ...readyAttachment("a-1"), status: "rejected" as const },
    ]);

    const { result } = await renderLoaded();
    await advancePastRevalidate();

    await waitFor(() => {
      const files = result.current.files;
      expect(files.status === "ready" && files.data[0].status).toBe("rejected");
    });
  });

  // Once nothing is displayed any more there is nothing left to take away, so
  // the watch stops rather than running for the life of the panel.
  it("stops watching once the rejection has been observed", async () => {
    mockFetchChannelAttachments.mockResolvedValueOnce([readyAttachment("a-1")]);
    mockFetchChannelAttachments.mockResolvedValue([
      { ...readyAttachment("a-1"), status: "rejected" as const },
    ]);

    await renderLoaded();
    await advancePastRevalidate();
    const callsAtRejection = mockFetchChannelAttachments.mock.calls.length;

    await advancePastRevalidate();
    await advancePastRevalidate();

    expect(mockFetchChannelAttachments).toHaveBeenCalledTimes(callsAtRejection);
  });

  // An attachment that disappears from the listing is the soft-delete case: the
  // server stops listing removed rows, and the panel must stop showing them.
  it("observes an attachment that leaves the listing entirely", async () => {
    mockFetchChannelAttachments.mockResolvedValueOnce([readyAttachment("a-1")]);
    mockFetchChannelAttachments.mockResolvedValue([]);

    const { result } = await renderLoaded();
    await advancePastRevalidate();

    await waitFor(() => {
      const files = result.current.files;
      expect(files.status === "ready" && files.data.length).toBe(0);
    });
    // The list is replaced, never merged — a removed attachment cannot survive
    // in the rendered state and keep its object URL alive.
    const callsAtRemoval = mockFetchChannelAttachments.mock.calls.length;
    await advancePastRevalidate();
    expect(mockFetchChannelAttachments).toHaveBeenCalledTimes(callsAtRemoval);
  });

  // A spent generation budget must not switch the watching off for a thumbnail
  // sharing the list with a stalled upload.
  it("falls back to watching when the generation budget is spent", async () => {
    mockFetchChannelAttachments.mockResolvedValue([
      readyAttachment("a-1"),
      scanningAttachment("a-2"),
    ]);

    await renderLoaded();
    await exhaustBudget();
    const callsAtLimit = mockFetchChannelAttachments.mock.calls.length;

    // The fast cycle gave up on the stalled scan, but the displayed thumbnail
    // is still being watched.
    await advancePastRevalidate();
    expect(mockFetchChannelAttachments).toHaveBeenCalledTimes(callsAtLimit + 1);
  });

  it("runs one timer for the panel, not one per displayed attachment", async () => {
    mockFetchChannelAttachments.mockResolvedValue([
      readyAttachment("a-1"),
      readyAttachment("a-2"),
      readyAttachment("a-3"),
    ]);

    await renderLoaded();
    await advancePastRevalidate();

    expect(mockFetchChannelAttachments).toHaveBeenCalledTimes(2);
  });

  it("never overlaps the watch requests when one is slower than the cadence", async () => {
    let release: (files: ChannelAttachment[]) => void = () => {};
    mockFetchChannelAttachments.mockResolvedValueOnce([readyAttachment("a-1")]);
    mockFetchChannelAttachments.mockImplementationOnce(
      () => new Promise<ChannelAttachment[]>((resolve) => (release = resolve)),
    );

    await renderLoaded();
    await advancePastRevalidate();
    expect(mockFetchChannelAttachments).toHaveBeenCalledTimes(2);

    await advancePastRevalidate();
    await advancePastRevalidate();
    expect(mockFetchChannelAttachments).toHaveBeenCalledTimes(2);

    await act(async () => {
      release([readyAttachment("a-1")]);
    });
    await advancePastRevalidate();
    expect(mockFetchChannelAttachments).toHaveBeenCalledTimes(3);
  });

  it("stops watching on unmount", async () => {
    mockFetchChannelAttachments.mockResolvedValue([readyAttachment("a-1")]);

    const { unmount } = await renderLoaded();
    unmount();

    await advancePastRevalidate();
    await advancePastRevalidate();

    expect(mockFetchChannelAttachments).toHaveBeenCalledTimes(1);
  });

  it("stops watching the previous conversation on a target switch", async () => {
    mockFetchChannelAttachments.mockResolvedValue([readyAttachment("a-1")]);
    mockFetchGroupDetails.mockResolvedValue({
      id: "cv-9",
      kind: "group",
      name: "Grupo",
    } as unknown as GroupDetails);

    const { rerender, result } = await renderLoaded();

    mockFetchChannelAttachments.mockClear();
    mockFetchChannelAttachments.mockResolvedValue([attachment("b-1")]);
    rerender({ kind: "group", id: "cv-9" });
    await waitFor(() => expect(result.current.files.status).toBe("ready"));

    const callsAfterSwitch = mockFetchChannelAttachments.mock.calls.length;
    await advancePastRevalidate();

    // The new list displays nothing, so nothing is watched — and the previous
    // conversation's timer did not survive the switch.
    expect(mockFetchChannelAttachments).toHaveBeenCalledTimes(callsAfterSwitch);
  });

  // A hidden tab is showing nothing, so there is nothing to take off the screen.
  it("pauses the watch while the tab is hidden and resumes when it returns", async () => {
    mockFetchChannelAttachments.mockResolvedValue([readyAttachment("a-1")]);

    await renderLoaded();

    // Hiding must also disarm the timer that was already running, not just stop
    // the next one from being scheduled.
    await setVisibility("hidden");

    await advancePastRevalidate();
    await advancePastRevalidate();
    expect(mockFetchChannelAttachments).toHaveBeenCalledTimes(1);

    await setVisibility("visible");

    // Re-armed, and the staleness after a resume is bounded by the ordinary
    // cadence rather than by how long the tab was away.
    await advancePastRevalidate();
    expect(mockFetchChannelAttachments).toHaveBeenCalledTimes(2);
  });

  // The generation cycle is bounded anyway, but a hidden tab should not spend
  // its budget while nobody is looking at the result.
  it("pauses the generation cycle while the tab is hidden", async () => {
    mockFetchChannelAttachments.mockResolvedValue([scanningAttachment("a-1")]);

    await renderLoaded();
    await setVisibility("hidden");

    await advancePastInterval();
    await advancePastDelay(1);
    expect(mockFetchChannelAttachments).toHaveBeenCalledTimes(1);

    await setVisibility("visible");
    await advancePastInterval();
    expect(mockFetchChannelAttachments).toHaveBeenCalledTimes(2);
  });

  it("removes the visibility listener when the panel goes idle", async () => {
    mockFetchChannelAttachments.mockResolvedValueOnce([readyAttachment("a-1")]);
    mockFetchChannelAttachments.mockResolvedValue([attachment("a-1")]);
    const removeListener = vi.spyOn(document, "removeEventListener");

    const { unmount } = await renderLoaded();
    // ready → unsupported: nothing displayed, nothing pending.
    await advancePastRevalidate();
    unmount();

    expect(removeListener).toHaveBeenCalledWith("visibilitychange", expect.any(Function));
    removeListener.mockRestore();
  });

  // ── The bounded window ─────────────────────────────────────────────────────
  //
  // A scan is not a bounded wait. A scanner that is stopped, unreachable or not
  // deployed never rules, and the attachment stays in pending_scan for as long
  // as the panel is open. These are the tests that keep that from being an
  // unbounded request loop.

  it("slows down while nothing changes, up to the ceiling", async () => {
    mockFetchChannelAttachments.mockResolvedValue([scanningAttachment("a-1")]);

    await renderLoaded();

    // Each poll finds the same list, so each delay is twice the last one — and
    // advancing by only the previous delay is never enough to reach the next.
    for (const attempt of [0, 1, 2, 3]) {
      const before = mockFetchChannelAttachments.mock.calls.length;
      if (attempt > 0) {
        await advancePastDelay(attempt - 1);
        expect(mockFetchChannelAttachments).toHaveBeenCalledTimes(before);
      }
      await advancePastDelay(attempt);
      expect(mockFetchChannelAttachments).toHaveBeenCalledTimes(before + 1);
    }
  });

  it("never waits longer than the ceiling between polls", () => {
    for (const attempt of [0, 1, 5, 12, 100]) {
      expect(previewReconcileDelayMs(attempt)).toBeLessThanOrEqual(previewReconcileMaxIntervalMs);
    }
    // And a negative count — which the hook cannot produce, but the exported
    // function is public — still yields the base delay rather than a fraction.
    expect(previewReconcileDelayMs(-1)).toBe(previewReconcileIntervalMs);
  });

  it("gives up after a bounded number of unchanged polls", async () => {
    mockFetchChannelAttachments.mockResolvedValue([scanningAttachment("a-1")]);

    await renderLoaded();
    await exhaustBudget();

    // The initial load plus exactly the budget, and nothing after it.
    expect(mockFetchChannelAttachments).toHaveBeenCalledTimes(previewReconcileMaxAttempts + 1);

    await advancePastDelay(previewReconcileMaxAttempts);
    await advancePastDelay(previewReconcileMaxAttempts);

    expect(mockFetchChannelAttachments).toHaveBeenCalledTimes(previewReconcileMaxAttempts + 1);
  });

  // The budget counts *unchanged* polls, so a long wait for the scan does not
  // spend what the render after it needs.
  it("restarts the budget whenever the server makes progress", async () => {
    mockFetchChannelAttachments.mockResolvedValue([scanningAttachment("a-1")]);

    await renderLoaded();
    // Spend all but one poll of the window.
    for (let attempt = 0; attempt < previewReconcileMaxAttempts - 1; attempt += 1) {
      await advancePastDelay(attempt);
    }

    // The scan finally rules. That is progress, so the window starts over.
    mockFetchChannelAttachments.mockResolvedValue([pendingAttachment("a-1")]);
    await advancePastDelay(previewReconcileMaxAttempts - 1);

    const callsAtProgress = mockFetchChannelAttachments.mock.calls.length;
    await advancePastInterval();
    expect(mockFetchChannelAttachments).toHaveBeenCalledTimes(callsAtProgress + 1);
  });

  // The way back for a panel whose window ran out, and the reason the window is
  // allowed to be finite at all.
  it("opens a new window on an explicit reload", async () => {
    mockFetchChannelAttachments.mockResolvedValue([scanningAttachment("a-1")]);

    const { result } = await renderLoaded();
    await exhaustBudget();
    const callsAtLimit = mockFetchChannelAttachments.mock.calls.length;

    await act(async () => {
      result.current.reload();
    });
    // The reload's own request, then a poll that the spent window would not
    // have scheduled.
    expect(mockFetchChannelAttachments).toHaveBeenCalledTimes(callsAtLimit + 1);

    await advancePastInterval();
    expect(mockFetchChannelAttachments).toHaveBeenCalledTimes(callsAtLimit + 2);
  });

  // Reopening the panel is the other way back: a fresh mount is a fresh window.
  it("opens a new window when the panel is reopened", async () => {
    mockFetchChannelAttachments.mockResolvedValue([scanningAttachment("a-1")]);

    const { unmount } = await renderLoaded();
    await exhaustBudget();
    unmount();

    mockFetchChannelAttachments.mockClear();
    await renderLoaded();
    await advancePastInterval();

    expect(mockFetchChannelAttachments).toHaveBeenCalledTimes(2);
  });
});
