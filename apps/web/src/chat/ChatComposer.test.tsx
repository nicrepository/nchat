import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";

const { mockFetchMentionCandidates } = vi.hoisted(() => ({
  mockFetchMentionCandidates: vi.fn(),
}));

vi.mock("./chatApi", () => ({
  fetchMentionCandidates: (...args: unknown[]) => mockFetchMentionCandidates(...args),
}));

import ChatComposer from "./ChatComposer";
import RichTextRenderer from "./RichTextRenderer";
import type { SendResult } from "./useMessages";

function clipboardData(html: string, plain: string): DataTransfer {
  return {
    files: [] as unknown as FileList,
    getData: vi.fn((type: string) => {
      if (type === "text/html") return html;
      if (type === "text/plain") return plain;
      return "";
    }),
    setData: vi.fn(),
    clearData: vi.fn(),
    items: [] as unknown as DataTransferItemList,
    types: html ? ["text/html", "text/plain"] : ["text/plain"],
    dropEffect: "none",
    effectAllowed: "all",
    setDragImage: vi.fn(),
  };
}

async function paste(html: string, plain: string): Promise<HTMLElement> {
  const input = await screen.findByTestId("chat-composer-input");
  fireEvent.paste(input, { clipboardData: clipboardData(html, plain) });
  await waitFor(() => {
    for (const word of plain.split(/\s+/)) expect(input).toHaveTextContent(word);
  });
  return input;
}

function setup() {
  const onSend = vi.fn<(body: string) => Promise<SendResult>>();
  onSend.mockResolvedValue({ status: "sent" });
  render(<ChatComposer bodyFormat="v2" placeholder="Mensagem..." onSend={onSend} />);
  return onSend;
}

