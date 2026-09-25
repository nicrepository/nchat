import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";

// The panel itself calls two endpoints for this flow (issue #469): the removal
// of a channel member and of a group participant. Everything else the panel's
// children reach for is stubbed so a row test is about the row.
const { removeChannelMember, removeGroupParticipant } = vi.hoisted(() => ({
  removeChannelMember: vi.fn(),
  removeGroupParticipant: vi.fn(),
}));

vi.mock("./chatApi", () => ({
  removeChannelMember,
  removeGroupParticipant,
  searchChannelMemberCandidates: vi.fn().mockResolvedValue([]),
  searchGroupParticipantCandidates: vi.fn().mockResolvedValue([]),
  addChannelMembers: vi.fn(),
  addGroupParticipants: vi.fn(),
}));

vi.mock("./filesApi", () => ({
  fetchAttachmentPreview: vi.fn(),
  fetchAttachmentContent: () => Promise.reject(new Error("not used")),
}));

import ConversationDetailsPanel from "./ConversationDetailsPanel";
import type { ChannelDetails, ChannelRoster, GroupDetails } from "./chatTypes";
import type { ConversationDetailsState } from "./useConversationDetails";

/**
 * Removing a member from the details panel (issue #469).
 *
 * The properties under test are the ones a screenshot cannot show: the control
 * appears exactly where the *server* said this caller may act, it is never
 * offered for the viewer's own row, the channel list it appears on is the
 * administrable membership rather than the presence preview, and one
 * confirmed removal produces exactly one request and one refetch.
 */

const currentUserId = "user-me";

function channelDetails(overrides: Partial<ChannelDetails> = {}): {
  kind: "channel";
} & ChannelDetails {
  return {
    kind: "channel" as const,
    id: "ch-1",
    slug: "infra",
    name: "Infraestrutura",
    type: "private",
    description: "",
    createdAt: "2024-01-12T09:30:00.000Z",
    memberCount: 3,
    onlineCount: 1,
    onlineMembers: [
      { userId: "u-online", displayName: "Bruno Dias", role: "member", presence: "online" },
    ],
    canAddMembers: true,
    canManageMembers: true,
    canRemoveMembers: true,
    ...overrides,
  };
}

function groupDetails(overrides: Partial<GroupDetails> = {}): { kind: "group" } & GroupDetails {
  return {
    kind: "group" as const,
    id: "conv-1",
    name: "Time de Infra",
    description: "",
    createdAt: "2024-03-04T15:00:00.000Z",
    participantCount: 2,
    participants: [
      { userId: "u-2", displayName: "Fernanda Nicácio" },
      { userId: currentUserId, displayName: "Eu Mesmo" },
    ],
    // Every participant may add; only the creator may remove. The default here
    // is the participant who may not.
    canManageMembers: true,
    canRemoveMembers: false,
    ...overrides,
  };
}

const roster: ChannelRoster = {
  memberCount: 3,
  members: [
    { userId: "u-2", displayName: "Fernanda Nicácio", role: "member" },
    { userId: "u-offline", displayName: "Zulmira Offline", role: "moderator" },
    { userId: currentUserId, displayName: "Eu Mesmo", role: "member" },
  ],
};

function channelState(
  details: { kind: "channel" } & ChannelDetails,
  rosterSection: ConversationDetailsState["roster"],
  reload = vi.fn(),
): ConversationDetailsState {
  return {
    details: { status: "ready", data: details },
    files: { status: "ready", data: [] },
    roster: rosterSection,
    reload,
  };
}

function renderChannel(
  details = channelDetails(),
  rosterSection: ConversationDetailsState["roster"] = { status: "ready", data: roster },
) {
  const reload = vi.fn();
  render(
    <ConversationDetailsPanel
      kind="channel"
      state={channelState(details, rosterSection, reload)}
      currentUserId={currentUserId}
      latestPin={null}
      onClose={vi.fn()}
    />,
  );
  return { reload };
}

