import { useRef, useState } from "react";

type CopyFeedback = "idle" | "success" | "error";

const feedbackText: Record<CopyFeedback, string> = {
  idle: "",
  success: "E-mail copiado",
  error: "Não foi possível copiar o e-mail",
};

/**
 * The "E-mail" row of a 1:1 profile, where activating the address copies it
 * (issue #898).
 *
 * Copy, not compose: the primary action is never a mailto: link, so nothing
 * here builds a URL from the address. The address only ever reaches the DOM as
 * the button's text and the clipboard as the argument the user asked for — it
 * is not logged, and a failure is reported in fixed words, never with the
 * browser's error.
 *
 * A missing Clipboard API is the same failure as a refused write: calling it
 * throws inside the try, and the reader is told the copy did not happen rather
 * than being shown a success that is not true.
 *
 * The live region is always in the DOM so the first message is announced.
 * Each attempt is numbered, and two rules follow from that number:
 *  - only the latest attempt may report: an older write settling late
 *    describes a click the user has already superseded;
 *  - the message node is keyed by its attempt, so a repeated outcome replaces
 *    the node instead of re-rendering identical text — which React would turn
 *    into no DOM change at all, and a screen reader into silence. This holds
 *    even when the reset and the failure are batched into one render, as they
 *    are when the API is missing and the attempt fails synchronously.
 *
 * Feedback belongs to one address: the caller keys this component by person,
 * so a switch to another DM starts from idle and a late promise from the
 * previous one lands on an unmounted instance.
 */
export default function CopyableEmailRow({ email }: { email: string }) {
  const [feedback, setFeedback] = useState({ status: "idle" as CopyFeedback, attempt: 0 });
  const latestAttempt = useRef(0);

  async function copy() {
    const attempt = ++latestAttempt.current;
    setFeedback({ status: "idle", attempt });
    let status: CopyFeedback = "error";
    try {
      await navigator.clipboard.writeText(email);
      status = "success";
    } catch {
      // Reported below in fixed words; the browser's error is never shown.
    }
    if (attempt === latestAttempt.current) setFeedback({ status, attempt });
  }

  return (
    <div className="chat-details__profile-row">
      <span className="chat-details__profile-row-label">E-mail</span>
      <span className="chat-details__profile-row-value chat-details__profile-copy-value">
        <button
          type="button"
          className="chat-details__profile-copy"
          title="Copiar e-mail"
          onClick={() => void copy()}
        >
          {email}
        </button>
        <span
          role="status"
          className={`chat-details__profile-copy-feedback chat-details__profile-copy-feedback--${feedback.status}`}
        >
          <span key={feedback.attempt}>{feedbackText[feedback.status]}</span>
        </span>
      </span>
    </div>
  );
}
