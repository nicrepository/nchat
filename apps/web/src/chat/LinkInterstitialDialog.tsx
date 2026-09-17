/**
 * The "this link could not be verified" confirmation (issue #807 §13).
 *
 * Opened for a link the server marked `unknown` with click policy
 * `interstitial`. It shows the real destination host — the punycoded one the
 * server canonicalised, never a page's own claim — and offers exactly two
 * actions: cancel, or open in a new tab with noopener/noreferrer. Nothing is
 * fetched before the reader decides; nothing here says the link is safe.
 *
 * The shell follows LeaveConversationDialog: a portal, a backdrop, role=dialog,
 * Escape closes, Tab is trapped, and focus returns to the element that opened
 * it. Initial focus is on Cancel: a stray Enter must not navigate.
 */

import { type KeyboardEvent, useCallback, useEffect, useRef, useState } from "react";
import { createPortal } from "react-dom";

import "./LinkInterstitialDialog.css";
import type { LinkSafetyRecheck } from "./chatTypes";
import type { MessageLink } from "./messageLinks";

const titleId = "chat-link-interstitial-title";
const descriptionId = "chat-link-interstitial-description";

export interface LinkInterstitialDialogProps {
  link: MessageLink;
  /** The element that opened the dialog; focus returns to it on close. */
  trigger: HTMLElement | null;
  onClose: () => void;
  /**
   * "Verificar novamente" (issue #135): asks the server to re-read a verdict it
   * may already hold. Never starts a scan. Absent when the message cannot be
   * rechecked, in which case the action is not offered.
   */
  onRecheck?: () => Promise<LinkSafetyRecheck | undefined>;
}

function trapTab(event: KeyboardEvent<HTMLDivElement>, dialog: HTMLDivElement | null) {
  const focusable = dialog?.querySelectorAll<HTMLElement>("button:not(:disabled), a[href]");
  if (!focusable?.length) return;
  const first = focusable[0];
  const last = focusable[focusable.length - 1];
  if (event.shiftKey && document.activeElement === first) {
    event.preventDefault();
    last.focus();
  } else if (!event.shiftKey && document.activeElement === last) {
    event.preventDefault();
    first.focus();
  }
}

export default function LinkInterstitialDialog({
  link,
  trigger,
  onClose,
  onRecheck,
}: LinkInterstitialDialogProps) {
  const cancelRef = useRef<HTMLButtonElement>(null);
  const dialogRef = useRef<HTMLDivElement>(null);
  const [checking, setChecking] = useState(false);
  const [recheckDone, setRecheckDone] = useState(false);

  useEffect(() => {
    cancelRef.current?.focus();
    return () => {
      // Back to the link that opened the dialog, whether it was closed by
      // Escape, Cancel or by opening the destination.
      trigger?.focus();
    };
  }, [trigger]);

  const handleKeyDown = useCallback(
    (event: KeyboardEvent<HTMLDivElement>) => {
      if (event.key === "Escape") {
        event.preventDefault();
        onClose();
        return;
      }
      if (event.key === "Tab") trapTab(event, dialogRef.current);
    },
    [onClose],
  );

  const recheck = useCallback(async () => {
    if (!onRecheck || checking) return;
    setChecking(true);
    try {
      await onRecheck();
    } finally {
      setChecking(false);
      setRecheckDone(true);
    }
  }, [checking, onRecheck]);

  return createPortal(
    <div className="link-interstitial__backdrop" onMouseDown={onClose}>
      <div
        ref={dialogRef}
        className="link-interstitial"
        role="dialog"
        aria-modal="true"
        aria-labelledby={titleId}
        aria-describedby={descriptionId}
        data-testid="chat-link-interstitial"
        onKeyDown={handleKeyDown}
        onMouseDown={(event) => event.stopPropagation()}
      >
        <h2 id={titleId} className="link-interstitial__title">
          <span className="material-symbols-outlined" aria-hidden="true">
            warning
          </span>
          Não foi possível verificar este link
        </h2>
        <p className="link-interstitial__host" data-testid="chat-link-interstitial-host">
          {link.hostname || link.url}
        </p>
        <p id={descriptionId} className="link-interstitial__description">
          O serviço de segurança não conseguiu verificar este endereço agora. Abra apenas se confiar
          em quem o enviou.
        </p>
        <p className="link-interstitial__url">{link.url}</p>
        <div className="link-interstitial__actions">
          {onRecheck && !recheckDone && (
            <button
              type="button"
              className="link-interstitial__recheck"
              disabled={checking}
              onClick={() => void recheck()}
            >
              {checking ? "Verificando…" : "Verificar novamente"}
            </button>
          )}
          <button
            ref={cancelRef}
            type="button"
            className="link-interstitial__cancel"
            onClick={onClose}
          >
            Cancelar
          </button>
          <a
            className="link-interstitial__open"
            href={link.url}
            target="_blank"
            rel="noopener noreferrer"
            onClick={onClose}
          >
            Abrir mesmo assim
          </a>
        </div>
      </div>
    </div>,
    document.body,
  );
}
