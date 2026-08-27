/**
 * The RF-09 cross-conversation quote a navigation is carrying (issue #496, CQ
 * follow-up).
 *
 * Someone quoting a message into another conversation arrives here with the
 * source in the router's location state. Reading it, validating it, resolving
 * the message it names and labelling where it came from were four steps
 * interleaved with everything else ChatMessageArea does; they are one subject,
 * and this is it.
 *
 * The location state is untrusted input like any other: it survives a reload, it
 * is writable from the address bar, and it decides which message this component
 * fetches. Every field is validated before use, and the fetch is authorized
 * server-side regardless.
 */

import { useEffect, useMemo, useState } from "react";

import { fetchChannelMessage, fetchDMMessage } from "./chatApi";
import type { PendingReferencePreview } from "./ChatComposer";
import type { Channel, DMConversation } from "./chatTypes";

export interface PendingReferenceLocation {
  messageId: string;
  targetKind: "channel" | "dm";
  targetId: string;
}

const UUID_PATTERN = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;
const NIL_UUID = "00000000-0000-0000-0000-000000000000";

function isValidUUID(value: unknown): value is string {
  return typeof value === "string" && UUID_PATTERN.test(value) && value.toLowerCase() !== NIL_UUID;
}

export function readPendingReference(state: unknown): PendingReferenceLocation | null {
  if (typeof state !== "object" || state === null) return null;
  const value = state as Record<string, unknown>;
  if (
    !isValidUUID(value.referencedMessageId) ||
    !isValidUUID(value.referenceTargetId) ||
    (value.referenceTargetKind !== "channel" && value.referenceTargetKind !== "dm")
  ) {
    return null;
  }
  return {
    messageId: value.referencedMessageId,
    targetKind: value.referenceTargetKind,
    targetId: value.referenceTargetId,
  };
}

/** Where the quote came from, as the composer's preview labels it. */
function originLabel(
  reference: PendingReferenceLocation | null,
  channels: Channel[],
  dms: DMConversation[],
): string {
  if (reference?.targetKind === "channel") {
    const name = channels.find((channel) => channel.id === reference.targetId)?.name;
    return name ? `#${name}` : "Canal";
  }
  if (reference?.targetKind === "dm") {
    return dms.find((dm) => dm.id === reference.targetId)?.name ?? "Conversa";
  }
  return "Conversa";
}

type ResolvedPreview = Extract<PendingReferencePreview, { status: "available" | "unavailable" }>;

export interface PendingReferenceState {
  /** Null when this navigation carries no quote. */
  reference: PendingReferenceLocation | null;
  /** The quoted message's id, or "" — what the send call passes through. */
  messageId: string;
  preview: PendingReferencePreview;
  originLabel: string;
}

export function usePendingReference(
  locationState: unknown,
  channels: Channel[],
  dms: DMConversation[],
): PendingReferenceState {
  const reference = useMemo(() => readPendingReference(locationState), [locationState]);
  const key = reference
    ? `${reference.targetKind}:${reference.targetId}:${reference.messageId}`
    : "";
  const [resolution, setResolution] = useState<{ key: string; preview: ResolvedPreview } | null>(
    null,
  );

  useEffect(() => {
    if (!reference) return;
    const { messageId, targetKind, targetId } = reference;
    const controller = new AbortController();
    const request =
      targetKind === "channel"
        ? fetchChannelMessage(targetId, messageId, controller.signal)
        : fetchDMMessage(targetId, messageId, controller.signal);
    request.then(
      (message) => {
        if (controller.signal.aborted) return;
        // A message that came back under a different id, or that has since been
        // removed, is "unavailable" rather than something to preview.
        const preview: ResolvedPreview =
          message.id === messageId && !message.isRemoved
            ? { status: "available", messageId, message }
            : { status: "unavailable", messageId };
        setResolution({ key, preview });
      },
      () => {
        // The reason is never surfaced: a refusal and a missing message must
        // look the same to a reader who may not know the source exists.
        if (!controller.signal.aborted) {
          setResolution({ key, preview: { status: "unavailable", messageId } });
        }
      },
    );
    return () => controller.abort();
  }, [reference, key]);

  const preview = useMemo<PendingReferencePreview>(() => {
    if (!reference) return { status: "idle" };
    return resolution?.key === key
      ? resolution.preview
      : { status: "loading", messageId: reference.messageId };
  }, [reference, key, resolution]);

  return {
    reference,
    messageId: reference?.messageId ?? "",
    preview,
    originLabel: originLabel(reference, channels, dms),
  };
}
