/**
 * Pure rules for the ad-hoc group form (RF-02).
 *
 * They exist apart from the dialog because they are the parts that must agree
 * with chat-service exactly, and agreement is much easier to prove on a function
 * than on a rendered component. None of them is an authorisation check: the
 * server re-validates every one, and these only keep the UI from offering an
 * action that would be refused.
 */

import type { DMCandidate } from "./chatTypes";

/** chat-service requires three participants counting the caller. */
export const MIN_GROUP_MEMBERS = 2;
/** chat-service caps a group at 50 participants counting the caller. */
export const MAX_GROUP_MEMBERS = 49;
/** chat-service measures the title with utf8.RuneCountInString — code points. */
export const MAX_GROUP_TITLE_CODE_POINTS = 120;

/**
 * Counts Unicode code points, which is what Go counts as runes.
 *
 * `String.length` counts UTF-16 units, so it reports 2 for any astral-plane
 * character — an emoji, a rare CJK ideograph, most historic scripts. Using it
 * against a rune-based server limit would silently halve the budget for those
 * users. Array.from iterates by code point, so both sides agree.
 *
 * Grapheme clusters are deliberately not the unit here: a flag or a
 * skin-toned emoji is several code points and the server counts it as several
 * runes too. Matching the server is the goal, not matching intuition.
 */
export function countCodePoints(value: string): number {
  return Array.from(value).length;
}

/** Returns value with at most `limit` code points, never splitting one in half. */
export function limitCodePoints(value: string, limit: number): string {
  const codePoints = Array.from(value);
  return codePoints.length <= limit ? value : codePoints.slice(0, limit).join("");
}

/**
 * Keeps a title within the server limit as the user types.
 *
 * Truncating on input (rather than accepting anything and rejecting on submit)
 * matches how the search field already behaves and means the submit button never
 * has to be blocked by a title. Trimming is NOT done here: it would eat the
 * space the moment it is typed. The API client trims when it serialises, which
 * is also where a blank title is dropped from the payload.
 */
export function limitGroupTitleInput(value: string): string {
  return limitCodePoints(value, MAX_GROUP_TITLE_CODE_POINTS);
}

/**
 * Adds or removes one person, by identity.
 *
 * A second toggle on the same person removes them, so the list can never hold a
 * duplicate; the cap is enforced by refusing the addition rather than by
 * dropping someone already chosen. Returns the same array reference when nothing
 * changes, so React can skip the re-render.
 */
export function toggleGroupMember(selected: DMCandidate[], candidate: DMCandidate): DMCandidate[] {
  if (selected.some((member) => member.userId === candidate.userId)) {
    return selected.filter((member) => member.userId !== candidate.userId);
  }
  if (selected.length >= MAX_GROUP_MEMBERS) return selected;
  return [...selected, candidate];
}
