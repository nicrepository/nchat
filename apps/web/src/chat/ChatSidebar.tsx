import { useEffect, useMemo, useRef, useState } from "react";
import { Link, useLocation, useNavigate } from "react-router";

import "./ChatSidebar.css";
import { useSelfProfile } from "../profile/selfProfile";
import { partitionDMs, type Channel, type ChannelCategory, type DMConversation } from "./chatTypes";
import ConversationActionsMenu from "./ConversationActionsMenu";
import {
  actionsTriggerLabel,
  conversationActions,
  conversationTargetKind,
  type ConversationActionId,
  type ConversationTarget,
} from "./conversationActions";
import LeaveConversationDialog from "./LeaveConversationDialog";
import RenameChannelDialog from "./RenameChannelDialog";
import { avatarColorFor, initialsFrom } from "./messageDisplay";
import NewConversationDialog from "./NewConversationDialog";
import { PersonAvatarImage } from "./PersonAvatarImage";
import PresenceDot from "./PresenceDot";
import { presenceLabel, presenceTargetKey, usePresence, type PresenceState } from "./presence";
import { sortByActivity } from "./sidebarOrder";
import type { SidebarState } from "./useChatSidebar";

/**
 * The pinned indicator (issue #527).
 *
 * A pin is now *state* and never an action: it is drawn only on a pinned row,
 * it is filled white so it reads as "on" without depending on colour alone, and
 * it is `aria-hidden` because the row's own accessible name already says
 * "fixado". Unpinning happens in the "…" menu, like every other action.
 *
 * An unpinned row draws nothing here — no ghost pin, no reserved outline — which
 * is what makes "has a pin" mean exactly one thing on screen.
 */
