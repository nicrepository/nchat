import { act, fireEvent, render, screen, within } from "@testing-library/react";
import type { ComponentProps } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { ApiRequestError } from "../lib/api";
import type { Channel, ChannelCategory, DMCandidate, DirectDMResult } from "./chatTypes";
import { MAX_GROUP_MEMBERS } from "./dmGroupForm";
import NewConversationDialog from "./NewConversationDialog";

const {
  mockSearchDMCandidates,
  mockGetOrCreateDirectDM,
  mockCreateGroupDM,
  mockCreateChannel,
  mockCreateChannelCategory,
} = vi.hoisted(() => ({
  mockSearchDMCandidates: vi.fn<(query: string, signal?: AbortSignal) => Promise<DMCandidate[]>>(),
  mockGetOrCreateDirectDM:
    vi.fn<(userId: string, signal?: AbortSignal) => Promise<DirectDMResult>>(),
  mockCreateGroupDM:
    vi.fn<
      (
        userIds: string[],
        title: string,
        avatarEmoji: string | undefined,
        signal?: AbortSignal,
      ) => Promise<string>
    >(),
  mockCreateChannel:
    vi.fn<
      (
        input: { slug: string; displayName: string; type: "public" | "private" },
        signal?: AbortSignal,
      ) => Promise<Channel>
    >(),
  mockCreateChannelCategory:
    vi.fn<(name: string, signal?: AbortSignal) => Promise<{ id?: string; name: string }>>(),
}));

// The #496 picker is exercised by its own suite; here it only has to hand an
// emoji back, the way the real one does on a click.
vi.mock("./emoji/EmojiPicker", () => ({
  default: ({ onSelect }: { onSelect: (emoji: string) => void }) => (
    <div aria-label="Emojis de teste" role="group">
      {["🎉", "👩‍💻"].map((emoji) => (
        <button key={emoji} type="button" onClick={() => onSelect(emoji)}>
          {emoji}
        </button>
      ))}
    </div>
  ),
}));

vi.mock("./chatApi", () => ({
  createChannel: (
    input: { slug: string; displayName: string; type: "public" | "private" },
    signal?: AbortSignal,
  ) => mockCreateChannel(input, signal),
  createChannelCategory: (name: string, signal?: AbortSignal) =>
    mockCreateChannelCategory(name, signal),
  searchDMCandidates: (query: string, signal?: AbortSignal) =>
    mockSearchDMCandidates(query, signal),
  getOrCreateDirectDM: (userId: string, signal?: AbortSignal) =>
    mockGetOrCreateDirectDM(userId, signal),
  createGroupDM: (
    userIds: string[],
    title: string,
    avatarEmoji: string | undefined,
    signal?: AbortSignal,
  ) => mockCreateGroupDM(userIds, title, avatarEmoji, signal),
}));

function deferred<T>() {
  let resolve!: (value: T) => void;
  let reject!: (reason?: unknown) => void;
  const promise = new Promise<T>((done, fail) => {
    resolve = done;
    reject = fail;
  });
  return { promise, resolve, reject };
}

function renderDialog(overrides: Partial<ComponentProps<typeof NewConversationDialog>> = {}) {
  const props = {
    currentUserId: "current-user",
    categories: [],
    onClose: vi.fn(),
    onOpened: vi.fn(),
    onChannelCreated: vi.fn(),
    ...overrides,
  };
  render(<NewConversationDialog {...props} />);
  return props;
}

async function advanceSearch() {
  await act(async () => {
    vi.advanceTimersByTime(150);
    await Promise.resolve();
  });
}

beforeEach(() => {
  vi.useFakeTimers();
  vi.clearAllMocks();
});

afterEach(() => {
  vi.clearAllTimers();
  vi.useRealTimers();
});

