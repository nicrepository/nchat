/**
 * Asking for confirmation from the composer (issue #824).
 *
 * The behaviours here are #824's and are unchanged; only the way in is. #824
 * parked a standalone toolbar toggle here because the priority popover it
 * belongs inside did not exist yet, and #822 built that popover — so the
 * request is now made where #820 always said it would be: beside the priority
 * it accompanies, applied with it, and cleared with it.
 */

import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";

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

function trigger() {
  return screen.getByTestId("toolbar-priority-btn");
}

/**
 * Opens the popover, states Urgente — the only priority this version's policy
 * offers the request under — ticks it, and applies.
 */
function requestAcknowledgement() {
  fireEvent.click(trigger());
  fireEvent.click(screen.getByTestId("priority-option-urgent"));
  fireEvent.click(screen.getByTestId("priority-acknowledgement"));
  fireEvent.click(screen.getByTestId("priority-apply"));
}

/**
 * Types into the composer through the paste path the existing suite uses: the
 * editor is a real TipTap instance, so a synthetic input event does not reach
 * its document model.
 */
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

describe("composer acknowledgement", () => {
  it("is off until somebody turns it on", async () => {
    setup();
    await screen.findByTestId("chat-composer-input");
    fireEvent.click(trigger());
    expect(screen.queryByTestId("priority-acknowledgement")).not.toBeInTheDocument();
    fireEvent.click(screen.getByTestId("priority-option-urgent"));
    expect(screen.getByTestId("priority-acknowledgement")).not.toBeChecked();
  });

  it("announces its state rather than relying on the tint", async () => {
    setup();
    await screen.findByTestId("chat-composer-input");
    requestAcknowledgement();
    expect(trigger()).toHaveAccessibleName(/confirmação solicitada/i);
    expect(screen.getByTestId("composer-priority-summary")).toHaveTextContent(
      /confirmação solicitada/i,
    );
  });

  // The default must stay exactly what it was before this issue: a send that
  // nobody configured asks nobody to confirm anything.
  it("sends an ordinary message without asking for confirmation", async () => {
    const onSend = setup();
    await type("olá");
    fireEvent.click(screen.getByTestId("chat-send-btn"));
    await waitFor(() => expect(onSend).toHaveBeenCalled());
    expect(onSend.mock.calls[0][2]?.acknowledgementRequired).toBeFalsy();
  });

  it("carries the request on the message it was composed with", async () => {
    const onSend = setup();
    await type("reiniciar o cluster");
    requestAcknowledgement();
    fireEvent.click(screen.getByTestId("chat-send-btn"));
    await waitFor(() => expect(onSend).toHaveBeenCalled());
    expect(onSend.mock.calls[0][2]).toMatchObject({
      priority: "urgent",
      acknowledgementRequired: true,
    });
  });

  // The request belongs to the message, not to the composer: leaving it on
  // would silently ask for confirmation of everything typed afterwards.
  it("clears itself once the message it belonged to is sent", async () => {
    setup();
    await type("primeira");
    requestAcknowledgement();
    fireEvent.click(screen.getByTestId("chat-send-btn"));
    await waitFor(() =>
      expect(screen.queryByTestId("composer-priority-summary")).not.toBeInTheDocument(),
    );
    expect(trigger()).toHaveAccessibleName("Prioridade da mensagem: Padrão");
  });
});
