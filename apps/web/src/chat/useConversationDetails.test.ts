import { act, renderHook, waitFor } from "@testing-library/react";

import { ApiRequestError } from "../lib/api";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import {
  channelFilesPreviewLimit,
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
