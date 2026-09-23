import { useEffect, useRef, type KeyboardEvent } from "react";
import { createPortal } from "react-dom";

import "./KeyboardShortcutsDialog.css";

interface ShortcutReference {
  keys: readonly string[];
  macKeys?: readonly string[];
  description: string;
}

interface ShortcutGroup {
  label: string;
  shortcuts: readonly ShortcutReference[];
}

const shortcutGroups: readonly ShortcutGroup[] = [
  {
    label: "Ações globais",
    shortcuts: [
      { keys: ["Ctrl", "K"], macKeys: ["⌘", "K"], description: "Abrir busca global" },
      { keys: ["Ctrl", "/"], macKeys: ["⌘", "/"], description: "Mostrar atalhos" },
    ],
  },
  {
    label: "Navegação sequencial na sidebar",
    shortcuts: [
      { keys: ["Alt", "↑"], description: "Conversa anterior" },
      { keys: ["Alt", "↓"], description: "Próxima conversa" },
    ],
  },
  {
    label: "Histórico de conversas",
    shortcuts: [
      { keys: ["Alt", "←"], description: "Voltar para a conversa visitada" },
      { keys: ["Alt", "→"], description: "Avançar para a conversa visitada" },
    ],
  },
];

export default function KeyboardShortcutsDialog({ onClose }: Readonly<{ onClose: () => void }>) {
  const closeButtonRef = useRef<HTMLButtonElement>(null);
  const openerRef = useRef<HTMLElement | null>(null);

  useEffect(() => {
    openerRef.current =
      document.activeElement instanceof HTMLElement ? document.activeElement : null;
    closeButtonRef.current?.focus();

    return () => {
      if (openerRef.current?.isConnected) openerRef.current.focus();
    };
  }, []);

  function onKeyDown(event: KeyboardEvent<HTMLDivElement>) {
    if (event.key !== "Escape") return;
    event.preventDefault();
    onClose();
  }

  return createPortal(
    <div className="keyboard-shortcuts__backdrop" onMouseDown={onClose}>
      <div
        className="keyboard-shortcuts"
        role="dialog"
        aria-modal="true"
        aria-labelledby="keyboard-shortcuts-title"
        onKeyDownCapture={onKeyDown}
        onMouseDown={(event) => event.stopPropagation()}
      >
        <header className="keyboard-shortcuts__header">
          <div>
            <span className="keyboard-shortcuts__eyebrow">Referência rápida</span>
            <h2 id="keyboard-shortcuts-title">Atalhos de teclado</h2>
          </div>
          <button ref={closeButtonRef} type="button" aria-label="Fechar atalhos" onClick={onClose}>
            ×
          </button>
        </header>
        <p>Use Ctrl no Windows/Linux ou ⌘ no macOS.</p>
        <div className="keyboard-shortcuts__groups">
          {shortcutGroups.map(({ label, shortcuts }) => (
            <section key={label} className="keyboard-shortcuts__group" aria-label={label}>
              <h3>{label}</h3>
              <dl>
                {shortcuts.map(({ keys, macKeys, description }) => (
                  <div key={description}>
                    <dd>{description}</dd>
                    <dt>
                      <KeySequence keys={keys} />
                      {macKeys && (
                        <>
                          <span className="keyboard-shortcuts__alternative">ou</span>
                          <KeySequence keys={macKeys} />
                        </>
                      )}
                    </dt>
                  </div>
                ))}
              </dl>
            </section>
          ))}
        </div>
      </div>
    </div>,
    document.body,
  );
}

function KeySequence({ keys }: Readonly<{ keys: readonly string[] }>) {
  return (
    <span className="keyboard-shortcuts__keys">
      {keys.map((key, index) => (
        <span key={key}>
          {index > 0 && <span className="keyboard-shortcuts__plus">+</span>}
          <kbd>{key}</kbd>
        </span>
      ))}
    </span>
  );
}
