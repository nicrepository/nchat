import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";

import ConversationSystemMessage from "./ConversationSystemMessage.tsx";
import type { Message } from "./chatTypes";
import { systemMessagePresentation } from "./conversationSystemMessage";

// The wording of a conversation event lives in the client, because the database
// stores facts and never a sentence (issue #527). These assert the sentences,
// the fail-safe behaviour for an event this build does not know, and that a
// name is always text.

const systemMessage = (overrides: Partial<Message> = {}): Message =>
  ({
    id: "msg-1",
    senderId: "user-1",
    senderDisplayName: "Álvaro Neto",
    senderEmail: "",
    kind: "system",
    eventType: "conversation_renamed",
    eventPayload: { oldName: "Projetos", newName: "Projetos Especiais" },
    bodyText: "",
    bodyFormat: 1,
    isRemoved: false,
    status: "active",
    createdAt: "2026-08-24T10:00:00Z",
    updatedAt: "2026-08-24T10:00:00Z",
    ...overrides,
  }) as Message;

describe("systemMessagePresentation", () => {
  it("describes a channel rename", () => {
    expect(systemMessagePresentation(systemMessage(), "channel")?.text).toBe(
      "Álvaro Neto renomeou o canal de Projetos para Projetos Especiais",
    );
  });

  it("describes a group rename", () => {
    expect(
      systemMessagePresentation(
        systemMessage({ eventPayload: { oldName: "Piloto", newName: "Piloto NChat" } }),
        "group",
      )?.text,
    ).toBe("Álvaro Neto renomeou o grupo de Piloto para Piloto NChat");
  });

  it("describes leaving a channel and leaving a group", () => {
    const left = systemMessage({ eventType: "conversation_member_left", eventPayload: {} });
    expect(systemMessagePresentation(left, "channel")?.text).toBe("Álvaro Neto saiu do canal");
    expect(systemMessagePresentation(left, "group")?.text).toBe("Álvaro Neto saiu do grupo");
  });

  // A group that had no title reads "renomeou o grupo para X" rather than
  // "de  para X".
  it("omits the old name when there was none", () => {
    expect(
      systemMessagePresentation(
        systemMessage({ eventPayload: { newName: "Piloto NChat" } }),
        "group",
      )?.text,
    ).toBe("Álvaro Neto renomeou o grupo para Piloto NChat");
  });

  // An identifier is not a name. A deleted or unresolvable actor degrades to a
  // readable word, never to a raw UUID.
  it("never shows a raw id when the actor cannot be resolved", () => {
    const text = systemMessagePresentation(
      systemMessage({ senderDisplayName: "  " }),
      "channel",
    )?.text;
    expect(text).toBe("Alguém renomeou o canal de Projetos para Projetos Especiais");
    expect(text).not.toContain("user-1");
  });

  // Fail-safe: an event from a newer server, a user message, or a rename with
  // nothing to rename to, all render nothing rather than a guess.
  it("returns nothing for anything it cannot describe honestly", () => {
    expect(
      systemMessagePresentation(
        systemMessage({ eventType: "conversation_archived" as Message["eventType"] }),
        "channel",
      ),
    ).toBeNull();
    expect(systemMessagePresentation(systemMessage({ kind: "user" }), "channel")).toBeNull();
    expect(
      systemMessagePresentation(
        systemMessage({ eventPayload: { oldName: "Projetos" } }),
        "channel",
      ),
    ).toBeNull();
  });
});

describe("ConversationSystemMessage", () => {
  it("renders the event as a discrete timeline line, not a message bubble", () => {
    render(<ConversationSystemMessage message={systemMessage()} scope="channel" />);

    const line = screen.getByTestId("chat-system-message");
    expect(line).toHaveTextContent(
      "Álvaro Neto renomeou o canal de Projetos para Projetos Especiais",
    );
    // None of a message's own affordances belong to an event.
    expect(screen.queryByRole("button")).not.toBeInTheDocument();
    expect(screen.queryByRole("img")).not.toBeInTheDocument();
  });

  // Names are data. React escapes them; this is the regression guard that they
  // are never routed through markup.
  it("treats HTML-like names as text", () => {
    const hostile = '<img src=x onerror="alert(1)">& "aspas" <b>';
    render(
      <ConversationSystemMessage
        message={systemMessage({ eventPayload: { oldName: "Projetos", newName: hostile } })}
        scope="channel"
      />,
    );

    const line = screen.getByTestId("chat-system-message");
    expect(line).toHaveTextContent(hostile);
    // The markup never became markup: no element was created, and every angle
    // bracket reached the DOM escaped. The words themselves are of course still
    // present — they are the name — which is exactly the point.
    expect(line.querySelector("img")).toBeNull();
    expect(line.querySelector("b")).toBeNull();
    expect(line.innerHTML).not.toContain("<img");
    expect(line.innerHTML).not.toContain("<b>");
    expect(line.innerHTML).toContain("&lt;img");
  });

  it("renders nothing for an event it cannot describe", () => {
    const { container } = render(
      <ConversationSystemMessage
        message={systemMessage({ eventType: "conversation_archived" as Message["eventType"] })}
        scope="group"
      />,
    );
    expect(container).toBeEmptyDOMElement();
  });
});
