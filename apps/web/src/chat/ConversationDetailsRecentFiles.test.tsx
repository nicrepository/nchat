/**
 * The details panel's recent files (issue #897): compact at five, expandable
 * through the shared primitive when the listing says more exists, paginated in
 * place, and a clean row that performs the product's existing action.
 *
 * The hook's side — cursors, stale pages, refresh — is useConversationDetails'
 * own suite. Here the section is driven through its public state, exactly as
 * the panel receives it.
 */

import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const { mockFetchContent, mockFetchManifest } = vi.hoisted(() => ({
  mockFetchContent: vi.fn(),
  mockFetchManifest: vi.fn(),
}));

vi.mock("./filesApi", () => ({
  // No fixture below has a preview, so a thumbnail is never requested; the
  // export simply has to exist.
  fetchAttachmentPreview: () => Promise.reject(new Error("not used")),
  fetchAttachmentContent: mockFetchContent,
  fetchDocumentPreviewManifest: mockFetchManifest,
  fetchDocumentPreviewPage: () => Promise.reject(new Error("not used")),
  fetchDocumentPreviewSheet: () => Promise.reject(new Error("not used")),
}));

vi.mock("./chatApi", () => ({
  fetchChannelMembers: vi.fn(),
  searchChannelMemberCandidates: vi.fn(),
  searchGroupParticipantCandidates: vi.fn(),
  addChannelMembers: vi.fn(),
  addGroupParticipants: vi.fn(),
}));

import ConversationDetailsPanel from "./ConversationDetailsPanel";
import type { ChannelAttachment, ChannelDetails } from "./chatTypes";
import type {
  ConversationDetailsState,
  FilesSection,
  NextFilesPage,
} from "./useConversationDetails";

function channel(): { kind: "channel" } & ChannelDetails {
  return {
    kind: "channel",
    id: "ch-1",
    slug: "infra",
    name: "Infraestrutura",
    type: "public",
    description: "",
    createdAt: "2024-01-12T09:30:00.000Z",
    memberCount: 1,
    onlineCount: 0,
    onlineMembers: [],
    canAddMembers: false,
    canManageMembers: false,
    canRemoveMembers: false,
  };
}

function file(index: number, overrides: Partial<ChannelAttachment> = {}): ChannelAttachment {
  return {
    id: `a-${index}`,
    filename: `arquivo-${index}.bin`,
    contentType: "application/octet-stream",
    size: 1024,
    status: "clean",
    previewStatus: "unsupported",
    createdAt: "2026-07-15T12:00:00.000Z",
    ...overrides,
  };
}

function files(count: number): ChannelAttachment[] {
  return Array.from({ length: count }, (_, index) => file(index + 1));
}

function next(overrides: Partial<NextFilesPage> = {}): NextFilesPage {
  return { status: "idle", load: vi.fn(), ...overrides };
}

function renderFiles(section: FilesSection, kind: "channel" | "group" = "channel") {
  const state: ConversationDetailsState = {
    details: {
      status: "ready",
      data:
        kind === "channel"
          ? channel()
          : {
              kind: "group",
              id: "g-1",
              name: "Grupo",
              description: "",
              createdAt: "2024-01-12T09:30:00.000Z",
              participantCount: 0,
              participants: [],
              canManageMembers: false,
              canRemoveMembers: false,
            },
    },
    files: section,
    roster: { status: "loading" },
    reload: vi.fn(),
  };
  return render(
    <ConversationDetailsPanel
      kind={kind}
      state={state}
      currentUserId="user-me"
      onClose={vi.fn()}
    />,
  );
}

/** The section itself, so its lines are never confused with another section's. */
function filesSection() {
  return screen.getByRole("region", { name: /Arquivos recentes/ });
}

function fileList() {
  return screen.getByRole("list", { name: "Arquivos recentes" });
}

function rows() {
  return within(fileList()).getAllByRole("listitem");
}

function expandControl() {
  return screen.queryByRole("button", { name: /Ver todos Arquivos recentes/ });
}

const createObjectURL = vi.fn(() => "blob:saved");
const revokeObjectURL = vi.fn();

beforeEach(() => {
  vi.stubGlobal("URL", { ...URL, createObjectURL, revokeObjectURL });
  vi.spyOn(HTMLAnchorElement.prototype, "click").mockImplementation(() => {});
});

afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
  mockFetchContent.mockReset();
  mockFetchManifest.mockReset();
});

