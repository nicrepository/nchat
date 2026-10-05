import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import { ApiRequestError } from "../lib/api";
import OwnershipRoster from "./OwnershipRoster";
import {
  assignConversationRole,
  transferConversationOwnership,
  type OwnershipDetails,
} from "./ownershipApi";

vi.mock("./ownershipApi", async (original) => ({
  ...(await original<typeof import("./ownershipApi")>()),
  assignConversationRole: vi.fn(),
  leaveOwnedConversation: vi.fn(),
  transferConversationOwnership: vi.fn(),
}));
vi.mock("./UserAvatar", () => ({ UserAvatar: () => <span /> }));

const facts = (): OwnershipDetails => ({
  enabled: true,
  capabilities: { addMembers: true, manageRoles: true, editMetadata: true, leave: true },
  leavePreview: { lastOwner: true, blocked: false, successorUserId: "b" },
  members: [
    {
      userId: "a",
      displayName: "Alice",
      role: "owner",
      actions: { remove: false, assignRole: false, transfer: true },
    },
    {
      userId: "b",
      displayName: "Bruno",
      role: "admin",
      actions: { remove: true, assignRole: true, transfer: true },
    },
  ],
});
const renderRoster = (ownership = facts()) => {
  const reload = vi.fn();
  const view = render(
    <OwnershipRoster
      kind="group"
      id="group"
      workspaceId="workspace"
      currentUserId="a"
      ownership={ownership}
      reload={reload}
      onAdd={vi.fn()}
      onRemove={vi.fn()}
    />,
  );
  return { reload, ...view };
};

beforeAll(() => {
  Object.defineProperty(HTMLDialogElement.prototype, "showModal", {
    configurable: true,
    value() {
      this.setAttribute("open", "");
    },
  });
  Object.defineProperty(HTMLDialogElement.prototype, "close", {
    configurable: true,
    value() {
      this.removeAttribute("open");
    },
  });
});
beforeEach(() => {
  vi.resetAllMocks();
  vi.mocked(assignConversationRole).mockResolvedValue(undefined);
  vi.mocked(transferConversationOwnership).mockResolvedValue(undefined);
});

async function openTransfer() {
  await userEvent.click(screen.getByLabelText("Ações de Alice"));
  await userEvent.click(screen.getByRole("button", { name: "Transferir minha propriedade" }));
}

describe("ownership roster", () => {
  it("shows roles independently of the current-user marker and server authority", () => {
    const ownership = facts();
    ownership.capabilities = {
      addMembers: false,
      manageRoles: false,
      editMetadata: false,
      leave: false,
    };
    ownership.members.forEach((member) => {
      member.actions = { remove: false, assignRole: false, transfer: false };
    });
    renderRoster(ownership);
    expect(screen.getByText("Proprietário")).toBeVisible();
    expect(screen.getByText("Administrador")).toBeVisible();
    expect(screen.getByText("[Você]")).toBeVisible();
    expect(screen.queryByLabelText(/Ações/)).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Adicionar membros" })).not.toBeInTheDocument();
  });

  it("searches and filters the complete list without changing roles", async () => {
    const ownership = facts();
    for (let i = 0; i < 6; i++)
      ownership.members.push({
        userId: `member-${i}`,
        displayName: `Pessoa ${i}`,
        role: "member",
        actions: { remove: false, assignRole: false, transfer: false },
      });
    renderRoster(ownership);
    expect(screen.queryByText("Pessoa 5")).not.toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Ver todos" }));
    await userEvent.type(screen.getByLabelText("Buscar participante"), "pessoa 5");
    expect(screen.getByText("Pessoa 5")).toBeVisible();
    await userEvent.selectOptions(screen.getByLabelText("Papel"), "owner");
    expect(screen.queryByText("Pessoa 5")).not.toBeInTheDocument();
  });

  it("requires a transfer target and sends the chosen role and atomic leave flag", async () => {
    const { reload } = renderRoster();
    await openTransfer();
    expect(screen.getByRole("dialog")).toHaveAccessibleName("Transferir minha propriedade");
    expect(screen.getByRole("button", { name: "Cancelar" })).toHaveFocus();
    expect(screen.getByRole("button", { name: "Confirmar" })).toBeDisabled();
    await userEvent.selectOptions(screen.getByLabelText("Novo proprietário"), "b");
    await userEvent.selectOptions(screen.getByLabelText("Meu papel"), "admin");
    await userEvent.click(screen.getByLabelText("Sair após transferir"));
    await userEvent.click(screen.getByRole("button", { name: "Confirmar" }));
    expect(transferConversationOwnership).toHaveBeenCalledWith(
      "group",
      "group",
      "b",
      "admin",
      true,
      expect.any(String),
    );
    expect(reload).toHaveBeenCalledOnce();
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
  });

  it("keeps a failed transfer recoverable and preserves the idempotency key", async () => {
    vi.mocked(transferConversationOwnership).mockRejectedValueOnce(
      new ApiRequestError(409, "ownership_conflict", "changed"),
    );
    renderRoster();
    await openTransfer();
    await userEvent.selectOptions(screen.getByLabelText("Novo proprietário"), "b");
    await userEvent.click(screen.getByRole("button", { name: "Confirmar" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("A propriedade mudou");
    await userEvent.click(screen.getByRole("button", { name: "Confirmar" }));
    const calls = vi.mocked(transferConversationOwnership).mock.calls;
    expect(calls).toHaveLength(2);
    expect(calls[1][5]).toBe(calls[0][5]);
  });

  it("blocks a last-owner departure without an automatic successor", async () => {
    const ownership = facts();
    ownership.leavePreview = { lastOwner: true, blocked: true, successorUserId: undefined };
    renderRoster(ownership);
    await userEvent.click(screen.getByRole("button", { name: "Sair da conversa" }));
    expect(screen.getByRole("dialog")).toHaveTextContent("Não há sucessor automático elegível");
    expect(screen.getByRole("button", { name: "Confirmar" })).toBeDisabled();
  });

  it("describes the predicted successor and restores focus on cancellation", async () => {
    renderRoster();
    const trigger = screen.getByRole("button", { name: "Sair da conversa" });
    await userEvent.click(trigger);
    expect(screen.getByRole("dialog")).toHaveTextContent("Bruno assumirá");
    fireEvent(screen.getByRole("dialog"), new Event("cancel", { cancelable: true }));
    expect(trigger).toHaveFocus();
  });

  it("does not apply a completed mutation to a panel that was closed", async () => {
    let finish!: () => void;
    vi.mocked(assignConversationRole).mockImplementation(
      () =>
        new Promise<void>((resolve) => {
          finish = resolve;
        }),
    );
    const { reload, unmount } = renderRoster();
    await userEvent.click(screen.getByLabelText("Ações de Bruno"));
    await userEvent.click(screen.getByRole("button", { name: "Tornar proprietário" }));
    await userEvent.click(screen.getByRole("button", { name: "Confirmar" }));
    await waitFor(() => expect(assignConversationRole).toHaveBeenCalledOnce());
    unmount();
    await act(async () => finish());
    expect(reload).not.toHaveBeenCalled();
  });
});
