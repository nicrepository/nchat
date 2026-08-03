import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import GroupDetailsPanel from "./GroupDetailsPanel";
import type { GroupDetails, GroupParticipant } from "./chatTypes";
import type { GroupDetailsState } from "./useGroupDetails";

const {
  searchChannelMemberCandidates,
  searchGroupParticipantCandidates,
  addChannelMembers,
  addGroupParticipants,
} = vi.hoisted(() => ({
  searchChannelMemberCandidates: vi.fn(),
  searchGroupParticipantCandidates: vi.fn(),
  addChannelMembers: vi.fn(),
  addGroupParticipants: vi.fn(),
}));

vi.mock("./chatApi", () => ({
  searchChannelMemberCandidates,
  searchGroupParticipantCandidates,
  addChannelMembers,
  addGroupParticipants,
}));

const currentUserId = "user-me";

function participant(overrides: Partial<GroupParticipant> = {}): GroupParticipant {
  return { userId: "p-1", displayName: "Ana Lima", presence: "online", ...overrides };
}

function groupDetails(overrides: Partial<GroupDetails> = {}): GroupDetails {
  return {
    id: "dm-1",
    name: "Time de Infra",
    createdAt: "2024-03-04T15:00:00.000Z",
    participantCount: 4,
    participants: [participant()],
    canManageMembers: false,
    ...overrides,
  };
}

function state(overrides: Partial<GroupDetailsState> = {}): GroupDetailsState {
  return {
    details: { status: "ready", data: groupDetails() },
    reload: vi.fn(),
    ...overrides,
  };
}

function renderPanel(overrides: Partial<Parameters<typeof GroupDetailsPanel>[0]> = {}) {
  const onClose = vi.fn();
  const view = render(
    <GroupDetailsPanel
      state={state()}
      currentUserId={currentUserId}
      onClose={onClose}
      {...overrides}
    />,
  );
  return { onClose, ...view };
}

beforeEach(() => {
  searchGroupParticipantCandidates.mockResolvedValue([
    { userId: "u-9", displayName: "Bruno Dias" },
  ]);
  addGroupParticipants.mockResolvedValue({ added: 1, alreadyMembers: 0, memberCount: 5 });
});

afterEach(() => vi.clearAllMocks());

describe("GroupDetailsPanel — conteúdo", () => {
  it("shows the creation date and the server's participant total", () => {
    renderPanel({
      state: state({
        details: {
          status: "ready",
          data: groupDetails({ participantCount: 12, participants: [participant()] }),
        },
      }),
    });

    // 12, not 1: the list is a capped preview and its length is not the count.
    expect(screen.getByText("12 participantes")).toBeInTheDocument();
    expect(screen.getByText(/Criado em/)).toBeInTheDocument();
  });

  // A group has no public/private column in the domain, so the panel must not
  // display one.
  it("shows no visibility, slug or description", () => {
    renderPanel();

    expect(screen.queryByText(/privado|público|publico/i)).not.toBeInTheDocument();
  });

  it("marks the viewer in the participant list", () => {
    renderPanel({
      state: state({
        details: {
          status: "ready",
          data: groupDetails({
            participants: [
              participant({ userId: currentUserId, displayName: "Eu Mesmo" }),
              participant({ userId: "p-2", displayName: "Ana Lima" }),
            ],
          }),
        },
      }),
    });

    const list = screen.getByRole("list", { name: "Participantes do grupo" });
    const rows = within(list).getAllByRole("listitem");
    expect(within(rows[0]).getByText("Você")).toBeInTheDocument();
    expect(within(rows[1]).queryByText("Você")).not.toBeInTheDocument();
  });

  // A group has no role to show; the row must not invent "Membro".
  it("shows no role for participants", () => {
    renderPanel();

    expect(screen.queryByText("Membro")).not.toBeInTheDocument();
    expect(screen.queryByText("Moderador")).not.toBeInTheDocument();
  });

  it.each([
    ["loading", { status: "loading" } as const, "Carregando participantes…"],
    ["error", { status: "error" } as const, "Não foi possível carregar os participantes."],
  ])("renders the %s state", (_label, details, text) => {
    renderPanel({ state: state({ details }) });

    expect(screen.getByText(text)).toBeInTheDocument();
  });
});

