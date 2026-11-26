/**
 * The reaction badges under a message, and the toolbar that appears over it.
 *
 * Split out of MessageBubble (issue #496, CQ follow-up): the bubble is about
 * showing what was said, this is about what can be done to it. The two shared a
 * file and a component, and the picker's placement rules had nowhere of their
 * own to live.
 */

import {
  lazy,
  Suspense,
  useCallback,
  useEffect,
  useId,
  useLayoutEffect,
  useMemo,
  useRef,
  useState,
} from "react";
import { createPortal } from "react-dom";
import type { MouseEvent as ReactMouseEvent, ReactElement, RefObject } from "react";

import type { Message } from "./chatTypes";
import type { EmojiUsage } from "./emoji/emojiUsage";
import { selectionHeaderClass } from "./message-area/timeline/useSelectionMode";
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
 * it, which is what it did before — except a touch menu, which goes to the
 * document's body instead (CQR R3 of issue #1238): a fixed menu inside a
 * virtualized row is placed against the row's transform and clipped by the
 * list, so the sheet left the screen.
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

/**
 * A touch menu's actions that are not reactions (issue #1238), behind one
 * "Mais ações" button: a long press is mostly for reacting, so the reactions
 * and the full catalog come first and the rest — the same buttons, under the
 * same permissions, as the hover toolbar's — open in place when asked for.
 *
 * Controlled by the toolbar: opening changes the menu's size, and the toolbar's
 * own commit is what re-places it.
 */
