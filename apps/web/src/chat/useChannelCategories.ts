import { useCallback, useEffect, useRef, useState } from "react";

import { fetchChannelCategories } from "./chatApi";
import type { ChannelCategoryGroup } from "./channelGrouping";

type ChannelCategoriesState =
  | { status: "loading" }
  | { status: "error" }
  | { status: "ready"; groups: ChannelCategoryGroup[]; canManage: boolean };

/**
 * Fetches the RF-17 grouping structure (`GET /api/chat/channel-categories`)
 * independently of `useChatSidebar`.
 *
 * Deliberately does not carry unread counts or activity timestamps, and does
 * not merge WebSocket events — those live only in `useChatSidebar`/
 * `sidebarOrder.ts`. `ChatSidebar` combines this hook's groups with the flat,
 * activity-aware channel list via `groupChannels` (`channelGrouping.ts`), so
 * that logic is never duplicated here.
 *
 * `status: "error"` is not surfaced as a sidebar-wide error: the caller falls
 * back to the flat channel list, since categorization is additive and the
 * channels themselves already loaded successfully through the sidebar
 * endpoint.
 */
export function useChannelCategories() {
  const [state, setState] = useState<ChannelCategoriesState>({ status: "loading" });
  const mountedRef = useRef(true);
  const loadPromiseRef = useRef<Promise<void> | null>(null);

  const reload = useCallback(() => {
    if (loadPromiseRef.current) return loadPromiseRef.current;
    setState({ status: "loading" });

    const loading = fetchChannelCategories()
      .then(({ groups, canManage }) => {
        if (mountedRef.current) setState({ status: "ready", groups, canManage });
      })
      .catch(() => {
        if (mountedRef.current) setState({ status: "error" });
      })
      .finally(() => {
        if (loadPromiseRef.current === loading) loadPromiseRef.current = null;
      });
    loadPromiseRef.current = loading;
    return loading;
  }, []);

  useEffect(() => {
    mountedRef.current = true;
    void reload();
    return () => {
      mountedRef.current = false;
    };
  }, [reload]);

  return { state, reload };
}
