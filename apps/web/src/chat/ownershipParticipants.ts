import type { ConversationRole, OwnershipMember } from "./ownershipApi";

export const participantCopy = {
  self: "[Você]",
  remove: "Remover",
  transfer: "Transferir minha propriedade",
  closeMenu: "Fechar menu",
  actions: (name: string) => `Ações de ${name}`,
  roleActions: {
    admin: "Tornar administrador",
    owner: "Tornar proprietário",
    member: "Tornar membro",
  },
  filters: { "": "Todos", owner: "Proprietários", admin: "Administradores", member: "Membros" },
} as const;

export interface ParticipantMenuAction {
  id: ConversationRole | "remove" | "transfer";
  label: string;
  destructive?: boolean;
}

export interface OwnershipParticipantView {
  member: OwnershipMember;
  isCurrentUser: boolean;
  actions: ParticipantMenuAction[];
}

const roleOrder = { owner: 1, admin: 2, member: 3 };
const roleTargets: Record<ConversationRole, readonly ConversationRole[]> = {
  member: ["admin", "owner"],
  admin: ["owner", "member"],
  owner: ["admin", "member"],
};

function nameKey(name: string): string {
  return name
    .normalize("NFD")
    .replace(/\p{Diacritic}/gu, "")
    .toLowerCase();
}

function participantActions(member: OwnershipMember): ParticipantMenuAction[] {
  const actions: ParticipantMenuAction[] =
    member.actions.assignRole === true
      ? roleTargets[member.role].map((id) => ({ id, label: participantCopy.roleActions[id] }))
      : [];
  if (member.actions.transfer === true)
    actions.push({ id: "transfer", label: participantCopy.transfer });
  // Explicit presentation contract: an owner must first change role.
  if (member.role !== "owner" && member.actions.remove === true)
    actions.push({ id: "remove", label: participantCopy.remove, destructive: true });
  return actions;
}

interface SortableParticipant {
  view: OwnershipParticipantView;
  rank: number;
  name: string;
}

function compareParticipants(a: SortableParticipant, b: SortableParticipant): number {
  if (a.rank !== b.rank) return a.rank - b.rank;
  if (a.name !== b.name) return a.name < b.name ? -1 : 1;
  return a.view.member.userId < b.view.member.userId
    ? -1
    : a.view.member.userId === b.view.member.userId
      ? 0
      : 1;
}

export function ownershipParticipants(
  members: readonly OwnershipMember[],
  currentUserId: string,
): OwnershipParticipantView[] {
  return members
    .map((member): SortableParticipant => {
      const isCurrentUser = member.userId === currentUserId;
      return {
        rank: isCurrentUser ? 0 : roleOrder[member.role],
        name: nameKey(member.displayName),
        view: { member, isCurrentUser, actions: participantActions(member) },
      };
    })
    .sort(compareParticipants)
    .map(({ view }) => view);
}

export function filterOwnershipParticipants(
  participants: readonly OwnershipParticipantView[],
  search: string,
  filter: string,
): OwnershipParticipantView[] {
  const query = nameKey(search.trim());
  return participants.filter(
    ({ member }) =>
      (filter === "" || member.role === filter) && nameKey(member.displayName).includes(query),
  );
}
