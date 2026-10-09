import { beforeEach, describe, expect, it, vi } from "vitest";
import { authenticatedFetch } from "../lib/authClient";
import {
  assignConversationRole,
  leaveOwnedConversation,
  transferConversationOwnership,
  parseOwnership,
} from "./ownershipApi";

describe("ownership capabilities", () => {
  it("fails closed for missing and malformed capabilities", () => {
    expect(parseOwnership(undefined)).toBeUndefined();
    expect(parseOwnership({ enabled: "true" })).toBeUndefined();
    const parsed = parseOwnership({
      enabled: true,
      capabilities: { add_members: 1, manage_roles: "true", edit_metadata: null },
      members: [
        { user_id: "a", role: "owner", actions: { remove: "true", assign_role: 1, transfer: {} } },
      ],
    });
    expect(parsed?.capabilities).toEqual({
      addMembers: false,
      manageRoles: false,
      editMetadata: false,
      leave: false,
    });
    expect(parsed?.members[0].actions).toEqual({
      remove: false,
      assignRole: false,
      transfer: false,
    });
    expect(parsed?.leavePreview.blocked).toBe(true);
  });
  it("preserves multiple owners and valid booleans without inferring role authority", () => {
    const parsed = parseOwnership({
      enabled: true,
      capabilities: { manage_roles: true },
      members: [
        { user_id: "a", role: "owner", actions: { assign_role: true } },
        { user_id: "b", role: "owner" },
        { user_id: "c", role: "superowner" },
      ],
      leave_preview: { last_owner: false, blocked: false },
    });
    expect(parsed?.members).toHaveLength(2);
    expect(parsed?.members[0].actions.assignRole).toBe(true);
    expect(parsed?.members[1].actions.assignRole).toBe(false);
    expect(parsed?.leavePreview.blocked).toBe(false);
  });
});

vi.mock("../lib/authClient", () => ({ authenticatedFetch: vi.fn() }));
beforeEach(() => vi.mocked(authenticatedFetch).mockReset());
describe("ownership request contracts", () => {
  it.each(["channel", "group"] as const)(
    "uses the %s APIs and only supported inputs",
    async (kind) => {
      const root = `/api/chat/${kind === "channel" ? "channels" : "dm"}/conversation`;
      await assignConversationRole(kind, "conversation", "target", "owner");
      expect(authenticatedFetch).toHaveBeenLastCalledWith(`${root}/members/target/role`, {
        method: "PATCH",
        body: JSON.stringify({ role: "owner" }),
      });
      for (const role of ["admin", "member"] as const) {
        await transferConversationOwnership(
          kind,
          "conversation",
          "target",
          role,
          false,
          "intent-key",
        );
        expect(authenticatedFetch).toHaveBeenLastCalledWith(`${root}/ownership/transfer`, {
          method: "POST",
          headers: { "Idempotency-Key": "intent-key" },
          body: JSON.stringify({ new_owner_user_id: "target", actor_new_role: role }),
        });
      }
      await transferConversationOwnership(
        kind,
        "conversation",
        "manual",
        "member",
        true,
        "leave-key",
      );
      expect(authenticatedFetch).toHaveBeenLastCalledWith(`${root}/ownership/transfer-and-leave`, {
        method: "POST",
        headers: { "Idempotency-Key": "leave-key" },
        body: JSON.stringify({ new_owner_user_id: "manual", actor_new_role: "member" }),
      });
      await leaveOwnedConversation(kind, "conversation");
      expect(authenticatedFetch).toHaveBeenLastCalledWith(`${root}/membership`, {
        method: "DELETE",
      });
    },
  );
});
