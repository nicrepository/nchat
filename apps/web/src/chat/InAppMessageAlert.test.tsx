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
