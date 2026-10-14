import { renderHook } from "@testing-library/react";
import { describe, expect, it } from "vitest";

import type { Message } from "../chatTypes";
import type { WSMessageUpdatedEvent, WSReactionUpdatedEvent } from "../useChatWebSocket";
import { useCatchUpJournal } from "./catchUpJournal";
import { reducer } from "./reducer";
import { initialState, type Action, type MessagesState } from "./types";

/**
 * Issue #947 / CQ947-03..05, 08: a catch-up read applied under the journal of
 * server facts, with this reader's intents redrawn on top and nothing outside
 * the timeline touched.
 */

function message(n: number, overrides: Partial<Message> = {}): Message {
  const at = new Date(Date.UTC(2026, 9, 1, 12, 0, n)).toISOString();
  return {
    id: `m-${n}`,
    senderId: "user-2",
    senderDisplayName: "Bea",
    senderEmail: "",
    kind: "user",
    bodyText: `mensagem ${n}`,
    bodyFormat: "v3",
    isRemoved: false,
    status: "active",
    createdAt: at,
    updatedAt: at,
    isEdited: false,
    editCount: 0,
    reactions: [],
    isFavorited: false,
    isForwarded: false,
    ...overrides,
  } as Message;
}

const later = "2026-10-05T00:00:00.000Z";
const latest = "2026-10-06T00:00:00.000Z";

function edited(n: number, body: string, editCount: number, at = later): Message {
  return message(n, {
    bodyText: body,
    isEdited: true,
    editCount,
    editedAt: at,
    updatedAt: at,
    linkSafetyState: "safe",
  });
}

function thumbs(users: string[], mine: boolean): Message["reactions"] {
  return [
    {
      emoji: "👍",
      count: users.length,
      reactedByMe: mine,
      users: users.map((userId) => ({ userId, displayName: userId })),
    },
  ];
}

function reactionEvent(
  messageId: string,
  emoji: string,
  users: string[],
  actor: string,
  added: boolean,
): Action {
  const event: WSReactionUpdatedEvent = {
    type: "reaction.updated",
    target_type: "channel",
    target_id: "ch-1",
    message_id: messageId,
    reaction: {
      message_id: messageId,
      actor_user_id: actor,
      emoji,
      added,
      reactions: users.length
        ? [{ emoji, count: users.length, users: users.map((user_id) => ({ user_id })) }]
        : [],
    },
  };
  return { type: "reaction_updated", event, actorIsMe: actor === "me" };
}

function remoteEdit(messageId: string, body: string, editCount: number, at: string): Action {
  const event: WSMessageUpdatedEvent = {
    type: "message.updated",
    target_type: "channel",
    target_id: "ch-1",
    message_update: {
      message_id: messageId,
      body,
      body_format: "v3",
      edited_at: at,
      edit_count: editCount,
      is_edited: true,
      updated_at: at,
    },
  };
  return { type: "message_updated", event };
}

function loaded(messages: Message[]): MessagesState {
  return reducer(initialState, { type: "loaded", page: { messages, nextCursor: "c0" } });
}

function run(state: MessagesState, ...actions: Action[]): MessagesState {
  return actions.reduce(reducer, state);
}

function caughtUp(read: Message[], journal: Action[] = [], replace = false): Action {
  return { type: "caught_up", page: { messages: read, nextCursor: "c9" }, replace, journal };
}

const editM2 = (body: string): Action => ({
  type: "edit_optimistic",
  messageId: "m-2",
  body,
  bodyFormat: "v3",
  editedAt: "2026-10-04T23:59:59.000Z",
});

const byId = (state: MessagesState, id: string) => state.messages.find((m) => m.id === id)!;

