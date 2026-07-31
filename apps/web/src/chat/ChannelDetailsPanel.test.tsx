import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";

import ChannelDetailsPanel from "./ChannelDetailsPanel";
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
    ...overrides,
  };
}

function state(overrides: Partial<ChannelDetailsState> = {}): ChannelDetailsState {
  return {
    details: { status: "ready", data: channelDetails() },
    files: { status: "ready", data: [] },
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
  it("offers the member actions as disabled controls with a stated reason", async () => {
    renderPanel();

    const seeAll = screen.getAllByRole("button", { name: "Ver todos" });
    const addMembers = screen.getByRole("button", { name: /Adicionar membros/ });
    for (const button of [...seeAll, addMembers]) {
      expect(button).toBeDisabled();
      expect(button).toHaveAttribute("aria-describedby");
    }

    // Clicking a disabled control changes nothing and, above all, never reports
    // a success that did not happen.
    await userEvent.click(addMembers);
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
    expect(
      screen.getByText("A gestão de membros do canal ainda não está disponível nesta versão."),
    ).toBeInTheDocument();
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