describe("NewConversationDialog", () => {
  it("renders an accessible focused search field and skips empty or short queries", async () => {
    renderDialog();

    const input = screen.getByRole("searchbox", { name: "Pesquisar pessoa" });
    expect(input).toHaveFocus();
    expect(screen.getByRole("dialog", { name: "Nova conversa" })).toBeInTheDocument();

    fireEvent.change(input, { target: { value: "a" } });
    await advanceSearch();
    expect(mockSearchDMCandidates).not.toHaveBeenCalled();
  });

  it("shows loading, results and an initials fallback", async () => {
    const request = deferred<DMCandidate[]>();
    mockSearchDMCandidates.mockReturnValue(request.promise);
    renderDialog();

    fireEvent.change(screen.getByRole("searchbox"), { target: { value: "jo" } });
    expect(screen.getByRole("status")).toHaveTextContent("Buscando pessoas");
    await advanceSearch();
    expect(mockSearchDMCandidates).toHaveBeenCalledWith("jo", expect.any(AbortSignal));

    await act(async () => request.resolve([{ userId: "user-2", displayName: "Joana Silva" }]));
    expect(screen.getByRole("button", { name: "Joana Silva" })).toBeInTheDocument();
    expect(screen.getByText("JS")).toBeInTheDocument();
  });

  it("shows an empty result state", async () => {
    mockSearchDMCandidates.mockResolvedValue([]);
    renderDialog();

    fireEvent.change(screen.getByRole("searchbox"), { target: { value: "ninguém" } });
    await advanceSearch();
    expect(screen.getByText("Nenhuma pessoa encontrada.")).toBeInTheDocument();
  });

  it("shows a generic search error and retries the same query", async () => {
    mockSearchDMCandidates
      .mockRejectedValueOnce(new Error("database host secret"))
      .mockResolvedValueOnce([{ userId: "user-2", displayName: "Joana" }]);
    renderDialog();

    fireEvent.change(screen.getByRole("searchbox"), { target: { value: "jo" } });
    await advanceSearch();
    expect(screen.getByRole("alert")).toHaveTextContent("Não foi possível buscar pessoas");
    expect(screen.queryByText(/database host secret/i)).not.toBeInTheDocument();

    fireEvent.click(screen.getByRole("button", { name: "Tentar novamente" }));
    await advanceSearch();
    expect(screen.getByRole("button", { name: "Joana" })).toBeInTheDocument();
    expect(mockSearchDMCandidates).toHaveBeenCalledTimes(2);
  });

  it.each([
    [403, "Você não tem acesso à busca de pessoas."],
    [429, "Muitas buscas em sequência."],
  ])("maps search status %s to a stable message", async (status, message) => {
    mockSearchDMCandidates.mockRejectedValue(new ApiRequestError(status, "internal", "secret"));
    renderDialog();

    fireEvent.change(screen.getByRole("searchbox"), { target: { value: "jo" } });
    await advanceSearch();
    expect(screen.getByRole("alert")).toHaveTextContent(message);
    expect(screen.queryByText("secret")).not.toBeInTheDocument();
  });

  it("ignores an older response after a newer search completes", async () => {
    const oldRequest = deferred<DMCandidate[]>();
    const newRequest = deferred<DMCandidate[]>();
    mockSearchDMCandidates
      .mockReturnValueOnce(oldRequest.promise)
      .mockReturnValueOnce(newRequest.promise);
    renderDialog();
    const input = screen.getByRole("searchbox");

    fireEvent.change(input, { target: { value: "jo" } });
    await advanceSearch();
    fireEvent.change(input, { target: { value: "ma" } });
    await advanceSearch();

    await act(async () => newRequest.resolve([{ userId: "user-3", displayName: "Maria" }]));
    expect(screen.getByRole("button", { name: "Maria" })).toBeInTheDocument();

    await act(async () => oldRequest.resolve([{ userId: "user-2", displayName: "Joana" }]));
    expect(screen.queryByRole("button", { name: "Joana" })).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Maria" })).toBeInTheDocument();
  });

  it("filters the current user even if returned by the backend", async () => {
    mockSearchDMCandidates.mockResolvedValue([
      { userId: "current-user", displayName: "Eu Mesmo" },
      { userId: "user-2", displayName: "Joana" },
    ]);
    renderDialog();

    fireEvent.change(screen.getByRole("searchbox"), { target: { value: "jo" } });
    await advanceSearch();
    expect(screen.queryByRole("button", { name: "Eu Mesmo" })).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Joana" })).toBeInTheDocument();
  });

  it("submits the selected user once and keeps the dialog open during the request", async () => {
    const createRequest = deferred<DirectDMResult>();
    mockSearchDMCandidates.mockResolvedValue([{ userId: "user-2", displayName: "Joana" }]);
    mockGetOrCreateDirectDM.mockReturnValue(createRequest.promise);
    const props = renderDialog();

    fireEvent.change(screen.getByRole("searchbox"), { target: { value: "jo" } });
    await advanceSearch();
    const candidate = screen.getByRole("button", { name: "Joana" });
    fireEvent.click(candidate);
    fireEvent.click(candidate);

    expect(mockGetOrCreateDirectDM).toHaveBeenCalledTimes(1);
    expect(mockGetOrCreateDirectDM).toHaveBeenCalledWith("user-2", expect.any(AbortSignal));
    expect(screen.getByText("Abrindo…")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Fechar nova conversa" })).toBeDisabled();
    fireEvent.keyDown(screen.getByRole("dialog"), { key: "Escape" });
    expect(props.onClose).not.toHaveBeenCalled();

    await act(async () =>
      createRequest.resolve({ conversationId: "dm-canonical", created: false }),
    );
    expect(props.onOpened).toHaveBeenCalledWith("dm-canonical");
  });

  it("keeps retry available after a sanitized create error", async () => {
    mockSearchDMCandidates.mockResolvedValue([{ userId: "user-2", displayName: "Joana" }]);
    mockGetOrCreateDirectDM
      .mockRejectedValueOnce(new ApiRequestError(500, "internal", "SQL connection failed"))
      .mockResolvedValueOnce({ conversationId: "dm-2", created: true });
    const props = renderDialog();

    fireEvent.change(screen.getByRole("searchbox"), { target: { value: "jo" } });
    await advanceSearch();
    const candidate = screen.getByRole("button", { name: "Joana" });
    fireEvent.click(candidate);
    await act(async () => Promise.resolve());

    expect(screen.getByRole("alert")).toHaveTextContent("Não foi possível abrir a conversa");
    expect(screen.queryByText(/sql/i)).not.toBeInTheDocument();
    expect(candidate).toBeEnabled();

    fireEvent.click(candidate);
    await act(async () => Promise.resolve());
    expect(props.onOpened).toHaveBeenCalledWith("dm-2");
  });

  it.each([
    [403, "Esta pessoa não está disponível para mensagens."],
    [404, "Esta pessoa não está disponível para mensagens."],
    [409, "A conversa mudou. Tente novamente."],
    [429, "Muitas solicitações em sequência."],
    [0, "Sem conexão. Verifique sua rede e tente novamente."],
  ])("maps create status %s to a stable message", async (status, message) => {
    mockSearchDMCandidates.mockResolvedValue([{ userId: "user-2", displayName: "Joana" }]);
    mockGetOrCreateDirectDM.mockRejectedValue(
      new ApiRequestError(status, "internal", "private backend detail"),
    );
    renderDialog();

    fireEvent.change(screen.getByRole("searchbox"), { target: { value: "jo" } });
    await advanceSearch();
    fireEvent.click(screen.getByRole("button", { name: "Joana" }));
    await act(async () => Promise.resolve());

    expect(screen.getByRole("alert")).toHaveTextContent(message);
    expect(screen.queryByText(/private backend detail/i)).not.toBeInTheDocument();
  });

  it("traps Tab focus, ignores inside clicks and closes from the backdrop", () => {
    const props = renderDialog();
    const dialog = screen.getByRole("dialog");
    const input = screen.getByRole("searchbox");
    const close = screen.getByRole("button", { name: "Fechar nova conversa" });

    expect(input).toHaveFocus();
    fireEvent.keyDown(dialog, { key: "Tab" });
    expect(close).toHaveFocus();
    fireEvent.keyDown(dialog, { key: "Tab", shiftKey: true });
    expect(input).toHaveFocus();
    fireEvent.keyDown(dialog, { key: "ArrowDown" });

    fireEvent.mouseDown(dialog);
    expect(props.onClose).not.toHaveBeenCalled();
    fireEvent.mouseDown(dialog.parentElement!);
    expect(props.onClose).toHaveBeenCalledTimes(1);
  });

  it("aborts an in-flight create and ignores its completion after unmount", async () => {
    const request = deferred<DirectDMResult>();
    mockSearchDMCandidates.mockResolvedValue([{ userId: "user-2", displayName: "Joana" }]);
    mockGetOrCreateDirectDM.mockReturnValue(request.promise);
    const onOpened = vi.fn();
    const view = render(
      <NewConversationDialog
        currentUserId="current-user"
        categories={[]}
        onClose={vi.fn()}
        onOpened={onOpened}
        onChannelCreated={vi.fn()}
      />,
    );

    fireEvent.change(screen.getByRole("searchbox"), { target: { value: "jo" } });
    await advanceSearch();
    fireEvent.click(screen.getByRole("button", { name: "Joana" }));
    const signal = mockGetOrCreateDirectDM.mock.calls[0][1];
    view.unmount();
    expect(signal?.aborted).toBe(true);

    await act(async () => request.resolve({ conversationId: "stale-dm", created: true }));
    expect(onOpened).not.toHaveBeenCalled();
  });

  it("renders returned names as text and closes with Escape when idle", async () => {
    mockSearchDMCandidates.mockResolvedValue([
      { userId: "user-2", displayName: '<img src=x onerror="alert(1)">' },
    ]);
    const props = renderDialog();

    fireEvent.change(screen.getByRole("searchbox"), { target: { value: "im" } });
    await advanceSearch();
    expect(screen.getByText(/<img src=x/)).toBeInTheDocument();
    expect(document.querySelector("img")).toBeNull();

    fireEvent.keyDown(screen.getByRole("dialog"), { key: "Escape" });
    expect(props.onClose).toHaveBeenCalledTimes(1);
  });
});

it("discards a stale search failure once a newer query has answered", async () => {
  const stale = deferred<DMCandidate[]>();
  mockSearchDMCandidates
    .mockReturnValueOnce(stale.promise)
    .mockResolvedValueOnce([{ userId: "user-3", displayName: "Maria" }]);
  renderDialog();
  const input = screen.getByRole("searchbox");

  fireEvent.change(input, { target: { value: "jo" } });
  await advanceSearch();
  fireEvent.change(input, { target: { value: "ma" } });
  await advanceSearch();
  expect(screen.getByRole("button", { name: "Maria" })).toBeInTheDocument();

  // The abandoned request fails afterwards: its error belongs to a query the
  // user has already moved on from and must not replace the visible results.
  await act(async () => {
    stale.reject(new ApiRequestError(500, "internal", "stale failure"));
    await Promise.resolve();
  });
  expect(screen.queryByRole("alert")).not.toBeInTheDocument();
  expect(screen.getByRole("button", { name: "Maria" })).toBeInTheDocument();
});

it("leaves Tab alone while focus is in the middle of the dialog", () => {
  renderDialog();
  const dialog = screen.getByRole("dialog");
  const groupRadio = screen.getByRole("radio", { name: "Grupo" });

  groupRadio.focus();
  fireEvent.keyDown(dialog, { key: "Tab" });
  // Neither edge of the trap: the browser's own tab order must win.
  expect(groupRadio).toHaveFocus();
});

it("renders a placeholder avatar when a name yields no initials", async () => {
  mockSearchDMCandidates.mockResolvedValue([{ userId: "user-2", displayName: "   " }]);
  renderDialog();

  fireEvent.change(screen.getByRole("searchbox"), { target: { value: "jo" } });
  await advanceSearch();
  expect(screen.getByText("?")).toBeInTheDocument();
});

it("shows the generic message when opening a DM fails without an API status", async () => {
  mockSearchDMCandidates.mockResolvedValue([{ userId: "user-2", displayName: "Joana" }]);
  mockGetOrCreateDirectDM.mockRejectedValue(new Error("socket hang up"));
  renderDialog();

  fireEvent.change(screen.getByRole("searchbox"), { target: { value: "jo" } });
  await advanceSearch();
  fireEvent.click(screen.getByRole("button", { name: "Joana" }));
  await act(async () => Promise.resolve());

  expect(screen.getByRole("alert")).toHaveTextContent("Não foi possível abrir a conversa");
  expect(screen.queryByText(/socket hang up/i)).not.toBeInTheDocument();
});

it("ignores a failure that lands after the dialog was unmounted", async () => {
  const request = deferred<DirectDMResult>();
  mockSearchDMCandidates.mockResolvedValue([{ userId: "user-2", displayName: "Joana" }]);
  mockGetOrCreateDirectDM.mockReturnValue(request.promise);
  const view = render(
    <NewConversationDialog
      currentUserId="current-user"
      categories={[]}
      onClose={vi.fn()}
      onOpened={vi.fn()}
      onChannelCreated={vi.fn()}
    />,
  );

  fireEvent.change(screen.getByRole("searchbox"), { target: { value: "jo" } });
  await advanceSearch();
  fireEvent.click(screen.getByRole("button", { name: "Joana" }));
  view.unmount();

  // Setting state here would warn and, worse, resurrect a closed dialog.
  await act(async () => {
    request.reject(new ApiRequestError(500, "internal", "late failure"));
    await Promise.resolve();
  });
  expect(screen.queryByRole("alert")).not.toBeInTheDocument();
});

// ── Ad-hoc group creation (RF-02) ─────────────────────────────────────────────

function switchToGroup() {
  fireEvent.click(screen.getByRole("radio", { name: "Grupo" }));
}

async function searchAndSelect(query: string, results: DMCandidate[], names: string[]) {
  mockSearchDMCandidates.mockResolvedValue(results);
  fireEvent.change(screen.getByRole("searchbox"), { target: { value: query } });
  await advanceSearch();
  for (const name of names) {
    fireEvent.click(screen.getByRole("button", { name }));
  }
}

/** Participantes → Identidade (issue #1026). */
function continueToIdentity() {
  fireEvent.click(screen.getByRole("button", { name: "Continuar" }));
}

const groupCandidates: DMCandidate[] = [
  { userId: "user-2", displayName: "Joana" },
  { userId: "user-3", displayName: "Marcos" },
  { userId: "user-4", displayName: "Rita" },
];

describe("NewConversationDialog — group mode", () => {
  it("switches modes with accessible controls and keeps the 1:1 flow untouched", async () => {
    mockSearchDMCandidates.mockResolvedValue([groupCandidates[0]]);
    renderDialog();

    expect(screen.getByRole("radio", { name: "Pessoa" })).toBeChecked();
    expect(screen.queryByRole("button", { name: "Criar grupo" })).not.toBeInTheDocument();

    fireEvent.change(screen.getByRole("searchbox"), { target: { value: "jo" } });
    await advanceSearch();
    // In 1:1 mode a result opens the conversation instead of being selected.
    expect(screen.getByRole("button", { name: "Joana" })).not.toHaveAttribute("aria-pressed");

    switchToGroup();
    expect(screen.getByRole("radio", { name: "Grupo" })).toBeChecked();
    // Participantes first: the name and the creation belong to Identidade.
    expect(screen.queryByLabelText("Nome do grupo (opcional)")).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Criar grupo" })).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Continuar" })).toBeDisabled();
    expect(mockGetOrCreateDirectDM).not.toHaveBeenCalled();
  });

  it("selects several people, de-duplicates them and removes one from the chips", async () => {
    renderDialog();
    switchToGroup();
    await searchAndSelect("jo", groupCandidates, ["Joana", "Marcos"]);

    const chips = screen.getByRole("list", { name: "Pessoas selecionadas" });
    expect(chips).toHaveTextContent("Joana");
    expect(chips).toHaveTextContent("Marcos");
    expect(screen.getByRole("button", { name: "Joana" })).toHaveAttribute("aria-pressed", "true");

    // A second click on the same row toggles off — it can never add a duplicate.
    fireEvent.click(screen.getByRole("button", { name: "Joana" }));
    expect(screen.getByRole("button", { name: "Joana" })).toHaveAttribute("aria-pressed", "false");
    fireEvent.click(screen.getByRole("button", { name: "Joana" }));
    expect(
      within(chips)
        .getAllByRole("listitem")
        .filter((item) => item.textContent?.includes("Joana")),
    ).toHaveLength(1);

    fireEvent.click(screen.getByRole("button", { name: "Remover Marcos" }));
    expect(chips).not.toHaveTextContent("Marcos");
    expect(screen.getByRole("button", { name: "Continuar" })).toBeDisabled();
  });

  it("keeps the selection when the query changes and when a stale response lands", async () => {
    const stale = deferred<DMCandidate[]>();
    renderDialog();
    switchToGroup();
    await searchAndSelect("jo", [groupCandidates[0]], ["Joana"]);

    mockSearchDMCandidates.mockReturnValueOnce(stale.promise);
    fireEvent.change(screen.getByRole("searchbox"), { target: { value: "ma" } });
    await advanceSearch();
    expect(screen.queryByRole("button", { name: "Joana" })).not.toBeInTheDocument();
    expect(screen.getByRole("list", { name: "Pessoas selecionadas" })).toHaveTextContent("Joana");

    fireEvent.change(screen.getByRole("searchbox"), { target: { value: "ri" } });
    mockSearchDMCandidates.mockResolvedValue([groupCandidates[2]]);
    await advanceSearch();
    await act(async () => stale.resolve([groupCandidates[1]]));
    expect(screen.queryByRole("button", { name: "Marcos" })).not.toBeInTheDocument();
    expect(screen.getByRole("list", { name: "Pessoas selecionadas" })).toHaveTextContent("Joana");
  });

  it("never lets the current user be selected", async () => {
    renderDialog();
    switchToGroup();
    await searchAndSelect(
      "eu",
      [{ userId: "current-user", displayName: "Eu Mesmo" }, groupCandidates[0]],
      ["Joana"],
    );

    expect(screen.queryByRole("button", { name: "Eu Mesmo" })).not.toBeInTheDocument();
    expect(screen.getByRole("list", { name: "Pessoas selecionadas" })).not.toHaveTextContent(
      "Eu Mesmo",
    );
  });

  it("blocks submission below the server minimum and submits once when it is met", async () => {
    const request = deferred<string>();
    mockCreateGroupDM.mockReturnValue(request.promise);
    const props = renderDialog();
    switchToGroup();
    await searchAndSelect("jo", groupCandidates, ["Joana"]);

    const next = screen.getByRole("button", { name: "Continuar" });
    expect(next).toBeDisabled();
    fireEvent.click(next);
    expect(screen.queryByRole("button", { name: "Criar grupo" })).not.toBeInTheDocument();
    expect(mockCreateGroupDM).not.toHaveBeenCalled();

    fireEvent.click(screen.getByRole("button", { name: "Marcos" }));
    continueToIdentity();
    expect(mockCreateGroupDM).not.toHaveBeenCalled();
    fireEvent.change(screen.getByLabelText("Nome do grupo (opcional)"), {
      target: { value: "  Infra  " },
    });
    const enabled = screen.getByRole("button", { name: "Criar grupo" });
    fireEvent.click(enabled);
    fireEvent.click(enabled);

    expect(mockCreateGroupDM).toHaveBeenCalledTimes(1);
    expect(mockCreateGroupDM).toHaveBeenCalledWith(
      ["user-2", "user-3"],
      "  Infra  ",
      undefined,
      expect.any(AbortSignal),
    );
    const busy = screen.getByRole("button", { name: "Criando…" });
    expect(busy).toBeDisabled();
    expect(busy).toHaveAttribute("aria-busy", "true");
    expect(screen.getByRole("button", { name: "Fechar nova conversa" })).toBeDisabled();
    fireEvent.keyDown(screen.getByRole("dialog"), { key: "Escape" });
    expect(props.onClose).not.toHaveBeenCalled();

    await act(async () => request.resolve("dm-group"));
    expect(props.onOpened).toHaveBeenCalledWith("dm-group");
  });

  it("creates a group without a name, passing the untouched field to the API client", async () => {
    mockCreateGroupDM.mockResolvedValue("dm-group");
    const props = renderDialog();
    switchToGroup();
    await searchAndSelect("jo", groupCandidates, ["Joana", "Marcos"]);
    continueToIdentity();

    fireEvent.click(screen.getByRole("button", { name: "Criar grupo" }));
    await act(async () => Promise.resolve());

    expect(mockCreateGroupDM).toHaveBeenCalledWith(
      ["user-2", "user-3"],
      "",
      undefined,
      expect.any(AbortSignal),
    );
    expect(props.onOpened).toHaveBeenCalledWith("dm-group");
  });

  it("keeps the selection and allows a retry after a sanitized failure", async () => {
    mockCreateGroupDM
      .mockRejectedValueOnce(new ApiRequestError(500, "internal", "SQL connection failed"))
      .mockResolvedValueOnce("dm-group");
    const props = renderDialog();
    switchToGroup();
    await searchAndSelect("jo", groupCandidates, ["Joana", "Marcos"]);
    continueToIdentity();

    fireEvent.click(screen.getByRole("button", { name: "Criar grupo" }));
    await act(async () => Promise.resolve());

    expect(screen.getByRole("alert")).toHaveTextContent("Não foi possível criar o grupo");
    expect(screen.queryByText(/sql/i)).not.toBeInTheDocument();
    expect(props.onOpened).not.toHaveBeenCalled();
    // Back to Participantes: the selection survived the failure.
    fireEvent.click(screen.getByRole("button", { name: "Voltar" }));
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
    expect(screen.getByRole("list", { name: "Pessoas selecionadas" })).toHaveTextContent("Joana");
    continueToIdentity();

    fireEvent.click(screen.getByRole("button", { name: "Criar grupo" }));
    await act(async () => Promise.resolve());
    expect(props.onOpened).toHaveBeenCalledWith("dm-group");
  });

  it.each([
    [400, "Revise as pessoas selecionadas e o nome do grupo."],
    [403, "Alguma pessoa selecionada não está disponível para conversar."],
    [404, "Alguma pessoa selecionada não está disponível para conversar."],
    [429, "Muitas solicitações em sequência."],
    [0, "Sem conexão. Verifique sua rede e tente novamente."],
  ])("maps group create status %s to a stable message", async (status, message) => {
    mockCreateGroupDM.mockRejectedValue(
      new ApiRequestError(status, "internal", "private backend detail"),
    );
    renderDialog();
    switchToGroup();
    await searchAndSelect("jo", groupCandidates, ["Joana", "Marcos"]);
    continueToIdentity();

    fireEvent.click(screen.getByRole("button", { name: "Criar grupo" }));
    await act(async () => Promise.resolve());

    expect(screen.getByRole("alert")).toHaveTextContent(message);
    expect(screen.queryByText(/private backend detail/i)).not.toBeInTheDocument();
  });

  it("stops a second click fired in the same frame, before React can disable the button", async () => {
    mockCreateGroupDM.mockResolvedValue("dm-group");
    const props = renderDialog();
    switchToGroup();
    await searchAndSelect("jo", groupCandidates, ["Joana", "Marcos"]);
    continueToIdentity();

    // Both clicks inside one act(): React has not re-rendered in between, so the
    // button is still enabled for the second one and only the in-flight guard
    // can prevent the duplicate group.
    const submit = screen.getByRole("button", { name: "Criar grupo" });
    await act(async () => {
      submit.click();
      submit.click();
    });

    expect(mockCreateGroupDM).toHaveBeenCalledTimes(1);
    expect(props.onOpened).toHaveBeenCalledTimes(1);
  });

  // The cap rule itself is proved on toggleGroupMember in dmGroupForm.test.ts.
  // What is left for the component is the boundary a user can see: the hint, and
  // which rows the cap disables.
  //
  // Every query here is resolved once, before the selection exists. At the cap
  // the dialog holds ~100 buttons — 50 result rows plus a chip each — and a
  // getByRole("button", { name }) computes an accessible name for all of them;
  // repeating that after the fill made the test slow enough to time out under
  // coverage instrumentation. React keeps these nodes across re-renders, so the
  // references taken up front stay the right ones.
  it("stops selecting past the participant cap without losing the current selection", async () => {
    const many = Array.from({ length: MAX_GROUP_MEMBERS + 1 }, (_, index) => ({
      userId: `user-${index}`,
      displayName: `Pessoa ${index}`,
    }));
    mockSearchDMCandidates.mockResolvedValue(many);
    renderDialog();
    switchToGroup();
    fireEvent.change(screen.getByRole("searchbox"), { target: { value: "pessoa" } });
    await advanceSearch();

    const rows = within(screen.getByRole("list", { name: "Pessoas encontradas" })).getAllByRole(
      "button",
    );
    expect(rows).toHaveLength(MAX_GROUP_MEMBERS + 1);
    const firstSelected = rows[0];
    const overflow = rows[MAX_GROUP_MEMBERS];
    const footer = screen.getByRole("dialog").querySelector("footer");
    expect(footer).not.toBeNull();

    // Selecting is a functional state update, so filling the group in one batch
    // is equivalent to 49 separate clicks and keeps the test fast.
    await act(async () => {
      rows.slice(0, MAX_GROUP_MEMBERS).forEach((row) => row.click());
    });

    expect(
      within(footer!).getByText(`Limite de ${MAX_GROUP_MEMBERS} pessoas atingido.`),
    ).toBeInTheDocument();
    expect(overflow).toBeDisabled();
    expect(
      within(screen.getByRole("list", { name: "Pessoas selecionadas" })).getAllByRole("button"),
    ).toHaveLength(MAX_GROUP_MEMBERS);

    // An already selected row stays clickable, so the only way out of the cap is
    // removing someone — the selection is never silently rewritten.
    expect(firstSelected).toBeEnabled();
    fireEvent.click(firstSelected);
    expect(
      within(footer!).getByText(`${MAX_GROUP_MEMBERS - 1} de no mínimo 2 pessoas selecionadas.`),
    ).toBeInTheDocument();
    expect(overflow).toBeEnabled();
  });

  it("renders a hostile group name as text", async () => {
    renderDialog();
    switchToGroup();
    await searchAndSelect("jo", groupCandidates, ["Joana", "Marcos"]);
    continueToIdentity();

    fireEvent.change(screen.getByLabelText("Nome do grupo (opcional)"), {
      target: { value: '<img src=x onerror="alert(1)">' },
    });
    expect(document.querySelector("img")).toBeNull();
    expect(screen.getByLabelText("Nome do grupo (opcional)")).toHaveValue(
      '<img src=x onerror="alert(1)">',
    );
  });

  it("keeps a 120-emoji name and never sends more code points than the server accepts", async () => {
    mockCreateGroupDM.mockResolvedValue("dm-group");
    renderDialog();
    switchToGroup();
    await searchAndSelect("jo", groupCandidates, ["Joana", "Marcos"]);
    continueToIdentity();

    const nameField = screen.getByLabelText("Nome do grupo (opcional)");
    const emojiName = Array.from({ length: 120 }, () => "🙂").join("");
    // 120 code points, 240 UTF-16 units: a maxLength of 120 would have cut this
    // in half even though the server accepts it whole.
    fireEvent.change(nameField, { target: { value: emojiName } });
    expect(nameField).toHaveValue(emojiName);

    fireEvent.change(nameField, { target: { value: emojiName + "🙂a" } });
    expect(nameField).toHaveValue(emojiName);

    fireEvent.click(screen.getByRole("button", { name: "Criar grupo" }));
    await act(async () => Promise.resolve());

    const [, sentTitle] = mockCreateGroupDM.mock.calls[0];
    expect(Array.from(sentTitle)).toHaveLength(120);
    expect(sentTitle).toBe(emojiName);
  });

  it("truncates an over-long ASCII name by code points as the user types", async () => {
    renderDialog();
    switchToGroup();
    await searchAndSelect("jo", groupCandidates, ["Joana", "Marcos"]);
    continueToIdentity();

    const nameField = screen.getByLabelText("Nome do grupo (opcional)");
    fireEvent.change(nameField, { target: { value: "a".repeat(121) } });
    expect(nameField).toHaveValue("a".repeat(120));
  });
});

