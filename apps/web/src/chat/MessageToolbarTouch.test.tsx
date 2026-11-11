/**
 * The reaction menu opened by a long press (issue #1152).
 *
 * The regression, measured in Chromium at 320px before the fix: a received
 * message's bubble spans the row (61→308 of 320px), so there is no room beside
 * it, and its neighbours end 26px above and start 24px below it — less than
 * the 36px toolbar plus gap and padding. Every anchored placement failed, and
 * the desktop verdict for that case, dismiss(), made the toolbar vanish. The
 * geometry below is that measurement. The desktop verdict is pinned too: only
 * a touch-opened menu falls back to the bottom sheet.
 */

import { act, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { emptyEmojiUsage } from "./emoji/emojiUsage";
import MessageBubble from "./MessageBubble";
import type { MessageBubbleProps } from "./MessageBubble";
import type { Message } from "./chatTypes";
import { flushResizeObservers, observedElements } from "../setupTests";

const SHEET = "chat-msg-area__reaction-menu--sheet";
const LIST = { top: 117, bottom: 587, left: 0, right: 320 };
const MENU = { width: 246, height: 36 };

function rect(top: number, left: number, width: number, height: number): DOMRect {
  const r = { top, left, width, height, bottom: top + height, right: left + width };
  return { ...r, x: left, y: top, toJSON: () => ({}) } as DOMRect;
}

/** Bubble rects by message id: previous, target, next — as measured. */
const bubbles: Record<string, DOMRect> = {};

function resetGeometry() {
  bubbles["msg-0"] = rect(270, 261, 47, 41);
  bubbles["msg-1"] = rect(337, 61, 247, 167);
  bubbles["msg-2"] = rect(528, 261, 47, 41);
}

function messageWith(id: string, overrides: Partial<Message> = {}): Message {
  return {
    id,
    senderId: "user-1",
    senderDisplayName: "Alex Souza",
    senderEmail: "alex@example.test",
    kind: "user",
    bodyText: "olá",
    bodyFormat: "v2",
    isRemoved: false,
    status: "active",
    linkSafetyState: "",
    deletedAt: null,
    createdAt: "2026-08-17T12:00:00Z",
    updatedAt: "2026-08-17T12:00:00Z",
    isEdited: false,
    editCount: 0,
    reactions: [],
    isFavorited: false,
    isForwarded: false,
    ...overrides,
  };
}

function propsWith(overrides: Partial<MessageBubbleProps> = {}): MessageBubbleProps {
  return {
    message: messageWith("msg-1"),
    isMine: false,
    onToggleReaction: vi.fn(),
    onReplyMessage: vi.fn(),
    onReferenceMessage: vi.fn(),
    onToggleFavorite: vi.fn(),
    onEditMessage: vi.fn(),
    onEditForbidden: vi.fn(),
    onDeleteMessage: vi.fn(),
    recentReactionEmojis: ["👍"],
    emojiUsage: emptyEmojiUsage,
    onEmojiToneChange: vi.fn(),
    currentUserId: "me",
    reactionMenuVisible: true,
    onReactionMenuVisibleChange: vi.fn(),
    pickerOpen: false,
    onPickerOpenChange: vi.fn(),
    reactionMenuTouch: true,
    onReactionMenuLongPress: vi.fn(),
    ...overrides,
  };
}

function Timeline(props: MessageBubbleProps) {
  const idle = { reactionMenuVisible: false, reactionMenuTouch: false, pickerOpen: false };
  return (
    <div className="chat-msg-area__list">
      <MessageBubble {...props} {...idle} message={messageWith("msg-0")} />
      <MessageBubble {...props} />
      <MessageBubble {...props} {...idle} message={messageWith("msg-2")} />
      <button type="button">fora</button>
    </div>
  );
}

function renderMenu(overrides: Partial<MessageBubbleProps> = {}) {
  const props = propsWith(overrides);
  const view = render(<Timeline {...props} />);
  return {
    ...view,
    props,
    rerender: (next: Partial<MessageBubbleProps>) =>
      view.rerender(<Timeline {...props} {...next} />),
  };
}

const menu = () => screen.getByRole("toolbar", { name: "Reagir à mensagem" });
const shellOf = (id: string) =>
  document.querySelector<HTMLElement>(`[data-message-id="${id}"]`) as HTMLElement;

beforeEach(() => {
  resetGeometry();
  vi.spyOn(Element.prototype, "getBoundingClientRect").mockImplementation(function (this: Element) {
    if (this.classList.contains("chat-msg-area__list")) {
      return rect(LIST.top, LIST.left, LIST.right - LIST.left, LIST.bottom - LIST.top);
    }
    if (this.classList.contains("chat-msg-area__msg-bubble")) {
      const id = this.closest("[data-message-id]")?.getAttribute("data-message-id") ?? "";
      return bubbles[id] ?? rect(0, 0, 0, 0);
    }
    if (this.getAttribute("role") === "toolbar") return rect(0, 0, MENU.width, MENU.height);
    return rect(0, 0, 0, 0);
  });
  vi.spyOn(window, "innerWidth", "get").mockReturnValue(320);
  vi.spyOn(window, "innerHeight", "get").mockReturnValue(740);
});

afterEach(() => {
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

describe("touch-opened reaction menu placement (issue #1152)", () => {
  it("keeps the desktop verdict for a hover menu with no room: it closes", () => {
    const { props } = renderMenu({ reactionMenuTouch: false });
    expect(props.onReactionMenuVisibleChange).toHaveBeenCalledWith("msg-1", false);
    expect(props.onPickerOpenChange).toHaveBeenCalledWith("msg-1", false);
  });

  it("falls back to a visible bottom sheet instead of vanishing", () => {
    const { props } = renderMenu();
    expect(menu()).toHaveClass(SHEET, "chat-msg-area__reaction-menu--touch");
    expect(menu()).toHaveStyle({ visibility: "visible" });
    // Without a Visual Viewport API, the window: 8px in from either side.
    expect(menu().style.left).toContain("8px");
    expect(menu().style.width).toContain("304px");
    expect(menu().style.top).toBe("");
    expect(menu().style.bottom).toContain("8px");
    expect(menu().style.maxHeight).toContain("724px");
    expect(props.onReactionMenuVisibleChange).not.toHaveBeenCalledWith("msg-1", false);
  });

  it("stays anchored to the bubble when a touch menu has room", () => {
    // The previous message far enough up to leave the toolbar room above.
    bubbles["msg-0"] = rect(150, 261, 47, 41);
    renderMenu();
    expect(menu()).not.toHaveClass(SHEET);
    expect(menu()).toHaveStyle({ top: "298px", left: "66px", visibility: "visible" });
  });

  // Measured in Chromium at 320px with touch-sized targets: the toolbar was
  // "placed" above the first message at x = -62, wider than the list itself.
  it("never clamps a touch toolbar wider than the list off-screen", () => {
    bubbles["msg-0"] = rect(150, 261, 47, 41);
    MENU.width = 400;
    try {
      renderMenu();
      expect(menu()).toHaveClass(SHEET);
      expect(menu().style.width).toContain("304px");
    } finally {
      MENU.width = 246;
    }
  });

  it("does not flip back out of the sheet on later commits", () => {
    const { rerender } = renderMenu();
    // Now there would be room, measured in the sheet's own layout: ignored.
    bubbles["msg-0"] = rect(150, 261, 47, 41);
    rerender({ isPinned: true });
    expect(menu()).toHaveClass(SHEET);
  });

  it("closes, sheet or not, once its message has left the list's band", () => {
    const { props } = renderMenu();
    expect(menu()).toHaveClass(SHEET);
    bubbles["msg-1"] = rect(-400, 61, 247, 167);
    act(() => {
      document.dispatchEvent(new Event("scroll"));
    });
    expect(props.onReactionMenuVisibleChange).toHaveBeenCalledWith("msg-1", false);
  });

  it("lifts the sheet above what the visual viewport says is covered", () => {
    const listeners: Record<string, EventListener> = {};
    const visual = {
      offsetLeft: 0,
      offsetTop: 0,
      width: 320,
      height: 440,
      addEventListener: vi.fn((type: string, listener: EventListener) => {
        listeners[type] = listener;
      }),
      removeEventListener: vi.fn(),
    };
    vi.stubGlobal("visualViewport", visual);
    const { unmount } = renderMenu();
    // 740 - 440: a keyboard covers the bottom 300px of the layout viewport.
    expect(menu().style.bottom).toContain("308px");

    visual.height = 740;
    act(() => listeners.resize(new Event("resize")));
    expect(menu().style.bottom).toContain("8px");
    expect(menu().style.bottom).not.toContain("308px");

    unmount();
    expect(visual.removeEventListener).toHaveBeenCalledWith("resize", listeners.resize);
    expect(visual.removeEventListener).toHaveBeenCalledWith("scroll", listeners.scroll);
  });

  /** A visual viewport: zoomed in, or shortened by the keyboard, and panned. */
  function stubVisualViewport(view: {
    offsetLeft: number;
    offsetTop: number;
    width: number;
    height: number;
  }) {
    const listeners: Record<string, EventListener> = {};
    const visual = {
      ...view,
      addEventListener: vi.fn((type: string, listener: EventListener) => {
        listeners[type] = listener;
      }),
      removeEventListener: vi.fn(),
    };
    vi.stubGlobal("visualViewport", visual);
    return { visual, listeners };
  }

  it("keeps the sheet inside a reduced, shifted visual viewport", () => {
    // Zoomed in and panned: the reader sees x 40–240, y 100–400.
    stubVisualViewport({ offsetLeft: 40, offsetTop: 100, width: 200, height: 300 });
    renderMenu();
    expect(menu()).toHaveClass(SHEET);
    expect(menu().style.left).toContain("48px");
    expect(menu().style.width).toContain("184px");
    // 740 - 400 below what is visible, plus the padding.
    expect(menu().style.bottom).toContain("348px");
    expect(menu().style.maxHeight).toContain("284px");
  });

  it("follows the visual viewport when it pans, re-placing the sheet", () => {
    const { visual, listeners } = stubVisualViewport({
      offsetLeft: 0,
      offsetTop: 0,
      width: 320,
      height: 740,
    });
    renderMenu();
    expect(menu().style.left).toContain("8px");
    visual.offsetLeft = 60;
    act(() => listeners.scroll(new Event("scroll")));
    expect(menu().style.left).toContain("68px");
  });

  it("does not anchor where the zoomed visual viewport cannot show it", () => {
    // Room above the bubble in the list — anchored at top 298 without zoom —
    // but the visual viewport starts at 300: the toolbar would be off-screen.
    bubbles["msg-0"] = rect(150, 261, 47, 41);
    stubVisualViewport({ offsetLeft: 0, offsetTop: 300, width: 320, height: 300 });
    renderMenu();
    expect(menu()).toHaveClass(SHEET);
    expect(menu().style.bottom).toContain("148px");
  });

  it("does not clamp a touch toolbar outside a horizontally panned visual viewport", () => {
    bubbles["msg-0"] = rect(150, 261, 47, 41);
    // Only x 100–320 is visible: 246px of toolbar does not fit in 220px.
    stubVisualViewport({ offsetLeft: 100, offsetTop: 0, width: 220, height: 740 });
    renderMenu();
    expect(menu()).toHaveClass(SHEET);
    expect(menu().style.left).toContain("108px");
    expect(menu().style.width).toContain("204px");
  });

  it("leaves a hover menu's band alone: the desktop placement ignores the visual viewport", () => {
    bubbles["msg-0"] = rect(150, 261, 47, 41);
    stubVisualViewport({ offsetLeft: 0, offsetTop: 300, width: 320, height: 300 });
    renderMenu({ reactionMenuTouch: false });
    expect(menu()).not.toHaveClass(SHEET);
    expect(menu()).toHaveStyle({ top: "298px", left: "66px", visibility: "visible" });
  });

  it("caps a touch-opened picker to the visual viewport, so it scrolls instead of closing", () => {
    stubVisualViewport({ offsetLeft: 0, offsetTop: 0, width: 320, height: 440 });
    renderMenu({ pickerOpen: true });
    const picker = document.querySelector<HTMLElement>(
      '[role="dialog"][aria-label="Escolher reação"]',
    )!;
    // The larger side of its menu (0–36, which the picker keeps clear of),
    // less the gap and the padding.
    expect(picker.style.getPropertyValue("--chat-emoji-picker-max-height")).toBe("389px");
    expect(picker.style.getPropertyValue("--chat-emoji-picker-max-width")).toBe("304px");
  });

  it("places a touch-opened picker clear of the whole menu, lined up with its button", () => {
    // A sheet of two rows, its "Mais reações" button in the second one. The
    // menu is in the top layer: a picker placed against the button alone would
    // sit under the first row, where nothing in it can be touched.
    const sheet = rect(600, 8, 304, 110);
    const button = rect(660, 250, 44, 44);
    const box = rect(0, 0, 280, 300);
    const measure = vi.mocked(Element.prototype.getBoundingClientRect).getMockImplementation()!;
    vi.mocked(Element.prototype.getBoundingClientRect).mockImplementation(function (this: Element) {
      if (this.getAttribute("role") === "toolbar") return sheet;
      if (this.getAttribute("aria-label") === "Mais reações") return button;
      if (this.getAttribute("aria-label") === "Escolher reação") return box;
      return measure.call(this);
    });
    renderMenu({ pickerOpen: true });
    const picker = document.querySelector<HTMLElement>(
      '[role="dialog"][aria-label="Escolher reação"]',
    )!;
    expect(picker.style.top).toBe(`${600 - 300 - 7}px`);
    // Right edges lined up with the button, as before, inside the padding.
    expect(picker.style.left).toBe(`${250 + 44 - 280}px`);
    expect(picker).toHaveStyle({ visibility: "visible" });
  });

  /**
   * Geometry for a touch picker over a sheet whose box can change under it
   * (CQ R9): rects are read live, so a test moves the sheet, the results area
   * or the first cell and lets the observers report it.
   */
  function stubPickerGeometry(geometry: { sheet: DOMRect; results?: DOMRect; cell?: DOMRect }) {
    const box = rect(0, 0, 280, 300);
    const measure = vi.mocked(Element.prototype.getBoundingClientRect).getMockImplementation()!;
    vi.mocked(Element.prototype.getBoundingClientRect).mockImplementation(function (this: Element) {
      if (this.getAttribute("role") === "toolbar") return geometry.sheet;
      if (this.getAttribute("aria-label") === "Mais reações") return rect(660, 250, 44, 44);
      if (this.getAttribute("aria-label") === "Escolher reação") return box;
      if (this.classList.contains("chat-emoji-picker__sections") && geometry.results) {
        return geometry.results;
      }
      if (this.classList.contains("chat-emoji-picker__emoji") && geometry.cell) {
        return geometry.cell;
      }
      return measure.call(this);
    });
  }

  const pickerElement = () =>
    document.querySelector<HTMLElement>('[role="dialog"][aria-label="Escolher reação"]')!;

  it("follows the menu when it grows, and when it shrinks, under an open picker", () => {
    // Quick reactions arriving after the picker opened add a row to the sheet.
    const geometry = { sheet: rect(670, 8, 304, 62) };
    stubPickerGeometry(geometry);
    renderMenu({ pickerOpen: true });
    expect(pickerElement().style.top).toBe(`${670 - 300 - 7}px`);
    expect(observedElements()).toContain(menu());

    geometry.sheet = rect(622, 8, 304, 110);
    act(() => flushResizeObservers());
    expect(pickerElement().style.top).toBe(`${622 - 300 - 7}px`);

    geometry.sheet = rect(682, 8, 304, 50);
    act(() => flushResizeObservers());
    expect(pickerElement().style.top).toBe(`${682 - 300 - 7}px`);
  });

  it("stops watching the menu once the picker closes", () => {
    stubPickerGeometry({ sheet: rect(670, 8, 304, 62) });
    const { rerender } = renderMenu({ pickerOpen: true });
    expect(observedElements()).toContain(menu());
    rerender({ pickerOpen: false });
    expect(observedElements()).not.toContain(menu());
  });

  it("does not watch a hover menu: its picker clears only its button", () => {
    bubbles["msg-0"] = rect(150, 261, 47, 41);
    renderMenu({ reactionMenuTouch: false, pickerOpen: true });
    expect(observedElements()).not.toContain(menu());
  });

  it("sets the menu's other actions and the picker's tabs aside when no result row fits", async () => {
    // 320×220, measured: a results area 6px tall, its first cell 64px down.
    stubPickerGeometry({
      sheet: rect(102, 8, 304, 110),
      results: rect(103, 18, 284, 6),
      cell: rect(129, 18, 36, 44),
    });
    const { rerender } = renderMenu({ pickerOpen: true });
    await screen.findByRole("searchbox", { name: "Buscar emoji" });
    act(() => flushResizeObservers());
    expect(menu()).toHaveClass("chat-msg-area__reaction-menu--cramped");
    expect(pickerElement()).toHaveClass("chat-emoji-surface--cramped");

    // Once per opening: placed again, it stays as it is.
    act(() => flushResizeObservers());
    expect(menu()).toHaveClass("chat-msg-area__reaction-menu--cramped");

    // A closed picker gives every action back, with focus on its button.
    rerender({ pickerOpen: false });
    expect(menu()).not.toHaveClass("chat-msg-area__reaction-menu--cramped");
  });

  it("leaves the menu and the picker whole while a result row fits", async () => {
    vi.spyOn(HTMLElement.prototype, "clientHeight", "get").mockReturnValue(85);
    stubPickerGeometry({
      sheet: rect(202, 8, 304, 110),
      results: rect(103, 18, 284, 85),
      cell: rect(129, 18, 36, 44),
    });
    renderMenu({ pickerOpen: true });
    await screen.findByRole("searchbox", { name: "Buscar emoji" });
    act(() => flushResizeObservers());
    expect(menu()).not.toHaveClass("chat-msg-area__reaction-menu--cramped");
    expect(pickerElement()).not.toHaveClass("chat-emoji-surface--cramped");
  });

  it("never squeezes a hover menu, whatever room its picker has", async () => {
    bubbles["msg-0"] = rect(150, 261, 47, 41);
    stubPickerGeometry({
      sheet: rect(102, 8, 304, 36),
      results: rect(103, 18, 284, 6),
      cell: rect(129, 18, 36, 44),
    });
    renderMenu({ reactionMenuTouch: false, pickerOpen: true });
    await screen.findByRole("searchbox", { name: "Buscar emoji" });
    act(() => flushResizeObservers());
    expect(menu()).not.toHaveClass("chat-msg-area__reaction-menu--cramped");
  });

  /**
   * The tone palette beside a touch menu (CQ R10). Measured at 320×210 in
   * Chromium: a cell at 64–108 with no room above it, a sheet at 140–202. Kept
   * to the window, the palette went below the cell, to 114–168, and every
   * tone's centre was under the sheet.
   */
  function stubToneGeometry(geometry: { sheet: DOMRect; picker: DOMRect; cell: DOMRect }) {
    const measure = vi.mocked(Element.prototype.getBoundingClientRect).getMockImplementation()!;
    vi.mocked(Element.prototype.getBoundingClientRect).mockImplementation(function (this: Element) {
      if (this.getAttribute("role") === "toolbar") return geometry.sheet;
      if (this.getAttribute("aria-label") === "Mais reações") return rect(150, 250, 44, 44);
      if (this.getAttribute("aria-label") === "Escolher reação") return geometry.picker;
      if (this.classList.contains("chat-emoji-picker__emoji")) return geometry.cell;
      if (this.classList.contains("chat-emoji-tone")) return rect(0, 0, 248, 54);
      return measure.call(this);
    });
  }

  async function openTonePalette() {
    fireEvent.change(await screen.findByRole("searchbox", { name: "Buscar emoji" }), {
      target: { value: "polegar para cima" },
    });
    fireEvent.click(await screen.findByRole("button", { name: "polegar para cima" }));
    return screen.getByRole("dialog", { name: /Tom de pele/ });
  }

  const lowScreen = {
    sheet: rect(140, 8, 304, 62),
    picker: rect(8, 8, 304, 125),
    cell: rect(64, 18, 38, 44),
  };

  it("keeps the tone palette off a sheet below its picker", async () => {
    stubToneGeometry(lowScreen);
    renderMenu({ pickerOpen: true });
    const palette = await openTonePalette();
    // No room above (64 − 54 − 6 < 8) nor below (108 + 6 + 54 > 140 − 8): it
    // sits over its own cell, ending 8px above the sheet.
    expect(palette).toHaveStyle({ top: `${140 - 54 - 8}px`, visibility: "visible" });
  });

  it("keeps the tone palette off a menu above its picker", async () => {
    // The window has room above the cell (70 − 54 − 6 ≥ 8), the band below the
    // menu does not, so the palette goes under the cell.
    stubToneGeometry({
      sheet: rect(20, 8, 304, 36),
      picker: rect(63, 8, 304, 300),
      cell: rect(70, 18, 38, 44),
    });
    renderMenu({ pickerOpen: true });
    const palette = await openTonePalette();
    expect(palette).toHaveStyle({ top: `${70 + 44 + 6}px` });
  });

  it("leaves a hover menu's tone palette to the window", async () => {
    bubbles["msg-0"] = rect(150, 261, 47, 41);
    stubToneGeometry(lowScreen);
    renderMenu({ reactionMenuTouch: false, pickerOpen: true });
    const palette = await openTonePalette();
    expect(palette).toHaveStyle({ top: `${64 + 44 + 6}px` });
  });

  it("leaves a hover menu's picker at its usual size", () => {
    bubbles["msg-0"] = rect(150, 261, 47, 41);
    renderMenu({ reactionMenuTouch: false, pickerOpen: true });
    const picker = document.querySelector<HTMLElement>(
      '[role="dialog"][aria-label="Escolher reação"]',
    )!;
    expect(picker.style.getPropertyValue("--chat-emoji-picker-max-height")).toBe("");
  });
});

describe("touch-opened reaction menu lifecycle (issue #1152)", () => {
  it("moves focus to its first action when it opens", () => {
    renderMenu();
    expect(menu().querySelector("button")).toHaveFocus();
  });

  it("closes on Escape and returns focus to the message", () => {
    const { props } = renderMenu();
    fireEvent.keyDown(document, { key: "Escape" });
    expect(props.onReactionMenuVisibleChange).toHaveBeenCalledWith("msg-1", false);
    expect(shellOf("msg-1")).toHaveFocus();
  });

  it("leaves Escape to the picker while the picker is open", () => {
    const { props } = renderMenu({ pickerOpen: true });
    vi.mocked(props.onReactionMenuVisibleChange).mockClear();
    fireEvent.keyDown(document, { key: "Escape" });
    // The picker's own handler closes the picker only, keeping the menu.
    expect(props.onPickerOpenChange).toHaveBeenCalledWith("msg-1", false);
    expect(props.onReactionMenuVisibleChange).not.toHaveBeenCalledWith("msg-1", false);
  });

  it("closes on a press outside its message, not on one inside it", () => {
    const { props } = renderMenu();
    fireEvent.pointerDown(shellOf("msg-1"));
    expect(props.onReactionMenuVisibleChange).not.toHaveBeenCalledWith("msg-1", false);
    fireEvent.pointerDown(screen.getByRole("button", { name: "fora" }));
    expect(props.onReactionMenuVisibleChange).toHaveBeenCalledWith("msg-1", false);
  });

  it("does not close for a press on the skin-tone palette portalled to the body", () => {
    const { props } = renderMenu();
    const palette = document.createElement("div");
    palette.className = "chat-emoji-tone";
    document.body.append(palette);
    fireEvent.pointerDown(palette);
    expect(props.onReactionMenuVisibleChange).not.toHaveBeenCalledWith("msg-1", false);
    palette.remove();
  });

  it("closes after an action, through the existing callback, on its own message", () => {
    const { props } = renderMenu();
    fireEvent.click(screen.getByRole("button", { name: "Responder" }));
    expect(props.onReplyMessage).toHaveBeenCalledTimes(1);
    expect(props.onReplyMessage).toHaveBeenCalledWith(props.message);
    expect(props.onReactionMenuVisibleChange).toHaveBeenCalledWith("msg-1", false);
  });

  it("stays open when Mais reações opens the picker over it", () => {
    const { props } = renderMenu();
    fireEvent.click(screen.getByRole("button", { name: "Mais reações" }));
    expect(props.onPickerOpenChange).toHaveBeenCalledWith("msg-1", true);
    expect(props.onReactionMenuVisibleChange).not.toHaveBeenCalledWith("msg-1", false);
  });

  it("keeps delete behind the existing confirmation", () => {
    const confirm = vi.spyOn(window, "confirm").mockReturnValue(false);
    const { props } = renderMenu({ isMine: true, message: messageWith("msg-1") });
    fireEvent.click(screen.getByRole("button", { name: "Excluir mensagem" }));
    expect(confirm).toHaveBeenCalledWith("Excluir esta mensagem?");
    expect(props.onDeleteMessage).not.toHaveBeenCalled();
  });

  it("offers only the actions the current capabilities allow", () => {
    const { rerender } = renderMenu({ isMine: true });
    expect(screen.getByRole("button", { name: "Editar mensagem" })).toBeInTheDocument();
    rerender({ editDisabled: true });
    expect(screen.queryByRole("button", { name: "Editar mensagem" })).not.toBeInTheDocument();
    rerender({ onTogglePin: undefined, onForwardMessage: undefined });
    expect(screen.queryByRole("button", { name: /Fixar/ })).not.toBeInTheDocument();
  });

  it("disappears with its message once that message is removed", () => {
    const { rerender } = renderMenu();
    rerender({ message: messageWith("msg-1", { isRemoved: true }) });
    expect(screen.queryByRole("toolbar")).not.toBeInTheDocument();
  });

  it("does none of this for a hover menu: no outside-press close, no action close", () => {
    bubbles["msg-0"] = rect(150, 261, 47, 41);
    const { props } = renderMenu({ reactionMenuTouch: false });
    fireEvent.pointerDown(screen.getByRole("button", { name: "fora" }));
    fireEvent.click(screen.getByRole("button", { name: "Favoritar mensagem" }));
    fireEvent.keyDown(document, { key: "Escape" });
    expect(props.onReactionMenuVisibleChange).not.toHaveBeenCalledWith("msg-1", false);
    expect(menu()).not.toHaveClass("chat-msg-area__reaction-menu--touch");
  });

  it("removes its document listeners on unmount", () => {
    const remove = vi.spyOn(document, "removeEventListener");
    const { unmount } = renderMenu();
    unmount();
    const types = remove.mock.calls.map(([type]) => type);
    expect(types).toEqual(expect.arrayContaining(["pointerdown", "keydown", "scroll"]));
  });
});
