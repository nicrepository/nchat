import { useState, type ReactNode } from "react";
import OwnershipActionDialog, { type OwnershipDialogContext } from "./OwnershipActionDialog";
import type { OwnershipAction } from "./ownershipPresentation";

export type OpenOwnershipAction = (action: OwnershipAction, trigger: HTMLElement) => void;

// Lives outside the conditional roster: a failed details refresh must not
// discard the open dialog or its request identity.
export default function OwnershipDialogs({
  context,
  status,
  children,
}: {
  context?: OwnershipDialogContext;
  status: "ready" | "loading" | "error";
  children: (open: OpenOwnershipAction) => ReactNode;
}) {
  const [opened, setOpened] = useState<OpenedOwnership | null>(null);
  if (shouldClose(opened, context, status)) setOpened(null);
  function open(action: OwnershipAction, trigger: HTMLElement) {
    if (context) setOpened({ action, context, trigger });
  }
  function close() {
    setOpened(null);
    restoreFocus(opened?.trigger);
  }
  const current = opened && context?.id === opened.context.id ? context : opened?.context;
  return (
    <>
      {children(open)}
      {opened && current && status !== "loading" && (
        <OwnershipActionDialog
          action={opened.action}
          props={{ ...current, projectionStatus: status === "error" ? "error" : "ready" }}
          onClose={close}
        />
      )}
    </>
  );
}

interface OpenedOwnership {
  action: OwnershipAction;
  context: OwnershipDialogContext;
  trigger: HTMLElement;
}
function shouldClose(
  opened: OpenedOwnership | null,
  context: OwnershipDialogContext | undefined,
  status: string,
) {
  if (!opened) return false;
  if (status === "loading") return true;
  if (!context) return status === "ready";
  return context.id !== opened.context.id || context.kind !== opened.context.kind;
}
function restoreFocus(trigger?: HTMLElement) {
  if (trigger?.isConnected) trigger.focus({ preventScroll: true });
  else
    document
      .querySelector<HTMLElement>("#ownership-roster-heading, .chat-details__close")
      ?.focus({ preventScroll: true });
}
