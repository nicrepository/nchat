import { getSessionGeneration } from "./authSession";

/**
 * Module-scoped state that belongs to one session and is rebuilt when that
 * session is replaced.
 *
 * Some client state has to outlive every natural reset the app has — a
 * remount, a route change, React StrictMode's second mount, a new WebSocket
 * generation — because none of those means anything changed about who is using
 * the app. State like that is module-scoped, which leaves it with no boundary
 * of its own, and the boundary it actually needs is the identity boundary: a
 * logout, a different account, a token replaced.
 *
 * `getSessionGeneration` is that boundary and already exists (see
 * lib/authSession); presence.ts scopes its own snapshot to it the same way.
 * This checks it lazily on access rather than subscribing, which is why it
 * needs no listener, no teardown and nothing to leak — and why it is still
 * correct for a module first loaded after the change happened.
 *
 * The check is a single integer compare on the caller's path.
 */
export function sessionScoped<T>(build: () => T): () => T {
  let scope = getSessionGeneration();
  let value = build();
  return () => {
    const generation = getSessionGeneration();
    if (generation !== scope) {
      scope = generation;
      value = build();
    }
    return value;
  };
}
