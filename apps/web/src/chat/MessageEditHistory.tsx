import { useCallback, useEffect, useState } from "react";
import { createPortal } from "react-dom";

import { getMessageHistory } from "./chatApi";
import { normalizeBodyFormat, type MessageEditHistoryEntry } from "./chatTypes";
import RichTextRenderer from "./RichTextRenderer";

interface MessageEditHistoryProps {
  messageId: string;
  onClose: () => void;
}

export default function MessageEditHistory({ messageId, onClose }: MessageEditHistoryProps) {
  const [entries, setEntries] = useState<MessageEditHistoryEntry[]>([]);
  const [cursor, setCursor] = useState<string>();
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState(false);

  const load = useCallback(
    async (nextCursor?: string) => {
      setLoading(true);
      setError(false);
      try {
        const page = await getMessageHistory(messageId, { cursor: nextCursor, limit: 50 });
        setEntries((current) => (nextCursor ? [...current, ...page.entries] : page.entries));
        setCursor(page.nextCursor);
      } catch {
        setError(true);
      } finally {
        setLoading(false);
      }
    },
    [messageId],
  );

  useEffect(() => {
    let active = true;
    void getMessageHistory(messageId, { cursor: undefined, limit: 50 }).then(
      (page) => {
        if (!active) return;
        setEntries(page.entries);
        setCursor(page.nextCursor);
        setLoading(false);
      },
      () => {
        if (!active) return;
        setError(true);
        setLoading(false);
      },
    );
    return () => {
      active = false;
    };
  }, [messageId]);

  useEffect(() => {
    const closeOnEscape = (event: KeyboardEvent) => {
      if (event.key === "Escape") onClose();
    };
    document.addEventListener("keydown", closeOnEscape);
    return () => document.removeEventListener("keydown", closeOnEscape);
  }, [onClose]);

  return createPortal(
    <div className="message-history__backdrop" onMouseDown={onClose}>
      <section
        className="message-history"
        role="dialog"
        aria-modal="true"
        aria-labelledby="message-history-title"
        onMouseDown={(event) => event.stopPropagation()}
      >
        <header className="message-history__header">
          <div>
            <h2 id="message-history-title">Histórico de edições</h2>
            <p>Versões anteriores, da mais recente para a mais antiga.</p>
          </div>
          <button type="button" aria-label="Fechar histórico" onClick={onClose}>
            <span className="material-symbols-outlined" aria-hidden="true">
              close
            </span>
          </button>
        </header>

        <div className="message-history__content">
          {entries.map((entry, index) => (
            <article className="message-history__entry" key={`${entry.versionedAt}-${index}`}>
              <time dateTime={entry.versionedAt}>
                {new Date(entry.versionedAt).toLocaleString("pt-BR")}
              </time>
              <div className="message-history__body">
                <RichTextRenderer
                  text={entry.body}
                  bodyFormat={normalizeBodyFormat(`v${entry.bodyFormat}`)}
                />
              </div>
            </article>
          ))}
          {!loading && !error && entries.length === 0 && (
            <p className="message-history__status">Nenhuma edição anterior.</p>
          )}
          {error && (
            <div className="message-history__status" role="alert">
              Não foi possível carregar o histórico.
              <button type="button" onClick={() => void load(cursor)}>
                Tentar novamente
              </button>
            </div>
          )}
          {loading && <p className="message-history__status">Carregando histórico…</p>}
        </div>

        {cursor && !error && (
          <button
            className="message-history__load-more"
            type="button"
            disabled={loading}
            onClick={() => void load(cursor)}
          >
            Carregar mais
          </button>
        )}
      </section>
    </div>,
    document.body,
  );
}
