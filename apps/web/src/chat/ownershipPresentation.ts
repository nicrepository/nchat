import type { ConversationRole, OwnershipMember } from "./ownershipApi";
export const roleLabels = { owner: "Proprietário", admin: "Administrador", member: "Membro" };
export type OwnershipAction =
  | { type: "transfer" }
  | { type: "leave" }
  | { type: "role"; member: OwnershipMember; role: ConversationRole };
