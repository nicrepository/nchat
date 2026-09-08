import { useEffect } from "react";

import { initialsFrom } from "./messageDisplay";
import { PersonAvatarImage } from "./PersonAvatarImage";
import "./InAppMessageAlert.css";

/**
 * The in-app surface of the delivery plan (issue #744): the `in_app` channel's
 * consumer.
 *
 * It is deliberately the smallest thing that can be called a surface — one
 * alert, the newest, dismissed by opening it or closing it, and gone on its own
 * after a few seconds. There is no history, nothing persisted, and no second
 * place to read it: those would be a notification centre, which is a product
 * decision this issue does not get to make.
 *
 * It decides nothing. Whether it is rendered at all is `policy.in_app`, resolved
 * per recipient by chat-service and narrowed only by strictly local conditions
 * in soundRules.ts.
 */
export interface InAppAlert {
  messageId: string;
  targetKind: "channel" | "dm";
  targetId: string;
  senderDisplayName: string;
  senderAvatarUrl?: string;
  bodyText: string;
  conversationName: string;
}

/** How long an unattended alert stays on screen. */
const AUTO_DISMISS_MS = 6000;

interface InAppMessageAlertProps {
  alert: InAppAlert;
  onOpen: (alert: InAppAlert) => void;
  onDismiss: () => void;
}

export default function InAppMessageAlert({ alert, onOpen, onDismiss }: InAppMessageAlertProps) {
  // Keyed by the message in AppShell, so a newer alert remounts this and the
  // timer starts again rather than inheriting the previous one's remaining time.
  useEffect(() => {
    const timer = window.setTimeout(onDismiss, AUTO_DISMISS_MS);
    return () => window.clearTimeout(timer);
  }, [onDismiss]);

  return (
    <aside
      className="in-app-alert"
      // Polite, not assertive: a message arriving elsewhere is worth announcing
      // and is never worth interrupting what the reader is doing or saying.
      role="status"
      aria-live="polite"
      data-testid="in-app-message-alert"
    >
      <button
        type="button"
        className="in-app-alert__open"
        onClick={() => onOpen(alert)}
        data-testid="in-app-message-alert-open"
      >
        <span className="in-app-alert__avatar" aria-hidden="true">
          <PersonAvatarImage
            src={alert.senderAvatarUrl}
            initials={initialsFrom(alert.senderDisplayName)}
            imgClassName="in-app-alert__avatar-img"
          />
        </span>
        <span className="in-app-alert__text">
          <strong className="in-app-alert__sender">{alert.senderDisplayName}</strong>
          <span className="in-app-alert__where">{alert.conversationName}</span>
          {/* The same preview the OS notification already shows; no field the
              product does not already put on another surface. */}
          <span className="in-app-alert__preview">{alert.bodyText}</span>
        </span>
      </button>
      <button
        type="button"
        className="in-app-alert__dismiss"
        onClick={onDismiss}
        aria-label="Dispensar notificação"
        data-testid="in-app-message-alert-dismiss"
      >
        <span className="material-symbols-outlined" aria-hidden="true">
          close
        </span>
      </button>
    </aside>
  );
}
