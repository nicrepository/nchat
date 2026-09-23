/**
 * The removal flow of the details panel's people section (issue #469).
 *
 * Split out of ConversationDetailsPanel because it is a flow and not a
 * rendering: which person is being removed, what happens to focus when the row
 * they were on disappears, and what the panel does once the server has agreed.
 * The panel already owns the add flow; owning a second one inline is how a
 * section becomes a state machine with four booleans.
 *
 * The state is one value — the member under confirmation, or nothing — keyed
 * by the conversation it was opened for. Two consequences, both deliberate:
 * an invalid combination like "pending for a member nobody selected" cannot be
 * represented, and a conversation switch closes the dialog during render
 * rather than through an effect, so the confirmation for A can never post
 * against B.
 *
 * That key is also what makes a *finished* write land in the right place. A
 * DELETE already sent is allowed to complete — cancelling a destructive write
 * because the reader navigated would leave nobody able to say whether it
 * happened — but everything the panel does *afterwards* is about a
 * conversation, and by then the panel may be describing a different one. So
 * the operation carries the identity it was started for, and the local
 * effects run only while that identity is still the one on screen.
 *
 * What this hook does *not* own: the request's own pending state and its error
 * text live in the dialog, which is the only thing that renders them.
 */

import { useCallback, useLayoutEffect, useRef, useState } from "react";

import { removeChannelMember, removeGroupParticipant } from "./chatApi";
import type { RemoveMemberTarget } from "./RemoveMemberDialog";
import type { RosterParticipant } from "./participantRosterOrder";

/**
 * The conversation an operation belongs to.
 *
 * A pair, not an id: a channel and a conversation are separate id spaces, so
 * the same string can name two different aggregates and only both fields
 * together identify one.
 */
interface RemovalTarget {
  kind: "channel" | "group";
  targetId: string;
}

function sameTarget(left: RemovalTarget, right: RemovalTarget): boolean {
  return left.kind === right.kind && left.targetId === right.targetId;
}

export interface MemberRemovalInput {
  kind: "channel" | "group";
  /** The conversation the section is describing right now; "" while loading. */
  targetId: string;
  /** The panel's single reconciliation path — refetches details and roster. */
  reload: () => void;
  /**
   * Where focus goes once the removed row is gone. The row's own button is
   * unmounted by then, so returning focus to it would drop it to <body>.
   */
  fallbackFocusRef: React.RefObject<HTMLElement | null>;
}

export interface MemberRemovalFlow {
  /** The member under confirmation for the current conversation, or null. */
  member: RemoveMemberTarget | null;
  /** What the live region announces after a successful removal, or "". */
  notice: string;
  /** Opens the confirmation for one person, remembering what opened it. */
  request: (participant: RosterParticipant, trigger: HTMLElement) => void;
  /** Closes without removing anything and returns focus to the trigger. */
  cancel: () => void;
  /** Performs the removal. Rejects with the API error so the dialog can show it. */
  confirm: () => Promise<void>;
}

/** The word each conversation uses for the person who just left it. */
const removedNotice = {
  channel: (name: string) => `${name} foi removido do canal.`,
  group: (name: string) => `${name} foi removido do grupo.`,
} as const;

