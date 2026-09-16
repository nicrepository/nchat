/**
 * Versioned link-safety corrections: what this client knows about a message's
 * links that the copy it is holding does not.
 *
 * A correction is retained whenever a verdict arrives out of band — before the
 * message itself, or after a read that predates it — and is applied to every
 * copy of that message the timeline draws: the message, a quote preview of it,
 * and a cross-target reference preview of it. Every comparison here is on the
 * version the two sides carry, never on arrival order, because neither the
 * socket nor the HTTP reads are ordered against each other.
 */

import type { Message } from "../chatTypes";
import type { LinkSafetyChange, LinkSafetyCorrections } from "./types";

export function isOlderSecurityVersion(candidate: string, current: string): boolean {
  const candidateMs = Date.parse(candidate);
  const currentMs = Date.parse(current);
  return Number.isFinite(candidateMs) && Number.isFinite(currentMs) && candidateMs < currentMs;
}

export function isNotNewerSecurityVersion(candidate: string, current: string): boolean {
  const candidateMs = Date.parse(candidate);
  const currentMs = Date.parse(current);
  return Number.isFinite(candidateMs) && Number.isFinite(currentMs) && candidateMs <= currentMs;
}

export function applyLinkSafetyCorrection(
  message: Message,
  correction: LinkSafetyChange | undefined,
): Message {
  if (!correction || isOlderSecurityVersion(correction.updatedAt, message.updatedAt))
    return message;
  return {
    ...message,
    linkSafetyState: correction.state,
    bodyText: correction.state === "malicious" ? "" : message.bodyText,
    updatedAt: correction.updatedAt,
  };
}

/** The correction for the quoted message, applied to the preview of it. */
function applyQuoteCorrection(message: Message, corrections: LinkSafetyCorrections): Message {
  const quoted = message.quoted;
  if (!quoted) return message;
  const correction = corrections.get(quoted.id);
  if (!correction) return message;
  const drawn = quoted.updatedAt ?? quoted.createdAt;
  if (isOlderSecurityVersion(correction.updatedAt, drawn)) return message;
  return {
    ...message,
    quoted: {
      ...quoted,
      linkSafetyState: correction.state ?? "unknown",
      bodyText: correction.state === "malicious" ? "" : quoted.bodyText,
      updatedAt: correction.updatedAt,
    },
  };
}

/** The correction for the referenced message, applied to the preview of it. */
function applyReferenceCorrection(message: Message, corrections: LinkSafetyCorrections): Message {
  const reference = message.reference;
  if (!reference?.available) return message;
  const correction = corrections.get(reference.messageId);
  if (!correction) return message;
  const drawn = reference.updatedAt ?? reference.createdAt;
  if (isOlderSecurityVersion(correction.updatedAt, drawn)) return message;
  return {
    ...message,
    reference: {
      ...reference,
      linkSafetyState: correction.state ?? "unknown",
      bodyText: correction.state === "malicious" ? "" : reference.bodyText,
      updatedAt: correction.updatedAt,
    },
  };
}

/** Every correction this client holds, applied to one message and its previews. */
export function applyLinkSafetyCorrections(
  message: Message,
  corrections: LinkSafetyCorrections,
): Message {
  const corrected = applyLinkSafetyCorrection(message, corrections.get(message.id));
  return applyReferenceCorrection(applyQuoteCorrection(corrected, corrections), corrections);
}

/**
 * A copy of the corrections with the one this version supersedes removed. A
 * correction newer than the version being applied is kept: it is still the most
 * recent thing known about that message's links.
 */
export function dropSupersededCorrection(
  corrections: LinkSafetyCorrections,
  messageId: string,
  version: string,
): LinkSafetyCorrections {
  const next = new Map(corrections);
  const correction = next.get(messageId);
  if (!correction || !isNotNewerSecurityVersion(version, correction.updatedAt)) {
    next.delete(messageId);
  }
  return next;
}