describe("recent files — compact state", () => {
  it("shows the real empty state for a conversation with no files", () => {
    renderFiles({ status: "ready", data: [] });

    expect(screen.getByTestId("chat-details-files-empty")).toHaveTextContent(
      "Nenhum arquivo enviado neste canal.",
    );
    expect(expandControl()).toBeNull();
  });

  it.each([1, 5])("shows %i file(s) with no expand control when nothing is older", (count) => {
    renderFiles({ status: "ready", data: files(count) });

    expect(rows()).toHaveLength(count);
    expect(expandControl()).toBeNull();
  });

  it("caps the compact list at five and offers the rest when six are held", () => {
    renderFiles({ status: "ready", data: files(6) });

    expect(rows()).toHaveLength(5);
    expect(expandControl()).toBeInTheDocument();
  });

  it("offers Ver todos for five files only when the listing says more exist", () => {
    renderFiles({ status: "ready", data: files(5), next: next() });

    expect(rows()).toHaveLength(5);
    expect(expandControl()).toHaveAttribute("aria-expanded", "false");
    // Collapsed, the next page's controls describe a list nobody can see.
    expect(screen.queryByRole("button", { name: "Carregar mais arquivos" })).toBeNull();
  });

  it("keeps loading and errors inside the section, and the rest of the panel rendered", () => {
    const { unmount } = renderFiles({ status: "loading" });
    expect(within(filesSection()).getByRole("status")).toHaveTextContent("Carregando arquivos…");
    expect(screen.getByText("Infraestrutura")).toBeInTheDocument();
    unmount();

    renderFiles({ status: "error" });
    expect(within(filesSection()).getByRole("alert")).toHaveTextContent(
      "Não foi possível carregar os arquivos.",
    );
    expect(screen.getByText("Infraestrutura")).toBeInTheDocument();
    expect(screen.queryByText(/ainda não está disponível/)).toBeNull();
  });

  it("uses the group's own empty wording", () => {
    renderFiles({ status: "ready", data: [] }, "group");

    expect(screen.getByTestId("chat-details-files-empty")).toHaveTextContent(
      "Nenhum arquivo enviado neste grupo.",
    );
  });
});

describe("recent files — expansion and pages", () => {
  it("asks for the next page once per expansion, keeping the first five on screen", async () => {
    const page = next();
    renderFiles({ status: "ready", data: files(5), next: page });

    await userEvent.click(expandControl()!);

    expect(page.load).toHaveBeenCalledTimes(1);
    expect(rows()).toHaveLength(5);
    expect(screen.getByRole("button", { name: /Mostrar menos Arquivos recentes/ })).toHaveAttribute(
      "aria-expanded",
      "true",
    );

    // Collapsing never fetches.
    await userEvent.click(screen.getByRole("button", { name: /Mostrar menos Arquivos recentes/ }));
    expect(page.load).toHaveBeenCalledTimes(1);
  });

  it("reveals held rows in a scrolling region and collapses back to five", async () => {
    renderFiles({ status: "ready", data: files(8) });

    await userEvent.click(expandControl()!);
    expect(rows()).toHaveLength(8);
    expect(fileList()).toHaveClass("chat-details__collection--expanded");
    // Scrollable, so reachable by keyboard even when a row is not.
    expect(fileList()).toHaveAttribute("tabindex", "0");

    await userEvent.click(screen.getByRole("button", { name: /Mostrar menos Arquivos recentes/ }));
    expect(rows()).toHaveLength(5);
    expect(fileList()).not.toHaveClass("chat-details__collection--expanded");
  });

  it("shows a localized loading line under the rows while the next page travels", async () => {
    const { rerender } = renderFiles({ status: "ready", data: files(5), next: next() });
    await userEvent.click(expandControl()!);

    rerender(
      <ConversationDetailsPanel
        kind="channel"
        state={{
          details: { status: "ready", data: channel() },
          files: { status: "ready", data: files(5), next: next({ status: "loading" }) },
          roster: { status: "loading" },
          reload: vi.fn(),
        }}
        currentUserId="user-me"
        onClose={vi.fn()}
      />,
    );

    expect(rows()).toHaveLength(5);
    expect(within(filesSection()).getByRole("status")).toHaveTextContent(
      "Carregando mais arquivos…",
    );
    // The control stays where it was, inert, so focus is never dropped mid-load.
    expect(screen.getByRole("button", { name: "Carregar mais arquivos" })).toHaveAttribute(
      "aria-disabled",
      "true",
    );
  });

  it("keeps keyboard focus through a page load and hands it to the list after the last", async () => {
    const state = (section: FilesSection): ConversationDetailsState => ({
      details: { status: "ready", data: channel() },
      files: section,
      roster: { status: "loading" },
      reload: vi.fn(),
    });
    const panel = (section: FilesSection) => (
      <ConversationDetailsPanel
        kind="channel"
        state={state(section)}
        currentUserId="user-me"
        onClose={vi.fn()}
      />
    );
    const { rerender } = render(panel({ status: "ready", data: files(25), next: next() }));
    await userEvent.click(expandControl()!);
    const more = screen.getByRole("button", { name: "Carregar mais arquivos" });
    more.focus();
    await userEvent.keyboard("{Enter}");

    rerender(panel({ status: "ready", data: files(25), next: next({ status: "loading" }) }));
    expect(more).toHaveFocus();

    rerender(panel({ status: "ready", data: files(30) }));
    expect(screen.queryByRole("button", { name: "Carregar mais arquivos" })).toBeNull();
    expect(fileList()).toHaveFocus();
    expect(rows()).toHaveLength(30);
  });

  it("appends a later page without duplicating rows and offers the one after it", async () => {
    const after = next();
    const { rerender } = renderFiles({ status: "ready", data: files(5), next: next() });
    await userEvent.click(expandControl()!);

    rerender(
      <ConversationDetailsPanel
        kind="channel"
        state={{
          details: { status: "ready", data: channel() },
          files: { status: "ready", data: files(25), next: after },
          roster: { status: "loading" },
          reload: vi.fn(),
        }}
        currentUserId="user-me"
        onClose={vi.fn()}
      />,
    );

    expect(rows()).toHaveLength(25);
    expect(new Set(rows().map((row) => row.dataset.testid)).size).toBe(25);
    await userEvent.click(screen.getByRole("button", { name: "Carregar mais arquivos" }));
    expect(after.load).toHaveBeenCalledTimes(1);
  });

  it("keeps every loaded row when the next page fails, and retries that page", async () => {
    const failed = next({ status: "error" });
    renderFiles({ status: "ready", data: files(25), next: failed });
    await userEvent.click(expandControl()!);

    expect(rows()).toHaveLength(25);
    expect(within(filesSection()).getByRole("alert")).toHaveTextContent(
      "Não foi possível carregar mais arquivos.",
    );
    await userEvent.click(screen.getByRole("button", { name: "Tentar novamente" }));
    // Once for the expansion that surfaced the failure, once for the retry.
    expect(failed.load).toHaveBeenCalledTimes(2);
  });

  it("offers no further page once the listing has none", async () => {
    renderFiles({ status: "ready", data: files(12) });
    await userEvent.click(expandControl()!);

    expect(rows()).toHaveLength(12);
    expect(screen.queryByRole("button", { name: "Carregar mais arquivos" })).toBeNull();
  });
});

