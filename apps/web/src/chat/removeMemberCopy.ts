/**
 * What the removal confirmation says, and what a failed removal says
 * (issue #469).
 *
 * Pure and in a module of its own: both are rules about wording that have to
 * be true of the domain, and both are worth asserting directly rather than by
 * reading sentences out of a rendered dialog.
 */

import { ApiRequestError } from "../lib/api";

/**
 * What the person actually loses, stated per kind and never overstated.
 *
 * A group and a private channel both require the membership row to be read at
 * all, so losing it really is losing access. A **public** channel is not:
 * `chat.channel_visible_to_user` admits every workspace role that can reach
 * public channels, with or without a row, so promising that someone "will lose
 * access to the messages" there would be a sentence the domain does not
 * honour — and it is issue #883, not this flow, that owns whether that stays
 * true. What removal actually takes from a public channel is the membership:
 * the sidebar entry, the notifications and the roster place.
 *
 * Phrased without gender throughout. The domain stores a display name and
 * nothing else, so "Esta pessoa" is the only pronoun this copy can honestly
 * use.
 */
export function removalConsequence(kind: "channel" | "group", isPrivateChannel: boolean): string {
  if (kind === "group") {
    return "Esta pessoa deixará de participar do grupo e perderá o acesso às mensagens, aos arquivos e aos eventos futuros.";
  }
  if (isPrivateChannel) {
    return "Esta pessoa perderá o acesso às mensagens, aos arquivos e aos eventos futuros deste canal privado.";
  }
  return "Esta pessoa deixa de ser membro do canal, que sai da barra lateral dela e para de notificá-la. Por ser um canal público, o conteúdo continua visível para quem faz parte do workspace.";
}

/**
 * What a failed removal says, by observable outcome.
 *
 * The statuses are the ones this route can actually produce: 403 for a caller
 * without the authority, 404 for a conversation that is gone or was never
 * visible, 400 for a target the domain refuses (the caller's own membership,
 * #geral), 429 for the shared write budget, and 0 for a request that never
 * reached the server.
 */
const removalFailureByStatus: Record<number, string> = {
  403: "Você não tem permissão para remover esta pessoa.",
  404: "Esta conversa não está mais disponível.",
  400: "Não é possível remover esta pessoa desta conversa.",
  429: "Muitas solicitações em sequência. Aguarde um momento e tente novamente.",
  0: "Sem conexão. Verifique sua rede e tente novamente.",
};

/** Any other outcome, including a failure that is not an API error at all. */
const removalFailureFallback = "Não foi possível remover. Tente novamente.";

/**
 * The message for a failed removal.
 *
 * The server's own text is never shown. It is written for an operator, it can
 * name a table or an internal rule, and a client that forwards it turns every
 * backend change into a UI change — so the wording is chosen here, from the
 * status alone.
 */
export function removeMemberErrorMessage(error: unknown): string {
  if (!(error instanceof ApiRequestError)) return removalFailureFallback;
  return removalFailureByStatus[error.status] ?? removalFailureFallback;
}
