/**
 * Attachment scan status on the client (RF-22).
 *
 * Everything here renders the **real** ConversationDetailsPanel. An earlier
 * version of this file built a small stand-in list with its own labels and its
 * own download button, and that made it worse than useless: it asserted that
 * "Verificado" was shown while the production panel deliberately suppressed the
 * badge for an approved file, and it asserted a download control the panel does
 * not have. A test that describes a UI nobody ships proves nothing about the
 * one people use.
 *
 * Two halves, matching where the behaviour actually lives:
 *
 *   - the panel, driven directly through its `state` prop, for what the three
 *     states look like and what they do not offer;
 *   - the panel driven by useConversationDetails, for the seam an
 *     attachment.status event lands in — the refetch is what updates the view,
 *     so the two have to be exercised together to prove it.
 */

import { act, render, screen, waitFor, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import ConversationDetailsPanel from "./ConversationDetailsPanel";
import { canShowPreview, isPreviewWorkPending } from "./useAttachmentPreview";
import {
  useConversationDetails,
  type ConversationDetailsState,
  type ConversationDetailsTarget,
} from "./useConversationDetails";
import type { ChannelAttachment, ChannelDetails } from "./chatTypes";

const { mockFetchChannelDetails, mockFetchConversationAttachments, mockFetchAttachmentPreview } =
  vi.hoisted(() => ({
    mockFetchChannelDetails: vi.fn(),
    mockFetchConversationAttachments: vi.fn(),
    mockFetchAttachmentPreview: vi.fn(),
  }));

vi.mock("./chatApi", () => ({
  fetchChannelDetails: (id: string, signal?: AbortSignal) => mockFetchChannelDetails(id, signal),
  fetchGroupDetails: vi.fn(),
  fetchDirectProfile: vi.fn(),
}));

vi.mock("./filesApi", () => ({
  fetchConversationAttachments: (
    target: { kind: "channel" | "dm"; id: string },
    limit: number,
    signal?: AbortSignal,
  ) => mockFetchConversationAttachments(target, limit, signal),
  fetchAttachmentPreview: (id: string, signal?: AbortSignal) =>
    mockFetchAttachmentPreview(id, signal),
  fetchAttachmentContent: () => Promise.reject(new Error("not used")),
}));

const currentUserId = "user-self";

/** The same shape ConversationDetailsPanel.test.tsx uses, so the panel renders
 * every section rather than throwing on a half-built projection. */
function channelDetails(): { kind: "channel" } & ChannelDetails {
  return {
    kind: "channel" as const,
    id: "ch-1",
    slug: "infra",
    name: "Infraestrutura",
    type: "public",
    createdAt: "2026-07-01T09:00:00.000Z",
    memberCount: 2,
    onlineCount: 0,
    onlineMembers: [],
    canManageMembers: false,
  };
}

function attachment(overrides: Partial<ChannelAttachment> = {}): ChannelAttachment {
  return {
    id: "a-1",
    filename: "relatorio.pdf",
    contentType: "application/pdf",
    size: 2048,
    status: "pending_scan",
    previewStatus: "pending",
    createdAt: "2026-08-07T12:00:00.000Z",
    ...overrides,
  };
}

/** The panel's state prop with a files section the caller chooses. */
function panelState(files: ChannelAttachment[]): ConversationDetailsState {
  return {
    details: { status: "ready", data: channelDetails() },
    files: { status: "ready", data: files },
    reload: vi.fn(),
  };
}

function renderPanel(files: ChannelAttachment[]) {
  return render(
    <ConversationDetailsPanel
      kind="channel"
      state={panelState(files)}
      currentUserId={currentUserId}
      latestPin={null}
      onClose={vi.fn()}
    />,
  );
}

/** The file row for an attachment, so assertions are scoped to it. */
function fileRow(attachmentId: string): HTMLElement {
  return screen.getByTestId(`chat-details-file-status-${attachmentId}`).closest("li")!;
}

beforeEach(() => {
  mockFetchChannelDetails.mockResolvedValue(channelDetails());
  mockFetchAttachmentPreview.mockResolvedValue(new Blob(["jpeg-bytes"]));
  let created = 0;
  vi.stubGlobal("URL", {
    ...URL,
    createObjectURL: vi.fn(() => `blob:preview-${++created}`),
    revokeObjectURL: vi.fn(),
  });
});

afterEach(() => {
  vi.unstubAllGlobals();
  vi.clearAllMocks();
});

// ── The three functional states, in the component that ships ────────────────

describe("os três estados no painel real", () => {
  it("mostra 'Em análise' enquanto o scan não decidiu", () => {
    renderPanel([attachment({ status: "pending_scan" })]);
    expect(screen.getByTestId("chat-details-file-status-a-1")).toHaveTextContent("Em análise");
  });

  it("mostra 'Verificado' para um anexo aprovado", () => {
    renderPanel([attachment({ status: "clean", previewStatus: "unsupported" })]);
    expect(screen.getByTestId("chat-details-file-status-a-1")).toHaveTextContent("Verificado");
  });

  it("mostra 'Reprovado' para um anexo bloqueado", () => {
    renderPanel([attachment({ status: "rejected", previewStatus: "unsupported" })]);
    expect(screen.getByTestId("chat-details-file-status-a-1")).toHaveTextContent("Reprovado");
  });

  // The regression this file exists for: an approved file used to render no
  // badge at all, so the only visible states meant "wait" and "blocked".
  it("distingue os três estados na mesma lista", () => {
    renderPanel([
      attachment({ id: "a-1", status: "pending_scan" }),
      attachment({ id: "a-2", status: "clean", previewStatus: "unsupported" }),
      attachment({ id: "a-3", status: "rejected", previewStatus: "unsupported" }),
    ]);

    const labels = ["a-1", "a-2", "a-3"].map(
      (id) => screen.getByTestId(`chat-details-file-status-${id}`).textContent,
    );
    expect(labels).toEqual(["Em análise", "Verificado", "Reprovado"]);
    // Distinguishable by more than text: each state carries its own modifier,
    // which is what the colour rules key off.
    expect(screen.getByTestId("chat-details-file-status-a-2").className).toContain(
      "chat-details__file-status--clean",
    );
  });
});

// ── Nothing that depends on an approval is offered before one ───────────────

describe("ações que dependem da aprovação", () => {
  it("não oferece link nem download para um anexo em análise", () => {
    renderPanel([attachment({ status: "pending_scan" })]);
    expect(within(fileRow("a-1")).queryAllByRole("link")).toHaveLength(0);
    expect(within(fileRow("a-1")).queryAllByRole("button")).toHaveLength(0);
  });

  it("não oferece link nem download para um anexo bloqueado", () => {
    renderPanel([attachment({ status: "rejected", previewStatus: "ready" })]);
    expect(within(fileRow("a-1")).queryAllByRole("link")).toHaveLength(0);
    expect(within(fileRow("a-1")).queryAllByRole("button")).toHaveLength(0);
  });

  // The preview route serves bytes derived from the same file, so requesting it
  // before the verdict would be the client acting as if the gate might not
  // apply. The server answers 403 either way; not asking is the point.
  it("não busca preview enquanto o scan não aprovou", () => {
    renderPanel([attachment({ status: "pending_scan", previewStatus: "ready" })]);
    expect(mockFetchAttachmentPreview).not.toHaveBeenCalled();
    expect(screen.queryByTestId("chat-details-file-thumb")).toBeNull();
  });

  it("não busca preview de um anexo bloqueado", () => {
    renderPanel([attachment({ status: "rejected", previewStatus: "ready" })]);
    expect(mockFetchAttachmentPreview).not.toHaveBeenCalled();
    expect(screen.queryByTestId("chat-details-file-thumb")).toBeNull();
  });

  it("busca o preview normalmente depois da aprovação", async () => {
    renderPanel([attachment({ status: "clean", previewStatus: "ready" })]);
    // The signal is the hook's own AbortController, so only the id is asserted.
    await waitFor(() =>
      expect(mockFetchAttachmentPreview).toHaveBeenCalledWith("a-1", expect.anything()),
    );
    expect(await screen.findByTestId("chat-details-file-thumb")).toBeInTheDocument();
  });
});

// The predicates the panel and the thumbnail share, asserted directly because
// they are the client's working definition of "not approved".
describe("os predicados do gate no cliente", () => {
  it("só mostra bytes de um anexo aprovado com preview pronto", () => {
    expect(canShowPreview(attachment({ status: "clean", previewStatus: "ready" }))).toBe(true);
    expect(canShowPreview(attachment({ status: "pending_scan", previewStatus: "ready" }))).toBe(
      false,
    );
    expect(canShowPreview(attachment({ status: "rejected", previewStatus: "ready" }))).toBe(false);
  });

  it("continua esperando enquanto um veredito ainda é possível", () => {
    expect(
      isPreviewWorkPending(attachment({ status: "pending_scan", previewStatus: "pending" })),
    ).toBe(true);
    // A rejected attachment has nothing left to wait for.
    expect(isPreviewWorkPending(attachment({ status: "rejected", previewStatus: "pending" }))).toBe(
      false,
    );
  });
});

// ── The seam an attachment.status event lands in ────────────────────────────

const target: ConversationDetailsTarget = { kind: "channel", id: "ch-1" };

/**
 * The real panel driven by the real hook — the same pair ChatMessageArea wires
 * together. `onAttachmentStatus` is bound to `state.reload`, so the event is
 * replayed here by calling exactly that.
 */
function LivePanel({ onReady }: { onReady: (reload: () => void) => void }) {
  const state = useConversationDetails(target);
  onReady(state.reload);
  return (
    <ConversationDetailsPanel
      kind="channel"
      state={state}
      currentUserId={currentUserId}
      latestPin={null}
      onClose={vi.fn()}
    />
  );
}

describe("reconciliação depois de um evento attachment.status", () => {
  /**
   * The event says which row changed, not what the list should become. The
   * client refetches, so the panel ends up showing what the server decided —
   * which is what keeps the persisted status the single authority and makes a
   * forged or stale event unable to change anything.
   */
  it("refaz a leitura em vez de aplicar o status do evento", async () => {
    mockFetchConversationAttachments
      .mockResolvedValueOnce([attachment({ status: "pending_scan" })])
      // What the server actually decided — not the "clean" a stale event could
      // have carried.
      .mockResolvedValue([attachment({ status: "rejected", previewStatus: "unsupported" })]);

    let reload = () => {};
    render(<LivePanel onReady={(fn) => (reload = fn)} />);
    await screen.findByTestId("chat-details-file-status-a-1");
    expect(screen.getByTestId("chat-details-file-status-a-1")).toHaveTextContent("Em análise");

    await act(async () => {
      reload();
    });

    await waitFor(() =>
      expect(screen.getByTestId("chat-details-file-status-a-1")).toHaveTextContent("Reprovado"),
    );
    expect(within(fileRow("a-1")).queryAllByRole("link")).toHaveLength(0);
  });

  it("aprova na UI quando o servidor aprova", async () => {
    mockFetchConversationAttachments
      .mockResolvedValueOnce([attachment({ status: "pending_scan" })])
      .mockResolvedValue([attachment({ status: "clean", previewStatus: "unsupported" })]);

    let reload = () => {};
    render(<LivePanel onReady={(fn) => (reload = fn)} />);
    await screen.findByTestId("chat-details-file-status-a-1");

    await act(async () => {
      reload();
    });

    await waitFor(() =>
      expect(screen.getByTestId("chat-details-file-status-a-1")).toHaveTextContent("Verificado"),
    );
  });

  it("não duplica o anexo quando dois eventos chegam juntos", async () => {
    mockFetchConversationAttachments.mockResolvedValue([
      attachment({ status: "clean", previewStatus: "unsupported" }),
    ]);

    let reload = () => {};
    render(<LivePanel onReady={(fn) => (reload = fn)} />);
    await screen.findByTestId("chat-details-file-status-a-1");

    await act(async () => {
      reload();
      reload();
    });

    await waitFor(() =>
      expect(
        within(screen.getByRole("list", { name: "Arquivos recentes" })).getAllByRole("listitem"),
      ).toHaveLength(1),
    );
  });

  // A missed event costs nothing: the persisted status is the source of truth
  // and a reconnect or a reload recovers it.
  it("recupera o status atual num remount, mesmo sem nenhum evento", async () => {
    mockFetchConversationAttachments
      .mockResolvedValueOnce([attachment({ status: "pending_scan" })])
      .mockResolvedValue([attachment({ status: "clean", previewStatus: "unsupported" })]);

    const first = render(<LivePanel onReady={() => {}} />);
    await screen.findByTestId("chat-details-file-status-a-1");
    first.unmount();

    render(<LivePanel onReady={() => {}} />);
    await waitFor(() =>
      expect(screen.getByTestId("chat-details-file-status-a-1")).toHaveTextContent("Verificado"),
    );
  });
});