describe("caught_up rebases the read under the journal of server facts", () => {
  it("keeps a reaction delivered while the read was in flight and a missed edit together", () => {
    const state = run(
      loaded([message(1), message(2)]),
      caughtUp(
        [message(1), edited(2, "editada", 1)],
        [reactionEvent("m-2", "👍", ["u3"], "u3", true)],
      ),
    );

    expect(byId(state, "m-2")).toMatchObject({ bodyText: "editada", editCount: 1 });
    expect(byId(state, "m-2").reactions).toMatchObject([{ emoji: "👍", count: 1 }]);
  });

  it("replays a remote edit newer than the read", () => {
    const state = run(
      loaded([message(1), message(2)]),
      caughtUp([message(1), edited(2, "v1", 1)], [remoteEdit("m-2", "v2", 2, latest)]),
    );

    expect(byId(state, "m-2")).toMatchObject({ bodyText: "v2", editCount: 2 });
  });

  it("keeps a message delivered or sent while the read was in flight", () => {
    const fresh = message(9, { senderId: "me", bodyText: "enviada" });
    const state = run(
      loaded([message(1)]),
      caughtUp([message(1)], [{ type: "ws_received", message: fresh }]),
    );

    expect(state.messages.map(({ id }) => id)).toEqual(["m-1", "m-9"]);
  });

  it("keeps the reader's own message still being scanned when a read replaces the timeline", () => {
    const pending = message(5, { status: "pending_link_scan", senderId: "me" });
    const state = run(loaded([message(1), pending]), caughtUp([message(8), message(9)], [], true));

    expect(state.messages.map(({ id }) => id)).toEqual(["m-5", "m-8", "m-9"]);
    expect(state.nextCursor).toBe("c9");
    expect(state.lastMutation).toBe("initial");
  });

  it("clears the owed reconciliation, and nothing else clears it", () => {
    const failed = run(loaded([message(1)]), { type: "catch_up_failed", error: "falhou" });
    const afterEvent = run(failed, reactionEvent("m-1", "👍", ["u3"], "u3", true), {
      type: "ws_subscription_ready",
    });

    expect(afterEvent.syncError).toBe("falhou");
    expect(run(afterEvent, caughtUp([message(1)])).syncError).toBeNull();
  });
});

describe("CQ947-03 — an optimistic edit is redrawn by version, never re-counted", () => {
  it("draws an optimistic edit the read does not have yet, at the version it claims", () => {
    const state = run(
      loaded([message(1), message(2)]),
      editM2("rascunho"),
      caughtUp([message(1), message(2)]),
    );

    expect(byId(state, "m-2")).toMatchObject({ bodyText: "rascunho", editCount: 1 });
  });

  it("takes the read when it already carries the edit, and accepts the confirmation", () => {
    const persisted = edited(2, "rascunho", 1);
    const state = run(
      loaded([message(1), message(2)]),
      editM2("rascunho"),
      caughtUp([message(1), persisted]),
      { type: "edit_confirmed", message: persisted },
    );

    expect(byId(state, "m-2")).toMatchObject({
      editCount: 1,
      editedAt: later,
      linkSafetyState: "safe",
    });
    expect(state.pendingEdits.size).toBe(0);
  });

  it("does not redraw an edit confirmed before the read landed", () => {
    const persisted = edited(2, "rascunho", 1);
    const state = run(
      loaded([message(1), message(2)]),
      editM2("rascunho"),
      { type: "edit_confirmed", message: persisted },
      caughtUp([message(1), message(2)], [{ type: "edit_confirmed", message: persisted }]),
    );

    expect(byId(state, "m-2")).toMatchObject({
      bodyText: "rascunho",
      editCount: 1,
      editedAt: later,
    });
    expect(state.pendingEdits.size).toBe(0);
  });

  it("does not redraw an edit the server refused", () => {
    const original = message(2);
    const state = run(
      loaded([message(1), original]),
      editM2("recusada"),
      { type: "edit_revert", message: original, optimisticEditedAt: "2026-10-04T23:59:59.000Z" },
      caughtUp([message(1), original]),
    );

    expect(byId(state, "m-2")).toMatchObject({ bodyText: "mensagem 2", editCount: 0 });
    expect(state.pendingEdits.size).toBe(0);
  });

  it("keeps the second of two quick edits pending after the first is confirmed", () => {
    const second: Action = { ...editM2("segunda"), editedAt: "2026-10-05T00:00:01.000Z" } as Action;
    const state = run(
      loaded([message(1), message(2)]),
      editM2("primeira"),
      second,
      { type: "edit_confirmed", message: edited(2, "primeira", 1) },
      caughtUp([message(1), edited(2, "primeira", 1)]),
    );

    expect(byId(state, "m-2")).toMatchObject({ bodyText: "segunda", editCount: 2 });
    const confirmed = run(state, {
      type: "edit_confirmed",
      message: edited(2, "segunda", 2, latest),
    });
    expect(byId(confirmed, "m-2")).toMatchObject({ bodyText: "segunda", editCount: 2 });
    expect(confirmed.pendingEdits.size).toBe(0);
  });

  it("yields to a remote edit of the same version or later", () => {
    const state = run(
      loaded([message(1), message(2)]),
      editM2("minha"),
      caughtUp([message(1), edited(2, "de outra pessoa", 1)]),
    );

    expect(byId(state, "m-2")).toMatchObject({ bodyText: "de outra pessoa", editCount: 1 });
  });

  it("does not draw an edit over a message deleted meanwhile", () => {
    const tombstone = message(2, {
      bodyText: "",
      isRemoved: true,
      status: "deleted",
      updatedAt: later,
    });
    const state = run(
      loaded([message(1), message(2)]),
      editM2("tarde"),
      caughtUp([message(1), tombstone]),
    );

    expect(byId(state, "m-2")).toMatchObject({ isRemoved: true, bodyText: "" });
  });

  it("ignores a late confirmation older than what the read says", () => {
    const state = run(
      loaded([message(1), message(2)]),
      caughtUp([message(1), edited(2, "terceira", 3, latest)]),
      { type: "edit_confirmed", message: edited(2, "primeira", 1) },
    );

    expect(byId(state, "m-2")).toMatchObject({ bodyText: "terceira", editCount: 3 });
  });
});

