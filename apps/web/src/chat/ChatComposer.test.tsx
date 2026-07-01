import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
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
  render(<ChatComposer placeholder="Mensagem..." onSend={onSend} />);
  return onSend;
}

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
