/**
 * "Citar em outra conversa": pick the conversation a quote is carried into
 * (moved out of ChatMessageArea, issue #834).
 *
 * A modal dialog in a portal, owning its own focus contract: initial focus on
 * the close button, Tab cycled within the dialog, Escape closes, and focus
 * restored to whatever held it before the dialog opened.
 */

import { useEffect, useRef } from "react";
import { createPortal } from "react-dom";

import type { ChatOutletContext } from "../../ChatShell";

export default function ReferenceDestinationDialog({
  current,
  channels,
  dms,
  onClose,
  onSelect,
}: {
  current: { kind: "channel" | "dm"; id: string };
  channels: ChatOutletContext["channels"];
  dms: ChatOutletContext["dms"];
  onClose: () => void;
  onSelect: (target: { kind: "channel" | "dm"; id: string }) => void;
}) {
  const dialogRef = useRef<HTMLDivElement>(null);
  const closeButtonRef = useRef<HTMLButtonElement>(null);
  useEffect(() => {
    const previouslyFocused = document.activeElement as HTMLElement;
    closeButtonRef.current!.focus();
    const handleDialogKey = (event: KeyboardEvent) => {
      if (event.key === "Escape") {
        event.preventDefault();
        onClose();
        return;
      }
      if (event.key !== "Tab") return;
      const focusable = dialogRef.current!.querySelectorAll<HTMLButtonElement>("button");
      const first = focusable[0];
      const last = focusable[focusable.length - 1];
      if (event.shiftKey && document.activeElement === first) {
        event.preventDefault();
        last.focus();
      } else if (!event.shiftKey && document.activeElement === last) {
        event.preventDefault();
        first.focus();
      }
    };
    document.addEventListener("keydown", handleDialogKey);
    return () => {
      document.removeEventListener("keydown", handleDialogKey);
      previouslyFocused.focus();
    };
  }, [onClose]);
  const targets = [
    ...channels.map((channel) => ({
      kind: "channel" as const,
      id: channel.id,
      name: channel.name,
    })),
    ...dms.map((dm) => ({ kind: "dm" as const, id: dm.id, name: dm.name })),
  ].filter((target) => target.kind !== current.kind || target.id !== current.id);

  return createPortal(
    <div className="chat-reference-dialog__backdrop" onMouseDown={onClose}>
      <div
        ref={dialogRef}
        className="chat-reference-dialog"
        role="dialog"
        aria-modal="true"
        aria-labelledby="chat-reference-dialog-title"
        onMouseDown={(event) => event.stopPropagation()}
      >
        <header>
          <div>
            <h2 id="chat-reference-dialog-title">Citar em outra conversa</h2>
            <p>Escolha onde a nova mensagem será enviada.</p>
          </div>
          <button ref={closeButtonRef} type="button" aria-label="Fechar" onClick={onClose}>
            <span className="material-symbols-outlined" aria-hidden="true">
              close
            </span>
          </button>
        </header>
        {targets.length === 0 ? (
          <p className="chat-reference-dialog__empty">Nenhum outro destino disponível.</p>
        ) : (
          <ul aria-label="Destinos disponíveis">
            {targets.map((target) => (
              <li key={`${target.kind}:${target.id}`}>
                <button type="button" onClick={() => onSelect(target)}>
                  <span className="material-symbols-outlined" aria-hidden="true">
                    {target.kind === "channel" ? "tag" : "forum"}
                  </span>
                  {target.name}
                </button>
              </li>
            ))}
          </ul>
        )}
      </div>
    </div>,
    document.body,
  );
}
