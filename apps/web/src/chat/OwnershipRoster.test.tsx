import { act, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import { ApiRequestError } from "../lib/api";
import OwnershipDialogs from "./OwnershipDialogs";
import OwnershipRoster from "./OwnershipRoster";
import {
  assignConversationRole,
  parseOwnership,
  transferConversationOwnership,
  type OwnershipDetails,
  type OwnershipMember,
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
      displayName: "Bruno",
      role: "admin",
      actions: { remove: true, assignRole: true, transfer: true },
    },
  ],
});
const renderRoster = (ownership = facts()) => {
  const reload = vi.fn();
  const onRemove = vi.fn();
  const view = render(
    <OwnershipDialogs
      status="ready"
      context={{ kind: "group", id: "group", ownership, currentUserId: "a", reload }}
    >
      {(open) => (
        <OwnershipRoster
          workspaceId="workspace"
          currentUserId="a"
          presence={{ covered: true, entries: new Map() }}
          ownership={ownership}
          onAdd={vi.fn()}
          onRemove={onRemove}
          onAction={open}
        />
      )}
    </OwnershipDialogs>,
  );
  return { reload, onRemove, ...view };
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

describe("ownership roster", () => {
  it("shares badges, self, alphabetical ownership ordering and the central avatar", async () => {
    const ownership = facts();
    ownership.capabilities = {
      addMembers: false,
      manageRoles: false,
      editMetadata: false,
      leave: false,
    };
    const identities: Array<Pick<OwnershipMember, "userId" | "displayName" | "role">> = [
      { userId: "owner-z", displayName: "Owner Z", role: "owner" },
      { userId: "member-b", displayName: "Member B", role: "member" },
      { userId: "a", displayName: "Current", role: "member" },
      { userId: "admin", displayName: "Admin C", role: "admin" },
      { userId: "owner-a", displayName: "Owner Á", role: "owner" },
      { userId: "member-a", displayName: "Member A", role: "member" },
    ];
    ownership.members = identities.map((member) => ({
      ...member,
      actions: { remove: false, assignRole: false, transfer: false },
    }));
    const original = ownership.members.map((member) => member.userId);
    const { container, rerender } = renderRoster(ownership);
    const names = () =>
      screen
        .getAllByRole("listitem")
        .map((row) => row.querySelector(".ownership-roster__name")?.textContent?.trim());
    expect(names()).toEqual(["Current [Você]", "Owner Á", "Owner Z", "Admin C", "Member A"]);
    expect(screen.getAllByText("Proprietário")).toHaveLength(2);
    expect(screen.getByText("Administrador")).toBeVisible();
    expect(screen.queryByText("Membro", { exact: true })).not.toBeInTheDocument();
    expect(container.querySelector("img")?.getAttribute("src")).toMatch(/^data:image\/svg\+xml/);
    expect(container.querySelector(".presence-dot")).toBeInTheDocument();
    expect(screen.queryByLabelText(/Ações/)).not.toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Ver todos" }));
    expect(names()).toEqual([
      "Current [Você]",
      "Owner Á",
      "Owner Z",
      "Admin C",
      "Member A",
      "Member B",
    ]);
    expect(ownership.members.map((member) => member.userId)).toEqual(original);
    for (const id of ["owner-z", "admin"]) {
      rerender(
        <OwnershipRoster
          workspaceId="workspace"
          currentUserId={id}
          ownership={ownership}
          onAdd={vi.fn()}
          onAction={vi.fn()}
          onRemove={vi.fn()}
        />,
      );
      expect(within(screen.getAllByRole("listitem")[0]).getByText("[Você]")).toBeVisible();
      expect(
        within(screen.getAllByRole("listitem")[0]).getByText(
          id === "admin" ? "Administrador" : "Proprietário",
        ),
      ).toBeVisible();
    }
  });

  it("combines localized role filters and trimmed search, including small rosters", async () => {
    const ownership = facts();
    ownership.members.push({
      userId: "member",
      displayName: "Dai Member",
      role: "member",
      actions: { remove: false, assignRole: false, transfer: false },
    });
    ownership.members[1].displayName = "Dai Admin";
    renderRoster(ownership);
    await userEvent.click(screen.getByRole("button", { name: "Ver todos" }));
    const filter = screen.getByLabelText("Papel");
    for (const [value, expected] of [
      ["owner", ["Alice"]],
      ["admin", ["Dai Admin"]],
      ["member", ["Dai Member"]],
      ["", ["Alice", "Dai Admin", "Dai Member"]],
    ] as const) {
      await userEvent.selectOptions(filter, value);
      expect(
        screen
          .getAllByRole("listitem")
          .map((row) => row.querySelector(".ownership-roster__name")?.textContent?.trim()),
      ).toEqual(expected.map((name) => (name === "Alice" ? "Alice [Você]" : name)));
    }
    expect(
      within(filter)
        .getAllByRole("option")
        .map((option) => option.textContent),
    ).toEqual(["Todos", "Proprietários", "Administradores", "Membros"]);
    await userEvent.type(screen.getByLabelText("Buscar participante"), "  DAI  ");
    expect(screen.getAllByRole("listitem")).toHaveLength(2);
    await userEvent.selectOptions(filter, "admin");
    expect(screen.getAllByRole("listitem")).toHaveLength(1);
    expect(screen.getByText("Dai Admin")).toBeVisible();
    await userEvent.selectOptions(filter, "owner");
    expect(screen.getByText("Nenhum participante encontrado.")).toBeVisible();
  });

  it("offers only strict target capabilities, ordered role actions and no direct owner removal", async () => {
    const cases: Array<{
      role: string;
      actor?: string;
      actions: Record<string, unknown>;
      expected: string[];
    }> = [
      {
        role: "member",
        actions: { assign_role: true, remove: true, transfer: true },
        expected: [
          "Tornar administrador",
          "Tornar proprietário",
          "Transferir minha propriedade",
          "Remover",
        ],
      },
      {
        role: "admin",
        actions: { assign_role: true, remove: true, transfer: true },
        expected: [
          "Tornar proprietário",
          "Tornar membro",
          "Transferir minha propriedade",
          "Remover",
        ],
      },
      {
        role: "owner",
        actions: { assign_role: true, remove: false, transfer: true },
        expected: ["Tornar administrador", "Tornar membro", "Transferir minha propriedade"],
      },
      { role: "member", actor: "admin", actions: { remove: true }, expected: ["Remover"] },
      { role: "member", actor: "member", actions: {}, expected: [] },
      {
        role: "owner",
        actor: "member",
        actions: { remove: false, assign_role: false, transfer: false },
        expected: [],
      },
      ...[false, undefined, null, "true", 1, {}, []].map((value) => ({
        role: "member",
        actions: { assign_role: value, remove: value, transfer: value },
        expected: [],
      })),
    ];
    for (const scenario of cases) {
      const ownership = parseOwnership({
        enabled: true,
        members: [
          {
            user_id: "a",
            display_name: "Actor",
            role: scenario.actor ?? "owner",
            actions: {
              assign_role: (scenario.actor ?? "owner") === "owner",
              remove: false,
              transfer: false,
            },
          },
          { user_id: "b", display_name: "Target", role: scenario.role, actions: scenario.actions },
        ],
      })!;
      const view = renderRoster(ownership);
      const trigger = screen.queryByLabelText("Ações de Target");
      if (scenario.expected.length) {
        expect(trigger).toBeVisible();
        await userEvent.click(trigger!);
        expect(screen.getAllByRole("menuitem").map((item) => item.textContent)).toEqual(
          scenario.expected,
        );
        if (scenario.expected.includes("Remover")) {
          await userEvent.click(screen.getByRole("menuitem", { name: "Remover" }));
          expect(view.onRemove).toHaveBeenCalledWith(ownership.members[1], trigger);
        }
      } else expect(trigger).not.toBeInTheDocument();
      view.unmount();
    }
  });

  it("opens by keyboard, navigates actions and restores focus without closing the parent panel", async () => {
    renderRoster();
    const user = userEvent.setup();
    const trigger = screen.getByLabelText("Ações de Bruno");
    trigger.focus();
    await user.keyboard("{Enter}");
    expect(screen.getByRole("menu", { name: "Ações de Bruno" })).toBeVisible();
    expect(screen.getByRole("menuitem", { name: "Tornar proprietário" })).toHaveFocus();
    fireEvent.scroll(window);
    fireEvent.resize(window);
    expect(screen.getByRole("menuitem", { name: "Tornar proprietário" })).toHaveFocus();
    await user.keyboard("{ArrowDown}{End}");
    expect(screen.getByRole("menuitem", { name: "Remover" })).toHaveFocus();
    await user.keyboard("{Home}{ArrowUp}");
    expect(screen.getByRole("menuitem", { name: "Remover" })).toHaveFocus();
    await user.keyboard("{Escape}");
    expect(screen.queryByRole("menu")).not.toBeInTheDocument();
    expect(trigger).toHaveFocus();
    await user.keyboard(" ");
    await user.click(screen.getByRole("menuitem", { name: "Tornar proprietário" }));
    expect(screen.getByRole("dialog")).toHaveAccessibleName("Tornar proprietário");
    await user.click(screen.getByRole("button", { name: "Cancelar" }));
    expect(trigger).toHaveFocus();
  });

  it.each([403])(
    "keeps a denied role mutation (%s) recoverable without projecting success",
    async (status) => {
      vi.mocked(assignConversationRole).mockRejectedValueOnce(
        new ApiRequestError(status, "denied", "denied"),
      );
      const { reload } = renderRoster();
      await userEvent.click(screen.getByLabelText("Ações de Bruno"));
      await userEvent.click(screen.getByRole("menuitem", { name: "Tornar proprietário" }));
      await userEvent.click(screen.getByRole("button", { name: "Confirmar" }));
      expect(await screen.findByRole("alert")).toBeVisible();
      expect(reload).not.toHaveBeenCalled();
      expect(screen.getByRole("button", { name: "Confirmar" })).toBeEnabled();
      await userEvent.click(screen.getByRole("button", { name: "Confirmar" }));
      expect(assignConversationRole).toHaveBeenLastCalledWith("group", "group", "b", "owner");
      expect(reload).toHaveBeenCalledOnce();
    },
  );

  it("blocks a last-owner departure without an automatic successor", async () => {
    const ownership = facts();
    ownership.leavePreview = { lastOwner: true, blocked: true, successorUserId: undefined };
    renderRoster(ownership);
    await userEvent.click(screen.getByRole("button", { name: "Sair da conversa" }));
    expect(screen.getByRole("dialog")).toHaveTextContent("Não há sucessor automático elegível");
    expect(screen.getByRole("button", { name: "Sair e transferir" })).toBeDisabled();
  });

  it("describes the predicted successor and restores focus on cancellation", async () => {
    renderRoster();
    const trigger = screen.getByRole("button", { name: "Sair da conversa" });
    await userEvent.click(trigger);
    expect(screen.getByRole("dialog")).toHaveTextContent("Bruno será promovido automaticamente");
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
    await userEvent.click(screen.getByRole("menuitem", { name: "Tornar proprietário" }));
    await userEvent.dblClick(screen.getByRole("button", { name: "Confirmar" }));
    expect(screen.getByRole("button", { name: "Confirmando…" })).toBeDisabled();
    await waitFor(() => expect(assignConversationRole).toHaveBeenCalledOnce());
    unmount();
    await act(async () => finish());
    expect(reload).not.toHaveBeenCalled();
  });
});
