/**
 * Which status change, if any, the console may offer for an account.
 *
 * `auth.users.status` holds five values, and this feature governs exactly one
 * transition pair: `active ↔ suspended`. `invited` is owned by the invitation
 * flow, `locked` by the brute-force protection, `deleted` by erasure — the API
 * refuses all three, and the console must not advertise an operation the
 * platform will reject.
 *
 * A pure function rather than a ternary in the table, because the rule was
 * already being asked in two places and the second one got it wrong: anything
 * that was not `active` was offered "Ativar", so an invited or locked account
 * showed a button whose only possible outcome was a 409.
 *
 * Returning `null` is the fail-closed default. A status this build has never
 * heard of gets no button, rather than the opposite one.
 */
export interface UserStatusAction {
  targetStatus: "active" | "suspended";
  label: string;
  confirmTitle: string;
  confirmBody: (who: string) => string;
  impact: string;
}

export function userStatusAction(status: string): UserStatusAction | null {
  switch (status) {
    case "active":
      return {
        targetStatus: "suspended",
        label: "Desativar",
        confirmTitle: "Desativar esta conta?",
        confirmBody: (who) => `${who} deixará de conseguir entrar no NChat.`,
        impact:
          "Todas as sessões ativas são encerradas na mesma transação. Isso desativa a conta no NChat e não no provedor de identidade.",
      };
    case "suspended":
      return {
        targetStatus: "active",
        label: "Ativar",
        confirmTitle: "Reativar esta conta?",
        confirmBody: (who) => `${who} voltará a conseguir entrar no NChat.`,
        impact: "Nenhuma sessão anterior é restaurada: a pessoa precisa entrar novamente.",
      };
    default:
      return null;
  }
}

/**
 * Why no action is offered, when none is.
 *
 * Shown instead of a button so the operator learns that the state is owned by
 * another flow, rather than wondering whether the console is broken.
 */
export function noStatusActionReason(status: string): string {
  switch (status) {
    case "invited":
      return "convite pendente";
    case "locked":
      return "bloqueado por segurança";
    default:
      return "estado não gerenciado aqui";
  }
}
