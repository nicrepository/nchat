import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import AddMembersDialog, { type AddMembersTarget } from "./AddMembersDialog";
import { ApiRequestError } from "../lib/api";
import { maxAddMembersPerRequest } from "./addMembersLimits";

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

const channelTarget: AddMembersTarget = { kind: "channel", channelId: "ch-1" };
const groupTarget: AddMembersTarget = { kind: "group", conversationId: "dm-1" };

function candidate(userId: string, displayName: string) {
  return { userId, displayName };
}

function renderDialog(overrides: Partial<Parameters<typeof AddMembersDialog>[0]> = {}) {
  const onClose = vi.fn();
  const onAdded = vi.fn();
  render(
    <AddMembersDialog
      target={channelTarget}
      excludedUserIds={[]}
      onClose={onClose}
      onAdded={onAdded}
      {...overrides}
    />,
  );
  return { onClose, onAdded };
}

/** Types a query and waits for the debounced search to settle. */
async function search(user: ReturnType<typeof userEvent.setup>, query = "an") {
  await user.type(screen.getByLabelText("Pesquisar pessoa"), query);
  await vi.advanceTimersByTimeAsync(200);
}

beforeEach(() => {
  vi.useFakeTimers({ shouldAdvanceTime: true });
  searchChannelMemberCandidates.mockResolvedValue([
    candidate("u-1", "Ana Lima"),
    candidate("u-2", "Bruno Dias"),
  ]);
  searchGroupParticipantCandidates.mockResolvedValue([
    candidate("u-1", "Ana Lima"),
    candidate("u-2", "Bruno Dias"),
  ]);
  addChannelMembers.mockResolvedValue({ added: 1, alreadyMembers: 0, memberCount: 5 });
  addGroupParticipants.mockResolvedValue({ added: 1, alreadyMembers: 0, memberCount: 5 });
});

afterEach(() => {
  vi.runOnlyPendingTimers();
  vi.useRealTimers();
  vi.clearAllMocks();
});

describe("AddMembersDialog — estados de busca", () => {
  it("starts idle and asks for a minimum query", () => {
    renderDialog();

    expect(screen.getByText("Digite pelo menos 2 caracteres.")).toBeInTheDocument();
    expect(searchChannelMemberCandidates).not.toHaveBeenCalled();
  });

  it("does not search below the minimum query length", async () => {
    const user = userEvent.setup({ advanceTimers: vi.advanceTimersByTime });
    renderDialog();

    await user.type(screen.getByLabelText("Pesquisar pessoa"), "a");
    await vi.advanceTimersByTimeAsync(300);

    expect(searchChannelMemberCandidates).not.toHaveBeenCalled();
  });

  it("announces loading, then renders the results", async () => {
    const user = userEvent.setup({ advanceTimers: vi.advanceTimersByTime });
    renderDialog();

    await user.type(screen.getByLabelText("Pesquisar pessoa"), "an");
    expect(screen.getByRole("status")).toHaveTextContent("Buscando pessoas…");

    await vi.advanceTimersByTimeAsync(200);
    expect(await screen.findByRole("button", { name: /Ana Lima/ })).toBeInTheDocument();
  });

  it("debounces so a typed word issues one search", async () => {
    const user = userEvent.setup({ advanceTimers: vi.advanceTimersByTime });
    renderDialog();

    await user.type(screen.getByLabelText("Pesquisar pessoa"), "ana");
    await vi.advanceTimersByTimeAsync(300);

    expect(searchChannelMemberCandidates).toHaveBeenCalledTimes(1);
    expect(searchChannelMemberCandidates).toHaveBeenLastCalledWith(
      "ch-1",
      "ana",
      expect.any(AbortSignal),
    );
  });

  it("shows an empty state when nobody matches", async () => {
    searchChannelMemberCandidates.mockResolvedValue([]);
    const user = userEvent.setup({ advanceTimers: vi.advanceTimersByTime });
    renderDialog();

    await search(user);

    expect(
      await screen.findByText("Nenhuma pessoa disponível para adicionar."),
    ).toBeInTheDocument();
  });

  it("reports a search failure and can retry", async () => {
    searchChannelMemberCandidates.mockRejectedValueOnce(new Error("network"));
    const user = userEvent.setup({ advanceTimers: vi.advanceTimersByTime });
    renderDialog();

    await search(user);

    const alert = await screen.findByRole("alert");
    expect(alert).toHaveTextContent("Não foi possível buscar pessoas.");

    searchChannelMemberCandidates.mockResolvedValue([candidate("u-1", "Ana Lima")]);
    await user.click(within(alert).getByRole("button", { name: "Tentar novamente" }));
    await vi.advanceTimersByTimeAsync(200);

    expect(await screen.findByRole("button", { name: /Ana Lima/ })).toBeInTheDocument();
  });

  // Current participants are removed from the results, so the workspace running
  // out of addable people lands in the same honest empty state.
  it("omits people who already participate", async () => {
    const user = userEvent.setup({ advanceTimers: vi.advanceTimersByTime });
    renderDialog({ excludedUserIds: ["u-1"] });

    await search(user);

    expect(await screen.findByRole("button", { name: /Bruno Dias/ })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /Ana Lima/ })).not.toBeInTheDocument();
  });
});