describe("ChatComposer focus", () => {
  it("focuses once when a desktop composer becomes writable without scrolling", async () => {
    const focus = vi.spyOn(HTMLElement.prototype, "focus");
    const { rerender } = render(
      <ChatComposer bodyFormat="v2" placeholder="Mensagem..." disabled onSend={vi.fn()} />,
    );

    const input = await screen.findByTestId("chat-composer-input");
    expect(input).not.toHaveFocus();
    rerender(<ChatComposer bodyFormat="v2" placeholder="Mensagem..." onSend={vi.fn()} />);

    await waitFor(() => expect(input).toHaveFocus());
    expect(focus).toHaveBeenCalledWith({ preventScroll: true });

    const other = document.createElement("button");
    document.body.append(other);
    other.focus();
    rerender(<ChatComposer bodyFormat="v2" placeholder="Nova" onSend={vi.fn()} />);
    expect(other).toHaveFocus();
    other.remove();
    focus.mockRestore();
  });

  it("does not autofocus read-only or mobile composers", async () => {
    const originalMatchMedia = window.matchMedia;
    window.matchMedia = vi.fn().mockReturnValue({
      matches: true,
      addEventListener: vi.fn(),
      removeEventListener: vi.fn(),
    });
    const { rerender } = render(
      <ChatComposer bodyFormat="v2" placeholder="Mensagem..." disabled onSend={vi.fn()} />,
    );
    const input = await screen.findByTestId("chat-composer-input");
    expect(input).not.toHaveFocus();

    rerender(<ChatComposer bodyFormat="v2" placeholder="Mensagem..." onSend={vi.fn()} />);
    await waitFor(() => expect(input).not.toHaveFocus());
    window.matchMedia = originalMatchMedia;
  });

  it("respects focus moved to another control while loading", async () => {
    const { rerender } = render(
      <ChatComposer bodyFormat="v2" placeholder="Mensagem..." disabled onSend={vi.fn()} />,
    );
    const other = document.createElement("button");
    document.body.append(other);
    other.focus();

    rerender(<ChatComposer bodyFormat="v2" placeholder="Mensagem..." onSend={vi.fn()} />);
    await waitFor(() => expect(other).toHaveFocus());
    other.remove();
  });

  it("keeps focus after Enter and preserves it with the draft on failure", async () => {
    let rejectSend!: (error: Error) => void;
    const onSend = vi.fn().mockReturnValue(new Promise((_, reject) => (rejectSend = reject)));
    render(<ChatComposer bodyFormat="v2" placeholder="Mensagem..." onSend={onSend} />);
    const input = await paste("", "tentar novamente");
    input.focus();

    fireEvent.keyDown(input, { key: "Enter", code: "Enter" });
    await waitFor(() => expect(onSend).toHaveBeenCalledOnce());
    await waitFor(() => expect(input).toHaveAttribute("aria-disabled", "true"));
    await act(async () => rejectSend(new Error("offline")));
    await waitFor(() => expect(input).toHaveAttribute("aria-disabled", "false"));

    expect(input).toHaveFocus();
    expect(input).toHaveTextContent("tentar novamente");
  });

  it("keeps focus after a successful Enter send", async () => {
    const onSend = setup();
    const input = await paste("", "enviar");
    input.focus();

    fireEvent.keyDown(input, { key: "Enter", code: "Enter" });

    await waitFor(() => expect(onSend).toHaveBeenCalledOnce());
    expect(input).toHaveFocus();
    await waitFor(() => expect(input.textContent?.trim()).toBe(""));
  });

  it("blocks edits during a pending send, then clears and restores focus", async () => {
    let resolveSend!: (result: SendResult) => void;
    const onSend = vi.fn().mockReturnValue(new Promise((resolve) => (resolveSend = resolve)));
    render(<ChatComposer bodyFormat="v2" placeholder="Mensagem..." onSend={onSend} />);
    const input = await paste("", "sent");
    input.focus();

    fireEvent.keyDown(input, { key: "Enter", code: "Enter" });
    await waitFor(() => expect(input).toHaveAttribute("aria-disabled", "true"));
    await userEvent.type(input, " later", { skipClick: true });
    expect(input).toHaveTextContent("sent");

    await act(async () => resolveSend({ status: "sent" }));
    await waitFor(() => expect(input).toHaveAttribute("aria-disabled", "false"));
    expect(input.textContent?.trim()).toBe("");
    expect(input).toHaveFocus();
  });

  it("does not restore focus over a control chosen during a pending send", async () => {
    let resolveSend!: (result: SendResult) => void;
    const onSend = vi.fn().mockReturnValue(new Promise((resolve) => (resolveSend = resolve)));
    render(<ChatComposer bodyFormat="v2" placeholder="Mensagem..." onSend={onSend} />);
    const input = await paste("", "sent");
    input.focus();
    fireEvent.keyDown(input, { key: "Enter", code: "Enter" });
    await waitFor(() => expect(input).toHaveAttribute("aria-disabled", "true"));

    const other = document.createElement("button");
    document.body.append(other);
    other.focus();
    await act(async () => resolveSend({ status: "sent" }));

    expect(other).toHaveFocus();
    other.remove();
  });

  it("returns focus after clicking send and keeps Shift+Enter as a line break", async () => {
    const onSend = setup();
    const input = await paste("", "linha um");
    input.focus();
    fireEvent.keyDown(input, { key: "Enter", code: "Enter", shiftKey: true });
    expect(onSend).not.toHaveBeenCalled();
    expect(input.querySelector("br")).not.toBeNull();

    await userEvent.click(screen.getByTestId("chat-send-btn"));
    expect(input).toHaveFocus();
    await waitFor(() => expect(onSend).toHaveBeenCalledOnce());
  });
});

async function send(onSend: ReturnType<typeof setup>): Promise<string> {
  await userEvent.click(await screen.findByTestId("chat-send-btn"));
  await waitFor(() => expect(onSend).toHaveBeenCalledOnce());
  return onSend.mock.calls[0][0];
}

