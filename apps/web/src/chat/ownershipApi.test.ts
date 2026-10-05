import { describe, expect, it } from "vitest";
import { parseOwnership } from "./ownershipApi";

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
