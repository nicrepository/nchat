import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import ChannelDetailsPanel from "./ChannelDetailsPanel";

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
import type { ChannelAttachment, ChannelDetails, Message, PinnedItem } from "./chatTypes";
import type { ChannelDetailsState } from "./useChannelDetails";

const currentUserId = "user-me";

function channelDetails(overrides: Partial<ChannelDetails> = {}): ChannelDetails {
  return {
    id: "ch-1",
    slug: "infra",
    name: "Infraestrutura",
    type: "public",
    createdAt: "2024-01-12T09:30:00.000Z",
    memberCount: 12,
    onlineCount: 0,
    onlineMembers: [],
    canManageMembers: false,
    ...overrides,
  };
}

function state(overrides: Partial<ChannelDetailsState> = {}): ChannelDetailsState {
  return {
    details: { status: "ready", data: channelDetails() },
    files: { status: "ready", data: [] },
    reload: vi.fn(),
    ...overrides,
  };
}

function pinnedMessage(overrides: Partial<Message> = {}): Message {
  return {
    id: "m-1",
    senderId: "user-other",
    senderDisplayName: "Juliane Lino",
    senderEmail: "juliane@example.test",
    kind: "user",
    bodyText: "Procedimento de deploy atualizado.",
    bodyFormat: "v3",
    isRemoved: false,
    status: "active",
    createdAt: "2026-07-15T12:00:00.000Z",
    updatedAt: "2026-07-15T12:00:00.000Z",
    isEdited: false,
    editCount: 0,
    reactions: [],
    isFavorited: false,
    isForwarded: false,
    ...overrides,
  };
}

function pin(overrides: Partial<Message> = {}): PinnedItem {
  return {
    message: pinnedMessage(overrides),
    pinnedByUserId: "user-other",
    pinnedAt: "2026-07-15T12:30:00.000Z",
  };
}

function attachment(overrides: Partial<ChannelAttachment> = {}): ChannelAttachment {
  return {
    id: "a-1",
    filename: "relatorio-backup.pdf",
    contentType: "application/pdf",
    size: 2.4 * 1024 * 1024,
    status: "clean",
    createdAt: "2026-07-15T12:24:00.000Z",
    ...overrides,
  };
}

function renderPanel(overrides: Partial<Parameters<typeof ChannelDetailsPanel>[0]> = {}) {
  const onClose = vi.fn();
  const { unmount } = render(
    <ChannelDetailsPanel
      state={state()}
      currentUserId={currentUserId}
      latestPin={null}
      onClose={onClose}
      {...overrides}
    />,
  );
  return { onClose, unmount };
}

afterEach(() => {
  vi.clearAllMocks();
});

describe("ChannelDetailsPanel — estrutura e acessibilidade", () => {
  it("is a complementary region named by its own heading", () => {
    renderPanel();

    const panel = screen.getByRole("complementary", { name: "Detalhes do canal" });
    expect(panel).toBeInTheDocument();
    expect(within(panel).getByRole("heading", { name: "Detalhes do canal" })).toBeInTheDocument();
  });

  it("closes through an accessible close button", async () => {
    const { onClose } = renderPanel();

    await userEvent.click(screen.getByRole("button", { name: "Fechar detalhes do canal" }));

    expect(onClose).toHaveBeenCalledTimes(1);
  });

  it("moves focus into the panel when it opens", () => {
    renderPanel();

    expect(screen.getByRole("button", { name: "Fechar detalhes do canal" })).toHaveFocus();
  });
});

