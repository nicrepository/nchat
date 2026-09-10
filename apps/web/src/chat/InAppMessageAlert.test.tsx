import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import InAppMessageAlert, { type InAppAlert } from "./InAppMessageAlert";

/**
 * The in-app surface itself (issue #744). What is proved here is that the
 * channel has a visible consumer: given an alert it renders one, and its two
 * actions are the only two it offers.
 */
const alert: InAppAlert = {
  messageId: "message-1",
  targetKind: "channel",
  targetId: "channel-1",
  senderDisplayName: "Ana",
  bodyText: "Nova mensagem",
  conversationName: "geral",
};

describe("InAppMessageAlert", () => {
  beforeEach(() => vi.useFakeTimers({ shouldAdvanceTime: true }));
  afterEach(() => vi.useRealTimers());

  it("shows who wrote, where, and a preview", () => {
    render(<InAppMessageAlert alert={alert} onOpen={vi.fn()} onDismiss={vi.fn()} />);
    expect(screen.getByTestId("in-app-message-alert")).toBeInTheDocument();
    expect(screen.getByText("Ana")).toBeInTheDocument();
    expect(screen.getByText("geral")).toBeInTheDocument();
    expect(screen.getByText("Nova mensagem")).toBeInTheDocument();
  });

  it("opens the conversation it points at", async () => {
    const onOpen = vi.fn();
    const user = userEvent.setup({ advanceTimers: vi.advanceTimersByTime });
    render(<InAppMessageAlert alert={alert} onOpen={onOpen} onDismiss={vi.fn()} />);

    await user.click(screen.getByTestId("in-app-message-alert-open"));

    expect(onOpen).toHaveBeenCalledWith(alert);
  });

  // The action is a labelled control, not a clickable card: its visible label
  // starts its accessible name (WCAG 2.5.3), and the conversation is what makes
  // that name unambiguous when it is read out of context.
  it("offers Abrir as a named, keyboard-reachable action", async () => {
    const onOpen = vi.fn();
    const user = userEvent.setup({ advanceTimers: vi.advanceTimersByTime });
    render(<InAppMessageAlert alert={alert} onOpen={onOpen} onDismiss={vi.fn()} />);

    const open = screen.getByRole("button", { name: "Abrir conversa geral" });
    expect(open).toHaveTextContent("Abrir");

    await user.tab();
    expect(open).toHaveFocus();
    await user.keyboard("{Enter}");

    expect(onOpen).toHaveBeenCalledWith(alert);
  });

  // An alert about somewhere else must never take the reader out of what they
  // are doing here.
  it("does not move focus when it appears", () => {
    const input = document.createElement("input");
    document.body.append(input);
    input.focus();

    render(<InAppMessageAlert alert={alert} onOpen={vi.fn()} onDismiss={vi.fn()} />);

    expect(input).toHaveFocus();
    input.remove();
  });

  // The body is data on the way in and data on the way out: whatever a sender
  // writes is text in the DOM, never markup.
  it("renders a message body as text, never as markup", () => {
    render(
      <InAppMessageAlert
        alert={{ ...alert, bodyText: "<img src=x onerror=alert(1)>" }}
        onOpen={vi.fn()}
        onDismiss={vi.fn()}
      />,
    );

    const surface = screen.getByTestId("in-app-message-alert");
    expect(surface.querySelector("img")).toBeNull();
    expect(screen.getByText("<img src=x onerror=alert(1)>")).toBeInTheDocument();
  });

  it("can be dismissed", async () => {
    const onDismiss = vi.fn();
    const user = userEvent.setup({ advanceTimers: vi.advanceTimersByTime });
    render(<InAppMessageAlert alert={alert} onOpen={vi.fn()} onDismiss={onDismiss} />);

    await user.click(screen.getByTestId("in-app-message-alert-dismiss"));

    expect(onDismiss).toHaveBeenCalled();
  });

  // It is a notification, not a panel: unattended, it goes away on its own and
  // leaves nothing behind.
  it("dismisses itself when nobody attends to it", () => {
    const onDismiss = vi.fn();
    render(<InAppMessageAlert alert={alert} onOpen={vi.fn()} onDismiss={onDismiss} />);

    expect(onDismiss).not.toHaveBeenCalled();
    vi.advanceTimersByTime(6000);
    expect(onDismiss).toHaveBeenCalledTimes(1);
  });

  // Announced, never interrupting: a message elsewhere is not worth cutting
  // across what a screen-reader user is doing.
  it("announces politely", () => {
    render(<InAppMessageAlert alert={alert} onOpen={vi.fn()} onDismiss={vi.fn()} />);
    const surface = screen.getByTestId("in-app-message-alert");
    expect(surface).toHaveAttribute("role", "status");
    expect(surface).toHaveAttribute("aria-live", "polite");
  });
});
