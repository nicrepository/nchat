import { act, fireEvent, render, screen, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { ApiRequestError } from "../lib/api";
import { maxAddMembersPerRequest } from "./addMembersLimits";
import type { CreateChannelInput } from "./chatApi";
import ChannelCreationForm from "./ChannelCreationForm";
import type { Channel, ChannelCategory, DMCandidate } from "./chatTypes";

// Issue #1025: the members step of a private channel, and the retry rules of
// the creation write.

const { mockSearch, mockCreateChannel, mockCreateCategory } = vi.hoisted(() => ({
  mockSearch: vi.fn<(query: string, signal?: AbortSignal) => Promise<DMCandidate[]>>(),
  mockCreateChannel: vi.fn<(input: CreateChannelInput, signal?: AbortSignal) => Promise<Channel>>(),
  mockCreateCategory:
    vi.fn<(name: string, signal?: AbortSignal) => Promise<{ id?: string; name: string }>>(),
}));

vi.mock("./chatApi", () => ({
  searchDMCandidates: (query: string, signal?: AbortSignal) => mockSearch(query, signal),
  createChannel: (input: CreateChannelInput, signal?: AbortSignal) =>
    mockCreateChannel(input, signal),
  createChannelCategory: (name: string, signal?: AbortSignal) => mockCreateCategory(name, signal),
}));

const people: DMCandidate[] = [
  { userId: "u-ana", displayName: "Ana" },
  { userId: "u-bia", displayName: "Bia" },
  { userId: "u-caio", displayName: "Caio" },
];
const created: Channel = { id: "ch-new", name: "Infra", type: "private", canWrite: true };

function renderForm(categories: ChannelCategory[] = []) {
  const onCreated = vi.fn();
  const onPendingChange = vi.fn();
  render(
    <ChannelCreationForm
      categories={categories}
      currentUserId="me"
      workspaceId="ws-1"
      onCreated={onCreated}
      onPendingChange={onPendingChange}
    />,
  );
  return { onCreated, onPendingChange };
}

const radio = (name: "Público" | "Privado") => screen.getByRole("radio", { name });
const submitButton = () => screen.getByRole("button", { name: "Criar canal" });
const memberSearch = () => screen.queryByRole("searchbox", { name: "Pesquisar pessoa" });
const chips = () => screen.getByRole("list", { name: "Membros selecionados" });

async function settle() {
  await act(async () => {
    await vi.runOnlyPendingTimersAsync();
  });
}

async function searchFor(query: string) {
  fireEvent.change(memberSearch()!, { target: { value: query } });
  await settle();
}

function pick(name: string) {
  const results = screen.getByRole("list", { name: "Pessoas encontradas" });
  fireEvent.click(within(results).getByRole("button", { name }));
}

async function privateDraftWith(...names: string[]) {
  fireEvent.change(screen.getByLabelText("Nome do canal"), { target: { value: "Infra" } });
  fireEvent.click(radio("Privado"));
  mockSearch.mockResolvedValue(people);
  for (const name of names) {
    await searchFor(name.slice(0, 2));
    pick(name);
  }
}

beforeEach(() => {
  vi.useFakeTimers();
  vi.clearAllMocks();
  mockSearch.mockResolvedValue([]);
});

afterEach(() => {
  vi.useRealTimers();
});

describe("ChannelCreationForm — private members step", () => {
  it("shows the members step only for a private channel, with the creator fixed", () => {
    renderForm();
    expect(memberSearch()).toBeNull();
    expect(screen.getByText("Canal público: todo o workspace poderá entrar.")).toBeInTheDocument();

    fireEvent.click(radio("Privado"));
    expect(memberSearch()).toBeInTheDocument();
    expect(within(chips()).getByText("Você (criador)")).toBeInTheDocument();
    // The creator has no remove control at all.
    expect(within(chips()).queryByRole("button")).toBeNull();
    expect(screen.getByText("1 membro, incluindo você.")).toBeInTheDocument();
    expect(screen.getByText("Canal privado: somente você terá acesso.")).toBeInTheDocument();
  });

  it("searches the workspace without the creator and reports loading, empty and error", async () => {
    renderForm();
    fireEvent.click(radio("Privado"));
    expect(screen.getByText("Digite pelo menos 2 caracteres.")).toBeInTheDocument();

    fireEvent.change(memberSearch()!, { target: { value: "zz" } });
    expect(screen.getByRole("status")).toHaveTextContent("Buscando pessoas…");
    await settle();
    expect(mockSearch).toHaveBeenCalledWith("zz", expect.any(AbortSignal));
    expect(screen.getByText("Nenhuma pessoa encontrada.")).toBeInTheDocument();

    mockSearch.mockResolvedValueOnce([{ userId: "me", displayName: "Eu" }, people[0]]);
    await searchFor("an");
    expect(screen.queryByRole("button", { name: "Eu" })).toBeNull();
    expect(screen.getByRole("button", { name: "Ana" })).toBeInTheDocument();

    mockSearch.mockRejectedValueOnce(new ApiRequestError(500, "err", "boom"));
    await searchFor("bo");
    expect(screen.getByRole("alert")).toHaveTextContent("Não foi possível buscar pessoas.");
  });

  it("drops the answer to a query the user already moved past", async () => {
    let resolveStale!: (value: DMCandidate[]) => void;
    mockSearch.mockImplementationOnce(
      () => new Promise<DMCandidate[]>((resolve) => (resolveStale = resolve)),
    );
    renderForm();
    fireEvent.click(radio("Privado"));
    await searchFor("an");
    mockSearch.mockResolvedValueOnce([people[1]]);
    await searchFor("bi");
    await act(async () => resolveStale([people[0]]));
    expect(screen.queryByRole("button", { name: "Ana" })).toBeNull();
    expect(screen.getByRole("button", { name: "Bia" })).toBeInTheDocument();
  });

  it("selects several people once each, removes one, and counts the creator", async () => {
    renderForm();
    await privateDraftWith("Ana", "Bia");
    // A selected person leaves the results, so they cannot be picked twice.
    await searchFor("an");
    expect(
      within(screen.getByRole("list", { name: "Pessoas encontradas" })).queryByRole("button", {
        name: "Ana",
      }),
    ).toBeNull();
    expect(screen.getByText("3 membros, incluindo você.")).toBeInTheDocument();
    expect(
      screen.getByText("Canal privado: somente você e 2 convidados terão acesso."),
    ).toBeInTheDocument();

    fireEvent.click(screen.getByRole("button", { name: "Remover Ana" }));
    expect(within(chips()).queryByText("Ana")).toBeNull();
    expect(
      screen.getByText("Canal privado: somente você e 1 convidado terão acesso."),
    ).toBeInTheDocument();
  });

  it("stops selecting at the canonical per-request limit", async () => {
    const crowd = Array.from({ length: maxAddMembersPerRequest + 1 }, (_, index) => ({
      userId: `u-${index}`,
      displayName: `Pessoa ${String(index).padStart(2, "0")}`,
    }));
    renderForm();
    fireEvent.click(radio("Privado"));
    mockSearch.mockResolvedValue(crowd);
    await searchFor("pe");
    // Plain DOM lookups: 25 accessible-name queries over 26 rows cost seconds.
    const results = screen.getByRole("list", { name: "Pessoas encontradas" });
    for (let picked = 0; picked < maxAddMembersPerRequest; picked += 1) {
      fireEvent.click(results.querySelector("button")!);
    }

    expect(screen.getByText(/limite de 25 convidados por criação atingido/)).toBeInTheDocument();
    const remaining = results.querySelectorAll("button");
    expect(remaining).toHaveLength(1);
    expect(remaining[0]).toBeDisabled();
    expect(chips().querySelectorAll("li")).toHaveLength(maxAddMembersPerRequest + 1);
  });

  it("keeps the invitees across Privado → Público → Privado, and sends none while public", async () => {
    mockCreateChannel.mockResolvedValue(created);
    const { onCreated } = renderForm();
    await privateDraftWith("Ana");

    fireEvent.click(radio("Público"));
    expect(memberSearch()).toBeNull();
    fireEvent.click(radio("Privado"));
    expect(within(chips()).getByText("Ana")).toBeInTheDocument();

    fireEvent.click(radio("Público"));
    fireEvent.click(submitButton());
    await settle();
    expect(mockCreateChannel.mock.calls[0][0]).toMatchObject({ type: "public" });
    expect(mockCreateChannel.mock.calls[0][0].initialMemberIds).toBeUndefined();
    expect(onCreated).toHaveBeenCalledWith("ch-new");
  });

  it("submits a private channel with exactly the selected IDs, once", async () => {
    mockCreateChannel.mockReturnValue(new Promise(() => {}));
    renderForm();
    await privateDraftWith("Bia", "Ana");

    fireEvent.click(submitButton());
    fireEvent.click(submitButton());
    expect(mockCreateChannel).toHaveBeenCalledTimes(1);
    expect(mockCreateChannel.mock.calls[0][0]).toMatchObject({
      type: "private",
      initialMemberIds: ["u-bia", "u-ana"],
    });
    expect(submitButton()).toBeDisabled();
    expect(screen.getByRole("button", { name: "Remover Ana" })).toBeDisabled();
  });

  it("does not submit the form when Enter is pressed in the member search", async () => {
    renderForm();
    await privateDraftWith("Ana");
    // Enter's default action in a form field is the implicit submit; the
    // search box cancels it, so choosing members never creates the channel.
    const enter = new KeyboardEvent("keydown", { key: "Enter", bubbles: true, cancelable: true });
    memberSearch()!.dispatchEvent(enter);
    expect(enter.defaultPrevented).toBe(true);
    expect(mockCreateChannel).not.toHaveBeenCalled();

    // Every other key keeps its default: the guard is about Enter alone.
    const letter = new KeyboardEvent("keydown", { key: "a", bubbles: true, cancelable: true });
    memberSearch()!.dispatchEvent(letter);
    expect(letter.defaultPrevented).toBe(false);
  });
});

describe("ChannelCreationForm — retry and idempotency", () => {
  it("keeps the draft after a network failure and retries with the same key", async () => {
    mockCreateChannel.mockRejectedValueOnce(new ApiRequestError(0, "network", "offline"));
    const { onCreated } = renderForm();
    await privateDraftWith("Ana");

    fireEvent.click(submitButton());
    await settle();
    expect(
      screen.getByText("Sem conexão. Verifique sua rede e tente novamente."),
    ).toBeInTheDocument();
    expect(screen.getByLabelText("Nome do canal")).toHaveValue("Infra");
    expect(within(chips()).getByText("Ana")).toBeInTheDocument();

    mockCreateChannel.mockResolvedValueOnce(created);
    fireEvent.click(submitButton());
    await settle();
    const [first, retry] = mockCreateChannel.mock.calls.map(([input]) => input.idempotencyKey);
    expect(first).toEqual(expect.any(String));
    expect(retry).toBe(first);
    expect(onCreated).toHaveBeenCalledWith("ch-new");
  });

  it("starts a new intent, with a new key, after a material edit", async () => {
    mockCreateChannel.mockRejectedValue(new ApiRequestError(500, "err", "boom"));
    renderForm();
    await privateDraftWith("Ana");
    fireEvent.click(submitButton());
    await settle();

    await searchFor("bi");
    pick("Bia");
    fireEvent.click(submitButton());
    await settle();

    const [first, second] = mockCreateChannel.mock.calls.map(([input]) => input.idempotencyKey);
    expect(second).not.toBe(first);
    expect(
      screen.getByText("Não foi possível criar o canal. Tente novamente."),
    ).toBeInTheDocument();
  });

  it("creates a new category once and reuses it on the retry", async () => {
    mockCreateCategory.mockResolvedValue({ id: "cat-new", name: "Projetos" });
    mockCreateChannel.mockRejectedValueOnce(new ApiRequestError(503, "err", "busy"));
    mockCreateChannel.mockResolvedValueOnce(created);
    renderForm();
    fireEvent.change(screen.getByLabelText("Nome do canal"), { target: { value: "Infra" } });
    fireEvent.change(screen.getByLabelText("Categoria"), { target: { value: "__new__" } });
    fireEvent.change(screen.getByLabelText("Nome da nova categoria"), {
      target: { value: "Projetos" },
    });

    fireEvent.click(submitButton());
    await settle();
    fireEvent.click(submitButton());
    await settle();

    expect(mockCreateCategory).toHaveBeenCalledTimes(1);
    expect(mockCreateChannel.mock.calls.map(([input]) => input.categoryId)).toEqual([
      "cat-new",
      "cat-new",
    ]);
    expect(mockCreateChannel.mock.calls[1][0].idempotencyKey).toBe(
      mockCreateChannel.mock.calls[0][0].idempotencyKey,
    );
  });

  it("sends no category when the new one came back without an ID", async () => {
    mockCreateCategory.mockResolvedValue({ name: "Projetos" });
    mockCreateChannel.mockResolvedValue(created);
    renderForm();
    fireEvent.change(screen.getByLabelText("Nome do canal"), { target: { value: "Infra" } });
    fireEvent.change(screen.getByLabelText("Categoria"), { target: { value: "__new__" } });
    fireEvent.change(screen.getByLabelText("Nome da nova categoria"), {
      target: { value: "Projetos" },
    });
    fireEvent.click(submitButton());
    await settle();
    expect(mockCreateChannel.mock.calls[0][0].categoryId).toBeUndefined();
  });

  it("falls back to generic copy for a failure that is not an API answer", async () => {
    mockCreateChannel.mockRejectedValueOnce(new Error("socket hang up"));
    renderForm();
    fireEvent.change(screen.getByLabelText("Nome do canal"), { target: { value: "Infra" } });
    fireEvent.click(submitButton());
    await settle();
    expect(screen.getByRole("alert")).toHaveTextContent(
      "Não foi possível criar o canal. Tente novamente.",
    );
  });

  it("tells a reused idempotency key apart from a slug clash", async () => {
    mockCreateChannel.mockRejectedValueOnce(
      new ApiRequestError(409, "idempotency_key_reused", "detail"),
    );
    renderForm();
    fireEvent.change(screen.getByLabelText("Nome do canal"), { target: { value: "Infra" } });
    fireEvent.click(submitButton());
    await settle();
    expect(screen.getByRole("alert")).toHaveTextContent("O canal mudou desde a última tentativa.");
    expect(screen.queryByText("Já existe um canal com esse identificador.")).toBeNull();
  });

  it.each([
    [403, true, "ou alguma pessoa selecionada não está disponível"],
    [403, false, "Você não tem permissão para criar canais neste workspace."],
    [401, false, "Sua sessão expirou."],
    [409, false, "Já existe um canal com esse identificador."],
    [400, false, "Revise o nome, o identificador e as pessoas do canal."],
    [429, false, "Muitas solicitações em sequência."],
  ])("maps %i (invitees: %s) to its own copy", async (status, withInvitees, copy) => {
    mockCreateChannel.mockRejectedValueOnce(new ApiRequestError(status, "err", "detail"));
    renderForm();
    if (withInvitees) {
      await privateDraftWith("Ana");
    } else {
      fireEvent.change(screen.getByLabelText("Nome do canal"), { target: { value: "Infra" } });
    }
    fireEvent.click(submitButton());
    await settle();
    expect(screen.getByRole("alert")).toHaveTextContent(copy);
    expect(screen.queryByText("detail")).toBeNull();
  });
});
