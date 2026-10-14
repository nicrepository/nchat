import { useEffect, useLayoutEffect, useReducer, useRef } from "react";
import { randomId } from "../lib/randomId";
import {
  assignConversationRole,
  leaveOwnedConversation,
  transferConversationOwnership,
} from "./ownershipApi";
import type { OwnershipAction } from "./ownershipPresentation";
import { ownershipCopy, ownershipFailure, type OwnershipSubmitState } from "./ownershipDialogState";
import type { OwnershipDialogContext } from "./OwnershipActionDialog";

export interface OwnershipSelection {
  target: string;
  actorRole: "admin" | "member";
}

async function execute(
  action: OwnershipAction,
  props: OwnershipDialogContext,
  selection: OwnershipSelection,
  key: string,
) {
  if (action.type === "role")
    return assignConversationRole(props.kind, props.id, action.member.userId, action.role);
  if (action.type === "leave" && !selection.target)
    return leaveOwnedConversation(props.kind, props.id);
  return transferConversationOwnership(
    props.kind,
    props.id,
    selection.target,
    selection.actorRole,
    action.type === "leave",
    key,
  );
}

export function useOwnershipSubmit(
  action: OwnershipAction,
  props: OwnershipDialogContext,
  onClose: () => void,
) {
  const [state, transition] = useReducer(
    (_: OwnershipSubmitState, next: OwnershipSubmitState) => next,
    { phase: "selecting" },
  );
  const pending = useRef(false);
  const mounted = useRef(true);
  const key = useRef("");
  useLayoutEffect(() => {
    mounted.current = true;
    return () => {
      mounted.current = false;
    };
  }, []);
  useEffect(() => {
    if (
      state.phase === "conflict" &&
      props.projectionStatus !== "error" &&
      props.ownership !== state.previous
    )
      transition({ phase: "recoverable_error", error: state.error, uncertain: false });
  }, [state, props.ownership, props.projectionStatus]);

  function newIntent() {
    key.current = "";
  }
  async function submit(selection: OwnershipSelection) {
    if (pending.current || state.phase === "conflict") return;
    pending.current = true;
    if (!key.current) key.current = randomId();
    transition({ phase: "submitting" });
    try {
      await execute(action, props, selection, key.current);
      if (!mounted.current) return;
      transition({ phase: "success" });
      props.onCommitted?.(ownershipCopy.done);
      onClose();
      props.reload();
    } catch (error) {
      if (!mounted.current) return;
      const failure = ownershipFailure(error, props.ownership);
      transition(failure);
      if (failure.phase === "conflict") props.reload();
    } finally {
      pending.current = false;
    }
  }
  return { state, submit, newIntent, pending };
}
