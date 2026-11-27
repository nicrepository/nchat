import { useCallback, useEffect, useRef, useState } from "react";
import type {
  FocusEvent as ReactFocusEvent,
  KeyboardEvent as ReactKeyboardEvent,
  MouseEvent as ReactMouseEvent,
  PointerEvent as ReactPointerEvent,
  RefObject,
} from "react";

import type { CallParticipantProfile } from "./chatApi";
import type {
  LinkSafetyRecheck,
  MentionTarget,
  Message,
  MessageAcknowledgement,
} from "./chatTypes";
import type { EmojiUsage } from "./emoji/emojiUsage";
import MessageAcknowledgementStrip from "./MessageAcknowledgement";
import MessageContent from "./MessageContent";
import MessageEditHistory from "./MessageEditHistory";
import MessageToolbar from "./MessageToolbar";
import { formatTime, senderLabel } from "./messageDisplay";
import { presenceLabel, usePresence, type PresenceState } from "./presence";
import { UserAvatar } from "./UserAvatar";
import type { MentionInteraction } from "./RichTextRenderer";
import { useMessageEditing } from "./useMessageEditing";
import { CONTROL_SELECTOR, useMessageTouchGestures } from "./useMessageTouchGestures";
import { reactionMenuId } from "./message-area/timeline/useReactionMenu";
import { anchorOutOfSight, selectionHeaderClass } from "./message-area/timeline/useSelectionMode";

export interface MessageBubbleProps {
  message: Message;
  workspaceId?: string;
  /** True when this message is from the viewing user (we don't know real user ID here). */
  isMine?: boolean;
  /** True when consecutive messages from the same sender within the same minute. */
  isGrouped?: boolean;
  onToggleReaction: (messageId: string, emoji: string) => void;
  onReplyMessage: (message: Message) => void;
  onReferenceMessage: (message: Message) => void;
  onForwardMessage?: (message: Message) => void;
  onToggleFavorite: (messageId: string, isFavorited: boolean) => void;
  onEditMessage: (
    messageId: string,
    body: string,
    bodyFormat: Message["bodyFormat"],
  ) => Promise<Message>;
  onEditForbidden: (messageId: string) => void;
  onDeleteMessage: (messageId: string) => Promise<void>;
  editDisabled?: boolean;
  mentionTarget?: MentionTarget;
  /** The conversation on screen; presence is resolved within it (RF-58). */
  presenceTarget?: string;
  /** RF-05: pin/unpin action for readable channels and DMs. */
  onTogglePin?: (messageId: string, pin: boolean) => void;
  /** RF-05: whether this message is currently pinned in the active target. */
  isPinned?: boolean;
  recentReactionEmojis: string[];
  /** Local emoji history and skin tone, owned by the conversation (issue #496). */
  emojiUsage: EmojiUsage;
  onEmojiToneChange: (tone: number) => void;
  /** The reader, so a reaction tooltip can say "Você" rather than their name. */
  currentUserId: string;
  onOpenAuthorDM?: (message: Message) => void;
  openingAuthorDM?: boolean;
  /** Opens a DM when a `@user` mention in the message body is clicked (issue #795). */
  mentionInteraction?: MentionInteraction;
  reactionMenuVisible: boolean;
  onReactionMenuVisibleChange: (messageId: string, visible: boolean) => void;
  pickerOpen: boolean;
  onPickerOpenChange: (messageId: string, open: boolean) => void;
  /** True when this message's menu was opened by a long press (issue #1152). */
  reactionMenuTouch?: boolean;
  /** Opens this message's menu from a long press (issue #1152). */
  onReactionMenuLongPress?: (messageId: string) => void;
  /**
   * Selected for its actions on touch (issue #1238): the header shows its
   * actions and a way back, the bubble a ring.
   */
  selected?: boolean;
  /** Selects this message — a tap, or keyboard focus, on touch (issue #1238). */
  onMessageSelect?: (messageId: string) => void;
  /** A touch menu or picker is open somewhere: a swipe must not reply under it. */
  gesturesBlocked?: boolean;
  /** Closes what this message has open when it is removed or unmounted (issue #1152). */
  onReactionMenuRelease?: (messageId: string) => void;
  quoteAuthorLabel?: string;
  canJumpToQuote?: boolean;
  onQuoteJump?: (messageId: string) => void;
  onReferenceJump?: (reference: NonNullable<Message["reference"]>) => void;
  isHighlighted?: boolean;
  setMessageRef?: (messageId: string, el: HTMLElement | null) => void;
  /**
   * RF-21 "Verificar novamente" (issue #135). Asks the server to re-read what it
   * already knows about this message's unverified links; it never starts a new
   * scan, and the notice's copy must not imply that it does.
   *
   * Optional: without it the notice is still shown, just without the action. The
   * warning is the important half — the action is a convenience.
   */
  onReconcileLinkSafety?: (messageId: string) => Promise<LinkSafetyRecheck | undefined>;
  /**
   * Issue #824. The server's own summary for this message, absent while it is
   * still being read and for every message that asked nobody — which is almost
   * all of them, so the strip below draws nothing at all in the common case.
   */
  acknowledgement?: MessageAcknowledgement;
  /** True while this message's confirmation is in flight. */
  acknowledging?: boolean;
  /**
   * Confirms receipt. Optional: without it the strip still reports the state,
   * which is the half that matters — an action nobody wired is better absent
   * than broken.
   */
  onAcknowledge?: (messageId: string) => void;
  /**
   * Issue #846. Reads this message's full per-recipient detail, for the
   * details popover the sender's summary opens. Optional, like onAcknowledge:
   * without it the summary counts still render, just without the popover.
   */
  onOpenAcknowledgementDetails?: (messageId: string) => void;
  /** Issue #846. See TimelineRowContext.resolveRecipientIdentities. */
  resolveRecipientIdentities?: (
    userIds: string[],
    signal?: AbortSignal,
  ) => Promise<CallParticipantProfile[]>;
}

