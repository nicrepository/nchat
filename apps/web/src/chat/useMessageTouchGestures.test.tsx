/**
 * The touch gesture recognizer of a message row (issue #1152): thresholds,
 * cancellations and precedence between tap, scroll, swipe and long press.
 *
 * jsdom has no touch pipeline, so the events are the ones the browser would
 * deliver — pointer events with pointerType "touch" — fired at a real row.
 */

import { act, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import {
  EDGE_GUARD_PX,
  LONG_PRESS_MS,
  SWIPE_REPLY_PX,
  TOUCH_SLOP_PX,
  useMessageTouchGestures,
  type MessageTouchGestureOptions,
} from "./useMessageTouchGestures";

const START = { x: 100, y: 300 };

function Row(options: MessageTouchGestureOptions) {
  const handlers = useMessageTouchGestures(options);
  return (
    <div data-testid="row" {...handlers}>
      <p>texto da mensagem</p>
      <a href="https://example.test">link</a>
      <button type="button">ação</button>
      <img alt="anexo" />
      <div contentEditable="true" suppressContentEditableWarning>
        editor
      </div>
      <div role="toolbar" aria-label="menu">
        <span>fundo do menu</span>
      </div>
    </div>
  );
}

function setup(overrides: Partial<MessageTouchGestureOptions> = {}) {
  const options: MessageTouchGestureOptions = {
    messageId: "msg-1",
    disabled: false,
    swipeBlocked: false,
    onLongPress: vi.fn(),
    onSwipeReply: vi.fn(),
    ...overrides,
  };
  const view = render(<Row {...options} />);
  return {
    ...view,
    options,
    rerender: (next: Partial<MessageTouchGestureOptions>) =>
      view.rerender(<Row {...options} {...next} />),
  };
}

const row = () => screen.getByTestId("row");
const text = () => screen.getByText("texto da mensagem");

function down(target: Element = text(), init: Record<string, unknown> = {}) {
  fireEvent.pointerDown(target, {
    pointerType: "touch",
    pointerId: 1,
    isPrimary: true,
    clientX: START.x,
    clientY: START.y,
    ...init,
  });
}

/** Where the finger last was: a release lifts it there unless told otherwise. */
let last = START;

function move(dx: number, dy: number, pointerId = 1, from = START) {
  if (pointerId === 1) last = { x: from.x + dx, y: from.y + dy };
  fireEvent.pointerMove(text(), {
    pointerType: "touch",
    pointerId,
    clientX: from.x + dx,
    clientY: from.y + dy,
  });
}

function up(pointerId = 1, at = last) {
  fireEvent.pointerUp(text(), { pointerType: "touch", pointerId, clientX: at.x, clientY: at.y });
}

/** A drag in a few steps, the way a finger reports it. */
function drag(dx: number, dy: number, from = START) {
  for (const step of [0.25, 0.5, 0.75, 1]) move(dx * step, dy * step, 1, from);
}

beforeEach(() => {
  vi.useFakeTimers();
  last = START;
});

afterEach(() => {
  vi.restoreAllMocks();
  vi.useRealTimers();
  Reflect.deleteProperty(document, "elementFromPoint");
});

/**
 * What the browser answers from now on: to ":modal", and to hit-testing a
 * point (the topmost backdrop, inert nodes skipped). jsdom has neither
 * showModal(), nor that pseudo-class, nor hit-testing, so both answers are
 * stood in for.
 */
function stubModal(
  modal: () => Element | null,
  hit: (x: number, y: number) => Element | null = modal,
) {
  const query = document.querySelector.bind(document);
  vi.spyOn(document, "querySelector").mockImplementation(((selector: string) =>
    selector === ":modal" ? modal() : query(selector)) as typeof document.querySelector);
  Object.defineProperty(document, "elementFromPoint", { configurable: true, value: hit });
}

/**
 * A dialog opened with showModal(): the rest of the page is inert under it,
 * and its focusing steps move the focus inside — unless told not to, for a
 * modal the focus never reaches.
 */
function openModal({ focus = true } = {}) {
  const dialog = document.createElement("dialog");
  dialog.setAttribute("open", "");
  dialog.innerHTML = '<button type="button">campo do modal</button>';
  document.body.append(dialog);
  stubModal(() => (dialog.isConnected ? dialog : null));
  if (focus) act(() => dialog.querySelector("button")?.focus());
  return { close: () => act(() => dialog.remove()) };
}

describe("long press", () => {
  it("opens once the finger has been held still for the long-press time", () => {
    const { options } = setup();
    down();
    act(() => vi.advanceTimersByTime(LONG_PRESS_MS - 1));
    expect(options.onLongPress).not.toHaveBeenCalled();
    act(() => vi.advanceTimersByTime(1));
    expect(options.onLongPress).toHaveBeenCalledTimes(1);
    expect(options.onLongPress).toHaveBeenCalledWith("msg-1");
    up();
    act(() => vi.advanceTimersByTime(LONG_PRESS_MS * 2));
    expect(options.onLongPress).toHaveBeenCalledTimes(1);
  });

  it("is a tap, and nothing, when the finger lifts first", () => {
    const { options } = setup();
    down();
    act(() => vi.advanceTimersByTime(LONG_PRESS_MS - 50));
    up();
    act(() => vi.advanceTimersByTime(LONG_PRESS_MS));
    expect(options.onLongPress).not.toHaveBeenCalled();
    expect(options.onSwipeReply).not.toHaveBeenCalled();
  });

  it("survives a finger settling inside the slop", () => {
    const { options } = setup();
    down();
    move(TOUCH_SLOP_PX - 1, 3);
    act(() => vi.advanceTimersByTime(LONG_PRESS_MS));
    expect(options.onLongPress).toHaveBeenCalledTimes(1);
  });

  it("never opens late once a vertical scroll has started", () => {
    const { options } = setup();
    down();
    move(2, -40);
    act(() => vi.advanceTimersByTime(LONG_PRESS_MS * 3));
    up();
    expect(options.onLongPress).not.toHaveBeenCalled();
    expect(options.onSwipeReply).not.toHaveBeenCalled();
  });

  it("is cancelled by pointercancel — the browser taking the touch over", () => {
    const { options } = setup();
    down();
    fireEvent.pointerCancel(text(), { pointerType: "touch", pointerId: 1 });
    act(() => vi.advanceTimersByTime(LONG_PRESS_MS));
    expect(options.onLongPress).not.toHaveBeenCalled();
  });

  it("ignores a pointercancel for some other pointer", () => {
    const { options } = setup();
    down();
    fireEvent.pointerCancel(text(), { pointerType: "touch", pointerId: 9 });
    act(() => vi.advanceTimersByTime(LONG_PRESS_MS));
    expect(options.onLongPress).toHaveBeenCalledTimes(1);
  });

  it("is cancelled by a second finger", () => {
    const { options } = setup();
    down();
    down(text(), { pointerId: 2, isPrimary: false });
    act(() => vi.advanceTimersByTime(LONG_PRESS_MS));
    expect(options.onLongPress).not.toHaveBeenCalled();
  });

  it("leaves no timer behind on unmount", () => {
    const { options, unmount } = setup();
    down();
    unmount();
    act(() => vi.advanceTimersByTime(LONG_PRESS_MS));
    expect(options.onLongPress).not.toHaveBeenCalled();
    expect(vi.getTimerCount()).toBe(0);
  });

  it("does not carry a press over to the next message a recycled row shows", () => {
    const { options, rerender } = setup();
    down();
    rerender({ messageId: "msg-2" });
    act(() => vi.advanceTimersByTime(LONG_PRESS_MS));
    expect(options.onLongPress).not.toHaveBeenCalled();
  });

  it("opens once when Android's contextmenu arrives before the timer", () => {
    const { options } = setup();
    down();
    const event = new MouseEvent("contextmenu", { bubbles: true, cancelable: true });
    act(() => {
      text().dispatchEvent(event);
    });
    expect(event.defaultPrevented).toBe(true);
    expect(options.onLongPress).toHaveBeenCalledTimes(1);
    act(() => vi.advanceTimersByTime(LONG_PRESS_MS));
    // A second contextmenu for the same hold is suppressed, and fires nothing.
    const again = new MouseEvent("contextmenu", { bubbles: true, cancelable: true });
    act(() => {
      text().dispatchEvent(again);
    });
    expect(again.defaultPrevented).toBe(true);
    expect(options.onLongPress).toHaveBeenCalledTimes(1);
  });

  it("is cancelled when the message is removed under the finger", () => {
    const { options, rerender } = setup();
    down();
    rerender({ disabled: true });
    act(() => vi.advanceTimersByTime(LONG_PRESS_MS));
    expect(options.onLongPress).not.toHaveBeenCalled();
    expect(vi.getTimerCount()).toBe(0);
  });

  it("asks again when its time comes: a row no longer in the document opens nothing", () => {
    const { options } = setup();
    down();
    const shown = row();
    const parent = shown.parentElement!;
    shown.remove();
    act(() => vi.advanceTimersByTime(LONG_PRESS_MS));
    expect(options.onLongPress).not.toHaveBeenCalled();
    parent.append(shown);
  });

  it("reads the options of the render current when it fires, not the one it started in", () => {
    const first = vi.fn();
    const { rerender } = setup({ onLongPress: first });
    down();
    const latest = vi.fn();
    rerender({ onLongPress: latest });
    act(() => vi.advanceTimersByTime(LONG_PRESS_MS));
    expect(first).not.toHaveBeenCalled();
    expect(latest).toHaveBeenCalledWith("msg-1");
  });

  it("is cancelled when anything scrolls under the finger", () => {
    const { options } = setup();
    down();
    fireEvent.scroll(document);
    act(() => vi.advanceTimersByTime(LONG_PRESS_MS));
    expect(options.onLongPress).not.toHaveBeenCalled();
  });

  it("is cancelled by a second finger landing anywhere, not only on this row", () => {
    const { options } = setup();
    down();
    fireEvent.pointerDown(document.body, { pointerType: "touch", pointerId: 2, isPrimary: false });
    act(() => vi.advanceTimersByTime(LONG_PRESS_MS));
    expect(options.onLongPress).not.toHaveBeenCalled();
  });

  it("is cancelled when the finger leaves the row", () => {
    const { options } = setup();
    down();
    fireEvent.pointerLeave(text(), { pointerType: "touch", pointerId: 1 });
    act(() => vi.advanceTimersByTime(LONG_PRESS_MS));
    expect(options.onLongPress).not.toHaveBeenCalled();
  });

  it("stops listening to the document once the press is over", () => {
    const remove = vi.spyOn(document, "removeEventListener");
    setup();
    down();
    up();
    expect(remove).toHaveBeenCalledWith("pointerdown", expect.any(Function), true);
    expect(remove).toHaveBeenCalledWith("scroll", expect.any(Function), true);
    expect(remove).toHaveBeenCalledWith("focusin", expect.any(Function), true);
    remove.mockRestore();
  });

  it("leaves a mouse right-click's contextmenu alone", () => {
    setup();
    const event = new MouseEvent("contextmenu", { bubbles: true, cancelable: true });
    text().dispatchEvent(event);
    expect(event.defaultPrevented).toBe(false);
  });
});

describe("what a gesture may start on", () => {
  it.each([
    ["mouse", "mouse"],
    ["pen", "pen"],
  ])("ignores %s pointers — hover and focus serve them", (_label, pointerType) => {
    const { options } = setup();
    down(text(), { pointerType });
    act(() => vi.advanceTimersByTime(LONG_PRESS_MS));
    expect(options.onLongPress).not.toHaveBeenCalled();
  });

  it.each([
    ["a link", () => screen.getByRole("link")],
    ["a button", () => screen.getByRole("button", { name: "ação" })],
    ["media", () => screen.getByRole("img", { name: "anexo" })],
    ["the inline editor", () => screen.getByText("editor")],
    ["the menu it opened", () => screen.getByText("fundo do menu")],
  ])("leaves a press on %s to that control", (_label, target) => {
    const { options } = setup();
    down(target());
    act(() => vi.advanceTimersByTime(LONG_PRESS_MS));
    drag(SWIPE_REPLY_PX + 20, 0);
    up();
    expect(options.onLongPress).not.toHaveBeenCalled();
    expect(options.onSwipeReply).not.toHaveBeenCalled();
  });

  it("starts nothing on a message nothing may be done to", () => {
    const { options } = setup({ disabled: true });
    down();
    act(() => vi.advanceTimersByTime(LONG_PRESS_MS));
    drag(SWIPE_REPLY_PX + 20, 0);
    up();
    expect(options.onLongPress).not.toHaveBeenCalled();
    expect(options.onSwipeReply).not.toHaveBeenCalled();
  });
});

describe("swipe to reply", () => {
  it("replies exactly once to a deliberate swipe right, on release", () => {
    const { options } = setup();
    down();
    drag(SWIPE_REPLY_PX + 10, 4);
    expect(options.onSwipeReply).not.toHaveBeenCalled();
    up();
    expect(options.onSwipeReply).toHaveBeenCalledTimes(1);
    expect(options.onSwipeReply).toHaveBeenCalledWith("msg-1");
    up();
    expect(options.onSwipeReply).toHaveBeenCalledTimes(1);
    act(() => vi.advanceTimersByTime(LONG_PRESS_MS));
    expect(options.onLongPress).not.toHaveBeenCalled();
  });

  it("follows the finger by transform only, capped, and resets on release", () => {
    setup();
    down();
    drag(40, 0);
    expect(row().style.transform).toBe("translateX(40px)");
    expect(row()).toHaveAttribute("data-swiping");
    expect(row()).not.toHaveAttribute("data-swipe-armed");
    drag(400, 0);
    expect(row().style.transform).toBe("translateX(96px)");
    expect(row()).toHaveAttribute("data-swipe-armed");
    up();
    expect(row().style.transform).toBe("");
    expect(row()).not.toHaveAttribute("data-swiping");
    expect(row()).not.toHaveAttribute("data-swipe-armed");
  });

  it("does nothing for a swipe short of the threshold", () => {
    const { options } = setup();
    down();
    drag(SWIPE_REPLY_PX - 1, 0);
    up();
    expect(options.onSwipeReply).not.toHaveBeenCalled();
  });

  it("does nothing for a diagonal swipe", () => {
    const { options } = setup();
    down();
    drag(SWIPE_REPLY_PX + 10, SWIPE_REPLY_PX);
    up();
    expect(options.onSwipeReply).not.toHaveBeenCalled();
  });

  it("does nothing once a recognised swipe drifts too far vertically", () => {
    const { options } = setup();
    down();
    drag(30, 0);
    move(SWIPE_REPLY_PX + 10, 60);
    up();
    expect(options.onSwipeReply).not.toHaveBeenCalled();
  });

  it("does nothing for a swipe that turns back", () => {
    const { options } = setup();
    down();
    drag(SWIPE_REPLY_PX + 20, 0);
    move(-20, 0);
    expect(row().style.transform).toBe("");
    move(20, 0);
    up();
    expect(options.onSwipeReply).not.toHaveBeenCalled();
  });

  it("stays cancelled through right → left → right past the threshold again", () => {
    const { options } = setup();
    down();
    drag(SWIPE_REPLY_PX + 20, 0);
    move(SWIPE_REPLY_PX - 20, 0);
    expect(row().style.transform).toBe("");
    move(SWIPE_REPLY_PX + 30, 0);
    expect(row().style.transform).toBe("");
    expect(row()).not.toHaveAttribute("data-swipe-armed");
    up();
    expect(options.onSwipeReply).not.toHaveBeenCalled();
  });

  it("measures the release where the finger lifts: short of the threshold, no reply", () => {
    const { options } = setup();
    down();
    drag(SWIPE_REPLY_PX + 20, 0);
    expect(row()).toHaveAttribute("data-swipe-armed");
    up(1, { x: START.x + SWIPE_REPLY_PX - 10, y: START.y });
    expect(options.onSwipeReply).not.toHaveBeenCalled();
  });

  it("tolerates a finger wobbling back inside the slop", () => {
    const { options } = setup();
    down();
    drag(SWIPE_REPLY_PX + 20, 0);
    move(SWIPE_REPLY_PX + 20 - TOUCH_SLOP_PX, 2);
    up();
    expect(options.onSwipeReply).toHaveBeenCalledTimes(1);
  });

  it("is over for good once it turns more vertical than horizontal", () => {
    const { options } = setup();
    down();
    drag(30, 0);
    move(30, 45);
    expect(row().style.transform).toBe("");
    drag(SWIPE_REPLY_PX + 20, 0);
    up();
    expect(options.onSwipeReply).not.toHaveBeenCalled();
  });

  it("does not reply to a message removed during the drag", () => {
    const { options, rerender } = setup();
    down();
    drag(SWIPE_REPLY_PX + 20, 0);
    rerender({ disabled: true });
    up();
    expect(options.onSwipeReply).not.toHaveBeenCalled();
    expect(row().style.transform).toBe("");
  });

  it.each([
    ["leftward", -(SWIPE_REPLY_PX + 20), 0],
    ["vertical", 0, SWIPE_REPLY_PX + 20],
  ])("leaves a %s move to the browser", (_label, dx, dy) => {
    const { options } = setup();
    down();
    drag(dx, dy);
    drag(SWIPE_REPLY_PX + 20, 0);
    up();
    expect(options.onSwipeReply).not.toHaveBeenCalled();
    expect(row().style.transform).toBe("");
  });

  it("leaves a swipe from the screen's left edge to the OS back gesture", () => {
    const { options } = setup();
    const edge = { x: EDGE_GUARD_PX - 1, y: START.y };
    down(text(), { clientX: edge.x, clientY: edge.y });
    drag(SWIPE_REPLY_PX + 20, 0, edge);
    up();
    expect(options.onSwipeReply).not.toHaveBeenCalled();
  });

  it("does not reply while a menu, picker or dialog is open", () => {
    const { options } = setup({ swipeBlocked: true });
    down();
    drag(SWIPE_REPLY_PX + 20, 0);
    up();
    expect(options.onSwipeReply).not.toHaveBeenCalled();
  });

  it("does not reply when a menu opened during the drag", () => {
    const { options, rerender } = setup();
    down();
    drag(SWIPE_REPLY_PX + 20, 0);
    rerender({ swipeBlocked: true });
    up();
    expect(options.onSwipeReply).not.toHaveBeenCalled();
  });

  it("is cancelled without side effects by a second finger mid-swipe", () => {
    const { options } = setup();
    down();
    drag(SWIPE_REPLY_PX + 20, 0);
    down(text(), { pointerId: 2, isPrimary: false });
    expect(row().style.transform).toBe("");
    up();
    expect(options.onSwipeReply).not.toHaveBeenCalled();
  });

  it("is cancelled by pointercancel mid-swipe", () => {
    const { options } = setup();
    down();
    drag(SWIPE_REPLY_PX + 20, 0);
    fireEvent.pointerCancel(text(), { pointerType: "touch", pointerId: 1 });
    up();
    expect(options.onSwipeReply).not.toHaveBeenCalled();
    expect(row().style.transform).toBe("");
  });

  it("ignores moves of a pointer it is not tracking", () => {
    const { options } = setup();
    down();
    move(SWIPE_REPLY_PX + 20, 0, 7);
    up(7);
    act(() => vi.advanceTimersByTime(LONG_PRESS_MS));
    expect(options.onSwipeReply).not.toHaveBeenCalled();
    expect(options.onLongPress).toHaveBeenCalledTimes(1);
  });

  it("does not become a swipe after a long press has fired", () => {
    const { options } = setup();
    down();
    act(() => vi.advanceTimersByTime(LONG_PRESS_MS));
    drag(SWIPE_REPLY_PX + 20, 0);
    up();
    expect(options.onLongPress).toHaveBeenCalledTimes(1);
    expect(options.onSwipeReply).not.toHaveBeenCalled();
    expect(row().style.transform).toBe("");
  });

  it("reports the message the swipe started on, after the row's options change", () => {
    const onSwipeReply = vi.fn();
    const { rerender } = setup({ onSwipeReply });
    down();
    drag(SWIPE_REPLY_PX + 20, 0);
    const latest = vi.fn();
    rerender({ onSwipeReply: latest });
    up();
    expect(onSwipeReply).not.toHaveBeenCalled();
    expect(latest).toHaveBeenCalledWith("msg-1");
  });
});

describe("a modal dialog taking over the page", () => {
  it("cancels a pending long press when a modal opens and takes the focus", () => {
    const { options } = setup();
    down();
    openModal();
    act(() => vi.advanceTimersByTime(LONG_PRESS_MS * 2));
    up();
    expect(options.onLongPress).not.toHaveBeenCalled();
  });

  it("asks again when its time comes: a modal the focus never reached still blocks it", () => {
    const { options } = setup();
    down();
    openModal({ focus: false });
    act(() => vi.advanceTimersByTime(LONG_PRESS_MS));
    up();
    expect(options.onLongPress).not.toHaveBeenCalled();
  });

  it("starts nothing under a modal already open", () => {
    const { options } = setup();
    openModal();
    down();
    act(() => vi.advanceTimersByTime(LONG_PRESS_MS));
    drag(SWIPE_REPLY_PX + 20, 0);
    up();
    expect(options.onLongPress).not.toHaveBeenCalled();
    expect(options.onSwipeReply).not.toHaveBeenCalled();
    expect(row()).not.toHaveAttribute("data-swiping");
  });

  it("drops a swipe in progress when a modal opens: no reply on release", () => {
    const { options } = setup();
    down();
    drag(SWIPE_REPLY_PX + 20, 0);
    openModal();
    expect(row().style.transform).toBe("");
    expect(row()).not.toHaveAttribute("data-swipe-armed");
    up();
    expect(options.onSwipeReply).not.toHaveBeenCalled();
  });

  it("does not reply to a swipe released under a modal the focus never reached", () => {
    const { options } = setup();
    down();
    drag(SWIPE_REPLY_PX + 20, 0);
    openModal({ focus: false });
    up();
    expect(options.onSwipeReply).not.toHaveBeenCalled();
  });

  it("does not revive the old press once the modal closes, and a new one works", () => {
    const { options } = setup();
    down();
    openModal().close();
    act(() => vi.advanceTimersByTime(LONG_PRESS_MS));
    drag(SWIPE_REPLY_PX + 20, 0);
    up();
    expect(options.onLongPress).not.toHaveBeenCalled();
    expect(options.onSwipeReply).not.toHaveBeenCalled();

    down();
    act(() => vi.advanceTimersByTime(LONG_PRESS_MS));
    up();
    expect(options.onLongPress).toHaveBeenCalledTimes(1);
    last = START;
    down();
    drag(SWIPE_REPLY_PX + 20, 0);
    up();
    expect(options.onSwipeReply).toHaveBeenCalledTimes(1);
  });

  it("leaves a press alone when the focus moves without a modal", () => {
    const { options } = setup();
    down();
    act(() => screen.getByRole("button", { name: "ação" }).focus());
    act(() => vi.advanceTimersByTime(LONG_PRESS_MS));
    expect(options.onLongPress).toHaveBeenCalledWith("msg-1");
  });

  it("still answers inside the modal or fullscreen element that holds the row", () => {
    const { options } = setup();
    stubModal(
      () => row().parentElement,
      () => text(),
    );
    down();
    act(() => vi.advanceTimersByTime(LONG_PRESS_MS));
    expect(options.onLongPress).toHaveBeenCalledWith("msg-1");
  });

  it("goes by the surface the browser hit-tests, not the first :modal in the DOM", () => {
    const { options } = setup();
    // A fullscreen ancestor of the row comes first in the DOM; a dialog opened
    // over it is the active modal, and its backdrop is what the finger is on.
    const dialog = document.createElement("dialog");
    document.body.append(dialog);
    stubModal(
      () => row().parentElement,
      () => dialog,
    );
    down();
    act(() => vi.advanceTimersByTime(LONG_PRESS_MS));
    up();
    expect(options.onLongPress).not.toHaveBeenCalled();
    dialog.remove();
  });

  it("blocks when the hit-test reaches nothing at all", () => {
    const { options } = setup();
    stubModal(
      () => row().parentElement,
      () => null,
    );
    down();
    act(() => vi.advanceTimersByTime(LONG_PRESS_MS));
    expect(options.onLongPress).not.toHaveBeenCalled();
  });

  it("hit-tests where the finger is now, not where it started", () => {
    const { options } = setup();
    const end = { x: START.x + SWIPE_REPLY_PX + 20, y: START.y };
    const hit = vi.fn((x: number, y: number) => (x === end.x && y === end.y ? text() : null));
    stubModal(() => row().parentElement, hit);
    down();
    drag(SWIPE_REPLY_PX + 20, 0);
    up();
    expect(hit).toHaveBeenLastCalledWith(end.x, end.y);
    expect(options.onSwipeReply).toHaveBeenCalledWith("msg-1");
  });

  describe("revalidated where the finger lifts", () => {
    const moved = { x: 192, y: 300 };
    const lifted = { x: 190, y: 300 };
    /** The browser's hit-test: the row is under `onRow` only. */
    const rowOnlyAt = (onRow: { x: number; y: number }) =>
      vi.fn((x: number, y: number) => (x === onRow.x && y === onRow.y ? text() : null));

    function swipeTo(at: { x: number; y: number }) {
      down();
      drag(moved.x - START.x, moved.y - START.y);
      up(1, at);
    }

    it("replies once when the release, not the last move, is on the row", () => {
      const { options } = setup();
      const hit = rowOnlyAt(lifted);
      stubModal(() => row().parentElement, hit);
      swipeTo(lifted);
      expect(hit).toHaveBeenLastCalledWith(lifted.x, lifted.y);
      expect(options.onSwipeReply).toHaveBeenCalledTimes(1);
      expect(options.onSwipeReply).toHaveBeenCalledWith("msg-1");
    });

    it("does not reply when the release lands behind the modal, though the last move did not", () => {
      const { options } = setup();
      stubModal(() => row().parentElement, rowOnlyAt(moved));
      swipeTo(lifted);
      expect(options.onSwipeReply).not.toHaveBeenCalled();
      expect(row().style.transform).toBe("");
    });

    it("still applies the slop: a release turned back past it does not reply", () => {
      const { options } = setup();
      const back = { x: moved.x - TOUCH_SLOP_PX - 1, y: moved.y };
      stubModal(() => row().parentElement, rowOnlyAt(back));
      swipeTo(back);
      expect(options.onSwipeReply).not.toHaveBeenCalled();
    });
  });
});
