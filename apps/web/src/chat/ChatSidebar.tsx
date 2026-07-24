import { useEffect, useRef, useState } from "react";
import { Link, useLocation, useNavigate } from "react-router-dom";

import "./ChatSidebar.css";
import type { Channel, CurrentUser, DMConversation } from "./chatTypes";
import { avatarColorFor, initialsFrom } from "./messageDisplay";
import NewDirectMessageDialog from "./NewDirectMessageDialog";

/**
 * Placeholder user shown in the sidebar footer.
 * Replace with a real profile API call once GET /api/auth/me is available.
 */
const PLACEHOLDER_USER: CurrentUser = {
  displayName: "Usuário",
  initials: "?",
  color: "purple",
  role: "",
};

// ── Inline SVG icons ─────────────────────────────────────────────────────────

function IconHash() {
  return (
    <svg
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth="2"
      strokeLinecap="round"
      strokeLinejoin="round"
      className="chat-sidebar__icon"
      aria-hidden="true"
    >
      <line x1="10" y1="4" x2="8" y2="20" />
      <line x1="16" y1="4" x2="14" y2="20" />
      <line x1="4" y1="9" x2="20" y2="9" />
      <line x1="3" y1="15" x2="19" y2="15" />
    </svg>
  );
}

function IconLock() {
  return (
    <svg
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth="2"
      strokeLinecap="round"
      strokeLinejoin="round"
      className="chat-sidebar__icon chat-sidebar__icon--sm"
      aria-hidden="true"
    >
      <rect x="3" y="11" width="18" height="11" rx="2" ry="2" />
      <path d="M7 11V7a5 5 0 0 1 10 0v4" />
    </svg>
  );
}

function IconAdd() {
  return (
    <svg
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth="2"
      strokeLinecap="round"
      strokeLinejoin="round"
      className="chat-sidebar__add-icon"
      aria-hidden="true"
    >
      <line x1="12" y1="5" x2="12" y2="19" />
      <line x1="5" y1="12" x2="19" y2="12" />
    </svg>
  );
}

function IconSettings() {
  return (
    <svg
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth="2"
      strokeLinecap="round"
      strokeLinejoin="round"
      className="chat-sidebar__icon"
      aria-hidden="true"
    >
      <circle cx="12" cy="12" r="3" />
      <path d="M19.4 15a1.65 1.65 0 0 0 .33 1.82l.06.06a2 2 0 0 1-2.83 2.83l-.06-.06a1.65 1.65 0 0 0-1.82-.33 1.65 1.65 0 0 0-1 1.51V21a2 2 0 0 1-4 0v-.09A1.65 1.65 0 0 0 9 19.4a1.65 1.65 0 0 0-1.82.33l-.06.06a2 2 0 0 1-2.83-2.83l.06-.06A1.65 1.65 0 0 0 4.68 15a1.65 1.65 0 0 0-1.51-1H3a2 2 0 0 1 0-4h.09A1.65 1.65 0 0 0 4.6 9a1.65 1.65 0 0 0-.33-1.82l-.06-.06a2 2 0 0 1 2.83-2.83l.06.06A1.65 1.65 0 0 0 9 4.68a1.65 1.65 0 0 0 1-1.51V3a2 2 0 0 1 4 0v.09a1.65 1.65 0 0 0 1 1.51 1.65 1.65 0 0 0 1.82-.33l.06-.06a2 2 0 0 1 2.83 2.83l-.06.06A1.65 1.65 0 0 0 19.4 9a1.65 1.65 0 0 0 1.51 1H21a2 2 0 0 1 0 4h-.09a1.65 1.65 0 0 0-1.51 1z" />
    </svg>
  );
}

function IconStar() {
  return (
    <svg
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth="2"
      strokeLinecap="round"
      strokeLinejoin="round"
      className="chat-sidebar__icon"
      aria-hidden="true"
    >
      <polygon points="12 2 15.09 8.26 22 9.27 17 14.14 18.18 21.02 12 17.77 5.82 21.02 7 14.14 2 9.27 8.91 8.26 12 2" />
    </svg>
  );
}

// ── Avatar helpers ────────────────────────────────────────────────────────────