// ── Channel mode (BUG #393) ───────────────────────────────────────────────────
// Channel creation has no sidebar control of its own any more: it is the third
// mode of this dialog, reachable by every authenticated member. Authorization is
// never evaluated here — the endpoint answers, and a denial arrives as a status.

const createdChannel: Channel = {
  id: "ch-1",
  name: "Infraestrutura",
  type: "public",
  canWrite: true,
};

const nameField = () => screen.getByLabelText(/nome do canal/i);
const slugField = () => screen.getByLabelText(/identificador/i);
const createChannelButton = () => screen.getByRole("button", { name: /criar canal/i });

function renderChannelMode(overrides: Partial<ComponentProps<typeof NewConversationDialog>> = {}) {
  const props = renderDialog(overrides);
  fireEvent.click(screen.getByRole("radio", { name: "Canal" }));
  return props;
}

/** Flushes the microtask queue without leaving the fake-timer clock behind. */
async function settle() {
  await act(async () => {
    await Promise.resolve();
  });
}

describe("NewConversationDialog — channel mode", () => {
  it("offers Pessoa, Grupo and Canal from the single dialog", () => {
    renderDialog();

    expect(screen.getByRole("radio", { name: "Pessoa" })).toBeInTheDocument();
    expect(screen.getByRole("radio", { name: "Grupo" })).toBeInTheDocument();
    expect(screen.getByRole("radio", { name: "Canal" })).toBeInTheDocument();
    // No admin-only wording anywhere in the flow.
    expect(screen.queryByText(/somente administradores/i)).not.toBeInTheDocument();
  });

  // Focus stays on the chosen mode (issue #1023): arrow keys must be able to
  // walk the radios without the channel form pulling focus away on the way.
  it("swaps the people search for the channel form and keeps focus on the mode", () => {
    renderChannelMode();

    expect(screen.getByRole("radio", { name: "Canal" })).toHaveFocus();
    expect(nameField()).toBeVisible();
    expect(screen.queryByRole("searchbox", { name: "Pesquisar pessoa" })).not.toBeInTheDocument();
    expect(screen.getByRole("dialog", { name: "Nova conversa" })).toBeInTheDocument();
  });

  it("does not search for people while the channel form is showing", async () => {
    renderDialog();
    fireEvent.change(screen.getByRole("searchbox"), { target: { value: "jo" } });
    fireEvent.click(screen.getByRole("radio", { name: "Canal" }));
    await advanceSearch();

    expect(mockSearchDMCandidates).not.toHaveBeenCalled();
  });

  it("sends only the caller-owned fields and hands the new channel ID back", async () => {
    mockCreateChannel.mockResolvedValue(createdChannel);
    const props = renderChannelMode();

    fireEvent.change(nameField(), { target: { value: "Infraestrutura" } });
    fireEvent.click(createChannelButton());
    await settle();

    expect(props.onChannelCreated).toHaveBeenCalledWith("ch-1");
    expect(mockCreateChannel).toHaveBeenCalledTimes(1);
    // Exactly the contract: no role, no actor, no workspace from the browser,
    // and no invitee list for a public channel (issue #1025).
    expect(mockCreateChannel.mock.calls[0][0]).toStrictEqual({
      slug: "infraestrutura",
      displayName: "Infraestrutura",
      type: "public",
      categoryId: undefined,
      initialMemberIds: undefined,
      idempotencyKey: expect.any(String),
    });
  });

  it("derives the slug from the name until the user edits it, then leaves it alone", async () => {
    mockCreateChannel.mockResolvedValue(createdChannel);
    renderChannelMode();

    fireEvent.change(nameField(), { target: { value: "Operações Críticas" } });
    expect(slugField()).toHaveValue("operacoes-criticas");

    fireEvent.change(slugField(), { target: { value: "ops" } });
    fireEvent.change(nameField(), { target: { value: "Outro nome" } });
    expect(slugField()).toHaveValue("ops");

    fireEvent.click(createChannelButton());
    await settle();
    expect(mockCreateChannel.mock.calls[0][0].slug).toBe("ops");
  });

  it("sends the selected channel type", async () => {
    mockCreateChannel.mockResolvedValue({ ...createdChannel, type: "private" });
    renderChannelMode();

    fireEvent.change(nameField(), { target: { value: "Diretoria" } });
    fireEvent.click(screen.getByRole("radio", { name: /privado/i }));
    fireEvent.click(createChannelButton());
    await settle();

    expect(mockCreateChannel.mock.calls[0][0].type).toBe("private");
  });

  // The field caps nothing: the name goes to the server as typed, whatever its
  // length or which plane its characters live in. Only the required-name rule
  // stands between the user and a request.
  // The name cap is a security bound, so the tests are about what the user can
  // actually do with the field — not about which attributes it carries. The one
  // attribute assertion is negative: maxLength must be absent, because the
  // browser counts UTF-16 units and would silently halve a pasted emoji name.
  it.each([
    ["ascii", "a"],
    ["emoji", "😀"],
  ])("submits exactly the limit in %s and blocks one past it", async (_label, unit) => {
    mockCreateChannel.mockResolvedValue(createdChannel);
    renderChannelMode();
    expect(nameField()).not.toHaveAttribute("maxlength");

    const overLimit = unit.repeat(101);
    fireEvent.change(nameField(), { target: { value: overLimit } });
    fireEvent.change(slugField(), { target: { value: "infra" } });

    // Nothing is taken from the user: the value stays whole and editable.
    expect(nameField()).toHaveValue(overLimit);
    expect(screen.getByRole("alert")).toHaveTextContent(/100 caracteres/);
    expect(createChannelButton()).toBeDisabled();

    // Shortening it clears the problem and re-enables submission.
    const atLimit = unit.repeat(100);
    fireEvent.change(nameField(), { target: { value: atLimit } });
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
    expect(createChannelButton()).toBeEnabled();

    fireEvent.click(createChannelButton());
    await settle();
    expect(mockCreateChannel).toHaveBeenCalledTimes(1);
    expect(mockCreateChannel.mock.calls[0][0].displayName).toBe(atLimit);
  });

  it("keeps a pasted over-limit name intact instead of truncating it", async () => {
    renderChannelMode();

    const pasted = "a".repeat(60) + "😀".repeat(60);
    fireEvent.paste(nameField(), {
      clipboardData: { getData: () => pasted },
    });
    // JSDOM does not apply a paste to a controlled input on its own, so the
    // resulting change is what the browser would dispatch next.
    fireEvent.change(nameField(), { target: { value: pasted } });

    expect(nameField()).toHaveValue(pasted);
    expect(Array.from((nameField() as HTMLInputElement).value)).toHaveLength(120);
    expect(screen.getByRole("alert")).toHaveTextContent(/100 caracteres/);
    expect(createChannelButton()).toBeDisabled();

    fireEvent.click(createChannelButton());
    await settle();
    expect(mockCreateChannel).not.toHaveBeenCalled();
  });

  it("associates the name error with the field for assistive technology", () => {
    renderChannelMode();
    fireEvent.change(nameField(), { target: { value: "😀".repeat(101) } });

    const field = nameField();
    const alert = screen.getByRole("alert");
    expect(field).toHaveAttribute("aria-invalid", "true");
    expect(field).toHaveAttribute("aria-describedby", alert.id);
    expect(alert.id).not.toBe("");

    fireEvent.change(nameField(), { target: { value: "Infra" } });
    expect(nameField()).toHaveAttribute("aria-invalid", "false");
    expect(nameField()).not.toHaveAttribute("aria-describedby");
  });

  // Client validation is a courtesy; the server is the authority, and its
  // refusal still has to reach the user.
  it("still surfaces a server-side rejection of a name it considered valid", async () => {
    mockCreateChannel.mockRejectedValue(new ApiRequestError(400, "bad_request", "invalid channel"));
    renderChannelMode();

    fireEvent.change(nameField(), { target: { value: "Infraestrutura" } });
    fireEvent.click(createChannelButton());
    await settle();

    expect(screen.getByRole("alert")).toHaveTextContent(/revise o nome/i);
    expect(mockCreateChannel).toHaveBeenCalledTimes(1);
  });

  it("keeps submit unavailable until a name is typed", () => {
    renderChannelMode();

    expect(createChannelButton()).toBeDisabled();
    fireEvent.change(nameField(), { target: { value: "Infra" } });
    expect(createChannelButton()).toBeEnabled();
  });

  it.each([
    ["Geral", undefined, /reservado/i],
    ["Infra", "-infra", /minúsculas/i],
  ])("rejects %s locally without calling the API", async (name, slug, expected) => {
    renderChannelMode();

    fireEvent.change(nameField(), { target: { value: name } });
    if (slug !== undefined) fireEvent.change(slugField(), { target: { value: slug } });
    fireEvent.click(createChannelButton());
    await settle();

    expect(screen.getByRole("alert")).toHaveTextContent(expected);
    expect(mockCreateChannel).not.toHaveBeenCalled();
  });

  // A denial, an expired session and an outage are different problems with
  // different fixes, so each status reaches the user as its own wording.
  it.each([
    [403, /permissão/i],
    [401, /sessão/i],
    [409, /já existe/i],
    [400, /revise/i],
    [429, /aguarde/i],
    [0, /conexão/i],
  ])("explains status %i in its own terms", async (status, expected) => {
    mockCreateChannel.mockRejectedValue(new ApiRequestError(status, "err", "server detail"));
    const props = renderChannelMode();

    fireEvent.change(nameField(), { target: { value: "Infra" } });
    fireEvent.click(createChannelButton());
    await settle();

    const alert = screen.getByRole("alert");
    expect(alert).toHaveTextContent(expected);
    // Server-provided text is never echoed back into the UI.
    expect(alert).not.toHaveTextContent(/server detail/i);
    expect(props.onChannelCreated).not.toHaveBeenCalled();
  });

  it("keeps the dialog open with the fields intact so a retry costs one click", async () => {
    mockCreateChannel.mockRejectedValueOnce(new ApiRequestError(500, "err", "boom"));
    const props = renderChannelMode();

    fireEvent.change(nameField(), { target: { value: "Infra" } });
    fireEvent.click(createChannelButton());
    await settle();

    expect(props.onClose).not.toHaveBeenCalled();
    expect(nameField()).toHaveValue("Infra");

    mockCreateChannel.mockResolvedValueOnce(createdChannel);
    fireEvent.click(createChannelButton());
    await settle();
    expect(props.onChannelCreated).toHaveBeenCalledWith("ch-1");
  });

  it("sends exactly one request when the button is clicked repeatedly", async () => {
    const pending = deferred<Channel>();
    mockCreateChannel.mockReturnValue(pending.promise);
    renderChannelMode();

    fireEvent.change(nameField(), { target: { value: "Infra" } });
    fireEvent.click(createChannelButton());
    fireEvent.click(createChannelButton());
    fireEvent.click(createChannelButton());

    expect(mockCreateChannel).toHaveBeenCalledTimes(1);
    expect(createChannelButton()).toBeDisabled();
    expect(createChannelButton()).toHaveAttribute("aria-busy", "true");

    await act(async () => {
      pending.resolve(createdChannel);
      await pending.promise;
    });
  });

  it("holds the dialog shut while a channel creation is in flight", async () => {
    const pending = deferred<Channel>();
    mockCreateChannel.mockReturnValue(pending.promise);
    const props = renderChannelMode();

    fireEvent.change(nameField(), { target: { value: "Infra" } });
    fireEvent.click(createChannelButton());

    fireEvent.keyDown(screen.getByRole("dialog"), { key: "Escape" });
    expect(props.onClose).not.toHaveBeenCalled();
    expect(screen.getByRole("button", { name: "Fechar nova conversa" })).toBeDisabled();
    // The mode cannot be switched out from under an in-flight write either.
    expect(screen.getByRole("radio", { name: "Pessoa" })).toBeDisabled();

    await act(async () => {
      pending.resolve(createdChannel);
      await pending.promise;
    });
  });

  it("creates a new category first and files the channel under it", async () => {
    mockCreateChannelCategory.mockResolvedValue({ id: "cat-new", name: "Projetos" });
    mockCreateChannel.mockResolvedValue(createdChannel);
    renderChannelMode();

    fireEvent.change(nameField(), { target: { value: "Infra" } });
    fireEvent.change(screen.getByLabelText("Categoria"), { target: { value: "__new__" } });
    fireEvent.click(createChannelButton());
    await settle();
    expect(screen.getByRole("alert")).toHaveTextContent("Digite o nome da nova categoria.");
    expect(mockCreateChannelCategory).not.toHaveBeenCalled();

    fireEvent.change(screen.getByLabelText("Nome da nova categoria"), {
      target: { value: "Projetos" },
    });
    fireEvent.click(createChannelButton());
    await settle();
    await settle();

    expect(mockCreateChannelCategory).toHaveBeenCalledWith("Projetos", expect.any(AbortSignal));
    expect(mockCreateChannel.mock.calls[0][0]).toMatchObject({ categoryId: "cat-new" });
  });

  it("closes on Escape without submitting", () => {
    const props = renderChannelMode();

    fireEvent.change(nameField(), { target: { value: "Infra" } });
    fireEvent.keyDown(screen.getByRole("dialog"), { key: "Escape" });

    expect(props.onClose).toHaveBeenCalled();
    expect(mockCreateChannel).not.toHaveBeenCalled();
  });

  it("returns to the people search when the mode goes back to Pessoa", async () => {
    mockSearchDMCandidates.mockResolvedValue([{ userId: "user-2", displayName: "Joana Silva" }]);
    renderChannelMode();

    fireEvent.click(screen.getByRole("radio", { name: "Pessoa" }));
    const input = screen.getByRole("searchbox", { name: "Pesquisar pessoa" });
    fireEvent.change(input, { target: { value: "jo" } });
    await advanceSearch();

    expect(screen.getByRole("button", { name: "Joana Silva" })).toBeInTheDocument();
    // Kept mounted for its draft, but out of sight and out of the a11y tree.
    expect(nameField()).not.toBeVisible();
  });
});

