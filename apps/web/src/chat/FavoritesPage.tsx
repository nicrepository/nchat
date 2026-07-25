/**
 * FavoritesPage — "Meus favoritos" (RF-06).
 *
 * Lists only the authenticated user's own favorites (server-enforced).
 * Cursor pagination via "Carregar mais" — the list is short-lived and small,
 * so no infinite scroll (ponytail: add IntersectionObserver if usage grows).
 * Deleted messages (RF-14) render the standard placeholder.
 */

import { useCallback, useEffect, useRef, useState } from "react";
import { Link } from "react-router";

import { fetchFavorites, unfavoriteMessage } from "./chatApi";
import type { FavoriteItem } from "./chatTypes";
import RichTextRenderer from "./RichTextRenderer";

type Status = "loading" | "ready" | "error";

interface FavoritesState {
  status: Status;
  items: FavoriteItem[];
  nextCursor: string;
  loadingMore: boolean;
  actionError: string | null;
}

function sourceLink(item: FavoriteItem): { to: string; label: string } | null {
  if (item.channelId) {
    return { to: `/chat/channel/${encodeURIComponent(item.channelId)}`, label: "Ver no canal" };
  }
  if (item.dmConversationId) {
    return {
      to: `/chat/dm/${encodeURIComponent(item.dmConversationId)}`,
      label: "Ver na conversa",
    };
  }
  return null;
}

function formatDateTime(iso: string): string {
  const d = new Date(iso);
  return Number.isNaN(d.getTime())
    ? ""
    : d.toLocaleString("pt-BR", { dateStyle: "short", timeStyle: "short" });
}

export default function FavoritesPage() {
  const [state, setState] = useState<FavoritesState>({
    status: "loading",
    items: [],
    nextCursor: "",
    loadingMore: false,
    actionError: null,
  });
  const abortRef = useRef<AbortController | null>(null);

  // No synchronous setState here: initial state is already "loading" and the
  // retry path resets status before calling load (react-hooks/set-state-in-effect).
  const load = useCallback(() => {
    abortRef.current?.abort();
    const ctrl = new AbortController();
    abortRef.current = ctrl;
    fetchFavorites(undefined, ctrl.signal).then(
      (page) =>
        setState({
          status: "ready",
          items: page.favorites,
          nextCursor: page.nextCursor,
          loadingMore: false,
          actionError: null,
        }),
      () => {
        if (!ctrl.signal.aborted) setState((s) => ({ ...s, status: "error" }));
      },
    );
  }, []);

  useEffect(() => {
    load();
    return () => abortRef.current?.abort();
  }, [load]);

  const retry = useCallback(() => {
    setState((s) => ({ ...s, status: "loading", actionError: null }));
    load();
  }, [load]);

  const loadMore = useCallback(() => {
    setState((current) => {
      if (current.loadingMore || !current.nextCursor) return current;
      fetchFavorites(current.nextCursor).then(
        (page) =>
          setState((s) => ({
            ...s,
            items: [...s.items, ...page.favorites],
            nextCursor: page.nextCursor,
            loadingMore: false,
          })),
        () =>
          setState((s) => ({
            ...s,
            loadingMore: false,
            actionError: "Não foi possível carregar mais favoritos.",
          })),
      );
      return { ...current, loadingMore: true, actionError: null };
    });
  }, []);

  const removeFavorite = useCallback((messageId: string) => {
    unfavoriteMessage(messageId).then(
      () =>
        setState((s) => ({
          ...s,
          items: s.items.filter((item) => item.message.id !== messageId),
        })),
      () =>
        setState((s) => ({
          ...s,
          actionError: "Não foi possível remover o favorito. Tente novamente.",
        })),
    );
  }, []);

  return (
    <div className="chat-favorites" data-testid="chat-favorites">
      <header className="chat-favorites__header">
        <h1 className="chat-favorites__title">Meus favoritos</h1>
        <p className="chat-favorites__sub">Visíveis apenas para você.</p>
      </header>

      {state.status === "loading" && (
        <div className="chat-favorites__status" role="status">
          Carregando favoritos…
        </div>
      )}

      {state.status === "error" && (
        <div className="chat-favorites__status" role="alert">
          Não foi possível carregar seus favoritos.{" "}
          <button type="button" className="chat-favorites__link-btn" onClick={retry}>
            Tentar novamente
          </button>
        </div>
      )}

      {state.status === "ready" && state.items.length === 0 && (
        <div className="chat-favorites__status" data-testid="chat-favorites-empty">
          Nenhuma mensagem favoritada ainda. Use a estrela no menu da mensagem para salvar.
        </div>
      )}

      {state.status === "ready" && state.items.length > 0 && (
        <ul className="chat-favorites__list" aria-label="Mensagens favoritas">
          {state.items.map((item) => {
            const link = sourceLink(item);
            return (
              <li
                key={item.message.id}
                className="chat-favorites__item"
                data-testid="favorite-item"
              >
                <div className="chat-favorites__item-meta">
                  <span className="chat-favorites__sender">
                    {item.message.senderDisplayName || item.message.senderEmail || "Usuário"}
                  </span>
                  <span className="chat-favorites__time">
                    {formatDateTime(item.message.createdAt)}
                  </span>
                </div>
                <div className="chat-favorites__body">
                  {item.message.isRemoved ? (
                    <em>Mensagem removida.</em>
                  ) : (
                    <RichTextRenderer
                      text={item.message.bodyText}
                      bodyFormat={item.message.bodyFormat}
                    />
                  )}
                </div>
                <div className="chat-favorites__actions">
                  {link && (
                    <Link to={link.to} className="chat-favorites__link-btn">
                      {link.label}
                    </Link>
                  )}
                  <button
                    type="button"
                    className="chat-favorites__link-btn"
                    aria-label="Remover dos favoritos"
                    onClick={() => removeFavorite(item.message.id)}
                  >
                    Remover
                  </button>
                </div>
              </li>
            );
          })}
        </ul>
      )}

      {state.status === "ready" && state.nextCursor && (
        <button
          type="button"
          className="chat-favorites__load-more"
          onClick={loadMore}
          disabled={state.loadingMore}
        >
          {state.loadingMore ? "Carregando…" : "Carregar mais"}
        </button>
      )}

      {state.actionError && (
        <div className="chat-favorites__status" role="alert">
          {state.actionError}
        </div>
      )}
    </div>
  );
}