function MessageMeta({
  message,
  isMine,
  isGrouped,
  senderPresence,
  onOpenHistory,
  onOpenAuthorDM,
  openingAuthorDM,
}: Pick<
  MessageBubbleProps,
  "message" | "isMine" | "isGrouped" | "onOpenAuthorDM" | "openingAuthorDM"
> & {
  senderPresence: PresenceState;
  onOpenHistory: () => void;
}) {
  if (isGrouped && !message.isEdited) return null;
  const showSender = !isGrouped && !isMine;
  const label = senderLabel(message);
  return (
    <div className="chat-msg-area__msg-meta">
      {showSender && onOpenAuthorDM && (
        <button
          type="button"
          className="chat-msg-area__msg-sender chat-msg-area__msg-sender-action"
          aria-label={`Abrir conversa com ${label}`}
          aria-busy={openingAuthorDM}
          disabled={openingAuthorDM}
          data-testid="chat-msg-sender"
          onClick={() => onOpenAuthorDM(message)}
        >
          {label}
          {openingAuthorDM && <span className="sr-only">Abrindo conversa</span>}
        </button>
      )}
      {showSender && !onOpenAuthorDM && (
        <span className="chat-msg-area__msg-sender" data-testid="chat-msg-sender">
          {label}
        </span>
      )}
      {/* The dot next to the avatar is decorative and lives inside an
          aria-hidden wrapper, so without this the sender's state would be
          conveyed by colour alone. It sits beside the name, in the same place
          and under the same condition, so a screen reader hears the person and
          their state once per group rather than once per message — and says
          nothing at all when the server has not answered yet, since "unknown"
          is the absence of a state and not a fourth one. */}
      {showSender && senderPresence !== "unknown" && (
        <span className="sr-only" data-testid="chat-msg-sender-presence">
          {`Status: ${presenceLabel(senderPresence)}`}
        </span>
      )}
      {!isGrouped && (
        <span className="chat-msg-area__msg-time">{formatTime(message.createdAt)}</span>
      )}
      {message.isEdited && !message.isRemoved && (
        <button
          type="button"
          className="chat-msg-area__edited"
          aria-label="Ver histórico de edições"
          aria-haspopup="dialog"
          onClick={onOpenHistory}
        >
          (editada)
        </button>
      )}
    </div>
  );
}

