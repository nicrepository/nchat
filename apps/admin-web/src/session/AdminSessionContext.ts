import { createContext } from "react";

import type { AdminBootstrap } from "../api/adminApi";

/**
 * How far the console has got in establishing who is looking at it.
 *
 * `forbidden` is a separate state from `unauthenticated` on purpose: a signed-in
 * NChat user who is not a platform administrator must be told that, rather than
 * being bounced back to a sign-in form that would succeed and change nothing.
 */
export type AdminSessionStatus =
  | "loading"
  | "ready"
  | "unauthenticated"
  | "forbidden"
  | "unavailable"
  | "error";

export interface AdminSessionValue {
  status: AdminSessionStatus;
  bootstrap: AdminBootstrap | null;
  /** Human-readable reason for the current failure, if any. */
  message: string;
  /** Re-runs the bootstrap request. */
  reload: () => void;
  /** Adopts a bootstrap payload obtained by signing in. */
  adopt: (bootstrap: AdminBootstrap) => void;
  signOut: () => Promise<void>;
  /**
   * Whether the shell should render a control for this capability.
   *
   * This is presentation only. The server re-evaluates the same question on
   * every request, so a console that got this wrong — or a user who edited it
   * in a debugger — changes what is drawn and never what is permitted.
   */
  can: (capability: string) => boolean;
}

export const AdminSessionContext = createContext<AdminSessionValue | null>(null);
