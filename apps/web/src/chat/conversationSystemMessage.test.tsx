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

  // Issue #685 visual pass: every event carries a fixed icon this build
  // chose from its type, and only the two call events get the "call" tone.
  it("picks an icon per event type and the call tone only for calls", () => {
    expect(systemMessagePresentation(systemMessage(), "channel")).toMatchObject({
      icon: "edit",
      tone: "neutral",
    });
    expect(
      systemMessagePresentation(
        systemMessage({ eventType: "conversation_member_left", eventPayload: {} }),
        "channel",
      ),
    ).toMatchObject({ icon: "logout", tone: "neutral" });
    expect(
      systemMessagePresentation(
        systemMessage({ eventType: "conversation_created", eventPayload: {} }),
        "channel",
      ),
    ).toMatchObject({ icon: "forum", tone: "neutral" });
    expect(
      systemMessagePresentation(
        systemMessage({ eventType: "conversation_archived", eventPayload: {} }),
        "channel",
      ),
    ).toMatchObject({ icon: "archive", tone: "neutral" });
    expect(
      systemMessagePresentation(
        systemMessage({
          eventType: "conversation_member_added",
          eventPayload: { targetUsers: [{ userId: "user-2", displayName: "Bruno" }] },
        }),
        "channel",
      ),
    ).toMatchObject({ icon: "person_add", tone: "neutral" });
    expect(
      systemMessagePresentation(
        systemMessage({
          eventType: "conversation_member_removed",
          eventPayload: { targetUsers: [{ userId: "user-2", displayName: "Bruno" }] },
        }),
        "channel",
      ),
    ).toMatchObject({ icon: "person_remove", tone: "neutral" });
    expect(
      systemMessagePresentation(
        systemMessage({
          eventType: "call_started",
          eventPayload: { callId: "call-1", callType: "video" },
        }),
        "channel",
      ),
    ).toMatchObject({ icon: "call", tone: "call" });
    expect(
      systemMessagePresentation(
        systemMessage({
          eventType: "call_ended",
          eventPayload: { callId: "call-1", callType: "video", callDurationSeconds: 60 },
        }),
        "channel",
      ),
    ).toMatchObject({ icon: "call_end", tone: "call" });
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
        systemMessage({ eventType: "something_from_the_future" as Message["eventType"] }),
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

  // A member change with no targets is the same "nothing honest to say" case:
  // there is no one to name.
  it("returns nothing for a member change with no targets", () => {
    expect(
      systemMessagePresentation(
        systemMessage({ eventType: "conversation_member_added", eventPayload: {} }),
        "group",
      ),
    ).toBeNull();
  });

  describe("conversation created and archived", () => {
    it("describes creation from a third party and from the viewer", () => {
      const created = systemMessage({ eventType: "conversation_created", eventPayload: {} });
      expect(systemMessagePresentation(created, "channel")?.text).toBe("Álvaro Neto criou o canal");
      expect(systemMessagePresentation(created, "channel", "user-1")?.text).toBe(
        "Você criou o canal",
      );
      expect(systemMessagePresentation(created, "group")?.text).toBe("Álvaro Neto criou o grupo");
    });

    it("describes archiving from a third party and from the viewer", () => {
      const archived = systemMessage({ eventType: "conversation_archived", eventPayload: {} });
      expect(systemMessagePresentation(archived, "channel")?.text).toBe(
        "Álvaro Neto arquivou o canal",
      );
      expect(systemMessagePresentation(archived, "channel", "user-1")?.text).toBe(
        "Você arquivou o canal",
      );
    });
  });

  describe("member added and removed", () => {
    const oneTarget = { targetUsers: [{ userId: "user-2", displayName: "Bruno Lima" }] };
    const twoTargets = {
      targetUsers: [
        { userId: "user-2", displayName: "Bruno Lima" },
        { userId: "user-3", displayName: "Carla Dias" },
      ],
    };
    const fourTargets = {
      targetUsers: [
        { userId: "user-2", displayName: "Bruno Lima" },
        { userId: "user-3", displayName: "Carla Dias" },
        { userId: "user-4", displayName: "Duda Reis" },
        { userId: "user-5", displayName: "Elis Souza" },
      ],
    };

    it("names a single target from a third party", () => {
      const added = systemMessage({
        eventType: "conversation_member_added",
        eventPayload: oneTarget,
      });
      expect(systemMessagePresentation(added, "group")?.text).toBe(
        "Álvaro Neto adicionou Bruno Lima ao grupo",
      );
    });

    it("joins two targets with 'e'", () => {
      const added = systemMessage({
        eventType: "conversation_member_added",
        eventPayload: twoTargets,
      });
      expect(systemMessagePresentation(added, "group")?.text).toBe(
        "Álvaro Neto adicionou Bruno Lima e Carla Dias ao grupo",
      );
    });

    it("truncates a bulk add to two names and a count", () => {
      const added = systemMessage({
        eventType: "conversation_member_added",
        eventPayload: fourTargets,
      });
      expect(systemMessagePresentation(added, "group")?.text).toBe(
        "Álvaro Neto adicionou Bruno Lima, Carla Dias e mais 2 pessoas ao grupo",
      );
    });

    it("says 'Você' when the viewer is the one who added", () => {
      const added = systemMessage({
        eventType: "conversation_member_added",
        eventPayload: twoTargets,
      });
      expect(systemMessagePresentation(added, "group", "user-1")?.text).toBe(
        "Você adicionou Bruno Lima e Carla Dias ao grupo",
      );
    });

    it("says 'você' when the viewer is the sole target, without naming them", () => {
      const added = systemMessage({
        eventType: "conversation_member_added",
        eventPayload: oneTarget,
      });
      expect(systemMessagePresentation(added, "group", "user-2")?.text).toBe(
        "Álvaro Neto adicionou você ao grupo",
      );
    });

    it("says 'você e mais N pessoas' when the viewer is one of several targets", () => {
      const added = systemMessage({
        eventType: "conversation_member_added",
        eventPayload: fourTargets,
      });
      expect(systemMessagePresentation(added, "group", "user-3")?.text).toBe(
        "Álvaro Neto adicionou você e mais 3 pessoas ao grupo",
      );
    });

    it("uses 'removeu ... do' for removal, symmetrically", () => {
      const removed = systemMessage({
        eventType: "conversation_member_removed",
        eventPayload: oneTarget,
      });
      expect(systemMessagePresentation(removed, "channel")?.text).toBe(
        "Álvaro Neto removeu Bruno Lima do canal",
      );
      expect(systemMessagePresentation(removed, "channel", "user-2")?.text).toBe(
        "Álvaro Neto removeu você do canal",
      );
    });

    it("falls back to 'Alguém' for a target with no resolvable name", () => {
      const added = systemMessage({
        eventType: "conversation_member_added",
        eventPayload: { targetUsers: [{ userId: "user-9" }] },
      });
      expect(systemMessagePresentation(added, "group")?.text).toBe(
        "Álvaro Neto adicionou Alguém ao grupo",
      );
    });
  });

  describe("calls", () => {
    it("describes a started call, third-party and viewer", () => {
      const started = systemMessage({
        eventType: "call_started",
        eventPayload: { callId: "call-1", callType: "video" },
      });
      expect(systemMessagePresentation(started, "channel")?.text).toBe(
        "Álvaro Neto iniciou uma chamada de vídeo",
      );
      expect(systemMessagePresentation(started, "channel", "user-1")?.text).toBe(
        "Você iniciou uma chamada de vídeo",
      );
    });

    it("describes an audio call", () => {
      const started = systemMessage({
        eventType: "call_started",
        eventPayload: { callId: "call-1", callType: "audio" },
      });
      expect(systemMessagePresentation(started, "channel")?.text).toBe(
        "Álvaro Neto iniciou uma chamada de voz",
      );
    });

    it("describes an ended call with its duration", () => {
      const ended = systemMessage({
        eventType: "call_ended",
        eventPayload: { callId: "call-1", callType: "video", callDurationSeconds: 725 },
      });
      expect(systemMessagePresentation(ended, "channel")?.text).toBe(
        "Álvaro Neto encerrou a chamada de vídeo (12 min 5 s)",
      );
    });

    it("formats a duration under a minute without the minutes part", () => {
      const ended = systemMessage({
        eventType: "call_ended",
        eventPayload: { callId: "call-1", callType: "audio", callDurationSeconds: 45 },
      });
      expect(systemMessagePresentation(ended, "channel")?.text).toBe(
        "Álvaro Neto encerrou a chamada de voz (45 s)",
      );
    });

    it("formats a duration with no leftover seconds", () => {
      const ended = systemMessage({
        eventType: "call_ended",
        eventPayload: { callId: "call-1", callType: "video", callDurationSeconds: 120 },
      });
      expect(systemMessagePresentation(ended, "channel")?.text).toBe(
        "Álvaro Neto encerrou a chamada de vídeo (2 min)",
      );
    });
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

  // A neutral event keeps the plain pill; only the two call events get the
  // design's tinted, higher-emphasis treatment (issue #685 visual pass,
  // matching prototype/claude-design-v1/nic-chat/dm.html's .syscall pill).
  it("gives only call events the tinted pill", () => {
    const { container: renamed } = render(
      <ConversationSystemMessage message={systemMessage()} scope="channel" />,
    );
    expect(renamed.querySelector(".chat-system-message--call")).toBeNull();

    const { container: started } = render(
      <ConversationSystemMessage
        message={systemMessage({
          eventType: "call_started",
          eventPayload: { callId: "call-1", callType: "video" },
        })}
        scope="channel"
      />,
    );
    expect(started.querySelector(".chat-system-message--call")).not.toBeNull();
  });

  // The icon is a fixed ligature this build chose from the event type, never
  // server data — a text node inside an aria-hidden span, not markup.
  it("renders a decorative icon matching the event type", () => {
    render(
      <ConversationSystemMessage
        message={systemMessage({
          eventType: "call_started",
          eventPayload: { callId: "call-1", callType: "video" },
        })}
        scope="channel"
      />,
    );
    const icon = screen
      .getByTestId("chat-system-message")
      .querySelector(".chat-system-message__icon");
    expect(icon).not.toBeNull();
    expect(icon).toHaveAttribute("aria-hidden", "true");
    expect(icon).toHaveTextContent("call");
  });

  it("renders nothing for an event it cannot describe", () => {
    const { container } = render(
      <ConversationSystemMessage
        message={systemMessage({
          eventType: "something_from_the_future" as Message["eventType"],
        })}
        scope="group"
      />,
    );
    expect(container).toBeEmptyDOMElement();
  });

  it("passes viewerId through to say 'Você'", () => {
    render(
      <ConversationSystemMessage
        message={systemMessage({
          eventType: "conversation_created",
          eventPayload: {},
          senderId: "user-1",
        })}
        scope="channel"
        viewerId="user-1"
      />,
    );
    expect(screen.getByTestId("chat-system-message")).toHaveTextContent("Você criou o canal");
  });
});
