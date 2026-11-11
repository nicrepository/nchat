/**
 * The reaction badges under a message, and the toolbar that appears over it.
 *
 * Split out of MessageBubble (issue #496, CQ follow-up): the bubble is about
 * showing what was said, this is about what can be done to it. The two shared a
 * file and a component, and the picker's placement rules had nowhere of their
 * own to live.
 */

import { lazy, Suspense, useCallback, useEffect, useLayoutEffect, useRef } from "react";
import { createPortal } from "react-dom";
import type { MouseEvent as ReactMouseEvent, RefObject } from "react";

import type { Message } from "./chatTypes";
import type { EmojiUsage } from "./emoji/emojiUsage";
import ReactionBadge from "./ReactionBadge";
import { useReactionPresence } from "./useReactionPresence";
import {
  anchorIsVisible,
  bandBeside,
  intersectBounds,
  placeAgainstAnchor,
  useAnchoredPicker,
  viewportPadding,
  visibleBounds,
  visualViewportBounds,
  type VisibleBounds,
} from "./emoji/useAnchoredPicker";

/**
 * The full picker and its catalog are a chunk of their own (issue #496): a
 * conversation that never opens one never downloads a thousand emoji names.
 */
const EmojiPicker = lazy(() => import("./emoji/EmojiPicker"));

/**
 * Whether this browser can put the toolbar in the top layer (issue #839).
 *
 * Decided once, not per render: the attribute must only ever be written where
 * togglePopover exists to show it — the UA stylesheet hides a popover until it
 * is shown, and a browser (or jsdom) that knows the attribute but not the
 * method would never show it. Without it the toolbar stays where the DOM puts
 * it, which is what it did before.
 */
const popoverSupported =
  typeof HTMLElement !== "undefined" && "togglePopover" in HTMLElement.prototype;

/**
 * Distance kept between the toolbar and the bubble, above it and below it.
 *
 * Small enough that the toolbar reads as attached to its own bubble rather
 * than the one above it (issue #852) — six pixels of border/shadow around the
 * toolbar already read as separation, so the gap itself only needs to keep it
 * from touching the bubble, not carry the whole visual distance on its own.
 */
const toolbarGap = 3;

/** The bottom-sheet presentation of the same menu, for touch (issue #1152). */
const sheetClass = "chat-msg-area__reaction-menu--sheet";

/**
 * A touch menu and its picker, when the two do not fit the screen together
 * (issue #1152): the menu keeps only the picker's own button and the picker
 * drops its category tabs, so at least one row of results is left whole. Both
 * come back when the picker closes.
 */
const crampedMenuClass = "chat-msg-area__reaction-menu--cramped";
const crampedPickerClass = "chat-emoji-surface--cramped";

/**
 * Whether a placed picker shows no whole row of results: its results area is
 * shorter than the distance from its top to the bottom of the first cell, at
 * any scroll position. No cell — still loading, or nothing found — is nothing
 * to judge yet.
 */
function showsNoResultRow(picker: HTMLElement): boolean {
  const results = picker.querySelector(".chat-emoji-picker__sections");
  const cell = results?.querySelector(".chat-emoji-picker__emoji");
  if (!results || !cell) return false;
  const firstRow =
    cell.getBoundingClientRect().bottom - results.getBoundingClientRect().top + results.scrollTop;
  return results.clientHeight < firstRow;
}

