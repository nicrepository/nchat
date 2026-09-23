/**
 * What both rename surfaces know about renaming a conversation (issue #893).
 *
 * The sidebar's modal (RenameChannelDialog, issue #527) and the details
 * panel's inline editor are two affordances over *one* operation, so the parts
 * that are neither layout nor gesture live here: the server's own bounds, how
 * a refusal becomes a sentence, and which mutation a given target may use.
 *
 * Nothing here is an authorization decision and nothing here is validation.
 * PATCH /api/chat/channels/{id} and PATCH /api/chat/dm/{id} re-derive the
 * caller's authority from the session and re-apply the domain's rules on every
 * call, so a client that ignores this module gets a refusal, not an effect.
 */

import { ApiRequestError } from "../lib/api";
import { canRenameConversation } from "./conversationActions";
import type { Channel } from "./chatTypes";

/** The two aggregates that have a name of their own. A 1:1 does not. */
export type ConversationRenameKind = "channel" | "group";

/**
 * The server's caps, in the server's own unit: Unicode code points.
 *
 * 100 for a channel (domain.MaxChannelDisplayNameCodePoints) and 120 for a
 * group (maxDMTitleRunes), both counted with Go's `utf8.RuneCountInString`.
 *
 * These numbers are only ever compared against a code-point count. They were
 * briefly handed to an HTML `maxLength`, which was wrong in a way that is easy
 * to miss and impossible to see in ASCII: `maxLength` measures UTF-16 code
 * units, an astral character such as an emoji is two of them, and so a field
 * bounded at 100 refused a 100-emoji name the backend accepts — the client
 * silently enforcing half the domain's limit. `String.prototype.length` has
 * exactly the same flaw and is equally unusable here.
 */
export const conversationNameMaxCodePoints: Record<ConversationRenameKind, number> = {
  channel: 100,
  group: 120,
};

/**
 * The length the backend counts.
 *
 * The string iterator yields code points, not UTF-16 code units, which is what
 * makes this agree with `utf8.RuneCountInString` for every input. It is
 * deliberately not grapheme segmentation: the backend counts runes, so a
 * family emoji or a flag is several units to it as well, and a client counting
 * *fewer* would start refusing names the server accepts — the same class of
 * bug from the other direction.
 */
export function conversationNameCodePointLength(value: string): number {
  return Array.from(value).length;
}

/** The vocabulary of one aggregate, so a shared control never says "canal" for a group. */
export const conversationRenameCopy = {
  channel: {
    editAction: "Renomear canal",
    field: "Nome do canal",
    confirm: "Salvar novo nome do canal",
    cancel: "Cancelar a renomeação do canal",
    empty: "Escolha um nome para este canal.",
    /** The aggregate as a word, for a sentence that also states its cap. */
    noun: "canal",
    unnamed: "Canal sem nome",
    /** Kept distinct per aggregate so an existing assertion stays meaningful. */
    testId: "chat-details-channel-name",
  },
  group: {
    editAction: "Renomear grupo",
    field: "Nome do grupo",
    confirm: "Salvar novo nome do grupo",
    cancel: "Cancelar a renomeação do grupo",
    empty: "Escolha um nome para este grupo.",
    noun: "grupo",
    unnamed: "Grupo sem nome",
    testId: "chat-details-group-name",
  },
} as const;

/**
 * A failure as a sentence, by status code only.
 *
 * The server's own message is never surfaced: a rejected name can be tens of
 * kilobytes of caller-controlled text, and the endpoints deliberately decline
 * to say whether a refused conversation exists at all — so a 404 reads the
 * same whether the target is gone, private, or in another workspace.
 */
const renameErrorCopy: Record<
  ConversationRenameKind,
  { byStatus: Record<number, string>; generic: string }