// ── Independent drafts and the shell (issue #1023) ────────────────────────────
// Each flow keeps its own state while the dialog is open; the shell only knows
// whether a write is running.

function chooseMode(name: "Pessoa" | "Grupo" | "Canal") {
  const radio = screen.getByRole("radio", { name });
  radio.focus();
  fireEvent.click(radio);
}

async function fillGroupDraft() {
  chooseMode("Grupo");
  await searchAndSelect("jo", groupCandidates, ["Joana", "Marcos"]);
  continueToIdentity();
  fireEvent.change(screen.getByLabelText("Nome do grupo (opcional)"), {
    target: { value: "Infra 🙂" },
  });
}

// The draft spans both steps: the flow comes back on Identidade, and going
// back to Participantes finds the selection and the query intact.
function expectGroupDraft() {
  expect(screen.getByLabelText("Nome do grupo (opcional)")).toHaveValue("Infra 🙂");
  expect(screen.getByRole("button", { name: "Criar grupo" })).toBeEnabled();
  fireEvent.click(screen.getByRole("button", { name: "Voltar" }));
  const chips = screen.getByRole("list", { name: "Pessoas selecionadas" });
  expect(chips).toHaveTextContent("Joana");
  expect(chips).toHaveTextContent("Marcos");
  expect(screen.getByRole("searchbox", { name: "Pesquisar pessoa" })).toHaveValue("jo");
  expect(screen.getByRole("button", { name: "Joana" })).toHaveAttribute("aria-pressed", "true");
  expect(screen.getByRole("button", { name: "Continuar" })).toBeEnabled();
}

