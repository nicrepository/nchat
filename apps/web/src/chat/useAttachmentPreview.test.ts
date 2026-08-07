/**
 * The two preview predicates (RF-31, issue #464).
 *
 * They are pure and they decide two different things, which is why they are
 * tested as a table over every combination the listing can actually return:
 *
 *   canShowPreview        may this attachment's *bytes* be requested;
 *   isPreviewWorkPending  can this attachment still change on its own.
 *
 * Getting the second one wrong is what shipped the bug this file exists for: it
 * required `clean`, so an attachment waiting for the malware scan — the initial
 * state of every upload in a deployment with scanning on — was treated as
 * finished, and the panel stopped watching before the work had even started.
 */

import { describe, expect, it } from "vitest";

import { canShowPreview, isPreviewWorkPending } from "./useAttachmentPreview";
import type { AttachmentPreviewStatus, AttachmentStatus, ChannelAttachment } from "./chatTypes";

function attachment(
  status: AttachmentStatus,
  previewStatus: AttachmentPreviewStatus,
): ChannelAttachment {
  return {
    id: "a-1",
    filename: "diagrama.png",
    contentType: "image/png",
    size: 2048,
    status,
    previewStatus,
    createdAt: "2026-07-15T12:00:00.000Z",
  };
}

describe("isPreviewWorkPending", () => {
  // Every combination the listing can return. The server only lists
  // pending_scan, clean and rejected — pending_upload, failed and deleted are
  // excluded from it — so this is the complete space, not a sample.
  const cases: Array<{
    status: AttachmentStatus;
    previewStatus: AttachmentPreviewStatus;
    pending: boolean;
    because: string;
  }> = [
    {
      status: "pending_scan",
      previewStatus: "pending",
      pending: true,
      because: "the scan can still approve, and the worker then renders",
    },
    {
      status: "clean",
      previewStatus: "pending",
      pending: true,
      because: "the preview worker can still finish",
    },
    {
      status: "clean",
      previewStatus: "ready",
      pending: false,
      because: "this is the destination",
    },
    {
      status: "clean",
      previewStatus: "failed",
      pending: false,
      because: "a failed render is terminal",
    },
    {
      status: "clean",
      previewStatus: "unsupported",
      pending: false,
      because: "there will never be a preview for this content",
    },
    {
      status: "rejected",
      previewStatus: "pending",
      pending: false,
      because: "a rejected attachment is never claimed by the preview worker",
    },
    {
      status: "rejected",
      previewStatus: "unsupported",
      pending: false,
      because: "the rejection already finalised the preview",
    },
    {
      status: "rejected",
      previewStatus: "ready",
      pending: false,
      because: "a preview from before the rejection is terminal and unservable",
    },
    {
      status: "rejected",
      previewStatus: "failed",
      pending: false,
      because: "both halves are terminal",
    },
    {
      status: "pending_scan",
      previewStatus: "unsupported",
      pending: false,
      because: "this content will never have a preview, whatever the scan says",
    },
    {
      status: "pending_scan",
      previewStatus: "ready",
      pending: false,
      because: "the preview is done; only delivery still waits on the scan",
    },
    {
      status: "pending_scan",
      previewStatus: "failed",
      pending: false,
      because: "a failed render is terminal",
    },
  ];

  it.each(cases)(
    "$status + $previewStatus is $pending because $because",
    ({ status, previewStatus, pending }) => {
      expect(isPreviewWorkPending(attachment(status, previewStatus))).toBe(pending);
    },
  );

  // The regression this predicate exists for: the initial state of the ordinary
  // flow must be watched, not written off.
  it("watches the initial state of an upload with the malware scan on", () => {
    expect(isPreviewWorkPending(attachment("pending_scan", "pending"))).toBe(true);
  });

  // And the state that would poll forever must not: a rejected attachment has
  // nothing left to wait for, however its preview column reads.
  it("never watches a rejected attachment", () => {
    for (const previewStatus of ["pending", "ready", "failed", "unsupported"] as const) {
      expect(isPreviewWorkPending(attachment("rejected", previewStatus))).toBe(false);
    }
  });
});

describe("canShowPreview", () => {
  it("is true only for a cleared attachment with a finished preview", () => {
    expect(canShowPreview(attachment("clean", "ready"))).toBe(true);
  });

  // The security-relevant half: waiting for the scan must never authorise a
  // request for the bytes. The server would answer 409, and asking anyway would
  // mean the client's rule and the server's had drifted.
  it.each([
    ["pending_scan", "ready"],
    ["pending_scan", "pending"],
    ["rejected", "ready"],
    ["rejected", "pending"],
    ["clean", "pending"],
    ["clean", "failed"],
    ["clean", "unsupported"],
  ] as Array<[AttachmentStatus, AttachmentPreviewStatus]>)(
    "is false for %s + %s",
    (status, previewStatus) => {
      expect(canShowPreview(attachment(status, previewStatus))).toBe(false);
    },
  );

  // The two predicates must never both be true: one says "wait", the other says
  // "fetch", and an attachment that did both would be fetched while still
  // being polled for.
  it("never overlaps with the reconciliation predicate", () => {
    for (const status of ["pending_scan", "clean", "rejected"] as const) {
      for (const previewStatus of ["pending", "ready", "failed", "unsupported"] as const) {
        const file = attachment(status, previewStatus);
        expect(canShowPreview(file) && isPreviewWorkPending(file)).toBe(false);
      }
    }
  });
});