function PinnedIndicator() {
  return (
    <span className="chat-sidebar__pinned" aria-hidden="true" data-testid="chat-sidebar-pinned">
      <svg viewBox="0 0 24 24" fill="currentColor" className="chat-sidebar__icon" focusable="false">
        <path d="M12 17v5M7 3h10l-2 5v4l3 3H6l3-3V8L7 3Z" />
      </svg>
    </span>
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
  return (
    <span
      className={`chat-sidebar__avatar chat-sidebar__avatar--${color} chat-sidebar__avatar--${size}`}
      aria-hidden="true"
    >
      <PersonAvatarImage src={src} initials={initials} imgClassName="chat-sidebar__avatar-img" />
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

/**
 * The unread badge never depends on color alone to signal a mention: the
 * accessible name says so explicitly, and the visible "@" prefix carries the
 * same information for sighted users who don't rely on the badge's color.
 */
function unreadBadgeLabel(count: number, hasMentionUnread?: boolean): string {
  return hasMentionUnread ? `${count} não lidas, incluindo menção` : `${count} não lidas`;
}

// ── Row actions ───────────────────────────────────────────────────────────────

/**
 * What every row needs to draw its "…" menu, threaded through the two list
 * components unchanged.
 *
 * `openTargetKey` is the identity of the single open menu — `type:id` — held by
 * the sidebar rather than by each row, so at most one popup exists in the tree
 * at a time and a reorder or a category collapse cannot leave an orphan one
 * behind: the row that owns it simply stops matching.
 */
interface RowActionsProps {
  openTargetKey: string | null;
  onOpenChange: (key: string, open: boolean) => void;
  onAction: (target: ConversationTarget, action: ConversationActionId) => void;
}

function targetKey(target: ConversationTarget): string {
  return `${target.kind}:${target.id}`;
}

/**
 * One row's trailing controls: the pinned state, then the actions menu.
 *
 * Both live outside the row's `role="option"` button — a control inside an
 * option is neither valid nor reachable — and the container reserves its width
 * unconditionally, so a menu appearing on hover cannot shift the name, the
 * avatar or the unread badge by a pixel.
 */
function RowActions({
  target,
  openTargetKey,
  onOpenChange,
  onAction,
}: RowActionsProps & { target: ConversationTarget }) {
  const key = targetKey(target);
  return (
    <span className="chat-sidebar__trailing">
      {target.pinned && <PinnedIndicator />}
      <ConversationActionsMenu
        triggerLabel={actionsTriggerLabel(target)}
        actions={conversationActions(target)}
        open={openTargetKey === key}
        onOpenChange={(open) => onOpenChange(key, open)}
        onAction={(action) => onAction(target, action)}
      />
    </span>
  );
}

function hasUnread(count: number | undefined): boolean {
  return count != null && count > 0;
}

/**
 * The row's accessible name carries the pinned state in words.
 *
 * That is what lets the pin itself stay decorative: a screen-reader user hears
 * "Canal geral, fixado" from the one element that is actually in the listbox,
 * instead of meeting a second focusable control whose only job is to say so.
 */
function withPinnedSuffix(label: string, pinned: boolean): string {
  return pinned ? `${label}, fixado` : label;
}

// ── Channel list ──────────────────────────────────────────────────────────────

interface ChannelListProps {
  channels: Channel[];
  activeChannelId: string | undefined;
  onSelect: (id: string) => void;
  labelId: string;
  actions: RowActionsProps;
}

function ChannelList({ channels, activeChannelId, onSelect, labelId, actions }: ChannelListProps) {
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
        const target: ConversationTarget = {
          kind: "channel",
          id: ch.id,
          name: ch.name,
          pinned: Boolean(ch.pinnedAt),
          canRename: ch.canRename,
          isGeneral: ch.isGeneral,
          muted: Boolean(ch.muted),
          hasUnread: hasUnread(ch.unreadCount),
        };
        return (
          <div key={ch.id} className="chat-sidebar__item-row">
            <button
              type="button"
              role="option"
              aria-selected={isActive}
              aria-label={withPinnedSuffix(
                `Canal ${ch.type === "private" ? "privado " : ""}${ch.name}`,
                target.pinned,
              )}
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
                  className={`chat-sidebar__unread-badge${ch.hasMentionUnread ? " chat-sidebar__unread-badge--mention" : ""}`}
                  aria-label={unreadBadgeLabel(ch.unreadCount, ch.hasMentionUnread)}
                >
                  {ch.hasMentionUnread && (
                    <span aria-hidden="true" className="chat-sidebar__unread-badge-mention-mark">
                      @
                    </span>
                  )}
                  {ch.unreadCount}
                </span>
              )}
            </button>
            <RowActions target={target} {...actions} />
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
  actions: RowActionsProps;
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
  actions,
}: {
  dm: DMConversation;
  isActive: boolean;
  onSelect: (id: string) => void;
  actions: RowActionsProps;
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
  // A group is a group and a 1:1 is a 1:1, from the server's own discriminator —
  // the same value partitionDMs filed this row under. It is what keeps "Sair"
  // out of a direct conversation structurally rather than by a check at the end.
  const target: ConversationTarget = {
    kind: isGroup ? "group" : "dm",
    id: dm.id,
    name: dm.name,
    pinned: Boolean(dm.pinnedAt),
    muted: Boolean(dm.muted),
    hasUnread: hasUnread(dm.unreadCount),
  };

  return (
    <div className="chat-sidebar__item-row">
      <button
        type="button"
        role="option"
        aria-selected={isActive}
        aria-label={withPinnedSuffix(label, target.pinned)}
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
          <span
            className={`chat-sidebar__unread-badge${dm.hasMentionUnread ? " chat-sidebar__unread-badge--mention" : ""}`}
            aria-label={unreadBadgeLabel(dm.unreadCount, dm.hasMentionUnread)}
          >
            {dm.hasMentionUnread && (
              <span aria-hidden="true" className="chat-sidebar__unread-badge-mention-mark">
                @
              </span>
            )}
            {dm.unreadCount}
          </span>
        )}
      </button>
      <RowActions target={target} {...actions} />
    </div>
  );
}

function DMList({ dms, activeDMId, onSelect, labelId, emptyMessage, actions }: DMListProps) {
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
          actions={actions}
        />
      ))}
    </div>
  );
}

// ── Rename dialog host ────────────────────────────────────────────────────────

/**
 * Resolves the channel a rename dialog is open for, and renders nothing when
 * there is none (issue #527).
 *
 * The name is read from the canonical list rather than captured when the menu
 * item was chosen, so the field is seeded from the same value the row shows —
 * and a channel the refetch removed (access revoked, archived) closes its dialog
 * instead of leaving a stale one open over a conversation that no longer exists.
 *
 * Its own component so ChatSidebar keeps one element here instead of a
 * three-way conditional, which is the shape that turns a container into a God
 * component one action at a time.
 */
/**
 * Resolves the conversation a rename dialog is open for, and renders nothing
 * when there is none (issue #527).
 *
 * One host for both kinds. A channel and a group are renamed through different
 * endpoints, but the dialog is the same — a name, a field and a Save — so the
 * only thing that varies is which persist function it is handed, chosen here
 * from the canonical lists rather than by the dialog.
 *
 * The name is read from those lists rather than captured when the menu item was
 * chosen, so the field is seeded from the same value the row shows, and a
 * conversation the refetch removed closes its dialog instead of leaving a stale
 * one open.
 */