export interface MessageToolbarProps {
  message: Message;
  /** Placement mirrors for the reader's own messages, which sit on the right. */
  isMine: boolean;
  bubbleRef: RefObject<HTMLDivElement | null>;
  /** The reader, so a reaction tooltip can say "Você" rather than their name. */
  currentUserId: string;
  recentReactionEmojis: string[];
  emojiUsage: EmojiUsage;
  onEmojiToneChange: (tone: number) => void;
  onToggleReaction: (messageId: string, emoji: string) => void;
  onReplyMessage: (message: Message) => void;
  onReferenceMessage: (message: Message) => void;
  onForwardMessage?: (message: Message) => void;
  onToggleFavorite: (messageId: string, isFavorited: boolean) => void;
  onTogglePin?: (messageId: string, pin: boolean) => void;
  isPinned: boolean;
  reactionMenuVisible: boolean;
  onReactionMenuVisibleChange: (messageId: string, visible: boolean) => void;
  pickerOpen: boolean;
  onPickerOpenChange: (messageId: string, open: boolean) => void;
  /**
   * The menu was opened by a long press (issue #1152): touch-sized targets,
   * a bottom sheet when it fits nowhere beside its bubble, and Escape or a tap
   * elsewhere closes it, since there is no pointer leaving to do that.
   */
  touchMode?: boolean;
  /** The menu's id, for the actions button that opens it (aria-controls). */
  menuId?: string;
  onStartEdit?: () => void;
  onDelete?: () => void;
  deleting: boolean;
}

type MessageActionButtonsProps = Pick<
  MessageToolbarProps,
  | "message"
  | "onReplyMessage"
  | "onReferenceMessage"
  | "onForwardMessage"
  | "onToggleFavorite"
  | "onTogglePin"
  | "isPinned"
  | "onStartEdit"
  | "onDelete"
  | "deleting"
>;

/**
 * The two actions the author of a message has over it. They are a pair — both
 * conditional on the caller offering them, and the delete button carries an
 * in-flight state the others do not — so they are extracted together.
 */
function MessageEditDeleteButtons({
  onStartEdit,
  onDelete,
  deleting,
}: Pick<MessageToolbarProps, "onStartEdit" | "onDelete" | "deleting">) {
  return (
    <>
      {onStartEdit && (
        <button type="button" aria-label="Editar mensagem" onClick={onStartEdit}>
          <span className="material-symbols-outlined" aria-hidden="true">
            edit
          </span>
        </button>
      )}
      {onDelete && (
        <button
          type="button"
          aria-label={deleting ? "Excluindo mensagem" : "Excluir mensagem"}
          aria-busy={deleting}
          disabled={deleting}
          onClick={onDelete}
        >
          <span className="material-symbols-outlined" aria-hidden="true">
            {deleting ? "progress_activity" : "delete"}
          </span>
        </button>
      )}
    </>
  );
}

/**
 * Everything the hover toolbar offers that is not a reaction: reply, quote,
 * forward, edit, delete, favourite and pin.
 */
function MessageActionButtons({
  message,
  onReplyMessage,
  onReferenceMessage,
  onForwardMessage,
  onToggleFavorite,
  onTogglePin,
  isPinned,
  onStartEdit,
  onDelete,
  deleting,
}: MessageActionButtonsProps) {
  return (
    <>
      <button type="button" aria-label="Responder" onClick={() => onReplyMessage(message)}>
        <span className="material-symbols-outlined" aria-hidden="true">
          reply
        </span>
      </button>
      <button
        type="button"
        aria-label="Citar em outra conversa"
        onClick={() => onReferenceMessage(message)}
      >
        <span className="material-symbols-outlined" aria-hidden="true">
          format_quote
        </span>
      </button>
      {onForwardMessage && message.kind === "user" && (
        <button type="button" aria-label="Encaminhar" onClick={() => onForwardMessage(message)}>
          <span className="material-symbols-outlined" aria-hidden="true">
            forward
          </span>
        </button>
      )}
      <MessageEditDeleteButtons onStartEdit={onStartEdit} onDelete={onDelete} deleting={deleting} />
      <button
        type="button"
        className={message.isFavorited ? "chat-msg-area__favorite--active" : undefined}
        aria-label={message.isFavorited ? "Remover dos favoritos" : "Favoritar mensagem"}
        aria-pressed={message.isFavorited}
        onClick={() => onToggleFavorite(message.id, !message.isFavorited)}
      >
        <span className="material-symbols-outlined" aria-hidden="true">
          star
        </span>
      </button>
      {onTogglePin && (
        <button
          type="button"
          className={isPinned ? "chat-msg-area__pin--active" : undefined}
          aria-label={isPinned ? "Desafixar mensagem" : "Fixar mensagem"}
          aria-pressed={isPinned}
          onClick={() => onTogglePin(message.id, !isPinned)}
        >
          <span className="material-symbols-outlined" aria-hidden="true">
            keep
          </span>
        </button>
      )}
    </>
  );
}

