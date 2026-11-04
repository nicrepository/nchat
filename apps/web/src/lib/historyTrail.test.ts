import { NavigationType } from "react-router";
import { beforeEach, describe, expect, it } from "vitest";

import { clearTokens, setTokens } from "./authSession";
import {
  _recordedEntryCount,
  claimHistoryTrail,
  historyTrail,
  historyTrailOwner,
  recordHistoryMove,
  startHistoryTrail,
} from "./historyTrail";

const at = (key: string, pathname: string) => ({ key, pathname });
const keys = () => historyTrail().map((entry) => entry.key);

// The browser's side: each entry's position, as the router keeps it in `idx`.
// The document starts at 5, as if reloaded there: 0–4 are entries from before.
const START = 5;
let position = START;
const positions = new Map<string, number>();
const push = (key: string, pathname = `/chat/dm/${key}`) => {
  position += 1;
  positions.set(key, position);
  recordHistoryMove(NavigationType.Push, at(key, pathname), position);
};
const replace = (key: string, pathname = `/chat/dm/${key}`) => {
  positions.set(key, position);
  recordHistoryMove(NavigationType.Replace, at(key, pathname), position);
};
const pop = (key: string, pathname = `/chat/dm/${key}`, index = positions.get(key)) => {
  position = index!;
  positions.set(key, position);
  recordHistoryMove(NavigationType.Pop, at(key, pathname), position);
};