/**
 * The sender's avatar column. Decorative, including the presence dot: the name
 * and the state are already in the meta row, where a screen reader reads them
 * once per group instead of once per message.
 */
function MessageAvatar({
  message,
  presence,
  workspaceId,
}: {
  message: Message;
  presence: PresenceState;
  workspaceId: string;
}) {
  return (
    <div className="chat-msg-area__msg-avatar" aria-hidden="true">
      <UserAvatar
        userId={message.senderId}
        workspaceId={workspaceId}
        displayName={message.senderDisplayName || message.senderEmail || ""}
        avatarUrl={message.senderAvatarUrl}
        presence={presence}
        size="sm"
        imageClassName="chat-msg-area__msg-avatar-img"
      />
    </div>
  );
}

function messageBodyClassName(message: Message): string {
  const removed = message.isRemoved ? " chat-msg-area__msg-bubble--removed" : "";
  const scanning =
    message.status === "pending_link_scan" ? " chat-msg-area__msg-bubble--pending-scan" : "";
  return `chat-msg-area__msg-bubble${removed}${scanning}`;
}

function messageShellClassName(flags: {
  isMine: boolean;
  isGrouped: boolean;
  isHighlighted: boolean;
  isSelected: boolean;
}): string {
  const mine = flags.isMine ? " chat-msg-area__msg--mine" : "";
  const grouped = flags.isGrouped ? " chat-msg-area__msg--grouped" : "";
  const highlighted = flags.isHighlighted ? " chat-msg-area__msg--highlight" : "";
  const selected = flags.isSelected ? " chat-msg-area__msg--selected" : "";
  return `chat-msg-area__msg${mine}${grouped}${highlighted}${selected}`;
}

/**
 * Whether focus at `node` is still the message's: inside its row, or inside a
 * surface portalled out of it — the picker, its tone palette, and a touch menu
 * without the top layer (issue #1238) — each marked with the message.
 */
function ownsFocus(row: HTMLElement, node: EventTarget | null, messageId: string): boolean {
  if (!(node instanceof Element)) return false;
  if (row.contains(node)) return true;
  const surface = node.closest("[data-reaction-picker-for]");
  return surface?.getAttribute("data-reaction-picker-for") === messageId;
}

/**
 * How long after a touch the browser's compatibility mouse events and the focus
 * a tap gives the row still belong to that touch.
 */
const touchEchoMs = 800;

/**
 * The pointer and focus handlers that reveal the hover toolbar.
 *
 * Both "leave" handlers check containment, because moving into the toolbar —
 * which is a descendant — must not read as leaving the message.
 *
 * A touch reveals nothing (issue #1152): a tap fires mouseenter and focuses the
 * row as compatibility events, and a scroll starts with a touch too, so showing
 * on any of them opened the toolbar under every finger. Touch has its own way
 * in — the long press — so those echoes are ignored, while mouse hover and
 * keyboard focus behave exactly as before.
 */
