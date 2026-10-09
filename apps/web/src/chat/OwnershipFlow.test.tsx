import { act, fireEvent, render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import OwnershipDialogs from "./OwnershipDialogs";
import { ApiRequestError } from "../lib/api";
import {
  assignConversationRole,
  leaveOwnedConversation,
  transferConversationOwnership,
  type OwnershipDetails,
} from "./ownershipApi";

vi.mock("./ownershipApi", async (original) => ({
  ...(await original<typeof import("./ownershipApi")>()),
  assignConversationRole: vi.fn(),
  leaveOwnedConversation: vi.fn(),
  transferConversationOwnership: vi.fn(),
}));
const facts = (): OwnershipDetails => ({
  enabled: true,
  capabilities: { addMembers: true, manageRoles: true, editMetadata: true, leave: true },
  leavePreview: { lastOwner: true, blocked: false, successorUserId: "b" },
  members: [
    {
      userId: "a",
      displayName: "Alice",
      role: "owner",
      actions: { remove: false, assignRole: true, transfer: false },
    },
    {
      userId: "b",
      displayName: "Caio",
      role: "admin",
      actions: { remove: true, assignRole: true, transfer: true },
    },
    {
      userId: "c",
      displayName: "Daiane",
      role: "member",
      actions: { remove: true, assignRole: true, transfer: true },
    },
  ],
});
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
});
function setup(initial = facts()) {
  const reload = vi.fn();
  const committed = vi.fn();
  function tree(ownership: OwnershipDetails, status: "ready" | "error" | "loading" = "ready") {
    return (
      <OwnershipDialogs
        status={status}
        context={
          status === "ready"
            ? {
                kind: "group",
                id: "group",
                ownership,
                currentUserId: "a",
                workspaceId: "workspace",
                reload,
                onCommitted: committed,
              }
            : undefined
        }
      >
        {(open) => (
          <>
            <button
              onClick={(event) =>
                open({ type: "transfer", member: ownership.members[1] }, event.currentTarget)
              }
            >
              Transferir
            </button>
            <button
              onClick={(event) =>
                open(
                  { type: "role", member: ownership.members[1], role: "owner" },
                  event.currentTarget,
                )
              }
            >
              Promover
            </button>
            <button onClick={(event) => open({ type: "leave" }, event.currentTarget)}>Sair</button>
          </>
        )}
      </OwnershipDialogs>
    );
  }
  const view = render(tree(initial));
  return {
    ...view,
    reload,
    committed,
    update: (data: OwnershipDetails, status?: "ready" | "error" | "loading") =>
      view.rerender(tree(data, status)),
  };
}
const transferButton = () => screen.getByRole("button", { name: "Transferir propriedade" });
const dialog = () => within(screen.getByRole("dialog"));

