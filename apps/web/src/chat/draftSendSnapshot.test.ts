/**
 * consumeSnapshot / sendSnapshotOf (issue #929) — the pure reconciliation a
 * confirmed send performs on its draft, field by field, by identity.
 */

import { describe, expect, it } from "vitest";

import { consumeSnapshot, sendSnapshotOf, type SendSnapshot } from "./draftSendSnapshot";
import type { ConversationDraft, DraftVoiceMessage } from "./useConversationDrafts";
import type { AttachmentUploadItem } from "./useAttachmentUpload";
import type { TTNode } from "./tiptapSerializer";

const textDoc = (text: string): TTNode => ({
  type: "doc",
  content: [{ type: "paragraph", content: [{ type: "text", text }] }],
});

function attachment(localId: string): AttachmentUploadItem {
  return {
    localId,
    file: new File(["x"], `${localId}.txt`),
    status: "success",
    progress: null,
    error: null,
    attachment: {
      id: `server-${localId}`,
      filename: `${localId}.txt`,
      contentType: "text/plain",
      size: 1,
      status: "pending_scan",
      previewStatus: "pending",
      createdAt: "",
    },
  };
}

function voice(id: string): DraftVoiceMessage {
  return {
    id,
    blob: new Blob(["x"]),
    previewUrl: `blob:${id}`,
    durationMs: 100,
    mimeType: "audio/webm",
  };
}

/** T1 / R1 / X / V1 — the composite draft the send below carries out. */
function compositeDraft(): ConversationDraft {
  return {
    text: textDoc("T1"),
    textRevision: 3,
    replyToMessageId: "R1",
    attachments: [attachment("X")],
    voiceMessage: voice("V1"),
    revision: 7,
    updatedAt: 1,
  };
}

const everything = { text: true, attachmentLocalIds: ["X"], voice: true };

describe("sendSnapshotOf", () => {
  it("captures the identity of every part the send carries", () => {
    expect(sendSnapshotOf("dm:a", compositeDraft(), everything)).toEqual<SendSnapshot>({
      draftKey: "dm:a",
      textRevision: 3,
      replyToMessageId: "R1",
      attachmentLocalIds: ["X"],
      voiceId: "V1",
    });
  });

  it("claims no text and no voice when the send carries neither (a voice note carries only its voice)", () => {
    const voiceOnly = sendSnapshotOf("dm:a", compositeDraft(), {
      text: false,
      attachmentLocalIds: [],
      voice: true,
    });
    expect(voiceOnly.textRevision).toBeNull();
    expect(voiceOnly.voiceId).toBe("V1");
    expect(voiceOnly.replyToMessageId).toBe("R1");
    expect(voiceOnly.attachmentLocalIds).toEqual([]);
  });

  it("is well-defined for a conversation with no draft at all", () => {
    expect(sendSnapshotOf("dm:none", undefined, everything)).toEqual<SendSnapshot>({
      draftKey: "dm:none",
      textRevision: 0,
      replyToMessageId: null,
      attachmentLocalIds: ["X"],
      voiceId: null,
    });
  });
});

describe("consumeSnapshot", () => {
  it("leaves nothing when the draft still holds exactly what the snapshot sent", () => {
    const draft = compositeDraft();
    const next = consumeSnapshot(draft, sendSnapshotOf("dm:a", draft, everything));
    expect(next).toMatchObject({
      text: null,
      replyToMessageId: null,
      attachments: [],
      voiceMessage: null,
    });
  });

  it("consumes only the snapshot and preserves everything composed after the submit (T2/R2/Z/V2)", () => {
    const before = compositeDraft();
    const snapshot = sendSnapshotOf("dm:a", before, everything);
    const during: ConversationDraft = {
      ...before,
      text: textDoc("T2"),
      textRevision: before.textRevision + 1,
      replyToMessageId: "R2",
      attachments: [attachment("X"), attachment("Z")],
      voiceMessage: voice("V2"),
    };

    const next = consumeSnapshot(during, snapshot);

    expect(next.text).toEqual(textDoc("T2"));
    expect(next.replyToMessageId).toBe("R2");
    expect(next.attachments.map((item) => item.localId)).toEqual(["Z"]);
    expect(next.voiceMessage?.id).toBe("V2");
  });

  it("reply: consumed when still R1, preserved when swapped for R2", () => {
    const draft = compositeDraft();
    const snapshot = sendSnapshotOf("dm:a", draft, everything);
    expect(consumeSnapshot(draft, snapshot).replyToMessageId).toBeNull();
    expect(consumeSnapshot({ ...draft, replyToMessageId: "R2" }, snapshot).replyToMessageId).toBe(
      "R2",
    );
  });

  it("attachments: X goes and Z stays; an X removed before the ACK is not resurrected", () => {
    const draft = compositeDraft();
    const snapshot = sendSnapshotOf("dm:a", draft, everything);
    const withZ = { ...draft, attachments: [attachment("X"), attachment("Z")] };
    expect(consumeSnapshot(withZ, snapshot).attachments.map((item) => item.localId)).toEqual(["Z"]);
    const withoutX = { ...draft, attachments: [attachment("Z")] };
    expect(consumeSnapshot(withoutX, snapshot).attachments.map((item) => item.localId)).toEqual([
      "Z",
    ]);
  });

  it("voice: consumed when still V1, preserved when V2 replaced it", () => {
    const draft = compositeDraft();
    const snapshot = sendSnapshotOf("dm:a", draft, everything);
    expect(consumeSnapshot(draft, snapshot).voiceMessage).toBeNull();
    expect(
      consumeSnapshot({ ...draft, voiceMessage: voice("V2") }, snapshot).voiceMessage?.id,
    ).toBe("V2");
  });

  it("text: consumed only at the revision that was submitted", () => {
    const draft = compositeDraft();
    const snapshot = sendSnapshotOf("dm:a", draft, everything);
    expect(consumeSnapshot(draft, snapshot).text).toBeNull();
    const edited = { ...draft, text: textDoc("T2"), textRevision: draft.textRevision + 1 };
    expect(consumeSnapshot(edited, snapshot).text).toEqual(textDoc("T2"));
  });

  it("a voice note's acknowledgement never touches the text sitting under it", () => {
    const draft = compositeDraft();
    const snapshot = sendSnapshotOf("dm:a", draft, {
      text: false,
      attachmentLocalIds: [],
      voice: true,
    });
    const next = consumeSnapshot(draft, snapshot);
    expect(next.text).toEqual(textDoc("T1"));
    expect(next.attachments.map((item) => item.localId)).toEqual(["X"]);
    expect(next.voiceMessage).toBeNull();
    expect(next.replyToMessageId).toBeNull();
  });

  it("does not touch the draft's bookkeeping fields", () => {
    const draft = compositeDraft();
    const next = consumeSnapshot(draft, sendSnapshotOf("dm:a", draft, everything));
    expect(next.revision).toBe(draft.revision);
    expect(next.textRevision).toBe(draft.textRevision);
  });
});
