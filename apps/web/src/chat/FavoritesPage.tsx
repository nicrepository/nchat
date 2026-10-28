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
 * doubt no data is shown and requests are aborted. Only the loaded page count
 * survives: confirmation reloads at most two authorized pages; deeper reading
 * is recovered only in bounded batches requested by the reader.
 */

import { useCallback, useEffect, useRef, useState, type RefObject } from "react";
import { Link, useOutletContext } from "react-router";

import { fetchFavorites, unfavoriteMessage } from "./chatApi";
import type { FavoriteItem } from "./chatTypes";
import RichTextRenderer from "./RichTextRenderer";
import SessionPending from "./SessionPending";
import { useSessionOwner, type SessionOwner } from "./useSessionOwner";
import type { ChatOutletContext } from "./ChatShell";
import { BackButton } from "./message-area/ConversationHeader";
import { useSessionPresentation } from "./useSessionPresentation";
import PaginationRecovery from "./PaginationRecovery";
import "./ChatMessageArea.css";

type Status = "loading" | "ready" | "error";

interface FavoritesState {
  revalidationsLeft: number;
  pages: number;
  restorePages: number;
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

/** One bounded read, shared by automatic rebuilding and explicit pagination. */
async function readFavoritePages(cursor: string | undefined, depth: number, signal: AbortSignal) {
  const items = new Map<string, FavoriteItem>();
  const seen = new Set<string>(cursor ? [cursor] : []);
  let loaded = 0;
  do {
    const page = await fetchFavorites(cursor, signal);
    if (signal.aborted) return;
    for (const item of page.favorites) items.set(item.message.id, item);
    cursor = page.nextCursor;
    loaded += 1;
    if (!cursor || seen.has(cursor)) { cursor = ""; break; }
    seen.add(cursor);
  } while (loaded < depth);
  return { favorites: [...items.values()], nextCursor: cursor ?? "", loaded };
}

export default function FavoritesPage() {
  const session = useSessionOwner();
  const goBack = useOutletContext<ChatOutletContext | null>()?.goBack;
  const rootRef = useRef<HTMLDivElement>(null);
  return (
    <div ref={rootRef} tabIndex={-1} className="chat-favorites" data-testid="chat-favorites">
      <header className="chat-favorites__header">
        {goBack && <BackButton onBack={goBack} />}
        <div>
          <h1 className="chat-favorites__title">Meus favoritos</h1>
          <p className="chat-favorites__sub">Visíveis apenas para você.</p>
        </div>
      </header>
      {session.owner !== null ? (
        <FavoritesList
          key={`${session.sessionKey}:${session.owner}`}
          session={session}
          rootRef={rootRef}
        />
      ) : (
        <SessionPending session={session} className="chat-favorites__status" />
      )}
    </div>
  );
}

function FavoritesList({ session, rootRef }: {
  session: SessionOwner;
  rootRef: RefObject<HTMLDivElement | null>;
}) {
  const [state, setState] = useState<FavoritesState>({
    revalidationsLeft: 1,
    pages: 0,
    restorePages: 0,
    status: "loading",
    items: [],
    nextCursor: "",
    loadingMore: false,
    actionError: null,
  });
  const abortRef = useRef<AbortController | null>(null);
  // Desired depth, including the unfinished recovery; never a cursor or data cache.
  const pagesRef = useRef(0);
  const pagingRef = useRef(false);
  const mountedRef = useRef(false);
  const removedRef = useRef(new Set<string>());
  useEffect(() => {
    mountedRef.current = true;
    return () => { mountedRef.current = false; };
  }, []);
  const [suspended, setSuspended] = useState(!session.settled);
  const cancelRecovery = useCallback(() => {
    pagesRef.current = state.status === "ready" ? state.pages : 0;
    if (state.loadingMore && state.restorePages > state.pages) {
      abortRef.current?.abort();
      abortRef.current = new AbortController();
      pagingRef.current = false;
    }
    setState((s) => ({ ...s, restorePages: 0, loadingMore: false }));
  }, [state.pages, state.loadingMore, state.restorePages, state.status]);
  const abandonReading = useSessionPresentation(
    rootRef, session.settled && state.status === "ready", !session.settled,
    {
      pending: state.restorePages > state.pages && !!state.nextCursor,
      unavailable: state.restorePages > state.pages && !state.nextCursor,
      onAbandon: cancelRecovery,
    },
  );
  if (suspended === session.settled) {
    setSuspended(!session.settled);
    if (!session.settled) {
      // A rotation can abort the terminal 401 before its catch runs. Keep the
      // budget across reconstructions, rather than treating each as a new load.
      setState((s) => ({
        ...s,
        status: s.revalidationsLeft > 0 ? "loading" : "error",
        revalidationsLeft: Math.max(0, s.revalidationsLeft - 1),
        items: [], nextCursor: "", loadingMore: false,
      }));
    }
  }

  // No synchronous setState here: initial state is already "loading" and the
  // retry path resets status before calling load (react-hooks/set-state-in-effect).
  const load = useCallback(() => {
    abortRef.current?.abort();
    const ctrl = new AbortController();
    abortRef.current = ctrl;
    pagingRef.current = false;
    // Only reconcile removals completing after this snapshot was requested.
    removedRef.current.clear();
    const goal = pagesRef.current;
    readFavoritePages(undefined, Math.min(2, Math.max(1, goal)), ctrl.signal).then(
      (page) => {
        // An answer that outlived its request — superseded, or the list gone
        // with the identity it was loaded for — is not shown.
        if (ctrl.signal.aborted || !page) return;
        const desired = Math.min(goal, pagesRef.current);
        pagesRef.current = page.nextCursor ? Math.max(desired, page.loaded) : page.loaded;
        setState({
          revalidationsLeft: 1,
          pages: page.loaded,
          restorePages: desired > 2 || desired > page.loaded ? desired : 0,
          status: "ready",
          items: page.favorites.filter((item) => !removedRef.current.has(item.message.id)),
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

  const canLoad = state.status !== "error";
  useEffect(() => {
    if (session.settled && canLoad) load();
    return () => abortRef.current?.abort();
  }, [load, session.settled, canLoad]);

  const retry = useCallback(() => {
    setState((s) => ({ ...s, revalidationsLeft: 1, status: "loading", actionError: null }));
  }, []);

  const loadMore = useCallback((recover = false) => {
    if (pagingRef.current || state.status !== "ready" || !state.nextCursor) return;
    // A page belongs to the list it continues: reloading or unmounting the
    // list aborts it, and an answer arriving after that is dropped.
    const signal = abortRef.current?.signal;
    if (!signal || signal.aborted) return;
    const depth = recover ? Math.min(5, state.restorePages - state.pages) : 1;
    if (depth <= 0) return;
    if (!recover) abandonReading();
    pagingRef.current = true;
    removedRef.current.clear();
    setState((current) => ({ ...current, revalidationsLeft: 1, loadingMore: true, actionError: null }));
    readFavoritePages(state.nextCursor, depth, signal).then(
      (page) => {
        if (signal.aborted || !page) return;
        pagingRef.current = false;
        pagesRef.current = page.nextCursor
          ? Math.max(pagesRef.current, state.pages + page.loaded)
          : state.pages + page.loaded;
        setState((s) => ({
          ...s,
          items: [...new Map(
            [...s.items, ...page.favorites]
              .filter((item) => !removedRef.current.has(item.message.id))
              .map((item) => [item.message.id, item]),
          ).values()],
          nextCursor: page.nextCursor,
          pages: s.pages + page.loaded,
          loadingMore: false,
        }));
      },
      () => {
        if (signal.aborted) return;
        pagingRef.current = false;
        setState((s) => ({
          ...s,
          loadingMore: false,
          actionError: "Não foi possível carregar mais favoritos.",
        }));
      },
    );
  }, [state.status, state.nextCursor, state.pages, state.restorePages, abandonReading]);

  const removeFavorite = useCallback((messageId: string) => {
    unfavoriteMessage(messageId).then(
      () => {
        if (!mountedRef.current) return;
        removedRef.current.add(messageId);
        setState((s) => ({
          ...s,
          items: s.items.filter((item) => item.message.id !== messageId),
        }));
      },
      () => {
        if (!mountedRef.current) return;
        setState((s) => ({
          ...s,
          actionError: "Não foi possível remover o favorito. Tente novamente.",
        }));
      },
    );
  }, []);

  if (!session.settled) return <SessionPending session={session} className="chat-favorites__status" />;

  return (
    <>
      {state.status === "ready" && state.restorePages > 0 && <PaginationRecovery
        remaining={Math.max(0, state.restorePages - state.pages)}
        hasMore={!!state.nextCursor}
        busy={state.loadingMore}
        onRecover={() => loadMore(true)}
        onCancel={abandonReading}
      />}
      {state.status === "loading" && (
        <div className="chat-favorites__status" role="status">
          Carregando favoritos…
        </div>
      )}

      {state.status === "error" && (
        <div className="chat-favorites__status" role="alert">
          Não foi possível carregar seus favoritos.{" "}
          <button type="button" data-reading-recovery="true" className="chat-favorites__link-btn" onClick={retry}>
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
          onClick={() => loadMore()}
          disabled={state.loadingMore}
          data-session-focus="favorites-more"
        >
          {state.loadingMore ? "Carregando…" : "Carregar mais"}
        </button>
      )}

      {state.actionError && (
        <div className="chat-favorites__status" role="alert">
          {state.actionError}
        </div>
      )}
    </>
  );
}
