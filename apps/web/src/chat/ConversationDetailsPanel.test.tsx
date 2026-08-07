import { act, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

// The panel itself calls no API; AddMembersDialog does (issue #398), and these
// stubs are what let the add flow be exercised through the real dialog.
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

// The RF-31 thumbnail fetches through filesApi. The panel's own tests are about
// the list, so the fetch is stubbed here and asserted in AttachmentThumbnail's
// tests; leaving it real would make every file row hit the network.
const mockFetchAttachmentPreview = vi.hoisted(() => vi.fn());
vi.mock("./filesApi", () => ({
  fetchAttachmentPreview: mockFetchAttachmentPreview,
}));

vi.mock("./chatApi", () => ({
  searchChannelMemberCandidates,
  searchGroupParticipantCandidates,
  addChannelMembers,
  addGroupParticipants,
}));

import ConversationDetailsPanel from "./ConversationDetailsPanel";
import type {
  ChannelAttachment,
  ChannelDetails,
  DirectDetails,
  DirectProfile,
  GroupDetails,
  Message,
  PinnedItem,
} from "./chatTypes";
import { localTimeRefreshMs } from "./conversationDetailsDisplay";
import type { ConversationDetailsState } from "./useConversationDetails";

const currentUserId = "user-me";

/**
 * A channel fixture already tagged with the discriminant the panel switches on,
 * so every existing case keeps exercising the channel vocabulary.
 */
function channelDetails(
  overrides: Partial<ChannelDetails> = {},
): { kind: "channel" } & ChannelDetails {
  return {
    kind: "channel" as const,
    id: "ch-1",
    slug: "infra",
    name: "Infraestrutura",
    type: "public",
    createdAt: "2024-01-12T09:30:00.000Z",
    memberCount: 12,
    onlineCount: 0,
    onlineMembers: [],
    // Off unless a case turns it on: the add action is absent by default, which
    // is what the server's own strict `=== true` normalization produces.
    canManageMembers: false,
    ...overrides,
  };
}

function state(overrides: Partial<ConversationDetailsState> = {}): ConversationDetailsState {
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
    previewStatus: "unsupported",
    createdAt: "2026-07-15T12:24:00.000Z",
    ...overrides,
  };
}

function renderPanel(overrides: Partial<Parameters<typeof ConversationDetailsPanel>[0]> = {}) {
  const onClose = vi.fn();
  const { unmount } = render(
    <ConversationDetailsPanel
      kind="channel"
      state={state()}
      currentUserId={currentUserId}
      latestPin={null}
      onClose={onClose}
      {...overrides}
    />,
  );
  return { onClose, unmount };
}

/**
 * Tabs forward until `target` has focus, so a test asserts "reachable by
 * keyboard" rather than "exactly N tab stops away" — the latter breaks whenever
 * an unrelated control is added to the panel.
 */
async function tabUntilFocused(target: HTMLElement, maxStops = 20) {
  for (let stop = 0; stop < maxStops && document.activeElement !== target; stop += 1) {
    await userEvent.tab();
  }
}

afterEach(() => {
  vi.clearAllMocks();
});