describe("AddMembersDialog — seleção", () => {
  it("selects several people and shows them as chips", async () => {
    const user = userEvent.setup({ advanceTimers: vi.advanceTimersByTime });
    renderDialog();

    await search(user);
    await user.click(await screen.findByRole("button", { name: /Ana Lima/ }));
    await user.click(await screen.findByRole("button", { name: /Bruno Dias/ }));

    const chips = screen.getByRole("list", { name: "Pessoas selecionadas" });
    expect(within(chips).getByText("Ana Lima")).toBeInTheDocument();
    expect(within(chips).getByText("Bruno Dias")).toBeInTheDocument();
  });

  // Someone already chosen leaves the result list, so there is no control that
  // could select them a second time.
  it("prevents selecting the same person twice", async () => {
    const user = userEvent.setup({ advanceTimers: vi.advanceTimersByTime });
    renderDialog();

    await search(user);
    await user.click(await screen.findByRole("button", { name: /Ana Lima/ }));

    const results = screen.getByRole("list", { name: "Pessoas encontradas" });
    expect(within(results).queryByRole("button", { name: /Ana Lima/ })).not.toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: "Adicionar" }));
    expect(addChannelMembers).toHaveBeenCalledWith("ch-1", ["u-1"], expect.any(AbortSignal));
  });

  it("removes one selected person individually", async () => {
    const user = userEvent.setup({ advanceTimers: vi.advanceTimersByTime });
    renderDialog();

    await search(user);
    await user.click(await screen.findByRole("button", { name: /Ana Lima/ }));
    await user.click(await screen.findByRole("button", { name: /Bruno Dias/ }));
    await user.click(screen.getByRole("button", { name: "Remover Ana Lima" }));

    await user.click(screen.getByRole("button", { name: "Adicionar" }));
    expect(addChannelMembers).toHaveBeenCalledWith("ch-1", ["u-2"], expect.any(AbortSignal));
  });

  it("keeps the confirm button disabled with an empty selection", () => {
    renderDialog();

    expect(screen.getByRole("button", { name: "Adicionar" })).toBeDisabled();
  });

  // Deliberately long: it performs the full 25 selections rather than asserting
  // the rule in the abstract, and under coverage instrumentation that exceeds
  // the 5s default. The budget is raised instead of the assertion weakened.
  it("stops accepting people at the per-request cap", { timeout: 30_000 }, async () => {
    const many = Array.from({ length: maxAddMembersPerRequest + 5 }, (_, i) =>
      candidate(`u-${i}`, `Pessoa ${String(i).padStart(2, "0")}`),
    );
    searchChannelMemberCandidates.mockResolvedValue(many);
    const user = userEvent.setup({ advanceTimers: vi.advanceTimersByTime });
    renderDialog();

    await search(user);
    // A selected person leaves the result list, so the first remaining button is
    // always someone new. Clicking that, rather than searching by name on each
    // iteration, keeps this one cheap query per click — 25 regex scans over the
    // rendered list is slow enough to time out under a loaded parallel run.
    await screen.findByRole("list", { name: "Pessoas encontradas" });
    for (let i = 0; i < maxAddMembersPerRequest; i++) {
      const list = screen.getByRole("list", { name: "Pessoas encontradas" });
      await user.click(within(list).getAllByRole("button")[0]);
    }

    expect(
      screen.getByText(`Limite de ${maxAddMembersPerRequest} pessoas por vez atingido.`),
    ).toBeInTheDocument();
    const chips = screen.getByRole("list", { name: "Pessoas selecionadas" });
    expect(within(chips).getAllByRole("listitem")).toHaveLength(maxAddMembersPerRequest);
  });
});

