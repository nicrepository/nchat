/**
 * FavoritesPage — "Meus favoritos" (RF-06).
 *
 * Lists only the authenticated user's own favorites (server-enforced).
 * Cursor pagination via "Carregar mais" — the list is short-lived and small,
 * so no infinite scroll (ponytail: add IntersectionObserver if usage grows).
 * Deleted messages (RF-14) render the standard placeholder.
 *
 * The list is the confirmed identity's (#1124, CQ-09 — see useSessionOwner):
 * nothing is requested before an identity is confirmed, and while it is in
 * doubt the list is not on screen at all — unmounted, with every request it
 * made aborted and any answer still arriving dropped. Confirmed again, the
 * list loads anew for that identity, so no favourite of one user is ever
 * carried to the next, nor across the doubt.
 */

import { useCallback, useEffect, useRef, useState } from "react";
import { Link } from "react-router";

import { fetchFavorites, unfavoriteMessage } from "./chatApi";
import type { FavoriteItem } from "./chatTypes";
import RichTextRenderer from "./RichTextRenderer";
import SessionPending from "./SessionPending";
import { useSessionOwner } from "./useSessionOwner";

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
  const session = useSessionOwner();
  return (
    <div className="chat-favorites" data-testid="chat-favorites">
      <header className="chat-favorites__header">
        <h1 className="chat-favorites__title">Meus favoritos</h1>
        <p className="chat-favorites__sub">Visíveis apenas para você.</p>
      </header>
      {session.settled ? (
        <FavoritesList key={session.owner} />
      ) : (
        <SessionPending session={session} className="chat-favorites__status" />
      )}
    </div>
  );
}

function FavoritesList() {
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
      (page) => {
        // An answer that outlived its request — superseded, or the list gone
        // with the identity it was loaded for — is not shown.
        if (ctrl.signal.aborted) return;
        setState({
          status: "ready",
          items: page.favorites,
          nextCursor: page.nextCursor,
          loadingMore: false,
          actionError: null,
        });
      },
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
      // A page belongs to the list it continues: reloading or unmounting the
      // list aborts it, and an answer arriving after that is dropped.
      const signal = abortRef.current?.signal;
      fetchFavorites(current.nextCursor, signal).then(
        (page) => {
          if (signal?.aborted) return;
          setState((s) => ({
            ...s,
            items: [...s.items, ...page.favorites],
            nextCursor: page.nextCursor,
            loadingMore: false,
          }));
        },
        () => {
          if (signal?.aborted) return;
          setState((s) => ({
            ...s,
            loadingMore: false,
            actionError: "Não foi possível carregar mais favoritos.",
          }));
        },
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
    <>
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

      {state.status === "ready" && (
        <FavoritesReady state={state} onLoadMore={loadMore} onRemove={removeFavorite} />
      )}

      {state.actionError && (
        <div className="chat-favorites__status" role="alert">
          {state.actionError}
        </div>
      )}
    </>
  );
}

/** The list once loaded: its items, or that there are none, and the next page. */
function FavoritesReady({
  state,
  onLoadMore,
  onRemove,
}: {
  state: FavoritesState;
  onLoadMore: () => void;
  onRemove: (messageId: string) => void;
}) {
  return (
    <>
      {state.items.length === 0 ? (
        <div className="chat-favorites__status" data-testid="chat-favorites-empty">
          Nenhuma mensagem favoritada ainda. Use a estrela no menu da mensagem para salvar.
        </div>
      ) : (
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
                    onClick={() => onRemove(item.message.id)}
                  >
                    Remover
                  </button>
                </div>
              </li>
            );
          })}
        </ul>
      )}
      {state.nextCursor && (
        <button
          type="button"
          className="chat-favorites__load-more"
          onClick={onLoadMore}
          disabled={state.loadingMore}
        >
          {state.loadingMore ? "Carregando…" : "Carregar mais"}
        </button>
      )}
    </>
  );
}