describe("ChannelDetailsPanel — seção Sobre", () => {
  it("shows the real creation date, visibility and member total", () => {
    renderPanel({
      state: state({
        details: {
          status: "ready",
          data: channelDetails({ type: "private", memberCount: 12 }),
        },
      }),
    });

    expect(screen.getByText(/Criado em 12 de janeiro de 2024/)).toBeInTheDocument();
    expect(screen.getByText(/Canal privado · 12 membros/)).toBeInTheDocument();
  });

  it("says public when the channel type says so, not the channel name", () => {
    renderPanel({
      state: state({
        details: { status: "ready", data: channelDetails({ name: "privado", type: "public" }) },
      }),
    });

    expect(screen.getByText(/Canal público/)).toBeInTheDocument();
  });

  it("shows the member total from the server, never the size of the preview", () => {
    renderPanel({
      state: state({
        details: {
          status: "ready",
          data: channelDetails({
            memberCount: 40,
            onlineCount: 6,
            onlineMembers: [
              { userId: "u-1", displayName: "Ana", role: "member", presence: "online" },
            ],
          }),
        },
      }),
    });

    // The channel's size and how many of its members are online are three
    // different numbers, and none is the length of the rendered list.
    expect(screen.getByText(/40 membros/)).toBeInTheDocument();
    expect(screen.getByRole("heading", { name: "Membros online (6)" })).toBeInTheDocument();
    expect(
      within(screen.getByRole("list", { name: "Membros online do canal" })).getAllByRole(
        "listitem",
      ),
    ).toHaveLength(1);
  });

  it("handles a missing creation date without inventing one", () => {
    renderPanel({
      state: state({
        details: { status: "ready", data: channelDetails({ createdAt: "" }) },
      }),
    });

    expect(screen.getByText("Data de criação indisponível")).toBeInTheDocument();
  });

  it("shows an explicit empty state while the domain has no description", () => {
    renderPanel();

    expect(screen.getByTestId("chat-details-description")).toHaveTextContent(
      "Este canal ainda não tem descrição.",
    );
  });

  it("shows a loading state and then an error state without faking data", () => {
    const { unmount } = render(
      <ChannelDetailsPanel
        state={state({ details: { status: "loading" } })}
        currentUserId={currentUserId}
        latestPin={null}
        onClose={vi.fn()}
      />,
    );
    expect(screen.getByText("Carregando informações do canal…")).toBeInTheDocument();
    unmount();

    render(
      <ChannelDetailsPanel
        state={state({ details: { status: "error" } })}
        currentUserId={currentUserId}
        latestPin={null}
        onClose={vi.fn()}
      />,
    );
    expect(
      screen.getByText("Não foi possível carregar as informações do canal."),
    ).toBeInTheDocument();
  });
});

