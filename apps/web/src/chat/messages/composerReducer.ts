/**
 * Sending a message, and what the server refused.
 *
 * The refusal copy lives here because it is part of the same decision: RF-21
 * distinguishes a link that was blocked from a check that could not run, and
 * saying the wrong one is worse than saying nothing.
 */

import { ApiRequestError } from "../../lib/api";
import { applyLinkSafetyCorrections } from "./linkSafetyCorrections";
import type { Action, ActionOf, MessagesState } from "./types";

/**
 * Copy for the send failures this client recognises by code (RF-21).
 *
 * Keyed on `code` and never on the message text: the code is the stable part of
 * the contract, and matching on English prose from a server would break the
 * moment that prose changed. The two entries are deliberately different
 * sentences — one says the link is dangerous and is final, the other says the
 * check could not run and is worth retrying — because telling someone to try
 * again on a blocked link is as wrong as telling them a transient outage means
 * their link is malicious.
 *
 * The security decision itself is entirely server-side. Nothing here inspects a
 * URL, and there is no provider credential in this bundle: this only renders a
 * verdict the backend already made and enforced.
 */
const sendErrorMessages: Record<string, string> = {
  malicious_url: "Este link foi bloqueado por segurança.",
  link_check_unavailable:
    "Não foi possível verificar a segurança do link. Tente novamente em instantes.",
  // A terminal outcome, not a transient one — the scan finished and produced no
  // usable verdict, so unlike link_check_unavailable this deliberately carries
  // no "try again" implication: retrying does not resubmit anything.
  link_check_inconclusive: "Não foi possível verificar a segurança deste link.",
  // The backend declined to start a new scan right now — a spent window or a
  // full queue. Deliberately worded like the unavailable case and deliberately
  // not like the blocked one: nothing was decided about this link, and telling
  // someone their link looks dangerous because a queue was full is a claim with
  // nothing behind it.
  link_check_capacity: "Não foi possível verificar os links agora. Tente novamente em instantes.",
};

/**
 * The copy for a pending message that is gone for a reason nobody attributed to
 * the link check.
 *
 * Separate from the blocked copy on purpose. Telling an author their link was
 * malicious when the evidence only says the message no longer exists is a claim
 * we cannot make, and it is the inference this whole path is built to avoid.
 */
const pendingMessageUnavailable = "Esta mensagem não está mais disponível.";

export function blockedMessageReason(
  reason?: string,
): "malicious_link" | "link_check_inconclusive" {
  return reason === "link_check_inconclusive" ? reason : "malicious_link";
}

export function sendErrorMessage(error: unknown): string {
  if (error instanceof ApiRequestError) {
    const known = sendErrorMessages[error.code];
    if (known) return known;
  }
  // Unchanged for everything else: the previous behaviour is the fallback, so
  // no existing error path is affected by RF-21.
  return error instanceof Error ? error.message : "Não foi possível enviar a mensagem.";
}

function applySent(state: MessagesState, action: ActionOf<"sent">): MessagesState {
  // Deduplicate: a realtime event or a prior send might have already added this message.
  const alreadyPresent = state.messages.some((m) => m.id === action.message.id);
  const message = applyLinkSafetyCorrections(action.message, state.linkSafetyCorrections);
  const linkSafetyCorrections = new Map(state.linkSafetyCorrections);
  linkSafetyCorrections.delete(action.message.id);
  return {
    ...state,
    messages: alreadyPresent ? state.messages : [...state.messages, message],
    sending: false,
    sendError: null,
    lastMutation: alreadyPresent ? "none" : "append",
    realtimeError: null,
    replyTo: null,
    linkSafetyCorrections,
  };
}

/**
 * The copy for a withheld message that reached a terminal state.
 *
 * "unavailable" is the one explicit sentinel for "the message itself is gone"
 * (reconciliation found no blocked verdict behind it). Every other reason — a
 * recognised one, or one this client does not know yet — is a link refusal, and
 * defaults to the malicious wording rather than the "gone" one: downgrading an
 * unrecognised-but-real refusal to "message unavailable" would discard a verdict
 * already established.
 */
function blockedSendError(reason: ActionOf<"message_blocked">["reason"]): string {
  if (reason === "unavailable") return pendingMessageUnavailable;
  if (reason === "link_check_inconclusive") return sendErrorMessages.link_check_inconclusive;
  return sendErrorMessages.malicious_url;
}

function applyMessageBlocked(
  state: MessagesState,
  action: ActionOf<"message_blocked">,
): MessagesState {
  // Only a message this client is still showing as pending is affected. A
  // late event for something already resolved, or for a message this view
  // never held, is a no-op.
  const isPending = state.messages.some(
    (m) => m.id === action.messageId && m.status === "pending_link_scan",
  );
  if (!isPending) return state;
  return {
    ...state,
    messages: state.messages.filter((m) => m.id !== action.messageId),
    sending: false,
    sendError: blockedSendError(action.reason),
    lastMutation: "none",
  };
}

/** Sending, and what the server refused. */
export function reduceComposer(state: MessagesState, action: Action): MessagesState | undefined {
  switch (action.type) {
    case "sending":
      return { ...state, sending: true, sendError: null };
    case "sent":
      return applySent(state, action);
    case "send_error":
      return { ...state, sending: false, sendError: action.error };
    case "message_blocked":
      return applyMessageBlocked(state, action);
    default:
      return undefined;
  }
}