describe("transfer/state machine", () => {
  it.each(["admin", "member"] as const)(
    "submits %s once and keeps promotion distinct",
    async (role) => {
      const view = setup();
      const user = userEvent.setup();
      await user.click(screen.getByRole("button", { name: "Promover" }));
      await user.click(screen.getByRole("button", { name: "Confirmar" }));
      expect(assignConversationRole).toHaveBeenCalledWith("group", "group", "b", "owner");
      expect(transferConversationOwnership).not.toHaveBeenCalled();
      await user.click(screen.getByRole("button", { name: "Transferir" }));
      expect(screen.getByRole("button", { name: "Cancelar" })).toHaveFocus();
      expect(dialog().getByRole("radio", { name: "Caio, Administrador" })).toBeChecked();
      expect(dialog().queryByRole("radio", { name: /Alice/ })).not.toBeInTheDocument();
      expect(dialog().queryByRole("checkbox")).not.toBeInTheDocument();
      await user.click(
        dialog().getByRole("radio", {
          name: role === "admin" ? "Administrador" : "Membro",
        }),
      );
      let finish!: () => void;
      vi.mocked(transferConversationOwnership).mockImplementationOnce(
        () =>
          new Promise((resolve) => {
            finish = resolve;
          }),
      );
      const button = transferButton();
      fireEvent.click(button);
      fireEvent.click(button);
      fireEvent.submit(button.closest("form")!);
      expect(transferConversationOwnership).toHaveBeenCalledExactlyOnceWith(
        "group",
        "group",
        "b",
        role,
        false,
        expect.any(String),
      );
      expect(dialog().getByRole("button", { name: "Confirmando…" })).toBeDisabled();
      expect(button.closest("form")).toHaveAttribute("aria-busy", "true");
      await act(async () => finish());
      expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
      expect(view.reload).toHaveBeenCalledTimes(2);
    },
  );

  it("pins uncertain retries and changes key for every changed intention", async () => {
    vi.mocked(transferConversationOwnership)
      .mockRejectedValueOnce(new ApiRequestError(0, "network", "secret"))
      .mockRejectedValueOnce(new ApiRequestError(403, "denied", "secret"))
      .mockRejectedValueOnce(new ApiRequestError(403, "denied", "secret"));
    setup();
    const user = userEvent.setup();
    await user.click(screen.getByRole("button", { name: "Transferir" }));
    await user.click(transferButton());
    expect(dialog().getByRole("alert")).toHaveTextContent("Não foi possível confirmar");
    expect(dialog().getByRole("radio", { name: "Daiane, Membro" })).toBeDisabled();
    await user.click(transferButton());
    let calls = vi.mocked(transferConversationOwnership).mock.calls;
    expect(calls[1]).toEqual(calls[0]);
    await user.click(dialog().getByRole("radio", { name: "Administrador" }));
    await user.click(transferButton());
    calls = vi.mocked(transferConversationOwnership).mock.calls;
    expect(calls[2][5]).not.toBe(calls[1][5]);
    await user.click(dialog().getByRole("radio", { name: "Daiane, Membro" }));
    await user.click(dialog().getByRole("radio", { name: "Caio, Administrador" }));
    await user.type(dialog().getByLabelText("Buscar participante"), "caio");
    await user.click(transferButton());
    calls = vi.mocked(transferConversationOwnership).mock.calls;
    expect(calls[3][5]).not.toBe(calls[2][5]);
  });

  it.each([403, 404])("maps %s without private detail or assumed success", async (status) => {
    vi.mocked(transferConversationOwnership).mockRejectedValueOnce(
      new ApiRequestError(status, "private", "private workspace detail"),
    );
    const view = setup();
    await userEvent.click(screen.getByRole("button", { name: "Transferir" }));
    await userEvent.click(transferButton());
    expect(dialog().getByRole("alert")).not.toHaveTextContent("private workspace detail");
    expect(view.committed).not.toHaveBeenCalled();
    expect(view.reload).not.toHaveBeenCalled();
  });

  it("keeps conflict useful through failed refresh and permanently clears stale target", async () => {
    vi.mocked(transferConversationOwnership).mockRejectedValueOnce(
      new ApiRequestError(409, "conflict", "changed"),
    );
    const original = facts();
    const view = setup(original);
    await userEvent.click(screen.getByRole("button", { name: "Transferir" }));
    await userEvent.click(transferButton());
    expect(view.reload).toHaveBeenCalledOnce();
    expect(transferButton()).toBeDisabled();
    view.update(original, "error");
    expect(screen.getByRole("dialog")).toBeVisible();
    expect(dialog().getByText(/Não foi possível atualizar/)).toBeVisible();
    await userEvent.click(dialog().getByRole("button", { name: "Atualizar detalhes" }));
    expect(view.reload).toHaveBeenCalledTimes(2);
    const updated = facts();
    updated.members[1].actions.transfer = false;
    view.update(updated);
    expect(transferButton()).toBeDisabled();
    view.update(facts());
    expect(dialog().getByRole("radio", { name: "Caio, Administrador" })).not.toBeChecked();
    expect(transferButton()).toBeDisabled();
    await userEvent.click(dialog().getByRole("radio", { name: "Daiane, Membro" }));
    await userEvent.click(transferButton());
    expect(transferConversationOwnership).toHaveBeenLastCalledWith(
      "group",
      "group",
      "c",
      "member",
      false,
      expect.any(String),
    );
  });
});

