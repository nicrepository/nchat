/**
 * The touch menu's lifecycle with the real controller (issue #1152): the
 * shared state of useReactionMenu wired to real MessageBubbles, the way the
 * timeline wires them — no mocked callbacks between the two. Covers what only
 * shows up when both halves run together: Escape and the focus it hands back,
 * a message removed, unmounted or recycled while it owns the menu, a stale
 * release racing a newer menu, a conversation switch, the actions button, and
 * the real meeting-scheduling modal opening over a gesture still in progress.
 */

import { act, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { StrictMode } from "react";
import { MemoryRouter } from "react-router";
import { afterAll, afterEach, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";

import { useMeetingScheduling } from "../meetings/meetingContext";
import MeetingSchedulingProvider from "../meetings/MeetingSchedulingProvider";
import { emptyEmojiUsage } from "./emoji/emojiUsage";
import MessageBubble from "./MessageBubble";
import type { Channel, Message } from "./chatTypes";
import { useReactionMenu } from "./message-area/timeline/useReactionMenu";
import { LONG_PRESS_MS, SWIPE_REPLY_PX } from "./useMessageTouchGestures";

function messageWith(id: string, overrides: Partial<Message> = {}): Message {
  return {
    id,
    senderId: "user-1",
    senderDisplayName: "Alex Souza",
    senderEmail: "alex@example.test",
    kind: "user",
    bodyText: `texto ${id}`,
    bodyFormat: "v1",
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

const onReplyMessage = vi.fn();
const onToggleReaction = vi.fn();

/** The timeline's wiring, minus the virtualizer: one controller, many rows. */
function Conversation({
  messages,
  keyByIndex = false,
}: {
  messages: Message[];
  keyByIndex?: boolean;
}) {
  const menu = useReactionMenu();
  return (
    <div>
      {messages.map((message, index) => (
        <MessageBubble
          key={keyByIndex ? index : message.id}
          message={message}
          isMine={false}
          onToggleReaction={onToggleReaction}
          onReplyMessage={onReplyMessage}
          onReferenceMessage={vi.fn()}
          onToggleFavorite={vi.fn()}
          onEditMessage={vi.fn()}
          onEditForbidden={vi.fn()}
          onDeleteMessage={vi.fn()}
          recentReactionEmojis={["👍"]}
          emojiUsage={emptyEmojiUsage}
          onEmojiToneChange={vi.fn()}
          currentUserId="me"
          reactionMenuVisible={menu.hoveredMessageId === message.id}
          onReactionMenuVisibleChange={menu.onReactionMenuVisibleChange}
          pickerOpen={menu.openPickerMessageId === message.id}
          onPickerOpenChange={menu.onPickerOpenChange}
          reactionMenuTouch={menu.touchMessageId === message.id}
          onReactionMenuLongPress={menu.onReactionMenuLongPress}
          gesturesBlocked={menu.touchSurfaceOpen}
          onReactionMenuRelease={menu.onReactionMenuRelease}
        />
      ))}
      <button type="button">fora</button>
    </div>
  );
}

const touch = { pointerType: "touch", pointerId: 1, isPrimary: true };
const body = (id: string) => screen.getByText(`texto ${id}`);
const shell = (id: string) =>
  document.querySelector<HTMLElement>(`[data-message-id="${id}"]`) as HTMLElement;
const menu = () => screen.queryByRole("toolbar", { name: "Reagir à mensagem" });

function longPress(id: string) {
  fireEvent.pointerDown(body(id), { ...touch, clientX: 100, clientY: 200 });
  act(() => vi.advanceTimersByTime(LONG_PRESS_MS));
  fireEvent.pointerUp(body(id), { ...touch, clientX: 100, clientY: 200 });
}

function swipe(id: string) {
  fireEvent.pointerDown(body(id), { ...touch, clientX: 100, clientY: 200 });
  for (const dx of [20, 50, SWIPE_REPLY_PX + 20]) {
    fireEvent.pointerMove(body(id), { ...touch, clientX: 100 + dx, clientY: 202 });
  }
  fireEvent.pointerUp(body(id), { ...touch, clientX: 100 + SWIPE_REPLY_PX + 20, clientY: 202 });
}

/** Past the hover menu's leave delay. */
const settle = () => act(() => vi.advanceTimersByTime(200));

beforeEach(() => {
  vi.useFakeTimers();
  onReplyMessage.mockClear();
  onToggleReaction.mockClear();
  // Room beside every bubble, so the menu anchors and nothing closes it.
  vi.spyOn(Element.prototype, "getBoundingClientRect").mockImplementation(function (this: Element) {
    const box = this.classList.contains("chat-msg-area__msg-bubble")
      ? { top: 100, left: 20, width: 200, height: 40 }
      : this.getAttribute("role") === "toolbar"
        ? { top: 0, left: 0, width: 246, height: 44 }
        : this.tagName === "BUTTON"
          ? { top: 100, left: 230, width: 44, height: 44 }
          : { top: 0, left: 0, width: 0, height: 0 };
    return {
      ...box,
      x: box.left,
      y: box.top,
      right: box.left + box.width,
      bottom: box.top + box.height,
      toJSON: () => ({}),
    } as DOMRect;
  });
});

afterEach(() => {
  vi.restoreAllMocks();
  vi.useRealTimers();
});

describe("Escape and focus", () => {
  it("keeps a long-pressed menu closed after Escape, past the touch-echo window", () => {
    render(<Conversation messages={[messageWith("msg-1")]} />);
    longPress("msg-1");
    expect(menu()).toBeInTheDocument();
    // Well past the window that hides a tap's compatibility focus.
    act(() => vi.advanceTimersByTime(2000));

    fireEvent.keyDown(document, { key: "Escape" });
    settle();
    expect(menu()).not.toBeInTheDocument();
    expect(shell("msg-1")).toHaveFocus();
    act(() => vi.advanceTimersByTime(2000));
    expect(menu()).not.toBeInTheDocument();
  });

  it("still opens for a keyboard arriving at the message from outside", () => {
    render(<Conversation messages={[messageWith("msg-1")]} />);
    longPress("msg-1");
    act(() => vi.advanceTimersByTime(2000));
    fireEvent.keyDown(document, { key: "Escape" });
    settle();

    act(() => screen.getByRole("button", { name: "fora" }).focus());
    act(() => shell("msg-1").focus());
    expect(menu()).toBeInTheDocument();
  });
});

describe("a message leaving while it owns the menu", () => {
  it("closes and frees swipes when the message is removed", () => {
    const { rerender } = render(
      <Conversation messages={[messageWith("msg-1"), messageWith("msg-2")]} />,
    );
    longPress("msg-1");
    expect(menu()).toBeInTheDocument();

    rerender(
      <Conversation messages={[messageWith("msg-1", { isRemoved: true }), messageWith("msg-2")]} />,
    );
    expect(menu()).not.toBeInTheDocument();
    swipe("msg-2");
    expect(onReplyMessage).toHaveBeenCalledTimes(1);
    expect(onReplyMessage).toHaveBeenCalledWith(expect.objectContaining({ id: "msg-2" }));
  });

  it("closes when its row unmounts, and does not reopen when the message comes back", () => {
    const one = messageWith("msg-1");
    const two = messageWith("msg-2");
    const { rerender } = render(<Conversation messages={[one, two]} />);
    longPress("msg-1");

    rerender(<Conversation messages={[two]} />);
    swipe("msg-2");
    expect(onReplyMessage).toHaveBeenCalledTimes(1);

    rerender(<Conversation messages={[one, two]} />);
    expect(menu()).not.toBeInTheDocument();
  });

  it("releases the message a recycled row showed, not the one it shows now", () => {
    const { rerender } = render(
      <Conversation keyByIndex messages={[messageWith("msg-1"), messageWith("msg-2")]} />,
    );
    longPress("msg-1");
    rerender(<Conversation keyByIndex messages={[messageWith("msg-3"), messageWith("msg-2")]} />);
    expect(menu()).not.toBeInTheDocument();
    swipe("msg-2");
    expect(onReplyMessage).toHaveBeenCalledTimes(1);
  });

  it("never lets a stale release close a newer menu on another message", () => {
    const one = messageWith("msg-1");
    const two = messageWith("msg-2");
    const { rerender } = render(<Conversation messages={[one, two]} />);
    longPress("msg-1");
    longPress("msg-2");
    fireEvent.click(screen.getByRole("button", { name: "Mais reações" }));

    rerender(<Conversation messages={[two]} />);
    expect(menu()).toBeInTheDocument();
    expect(shell("msg-2")).toContainElement(menu());
    expect(screen.getByRole("button", { name: "Mais reações" })).toHaveAttribute(
      "aria-expanded",
      "true",
    );
  });

  it("starts a switched-to conversation with nothing open", () => {
    const { rerender } = render(<Conversation key="a" messages={[messageWith("msg-1")]} />);
    longPress("msg-1");
    rerender(<Conversation key="b" messages={[messageWith("msg-9")]} />);
    expect(menu()).not.toBeInTheDocument();
    swipe("msg-9");
    expect(onReplyMessage).toHaveBeenCalledTimes(1);
  });
});

describe("the actions button", () => {
  const actions = () => screen.getByRole("button", { name: "Ações da mensagem" });

  it("opens the same menu, names it, and hands focus back to itself on Escape", () => {
    render(<Conversation messages={[messageWith("msg-1")]} />);
    expect(actions()).toHaveAttribute("aria-expanded", "false");
    expect(actions()).not.toHaveAttribute("aria-controls");

    act(() => actions().focus());
    fireEvent.click(actions());
    expect(menu()).toHaveClass("chat-msg-area__reaction-menu--touch");
    expect(actions()).toHaveAttribute("aria-expanded", "true");
    expect(actions()).toHaveAttribute("aria-controls", menu()!.id);
    expect(menu()!.querySelector("button")).toHaveFocus();

    fireEvent.keyDown(document, { key: "Escape" });
    settle();
    expect(menu()).not.toBeInTheDocument();
    expect(actions()).toHaveFocus();
    act(() => vi.advanceTimersByTime(2000));
    expect(menu()).not.toBeInTheDocument();
  });

  it("opens the touch menu even when keyboard focus already revealed the hover one", () => {
    render(<Conversation messages={[messageWith("msg-1")]} />);
    act(() => shell("msg-1").focus());
    expect(menu()).not.toHaveClass("chat-msg-area__reaction-menu--touch");
    act(() => actions().focus());
    fireEvent.click(actions());
    expect(menu()).toHaveClass("chat-msg-area__reaction-menu--touch");
    expect(actions()).toHaveAttribute("aria-expanded", "true");
  });

  /**
   * A finger's tap the way WebKit delivers it: the compatibility mousedown
   * focuses the row — the nearest focusable ancestor — never the button.
   */
  function tapActions(id: string) {
    fireEvent.pointerDown(actions(), touch);
    fireEvent.pointerUp(actions(), touch);
    act(() => shell(id).focus());
    fireEvent.click(actions());
  }

  it("hands focus back to itself after a tap that focused the row instead", () => {
    render(<Conversation messages={[messageWith("msg-1")]} />);
    act(() => screen.getByRole("button", { name: "fora" }).focus());
    tapActions("msg-1");
    expect(menu()).toHaveClass("chat-msg-area__reaction-menu--touch");
    expect(menu()!.querySelector("button")).toHaveFocus();

    fireEvent.keyDown(document, { key: "Escape" });
    settle();
    expect(menu()).not.toBeInTheDocument();
    expect(actions()).toHaveFocus();
    act(() => vi.advanceTimersByTime(2000));
    expect(menu()).not.toBeInTheDocument();

    tapActions("msg-1");
    expect(menu()).toBeInTheDocument();
  });

  it("keeps the menu and its picker open when focus moves into the picker", () => {
    render(<Conversation messages={[messageWith("msg-1")]} />);
    tapActions("msg-1");
    const more = screen.getByRole("button", { name: "Mais reações" });
    act(() => more.focus());
    fireEvent.click(more);
    // Once its catalog loads, the picker focuses its search field: a field
    // inside the portalled dialog, outside the message's DOM. A plain one
    // stands in for it here, so the lazy catalog stays out of a timer test.
    const search = document.createElement("input");
    search.setAttribute("aria-label", "Buscar emoji");
    screen.getByRole("dialog", { name: "Escolher reação" }).append(search);
    act(() => search.focus());
    settle();

    expect(screen.getByRole("dialog", { name: "Escolher reação" })).toBeInTheDocument();
    expect(menu()).toHaveClass("chat-msg-area__reaction-menu--touch");
    expect(search).toHaveFocus();
  });

  it("does the same under StrictMode", () => {
    render(
      <StrictMode>
        <Conversation messages={[messageWith("msg-1")]} />
      </StrictMode>,
    );
    tapActions("msg-1");
    fireEvent.keyDown(document, { key: "Escape" });
    settle();
    expect(menu()).not.toBeInTheDocument();
    expect(actions()).toHaveFocus();
  });

  it("focuses nothing of a message removed while its tapped menu is open", () => {
    const { rerender } = render(<Conversation messages={[messageWith("msg-1")]} />);
    tapActions("msg-1");
    rerender(<Conversation messages={[messageWith("msg-1", { isRemoved: true })]} />);
    fireEvent.keyDown(document, { key: "Escape" });
    settle();
    expect(menu()).not.toBeInTheDocument();
    expect(shell("msg-1").contains(document.activeElement)).toBe(false);
  });

  it("focuses nothing of the old conversation after a switch", () => {
    const { rerender } = render(<Conversation key="a" messages={[messageWith("msg-1")]} />);
    tapActions("msg-1");
    rerender(<Conversation key="b" messages={[messageWith("msg-9")]} />);
    fireEvent.keyDown(document, { key: "Escape" });
    settle();
    expect(menu()).not.toBeInTheDocument();
    expect(document.activeElement).toBe(document.body);
  });

  it("closes the menu when pressed again", () => {
    render(<Conversation messages={[messageWith("msg-1")]} />);
    fireEvent.click(actions());
    fireEvent.click(actions());
    settle();
    expect(menu()).not.toBeInTheDocument();
  });

  it("is not offered for a removed message", () => {
    render(<Conversation messages={[messageWith("msg-1", { isRemoved: true })]} />);
    expect(screen.queryByRole("button", { name: "Ações da mensagem" })).not.toBeInTheDocument();
  });
});

describe("the meeting-scheduling modal opening over a gesture", () => {
  const channels = [{ id: "ch", name: "Equipe" }] as Channel[];

  function ScheduleButton() {
    const open = useMeetingScheduling();
    return (
      <button type="button" onClick={(event) => open?.("channel", "ch", event.currentTarget)}>
        Agendar reunião
      </button>
    );
  }

  /** The chat as AppShell nests it: the provider around the header and the timeline. */
  function Chat({ messages }: { messages: Message[] }) {
    return (
      <MemoryRouter initialEntries={["/chat/channel/ch"]}>
        <MeetingSchedulingProvider userId="me" workspaceId="ws" channels={channels} dms={[]}>
          <ScheduleButton />
          <Conversation messages={messages} />
        </MeetingSchedulingProvider>
      </MemoryRouter>
    );
  }

  const scheduleButton = () => screen.getByRole("button", { name: "Agendar reunião" });
  const modal = () => document.querySelector("dialog.meeting-dialog");

  /** Enter on the focused header button: the activation a keyboard gives it. */
  function scheduleByKeyboard() {
    act(() => scheduleButton().focus());
    fireEvent.click(scheduleButton());
    expect(modal()).toHaveAttribute("open");
  }

  beforeEach(() => {
    // Candidates load in the background; the modal is what matters here.
    vi.stubGlobal(
      "fetch",
      vi.fn(() => new Promise<Response>(() => {})),
    );
    // jsdom has neither :modal, nor hit-testing, nor showModal() (stood in
    // for below): the open dialog is the modal, and its backdrop is what any
    // point outside it hits.
    const query = document.querySelector.bind(document);
    vi.spyOn(document, "querySelector").mockImplementation(((selector: string) =>
      query(selector === ":modal" ? "dialog[open]" : selector)) as typeof document.querySelector);
    Object.defineProperty(document, "elementFromPoint", {
      configurable: true,
      value: () => query("dialog[open]"),
    });
  });

  afterEach(() => {
    vi.unstubAllGlobals();
    Reflect.deleteProperty(document, "elementFromPoint");
  });

  beforeAll(() => {
    HTMLDialogElement.prototype.showModal = function () {
      this.setAttribute("open", "");
    };
    HTMLDialogElement.prototype.close = function () {
      this.removeAttribute("open");
    };
  });

  afterAll(() => {
    Reflect.deleteProperty(HTMLDialogElement.prototype, "showModal");
    Reflect.deleteProperty(HTMLDialogElement.prototype, "close");
  });

  it("opens no actions over the modal when the press pending under it comes due", () => {
    render(<Chat messages={[messageWith("msg-1")]} />);
    fireEvent.pointerDown(body("msg-1"), { ...touch, clientX: 100, clientY: 200 });
    scheduleByKeyboard();
    act(() => vi.advanceTimersByTime(LONG_PRESS_MS * 2));
    fireEvent.pointerUp(body("msg-1"), { ...touch, clientX: 100, clientY: 200 });

    expect(menu()).not.toBeInTheDocument();
    expect(modal()?.contains(document.activeElement)).toBe(true);
  });

  it("sends no reply for a swipe released after the modal opened", () => {
    render(<Chat messages={[messageWith("msg-1")]} />);
    fireEvent.pointerDown(body("msg-1"), { ...touch, clientX: 100, clientY: 200 });
    for (const dx of [20, 50, SWIPE_REPLY_PX + 20]) {
      fireEvent.pointerMove(body("msg-1"), { ...touch, clientX: 100 + dx, clientY: 202 });
    }
    scheduleByKeyboard();
    fireEvent.pointerUp(body("msg-1"), {
      ...touch,
      clientX: 100 + SWIPE_REPLY_PX + 20,
      clientY: 202,
    });

    expect(onReplyMessage).not.toHaveBeenCalled();
    expect(shell("msg-1").style.transform).toBe("");
  });

  it("starts no gesture while the modal is open", () => {
    render(<Chat messages={[messageWith("msg-1")]} />);
    scheduleByKeyboard();
    longPress("msg-1");
    swipe("msg-1");
    expect(menu()).not.toBeInTheDocument();
    expect(onReplyMessage).not.toHaveBeenCalled();
  });

  it("returns the focus on Escape, revives nothing, and lets a new gesture work", () => {
    render(<Chat messages={[messageWith("msg-1")]} />);
    fireEvent.pointerDown(body("msg-1"), { ...touch, clientX: 100, clientY: 200 });
    scheduleByKeyboard();
    // Escape on a modal dialog is its cancel event.
    fireEvent(modal() as Element, new Event("cancel", { cancelable: true }));
    expect(modal()).not.toBeInTheDocument();
    expect(scheduleButton()).toHaveFocus();

    act(() => vi.advanceTimersByTime(LONG_PRESS_MS * 2));
    fireEvent.pointerUp(body("msg-1"), { ...touch, clientX: 100, clientY: 200 });
    expect(menu()).not.toBeInTheDocument();

    longPress("msg-1");
    expect(menu()).toBeInTheDocument();
    expect(onReplyMessage).not.toHaveBeenCalled();
  });
});

describe("a conversation switch during a gesture", () => {
  it("lets neither the pending press nor the swipe of the old conversation land", () => {
    const { rerender } = render(<Conversation key="a" messages={[messageWith("msg-1")]} />);
    fireEvent.pointerDown(body("msg-1"), { ...touch, clientX: 100, clientY: 200 });
    fireEvent.pointerMove(body("msg-1"), { ...touch, clientX: 101, clientY: 200 });
    const old = shell("msg-1");
    rerender(<Conversation key="b" messages={[messageWith("msg-9")]} />);
    act(() => vi.advanceTimersByTime(LONG_PRESS_MS * 2));
    fireEvent.pointerUp(old, { ...touch, clientX: 100 + SWIPE_REPLY_PX + 20, clientY: 200 });

    expect(menu()).not.toBeInTheDocument();
    expect(onReplyMessage).not.toHaveBeenCalled();
    longPress("msg-9");
    expect(menu()).toBeInTheDocument();
  });
});

/**
 * The picker's tone palette is a second portal, opened from inside the first
 * one (issue #1152, CQ R8). Focus moving into it leaves the picker's DOM as
 * well as the message's, so it has to be recognised as the same message's —
 * or the leave delay closes a touch-opened menu, picker and palette with it.
 * The real picker, catalog and palette, not stand-ins.
 */
describe("the picker's tone palette", () => {
  const thumbsUp = "polegar para cima";
  const palette = () => screen.queryByRole("dialog", { name: `Tom de pele para ${thumbsUp}` });
  const picker = () => screen.queryByRole("dialog", { name: "Escolher reação" });
  const tap = (element: HTMLElement) => {
    fireEvent.pointerDown(element, touch);
    fireEvent.pointerUp(element, touch);
    fireEvent.click(element);
  };

  /** Long-press, open the picker, let it load and focus its search, open the palette. */
  async function openPalette(id = "msg-1") {
    longPress(id);
    tap(screen.getByRole("button", { name: "Mais reações" }));
    // The catalog is a lazy chunk; it loads on real timers.
    vi.useRealTimers();
    const search = await screen.findByRole("searchbox", { name: "Buscar emoji" });
    await waitFor(() => expect(search).toHaveFocus());
    fireEvent.change(search, { target: { value: thumbsUp } });
    vi.useFakeTimers();
    const cell = within(picker()!).getByRole("button", { name: thumbsUp });
    tap(cell);
    return { cell, options: within(palette()!).getAllByRole("button") };
  }

  /** The menu, its picker and the palette all open, touch-opened, for msg-1. */
  function expectStillOpen() {
    expect(menu()).toHaveClass("chat-msg-area__reaction-menu--touch");
    expect(shell("msg-1")).toContainElement(menu());
    expect(picker()).toBeInTheDocument();
    expect(palette()).toBeInTheDocument();
  }

  it("keeps the touch menu, picker and palette when focus moves into the palette", async () => {
    render(<Conversation messages={[messageWith("msg-1")]} />);
    const { options } = await openPalette();
    expect(options[0]).toHaveFocus();

    settle();
    expectStillOpen();
    // Past the touch-echo window too: nothing is waiting to close it later.
    act(() => vi.advanceTimersByTime(1000));
    expectStillOpen();
    act(() => options[2].focus());
    settle();
    expectStillOpen();
    expect(options[2]).toHaveFocus();
  });

  it("does the same under StrictMode", async () => {
    render(
      <StrictMode>
        <Conversation messages={[messageWith("msg-1")]} />
      </StrictMode>,
    );
    await openPalette();
    act(() => vi.advanceTimersByTime(1000));
    expectStillOpen();
  });

  it("reacts once, to its own message, with the tone chosen", async () => {
    render(<Conversation messages={[messageWith("msg-0"), messageWith("msg-1")]} />);
    const { options } = await openPalette();
    settle();
    tap(options[3]);
    settle();

    expect(onToggleReaction).toHaveBeenCalledTimes(1);
    const [messageId, emoji] = onToggleReaction.mock.calls[0];
    expect(messageId).toBe("msg-1");
    expect(emoji).toBe(options[3].textContent);
    expect(palette()).not.toBeInTheDocument();
    expect(picker()).not.toBeInTheDocument();
  });

  it("closes the palette alone on Escape, back on its emoji, then the picker", async () => {
    render(<Conversation messages={[messageWith("msg-1")]} />);
    const { cell, options } = await openPalette();
    fireEvent.keyDown(options[0], { key: "Escape" });
    settle();

    expect(palette()).not.toBeInTheDocument();
    expect(cell).toHaveFocus();
    expect(picker()).toBeInTheDocument();
    expect(menu()).toHaveClass("chat-msg-area__reaction-menu--touch");

    fireEvent.keyDown(cell, { key: "Escape" });
    settle();
    expect(picker()).not.toBeInTheDocument();
  });

  it("closes everything when a tap lands outside", async () => {
    render(<Conversation messages={[messageWith("msg-1")]} />);
    await openPalette();
    const outside = screen.getByRole("button", { name: "fora" });
    fireEvent.pointerDown(outside, touch);
    act(() => outside.focus());
    settle();

    expect(menu()).not.toBeInTheDocument();
    expect(picker()).not.toBeInTheDocument();
    expect(palette()).not.toBeInTheDocument();
  });

  /**
   * Focus leaving with no press is the hover rule as it always was: the menu
   * stops being the message's touch menu. The picker keeps to its own rules —
   * a press outside, Escape, a choice, or no room left beside the bubble.
   */
  const expectTouchMarkDropped = () =>
    expect(screen.getByRole("toolbar", { name: "Reagir à mensagem" })).not.toHaveClass(
      "chat-msg-area__reaction-menu--touch",
    );

  it("drops the touch mark when focus moves outside", async () => {
    render(<Conversation messages={[messageWith("msg-1")]} />);
    await openPalette();
    act(() => screen.getByRole("button", { name: "fora" }).focus());
    settle();
    expectTouchMarkDropped();
  });

  it("drops the touch mark when focus is dropped to nowhere", async () => {
    render(<Conversation messages={[messageWith("msg-1")]} />);
    const { options } = await openPalette();
    act(() => options[0].blur());
    settle();
    expectTouchMarkDropped();
  });

  it("does not take another message's surface for its own", async () => {
    render(<Conversation messages={[messageWith("msg-1")]} />);
    await openPalette();
    // A surface marked for another message, as a stale or foreign one would be.
    const foreign = document.createElement("div");
    foreign.setAttribute("data-reaction-picker-for", "msg-2");
    const field = document.createElement("button");
    foreign.append(field);
    document.body.append(foreign);
    act(() => field.focus());
    settle();
    expectTouchMarkDropped();
    foreign.remove();
  });

  it("hands the touch menu over to another message long-pressed meanwhile", async () => {
    render(<Conversation messages={[messageWith("msg-1"), messageWith("msg-2")]} />);
    await openPalette();
    longPress("msg-2");
    settle();

    expect(picker()).not.toBeInTheDocument();
    expect(palette()).not.toBeInTheDocument();
    expect(menu()).toHaveClass("chat-msg-area__reaction-menu--touch");
    expect(shell("msg-2")).toContainElement(menu());
  });

  it("closes everything when its message is removed", async () => {
    const { rerender } = render(<Conversation messages={[messageWith("msg-1")]} />);
    await openPalette();
    rerender(<Conversation messages={[messageWith("msg-1", { isRemoved: true })]} />);
    settle();

    expect(menu()).not.toBeInTheDocument();
    expect(picker()).not.toBeInTheDocument();
    expect(palette()).not.toBeInTheDocument();
  });

  it("closes everything on a conversation switch", async () => {
    const { rerender } = render(<Conversation key="a" messages={[messageWith("msg-1")]} />);
    await openPalette();
    rerender(<Conversation key="b" messages={[messageWith("msg-9")]} />);
    settle();

    expect(menu()).not.toBeInTheDocument();
    expect(picker()).not.toBeInTheDocument();
    expect(palette()).not.toBeInTheDocument();
    swipe("msg-9");
    expect(onReplyMessage).toHaveBeenCalledTimes(1);
  });
});
