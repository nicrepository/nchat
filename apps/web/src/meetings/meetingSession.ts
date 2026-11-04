import { ApiRequestError } from "../lib/api";
import { createMeeting, inviteAll, meetingError } from "./meetingApi";
import { initialMeetingDraft, meetingPayload } from "./meetingTime";
import type { MeetingDraft, MeetingOperation, MeetingTarget } from "./meetingTypes";

export interface MeetingSnapshot {
  draft: MeetingDraft;
  baseline: MeetingDraft;
  operation: MeetingOperation;
  seeded: boolean;
}

/** In-memory, session-scoped draft and operation; never writes into chat state. */
export function createMeetingSession(
  target: MeetingTarget,
  organizerId: string,
  identityConfirmed = () => true,
) {
  const draft = initialMeetingDraft(target);
  let snapshot: MeetingSnapshot = {
    draft,
    baseline: draft,
    operation: { stage: "editing" },
    seeded: !target.direct,
  };
  let intent: { fingerprint: string; key: string } | undefined;
  let running = false;
  const listeners = new Set<() => void>();
  function update(patch: Partial<MeetingSnapshot>) {
    snapshot = { ...snapshot, ...patch };
    listeners.forEach((listener) => listener());
  }
  async function completeInvites(
    operation: Extract<MeetingOperation, { stage: "inviting" | "invite-error" }>,
  ) {
    update({ operation: { ...operation, stage: "inviting" } });
    try {
      if (!identityConfirmed())
        throw new ApiRequestError(401, "identity_unconfirmed", "Session confirmation pending");
      const added = await inviteAll(operation.meeting.id);
      update({ operation: { ...operation, stage: "complete", added } });
    } catch (error) {
      update({
        operation: { ...operation, stage: "invite-error", message: meetingError(error, true) },
      });
    }
  }
  async function create() {
    const payload = meetingPayload(snapshot.draft, target, organizerId);
    const fingerprint = JSON.stringify(payload);
    if (intent?.fingerprint !== fingerprint) intent = { fingerprint, key: crypto.randomUUID() };
    update({ operation: { stage: "creating" } });
    try {
      const result = await createMeeting(payload, intent.key);
      if (
        snapshot.draft.inviteAll &&
        ["scheduled", "in_progress"].includes(result.meeting.status)
      ) {
        await completeInvites({ stage: "inviting", ...result });
      } else update({ operation: { stage: "complete", ...result } });
    } catch (error) {
      const uncertain =
        !(error instanceof ApiRequestError) || error.status === 0 || error.status >= 500;
      update({ operation: { stage: "create-error", message: meetingError(error), uncertain } });
    }
  }
  return {
    target,
    identityConfirmed,
    getSnapshot: () => snapshot,
    subscribe: (listener: () => void) => {
      listeners.add(listener);
      return () => {
        listeners.delete(listener);
      };
    },
    update,
    async submit() {
      if (running || snapshot.operation.stage === "complete" || !identityConfirmed()) return;
      running = true;
      try {
        if (snapshot.operation.stage === "invite-error") await completeInvites(snapshot.operation);
        else await create();
      } finally {
        running = false;
      }
    },
    finishWithoutBulk() {
      if (snapshot.operation.stage !== "invite-error") return;
      const { meeting, replayed } = snapshot.operation;
      update({ operation: { stage: "complete", meeting, replayed } });
    },
  };
}
export type MeetingSession = ReturnType<typeof createMeetingSession>;
export function meetingDirty(snapshot: MeetingSnapshot) {
  return JSON.stringify(snapshot.draft) !== JSON.stringify(snapshot.baseline);
}
export function meetingBusy(operation: MeetingOperation) {
  return operation.stage === "creating" || operation.stage === "inviting";
}
export function meetingLocked(operation: MeetingOperation) {
  if (operation.stage === "editing") return false;
  if (operation.stage === "create-error") return operation.uncertain;
  return true;
}
