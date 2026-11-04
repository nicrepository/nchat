import type { ReactNode } from "react";
import { unstable_HistoryRouter as HistoryRouter } from "react-router";

import { DocumentRouterContext, documentHistory } from "./documentEntry";

/**
 * The app's router: BrowserRouter's behaviour (same browser history, same
 * transitions), on a history that records the app leaving the document's
 * entry the moment it moves (issue #1130, see documentEntry).
 */
export function DocumentRouter({ children }: { children: ReactNode }) {
  return (
    <HistoryRouter history={documentHistory}>
      <DocumentRouterContext.Provider value>{children}</DocumentRouterContext.Provider>
    </HistoryRouter>
  );
}
