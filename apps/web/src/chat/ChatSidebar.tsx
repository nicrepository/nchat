import { useEffect, useMemo, useRef, useState } from "react";
import { Link, useLocation, useNavigate } from "react-router";

import "./ChatSidebar.css";
import { useSelfProfile } from "../profile/selfProfile";
import { partitionDMs, type Channel, type DMConversation } from "./chatTypes";
import { avatarColorFor, initialsFrom } from "./messageDisplay";
import NewConversationDialog from "./NewConversationDialog";
import PresenceDot from "./PresenceDot";
import { presenceLabel, presenceTargetKey, usePresence, type PresenceState } from "./presence";
import { sortByActivity } from "./sidebarOrder";
import type { SidebarState } from "./useChatSidebar";

function IconPin() {
  return (
    <svg
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth="2"
      className="chat-sidebar__icon"
      aria-hidden="true"
    >
      <path d="M12 17v5M7 3h10l-2 5v4l3 3H6l3-3V8L7 3Z" />
    </svg>
  );
}

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

function IconSearch() {
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
      <circle cx="11" cy="11" r="8" />
      <line x1="21" y1="21" x2="16.65" y2="16.65" />
    </svg>
  );
}

function IconChevronDown() {
  return (
    <svg
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth="2"
      strokeLinecap="round"
      strokeLinejoin="round"
      className="chat-sidebar__chevron-icon"
      aria-hidden="true"
    >
      <polyline points="6 9 12 15 18 9" />
    </svg>
  );
}

// ── Avatar helpers ────────────────────────────────────────────────────────────

interface AvatarProps {
  initials: string;
  /** Optional picture. Initials are shown when absent or when loading fails. */
  src?: string;
  color?: string;
  /**
   * Live presence (RF-58). Absent for an avatar that stands for a conversation
   * rather than a person — a group has no single state to report.
   *
   * The dot is decoration: the row that owns this avatar names the state in its
   * accessible label, so nothing here has to be reachable on its own.
   */
  status?: PresenceState;
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
      {status && <PresenceDot state={status} size={size} ringColor="var(--cs-sidebar-bg)" />}
    </span>
  );
}