const draftCategories: ChannelCategory[] = [{ id: "cat-1", name: "Projetos", kind: "category" }];

function fillChannelDraft() {
  chooseMode("Canal");
  fireEvent.change(nameField(), { target: { value: "Operações 🚀" } });
  fireEvent.change(slugField(), { target: { value: "ops" } });
  fireEvent.click(screen.getByRole("radio", { name: /privado/i }));
  fireEvent.change(screen.getByLabelText("Categoria"), { target: { value: "cat-1" } });
}

function expectChannelDraft() {
  expect(nameField()).toHaveValue("Operações 🚀");
  expect(slugField()).toHaveValue("ops");
  expect(screen.getByRole("radio", { name: /privado/i })).toBeChecked();
  expect(screen.getByLabelText("Categoria")).toHaveValue("cat-1");
}

describe("NewConversationDialog — independent drafts", () => {
  it.each([["Canal"], ["Pessoa"]] as const)(
    "keeps the group draft across Grupo → %s → Grupo",
    async (detour) => {
      const props = renderDialog();
      await fillGroupDraft();

      chooseMode(detour);
      expect(screen.queryByRole("button", { name: "Criar grupo" })).not.toBeInTheDocument();
      chooseMode("Grupo");

      expectGroupDraft();
      expect(props.onClose).not.toHaveBeenCalled();
    },
  );

  it.each([["Pessoa"], ["Grupo"]] as const)(
    "keeps the channel draft across Canal → %s → Canal",
    (detour) => {
      renderDialog({ categories: draftCategories });
      fillChannelDraft();

      chooseMode(detour);
      expect(nameField()).not.toBeVisible();
      chooseMode("Canal");

      expectChannelDraft();
    },
  );

  it("submits the channel draft it kept, once, with the fields it had", async () => {
    mockCreateChannel.mockResolvedValue(createdChannel);
    const props = renderDialog({ categories: draftCategories });
    fillChannelDraft();
    chooseMode("Grupo");
    chooseMode("Canal");

    fireEvent.click(createChannelButton());
    fireEvent.click(createChannelButton());
    await settle();

    expect(mockCreateChannel).toHaveBeenCalledTimes(1);
    expect(mockCreateChannel.mock.calls[0][0]).toEqual({
      slug: "ops",
      displayName: "Operações 🚀",
      type: "private",
      categoryId: "cat-1",
      initialMemberIds: [],
      idempotencyKey: expect.any(String),
    });
    expect(props.onChannelCreated).toHaveBeenCalledWith("ch-1");
  });

  it("keeps the person search and the group search apart", async () => {
    mockSearchDMCandidates.mockResolvedValue([groupCandidates[0]]);
    renderDialog();
    fireEvent.change(screen.getByRole("searchbox"), { target: { value: "jo" } });
    await advanceSearch();

    chooseMode("Grupo");
    expect(screen.getByRole("searchbox")).toHaveValue("");
    expect(screen.getByText("Digite pelo menos 2 caracteres.")).toBeInTheDocument();
  });

  // A hidden Group flow keeps its draft but must not keep searching.
  it.each([["Canal"], ["Pessoa"]] as const)(
    "starts no group search after leaving for %s before the debounce",
    async (detour) => {
      renderDialog();
      chooseMode("Grupo");
      fireEvent.change(screen.getByRole("searchbox"), { target: { value: "jo" } });

      chooseMode(detour);
      await advanceSearch();
      await advanceSearch();

      expect(mockSearchDMCandidates).not.toHaveBeenCalled();
    },
  );

  it.each([["Canal"], ["Pessoa"]] as const)(
    "aborts an in-flight group search on leaving for %s and ignores its late answer",
    async (detour) => {
      const stale = deferred<DMCandidate[]>();
      const fresh = deferred<DMCandidate[]>();
      mockSearchDMCandidates.mockReturnValueOnce(stale.promise).mockReturnValueOnce(fresh.promise);
      renderDialog();
      chooseMode("Grupo");
      fireEvent.change(screen.getByRole("searchbox"), { target: { value: "jo" } });
      await advanceSearch();
      const groupSignal = mockSearchDMCandidates.mock.calls[0][1];

      chooseMode(detour);
      expect(groupSignal?.aborted).toBe(true);
      await act(async () => stale.resolve([groupCandidates[1]]));
      // Nor does the hidden flow start a replacement request.
      await advanceSearch();
      expect(mockSearchDMCandidates).toHaveBeenCalledTimes(1);

      // Back in Grupo the interrupted search resumes for the same query, and
      // only its answer is shown — never the abandoned one.
      chooseMode("Grupo");
      expect(screen.getByRole("searchbox")).toHaveValue("jo");
      expect(screen.getByRole("status")).toHaveTextContent("Buscando pessoas");
      expect(screen.queryByRole("button", { name: "Marcos" })).not.toBeInTheDocument();
      await advanceSearch();
      await act(async () => fresh.resolve([groupCandidates[0]]));

      expect(screen.getByRole("button", { name: "Joana" })).toBeInTheDocument();
      expect(screen.queryByRole("button", { name: "Marcos" })).not.toBeInTheDocument();
      expect(mockSearchDMCandidates).toHaveBeenCalledTimes(2);
    },
  );

  it("resumes an interrupted group search once and does not re-run a settled one", async () => {
    mockSearchDMCandidates.mockResolvedValue([groupCandidates[0]]);
    renderDialog();
    chooseMode("Grupo");
    fireEvent.change(screen.getByRole("searchbox"), { target: { value: "jo" } });
    chooseMode("Canal");
    chooseMode("Grupo");

    expect(screen.getByRole("searchbox")).toHaveValue("jo");
    await advanceSearch();
    expect(mockSearchDMCandidates).toHaveBeenCalledTimes(1);
    expect(mockSearchDMCandidates).toHaveBeenCalledWith("jo", expect.any(AbortSignal));
    fireEvent.click(screen.getByRole("button", { name: "Joana" }));

    // Settled results are part of the draft: a round trip shows them again
    // without asking the server twice.
    chooseMode("Canal");
    chooseMode("Grupo");
    await advanceSearch();
    expect(mockSearchDMCandidates).toHaveBeenCalledTimes(1);
    expect(screen.getByRole("button", { name: "Joana" })).toHaveAttribute("aria-pressed", "true");

    // And the search keeps working normally for a new query.
    mockSearchDMCandidates.mockResolvedValue([groupCandidates[2]]);
    fireEvent.change(screen.getByRole("searchbox"), { target: { value: "ri" } });
    await advanceSearch();
    expect(mockSearchDMCandidates).toHaveBeenCalledTimes(2);
    expect(screen.getByRole("button", { name: "Rita" })).toBeInTheDocument();
  });

  it("cancels the person search when Pessoa unmounts", async () => {
    const pending = deferred<DMCandidate[]>();
    mockSearchDMCandidates.mockReturnValueOnce(pending.promise);
    renderDialog();
    fireEvent.change(screen.getByRole("searchbox"), { target: { value: "jo" } });
    await advanceSearch();
    const personSignal = mockSearchDMCandidates.mock.calls[0][1];

    chooseMode("Grupo");
    expect(personSignal?.aborted).toBe(true);
    await act(async () => pending.resolve([groupCandidates[0]]));
    chooseMode("Pessoa");
    expect(screen.queryByRole("button", { name: "Joana" })).not.toBeInTheDocument();
  });

  it("freezes the mode switch and Escape while a DM is opening", async () => {
    const request = deferred<DirectDMResult>();
    mockSearchDMCandidates.mockResolvedValue([groupCandidates[0]]);
    mockGetOrCreateDirectDM.mockReturnValue(request.promise);
    const props = renderDialog();

    fireEvent.change(screen.getByRole("searchbox"), { target: { value: "jo" } });
    await advanceSearch();
    fireEvent.click(screen.getByRole("button", { name: "Joana" }));

    for (const name of ["Pessoa", "Grupo", "Canal"]) {
      expect(screen.getByRole("radio", { name })).toBeDisabled();
    }
    fireEvent.keyDown(screen.getByRole("dialog"), { key: "Escape" });
    fireEvent.mouseDown(screen.getByRole("dialog").parentElement!);
    expect(props.onClose).not.toHaveBeenCalled();

    await act(async () => request.resolve({ conversationId: "dm-1", created: false }));
    expect(screen.getByRole("radio", { name: "Grupo" })).toBeEnabled();
  });

  it("keeps the Tab trap on the visible flow when others are hidden", () => {
    renderDialog();
    chooseMode("Canal");
    chooseMode("Grupo");
    chooseMode("Pessoa");
    const dialog = screen.getByRole("dialog");
    const search = screen.getByRole("searchbox", { name: "Pesquisar pessoa" });
    const close = screen.getByRole("button", { name: "Fechar nova conversa" });

    // The person search is the last visible control: hidden channel and group
    // fields after it in the DOM must not count as the trap's edge.
    search.focus();
    fireEvent.keyDown(dialog, { key: "Tab" });
    expect(close).toHaveFocus();
    fireEvent.keyDown(dialog, { key: "Tab", shiftKey: true });
    expect(search).toHaveFocus();
  });

  it("describes the active mode to assistive technology", () => {
    renderDialog();
    const dialog = screen.getByRole("dialog", { name: "Nova conversa" });

    expect(dialog).toHaveAccessibleDescription(/encontre uma pessoa/i);
    chooseMode("Grupo");
    expect(dialog).toHaveAccessibleDescription(/pelo menos 2 pessoas/i);
    chooseMode("Canal");
    expect(dialog).toHaveAccessibleDescription(/canais públicos/i);
  });
});