describe("ChatComposer clipboard round-trip", () => {
  it("pastes bold+italic HTML through the production editor and renderer", async () => {
    const onSend = setup();
    await paste("<p><strong><em>both</em></strong></p>", "both");

    const stored = await send(onSend);
    const { container } = render(<RichTextRenderer text={stored} bodyFormat="v2" />);

    expect(stored).toBe("***both***");
    expect(container.querySelector("strong > em")?.textContent).toBe("both");
  });

  it("pastes nested lists without losing hierarchy or content", async () => {
    const onSend = setup();
    await paste(
      "<ul><li><p>parent</p><ol><li><p>child</p><ul><li><p>grandchild</p></li></ul></li></ol></li></ul>",
      "parent child grandchild",
    );

    const stored = await send(onSend);
    const { container } = render(<RichTextRenderer text={stored} bodyFormat="v2" />);

    expect(stored).toBe("- parent\n  1. child\n    - grandchild");
    expect(container.querySelector("ul > li > ol > li > ul > li")?.textContent).toBe("grandchild");
  });

  it("strips pasted script HTML before storage and rendering", async () => {
    const onSend = setup();
    await paste("<p>safe<script>alert(1)</script></p>", "safe");

    const stored = await send(onSend);
    const { container } = render(<RichTextRenderer text={stored} bodyFormat="v2" />);

    expect(stored).toBe("safe");
    expect(container.querySelector("script")).toBeNull();
    expect(container.textContent).toBe("safe");
  });
});

describe("ChatComposer list UX", () => {
  it("provides the geometry APIs TipTap paste needs in JSDOM", () => {
    const range = document.createRange();

    expect(range.getClientRects()).toHaveLength(0);
    expect(range.getBoundingClientRect().width).toBe(0);
    expect(document.body.getBoundingClientRect().width).toBe(0);
  });

  it("hides the placeholder when an empty list is created", async () => {
    setup();

    expect(screen.getByText("Mensagem...")).toBeInTheDocument();
    await userEvent.click(await screen.findByTestId("fmt-ul"));

    await waitFor(() => {
      expect(screen.queryByText("Mensagem...")).not.toBeInTheDocument();
      expect(screen.getByTestId("chat-composer-input").querySelector("ul")).not.toBeNull();
    });
  });

  it("Enter inside a list creates another item without sending", async () => {
    const onSend = setup();
    await userEvent.click(await screen.findByTestId("fmt-ul"));
    const input = await paste("", "first");

    fireEvent.keyDown(input, { key: "Enter", code: "Enter" });

    await waitFor(() => expect(input.querySelectorAll("li")).toHaveLength(2));
    expect(onSend).not.toHaveBeenCalled();
  });

  it("Shift+Enter inside a list creates a hard break in the current item", async () => {
    const onSend = setup();
    await userEvent.click(await screen.findByTestId("fmt-ul"));
    const input = await paste("", "first");

    fireEvent.keyDown(input, { key: "Enter", code: "Enter", shiftKey: true });

    await waitFor(() => expect(input.querySelector("li br")).not.toBeNull());
    expect(input.querySelectorAll("li")).toHaveLength(1);
    expect(onSend).not.toHaveBeenCalled();
  });
});

describe("ChatComposer mentions", () => {
  it("autocompletes a channel-scoped user and sends a renderable v3 token", async () => {
    mockFetchMentionCandidates.mockResolvedValue([
      {
        mentionType: "user",
        id: "11111111-1111-1111-1111-111111111111",
        label: "Ana",
      },
    ]);
    const onSend = vi.fn<(body: string) => Promise<SendResult>>().mockResolvedValue({
      status: "sent",
    });
    render(
      <ChatComposer
        channelId="22222222-2222-2222-2222-222222222222"
        bodyFormat="v3"
        placeholder="Mensagem..."
        onSend={onSend}
      />,
    );
    const input = await screen.findByTestId("chat-composer-input");

    input.focus();
    await userEvent.type(input, "@an", { skipClick: true });
    fireEvent.mouseDown(await screen.findByRole("option", { name: /Ana/ }));

    expect(mockFetchMentionCandidates).toHaveBeenLastCalledWith(
      "22222222-2222-2222-2222-222222222222",
      "an",
      expect.any(AbortSignal),
    );
    expect(input.querySelector(".chat-mention")).toHaveTextContent("@Ana");

    const stored = await send(onSend);
    const { container } = render(<RichTextRenderer text={stored} bodyFormat="v3" />);
    expect(container.querySelector(".rtr-mention")).toHaveTextContent("@Ana");
  });

  it("closes autocomplete with Escape", async () => {
    mockFetchMentionCandidates.mockResolvedValue([
      { mentionType: "channel", id: "channel-1", label: "anuncios" },
    ]);
    render(
      <ChatComposer
        channelId="22222222-2222-2222-2222-222222222222"
        bodyFormat="v3"
        placeholder="Mensagem..."
        onSend={vi.fn().mockResolvedValue({ status: "sent" })}
      />,
    );
    const input = await screen.findByTestId("chat-composer-input");
    input.focus();
    await userEvent.type(input, "@a", { skipClick: true });
    expect(await screen.findByRole("option", { name: /anuncios/ })).toBeInTheDocument();

    fireEvent.keyDown(input, { key: "Escape", code: "Escape" });

    await waitFor(() => expect(screen.queryByRole("option", { name: /anuncios/ })).toBeNull());
  });
});

