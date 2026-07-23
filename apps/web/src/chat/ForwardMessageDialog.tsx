import { useEffect, useMemo, useRef, useState } from "react";
import { createPortal } from "react-dom";

import { forwardChannelMessage } from "./chatApi";
import type { Channel } from "./chatTypes";

export interface ForwardSourceContext {
  messageID: string;
  sourceChannelID: string;
}

interface ForwardMessageDialogProps {
  source: ForwardSourceContext;
  channels: Channel[];
  onClose: () => void;
  onSuccess: () => void;
}

function isAbortError(error: unknown): boolean {
  return error instanceof DOMException && error.name === "AbortError";
}

function getEligibleChannels(channels: Channel[], sourceChannelID: string): Channel[] {
  return channels.filter((channel) => channel.id !== sourceChannelID && channel.canWrite);
}

export default function ForwardMessageDialog({
  source,
  channels,
  onClose,
  onSuccess,
}: ForwardMessageDialogProps) {
  const dialogRef = useRef<HTMLDivElement>(null);
  const searchRef = useRef<HTMLInputElement>(null);
  const controllerRef = useRef<AbortController | null>(null);
  const submittingRef = useRef(false);
  const mountedRef = useRef(false);
  const onCloseRef = useRef(onClose);
  const onSuccessRef = useRef(onSuccess);
  const idempotencyKeyRef = useRef("");
  const [query, setQuery] = useState("");
  const [selectedId, setSelectedId] = useState("");
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState("");
  const eligibleChannels = useMemo(
    () => getEligibleChannels(channels, source.sourceChannelID),
    [channels, source.sourceChannelID],
  );
  const availableChannels = useMemo(() => {
    const normalizedQuery = query.trim().toLocaleLowerCase("pt-BR");
    return eligibleChannels.filter(
      (channel) =>
        !normalizedQuery || channel.name.toLocaleLowerCase("pt-BR").includes(normalizedQuery),
    );
  }, [eligibleChannels, query]);
  const selectedChannel = availableChannels.find((channel) => channel.id === selectedId);
  const canSubmit = selectedChannel !== undefined && !submitting;
  const emptyMessage =
    eligibleChannels.length === 0
      ? "Não há outros canais disponíveis para encaminhamento."
      : "Nenhum canal encontrado para a busca.";

  useEffect(() => {
    onCloseRef.current = onClose;
    onSuccessRef.current = onSuccess;
  }, [onClose, onSuccess]);

  useEffect(() => {
    mountedRef.current = true;
    const previouslyFocused = document.activeElement as HTMLElement | null;
    searchRef.current?.focus();
    const handleKeyDown = (event: KeyboardEvent) => {
      if (event.key === "Escape") {
        event.preventDefault();
        controllerRef.current?.abort();
        onCloseRef.current();
        return;
      }
      if (event.key !== "Tab") return;
      const focusable = dialogRef.current?.querySelectorAll<HTMLElement>(
        "input:not([disabled]), button:not([disabled])",
      );
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
    };
    document.addEventListener("keydown", handleKeyDown);
    return () => {
      mountedRef.current = false;
      document.removeEventListener("keydown", handleKeyDown);
      controllerRef.current?.abort();
      previouslyFocused?.focus();
    };
  }, []);

  const cancel = () => {
    controllerRef.current?.abort();
    onCloseRef.current();
  };

  const submit = async () => {
    if (!selectedChannel || submittingRef.current) return;
    if (!idempotencyKeyRef.current) idempotencyKeyRef.current = crypto.randomUUID();
    submittingRef.current = true;
    setSubmitting(true);
    setError("");
    const controller = new AbortController();
    controllerRef.current = controller;
    try {
      await forwardChannelMessage(
        selectedChannel.id,
        source.messageID,
        idempotencyKeyRef.current,
        controller.signal,
      );
      if (!controller.signal.aborted && mountedRef.current) onSuccessRef.current();
    } catch (requestError) {
      if (!controller.signal.aborted && !isAbortError(requestError) && mountedRef.current) {
        setError("Não foi possível encaminhar a mensagem. Tente novamente.");
      }
    } finally {
      submittingRef.current = false;
      if (controllerRef.current === controller) {
        controllerRef.current = null;
      }
      if (mountedRef.current) {
        setSubmitting(false);
      }
    }
  };

  return createPortal(
    <div className="chat-forward-dialog__backdrop" onMouseDown={cancel}>
      <div
        ref={dialogRef}
        className="chat-forward-dialog"
        role="dialog"
        aria-modal="true"
        aria-labelledby="chat-forward-dialog-title"
        onMouseDown={(event) => event.stopPropagation()}
      >
        <header>
          <div>
            <h2 id="chat-forward-dialog-title">Encaminhar mensagem</h2>
            <p>Escolha o canal de destino.</p>
          </div>
          <button type="button" aria-label="Fechar" onClick={cancel}>
            <span className="material-symbols-outlined" aria-hidden="true">
              close
            </span>
          </button>
        </header>
        <div className="chat-forward-dialog__content">
          <label htmlFor="chat-forward-search">Buscar canal</label>
          <input
            ref={searchRef}
            id="chat-forward-search"
            type="search"
            value={query}
            disabled={submitting}
            onChange={(event) => setQuery(event.target.value)}
            placeholder="Digite o nome do canal"
          />
          {availableChannels.length === 0 ? (
            <p className="chat-forward-dialog__empty" role="status">
              {emptyMessage}
            </p>
          ) : (
            <ul aria-label="Canais disponíveis">
              {availableChannels.map((channel) => (
                <li key={channel.id}>
                  <button
                    type="button"
                    aria-pressed={selectedId === channel.id}
                    className={selectedId === channel.id ? "chat-forward-dialog__selected" : ""}
                    disabled={submitting}
                    onClick={() => setSelectedId(channel.id)}
                  >
                    <span className="material-symbols-outlined" aria-hidden="true">
                      tag
                    </span>
                    {channel.name}
                  </button>
                </li>
              ))}
            </ul>
          )}
          {error && (
            <p className="chat-forward-dialog__error" role="alert">
              {error}
            </p>
          )}
        </div>
        <footer>
          <button type="button" className="chat-forward-dialog__cancel" onClick={cancel}>
            Cancelar
          </button>
          <button
            type="button"
            className="chat-forward-dialog__confirm"
            disabled={!canSubmit}
            aria-busy={submitting}
            onClick={() => void submit()}
          >
            {submitting ? "Encaminhando…" : "Encaminhar"}
          </button>
        </footer>
      </div>
    </div>,
    document.body,
  );
}