describe("AddMembersDialog — envio", () => {
  it("sends only the selected user IDs to the channel endpoint", async () => {
    const user = userEvent.setup({ advanceTimers: vi.advanceTimersByTime });
    const { onAdded } = renderDialog();

    await search(user);
    await user.click(await screen.findByRole("button", { name: /Ana Lima/ }));
    await user.click(screen.getByRole("button", { name: "Adicionar" }));

    await waitFor(() => expect(onAdded).toHaveBeenCalled());
    expect(addChannelMembers).toHaveBeenCalledWith("ch-1", ["u-1"], expect.any(AbortSignal));
    expect(addGroupParticipants).not.toHaveBeenCalled();
    expect(onAdded).toHaveBeenCalledWith({ added: 1, alreadyMembers: 0, memberCount: 5 });
  });

  it("routes a group target to the DM endpoint", async () => {
    const user = userEvent.setup({ advanceTimers: vi.advanceTimersByTime });
    const { onAdded } = renderDialog({ target: groupTarget });

    await search(user);
    await user.click(await screen.findByRole("button", { name: /Ana Lima/ }));
    await user.click(screen.getByRole("button", { name: "Adicionar" }));

    await waitFor(() => expect(onAdded).toHaveBeenCalled());
    expect(addGroupParticipants).toHaveBeenCalledWith("dm-1", ["u-1"], expect.any(AbortSignal));
    expect(addChannelMembers).not.toHaveBeenCalled();
    // A group picker must query the group route, never the channel one.
    expect(searchGroupParticipantCandidates).toHaveBeenCalledWith(
      "dm-1",
      "an",
      expect.any(AbortSignal),
    );
    expect(searchChannelMemberCandidates).not.toHaveBeenCalled();
  });

  // The ref guard, not the disabled attribute, is what makes this hold: two
  // clicks in the same tick both run before React re-renders.
  it("submits once even when confirm is clicked twice", async () => {
    let resolve: (value: unknown) => void = () => {};
    addChannelMembers.mockReturnValue(new Promise((r) => (resolve = r)));
    const user = userEvent.setup({ advanceTimers: vi.advanceTimersByTime });
    renderDialog();

    await search(user);
    await user.click(await screen.findByRole("button", { name: /Ana Lima/ }));
    const confirm = screen.getByRole("button", { name: "Adicionar" });
    await user.click(confirm);
    await user.click(screen.getByRole("button", { name: "Adicionando…" }));

    expect(addChannelMembers).toHaveBeenCalledTimes(1);
    resolve({ added: 1, alreadyMembers: 0, memberCount: 5 });
  });

  it("disables the controls while the write is in flight", async () => {
    let resolve: (value: unknown) => void = () => {};
    addChannelMembers.mockReturnValue(new Promise((r) => (resolve = r)));
    const user = userEvent.setup({ advanceTimers: vi.advanceTimersByTime });
    const { onClose } = renderDialog();

    await search(user);
    await user.click(await screen.findByRole("button", { name: /Ana Lima/ }));
    await user.click(screen.getByRole("button", { name: "Adicionar" }));

    expect(screen.getByRole("button", { name: "Adicionando…" })).toBeDisabled();
    expect(screen.getByRole("button", { name: "Cancelar" })).toBeDisabled();
    // The dialog must not close mid-write: the user would never learn the outcome.
    await user.keyboard("{Escape}");
    expect(onClose).not.toHaveBeenCalled();

    resolve({ added: 1, alreadyMembers: 0, memberCount: 5 });
  });

  it("keeps the selection after a recoverable failure", async () => {
    addChannelMembers.mockRejectedValue(new ApiRequestError(429, "rate_limited", "rate limited"));
    const user = userEvent.setup({ advanceTimers: vi.advanceTimersByTime });
    const { onAdded, onClose } = renderDialog();

    await search(user);
    await user.click(await screen.findByRole("button", { name: /Ana Lima/ }));
    await user.click(screen.getByRole("button", { name: "Adicionar" }));

    const alert = await screen.findByRole("alert");
    expect(alert).toHaveTextContent("Muitas solicitações em sequência.");
    expect(onAdded).not.toHaveBeenCalled();
    expect(onClose).not.toHaveBeenCalled();
    // Re-picking the people to retry would be the real cost of a 429.
    const chips = screen.getByRole("list", { name: "Pessoas selecionadas" });
    expect(within(chips).getByText("Ana Lima")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Adicionar" })).toBeEnabled();
  });

  it.each([
    [400, "Revise as pessoas selecionadas e tente novamente."],
    [403, /não tem permissão/],
    [404, "Esta conversa não está mais disponível."],
    [413, /Seleção grande demais/],
    [429, /Muitas solicitações/],
    [500, "Não foi possível adicionar as pessoas. Tente novamente."],
    [0, /Sem conexão/],
  ])("maps HTTP %s to its own message", async (status, expected) => {
    addChannelMembers.mockRejectedValue(new ApiRequestError(status as number, "failed", "failed"));
    const user = userEvent.setup({ advanceTimers: vi.advanceTimersByTime });
    renderDialog();

    await search(user);
    await user.click(await screen.findByRole("button", { name: /Ana Lima/ }));
    await user.click(screen.getByRole("button", { name: "Adicionar" }));

    expect(await screen.findByRole("alert")).toHaveTextContent(expected);
  });
});

describe("AddMembersDialog — acessibilidade", () => {
  it("is a labelled modal dialog focused on the search field", () => {
    renderDialog();

    const dialog = screen.getByRole("dialog");
    expect(dialog).toHaveAttribute("aria-modal", "true");
    expect(within(dialog).getByRole("heading", { name: "Adicionar membros" })).toBeInTheDocument();
    expect(screen.getByLabelText("Pesquisar pessoa")).toHaveFocus();
  });

  it("closes on Escape without mutating anything", async () => {
    const user = userEvent.setup({ advanceTimers: vi.advanceTimersByTime });
    const { onClose } = renderDialog();

    await user.keyboard("{Escape}");

    expect(onClose).toHaveBeenCalledTimes(1);
    expect(addChannelMembers).not.toHaveBeenCalled();
  });

  it("closes from the explicit cancel control without mutating", async () => {
    const user = userEvent.setup({ advanceTimers: vi.advanceTimersByTime });
    const { onClose } = renderDialog();

    await user.click(screen.getByRole("button", { name: "Cancelar" }));

    expect(onClose).toHaveBeenCalledTimes(1);
    expect(addChannelMembers).not.toHaveBeenCalled();
  });

  it("can be driven entirely by keyboard", async () => {
    const user = userEvent.setup({ advanceTimers: vi.advanceTimersByTime });
    renderDialog();

    await search(user);
    const result = await screen.findByRole("button", { name: /Ana Lima/ });
    result.focus();
    await user.keyboard("{Enter}");

    // Selected by keyboard, and removable by keyboard from the chip.
    const remove = screen.getByRole("button", { name: "Remover Ana Lima" });
    remove.focus();
    await user.keyboard("{Enter}");

    expect(screen.queryByRole("list", { name: "Pessoas selecionadas" })).not.toBeInTheDocument();
  });

  it("keeps Tab inside the dialog", async () => {
    const user = userEvent.setup({ advanceTimers: vi.advanceTimersByTime });
    renderDialog();

    for (let i = 0; i < 12; i++) {
      await user.tab();
      expect(screen.getByRole("dialog")).toContainElement(document.activeElement as HTMLElement);
    }
  });

  it("associates the submit error with the confirm control", async () => {
    addChannelMembers.mockRejectedValue(new ApiRequestError(403, "forbidden", "nope"));
    const user = userEvent.setup({ advanceTimers: vi.advanceTimersByTime });
    renderDialog();

    await search(user);
    await user.click(await screen.findByRole("button", { name: /Ana Lima/ }));
    await user.click(screen.getByRole("button", { name: "Adicionar" }));

    const confirm = await screen.findByRole("button", { name: "Adicionar" });
    const describedBy = confirm.getAttribute("aria-describedby");
    expect(describedBy).toBeTruthy();
    expect(document.getElementById(describedBy as string)).toHaveTextContent(/não tem permissão/);
  });
});

// Channels and groups have no fixed capacity, so the dialog must not claim one.
describe("AddMembersDialog — sem limite de capacidade", () => {
  it("shows no 'conversation is full' message for any failure", async () => {
    const user = userEvent.setup({ advanceTimers: vi.advanceTimersByTime });
    for (const status of [400, 403, 404, 409, 429, 500]) {
      addChannelMembers.mockRejectedValue(new ApiRequestError(status, "err", "failed"));
      const { unmount } = render(
        <AddMembersDialog
          target={channelTarget}
          excludedUserIds={[]}
          onClose={vi.fn()}
          onAdded={vi.fn()}
        />,
      );

      await search(user);
      await user.click(await screen.findByRole("button", { name: /Ana Lima/ }));
      await user.click(screen.getByRole("button", { name: "Adicionar" }));

      const alert = await screen.findByRole("alert");
      expect(alert.textContent ?? "").not.toMatch(/limite de participantes|chei[ao]/i);
      unmount();
    }
  });
});
