import { describe, expect, it } from "vitest";

import type { TrailEntry } from "../lib/historyTrail";
import {
  conversationBackDelta,
  sessionOwnsTrail,
  type Availability,
  type BackSession,
} from "./conversationBack";

const A = "/chat/channel/aaaaaaaa-0000-4000-8000-000000000001";
const B = "/chat/dm/bbbbbbbb-0000-4000-8000-000000000002";
const C = "/chat/dm/cccccccc-0000-4000-8000-000000000003";

/** Nearest first, like historyTrail(): keys are e0 (current), e1, e2… */
function trail(...pathnames: string[]): TrailEntry[] {
  return pathnames.map((pathname, index) => ({ key: `e${index}`, pathname }));
}

const everyone = (): Availability => "available";
const settled: BackSession = { confirmed: "u1:ws", owner: "u1:ws", pending: false };

describe("conversationBackDelta (#1124)", () => {
  it("A → B → C: C goes one back to B, B one back to A, A to the list", () => {
    expect(conversationBackDelta(trail(C, B, A), "e0", settled, everyone)).toBe(1);
    expect(conversationBackDelta(trail(B, A), "e0", settled, everyone)).toBe(1);
    expect(conversationBackDelta(trail(A), "e0", settled, everyone)).toBeNull();
  });

  it("tells a channel and a DM with the same id apart", () => {
    const channel = "/chat/channel/same";
    const dm = "/chat/dm/same";
    expect(conversationBackDelta(trail(dm, channel), "e0", settled, everyone)).toBe(1);
  });

  it("skips entries of the conversation on screen — message jumps, replaced entries", () => {
    expect(conversationBackDelta(trail(B, B, B, A), "e0", settled, everyone)).toBe(3);
    expect(conversationBackDelta(trail(B, B), "e0", settled, everyone)).toBeNull();
  });

  it("identifies a conversation the way the route does: decoded and normalised", () => {
    const upper = "/chat/dm/BBBBBBBB-0000-4000-8000-000000000002/";
    const encoded = "/chat/dm/bbbbbbbb%2D0000-4000-8000-000000000002";
    expect(conversationBackDelta(trail(upper, encoded, A), "e0", settled, everyone)).toBe(2);
  });

  it("A → B → A goes back to B, then to A, then to the list — never a loop", () => {
    expect(conversationBackDelta(trail(A, B, A), "e0", settled, everyone)).toBe(1);
    expect(conversationBackDelta(trail(B, A), "e0", settled, everyone)).toBe(1);
    expect(conversationBackDelta(trail(A), "e0", settled, everyone)).toBeNull();
  });

  it("skips conversations no longer available, and falls back when none is left", () => {
    const available = (conversation: { id: string }): Availability =>
      conversation.id.startsWith("bbbb") ? "unavailable" : "available";
    expect(conversationBackDelta(trail(C, B, A), "e0", settled, available)).toBe(2);
    expect(conversationBackDelta(trail(C, B), "e0", settled, available)).toBeNull();
  });

  it("never travels to, nor past, a conversation whose availability is unknown (CQ-03)", () => {
    const unknown = (): Availability => "unknown";
    expect(conversationBackDelta(trail(C, B, A), "e0", settled, unknown)).toBe("unknown");

    // B unknown, A known: neither skipped past B to A, nor sent to B.
    const bUnknown = (conversation: { id: string }): Availability =>
      conversation.id.startsWith("bbbb") ? "unknown" : "available";
    expect(conversationBackDelta(trail(C, B, A), "e0", settled, bUnknown)).toBe("unknown");

    // An unavailable one first, then an unknown one: still unknown, never A.
    const goneThenUnknown = (conversation: { id: string }): Availability =>
      conversation.id.startsWith("cccc") ? "unavailable" : "unknown";
    expect(conversationBackDelta(trail(B, C, A), "e0", settled, goneThenUnknown)).toBe("unknown");
  });

  it("unknown availability does not stand in the way of what needs none", () => {
    const unknown = (): Availability => "unknown";
    // The entries of the conversation on screen and the search are not conversations to vouch for.
    expect(conversationBackDelta(trail(C, C, "/chat/search", A), "e0", settled, unknown)).toBe(2);
    expect(conversationBackDelta(trail(C, "/profile", A), "e0", settled, unknown)).toBeNull();
    expect(conversationBackDelta(trail(C), "e0", settled, unknown)).toBeNull();
  });

  it("with several predecessors, one invalid and one valid, lands on the valid one", () => {
    const valid = (conversation: { id: string }): Availability =>
      conversation.id.startsWith("cccc") ? "unavailable" : "available";
    expect(conversationBackDelta(trail(B, C, A), "e0", settled, valid)).toBe(2);
  });

  it("returns to the search or the favourites the conversation was opened from", () => {
    expect(conversationBackDelta(trail(C, "/chat/search", A), "e0", settled, everyone)).toBe(1);
    expect(conversationBackDelta(trail(C, "/chat/favorites"), "e0", settled, everyone)).toBe(1);
  });

  it("stops at anything else: the home, the profile, the sign-in", () => {
    for (const other of ["/chat", "/profile", "/login", "/admin/users", "/call/x"]) {
      expect(conversationBackDelta(trail(C, other, A), "e0", settled, everyone)).toBeNull();
    }
  });

  it("counts from the entry on screen when a newer one is still suspended", () => {
    // History is already at C; the screen still shows B. Back from B means A.
    expect(conversationBackDelta(trail(C, B, A), "e1", settled, everyone)).toBe(2);
  });

  it("offers nothing when the entry on screen is unknown or not a supported surface", () => {
    expect(conversationBackDelta(trail(C, B), "elsewhere", settled, everyone)).toBeNull();
    expect(conversationBackDelta(trail("/profile", A), "e0", settled, everyone)).toBeNull();
    expect(conversationBackDelta([], "e0", settled, everyone)).toBeNull();
  });

  describe("returning from search and favourites", () => {
    it.each(["/profile", "/profile/notifications", "/profile/security", "/profile/sessions"])(
      "search returns to the recorded profile origin %s only for its confirmed owner",
      (profile) => {
        const entries = trail("/chat/search", profile);
        expect(conversationBackDelta(entries, "e0", settled, everyone)).toBe(1);
        expect(conversationBackDelta(entries, "e0", { ...settled, pending: true }, everyone)).toBe(
          "unknown",
        );
        expect(conversationBackDelta(entries, "e0", { ...settled, owner: "u2:ws" }, everyone)).toBe(
          "unknown",
        );
        expect(conversationBackDelta(trail(C, profile), "e0", settled, everyone)).toBeNull();
        expect(
          conversationBackDelta(trail("/chat/favorites", profile), "e0", settled, everyone),
        ).toBeNull();
      },
    );
    for (const surface of ["/chat/search", "/chat/favorites"]) {
      it(`${surface} returns to a known conversation or Home, skipping its own repeated entries`, () => {
        expect(conversationBackDelta(trail(surface, A), "e0", settled, everyone)).toBe(1);
        expect(
          conversationBackDelta(trail(surface, `${surface}/`, B), "e0", settled, everyone),
        ).toBe(2);
        expect(conversationBackDelta(trail(surface, "/chat/"), "e0", settled, everyone)).toBe(1);
      });

      it(`${surface} never traverses an external, login or unowned predecessor`, () => {
        for (const path of [
          "https://outside.example/",
          "/login",
          "/profile/unknown",
          "/admin/users",
        ]) {
          expect(
            conversationBackDelta(trail(surface, path, A), "e0", settled, everyone),
          ).toBeNull();
        }
        expect(
          conversationBackDelta(trail(surface, A), "e0", { ...settled, pending: true }, everyone),
        ).toBe("unknown");
        expect(
          conversationBackDelta(trail(surface, A), "e0", { ...settled, owner: "u2:ws" }, everyone),
        ).toBe("unknown");
        expect(
          conversationBackDelta(trail(surface, B, A), "e0", settled, ({ id }) =>
            id === "bbbbbbbb-0000-4000-8000-000000000002" ? "unavailable" : "available",
          ),
        ).toBe(2);
      });
    }
  });

  describe("identity first (CQ-05)", () => {
    const SEARCH = "/chat/search";
    const FAVORITES = "/chat/favorites";
    const unsettled: Record<string, BackSession> = {
      "a rotated token waiting to be confirmed": { ...settled, pending: true },
      "no identity confirmed": { confirmed: null, owner: "u1:ws", pending: false },
      "a trail claimed by another identity": { ...settled, owner: "u2:ws" },
      "a trail nobody claimed yet": { ...settled, owner: null },
    };

    for (const [name, session] of Object.entries(unsettled)) {
      it(`with ${name}, no destination of any kind is chosen`, () => {
        const asked: string[] = [];
        const spy = (conversation: { id: string }): Availability => {
          asked.push(conversation.id);
          return "available";
        };
        // The surfaces that need no availability — the case the check used to miss.
        expect(conversationBackDelta(trail(C, SEARCH, A), "e0", session, spy)).toBe("unknown");
        expect(conversationBackDelta(trail(C, FAVORITES, A), "e0", session, spy)).toBe("unknown");
        // A conversation of each kind: the availability is not even asked.
        expect(conversationBackDelta(trail(C, B, A), "e0", session, spy)).toBe("unknown");
        expect(conversationBackDelta(trail(B, A), "e0", session, spy)).toBe("unknown");
        expect(asked).toEqual([]);
      });
    }

    it("the same trail, once settled, leads where it did", () => {
      expect(conversationBackDelta(trail(C, SEARCH, A), "e0", settled, everyone)).toBe(1);
      expect(conversationBackDelta(trail(C, FAVORITES, A), "e0", settled, everyone)).toBe(1);
    });

    it("sessionOwnsTrail: confirmed, unpending, and the owner — all three", () => {
      expect(sessionOwnsTrail(settled)).toBe(true);
      for (const session of Object.values(unsettled)) expect(sessionOwnsTrail(session)).toBe(false);
    });
  });
});