function SidebarRenameDialog({
  channels,
  dms,
  targetId,
  onClose,
  onRenameChannel,
  onRenameGroup,
}: {
  channels: Channel[] | undefined;
  dms: DMConversation[] | undefined;
  targetId: string | null;
  onClose: () => void;
  onRenameChannel?: (channelId: string, displayName: string) => Promise<void>;
  onRenameGroup?: (conversationId: string, title: string) => Promise<void>;
}) {
  const channel = channels?.find((candidate) => candidate.id === targetId);
  // Groups only: a 1:1 has no title of its own, so it is never a rename target.
  const group = dms?.find((candidate) => candidate.id === targetId && candidate.type === "group");
  const rename = channel ? onRenameChannel : onRenameGroup;
  const conversation = channel ?? group;
  if (!conversation || !rename) return null;
  return (
    <RenameChannelDialog
      // Keyed by conversation so switching targets remounts with the right name
      // rather than keeping the previous edit in the field.
      key={conversation.id}
      kind={channel ? "channel" : "group"}
      channelId={conversation.id}
      currentName={conversation.name}
      onClose={onClose}
      onRename={rename}
    />
  );
}

/**
 * Resolves the conversation a leave confirmation is open for.
 *
 * Same shape and same reasoning as the rename host above, plus one fact the
 * dialog needs and cannot derive: whether a channel is private, which decides
 * what the person is told they will lose.
 *
 * A 1:1 conversation can never appear here — it is not in either lookup — which
 * is the structural half of "a DM has no Sair".
 */