interface AvatarProps {
  initials: string;
  /** Optional picture. Initials are shown when absent or when loading fails. */
  src?: string;
  color?: string;
  status?: "online" | "away" | "offline";
  size?: "sm" | "md";
}

function Avatar({ initials, src, color = "purple", status, size = "sm" }: AvatarProps) {
  // A load failure is scoped to the URL that was current when it happened, so a
  // change of src must clear it — otherwise an A → B → A cycle would never retry
  // A. This uses React's "adjust state when a prop changes" pattern (reset during
  // render, guarded so it runs ONLY when src actually changes, never every
  // render); an effect would trip react-hooks/set-state-in-effect. An unchanged
  // src that keeps failing stays on the initials fallback.
  const [failedSrc, setFailedSrc] = useState<string | null>(null);
  const [trackedSrc, setTrackedSrc] = useState(src);
  if (src !== trackedSrc) {
    setTrackedSrc(src);
    setFailedSrc(null);
  }
  const showImage = Boolean(src) && failedSrc !== src;

  return (
    <span
      className={`chat-sidebar__avatar chat-sidebar__avatar--${color} chat-sidebar__avatar--${size}`}
      aria-hidden="true"
    >
      {showImage ? (
        <img
          className="chat-sidebar__avatar-img"
          src={src}
          alt=""
          referrerPolicy="no-referrer"
          onError={() => setFailedSrc(src ?? null)}
        />
      ) : (
        initials
      )}
      {status && (
        <span className={`chat-sidebar__avatar-status chat-sidebar__avatar-status--${status}`} />
      )}
    </span>
  );
}

function GroupAvatars({ dm }: { dm: DMConversation }) {
  const first = dm.participants[0];
  const second = dm.participants[1];
  return (
    <span className="chat-sidebar__group-avatars" aria-hidden="true">
      {first && (
        <span
          className={`chat-sidebar__avatar chat-sidebar__avatar--${first.color} chat-sidebar__avatar--sm chat-sidebar__avatar--group-back`}
        >
          {first.initials}
        </span>
      )}
      {second && (
        <span
          className={`chat-sidebar__avatar chat-sidebar__avatar--${second.color} chat-sidebar__avatar--sm chat-sidebar__avatar--group-front`}
        >
          {second.initials}
        </span>
      )}
    </span>
  );
}

// ── Loading skeleton ──────────────────────────────────────────────────────────

function LoadingSkeleton() {
  return (
    <div className="chat-sidebar__skeleton" aria-label="Carregando sidebar" role="status">
      <div className="chat-sidebar__skeleton-label" />
      {[1, 2, 3].map((i) => (
        <div key={i} className="chat-sidebar__skeleton-item" />
      ))}
      <div className="chat-sidebar__skeleton-label chat-sidebar__skeleton-label--mt" />
      {[1, 2].map((i) => (
        <div key={i} className="chat-sidebar__skeleton-item" />
      ))}
    </div>
  );
}

// ── Error state ───────────────────────────────────────────────────────────────

interface ErrorStateProps {
  onRetry: () => void;
}

function ErrorState({ onRetry }: ErrorStateProps) {
  return (
    <div className="chat-sidebar__error" role="alert">
      <p className="chat-sidebar__error-msg">Não foi possível carregar os canais.</p>
      <button
        type="button"
        className="chat-sidebar__retry-btn"
        onClick={onRetry}
        aria-label="Tentar novamente"
      >
        Tentar novamente
      </button>
    </div>
  );
}

// ── Channel list ──────────────────────────────────────────────────────────────

interface ChannelListProps {
  channels: Channel[];
  activeChannelId: string | undefined;
  onSelect: (id: string) => void;
}

function ChannelList({ channels, activeChannelId, onSelect }: ChannelListProps) {
  if (channels.length === 0) {
    return (
      <p className="chat-sidebar__empty" role="status">
        Nenhum canal disponível.
      </p>
    );
  }

  return (
    <>
      {channels.map((ch) => {
        const isActive = ch.id === activeChannelId;
        return (
          <button
            key={ch.id}
            type="button"
            role="option"
            aria-selected={isActive}
            aria-label={`Canal ${ch.type === "private" ? "privado " : ""}${ch.name}`}
            className={`chat-sidebar__nav-item${isActive ? " chat-sidebar__nav-item--active" : ""}`}
            onClick={() => onSelect(ch.id)}
          >
            {ch.type === "private" ? <IconLock /> : <IconHash />}
            <span className="chat-sidebar__nav-item-name">{ch.name}</span>
            {ch.type === "private" && (
              <span className="chat-sidebar__badge chat-sidebar__badge--private sr-only">
                privado
              </span>
            )}
            {ch.unreadCount != null && ch.unreadCount > 0 && (
              <span
                className="chat-sidebar__unread-badge"
                aria-label={`${ch.unreadCount} não lidas`}
              >
                {ch.unreadCount}
              </span>
            )}
          </button>
        );
      })}
    </>
  );
}