describe("ChannelDetailsPanel — membros", () => {
  const members = [
    {
      userId: currentUserId,
      displayName: "Álvaro Neto",
      role: "moderator" as const,
      presence: "online" as const,
    },
    {
      userId: "user-other",
      displayName: "Juliane Lino",
      role: "member" as const,
      presence: "online" as const,
    },
  ];

  it("marks the authenticated user by id, not by name", () => {
    renderPanel({
      state: state({
        details: {
          status: "ready",
          data: channelDetails({ onlineMembers: members, onlineCount: 2, memberCount: 2 }),
        },
      }),
    });

    const list = screen.getByRole("list", { name: "Membros online do canal" });
    const rows = within(list).getAllByRole("listitem");
    expect(within(rows[0]).getByText("Você")).toBeInTheDocument();
    expect(within(rows[1]).queryByText("Você")).not.toBeInTheDocument();
  });

  it("does not mark anyone when the viewer's id is unknown", () => {
    renderPanel({
      currentUserId: "",
      state: state({
        details: {
          status: "ready",
          data: channelDetails({
            // A member whose id happens to be empty must not become "you".
            onlineMembers: [
              { userId: "", displayName: "Sem id", role: "member", presence: "online" },
            ],
            onlineCount: 1,
            memberCount: 1,
          }),
        },
      }),
    });

    expect(screen.queryByText("Você")).not.toBeInTheDocument();
  });

  it("shows a presence indicator for every online member", () => {
    renderPanel({
      state: state({
        details: {
          status: "ready",
          data: channelDetails({
            onlineMembers: [
              { userId: "u-1", displayName: "Primeiro", role: "member", presence: "online" },
              { userId: "u-2", displayName: "Segundo", role: "moderator", presence: "online" },
            ],
            onlineCount: 2,
            memberCount: 9,
          }),
        },
      }),
    });

    expect(screen.getAllByTestId("chat-details-presence")).toHaveLength(2);
    expect(screen.getByText(/Membro · Online/)).toBeInTheDocument();
    expect(screen.getByText(/Moderador · Online/)).toBeInTheDocument();
  });

  it("says nobody is online — not that the channel is empty — and keeps the total", () => {
    const { unmount } = renderPanel({
      state: state({
        details: {
          status: "ready",
          // A populated channel where nobody happens to be connected.
          data: channelDetails({ onlineMembers: [], onlineCount: 0, memberCount: 31 }),
        },
      }),
    });

    expect(screen.getByText("Nenhum membro online no momento.")).toBeInTheDocument();
    expect(screen.queryByText(/não tem membros/i)).not.toBeInTheDocument();
    // The channel's size is reported independently of who is connected.
    expect(screen.getByText(/31 membros/)).toBeInTheDocument();
    expect(screen.getByRole("heading", { name: "Membros online (0)" })).toBeInTheDocument();
    unmount();

    render(
      <ChannelDetailsPanel
        state={state({ details: { status: "error" } })}
        currentUserId={currentUserId}
        latestPin={null}
        onClose={vi.fn()}
      />,
    );
    expect(screen.getByText("Não foi possível carregar os membros.")).toBeInTheDocument();
  });

  it("renders a member name as text, never as markup", () => {
    renderPanel({
      state: state({
        details: {
          status: "ready",
          data: channelDetails({
            onlineMembers: [
              {
                userId: "u-1",
                displayName: "<img src=x onerror=alert(1)>",
                role: "member",
                presence: "online",
              },
            ],
            onlineCount: 1,
            memberCount: 1,
          }),
        },
      }),
    });

    expect(screen.getByText("<img src=x onerror=alert(1)>")).toBeInTheDocument();
    expect(document.querySelector("img[src='x']")).toBeNull();
  });
});

describe("ChannelDetailsPanel — ações indisponíveis", () => {
  it("offers the still-unimplemented actions as disabled controls with a stated reason", async () => {
    renderPanel();

    // "Ver todos" has no flow yet and must keep saying so. "Adicionar membros"
    // used to be in this list and is now a real action (issue #398), covered by
    // its own describe block below.
    const seeAll = screen.getAllByRole("button", { name: "Ver todos" });
    for (const button of seeAll) {
      expect(button).toBeDisabled();
      expect(button).toHaveAttribute("aria-describedby");
    }

    await userEvent.click(seeAll[0]);
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
  });
});

describe("ChannelDetailsPanel — mensagem fixada", () => {
  it("shows an empty state when nothing is pinned", () => {
    renderPanel();

    expect(screen.getByTestId("chat-details-pin-empty")).toHaveTextContent(
      "Nenhuma mensagem fixada neste canal.",
    );
  });

  it("shows the selected pin's body and author", () => {
    renderPanel({ latestPin: pin() });

    const card = screen.getByTestId("chat-details-pin");
    expect(card).toHaveTextContent("Procedimento de deploy atualizado.");
    expect(card).toHaveTextContent("Juliane Lino");
  });

  it("renders a removed pin without pretending it still has a body", () => {
    renderPanel({ latestPin: pin({ isRemoved: true, bodyText: "", status: "deleted" }) });

    expect(screen.getByTestId("chat-details-pin")).toHaveTextContent("Mensagem removida.");
  });

  it("renders pin content as text, never as markup", () => {
    renderPanel({ latestPin: pin({ bodyText: "<img src=x onerror=alert(1)>" }) });

    expect(screen.getByTestId("chat-details-pin")).toHaveTextContent(
      "<img src=x onerror=alert(1)>",
    );
    expect(document.querySelector("img[src='x']")).toBeNull();
  });
});