/**
 * The composer's emoji button (issue #496).
 *
 * These go through the real TipTap editor rather than a mock, because what is
 * under test is *where* the emoji lands — at the caret, over a selection —
 * which only the real editor's selection can answer.
 */
describe("ChatComposer emoji picker", () => {
  async function openPicker() {
    await userEvent.click(await screen.findByTestId("toolbar-emoji-btn"));
    const search = await screen.findByRole("searchbox", { name: "Buscar emoji" });
    await waitFor(() => expect(search).toHaveFocus());
  }

  /**
   * Puts the caret inside the paragraph the reader has typed.
   *
   * jsdom has no caret of its own — ArrowLeft moves nothing in a contenteditable
   * — so the selection is set through the DOM Selection API and ProseMirror
   * picks it up from there, exactly as it does from a real browser.
   */
  function select(input: HTMLElement, from: number, to = from) {
    const text = input.querySelector("p")?.firstChild;
    if (!text) throw new Error("composer has no typed text to select in");
    const range = document.createRange();
    range.setStart(text, from);
    range.setEnd(text, to);
    const selection = window.getSelection();
    selection?.removeAllRanges();
    selection?.addRange(range);
    document.dispatchEvent(new Event("selectionchange"));
  }

  /**
   * The editor, focused, ready to be typed into.
   *
   * Focused rather than clicked: ProseMirror answers a mousedown by asking the
   * document what is at those coordinates, and jsdom has no layout to answer
   * with. A real click is covered by the end-to-end spec, in a real browser.
   */
  async function focusedInput() {
    const input = await screen.findByTestId("chat-composer-input");
    input.focus();
    return input;
  }

  async function pick(label: string) {
    await userEvent.click(screen.getByRole("button", { name: label }));
  }

  it("inserts at the caret, in the middle of what is already typed", async () => {
    const onSend = setup();
    const input = await focusedInput();
    await userEvent.keyboard("bomdia");
    select(input, 3);

    await openPicker();
    await pick("rosto risonho");

    expect(await send(onSend)).toBe("bom😀dia");
  });

  it("replaces the selection, exactly as typing a character would", async () => {
    const onSend = setup();
    const input = await focusedInput();
    await userEvent.keyboard("bom dia");
    select(input, 4, 7);

    await openPicker();
    await pick("rosto risonho");

    expect(await send(onSend)).toBe("bom 😀");
  });

  it("takes several emoji without being reopened", async () => {
    const onSend = setup();
    await focusedInput();

    await openPicker();
    await pick("rosto risonho");
    await pick("rosto chorando de rir");

    expect(await send(onSend)).toBe("😀😂");
  });

  // A picker left hanging over a message that has already gone is noise.
  it("closes once the message is on its way", async () => {
    const onSend = setup();
    await focusedInput();
    await openPicker();
    await pick("rosto risonho");
    expect(screen.getByTestId("toolbar-emoji-picker")).toBeInTheDocument();

    await send(onSend);

    await waitFor(() => expect(screen.queryByTestId("toolbar-emoji-picker")).toBeNull());
  });
});