function GroupAvatars({ dm }: { dm: DMConversation }) {
  const first = dm.participants[0];
  const second = dm.participants[1];
  // The sidebar payload carries no participants (chatApi maps them to []), so
  // without this every group reserved the avatar slot and left it empty
  // (BUG #395). The group name is already on the row, so its initials come from
  // the same canonical rule the 1:1 rows use — no second rule, no empty space.
  if (!first) {
    return <Avatar initials={initialsFrom(dm.name)} color={avatarColorFor(dm.id)} size="sm" />;
  }
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

// ── Section shell ─────────────────────────────────────────────────────────────

interface SectionProps {
  /** Stable id; the heading owns it and each list is labelled by it. */
  labelId: string;
  title: string;
  /** Only the first section sits flush against the CTA above it. */
  spaced?: boolean;
  children: React.ReactNode;
}

/**
 * One sidebar category: a real heading plus its list.
 *
 * The listbox lives inside each list component rather than here so that an
 * empty section renders its message *instead of* an options container — an
 * empty `role="listbox"` with a paragraph inside is not a valid one.
 */
function Section({ labelId, title, spaced, children }: SectionProps) {
  return (
    <section className="chat-sidebar__section" aria-labelledby={labelId}>
      <h2
        id={labelId}
        className={`chat-sidebar__section-label${spaced ? " chat-sidebar__section-label--mt" : ""}`}
      >
        {title}
      </h2>
      {children}
    </section>
  );
}

// ── Channel list ──────────────────────────────────────────────────────────────

interface ChannelListProps {
  channels: Channel[];
  activeChannelId: string | undefined;
  onSelect: (id: string) => void;
  labelId: string;
  onPin: (id: string, pinned: boolean) => void;
}

function ChannelList({ channels, activeChannelId, onSelect, labelId, onPin }: ChannelListProps) {
  if (channels.length === 0) {
    return (
      <p className="chat-sidebar__empty" role="status">
        Nenhum canal disponível.
      </p>
    );
  }

  return (
    <div className="chat-sidebar__section-list" role="listbox" aria-labelledby={labelId}>
      {channels.map((ch) => {
        const isActive = ch.id === activeChannelId;
        return (
          <div key={ch.id} className="chat-sidebar__item-row">
            <button
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
            <button
              type="button"
              className="chat-sidebar__pin-action"
              aria-pressed={Boolean(ch.pinnedAt)}
              aria-label={ch.pinnedAt ? `Desafixar ${ch.name}` : `Fixar ${ch.name} no topo`}
              title={ch.pinnedAt ? "Desafixar" : "Fixar no topo"}
              onClick={() => onPin(ch.id, Boolean(ch.pinnedAt))}
            >
              <IconPin />
            </button>
          </div>
        );
      })}
    </div>
  );
}

// ── DM / group list ───────────────────────────────────────────────────────────

interface DMListProps {
  /** Already narrowed to a single canonical category by partitionDMs. */
  dms: DMConversation[];
  activeDMId: string | undefined;
  onSelect: (id: string) => void;
  labelId: string;
  emptyMessage: string;
  onPin: (id: string, pinned: boolean) => void;
}

/**
 * One conversation row.
 *
 * Its own component because presence is a subscription, and a subscription
 * needs a hook: keeping it here means the row of the person who just went away
 * re-renders and the other forty do not. The group branch calls the same hook
 * with no id — a group has no single presence — so the rule of hooks holds
 * without a second component.
 */
function DMRow({
  dm,
  isActive,
  onSelect,
  onPin,
}: {
  dm: DMConversation;
  isActive: boolean;
  onSelect: (id: string) => void;
  onPin: (id: string, pinned: boolean) => void;
}) {
  const isGroup = dm.type === "group";
  const counterpart = dm.counterpart;
  // Scoped to this conversation: the counterpart is one of its two participants,
  // so the server's roster for it is exactly the list that would have named them.
  const presence = usePresence(
    isGroup ? undefined : counterpart?.userId,
    presenceTargetKey("dm", dm.id),
  );
  const baseLabel = isGroup ? `Grupo ${dm.name}` : `Mensagem direta com ${dm.name}`;
  // Presence in words, in the row's accessible name. The dot is what a sighted
  // user sees and this is what a screen reader hears, from the same value —
  // and it is why the avatar does not need to become a focusable control to
  // make the state reachable by keyboard.
  const label = presence === "unknown" ? baseLabel : `${baseLabel}, ${presenceLabel(presence)}`;

  return (
    <div className="chat-sidebar__item-row">
      <button
        type="button"
        role="option"
        aria-selected={isActive}
        aria-label={label}
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
            status={presence}
            size="sm"
          />
        )}
        <span className="chat-sidebar__dm-name">{dm.name}</span>
        {isGroup && (
          <span className="chat-sidebar__badge chat-sidebar__badge--group sr-only">grupo</span>
        )}
        {dm.unreadCount != null && dm.unreadCount > 0 && (
          <span className="chat-sidebar__unread-badge" aria-label={`${dm.unreadCount} não lidas`}>
            {dm.unreadCount}
          </span>
        )}
      </button>
      <button
        type="button"
        className="chat-sidebar__pin-action"
        aria-pressed={Boolean(dm.pinnedAt)}
        aria-label={dm.pinnedAt ? `Desafixar ${dm.name}` : `Fixar ${dm.name} no topo`}
        title={dm.pinnedAt ? "Desafixar" : "Fixar no topo"}
        onClick={() => onPin(dm.id, Boolean(dm.pinnedAt))}
      >
        <IconPin />
      </button>
    </div>
  );
}