// ── Group identity step (issue #1026) ────────────────────────────────────────

async function settleLazy() {
  await act(async () => {
    await vi.dynamicImportSettled();
  });
}

const preview = () => screen.getByRole("img", { name: /Prévia da identidade/ });

async function openIdentityStep() {
  const props = renderDialog();
  switchToGroup();
  await searchAndSelect("jo", groupCandidates, ["Joana", "Marcos"]);
  continueToIdentity();
  return props;
}

async function chooseEmoji(emoji: string) {
  fireEvent.click(screen.getByRole("radio", { name: "Emoji" }));
  await settleLazy();
  fireEvent.click(screen.getByRole("button", { name: emoji }));
}

describe("NewConversationDialog — group identity (issue #1026)", () => {
  it("moves Participantes → Identidade with focus on the name and Automático chosen", async () => {
    await openIdentityStep();

    expect(screen.getByLabelText("Nome do grupo (opcional)")).toHaveFocus();
    expect(screen.queryByRole("searchbox")).not.toBeInTheDocument();
    expect(screen.getByRole("group", { name: "Identidade do grupo" })).toBeInTheDocument();
    expect(screen.getByRole("radio", { name: "Automático" })).toBeChecked();
    expect(screen.getByRole("radio", { name: "Emoji" })).not.toBeChecked();
    // Untitled, the preview shows the name the server will give the group.
    expect(preview()).toHaveAccessibleName("Prévia da identidade: iniciais GD");
  });

  it("recomputes the automatic preview as the name is typed, without any request", async () => {
    await openIdentityStep();

    fireEvent.change(screen.getByLabelText("Nome do grupo (opcional)"), {
      target: { value: "Infra Web" },
    });
    expect(preview()).toHaveAccessibleName("Prévia da identidade: iniciais IW");
    expect(preview().textContent).toBe("IW");
    fireEvent.change(screen.getByLabelText("Nome do grupo (opcional)"), {
      target: { value: "Plataforma" },
    });
    expect(preview().textContent).toBe("P");
    expect(mockCreateGroupDM).not.toHaveBeenCalled();
  });

  it("previews a chosen emoji and keeps Criar grupo off until one is chosen", async () => {
    await openIdentityStep();

    fireEvent.click(screen.getByRole("radio", { name: "Emoji" }));
    expect(screen.getByText("Escolha um emoji para o grupo.")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Criar grupo" })).toBeDisabled();
    await settleLazy();
    fireEvent.click(screen.getByRole("button", { name: "👩‍💻" }));

    expect(preview()).toHaveAccessibleName("Prévia da identidade: emoji 👩‍💻");
    expect(preview().textContent).toBe("👩‍💻");
    expect(screen.getByRole("button", { name: "Criar grupo" })).toBeEnabled();
    expect(mockCreateGroupDM).not.toHaveBeenCalled();
  });

  it("keeps participants, name and emoji across Identidade → Participantes → Identidade", async () => {
    await openIdentityStep();
    fireEvent.change(screen.getByLabelText("Nome do grupo (opcional)"), {
      target: { value: "Infra" },
    });
    await chooseEmoji("🎉");

    fireEvent.click(screen.getByRole("button", { name: "Voltar" }));
    expect(screen.getByRole("searchbox", { name: "Pesquisar pessoa" })).toHaveFocus();
    const chips = screen.getByRole("list", { name: "Pessoas selecionadas" });
    expect(chips).toHaveTextContent("Joana");
    expect(chips).toHaveTextContent("Marcos");

    continueToIdentity();
    await settleLazy();
    expect(screen.getByLabelText("Nome do grupo (opcional)")).toHaveValue("Infra");
    expect(screen.getByRole("radio", { name: "Emoji" })).toBeChecked();
    expect(preview().textContent).toBe("🎉");
    expect(mockCreateGroupDM).not.toHaveBeenCalled();
  });

  it("submits the chosen emoji", async () => {
    mockCreateGroupDM.mockResolvedValue("dm-group");
    const props = await openIdentityStep();
    await chooseEmoji("👩‍💻");

    fireEvent.click(screen.getByRole("button", { name: "Criar grupo" }));
    await act(async () => Promise.resolve());

    expect(mockCreateGroupDM).toHaveBeenCalledTimes(1);
    expect(mockCreateGroupDM).toHaveBeenCalledWith(
      ["user-2", "user-3"],
      "",
      "👩‍💻",
      expect.any(AbortSignal),
    );
    expect(props.onOpened).toHaveBeenCalledWith("dm-group");
  });

  it("drops the emoji when switching back to Automático, so none is sent", async () => {
    mockCreateGroupDM.mockResolvedValue("dm-group");
    await openIdentityStep();
    fireEvent.change(screen.getByLabelText("Nome do grupo (opcional)"), {
      target: { value: "Infra" },
    });
    await chooseEmoji("🎉");

    fireEvent.click(screen.getByRole("radio", { name: "Automático" }));
    expect(preview().textContent).toBe("I");
    expect(screen.queryByRole("button", { name: "🎉" })).not.toBeInTheDocument();
    // Emoji again starts empty: the previous choice did not survive.
    fireEvent.click(screen.getByRole("radio", { name: "Emoji" }));
    expect(screen.getByRole("button", { name: "Criar grupo" })).toBeDisabled();
    fireEvent.click(screen.getByRole("radio", { name: "Automático" }));

    fireEvent.click(screen.getByRole("button", { name: "Criar grupo" }));
    await act(async () => Promise.resolve());
    expect(mockCreateGroupDM).toHaveBeenCalledWith(
      ["user-2", "user-3"],
      "Infra",
      undefined,
      expect.any(AbortSignal),
    );
  });

  it("locks the identity while the creation is pending", async () => {
    const request = deferred<string>();
    mockCreateGroupDM.mockReturnValue(request.promise);
    await openIdentityStep();
    await chooseEmoji("🎉");

    fireEvent.click(screen.getByRole("button", { name: "Criar grupo" }));

    expect(screen.getByRole("radio", { name: "Automático" })).toBeDisabled();
    expect(screen.getByRole("button", { name: "Voltar" })).toBeDisabled();
    expect(screen.queryByRole("group", { name: "Emojis de teste" })).not.toBeInTheDocument();
    await act(async () => request.resolve("dm-group"));
  });
});