type PlacementInput = Pick<
  MessageToolbarProps,
  | "isMine"
  | "bubbleRef"
  | "reactionMenuVisible"
  | "pickerOpen"
  | "onPickerOpenChange"
  | "onReactionMenuVisibleChange"
  | "touchMode"
> & { messageId: string };

interface Placement {
  menuRef: RefObject<HTMLDivElement | null>;
  anchorRef: RefObject<HTMLButtonElement | null>;
  pickerRef: RefObject<HTMLDivElement | null>;
  closePicker: (restoreFocus: boolean) => void;
  dismiss: () => void;
  /** Where the picker's tone palette may sit; absent for a hover toolbar. */
  toneBounds?: () => VisibleBounds;
}

/**
 * The bottom edge of a message's own content — its reaction badges when it
 * has any, since those sit below its bubble and are still part of what
 * reads as belonging to it, or its bubble otherwise. `null` for a shell with
 * neither, such as a system event's own layout.
 */
function messageBottom(shell: Element): number | null {
  const box =
    shell.querySelector(".chat-msg-area__reactions") ??
    shell.querySelector(".chat-msg-area__msg-bubble");
  return box ? box.getBoundingClientRect().bottom : null;
}

/**
 * The bottom edge of the previous message, if one is mounted right before
 * this one (issue #852): placing the toolbar above the target must leave
 * that message alone too, not just the list's own edges, or a pair grouped
 * close together reads as the toolbar belonging to the wrong message.
 *
 * A virtualized row wraps the message shell in its own translated container
 * (issue #839), so the sibling that matters is the wrapper's, not the
 * shell's — walking the shell's own siblings would see nothing between rows.
 * A non-message row in between (a day divider) has nothing to compare
 * against, so it counts as no previous message rather than reaching past it.
 */
function previousBubbleBottom(anchor: Element): number | null {
  const shell = anchor.closest("[data-message-id]");
  const row = shell?.closest(".chat-msg-area__virtual-row") ?? shell;
  const sibling = row?.previousElementSibling;
  if (!sibling) return null;
  const previousShell = sibling.matches("[data-message-id]")
    ? sibling
    : sibling.querySelector("[data-message-id]");
  return previousShell ? messageBottom(previousShell) : null;
}

/**
 * The top edge of the next message's bubble, mirroring
 * {@link previousBubbleBottom} for the fallback placed below the target
 * (issue #852): a bubble the toolbar drops below must not then cross into
 * the one that comes after it either.
 */
function nextBubbleTop(anchor: Element): number | null {
  const shell = anchor.closest("[data-message-id]");
  const row = shell?.closest(".chat-msg-area__virtual-row") ?? shell;
  const sibling = row?.nextElementSibling;
  if (!sibling) return null;
  const nextShell = sibling.matches("[data-message-id]")
    ? sibling
    : sibling.querySelector("[data-message-id]");
  const bubble = nextShell?.querySelector(".chat-msg-area__msg-bubble");
  return bubble ? bubble.getBoundingClientRect().top : null;
}

/**
 * Places the toolbar beside its own bubble instead of above or below it
 * (issue #852): a normal single-line message sits closer to its neighbors
 * than the toolbar is tall, so above and below both cross into one of them
 * more often than not — squeezing the toolbar into that vertical band would
 * mean shrinking it or the row, and doing either changes what the timeline
 * is for a fix this small. The row's own horizontal room is usually wider
 * than the bubble, so sitting beside it — vertically centred on it, never
 * past the reader's own band — is the side that actually has space.
 */
