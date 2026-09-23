/**
 * The roster's ordering rule (issue #895), proved as a function.
 *
 * The rule is stated here rather than read out of a rendered list because a
 * DOM assertion can only show that *this* input produced *that* order. What
 * matters about a fallback is that it is total, stable and free of anything the
 * runtime might answer differently tomorrow, and those are properties of the
 * comparator, not of a screen.
 */

import { describe, expect, it } from "vitest";

import {
  compareRosterKeys,
  orderRoster,
  rosterPresenceStates,
  type RosterParticipant,
} from "./participantRosterOrder";
import {
  applyPresenceUpdate,
  emptyTargetPresence,
  type PresenceState,
  type TargetPresence,
} from "./presence";

function person(userId: string, displayName: string): RosterParticipant {
  return { userId, displayName, subtitle: "Participante" };
}

/** Presence as a lookup, so a case states exactly the states it means to test. */
function states(byUser: Record<string, PresenceState>) {
  return (userId: string): PresenceState => byUser[userId] ?? "unknown";
}

const ids = (participants: readonly RosterParticipant[]) => participants.map((p) => p.userId);

describe("orderRoster — presence class", () => {
  it("orders online before away before unknown before offline", () => {
    const roster = [
      person("d", "Nome D"),
      person("c", "Nome C"),
      person("b", "Nome B"),
      person("a", "Nome A"),
    ];

    const ordered = orderRoster(
      roster,
      states({ a: "offline", b: "unknown", c: "away", d: "online" }),
    );

    expect(ids(ordered)).toEqual(["d", "c", "b", "a"]);
  });

  it("ranks unknown above offline, because only offline is a claim of absence", () => {
    // The roster arrives before the presence snapshot does. Nobody has been
    // reported absent yet, so nobody is sorted as if they had been.
    const ordered = orderRoster(
      [person("gone", "Aaa"), person("silent", "Zzz")],
      states({ gone: "offline" }),
    );

    expect(ids(ordered)).toEqual(["silent", "gone"]);
  });

  it("lets an offline participant outrank an online one from no other rule", () => {
    // Stated the other way round from the case above: presence is the *first*
    // key, so a name that would sort first cannot pull an offline person above
    // an online one.
    const ordered = orderRoster(
      [person("offline-aaa", "Aaa"), person("online-zzz", "Zzz")],
      states({ "offline-aaa": "offline", "online-zzz": "online" }),
    );

    expect(ids(ordered)).toEqual(["online-zzz", "offline-aaa"]);
  });

  it("changes priority when presence changes, without changing identity", () => {
    const roster = [person("u1", "Bea"), person("u2", "Ana")];

    const before = orderRoster(roster, states({ u1: "online", u2: "offline" }));
    const after = orderRoster(roster, states({ u1: "offline", u2: "online" }));

    expect(ids(before)).toEqual(["u1", "u2"]);
    expect(ids(after)).toEqual(["u2", "u1"]);
    // Same people, both times: reordering is not a membership change.
    expect([...ids(after)].sort()).toEqual([...ids(before)].sort());
  });
});

describe("orderRoster — the deterministic fallback", () => {
  it("falls back to the name when nothing distinguishes presence", () => {
    const ordered = orderRoster(
      [person("u3", "Carla"), person("u1", "Ana"), person("u2", "Bea")],
      states({}),
    );

    expect(ids(ordered)).toEqual(["u1", "u2", "u3"]);
  });

  it("folds case, so capitalisation does not decide the order", () => {
    const ordered = orderRoster([person("u1", "bea"), person("u2", "Ana")], states({}));

    expect(ids(ordered)).toEqual(["u2", "u1"]);
  });

  it("folds accents, so an accented name is not exiled past Z", () => {
    // The whole reason the key is folded: every Latin-1 accented letter sorts
    // above every unaccented one by code unit, so "Álvaro" would land after
    // "Zoe" and this product's users would all be at the bottom of the list.
    const ordered = orderRoster(
      [person("u1", "Zoe"), person("u2", "Álvaro"), person("u3", "Beatriz")],
      states({}),
    );

    expect(ids(ordered)).toEqual(["u2", "u3", "u1"]);
  });

  it("folds nothing it was not asked to: surrounding whitespace still orders", () => {
    // The server trims display names (COALESCE(NULLIF(BTRIM(...)))), so a
    // leading space is not a case this has to be clever about — only one it must
    // stay deterministic about. A space sorts below every letter, and that is
    // the same answer every run.
    const ordered = orderRoster(
      [person("u1", "Bea"), person("u2", " Ana"), person("u3", "Ana")],
      states({}),
    );

    expect(ids(ordered)).toEqual(["u2", "u3", "u1"]);
  });

  it("orders non-Latin names deterministically rather than dropping them", () => {
    // No collation is invented for scripts the fold does not touch: they order
    // by code unit, which is arbitrary as an alphabet and stable as an order —
    // and every name still appears exactly once.
    const roster = [person("u1", "Ana"), person("u2", "Ямал"), person("u3", "佐藤")];

    const first = ids(orderRoster(roster, states({})));
    const second = ids(orderRoster([...roster].reverse(), states({})));

    expect(first).toEqual(second);
    expect([...first].sort()).toEqual(["u1", "u2", "u3"]);
    // Latin first here is a consequence of code-unit order, not a policy about
    // whose names come first: presence still outranks all of it.
    expect(first[0]).toBe("u1");
  });

  it("separates identical names by user id", () => {
    const ordered = orderRoster([person("u9", "Álvaro"), person("u1", "Alvaro")], states({}));

    // Folded to the same key, so the ids decide — and they decide the same way
    // every time.
    expect(ids(ordered)).toEqual(["u1", "u9"]);
  });

  it("orders a whole group of identical names by id, in both directions", () => {
    // Three people with the same name is what exercises the last key both ways:
    // the sort has to conclude that one id sorts before another *and* that
    // another sorts after, so the comparator cannot be right by luck.
    const roster = [person("u2", "Ana"), person("u3", "Ana"), person("u1", "Ana")];

    expect(ids(orderRoster(roster, states({})))).toEqual(["u1", "u2", "u3"]);
    expect(ids(orderRoster([...roster].reverse(), states({})))).toEqual(["u1", "u2", "u3"]);
  });

  it("is stable across repeated runs on a shuffled input", () => {
    const roster = [
      person("u4", "Ana"),
      person("u1", "Ana"),
      person("u3", "Bea"),
      person("u2", "Bea"),
    ];
    const presence = states({ u3: "online", u1: "online" });

    const first = ids(orderRoster(roster, presence));
    const second = ids(orderRoster([...roster].reverse(), presence));

    expect(first).toEqual(["u1", "u3", "u4", "u2"]);
    expect(second).toEqual(first);
  });

  it("never invents recency from a timestamp it does not have", () => {
    // Two people the server has said nothing about beyond "offline". Nothing in
    // any contract on this screen carries a last-activity instant, so they are
    // ordered by the documented fallback and not by an invented one.
    const ordered = orderRoster(
      [person("u2", "Bruno"), person("u1", "Aline")],
      states({ u1: "offline", u2: "offline" }),
    );

    expect(ids(ordered)).toEqual(["u1", "u2"]);
  });
});