> = {
  channel: {
    byStatus: {
      400: "Escolha um nome válido para esta conversa.",
      403: "Você não tem permissão para renomear este canal.",
      404: "Este canal não está mais disponível.",
      409: "O canal mudou enquanto você editava. Recarregue e tente de novo.",
      429: "Muitas solicitações em sequência. Aguarde um momento e tente novamente.",
      0: "Sem conexão. Verifique sua rede e tente novamente.",
    },
    generic: "Não foi possível renomear o canal. Tente novamente.",
  },
  group: {
    byStatus: {
      400: "Escolha um nome válido para esta conversa.",
      403: "Você não tem permissão para renomear este grupo.",
      404: "Este grupo não está mais disponível.",
      409: "O grupo mudou enquanto você editava. Recarregue e tente de novo.",
      429: "Muitas solicitações em sequência. Aguarde um momento e tente novamente.",
      0: "Sem conexão. Verifique sua rede e tente novamente.",
    },
    generic: "Não foi possível renomear o grupo. Tente novamente.",
  },
};

/**
 * Why this name cannot be sent, or "" when it can.
 *
 * The two verdicts a client can reach on its own, and the only two it reaches:
 * a name with nothing in it, and one past the cap. Both save a round trip the
 * user would only be told to undo, and neither makes this the authority —
 * the endpoints normalize and re-check the same two rules on every call, and a
 * name that gets past this still gets a 400 if the server disagrees.
 *
 * Shared by the inline editor and the sidebar's dialog so the two surfaces
 * cannot drift, and so a fix like the code-point one lands in both at once.
 *
 * `trimmed` is expected to be trimmed already: the caller trims because it is
 * also what it sends, and nothing here ever alters the value. In particular
 * there is no truncation — a name past the cap is refused and left in the
 * field for the user to shorten, never quietly cut to fit.
 */
export function renameNameRefusal(kind: ConversationRenameKind, trimmed: string): string {
  const copy = conversationRenameCopy[kind];
  if (!trimmed) return copy.empty;
  const cap = conversationNameMaxCodePoints[kind];
  if (conversationNameCodePointLength(trimmed) > cap) {
    // The number is read from the cap rather than written into the sentence,
    // so the copy cannot come to disagree with the rule it describes.
    return `O nome do ${copy.noun} deve ter no máximo ${cap} caracteres.`;
  }
  return "";
}

export function renameErrorMessage(kind: ConversationRenameKind, error: unknown): string {
  const copy = renameErrorCopy[kind];
  if (!(error instanceof ApiRequestError)) return copy.generic;
  return copy.byStatus[error.status] ?? copy.generic;
}

/** Persists one new name for the conversation the caller already identified. */
export type ConversationRenameAction = (name: string) => Promise<void>;

export interface ConversationRenameInput {
  /** The panel's own discriminant; null while no target is resolved. */
  kind: "channel" | "group" | "direct" | null;
  targetId: string;
  /** The canonical sidebar list, which carries the server's capability flags. */
  channels: Channel[];
  renameChannel?: (channelId: string, displayName: string) => Promise<void>;
  renameGroup?: (conversationId: string, title: string) => Promise<void>;
}

/**
 * The channel half: the capability is the server's, read from the sidebar row.
 *
 * `canRenameConversation` is the sidebar row menu's own predicate, so the panel
 * and the menu offer rename under exactly one rule — including the general
 * channel, which neither offers and the backend refuses in SQL regardless.
 */
function channelRenameAction(input: ConversationRenameInput): ConversationRenameAction | undefined {
  const { targetId, channels, renameChannel } = input;
  if (!renameChannel) return undefined;
  const channel = channels.find((candidate) => candidate.id === targetId);
  if (!channel) return undefined;
  const allowed = canRenameConversation({
    kind: "channel",
    canRename: channel.canRename,
    isGeneral: channel.isGeneral,
  });
  return allowed ? (name) => renameChannel(targetId, name) : undefined;
}

/**
 * How this caller may rename this target, or undefined when they may not.
 *
 * Undefined is the whole "no affordance" mechanism: a surface that gets no
 * action renders no control, so "not permitted", "general channel", "1:1" and
 * "no mutation wired" are one absent value rather than four flags to forget.
 */
export function conversationRenameAction(
  input: ConversationRenameInput,
): ConversationRenameAction | undefined {
  const { kind, targetId, renameGroup } = input;
  if (!targetId) return undefined;
  if (kind === "channel") return channelRenameAction(input);
  if (kind !== "group" || !renameGroup) return undefined;
  return (name) => renameGroup(targetId, name);
}
