/**
 * Chat API boundary.
 *
 * Currently returns typed fixture data while backend endpoints are pending.
 * See chatFixtures.ts for the pending backend contract.
 *
 * To wire up real endpoints, replace the fixture imports with calls to
 * `authenticatedFetch` using the contract documented in chatFixtures.ts.
 */

import { FIXTURE_CHANNELS, FIXTURE_DMS } from "./chatFixtures";
import type { Channel, DMConversation } from "./chatTypes";

export async function fetchChannels(): Promise<Channel[]> {
  // TODO: replace with authenticatedFetch(`${CHAT_BASE}/channels`, { method: "GET" })
  return Promise.resolve(FIXTURE_CHANNELS);
}

export async function fetchDMs(): Promise<DMConversation[]> {
  // TODO: replace with authenticatedFetch(`${CHAT_BASE}/dms`, { method: "GET" })
  return Promise.resolve(FIXTURE_DMS);
}
