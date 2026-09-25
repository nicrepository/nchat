import { act, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
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
  // The panel renders AttachmentVideo for every file row. This panel's tests are
  // about the rows themselves, not about playback, so content is never
  // available here and every player falls back — which is exactly what a row
  // must survive.
  fetchAttachmentContent: () => Promise.reject(new Error("not used")),
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
import { formatLongDate } from "./messageDisplay";
import { conversationNameMaxCodePoints } from "./conversationRename";
import { ApiRequestError } from "../lib/api";
import type { ConversationDetailsState } from "./useConversationDetails";
import type { DirectMessageAccess } from "./directMessage";

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
    // Absent by default (issue #894), so every case that says nothing about
    // the description exercises the empty state the domain actually produces.
    description: "",
    createdAt: "2024-01-12T09:30:00.000Z",
    memberCount: 12,
    onlineCount: 0,
    onlineMembers: [],
    canAddMembers: false,
    // Off unless a case turns it on: the add action is absent by default, which
    // is what the server's own strict `=== true` normalization produces.
    canManageMembers: false,
    // Same default and the same reason (issue #469): no capability, no
    // removal control, and no roster request behind it.
    canRemoveMembers: false,
    ...overrides,
  };
}

function state(overrides: Partial<ConversationDetailsState> = {}): ConversationDetailsState {
  const details = overrides.details ?? { status: "ready", data: channelDetails() };
  const roster =
    overrides.roster ??
    (details.status === "ready" && details.data.kind === "channel"
      ? {
          status: "ready" as const,
          data: {
            memberCount: details.data.memberCount,
            members: details.data.onlineMembers.map((member) => ({
              userId: member.userId,
              displayName: member.displayName,
              avatarUrl: member.avatarUrl,
              role: member.role,
            })),
          },
        }
      : { status: "loading" as const });
  return {
    details,
    files: { status: "ready", data: [] },
    roster,
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

  // Issue #467: the panel closes on Escape, which is what gets a keyboard user
  // out of it where it covers the conversation instead of sitting beside it.
  it("closes on Escape", async () => {
    const { onClose } = renderPanel();

    await userEvent.keyboard("{Escape}");

    expect(onClose).toHaveBeenCalledTimes(1);
  });

  // React bubbles a portal's events to its React parent rather than its DOM one,
  // so Escape inside a dialog this panel opened reaches the panel's own handler.
  // Dismissing that dialog must leave the panel exactly where it was: the dialog
  // hands focus back to the control inside the panel that opened it, and there
  // would be nothing left to hand it back to.
  it("stays open when Escape dismisses a dialog it opened", async () => {
    const { onClose } = renderPanel({
      state: state({
        details: {
          status: "ready",
          data: channelDetails({ canAddMembers: true, canManageMembers: true }),
        },
      }),
    });

    const addMembers = screen.getByRole("button", { name: "Adicionar membros" });
    await userEvent.click(addMembers);
    expect(await screen.findByRole("dialog", { name: "Adicionar membros" })).toBeInTheDocument();

    await userEvent.keyboard("{Escape}");

    expect(screen.queryByRole("dialog", { name: "Adicionar membros" })).not.toBeInTheDocument();
    expect(onClose).not.toHaveBeenCalled();
    expect(addMembers).toHaveFocus();
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
    // Visibility and size are two facts and now two rows (issue #894): the
    // channel's type is not a qualifier on its member count.
    expect(screen.getByText("Canal privado")).toBeInTheDocument();
    expect(screen.getByText("12 membros")).toBeInTheDocument();
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
    expect(screen.getByTestId("chat-details-people-count")).toHaveTextContent("40 membros");
    expect(screen.getByRole("heading", { name: "Membros (40)" })).toBeInTheDocument();
    expect(
      within(screen.getByRole("list", { name: "Membros do canal" })).getAllByRole("listitem"),
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

  it("shows an explicit empty state when the channel has no description", () => {
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

    const list = screen.getByRole("list", { name: "Membros do canal" });
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

  // RF-58/CQ-3: the endpoint still reports `presence`, and the panel no longer
  // renders it. Presence has one authority in this client — the realtime store —
  // so a panel that also read the HTTP field could show "Online" for someone the
  // sidebar and the header were showing nothing for.
  it("ignores the presence the endpoint reports", () => {
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

    // No indicator and no word, because the store has said nothing. Both rows
    // are still there: the roster does not depend on presence.
    expect(screen.queryAllByTestId("presence-dot")).toHaveLength(0);
    expect(screen.queryByText(/· Online/)).not.toBeInTheDocument();
    expect(screen.getByText("Membro")).toBeInTheDocument();
    expect(screen.getByText("Moderador")).toBeInTheDocument();
  });

  it("shows an offline member even when the presence preview is empty", () => {
    const { unmount } = renderPanel({
      state: state({
        details: {
          status: "ready",
          data: channelDetails({ onlineMembers: [], onlineCount: 0, memberCount: 31 }),
        },
        roster: {
          status: "ready",
          data: {
            memberCount: 31,
            members: [{ userId: "offline-1", displayName: "Membro offline", role: "member" }],
          },
        },
      }),
    });

    expect(screen.getByText("Membro offline")).toBeInTheDocument();
    expect(screen.getByRole("heading", { name: "Membros (31)" })).toBeInTheDocument();
    expect(screen.queryByTestId("presence-dot")).not.toBeInTheDocument();
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

// ── Seções expansíveis (issue #892) ──────────────────────────────────────────
//
// The panel's side of the shared primitive. Its own contracts — the compact
// cap, ARIA, focus, keyboard, two independent instances — are covered in
// ExpandableDetailsSection.test.tsx and are deliberately not repeated here.
// What belongs here is that the panel wires real conversation data into it and
// that each section still renders its own domain rows.

/** Enough online members to exceed the compact cap. */
function onlineRoster(count: number) {
  return Array.from({ length: count }, (_, index) => ({
    userId: `user-${index}`,
    displayName: `Pessoa ${index + 1}`,
    role: "member" as const,
  }));
}

describe("ConversationDetailsPanel — canal: seção de pessoas expansível", () => {
  it("no longer offers an unavailable control or the sentence that explained it", () => {
    renderPanel();

    // The channel fixture has nobody online, so there is nothing to expand to
    // and no control at all — not a visible one that reveals nothing.
    expect(screen.queryByRole("button", { name: /Ver todos/ })).not.toBeInTheDocument();
    expect(screen.queryByText(/ainda não está disponível nesta versão/)).not.toBeInTheDocument();
  });

  it("shows five of seven online members and reveals the rest on demand", async () => {
    renderPanel({
      state: state({
        details: {
          status: "ready",
          data: channelDetails({ onlineMembers: onlineRoster(7), onlineCount: 7, memberCount: 7 }),
        },
      }),
    });

    const list = () => screen.getByRole("list", { name: "Membros do canal" });
    expect(within(list()).getAllByRole("listitem")).toHaveLength(5);
    expect(screen.getByRole("heading", { name: "Membros (7)" })).toBeInTheDocument();
    expect(screen.queryByText("Pessoa 7")).not.toBeInTheDocument();

    const toggle = screen.getByRole("button", { name: /Ver todos Membros/ });
    // Reachable from the close button the panel focuses on open.
    expect(screen.getByRole("button", { name: "Fechar detalhes do canal" })).toHaveFocus();
    await tabUntilFocused(toggle);
    await userEvent.keyboard("{Enter}");

    expect(within(list()).getAllByRole("listitem")).toHaveLength(7);
    // Still the domain's own row, not something the primitive drew.
    expect(within(list()).getAllByTestId("chat-details-member-avatar")).toHaveLength(7);
    expect(screen.getByText("Pessoa 7")).toBeInTheDocument();

    await userEvent.click(screen.getByRole("button", { name: /Mostrar menos Membros/ }));
    expect(within(list()).getAllByRole("listitem")).toHaveLength(5);
  });

  it("reports a total larger than the preview without offering to list it", () => {
    // The server caps the roster and reports the real total separately, so the
    // two legitimately disagree. Three carried of forty online is the shape
    // that discriminates: nothing local is hidden, and no flow exists to fetch
    // the other thirty-seven, so a "Ver todos" here could only expand to the
    // same three rows.
    renderPanel({
      state: state({
        details: {
          status: "ready",
          data: channelDetails({
            onlineMembers: onlineRoster(3),
            onlineCount: 40,
            memberCount: 40,
          }),
        },
      }),
    });

    expect(screen.getByRole("heading", { name: "Membros (40)" })).toBeInTheDocument();
    expect(
      within(screen.getByRole("list", { name: "Membros do canal" })).getAllByRole("listitem"),
    ).toHaveLength(3);
    expect(screen.queryByRole("button", { name: /Ver todos/ })).not.toBeInTheDocument();
  });

  it("expands a capped preview to its own rows and never promises the rest", async () => {
    // The real backend limit: MaxChannelDetailsMembers rows carried, forty
    // reported. The control exists — twenty-five loaded rows are hidden by the
    // compact cap — and what it reveals is those, never the ten the server
    // never sent. So it does not say "Ver todos": expanding shows more, not all.
    renderPanel({
      state: state({
        details: {
          status: "ready",
          data: channelDetails({
            onlineMembers: onlineRoster(30),
            onlineCount: 40,
            memberCount: 40,
          }),
        },
      }),
    });

    const list = () => screen.getByRole("list", { name: "Membros do canal" });
    expect(within(list()).getAllByRole("listitem")).toHaveLength(5);
    expect(screen.queryByRole("button", { name: /Ver todos/ })).not.toBeInTheDocument();
    // And the shortfall is named rather than left for the reader to infer from
    // a heading that says forty above a list that stops at thirty.
    expect(screen.getByTestId("chat-details-roster-shortfall")).toHaveTextContent(
      "30 de 40 membros carregados.",
    );

    await userEvent.click(screen.getByRole("button", { name: /Mostrar mais Membros/ }));

    expect(within(list()).getAllByRole("listitem")).toHaveLength(30);
    expect(screen.getByRole("heading", { name: "Membros (40)" })).toBeInTheDocument();
    // The way back is unchanged: only the promise of "all" was wrong.
    expect(screen.getByRole("button", { name: /Mostrar menos Membros/ })).toBeInTheDocument();
  });

  it("offers no control when exactly five members are online", () => {
    renderPanel({
      state: state({
        details: {
          status: "ready",
          data: channelDetails({ onlineMembers: onlineRoster(5), onlineCount: 5, memberCount: 5 }),
        },
      }),
    });

    expect(
      within(screen.getByRole("list", { name: "Membros do canal" })).getAllByRole("listitem"),
    ).toHaveLength(5);
    expect(screen.queryByRole("button", { name: /Ver todos/ })).not.toBeInTheDocument();
  });

  it("collapses the roster again when the panel is pointed at another conversation", async () => {
    const expanded = state({
      details: {
        status: "ready",
        data: channelDetails({ onlineMembers: onlineRoster(7), onlineCount: 7, memberCount: 7 }),
      },
    });
    const { rerender } = render(
      <ConversationDetailsPanel
        kind="channel"
        state={expanded}
        currentUserId={currentUserId}
        latestPin={null}
        onClose={vi.fn()}
      />,
    );
    await userEvent.click(screen.getByRole("button", { name: /Ver todos Membros/ }));
    expect(
      within(screen.getByRole("list", { name: "Membros do canal" })).getAllByRole("listitem"),
    ).toHaveLength(7);

    // The panel is deliberately not remounted on a target switch; the section
    // still must not carry one conversation's expansion into the next.
    rerender(
      <ConversationDetailsPanel
        kind="channel"
        state={state({
          details: {
            status: "ready",
            data: channelDetails({
              id: "ch-2",
              onlineMembers: onlineRoster(7),
              onlineCount: 7,
              memberCount: 7,
            }),
          },
        })}
        currentUserId={currentUserId}
        latestPin={null}
        onClose={vi.fn()}
      />,
    );

    expect(
      within(screen.getByRole("list", { name: "Membros do canal" })).getAllByRole("listitem"),
    ).toHaveLength(5);
    expect(screen.getByRole("button", { name: /Ver todos Membros/ })).toBeInTheDocument();
  });

  it("keeps the two sections' expansions independent of each other", async () => {
    renderPanel({
      state: state({
        details: {
          status: "ready",
          data: channelDetails({ onlineMembers: onlineRoster(7), onlineCount: 7, memberCount: 7 }),
        },
        files: {
          status: "ready",
          data: Array.from({ length: 6 }, (_, index) =>
            attachment({ id: `a-${index}`, filename: `arquivo-${index}.pdf` }),
          ),
        },
      }),
    });

    await userEvent.click(screen.getByRole("button", { name: /Ver todos Membros/ }));

    expect(
      within(screen.getByRole("list", { name: "Membros do canal" })).getAllByRole("listitem"),
    ).toHaveLength(7);
    // The files section is the second consumer of the same primitive and is
    // untouched by what the people section did.
    expect(
      within(screen.getByRole("list", { name: "Arquivos recentes" })).getAllByRole("listitem"),
    ).toHaveLength(5);
    expect(screen.getByRole("button", { name: /Ver todos Arquivos recentes/ })).toBeInTheDocument();
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

  it("picks the file icon from the detected type, never from the name", async () => {
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

    // Six files exceed the compact cap (issue #892), so the sixth icon is only
    // reachable once the section is expanded — which is itself the files
    // section proving it shares the primitive.
    await userEvent.click(screen.getByRole("button", { name: /Ver todos Arquivos recentes/ }));
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
    description: "",
    createdAt: "2024-03-04T15:00:00.000Z",
    participantCount: 4,
    participants: [],
    canManageMembers: false,
    // Creator-only for a group (issue #469), so off unless a case says so.
    canRemoveMembers: false,
    ...overrides,
  };
}

function renderGroupPanel(
  details: { kind: "group" } & GroupDetails,
  viewerId = currentUserId,
  reload: () => void = vi.fn(),
  openDM?: DirectMessageAccess,
  files: ChannelAttachment[] = [],
) {
  const onClose = vi.fn();
  const rendered = render(
    <ConversationDetailsPanel
      kind="group"
      state={{
        details: { status: "ready", data: details },
        files: { status: "ready", data: files },
        roster: { status: "loading" },
        reload,
      }}
      currentUserId={viewerId}
      latestPin={null}
      openDM={openDM}
      onClose={onClose}
    />,
  );
  return { onClose, reload, ...rendered };
}

/**
 * The open-DM capability as a double: the flow's own semantics live in
 * useAuthorDM and are tested there, so what a panel test needs is the shape of
 * what it is handed and a record of what it asked for.
 */
function fakeOpenDM(
  options: { pending?: ReadonlySet<string> } = {},
): DirectMessageAccess & { open: ReturnType<typeof vi.fn> } {
  const open = vi.fn();
  const pending = options.pending ?? new Set<string>();
  return {
    coordinator: {
      open,
      releaseOrigin: () => {},
      isPending: (recipientId: string) => pending.has(recipientId),
      subscribePending: () => () => {},
      error: () => null,
      subscribeError: () => () => {},
      setDeps: () => {},
      dispose: () => {},
    },
    origin: testOrigin,
    open,
  };
}

/** The origin every row in these tests opens on behalf of. */
const testOrigin = "panel-test-origin";

/** The roster row for a person, found by the action it offers. */
function participantAction(displayName: string) {
  return screen.getByRole("button", { name: new RegExp(`^Abrir conversa com ${displayName}\\.`) });
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

  it("never shows a channel's visibility or a channel's empty description", () => {
    renderGroupPanel(groupDetails());

    // A group is neither public nor private. It does have a description
    // (issue #894), but the absence is worded for a group — a panel that said
    // "canal" here would name the wrong aggregate.
    expect(screen.queryByText(/Canal público/)).not.toBeInTheDocument();
    expect(screen.queryByText(/Canal privado/)).not.toBeInTheDocument();
    expect(screen.getByTestId("chat-details-description")).toHaveTextContent(
      "Este grupo ainda não tem descrição.",
    );
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
    // Unlike the channel panel, presence decorates a row and never removes it —
    // and with the store silent there is no decoration to draw (RF-58/CQ-3).
    expect(rows).toHaveLength(2);
    expect(screen.getByText("Desconectado")).toBeInTheDocument();
    expect(screen.getAllByText("Participante")).toHaveLength(2);
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
    // Asserted by identity rather than by position: two people with the same
    // name are separated by their ids (issue #895), so which of them the
    // deterministic tiebreak puts first is not what this test is about. Exactly
    // one of the two is the viewer, and it is the one whose id matches.
    expect(rows).toHaveLength(2);
    expect(rows.filter((row) => within(row).queryByText("Você") !== null)).toHaveLength(1);
    expect(
      within(rows.find((row) => within(row).queryByText("Você") !== null)!).queryByRole("button"),
    ).toBeNull();
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

  it("reports a participant total larger than the preview without offering to list it", () => {
    renderGroupPanel(
      groupDetails({
        participants: Array.from({ length: 3 }, (_, index) => ({
          userId: `user-${index}`,
          displayName: `Participante ${index + 1}`,
        })),
        participantCount: 40,
      }),
    );

    expect(screen.getByRole("heading", { name: "Participantes (40)" })).toBeInTheDocument();
    expect(
      within(screen.getByRole("list", { name: "Participantes do grupo" })).getAllByRole("listitem"),
    ).toHaveLength(3);
    expect(screen.queryByRole("button", { name: /Ver todos/ })).not.toBeInTheDocument();
  });

  it("expands the participant list in the group's own vocabulary", async () => {
    renderGroupPanel(
      groupDetails({
        participants: Array.from({ length: 7 }, (_, index) => ({
          userId: `user-${index}`,
          displayName: `Participante ${index + 1}`,
        })),
        participantCount: 7,
      }),
    );

    const list = () => screen.getByRole("list", { name: "Participantes do grupo" });
    expect(within(list()).getAllByRole("listitem")).toHaveLength(5);
    expect(screen.getByRole("heading", { name: "Participantes (7)" })).toBeInTheDocument();

    const toggle = screen.getByRole("button", { name: /Ver todos Participantes/ });
    expect(toggle).not.toBeDisabled();
    // The control that used to state why it could do nothing now does the thing.
    expect(toggle).not.toHaveAttribute("aria-disabled");
    await userEvent.click(toggle);

    expect(within(list()).getAllByRole("listitem")).toHaveLength(7);
    expect(screen.getByRole("button", { name: /Mostrar menos Participantes/ })).toBeInTheDocument();
    expect(screen.queryByText(/ainda não está disponível nesta versão/)).not.toBeInTheDocument();
  });

  it("shows group wording while loading and on error", () => {
    const { unmount } = render(
      <ConversationDetailsPanel
        kind="group"
        state={{
          details: { status: "loading" },
          files: { status: "loading" },
          roster: { status: "loading" },
          reload: vi.fn(),
        }}
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
        state={{
          details: { status: "error" },
          files: { status: "error" },
          roster: { status: "loading" },
          reload: vi.fn(),
        }}
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
        roster: { status: "loading" },
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

/**
 * The roster's navigation (issue #895).
 *
 * The open-DM flow itself — the self guard, the per-recipient dedupe, the abort
 * and generation bookkeeping, the 404 copy — belongs to useAuthorDM and is
 * proved by its own tests. What is proved here is the panel's half of the
 * contract: who gets an activatable row, what activating one asks for, and that
 * nothing else in the section is disturbed by a failure.
 */
describe("ConversationDetailsPanel — roster: abrir conversa", () => {
  const people = [
    { userId: "user-ana", displayName: "Ana Lima" },
    { userId: currentUserId, displayName: "Álvaro Neto" },
    { userId: "user-bruno", displayName: "Bruno Sá" },
  ];

  it("asks the shared flow for the participant that was activated, by id", async () => {
    const openDM = fakeOpenDM();
    renderGroupPanel(
      groupDetails({ participants: people, participantCount: 3 }),
      currentUserId,
      vi.fn(),
      openDM,
    );

    await userEvent.click(participantAction("Bruno Sá"));

    expect(openDM.open).toHaveBeenCalledTimes(1);
    // The id, never the name and never a route assembled here: the destination
    // is the conversation the server answers with. The second argument is this
    // host's claim on the operation — the row never handles one itself.
    expect(openDM.open).toHaveBeenCalledWith("user-bruno", testOrigin);
  });

  it("names the action, and carries the status the row shows", () => {
    const openDM = fakeOpenDM();
    renderGroupPanel(
      groupDetails({ participants: people, participantCount: 3 }),
      currentUserId,
      vi.fn(),
      openDM,
    );

    // With the store silent there is no status to carry, so the name is the
    // action and the person's role — never a raw id.
    const action = participantAction("Ana Lima");
    expect(action).toHaveAccessibleName("Abrir conversa com Ana Lima. Participante");
    expect(action.getAttribute("aria-label")).not.toContain("user-ana");
  });

  it("activates from the avatar and the name alike, because they are one control", async () => {
    const openDM = fakeOpenDM();
    renderGroupPanel(
      groupDetails({ participants: people, participantCount: 3 }),
      currentUserId,
      vi.fn(),
      openDM,
    );

    const action = participantAction("Ana Lima");
    await userEvent.click(within(action).getByTestId("chat-details-member-avatar"));
    await userEvent.click(within(action).getByText("Ana Lima"));

    expect(openDM.open).toHaveBeenCalledTimes(2);
    expect(openDM.open).toHaveBeenNthCalledWith(2, "user-ana", testOrigin);
  });

  it("is reachable and activatable by keyboard, with Enter and with Space", async () => {
    const openDM = fakeOpenDM();
    renderGroupPanel(
      groupDetails({ participants: people, participantCount: 3 }),
      currentUserId,
      vi.fn(),
      openDM,
    );

    const action = participantAction("Ana Lima");
    await tabUntilFocused(action);
    expect(action).toHaveFocus();

    await userEvent.keyboard("{Enter}");
    await userEvent.keyboard(" ");

    // A <button> is what gives both keys for free; that is the reason the row is
    // one rather than an <li> with an onClick.
    expect(openDM.open).toHaveBeenCalledTimes(2);
    expect(openDM.open).toHaveBeenNthCalledWith(1, "user-ana", testOrigin);
    expect(openDM.open).toHaveBeenNthCalledWith(2, "user-ana", testOrigin);
  });

  it("offers no action on the viewer's own row", () => {
    const openDM = fakeOpenDM();
    renderGroupPanel(
      groupDetails({ participants: people, participantCount: 3 }),
      currentUserId,
      vi.fn(),
      openDM,
    );

    expect(
      screen.queryByRole("button", { name: /Abrir conversa com Álvaro Neto/ }),
    ).not.toBeInTheDocument();
    // The row is still there, still says who it is, and still says it is you.
    const rows = within(screen.getByRole("list", { name: "Participantes do grupo" })).getAllByRole(
      "listitem",
    );
    const own = rows.find((row) => within(row).queryByText("Você") !== null)!;
    expect(within(own).getByText("Álvaro Neto")).toBeInTheDocument();
    expect(within(own).queryByRole("button")).toBeNull();
  });

  it("shows no action at all when the host has not wired the flow", () => {
    renderGroupPanel(groupDetails({ participants: people, participantCount: 3 }));

    expect(screen.queryByRole("button", { name: /Abrir conversa com/ })).not.toBeInTheDocument();
    // Honest rather than broken: three rows, none of them pretending.
    expect(
      within(screen.getByRole("list", { name: "Participantes do grupo" })).getAllByRole("listitem"),
    ).toHaveLength(3);
  });

  it("marks a recipient being resolved as busy, without leaving the tab order", async () => {
    const openDM = fakeOpenDM({ pending: new Set(["user-ana"]) });
    renderGroupPanel(
      groupDetails({ participants: people, participantCount: 3 }),
      currentUserId,
      vi.fn(),
      openDM,
    );

    const pending = participantAction("Ana Lima");
    expect(pending).toHaveAttribute("aria-busy", "true");
    // Never `disabled`: that would move focus out from under whoever just
    // pressed it, and repeating the request is refused by the flow anyway.
    expect(pending).toBeEnabled();
    await tabUntilFocused(pending);
    expect(pending).toHaveFocus();

    expect(participantAction("Bruno Sá")).toHaveAttribute("aria-busy", "false");
  });

  it("keeps handing the same id to the flow on rapid repeated activation", async () => {
    // Deduplication lives in the flow, which refuses a recipient it is already
    // resolving. The panel's part is to keep addressing the same person rather
    // than, say, resolving a row index that reordering could move.
    const openDM = fakeOpenDM();
    renderGroupPanel(
      groupDetails({ participants: people, participantCount: 3 }),
      currentUserId,
      vi.fn(),
      openDM,
    );

    const action = participantAction("Bruno Sá");
    await userEvent.click(action);
    await userEvent.click(action);
    await userEvent.click(action);

    expect(openDM.open.mock.calls).toEqual([
      ["user-bruno", testOrigin],
      ["user-bruno", testOrigin],
      ["user-bruno", testOrigin],
    ]);
  });

  it("leaves a refusal to the shell, and keeps the roster it was raised from", () => {
    // The flow has one owner and one place that reports a refusal (issue #895):
    // this panel and the conversation behind it are both on screen, and drawing
    // the sentence here as well announced one failure twice. What the panel
    // must do is survive the failure, which is asserted here; that exactly one
    // alert exists is asserted where both surfaces are mounted together, in
    // ChatMessageArea.test.tsx.
    const openDM = fakeOpenDM();
    renderGroupPanel(
      groupDetails({ participants: people, participantCount: 3 }),
      currentUserId,
      vi.fn(),
      openDM,
    );

    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
    // The rows the reader was looking at are exactly where they were.
    expect(
      within(screen.getByRole("list", { name: "Participantes do grupo" })).getAllByRole("listitem"),
    ).toHaveLength(3);
    expect(screen.getByRole("heading", { name: "Participantes (3)" })).toBeInTheDocument();
    // And still activatable: a refusal is not a dead section.
    expect(participantAction("Bruno Sá")).toBeEnabled();
  });

  it("gives a channel member the same navigable row", async () => {
    // The channel roster is still blocked on issue #877's membership contract,
    // but a member the server already vouched for is someone this user may open a
    // conversation with — navigation depends on no roster contract at all.
    const openDM = fakeOpenDM();
    renderPanel({
      openDM,
      state: state({
        details: {
          status: "ready",
          data: channelDetails({
            onlineMembers: [
              { userId: "user-ana", displayName: "Ana Lima", role: "member", presence: "online" },
            ],
            onlineCount: 1,
            memberCount: 9,
          }),
        },
      }),
    });

    await userEvent.click(
      screen.getByRole("button", { name: "Abrir conversa com Ana Lima. Membro" }),
    );

    expect(openDM.open).toHaveBeenCalledWith("user-ana", testOrigin);
  });
});

/**
 * What the roster shows about a person, and what it refuses to show.
 */
describe("ConversationDetailsPanel — roster: identidade e ordem", () => {
  it("orders by the documented fallback when the presence store is silent", () => {
    renderGroupPanel(
      groupDetails({
        participants: [
          { userId: "u-zoe", displayName: "Zoe" },
          { userId: "u-alv", displayName: "Álvaro" },
          { userId: "u-bea", displayName: "Beatriz" },
        ],
        participantCount: 3,
      }),
    );

    const names = within(screen.getByRole("list", { name: "Participantes do grupo" }))
      .getAllByRole("listitem")
      .map((row) => within(row).getByText(/^(Zoe|Álvaro|Beatriz)$/).textContent);
    // Accent-folded, so the accented name is not exiled past Z.
    expect(names).toEqual(["Álvaro", "Beatriz", "Zoe"]);
  });

  it("keeps only five rows compact and restores five on collapse", async () => {
    renderGroupPanel(
      groupDetails({
        participants: Array.from({ length: 8 }, (_, index) => ({
          userId: `user-${index}`,
          displayName: `Participante ${index + 1}`,
        })),
        participantCount: 8,
      }),
    );

    const list = () => screen.getByRole("list", { name: "Participantes do grupo" });
    expect(within(list()).getAllByRole("listitem")).toHaveLength(5);

    await userEvent.click(screen.getByRole("button", { name: /Ver todos Participantes/ }));
    expect(within(list()).getAllByRole("listitem")).toHaveLength(8);

    await userEvent.click(screen.getByRole("button", { name: /Mostrar menos Participantes/ }));
    expect(within(list()).getAllByRole("listitem")).toHaveLength(5);
  });

  it("gives an offline participant one of the five compact slots", () => {
    // The roster is membership, not presence: the group's own contract lists
    // every active participant and this list does not thin it out.
    renderGroupPanel(
      groupDetails({
        participants: [
          { userId: "u-1", displayName: "Ana", presence: "offline" },
          { userId: "u-2", displayName: "Bruno", presence: "offline" },
        ],
        participantCount: 2,
      }),
    );

    expect(
      within(screen.getByRole("list", { name: "Participantes do grupo" })).getAllByRole("listitem"),
    ).toHaveLength(2);
    expect(screen.getByRole("heading", { name: "Participantes (2)" })).toBeInTheDocument();
  });

  it("never falls back to a user id when the name is missing", () => {
    renderGroupPanel(
      groupDetails({
        participants: [{ userId: "7c9e6679-7425-40de-944b-e07fc1f90ae7", displayName: "" }],
        participantCount: 1,
      }),
    );

    const row = within(screen.getByRole("list", { name: "Participantes do grupo" })).getByRole(
      "listitem",
    );
    expect(row).not.toHaveTextContent("7c9e6679");
    // The row still exists and still says what the domain calls this person.
    expect(within(row).getByText("Participante")).toBeInTheDocument();
  });

  it("offers Ver todos only when the preview really is the whole group", async () => {
    // CASE A: the client holds everybody, so expanding shows everybody and the
    // control may say so.
    renderGroupPanel(
      groupDetails({
        participants: Array.from({ length: 8 }, (_, index) => ({
          userId: `user-${index}`,
          displayName: `Participante ${index + 1}`,
        })),
        participantCount: 8,
      }),
    );

    const list = () => screen.getByRole("list", { name: "Participantes do grupo" });
    expect(within(list()).getAllByRole("listitem")).toHaveLength(5);
    expect(screen.queryByTestId("chat-details-roster-shortfall")).not.toBeInTheDocument();

    await userEvent.click(screen.getByRole("button", { name: /Ver todos Participantes/ }));
    expect(within(list()).getAllByRole("listitem")).toHaveLength(8);
    expect(screen.getByRole("button", { name: /Mostrar menos Participantes/ })).toBeInTheDocument();
  });

  it("says Mostrar mais, and how much it holds, when the group is larger than the preview", async () => {
    // CASE B: the server's own cap. There is no route that lists the other ten
    // (GET /dm/{id}/details is the only participant source and it is capped),
    // so the control may not offer them and the section says what it has.
    renderGroupPanel(
      groupDetails({
        participants: Array.from({ length: 30 }, (_, index) => ({
          userId: `user-${index}`,
          displayName: `Participante ${String(index + 1).padStart(2, "0")}`,
        })),
        participantCount: 40,
      }),
    );

    const list = () => screen.getByRole("list", { name: "Participantes do grupo" });
    expect(within(list()).getAllByRole("listitem")).toHaveLength(5);
    expect(screen.queryByRole("button", { name: /Ver todos/ })).not.toBeInTheDocument();
    expect(screen.getByRole("heading", { name: "Participantes (40)" })).toBeInTheDocument();
    expect(screen.getByTestId("chat-details-roster-shortfall")).toHaveTextContent(
      "30 de 40 participantes carregados.",
    );

    // The thirty it does hold stay reachable — the honest label is not a reason
    // to hide rows 6..30.
    await userEvent.click(screen.getByRole("button", { name: /Mostrar mais Participantes/ }));
    expect(within(list()).getAllByRole("listitem")).toHaveLength(30);

    // Nothing is invented about the missing ten: not offline, not unavailable.
    const section = screen.getByRole("heading", { name: "Participantes (40)" }).closest("section")!;
    expect(section).not.toHaveTextContent(/indisponí/i);
    expect(section).not.toHaveTextContent(/Offline/);
  });

  it("offers no control at all when the whole group fits in the compact state", () => {
    // CASE C: five or fewer, all held. Nothing is hidden, so nothing expands.
    renderGroupPanel(
      groupDetails({
        participants: Array.from({ length: 4 }, (_, index) => ({
          userId: `user-${index}`,
          displayName: `Participante ${index + 1}`,
        })),
        participantCount: 4,
      }),
    );

    expect(
      within(screen.getByRole("list", { name: "Participantes do grupo" })).getAllByRole("listitem"),
    ).toHaveLength(4);
    expect(
      screen.queryByRole("button", { name: /Ver todos|Mostrar mais/ }),
    ).not.toBeInTheDocument();
    expect(screen.queryByTestId("chat-details-roster-shortfall")).not.toBeInTheDocument();
  });

  it("leaves the other sections of the panel on the default wording", () => {
    // CASE D: the label override is the roster's, not the primitive's. The files
    // section shares the same component and is untouched by it.
    renderGroupPanel(
      groupDetails({
        participants: Array.from({ length: 30 }, (_, index) => ({
          userId: `user-${index}`,
          displayName: `Participante ${String(index + 1).padStart(2, "0")}`,
        })),
        participantCount: 40,
      }),
      currentUserId,
      vi.fn(),
      undefined,
      Array.from({ length: 7 }, (_, index) =>
        attachment({ id: `file-${index}`, filename: `arquivo-${index}.pdf` }),
      ),
    );

    expect(screen.getByRole("button", { name: /Mostrar mais Participantes/ })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /Ver todos Arquivos recentes/ })).toBeInTheDocument();
  });

  it("renders the avatar the server vouched for, and initials when there is none", () => {
    renderGroupPanel(
      groupDetails({
        participants: [
          { userId: "u-1", displayName: "Ana Lima", avatarUrl: "/media/avatars/ana.png" },
          { userId: "u-2", displayName: "Bruno Sá" },
        ],
        participantCount: 2,
      }),
    );

    const [withPhoto, withInitials] = within(
      screen.getByRole("list", { name: "Participantes do grupo" }),
    ).getAllByTestId("chat-details-member-avatar");

    // chatApi already rejected anything that is not a safe same-origin target,
    // so the only URL that reaches here is one the client vouched for. The
    // empty alt is what makes it `presentation` rather than an image with a
    // name: the person's name beside it is the accessible text, and a second
    // one inside the avatar would be announced twice.
    const image = within(withPhoto).getByRole("presentation", { hidden: true });
    expect(image).toHaveAttribute("src", "/media/avatars/ana.png");
    expect(image).toHaveAttribute("referrerpolicy", "no-referrer");

    // No URL, so initials — never the user id, and never a broken image.
    expect(withInitials).toHaveTextContent("B");
    expect(within(withInitials).queryByRole("presentation", { hidden: true })).toBeNull();
    expect(withInitials).not.toHaveTextContent("u-2");
  });

  it("shows nothing about presence while the store has said nothing", () => {
    renderGroupPanel(
      groupDetails({
        participants: [{ userId: "u-1", displayName: "Ana", presence: "online" }],
        participantCount: 1,
      }),
    );

    // The HTTP payload claims "online" and is deliberately not read (RF-58):
    // no dot, and no word after the role.
    expect(screen.queryByTestId("presence-dot")).not.toBeInTheDocument();
    expect(screen.getByText("Participante")).toBeInTheDocument();
    expect(screen.queryByText(/Participante · /)).not.toBeInTheDocument();
  });
});

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
  it("shows the other participant's name and role", () => {
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
    // The presence badge belongs to the realtime store, which has said nothing
    // here, so it is absent rather than repeating a fetched field (RF-58/CQ-3).
    expect(screen.queryByTestId("chat-details-profile-status")).not.toBeInTheDocument();
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
        state={{
          details: { status: "loading" },
          files: { status: "loading" },
          roster: { status: "loading" },
          reload: vi.fn(),
        }}
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
        state={{
          details: { status: "error" },
          files: { status: "loading" },
          roster: { status: "loading" },
          reload: vi.fn(),
        }}
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
          roster: { status: "loading" },
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
          roster: { status: "loading" },
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
      data: channelDetails({ canAddMembers: true, canManageMembers: true, ...overrides }),
    },
    files: { status: "ready" as const, data: [] },
    roster: { status: "loading" as const },
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
    roster: { status: "loading" as const },
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
  it("offers the action when the server says the caller may add members", () => {
    renderPanel({ state: state({ details: readyChannel({ canAddMembers: true }).details }) });

    expect(screen.getByTestId("chat-details-add-members")).toBeEnabled();
    expect(screen.getByTestId("chat-details-add-members")).toHaveTextContent("Adicionar membros");
  });

  it.each([
    ["public", "public" as const],
    ["private", "private" as const],
  ])("offers the action on a %s channel", (_label, type) => {
    renderPanel({ state: state({ details: readyChannel({ type, canAddMembers: true }).details }) });

    expect(screen.getByTestId("chat-details-add-members")).toBeInTheDocument();
  });

  it("offers the action on a group, with the group's wording", () => {
    renderGroupPanel(groupDetails({ canManageMembers: true }));

    expect(screen.getByTestId("chat-details-add-members")).toHaveTextContent(
      "Adicionar participantes",
    );
  });

  it("offers add without widening administrative management", () => {
    renderPanel({
      state: state({
        details: {
          status: "ready",
          data: channelDetails({
            canAddMembers: true,
            canManageMembers: false,
            canRemoveMembers: false,
          }),
        },
      }),
    });

    expect(screen.getByTestId("chat-details-add-members")).toBeInTheDocument();
    expect(screen.queryByTestId("chat-details-remove-member")).not.toBeInTheDocument();
  });

  it("hides the action when the server withholds the add capability", () => {
    renderPanel({
      state: state({
        details: { status: "ready", data: channelDetails({ canAddMembers: false }) },
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
          roster: { status: "loading" },
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

// ── Inline rename (issue #893) ──────────────────────────────────────────────
//
// The panel's own half of the feature: which affordance it draws, what the
// editor does, and what it refuses to do to the rest of the UI. *Whether* a
// given target may be renamed at all is conversationRename's decision and is
// tested there — here the presence or absence of `onRename` stands for the
// answer it already gave.

function renderRenamePanel(
  overrides: Partial<Parameters<typeof ConversationDetailsPanel>[0]> = {},
) {
  const onRename = vi.fn().mockResolvedValue(undefined);
  const reload = vi.fn();
  const rendered = render(
    <ConversationDetailsPanel
      kind="channel"
      state={state({ reload })}
      currentUserId={currentUserId}
      latestPin={null}
      onRename={onRename}
      onClose={vi.fn()}
      {...overrides}
    />,
  );
  return { onRename, reload, ...rendered };
}

/** Opens the editor the way a user does, and hands back the field. */
async function openEditor(user: ReturnType<typeof userEvent.setup>, label = "Renomear canal") {
  await user.click(screen.getByRole("button", { name: label }));
  return screen.getByRole("textbox", { name: "Nome do canal" });
}

describe("ConversationDetailsPanel — renomear inline: a ação", () => {
  it("offers the rename control on a channel the caller may rename", () => {
    renderRenamePanel();

    expect(screen.getByRole("button", { name: "Renomear canal" })).toBeInTheDocument();
    expect(screen.getByTestId("chat-details-channel-name")).toHaveTextContent("Infraestrutura");
  });

  // The one absent value covers every reason there is: no capability, the
  // general channel, and a host with no mutation wired.
  it("shows the name without any control when the caller may not rename", () => {
    renderRenamePanel({ onRename: undefined });

    expect(screen.getByTestId("chat-details-channel-name")).toHaveTextContent("Infraestrutura");
    expect(screen.queryByRole("button", { name: /Renomear/ })).not.toBeInTheDocument();
  });

  it("offers the group vocabulary on a group", () => {
    renderRenamePanel({
      kind: "group",
      state: {
        details: { status: "ready", data: groupDetails({ name: "Time de Infra" }) },
        files: { status: "ready", data: [] },
        roster: { status: "loading" },
        reload: vi.fn(),
      },
    });

    expect(screen.getByRole("button", { name: "Renomear grupo" })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Renomear canal" })).not.toBeInTheDocument();
  });

  // A 1:1 has no name of its own — its title is the counterpart's, resolved per
  // viewer — so the profile panel has no name field to grow a rename from.
  it("never renders a name field on a 1:1 profile", () => {
    renderRenamePanel({
      kind: "direct",
      state: {
        details: { status: "ready", data: directDetails() },
        files: { status: "ready", data: [] },
        roster: { status: "loading" },
        reload: vi.fn(),
      },
    });

    expect(screen.queryByRole("button", { name: /Renomear/ })).not.toBeInTheDocument();
    expect(screen.queryByTestId("chat-details-channel-name")).not.toBeInTheDocument();
    expect(screen.queryByTestId("chat-details-group-name")).not.toBeInTheDocument();
  });

  it("shows no control while the details are still loading", () => {
    renderRenamePanel({ state: state({ details: { status: "loading" } }) });

    expect(screen.queryByRole("button", { name: /Renomear/ })).not.toBeInTheDocument();
  });
});

describe("ConversationDetailsPanel — renomear inline: entrar e cancelar", () => {
  it("opens an inline field seeded with the persisted name, focused, and asks nothing", async () => {
    const user = userEvent.setup();
    const { onRename } = renderRenamePanel();

    const field = await openEditor(user);

    expect(field).toHaveValue("Infraestrutura");
    expect(field).toHaveFocus();
    // Inline, not the sidebar's modal.
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    expect(onRename).not.toHaveBeenCalled();
  });

  it("discards the draft on Escape and restores the persisted name", async () => {
    const user = userEvent.setup();
    const { onRename } = renderRenamePanel();

    const field = await openEditor(user);
    await user.clear(field);
    await user.type(field, "Plataforma");
    await user.keyboard("{Escape}");

    expect(onRename).not.toHaveBeenCalled();
    expect(screen.queryByRole("textbox", { name: "Nome do canal" })).not.toBeInTheDocument();
    expect(screen.getByTestId("chat-details-channel-name")).toHaveTextContent("Infraestrutura");
  });

  // Escape belongs to the editor while it is open; the panel must not close
  // underneath it.
  it("keeps the panel open when Escape cancels the editor", async () => {
    const user = userEvent.setup();
    const onClose = vi.fn();
    renderRenamePanel({ onClose });

    const field = await openEditor(user);
    await user.type(field, "x");
    await user.keyboard("{Escape}");

    expect(onClose).not.toHaveBeenCalled();
  });

  it("cancels through the cancel control and returns focus to the rename control", async () => {
    const user = userEvent.setup();
    const { onRename } = renderRenamePanel();

    await openEditor(user);
    await user.click(screen.getByRole("button", { name: "Cancelar a renomeação do canal" }));

    expect(onRename).not.toHaveBeenCalled();
    expect(screen.getByRole("button", { name: "Renomear canal" })).toHaveFocus();
  });

  it("reopens with the persisted name, not the abandoned draft, and without the old error", async () => {
    const user = userEvent.setup();
    const onRename = vi.fn().mockRejectedValue(new ApiRequestError(403, "forbidden", "forbidden"));
    renderRenamePanel({ onRename });

    const field = await openEditor(user);
    await user.clear(field);
    await user.type(field, "Plataforma{Enter}");
    expect(await screen.findByRole("alert")).toBeInTheDocument();

    await user.keyboard("{Escape}");
    const reopened = await openEditor(user);

    expect(reopened).toHaveValue("Infraestrutura");
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
  });
});

describe("ConversationDetailsPanel — renomear inline: confirmar", () => {
  it("confirms with Enter, trims, and never adopts the typed name itself", async () => {
    const user = userEvent.setup();
    const { onRename, reload } = renderRenamePanel();

    const field = await openEditor(user);
    await user.clear(field);
    await user.type(field, "   Plataforma   {Enter}");

    await waitFor(() => expect(onRename).toHaveBeenCalledWith("Plataforma"));
    expect(onRename).toHaveBeenCalledTimes(1);
    await waitFor(() =>
      expect(screen.queryByRole("textbox", { name: "Nome do canal" })).not.toBeInTheDocument(),
    );
    // Still the persisted name: convergence is the canonical list moving and
    // useReloadOnRename refetching it, never a write-back from the editor.
    // The editor asks for no reload of its own (CQ-893-03).
    expect(screen.getByTestId("chat-details-channel-name")).toHaveTextContent("Infraestrutura");
    expect(reload).not.toHaveBeenCalled();
  });

  it("confirms through the confirm control", async () => {
    const user = userEvent.setup();
    const { onRename } = renderRenamePanel();

    const field = await openEditor(user);
    await user.clear(field);
    await user.type(field, "Plataforma");
    await user.click(screen.getByRole("button", { name: "Salvar novo nome do canal" }));

    await waitFor(() => expect(onRename).toHaveBeenCalledWith("Plataforma"));
  });

  it("refuses a whitespace-only name locally and says so on the field", async () => {
    const user = userEvent.setup();
    const { onRename } = renderRenamePanel();

    const field = await openEditor(user);
    await user.clear(field);
    await user.type(field, "   {Enter}");

    expect(onRename).not.toHaveBeenCalled();
    const error = screen.getByRole("alert");
    expect(error).toHaveTextContent("Escolha um nome para este canal.");
    expect(field).toHaveAttribute("aria-invalid", "true");
    expect(field).toHaveAttribute("aria-describedby", error.id);
    expect(field).toHaveFocus();
  });

  // A write that would store the same string is a request with no change.
  it("asks for nothing when the trimmed name is the persisted one", async () => {
    const user = userEvent.setup();
    const { onRename, reload } = renderRenamePanel();

    const field = await openEditor(user);
    await user.clear(field);
    await user.type(field, "  Infraestrutura  {Enter}");

    expect(onRename).not.toHaveBeenCalled();
    // No request, so nothing moves the canonical list and nothing is
    // refetched: a no-op rename costs exactly nothing (CQ-893-03).
    expect(reload).not.toHaveBeenCalled();
    expect(screen.queryByRole("textbox", { name: "Nome do canal" })).not.toBeInTheDocument();
  });

  it("accepts an ASCII name at the channel cap", async () => {
    const user = userEvent.setup();
    const { onRename } = renderRenamePanel();

    const atCap = "a".repeat(conversationNameMaxCodePoints.channel);
    const field = await openEditor(user);
    await user.clear(field);
    await user.type(field, atCap);
    await user.keyboard("{Enter}");

    await waitFor(() => expect(onRename).toHaveBeenCalledWith(atCap));
  });

  // ── The cap is in code points, the field is not (CQ-893-01) ──────────────
  //
  // 100 emoji is 100 code points and 200 UTF-16 code units. The backend
  // accepts it; a `maxLength={100}` field silently refused half of it, and
  // `String.prototype.length` would have made the same mistake. These cases
  // pin both sides of both boundaries.

  it("accepts a channel name of exactly 100 emoji, whole and untruncated", async () => {
    const user = userEvent.setup();
    const { onRename } = renderRenamePanel();

    const atCap = "😀".repeat(100);
    const field = await openEditor(user);
    // Pasted rather than typed: 100 emoji through the keyboard is 200 events.
    fireEvent.change(field, { target: { value: atCap } });
    expect(field).toHaveValue(atCap);
    await user.keyboard("{Enter}");

    await waitFor(() => expect(onRename).toHaveBeenCalledWith(atCap));
    // Exactly what was typed reached the mutation — not 50 emoji, and nothing
    // cut at 100 UTF-16 units.
    expect((onRename.mock.calls[0][0] as string).length).toBe(200);
  });

  it("refuses a channel name of 101 emoji locally, keeping the draft", async () => {
    const user = userEvent.setup();
    const { onRename } = renderRenamePanel();

    const overCap = "😀".repeat(101);
    const field = await openEditor(user);
    fireEvent.change(field, { target: { value: overCap } });
    await user.keyboard("{Enter}");

    expect(onRename).not.toHaveBeenCalled();
    const error = screen.getByRole("alert");
    expect(error).toHaveTextContent("O nome do canal deve ter no máximo 100 caracteres.");
    expect(field).toHaveAttribute("aria-invalid", "true");
    expect(field).toHaveAttribute("aria-describedby", error.id);
    // Nothing was cut to fit; the user shortens it themselves.
    expect(field).toHaveValue(overCap);
    expect(field).toHaveFocus();
  });

  it("accepts a group name of exactly 120 emoji and refuses 121", async () => {
    const user = userEvent.setup();
    const onRename = vi.fn().mockResolvedValue(undefined);
    renderRenamePanel({
      kind: "group",
      onRename,
      state: {
        details: { status: "ready", data: groupDetails({ name: "Time de Infra" }) },
        files: { status: "ready", data: [] },
        roster: { status: "loading" },
        reload: vi.fn(),
      },
    });

    await user.click(screen.getByRole("button", { name: "Renomear grupo" }));
    const field = screen.getByRole("textbox", { name: "Nome do grupo" });

    const overCap = "😀".repeat(121);
    fireEvent.change(field, { target: { value: overCap } });
    await user.keyboard("{Enter}");
    expect(onRename).not.toHaveBeenCalled();
    expect(screen.getByRole("alert")).toHaveTextContent(
      "O nome do grupo deve ter no máximo 120 caracteres.",
    );

    const atCap = "😀".repeat(120);
    fireEvent.change(field, { target: { value: atCap } });
    await user.keyboard("{Enter}");
    await waitFor(() => expect(onRename).toHaveBeenCalledWith(atCap));
  });

  // The local check saves a round trip; it is not the authority. A name this
  // client considers fine can still come back refused.
  it("still renders the server's refusal for a name it let through", async () => {
    const user = userEvent.setup();
    const onRename = vi.fn().mockRejectedValue(new ApiRequestError(400, "bad_request", "nope"));
    renderRenamePanel({ onRename });

    const field = await openEditor(user);
    await user.clear(field);
    await user.type(field, "Plataforma{Enter}");

    expect(await screen.findByRole("alert")).toHaveTextContent(
      "Escolha um nome válido para esta conversa.",
    );
    expect(screen.getByRole("textbox", { name: "Nome do canal" })).toHaveValue("Plataforma");
  });
});

describe("ConversationDetailsPanel — renomear inline: pendente, erro e submit único", () => {
  /** A rename whose resolution the test controls. */
  function deferredRename() {
    let settle: { resolve: () => void; reject: (error: unknown) => void } | undefined;
    const onRename = vi.fn(
      () =>
        new Promise<void>((resolve, reject) => {
          settle = { resolve, reject: (error) => reject(error) };
        }),
    );
    return { onRename, settle: () => settle };
  }

  it("keeps the wait inside the editor and leaves the rest of the panel usable", async () => {
    const user = userEvent.setup();
    const { onRename, settle } = deferredRename();
    renderRenamePanel({ onRename });

    const field = await openEditor(user);
    await user.clear(field);
    await user.type(field, "Plataforma{Enter}");

    const confirm = screen.getByRole("button", { name: "Salvar novo nome do canal" });
    await waitFor(() => expect(confirm).toBeDisabled());
    expect(confirm).toHaveAttribute("aria-busy", "true");
    expect(field).toHaveAttribute("readonly");
    // The panel itself is untouched: its own controls still work.
    expect(screen.getByRole("button", { name: "Fechar detalhes do canal" })).toBeEnabled();

    await act(async () => {
      settle()?.resolve();
    });
  });

  it("stays open with the typed name when the server refuses, and retries", async () => {
    const user = userEvent.setup();
    const onRename = vi
      .fn()
      .mockRejectedValueOnce(new ApiRequestError(429, "rate_limited", "slow down"))
      .mockResolvedValueOnce(undefined);
    const { reload } = renderRenamePanel({ onRename });

    const field = await openEditor(user);
    await user.clear(field);
    await user.type(field, "Plataforma{Enter}");

    expect(await screen.findByRole("alert")).toHaveTextContent(/Muitas solicitações/);
    const retained = screen.getByRole("textbox", { name: "Nome do canal" });
    expect(retained).toHaveValue("Plataforma");
    // The persisted name is still what the panel states elsewhere.
    expect(screen.queryByText("Plataforma")).not.toBeInTheDocument();

    await user.keyboard("{Enter}");
    await waitFor(() => expect(onRename).toHaveBeenCalledTimes(2));
    await waitFor(() =>
      expect(screen.queryByRole("textbox", { name: "Nome do canal" })).not.toBeInTheDocument(),
    );
    // Neither the failure nor the retry asks the panel to refetch: that is
    // useReloadOnRename's job, on the canonical name moving (CQ-893-03).
    expect(reload).not.toHaveBeenCalled();
  });

  // The server's own message is never rendered: it echoes caller-controlled
  // text and may describe a resource the caller cannot see.
  it("renders a refusal from its status alone, never the server's message", async () => {
    const user = userEvent.setup();
    const onRename = vi
      .fn()
      .mockRejectedValue(new ApiRequestError(404, "not_found", "channel 9f2 in workspace acme"));
    renderRenamePanel({ onRename });

    const field = await openEditor(user);
    await user.clear(field);
    await user.type(field, "Plataforma{Enter}");

    expect(await screen.findByRole("alert")).toHaveTextContent(
      "Este canal não está mais disponível.",
    );
    expect(screen.queryByText(/workspace acme/)).not.toBeInTheDocument();
  });

  it("uses the group's vocabulary for a group's refusal", async () => {
    const user = userEvent.setup();
    const onRename = vi.fn().mockRejectedValue(new ApiRequestError(403, "forbidden", "forbidden"));
    renderRenamePanel({
      kind: "group",
      onRename,
      state: {
        details: { status: "ready", data: groupDetails({ name: "Time de Infra" }) },
        files: { status: "ready", data: [] },
        roster: { status: "loading" },
        reload: vi.fn(),
      },
    });

    await user.click(screen.getByRole("button", { name: "Renomear grupo" }));
    const field = screen.getByRole("textbox", { name: "Nome do grupo" });
    await user.clear(field);
    await user.type(field, "Plataforma{Enter}");

    expect(await screen.findByRole("alert")).toHaveTextContent(
      "Você não tem permissão para renomear este grupo.",
    );
  });

  it("sends one request however many times confirm is clicked", async () => {
    const user = userEvent.setup();
    const { onRename, settle } = deferredRename();
    renderRenamePanel({ onRename });

    const field = await openEditor(user);
    await user.clear(field);
    await user.type(field, "Plataforma");
    const confirm = screen.getByRole("button", { name: "Salvar novo nome do canal" });
    await user.click(confirm);
    await user.click(confirm);
    await user.click(confirm);

    expect(onRename).toHaveBeenCalledTimes(1);
    await act(async () => {
      settle()?.resolve();
    });
  });

  it("sends one request for a repeated Enter and for Enter plus a click", async () => {
    const user = userEvent.setup();
    const { onRename, settle } = deferredRename();
    renderRenamePanel({ onRename });

    const field = await openEditor(user);
    await user.clear(field);
    await user.type(field, "Plataforma");
    // A held key repeats; the repeat is not a second request.
    await user.keyboard("{Enter>3/}");
    await user.click(screen.getByRole("button", { name: "Salvar novo nome do canal" }));

    expect(onRename).toHaveBeenCalledTimes(1);
    await act(async () => {
      settle()?.resolve();
    });
  });

  // Enter is not always a confirmation: an IME sends one to commit a candidate
  // while composing, and a held key repeats. Neither is a request.
  it("ignores an Enter that is a composition commit or a key repeat", async () => {
    const user = userEvent.setup();
    const { onRename } = renderRenamePanel();

    const field = await openEditor(user);
    await user.clear(field);
    await user.type(field, "Plataforma");

    fireEvent.keyDown(field, { key: "Enter", isComposing: true });
    fireEvent.keyDown(field, { key: "Enter", repeat: true });

    expect(onRename).not.toHaveBeenCalled();
    expect(screen.getByRole("textbox", { name: "Nome do canal" })).toBeInTheDocument();
  });

  // Cancelling mid-write would hide whether the request landed, so the editor
  // holds until the answer arrives — and then closes on the user's next gesture.
  it("refuses to close while a rename is still in flight", async () => {
    const user = userEvent.setup();
    const { onRename, settle } = deferredRename();
    renderRenamePanel({ onRename });

    const field = await openEditor(user);
    await user.clear(field);
    await user.type(field, "Plataforma{Enter}");
    await waitFor(() => expect(onRename).toHaveBeenCalledTimes(1));

    await user.click(screen.getByRole("button", { name: "Cancelar a renomeação do canal" }));
    expect(screen.getByRole("textbox", { name: "Nome do canal" })).toBeInTheDocument();

    await act(async () => {
      settle()?.resolve();
    });
    await waitFor(() =>
      expect(screen.queryByRole("textbox", { name: "Nome do canal" })).not.toBeInTheDocument(),
    );
  });

  // Switching conversations unmounts the editor; the mutation for the previous
  // target still resolves, and nothing it carries may reach the new one.
  it("cannot leak a pending rename into the conversation opened after it", async () => {
    const user = userEvent.setup();
    const { onRename, settle } = deferredRename();
    const reload = vi.fn();
    const { rerender } = render(
      <ConversationDetailsPanel
        kind="channel"
        state={state({ reload })}
        currentUserId={currentUserId}
        latestPin={null}
        onRename={onRename}
        onClose={vi.fn()}
      />,
    );

    const field = await openEditor(user);
    await user.clear(field);
    await user.type(field, "Plataforma{Enter}");
    await waitFor(() => expect(onRename).toHaveBeenCalledTimes(1));

    // The reader moves to another channel. The panel is deliberately not
    // remounted, so only the editor's own key unmounts it.
    rerender(
      <ConversationDetailsPanel
        kind="channel"
        state={state({
          details: {
            status: "ready",
            data: channelDetails({ id: "ch-2", slug: "produto", name: "Produto" }),
          },
          reload,
        })}
        currentUserId={currentUserId}
        latestPin={null}
        onRename={onRename}
        onClose={vi.fn()}
      />,
    );

    await act(async () => {
      settle()?.resolve();
    });

    // B is in its own read state: no draft, no field, no error, its own name.
    expect(screen.getByTestId("chat-details-channel-name")).toHaveTextContent("Produto");
    expect(screen.queryByRole("textbox", { name: "Nome do canal" })).not.toBeInTheDocument();
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
    expect(screen.queryByText("Plataforma")).not.toBeInTheDocument();
    // And the panel showing B was not refetched by A's resolution.
    expect(reload).not.toHaveBeenCalled();
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
  });

  it("does not surface a rejection that lands after the conversation changed", async () => {
    const user = userEvent.setup();
    const { onRename, settle } = deferredRename();
    const { rerender } = render(
      <ConversationDetailsPanel
        kind="channel"
        state={state()}
        currentUserId={currentUserId}
        latestPin={null}
        onRename={onRename}
        onClose={vi.fn()}
      />,
    );

    const field = await openEditor(user);
    await user.clear(field);
    await user.type(field, "Plataforma{Enter}");
    await waitFor(() => expect(onRename).toHaveBeenCalledTimes(1));

    rerender(
      <ConversationDetailsPanel
        kind="channel"
        state={state({
          details: {
            status: "ready",
            data: channelDetails({ id: "ch-2", slug: "produto", name: "Produto" }),
          },
        })}
        currentUserId={currentUserId}
        latestPin={null}
        onRename={onRename}
        onClose={vi.fn()}
      />,
    );

    await act(async () => {
      settle()?.reject(new ApiRequestError(403, "forbidden", "forbidden"));
    });

    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
    expect(screen.getByTestId("chat-details-channel-name")).toHaveTextContent("Produto");
  });
});

// ── Bloco "Sobre": descrição, criação e criador (issue #894) ─────────────────
//
// The block states four facts about a conversation, and each of them has an
// absence that has to read as an absence. What it must never do is fill a gap
// with something technical: an identifier, part of one, or a value derived from
// somewhere else in the payload.

describe("ConversationDetailsPanel — Sobre: descrição", () => {
  it.each([
    [
      "canal",
      () =>
        renderPanel({
          state: state({
            details: {
              status: "ready",
              data: channelDetails({ description: "Infraestrutura e operações." }),
            },
          }),
        }),
    ],
    ["grupo", () => renderGroupPanel(groupDetails({ description: "Infraestrutura e operações." }))],
  ])("renders the persisted description of a %s under its own label", (_kind, renderIt) => {
    renderIt();

    expect(screen.getByRole("heading", { name: "Descrição" })).toBeInTheDocument();
    expect(screen.getByTestId("chat-details-description")).toHaveTextContent(
      "Infraestrutura e operações.",
    );
  });

  it("words the empty state for the aggregate it is describing", () => {
    const { unmount } = renderPanel();
    expect(screen.getByTestId("chat-details-description")).toHaveTextContent(
      "Este canal ainda não tem descrição.",
    );
    unmount();

    renderGroupPanel(groupDetails());
    expect(screen.getByTestId("chat-details-description")).toHaveTextContent(
      "Este grupo ainda não tem descrição.",
    );
  });

  // The description is server-side content and the only markup-shaped value in
  // the block, so the invariant is asserted payload-independently rather than
  // per-payload: the element holds the string verbatim and contains no element
  // children at all. That is only true of a React text node, and it holds for
  // any markup — including shapes no test enumerated.
  it.each([
    ["script tag", "<script>window.__pwned = true</script>"],
    ["img onerror", '<img src=x onerror="window.__pwned = true">'],
    ["svg onload", '<svg onload="window.__pwned = true"></svg>'],
    ["javascript: anchor", '<a href="javascript:window.__pwned = true">click</a>'],
    ["attribute breakout", "\"'><script>window.__pwned = true</script>"],
    ["template expression", "{{constructor.constructor('window.__pwned = true')()}}"],
    ["bare entities", "& < > \" ' `"],
  ])("renders a %s description as inert text", (_label, hostile) => {
    renderPanel({
      state: state({
        details: { status: "ready", data: channelDetails({ description: hostile }) },
      }),
    });

    const description = screen.getByTestId("chat-details-description");
    // Verbatim, character for character — not escaped-and-unescaped, not
    // stripped, not normalized.
    expect(description.textContent).toBe(hostile);
    // Nothing was parsed out of it. Checking for zero element children rather
    // than for a <script> or an <img> is what makes this hold for payloads the
    // list does not name.
    expect(description.querySelectorAll("*")).toHaveLength(0);
    expect((window as unknown as { __pwned?: boolean }).__pwned).toBeUndefined();
  });

  // The creator's name is `auth.users.full_name`/`display_name`, which a person
  // sets on themselves through PATCH /auth/me. Issue #894 renders it in a place
  // it was never rendered before, so the content behind this row is
  // attacker-controlled and the same inertness has to hold for it.
  //
  // This is also the guard for the navigable creator the issue anticipates: the
  // day the name becomes a link, an href built from it would fail here rather
  // than ship.
  it("renders a hostile creator name as inert text, in channel and in group", () => {
    const hostile = '<img src=x onerror="window.__pwned = true">';

    // The row carries one decorative icon of its own, so "the name contributed
    // no elements" is the assertion — not "the row has no elements".
    const contributedElements = (row: HTMLElement) =>
      [...row.querySelectorAll("*")].filter((el) => el.getAttribute("aria-hidden") !== "true");

    const { unmount } = renderPanel({
      state: state({
        details: {
          status: "ready",
          data: channelDetails({ creatorDisplayName: hostile }),
        },
      }),
    });
    const channelRow = screen.getByText(`Criado por ${hostile}`);
    expect(channelRow.textContent).toContain(hostile);
    expect(contributedElements(channelRow)).toHaveLength(0);
    unmount();

    renderGroupPanel(groupDetails({ creatorDisplayName: hostile }));
    const groupRow = screen.getByText(`Criado por ${hostile}`);
    expect(groupRow.textContent).toContain(hostile);
    expect(contributedElements(groupRow)).toHaveLength(0);

    expect((window as unknown as { __pwned?: boolean }).__pwned).toBeUndefined();
  });

  it("keeps accents, emoji and line breaks in a description", () => {
    const description = "Operações — ç, ã, ü 🚀\nSegunda linha";
    renderPanel({
      state: state({
        details: { status: "ready", data: channelDetails({ description }) },
      }),
    });

    // textContent keeps the newline; the break itself is CSS (white-space:
    // pre-wrap), never an interpreted <br>.
    expect(screen.getByTestId("chat-details-description").textContent).toBe(description);
  });
});

describe("ConversationDetailsPanel — Sobre: criador", () => {
  it("names the creator by display name for a channel and for a group", () => {
    const { unmount } = renderPanel({
      state: state({
        details: {
          status: "ready",
          data: channelDetails({ creatorDisplayName: "Álvaro Neto" }),
        },
      }),
    });
    expect(screen.getByText("Criado por Álvaro Neto")).toBeInTheDocument();
    unmount();

    renderGroupPanel(groupDetails({ creatorDisplayName: "Juliane Lino" }));
    expect(screen.getByText("Criado por Juliane Lino")).toBeInTheDocument();
  });

  it("falls back to a neutral state when the creator is unresolved", () => {
    const { unmount } = renderPanel();
    expect(screen.getByText("Criador não identificado")).toBeInTheDocument();
    expect(screen.queryByText(/Criado por/)).not.toBeInTheDocument();
    unmount();

    renderGroupPanel(groupDetails());
    expect(screen.getByText("Criador não identificado")).toBeInTheDocument();
  });

  it("never shows an identifier where the creator's name would go", () => {
    // The panel is handed a details object that still carries the conversation's
    // own id and every id in its preview, and no creator name. None of them may
    // become the creator.
    const creatorId = "11111111-2222-4333-8444-555555555555";
    renderPanel({
      state: state({
        details: {
          status: "ready",
          data: channelDetails({
            id: creatorId,
            onlineCount: 1,
            onlineMembers: [
              { userId: creatorId, displayName: "Álvaro", role: "member", presence: "online" },
            ],
          }),
        },
      }),
    });

    expect(screen.getByText("Criador não identificado")).toBeInTheDocument();
    // Not the id, and not a prefix of it either: a truncated UUID is still a
    // UUID on screen.
    expect(screen.queryByText(new RegExp(creatorId.slice(0, 8)))).not.toBeInTheDocument();
  });
});

describe("ConversationDetailsPanel — Sobre: data de criação", () => {
  it("formats the aggregate's own timestamp with the shared long-date format", () => {
    renderPanel({
      state: state({
        details: {
          status: "ready",
          data: channelDetails({ createdAt: "2026-08-31T12:00:00.000Z" }),
        },
      }),
    });

    expect(
      screen.getByText(`Criado em ${formatLongDate("2026-08-31T12:00:00.000Z")}`),
    ).toBeInTheDocument();
  });

  it.each([
    ["absent", ""],
    ["unparseable", "ontem de manhã"],
  ])(
    "reads as unavailable when the date is %s, never as a half-written sentence",
    (_case, value) => {
      renderPanel({
        state: state({
          details: { status: "ready", data: channelDetails({ createdAt: value }) },
        }),
      });

      expect(screen.getByText("Data de criação indisponível")).toBeInTheDocument();
      expect(screen.queryByText(/^Criado em\s*$/)).not.toBeInTheDocument();
    },
  );
});

describe("ConversationDetailsPanel — Sobre: contagem", () => {
  it.each([
    [1, "1 membro"],
    [6, "6 membros"],
  ])("a channel of %d reads %s", (memberCount, expected) => {
    renderPanel({
      state: state({
        details: { status: "ready", data: channelDetails({ memberCount }) },
      }),
    });

    expect(screen.getByText(expected)).toBeInTheDocument();
  });

  it.each([
    [1, "1 participante"],
    [6, "6 participantes"],
  ])("a group of %d reads %s", (participantCount, expected) => {
    renderGroupPanel(groupDetails({ participantCount }));

    expect(screen.getByText(expected)).toBeInTheDocument();
  });

  it("counts the server's total, never the preview it was given", () => {
    // Three independent numbers, deliberately all different: the size of the
    // conversation, how many are online, and how many rows the preview holds.
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

    expect(screen.getByText("40 membros")).toBeInTheDocument();
    expect(screen.queryByText("1 membro")).not.toBeInTheDocument();
    expect(screen.queryByText("6 membros")).not.toBeInTheDocument();
  });

  it("counts a group's total, never its capped participant preview", () => {
    renderGroupPanel(
      groupDetails({
        participantCount: 31,
        participants: [
          { userId: "u-1", displayName: "Ana" },
          { userId: "u-2", displayName: "Bruno" },
        ],
      }),
    );

    expect(screen.getByText("31 participantes")).toBeInTheDocument();
    expect(screen.queryByText("2 participantes")).not.toBeInTheDocument();
  });
});

describe("ConversationDetailsPanel — Sobre: a DM 1:1 não ganhou nada", () => {
  it("shows no conversation description, creator or count on a 1:1 profile", () => {
    // A direct conversation is a person, not a described conversation: issue
    // #894 added a block to the channel and group panels and nothing at all
    // here, and the profile must not have inherited any of it.
    renderProfilePanel(directDetails({ displayName: "Juliane Lino" }));

    expect(screen.queryByRole("heading", { name: "Descrição" })).not.toBeInTheDocument();
    expect(screen.queryByTestId("chat-details-description")).not.toBeInTheDocument();
    expect(screen.queryByText(/ainda não tem descrição/)).not.toBeInTheDocument();
    expect(screen.queryByText(/Criado por/)).not.toBeInTheDocument();
    expect(screen.queryByText("Criador não identificado")).not.toBeInTheDocument();
    expect(screen.queryByText(/participantes?$/)).not.toBeInTheDocument();
    expect(screen.queryByText(/membros?$/)).not.toBeInTheDocument();
  });
});