describe("CQ947-04 — a reaction intent is a desired result, redrawn over the read", () => {
  it("leaves no intent once a removal is confirmed and the read agrees", () => {
    const state = run(
      loaded([message(1), message(2, { reactions: thumbs(["me"], true) })]),
      { type: "reaction_optimistic", messageId: "m-2", emoji: "👍" },
      reactionEvent("m-2", "👍", [], "me", false),
      caughtUp([message(1), message(2)], [reactionEvent("m-2", "👍", [], "me", false)]),
    );

    expect(byId(state, "m-2").reactions).toEqual([]);
    expect(state.pendingReactions.size).toBe(0);
  });

  it("draws nothing for an addition the server refused", () => {
    const state = run(
      loaded([message(1), message(2)]),
      { type: "reaction_optimistic", messageId: "m-2", emoji: "👍" },
      { type: "reaction_revert", messageId: "m-2", emoji: "👍", error: "falhou" },
      caughtUp([message(1), message(2)]),
    );

    expect(byId(state, "m-2").reactions).toEqual([]);
    expect(state.pendingReactions.size).toBe(0);
  });

  it("keeps the reaction when a removal was refused", () => {
    const mine = thumbs(["me"], true);
    const state = run(
      loaded([message(1), message(2, { reactions: mine })]),
      { type: "reaction_optimistic", messageId: "m-2", emoji: "👍" },
      { type: "reaction_revert", messageId: "m-2", emoji: "👍", error: "falhou" },
      caughtUp([message(1), message(2, { reactions: mine })]),
    );

    expect(byId(state, "m-2").reactions).toMatchObject([{ emoji: "👍", reactedByMe: true }]);
  });

  it("draws a pending removal over a read that still has the reaction", () => {
    const mine = thumbs(["me", "u3"], true);
    const state = run(
      loaded([message(1), message(2, { reactions: mine })]),
      { type: "reaction_optimistic", messageId: "m-2", emoji: "👍" },
      caughtUp([message(1), message(2, { reactions: mine })]),
    );

    expect(byId(state, "m-2").reactions).toMatchObject([
      { emoji: "👍", count: 1, reactedByMe: false },
    ]);
    expect(state.pendingReactions.get("m-2")?.intents.get("👍")).toBe("removed");
  });

  it("combines another person's reaction during the read with this reader's pending one", () => {
    const state = run(
      loaded([message(1), message(2)]),
      { type: "reaction_optimistic", messageId: "m-2", emoji: "❤️" },
      reactionEvent("m-2", "👍", ["u3"], "u3", true),
      caughtUp([message(1), message(2)], [reactionEvent("m-2", "👍", ["u3"], "u3", true)]),
    );

    const emojis = byId(state, "m-2").reactions.map(({ emoji, count }) => `${emoji}${count}`);
    expect(emojis.sort()).toEqual(["❤️1", "👍1"]);
  });
});