describe("leave/last-owner/manual-successor", () => {
  it("shows server preview, then submits the explicit override atomically and recovers stale selection", async () => {
    vi.mocked(transferConversationOwnership).mockRejectedValueOnce(
      new ApiRequestError(409, "conflict", "changed"),
    );
    const view = setup();
    await userEvent.click(screen.getByRole("button", { name: "Sair" }));
    expect(screen.getByRole("dialog")).toHaveTextContent(
      "Se sair, Caio será promovido automaticamente",
    );
    await userEvent.click(dialog().getByRole("button", { name: "Escolher outro proprietário" }));
    await userEvent.type(dialog().getByLabelText("Buscar participante"), " DAI ");
    expect(dialog().queryByRole("radio", { name: /Caio/ })).not.toBeInTheDocument();
    await userEvent.click(dialog().getByRole("radio", { name: "Daiane, Membro" }));
    await userEvent.click(dialog().getByRole("button", { name: "Sair e transferir" }));
    expect(transferConversationOwnership).toHaveBeenCalledExactlyOnceWith(
      "group",
      "group",
      "c",
      "member",
      true,
      expect.any(String),
    );
    expect(leaveOwnedConversation).not.toHaveBeenCalled();
    expect(view.committed).not.toHaveBeenCalled();
    expect(view.reload).toHaveBeenCalledOnce();
    const updated = facts();
    updated.members[2].actions.transfer = false;
    view.update(updated);
    expect(dialog().getByRole("button", { name: "Sair e transferir" })).toBeDisabled();
    expect(screen.getByRole("dialog")).toBeVisible();
    await userEvent.click(dialog().getByRole("button", { name: "Usar sucessor automático" }));
    await userEvent.click(dialog().getByRole("button", { name: "Sair e transferir" }));
    expect(leaveOwnedConversation).toHaveBeenCalledExactlyOnceWith("group", "group");
  });

  it.each(["multiple", "last", "automatic"])("uses normal DELETE for %s", async (scenario) => {
    const ownership = facts();
    if (scenario === "multiple") ownership.leavePreview = { lastOwner: false, blocked: false };
    if (scenario === "last") {
      ownership.members = [ownership.members[0]];
      ownership.leavePreview = { lastOwner: true, blocked: false };
    }
    setup(ownership);
    await userEvent.click(screen.getByRole("button", { name: "Sair" }));
    if (scenario === "last")
      expect(screen.getByRole("dialog")).toHaveTextContent("sem participantes ativos");
    if (scenario !== "automatic")
      expect(
        dialog().queryByRole("button", { name: "Escolher outro proprietário" }),
      ).not.toBeInTheDocument();
    await userEvent.click(
      dialog().getByRole("button", {
        name: scenario === "automatic" ? "Sair e transferir" : "Sair da conversa",
      }),
    );
    expect(leaveOwnedConversation).toHaveBeenCalledExactlyOnceWith("group", "group");
    expect(transferConversationOwnership).not.toHaveBeenCalled();
  });

  it("cancels without mutation and resets on conversation loading", async () => {
    const data = facts();
    const view = setup(data);
    const trigger = screen.getByRole("button", { name: "Sair" });
    await userEvent.click(trigger);
    fireEvent(screen.getByRole("dialog"), new Event("cancel", { cancelable: true }));
    expect(trigger).toHaveFocus();
    expect(leaveOwnedConversation).not.toHaveBeenCalled();
    await userEvent.click(trigger);
    view.update(data, "loading");
    view.update(data);
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
  });
});
