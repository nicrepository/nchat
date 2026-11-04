/**
 * Whether a mount is the entry of a reloaded document — F5, Ctrl+R, the
 * browser's reload button — still where the reload left it (issue #1130).
 *
 * Two facts, each from the one place that knows it:
 *
 *  - The browser knows how the *document* was entered: its Navigation Timing
 *    entry says "reload". Without that entry (a browser that does not record
 *    it) the answer is predictably "not a reload", the behaviour before #1130.
 *  - The document's history knows whether the app has moved since. The router
 *    hears every move — PUSH, REPLACE, POP — through the one listener it
 *    subscribes to the history; `documentHistory` records the move right
 *    there, synchronously, before handing it on. React only renders the move
 *    later, in a transition it may suspend and throw away, so nothing rendered
 *    (state, refs, effects) can stand in for this record. Leaving the
 *    document's first location is final: coming back to the same path, even by
 *    POP to the same history entry, is a return, not the reload.
 *
 * A redirect is a move like any other, so a reload at "/" (which redirects to
 * "/chat") or at a protected route without a session (which redirects to the
 * login) is not a reload of the shell. Neither is a loss: the address bar
 * already reads "/chat" after the first redirect, so it is "/chat" that gets
 * reloaded, and a sign-in has its own entry (issue #1123).
 *
 * The entry is spent by the first mount that asks, so a remount of the shell in
 * the same document never sees it again. Module memory on purpose, like the
 * fresh-login mark in authSession: it lives exactly as long as the document (a
 * reload starts a new one), nothing is persisted, it holds no identity and
 * grants nothing.
 */

import { createContext, useContext, useEffect } from "react";
import { type HistoryRouterProps, UNSAFE_createBrowserHistory } from "react-router";

import { recordHistoryMove, startHistoryTrail } from "./historyTrail";

type History = HistoryRouterProps["history"];

// v5Compat: the listener hears PUSH and REPLACE too, not only POP — the same
// options BrowserRouter creates its history with.
const browserHistory = UNSAFE_createBrowserHistory({ v5Compat: true });

/**
 * The entry's position in the browser's list: the router keeps it in the
 * history state as `idx` (#1124), the trail uses it to know which entries a
 * PUSH cut off.
 */
function historyIndex(): number | null {
  const idx: unknown = (window.history.state as { idx?: unknown } | null)?.idx;
  return typeof idx === "number" ? idx : null;
}

let leftEntry = false;
let spent = false;
startHistoryTrail(browserHistory.location, historyIndex());

/**
 * The browser history the app's router runs on (DocumentRouter): the same
 * history BrowserRouter would create, whose listener also records that the
 * app left the document's entry.
 */
export const documentHistory: History = {
  get action() {
    return browserHistory.action;
  },
  get location() {
    return browserHistory.location;
  },
  createHref: browserHistory.createHref,
  createURL: browserHistory.createURL,
  encodeLocation: browserHistory.encodeLocation,
  push: browserHistory.push,
  replace: browserHistory.replace,
  go: browserHistory.go,
  listen: (listener) =>
    browserHistory.listen((update) => {
      leftEntry = true;
      // The same synchronous moment, for the trail the contextual Back reads (#1124).
      recordHistoryMove(update.action, update.location, historyIndex());
      listener(update);
    }),
};

/**
 * Whether the tree runs on documentHistory. Provided by DocumentRouter; false
 * outside it, so no other router's mount is ever taken for the document's
 * entry — nothing would record that router's moves.
 */
export const DocumentRouterContext = createContext(false);

function spendDocumentReload(): void {
  spent = true;
}

function documentWasReloaded(): boolean {
  if (typeof performance.getEntriesByType !== "function") return false;
  const [entry] = performance.getEntriesByType("navigation") as PerformanceNavigationTiming[];
  return entry?.type === "reload";
}

/** Read on mount by the shell; spent by its first mount whatever it decided. */
export function useDocumentReloadEntry(): boolean {
  const onDocumentRouter = useContext(DocumentRouterContext);
  useEffect(spendDocumentReload, []);
  return onDocumentRouter && !leftEntry && !spent && documentWasReloaded();
}

/** For test isolation only: each test starts as a freshly loaded document. */
export function _resetDocumentEntry(): void {
  leftEntry = false;
  spent = false;
  startHistoryTrail(browserHistory.location, historyIndex());
}
