/**
 * The header's side of a resource call (RF-24, issues #622 and #657).
 *
 * Lives here, next to the call surfaces, rather than in the conversation
 * component that happens to render it (issue #834): useResourceCallBar
 * produces this value and the header consumes it, so neither of them has any
 * business importing the composition root to name it.
 *
 * #657: the header only ever renders the "start a call" action. Once a call is
 * active — or this reader is participating in one — ActiveResourceCallBar takes
 * over presentation completely and the header receives undefined instead.
 */
export type ResourceCallHeaderState = { onCall: () => void; disabled?: boolean };
