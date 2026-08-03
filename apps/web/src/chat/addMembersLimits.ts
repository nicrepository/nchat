/**
 * The one add-members bound the browser needs to know (issue #398).
 *
 * It lives in its own module, next to dmGroupForm's constants and for the same
 * reason: it must agree with chat-service exactly, and agreement is easier to
 * keep on a named constant than on a number inlined in a component.
 *
 * This is not a check. `domain.MaxAddMembersPerRequest` in chat-service is the
 * limit; this only stops the UI from offering a selection the server would
 * refuse, so the user finds out at the 26th pick instead of at submit time.
 */
export const maxAddMembersPerRequest = 25;
