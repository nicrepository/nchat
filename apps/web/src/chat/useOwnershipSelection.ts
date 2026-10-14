import { useState } from "react";
import type { OwnershipDialogContext } from "./OwnershipActionDialog";
import type { OwnershipAction } from "./ownershipPresentation";
import type { OwnershipSubmitState } from "./ownershipDialogState";

function candidatesOf(props: OwnershipDialogContext) {
  return props.ownership.members.filter(
    (member) => member.actions.transfer === true && member.userId !== props.currentUserId,
  );
}
function selectionAllowed(
  action: OwnershipAction,
  props: OwnershipDialogContext,
  manual: boolean,
  validTarget: boolean,
) {
  if (action.type === "transfer" || manual) return validTarget;
  return action.type !== "leave" || !props.ownership.leavePreview.blocked;
}
function availability(
  action: OwnershipAction,
  props: OwnershipDialogContext,
  state: OwnershipSubmitState,
  manual: boolean,
  validTarget: boolean,
) {
  const busy = state.phase === "submitting";
  const refreshing = state.phase === "conflict";
  const uncertain = state.phase === "recoverable_error" && state.uncertain;
  const unavailable = props.projectionStatus === "error";
  const needsTarget = action.type === "transfer" || manual;
  const locked = busy || refreshing || unavailable;
  return {
    busy,
    refreshing,
    uncertain,
    unavailable,
    needsTarget,
    fieldsDisabled: locked || uncertain,
    canSubmit: !locked && (uncertain || selectionAllowed(action, props, manual, validTarget)),
  };
}
export function useOwnershipSelection(
  action: OwnershipAction,
  props: OwnershipDialogContext,
  state: OwnershipSubmitState,
  newIntent: () => void,
) {
  const [target, setTarget] = useState(action.type === "transfer" ? action.member.userId : "");
  const [actorRole, setActorRole] = useState<"admin" | "member">("member");
  const [manual, setManual] = useState(false);
  const candidates = candidatesOf(props);
  const selected = candidates.find((member) => member.userId === target);
  const successor = props.ownership.members.find(
    (member) => member.userId === props.ownership.leavePreview.successorUserId,
  );
  const flags = availability(action, props, state, manual, Boolean(selected));
  // A refreshed capability permanently clears a stale selection. An uncertain
  // operation retains its original input so the backend can resolve its replay.
  const [previous, setPrevious] = useState(props.ownership);
  if (previous !== props.ownership) {
    setPrevious(props.ownership);
    if (target && !selected && !flags.uncertain) setTarget("");
  }
  function changeTarget(value: string) {
    newIntent();
    setTarget(value);
  }
  function changeRole(value: "admin" | "member") {
    newIntent();
    setActorRole(value);
  }
  function toggleManual() {
    newIntent();
    setTarget("");
    setManual(!manual);
  }
  return {
    ...flags,
    target,
    actorRole,
    manual,
    candidates,
    selected,
    successor,
    changeTarget,
    changeRole,
    toggleManual,
  };
}
