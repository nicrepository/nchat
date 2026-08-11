import type { Channel } from "./chatTypes";
import { sortPinnedFirst } from "./sidebarOrder";

// ── Channel categories (RF-17) ──────────────────────────────────────────────

export type ChannelCategoryGroupKind = "category" | "uncategorized";

/**
 * One group as `GET /api/chat/channel-categories` orders it — the shape the
 * hook fetches, before the flat `Channel` objects are attached.
 *
 * `id` is absent for the virtual "uncategorized" group: it is not a row, so
 * there is nothing to key it by except the sentinel `UNCATEGORIZED_GROUP_KEY`
 * a caller derives from `kind`.
 */
export interface ChannelCategoryGroup {
  kind: ChannelCategoryGroupKind;
  id?: string;
  name: string;
  channelIds: string[];
}

/** One group with its channels resolved, ready to render. */
export interface GroupedChannels {
  key: string;
  kind: ChannelCategoryGroupKind;
  id?: string;
  name: string;
  channels: Channel[];
}

/** Stable React/collapse key for the virtual "uncategorized" group. */
export const UNCATEGORIZED_GROUP_KEY = "__uncategorized__";

function groupKey(group: ChannelCategoryGroup): string {
  return group.kind === "category" && group.id ? group.id : UNCATEGORIZED_GROUP_KEY;
}

/**
 * Combines the flat, activity-aware channel list from `/api/chat/sidebar`
 * with the grouping structure from `/api/chat/channel-categories`.
 *
 * Group order is exactly what the API returned — never reordered here
 * (RF-17: "manter a ordem de categorias ... fornecida pelo domínio/API").
 * Within a group, channels keep the same activity ordering the flat list has
 * always used (`sortByActivity`, issue #414) — an existing product behavior,
 * not a new one invented for this grouping.
 *
 * A channel present in `channels` but absent from every group — a drift
 * between the two independent responses — is never dropped: it is appended
 * to the uncategorized group (synthesizing one at the end if the API did not
 * return one), since `channels` is the authoritative access list.
 */
export function groupChannels(
  channels: readonly Channel[],
  groups: readonly ChannelCategoryGroup[],
): GroupedChannels[] {
  const byId = new Map(channels.map((channel) => [channel.id, channel]));
  const claimed = new Set<string>();

  const result: GroupedChannels[] = groups.map((group) => {
    const resolved: Channel[] = [];
    for (const id of group.channelIds) {
      const channel = byId.get(id);
      if (!channel || claimed.has(id)) continue;
      claimed.add(id);
      resolved.push(channel);
    }
    return {
      key: groupKey(group),
      kind: group.kind,
      id: group.id,
      name: group.name,
      channels: sortPinnedFirst(resolved),
    };
  });

  const orphans = channels.filter((channel) => !claimed.has(channel.id));
  if (orphans.length > 0) {
    const uncategorized = result.find((group) => group.kind === "uncategorized");
    if (uncategorized) {
      uncategorized.channels = sortPinnedFirst([...uncategorized.channels, ...orphans]);
    } else {
      result.unshift({
        key: UNCATEGORIZED_GROUP_KEY,
        kind: "uncategorized",
        name: "Geral",
        channels: sortPinnedFirst(orphans),
      });
    }
  }

  return result;
}