describe("reactions about messages the read did not carry", () => {
  it("applies a reaction to a message delivered while the read was in flight", () => {
    const fresh = message(9);
    const delivered = run(loaded([message(1)]), { type: "ws_received", message: fresh });
    const state = run(
      delivered,
      caughtUp(
        [message(1)],
        [{ type: "ws_received", message: fresh }, reactionEvent("m-9", "👍", ["u3"], "u3", true)],
      ),
    );

    expect(byId(state, "m-9").reactions).toMatchObject([{ emoji: "👍", count: 1 }]);
  });

  it("keeps the intent on the reader's own message still being scanned, which no read returns", () => {
    const pending = message(5, { status: "pending_link_scan", senderId: "me" });
    const state = run(
      loaded([message(1), pending]),
      { type: "reaction_optimistic", messageId: "m-5", emoji: "👍" },
      reactionEvent("m-5", "❤️", ["u3"], "u3", true),
      caughtUp([message(1)], [reactionEvent("m-5", "❤️", ["u3"], "u3", true)]),
    );

    const emojis = byId(state, "m-5").reactions.map(({ emoji }) => emoji);
    expect(emojis.sort()).toEqual(["❤️", "👍"]);
    expect(state.pendingReactions.get("m-5")?.intents.get("👍")).toBe("added");
  });

  it("leaves an intent alone when nothing in the read or the journal spoke about its message", () => {
    const pending = message(5, { status: "pending_link_scan", senderId: "me" });
    const before = run(loaded([message(1), pending]), {
      type: "reaction_optimistic",
      messageId: "m-5",
      emoji: "👍",
    });

    const state = run(before, caughtUp([message(1)]));

    expect(state.pendingReactions.get("m-5")).toEqual(before.pendingReactions.get("m-5"));
    expect(byId(state, "m-5").reactions).toEqual(byId(before, "m-5").reactions);
  });

  it("ignores a reaction about a message the timeline does not hold", () => {
    const before = loaded([message(1)]);

    const state = run(
      before,
      caughtUp([message(1)], [reactionEvent("m-404", "👍", ["u3"], "u3", true)]),
    );

    expect(state.messages).toEqual(before.messages);
  });
});

describe("CQ947-05 — the rebase rewrites the timeline and nothing the composer owns", () => {
  it("leaves the reply, the send in flight and its error exactly as they are", () => {
    const sentA = message(9, { senderId: "me" });
    const before = run(
      loaded([message(1), message(2)]),
      { type: "reply_set", message: message(1) },
      { type: "sending" },
      { type: "send_error", error: "anterior" },
      { type: "sending" },
    );

    const state = run(
      before,
      caughtUp([message(1), message(2)], [{ type: "ws_received", message: sentA }]),
    );

    expect(state.replyTo?.id).toBe("m-1");
    expect(state.sending).toBe(true);
    expect(state.sendError).toBe(before.sendError);
    expect(state.messages.map(({ id }) => id)).toContain("m-9");
  });
});

