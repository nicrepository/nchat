/**
 * The composer's priority selector, end to end (issue #822).
 *
 * Its own file rather than more cases in ChatComposer.test.tsx: this covers one
 * coherent surface — a popover, a transactional draft, an applied summary and
 * what all of that hands to the send — and the composer's existing suite is
 * already about the editor.
 *
 * The behaviours asserted here are the ones that are expensive to get wrong:
 * Cancelar and Escape leaving the applied value alone, a downgrade not leaving
 * urgent-only flags behind, and the send carrying exactly what was applied.
 */

import { act, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";

import { flushResizeObservers } from "../setupTests";
import ChatComposer from "./ChatComposer";
import type { MessagePriorityIntent } from "./messagePriority";
import type { SendResult } from "./messages/types";

type Send = (
  body: string,
  attachmentIds?: string[],
  priority?: MessagePriorityIntent,
) => Promise<SendResult>;

function setup() {
  const onSend = vi.fn<Send>();
  onSend.mockResolvedValue({ status: "sent" });
  render(<ChatComposer bodyFormat="v2" placeholder="Mensagem..." onSend={onSend} />);
  return onSend;
}

const trigger = () => screen.getByTestId("toolbar-priority-btn");
const dialog = () => screen.getByTestId("composer-priority-dialog");
const summary = () => screen.queryByTestId("composer-priority-summary");

function open() {
  fireEvent.click(trigger());
  return dialog();
}

/** Opens, states a priority, ticks whatever it unlocks, and applies. */
function apply(priority: "standard" | "important" | "urgent", options: string[] = []) {
  open();
  fireEvent.click(screen.getByTestId(`priority-option-${priority}`));
  for (const option of options) fireEvent.click(screen.getByTestId(option));
  fireEvent.click(screen.getByTestId("priority-apply"));
}

async function type(text: string) {
  const input = await screen.findByTestId("chat-composer-input");
  fireEvent.paste(input, {
    clipboardData: {
      types: ["text/plain"],
      getData: (format: string) => (format === "text/plain" ? text : ""),
      items: [],
      files: [],
    },
  });
  await waitFor(() => expect(input).toHaveTextContent(text));
  return input;
}

async function send(onSend: ReturnType<typeof setup>) {
  fireEvent.click(screen.getByTestId("chat-send-btn"));
  await waitFor(() => expect(onSend).toHaveBeenCalled());
  return onSend.mock.calls[0][2];
}

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("composer priority — initial state", () => {
  it("starts standard, with nothing stated above the text", async () => {
    setup();
    await screen.findByTestId("chat-composer-input");
    expect(trigger()).toHaveAccessibleName("Prioridade da mensagem: Padrão");
    expect(summary()).not.toBeInTheDocument();
    expect(screen.queryByTestId("composer-priority-dialog")).not.toBeInTheDocument();
  });

  it("opens on the applied choice and offers the three priorities", async () => {
    setup();
    await screen.findByTestId("chat-composer-input");
    open();
    expect(screen.getByTestId("priority-option-standard")).toBeChecked();
    for (const label of ["Padrão", "Importante", "Urgente"]) {
      expect(within(dialog()).getByLabelText(label)).toBeInTheDocument();
    }
  });
});

describe("composer priority — selection", () => {
  it.each([
    ["important", "Importante"],
    ["urgent", "Urgente"],
  ] as const)("applies %s and states it above the text", async (priority, label) => {
    setup();
    await screen.findByTestId("chat-composer-input");
    apply(priority);
    expect(summary()).toHaveTextContent(label);
    expect(trigger()).toHaveAccessibleName(`Prioridade da mensagem: ${label}`);
  });

  it("shows the urgent options only where the policy allows them", async () => {
    setup();
    await screen.findByTestId("chat-composer-input");
    open();
    expect(screen.queryByTestId("priority-acknowledgement")).not.toBeInTheDocument();
    fireEvent.click(screen.getByTestId("priority-option-important"));
    expect(screen.queryByTestId("priority-persistent")).not.toBeInTheDocument();
    fireEvent.click(screen.getByTestId("priority-option-urgent"));
    expect(screen.getByTestId("priority-acknowledgement")).toBeInTheDocument();
    expect(screen.getByTestId("priority-persistent")).toBeInTheDocument();
  });

  it("explains what persistent notifications actually do", async () => {
    setup();
    await screen.findByTestId("chat-composer-input");
    open();
    fireEvent.click(screen.getByTestId("priority-option-urgent"));
    expect(screen.getByTestId("priority-persistent")).toHaveAccessibleDescription(
      "lembretes a cada 5 minutos até confirmação ou resposta",
    );
  });

  it("describes both applied flags in the summary", async () => {
    setup();
    await screen.findByTestId("chat-composer-input");
    apply("urgent", ["priority-acknowledgement", "priority-persistent"]);
    expect(summary()).toHaveTextContent("Confirmação solicitada");
    expect(summary()).toHaveTextContent("Notificações persistentes");
  });
});

describe("composer priority — discarding a draft", () => {
  // The bug this whole transactional shape exists to prevent.
  it("keeps the previous state when the draft is cancelled", async () => {
    const onSend = setup();
    await type("olá");
    apply("important");

    open();
    fireEvent.click(screen.getByTestId("priority-option-urgent"));
    fireEvent.click(screen.getByTestId("priority-acknowledgement"));
    fireEvent.click(screen.getByTestId("priority-cancel"));

    expect(summary()).toHaveTextContent("Importante");
    expect(await send(onSend)).toEqual({
      priority: "important",
      acknowledgementRequired: false,
      persistentNotifications: false,
    });
  });

  it("discards the draft on Escape and hands focus back to the trigger", async () => {
    const user = userEvent.setup();
    setup();
    await screen.findByTestId("chat-composer-input");
    apply("important");

    open();
    fireEvent.click(screen.getByTestId("priority-option-urgent"));
    await user.keyboard("{Escape}");

    expect(screen.queryByTestId("composer-priority-dialog")).not.toBeInTheDocument();
    expect(summary()).toHaveTextContent("Importante");
    expect(trigger()).toHaveFocus();
  });

  it("treats a click outside as a cancel, like every other dialog here", async () => {
    setup();
    await screen.findByTestId("chat-composer-input");
    apply("urgent");

    open();
    fireEvent.click(screen.getByTestId("priority-option-standard"));
    fireEvent.mouseDown(dialog().parentElement as HTMLElement);

    expect(screen.queryByTestId("composer-priority-dialog")).not.toBeInTheDocument();
    expect(summary()).toHaveTextContent("Urgente");
  });
});

describe("composer priority — downgrading", () => {
  // Hiding the two options is presentation. Clearing them is the contract: a
  // persistent_notifications left true under a non-urgent priority is refused
  // by chat-service outright.
  it.each([
    ["important", "Importante"],
    ["standard", "Padrão"],
  ] as const)("leaves no urgent-only flag behind on %s", async (priority, label) => {
    const onSend = setup();
    await type("olá");
    apply("urgent", ["priority-acknowledgement", "priority-persistent"]);
    apply(priority);

    if (priority === "standard") expect(summary()).not.toBeInTheDocument();
    else expect(summary()).toHaveTextContent(label);
    expect(await send(onSend)).toEqual({
      priority,
      acknowledgementRequired: false,
      persistentNotifications: false,
    });
  });

  it("reopens with the urgent options already cleared", async () => {
    setup();
    await screen.findByTestId("chat-composer-input");
    apply("urgent", ["priority-acknowledgement"]);
    apply("important");

    open();
    fireEvent.click(screen.getByTestId("priority-option-urgent"));
    expect(screen.getByTestId("priority-acknowledgement")).not.toBeChecked();
  });
});

describe("composer priority — removing", () => {
  it("restores standard and clears everything that came with it", async () => {
    const onSend = setup();
    await type("olá");
    apply("urgent", ["priority-acknowledgement", "priority-persistent"]);

    fireEvent.click(screen.getByLabelText("Remover prioridade da mensagem"));

    expect(summary()).not.toBeInTheDocument();
    expect(trigger()).toHaveAccessibleName("Prioridade da mensagem: Padrão");
    expect(await send(onSend)).toEqual({
      priority: "standard",
      acknowledgementRequired: false,
      persistentNotifications: false,
    });
  });
});

describe("composer priority — sending", () => {
  it("carries the applied configuration on the message", async () => {
    const onSend = setup();
    await type("reiniciar o cluster");
    apply("urgent", ["priority-acknowledgement", "priority-persistent"]);

    expect(await send(onSend)).toEqual({
      priority: "urgent",
      acknowledgementRequired: true,
      persistentNotifications: true,
    });
  });

  // No regression: an ordinary send is exactly what it was before this issue.
  it("sends an unconfigured message as standard", async () => {
    const onSend = setup();
    await type("olá");
    expect(await send(onSend)).toEqual({
      priority: "standard",
      acknowledgementRequired: false,
      persistentNotifications: false,
    });
  });

  it("resets to standard once the message that carried it is sent", async () => {
    const onSend = setup();
    await type("olá");
    apply("urgent", ["priority-persistent"]);
    await send(onSend);
    await waitFor(() => expect(summary()).not.toBeInTheDocument());
  });
});

describe("composer priority — keyboard and focus", () => {
  it("is reachable and operable without a mouse", async () => {
    const user = userEvent.setup();
    const onSend = setup();
    await type("olá");

    trigger().focus();
    await user.keyboard("{Enter}");
    expect(dialog()).toBeInTheDocument();

    // A native radio group: the arrow keys move and select in one gesture.
    await user.keyboard("{ArrowDown}{ArrowDown}");
    expect(screen.getByTestId("priority-option-urgent")).toBeChecked();

    await user.tab();
    await user.keyboard(" ");
    expect(screen.getByTestId("priority-acknowledgement")).toBeChecked();

    await user.click(screen.getByTestId("priority-apply"));
    expect(await send(onSend)).toMatchObject({
      priority: "urgent",
      acknowledgementRequired: true,
    });
  });

  it("keeps Tab inside the dialog while it is open", async () => {
    const user = userEvent.setup();
    setup();
    await screen.findByTestId("chat-composer-input");
    open();

    const apply = screen.getByTestId("priority-apply");
    apply.focus();
    await user.tab();
    expect(dialog()).toContainElement(document.activeElement as HTMLElement);

    // And backwards out of the first control, which is the half a trap that
    // only wraps forwards silently loses.
    screen.getByTestId("priority-option-standard").focus();
    await user.tab({ shift: true });
    expect(dialog()).toContainElement(document.activeElement as HTMLElement);
  });

  it("returns focus to the trigger after applying", async () => {
    setup();
    await screen.findByTestId("chat-composer-input");
    apply("important");
    expect(trigger()).toHaveFocus();
  });
});

describe("composer priority — desktop", () => {
  /**
   * jsdom lays nothing out, so the trigger has to be given a box for the
   * placement to have anything to place against — the same device
   * ComposerToolbar.test.tsx uses for the emoji picker.
   */
  function stubTriggerBox() {
    const real = Element.prototype.getBoundingClientRect;
    const box = {
      x: 300,
      y: 500,
      left: 300,
      right: 330,
      top: 500,
      bottom: 530,
      width: 30,
      height: 30,
      toJSON: () => ({}),
    } as DOMRect;
    vi.spyOn(Element.prototype, "getBoundingClientRect").mockImplementation(function (
      this: Element,
    ) {
      return this.getAttribute("data-testid") === "toolbar-priority-btn" ? box : real.call(this);
    });
  }

  afterEach(() => vi.restoreAllMocks());

  it("hangs the popover off its button when there is room beside it", async () => {
    stubTriggerBox();
    setup();
    await screen.findByTestId("chat-composer-input");
    open();

    expect(dialog()).toHaveClass("msg-priority--anchored");
    // Placed above the button, inside the viewport — not left at 0,0.
    expect(dialog().style.top).not.toBe("");
    expect(dialog()).toBeVisible();
  });

  // No room beside the button is the case that used to leave an invisible
  // dialog behind: it falls back to the sheet rather than to nothing.
  it("falls back to the sheet layout when it cannot be placed", async () => {
    setup();
    await screen.findByTestId("chat-composer-input");
    open();

    expect(dialog()).not.toHaveClass("msg-priority--anchored");
    expect(dialog()).toBeVisible();
  });
});

/**
 * ISSUE #823 — the popover has to stay whole while its own content changes.
 *
 * Choosing Urgente reveals two checkboxes and a hint, and the panel grows by
 * about a third *after* it has been placed. Placed above the button, the growth
 * is all downwards, so the tall panel keeps the short one's top edge and its
 * bottom — with Aplicar on it — leaves the screen.
 *
 * jsdom lays nothing out, so both boxes are stubbed: the trigger's, and the
 * panel's, whose height answers according to what it is currently showing.
 * That is what lets these assert a real re-placement rather than the same
 * arithmetic twice.
 */
describe("composer priority — staying inside the viewport", () => {
  const anchorTop = 500;
  const shortPanel = 200;
  const tallPanel = 320;

  /** The panel is tall exactly when the urgent options are on screen. */
  function stubBoxes(viewportHeight: number, top = anchorTop) {
    vi.stubGlobal("innerHeight", viewportHeight);
    const real = Element.prototype.getBoundingClientRect;
    const rect = (top: number, height: number, left: number, width: number) =>
      ({
        x: left,
        y: top,
        left,
        right: left + width,
        top,
        bottom: top + height,
        width,
        height,
        toJSON: () => ({}),
      }) as DOMRect;
    vi.spyOn(Element.prototype, "getBoundingClientRect").mockImplementation(function (
      this: Element,
    ) {
      const id = this.getAttribute("data-testid");
      if (id === "toolbar-priority-btn") return rect(top, 30, 300, 30);
      if (id !== "composer-priority-dialog") return real.call(this);
      const tall = Boolean(this.querySelector("[data-testid='priority-persistent']"));
      return rect(0, tall ? tallPanel : shortPanel, 300, 288);
    });
  }

  afterEach(() => vi.restoreAllMocks());

  const topOf = () => Number.parseFloat(dialog().style.top);
  const maxHeightOf = () => dialog().style.maxHeight;

  /**
   * The cap is the room beside the anchor, which is the whole point: a viewport
   * fraction knows nothing about where the button is, and 85vh overflows a
   * button that sits at 85vh.
   */
  it("caps itself to the room beside its trigger, not to a slice of the viewport", () => {
    stubBoxes(620);
    setup();
    open();

    // Above the button: its top, less the gap and the edge padding.
    expect(maxHeightOf()).toBe(`${anchorTop - 7 - 8}px`);
    expect(dialog()).toHaveClass("msg-priority--anchored");
  });

  // The regression itself. Without a second placement the top stays where the
  // short panel put it and the tall panel hangs off the bottom.
  it("places itself again when choosing Urgente makes it taller", () => {
    stubBoxes(620);
    setup();
    open();
    expect(topOf()).toBe(anchorTop - shortPanel - 7);

    fireEvent.click(screen.getByTestId("priority-option-urgent"));
    act(() => flushResizeObservers());

    expect(topOf()).toBe(anchorTop - tallPanel - 7);
    // Whole, above the button, with nothing past either edge.
    expect(topOf()).toBeGreaterThanOrEqual(8);
    expect(topOf() + tallPanel).toBeLessThanOrEqual(anchorTop);
  });

  // The composer sits at the bottom, so a shorter window brings its button up
  // with it and the room above shrinks. The cap has to follow, or the panel
  // that fitted a moment ago no longer does.
  it("places itself again when the window changes size underneath it", () => {
    stubBoxes(620);
    setup();
    open();
    expect(maxHeightOf()).toBe(`${anchorTop - 7 - 8}px`);

    vi.restoreAllMocks();
    stubBoxes(500, 380);
    act(() => {
      window.dispatchEvent(new Event("resize"));
    });

    expect(maxHeightOf()).toBe(`${380 - 7 - 8}px`);
    expect(topOf()).toBe(380 - shortPanel - 7);
    expect(dialog()).toHaveClass("msg-priority--anchored");
  });

  /**
   * Too little room on either side is the sheet's case. Capping down to a
   * sliver would be a scrollport with nothing in it, so the layout changes
   * instead — and the decision is taken from the anchor alone, which is what
   * stops it flipping back the moment the new layout resizes the panel.
   */
  it("falls back to the sheet when neither side has usable room", () => {
    vi.stubGlobal("innerHeight", 200);
    const real = Element.prototype.getBoundingClientRect;
    vi.spyOn(Element.prototype, "getBoundingClientRect").mockImplementation(function (
      this: Element,
    ) {
      if (this.getAttribute("data-testid") !== "toolbar-priority-btn") return real.call(this);
      return {
        x: 300,
        y: 90,
        left: 300,
        right: 330,
        top: 90,
        bottom: 120,
        width: 30,
        height: 30,
        toJSON: () => ({}),
      } as DOMRect;
    });
    setup();
    open();

    expect(dialog()).not.toHaveClass("msg-priority--anchored");
    expect(dialog()).not.toHaveAttribute("style");
    expect(screen.getByTestId("priority-apply")).toBeVisible();
  });
});

describe("composer priority — small viewport", () => {
  /** matchMedia is not implemented by jsdom; the sheet query is what decides. */
  function stubViewport(compact: boolean) {
    vi.stubGlobal("matchMedia", (query: string) => ({
      matches: compact,
      media: query,
      addEventListener: () => undefined,
      removeEventListener: () => undefined,
    }));
  }

  it("becomes a sheet instead of a popover, with the same options", async () => {
    stubViewport(true);
    const onSend = setup();
    await type("olá");

    open();
    expect(dialog()).not.toHaveClass("msg-priority--anchored");
    // Placed by the stylesheet, never by an inline coordinate that could put it
    // off screen: nothing here writes left/top on the sheet.
    expect(dialog()).not.toHaveAttribute("style");

    fireEvent.click(screen.getByTestId("priority-option-urgent"));
    fireEvent.click(screen.getByTestId("priority-persistent"));
    fireEvent.click(screen.getByTestId("priority-apply"));

    expect(await send(onSend)).toMatchObject({
      priority: "urgent",
      persistentNotifications: true,
    });
  });
});