function renderGroup(details = groupDetails()) {
  const reload = vi.fn();
  render(
    <ConversationDetailsPanel
      kind="group"
      state={{
        details: { status: "ready", data: details },
        files: { status: "ready", data: [] },
        roster: { status: "loading" },
        reload,
      }}
      currentUserId={currentUserId}
      latestPin={null}
      onClose={vi.fn()}
    />,
  );
  return { reload };
}

const removeButtons = () => screen.queryAllByTestId("chat-details-participant-remove");

beforeEach(() => {
  removeChannelMember.mockReset().mockResolvedValue(undefined);
  removeGroupParticipant.mockReset().mockResolvedValue(undefined);
});

describe("channel removal controls", () => {
  // The whole reason the roster route exists: an offline member has no row in
  // the presence preview, and a member with no row cannot be removed.
  it("lists the administrable membership instead of the presence preview", () => {
    renderChannel();

    expect(screen.getByRole("heading", { name: /^Membros/ })).toBeInTheDocument();
    expect(screen.getByText("Zulmira Offline")).toBeInTheDocument();
    // The online-only preview's member is not part of this membership payload,
    // so it is not invented here either.
    expect(screen.queryByText("Bruno Dias")).not.toBeInTheDocument();
  });

  it("offers a removal for every member except the viewer", () => {
    renderChannel();

    expect(removeButtons()).toHaveLength(2);
    expect(
      screen.getByRole("button", { name: "Remover Fernanda Nicácio do canal" }),
    ).toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: "Remover Eu Mesmo do canal" }),
    ).not.toBeInTheDocument();
  });

  // Hiding the control is presentation; the server refuses regardless. But
  // offering an action that can only fail is worse than not offering it.
  it("offers nothing when the server says this caller may not remove", () => {
    renderChannel(channelDetails({ canRemoveMembers: false }));

    expect(removeButtons()).toHaveLength(0);
  });

  // can_add_members is independent from removal. A caller who may add and may
  // not remove must see the add action and no minus button.
  it("does not read the add capability as the removal one", () => {
    renderChannel(
      channelDetails({
        canAddMembers: true,
        canManageMembers: false,
        canRemoveMembers: false,
      }),
    );

    expect(screen.getByTestId("chat-details-add-members")).toBeInTheDocument();
    expect(removeButtons()).toHaveLength(0);
  });

  it("names the action for pointer and for assistive technology alike", () => {
    renderChannel();

    const button = screen.getByRole("button", { name: "Remover Fernanda Nicácio do canal" });
    expect(button).toHaveAttribute("title", "Remover membro");
    // The glyph is decoration and must not be read out as part of the name.
    expect(button.querySelector(".material-symbols-outlined")).toHaveAttribute(
      "aria-hidden",
      "true",
    );
  });

  it("waits for the complete roster instead of showing an online-only preview", () => {
    renderChannel(channelDetails(), { status: "loading" });

    expect(screen.getByRole("heading", { name: "Membros" })).toBeInTheDocument();
    expect(screen.getByText("Carregando membros…")).toBeInTheDocument();
    expect(screen.queryByText("Bruno Dias")).not.toBeInTheDocument();
  });

  it("does not replace a failed roster with an online-only preview", () => {
    renderChannel(channelDetails(), { status: "error" });

    expect(screen.getByRole("heading", { name: "Membros" })).toBeInTheDocument();
    expect(screen.getByText("Não foi possível carregar os membros.")).toBeInTheDocument();
    expect(screen.queryByText("Bruno Dias")).not.toBeInTheDocument();
  });

  it("says how much of a large membership it is holding", () => {
    renderChannel(channelDetails(), {
      status: "ready",
      data: { memberCount: 42, members: roster.members },
    });

    expect(screen.getByTestId("chat-details-roster-shortfall")).toHaveTextContent(
      "3 de 42 membros carregados.",
    );
  });
});

