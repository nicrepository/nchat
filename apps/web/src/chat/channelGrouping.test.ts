import { describe, expect, it } from "vitest";

import { groupChannels, UNCATEGORIZED_GROUP_KEY, type ChannelCategoryGroup } from "./channelGrouping";
import type { Channel } from "./chatTypes";

function channel(id: string, name: string, lastMessageAt: string | null = null): Channel {
  return { id, name, type: "public", canWrite: true, lastMessageAt };
}

describe("groupChannels", () => {
  it("groups channels under their category, preserving API group order", () => {
    const channels = [channel("c1", "geral"), channel("c2", "produto"), channel("c3", "engenharia")];
    const groups: ChannelCategoryGroup[] = [
      { kind: "uncategorized", name: "Geral", channelIds: ["c1"] },
      { kind: "category", id: "cat-1", name: "Times", channelIds: ["c3"] },
      { kind: "category", id: "cat-2", name: "Projetos", channelIds: ["c2"] },
    ];

    const result = groupChannels(channels, groups);

    expect(result.map((g) => g.name)).toEqual(["Geral", "Times", "Projetos"]);
    expect(result[0].channels.map((c) => c.id)).toEqual(["c1"]);
    expect(result[1].channels.map((c) => c.id)).toEqual(["c3"]);
    expect(result[2].channels.map((c) => c.id)).toEqual(["c2"]);
  });

  it("uses a stable sentinel key for the virtual uncategorized group", () => {
    const groups: ChannelCategoryGroup[] = [{ kind: "uncategorized", name: "Geral", channelIds: [] }];
    const result = groupChannels([], groups);
    expect(result[0].key).toBe(UNCATEGORIZED_GROUP_KEY);
    expect(result[0].id).toBeUndefined();
  });

  it("uses the category id as the group key for persisted categories", () => {
    const groups: ChannelCategoryGroup[] = [{ kind: "category", id: "cat-1", name: "Times", channelIds: [] }];
    const result = groupChannels([], groups);
    expect(result[0].key).toBe("cat-1");
    expect(result[0].id).toBe("cat-1");
  });

  it("keeps an empty category with an empty channel list rather than dropping it", () => {
    const groups: ChannelCategoryGroup[] = [
      { kind: "uncategorized", name: "Geral", channelIds: [] },
      { kind: "category", id: "cat-1", name: "Vazia", channelIds: [] },
    ];
    const result = groupChannels([], groups);
    expect(result).toHaveLength(2);
    expect(result[1].channels).toEqual([]);
  });

  it("orders channels within a category by activity, not by API list order", () => {
    const channels = [
      channel("older", "older", "2024-01-01T00:00:00Z"),
      channel("newer", "newer", "2024-06-01T00:00:00Z"),
    ];
    const groups: ChannelCategoryGroup[] = [
      { kind: "category", id: "cat-1", name: "Times", channelIds: ["older", "newer"] },
    ];

    const result = groupChannels(channels, groups);

    expect(result[0].channels.map((c) => c.id)).toEqual(["newer", "older"]);
  });

  it("falls back an orphan channel (present in the flat list but in no group) into the existing uncategorized group", () => {
    const channels = [channel("c1", "geral"), channel("orphan", "orphan")];
    const groups: ChannelCategoryGroup[] = [
      { kind: "uncategorized", name: "Geral", channelIds: ["c1"] },
      { kind: "category", id: "cat-1", name: "Times", channelIds: [] },
    ];

    const result = groupChannels(channels, groups);

    const geral = result.find((g) => g.kind === "uncategorized");
    expect(geral?.channels.map((c) => c.id).sort()).toEqual(["c1", "orphan"]);
  });

  it("synthesizes an uncategorized group for orphan channels when the API returned none", () => {
    const channels = [channel("orphan", "orphan")];
    const groups: ChannelCategoryGroup[] = [{ kind: "category", id: "cat-1", name: "Times", channelIds: [] }];

    const result = groupChannels(channels, groups);

    expect(result[0].kind).toBe("uncategorized");
    expect(result[0].channels.map((c) => c.id)).toEqual(["orphan"]);
    expect(result[1].name).toBe("Times");
  });

  it("ignores a channel id from the API that no longer exists in the flat list", () => {
    const channels = [channel("c1", "geral")];
    const groups: ChannelCategoryGroup[] = [
      { kind: "category", id: "cat-1", name: "Times", channelIds: ["c1", "gone"] },
    ];

    const result = groupChannels(channels, groups);

    expect(result[0].channels.map((c) => c.id)).toEqual(["c1"]);
  });

  it("returns an empty list when there are no groups and no channels", () => {
    expect(groupChannels([], [])).toEqual([]);
  });

  it("synthesizes a single uncategorized group when the API returned no groups at all", () => {
    const result = groupChannels([channel("c1", "geral")], []);
    expect(result).toHaveLength(1);
    expect(result[0].kind).toBe("uncategorized");
    expect(result[0].channels.map((c) => c.id)).toEqual(["c1"]);
  });
});