describe("GroupDetailsPanel — adicionar membros", () => {
  it("offers the action when the server permits it", () => {
    renderPanel({
      state: state({
        details: { status: "ready", data: groupDetails({ canManageMembers: true }) },
      }),
    });

    expect(screen.getByTestId("chat-group-add-members")).toBeEnabled();
  });

  it("hides the action when the server withholds the permission", () => {
    renderPanel();

    expect(screen.queryByTestId("chat-group-add-members")).not.toBeInTheDocument();
  });

  it.each([
    ["loading", { status: "loading" } as const],
    ["error", { status: "error" } as const],
  ])("hides the action while %s", (_label, details) => {
    renderPanel({ state: state({ details }) });

    expect(screen.queryByTestId("chat-group-add-members")).not.toBeInTheDocument();
  });

  it("opens the picker for this group and sends the conversation ID", async () => {
    const user = userEvent.setup();
    renderPanel({
      state: state({
        details: {
          status: "ready",
          data: groupDetails({ id: "dm-42", canManageMembers: true }),
        },
      }),
    });

    await user.click(screen.getByTestId("chat-group-add-members"));
    const dialog = screen.getByRole("dialog", { name: "Adicionar membros" });
    await user.type(within(dialog).getByLabelText("Pesquisar pessoa"), "br");
    await user.click(await within(dialog).findByRole("button", { name: /Bruno Dias/ }));
    await user.click(within(dialog).getByRole("button", { name: "Adicionar" }));

    await waitFor(() =>
      expect(addGroupParticipants).toHaveBeenCalledWith("dm-42", ["u-9"], expect.any(AbortSignal)),
    );
    // The group flow must never reach the channel endpoint.
    expect(addChannelMembers).not.toHaveBeenCalled();
  });

  it("refetches after a successful add instead of merging the response", async () => {
    const reload = vi.fn();
    const user = userEvent.setup();
    renderPanel({
      state: state({
        details: { status: "ready", data: groupDetails({ canManageMembers: true }) },
        reload,
      }),
    });

    await user.click(screen.getByTestId("chat-group-add-members"));
    const dialog = screen.getByRole("dialog", { name: "Adicionar membros" });
    await user.type(within(dialog).getByLabelText("Pesquisar pessoa"), "br");
    await user.click(await within(dialog).findByRole("button", { name: /Bruno Dias/ }));
    await user.click(within(dialog).getByRole("button", { name: "Adicionar" }));

    await waitFor(() => expect(reload).toHaveBeenCalled());
    expect(await screen.findByText("1 pessoa adicionada ao grupo.")).toBeInTheDocument();
  });

  // The viewer is filtered locally because the client already knows who they
  // are. Current participants are NOT filtered here: the search endpoint
  // excludes them in SQL, because this panel only ever holds a capped preview
  // and using it made participants past the 30th appear as selectable.
  it("filters the viewer locally and leaves membership exclusion to the server", async () => {
    searchGroupParticipantCandidates.mockResolvedValue([
      { userId: currentUserId, displayName: "Eu Mesmo" },
      { userId: "u-9", displayName: "Bruno Dias" },
    ]);
    const user = userEvent.setup();
    renderPanel({
      state: state({
        details: {
          status: "ready",
          data: groupDetails({ id: "dm-77", canManageMembers: true }),
        },
      }),
    });

    await user.click(screen.getByTestId("chat-group-add-members"));
    const dialog = screen.getByRole("dialog", { name: "Adicionar membros" });
    await user.type(within(dialog).getByLabelText("Pesquisar pessoa"), "an");

    expect(await within(dialog).findByRole("button", { name: /Bruno Dias/ })).toBeInTheDocument();
    expect(within(dialog).queryByRole("button", { name: /Eu Mesmo/ })).not.toBeInTheDocument();
    // Group-scoped route, with the conversation as context.
    expect(searchGroupParticipantCandidates).toHaveBeenCalledWith(
      "dm-77",
      "an",
      expect.any(AbortSignal),
    );
  });

  // The panel must not hand its preview to the picker as an exclusion list —
  // that list is incomplete by construction.
  it("does not derive exclusions from the participant preview", async () => {
    searchGroupParticipantCandidates.mockResolvedValue([
      { userId: "p-1", displayName: "Ana Lima" },
    ]);
    const user = userEvent.setup();
    renderPanel({
      state: state({
        details: {
          status: "ready",
          // "p-1" is in the preview; the client must not treat that as the rule.
          data: groupDetails({ canManageMembers: true, participants: [participant()] }),
        },
      }),
    });

    await user.click(screen.getByTestId("chat-group-add-members"));
    const dialog = screen.getByRole("dialog", { name: "Adicionar membros" });
    await user.type(within(dialog).getByLabelText("Pesquisar pessoa"), "an");

    // Whatever the endpoint returns is offered: the server is the authority on
    // membership, and in production it would not have returned a participant.
    expect(await within(dialog).findByRole("button", { name: /Ana Lima/ })).toBeInTheDocument();
  });

  it("returns focus to the action when the picker closes", async () => {
    const user = userEvent.setup();
    renderPanel({
      state: state({
        details: { status: "ready", data: groupDetails({ canManageMembers: true }) },
      }),
    });

    const action = screen.getByTestId("chat-group-add-members");
    await user.click(action);
    expect(screen.getByRole("dialog", { name: "Adicionar membros" })).toBeInTheDocument();

    await user.keyboard("{Escape}");

    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    expect(action).toHaveFocus();
  });
});

