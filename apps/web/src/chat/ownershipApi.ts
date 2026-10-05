import { authenticatedFetch } from "../lib/authClient";
import { safeAvatarUrl } from "./avatarUrl";

export type ConversationRole = "owner" | "admin" | "member";
export interface OwnershipMember {
  userId: string;
  displayName: string;
  avatarUrl?: string;
  role: ConversationRole;
  actions: { remove: boolean; assignRole: boolean; transfer: boolean };
}
export interface OwnershipDetails {
  enabled: boolean;
  members: OwnershipMember[];
  capabilities: {
    addMembers: boolean;
    manageRoles: boolean;
    editMetadata: boolean;
    leave: boolean;
  };
  leavePreview: { lastOwner: boolean; successorUserId?: string; blocked: boolean };
}
function record(raw: unknown): Record<string, unknown> {
  return typeof raw === "object" && raw !== null ? (raw as Record<string, unknown>) : {};
}
function role(raw: unknown): ConversationRole | undefined {
  return raw === "owner" || raw === "admin" || raw === "member" ? raw : undefined;
}
function parseMember(raw: unknown): OwnershipMember | undefined {
  const member = record(raw);
  const memberRole = role(member.role);
  if (typeof member.user_id !== "string" || !memberRole) return undefined;
  const actions = record(member.actions);
  return {
    userId: member.user_id,
    displayName: typeof member.display_name === "string" ? member.display_name : "Participante",
    avatarUrl: safeAvatarUrl(member.avatar_url),
    role: memberRole,
    actions: {
      remove: actions.remove === true,
      assignRole: actions.assign_role === true,
      transfer: actions.transfer === true,
    },
  };
}
export function parseOwnership(raw: unknown): OwnershipDetails | undefined {
  const data = record(raw);
  if (data.enabled !== true) return undefined;
  const capabilities = record(data.capabilities);
  const preview = record(data.leave_preview);
  return {
    enabled: true,
    members: Array.isArray(data.members)
      ? data.members
          .map(parseMember)
          .filter((member): member is OwnershipMember => member !== undefined)
      : [],
    capabilities: {
      addMembers: capabilities.add_members === true,
      manageRoles: capabilities.manage_roles === true,
      editMetadata: capabilities.edit_metadata === true,
      leave: capabilities.leave === true,
    },
    leavePreview: {
      lastOwner: preview.last_owner === true,
      successorUserId:
        typeof preview.successor_user_id === "string" ? preview.successor_user_id : undefined,
      blocked: preview.blocked !== false,
    },
  };
}
function base(kind: "channel" | "group", id: string): string {
  const collection = kind === "channel" ? "channels" : "dm";
  return `/api/chat/${collection}/${encodeURIComponent(id)}`;
}
export async function assignConversationRole(
  kind: "channel" | "group",
  id: string,
  userId: string,
  newRole: ConversationRole,
): Promise<void> {
  await authenticatedFetch(`${base(kind, id)}/members/${encodeURIComponent(userId)}/role`, {
    method: "PATCH",
    body: JSON.stringify({ role: newRole }),
  });
}
export async function transferConversationOwnership(
  kind: "channel" | "group",
  id: string,
  target: string,
  actorRole: "admin" | "member",
  leave: boolean,
  key: string,
): Promise<void> {
  await authenticatedFetch(
    `${base(kind, id)}/ownership/${leave ? "transfer-and-leave" : "transfer"}`,
    {
      method: "POST",
      headers: { "Idempotency-Key": key },
      body: JSON.stringify({ new_owner_user_id: target, actor_new_role: actorRole }),
    },
  );
}
export async function leaveOwnedConversation(kind: "channel" | "group", id: string): Promise<void> {
  await authenticatedFetch(`${base(kind, id)}/membership`, { method: "DELETE" });
}
