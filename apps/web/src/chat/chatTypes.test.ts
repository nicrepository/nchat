import { describe, expect, it } from "vitest";

import { partitionDMs, type DMConversation } from "./chatTypes";

function dm(id: string, type: DMConversation["type"], name = id): DMConversation {
  return { id, type, name, participants: [] };
}

describe("partitionDMs", () => {
  it("routes 1:1 conversations to directs and ad-hoc groups to groups", () => {
    const { directs, groups } = partitionDMs([
      dm("dm-1", "1:1", "Juliane Lino"),
      dm("dm-grp", "group", "Equipe Infra"),
      dm("dm-2", "1:1", "Caio Almeida"),
    ]);

    expect(directs.map((c) => c.id)).toEqual(["dm-1", "dm-2"]);
    expect(groups.map((c) => c.id)).toEqual(["dm-grp"]);
  });

  it("places every conversation in exactly one bucket", () => {
    const input = [dm("a", "1:1"), dm("b", "group"), dm("c", "1:1"), dm("d", "group")];
    const { directs, groups } = partitionDMs(input);

    const seen = [...directs, ...groups].map((c) => c.id).sort();
    expect(seen).toEqual(["a", "b", "c", "d"]);
    expect(directs.length + groups.length).toBe(input.length);
    expect(directs.some((c) => groups.includes(c))).toBe(false);
  });

  it("keeps the incoming order inside each bucket", () => {
    const { directs, groups } = partitionDMs([
      dm("g1", "group"),
      dm("d1", "1:1"),
      dm("g2", "group"),
      dm("d2", "1:1"),
    ]);

    expect(directs.map((c) => c.id)).toEqual(["d1", "d2"]);
    expect(groups.map((c) => c.id)).toEqual(["g1", "g2"]);
  });

  it("returns the same object references, never copies", () => {
    const group = dm("dm-grp", "group");
    const direct = dm("dm-1", "1:1");
    const { directs, groups } = partitionDMs([group, direct]);

    expect(groups[0]).toBe(group);
    expect(directs[0]).toBe(direct);
  });

  it("classifies on the type field alone, not on the visible name", () => {
    // A 1:1 whose counterpart is literally named like a group, and a group
    // named like a person: only `type` decides.
    const { directs, groups } = partitionDMs([
      dm("dm-1", "1:1", "Equipe Infra, Caio e Ana"),
      dm("dm-grp", "group", "Juliane Lino"),
    ]);

    expect(directs.map((c) => c.id)).toEqual(["dm-1"]);
    expect(groups.map((c) => c.id)).toEqual(["dm-grp"]);
  });

  it("yields two empty buckets for an empty list", () => {
    expect(partitionDMs([])).toEqual({ directs: [], groups: [] });
  });
});
