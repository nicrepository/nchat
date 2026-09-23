/**
 * How a participant roster is ordered, and how its presence is resolved
 * (issue #895).
 *
 * Pure and free of JSX on purpose: ordering is the one rule in this feature
 * that can be stated as a function of its inputs, so it is proved by its own
 * unit tests rather than by reading rows out of a rendered list.
 *
 * Nothing here decides *who* is in a roster. A participant arrives already
 * decided by whichever contract the caller read (a channel's members, a group's
 * participants); presence only arranges them. That separation is the whole
 * difference between this roster and the presence-filtered list a channel's
 * details endpoint returns.
 */

import { selectTargetPresence, type PresenceState, type TargetPresence } from "./presence";
import type { DirectMessageAccess } from "./directMessage";

/**
 * One person, as the roster renders them.
 *
 * A presentation projection rather than a domain union: a channel member and a
 * group participant are different records with different rules — one has a
 * channel role, the other has none, and they live in different tables — and the
 * only thing this list needs from either is who they are and what one line of
 * context says about them. Each caller maps its own payload, so no shared type
 * here has to pretend the two persistences are one.
 *
 * `subtitle` is that line of context and is always a word the caller's own
 * domain uses ("Moderador", "Membro", "Participante"). It is never invented for
 * a domain that has no such concept.
 *
 * Deliberately no `presence` field: presence is answered by the realtime store
 * at render time, and a snapshot carried alongside the person would be a second
 * source of truth for it — the one RF-58 already removed from this panel.
 */
export interface RosterParticipant {
  userId: string;
  displayName: string;
  /** Already filtered by chatApi's same-origin rule; absent means initials. */
  avatarUrl?: string;
  subtitle: string;
}

/**
 * Everything a roster needs that is not the people in it.
 *
 * `presence` is *this conversation's* view of the presence store, already
 * scoped by the caller's subscription — so "not in this conversation's
 * snapshot" can mean offline here and nowhere else, and a frame about another
 * conversation never reaches this roster at all.
 *
 * `openDM` absent renders a roster that shows people without offering to open
 * conversations with them, which is what a host that has not wired the flow
 * should look like. It is the shared operation plus this host's claim on it,
 * and its identity is stable — a request starting for somebody does not change
 * it, so the rows are not rebuilt by one.
 */
export interface RosterContext {
  presence: TargetPresence;
  /** Identifies the viewer by id; a display name would be ambiguous. */
  currentUserId: string;
  openDM?: DirectMessageAccess;
}

/**
 * The order presence imposes on the list.
 *
 * Only these four values exist, and only one of them is a claim of absence.
 * `offline` is the server saying this person is gone; `unknown` is this tab
 * saying it has not been told. Ranking `unknown` above `offline` is what keeps
 * a roster loaded before the presence snapshot arrives from reading as a list
 * of absent people — nobody has been reported absent yet.
 *
 * "Away / recently active" is `away`, and that is as far as recency goes. The
 * issue's third preference — offline ordered by most recent activity — is not
 * implemented, because no contract on this screen carries a last-activity
 * instant. Presence entries do carry an `updatedAt`, but it stamps the moment a
 * *state transition* was decided, and an offline participant usually has no
 * entry at all: they are offline precisely because a complete snapshot did not
 * mention them, so there is nothing to have stamped. Ordering by it would be a
 * recency claim invented from the shape of the data rather than read from it.
 */
const presenceRank: Record<PresenceState, number> = {
  online: 0,
  away: 1,
  unknown: 2,
  offline: 3,
};

/**
 * Every participant's presence, read from one conversation's view in a single
 * pass.
 *
 * Resolving it up front is what makes the comparator below consistent: a sort
 * whose ordering function re-read a live store could compare the same pair
 * differently at two points of the same sort. It is also the answer to N+1 —
 * one subscription for the section, one map, no per-row lookup into a store
 * that would have to be subscribed to N times to be watched.
 */
export function rosterPresenceStates(
  participants: readonly RosterParticipant[],
  presence: TargetPresence,
): Map<string, PresenceState> {
  const states = new Map<string, PresenceState>();
  for (const participant of participants) {
    if (states.has(participant.userId)) continue;
    states.set(participant.userId, selectTargetPresence(presence, participant.userId));
  }
  return states;
}

/**
 * The name a tiebreak compares: case-folded and accent-folded.
 *
 * Folding the accent is what keeps "Álvaro" beside "Alvaro" instead of after
 * "Zoe": the roster is sorted by code unit (see below), and every Latin-1
 * accented letter sorts above every unaccented one, so an unfolded comparison
 * would exile exactly the names this product's Portuguese-speaking users have.
 * The same two-step normalization the emoji catalogue and the channel-slug
 * field already use, for the same reason.
 */
function sortKey(displayName: string): string {
  return displayName
    .normalize("NFD")
    .replace(/\p{Diacritic}/gu, "")
    .toLowerCase();
}

/**
 * Orders a roster by presence, then deterministically.
 *
 * The tiebreak is the folded name, then `userId`, compared by code unit rather
 * than through `localeCompare`. That is the point: `localeCompare` answers from
 * the ICU collation data of whatever runtime is executing, and "deterministic
 * fallback" cannot mean "whatever this engine's tables say today". Code-unit
 * order over a folded key is the same answer in a browser, in a test runner and
 * in a server render. Two people with identical names are separated by their
 * ids, so the order is total and repeated renders cannot swap them.
 *
 * The keys are computed once per person, not once per comparison: a sort makes
 * O(n log n) comparisons and `normalize` is not free, and it also guarantees
 * the comparator is consistent — it cannot read a different value for the same
 * person at two points of the same sort.
 *
 * Duplicates are dropped by `userId`, first occurrence wins. The servers do not
 * produce them — one membership row per person — so this is not compensating
 * for a contract: a duplicate id would be two React children under one key,
 * which is a rendering fault rather than a display error, and this is the one
 * place every roster passes through.
 *
 * The input is never mutated.
 */
export interface RosterSortKey {
  /** Presence class, per {@link presenceRank}. */
  rank: number;
  /** The folded display name, per {@link sortKey}. */
  name: string;
  userId: string;
}

/**
 * The comparator, over the three keys and nothing else.
 *
 * Named and exported so the contract can be stated as a contract: a comparator
 * must be antisymmetric and must answer 0 for a value and itself, and a sort
 * that only ever calls it on distinct elements would never reveal whether it
 * does. The reflexive case is unreachable through `orderRoster` — the map below
 * has already made the ids unique — and is written and tested anyway, because
 * "unreachable today" is not the same as "correct".
 */
export function compareRosterKeys(a: RosterSortKey, b: RosterSortKey): number {
  if (a.userId === b.userId) return 0;
  if (a.rank !== b.rank) return a.rank - b.rank;
  if (a.name !== b.name) return a.name < b.name ? -1 : 1;
  return a.userId < b.userId ? -1 : 1;
}

export function orderRoster<T extends RosterParticipant>(
  participants: readonly T[],
  presenceOf: (userId: string) => PresenceState,
): T[] {
  const ordered = new Map<string, { participant: T } & RosterSortKey>();
  for (const participant of participants) {
    if (ordered.has(participant.userId)) continue;
    ordered.set(participant.userId, {
      participant,
      userId: participant.userId,
      rank: presenceRank[presenceOf(participant.userId)],
      name: sortKey(participant.displayName),
    });
  }
  return [...ordered.values()].sort(compareRosterKeys).map((entry) => entry.participant);
}