describe("ConversationDetailsPanel — canal: estrutura e acessibilidade", () => {
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

describe("ConversationDetailsPanel — canal: seção Sobre", () => {
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
      <ConversationDetailsPanel
        kind="channel"
        state={state({ details: { status: "loading" } })}
        currentUserId={currentUserId}
        latestPin={null}
        onClose={vi.fn()}
      />,
    );
    expect(screen.getByText("Carregando informações do canal…")).toBeInTheDocument();
    unmount();

    render(
      <ConversationDetailsPanel
        kind="channel"
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

describe("ConversationDetailsPanel — canal: membros", () => {
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
      <ConversationDetailsPanel
        kind="channel"
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

describe("ConversationDetailsPanel — canal: ações indisponíveis", () => {
  it("offers the still-unimplemented actions as reachable controls that state their reason", async () => {
    renderPanel();

    // "Ver todos" is what remains unavailable; adding members is a real flow
    // (issue #398) and is asserted separately below.
    const seeAll = screen.getAllByRole("button", { name: "Ver todos" });
    for (const button of seeAll) {
      // aria-disabled, never the HTML attribute: `disabled` would drop the
      // control out of the tab order and take the announced reason with it.
      expect(button).not.toBeDisabled();
      expect(button).toHaveAttribute("aria-disabled", "true");
      expect(button).toHaveAccessibleDescription();
    }
    // Each "Ver todos" is described by its own section's reason, not a shared one.
    expect(seeAll[0]).toHaveAccessibleDescription(
      "A lista completa de membros do canal ainda não está disponível nesta versão.",
    );
    expect(seeAll[1]).toHaveAccessibleDescription(
      "A central de arquivos do canal ainda não está disponível nesta versão.",
    );

    // Activating an unavailable control changes nothing and, above all, never
    // reports a success that did not happen.
    await userEvent.click(seeAll[0]);
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
  });

  it("keeps the unavailable actions in the keyboard tab order", async () => {
    renderPanel();

    // The panel puts focus on its close button on open; from there the tab
    // order must actually reach the unavailable actions.
    expect(screen.getByRole("button", { name: "Fechar detalhes do canal" })).toHaveFocus();
    const seeAll = screen.getAllByRole("button", { name: "Ver todos" })[0];
    await tabUntilFocused(seeAll);

    expect(seeAll).toHaveFocus();
  });
});

describe("ConversationDetailsPanel — canal: mensagem fixada", () => {
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

describe("ConversationDetailsPanel — canal: arquivos recentes", () => {
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
      <ConversationDetailsPanel
        kind="channel"
        state={state({ files: { status: "loading" } })}
        currentUserId={currentUserId}
        latestPin={null}
        onClose={vi.fn()}
      />,
    );
    expect(screen.getByText("Carregando arquivos…")).toBeInTheDocument();
    loading.unmount();

    render(
      <ConversationDetailsPanel
        kind="channel"
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

// ── Painel de grupo (issue #441) ─────────────────────────────────────────────

function groupDetails(overrides: Partial<GroupDetails> = {}): { kind: "group" } & GroupDetails {
  return {
    kind: "group" as const,
    id: "conv-1",
    name: "Time de Infra",
    createdAt: "2024-03-04T15:00:00.000Z",
    participantCount: 4,
    participants: [],
    canManageMembers: false,
    ...overrides,
  };
}

function renderGroupPanel(
  details: { kind: "group" } & GroupDetails,
  viewerId = currentUserId,
  reload: () => void = vi.fn(),
) {
  const onClose = vi.fn();
  const rendered = render(
    <ConversationDetailsPanel
      kind="group"
      state={{
        details: { status: "ready", data: details },
        files: { status: "ready", data: [] },
        reload,
      }}
      currentUserId={viewerId}
      latestPin={null}
      onClose={onClose}
    />,
  );
  return { onClose, reload, ...rendered };
}

describe("ConversationDetailsPanel — grupo", () => {
  it("uses the group heading and close label, not the channel ones", () => {
    renderGroupPanel(groupDetails());

    expect(screen.getByRole("complementary", { name: "Detalhes do grupo" })).toBeInTheDocument();
    expect(screen.getByRole("heading", { name: "Detalhes do grupo" })).toBeInTheDocument();
    expect(screen.queryByRole("heading", { name: "Detalhes do canal" })).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Fechar detalhes do grupo" })).toBeInTheDocument();
  });

  it("shows the group name, creation date and participant total", () => {
    renderGroupPanel(groupDetails({ name: "Time de Infra", participantCount: 12 }));

    expect(screen.getByTestId("chat-details-group-name")).toHaveTextContent("Time de Infra");
    expect(screen.getByText(/Criado em 4 de março de 2024/)).toBeInTheDocument();
    expect(screen.getByText(/12 participantes/)).toBeInTheDocument();
  });

  it("never shows a channel's visibility or description", () => {
    renderGroupPanel(groupDetails());

    // A group is neither public nor private, and has no description column.
    expect(screen.queryByText(/Canal público/)).not.toBeInTheDocument();
    expect(screen.queryByText(/Canal privado/)).not.toBeInTheDocument();
    expect(screen.queryByTestId("chat-details-description")).not.toBeInTheDocument();
    // Nor the channel's people vocabulary.
    expect(screen.queryByRole("heading", { name: /Membros online/ })).not.toBeInTheDocument();
  });

  it("calls the section Participantes and counts the server total", () => {
    renderGroupPanel(
      groupDetails({
        participantCount: 12,
        participants: [{ userId: "u-1", displayName: "Ana" }],
      }),
    );

    // The heading counts every participant, not the length of the capped list.
    expect(screen.getByRole("heading", { name: "Participantes (12)" })).toBeInTheDocument();
    expect(
      within(screen.getByRole("list", { name: "Participantes do grupo" })).getAllByRole("listitem"),
    ).toHaveLength(1);
  });

  it("keeps offline participants in the list", () => {
    renderGroupPanel(
      groupDetails({
        participantCount: 2,
        participants: [
          { userId: "u-1", displayName: "Conectada", presence: "online" },
          { userId: "u-2", displayName: "Desconectado", presence: "offline" },
        ],
      }),
    );

    const rows = within(screen.getByRole("list", { name: "Participantes do grupo" })).getAllByRole(
      "listitem",
    );
    // Unlike the channel panel, presence decorates a row and never removes it.
    expect(rows).toHaveLength(2);
    expect(screen.getByText("Desconectado")).toBeInTheDocument();
    expect(screen.getByText(/Participante · Offline/)).toBeInTheDocument();
  });

  it("marks the authenticated participant by id, not by name", () => {
    renderGroupPanel(
      groupDetails({
        participantCount: 2,
        participants: [
          { userId: currentUserId, displayName: "Álvaro" },
          // Same display name, different person: only the ID may decide.
          { userId: "someone-else", displayName: "Álvaro" },
        ],
      }),
    );

    const rows = within(screen.getByRole("list", { name: "Participantes do grupo" })).getAllByRole(
      "listitem",
    );
    expect(within(rows[0]).getByText("Você")).toBeInTheDocument();
    expect(within(rows[1]).queryByText("Você")).not.toBeInTheDocument();
  });

  it("does not mark anyone when the viewer's id is unknown", () => {
    renderGroupPanel(groupDetails({ participants: [{ userId: "", displayName: "Sem id" }] }), "");

    expect(screen.queryByText("Você")).not.toBeInTheDocument();
  });

  it("handles an empty participant list without claiming the group is broken", () => {
    renderGroupPanel(groupDetails({ participantCount: 0, participants: [] }));

    expect(screen.getByText("Nenhum participante para exibir.")).toBeInTheDocument();
  });

  it("falls back to a neutral label when the group has no title", () => {
    renderGroupPanel(groupDetails({ name: "" }));

    expect(screen.getByTestId("chat-details-group-name")).toHaveTextContent("Grupo sem nome");
  });

  it("renders the group name and participant names as text, never as markup", () => {
    renderGroupPanel(
      groupDetails({
        name: "<img src=x onerror=alert(1)>",
        participants: [{ userId: "u-1", displayName: "<script>alert(1)</script>" }],
      }),
    );

    expect(screen.getByTestId("chat-details-group-name")).toHaveTextContent(
      "<img src=x onerror=alert(1)>",
    );
    expect(screen.getByText("<script>alert(1)</script>")).toBeInTheDocument();
    expect(document.querySelector("img[src='x']")).toBeNull();
    expect(document.querySelector("script")).toBeNull();
  });

  it("uses group wording for the empty pin and file states", () => {
    renderGroupPanel(groupDetails());

    expect(screen.getByTestId("chat-details-pin-empty")).toHaveTextContent(
      "Nenhuma mensagem fixada neste grupo.",
    );
    expect(screen.getByTestId("chat-details-files-empty")).toHaveTextContent(
      "Nenhum arquivo enviado neste grupo.",
    );
  });

  it("states the group reason for the action that is still unavailable", async () => {
    renderGroupPanel(groupDetails());

    const seeAll = screen.getAllByRole("button", { name: "Ver todos" })[0];
    expect(seeAll).not.toBeDisabled();
    expect(seeAll).toHaveAttribute("aria-disabled", "true");
    expect(seeAll).toHaveAccessibleDescription(
      "A lista completa de participantes do grupo ainda não está disponível nesta versão.",
    );

    await userEvent.click(seeAll);
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
  });

  it("shows group wording while loading and on error", () => {
    const { unmount } = render(
      <ConversationDetailsPanel
        kind="group"
        state={{ details: { status: "loading" }, files: { status: "loading" }, reload: vi.fn() }}
        currentUserId={currentUserId}
        latestPin={null}
        onClose={vi.fn()}
      />,
    );
    expect(screen.getByText("Carregando informações do grupo…")).toBeInTheDocument();
    expect(screen.getByText("Carregando participantes…")).toBeInTheDocument();
    unmount();

    render(
      <ConversationDetailsPanel
        kind="group"
        state={{ details: { status: "error" }, files: { status: "error" }, reload: vi.fn() }}
        currentUserId={currentUserId}
        latestPin={null}
        onClose={vi.fn()}
      />,
    );
    expect(
      screen.getByText("Não foi possível carregar as informações do grupo."),
    ).toBeInTheDocument();
    expect(screen.getByText("Não foi possível carregar os participantes.")).toBeInTheDocument();
  });
});

// ── Painel de perfil, DM 1:1 (issue #443) ────────────────────────────────────

function directDetails(overrides: Partial<DirectProfile> = {}): { kind: "direct" } & DirectDetails {
  return {
    kind: "direct" as const,
    conversationId: "conv-dm-1",
    profile: {
      userId: "user-other",
      displayName: "Juliane Lino",
      ...overrides,
    },
  };
}

function renderProfilePanel(details: { kind: "direct" } & DirectDetails = directDetails()) {
  const onClose = vi.fn();
  const rendered = render(
    <ConversationDetailsPanel
      kind="direct"
      state={{
        details: { status: "ready", data: details },
        files: { status: "loading" },
        reload: vi.fn(),
      }}
      currentUserId={currentUserId}
      latestPin={null}
      onClose={onClose}
    />,
  );
  return { onClose, ...rendered };
}

/**
 * The value of the metadata row labelled `label`.
 *
 * Reading the row by its label rather than by index is what lets the "ordem do
 * protótipo" test and the per-field tests fail for different reasons.
 */
function metaRow(label: string): string {
  const card = screen.getByTestId("chat-details-profile-meta");
  const row = Array.from(card.children).find(
    (child) => child.firstElementChild?.textContent === label,
  );
  expect(row, `no metadata row labelled ${label}`).toBeTruthy();
  return row?.lastElementChild?.textContent ?? "";
}

describe("ConversationDetailsPanel — DM 1:1: estrutura e acessibilidade", () => {
  it("is titled Perfil, not the conversation vocabulary", () => {
    renderProfilePanel();

    expect(screen.getByRole("complementary", { name: "Perfil" })).toBeInTheDocument();
    expect(screen.getByRole("heading", { name: "Perfil" })).toBeInTheDocument();
    expect(screen.queryByRole("heading", { name: "Detalhes do canal" })).not.toBeInTheDocument();
    expect(screen.queryByRole("heading", { name: "Detalhes do grupo" })).not.toBeInTheDocument();
  });

  it("closes through an accessible close button and takes focus on open", async () => {
    const { onClose } = renderProfilePanel();

    const close = screen.getByRole("button", { name: "Fechar perfil" });
    expect(close).toHaveFocus();
    await userEvent.click(close);
    expect(onClose).toHaveBeenCalledTimes(1);
  });

  it("shows no channel or group section at all", () => {
    renderProfilePanel();

    // A profile is not a conversation projection: none of these belongs here,
    // and a two-person "participants" list would describe the conversation
    // instead of the person.
    expect(screen.queryByRole("heading", { name: /Membros online/ })).not.toBeInTheDocument();
    expect(screen.queryByRole("heading", { name: /Participantes/ })).not.toBeInTheDocument();
    expect(screen.queryByRole("heading", { name: "Mensagem fixada" })).not.toBeInTheDocument();
    expect(screen.queryByRole("heading", { name: "Arquivos recentes" })).not.toBeInTheDocument();
    expect(screen.queryByRole("heading", { name: "Sobre" })).not.toBeInTheDocument();
    expect(screen.queryByTestId("chat-details-description")).not.toBeInTheDocument();
    expect(screen.queryByText(/Canal público/)).not.toBeInTheDocument();
    expect(screen.queryByText(/participantes/)).not.toBeInTheDocument();
  });
});

describe("ConversationDetailsPanel — DM 1:1: dados do perfil", () => {
  it("shows the other participant's name, role and presence", () => {
    renderProfilePanel(
      directDetails({
        displayName: "Juliane Lino",
        jobTitle: "Infraestrutura & Suporte",
        presence: "online",
      }),
    );

    expect(screen.getByTestId("chat-details-profile-name")).toHaveTextContent("Juliane Lino");
    // The subtitle repeats the job title exactly as the prototype does.
    expect(screen.getAllByText("Infraestrutura & Suporte").length).toBeGreaterThan(0);
    // Presence is a word, never only a colour.
    expect(screen.getByTestId("chat-details-profile-status")).toHaveTextContent("Online");
  });

  it("falls back to initials when there is no avatar", () => {
    renderProfilePanel(directDetails({ displayName: "Juliane Lino" }));

    const avatar = screen.getByTestId("chat-details-profile-avatar");
    expect(avatar.querySelector("img")).toBeNull();
    expect(avatar).toHaveTextContent("JL");
  });

  it("renders an accepted avatar as a decorative image", () => {
    renderProfilePanel(directDetails({ avatarUrl: "/media/juliane.png" }));

    const image = screen.getByTestId("chat-details-profile-avatar").querySelector("img");
    expect(image).toHaveAttribute("src", "/media/juliane.png");
    // Decorative: the name is right next to it, so an alt would be a duplicate.
    expect(image).toHaveAttribute("alt", "");
  });

  it("shows every metadata row the prototype has, in order", () => {
    renderProfilePanel(
      directDetails({
        jobTitle: "Infraestrutura & Suporte",
        department: "TI",
        timezone: "America/Sao_Paulo",
        email: "juliane.lino@nic-labs.test",
      }),
    );

    const labels = Array.from(screen.getByTestId("chat-details-profile-meta").children).map(
      (row) => row.firstElementChild?.textContent,
    );
    expect(labels).toEqual(["Cargo", "Departamento", "Fuso horário", "Horário local", "E-mail"]);
    expect(metaRow("Cargo")).toBe("Infraestrutura & Suporte");
    expect(metaRow("Departamento")).toBe("TI");
    expect(metaRow("Fuso horário")).toBe("America/Sao_Paulo");
    expect(metaRow("E-mail")).toBe("juliane.lino@nic-labs.test");
  });

  it("says Não informado for every field the domain does not record", () => {
    // Today's real payload: an identity and nothing else. The card keeps its
    // shape and states the absence rather than dropping rows.
    renderProfilePanel(directDetails());

    for (const label of ["Cargo", "Departamento", "Fuso horário", "Horário local", "E-mail"]) {
      expect(metaRow(label)).toBe("Não informado");
    }
    // An absent job title leaves no empty subtitle behind.
    expect(document.querySelector(".chat-details__profile-role")).toBeNull();
  });

  it("omits the presence badge when the server tracks nothing", () => {
    renderProfilePanel(directDetails());

    // Absent is not "offline": the UI must not assert a state on the server's
    // behalf.
    expect(screen.queryByTestId("chat-details-profile-status")).not.toBeInTheDocument();
  });

  it("shows the e-mail as text, never as a mailto link", () => {
    renderProfilePanel(directDetails({ email: "juliane.lino@nic-labs.test" }));

    expect(screen.queryByRole("link")).not.toBeInTheDocument();
    expect(document.querySelector("a[href^='mailto:']")).toBeNull();
  });

  it("renders name, job title and department as text, never as markup", () => {
    renderProfilePanel(
      directDetails({
        displayName: "<img src=x onerror=alert(1)>",
        jobTitle: "<script>alert('cargo')</script>",
        department: "<iframe src=javascript:alert(1)>",
        email: "<b>nao@e.markup</b>",
      }),
    );

    expect(screen.getByTestId("chat-details-profile-name")).toHaveTextContent(
      "<img src=x onerror=alert(1)>",
    );
    expect(metaRow("Cargo")).toBe("<script>alert('cargo')</script>");
    expect(metaRow("Departamento")).toBe("<iframe src=javascript:alert(1)>");
    expect(metaRow("E-mail")).toBe("<b>nao@e.markup</b>");
    expect(document.querySelector("img[src='x']")).toBeNull();
    expect(document.querySelector("script")).toBeNull();
    expect(document.querySelector("iframe")).toBeNull();
    expect(document.querySelector("b")).toBeNull();
  });
});

describe("ConversationDetailsPanel — DM 1:1: fuso e horário local", () => {
  beforeEach(() => {
    vi.useFakeTimers();
    // A fixed instant, so "the clock in São Paulo" is a computable value rather
    // than whatever the CI machine happens to read.
    vi.setSystemTime(new Date("2026-07-15T13:12:00.000Z"));
  });

  afterEach(() => {
    vi.useRealTimers();
  });

  it("derives the local time from the profile's zone", () => {
    renderProfilePanel(directDetails({ timezone: "America/Sao_Paulo" }));

    // 13:12 UTC is 10:12 in São Paulo (UTC-3).
    expect(metaRow("Horário local")).toBe("10:12");
  });

  it("uses the profile's zone even when the viewer is somewhere else", () => {
    renderProfilePanel(directDetails({ timezone: "Asia/Tokyo" }));

    // 13:12 UTC is 22:12 in Tokyo. Nothing here may fall back to the reader's
    // own clock, which would state the wrong thing about another person.
    expect(metaRow("Horário local")).toBe("22:12");
  });

  it("respects daylight saving instead of a fixed offset", () => {
    // Lisbon is UTC+1 in July and UTC+0 in January; a stored offset would get
    // one of the two wrong.
    const summer = renderProfilePanel(directDetails({ timezone: "Europe/Lisbon" }));
    expect(metaRow("Horário local")).toBe("14:12");
    summer.unmount();

    vi.setSystemTime(new Date("2026-01-15T13:12:00.000Z"));
    renderProfilePanel(directDetails({ timezone: "Europe/Lisbon" }));
    expect(metaRow("Horário local")).toBe("13:12");
  });

  it("advances the clock without re-rendering every second", () => {
    renderProfilePanel(directDetails({ timezone: "America/Sao_Paulo" }));
    expect(metaRow("Horário local")).toBe("10:12");

    act(() => {
      vi.advanceTimersByTime(localTimeRefreshMs);
    });
    expect(metaRow("Horário local")).toBe("10:13");
  });

  it("treats an invalid or hostile zone as absent, without breaking the panel", () => {
    for (const timezone of ["Nao/Existe", "-03:00", "<script>alert(1)</script>", " "]) {
      const { unmount } = renderProfilePanel(directDetails({ timezone }));

      expect(metaRow("Fuso horário")).toBe("Não informado");
      expect(metaRow("Horário local")).toBe("Não informado");
      // The rest of the profile is unaffected.
      expect(screen.getByTestId("chat-details-profile-name")).toHaveTextContent("Juliane Lino");
      unmount();
    }
  });

  it("starts no timer for a profile without a usable zone", () => {
    // The panel's own interval is what is being observed, so setInterval is
    // spied on directly: a global timer count would also see React's scheduler.
    const scheduled = vi.spyOn(globalThis, "setInterval");
    const { unmount } = renderProfilePanel(directDetails({ timezone: "Nao/Existe" }));

    expect(scheduled).not.toHaveBeenCalled();
    unmount();
    scheduled.mockRestore();
  });

  it("ticks once a minute and clears its timer on unmount", () => {
    const scheduled = vi.spyOn(globalThis, "setInterval");
    const cleared = vi.spyOn(globalThis, "clearInterval");
    const { unmount } = renderProfilePanel(directDetails({ timezone: "America/Sao_Paulo" }));

    // One timer, at the display's own resolution — not sixty ticks per visible
    // change.
    expect(scheduled).toHaveBeenCalledTimes(1);
    expect(scheduled).toHaveBeenCalledWith(expect.any(Function), localTimeRefreshMs);

    // A conversation switch unmounts this panel; a leaked interval would keep
    // ticking against a dead component for the rest of the session.
    unmount();
    expect(cleared).toHaveBeenCalledWith(scheduled.mock.results[0]?.value);
    scheduled.mockRestore();
    cleared.mockRestore();
  });
});

describe("ConversationDetailsPanel — DM 1:1: ação e estados", () => {
  it("offers 'Ver perfil completo' as explicitly unavailable", async () => {
    renderProfilePanel();

    const action = screen.getByRole("button", { name: "Ver perfil completo" });
    // No route renders another user's full profile, so the affordance stays
    // visible and unavailable with its reason announced — never an href="#" and
    // never a navigation to the reader's own account page.
    expect(action).toHaveAttribute("aria-disabled", "true");
    expect(screen.queryByRole("link")).not.toBeInTheDocument();

    await userEvent.click(action);
    expect(
      screen.getByText(
        "O perfil completo de outros usuários ainda não está disponível nesta versão.",
      ),
    ).toBeInTheDocument();
  });

  it("does not carry the HTML disabled attribute, which would hide it from the tab order", () => {
    renderProfilePanel();

    const action = screen.getByRole("button", { name: "Ver perfil completo" });
    // The whole point of the control is the sentence it is described by. A
    // `disabled` button cannot be focused, so that sentence would never be
    // announced to anyone navigating by keyboard.
    expect(action).not.toBeDisabled();
    expect(action).not.toHaveAttribute("disabled");
  });

  it("announces the reason as its accessible description", () => {
    renderProfilePanel();

    const action = screen.getByRole("button", { name: "Ver perfil completo" });
    const reasonId = action.getAttribute("aria-describedby");
    expect(reasonId).toBeTruthy();
    // The reference must resolve: a dangling aria-describedby announces nothing.
    expect(document.querySelectorAll(`#${reasonId}`)).toHaveLength(1);
    expect(action).toHaveAccessibleDescription(
      "O perfil completo de outros usuários ainda não está disponível nesta versão.",
    );
    // The description complements the name; it never replaces it.
    expect(action).toHaveAccessibleName("Ver perfil completo");
  });

  it("is reachable by Tab from the panel's initial focus", async () => {
    renderProfilePanel();

    expect(screen.getByRole("button", { name: "Fechar perfil" })).toHaveFocus();
    const action = screen.getByRole("button", { name: "Ver perfil completo" });
    // Walks the real tab order rather than assuming a fixed number of stops.
    await tabUntilFocused(action);

    expect(action).toHaveFocus();
  });

  it("does nothing when activated by Enter, Space or a click", async () => {
    const { onClose } = renderProfilePanel();
    const action = screen.getByRole("button", { name: "Ver perfil completo" });
    await tabUntilFocused(action);

    const pathBefore = window.location.pathname;
    for (const key of ["{Enter}", " "]) {
      await userEvent.keyboard(key);
    }
    await userEvent.click(action);

    // No navigation, no dialog, no success, and the panel is still the panel.
    expect(window.location.pathname).toBe(pathBefore);
    expect(onClose).not.toHaveBeenCalled();
    expect(screen.getByRole("complementary", { name: "Perfil" })).toBeInTheDocument();
    expect(screen.getByTestId("chat-details-profile-name")).toBeInTheDocument();
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
    expect(screen.queryByRole("link")).not.toBeInTheDocument();
    // Focus stays where the user put it.
    expect(action).toHaveFocus();
  });

  it("announces loading under the Perfil heading", () => {
    render(
      <ConversationDetailsPanel
        kind="direct"
        state={{ details: { status: "loading" }, files: { status: "loading" }, reload: vi.fn() }}
        currentUserId={currentUserId}
        latestPin={null}
        onClose={vi.fn()}
      />,
    );

    expect(screen.getByRole("heading", { name: "Perfil" })).toBeInTheDocument();
    expect(screen.getByText("Carregando perfil…")).toBeInTheDocument();
    // No card of "Não informado" rows while nothing has arrived.
    expect(screen.queryByTestId("chat-details-profile-meta")).not.toBeInTheDocument();
  });

  it("shows an error instead of an empty profile card", () => {
    render(
      <ConversationDetailsPanel
        kind="direct"
        state={{ details: { status: "error" }, files: { status: "loading" }, reload: vi.fn() }}
        currentUserId={currentUserId}
        latestPin={null}
        onClose={vi.fn()}
      />,
    );

    expect(screen.getByRole("alert")).toHaveTextContent("Não foi possível carregar o perfil.");
    // A failure must never read as "a person with no attributes".
    expect(screen.queryByTestId("chat-details-profile-meta")).not.toBeInTheDocument();
    expect(screen.queryByText("Não informado")).not.toBeInTheDocument();
  });

  it("refuses to render a channel or group payload as a profile", () => {
    render(
      <ConversationDetailsPanel
        kind="direct"
        state={{
          details: { status: "ready", data: groupDetails() },
          files: { status: "ready", data: [] },
          reload: vi.fn(),
        }}
        currentUserId={currentUserId}
        latestPin={null}
        onClose={vi.fn()}
      />,
    );

    // The tag records which request produced the data, so a response that
    // outlived a conversation switch cannot be shown here as somebody's profile.
    expect(screen.getByRole("alert")).toHaveTextContent("Não foi possível carregar o perfil.");
    expect(screen.queryByText("Time de Infra")).not.toBeInTheDocument();
  });
});

describe("ConversationDetailsPanel — DM 1:1: variante divergente", () => {
  it("refuses to render a payload whose tag is not direct", () => {
    // The hook stores what the client returned. A value tagged for another
    // aggregate reaching the direct panel means something upstream mislabelled
    // it, and the panel must not translate it into a person.
    render(
      <ConversationDetailsPanel
        kind="direct"
        state={{
          details: { status: "ready", data: channelDetails() },
          files: { status: "ready", data: [] },
          reload: vi.fn(),
        }}
        currentUserId={currentUserId}
        latestPin={null}
        onClose={vi.fn()}
      />,
    );

    expect(screen.getByRole("alert")).toHaveTextContent("Não foi possível carregar o perfil.");
    expect(screen.queryByTestId("chat-details-profile-name")).not.toBeInTheDocument();
    expect(screen.queryByTestId("chat-details-profile-meta")).not.toBeInTheDocument();
    expect(screen.queryByText("Infraestrutura")).not.toBeInTheDocument();
  });
});

// ── Adicionar membros (issue #398) ───────────────────────────────────────────
//
// Migrated from the deleted ChannelDetailsPanel and GroupDetailsPanel suites:
// the flow now lives in the unified panel and must behave identically for both
// vocabularies, and must not exist at all for a 1:1.

function readyChannel(overrides: Partial<ChannelDetails> = {}, reload = vi.fn()) {
  return {
    details: {
      status: "ready" as const,
      data: channelDetails({ canManageMembers: true, ...overrides }),
    },
    files: { status: "ready" as const, data: [] },
    reload,
  };
}

function readyGroup(overrides: Partial<GroupDetails> = {}, reload = vi.fn()) {
  return {
    details: {
      status: "ready" as const,
      data: groupDetails({ canManageMembers: true, ...overrides }),
    },
    files: { status: "ready" as const, data: [] },
    reload,
  };
}

function renderChannelFor(state: ConversationDetailsState) {
  return render(
    <ConversationDetailsPanel
      kind="channel"
      state={state}
      currentUserId={currentUserId}
      latestPin={null}
      onClose={vi.fn()}
    />,
  );
}

describe("ConversationDetailsPanel — adicionar membros: permissão", () => {
  // The action is server-gated. `canManageMembers` is normalized to false unless
  // the server explicitly said true, so every state that is not "ready and
  // permitted" must leave the control absent.
  it("offers the action when the server says the caller may manage members", () => {
    renderPanel({ state: state({ details: readyChannel().details }) });

    expect(screen.getByTestId("chat-details-add-members")).toBeEnabled();
    expect(screen.getByTestId("chat-details-add-members")).toHaveTextContent("Adicionar membros");
  });

  it.each([
    ["public", "public" as const],
    ["private", "private" as const],
  ])("offers the action on a %s channel", (_label, type) => {
    renderPanel({ state: state({ details: readyChannel({ type }).details }) });

    expect(screen.getByTestId("chat-details-add-members")).toBeInTheDocument();
  });

  it("offers the action on a group, with the group's wording", () => {
    renderGroupPanel(groupDetails({ canManageMembers: true }));

    expect(screen.getByTestId("chat-details-add-members")).toHaveTextContent(
      "Adicionar participantes",
    );
  });

  it("hides the action when the caller may not manage members", () => {
    renderPanel({
      state: state({
        details: { status: "ready", data: channelDetails({ canManageMembers: false }) },
      }),
    });
    expect(screen.queryByTestId("chat-details-add-members")).not.toBeInTheDocument();
  });

  it("hides the action on a group when the server withholds the permission", () => {
    renderGroupPanel(groupDetails({ canManageMembers: false }));

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

  // A 1:1 has no add action at all: a third person would convert the direct
  // conversation into a group, which issue #398 deliberately does not do.
  it("never offers the action on a 1:1 profile", () => {
    render(
      <ConversationDetailsPanel
        kind="direct"
        state={{
          details: {
            status: "ready",
            data: {
              kind: "direct",
              conversationId: "dm-1",
              profile: { userId: "user-other", displayName: "Juliane Lino" },
            },
          },
          files: { status: "ready", data: [] },
          reload: vi.fn(),
        }}
        currentUserId={currentUserId}
        latestPin={null}
        onClose={vi.fn()}
      />,
    );

    expect(screen.queryByTestId("chat-details-add-members")).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /Adicionar/ })).not.toBeInTheDocument();
  });
});

describe("ConversationDetailsPanel — adicionar membros: fluxo", () => {
  beforeEach(() => {
    searchChannelMemberCandidates.mockResolvedValue([{ userId: "u-9", displayName: "Bruno Dias" }]);
    searchGroupParticipantCandidates.mockResolvedValue([
      { userId: "u-9", displayName: "Bruno Dias" },
    ]);
    addChannelMembers.mockResolvedValue({ added: 1, alreadyMembers: 0, memberCount: 5 });
    addGroupParticipants.mockResolvedValue({ added: 1, alreadyMembers: 0, memberCount: 5 });
  });

  it("opens and closes the picker, returning focus to the action", async () => {
    const user = userEvent.setup();
    renderPanel({ state: state({ details: readyChannel().details }) });

    const action = screen.getByTestId("chat-details-add-members");
    await user.click(action);
    expect(screen.getByRole("dialog", { name: "Adicionar membros" })).toBeInTheDocument();

    await user.keyboard("{Escape}");
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    expect(action).toHaveFocus();
  });

  it("posts a channel selection to the channel endpoint", async () => {
    const user = userEvent.setup();
    renderChannelFor(readyChannel({ id: "ch-77" }));

    await user.click(screen.getByTestId("chat-details-add-members"));
    const dialog = screen.getByRole("dialog", { name: "Adicionar membros" });
    await user.type(within(dialog).getByLabelText("Pesquisar pessoa"), "br");
    await user.click(await within(dialog).findByRole("button", { name: /Bruno Dias/ }));
    await user.click(within(dialog).getByRole("button", { name: "Adicionar" }));

    await waitFor(() =>
      expect(addChannelMembers).toHaveBeenCalledWith("ch-77", ["u-9"], expect.any(AbortSignal)),
    );
    // A channel flow must never reach the group endpoint.
    expect(addGroupParticipants).not.toHaveBeenCalled();
  });

  it("posts a group selection to the group endpoint", async () => {
    const user = userEvent.setup();
    render(
      <ConversationDetailsPanel
        kind="group"
        state={readyGroup({ id: "dm-42" })}
        currentUserId={currentUserId}
        latestPin={null}
        onClose={vi.fn()}
      />,
    );

    await user.click(screen.getByTestId("chat-details-add-members"));
    const dialog = screen.getByRole("dialog", { name: "Adicionar membros" });
    await user.type(within(dialog).getByLabelText("Pesquisar pessoa"), "br");
    await user.click(await within(dialog).findByRole("button", { name: /Bruno Dias/ }));
    await user.click(within(dialog).getByRole("button", { name: "Adicionar" }));

    await waitFor(() =>
      expect(addGroupParticipants).toHaveBeenCalledWith("dm-42", ["u-9"], expect.any(AbortSignal)),
    );
    expect(addChannelMembers).not.toHaveBeenCalled();
  });

  it("refetches after a successful add instead of merging the response", async () => {
    const reload = vi.fn();
    const user = userEvent.setup();
    renderChannelFor(readyChannel({}, reload));

    await user.click(screen.getByTestId("chat-details-add-members"));
    const dialog = screen.getByRole("dialog", { name: "Adicionar membros" });
    await user.type(within(dialog).getByLabelText("Pesquisar pessoa"), "br");
    await user.click(await within(dialog).findByRole("button", { name: /Bruno Dias/ }));
    await user.click(within(dialog).getByRole("button", { name: "Adicionar" }));

    await waitFor(() => expect(reload).toHaveBeenCalled());
    expect(await screen.findByText("1 pessoa adicionada ao canal.")).toBeInTheDocument();
  });

  it("uses the group's wording in the success notice", async () => {
    const reload = vi.fn();
    const user = userEvent.setup();
    render(
      <ConversationDetailsPanel
        kind="group"
        state={readyGroup({}, reload)}
        currentUserId={currentUserId}
        latestPin={null}
        onClose={vi.fn()}
      />,
    );

    await user.click(screen.getByTestId("chat-details-add-members"));
    const dialog = screen.getByRole("dialog", { name: "Adicionar membros" });
    await user.type(within(dialog).getByLabelText("Pesquisar pessoa"), "br");
    await user.click(await within(dialog).findByRole("button", { name: /Bruno Dias/ }));
    await user.click(within(dialog).getByRole("button", { name: "Adicionar" }));

    await waitFor(() => expect(reload).toHaveBeenCalled());
    expect(await screen.findByText("1 pessoa adicionada ao grupo.")).toBeInTheDocument();
  });

  // added: 0 is a legitimate outcome — everyone picked was already in — and it
  // must read as such rather than as a failure or a fresh success.
  it("reports an add that changed nothing without claiming a success", async () => {
    addChannelMembers.mockResolvedValue({ added: 0, alreadyMembers: 1, memberCount: 5 });
    const user = userEvent.setup();
    renderChannelFor(readyChannel());

    await user.click(screen.getByTestId("chat-details-add-members"));
    const dialog = screen.getByRole("dialog", { name: "Adicionar membros" });
    await user.type(within(dialog).getByLabelText("Pesquisar pessoa"), "br");
    await user.click(await within(dialog).findByRole("button", { name: /Bruno Dias/ }));
    await user.click(within(dialog).getByRole("button", { name: "Adicionar" }));

    expect(
      await screen.findByText("Todas as pessoas selecionadas já participam deste canal."),
    ).toBeInTheDocument();
  });
});

// The corrected eligibility source (issue #398).
describe("ConversationDetailsPanel — busca contextual de candidatos", () => {
  it("searches through the channel-scoped endpoint", async () => {
    searchChannelMemberCandidates.mockResolvedValue([{ userId: "u-9", displayName: "Bruno Dias" }]);
    const user = userEvent.setup();
    renderChannelFor(readyChannel({ id: "ch-77" }));

    await user.click(screen.getByTestId("chat-details-add-members"));
    await user.type(screen.getByLabelText("Pesquisar pessoa"), "br");

    expect(await screen.findByRole("button", { name: /Bruno Dias/ })).toBeInTheDocument();
    expect(searchChannelMemberCandidates).toHaveBeenCalledWith(
      "ch-77",
      "br",
      expect.any(AbortSignal),
    );
    expect(searchGroupParticipantCandidates).not.toHaveBeenCalled();
  });

  it("searches through the group-scoped endpoint and filters the viewer locally", async () => {
    searchGroupParticipantCandidates.mockResolvedValue([
      { userId: currentUserId, displayName: "Eu Mesmo" },
      { userId: "u-9", displayName: "Bruno Dias" },
    ]);
    const user = userEvent.setup();
    render(
      <ConversationDetailsPanel
        kind="group"
        state={readyGroup({ id: "dm-77" })}
        currentUserId={currentUserId}
        latestPin={null}
        onClose={vi.fn()}
      />,
    );

    await user.click(screen.getByTestId("chat-details-add-members"));
    const dialog = screen.getByRole("dialog", { name: "Adicionar membros" });
    await user.type(within(dialog).getByLabelText("Pesquisar pessoa"), "an");

    expect(await within(dialog).findByRole("button", { name: /Bruno Dias/ })).toBeInTheDocument();
    expect(within(dialog).queryByRole("button", { name: /Eu Mesmo/ })).not.toBeInTheDocument();
    expect(searchGroupParticipantCandidates).toHaveBeenCalledWith(
      "dm-77",
      "an",
      expect.any(AbortSignal),
    );
  });

  // The panel's rendered roster must not become the exclusion list: the channel
  // list is presence-filtered and the group list is capped, so both are
  // incomplete by construction and using them offered current members.
  it("does not derive exclusions from the online-members preview", async () => {
    searchChannelMemberCandidates.mockResolvedValue([
      { userId: "online-member", displayName: "Ana Lima" },
    ]);
    const user = userEvent.setup();
    renderChannelFor(
      readyChannel({
        onlineMembers: [
          { userId: "online-member", displayName: "Ana Lima", role: "member", presence: "online" },
        ],
      }),
    );

    await user.click(screen.getByTestId("chat-details-add-members"));
    await user.type(screen.getByLabelText("Pesquisar pessoa"), "an");

    // Whatever the endpoint returns is offered — the server decides membership,
    // and in production it would not have returned a current member.
    expect(await screen.findByRole("button", { name: /Ana Lima/ })).toBeInTheDocument();
  });

  it("does not derive exclusions from the participant preview", async () => {
    searchGroupParticipantCandidates.mockResolvedValue([
      { userId: "p-1", displayName: "Ana Lima" },
    ]);
    const user = userEvent.setup();
    render(
      <ConversationDetailsPanel
        kind="group"
        state={readyGroup({
          participants: [{ userId: "p-1", displayName: "Ana Lima", presence: "online" }],
        })}
        currentUserId={currentUserId}
        latestPin={null}
        onClose={vi.fn()}
      />,
    );

    await user.click(screen.getByTestId("chat-details-add-members"));
    const dialog = screen.getByRole("dialog", { name: "Adicionar membros" });
    await user.type(within(dialog).getByLabelText("Pesquisar pessoa"), "an");

    expect(await within(dialog).findByRole("button", { name: /Ana Lima/ })).toBeInTheDocument();
  });
});

// The panel deliberately survives a target switch, so nothing unmounts the
// dialog for us. These prove a selection made for conversation A can never be
// confirmed into conversation B.
describe("ConversationDetailsPanel — troca de conversa", () => {
  beforeEach(() => {
    searchChannelMemberCandidates.mockResolvedValue([{ userId: "u-9", displayName: "Bruno Dias" }]);
    addChannelMembers.mockResolvedValue({ added: 1, alreadyMembers: 0, memberCount: 5 });
  });

  it("closes the picker and sends nothing when the channel changes mid-selection", async () => {
    const user = userEvent.setup();
    const { rerender } = renderChannelFor(readyChannel({ id: "ch-A" }));

    await user.click(screen.getByTestId("chat-details-add-members"));
    const dialog = screen.getByRole("dialog", { name: "Adicionar membros" });
    await user.type(within(dialog).getByLabelText("Pesquisar pessoa"), "br");
    await user.click(await within(dialog).findByRole("button", { name: /Bruno Dias/ }));
    expect(within(dialog).getByRole("list", { name: "Pessoas selecionadas" })).toBeInTheDocument();

    rerender(
      <ConversationDetailsPanel
        kind="channel"
        state={readyChannel({ id: "ch-B" })}
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
    const { rerender } = renderChannelFor(readyChannel({ id: "ch-A" }));

    await user.click(screen.getByTestId("chat-details-add-members"));
    await user.type(screen.getByLabelText("Pesquisar pessoa"), "br");
    await user.click(await screen.findByRole("button", { name: /Bruno Dias/ }));

    rerender(
      <ConversationDetailsPanel
        kind="channel"
        state={readyChannel({ id: "ch-B" })}
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
    const { rerender } = renderChannelFor(readyChannel({ id: "ch-A" }));

    await user.click(screen.getByTestId("chat-details-add-members"));
    await user.type(screen.getByLabelText("Pesquisar pessoa"), "br");

    rerender(
      <ConversationDetailsPanel
        kind="channel"
        state={readyChannel({ id: "ch-B" })}
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
    const { rerender } = renderChannelFor(readyChannel({ id: "ch-A" }));

    await user.click(screen.getByTestId("chat-details-add-members"));
    await user.type(screen.getByLabelText("Pesquisar pessoa"), "br");
    await user.click(await screen.findByRole("button", { name: /Bruno Dias/ }));
    await user.click(screen.getByRole("button", { name: "Adicionar" }));
    expect(await screen.findByRole("alert")).toBeInTheDocument();

    rerender(
      <ConversationDetailsPanel
        kind="channel"
        state={readyChannel({ id: "ch-B" })}
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

  // The panel stays mounted across a switch by design, so the notice must be
  // cleared by identity rather than by unmount.
  it("clears the added notice when the panel switches channel", async () => {
    const user = userEvent.setup();
    const { rerender } = renderChannelFor(readyChannel({ id: "ch-1" }));

    await user.click(screen.getByTestId("chat-details-add-members"));
    await user.type(screen.getByLabelText("Pesquisar pessoa"), "br");
    await user.click(await screen.findByRole("button", { name: /Bruno Dias/ }));
    await user.click(screen.getByRole("button", { name: "Adicionar" }));
    expect(await screen.findByText("1 pessoa adicionada ao canal.")).toBeInTheDocument();

    rerender(
      <ConversationDetailsPanel
        kind="channel"
        state={readyChannel({ id: "ch-2" })}
        currentUserId={currentUserId}
        latestPin={null}
        onClose={vi.fn()}
      />,
    );

    expect(screen.queryByText(/pessoas? adicionada/)).not.toBeInTheDocument();
  });
});

describe("ConversationDetailsPanel attachment previews (RF-31)", () => {
  it("shows a thumbnail for a ready preview and the icon for everything else", async () => {
    mockFetchAttachmentPreview.mockResolvedValue(new Blob(["jpeg-bytes"]));
    const createObjectURL = vi.fn(() => "blob:preview-1");
    vi.stubGlobal("URL", { ...URL, createObjectURL, revokeObjectURL: vi.fn() });

    renderPanel({
      state: state({
        files: {
          status: "ready",
          data: [
            attachment({ id: "a-ready", filename: "foto.png", previewStatus: "ready" }),
            attachment({ id: "a-plain", filename: "planilha.xlsx", previewStatus: "unsupported" }),
          ],
        },
      }),
    });

    const thumbs = await screen.findAllByTestId("chat-details-file-thumb");
    expect(thumbs).toHaveLength(1);
    expect(thumbs[0]).toHaveAttribute("alt", "Pré-visualização de foto.png");
    // The attachment with no preview kept the icon, and was never requested.
    expect(mockFetchAttachmentPreview).toHaveBeenCalledTimes(1);
    expect(mockFetchAttachmentPreview).toHaveBeenCalledWith("a-ready", expect.any(AbortSignal));

    vi.unstubAllGlobals();
  });
});