function placeBeside(
  element: HTMLElement,
  anchor: DOMRect,
  box: DOMRect,
  isMine: boolean,
  gap: number,
  bounds: VisibleBounds,
): boolean {
  const left = isMine ? anchor.left - gap - box.width : anchor.right + gap;
  const fits = isMine
    ? left >= bounds.left + viewportPadding
    : left + box.width <= bounds.right - viewportPadding;
  if (!anchorIsVisible(anchor, bounds) || !fits) {
    element.style.visibility = "hidden";
    return false;
  }
  const centered = anchor.top + anchor.height / 2 - box.height / 2;
  const top = Math.min(
    Math.max(bounds.top + viewportPadding, centered),
    bounds.bottom - box.height - viewportPadding,
  );
  element.style.left = `${left}px`;
  element.style.top = `${top}px`;
  element.style.visibility = "visible";
  return true;
}

/**
 * The toolbar against its own bubble, as on desktop (issues #839, #852):
 * beside it first, then above or below inside the list's visible band and
 * clear of the neighbouring messages. Returns whether it was placed.
 */
function placeAnchored(
  menu: HTMLElement,
  anchor: HTMLElement,
  bubble: DOMRect,
  bounds: VisibleBounds,
  isMine: boolean,
): boolean {
  const box = menu.getBoundingClientRect();
  const midX = bubble.left + bubble.width / 2;
  const left = isMine ? midX - box.width : midX;
  // Beside its own bubble is where the toolbar sits by default (issue
  // #852): a stack of messages almost always sits closer together than
  // the toolbar is tall, so above/below either cross into a neighbor
  // outright or leave only a pointer's-width of clearance to aim at — a
  // hitbox the reader cannot reliably hit without opening the wrong
  // message's toolbar instead. The row's own horizontal room is usually
  // wider than the bubble, so that is the side that actually has space,
  // and it is the same regardless of what sits above or below.
  let placed = placeBeside(menu, bubble, box, isMine, toolbarGap, bounds);
  // No horizontal room beside it: the list's own top/bottom edge, or a
  // neighboring message's bubble, is where it goes instead — confined to
  // what the reader can see of the list, so a bubble near its top edge
  // gets the toolbar below it rather than over the header, and narrowed
  // further by the immediate neighbor so a pair grouped close together
  // does not cross into it (with the same edge padding the list's own
  // edges get, for the same aiming reason placeBeside is now preferred).
  // A bubble with no room on any side gets no toolbar rather than one
  // drawn where its message is not.
  if (!placed) {
    const previousBottom = previousBubbleBottom(anchor);
    const nextTop = nextBubbleTop(anchor);
    // A message genuinely before or after this one ends or begins at its
    // own edge; a reading that crosses into this one's own box is not one
    // to trust — it says nothing sane about the timeline — so the list's
    // own edge stays the bound instead of forcing a fallback the layout
    // does not call for.
    const constrainedBounds = {
      ...bounds,
      top:
        previousBottom !== null && previousBottom > bounds.top && previousBottom <= bubble.top
          ? previousBottom
          : bounds.top,
      bottom:
        nextTop !== null && nextTop < bounds.bottom && nextTop >= bubble.bottom
          ? nextTop
          : bounds.bottom,
    };
    placed = placeAgainstAnchor(menu, bubble, box, left, toolbarGap, toolbarGap, constrainedBounds);
  }
  return placed;
}

/** Whether the menu's width fits inside the band, edge padding included. */
function fitsBand(menu: HTMLElement, bounds: VisibleBounds): boolean {
  return menu.getBoundingClientRect().width <= bounds.right - bounds.left - 2 * viewportPadding;
}