describe("confirming a channel removal", () => {
  it("removes nothing until the confirmation is accepted", async () => {
    const user = userEvent.setup();
    renderChannel();

    await user.click(screen.getByRole("button", { name: "Remover Fernanda Nicácio do canal" }));

    const dialog = screen.getByRole("dialog", { name: "Remover membro?" });
    expect(dialog).toHaveTextContent("Fernanda Nicácio");
    expect(dialog).toHaveTextContent("Infraestrutura");
    expect(removeChannelMember).not.toHaveBeenCalled();
  });

  it("returns focus to the row's control when cancelled", async () => {
    const user = userEvent.setup();
    renderChannel();
    const trigger = screen.getByRole("button", { name: "Remover Fernanda Nicácio do canal" });

    await user.click(trigger);
    await user.click(screen.getByRole("button", { name: "Cancelar" }));

    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    expect(removeChannelMember).not.toHaveBeenCalled();
    expect(trigger).toHaveFocus();
  });

  it("sends exactly the conversation and the target, then refetches", async () => {
    const user = userEvent.setup();
    const { reload } = renderChannel();

    await user.click(screen.getByRole("button", { name: "Remover Fernanda Nicácio do canal" }));
    await user.click(screen.getByRole("button", { name: "Remover membro" }));

    await waitFor(() => expect(removeChannelMember).toHaveBeenCalledWith("ch-1", "u-2"));
    expect(removeChannelMember).toHaveBeenCalledTimes(1);
    // Reconciliation is a refetch, never a local splice.
    await waitFor(() => expect(reload).toHaveBeenCalledTimes(1));
    expect(removeGroupParticipant).not.toHaveBeenCalled();
  });

  it("closes the dialog, keeps the panel open and announces the removal", async () => {
    const user = userEvent.setup();
    renderChannel();

    await user.click(screen.getByRole("button", { name: "Remover Fernanda Nicácio do canal" }));
    await user.click(screen.getByRole("button", { name: "Remover membro" }));

    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
    expect(screen.getByTestId("chat-conversation-details")).toBeInTheDocument();
    expect(await screen.findByRole("status")).toHaveTextContent(
      "Fernanda Nicácio foi removido do canal.",
    );
  });

  // The row the trigger lived on is about to disappear, so focus must land on
  // something that survives the refetch.
  it("moves focus to a control that outlives the removed row", async () => {
    const user = userEvent.setup();
    renderChannel();

    await user.click(screen.getByRole("button", { name: "Remover Fernanda Nicácio do canal" }));
    await user.click(screen.getByRole("button", { name: "Remover membro" }));

    await waitFor(() => expect(screen.getByTestId("chat-details-add-members")).toHaveFocus());
  });

  it("keeps the dialog open and the list untouched when the server refuses", async () => {
    const user = userEvent.setup();
    const { ApiRequestError } = await import("../lib/api");
    removeChannelMember.mockRejectedValueOnce(new ApiRequestError(403, "forbidden", "forbidden"));
    const { reload } = renderChannel();

    await user.click(screen.getByRole("button", { name: "Remover Fernanda Nicácio do canal" }));
    await user.click(screen.getByRole("button", { name: "Remover membro" }));

    expect(await screen.findByRole("alert")).toHaveTextContent(
      "Você não tem permissão para remover esta pessoa.",
    );
    expect(screen.getByRole("dialog")).toBeInTheDocument();
    // Nothing was removed, so nothing is reconciled and the row is still there
    // with its action intact.
    expect(reload).not.toHaveBeenCalled();
    expect(
      screen.getByRole("button", { name: "Remover Fernanda Nicácio do canal" }),
    ).toBeInTheDocument();
  });
});