// ── DM list ───────────────────────────────────────────────────────────────────

interface DMListProps {
  dms: DMConversation[];
  activeDMId: string | undefined;
  onSelect: (id: string) => void;
}

function DMList({ dms, activeDMId, onSelect }: DMListProps) {
  if (dms.length === 0) {
    return (
      <p className="chat-sidebar__empty" role="status">
        Nenhuma mensagem direta.
      </p>
    );
  }

  return (
    <>
      {dms.map((dm) => {
        const isActive = dm.id === activeDMId;
        const isGroup = dm.type === "group";
        const counterpart = dm.counterpart;

        return (
          <button
            key={dm.id}
            type="button"
            role="option"
            aria-selected={isActive}
            aria-label={isGroup ? `Grupo ${dm.name}` : `Mensagem direta com ${dm.name}`}
            className={`chat-sidebar__dm-item${isActive ? " chat-sidebar__dm-item--active" : ""}`}
            onClick={() => onSelect(dm.id)}
          >
            {/* The 1:1 avatar always renders — with a picture when there is
                one, with initials otherwise — so the row height never shifts
                depending on whether a counterpart has an avatar. */}
            {isGroup ? (
              <GroupAvatars dm={dm} />
            ) : (
              <Avatar
                initials={initialsFrom(counterpart?.displayName ?? dm.name)}
                src={counterpart?.avatarUrl}
                color={avatarColorFor(counterpart?.userId ?? dm.id)}
                size="sm"
              />
            )}
            <span className="chat-sidebar__dm-name">{dm.name}</span>
            {isGroup && (
              <span className="chat-sidebar__badge chat-sidebar__badge--group sr-only">grupo</span>
            )}
            {dm.unreadCount != null && dm.unreadCount > 0 && (
              <span
                className="chat-sidebar__unread-badge"
                aria-label={`${dm.unreadCount} não lidas`}
              >
                {dm.unreadCount}
              </span>
            )}
          </button>
        );
      })}
    </>
  );
}

// ── Safe URL decode ───────────────────────────────────────────────────────────

function safeDecodeURIComponent(segment: string): string {
  try {
    return decodeURIComponent(segment);
  } catch {
    // Malformed percent-encoding (e.g. a bare `%` or `%ZZ`) — return raw segment.
    return segment;
  }
}

// ── Main sidebar ──────────────────────────────────────────────────────────────

type SidebarState =
  | { status: "loading" }
  | { status: "error"; error: string }
  | { status: "ready"; currentUserId: string; channels: Channel[]; dms: DMConversation[] };

interface ChatSidebarProps {
  state: SidebarState;
  retry: () => void;
}