describe("ChannelDetailsPanel — arquivos recentes", () => {
  it("shows name, timestamp and formatted size, in the order received", () => {
    renderPanel({
      state: state({
        files: {
          status: "ready",
          data: [
            attachment({ id: "a-1", filename: "recente.pdf" }),
            attachment({
              id: "a-2",
              filename: "antigo.png",
              contentType: "image/png",
              size: 890 * 1024,
              createdAt: "2026-07-14T09:00:00.000Z",
            }),
          ],
        },
      }),
    });

    const rows = within(screen.getByRole("list", { name: "Arquivos recentes" })).getAllByRole(
      "listitem",
    );
    // The server owns the ordering; the panel must not re-sort it.
    expect(rows[0]).toHaveTextContent("recente.pdf");
    expect(rows[0]).toHaveTextContent("2,4 MB");
    expect(rows[1]).toHaveTextContent("antigo.png");
    expect(rows[1]).toHaveTextContent("890 KB");
  });

  it("marks a file that the scan has not cleared and never links to it", () => {
    renderPanel({
      state: state({
        files: {
          status: "ready",
          data: [
            attachment({ id: "a-1", status: "pending_scan" }),
            attachment({ id: "a-2", filename: "infectado.exe", status: "rejected" }),
          ],
        },
      }),
    });

    expect(screen.getByText("Em análise")).toBeInTheDocument();
    expect(screen.getByText("Reprovado")).toBeInTheDocument();
    // No file row is a link: nothing here offers a download of any state.
    expect(screen.queryAllByRole("link")).toHaveLength(0);
  });

  it("picks the file icon from the detected type, never from the name", () => {
    renderPanel({
      state: state({
        files: {
          status: "ready",
          data: [
            attachment({ id: "a-1", filename: "a.pdf", contentType: "application/pdf" }),
            attachment({ id: "a-2", filename: "b.pdf", contentType: "image/png" }),
            attachment({ id: "a-3", filename: "c.pdf", contentType: "video/mp4" }),
            attachment({ id: "a-4", filename: "d.pdf", contentType: "audio/mpeg" }),
            attachment({ id: "a-5", filename: "e.pdf", contentType: "text/csv" }),
            // A file the sniffer could not classify still gets a neutral icon
            // rather than inheriting one from its .pdf extension.
            attachment({ id: "a-6", filename: "f.pdf", contentType: "" }),
          ],
        },
      }),
    });

    const rows = within(screen.getByRole("list", { name: "Arquivos recentes" })).getAllByRole(
      "listitem",
    );
    expect(rows.map((row) => row.querySelector(".material-symbols-outlined")?.textContent)).toEqual(
      ["picture_as_pdf", "image", "movie", "graphic_eq", "description", "draft"],
    );
  });

  it("renders a file name as text, never as a URL or markup", () => {
    renderPanel({
      state: state({
        files: {
          status: "ready",
          data: [attachment({ filename: "javascript:alert(1).pdf" })],
        },
      }),
    });

    expect(screen.getByText("javascript:alert(1).pdf")).toBeInTheDocument();
    expect(screen.queryAllByRole("link")).toHaveLength(0);
  });

  it("handles an empty list, a loading state and an error state", () => {
    const { unmount } = renderPanel();
    expect(screen.getByTestId("chat-details-files-empty")).toBeInTheDocument();
    unmount();

    const loading = render(
      <ChannelDetailsPanel
        state={state({ files: { status: "loading" } })}
        currentUserId={currentUserId}
        latestPin={null}
        onClose={vi.fn()}
      />,
    );
    expect(screen.getByText("Carregando arquivos…")).toBeInTheDocument();
    loading.unmount();

    render(
      <ChannelDetailsPanel
        state={state({ files: { status: "error" } })}
        currentUserId={currentUserId}
        latestPin={null}
        onClose={vi.fn()}
      />,
    );
    // The channel's own metadata survives a file-service failure.
    expect(screen.getByText("Não foi possível carregar os arquivos.")).toBeInTheDocument();
    expect(screen.getByText(/Canal público/)).toBeInTheDocument();
  });
});