/**
 * The menu as a bottom sheet (issue #1152), wholly inside the visual viewport:
 * zoomed in, the reader sees only part of the layout viewport a fixed element
 * is placed in, and the on-screen keyboard or the browser's own bars cover its
 * bottom — so `left: 0; right: 0; bottom: 0` would put the sheet off-screen or
 * under them. Edges, width and height limit all come from what is visible,
 * less the padding and the device's safe areas; past the limit it scrolls.
 */
function placeSheet(menu: HTMLElement) {
  const view = visualViewportBounds();
  const covered = Math.max(0, window.innerHeight - view.bottom);
  const pad = viewportPadding;
  menu.style.top = "";
  menu.style.left = `calc(${view.left + pad}px + env(safe-area-inset-left, 0px))`;
  menu.style.width = `calc(${view.right - view.left - 2 * pad}px - env(safe-area-inset-left, 0px) - env(safe-area-inset-right, 0px))`;
  menu.style.bottom = `calc(${covered + pad}px + env(safe-area-inset-bottom, 0px))`;
  menu.style.maxHeight = `calc(${view.bottom - view.top - 2 * pad}px - env(safe-area-inset-top, 0px) - env(safe-area-inset-bottom, 0px))`;
  menu.style.visibility = "visible";
}

/**
 * The band a toolbar may be drawn in: the list's visible band, and for a
 * touch-opened menu only the part of it the visual viewport shows (issue
 * #1152). The desktop keeps the band it was approved with (#839, #852).
 */
function placementBounds(anchor: Element, touchMode: boolean): VisibleBounds {
  const bounds = visibleBounds(anchor);
  return touchMode ? intersectBounds(bounds, visualViewportBounds()) : bounds;
}

/**
 * Where the hover toolbar sits, and when the picker it holds closes.
 *
 * The toolbar floats outside the message's own box, so it cannot be placed by
 * CSS alone. The picker's placement is not written here: it is the same problem
 * the composer's picker has, and useAnchoredPicker owns it for both.
 *
 * Placement is derived from the bubble's *current* box on every commit and on
 * every scroll or resize (issue #839). A prepend of older history re-keys the
 * virtual rows, moves them, remeasures them and compensates the scrollport —
 * any of which can happen while the toolbar is open — so nothing measured
 * earlier is kept: the message id resolves to whatever bubble is mounted for it
 * now, and that bubble's rect is the only input. A bubble that has left the
 * band the reader can see has nothing for the toolbar to hang off, so the
 * toolbar closes rather than float over the header or the composer.
 */