export default function ChatSidebar({ state, retry }: ChatSidebarProps) {
  const navigate = useNavigate();
  const location = useLocation();
  const [newDMOpen, setNewDMOpen] = useState(false);
  const newDMButtonRef = useRef<HTMLButtonElement>(null);
  const restoreNewDMFocusRef = useRef(false);

  useEffect(() => {
    if (!newDMOpen && state.status === "ready" && restoreNewDMFocusRef.current) {
      newDMButtonRef.current?.focus();
      restoreNewDMFocusRef.current = false;
    }
  }, [newDMOpen, state.status]);

  // Derive active item from pathname: /chat/channel/:id or /chat/dm/:id
  // decodeURIComponent handles IDs that were encoded with encodeURIComponent on navigate.
  const pathParts = location.pathname.split("/").filter(Boolean);
  // pathParts: ["chat", "channel"|"dm", encodedId]
  const activeType = pathParts[1] as "channel" | "dm" | undefined;
  const activeId = pathParts[2] ? safeDecodeURIComponent(pathParts[2]) : undefined;

  const activeChannelId = activeType === "channel" ? activeId : undefined;
  const activeDMId = activeType === "dm" ? activeId : undefined;

  function handleChannelSelect(id: string) {
    navigate(`/chat/channel/${encodeURIComponent(id)}`);
  }

  function handleDMSelect(id: string) {
    navigate(`/chat/dm/${encodeURIComponent(id)}`);
  }

  function closeNewDM() {
    restoreNewDMFocusRef.current = true;
    setNewDMOpen(false);
  }

  function handleDMOpened(id: string) {
    closeNewDM();
    navigate(`/chat/dm/${encodeURIComponent(id)}`);
    retry();
  }

  return (
    <aside
      className="chat-sidebar"
      aria-label="Navegação do workspace NIC Chat"
      data-testid="chat-sidebar"
    >
      {/* ── Brand ── */}
      <Link to="/chat" className="chat-sidebar__brand" aria-label="NIC Chat — Workspace NIC-Labs">
        <div className="chat-sidebar__brand-mark">
          <img src="/assets/nic-labs-icon.png" alt="NIC-Labs" className="chat-sidebar__brand-img" />
        </div>
        <div>
          <p className="chat-sidebar__brand-title">NIC Chat</p>
          <p className="chat-sidebar__brand-sub">Workspace NIC-Labs</p>
        </div>
      </Link>

      {/* ── New channel CTA ── */}
      <button
        type="button"
        className="chat-sidebar__cta"
        aria-label="Novo canal"
        disabled
        aria-disabled="true"
        title="Em breve"
      >
        <IconAdd />
        Novo canal
      </button>

      {/* ── Nav ── */}
      <div className="chat-sidebar__nav" role="listbox" aria-label="Canais e mensagens diretas">
        {state.status === "loading" && <LoadingSkeleton />}

        {state.status === "error" && <ErrorState onRetry={retry} />}

        {state.status === "ready" && (
          <>
            {/* Channels section */}
            <div className="chat-sidebar__section-label">
              <span>Canais</span>
              <button
                type="button"
                className="chat-sidebar__section-action"
                aria-label="Adicionar canal"
                disabled
                aria-disabled="true"
                title="Em breve"
              >
                <IconAdd />
              </button>
            </div>
            <ChannelList
              channels={state.channels}
              activeChannelId={activeChannelId}
              onSelect={handleChannelSelect}
            />

            {/* DMs section */}
            <div className="chat-sidebar__section-label chat-sidebar__section-label--mt">
              <span>Mensagens diretas</span>
              <button
                ref={newDMButtonRef}
                type="button"
                className="chat-sidebar__section-action"
                aria-label="Nova mensagem direta"
                aria-haspopup="dialog"
                onClick={() => setNewDMOpen(true)}
              >
                <IconAdd />
              </button>
            </div>
            <DMList dms={state.dms} activeDMId={activeDMId} onSelect={handleDMSelect} />
          </>
        )}
      </div>

      {/* ── Footer ── */}
      <div className="chat-sidebar__footer">
        <Link
          to="/chat/favorites"
          className="chat-sidebar__footer-item"
          aria-label="Meus favoritos"
        >
          <IconStar />
          <span>Favoritos</span>
        </Link>
        <Link to="/admin/users" className="chat-sidebar__footer-item" aria-label="Configurações">
          <IconSettings />
          <span>Configurações</span>
        </Link>
        <Link to="/profile" className="chat-sidebar__user" aria-label="Meu perfil">
          <Avatar
            initials={PLACEHOLDER_USER.initials}
            color={PLACEHOLDER_USER.color}
            status="online"
            size="md"
          />
          <div className="chat-sidebar__user-info" aria-hidden="true">
            <div className="chat-sidebar__user-name">{PLACEHOLDER_USER.displayName}</div>
            <div className="chat-sidebar__user-role">{PLACEHOLDER_USER.role}</div>
          </div>
        </Link>
      </div>
      {newDMOpen && state.status === "ready" && (
        <NewDirectMessageDialog
          currentUserId={state.currentUserId}
          onClose={closeNewDM}
          onOpened={handleDMOpened}
        />
      )}
    </aside>
  );
}