function DMList({ dms, activeDMId, onSelect, labelId, emptyMessage, onPin }: DMListProps) {
  if (dms.length === 0) {
    return (
      <p className="chat-sidebar__empty" role="status">
        {emptyMessage}
      </p>
    );
  }

  return (
    <div className="chat-sidebar__section-list" role="listbox" aria-labelledby={labelId}>
      {dms.map((dm) => (
        <DMRow
          key={dm.id}
          dm={dm}
          isActive={dm.id === activeDMId}
          onSelect={onSelect}
          onPin={onPin}
        />
      ))}
    </div>
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

// ── Footer user block ─────────────────────────────────────────────────────────

/**
 * The authenticated user's own row: picture, name, and Settings beside it.
 *
 * Identity comes from GET /api/auth/me through the shared self-profile cache —
 * never from a client-chosen id, never from a fixture. Until that answer exists
 * the row is identity-neutral: a placeholder of the same size, so nothing shifts
 * and no invented name ("Usuário") or invented initial ("?") is ever on screen,
 * not even for a frame. A load failure is its own state for the same reason: it
 * is not a user without an avatar.
 *
 * Profile and Settings are siblings, not nested links: one interactive element
 * may not contain another.
 */
function SidebarUser() {
  const self = useSelfProfile();
  // "" covers absent / null / whitespace-only — normalised once, in profileApi.
  const displayName = self.status === "ready" ? self.profile.displayName : "";
  // The viewer's own presence comes back from the server like everyone else's:
  // their session announces itself into the conversations it subscribes to, and
  // the echo is what this reads. Nothing here decides locally that "I" am
  // online, so the footer shows what the server would tell anybody else.
  //
  // No conversation is passed, and none would be right: the footer is not
  // rendering this person *inside* a conversation. It therefore never shows
  // "offline" — which is correct, because a viewer looking at their own row is
  // by definition connected.
  const presence = usePresence(self.status === "ready" ? self.profile.id : undefined);
  const baseLabel = displayName ? `Meu perfil de ${displayName}` : "Meu perfil";

  return (
    <div className="chat-sidebar__user-row">
      <Link
        to="/profile"
        className="chat-sidebar__user"
        aria-label={presence === "unknown" ? baseLabel : `${baseLabel}, ${presenceLabel(presence)}`}
      >
        {self.status === "ready" ? (
          <>
            <Avatar
              // No usable name means no initials to derive: an empty swatch,
              // never "?". Its colour is still the user's own, so the row does
              // not change identity when a name arrives.
              initials={displayName ? initialsFrom(displayName) : ""}
              src={self.profile.avatarUrl}
              color={avatarColorFor(self.profile.id)}
              status={presence}
              size="md"
            />
            <span className="chat-sidebar__user-name">{displayName || "Meu perfil"}</span>
          </>
        ) : (
          <span
            className="chat-sidebar__user-placeholder"
            data-state={self.status}
            data-testid="chat-sidebar-user-placeholder"
            aria-hidden="true"
          >
            <span className="chat-sidebar__avatar chat-sidebar__avatar--md chat-sidebar__user-avatar-skeleton" />
            <span className="chat-sidebar__user-name-skeleton" />
          </span>
        )}
      </Link>
      <Link
        to="/admin/users"
        className="chat-sidebar__user-settings"
        aria-label="Configurações"
        title="Configurações"
      >
        <IconSettings />
      </Link>
    </div>
  );
}

// ── Main sidebar ──────────────────────────────────────────────────────────────

interface ChatSidebarProps {
  state: SidebarState;
  retry: () => void;
  setPinned?: (
    target: { kind: "channel" | "dm"; targetId: string },
    pinned: boolean,
  ) => Promise<void>;
}

// Static because the sidebar is mounted once per app; each heading owns the id
// its section's listbox is labelled by.
const CHANNELS_LABEL_ID = "chat-sidebar-section-channels";
const DIRECTS_LABEL_ID = "chat-sidebar-section-directs";
const GROUPS_LABEL_ID = "chat-sidebar-section-groups";

export default function ChatSidebar({ state, retry, setPinned }: ChatSidebarProps) {
  const navigate = useNavigate();
  const location = useLocation();
  // One dialog, one trigger, one piece of open state: two of them could be open
  // at once, and there is nothing left for a second one to do (BUG #393).
  const [newConversationOpen, setNewConversationOpen] = useState(false);
  const [collapsedCategories, setCollapsedCategories] = useState<Record<string, boolean>>({});

  const toggleCategory = (key: string) => {
    setCollapsedCategories((prev) => ({
      ...prev,
      [key]: !prev[key],
    }));
  };

  const newConversationButtonRef = useRef<HTMLButtonElement>(null);
  const restoreFocusRef = useRef(false);
  const [pinError, setPinError] = useState("");

  useEffect(() => {
    if (!newConversationOpen && state.status === "ready" && restoreFocusRef.current) {
      newConversationButtonRef.current?.focus();
      restoreFocusRef.current = false;
    }
  }, [newConversationOpen, state.status]);

  // Derive active item from pathname: /chat/channel/:id or /chat/dm/:id
  // decodeURIComponent handles IDs that were encoded with encodeURIComponent on navigate.
  const pathParts = location.pathname.split("/").filter(Boolean);
  // pathParts: ["chat", "channel"|"dm", encodedId]
  const activeType = pathParts[1] as "channel" | "dm" | undefined;
  const activeId = pathParts[2] ? safeDecodeURIComponent(pathParts[2]) : undefined;

  const activeChannelId = activeType === "channel" ? activeId : undefined;
  const activeDMId = activeType === "dm" ? activeId : undefined;

  // Derived on every render from the canonical list, so a refetch that reorders
  // or replaces items cannot leave a stale copy behind in either section, and
  // each section is then ordered by its own activity and by nothing else's
  // (issue #414). Three independent sorts over three disjoint lists: a busy
  // channel cannot move a group, and a busy group cannot move a DM. Ordering
  // here, on the rendered value rather than where the data arrives, means every
  // source of change — first load, refetch, realtime activity — lands in the
  // same order without each having to remember to re-apply it.
  //
  // `sortByActivity` copies before sorting, so neither the props nor the
  // reducer's state array is ever reordered in place. Rows are keyed by
  // conversation id (never by index), so React moves the existing DOM nodes
  // instead of rewriting them: the selected row stays selected, focus stays on
  // the element that had it, and the scroll position survives a reorder.
  const channels = state.status === "ready" ? state.channels : undefined;
  const dms = state.status === "ready" ? state.dms : undefined;
  const categories = state.status === "ready" ? state.categories : undefined;

  const effectiveCategories = useMemo(
    () =>
      categories && categories.length > 0
        ? categories
        : [{ id: undefined, name: "Geral", kind: "uncategorized" as const }],
    [categories],
  );

  const groupedChannelsByCategory = useMemo(() => {
    if (!channels) return [];

    const channelsByCat = new Map<string | undefined, Channel[]>();
    for (const ch of channels) {
      const key = ch.categoryId || undefined;
      const list = channelsByCat.get(key) ?? [];
      list.push(ch);
      channelsByCat.set(key, list);
    }
    return effectiveCategories.map((cat) => {
      const key = cat.id || undefined;
      const catChannels = channelsByCat.get(key) ?? [];
      const ordered = sortByActivity(catChannels);
      return {
        category: cat,
        channels: ordered,
      };
    });
  }, [channels, effectiveCategories]);

  const { orderedDirects, orderedGroups } = useMemo(() => {
    const { directs, groups } = partitionDMs(dms ?? []);
    return { orderedDirects: sortByActivity(directs), orderedGroups: sortByActivity(groups) };
  }, [dms]);

  function handleChannelSelect(id: string) {
    navigate(`/chat/channel/${encodeURIComponent(id)}`);
  }

  function handleDMSelect(id: string) {
    navigate(`/chat/dm/${encodeURIComponent(id)}`);
  }

  function handlePin(kind: "channel" | "dm", id: string, pinned: boolean) {
    if (!setPinned) return;
    setPinError("");
    void setPinned({ kind, targetId: id }, !pinned).catch(() =>
      setPinError("Nao foi possivel atualizar a fixacao."),
    );
  }

  function closeNewConversation() {
    restoreFocusRef.current = true;
    setNewConversationOpen(false);
  }

  function handleDMOpened(id: string) {
    closeNewConversation();
    navigate(`/chat/dm/${encodeURIComponent(id)}`);
    retry();
  }

  // The created channel is opened straight away, but the sidebar list itself
  // comes from the canonical refetch — never from the creation response — so
  // what is listed is always what the server says the user may see, and a new
  // channel lands in "Canais" because that is where the refetch puts it.
  function handleChannelCreated(id: string) {
    closeNewConversation();
    navigate(`/chat/channel/${encodeURIComponent(id)}`);
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

      {/* ── New conversation CTA ──
          The sidebar's single creation entry point: the dialog behind it is
          where Pessoa/Grupo/Canal is chosen. The accessible name is the visible
          text, so it does not depend on a tooltip. Unavailable until the sidebar
          is ready because the dialog needs the current user id to exclude the
          actor from the search — never because of a role, which the server alone
          evaluates and which channel creation does not consider at all. */}
      <button
        ref={newConversationButtonRef}
        type="button"
        className="chat-sidebar__cta"
        aria-haspopup="dialog"
        disabled={state.status !== "ready"}
        onClick={() => setNewConversationOpen(true)}
      >
        <IconAdd />
        Nova conversa
      </button>

      {/* ── Nav ──
          Three product categories, three sections. Channels come from their own
          canonical list; 1:1 conversations and ad-hoc groups are split from the
          single DM list by the server-derived discriminator, so a conversation
          cannot show up twice or land in the wrong section. Nothing is
          classified while loading or on error: the sections only exist once the
          canonical data does. */}
      <div className="chat-sidebar__nav">
        {state.status === "loading" && <LoadingSkeleton />}

        {state.status === "error" && <ErrorState onRetry={retry} />}

        {state.status === "ready" && (
          <>
            <Section labelId={CHANNELS_LABEL_ID} title="Canais">
              {groupedChannelsByCategory.length <= 1 &&
              groupedChannelsByCategory[0]?.category.kind === "uncategorized" ? (
                <ChannelList
                  channels={groupedChannelsByCategory[0]?.channels ?? []}
                  activeChannelId={activeChannelId}
                  onSelect={handleChannelSelect}
                  labelId={CHANNELS_LABEL_ID}
                  onPin={(id, pinned) => handlePin("channel", id, pinned)}
                />
              ) : (
                <div className="chat-sidebar__categories-list">
                  {groupedChannelsByCategory.map(({ category, channels: categoryChannels }) => {
                    const categoryKey = category.id ?? "uncategorized";
                    const headerId = `chat-sidebar-category-${categoryKey}`;
                    const collapsed = Boolean(collapsedCategories[categoryKey]);
                    return (
                      <div key={categoryKey} className="chat-sidebar__category-group">
                        <button
                          type="button"
                          id={headerId}
                          className="chat-sidebar__category-header"
                          aria-expanded={!collapsed}
                          onClick={() => toggleCategory(categoryKey)}
                        >
                          <span
                            className={`chat-sidebar__category-chevron${collapsed ? " chat-sidebar__category-chevron--collapsed" : ""}`}
                          >
                            <IconChevronDown />
                          </span>
                          <span className="chat-sidebar__category-title">{category.name}</span>
                        </button>
                        {!collapsed && (
                          <div className="chat-sidebar__category-channels">
                            <ChannelList
                              channels={categoryChannels}
                              activeChannelId={activeChannelId}
                              onSelect={handleChannelSelect}
                              labelId={headerId}
                              onPin={(id, pinned) => handlePin("channel", id, pinned)}
                            />
                          </div>
                        )}
                      </div>
                    );
                  })}
                </div>
              )}
            </Section>

            <Section labelId={DIRECTS_LABEL_ID} title="Mensagens diretas" spaced>
              <DMList
                dms={orderedDirects}
                activeDMId={activeDMId}
                onSelect={handleDMSelect}
                labelId={DIRECTS_LABEL_ID}
                emptyMessage="Nenhuma mensagem direta."
                onPin={(id, pinned) => handlePin("dm", id, pinned)}
              />
            </Section>

            <Section labelId={GROUPS_LABEL_ID} title="Grupos" spaced>
              <DMList
                dms={orderedGroups}
                activeDMId={activeDMId}
                onSelect={handleDMSelect}
                labelId={GROUPS_LABEL_ID}
                emptyMessage="Nenhum grupo."
                onPin={(id, pinned) => handlePin("dm", id, pinned)}
              />
            </Section>
          </>
        )}
        {pinError && (
          <p className="chat-sidebar__pin-error" role="alert">
            {pinError}
          </p>
        )}
      </div>

      {/* ── Footer ── */}
      <div className="chat-sidebar__footer">
        <Link to="/chat/search" className="chat-sidebar__footer-item" aria-label="Buscar">
          <IconSearch />
          <span>Buscar</span>
        </Link>
        <Link
          to="/chat/favorites"
          className="chat-sidebar__footer-item"
          aria-label="Meus favoritos"
        >
          <IconStar />
          <span>Favoritos</span>
        </Link>
        <SidebarUser />
      </div>
      {newConversationOpen && state.status === "ready" && (
        <NewConversationDialog
          currentUserId={state.currentUserId}
          categories={categories || []}
          onClose={closeNewConversation}
          onOpened={handleDMOpened}
          onChannelCreated={handleChannelCreated}
        />
      )}
    </aside>
  );
}