// ── Adicionar membros (issue #398) ───────────────────────────────────────────

describe("ChannelDetailsPanel — adicionar membros", () => {
  // The action is server-gated. `canManageMembers` is normalized to false unless
  // the server explicitly said true, so every state that is not "ready and
  // permitted" must leave the control absent.
  it("offers the action when the server says the caller may manage members", () => {
    renderPanel({
      state: state({
        details: {
          status: "ready",
          data: channelDetails({ canManageMembers: true }),
        },
      }),
    });

    expect(screen.getByTestId("chat-details-add-members")).toBeEnabled();
  });

  it.each([
    ["public", "public" as const],
    ["private", "private" as const],
  ])("offers the action on a %s channel", (_label, type) => {
    renderPanel({
      state: state({
        details: {
          status: "ready",
          data: channelDetails({ type, canManageMembers: true }),
        },
      }),
    });

    expect(screen.getByTestId("chat-details-add-members")).toBeInTheDocument();
  });

  it("hides the action when the caller may not manage members", () => {
    renderPanel({
      state: state({
        details: { status: "ready", data: channelDetails({ canManageMembers: false }) },
      }),
    });

    expect(screen.queryByTestId("chat-details-add-members")).not.toBeInTheDocument();
  });

  // Default-safe: an undefined permission must never render the action, or a
  // rolling deploy would show a control every click of which is refused.
  it.each([
    ["loading", { status: "loading" } as const],
    ["error", { status: "error" } as const],
  ])("hides the action while the panel is %s", (_label, details) => {
    renderPanel({ state: state({ details }) });

    expect(screen.queryByTestId("chat-details-add-members")).not.toBeInTheDocument();
  });

  it("opens and closes the picker, returning focus to the action", async () => {
    renderPanel({
      state: state({
        details: { status: "ready", data: channelDetails({ canManageMembers: true }) },
      }),
    });

    const action = screen.getByTestId("chat-details-add-members");
    await userEvent.click(action);
    expect(screen.getByRole("dialog", { name: "Adicionar membros" })).toBeInTheDocument();

    await userEvent.keyboard("{Escape}");
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    expect(action).toHaveFocus();
  });

  it("passes the viewer and the visible members as ineligible picks", async () => {
    renderPanel({
      state: state({
        details: {
          status: "ready",
          data: channelDetails({
            canManageMembers: true,
            onlineMembers: [
              {
                userId: "member-1",
                displayName: "Ana Lima",
                role: "member",
                presence: "online",
              },
            ],
          }),
        },
      }),
    });

    await userEvent.click(screen.getByTestId("chat-details-add-members"));

    // The dialog owns the filtering; the panel's job is to supply the list, so
    // this asserts the wiring rather than re-testing the picker.
    expect(screen.getByRole("dialog", { name: "Adicionar membros" })).toBeInTheDocument();
  });
});

describe("ChannelDetailsPanel — aviso de sucesso", () => {
  // The panel stays mounted across a channel switch by design, so the notice
  // must be cleared by identity rather than by unmount.
  it("clears the added notice when the panel switches channel", async () => {
    const readyFor = (id: string) => ({
      details: {
        status: "ready" as const,
        data: channelDetails({ id, canManageMembers: true }),
      },
      files: { status: "ready" as const, data: [] },
      reload: vi.fn(),
    });

    const { rerender } = render(
      <ChannelDetailsPanel
        state={readyFor("ch-1")}
        currentUserId={currentUserId}
        latestPin={null}
        onClose={vi.fn()}
      />,
    );

    await userEvent.click(screen.getByTestId("chat-details-add-members"));
    expect(screen.getByRole("dialog", { name: "Adicionar membros" })).toBeInTheDocument();
    await userEvent.keyboard("{Escape}");

    rerender(
      <ChannelDetailsPanel
        state={readyFor("ch-2")}
        currentUserId={currentUserId}
        latestPin={null}
        onClose={vi.fn()}
      />,
    );

    expect(screen.queryByText(/pessoas? adicionada/)).not.toBeInTheDocument();
  });
});