export function useMemberRemoval(input: MemberRemovalInput): MemberRemovalFlow {
  const { kind, targetId, reload, fallbackFocusRef } = input;
  const [pendingFor, setPendingFor] = useState<{
    target: RemovalTarget;
    member: RemoveMemberTarget;
  } | null>(null);
  const [notice, setNotice] = useState("");
  const triggerRef = useRef<HTMLElement | null>(null);

  // Whether this flow still belongs to a panel that is on screen.
  //
  // A DELETE already sent is never cancelled, so its continuation can arrive
  // after the panel is gone — and a ref holding the last conversation would
  // still answer "yes, that is the one", letting a dead panel refetch and take
  // focus. Lifecycle and identity are separate questions and are asked
  // separately: this one is "is there still a panel", the one below is "is it
  // still describing the same conversation".
  //
  // In the layout phase, like the target below and for the same reason: an
  // unmount commits synchronously, and a promise settling right after it must
  // already see `false`.
  const mountedRef = useRef(true);
  useLayoutEffect(() => {
    mountedRef.current = true;
    return () => {
      mountedRef.current = false;
    };
  }, []);

  // The conversation the panel is describing *now*: a callback that is already
  // awaiting cannot ask its closure, which still holds the conversation the
  // user was looking at when they confirmed.
  //
  // Synchronised in a *layout* effect, not a passive one. A passive effect can
  // be scheduled as a task of its own after the commit that switched the
  // conversation, and a promise settling in between resolves on the microtask
  // queue — while a passively-written ref would still name the conversation
  // the reader left. The completion of A would then be applied to the B
  // already on screen. A layout effect runs inside the commit itself, so there
  // is no interval in which the ref and the screen disagree.
  //
  // Written in an effect rather than during render, because a render can be
  // discarded and a ref updated by a discarded one would name a conversation
  // never shown — the reason concurrent rendering forbids that shortcut.
  const currentTargetRef = useRef<RemovalTarget>({ kind, targetId });
  useLayoutEffect(() => {
    currentTargetRef.current = { kind, targetId };
  }, [kind, targetId]);

  // Open only while the conversation it was opened for is still on screen. The
  // comparison closes it during render — the dialog unmounts and takes its
  // pending state with it — which is the same mechanism the add flow uses.
  const member =
    pendingFor !== null && sameTarget(pendingFor.target, { kind, targetId }) && targetId !== ""
      ? pendingFor.member
      : null;

  const request = useCallback(
    (participant: RosterParticipant, trigger: HTMLElement) => {
      triggerRef.current = trigger;
      setNotice("");
      setPendingFor({
        target: { kind, targetId },
        member: { userId: participant.userId, displayName: participant.displayName },
      });
    },
    [kind, targetId],
  );

  const cancel = useCallback(() => {
    setPendingFor(null);
    // Back to the control that opened the dialog, which is still there
    // precisely because nothing was removed. `isConnected` covers the one case
    // where it is not: a refetch that revoked the capability while the dialog
    // was open unmounted the row's button.
    const trigger = triggerRef.current;
    triggerRef.current = null;
    if (trigger?.isConnected) trigger.focus();
  }, []);

  const confirm = useCallback(async () => {
    if (member === null || targetId === "") return;
    // The identity this operation belongs to, frozen before the request: what
    // follows the await must be decided against it and not against whatever
    // the closure happens to hold.
    const target: RemovalTarget = { kind, targetId };
    // Only the two identifiers the route names. The workspace, the actor and
    // the authority behind the call are the session's, server-side.
    //
    // Not aborted on unmount, unlike the panel's reads: this is a destructive
    // write, and cancelling one in flight would leave nobody — user or client
    // — able to say whether it happened. It is allowed to finish, and the next
    // authorized read is what the UI believes.
    if (target.kind === "channel") {
      await removeChannelMember(target.targetId, member.userId);
    } else {
      await removeGroupParticipant(target.targetId, member.userId);
    }
    // Past this point everything is local to a panel: state it renders, a
    // refetch it owns, focus inside it. The write itself is already done and
    // is never undone by what follows — the two questions are asked in the
    // order they can be answered.
    //
    // No panel left: nothing local to do. The removal stands and the next
    // mount reads it back from the server.
    if (!mountedRef.current) return;
    // This operation's own dialog state, cleared wherever the reader is now:
    // it belongs to the conversation just written to, and leaving it behind
    // would reopen a confirmation for somebody already removed on the way
    // back. A confirmation opened meanwhile for another conversation is left
    // exactly as it is.
    setPendingFor((current) =>
      current !== null && sameTarget(current.target, target) ? null : current,
    );
    // Everything below describes a conversation to whoever is looking at it.
    // If that is no longer this one, the write stands and the panel says
    // nothing: the reader did not remove anybody from what they are reading
    // now, their focus is where they put it, and the conversation on screen
    // has no reason to refetch.
    if (!sameTarget(currentTargetRef.current, target)) return;
    setNotice(removedNotice[target.kind](member.displayName));
    // Catching up with a write that has already committed is not part of the
    // write. The dialog turns a rejection from here into "não foi possível
    // remover" and offers a retry, which would send a second DELETE for
    // somebody already gone — so a reconciliation that fails stays a
    // reconciliation failure, which the section renders as its own error
    // state after refetching.
    try {
      // The single reconciliation path: nothing is spliced out of the rendered
      // list and no counter is decremented here. The server is asked again, so
      // this removal and a concurrent one by somebody else converge on one
      // answer instead of two local edits.
      reload();
      // The row is about to disappear, so focus moves to something that will
      // still be there. The live region above announces what happened for
      // anyone who cannot see that the list got shorter.
      fallbackFocusRef.current?.focus();
    } catch {
      // Best-effort, unlike the removal itself.
    }
    triggerRef.current = null;
  }, [fallbackFocusRef, kind, member, reload, targetId]);

  return { member, notice, request, cancel, confirm };
}
