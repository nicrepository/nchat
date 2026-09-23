/**
 * One URL occurrence in a message body, drawn the way the server described it
 * (issue #807).
 *
 * The four states are four different elements on purpose, because they are
 * four different things to a keyboard and to a screen reader:
 *
 *   safe      an ordinary anchor to the server's href, new tab, noopener
 *   unknown   a button — there is no href — that opens the interstitial
 *   pending   plain text with a status note; not focusable, not a link
 *   blocked   a chip in place of the withheld URL; not focusable, not a link
 *
 * Nothing here fetches, prefetches or parses. The href comes from the server
 * or there is none.
 */

import type { MessageLink } from "./messageLinks";

export const PENDING_LINK_LABEL = "Verificando segurança do link…";
export const UNKNOWN_LINK_LABEL = "Link não verificado";
export const BLOCKED_LINK_LABEL = "Link bloqueado por segurança";

export interface MessageLinkSpanProps {
  link: MessageLink;
  /** The span's text as written in the body. */
  text: string;
  onOpenUnverified?: (link: MessageLink, trigger: HTMLElement) => void;
}

function SafeAnchor({ link, text }: { link: MessageLink; text: string }) {
  return (
    <a
      className="rtr-link"
      href={link.href}
      target="_blank"
      rel="noopener noreferrer"
      // The canonical destination on hover, so an IDN spelling shows its
      // punycode form: the label is what was written, the title is where it
      // goes.
      title={link.href !== text ? link.href : undefined}
      data-link-safety="safe"
    >
      {text}
    </a>
  );
}

function UnverifiedButton({
  link,
  text,
  onOpen,
}: {
  link: MessageLink;
  text: string;
  onOpen?: (link: MessageLink, trigger: HTMLElement) => void;
}) {
  return (
    <button
      type="button"
      className="rtr-link rtr-link--unverified"
      data-link-safety="unknown"
      aria-label={`${text} — ${UNKNOWN_LINK_LABEL}`}
      onClick={(event) => onOpen?.(link, event.currentTarget)}
    >
      <span className="material-symbols-outlined" aria-hidden="true">
        help
      </span>
      {text}
    </button>
  );
}

function PendingText({ text }: { text: string }) {
  return (
    <span className="rtr-link-pending" data-link-safety="pending">
      <span className="rtr-link-pending__text">{text}</span>
      <span className="rtr-link-pending__note" role="status">
        <span className="material-symbols-outlined" aria-hidden="true">
          shield
        </span>
        {PENDING_LINK_LABEL}
      </span>
    </span>
  );
}

/** The chip drawn where the server withheld a condemned URL. */
export function BlockedLinkChip() {
  return (
    <span className="rtr-link-blocked" data-link-safety="malicious" role="note">
      <span className="material-symbols-outlined" aria-hidden="true">
        gpp_maybe
      </span>
      {BLOCKED_LINK_LABEL}
    </span>
  );
}

export default function MessageLinkSpan({ link, text, onOpenUnverified }: MessageLinkSpanProps) {
  if (link.click === "direct" && link.href !== "") return <SafeAnchor link={link} text={text} />;
  if (link.click === "interstitial" && link.url !== "") {
    return <UnverifiedButton link={link} text={text} onOpen={onOpenUnverified} />;
  }
  if (link.safety === "malicious") return <BlockedLinkChip />;
  return <PendingText text={text} />;
}
