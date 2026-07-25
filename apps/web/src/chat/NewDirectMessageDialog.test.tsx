import { act, fireEvent, render, screen, within } from "@testing-library/react";
import type { ComponentProps } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { ApiRequestError } from "../lib/api";
import type { DMCandidate, DirectDMResult } from "./chatTypes";
import { MAX_GROUP_MEMBERS } from "./dmGroupForm";
import NewDirectMessageDialog from "./NewDirectMessageDialog";

const { mockSearchDMCandidates, mockGetOrCreateDirectDM, mockCreateGroupDM } = vi.hoisted(() => ({
  mockSearchDMCandidates: vi.fn<(query: string, signal?: AbortSignal) => Promise<DMCandidate[]>>(),
  mockGetOrCreateDirectDM:
    vi.fn<(userId: string, signal?: AbortSignal) => Promise<DirectDMResult>>(),
  mockCreateGroupDM:
    vi.fn<(userIds: string[], title: string, signal?: AbortSignal) => Promise<string>>(),
}));

vi.mock("./chatApi", () => ({
  searchDMCandidates: (query: string, signal?: AbortSignal) =>
    mockSearchDMCandidates(query, signal),
  getOrCreateDirectDM: (userId: string, signal?: AbortSignal) =>
    mockGetOrCreateDirectDM(userId, signal),
  createGroupDM: (userIds: string[], title: string, signal?: AbortSignal) =>
    mockCreateGroupDM(userIds, title, signal),
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

function renderDialog(overrides: Partial<ComponentProps<typeof NewDirectMessageDialog>> = {}) {
  const props = {
    currentUserId: "current-user",
    onClose: vi.fn(),
    onOpened: vi.fn(),
    ...overrides,
  };
  render(<NewDirectMessageDialog {...props} />);
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

describe("NewDirectMessageDialog", () => {
  it("renders an accessible focused search field and skips empty or short queries", async () => {
    renderDialog();

    const input = screen.getByRole("searchbox", { name: "Pesquisar pessoa" });
    expect(input).toHaveFocus();
    expect(screen.getByRole("dialog", { name: "Nova mensagem" })).toBeInTheDocument();

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
    expect(screen.getByRole("button", { name: "Fechar nova mensagem" })).toBeDisabled();
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
    const close = screen.getByRole("button", { name: "Fechar nova mensagem" });

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
      <NewDirectMessageDialog currentUserId="current-user" onClose={vi.fn()} onOpened={onOpened} />,
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
    <NewDirectMessageDialog currentUserId="current-user" onClose={vi.fn()} onOpened={vi.fn()} />,
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

const groupCandidates: DMCandidate[] = [
  { userId: "user-2", displayName: "Joana" },
  { userId: "user-3", displayName: "Marcos" },
  { userId: "user-4", displayName: "Rita" },
];

describe("NewDirectMessageDialog — group mode", () => {
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
    expect(screen.getByLabelText("Nome do grupo (opcional)")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Criar grupo" })).toBeDisabled();
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
    expect(screen.getByRole("button", { name: "Criar grupo" })).toBeDisabled();
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

    const submit = screen.getByRole("button", { name: "Criar grupo" });
    expect(submit).toBeDisabled();
    fireEvent.click(submit);
    expect(mockCreateGroupDM).not.toHaveBeenCalled();

    fireEvent.click(screen.getByRole("button", { name: "Marcos" }));
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
      expect.any(AbortSignal),
    );
    const busy = screen.getByRole("button", { name: "Criando…" });
    expect(busy).toBeDisabled();
    expect(busy).toHaveAttribute("aria-busy", "true");
    expect(screen.getByRole("button", { name: "Fechar nova mensagem" })).toBeDisabled();
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

    fireEvent.click(screen.getByRole("button", { name: "Criar grupo" }));
    await act(async () => Promise.resolve());

    expect(mockCreateGroupDM).toHaveBeenCalledWith(
      ["user-2", "user-3"],
      "",
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

    fireEvent.click(screen.getByRole("button", { name: "Criar grupo" }));
    await act(async () => Promise.resolve());

    expect(screen.getByRole("alert")).toHaveTextContent("Não foi possível criar o grupo");
    expect(screen.queryByText(/sql/i)).not.toBeInTheDocument();
    expect(props.onOpened).not.toHaveBeenCalled();
    expect(screen.getByRole("list", { name: "Pessoas selecionadas" })).toHaveTextContent("Joana");

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

    // Selecting is a functional state update, so filling the group in one batch
    // is equivalent to 49 separate clicks and keeps the test fast.
    const rows = within(screen.getByRole("list", { name: "Pessoas encontradas" })).getAllByRole(
      "button",
    );
    await act(async () => {
      rows.slice(0, MAX_GROUP_MEMBERS).forEach((row) => row.click());
    });

    expect(
      screen.getByText(`Limite de ${MAX_GROUP_MEMBERS} pessoas atingido.`),
    ).toBeInTheDocument();
    const overflow = screen.getByRole("button", { name: `Pessoa ${MAX_GROUP_MEMBERS}` });
    expect(overflow).toBeDisabled();

    // An already selected row stays clickable, so the only way out of the cap is
    // removing someone — the selection is never silently rewritten.
    const selectedRow = screen.getByRole("button", { name: "Pessoa 0" });
    expect(selectedRow).toBeEnabled();
    fireEvent.click(selectedRow);
    expect(
      screen.getByText(`${MAX_GROUP_MEMBERS - 1} de no mínimo 2 pessoas selecionadas.`),
    ).toBeInTheDocument();
    expect(screen.getByRole("button", { name: `Pessoa ${MAX_GROUP_MEMBERS}` })).toBeEnabled();
  });

  it("renders a hostile group name as text", async () => {
    renderDialog();
    switchToGroup();

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

    const nameField = screen.getByLabelText("Nome do grupo (opcional)");
    fireEvent.change(nameField, { target: { value: "a".repeat(121) } });
    expect(nameField).toHaveValue("a".repeat(120));
  });
});