describe("compareRosterKeys", () => {
  const key = (userId: string, rank = 0, name = "ana") => ({ userId, rank, name });

  it("answers 0 for the same person, whatever else differs", () => {
    // Reflexivity is the one thing a sort over distinct elements can never
    // demonstrate, and it is the property a comparator is required to have.
    expect(compareRosterKeys(key("u1"), key("u1"))).toBe(0);
    expect(compareRosterKeys(key("u1", 0, "ana"), key("u1", 3, "zoe"))).toBe(0);
  });

  it("is antisymmetric on every key it orders by", () => {
    const pairs: [ReturnType<typeof key>, ReturnType<typeof key>][] = [
      [key("u1", 0), key("u2", 3)],
      [key("u1", 1, "ana"), key("u2", 1, "bea")],
      [key("u1", 1, "ana"), key("u2", 1, "ana")],
    ];
    for (const [a, b] of pairs) {
      expect(compareRosterKeys(a, b)).toBeLessThan(0);
      expect(compareRosterKeys(b, a)).toBeGreaterThan(0);
    }
  });
});

describe("orderRoster — identity", () => {
  it("drops a duplicate user id, keeping the first occurrence", () => {
    const first = { ...person("u1", "Ana"), subtitle: "Moderador" };
    const duplicate = { ...person("u1", "Ana"), subtitle: "Membro" };

    const ordered = orderRoster([first, duplicate, person("u2", "Bea")], states({}));

    expect(ids(ordered)).toEqual(["u1", "u2"]);
    expect(ordered[0].subtitle).toBe("Moderador");
  });

  it("does not mutate its input", () => {
    const roster = [person("u2", "Zoe"), person("u1", "Ana")];
    const snapshot = [...roster];

    orderRoster(roster, states({ u2: "online" }));

    expect(roster).toEqual(snapshot);
    expect(roster[0].userId).toBe("u2");
  });

  it("orders an empty roster and a single participant without special cases", () => {
    expect(orderRoster([], states({}))).toEqual([]);
    expect(ids(orderRoster([person("u1", "Ana")], states({ u1: "offline" })))).toEqual(["u1"]);
  });
});

describe("rosterPresenceStates", () => {
  /** One conversation's view, built through the store's own reducer. */
  function viewWith(userId: string, state: PresenceState): TargetPresence {
    return {
      entries: applyPresenceUpdate(new Map(), userId, {
        state: state as Exclude<PresenceState, "unknown">,
        updatedAt: { secondMs: 1_000, nanosecond: 0 },
      }),
      covered: true,
    };
  }

  it("resolves every participant against the conversation it is rendered in", () => {
    const resolved = rosterPresenceStates(
      [person("u1", "Ana"), person("u2", "Bea")],
      viewWith("u1", "away"),
    );

    expect(resolved.get("u1")).toBe("away");
    // Absent from a conversation the server has fully described: that is the
    // one place absence may be read as offline.
    expect(resolved.get("u2")).toBe("offline");
  });

  it("answers unknown for a conversation the server has not fully described", () => {
    const resolved = rosterPresenceStates([person("u1", "Ana")], emptyTargetPresence);

    expect(resolved.get("u1")).toBe("unknown");
  });

  it("resolves each person once, whatever the roster repeats", () => {
    const resolved = rosterPresenceStates(
      [person("u1", "Ana"), person("u1", "Ana")],
      viewWith("u1", "online"),
    );

    expect(resolved.size).toBe(1);
    expect(resolved.get("u1")).toBe("online");
  });
});