function useToolbarReveal(
  messageId: string,
  onVisibleChange: (messageId: string, visible: boolean) => void,
  touchOpen: boolean,
) {
  const lastTouchRef = useRef(Number.NEGATIVE_INFINITY);
  const markTouch = useCallback((event: ReactPointerEvent<HTMLDivElement>) => {
    if (event.pointerType === "touch") lastTouchRef.current = event.timeStamp;
  }, []);
  const show = useCallback(
    (event: { timeStamp: number }) => {
      if (event.timeStamp - lastTouchRef.current < touchEchoMs) return;
      onVisibleChange(messageId, true);
    },
    [messageId, onVisibleChange],
  );
  // Focus moving inside the message — into its menu, or handed back to the row
  // when Escape closes that menu — is not the reader arriving: revealing on it
  // would reopen what they just closed.
  const showOnFocus = useCallback(
    (event: ReactFocusEvent<HTMLDivElement>) => {
      if (!ownsFocus(event.currentTarget, event.relatedTarget, messageId)) show(event);
    },
    [messageId, show],
  );
  // Nor does a mouse leaving the row end a menu opened by touch (CQR R10 F-1 of
  // issue #1238): a finger hovers nothing, and the mouse WebKit synthesises
  // after a rotation leaves a row that moved — dropping the touch menu, picker
  // and focus with it. That menu ends on its own terms, as another message's
  // touch menu already did (useReactionMenu).
  const hideOnLeave = useCallback(
    (event: ReactMouseEvent<HTMLDivElement>) => {
      if (touchOpen) return;
      const nextTarget = event.relatedTarget;
      if (!(nextTarget instanceof Node) || !event.currentTarget.contains(nextTarget)) {
        onVisibleChange(messageId, false);
      }
    },
    [messageId, onVisibleChange, touchOpen],
  );
  // Focus going into the message's portalled surfaces — the search field
  // taking focus once the catalog loads, the palette its tone — stays with the
  // message: hiding on it closed a touch-opened menu, picker and all. So does
  // focus a touch drops to nowhere (CQR R4 of issue #1238): WebKit does not
  // focus a tapped button, so a tap on the menu's own buttons blurred to the
  // body and dropped the touch menu before its click arrived.
  const hideOnBlur = useCallback(
    (event: ReactFocusEvent<HTMLDivElement>) => {
      if (ownsFocus(event.currentTarget, event.relatedTarget, messageId)) return;
      const tapEcho = event.timeStamp - lastTouchRef.current < touchEchoMs;
      if (event.relatedTarget === null && tapEcho) return;
      onVisibleChange(messageId, false);
    },
    [messageId, onVisibleChange],
  );
  return {
    onMouseEnter: show,
    onMouseLeave: hideOnLeave,
    onFocus: showOnFocus,
    onBlur: hideOnBlur,
    onPointerDownCapture: markTouch,
    onPointerUpCapture: markTouch,
  };
}

/**
 * Swipe-to-reply and long-press for one message (issue #1152). The gestures
 * are recognised by useMessageTouchGestures; what they are allowed to do is
 * decided here, against the message as it is when the finger lifts — a reply
 * is only ever the existing reply action, for the message the gesture started
 * on, and never for one that has since been removed.
 */
function useMessageGestures(props: MessageBubbleProps) {
  const { message, onReplyMessage, onReactionMenuLongPress } = props;
  const replyIfStillValid = useCallback(
    (messageId: string) => {
      if (messageId === message.id && !message.isRemoved) onReplyMessage(message);
    },
    [message, onReplyMessage],
  );
  return useMessageTouchGestures({
    messageId: message.id,
    disabled: message.isRemoved || !onReactionMenuLongPress,
    swipeBlocked: props.gesturesBlocked ?? false,
    onLongPress: (messageId) => onReactionMenuLongPress?.(messageId),
    onSwipeReply: replyIfStillValid,
  });
}

/** Where there is no hover to reveal a message's actions (see its CSS). */
const touchQuery = "(hover: none), (pointer: coarse)";

/**
 * Watches a selected row and calls `onOut` once it is out of the timeline's
 * view (CQR R3/R4 of issue #1238). A reading out of view ends it, the first one
 * included — focus is no proof of visibility: a row focused with
 * `preventScroll`, or handed focus as its menu closes, may be anywhere.
 *
 * The one reading it does not take at its word is the first one after the
 * keyboard arrived at the row (`keyboardEntry`): WebKit scrolls a focused
 * element in after the focus event. That reading is reconciled instead, once
 * the browser has had its frame to scroll — measured against the list's band
 * and the visual viewport — and ends the selection if the row is still out.
 * A later reading in view settles it as well.
 *
 * The list's observer sees the list, not what the reader sees of it (CQR R5):
 * pinch zoom, the on-screen keyboard and rotation move or resize the visual
 * viewport and leave the list's layout alone. So a reading in the list counts
 * only if the row is in the visual viewport too, and each of those changes is
 * reconciled the same way — one pending check at a time, cancelled with the
 * watch. Returns the cleanup.
 */
