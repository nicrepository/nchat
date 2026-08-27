/**
 * The conversation details panel's own state: whether it is open, what it is
 * about, and when it has to reload.
 *
 * Split out of ChatMessageArea (issue #496, CQ follow-up). Its open flag, the
 * domain discriminant it renders from, its focus restoration, its accessible
 * label and the two effects that keep it in step with a rename were spread
 * through a component that also owns messages, the composer and calls. They are
 * one subject and now have one home.
 *
 * The state deliberately lives outside the message list: this hook's owner is
 * not remounted when messages change or when the route parameter changes, so
 * toggling the panel cannot restart useMessages, the WebSocket subscription, the
 * composer or the scroll position — and the panel stays open across a switch.
 */

import { useCallback, useEffect, useMemo, useRef, useState } from "react";

import type { DMConversation } from "./chatTypes";
import { useConversationDetails, type ConversationDetailsState } from "./useConversationDetails";

export type ConversationDetailsKind = "channel" | "group" | "direct";

export interface ConversationDetailsPanelInput {
  kind: "channel" | "dm";
  targetId: string;
  /** The sidebar's row for this DM, which carries the server's own type. */
  activeDM: DMConversation | undefined;
  /**
   * The control that opens the panel. Owned by the caller so it can be handed
   * straight to an element as a ref prop; this hook only focuses it on close.
   */
  toggleRef: React.RefObject<HTMLButtonElement | null>;
  /** The name the header renders, watched so a rename reloads an open panel. */
  resolvedName: string;
}

export interface ConversationDetailsPanelState {
  detailsKind: ConversationDetailsKind | null;
  supportsDetails: boolean;
  /** True only when the panel is both supported here and open. */
  showDetails: boolean;
  detailsState: ConversationDetailsState;
  toggleLabel: string;
  toggle: () => void;
  close: () => void;
  /** Refetch for callers that learn the panel's data changed under it. */
  reload: () => void;
}

/**
 * Which aggregate the panel would describe, from the domain discriminant:
 * chat.channels for a channel, and a chat.dm_conversations row of type 'group'
 * or 'direct' for a conversation. Only an unknown target resolves to null.
 *
 * activeDM.type is the server's own value carried by the sidebar payload, never
 * the participant count or the conversation's name: a group that happens to have
 * two people is still a group, and a 1:1 whose title looks like a group's is
 * still a 1:1. The same discriminant decides whether the panel offers "Adicionar
 * membros" (issue #398) — a channel and a group do, a 1:1 does not, because
 * adding a third person would convert a direct conversation into a group.
 */
function detailsKindFor(input: ConversationDetailsPanelInput): ConversationDetailsKind | null {
  if (input.targetId === "") return null;
  if (input.kind === "channel") return "channel";
  if (input.activeDM === undefined) return null;
  return input.activeDM.type === "group" ? "group" : "direct";
}

/**
 * A group's control names the panel; a 1:1's names the person it is about,
 * because "Detalhes da conversa" would be an odd thing to hear announced for a
 * panel that shows one profile. The name comes from the sidebar payload the
 * header already renders, so the label never waits on a request; when the
 * counterpart could not be resolved it degrades to the conversation itself
 * rather than to a blank or an ID.
 */
function toggleLabelFor(
  detailsKind: ConversationDetailsKind | null,
  activeDM: DMConversation | undefined,
): string {
  if (detailsKind !== "direct")
    return detailsKind === "channel" ? "Detalhes do canal" : "Detalhes do grupo";
  const counterpart = activeDM?.counterpart?.displayName;
  return counterpart ? `Abrir perfil de ${counterpart}` : "Abrir perfil da conversa";
}

/**
 * Reloads an open panel when the conversation is renamed under it.
 *
 * A rename lands in the sidebar payload first — through this actor's own
 * refetch, or through channel.updated for everybody else — and the header reads
 * its name straight from there. The panel does not: it holds its own
 * display_name from GET /details, so without this it would keep showing the old
 * one until it was closed and reopened (issue #527).
 *
 * The name is watched *together with the target's identity*, and that pairing is
 * the point. Switching conversations also changes the name, but there
 * useConversationDetails is already loading the new target from its own effect —
 * reloading here too would abort that request and issue a second one for the
 * same panel. So a changed identity is left alone, and only a name that moved
 * under the same conversation triggers a refetch.
 */
function useReloadOnRename(key: string, name: string, open: boolean, reload: () => void): void {
  const last = useRef({ key, name });
  useEffect(() => {
    const previous = last.current;
    last.current = { key, name };
    if (previous.key !== key) return;
    if (previous.name === name) return;
    if (open) reload();
  }, [key, name, open, reload]);
}

/**
 * Returns focus to the control that opened the panel — after the render that
 * closes it, never during the click that asked for it.
 *
 * Below the wide-desktop threshold (issue #467) the panel covers the
 * conversation and the header is hidden underneath it, and an element inside a
 * hidden subtree cannot take focus; by the time this effect runs the header is
 * on screen again. The flag makes only a real close restore focus, and the ref
 * makes a control that is no longer mounted — after a switch from a channel to a
 * DM it is not — never drop focus to <body>.
 */
function useRestoreFocusOnClose(
  open: boolean,
  toggleRef: React.RefObject<HTMLButtonElement | null>,
) {
  const pending = useRef(false);
  useEffect(() => {
    if (open || !pending.current) return;
    pending.current = false;
    toggleRef.current?.focus();
  }, [open, toggleRef]);
  return pending;
}

export function useConversationDetailsPanel(
  input: ConversationDetailsPanelInput,
): ConversationDetailsPanelState {
  const { kind, targetId, activeDM, resolvedName, toggleRef } = input;
  const [open, setOpen] = useState(false);

  const detailsKind = detailsKindFor(input);
  const target = useMemo(
    () => (detailsKind && open ? { kind: detailsKind, id: targetId } : null),
    [detailsKind, open, targetId],
  );
  const detailsState = useConversationDetails(target);
  const reload = detailsState.reload;

  useReloadOnRename(`${kind}:${targetId}`, resolvedName, open, reload);
  const restorePendingRef = useRestoreFocusOnClose(open, toggleRef);

  const close = useCallback(() => {
    restorePendingRef.current = true;
    setOpen(false);
  }, [restorePendingRef]);
  const toggle = useCallback(() => setOpen((current) => !current), []);

  return {
    detailsKind,
    supportsDetails: detailsKind !== null,
    showDetails: detailsKind !== null && open,
    detailsState,
    toggleLabel: toggleLabelFor(detailsKind, activeDM),
    toggle,
    close,
    reload,
  };
}