describe("historyTrail (#1124)", () => {
  beforeEach(() => {
    position = START;
    positions.clear();
    positions.set("a", START);
    startHistoryTrail(at("a", "/chat/channel/a"), START);
  });

  it("PUSH links the new entry to the one it was pushed from", () => {
    recordHistoryMove(NavigationType.Push, at("b", "/chat/dm/b"));
    recordHistoryMove(NavigationType.Push, at("c", "/chat/channel/c"));

    expect(historyTrail()).toEqual([
      at("c", "/chat/channel/c"),
      at("b", "/chat/dm/b"),
      at("a", "/chat/channel/a"),
    ]);
  });

  it("REPLACE takes the replaced entry's place, inheriting what was behind it", () => {
    recordHistoryMove(NavigationType.Push, at("b", "/chat/dm/b"));
    recordHistoryMove(NavigationType.Replace, at("b2", "/chat/dm/b"));

    expect(keys()).toEqual(["b2", "a"]);
  });

  it("POP moves along entries already recorded without changing their links", () => {
    recordHistoryMove(NavigationType.Push, at("b", "/chat/dm/b"));
    recordHistoryMove(NavigationType.Push, at("c", "/chat/dm/c"));
    recordHistoryMove(NavigationType.Pop, at("b", "/chat/dm/b"));
    expect(keys()).toEqual(["b", "a"]);

    // Forward again: C still knows B is behind it.
    recordHistoryMove(NavigationType.Pop, at("c", "/chat/dm/c"));
    expect(keys()).toEqual(["c", "b", "a"]);
  });

  it("POP to an entry from before a reload knows its path and nothing behind it", () => {
    recordHistoryMove(NavigationType.Pop, at("old", "/chat/dm/old"));
    expect(historyTrail()).toEqual([at("old", "/chat/dm/old")]);

    recordHistoryMove(NavigationType.Push, at("n", "/chat/dm/n"));
    expect(keys()).toEqual(["n", "old"]);
  });

  it("an entry whose predecessor was replaced since has the replacement behind it", () => {
    recordHistoryMove(NavigationType.Push, at("b", "/chat/dm/b"));
    recordHistoryMove(NavigationType.Pop, at("a", "/chat/channel/a"));
    recordHistoryMove(NavigationType.Replace, at("a2", "/chat/channel/a"));
    // Forward to B: A2 is in A's slot of the browser's list, so it is behind B.
    recordHistoryMove(NavigationType.Pop, at("b", "/chat/dm/b"));

    expect(keys()).toEqual(["b", "a2"]);
  });

  it("A → B → C, Back to B, REPLACE B with B′, Forward to C: C → B′ → A (CQ-01)", () => {
    push("b");
    push("c");
    pop("b");
    replace("b2", "/chat/channel/b2");
    pop("c");

    expect(historyTrail()).toEqual([
      at("c", "/chat/dm/c"),
      at("b2", "/chat/channel/b2"),
      at("a", "/chat/channel/a"),
    ]);
  });

  it("REPLACE of the same conversation changing only the query keeps its place", () => {
    // Only paths are recorded, so the query never reaches the trail.
    push("b", "/chat/dm/b");
    push("c");
    pop("b");
    replace("b-query", "/chat/dm/b");
    pop("c");

    expect(historyTrail()).toEqual([
      at("c", "/chat/dm/c"),
      at("b-query", "/chat/dm/b"),
      at("a", "/chat/channel/a"),
    ]);
  });

  it("POP → REPLACE → POP → REPLACE → POP keeps every link", () => {
    push("b");
    push("c");
    push("d");
    pop("c");
    replace("c2");
    pop("b");
    replace("b2");
    pop("c2");
    pop("d");

    expect(keys()).toEqual(["d", "c2", "b2", "a"]);
  });

  it("several REPLACEs of the same entry chain onto the latest one", () => {
    push("b");
    push("c");
    pop("b");
    replace("b2");
    replace("b3");
    replace("b4");
    pop("c");

    expect(keys()).toEqual(["c", "b4", "a"]);
    pop("b4");
    expect(keys()).toEqual(["b4", "a"]);
  });

  it("a PUSH after going back abandons the old forward branch for good", () => {
    push("b");
    push("c");
    pop("b");
    push("d");
    expect(keys()).toEqual(["d", "b", "a"]);

    // The abandoned C is never reachable again; replacing D or B does not revive it.
    replace("d2");
    expect(keys()).toEqual(["d2", "b", "a"]);
    pop("b");
    replace("b2");
    pop("d2");
    expect(keys()).toEqual(["d2", "b2", "a"]);
  });

  it("a REPLACE that reuses the current key changes only its path", () => {
    push("b");
    replace("b", "/chat/dm/other");
    expect(historyTrail()).toEqual([at("b", "/chat/dm/other"), at("a", "/chat/channel/a")]);
  });

  it("a new session starts over from the entry on screen", () => {
    recordHistoryMove(NavigationType.Push, at("b", "/chat/dm/b"));

    clearTokens();
    expect(keys()).toEqual(["b"]);

    recordHistoryMove(NavigationType.Push, at("c", "/chat/dm/c"));
    setTokens("another-user");
    expect(keys()).toEqual(["c"]);
    clearTokens();
  });

  it("a token rotation by itself keeps the trail: it is only a claim of the same user", () => {
    claimHistoryTrail("u1:w1");
    recordHistoryMove(NavigationType.Push, at("b", "/chat/dm/b"));
    setTokens("rotated", "refresh");

    expect(keys()).toEqual(["b", "a"]);
    expect(historyTrailOwner()).toBe("u1:w1");
    clearTokens();
  });

  it("a rotation before any identity claimed the trail starts it over (CQ-05)", () => {
    // Nobody confirmed whose these entries were, so nobody can confirm the
    // rotated token is the same: the next claim must not adopt them.
    push("b", "/chat/search");
    push("c");
    setTokens("rotated", "refresh");
    expect(keys()).toEqual(["c"]);

    claimHistoryTrail("u2:w1");
    expect(keys()).toEqual(["c"]);
    clearTokens();
  });

  it("the first confirmed identity adopts the trail; the same one keeps it (CQ-02)", () => {
    push("b");
    claimHistoryTrail("u1:w1");
    push("c");
    claimHistoryTrail("u1:w1");
    setTokens("rotated", "refresh");
    claimHistoryTrail("u1:w1");

    expect(keys()).toEqual(["c", "b", "a"]);
    clearTokens();
  });

  it("another confirmed user or workspace starts over from the entry on screen (CQ-02)", () => {
    push("b");
    claimHistoryTrail("u1:w1");
    push("c");
    claimHistoryTrail("u2:w1");
    expect(keys()).toEqual(["c"]);

    push("d");
    claimHistoryTrail("u2:w2");
    expect(keys()).toEqual(["d"]);
  });

  it("a session change forgets the owner as well as the entries", () => {
    claimHistoryTrail("u1:w1");
    push("b");
    clearTokens();
    push("c");
    // Whoever signs in next adopts only what was recorded since the sign-out.
    claimHistoryTrail("u1:w1");
    expect(keys()).toEqual(["c", "b"]);
  });

  describe("what is kept (CQ-06)", () => {
    it("10,000 PUSH-after-POP branches keep only the reachable entries", () => {
      push("b");
      for (let round = 0; round < 10_000; round += 1) {
        pop("a", "/chat/channel/a");
        push(`b${round}`);
      }
      expect(keys()).toEqual(["b9999", "a"]);
      expect(_recordedEntryCount()).toBe(2);
    });

    it("a PUSH drops the whole forward branch, however long, and nothing behind", () => {
      push("b");
      push("c");
      push("d");
      push("e");
      pop("b");
      push("x");
      expect(keys()).toEqual(["x", "b", "a"]);
      expect(_recordedEntryCount()).toBe(3);
    });

    it("entries still reachable by Back or Forward are never dropped", () => {
      push("b");
      push("c");
      push("d");
      pop("c");
      pop("b");
      // Back and Forward only: C and D are still the browser's, and kept.
      expect(_recordedEntryCount()).toBe(4);
      pop("d");
      expect(keys()).toEqual(["d", "c", "b", "a"]);

      // A REPLACE does not cut the forward branch either (CQ-01).
      pop("b");
      replace("b2");
      expect(_recordedEntryCount()).toBe(4);
      pop("d");
      expect(keys()).toEqual(["d", "c", "b2", "a"]);
    });

    it("repeated branching through REPLACEs and multi-step Backs stays bounded", () => {
      for (let round = 0; round < 1_000; round += 1) {
        push(`p${round}`);
        push(`q${round}`);
        push(`r${round}`);
        pop(`p${round}`);
        replace(`p${round}x`);
        pop("a", "/chat/channel/a");
      }
      // From A, every branch but the last was cut by the next round's PUSH.
      expect(_recordedEntryCount()).toBe(4);
      pop("r999");
      expect(keys()).toEqual(["r999", "q999", "p999x", "a"]);
    });

    it("an entry from before a reload is kept as reachable, and pruned once cut", () => {
      pop("old", "/chat/dm/old", START - 1);
      push("n");
      pop("old", "/chat/dm/old");
      push("m");
      expect(keys()).toEqual(["m", "old"]);
      expect(_recordedEntryCount()).toBe(2); // old, m — n and the first entry are gone
    });

    it("1,000 branches from an entry before the reload keep only what is reachable (CQ-08)", () => {
      // A (first entry) → Back to OLD (before the reload) → N: the browser drops
      // A, which no link here placed after OLD — its position does.
      for (let round = 0; round < 1_000; round += 1) {
        pop("old", "/chat/dm/old", START - 1);
        push(`n${round}`);
      }
      expect(keys()).toEqual(["n999", "old"]);
      expect(_recordedEntryCount()).toBe(2);
    });

    it("an entry whose position is unknown is kept, never dropped by a guess", () => {
      recordHistoryMove(NavigationType.Pop, at("lost", "/chat/dm/lost"));
      pop("a", "/chat/channel/a");
      push("b");
      recordHistoryMove(NavigationType.Pop, at("lost", "/chat/dm/lost"));
      expect(keys()).toEqual(["lost"]);
      expect(_recordedEntryCount()).toBe(3);
    });

    it.each([NaN, Infinity, -Infinity, -1, 2.5])(
      "a position of %s is no position: it prunes nothing and is never pruned (CQ-12)",
      (invalid) => {
        push("b");
        push("c");
        pop("b");
        // Not a place in the list: C, still reachable by Forward, stays.
        recordHistoryMove(NavigationType.Push, at("x", "/chat/dm/x"), invalid);
        expect(_recordedEntryCount()).toBe(4);
        expect(keys()).toEqual(["x", "b", "a"]);

        // A valid PUSH cuts what its position proves cut — B and C — not X.
        pop("a", "/chat/channel/a");
        push("y");
        expect(_recordedEntryCount()).toBe(3);
        expect(keys()).toEqual(["y", "a"]);
      },
    );

    it("a reload onto an invalid position starts the record with it unknown (CQ-12)", () => {
      startHistoryTrail(at("r", "/chat/dm/r"), Number.NaN);
      push("b");
      pop("r", "/chat/dm/r", Number.NaN);
      push("c");
      // R's position is unknown: kept, never guessed into the pruned branch.
      expect(keys()).toEqual(["c", "r"]);
    });
  });
});