function useReactionPickerPlacement({
  messageId,
  isMine,
  bubbleRef,
  reactionMenuVisible,
  pickerOpen,
  onPickerOpenChange,
  onReactionMenuVisibleChange,
  touchMode,
}: PlacementInput): Placement {
  const menuRef = useRef<HTMLDivElement>(null);
  const anchorRef = useRef<HTMLButtonElement>(null);
  const sheetRef = useRef(false);

  const dismiss = useCallback(() => {
    onPickerOpenChange(messageId, false);
    onReactionMenuVisibleChange(messageId, false);
  }, [messageId, onPickerOpenChange, onReactionMenuVisibleChange]);

  const positionMenu = useCallback(() => {
    const anchor = bubbleRef.current;
    const menu = menuRef.current;
    if (!reactionMenuVisible) {
      sheetRef.current = false;
      return;
    }
    if (!anchor || !menu) return;
    const bubble = anchor.getBoundingClientRect();
    const bounds = placementBounds(anchor, !!touchMode);
    // The sheet is decided once per opening: measured again in its own
    // layout, the toolbar would flip between the two on every commit.
    // A touch menu wider than the list (touch-sized targets on a phone) is not
    // anchored at all: the horizontal clamp would push it off-screen left.
    const anchorable = !sheetRef.current && (!touchMode || fitsBand(menu, bounds));
    if (anchorable && placeAnchored(menu, anchor, bubble, bounds, isMine)) return;
    // Touch only (issue #1152): on a phone a bubble spans the row and its
    // neighbours sit closer than the toolbar is tall, so no anchored spot
    // exists and closing — the desktop verdict — left the reader with nothing.
    // The menu stays bound to its message, just not drawn beside it. An anchor
    // that has left the list's band still closes it, sheet or not.
    sheetRef.current = !!touchMode && anchorIsVisible(bubble, bounds);
    menu.classList.toggle(sheetClass, sheetRef.current);
    if (sheetRef.current) placeSheet(menu);
    else dismiss();
  }, [bubbleRef, dismiss, isMine, reactionMenuVisible, touchMode]);

  // No dependency list on purpose: every commit of the toolbar is a moment the
  // timeline may have moved its anchor, and re-reading one rect is cheaper than
  // knowing why it rendered.
  useLayoutEffect(() => {
    // Into the top layer before measuring — see the toolbar's CSS. A no-op
    // once shown.
    if (popoverSupported) menuRef.current?.togglePopover(true);
    // A closed picker gives the menu back the actions it set aside.
    if (!pickerOpen) menuRef.current?.classList.remove(crampedMenuClass);
    positionMenu();
  });

  // The timeline or the window moving under an open toolbar moves its anchor
  // too: the toolbar follows it, or closes when it has left the band the
  // reader sees. The same placement, and the same verdict, as on a commit.
  useEffect(() => {
    if (!reactionMenuVisible) return;
    document.addEventListener("scroll", positionMenu, true);
    window.addEventListener("resize", positionMenu);
    // The on-screen keyboard and pinch zoom move the visual viewport only:
    // resizing it, or panning it across a zoomed page.
    window.visualViewport?.addEventListener("resize", positionMenu);
    window.visualViewport?.addEventListener("scroll", positionMenu);
    return () => {
      document.removeEventListener("scroll", positionMenu, true);
      window.removeEventListener("resize", positionMenu);
      window.visualViewport?.removeEventListener("resize", positionMenu);
      window.visualViewport?.removeEventListener("scroll", positionMenu);
    };
  }, [positionMenu, reactionMenuVisible]);

  /**
   * Closes the picker, returning focus to the button that opened it when the
   * user closed it deliberately — Escape, or picking an emoji. A click outside
   * passes false: focus belongs wherever the click put it.
   *
   * The toolbar is kept visible first, because it is what the anchor lives in:
   * without that, closing the picker while the pointer is elsewhere would
   * unmount the button at the same instant focus was being handed back to it.
   */
  const closePicker = useCallback(
    (restoreFocus: boolean) => {
      if (restoreFocus) onReactionMenuVisibleChange(messageId, true);
      onPickerOpenChange(messageId, false);
      if (restoreFocus) anchorRef.current?.focus();
    },
    [messageId, onPickerOpenChange, onReactionMenuVisibleChange],
  );

  /**
   * Makes room for a touch picker that shows no whole row of results beside its
   * menu. Once per opening: the classes only ever shrink the menu and the
   * picker's header, which the placement observes and re-places against, and
   * nothing here undoes them while the picker is open — so a placement cannot
   * flip it back and forth.
   */
  const makeRoom = useCallback(
    (picker: HTMLElement) => {
      const menu = menuRef.current;
      if (!menu || menu.classList.contains(crampedMenuClass) || !showsNoResultRow(picker)) return;
      menu.classList.add(crampedMenuClass);
      picker.classList.add(crampedPickerClass);
      positionMenu();
    },
    [positionMenu],
  );

  const pickerRef = useAnchoredPicker({
    open: pickerOpen,
    anchorRef,
    onDismiss: closePicker,
    containerRef: menuRef,
    // Opened by touch, the picker stays inside what the reader can see.
    bounds: touchMode ? visualViewportBounds : undefined,
    // And clear of the touch menu, which is painted over anything under it.
    clearRef: touchMode ? menuRef : undefined,
    onPlaced: touchMode ? makeRoom : undefined,
  });

  useTouchMenuDismissal({
    active: reactionMenuVisible && !!touchMode,
    pickerOpen,
    menuRef,
    pickerRef,
    dismiss,
  });

  // Where a touch picker's tone palette may sit (issue #1152): the visual
  // viewport on the picker's side of its menu. The menu is in the top layer, so
  // a palette crossing it is painted under it and cannot be touched there.
  const toneBounds = useCallback(
    () =>
      bandBeside(
        visualViewportBounds(),
        menuRef.current?.getBoundingClientRect(),
        pickerRef.current?.getBoundingClientRect(),
      ),
    [pickerRef],
  );

  return {
    menuRef,
    anchorRef,
    pickerRef,
    closePicker,
    dismiss,
    toneBounds: touchMode ? toneBounds : undefined,
  };
}