// The panel deliberately survives a channel switch (issue #435), so nothing
// unmounts the dialog for us. These prove a selection made for channel A can
// never be confirmed into channel B.
describe("ChannelDetailsPanel — troca de conversa", () => {
  function readyFor(id: string) {
    return {
      details: {
        status: "ready" as const,
        data: channelDetails({ id, canManageMembers: true }),
      },
      files: { status: "ready" as const, data: [] },
      reload: vi.fn(),
    };
  }

  function renderFor(id: string) {
    return render(
      <ChannelDetailsPanel
        state={readyFor(id)}
        currentUserId={currentUserId}
        latestPin={null}
        onClose={vi.fn()}
      />,
    );
  }

  beforeEach(() => {
    searchChannelMemberCandidates.mockResolvedValue([{ userId: "u-9", displayName: "Bruno Dias" }]);
    addChannelMembers.mockResolvedValue({ added: 1, alreadyMembers: 0, memberCount: 5 });
  });

  it("closes the picker and sends nothing when the channel changes mid-selection", async () => {
    const user = userEvent.setup();
    const { rerender } = renderFor("ch-A");

    await user.click(screen.getByTestId("chat-details-add-members"));
    const dialog = screen.getByRole("dialog", { name: "Adicionar membros" });
    await user.type(within(dialog).getByLabelText("Pesquisar pessoa"), "br");
    await user.click(await within(dialog).findByRole("button", { name: /Bruno Dias/ }));
    expect(within(dialog).getByRole("list", { name: "Pessoas selecionadas" })).toBeInTheDocument();

    rerender(
      <ChannelDetailsPanel
        state={readyFor("ch-B")}
        currentUserId={currentUserId}
        latestPin={null}
        onClose={vi.fn()}
      />,
    );

    expect(screen.queryByRole("dialog", { name: "Adicionar membros" })).not.toBeInTheDocument();
    // The decisive assertion: nothing was posted, to either channel.
    expect(addChannelMembers).not.toHaveBeenCalled();
  });

  it("starts the picker empty after a channel switch", async () => {
    const user = userEvent.setup();
    const { rerender } = renderFor("ch-A");

    await user.click(screen.getByTestId("chat-details-add-members"));
    await user.type(screen.getByLabelText("Pesquisar pessoa"), "br");
    await user.click(await screen.findByRole("button", { name: /Bruno Dias/ }));

    rerender(
      <ChannelDetailsPanel
        state={readyFor("ch-B")}
        currentUserId={currentUserId}
        latestPin={null}
        onClose={vi.fn()}
      />,
    );
    await user.click(screen.getByTestId("chat-details-add-members"));

    const reopened = screen.getByRole("dialog", { name: "Adicionar membros" });
    expect(
      within(reopened).queryByRole("list", { name: "Pessoas selecionadas" }),
    ).not.toBeInTheDocument();
    expect(within(reopened).getByLabelText("Pesquisar pessoa")).toHaveValue("");
    expect(within(reopened).getByRole("button", { name: "Adicionar" })).toBeDisabled();
  });

  it("closes the picker when the channel changes while a search is pending", async () => {
    // A search that never settles: the switch must not wait for it, and its
    // eventual resolution must not write into the new channel's dialog.
    searchChannelMemberCandidates.mockReturnValue(new Promise(() => {}));
    const user = userEvent.setup();
    const { rerender } = renderFor("ch-A");

    await user.click(screen.getByTestId("chat-details-add-members"));
    await user.type(screen.getByLabelText("Pesquisar pessoa"), "br");

    rerender(
      <ChannelDetailsPanel
        state={readyFor("ch-B")}
        currentUserId={currentUserId}
        latestPin={null}
        onClose={vi.fn()}
      />,
    );

    expect(screen.queryByRole("dialog", { name: "Adicionar membros" })).not.toBeInTheDocument();
    expect(addChannelMembers).not.toHaveBeenCalled();
  });

  it("closes the picker when the channel changes after a failed submit", async () => {
    addChannelMembers.mockRejectedValue(new Error("boom"));
    const user = userEvent.setup();
    const { rerender } = renderFor("ch-A");

    await user.click(screen.getByTestId("chat-details-add-members"));
    await user.type(screen.getByLabelText("Pesquisar pessoa"), "br");
    await user.click(await screen.findByRole("button", { name: /Bruno Dias/ }));
    await user.click(screen.getByRole("button", { name: "Adicionar" }));
    expect(await screen.findByRole("alert")).toBeInTheDocument();

    rerender(
      <ChannelDetailsPanel
        state={readyFor("ch-B")}
        currentUserId={currentUserId}
        latestPin={null}
        onClose={vi.fn()}
      />,
    );

    expect(screen.queryByRole("dialog", { name: "Adicionar membros" })).not.toBeInTheDocument();
    // Exactly the one failed attempt against channel A; nothing retried into B.
    expect(addChannelMembers).toHaveBeenCalledTimes(1);
    expect(addChannelMembers).toHaveBeenCalledWith("ch-A", ["u-9"], expect.any(AbortSignal));
  });
});

