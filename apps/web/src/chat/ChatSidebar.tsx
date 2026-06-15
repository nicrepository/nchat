import { useLocation, useNavigate } from "react-router-dom";

import "./ChatSidebar.css";
import { useChatSidebar } from "./useChatSidebar";
import type { Channel, DMConversation } from "./chatTypes";

// ── Inline SVG icons ─────────────────────────────────────────────────────────

function IconHash() {
  return (
    <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2"
      strokeLinecap="round" strokeLinejoin="round" className="chat-sidebar__icon" aria-hidden="true">
      <line x1="10" y1="4" x2="8" y2="20" />
      <line x1="16" y1="4" x2="14" y2="20" />
      <line x1="4"  y1="9" x2="20" y2="9"  />
      <line x1="3"  y1="15" x2="19" y2="15"/>
    </svg>
  );
}

function IconLock() {
  return (
    <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2"
      strokeLinecap="round" strokeLinejoin="round" className="chat-sidebar__icon chat-sidebar__icon--sm" aria-hidden="true">
      <rect x="3" y="11" width="18" height="11" rx="2" ry="2"/>
      <path d="M7 11V7a5 5 0 0 1 10 0v4"/>
    </svg>
  );
}

function IconAdd() {
  return (
    <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2"
      strokeLinecap="round" strokeLinejoin="round" className="chat-sidebar__add-icon" aria-hidden="true">
      <line x1="12" y1="5" x2="12" y2="19"/>
      <line x1="5"  y1="12" x2="19" y2="12"/>
    </svg>
  );
}

function IconSettings() {
  return (
    <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2"
      strokeLinecap="round" strokeLinejoin="round" className="chat-sidebar__icon" aria-hidden="true">
      <circle cx="12" cy="12" r="3"/>
      <path d="M19.4 15a1.65 1.65 0 0 0 .33 1.82l.06.06a2 2 0 0 1-2.83 2.83l-.06-.06a1.65 1.65 0 0 0-1.82-.33 1.65 1.65 0 0 0-1 1.51V21a2 2 0 0 1-4 0v-.09A1.65 1.65 0 0 0 9 19.4a1.65 1.65 0 0 0-1.82.33l-.06.06a2 2 0 0 1-2.83-2.83l.06-.06A1.65 1.65 0 0 0 4.68 15a1.65 1.65 0 0 0-1.51-1H3a2 2 0 0 1 0-4h.09A1.65 1.65 0 0 0 4.6 9a1.65 1.65 0 0 0-.33-1.82l-.06-.06a2 2 0 0 1 2.83-2.83l.06.06A1.65 1.65 0 0 0 9 4.68a1.65 1.65 0 0 0 1-1.51V3a2 2 0 0 1 4 0v.09a1.65 1.65 0 0 0 1 1.51 1.65 1.65 0 0 0 1.82-.33l.06-.06a2 2 0 0 1 2.83 2.83l-.06.06A1.65 1.65 0 0 0 19.4 9a1.65 1.65 0 0 0 1.51 1H21a2 2 0 0 1 0 4h-.09a1.65 1.65 0 0 0-1.51 1z"/>
    </svg>
  );
}

// ── Avatar helpers ────────────────────────────────────────────────────────────

interface AvatarProps {
  initials: string;
  color?: string;
  status?: "online" | "away" | "offline";
  size?: "sm" | "md";
}

function Avatar({ initials, color = "purple", status, size = "sm" }: AvatarProps) {
  return (
    <span
      className={`chat-sidebar__avatar chat-sidebar__avatar--${color} chat-sidebar__avatar--${size}`}
      aria-hidden="true"
    >
      {initials}
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
        <span className={`chat-sidebar__avatar chat-sidebar__avatar--${first.color} chat-sidebar__avatar--sm chat-sidebar__avatar--group-back`}>
          {first.initials}
        </span>
      )}
      {second && (
        <span className={`chat-sidebar__avatar chat-sidebar__avatar--${second.color} chat-sidebar__avatar--sm chat-sidebar__avatar--group-front`}>
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
              <span className="chat-sidebar__unread-badge" aria-label={`${ch.unreadCount} não lidas`}>
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
        const firstParticipant = dm.participants[0];
        const isGroup = dm.type === "group";

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
            {isGroup ? (
              <GroupAvatars dm={dm} />
            ) : firstParticipant ? (
              <Avatar
                initials={firstParticipant.initials}
                color={firstParticipant.color}
                status={firstParticipant.status}
                size="sm"
              />
            ) : null}
            <span className="chat-sidebar__dm-name">{dm.name}</span>
            {isGroup && (
              <span className="chat-sidebar__badge chat-sidebar__badge--group sr-only">
                grupo
              </span>
            )}
            {dm.unreadCount != null && dm.unreadCount > 0 && (
              <span className="chat-sidebar__unread-badge" aria-label={`${dm.unreadCount} não lidas`}>
                {dm.unreadCount}
              </span>
            )}
          </button>
        );
      })}
    </>
  );
}

// ── Main sidebar ──────────────────────────────────────────────────────────────

export default function ChatSidebar() {
  const navigate = useNavigate();
  const location = useLocation();
  const { state, retry } = useChatSidebar();

  // Derive active item from pathname: /chat/channel/:id or /chat/dm/:id
  const pathParts = location.pathname.split("/").filter(Boolean);
  // pathParts: ["chat", "channel"|"dm", id]
  const activeType = pathParts[1] as "channel" | "dm" | undefined;
  const activeId   = pathParts[2];

  const activeChannelId = activeType === "channel" ? activeId : undefined;
  const activeDMId      = activeType === "dm"      ? activeId : undefined;

  function handleChannelSelect(id: string) {
    navigate(`/chat/channel/${id}`);
  }

  function handleDMSelect(id: string) {
    navigate(`/chat/dm/${id}`);
  }

  return (
    <aside
      className="chat-sidebar"
      aria-label="Navegação do workspace NIC Chat"
      data-testid="chat-sidebar"
    >
      {/* ── Brand ── */}
      <a href="/chat" className="chat-sidebar__brand" aria-label="NIC Chat — Workspace NIC-Labs">
        <div className="chat-sidebar__brand-mark">
          <img src="/assets/nic-labs-icon.png" alt="" className="chat-sidebar__brand-img" />
        </div>
        <div>
          <p className="chat-sidebar__brand-title">NIC Chat</p>
          <p className="chat-sidebar__brand-sub">Workspace NIC-Labs</p>
        </div>
      </a>

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
                type="button"
                className="chat-sidebar__section-action"
                aria-label="Nova mensagem direta"
                disabled
                aria-disabled="true"
                title="Em breve"
              >
                <IconAdd />
              </button>
            </div>
            <DMList
              dms={state.dms}
              activeDMId={activeDMId}
              onSelect={handleDMSelect}
            />
          </>
        )}
      </div>

      {/* ── Footer ── */}
      <div className="chat-sidebar__footer">
        <a
          href="/admin/users"
          className="chat-sidebar__footer-item"
          aria-label="Configurações"
        >
          <IconSettings />
          <span>Configurações</span>
        </a>
        <div className="chat-sidebar__user">
          <Avatar initials="AN" color="purple" status="online" size="md" />
          <div className="chat-sidebar__user-info" aria-hidden="true">
            <div className="chat-sidebar__user-name">Álvaro Neto</div>
            <div className="chat-sidebar__user-role">Infraestrutura &amp; Segurança</div>
          </div>
        </div>
      </div>
    </aside>
  );
}