describe("CQ947-08 — the batch means exactly what one snapshot after another meant", () => {
  function oneByOne(state: MessagesState, read: Message[]): Message[] {
    return read.reduce(
      (current, snapshot) =>
        reducer(current, { type: "message_snapshot", message: snapshot, insertIfMissing: true }),
      state,
    ).messages;
  }

  const cases: Array<[string, Message[], Message[]]> = [
    ["edits in place", [message(1), message(2)], [edited(1, "nova", 1), edited(2, "outra", 1)]],
    ["duplicates in the read", [message(1)], [edited(1, "a", 1), edited(1, "b", 2, latest)]],
    ["inserts out of order", [message(2), message(5)], [message(7), message(1), message(4)]],
    [
      "a tombstone stays a tombstone",
      [message(1, { isRemoved: true, status: "deleted", bodyText: "" })],
      [message(1)],
    ],
    [
      "a pending message is promoted in place",
      [message(1, { status: "pending_link_scan" }), message(2)],
      [message(1, { updatedAt: later })],
    ],
  ];

  it.each(cases)("%s", (_name, held, read) => {
    const state = loaded(held);
    const batch = reducer(state, caughtUp(read)).messages;

    expect(batch).toEqual(oneByOne(state, read));
  });

  // The one place the batch is stricter than one snapshot after another: one
  // by one, a quoting message copied *after* the deletion of what it quotes
  // brought the deleted text back. The batch never does, whatever the order of
  // the read — it means what one by one meant with the withdrawal applied last.
  it("blanks the quote of a deleted message whatever the order of the read", () => {
    const quoting = message(3, { quoted: { id: "m-1", bodyText: "citada" } as Message["quoted"] });
    const deletion = message(1, { isRemoved: true, status: "deleted", updatedAt: later });
    const state = loaded([message(1), message(2), quoting]);

    const batch = reducer(state, caughtUp([deletion, quoting])).messages;

    expect(batch).toEqual(oneByOne(state, [quoting, deletion]));
    expect(batch.find(({ id }) => id === "m-3")?.quoted).toMatchObject({
      isRemoved: true,
      bodyText: "",
    });
  });
});

describe("CQ947-R2-01 — a refusal delivered during the read survives the rebase", () => {
  const pending = message(5, { status: "pending_link_scan", senderId: "me" });
  const refused: Action = { type: "message_blocked", messageId: "m-5", reason: "malicious_link" };

  it.each([false, true])(
    "keeps the refused message out of a stale read (replace: %s)",
    (replace) => {
      const live = run(loaded([message(1), pending]), refused, { type: "sending" });

      const state = run(live, caughtUp([message(1), pending], [refused], replace));

      expect(state.messages.map(({ id }) => id)).toEqual(["m-1"]);
      expect(state.sending).toBe(true);
      expect(state.sendError).toBeNull();
    },
  );

  it("does not withdraw a message the read has already published", () => {
    const published = message(5, { senderId: "me", updatedAt: later });

    const state = run(loaded([message(1), pending]), caughtUp([message(1), published], [refused]));

    expect(byId(state, "m-5").status).toBe("active");
  });
});

describe("CQ947-R2-02 — a reply whose parent the read finds deleted is dropped, alone", () => {
  const deleted = message(2, {
    isRemoved: true,
    status: "deleted",
    bodyText: "",
    updatedAt: later,
  });

  function replying(to: Message): MessagesState {
    return run(
      loaded([message(1), message(2), message(3)]),
      { type: "reply_set", message: to },
      { type: "send_error", error: "anterior" },
      { type: "sending" },
    );
  }

  it.each([false, true])("drops it and keeps the send in flight (replace: %s)", (replace) => {
    const before = replying(message(2));

    const state = run(before, caughtUp([message(1), deleted, message(3)], [], replace));

    expect(byId(state, "m-2").isRemoved).toBe(true);
    expect(state.replyTo).toBeNull();
    expect(state.sending).toBe(true);
    expect(state.sendError).toBe(before.sendError);
  });

  it("keeps it when another message was deleted", () => {
    const state = run(replying(message(1)), caughtUp([message(1), deleted, message(3)]));

    expect(state.replyTo?.id).toBe("m-1");
  });

  // Absence from a read is not evidence of a deletion.
  it("keeps it when the read does not carry its parent", () => {
    const state = run(replying(message(2)), caughtUp([message(3)]));

    expect(state.replyTo?.id).toBe("m-2");
  });

  it("means what one snapshot after another meant", () => {
    const before = replying(message(2));
    const oneByOne = run(before, {
      type: "message_snapshot",
      message: deleted,
      insertIfMissing: true,
    });

    expect(run(before, caughtUp([deleted])).replyTo).toBe(oneByOne.replyTo);
  });
});

/**
 * Section 10 of the second review: every server fact delivered while a read is
 * in flight, followed by a read taken before it, must leave the conversation
 * exactly as the read followed by the fact would. Driven through the real
 * journal, so what is (not) recorded is part of what is tested.
 */
