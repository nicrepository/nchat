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
/** The touch menu keeps its non-reaction actions behind "Mais ações" (issue #1238). */
const openMoreActions = () => fireEvent.click(screen.getByRole("button", { name: "Mais ações" }));
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
    // Where a sheet sits: at the bottom of the list's band (CQR R5 of #1238).
    // Anchored placement reads only its size.
    if (this.getAttribute("role") === "toolbar") {
      return rect(LIST.bottom - 8 - MENU.height, 8, MENU.width, MENU.height);
    }
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
    // Without a Visual Viewport API, the window's sides: 8px in from either.
    expect(menu().style.left).toContain("8px");
    expect(menu().style.width).toContain("304px");
    expect(menu().style.top).toBe("");
    // CQR R5 of #1238: above the composer, which starts where the list ends
    // (587 of 740), and below the header (117): no safe area to clear there.
    expect(menu().style.bottom).toBe(`calc(${740 - 587 + 8}px)`);
    expect(menu().style.maxHeight).toBe(`calc(${587 - 117 - 16}px)`);
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

  it("moves an anchored touch menu to the sheet once its picker opens (issue #1238)", () => {
    bubbles["msg-0"] = rect(150, 261, 47, 41);
    const { rerender } = renderMenu();
    expect(menu()).not.toHaveClass(SHEET);
    rerender({ pickerOpen: true });
    expect(menu()).toHaveClass(SHEET);
    // Decided once per opening: the picker closing does not move it back.
    rerender({ pickerOpen: false });
    expect(menu()).toHaveClass(SHEET);
  });

  /**
   * CQR R4 of issue #1238, measured at 1280×800 with touch and no Popover API:
   * the sheet spanned x=8…1272 over a sidebar at x=0…240. The column is the
   * list's sides; a wide screen keeps the menu by its message instead.
   */
  function wideTouchScreen() {
    vi.spyOn(window, "innerWidth", "get").mockReturnValue(1280);
    vi.spyOn(window, "innerHeight", "get").mockReturnValue(800);
    Object.assign(LIST, { top: 117, bottom: 730, left: 240, right: 1280 });
    bubbles["msg-0"] = rect(150, 300, 247, 41);
    bubbles["msg-1"] = rect(337, 300, 247, 40);
    bubbles["msg-2"] = rect(600, 300, 247, 41);
  }
  const restoreList = () => Object.assign(LIST, { top: 117, bottom: 587, left: 0, right: 320 });

  it("keeps an anchored menu by its message when its picker opens with room (CQR R4)", () => {
    wideTouchScreen();
    try {
      const { rerender } = renderMenu();
      expect(menu()).not.toHaveClass(SHEET);
      rerender({ pickerOpen: true });
      expect(menu()).not.toHaveClass(SHEET);
      expect(menu()).toHaveStyle({ visibility: "visible" });
    } finally {
      restoreList();
    }
  });

  it("bounds the sheet to the conversation's column, off the sidebar (CQR R4)", () => {
    wideTouchScreen();
    MENU.width = 1100;
    try {
      renderMenu();
      expect(menu()).toHaveClass(SHEET);
      // The column's left side is not the screen's: no safe area to clear.
      expect(menu().style.left).toContain("248px");
      expect(menu().style.left).not.toContain("safe-area");
      expect(menu().style.width).toContain("1024px");
      expect(menu().style.width).toContain("safe-area-inset-right");
    } finally {
      MENU.width = 246;
      restoreList();
    }
  });

  it("does not flip back out of the sheet on later commits", () => {
    const { rerender } = renderMenu();
    // Now there would be room, measured in the sheet's own layout: ignored.
    bubbles["msg-0"] = rect(150, 261, 47, 41);
    rerender({ isPinned: true });
    expect(menu()).toHaveClass(SHEET);
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
    // 740 - 440: a keyboard covers the bottom 300px of the layout viewport,
    // composer included — the band ends at the visible edge.
    expect(menu().style.bottom).toContain("308px");
    expect(menu().style.bottom).toContain("safe-area-inset-bottom");

    // The keyboard gone, the composer is visible again: the sheet sits above it.
    visual.height = 740;
    act(() => listeners.resize(new Event("resize")));
    expect(menu().style.bottom).toBe(`calc(${740 - 587 + 8}px)`);

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
    // From the list's top (117, below the header) to 400, less the padding.
    expect(menu().style.maxHeight).toContain(`${400 - 117 - 16}px`);
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
    // Visible down to 600, the composer from 587: the sheet sits above it.
    expect(menu().style.bottom).toBe(`calc(${740 - 587 + 8}px)`);
  });

  /**
   * CQR R5 of #1238, measured at 390×740, 768×1024 and 1280×560 in Chromium and
   * WebKit: with the picker open, the composer's centre was the sheet's or the
   * picker's in the hit-test. Neither leaves the list's band now.
   */
  it("keeps the sheet and its picker inside the list, clear of the composer", () => {
    const sheet = rect(LIST.bottom - 8 - 110, 8, 304, 110);
    const measure = vi.mocked(Element.prototype.getBoundingClientRect).getMockImplementation()!;
    vi.mocked(Element.prototype.getBoundingClientRect).mockImplementation(function (this: Element) {
      if (this.getAttribute("role") === "toolbar") return sheet;
      if (this.getAttribute("aria-label") === "Escolher reação") return rect(0, 0, 280, 300);
      return measure.call(this);
    });
    renderMenu({ pickerOpen: true });
    expect(menu().style.bottom).toBe(`calc(${740 - 587 + 8}px)`);
    const picker = document.querySelector<HTMLElement>(
      '[role="dialog"][aria-label="Escolher reação"]',
    )!;
    // Capped to the room between the header and the sheet, then placed there.
    expect(picker.style.getPropertyValue("--chat-emoji-picker-max-height")).toBe(
      `${sheet.top - 117 - 7 - 8}px`,
    );
    expect(picker.style.top).toBe(`${sheet.top - 300 - 7}px`);
    expect(picker).toHaveStyle({ visibility: "visible" });
  });

  /**
   * The surfaces as Chromium lays them out on a phone (CQR R6 of #1238): a
   * 62px menu, which "Mais reações" alone keeps as tall, and a picker whose
   * search field and one row of results take 120px. jsdom lays nothing out.
   */
  function measureSurfaces() {
    vi.spyOn(HTMLElement.prototype, "scrollHeight", "get").mockImplementation(function (
      this: HTMLElement,
    ) {
      return this.getAttribute("role") === "toolbar" ? 62 : 0;
    });
    const measure = vi.mocked(Element.prototype.getBoundingClientRect).getMockImplementation()!;
    vi.mocked(Element.prototype.getBoundingClientRect).mockImplementation(function (this: Element) {
      if (this.getAttribute("aria-label") === "Mais reações") return rect(0, 0, 44, 62);
      if (this.getAttribute("aria-label") === "Escolher reação") return rect(0, 0, 280, 120);
      return measure.call(this);
    });
  }

  /** A short screen: its height, and where its list ends above the composer. */
  function shortScreen(height: number, listBottom: number) {
    vi.spyOn(window, "innerHeight", "get").mockReturnValue(height);
    Object.assign(LIST, { top: 117, bottom: listBottom });
    bubbles["msg-1"] = rect(118, 61, 247, 20);
    measureSurfaces();
  }

  /**
   * CQR R6 of #1238, measured at 360×440 and 360×385: the list is 170 or 115px
   * (from 117), short of the 205px the menu, the gap and a picker showing one
   * row need, and the composer starts 8px under it. The band grows upwards
   * only — over the header, to the screen's top — and stops at the composer,
   * which keeps its touches: 287 or 232px are enough.
   */
  it.each([
    [440, 287],
    [385, 232],
  ])(
    "grows a short list's band over the header, never over the composer (360×%i)",
    (height, listBottom) => {
      shortScreen(height, listBottom);
      try {
        renderMenu({ pickerOpen: true });
        expect(menu()).toHaveClass(SHEET);
        expect(menu().style.bottom).toBe(`calc(${height - listBottom + 8}px)`);
        // Its top is the screen's: that safe area is cleared, the bottom's is not
        // (jsdom reorders the env() it cannot resolve, hence the parts).
        expect(menu().style.maxHeight).toContain(`calc(${listBottom - 16}px - env(`);
        expect(menu().style.maxHeight).toContain("safe-area-inset-top");
        expect(menu().style.maxHeight).not.toContain("safe-area-inset-bottom");
      } finally {
        Object.assign(LIST, { top: 117, bottom: 587 });
      }
    },
  );

  /**
   * A screen too short for that too (320×210 leaves the list 24px, 117–141,
   * under a composer at 125–250): above the composer is less than the 205px the
   * open picker needs, so the sheet keeps the band issue #1152 made usable
   * there — the composer under it (CQR R6). The menu alone needs 78px, which
   * the room above the composer holds.
   */
  it("keeps the visual viewport's height on a screen whose list cannot hold the surfaces", () => {
    shortScreen(210, 141);
    try {
      const { rerender } = renderMenu();
      expect(menu()).toHaveClass(SHEET);
      expect(menu().style.bottom).toBe(`calc(${210 - 141 + 8}px)`);
      rerender({ pickerOpen: true });
      expect(menu().style.bottom).toContain("8px");
      expect(menu().style.bottom).toContain("safe-area-inset-bottom");
      expect(menu().style.maxHeight).toContain(`${210 - 16}px`);
    } finally {
      Object.assign(LIST, { top: 117, bottom: 587 });
    }
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

  it("caps a touch-opened picker to the visible list, so it scrolls instead of closing", () => {
    // A keyboard leaves y 0–440 visible; the sheet sits at its bottom, 394–432.
    stubVisualViewport({ offsetLeft: 0, offsetTop: 0, width: 320, height: 440 });
    const measure = vi.mocked(Element.prototype.getBoundingClientRect).getMockImplementation()!;
    vi.mocked(Element.prototype.getBoundingClientRect).mockImplementation(function (this: Element) {
      if (this.getAttribute("role") === "toolbar") return rect(394, 8, 304, 38);
      return measure.call(this);
    });
    renderMenu({ pickerOpen: true });
    const picker = document.querySelector<HTMLElement>(
      '[role="dialog"][aria-label="Escolher reação"]',
    )!;
    // The larger side of its menu inside the band — from the list's top (117)
    // to the sheet — less the gap and the padding.
    expect(picker.style.getPropertyValue("--chat-emoji-picker-max-height")).toBe(
      `${394 - 117 - 7 - 8}px`,
    );
    expect(picker.style.getPropertyValue("--chat-emoji-picker-max-width")).toBe("304px");
  });

  it("places a touch-opened picker clear of the whole menu, lined up with its button", () => {
    // A sheet of two rows, its "Mais reações" button in the second one. The
    // menu is in the top layer: a picker placed against the button alone would
    // sit under the first row, where nothing in it can be touched.
    const sheet = rect(450, 8, 304, 110);
    const button = rect(510, 250, 44, 44);
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
    expect(picker.style.top).toBe(`${450 - 300 - 7}px`);
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
    // Quick reactions arriving after the picker opened add a row to the sheet,
    // which keeps its bottom above the composer (the list ends at 587).
    const geometry = { sheet: rect(517, 8, 304, 62) };
    stubPickerGeometry(geometry);
    renderMenu({ pickerOpen: true });
    expect(pickerElement().style.top).toBe(`${517 - 300 - 7}px`);
    expect(observedElements()).toContain(menu());

    geometry.sheet = rect(469, 8, 304, 110);
    act(() => flushResizeObservers());
    expect(pickerElement().style.top).toBe(`${469 - 300 - 7}px`);

    geometry.sheet = rect(529, 8, 304, 50);
    act(() => flushResizeObservers());
    expect(pickerElement().style.top).toBe(`${529 - 300 - 7}px`);
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
      sheet: rect(469, 8, 304, 110),
      results: rect(203, 18, 284, 85),
      cell: rect(229, 18, 36, 44),
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
    // The list has room above the cell (200 − 54 − 6 ≥ 117 + 8), the band
    // below the menu does not (156 + 8 > 140), so the palette goes under it.
    stubToneGeometry({
      sheet: rect(120, 8, 304, 36),
      picker: rect(163, 8, 304, 300),
      cell: rect(200, 18, 38, 44),
    });
    renderMenu({ pickerOpen: true });
    const palette = await openTonePalette();
    expect(palette).toHaveStyle({ top: `${200 + 44 + 6}px` });
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
    openMoreActions();
    fireEvent.click(screen.getByRole("button", { name: "Responder" }));
    expect(props.onReplyMessage).toHaveBeenCalledTimes(1);
    expect(props.onReplyMessage).toHaveBeenCalledWith(props.message);
    expect(props.onReactionMenuVisibleChange).toHaveBeenCalledWith("msg-1", false);
  });

  /**
   * The completion contract (CQR R4 of issue #1238): a quick reaction or an
   * emoji confirmed in the picker reacts once, ends menu and picker, and hands
   * focus back to what opened the menu — never leaving it on the body.
   */
  it("reacts once with a quick reaction, closes, and hands focus back to the message", () => {
    const { props } = renderMenu();
    const quick = screen.getByRole("button", { name: "Reagir rapidamente com 👍" });
    expect(quick).toHaveFocus();
    fireEvent.click(quick);
    expect(props.onToggleReaction).toHaveBeenCalledTimes(1);
    expect(props.onToggleReaction).toHaveBeenCalledWith("msg-1", "👍");
    expect(props.onReactionMenuVisibleChange).toHaveBeenCalledWith("msg-1", false);
    expect(shellOf("msg-1")).toHaveFocus();
  });

  it("hands focus back to the header's actions button that opened it, while it is there", () => {
    const opener = document.createElement("button");
    opener.setAttribute("aria-controls", "chat-msg-menu-msg-1");
    document.body.append(opener);
    opener.focus();
    try {
      const { unmount } = renderMenu();
      fireEvent.click(screen.getByRole("button", { name: "Reagir rapidamente com 👍" }));
      expect(opener).toHaveFocus();
      unmount();

      // Gone before the menu closes (the selection mode left): the message.
      opener.focus();
      renderMenu();
      opener.remove();
      fireEvent.click(screen.getByRole("button", { name: "Reagir rapidamente com 👍" }));
      expect(shellOf("msg-1")).toHaveFocus();
    } finally {
      opener.remove();
    }
  });

  it("never hands focus to a button controlling another message's menu", () => {
    const foreign = document.createElement("button");
    foreign.setAttribute("aria-controls", "chat-msg-menu-msg-2");
    document.body.append(foreign);
    foreign.focus();
    try {
      renderMenu();
      fireEvent.click(screen.getByRole("button", { name: "Reagir rapidamente com 👍" }));
      expect(shellOf("msg-1")).toHaveFocus();
    } finally {
      foreign.remove();
    }
  });

  it("leaves focus an action moved elsewhere where it went", () => {
    const { props } = renderMenu();
    vi.mocked(props.onReplyMessage).mockImplementation(() =>
      screen.getByRole("button", { name: "fora" }).focus(),
    );
    openMoreActions();
    fireEvent.click(screen.getByRole("button", { name: "Responder" }));
    expect(props.onReactionMenuVisibleChange).toHaveBeenCalledWith("msg-1", false);
    expect(screen.getByRole("button", { name: "fora" })).toHaveFocus();
  });

  it("reacts once with an emoji confirmed in the picker, and ends the menu with it", async () => {
    const { props } = renderMenu({ pickerOpen: true });
    fireEvent.change(await screen.findByRole("searchbox", { name: "Buscar emoji" }), {
      target: { value: "foguete" },
    });
    const rocket = await screen.findByRole("button", { name: "foguete" });
    // What opening the menu and the picker said is not what confirming says.
    vi.mocked(props.onReactionMenuVisibleChange).mockClear();
    fireEvent.click(rocket);
    expect(props.onToggleReaction).toHaveBeenCalledTimes(1);
    expect(props.onToggleReaction).toHaveBeenCalledWith("msg-1", "🚀");
    expect(props.onPickerOpenChange).toHaveBeenCalledWith("msg-1", false);
    expect(props.onReactionMenuVisibleChange).toHaveBeenCalledWith("msg-1", false);
    expect(props.onReactionMenuVisibleChange).not.toHaveBeenCalledWith("msg-1", true);
    expect(shellOf("msg-1")).toHaveFocus();
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
    openMoreActions();
    fireEvent.click(screen.getByRole("button", { name: "Excluir mensagem" }));
    expect(confirm).toHaveBeenCalledWith("Excluir esta mensagem?");
    expect(props.onDeleteMessage).not.toHaveBeenCalled();
  });

  it("offers only the actions the current capabilities allow", () => {
    const { rerender } = renderMenu({ isMine: true });
    openMoreActions();
    expect(screen.getByRole("button", { name: "Editar mensagem" })).toBeInTheDocument();
    rerender({ editDisabled: true });
    expect(screen.queryByRole("button", { name: "Editar mensagem" })).not.toBeInTheDocument();
    rerender({ onTogglePin: undefined, onForwardMessage: undefined });
    expect(screen.queryByRole("button", { name: /Fixar/ })).not.toBeInTheDocument();
  });

  it("leads with the reactions and keeps the other actions behind Mais ações (issue #1238)", () => {
    const { props } = renderMenu();
    const labels = [...menu().querySelectorAll(":scope > button")].map((button) =>
      button.getAttribute("aria-label"),
    );
    expect(labels).toEqual(["Reagir rapidamente com 👍", "Mais reações", "Mais ações"]);
    expect(screen.queryByRole("button", { name: "Responder" })).not.toBeInTheDocument();

    const more = screen.getByRole("button", { name: "Mais ações" });
    expect(more).toHaveAttribute("aria-expanded", "false");
    expect(more).not.toHaveAttribute("aria-controls");
    fireEvent.click(more);
    // Opening the rest of the actions is not choosing one: the menu stays.
    expect(props.onReactionMenuVisibleChange).not.toHaveBeenCalledWith("msg-1", false);
    const group = screen.getByRole("group", { name: "Outras ações" });
    expect(more).toHaveAttribute("aria-expanded", "true");
    expect(more).toHaveAttribute("aria-controls", group.id);
    for (const name of ["Responder", "Citar em outra conversa", "Favoritar mensagem"]) {
      expect(screen.getByRole("button", { name })).toBeInTheDocument();
    }

    fireEvent.click(more);
    expect(screen.queryByRole("group", { name: "Outras ações" })).not.toBeInTheDocument();
    expect(more).toHaveAttribute("aria-expanded", "false");
  });

  it("starts every opening with the other actions collapsed", () => {
    const { rerender } = renderMenu();
    openMoreActions();
    rerender({ reactionMenuVisible: false, reactionMenuTouch: false });
    rerender({ reactionMenuVisible: true, reactionMenuTouch: true });
    expect(screen.getByRole("button", { name: "Mais ações" })).toHaveAttribute(
      "aria-expanded",
      "false",
    );
    expect(screen.queryByRole("button", { name: "Responder" })).not.toBeInTheDocument();
  });

  it("keeps the hover toolbar's actions and order, with no Mais ações (#1153)", () => {
    bubbles["msg-0"] = rect(150, 261, 47, 41);
    renderMenu({ reactionMenuTouch: false });
    const labels = [...menu().querySelectorAll(":scope > button")].map((button) =>
      button.getAttribute("aria-label"),
    );
    expect(labels).toEqual([
      "Reagir rapidamente com 👍",
      "Responder",
      "Citar em outra conversa",
      "Favoritar mensagem",
      "Mais reações",
    ]);
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

/**
 * CQR R8 of issue #1238, measured in WebKit at 360×350: shrinking the viewport
 * lays the list out shorter (117–197) a frame before the timeline puts its
 * scroll back, and for that frame the selected bubble read 306–347. The menu
 * closed on that reading — picker, search and focus with it — and the bubble
 * was back at 138–179 the next frame. A reading out of sight is now taken again
 * two frames later; only one still out closes the menu.
 */
describe("a touch menu whose message reads out of sight (CQR R8 of issue #1238)", () => {
  let frames: Map<number, FrameRequestCallback>;
  let nextFrame: number;
  beforeEach(() => {
    frames = new Map();
    nextFrame = 0;
    vi.stubGlobal("requestAnimationFrame", (callback: FrameRequestCallback) => {
      frames.set(++nextFrame, callback);
      return nextFrame;
    });
    vi.stubGlobal("cancelAnimationFrame", (id: number) => frames.delete(id));
  });

  /** One frame: the callbacks requested before it began. */
  const frame = () =>
    act(() => {
      const due = [...frames.values()];
      frames.clear();
      due.forEach((callback) => callback(0));
    });
  const scroll = () => act(() => void document.dispatchEvent(new Event("scroll")));
  const leaveList = () => (bubbles["msg-1"] = rect(600, 61, 247, 41));
  const focusableList = () => {
    const list = document.querySelector<HTMLElement>(".chat-msg-area__list")!;
    list.tabIndex = -1;
    return list;
  };
  const closedAny = (props: MessageBubbleProps) =>
    vi.mocked(props.onReactionMenuVisibleChange).mock.calls.some(([, visible]) => !visible) ||
    vi.mocked(props.onPickerOpenChange).mock.calls.some(([, open]) => !open);

  async function openWithSearch() {
    const view = renderMenu({ pickerOpen: true });
    const search = await screen.findByRole("searchbox", { name: "Buscar emoji" });
    search.focus();
    vi.mocked(view.props.onReactionMenuVisibleChange).mockClear();
    vi.mocked(view.props.onPickerOpenChange).mockClear();
    return { ...view, search };
  }

  it("keeps menu, picker, search and focus when the bubble is back in sight", async () => {
    const { props, search } = await openWithSearch();
    leaveList();
    scroll();
    expect(closedAny(props)).toBe(false);
    // The timeline puts its scroll back the next frame.
    resetGeometry();
    frame();
    frame();
    expect(closedAny(props)).toBe(false);
    expect(search).toHaveFocus();
    expect(menu()).toHaveClass(SHEET);
    expect(menu()).toHaveStyle({ visibility: "visible" });
    expect(frames.size).toBe(0);
  });

  it("closes menu and picker once the bubble stays out, focus to the list", async () => {
    const { props } = await openWithSearch();
    const list = focusableList();
    leaveList();
    scroll();
    frame();
    expect(closedAny(props)).toBe(false);
    frame();
    expect(props.onPickerOpenChange).toHaveBeenCalledWith("msg-1", false);
    expect(props.onReactionMenuVisibleChange).toHaveBeenCalledWith("msg-1", false);
    expect(list).toHaveFocus();
  });

  it("keeps a sheet in the band of now while the reading is pending", () => {
    // Left where the old band put it, the sheet was laid out again inside the
    // picker's observer once the bubble was back: a ResizeObserver loop in WebKit.
    const { props } = renderMenu();
    expect(menu().style.bottom).toBe(`calc(${740 - 587 + 8}px)`);
    leaveList();
    vi.spyOn(window, "innerHeight", "get").mockReturnValue(640);
    Object.assign(LIST, { bottom: 487 });
    try {
      scroll();
      expect(menu().style.bottom).toBe(`calc(${640 - 487 + 8}px)`);
      expect(menu()).toHaveStyle({ visibility: "visible" });
      expect(closedAny(props)).toBe(false);
    } finally {
      Object.assign(LIST, { bottom: 587 });
    }
  });

  it("closes, sheet or not, once its message has left the list's band", () => {
    const { props } = renderMenu();
    expect(menu()).toHaveClass(SHEET);
    bubbles["msg-1"] = rect(-400, 61, 247, 167);
    scroll();
    frame();
    frame();
    expect(props.onReactionMenuVisibleChange).toHaveBeenCalledWith("msg-1", false);
  });

  it("takes focus back from the header's selection controls, which go with the selection", () => {
    const header = document.createElement("header");
    header.className = "chat-msg-area__header--selection";
    const actions = document.createElement("button");
    header.append(actions);
    document.body.append(header);
    try {
      renderMenu();
      const list = focusableList();
      actions.focus();
      leaveList();
      scroll();
      frame();
      frame();
      expect(list).toHaveFocus();
    } finally {
      header.remove();
    }
  });

  it("leaves focus the reader moved to another control where it is", () => {
    const { props } = renderMenu();
    focusableList();
    const elsewhere = screen.getByRole("button", { name: "fora" });
    elsewhere.focus();
    leaveList();
    scroll();
    frame();
    frame();
    expect(props.onReactionMenuVisibleChange).toHaveBeenCalledWith("msg-1", false);
    expect(elsewhere).toHaveFocus();
  });

  it("checks once, from the first reading: later readings do not postpone it", () => {
    const { props } = renderMenu();
    leaveList();
    scroll();
    frame();
    scroll();
    frame();
    expect(props.onReactionMenuVisibleChange).toHaveBeenCalledWith("msg-1", false);
    expect(frames.size).toBe(0);
  });

  it("drops the pending check when the menu closes or the toolbar unmounts", () => {
    const { props, rerender, unmount } = renderMenu();
    leaveList();
    scroll();
    rerender({ reactionMenuVisible: false, reactionMenuTouch: false });
    expect(frames.size).toBe(0);

    rerender({ reactionMenuVisible: true, reactionMenuTouch: true });
    scroll();
    expect(frames.size).toBe(1);
    unmount();
    expect(frames.size).toBe(0);
    expect(props.onReactionMenuVisibleChange).not.toHaveBeenCalledWith("msg-1", false);
  });

  it("keeps a hover menu's verdict immediate (#839)", () => {
    bubbles["msg-0"] = rect(150, 261, 47, 41);
    const { props } = renderMenu({ reactionMenuTouch: false });
    leaveList();
    scroll();
    expect(props.onReactionMenuVisibleChange).toHaveBeenCalledWith("msg-1", false);
  });
});

/**
 * CQR R10 F-1 of issue #1238, measured rotating 390×740 → 740×390 with the
 * picker open: for a frame the list is laid out with the selected message below
 * it (424–465 under a list ending at 273). The menu, an anchored one, stays
 * where it was (523–575) while that reading is confirmed — and the picker,
 * placed clear of the menu, judged that box out of its band and closed at once:
 * focus to the body, the selection and the menu kept. The picker's verdict now
 * waits for the menu's.
 */
describe("a touch picker over a menu whose message reads out of sight (CQR R10 F-1)", () => {
  let frames: Map<number, FrameRequestCallback>;
  let nextFrame: number;
  // Where the anchored menu was left while its reading is confirmed.
  let staleMenu: DOMRect | null;
  beforeEach(() => {
    frames = new Map();
    nextFrame = 0;
    staleMenu = null;
    vi.stubGlobal("requestAnimationFrame", (callback: FrameRequestCallback) => {
      frames.set(++nextFrame, callback);
      return nextFrame;
    });
    vi.stubGlobal("cancelAnimationFrame", (id: number) => frames.delete(id));
    // A wide touch screen, where the menu keeps by its message with the picker
    // open (CQR R4) — the anchored menu the rotation starts from.
    vi.spyOn(window, "innerWidth", "get").mockReturnValue(1280);
    vi.spyOn(window, "innerHeight", "get").mockReturnValue(800);
    Object.assign(LIST, { top: 117, bottom: 730, left: 240, right: 1280 });
    bubbles["msg-0"] = rect(150, 300, 247, 41);
    bubbles["msg-1"] = rect(337, 300, 247, 40);
    bubbles["msg-2"] = rect(600, 300, 247, 41);
    const measured = vi.mocked(Element.prototype.getBoundingClientRect).getMockImplementation()!;
    vi.mocked(Element.prototype.getBoundingClientRect).mockImplementation(function (this: Element) {
      if (staleMenu && this.getAttribute("role") === "toolbar") return staleMenu;
      return measured.call(this);
    });
    // jsdom focuses what CSS hides; a browser does not.
    const focus = HTMLElement.prototype.focus;
    vi.spyOn(HTMLElement.prototype, "focus").mockImplementation(function (
      this: HTMLElement,
      options?: FocusOptions,
    ) {
      if (this.closest<HTMLElement>('[role="toolbar"]')?.style.visibility === "hidden") return;
      focus.call(this, options);
    });
  });
  afterEach(() => Object.assign(LIST, { top: 117, bottom: 587, left: 0, right: 320 }));

  const frame = () =>
    act(() => {
      const due = [...frames.values()];
      frames.clear();
      due.forEach((callback) => callback(0));
    });
  const scroll = () => act(() => void document.dispatchEvent(new Event("scroll")));
  /** The rotation's first frame: the message below the list, the menu left behind. */
  const rotate = () => {
    bubbles["msg-1"] = rect(900, 300, 247, 40);
    staleMenu = rect(950, 300, 246, 36);
    scroll();
  };
  /** The timeline puts its scroll back: the message, and the menu with it, in sight. */
  const settle = () => {
    bubbles["msg-1"] = rect(337, 300, 247, 40);
    staleMenu = null;
  };
  const closedAny = (props: MessageBubbleProps) =>
    vi.mocked(props.onReactionMenuVisibleChange).mock.calls.some(([, visible]) => !visible) ||
    vi.mocked(props.onPickerOpenChange).mock.calls.some(([, open]) => !open);

  async function openAnchoredPicker() {
    const view = renderMenu({ pickerOpen: true });
    const search = await screen.findByRole("searchbox", { name: "Buscar emoji" });
    expect(menu()).not.toHaveClass(SHEET);
    search.focus();
    vi.mocked(view.props.onReactionMenuVisibleChange).mockClear();
    vi.mocked(view.props.onPickerOpenChange).mockClear();
    return { ...view, search };
  }

  it("closes nothing while the reading is pending", async () => {
    const { props, search } = await openAnchoredPicker();
    rotate();
    expect(frames.size).toBe(1);
    expect(closedAny(props)).toBe(false);
    expect(search).toHaveFocus();
  });

  it("keeps menu, picker, search and focus once the message is back, and places both", async () => {
    const { props, search } = await openAnchoredPicker();
    rotate();
    settle();
    const placed = vi.fn();
    document.addEventListener("chat-emoji-picker-placed", placed);
    try {
      frame();
      frame();
    } finally {
      document.removeEventListener("chat-emoji-picker-placed", placed);
    }
    expect(closedAny(props)).toBe(false);
    expect(search).toHaveFocus();
    expect(menu()).toHaveStyle({ visibility: "visible" });
    // The picker placed again against the menu, by the confirmation.
    expect(placed).toHaveBeenCalled();
    expect(frames.size).toBe(0);
  });

  it("closes picker and menu together once the message stays out, focus to the list", async () => {
    const { props } = await openAnchoredPicker();
    const list = document.querySelector<HTMLElement>(".chat-msg-area__list")!;
    list.tabIndex = -1;
    rotate();
    frame();
    expect(closedAny(props)).toBe(false);
    frame();
    expect(props.onPickerOpenChange).toHaveBeenCalledWith("msg-1", false);
    expect(props.onReactionMenuVisibleChange).toHaveBeenCalledWith("msg-1", false);
    expect(list).toHaveFocus();
  });

  it("still closes the picker at once on Escape while the reading is pending", async () => {
    const { props } = await openAnchoredPicker();
    rotate();
    fireEvent.keyDown(document, { key: "Escape" });
    // At once, not after the confirmation; the picker only, as ever.
    expect(frames.size).toBe(1);
    expect(props.onPickerOpenChange).toHaveBeenCalledWith("msg-1", false);
    expect(props.onReactionMenuVisibleChange).not.toHaveBeenCalledWith("msg-1", false);
    // The menu hidden meanwhile cannot take focus: the message holds it, never
    // the body, and hands it on to Mais reações once the message is back.
    expect(shellOf("msg-1")).toHaveFocus();
    settle();
    frame();
    frame();
    expect(screen.getByRole("button", { name: "Mais reações" })).toHaveFocus();
  });

  // WebKit dropped focus a hidden menu held to the body, and the row took that
  // for focus leaving: the menu and the selection closed, the message in view.
  it("parks focus the hidden menu held on the message, and hands it back once placed", () => {
    renderMenu();
    expect(menu()).not.toHaveClass(SHEET);
    const quick = menu().querySelector("button")!;
    expect(quick).toHaveFocus();
    rotate();
    expect(shellOf("msg-1")).toHaveFocus();
    settle();
    frame();
    frame();
    expect(quick).toHaveFocus();
  });

  it("leaves focus the reader moved off the parked message where it is", () => {
    renderMenu();
    rotate();
    const elsewhere = screen.getByRole("button", { name: "fora" });
    elsewhere.focus();
    settle();
    frame();
    frame();
    expect(elsewhere).toHaveFocus();
  });

  it("hands focus Escape left on the message to the list once the message stays out", async () => {
    await openAnchoredPicker();
    const list = document.querySelector<HTMLElement>(".chat-msg-area__list")!;
    list.tabIndex = -1;
    rotate();
    fireEvent.keyDown(document, { key: "Escape" });
    expect(shellOf("msg-1")).toHaveFocus();
    frame();
    frame();
    expect(list).toHaveFocus();
  });

  it("still closes the picker at once on a press outside while the reading is pending", async () => {
    const { props } = await openAnchoredPicker();
    rotate();
    fireEvent.mouseDown(screen.getByRole("button", { name: "fora" }));
    expect(props.onPickerOpenChange).toHaveBeenCalledWith("msg-1", false);
  });

  it("drops the pending reading on unmount — the message removed — and reopens nothing", async () => {
    const { props, unmount } = await openAnchoredPicker();
    rotate();
    unmount();
    expect(frames.size).toBe(0);
    expect(closedAny(props)).toBe(false);
    expect(props.onReactionMenuVisibleChange).not.toHaveBeenCalledWith("msg-1", true);
    expect(props.onPickerOpenChange).not.toHaveBeenCalledWith("msg-1", true);
  });

  it("leaves focus the reader moved to another control where it is", async () => {
    const { props } = await openAnchoredPicker();
    const elsewhere = screen.getByRole("button", { name: "fora" });
    rotate();
    elsewhere.focus();
    frame();
    frame();
    expect(props.onReactionMenuVisibleChange).toHaveBeenCalledWith("msg-1", false);
    expect(elsewhere).toHaveFocus();
  });

  it("hands focus to Mais reações when the picker is lost with nothing pending", async () => {
    const { props } = await openAnchoredPicker();
    // The message in sight, the menu's box out of the picker's band: no reading
    // to confirm, the picker's own verdict stands.
    staleMenu = rect(950, 300, 246, 36);
    scroll();
    expect(frames.size).toBe(0);
    expect(props.onPickerOpenChange).toHaveBeenCalledWith("msg-1", false);
    expect(screen.getByRole("button", { name: "Mais reações" })).toHaveFocus();
  });
});

/**
 * CQR R10 F-1 of issue #1238, measured in WebKit rotating 740×390 → 390×740:
 * the mouse WebKit synthesises after the resize left the row that moved, the
 * hover close dropped the message's touch state with the picker open, and the
 * toolbar — no longer a touch one — closed everything on the desktop verdict.
 */
describe("a mouse leaving the row of a touch menu (CQR R10 F-1)", () => {
  it("does not hide the menu its message opened by touch", () => {
    const { props } = renderMenu({ pickerOpen: true });
    fireEvent.mouseLeave(shellOf("msg-1"), { relatedTarget: document.body });
    expect(props.onReactionMenuVisibleChange).not.toHaveBeenCalledWith("msg-1", false);
  });

  it("still hides a hover menu when the mouse leaves its row (#1153)", () => {
    bubbles["msg-0"] = rect(150, 261, 47, 41);
    const { props } = renderMenu({ reactionMenuTouch: false });
    fireEvent.mouseLeave(shellOf("msg-1"), { relatedTarget: document.body });
    expect(props.onReactionMenuVisibleChange).toHaveBeenCalledWith("msg-1", false);
  });
});

/**
 * Without the Popover API there is no top layer, and a fixed menu inside a
 * virtualized row is placed against the row's transform and clipped by the
 * list (CQR R3 of #1238): a touch menu goes to the body instead, still the
 * message's. jsdom has no Popover API, so this is the path the rest of this
 * file runs too; the last case loads the module with one.
 */
describe("a touch menu without the top layer (CQR R3 of issue #1238)", () => {
  it("goes to the body, marked with its message and the conversation's palette", () => {
    renderMenu({ reactionMenuVisible: true, reactionMenuTouch: true });
    expect(menu().parentElement).toBe(document.body);
    expect(shellOf("msg-1")).not.toContainElement(menu());
    expect(menu()).toHaveAttribute("data-reaction-picker-for", "msg-1");
    expect(menu()).toHaveClass("chat-theme");
    expect(menu()).not.toHaveAttribute("popover");
  });

  it("keeps a hover menu in its message, as before (#839)", () => {
    renderMenu({ reactionMenuVisible: true, reactionMenuTouch: false });
    // Placed by hover rules, which find no room here: hidden, not removed.
    const hover = document.getElementById("chat-msg-menu-msg-1")!;
    expect(shellOf("msg-1")).toContainElement(hover);
    expect(hover).not.toHaveAttribute("data-reaction-picker-for");
    expect(hover).not.toHaveClass("chat-theme");
  });

  it("closes on a press outside it, never on one inside it", () => {
    const { props } = renderMenu({ reactionMenuVisible: true, reactionMenuTouch: true });
    fireEvent.pointerDown(menu());
    expect(props.onReactionMenuVisibleChange).not.toHaveBeenCalledWith("msg-1", false);
    fireEvent.pointerDown(screen.getByRole("button", { name: "fora" }));
    expect(props.onReactionMenuVisibleChange).toHaveBeenCalledWith("msg-1", false);
  });

  /**
   * CQR R5 of #1238, in Chromium and WebKit: a tap on the header's "Mensagem
   * selecionada" — outside the menu, taking no focus — closed the menu and left
   * focus on the body, the selection still on. Focus is settled once the press
   * has played out, by the same rules as the menu's other ways to close.
   */
  describe("focus after a press outside it (CQR R5)", () => {
    let controller: HTMLButtonElement;
    let title: HTMLSpanElement;
    beforeEach(() => {
      controller = document.createElement("button");
      controller.setAttribute("aria-controls", "chat-msg-menu-msg-1");
      title = document.createElement("span");
      title.textContent = "Mensagem selecionada";
      document.body.append(controller, title);
    });
    afterEach(() => {
      controller.remove();
      title.remove();
    });

    /** Pressed where nothing takes focus: the browser blurs to the body, then clicks. */
    const pressNowhere = (target: Element, between?: () => void) => {
      fireEvent.pointerDown(target);
      (document.activeElement as HTMLElement | null)?.blur();
      between?.();
      fireEvent.click(target);
    };

    it("hands focus to the selection's button controlling the menu, reacting to nothing", () => {
      const { props } = renderMenu();
      expect(menu()).toContainElement(document.activeElement as HTMLElement);
      pressNowhere(title);
      expect(props.onReactionMenuVisibleChange).toHaveBeenCalledWith("msg-1", false);
      expect(controller).toHaveFocus();
      expect(props.onToggleReaction).not.toHaveBeenCalled();
    });

    it("leaves focus a focusable target took — the composer — where it is", () => {
      renderMenu();
      const composer = screen.getByRole("button", { name: "fora" });
      fireEvent.pointerDown(composer);
      composer.focus();
      fireEvent.click(composer);
      expect(composer).toHaveFocus();
    });

    it("leaves focus the pressed control handed to the message where it is", () => {
      renderMenu();
      fireEvent.pointerDown(title);
      shellOf("msg-1").focus();
      fireEvent.click(title);
      expect(shellOf("msg-1")).toHaveFocus();
    });

    it("falls back to the conversation's list once the message went during the close", () => {
      renderMenu();
      const list = document.querySelector<HTMLElement>(".chat-msg-area__list")!;
      list.tabIndex = -1;
      // Removed: the selection, and the header's button with it, are gone.
      pressNowhere(title, () => controller.remove());
      expect(list).toHaveFocus();
    });

    it("focuses nothing of a conversation that went during the close", () => {
      const { unmount } = renderMenu();
      fireEvent.pointerDown(title);
      unmount();
      controller.remove();
      fireEvent.click(title);
      expect(document.activeElement).toBe(document.body);
    });

    it("settles on a cancelled press too, once", () => {
      renderMenu();
      fireEvent.pointerDown(title);
      // Still in the closing menu: no click is coming to move it.
      fireEvent.pointerCancel(title);
      expect(controller).toHaveFocus();
      controller.blur();
      fireEvent.click(title);
      expect(document.activeElement).toBe(document.body);
    });
  });

  it("stays in its message, a manual popover, where the Popover API exists", async () => {
    Object.defineProperty(HTMLElement.prototype, "togglePopover", {
      configurable: true,
      value: vi.fn(),
    });
    vi.resetModules();
    try {
      const { default: FreshBubble } = await import("./MessageBubble");
      render(
        <div className="chat-msg-area__list">
          <FreshBubble {...propsWith({ reactionMenuVisible: true, reactionMenuTouch: true })} />
        </div>,
      );
      // jsdom hides a popover it was never told is open: found by its id.
      const toolbar = document.getElementById("chat-msg-menu-msg-1")!;
      expect(shellOf("msg-1")).toContainElement(toolbar);
      expect(toolbar).toHaveAttribute("popover", "manual");
      expect(toolbar).not.toHaveAttribute("data-reaction-picker-for");
    } finally {
      delete (HTMLElement.prototype as { togglePopover?: unknown }).togglePopover;
      vi.resetModules();
    }
  });
});