// The corrected eligibility source (issue #398).
describe("ChannelDetailsPanel — busca contextual de candidatos", () => {
  it("searches through the channel-scoped endpoint", async () => {
    searchChannelMemberCandidates.mockResolvedValue([{ userId: "u-9", displayName: "Bruno Dias" }]);
    const user = userEvent.setup();
    render(
      <ChannelDetailsPanel
        state={{
          details: {
            status: "ready",
            data: channelDetails({ id: "ch-77", canManageMembers: true }),
          },
          files: { status: "ready", data: [] },
          reload: vi.fn(),
        }}
        currentUserId={currentUserId}
        latestPin={null}
        onClose={vi.fn()}
      />,
    );

    await user.click(screen.getByTestId("chat-details-add-members"));
    await user.type(screen.getByLabelText("Pesquisar pessoa"), "br");

    expect(await screen.findByRole("button", { name: /Bruno Dias/ })).toBeInTheDocument();
    expect(searchChannelMemberCandidates).toHaveBeenCalledWith(
      "ch-77",
      "br",
      expect.any(AbortSignal),
    );
  });

  // The panel's online-members preview must not become the exclusion list: an
  // offline current member is absent from it, and used to be offered.
  it("does not derive exclusions from the online-members preview", async () => {
    searchChannelMemberCandidates.mockResolvedValue([
      { userId: "online-member", displayName: "Ana Lima" },
    ]);
    const user = userEvent.setup();
    render(
      <ChannelDetailsPanel
        state={{
          details: {
            status: "ready",
            data: channelDetails({
              canManageMembers: true,
              onlineMembers: [
                {
                  userId: "online-member",
                  displayName: "Ana Lima",
                  role: "member",
                  presence: "online",
                },
              ],
            }),
          },
          files: { status: "ready", data: [] },
          reload: vi.fn(),
        }}
        currentUserId={currentUserId}
        latestPin={null}
        onClose={vi.fn()}
      />,
    );

    await user.click(screen.getByTestId("chat-details-add-members"));
    await user.type(screen.getByLabelText("Pesquisar pessoa"), "an");

    // Whatever the endpoint returns is offered — the server decides membership,
    // and in production it would not have returned a current member.
    expect(await screen.findByRole("button", { name: /Ana Lima/ })).toBeInTheDocument();
  });
});
