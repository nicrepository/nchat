/**
 * Asking for confirmation from the composer (issue #824).
 *
 * The policy half first: #820 keeps acknowledgement independent of priority in
 * the domain, and says the first UI *may* offer it only for Urgent rather than
 * that it must. The priority popover it describes belongs to #822 and does not
 * exist yet, so this is a toolbar toggle — off by default, applied to exactly
 * the message that was composed with it on, and absorbed into that popover when
 * it lands.
 */

import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";

import ChatComposer from "./ChatComposer";
import type { SendResult } from "./messages/types";

type Send = (
  body: string,
  attachmentIds?: string[],
  acknowledgementRequired?: boolean,
) => Promise<SendResult>;

function setup() {
  const onSend = vi.fn<Send>();
  onSend.mockResolvedValue({ status: "sent" });
  render(<ChatComposer bodyFormat="v2" placeholder="Mensagem..." onSend={onSend} />);
  return onSend;
}

function toggle() {
  return screen.getByRole("button", { name: /solicitar confirmação de recebimento/i });
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
    expect(toggle()).toHaveAttribute("aria-pressed", "false");
  });

  it("announces its state rather than relying on the tint", async () => {
    setup();
    await screen.findByTestId("chat-composer-input");
    fireEvent.click(toggle());
    expect(toggle()).toHaveAttribute("aria-pressed", "true");
    fireEvent.click(toggle());
    expect(toggle()).toHaveAttribute("aria-pressed", "false");
  });

  // The default must stay exactly what it was before this issue: a send that
  // nobody configured asks nobody to confirm anything.
  it("sends an ordinary message without asking for confirmation", async () => {
    const onSend = setup();
    await type("olá");
    fireEvent.click(screen.getByTestId("chat-send-btn"));
    await waitFor(() => expect(onSend).toHaveBeenCalled());
    expect(onSend.mock.calls[0][2]).toBeFalsy();
  });

  it("carries the request on the message it was composed with", async () => {
    const onSend = setup();
    await type("reiniciar o cluster");
    fireEvent.click(toggle());
    fireEvent.click(screen.getByTestId("chat-send-btn"));
    await waitFor(() => expect(onSend).toHaveBeenCalled());
    expect(onSend.mock.calls[0][2]).toBe(true);
  });

  // The request belongs to the message, not to the composer: leaving it on
  // would silently ask for confirmation of everything typed afterwards.
  it("clears itself once the message it belonged to is sent", async () => {
    setup();
    await type("primeira");
    fireEvent.click(toggle());
    fireEvent.click(screen.getByTestId("chat-send-btn"));
    await waitFor(() => expect(toggle()).toHaveAttribute("aria-pressed", "false"));
  });
});