function watchSelectedRow(
  row: HTMLElement,
  root: Element,
  keyboardEntry: boolean,
  onOut: () => void,
): () => void {
  let pendingEntry = keyboardEntry;
  let frame = 0;
  const reconcile = () => {
    cancelAnimationFrame(frame);
    frame = requestAnimationFrame(() => {
      frame = requestAnimationFrame(() => {
        if (anchorOutOfSight(row)) onOut();
      });
    });
  };
  const observer = new IntersectionObserver(
    ([entry]) => {
      const entering = pendingEntry;
      pendingEntry = false;
      if (entry.isIntersecting && !anchorOutOfSight(row)) return;
      if (entering) reconcile();
      else onOut();
    },
    { root },
  );
  observer.observe(row);
  const view = window.visualViewport;
  view?.addEventListener("resize", reconcile);
  view?.addEventListener("scroll", reconcile);
  window.addEventListener("resize", reconcile);
  return () => {
    observer.disconnect();
    cancelAnimationFrame(frame);
    view?.removeEventListener("resize", reconcile);
    view?.removeEventListener("scroll", reconcile);
    window.removeEventListener("resize", reconcile);
  };
}

/**
 * Ends a selection whose row went out of sight (CQR R5 of issue #1238). Focus
 * left on that row, on the header's selection controls — which go with the
 * selection — or on nothing, its menu having closed under it, goes to the
 * conversation's list, the place #675 gives focus that lost its row. Focus the
 * reader put anywhere else stays there.
 */
function endUnseenSelection(row: HTMLElement, list: Element, release: () => void) {
  const active = document.activeElement;
  const adrift =
    !active ||
    active === document.body ||
    row.contains(active) ||
    !!active.closest(`.${selectionHeaderClass}`);
  if (adrift && list instanceof HTMLElement) list.focus({ preventScroll: true });
  release();
}

/**
 * How a message becomes, and stops being, the selected one on touch (CQR R3 of
 * issue #1238). A tap on it — not on a link, a mention, an attachment or any
 * other control inside it, whose tap stays theirs — or keyboard focus arriving
 * at it selects it; a programmatic focus (a jump to the message, a menu handing
 * focus back) does not. Enter on the selected row opens its menu, the
 * keyboard's way to what the header's actions button opens.
 *
 * Selected with nothing open, it watches whether it is still in the timeline's
 * view — one observer, for the selected message only — and ends the selection
 * once it has scrolled out: the header would otherwise act on a message the
 * reader no longer sees. An open menu or picker has its own verdict (#839).
 */