/**
 * How a touch-opened menu closes (issue #1152). A hover menu closes when the
 * pointer leaves; a finger never leaves, so a tap outside the message (and
 * outside its picker) closes it, and so does Escape — returning focus to what
 * opened it: the message's actions button when that was it, the message itself
 * otherwise. Focus starts on the menu's first action, so assistive technology
 * lands inside it.
 */
function useTouchMenuDismissal({
  active,
  pickerOpen,
  menuRef,
  pickerRef,
  dismiss,
}: {
  active: boolean;
  pickerOpen: boolean;
  menuRef: RefObject<HTMLDivElement | null>;
  pickerRef: RefObject<HTMLDivElement | null>;
  dismiss: () => void;
}) {
  const openerRef = useRef<HTMLElement | null>(null);

  useEffect(() => {
    if (!active) return;
    const shell = menuRef.current?.closest<HTMLElement>("[data-message-id]") ?? null;
    const closeOnOutsidePress = (event: PointerEvent) => {
      const target = event.target as Node;
      if (shell?.contains(target) || pickerRef.current?.contains(target)) return;
      if (target instanceof Element && target.closest(".chat-emoji-tone")) return;
      dismiss();
    };
    // With the picker open, Escape is the picker's: it closes that first.
    const closeOnEscape = (event: KeyboardEvent) => {
      if (event.key !== "Escape" || pickerOpen) return;
      dismiss();
      (openerRef.current ?? shell)?.focus({ preventScroll: true });
    };
    document.addEventListener("pointerdown", closeOnOutsidePress, true);
    document.addEventListener("keydown", closeOnEscape);
    return () => {
      document.removeEventListener("pointerdown", closeOnOutsidePress, true);
      document.removeEventListener("keydown", closeOnEscape);
    };
  }, [active, dismiss, menuRef, pickerOpen, pickerRef]);

  // Once per opening, not per picker toggle: closing the picker hands focus
  // back to its own button, which must not be taken away again.
  useEffect(() => {
    if (!active) return;
    const opener = document.activeElement;
    const shell = menuRef.current?.closest("[data-message-id]");
    openerRef.current = opener instanceof HTMLElement && shell?.contains(opener) ? opener : null;
    menuRef.current?.querySelector("button")?.focus({ preventScroll: true });
  }, [active, menuRef]);
}

function ReactionBadges({
  message,
  currentUserId,
  onToggleReaction,
}: Pick<MessageToolbarProps, "message" | "currentUserId" | "onToggleReaction">) {
  const { rendered, onExited } = useReactionPresence(message.reactions);
  if (rendered.length === 0) return null;
  return (
    <div className="chat-msg-area__reactions" aria-label="Reações da mensagem">
      {rendered.map(({ reaction, exiting }) => (
        <ReactionBadge
          key={reaction.emoji}
          messageId={message.id}
          reaction={reaction}
          currentUserId={currentUserId}
          onToggle={onToggleReaction}
          exiting={exiting}
          onExited={onExited}
        />
      ))}
    </div>
  );
}