describe("recent files — metadata", () => {
  it("joins date, time and size, and draws the status as text", () => {
    renderFiles({ status: "ready", data: [file(1, { size: 113 * 1024 })] });

    const meta = rows()[0].querySelector(".chat-details__file-meta");
    expect(meta).toHaveTextContent(/^\d{1,2} de .+ de 2026, \d{2}:\d{2} · 113 KBVerificado$/);
  });

  it.each([
    ["absent", ""],
    ["unparseable", "not-a-date"],
  ])("leaves out a %s date without stray separators", (_label, createdAt) => {
    renderFiles({ status: "ready", data: [file(1, { createdAt, size: 20 * 1024 })] });

    const meta = rows()[0].querySelector(".chat-details__file-meta");
    expect(meta).toHaveTextContent(/^20 KBVerificado$/);
    expect(meta?.textContent).not.toMatch(/,|·/);
  });

  it("uses the detected type for the icon and never invents a thumbnail", () => {
    renderFiles({ status: "ready", data: [file(1, { contentType: "", filename: "foto.png" })] });

    expect(rows()[0].querySelector(".material-symbols-outlined")).toHaveTextContent("draft");
    expect(screen.queryByTestId("chat-details-file-thumb")).toBeNull();
  });
});

describe("recent files — scan state and action", () => {
  it("offers no control for a file the scan has not approved", () => {
    renderFiles({
      status: "ready",
      data: [
        file(1, { status: "pending_scan", filename: "analise.pdf" }),
        file(2, { status: "rejected", filename: "virus.exe" }),
      ],
    });

    expect(within(fileList()).queryAllByRole("button")).toHaveLength(0);
    expect(within(fileList()).queryAllByRole("link")).toHaveLength(0);
    expect(screen.getByTestId("chat-details-file-status-a-1")).toHaveTextContent("Em análise");
    expect(screen.getByTestId("chat-details-file-status-a-2")).toHaveTextContent("Reprovado");
  });

  it("downloads a clean file through the authenticated route, once per activation", async () => {
    mockFetchContent.mockResolvedValue(new Blob(["x"]));
    renderFiles({
      status: "ready",
      data: [file(1, { filename: "relatorio.zip", size: 20 * 1024 })],
    });

    const action = screen.getByRole("button", { name: "Baixar relatorio.zip" });
    expect(action).toHaveAccessibleDescription(/20 KB\s*Verificado$/);
    await userEvent.click(action);

    await waitFor(() => expect(createObjectURL).toHaveBeenCalledTimes(1));
    expect(mockFetchContent).toHaveBeenCalledTimes(1);
    expect(mockFetchContent).toHaveBeenCalledWith("a-1");
    expect(HTMLAnchorElement.prototype.click).toHaveBeenCalledTimes(1);
    expect(revokeObjectURL).toHaveBeenCalledWith("blob:saved");
  });

  it("activates from the keyboard like any button", async () => {
    mockFetchContent.mockResolvedValue(new Blob(["x"]));
    renderFiles({ status: "ready", data: [file(1, { filename: "planilha.bin" })] });

    const action = screen.getByRole("button", { name: "Baixar planilha.bin" });
    action.focus();
    await userEvent.keyboard("{Enter}");
    await waitFor(() => expect(mockFetchContent).toHaveBeenCalledTimes(1));

    await userEvent.keyboard(" ");
    await waitFor(() => expect(mockFetchContent).toHaveBeenCalledTimes(2));
    expect(action).toHaveFocus();
  });

  it("refuses a second download while one is running, and keeps focus on the row", async () => {
    let finish: (blob: Blob) => void = () => {};
    mockFetchContent.mockReturnValue(new Promise<Blob>((resolve) => (finish = resolve)));
    renderFiles({ status: "ready", data: [file(1)] });

    const action = screen.getByRole("button", { name: "Baixar arquivo-1.bin" });
    await userEvent.click(action);
    expect(action).toHaveAttribute("aria-disabled", "true");
    await userEvent.click(action);

    expect(mockFetchContent).toHaveBeenCalledTimes(1);
    expect(action).toHaveFocus();
    finish(new Blob(["x"]));
    await waitFor(() => expect(action).not.toHaveAttribute("aria-disabled"));
  });

  it("says so when a download fails and leaves the row as it was", async () => {
    mockFetchContent.mockRejectedValue(new Error("403"));
    renderFiles({ status: "ready", data: [file(1)] });

    await userEvent.click(screen.getByRole("button", { name: "Baixar arquivo-1.bin" }));

    expect(await screen.findByRole("alert")).toHaveTextContent(
      "Não foi possível baixar o arquivo.",
    );
    expect(screen.getByTestId("chat-details-file-status-a-1")).toHaveTextContent("Verificado");
  });

  it("opens a document with a rendered preview in the shared viewer, not a download", async () => {
    mockFetchManifest.mockReturnValue(new Promise(() => {}));
    renderFiles({
      status: "ready",
      data: [
        file(1, {
          filename: "relatorio.pdf",
          contentType: "application/pdf",
          previewStatus: "available",
        }),
      ],
    });

    await userEvent.click(screen.getByRole("button", { name: "Visualizar relatorio.pdf" }));

    expect(await screen.findByRole("dialog")).toBeInTheDocument();
    expect(mockFetchManifest).toHaveBeenCalledWith("a-1", expect.any(AbortSignal));
    expect(mockFetchContent).not.toHaveBeenCalled();
  });

  it("downloads a document whose preview is not available", () => {
    renderFiles({
      status: "ready",
      data: [file(1, { filename: "scan.pdf", contentType: "application/pdf" })],
    });

    expect(screen.getByRole("button", { name: "Baixar scan.pdf" })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Visualizar scan.pdf" })).toBeNull();
  });

  it("closes a panel document viewer when the conversation changes", async () => {
    mockFetchManifest.mockReturnValue(new Promise(() => {}));
    const { rerender } = renderFiles({
      status: "ready",
      data: [file(1, { contentType: "application/pdf", previewStatus: "available" })],
    });
    await userEvent.click(screen.getByRole("button", { name: "Visualizar arquivo-1.bin" }));
    expect(screen.getByRole("dialog")).toBeInTheDocument();

    rerender(
      <ConversationDetailsPanel
        kind="channel"
        state={{
          details: { status: "ready", data: { ...channel(), id: "ch-2", name: "Outro canal" } },
          files: { status: "ready", data: [file(2)] },
          roster: { status: "loading" },
          reload: vi.fn(),
        }}
        currentUserId="user-me"
        onClose={vi.fn()}
      />,
    );

    expect(screen.queryByRole("dialog")).toBeNull();
    expect(screen.getByRole("button", { name: "Baixar arquivo-2.bin" })).toBeInTheDocument();
  });

  it("closes the viewer on a switch even when the details request failed for both", async () => {
    mockFetchManifest.mockReturnValue(new Promise(() => {}));
    const withFiles = (files: FilesSection) => (
      <ConversationDetailsPanel
        kind="channel"
        state={{
          details: { status: "error" },
          files,
          roster: { status: "loading" },
          reload: vi.fn(),
        }}
        currentUserId="user-me"
        onClose={vi.fn()}
      />
    );
    const document = file(1, { contentType: "application/pdf", previewStatus: "available" });
    const { rerender } = render(withFiles({ status: "ready", data: [document] }));
    await userEvent.click(screen.getByRole("button", { name: "Visualizar arquivo-1.bin" }));
    expect(screen.getByRole("dialog")).toBeInTheDocument();

    // The hook's own switch sequence: the files reset, then the new list lands.
    rerender(withFiles({ status: "loading" }));
    rerender(withFiles({ status: "ready", data: [file(2)] }));

    expect(screen.queryByRole("dialog")).toBeNull();
    expect(screen.getByRole("button", { name: "Baixar arquivo-2.bin" })).toBeInTheDocument();
  });

  it("keeps an open viewer through a refresh of the same conversation", async () => {
    mockFetchManifest.mockReturnValue(new Promise(() => {}));
    const document = file(1, { contentType: "application/pdf", previewStatus: "available" });
    const { rerender } = renderFiles({ status: "ready", data: [document] });
    await userEvent.click(screen.getByRole("button", { name: "Visualizar arquivo-1.bin" }));

    // A refresh replaces the list in place and never passes through loading.
    rerender(
      <ConversationDetailsPanel
        kind="channel"
        state={{
          details: { status: "ready", data: channel() },
          files: { status: "ready", data: [file(0), document] },
          roster: { status: "loading" },
          reload: vi.fn(),
        }}
        currentUserId="user-me"
        onClose={vi.fn()}
      />,
    );

    expect(screen.getByRole("dialog")).toBeInTheDocument();
  });

  it("does not bring a document viewer back when returning to its conversation", async () => {
    mockFetchManifest.mockReturnValue(new Promise(() => {}));
    const document = file(1, { contentType: "application/pdf", previewStatus: "available" });
    const panelFor = (details: ReturnType<typeof channel>, rows: ChannelAttachment[]) => (
      <ConversationDetailsPanel
        kind="channel"
        state={{
          details: { status: "ready", data: details },
          files: { status: "ready", data: rows },
          roster: { status: "loading" },
          reload: vi.fn(),
        }}
        currentUserId="user-me"
        onClose={vi.fn()}
      />
    );
    const { rerender } = render(panelFor(channel(), [document]));
    await userEvent.click(screen.getByRole("button", { name: "Visualizar arquivo-1.bin" }));
    expect(screen.getByRole("dialog")).toBeInTheDocument();

    rerender(panelFor({ ...channel(), id: "ch-2", name: "Outro canal" }, [file(2)]));
    expect(screen.queryByRole("dialog")).toBeNull();

    rerender(panelFor(channel(), [document]));
    expect(screen.queryByRole("dialog")).toBeNull();
    expect(screen.getByRole("button", { name: "Visualizar arquivo-1.bin" })).toBeInTheDocument();
  });

  it("names a clean file with no name by the product's own fallback", () => {
    renderFiles({ status: "ready", data: [file(1, { filename: "" })] });

    expect(screen.getByRole("button", { name: "Baixar arquivo" })).toBeInTheDocument();
  });

  it("renders filename with XSS payload strictly as text node", () => {
    const xssPayload = '<img src=x onerror=alert(1)><script>alert("xss")</script>';
    renderFiles({ status: "ready", data: [file(1, { filename: xssPayload })] });

    const fileNameElement = document.querySelector(".chat-details__file-name");
    expect(fileNameElement).not.toBeNull();
    expect(fileNameElement?.textContent).toBe(xssPayload);
    expect(document.querySelector("img[src='x']")).toBeNull();
    expect(document.querySelector("script")).toBeNull();
  });

  it("fails closed on unknown status: never enables button or interactive controls", () => {
    renderFiles({
      status: "ready",
      data: [file(1, { status: "unknown_status" as unknown as ChannelAttachment["status"] })],
    });

    expect(within(fileList()).queryAllByRole("button")).toHaveLength(0);
    expect(within(fileList()).queryAllByRole("link")).toHaveLength(0);
  });
});