function useMessageSelection(props: MessageBubbleProps, rowRef: RefObject<HTMLDivElement | null>) {
  const { message, selected = false, onMessageSelect, onReactionMenuRelease } = props;
  const surfaceOpen = !!props.reactionMenuTouch || props.pickerOpen;
  // Set by the keyboard arriving at the row, read once by the next watch.
  const keyboardEntryRef = useRef(false);
  useEffect(() => {
    const row = rowRef.current;
    const root = row?.closest(".chat-msg-area__list");
    if (!selected || surfaceOpen || !row || !root || !onReactionMenuRelease) return;
    if (typeof IntersectionObserver === "undefined") return;
    const entering = keyboardEntryRef.current;
    keyboardEntryRef.current = false;
    return watchSelectedRow(row, root, entering, () =>
      endUnseenSelection(row, root, () => onReactionMenuRelease(message.id)),
    );
  }, [message.id, onReactionMenuRelease, rowRef, selected, surfaceOpen]);

  const selectable = !!onMessageSelect && !message.isRemoved;
  const select = () => {
    if (!selectable || !window.matchMedia?.(touchQuery).matches) return false;
    onMessageSelect(message.id);
    return true;
  };
  return {
    onClick: (event: ReactMouseEvent<HTMLDivElement>) => {
      if (!(event.target as Element).closest(CONTROL_SELECTOR)) select();
    },
    onFocus: (event: ReactFocusEvent<HTMLDivElement>) => {
      const row = event.currentTarget;
      const arriving = !ownsFocus(row, event.relatedTarget, message.id);
      if (event.target !== row || !arriving || !row.matches(":focus-visible")) return;
      // Only a selection this focus starts gets a new watch to tell it about.
      if (!selected && select()) keyboardEntryRef.current = true;
    },
    onKeyDown: (event: ReactKeyboardEvent<HTMLDivElement>) => {
      if (event.key !== "Enter" || event.target !== event.currentTarget || !selected) return;
      event.preventDefault();
      props.onReactionMenuLongPress?.(message.id);
    },
  };
}

/**
 * A message that is removed, or whose row is unmounted or recycled for another
 * message, takes the touch menu, picker or selection it owns with it (issues
 * #1152, #1238):
 * otherwise the shared state still names it, every swipe stays blocked behind
 * a menu nobody can see, and the menu reappears when the message comes back.
 * The release names this message, so it never closes another one's.
 */
function useReleaseSurfacesOnExit(props: MessageBubbleProps) {
  const { message, onReactionMenuRelease: release } = props;
  // Read when the message leaves, not tracked as a dependency: a menu merely
  // closing (the picker handing focus back to it, say) is not the message
  // leaving, and must not take the rest of its state with it.
  const ownsRef = useRef(false);
  useEffect(() => {
    ownsRef.current = !!props.reactionMenuTouch || props.pickerOpen || !!props.selected;
  });
  useEffect(() => {
    if (!release) return;
    if (message.isRemoved && ownsRef.current) release(message.id);
    return () => {
      if (ownsRef.current) release(message.id);
    };
  }, [message.id, message.isRemoved, release]);
}

/**
 * Everything beside the avatar: who and when, what was said, what can be done to
 * it, and the edit history when it is open.
 *
 * This is where a message's own state lives — the editor, a delete in flight,
 * the history dialog — so the shell around it stays a layout decision and
 * nothing else.
 */