function SidebarLeaveDialog({
  channels,
  dms,
  targetId,
  onClose,
  onLeave,
}: {
  channels: Channel[] | undefined;
  dms: DMConversation[] | undefined;
  targetId: string | null;
  onClose: () => void;
  onLeave?: (target: { kind: "channel" | "dm"; targetId: string }) => Promise<void>;
}) {
  const channel = channels?.find((candidate) => candidate.id === targetId);
  const group = dms?.find((candidate) => candidate.id === targetId && candidate.type === "group");
  const conversation = channel ?? group;
  if (!conversation || !onLeave) return null;
  return (
    <LeaveConversationDialog
      key={conversation.id}
      kind={channel ? "channel" : "group"}
      name={conversation.name}
      isPrivate={channel?.type === "private"}
      onClose={onClose}
      onConfirm={() => onLeave({ kind: channel ? "channel" : "dm", targetId: conversation.id })}
    />
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

/**
 * The conversation the route names, split into the two ids the lists compare
 * against. `/chat/channel/:id` and `/chat/dm/:id` are the only two shapes; a
 * malformed or unrelated path selects nothing rather than guessing.
 */
function selectionFromPath(pathname: string): {
  activeChannelId: string | undefined;
  activeDMId: string | undefined;
} {
  // pathParts: ["chat", "channel"|"dm", encodedId]. The id is decoded because
  // navigate() encodes it.
  const pathParts = pathname.split("/").filter(Boolean);
  const activeType = pathParts[1];
  const activeId = pathParts[2] ? safeDecodeURIComponent(pathParts[2]) : undefined;
  return {
    activeChannelId: activeType === "channel" ? activeId : undefined,
    activeDMId: activeType === "dm" ? activeId : undefined,
  };
}

/**
 * The canonical lists, or nothing at all while loading or on error.
 *
 * Undefined rather than empty on purpose: an empty list is a real answer ("you
 * are in no channels") and the sections render it as such, so a state that has
 * no answer yet must be distinguishable from one that does.
 */
function sidebarLists(state: SidebarState): {
  channels: Channel[] | undefined;
  dms: DMConversation[] | undefined;
  categories: ChannelCategory[] | undefined;
} {
  if (state.status !== "ready")
    return { channels: undefined, dms: undefined, categories: undefined };
  return { channels: state.channels, dms: state.dms, categories: state.categories };
}

interface ChannelsByCategoryProps {
  grouped: { category: ChannelCategory; channels: Channel[] }[];
  activeChannelId: string | undefined;
  onSelect: (id: string) => void;
  actions: RowActionsProps;
  collapsed: Record<string, boolean>;
  onToggleCategory: (key: string) => void;
}

/**
 * The channels section, with or without category headers.
 *
 * A workspace that has never created a category has exactly one implicit group,
 * and drawing a "Geral" header above the only list would be a heading that
 * distinguishes nothing — so that case renders the plain list. Everything else
 * gets one collapsible group per category, each list labelled by its own header
 * so the listbox is never anonymous.
 */
function ChannelsByCategory({
  grouped,
  activeChannelId,
  onSelect,
  actions,
  collapsed,
  onToggleCategory,
}: ChannelsByCategoryProps) {
  if (grouped.length <= 1 && grouped[0]?.category.kind === "uncategorized") {
    return (
      <ChannelList
        channels={grouped[0]?.channels ?? []}
        activeChannelId={activeChannelId}
        onSelect={onSelect}
        labelId={CHANNELS_LABEL_ID}
        actions={actions}
      />
    );
  }
  return (
    <div className="chat-sidebar__categories-list">
      {grouped.map(({ category, channels: categoryChannels }) => {
        const categoryKey = category.id ?? "uncategorized";
        const headerId = `chat-sidebar-category-${categoryKey}`;
        const isCollapsed = Boolean(collapsed[categoryKey]);
        return (
          <div key={categoryKey} className="chat-sidebar__category-group">
            <button
              type="button"
              id={headerId}
              className="chat-sidebar__category-header"
              aria-expanded={!isCollapsed}
              onClick={() => onToggleCategory(categoryKey)}
            >
              <span
                className={`chat-sidebar__category-chevron${isCollapsed ? " chat-sidebar__category-chevron--collapsed" : ""}`}
              >
                <IconChevronDown />
              </span>
              <span className="chat-sidebar__category-title">{category.name}</span>
            </button>
            {!isCollapsed && (
              <div className="chat-sidebar__category-channels">
                <ChannelList
                  channels={categoryChannels}
                  activeChannelId={activeChannelId}
                  onSelect={onSelect}
                  labelId={headerId}
                  actions={actions}
                />
              </div>
            )}
          </div>
        );
      })}
    </div>
  );
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
  /** Clears a conversation's unread badge without opening it (issue #527). */
  markRead?: (target: { kind: "channel" | "dm"; targetId: string }) => void;
  /** Persists a channel's new name; rejects with the API error (issue #527). */
  renameChannel?: (channelId: string, displayName: string) => Promise<void>;
  /** Persists a group's new title; rejects with the API error (issue #527). */
  renameGroup?: (conversationId: string, title: string) => Promise<void>;
  /** Silences or restores one conversation for this viewer (issue #527). */
  setMuted?: (
    target: { kind: "channel" | "dm"; targetId: string },
    muted: boolean,
  ) => Promise<void>;
  /** Removes this viewer from a channel or group (issue #527). */
  leaveConversation?: (target: { kind: "channel" | "dm"; targetId: string }) => Promise<void>;
  /**
   * Opens the details panel for the conversation whose menu was used —
   * deliberately the menu's target and never the selected one (issue #527).
   * Absent when the shell has no panel to open, which simply omits the action's
   * effect rather than the item.
   */
  onOpenDetails?: (kind: "channel" | "dm", targetId: string) => void;
}

// Static because the sidebar is mounted once per app; each heading owns the id
// its section's listbox is labelled by.
const CHANNELS_LABEL_ID = "chat-sidebar-section-channels";
const DIRECTS_LABEL_ID = "chat-sidebar-section-directs";
const GROUPS_LABEL_ID = "chat-sidebar-section-groups";

export default function ChatSidebar({
  state,
  retry,
  setPinned,
  markRead,
  renameChannel,
  renameGroup,
  setMuted,
  leaveConversation,
  onOpenDetails,
}: ChatSidebarProps) {
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
  // One line for every row action that can fail without a dialog of its own:
  // pinning and muting. Two separate states would let a stale pin error sit
  // under a fresh mute failure, and the row only ever has one thing to say.
  const [actionError, setActionError] = useState("");
  // One open menu for the whole sidebar, identified by `type:id` (issue #527).
  // Holding it here rather than per row is what keeps forty rows from mounting
  // forty popovers and forty document listeners, and it makes "the anchor stopped
  // existing" — a reorder, a collapsed category, a conversation the refetch
  // dropped — resolve itself: no row matches the key any more, so nothing renders.
  const [openMenuKey, setOpenMenuKey] = useState<string | null>(null);
  // The channel a rename dialog is open for, by id. The name is read from the
  // canonical list at render time rather than captured here, so the field is
  // seeded from the same value the row shows.
  const [renamingId, setRenamingId] = useState<string | null>(null);
  // The conversation a leave confirmation is open for. Like the rename dialog,
  // it holds only an id: the name and the kind are read from the canonical list
  // at render time, so a refetch that removed the conversation closes the
  // dialog instead of leaving one open over nothing.
  const [leavingId, setLeavingId] = useState<string | null>(null);

  useEffect(() => {
    if (!newConversationOpen && state.status === "ready" && restoreFocusRef.current) {
      newConversationButtonRef.current?.focus();
      restoreFocusRef.current = false;
    }
  }, [newConversationOpen, state.status]);

  const { activeChannelId, activeDMId } = selectionFromPath(location.pathname);

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
  const { channels, dms, categories } = sidebarLists(state);

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
    setActionError("");
    void setPinned({ kind, targetId: id }, !pinned).catch(() =>
      setActionError("Não foi possível atualizar a fixação."),
    );
  }

  function handleMenuOpenChange(key: string, open: boolean) {
    // A single slot, so opening one row's menu closes any other by construction.
    setOpenMenuKey(open ? key : (current) => (current === key ? null : current));
  }

  function handleMute(target: ConversationTarget, muted: boolean) {
    if (!setMuted) return;
    setActionError("");
    void setMuted({ kind: conversationTargetKind(target.kind), targetId: target.id }, muted).catch(
      () => setActionError("Não foi possível atualizar as notificações."),
    );
  }

  /**
   * Runs one menu action against one target.
   *
   * A table rather than a cascade of ifs, and deliberately the only place that
   * maps an action id to an effect.
   *
   * Every one of these acts on `target` — the conversation whose menu was
   * opened — and never on whatever happens to be selected. That distinction is
   * the whole point of threading the target through: muting a channel from the
   * sidebar while reading a different one must silence the one that was clicked.
   *
   * None of them navigates, changes the selection or touches the composer.
   * Pinning is the #474 hook's optimistic write and rollback, marking read is
   * the same pair the navigation effect performs, muting is the equivalent for
   * notifications, and rename, details and leave only open something.
   */
  function handleAction(target: ConversationTarget, action: ConversationActionId) {
    const apiTarget = { kind: conversationTargetKind(target.kind), targetId: target.id };
    const effects: Record<ConversationActionId, () => void> = {
      pin: () => handlePin(apiTarget.kind, target.id, false),
      unpin: () => handlePin(apiTarget.kind, target.id, true),
      "mark-read": () => markRead?.(apiTarget),
      mute: () => handleMute(target, true),
      unmute: () => handleMute(target, false),
      rename: () => setRenamingId(target.id),
      details: () => onOpenDetails?.(apiTarget.kind, target.id),
      leave: () => setLeavingId(target.id),
    };
    effects[action]();
  }

  const rowActions: RowActionsProps = {
    openTargetKey: openMenuKey,
    onOpenChange: handleMenuOpenChange,
    onAction: handleAction,
  };

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
      aria-label="Navegação do workspace Nchat"
      data-testid="chat-sidebar"
    >
      {/* ── Brand ── */}
      <Link to="/chat" className="chat-sidebar__brand" aria-label="Nchat — Workspace Nic-Labs">
        <img
          src="/assets/icononly_transparent.png"
          alt=""
          width={30}
          height={34}
          className="chat-sidebar__brand-img"
        />
        <div className="chat-sidebar__brand-copy">
          <p className="chat-sidebar__brand-title">Nchat</p>
          <p className="chat-sidebar__brand-sub">Workspace Nic-Labs</p>
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
              <ChannelsByCategory
                grouped={groupedChannelsByCategory}
                activeChannelId={activeChannelId}
                onSelect={handleChannelSelect}
                actions={rowActions}
                collapsed={collapsedCategories}
                onToggleCategory={toggleCategory}
              />
            </Section>

            <Section labelId={DIRECTS_LABEL_ID} title="Mensagens diretas" spaced>
              <DMList
                dms={orderedDirects}
                activeDMId={activeDMId}
                onSelect={handleDMSelect}
                labelId={DIRECTS_LABEL_ID}
                emptyMessage="Nenhuma mensagem direta."
                actions={rowActions}
              />
            </Section>

            <Section labelId={GROUPS_LABEL_ID} title="Grupos" spaced>
              <DMList
                dms={orderedGroups}
                activeDMId={activeDMId}
                onSelect={handleDMSelect}
                labelId={GROUPS_LABEL_ID}
                emptyMessage="Nenhum grupo."
                actions={rowActions}
              />
            </Section>
          </>
        )}
        {actionError && (
          <p className="chat-sidebar__pin-error" role="alert">
            {actionError}
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
      <SidebarRenameDialog
        channels={channels}
        dms={dms}
        targetId={renamingId}
        onClose={() => setRenamingId(null)}
        onRenameChannel={renameChannel}
        onRenameGroup={renameGroup}
      />
      <SidebarLeaveDialog
        channels={channels}
        dms={dms}
        targetId={leavingId}
        onClose={() => setLeavingId(null)}
        onLeave={leaveConversation}
      />
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
