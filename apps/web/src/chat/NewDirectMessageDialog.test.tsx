import { act, fireEvent, render, screen } from "@testing-library/react";
import type { ComponentProps } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { ApiRequestError } from "../lib/api";
import type { DMCandidate, DirectDMResult } from "./chatTypes";
import NewDirectMessageDialog from "./NewDirectMessageDialog";

const { mockSearchDMCandidates, mockGetOrCreateDirectDM } = vi.hoisted(() => ({
  mockSearchDMCandidates: vi.fn<(query: string, signal?: AbortSignal) => Promise<DMCandidate[]>>(),
  mockGetOrCreateDirectDM:
    vi.fn<(userId: string, signal?: AbortSignal) => Promise<DirectDMResult>>(),
}));

vi.mock("./chatApi", () => ({
  searchDMCandidates: (query: string, signal?: AbortSignal) =>
    mockSearchDMCandidates(query, signal),
  getOrCreateDirectDM: (userId: string, signal?: AbortSignal) =>
    mockGetOrCreateDirectDM(userId, signal),
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
