import { describe, expect, it } from "vitest";

import { formatReactionAuthors, reactionAccessibleDescription } from "./reactionAuthors";
import type { MessageReaction, ReactionUser } from "./chatTypes";

const me = "me-123";

function reaction(over: Partial<MessageReaction> = {}): MessageReaction {
  return { emoji: "👍", count: 1, reactedByMe: false, users: [], ...over };
}

function user(userId: string, displayName: string): ReactionUser {
  return { userId, displayName };
}

describe("formatReactionAuthors", () => {
  it("names one person", () => {
    expect(
      formatReactionAuthors(reaction({ count: 1, users: [user("u1", "Álvaro Neto")] }), me),
    ).toBe("Álvaro Neto");
  });

  it("names two people", () => {
    expect(
      formatReactionAuthors(
        reaction({ count: 2, users: [user("u1", "Álvaro Neto"), user("u2", "Caio Almeida")] }),
        me,
      ),
    ).toBe("Álvaro Neto e Caio Almeida");
  });

  it("summarises the rest once there are more than it spells out", () => {
    expect(
      formatReactionAuthors(
        reaction({ count: 5, users: [user("u1", "Álvaro Neto"), user("u2", "Caio Almeida")] }),
        me,
      ),
    ).toBe("Álvaro Neto, Caio Almeida e mais 3");
  });

  it("calls the reader Você when they are the only one", () => {
    expect(
      formatReactionAuthors(reaction({ count: 1, reactedByMe: true, users: [user(me, "Eu")] }), me),
    ).toBe("Você");
  });

  it("puts the reader first, by that name, next to someone else", () => {
    expect(
      formatReactionAuthors(
        reaction({
          count: 2,
          reactedByMe: true,
          users: [user(me, "Eu"), user("u2", "Caio Almeida")],
        }),
        me,
      ),
    ).toBe("Você e Caio Almeida");
  });

  it("counts the reader as one of the names it spells out", () => {
    expect(
      formatReactionAuthors(
        reaction({
          count: 6,
          reactedByMe: true,
          users: [user(me, "Eu"), user("u2", "Caio Almeida"), user("u3", "Bruna Dias")],
        }),
        me,
      ),
    ).toBe("Você, Caio Almeida e mais 4");
  });

  // A retried toggle or a re-delivered event can leave the same person in the
  // list twice, and a tooltip that says a name twice is a bug the reader sees.
  it("never repeats a person", () => {
    expect(
      formatReactionAuthors(
        reaction({
          count: 2,
          users: [user("u1", "Álvaro Neto"), user("u1", "Álvaro Neto"), user("u2", "Caio Almeida")],
        }),
        me,
      ),
    ).toBe("Álvaro Neto e Caio Almeida");
  });

  // The count is authoritative: someone with no display name is still one of
  // the people who reacted, and belongs in the summary rather than nowhere.
  it("falls back to a count when nobody can be named", () => {
    expect(formatReactionAuthors(reaction({ count: 1 }), me)).toBe("1 pessoa");
    expect(formatReactionAuthors(reaction({ count: 4 }), me)).toBe("4 pessoas");
  });

  it("says nothing about a reaction nobody is in", () => {
    expect(formatReactionAuthors(reaction({ count: 0 }), me)).toBe("");
  });

  it("keeps the server's order", () => {
    const users = [user("u2", "Caio Almeida"), user("u1", "Álvaro Neto")];
    expect(formatReactionAuthors(reaction({ count: 2, users }), me)).toBe(
      "Caio Almeida e Álvaro Neto",
    );
  });
});

describe("reactionAccessibleDescription", () => {
  it("carries the emoji and who reacted in one string", () => {
    expect(
      reactionAccessibleDescription(
        reaction({ emoji: "🎉", count: 1, users: [user("u1", "Álvaro Neto")] }),
        me,
      ),
    ).toBe("🎉: Álvaro Neto");
  });

  it("degrades to the emoji alone when there is nobody to describe", () => {
    expect(reactionAccessibleDescription(reaction({ emoji: "🎉", count: 0 }), me)).toBe("🎉");
  });
});