function TouchMoreActions({
  open,
  onToggle,
  ...props
}: MessageActionButtonsProps & { open: boolean; onToggle: () => void }) {
  const groupId = useId();
  return (
    <>
      <button
        type="button"
        data-keeps-menu=""
        aria-label="Mais ações"
        aria-expanded={open}
        aria-controls={open ? groupId : undefined}
        onClick={onToggle}
      >
        <span className="material-symbols-outlined" aria-hidden="true">
          more_horiz
        </span>
      </button>
      {open && (
        <div
          id={groupId}
          className="chat-msg-area__reaction-menu-actions"
          role="group"
          aria-label="Outras ações"
        >
          <MessageActionButtons {...props} />
        </div>
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
  /** Ends a touch menu, picker and all, handing focus back (see useTouchMenuDismissal). */
  closeTouchMenu: () => void;
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

/** The gap useAnchoredPicker leaves between a picker and the box it clears. */
const pickerGap = 7;

/**
 * The picker's full height, as its stylesheet caps it: at most 24rem, and
 * never more than the window less 96px (`.chat-emoji-picker` in its CSS).
 */
function pickerFullHeight(): number {
  const rem = Number.parseFloat(getComputedStyle(document.documentElement).fontSize) || 16;
  return Math.min(24 * rem, window.innerHeight - 96);
}

/**
 * Whether an anchored touch menu leaves its picker room (CQR R4 of issue
 * #1238): a column at least twice the menu's width, and the full-height picker
 * on one side of the menu inside the band. A phone fails the first — the menu
 * spans most of it — and keeps the sheet both were made for.
 * ponytail: twice the width is a proportion, not a measured layout; measure the
 * picker beside the menu if a third layout ever needs it.
 */
function leavesPickerRoom(menu: HTMLElement, bounds: VisibleBounds): boolean {
  const box = menu.getBoundingClientRect();
  if (box.width * 2 > bounds.right - bounds.left) return false;
  const room = Math.max(box.top - bounds.top, bounds.bottom - box.bottom);
  return room - pickerGap - viewportPadding >= pickerFullHeight();
}

/**
 * Whether a menu may try a spot beside its bubble. A hover menu always may. A
 * touch menu wider than the list (touch-sized targets on a phone) may not: the
 * horizontal clamp would push it off-screen left (#1152). Nor may one whose
 * open picker it leaves no room for (issue #1238): the two then share the
 * sheet. Where there is room — a tablet or a wide touch screen — the menu stays
 * by its message (CQR R4).
 */
function mayAnchor(
  menu: HTMLElement,
  bounds: VisibleBounds,
  touchMode: boolean,
  pickerOpen: boolean,
): boolean {
  if (!touchMode) return true;
  return fitsBand(menu, bounds) && (!pickerOpen || leavesPickerRoom(menu, bounds));
}

/**
 * Whether a touch menu's other actions are open (issue #1238). Every opening of
 * the menu starts with them collapsed.
 */
function useMoreActions(menuVisible: boolean) {
  const [open, setOpen] = useState(false);
  if (!menuVisible && open) setOpen(false);
  const toggle = useCallback(() => setOpen((wasOpen) => !wasOpen), []);
  return { open, toggle };
}

/**
 * The menu as a bottom sheet (issue #1152), wholly inside the visual viewport:
 * zoomed in, the reader sees only part of the layout viewport a fixed element
 * is placed in, and the on-screen keyboard or the browser's own bars cover its
 * bottom — so `left: 0; right: 0; bottom: 0` would put the sheet off-screen or
 * under them. Edges, width and height limit all come from what is visible,
 * less the padding and the device's safe areas; past the limit it scrolls.
 *
 * Its band is the touch band (CQR R4 and R5 of issue #1238): spanning the
 * visual viewport, a sheet on a wide touch screen covered the sidebar, and one
 * as tall as it covered the composer — open picker and all, the composer took
 * no touch. An edge the band shares with the screen has the device's safe area
 * to clear; the column's, the header's or the composer's has none.
 */
function placeSheet(menu: HTMLElement, band: VisibleBounds) {
  const view = visualViewportBounds();
  const pad = viewportPadding;
  const inset = (side: string, screenEdge: boolean) =>
    screenEdge ? [`env(safe-area-inset-${side}, 0px)`] : [];
  const sum = (sign: string, terms: string[]) => terms.map((term) => ` ${sign} ${term}`).join("");
  const top = inset("top", band.top <= view.top);
  const right = inset("right", band.right >= view.right);
  const bottom = inset("bottom", band.bottom >= view.bottom);
  const left = inset("left", band.left <= view.left);
  menu.style.top = "";
  menu.style.left = `calc(${band.left + pad}px${sum("+", left)})`;
  menu.style.width = `calc(${band.right - band.left - 2 * pad}px${sum("-", [...left, ...right])})`;
  menu.style.bottom = `calc(${window.innerHeight - band.bottom + pad}px${sum("+", bottom)})`;
  menu.style.maxHeight = `calc(${band.bottom - band.top - 2 * pad}px${sum("-", [...top, ...bottom])})`;
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
 * A message's open picker, by the owner it names: the placement that measures
 * it is set up before the hook that holds its ref.
 */
function openPicker(messageId: string): HTMLElement | null {
  const pickers = document.querySelectorAll<HTMLElement>(
    '[role="dialog"][data-reaction-picker-for]',
  );
  return [...pickers].find((node) => node.dataset.reactionPickerFor === messageId) ?? null;
}

/** The sum of a node's computed lengths, such as its padding and border. */
function edges(node: Element, ...properties: string[]): number {
  const style = getComputedStyle(node);
  return properties.reduce(
    (sum, property) => sum + (Number.parseFloat(style.getPropertyValue(property)) || 0),
    0,
  );
}

const verticalEdges = ["padding-top", "padding-bottom", "border-top-width", "border-bottom-width"];

/**
 * The least an open picker can show: everything down to its first whole row of
 * results, as the stylesheet lays it out, without the tabs — what makeRoom
 * shrinks it to, whether or not it has yet. A picker with no row to measure
 * (its catalog loading, a search with no result) needs what it shows; it is
 * measured again once placed.
 */
function compactPickerHeight(picker: HTMLElement): number {
  const sections = picker.querySelector(".chat-emoji-picker__sections");
  const cell = sections?.querySelector(".chat-emoji-picker__emoji");
  const box = picker.getBoundingClientRect();
  if (!sections || !cell) return box.height;
  const tabs = picker.querySelector(".chat-emoji-picker__tabs");
  const tabsHeight = tabs?.getBoundingClientRect().height ?? 0;
  const tabsBlock =
    tabs && tabsHeight ? tabsHeight + edges(tabs, "margin-top", "margin-bottom") : 0;
  return (
    cell.getBoundingClientRect().bottom +
    sections.scrollTop -
    box.top -
    tabsBlock +
    edges(picker, "padding-bottom", "border-bottom-width")
  );
}

/**
 * The height a touch sheet's band must have (CQR R6 of issue #1238), measured
 * from what it holds: with the picker open, the menu set down to "Mais
 * reações", the gap and the compact picker; with it closed, the whole menu.
 * Both with the sheet's padding.
 */
function touchNeed(menu: HTMLElement | null, picker: HTMLElement | null): number {
  if (!menu) return 0;
  if (!picker) {
    return (
      menu.scrollHeight +
      edges(menu, "border-top-width", "border-bottom-width") +
      2 * viewportPadding
    );
  }
  const more = menu.querySelector(':scope > [aria-haspopup="dialog"]');
  const compactMenu = (more?.getBoundingClientRect().height ?? 0) + edges(menu, ...verticalEdges);
  return compactMenu + pickerGap + compactPickerHeight(picker) + 2 * viewportPadding;
}

/**
 * The band a touch sheet, its picker and the tone palette may use (CQR R4/R5 of
 * issue #1238): the conversation's list as the visual viewport shows it — below
 * the header, above the composer, inside the column — so the composer and the
 * header take their touches while the picker is open; the picker caps itself to
 * what is left and scrolls inside.
 *
 * A list too short for what the sheet holds (touchNeed) grows the band upwards
 * only, over the header, to the visual viewport's top: the composer keeps its
 * touches — a tap there closes the picker — and the header is back once the
 * picker closes. 360×385 leaves the list 115px and 232 above the composer, for
 * the 205 a menu and a picker showing one row need (CQR R6). Where even that is
 * too short, the visual viewport's full height is all there is: at 320×210,
 * 220 and 320 the composer starts at 125 or 175px, and is under the open picker
 * there, which Escape, "Mais reações" or a chosen emoji close.
 */
function touchBand(
  anchor: Element | null,
  menu: HTMLElement | null,
  picker: HTMLElement | null,
): VisibleBounds {
  const view = visualViewportBounds();
  if (!anchor) return view;
  const list = intersectBounds(visibleBounds(anchor), view);
  const need = touchNeed(menu, picker);
  const fits = (band: VisibleBounds) => band.bottom - band.top >= need;
  const aboveComposer = { ...list, top: view.top };
  if (fits(list)) return list;
  if (fits(aboveComposer)) return aboveComposer;
  return { ...view, left: list.left, right: list.right };
}

/**
 * Ends a touch menu whose message stayed out of the reader's view (CQR R8 of
 * issue #1238), as the selection's own watch ends one with nothing open
 * (MessageBubble's endUnseenSelection). Focus left in the closing menu, its
 * picker or palette, on the message, on the header's selection controls — which
 * go with the selection — or on nothing goes to the conversation's list first,
 * the place #675 gives focus that lost its row; focus leaving the message that
 * way ends its selection too (useSelectionMode). Focus the reader put anywhere
 * else stays there.
 */
function closeUnseen(
  menu: HTMLElement | null,
  bubble: HTMLElement | null,
  messageId: string,
  dismiss: () => void,
) {
  const list = bubble?.closest<HTMLElement>(".chat-msg-area__list");
  if (bubble && list?.checkVisibility?.() !== false && focusLeftBehind(menu, bubble, messageId)) {
    list?.focus({ preventScroll: true });
  }
  dismiss();
}

/**
 * A touch menu hidden while its message's reading is confirmed (CQR R10 F-1 of
 * issue #1238) cannot keep focus: WebKit, rotating, dropped the focused button
 * to the body, and the row took that blur for focus leaving — closing the menu
 * and the selection with the message back in view. Focus held in the menu goes
 * to the message instead, before the browser drops it; the confirmation hands
 * it back (placeAgain), or on to the list with the selection (closeUnseen).
 */
function parkFocus(
  menu: HTMLElement,
  bubble: HTMLElement,
  backRef: RefObject<HTMLElement | null>,
): void {
  const focused = document.activeElement;
  if (!(focused instanceof HTMLElement) || !menu.contains(focused)) return;
  backRef.current = focused;
  shellOf(bubble)?.focus({ preventScroll: true });
}

/** The message's row, the focusable shell around its bubble. */
function shellOf(bubble: HTMLElement | null): HTMLElement | null {
  return bubble?.closest<HTMLElement>("[data-message-id]") ?? null;
}

/** Whether focus goes with a message leaving the reader's view (see closeUnseen). */
function focusLeftBehind(menu: HTMLElement | null, bubble: HTMLElement, messageId: string) {
  if (document.activeElement?.closest(`.${selectionHeaderClass}`)) return true;
  const shell = bubble.closest<HTMLElement>("[data-message-id]");
  return focusAdrift(menu, openPicker(messageId), shell);
}

/**
 * The out-of-sight verdict for a touch menu (CQR R8 of issue #1238). A reading
 * is not taken at its word: WebKit, shrinking the viewport, lays the list out
 * shorter a frame before the timeline puts its scroll position back, and for
 * that frame the selected bubble reads below the list. Closing on it took the
 * picker, its search and focus, while the selection stayed — and the menu too,
 * revived by WebKit's mouseover.
 *
 * So the reading is taken again two frames later, after the frame the browser
 * corrects the scroll in, as the selected row's own watch does (MessageBubble).
 * Back in sight, the menu is placed again and nothing closed; still out, the
 * message has left the reader's view (closeUnseen). One pending check, from the
 * first reading — later ones do not postpone it — cancelled when the menu
 * closes or the toolbar unmounts.
 */
function useUnseenConfirmation({
  bubbleRef,
  menuRef,
  positionRef,
  messageId,
  dismiss,
}: {
  bubbleRef: RefObject<HTMLElement | null>;
  menuRef: RefObject<HTMLElement | null>;
  positionRef: RefObject<() => void>;
  messageId: string;
  dismiss: () => void;
}) {
  const frameRef = useRef(0);
  useEffect(() => () => cancelAnimationFrame(frameRef.current), []);
  return useMemo(() => {
    const confirm = () => {
      frameRef.current = 0;
      const bubble = bubbleRef.current;
      if (!menuRef.current) return;
      const bounds = bubble && placementBounds(bubble, true);
      if (bounds && anchorIsVisible(bubble.getBoundingClientRect(), bounds)) {
        positionRef.current();
        return;
      }
      closeUnseen(menuRef.current, bubble, messageId, dismiss);
    };
    return {
      schedule() {
        if (frameRef.current) return;
        frameRef.current = requestAnimationFrame(() => {
          frameRef.current = requestAnimationFrame(confirm);
        });
      },
      cancel() {
        cancelAnimationFrame(frameRef.current);
        frameRef.current = 0;
      },
      // A reading waits for its verdict (CQR R10 F-1): see losePicker.
      pending: () => frameRef.current !== 0,
    };
  }, [bubbleRef, dismiss, menuRef, messageId, positionRef]);
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

  // The latest placement, for a confirmation that finds the anchor back in sight.
  const positionRef = useRef<() => void>(() => {});
  // The picker's own placement (useAnchoredPicker), for the same confirmation.
  const placePickerRef = useRef<() => void>(() => {});
  // Where focus goes back once a menu hidden while its reading is confirmed is
  // placed again, parked on the message meanwhile (CQR R10 F-1): parkFocus.
  const focusDeferredRef = useRef<HTMLElement | null>(null);
  const confirmUnseen = useUnseenConfirmation({
    bubbleRef,
    menuRef,
    positionRef,
    messageId,
    dismiss,
  });

  const positionMenu = useCallback(() => {
    const anchor = bubbleRef.current;
    const menu = menuRef.current;
    if (!reactionMenuVisible) {
      sheetRef.current = false;
      confirmUnseen.cancel();
      return;
    }
    if (!anchor || !menu) return;
    const bubble = anchor.getBoundingClientRect();
    const bounds = placementBounds(anchor, !!touchMode);
    // The sheet is decided once per opening: measured again in its own
    // layout, the toolbar would flip between the two on every commit.
    const anchorable = !sheetRef.current && mayAnchor(menu, bounds, !!touchMode, pickerOpen);
    if (anchorable && placeAnchored(menu, anchor, bubble, bounds, isMine)) return;
    if (!touchMode) {
      sheetRef.current = false;
      menu.classList.remove(sheetClass);
      return dismiss();
    }
    // A touch menu whose anchor reads out of sight is judged again once the
    // browser has settled the frame (CQR R8 of #1238): see useUnseenConfirmation.
    // Meanwhile an anchored one stays hidden, as placeAnchored left it, and a
    // sheet — bound to the band, not to the bubble — keeps to the band of now:
    // left where the last band put it, it would be laid out again inside the
    // picker's observer once the anchor is back, which WebKit reports as a loop.
    if (!anchorIsVisible(bubble, bounds)) {
      confirmUnseen.schedule();
      if (!sheetRef.current) return parkFocus(menu, anchor, focusDeferredRef);
    }
    // Touch only (issue #1152): on a phone a bubble spans the row and its
    // neighbours sit closer than the toolbar is tall, so no anchored spot
    // exists and closing — the desktop verdict — left the reader with nothing.
    // The menu stays bound to its message, just not drawn beside it.
    sheetRef.current = true;
    menu.classList.add(sheetClass);
    placeSheet(menu, touchBand(anchor, menu, openPicker(messageId)));
  }, [
    bubbleRef,
    confirmUnseen,
    dismiss,
    isMine,
    messageId,
    pickerOpen,
    reactionMenuVisible,
    touchMode,
  ]);

  // Back in sight once confirmed (CQR R10 F-1): the menu, and the picker that
  // waited on it placed against where the menu is now. Neither reopens what
  // closed meanwhile — each returns at once when closed.
  const placeAgain = useCallback(() => {
    positionMenu();
    placePickerRef.current();
    // Focus parked on the message while the menu was hidden goes back where it
    // was meant to be, unless the reader moved it meanwhile.
    const back = focusDeferredRef.current;
    focusDeferredRef.current = null;
    if (back?.isConnected && document.activeElement === shellOf(bubbleRef.current)) back.focus();
  }, [bubbleRef, positionMenu]);

  // No dependency list on purpose: every commit of the toolbar is a moment the
  // timeline may have moved its anchor, and re-reading one rect is cheaper than
  // knowing why it rendered.
  useLayoutEffect(() => {
    // Into the top layer before measuring — see the toolbar's CSS. A no-op
    // once shown.
    if (popoverSupported) menuRef.current?.togglePopover(true);
    // A closed picker gives the menu back the actions it set aside.
    if (!pickerOpen) menuRef.current?.classList.remove(crampedMenuClass);
    positionRef.current = placeAgain;
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
   *
   * A touch menu hidden while its message's reading is confirmed (CQR R10 F-1
   * of issue #1238: Escape right after a rotation) cannot take focus, and the
   * picker's search going left it on the body with the selection active. It
   * goes to the message meanwhile, as the menu's own Escape does; the
   * confirmation hands it on to the button, or to the list with the selection.
   */
  const closePicker = useCallback(
    (restoreFocus: boolean) => {
      if (restoreFocus) onReactionMenuVisibleChange(messageId, true);
      onPickerOpenChange(messageId, false);
      if (!restoreFocus) return;
      anchorRef.current?.focus();
      if (document.activeElement === anchorRef.current) return;
      focusDeferredRef.current = anchorRef.current;
      shellOf(bubbleRef.current)?.focus({ preventScroll: true });
    },
    [bubbleRef, messageId, onPickerOpenChange, onReactionMenuVisibleChange],
  );

  /**
   * A touch picker whose anchor reads as gone (CQR R10 F-1 of issue #1238).
   * Rotating the phone lays the list out for a frame with the message below
   * it: the menu, an anchored one, stays where it was while that reading is
   * confirmed, and judged against that box the picker closed at once — focus
   * to the body, the selection and the menu kept. While the reading is pending
   * the verdict is the confirmation's: it places both again, or closes both
   * with the selection. Lost with nothing pending, the picker hands focus it
   * held back to the button that opened it.
   */
  const losePicker = useCallback(() => {
    if (confirmUnseen.pending()) return;
    closePicker(focusAdrift(null, openPicker(messageId), null));
  }, [closePicker, confirmUnseen, messageId]);

  /**
   * Makes room for a touch picker that shows no whole row of results beside its
   * menu. Once per opening: the classes only ever shrink the menu and the
   * picker's header, which the placement observes and re-places against, and
   * nothing here undoes them while the picker is open — so a placement cannot
   * flip it back and forth.
   *
   * And re-places the menu on every placement of the picker: the band is chosen
   * from the picker's measure (CQR R6), which its catalog arriving changes. A
   * menu that moved, which no observer sees, is reported back so the picker is
   * placed against where it is now.
   */
  const makeRoom = useCallback(
    (picker: HTMLElement) => {
      const menu = menuRef.current;
      if (!menu) return false;
      const before = menu.getBoundingClientRect();
      positionMenu();
      const after = menu.getBoundingClientRect();
      // Placed against where the menu was: the room it has is judged on the
      // placement against where it is now, or a band left behind would shrink
      // the menu for good.
      if (before.top !== after.top || before.bottom !== after.bottom) return true;
      if (!menu.classList.contains(crampedMenuClass) && showsNoResultRow(picker)) {
        menu.classList.add(crampedMenuClass);
        picker.classList.add(crampedPickerClass);
        positionMenu();
      }
      return false;
    },
    [positionMenu],
  );

  // Opened by touch, the picker stays inside what the reader can see of the
  // conversation's list: clear of the header and the composer.
  const pickerBand = useCallback(
    () => touchBand(bubbleRef.current, menuRef.current, openPicker(messageId)),
    [bubbleRef, messageId],
  );

  const pickerRef = useAnchoredPicker({
    open: pickerOpen,
    anchorRef,
    onDismiss: closePicker,
    onLost: touchMode ? losePicker : undefined,
    placeRef: placePickerRef,
    containerRef: menuRef,
    bounds: touchMode ? pickerBand : undefined,
    // And clear of the touch menu, which is painted over anything under it.
    clearRef: touchMode ? menuRef : undefined,
    // In the band the picker is about to be placed in (CQR R7): a search
    // changing what the sheet needs moves that band, and the menu with it.
    placeClear: touchMode ? positionMenu : undefined,
    onPlaced: touchMode ? makeRoom : undefined,
  });

  const closeTouchMenu = useTouchMenuDismissal({
    active: reactionMenuVisible && !!touchMode,
    pickerOpen,
    menuRef,
    pickerRef,
    bubbleRef,
    dismiss,
  });

  // Where a touch picker's tone palette may sit (issue #1152): the visual
  // viewport on the picker's side of its menu. The menu is in the top layer, so
  // a palette crossing it is painted under it and cannot be touched there.
  const toneBounds = useCallback(
    () =>
      bandBeside(
        pickerBand(),
        menuRef.current?.getBoundingClientRect(),
        pickerRef.current?.getBoundingClientRect(),
      ),
    [pickerBand, pickerRef],
  );

  return {
    menuRef,
    anchorRef,
    pickerRef,
    closePicker,
    closeTouchMenu,
    toneBounds: touchMode ? toneBounds : undefined,
  };
}

/**
 * Whether focus is somewhere a closing touch menu must take it back from: in
 * the menu, its picker or the tone palette — all about to go — or nowhere, or
 * on the message's row itself, where WebKit puts a tap on the menu's buttons.
 * Focus an action already moved elsewhere (the composer, a dialog) stays there.
 */
function focusAdrift(
  menu: HTMLElement | null,
  picker: HTMLElement | null,
  shell: HTMLElement | null,
): boolean {
  const active = document.activeElement;
  if (!active || active === document.body || active === shell) return true;
  if (menu?.contains(active) || picker?.contains(active)) return true;
  return !!active.closest(".chat-emoji-tone");
}

/**
 * Where focus may go back to: connected, drawn, and still this message's — its
 * row, or the button that controls its menu. Never a node that was removed,
 * hidden, or belongs to another message or conversation.
 */
function focusTarget(
  candidates: (HTMLElement | null)[],
  shell: HTMLElement | null,
  menu: HTMLElement | null,
): HTMLElement | undefined {
  return candidates.find(
    (node): node is HTMLElement =>
      !!node?.isConnected &&
      // Hidden by an ancestor's display:none, or by its own; a browser without
      // the check (and jsdom) has only connection and ownership to go by.
      (node.checkVisibility?.() ?? true) &&
      (!!shell?.contains(node) || controls(node, menu)),
  );
}

/**
 * How a touch-opened menu closes (issue #1152). A hover menu closes when the
 * pointer leaves; a finger never leaves, so a tap outside the message (and
 * outside its picker) closes it, and so does Escape. Focus starts on the menu's
 * first action, so assistive technology lands inside it.
 *
 * Returns the one way a touch menu ends on the reader's say (CQR R4 of issue
 * #1238): Escape, a quick reaction, an emoji confirmed in the picker, or any
 * other action in it. Focus that would be left in the closing surfaces goes
 * back to what opened the menu — the header's actions button for the selected
 * message, which stays selected — or the message itself; never to the body.
 * Cancelling the picker alone is the picker's own (closePicker): the menu stays.
 */
function useTouchMenuDismissal({
  active,
  pickerOpen,
  menuRef,
  pickerRef,
  bubbleRef,
  dismiss,
}: {
  active: boolean;
  pickerOpen: boolean;
  menuRef: RefObject<HTMLDivElement | null>;
  pickerRef: RefObject<HTMLDivElement | null>;
  bubbleRef: RefObject<HTMLElement | null>;
  dismiss: () => void;
}) {
  const openerRef = useRef<HTMLElement | null>(null);

  const close = useCallback(() => {
    // The message, found from its bubble: the menu itself may be in the body.
    const shell = bubbleRef.current?.closest<HTMLElement>("[data-message-id]") ?? null;
    // Chosen before anything closes: whether it is this menu's opener is read
    // against the menu's own id.
    const target = focusAdrift(menuRef.current, pickerRef.current, shell)
      ? focusTarget([openerRef.current, shell], shell, menuRef.current)
      : undefined;
    dismiss();
    target?.focus({ preventScroll: true });
  }, [bubbleRef, dismiss, menuRef, pickerRef]);

  // The settling of the last outside press, if it has not come yet.
  const settleRef = useRef<(() => void) | null>(null);
  useEffect(() => () => settleRef.current?.(), []);

  useEffect(() => {
    if (!active) return;
    const shell = bubbleRef.current?.closest<HTMLElement>("[data-message-id]") ?? null;
    const closeOnOutsidePress = (event: PointerEvent) => {
      if (pressedInside(event.target as Node, menuRef.current, pickerRef.current, shell)) return;
      settleRef.current?.();
      settleRef.current = settleOutsidePress(menuRef.current, pickerRef.current, shell, () => {
        settleRef.current = null;
      });
      dismiss();
    };
    // With the picker open, Escape is the picker's: it closes that first.
    const closeOnEscape = (event: KeyboardEvent) => {
      if (event.key === "Escape" && !pickerOpen) close();
    };
    document.addEventListener("pointerdown", closeOnOutsidePress, true);
    document.addEventListener("keydown", closeOnEscape);
    return () => {
      document.removeEventListener("pointerdown", closeOnOutsidePress, true);
      document.removeEventListener("keydown", closeOnEscape);
    };
  }, [active, bubbleRef, close, dismiss, menuRef, pickerOpen, pickerRef]);

  // Once per opening, not per picker toggle: closing the picker hands focus
  // back to its own button, which must not be taken away again.
  useEffect(() => {
    if (!active) return;
    const opener = document.activeElement;
    const shell = bubbleRef.current?.closest("[data-message-id]");
    const owned = shell?.contains(opener) || controls(opener, menuRef.current);
    openerRef.current = opener instanceof HTMLElement && owned ? opener : null;
    menuRef.current?.querySelector("button")?.focus({ preventScroll: true });
  }, [active, bubbleRef, menuRef]);

  return close;
}

/**
 * Where focus goes once a press outside a touch menu has played out (CQR R5 of
 * issue #1238). The press closes the menu at once; where focus lands is the
 * press's to decide — a focusable target, such as the composer or another
 * control, keeps it. A target that takes no focus (the header's "Mensagem
 * selecionada", say) leaves it on the body, or still in the closing menu, and
 * it is handed to the selection mode's button controlling the menu while that
 * is drawn, else to the conversation's list; never to the message's row, whose
 * focus would reveal its hover toolbar again.
 *
 * Read on the press's click — after the target's own handlers, before the
 * list's deferred recovery (#675) would take focus and end the selection with
 * it — or as the press is cancelled. Both candidates are chosen as the press
 * starts, while the menu still names its controller, and must still be
 * connected and drawn when used. Returns the cancel; `done` runs once either
 * way.
 */
function settleOutsidePress(
  menu: HTMLElement | null,
  picker: HTMLElement | null,
  shell: HTMLElement | null,
  done: () => void,
): () => void {
  const controller = [...document.querySelectorAll<HTMLElement>("[aria-controls]")].find((node) =>
    controls(node, menu),
  );
  const candidates = [controller, shell?.closest<HTMLElement>(".chat-msg-area__list")];
  const stop = () => {
    document.removeEventListener("click", settle);
    document.removeEventListener("pointercancel", settle, true);
    done();
  };
  function settle() {
    stop();
    // The row is not adrift here: the press was outside it, so focus on it
    // was put there by what was pressed ("Cancelar seleção" hands it back).
    if (!focusAdrift(menu, picker, null)) return;
    candidates
      .find((node) => !!node?.isConnected && (node.checkVisibility?.() ?? true))
      ?.focus({ preventScroll: true });
  }
  document.addEventListener("click", settle);
  document.addEventListener("pointercancel", settle, true);
  return stop;
}

/**
 * Whether a press is on a touch menu's own ground (issue #1152): its message,
 * the menu, its picker, the tone palette (portalled out of the picker), or the
 * button controlling the menu, which toggles it on its own click (#1238).
 */
function pressedInside(
  target: Node,
  menu: HTMLElement | null,
  picker: HTMLElement | null,
  shell: HTMLElement | null,
): boolean {
  if ([shell, menu, picker].some((surface) => surface?.contains(target))) return true;
  if (target instanceof Element && target.closest(".chat-emoji-tone")) return true;
  return controls(target, menu);
}

/** Whether `node` is in the button that controls `menu` (issue #1238). */
function controls(node: Node | null, menu: HTMLElement | null): boolean {
  const button = node instanceof Element ? node.closest("[aria-controls]") : null;
  return !!menu?.id && button?.getAttribute("aria-controls") === menu.id;
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
  const { menuRef, anchorRef, pickerRef, closePicker, closeTouchMenu, toneBounds } =
    useReactionPickerPlacement({
      ...props,
      messageId: message.id,
    });
  const touchMode = props.touchMode ?? false;
  const moreActions = useMoreActions(reactionMenuVisible);

  // A touch menu closes once an action in it is chosen — except "Mais
  // reações", which opens the picker over it, and "Mais ações", which opens
  // the rest of the actions in it. A click inside the picker is not a click
  // inside the menu: the picker is portalled out of its DOM.
  const closeAfterTouchAction = (event: ReactMouseEvent<HTMLDivElement>) => {
    const button = event.target instanceof Element ? event.target.closest("button") : null;
    if (!touchMode || !button || button.hasAttribute("data-keeps-menu")) return;
    if (event.currentTarget.contains(button)) closeTouchMenu();
  };

  // An emoji confirmed in the picker reacts once. On touch it ends the menu as
  // a quick reaction does (CQR R4 of issue #1238); the hover toolbar keeps its
  // menu and hands focus back to "Mais reações" (#1153).
  const selectReaction = (emoji: string) => {
    onToggleReaction(message.id, emoji);
    if (touchMode) closeTouchMenu();
    else closePicker(true);
  };

  if (message.isRemoved) return null;
  return (
    <>
      <ReactionBadges
        message={message}
        currentUserId={currentUserId}
        onToggleReaction={onToggleReaction}
      />
      {reactionMenuVisible &&
        inBodyWithoutTopLayer(
          touchMode,
          <div
            ref={menuRef}
            id={props.menuId}
            className={touchMenuClassName(touchMode)}
            role="toolbar"
            // A manual popover is rendered in the top layer, where the transform
            // that positions a virtualized row is not its containing block
            // (issue #839) — while staying a DOM descendant of the message, so
            // hover containment, Tab order and focus recovery are untouched.
            popover={popoverSupported ? "manual" : undefined}
            // In the body instead (no top layer), it says whose it is, as the
            // picker does, for the message's focus handling.
            data-reaction-picker-for={touchMode && !popoverSupported ? message.id : undefined}
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
            {/* Touch (issue #1238): reactions first, the rest behind "Mais ações".
              The hover toolbar keeps the order it was approved with (#1153). */}
            {!touchMode && <MessageActionButtons {...props} />}
            <button
              ref={anchorRef}
              type="button"
              data-keeps-menu=""
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
            {touchMode && (
              <TouchMoreActions {...props} open={moreActions.open} onToggle={moreActions.toggle} />
            )}
          </div>,
        )}
    </>
  );
}

/**
 * Where a touch menu goes without the top layer (CQR R3 of issue #1238): the
 * document's body, whose fixed descendants are placed against the viewport —
 * in a virtualized row they are placed against its transform and clipped by
 * the list. The hover menu stays where it always was (#839, #1153).
 */
function inBodyWithoutTopLayer(touchMode: boolean, menu: ReactElement) {
  return touchMode && !popoverSupported ? createPortal(menu, document.body) : menu;
}

/** In the body, the menu brings the conversation's palette along, as the picker does. */
function touchMenuClassName(touchMode: boolean): string {
  if (!touchMode) return "chat-msg-area__reaction-menu";
  const theme = popoverSupported ? "" : " chat-theme";
  return `chat-msg-area__reaction-menu chat-msg-area__reaction-menu--touch${theme}`;
}