function QuickReactionButtons({
  messageId,
  emojis,
  onToggleReaction,
}: {
  messageId: string;
  emojis: string[];
  onToggleReaction: (messageId: string, emoji: string) => void;
}) {
  return (
    <>
      {emojis.map((emoji) => (
        <button
          key={emoji}
          type="button"
          aria-label={`Reagir rapidamente com ${emoji}`}
          onClick={() => onToggleReaction(messageId, emoji)}
        >
          {emoji}
        </button>
      ))}
    </>
  );
}

export default function MessageToolbar(props: MessageToolbarProps) {
  const { message, currentUserId, onToggleReaction, reactionMenuVisible } = props;
  const { menuRef, anchorRef, pickerRef, closePicker, dismiss, toneBounds } =
    useReactionPickerPlacement({
      ...props,
      messageId: message.id,
    });
  const touchMode = props.touchMode ?? false;

  // A touch menu closes once an action in it is chosen — except "Mais
  // reações", which opens the picker over it. A click inside the picker is not
  // a click inside the menu: the picker is portalled out of its DOM.
  const closeAfterTouchAction = (event: ReactMouseEvent<HTMLDivElement>) => {
    const button = event.target instanceof Element ? event.target.closest("button") : null;
    if (!touchMode || !button || button === anchorRef.current) return;
    if (event.currentTarget.contains(button)) dismiss();
  };

  const selectReaction = (emoji: string) => {
    onToggleReaction(message.id, emoji);
    closePicker(true);
  };

  if (message.isRemoved) return null;
  return (
    <>
      <ReactionBadges
        message={message}
        currentUserId={currentUserId}
        onToggleReaction={onToggleReaction}
      />
      {reactionMenuVisible && (
        <div
          ref={menuRef}
          id={props.menuId}
          className={
            touchMode
              ? "chat-msg-area__reaction-menu chat-msg-area__reaction-menu--touch"
              : "chat-msg-area__reaction-menu"
          }
          role="toolbar"
          // A manual popover is rendered in the top layer, where the transform
          // that positions a virtualized row is not its containing block
          // (issue #839) — while staying a DOM descendant of the message, so
          // hover containment, Tab order and focus recovery are untouched.
          popover={popoverSupported ? "manual" : undefined}
          aria-label="Reagir à mensagem"
          onMouseEnter={() => props.onReactionMenuVisibleChange(message.id, true)}
          onClick={closeAfterTouchAction}
          style={{ visibility: "hidden" }}
        >
          <QuickReactionButtons
            messageId={message.id}
            emojis={props.recentReactionEmojis}
            onToggleReaction={onToggleReaction}
          />
          <MessageActionButtons {...props} />
          <button
            ref={anchorRef}
            type="button"
            aria-label="Mais reações"
            aria-expanded={props.pickerOpen}
            aria-haspopup="dialog"
            onClick={() => props.onPickerOpenChange(message.id, !props.pickerOpen)}
          >
            <span className="material-symbols-outlined" aria-hidden="true">
              add_reaction
            </span>
          </button>
          {props.pickerOpen &&
            createPortal(
              <div
                ref={pickerRef}
                // The portal escapes the conversation's DOM, and with it the
                // scoped palette and radius scale that .chat-app defines. The
                // class comes along so the picker is NChat-purple like the
                // surface it belongs to, instead of falling back to the root
                // theme.
                className="chat-theme chat-emoji-surface"
                // Whose picker this is, for the message's focus handling: see
                // useToolbarReveal.
                data-reaction-picker-for={message.id}
                role="dialog"
                aria-label="Escolher reação"
                style={{ visibility: "hidden" }}
              >
                <Suspense
                  fallback={
                    <p className="chat-emoji-picker__status" role="status">
                      Carregando emojis…
                    </p>
                  }
                >
                  <EmojiPicker
                    usage={props.emojiUsage}
                    onToneChange={props.onEmojiToneChange}
                    onSelect={selectReaction}
                    reactionFor={message.id}
                    toneBounds={toneBounds}
                  />
                </Suspense>
              </div>,
              document.body,
            )}
        </div>
      )}
    </>
  );
}