// A destructive write is never cancelled because the reader navigated, so it
// can land while the panel is describing another conversation. Everything the
// panel does *after* the write is about a conversation, and none of it may be
// applied to the wrong one.
describe("a removal that finishes after the reader moved on", () => {
  it("leaves the conversation now on screen untouched", async () => {
    const user = userEvent.setup();
    let settle: (() => void) | undefined;
    removeChannelMember.mockImplementation(
      () =>
        new Promise<void>((resolve) => {
          settle = resolve;
        }),
    );
    const reloadA = vi.fn();
    const reloadB = vi.fn();
    const { rerender } = render(
      <ConversationDetailsPanel
        kind="channel"
        state={channelState(channelDetails(), { status: "ready", data: roster }, reloadA)}
        currentUserId={currentUserId}
        latestPin={null}
        onClose={vi.fn()}
      />,
    );

    await user.click(screen.getByRole("button", { name: "Remover Fernanda Nicácio do canal" }));
    await user.click(screen.getByRole("button", { name: "Remover membro" }));
    await waitFor(() => expect(removeChannelMember).toHaveBeenCalledWith("ch-1", "u-2"));

    // The reader switches conversation while the DELETE is still in flight.
    // The panel is deliberately not remounted, which is exactly why the
    // finished write has to know which conversation it belonged to.
    const otherChannel = channelDetails({ id: "ch-2", name: "Outro canal" });
    const otherRoster = {
      memberCount: 1,
      members: [{ userId: "u-9", displayName: "Outra Pessoa", role: "member" as const }],
    };
    rerender(
      <ConversationDetailsPanel
        kind="channel"
        state={channelState(otherChannel, { status: "ready", data: otherRoster }, reloadB)}
        currentUserId={currentUserId}
        latestPin={null}
        onClose={vi.fn()}
      />,
    );
    const focusedBeforeSettling = document.activeElement;

    settle?.();
    await waitFor(() => expect(screen.getByText("Outra Pessoa")).toBeInTheDocument());

    // The write stands and was sent once, against the conversation it was
    // started for.
    expect(removeChannelMember).toHaveBeenCalledTimes(1);
    expect(removeChannelMember).toHaveBeenCalledWith("ch-1", "u-2");
    // Nothing about it reached the conversation now on screen.
    expect(screen.queryByRole("status")).not.toBeInTheDocument();
    expect(screen.queryByText(/Fernanda Nicácio/)).not.toBeInTheDocument();
    expect(reloadB).not.toHaveBeenCalled();
    expect(document.activeElement).toBe(focusedBeforeSettling);
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
  });
});

describe("group removal controls", () => {
  // The group's real policy: adding is open to every participant, removing is
  // the creator's alone, and the panel must not conflate them.
  it("offers nothing to a participant who is not the creator", () => {
    renderGroup();

    expect(screen.getByTestId("chat-details-add-members")).toBeInTheDocument();
    expect(removeButtons()).toHaveLength(0);
  });

  it("offers a removal to the creator, except on their own row", () => {
    renderGroup(groupDetails({ canRemoveMembers: true }));

    expect(removeButtons()).toHaveLength(1);
    expect(
      screen.getByRole("button", { name: "Remover Fernanda Nicácio do grupo" }),
    ).toBeInTheDocument();
  });

  it("posts a group removal to the group endpoint", async () => {
    const user = userEvent.setup();
    const { reload } = renderGroup(groupDetails({ canRemoveMembers: true }));

    await user.click(screen.getByRole("button", { name: "Remover Fernanda Nicácio do grupo" }));
    const dialog = screen.getByRole("dialog", { name: "Remover membro?" });
    expect(dialog).toHaveTextContent("Time de Infra");
    await user.click(within(dialog).getByRole("button", { name: "Remover membro" }));

    await waitFor(() => expect(removeGroupParticipant).toHaveBeenCalledWith("conv-1", "u-2"));
    // A group flow must never reach the channel endpoint.
    expect(removeChannelMember).not.toHaveBeenCalled();
    await waitFor(() => expect(reload).toHaveBeenCalledTimes(1));
  });
});