describe("GroupDetailsPanel — acessibilidade", () => {
  it("is a labelled complementary region with a close control", async () => {
    const user = userEvent.setup();
    const { onClose } = renderPanel();

    const panel = screen.getByTestId("chat-group-details");
    expect(panel).toHaveAccessibleName("Detalhes do grupo");

    const close = screen.getByRole("button", { name: "Fechar detalhes do grupo" });
    expect(close).toHaveFocus();
    await user.click(close);
    expect(onClose).toHaveBeenCalledTimes(1);
  });
});

// The panel is not remounted when the conversation changes, so a selection made
// for group A must not survive into group B — see ChannelDetailsPanel's twin.
describe("GroupDetailsPanel — troca de conversa", () => {
  it("closes the picker and drops the selection when the group changes", async () => {
    const user = userEvent.setup();
    const { rerender } = render(
      <GroupDetailsPanel
        state={state({
          details: {
            status: "ready",
            data: groupDetails({ id: "dm-A", canManageMembers: true }),
          },
        })}
        currentUserId={currentUserId}
        onClose={vi.fn()}
      />,
    );

    await user.click(screen.getByTestId("chat-group-add-members"));
    const dialog = screen.getByRole("dialog", { name: "Adicionar membros" });
    await user.type(within(dialog).getByLabelText("Pesquisar pessoa"), "br");
    await user.click(await within(dialog).findByRole("button", { name: /Bruno Dias/ }));

    rerender(
      <GroupDetailsPanel
        state={state({
          details: {
            status: "ready",
            data: groupDetails({ id: "dm-B", canManageMembers: true }),
          },
        })}
        currentUserId={currentUserId}
        onClose={vi.fn()}
      />,
    );

    expect(screen.queryByRole("dialog", { name: "Adicionar membros" })).not.toBeInTheDocument();
    expect(addGroupParticipants).not.toHaveBeenCalled();

    // Reopening in group B starts empty — nothing carried over from A.
    await user.click(screen.getByTestId("chat-group-add-members"));
    const reopened = screen.getByRole("dialog", { name: "Adicionar membros" });
    expect(
      within(reopened).queryByRole("list", { name: "Pessoas selecionadas" }),
    ).not.toBeInTheDocument();
    expect(within(reopened).getByRole("button", { name: "Adicionar" })).toBeDisabled();
  });
});
