/**
 * Loading the emoji catalog, as state the picker can render (issue #496).
 *
 * The catalog is a lazily-imported chunk, so it can fail like anything else that
 * crosses the network. This turns that into three states the UI can actually
 * show — and a retry that reaches the network again, rather than a spinner that
 * never resolves.
 */

import { useCallback, useEffect, useState } from "react";

import { loadEmojiCatalog, type EmojiCatalog } from "./emojiCatalog";

export type EmojiCatalogStatus = "loading" | "ready" | "error";

export interface EmojiCatalogResource {
  status: EmojiCatalogStatus;
  /** The catalog, non-null exactly when status is "ready". */
  catalog: EmojiCatalog | null;
  retry: () => void;
}

interface CatalogState {
  status: EmojiCatalogStatus;
  catalog: EmojiCatalog | null;
}

const loadingState: CatalogState = { status: "loading", catalog: null };

export function useEmojiCatalog(): EmojiCatalogResource {
  const [state, setState] = useState<CatalogState>(loadingState);
  const [attempt, setAttempt] = useState(0);

  useEffect(() => {
    let active = true;
    loadEmojiCatalog().then(
      (catalog) => {
        // The guard is what keeps a slow load from writing state into a picker
        // the reader has already closed.
        if (active) setState({ status: "ready", catalog });
      },
      () => {
        // The reason is deliberately dropped rather than shown: a module-loading
        // error says nothing a reader can act on, and the notice offers the one
        // thing that can help.
        if (active) setState({ status: "error", catalog: null });
      },
    );
    return () => {
      active = false;
    };
  }, [attempt]);

  const retry = useCallback(() => {
    setState(loadingState);
    setAttempt((current) => current + 1);
  }, []);

  return { status: state.status, catalog: state.catalog, retry };
}