function MessageBubbleBody({
  props,
  senderPresence,
  menuId,
}: {
  props: MessageBubbleProps;
  senderPresence: PresenceState;
  menuId: string;
}) {
  const { message, isMine = false, isGrouped = false } = props;
  const { onReactionMenuVisibleChange } = props;
  const bubbleRef = useRef<HTMLDivElement>(null);
  const [historyOpen, setHistoryOpen] = useState(false);

  const hideToolbar = useCallback(
    () => onReactionMenuVisibleChange(message.id, false),
    [message.id, onReactionMenuVisibleChange],
  );
  const editing = useMessageEditing({
    message,
    isMine,
    editDisabled: props.editDisabled ?? false,
    onEditMessage: props.onEditMessage,
    onEditForbidden: props.onEditForbidden,
    onDeleteMessage: props.onDeleteMessage,
    onHideToolbar: hideToolbar,
  });

  return (
    <div className="chat-msg-area__msg-body">
      <MessageMeta
        message={message}
        isMine={isMine}
        isGrouped={isGrouped}
        senderPresence={senderPresence}
        onOpenHistory={() => setHistoryOpen(true)}
        onOpenAuthorDM={props.onOpenAuthorDM}
        openingAuthorDM={props.openingAuthorDM}
      />
      <div ref={bubbleRef} className={messageBodyClassName(message)}>
        <MessageContent
          message={message}
          mentionTarget={props.mentionTarget}
          editing={editing.editing}
          onSaveEdit={editing.saveEdit}
          onCancelEdit={editing.cancelEdit}
          onEditForbidden={editing.handleForbidden}
          quoteAuthorLabel={props.quoteAuthorLabel}
          canJumpToQuote={props.canJumpToQuote}
          onQuoteJump={props.onQuoteJump}
          onReferenceJump={props.onReferenceJump}
          onReconcileLinkSafety={props.onReconcileLinkSafety}
          mentionInteraction={props.mentionInteraction}
        />
        {props.onAcknowledge ? (
          <MessageAcknowledgementStrip
            messageId={message.id}
            workspaceId={props.workspaceId}
            acknowledgement={props.acknowledgement}
            senderId={message.senderId}
            currentUserId={props.currentUserId}
            submitting={props.acknowledging ?? false}
            onAcknowledge={props.onAcknowledge}
            onOpenDetails={props.onOpenAcknowledgementDetails}
            resolveIdentities={props.resolveRecipientIdentities}
          />
        ) : null}
      </div>
      <MessageToolbar
        message={message}
        isMine={isMine}
        bubbleRef={bubbleRef}
        currentUserId={props.currentUserId}
        recentReactionEmojis={props.recentReactionEmojis}
        emojiUsage={props.emojiUsage}
        onEmojiToneChange={props.onEmojiToneChange}
        onToggleReaction={props.onToggleReaction}
        onReplyMessage={props.onReplyMessage}
        onReferenceMessage={props.onReferenceMessage}
        onForwardMessage={props.onForwardMessage}
        onToggleFavorite={props.onToggleFavorite}
        onTogglePin={props.onTogglePin}
        isPinned={props.isPinned ?? false}
        reactionMenuVisible={props.reactionMenuVisible || props.pickerOpen}
        onReactionMenuVisibleChange={onReactionMenuVisibleChange}
        pickerOpen={props.pickerOpen}
        onPickerOpenChange={props.onPickerOpenChange}
        touchMode={props.reactionMenuTouch}
        menuId={menuId}
        onStartEdit={editing.startEdit}
        onDelete={editing.requestDelete}
        deleting={editing.deleting}
      />
      {historyOpen && !message.isRemoved && (
        <MessageEditHistory messageId={message.id} onClose={() => setHistoryOpen(false)} />
      )}
    </div>
  );
}

export default function MessageBubble(props: MessageBubbleProps) {
  const { message, isMine = false, isGrouped = false, isHighlighted = false } = props;
  // Read for every message, including the user's own, so the hook order does
  // not depend on who wrote it. Only the other party's avatar is drawn.
  //
  // Scoped to the conversation being read: the sender wrote here, so this
  // conversation's roster is the one that would have named them.
  const senderPresence = usePresence(message.senderId, props.presenceTarget);
  const reveal = useToolbarReveal(
    message.id,
    props.onReactionMenuVisibleChange,
    !!props.reactionMenuTouch,
  );
  const gestures = useMessageGestures(props);
  useReleaseSurfacesOnExit(props);
  const rowRef = useRef<HTMLDivElement | null>(null);
  const selection = useMessageSelection(props, rowRef);
  const menuId = reactionMenuId(message.id);

  return (
    <div
      ref={(el) => {
        rowRef.current = el;
        props.setMessageRef?.(message.id, el);
      }}
      className={messageShellClassName({
        isMine,
        isGrouped,
        isHighlighted,
        isSelected: !!props.selected,
      })}
      data-testid="chat-msg-bubble"
      data-message-id={message.id}
      tabIndex={0}
      {...reveal}
      {...gestures}
      onFocus={(event) => {
        reveal.onFocus(event);
        selection.onFocus(event);
      }}
      onClick={selection.onClick}
      onKeyDown={selection.onKeyDown}
    >
      {!isMine && (
        <MessageAvatar
          message={message}
          presence={senderPresence}
          workspaceId={props.workspaceId ?? ""}
        />
      )}
      <MessageBubbleBody props={props} senderPresence={senderPresence} menuId={menuId} />
    </div>
  );
}