describe("a fact during the read, then a stale read, means the read then the fact", () => {
  function rebased(before: MessagesState, fact: Action, read: Message[], replace: boolean) {
    let live = before;
    const { result } = renderHook(() =>
      useCatchUpJournal((action) => {
        live = reducer(live, action);
      }),
    );
    const open = result.current.open();
    result.current.dispatch(fact);
    result.current.close(open);
    result.current.dispatch(caughtUp(read, open.entries, replace));
    return live;
  }

  const project = (state: MessagesState) => ({
    messages: state.messages.map((m) => ({
      id: m.id,
      status: m.status,
      isRemoved: m.isRemoved,
      bodyText: m.bodyText,
      editCount: m.editCount,
      reactions: m.reactions.map(({ emoji, count, reactedByMe }) => ({
        emoji,
        count,
        reactedByMe,
      })),
      linkSafetyState: m.linkSafetyState,
      isFavorited: m.isFavorited,
    })),
    replyTo: state.replyTo?.id ?? null,
    sending: state.sending,
    sendError: state.sendError,
  });

  const pending = message(5, { status: "pending_link_scan", senderId: "me" });
  const held = [message(1), message(2), message(3), pending];
  const deletedM2 = message(2, {
    isRemoved: true,
    status: "deleted",
    bodyText: "",
    updatedAt: later,
  });
  const fresh = message(9, { senderId: "me", bodyText: "nova" });
  const deletion: Action = {
    type: "message_updated",
    event: {
      type: "message.updated",
      target_type: "channel",
      target_id: "ch-1",
      message_update: {
        message_id: "m-2",
        body: "",
        body_format: "v3",
        edited_at: later,
        edit_count: 0,
        is_edited: false,
        status: "deleted",
        is_removed: true,
        deleted_at: later,
        updated_at: later,
      },
    },
  };

  const facts: Array<[string, Action[], Action]> = [
    [
      "message_blocked",
      [],
      { type: "message_blocked", messageId: "m-5", reason: "malicious_link" },
    ],
    ["message_updated (deletion)", [], deletion],
    ["message_updated (edit)", [], remoteEdit("m-2", "editada", 1, later)],
    [
      "message_snapshot (deletion)",
      [],
      { type: "message_snapshot", message: deletedM2, insertIfMissing: false },
    ],
    [
      "message_snapshot (new)",
      [],
      { type: "message_snapshot", message: fresh, insertIfMissing: true },
    ],
    ["ws_received", [], { type: "ws_received", message: fresh }],
    [
      "sent",
      [{ type: "reply_set", message: message(2) }, { type: "sending" }],
      { type: "sent", message: fresh, parentMessageId: "m-2" },
    ],
    [
      "edit_confirmed",
      [editM2("editada")],
      { type: "edit_confirmed", message: edited(2, "editada", 1) },
    ],
    ["reaction_updated", [], reactionEvent("m-2", "👍", ["u3"], "u3", true)],
    [
      "reaction_snapshot",
      [],
      { type: "reaction_snapshot", messageId: "m-2", reactions: thumbs(["u3"], false) },
    ],
    [
      "link_safety_changed",
      [],
      { type: "link_safety_changed", messageId: "m-2", state: "malicious", updatedAt: later },
    ],
    ["favorite_set", [], { type: "favorite_set", messageId: "m-2", isFavorited: true }],
  ];

  it.each(facts)("%s", (_name, setup, fact) => {
    const before = run(loaded(held), ...setup);
    const stale = [message(1), message(2), message(3), pending];

    expect(project(rebased(before, fact, stale, false))).toEqual(
      project(run(before, caughtUp(stale), fact)),
    );
  });

  it("does not let an older single-message read undo a newer listing copy", () => {
    const older: Action = {
      type: "message_snapshot",
      message: edited(2, "v1", 1),
      insertIfMissing: true,
    };

    const state = rebased(loaded(held), older, [message(1), edited(2, "v2", 2, latest)], false);

    expect(byId(state, "m-2")).toMatchObject({ bodyText: "v2", editCount: 2 });
  });

  // A gap too long to stitch: the read holds none of what is drawn.
  it.each(facts)("%s, when the read replaces the timeline", (_name, setup, fact) => {
    const before = run(loaded(held), ...setup);
    const stale = [message(7), message(8)];

    expect(project(rebased(before, fact, stale, true))).toEqual(
      project(run(before, caughtUp(stale, [], true), fact)),
    );
  });
});
