/**
 * RF-21 (issue #135): what is known about a published message's links changed.
 *
 * One event, applied to every copy of the message this timeline draws — the
 * message itself, a quote preview of it, a cross-target reference preview of it,
 * and the reply target — and retained as a versioned correction for the copies
 * that have not arrived yet.
 */

import type { Message } from "../chatTypes";
import { applyLinkUpdate, type MessageLink } from "../messageLinks";
import {
  applyLinkSafetyCorrection,
  bodyUnderAggregate,
  isOlderSecurityVersion,
} from "./linkSafetyCorrections";
import type { Action, ActionOf, LinkSafetyChange, MessagesState } from "./types";

type Changed = ActionOf<"link_safety_changed">;

/** True when this message, its quote or its reference is newer than the change. */
function holdsNewerVersionOf(message: Message, action: Changed): boolean {
  const quoted = message.quoted;
  const reference = message.reference;
  if (message.id === action.messageId) {
    return isOlderSecurityVersion(action.updatedAt, message.updatedAt);
  }
  if (quoted?.id === action.messageId) {
    return isOlderSecurityVersion(action.updatedAt, quoted.updatedAt ?? quoted.createdAt);
  }
  if (reference?.available && reference.messageId === action.messageId) {
    return isOlderSecurityVersion(action.updatedAt, reference.updatedAt ?? reference.createdAt);
  }
  return false;
}

/**
 * Whether a link-safety correction is already reflected, or is older than what
 * is drawn.
 *
 * A quote or a cross-target reference can be the only visible copy of the
 * source, and its version is authoritative too: accepting an older correction
 * here would let a later stale snapshot re-apply it. At-least-once delivery of
 * this event is free precisely because this says "nothing to do" for a repeat.
 */
function linkSafetyChangeIsStale(state: MessagesState, action: Changed): boolean {
  const previous = state.linkSafetyCorrections.get(action.messageId);
  if (previous?.state === action.state && previous?.updatedAt === action.updatedAt) return true;
  if (previous && isOlderSecurityVersion(action.updatedAt, previous.updatedAt)) return true;
  if (
    state.replyTo?.id === action.messageId &&
    isOlderSecurityVersion(action.updatedAt, state.replyTo.updatedAt)
  ) {
    return true;
  }
  return state.messages.some((message) => holdsNewerVersionOf(message, action));
}

/**
 * The message body's own correction: nothing when it is already applied. The
 * aggregate never withholds the body of a message that carries links[] — that
 * body is the server's own span-level projection (bodyUnderAggregate).
 */
function correctMessageBody(message: Message, action: Changed): Message {
  const bodyText = bodyUnderAggregate(message, action.state);
  const unchanged =
    (message.linkSafetyState ?? "") === (action.state ?? "") && bodyText === message.bodyText;
  if (message.id !== action.messageId || unchanged) return message;
  if (isOlderSecurityVersion(action.updatedAt, message.updatedAt)) return message;
  return { ...message, linkSafetyState: action.state, bodyText, updatedAt: action.updatedAt };
}

/** The same correction applied to the quote preview this message carries. */
function correctQuotedPreview(message: Message, action: Changed, malicious: boolean): Message {
  const quoted = message.quoted;
  if (!quoted || quoted.id !== action.messageId) return message;
  const unchanged =
    quoted.linkSafetyState === action.state && !(malicious && quoted.bodyText !== "");
  if (unchanged) return message;
  if (isOlderSecurityVersion(action.updatedAt, quoted.updatedAt ?? quoted.createdAt))
    return message;
  return {
    ...message,
    quoted: {
      ...quoted,
      linkSafetyState: action.state ?? "",
      bodyText: malicious ? "" : quoted.bodyText,
      updatedAt: action.updatedAt,
    },
  };
}

/** Narrows a reference to the available branch and to one source message. */
function referenceIsAbout(
  reference: Message["reference"],
  messageId: string,
): reference is Extract<NonNullable<Message["reference"]>, { available: true }> {
  return reference?.available === true && reference.messageId === messageId;
}

/** The same correction applied to the cross-target reference preview. */
function correctReferencePreview(message: Message, action: Changed, malicious: boolean): Message {
  const reference = message.reference;
  if (!referenceIsAbout(reference, action.messageId)) return message;
  const unchanged =
    reference.linkSafetyState === action.state && !(malicious && reference.bodyText !== "");
  if (unchanged) return message;
  if (isOlderSecurityVersion(action.updatedAt, reference.updatedAt ?? reference.createdAt)) {
    return message;
  }
  return {
    ...message,
    reference: {
      ...reference,
      linkSafetyState: action.state ?? "",
      bodyText: malicious ? "" : reference.bodyText,
      updatedAt: action.updatedAt,
    },
  };
}

/**
 * RF-21 (issue #135): what is known about a published message's links changed.
 *
 * Nothing here inserts a message and nothing changes `status`: if the message
 * has not arrived yet, only a versioned correction is retained for its eventual
 * create event. Nothing here fetches a URL either — see MessageContent, where
 * this state is rendered and never acted on.
 *
 * lastMutation stays "none": nothing was added or removed, so the list must not
 * scroll. A notice appearing above a message the reader is looking at should not
 * move the conversation under them.
 */
function applyLinkSafetyChanged(state: MessagesState, action: Changed): MessagesState {
  if (linkSafetyChangeIsStale(state, action)) return state;
  const change: LinkSafetyChange = { state: action.state, updatedAt: action.updatedAt };
  const linkSafetyCorrections = new Map(state.linkSafetyCorrections);
  linkSafetyCorrections.set(action.messageId, change);
  const malicious = action.state === "malicious";
  const messages = state.messages.map((message) =>
    correctReferencePreview(
      correctQuotedPreview(correctMessageBody(message, action), action, malicious),
      action,
      malicious,
    ),
  );
  const replyTo =
    state.replyTo?.id === action.messageId
      ? applyLinkSafetyCorrection(state.replyTo, change)
      : state.replyTo;
  return { ...state, messages, replyTo, linkSafetyCorrections, lastMutation: "none" };
}

/**
 * Issue #807: a per-link update, applied to every occurrence of its URL in the
 * one message it names. Nothing else moves — not the body, not the aggregate
 * marker — because the update describes a target, not the message. A condemned
 * target never arrives here: it carries no URL, and the hook re-reads the
 * message instead so the redacted body arrives with the links.
 */
function applyLinkUpdated(state: MessagesState, action: ActionOf<"link_updated">): MessagesState {
  let changed = false;
  const messages = state.messages.map((message) => {
    if (message.id !== action.messageId || !message.links) return message;
    const links = applyLinkUpdate(message.links, action.link);
    if (!links || linksEqual(links, message.links)) return message;
    changed = true;
    return { ...message, links };
  });
  return changed ? { ...state, messages, lastMutation: "none" } : state;
}

function linksEqual(a: readonly MessageLink[], b: readonly MessageLink[]): boolean {
  return a.length === b.length && a.every((link, index) => link === b[index]);
}

/** RF-21 verdicts about a published message's links. */
export function reduceLinkSafety(state: MessagesState, action: Action): MessagesState | undefined {
  if (action.type === "link_updated") return applyLinkUpdated(state, action);
  return action.type === "link_safety_changed" ? applyLinkSafetyChanged(state, action) : undefined;
}
