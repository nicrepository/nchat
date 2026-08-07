/**
 * file-service API client (issue #435).
 *
 * Separate from chatApi because the gateway routes it to a different service
 * under a different prefix (/api/files, stripped before file-service sees it).
 * Auth is authenticatedFetch's, exactly like chatApi's: no token is stored,
 * read or placed in a URL here.
 *
 * This module lists attachment *metadata* only. Content lives behind
 * GET /api/files/attachments/{id}/content, which requires a Bearer header and
 * refuses anything the scan has not cleared — so there is deliberately no URL
 * built from a filename or an ID anywhere in this file.
 */

import { ApiRequestError } from "../lib/api";
import { authenticatedFetch } from "../lib/authClient";
import { formatUploadLimit } from "../lib/uploadLimit";
import type { AttachmentPreviewStatus, AttachmentStatus, ChannelAttachment } from "./chatTypes";

const FILES_BASE = import.meta.env.VITE_FILES_API_BASE_URL ?? "/api/files";

interface AttachmentResponse {
  id?: unknown;
  filename?: unknown;
  contentType?: unknown;
  size?: unknown;
  status?: unknown;
  previewStatus?: unknown;
  createdAt?: unknown;
}

interface AttachmentsEnvelope {
  data: { attachments?: unknown };
}

/**
 * Accepts only the statuses the listing contract defines. An unknown value
 * degrades to "pending_scan" — the conservative reading, since the UI keys
 * "not downloadable" off anything that is not "clean" and must never promote an
 * unrecognised state to clean.
 */
function attachmentStatus(raw: unknown): AttachmentStatus {
  return raw === "clean" || raw === "rejected" ? raw : "pending_scan";
}

/**
 * Accepts only the four states the preview contract defines. Anything else —
 * an older server that publishes no field at all, or a state this build does
 * not know — degrades to "unsupported", which is the conservative reading: the
 * UI shows the icon and the download action, and never promises a preview that
 * may not exist.
 */
function attachmentPreviewStatus(raw: unknown): AttachmentPreviewStatus {
  return raw === "pending" || raw === "ready" || raw === "failed" ? raw : "unsupported";
}

function mapAttachment(raw: unknown): ChannelAttachment | undefined {
  if (typeof raw !== "object" || raw === null) return undefined;
  const item = raw as AttachmentResponse;
  if (typeof item.id !== "string" || item.id === "") return undefined;
  return {
    id: item.id,
    filename: typeof item.filename === "string" ? item.filename : "",
    contentType: typeof item.contentType === "string" ? item.contentType : "",
    size: typeof item.size === "number" && Number.isFinite(item.size) ? item.size : 0,
    status: attachmentStatus(item.status),
    previewStatus: attachmentPreviewStatus(item.previewStatus),
    createdAt: typeof item.createdAt === "string" ? item.createdAt : "",
  };
}

/**
 * Lists one destination's most recent attachments, newest first.
 *
 * The destination kind selects the route, and the two routes are separate
 * resources on the server with separate authorization — a channel id can never
 * reach the conversation listing or the other way round.
 *
 * `limit` is a request, not a guarantee: the server clamps it and owns the
 * ordering, so a caller cannot ask for an unbounded scan or a different sort.
 */
export async function fetchConversationAttachments(
  target: { kind: "channel" | "dm"; id: string },
  limit: number,
  signal?: AbortSignal,
): Promise<ChannelAttachment[]> {
  const collection = target.kind === "channel" ? "channels" : "dm";
  const res = await authenticatedFetch<AttachmentsEnvelope>(
    `${FILES_BASE}/${collection}/${encodeURIComponent(target.id)}/attachments?limit=${encodeURIComponent(limit)}`,
    { method: "GET", signal },
  );
  const raw = res.data.attachments;
  if (!Array.isArray(raw)) return [];
  return raw
    .map(mapAttachment)
    .filter((attachment): attachment is ChannelAttachment => attachment !== undefined);
}

// ── Inline preview (RF-31, issue #464) ───────────────────────────────────────

/**
 * Fetches one attachment's preview image.
 *
 * The bytes come back as a Blob because that is the only shape a browser can
 * turn into something an `<img>` will render without a URL that carries
 * credentials. The request itself is an ordinary authenticated one: the bearer
 * token travels in the header, exactly as it does for the listing, and never in
 * a query string where it would land in history, logs and referrers.
 *
 * There is deliberately no URL built here that anyone could hold on to. The
 * server refuses this route without a valid session and re-checks the
 * attachment's visibility and its scan state on every call, so a preview is
 * never more durable, or more shareable, than the caller's own access.
 *
 * A preview that is not servable answers 409 and a caller that cannot see the
 * attachment answers 404 — both surface as ApiRequestError, and both mean the
 * same thing to the UI: draw the fallback.
 */
export async function fetchAttachmentPreview(
  attachmentId: string,
  signal?: AbortSignal,
): Promise<Blob> {
  return authenticatedFetch<Blob>(
    `${FILES_BASE}/attachments/${encodeURIComponent(attachmentId)}/preview`,
    { method: "GET", signal },
    (response) => response.blob(),
  );
}

/**
 * Fetches one attachment's decrypted content.
 *
 * Same authentication and the same server-side gates as every other route here:
 * the bearer token travels in the header, never in a query string, and the
 * server re-checks the attachment's visibility and its scan state on every call.
 * No URL is built that anyone could hold on to or share.
 *
 * The bytes come back as a Blob because that is the only shape a browser can
 * turn into something a media element will play without a URL that carries
 * credentials. That is also this function's limit, and the reason its caller
 * caps what it will ask for: a Blob is the whole file in memory, so the server's
 * byte-range support — which exists precisely so a player can seek without
 * downloading everything — is not what is being used here. See
 * MAX_INLINE_VIDEO_BYTES in AttachmentVideo.
 *
 * A file the scan has not cleared answers 409 and one the caller cannot see
 * answers 404 — both surface as ApiRequestError, and both mean the same thing to
 * the UI: there is nothing to play.
 */
export async function fetchAttachmentContent(
  attachmentId: string,
  signal?: AbortSignal,
): Promise<Blob> {
  return authenticatedFetch<Blob>(
    `${FILES_BASE}/attachments/${encodeURIComponent(attachmentId)}/content`,
    { method: "GET", signal },
    (response) => response.blob(),
  );
}

// ── Upload (RF-32, issue #458) ───────────────────────────────────────────────

export type AttachmentUploadErrorReason =
  | "too_large"
  | "unsupported"
  | "forbidden"
  | "unavailable"
  | "unknown";

/**
 * A rejected upload, carrying a typed reason so the UI can show the right
 * message instead of parsing whatever text came back. Mirrors
 * AvatarUploadError in profileApi.ts, which solves the same problem for the
 * avatar upload.
 */
export class AttachmentUploadError extends Error {
  readonly reason: AttachmentUploadErrorReason;

  constructor(reason: AttachmentUploadErrorReason, message: string) {
    super(message);
    this.name = "AttachmentUploadError";
    this.reason = reason;
  }
}

/**
 * The one message that has to name the limit, in the unit the policy is defined
 * in. When the limit is unknown — the server published none — the message says
 * so without inventing a number this client cannot know.
 */
export function tooLargeMessage(maxUploadBytes: number | null): string {
  if (maxUploadBytes === null) return "O arquivo excede o limite permitido.";
  return `O arquivo excede o limite permitido de ${formatUploadLimit(maxUploadBytes)}.`;
}

/**
 * Turns a failed upload into a typed reason.
 *
 * The HTTP status leads and the error code only refines it. That ordering is
 * deliberate: an oversized body can be refused by the gateway before it ever
 * reaches file-service, and the gateway answers 413 with its own body — not the
 * service's `{error:{code,message}}` envelope — so `code` is absent exactly when
 * the size is the problem. Keying on the code alone would show a generic error
 * for the one case the user can actually fix.
 *
 * No server text is ever surfaced: it may carry detail that does not belong in
 * the UI.
 */
function mapUploadError(error: unknown, maxUploadBytes: number | null): AttachmentUploadError {
  if (error instanceof ApiRequestError) {
    if (error.status === 413 || error.code === "payload_too_large") {
      return new AttachmentUploadError("too_large", tooLargeMessage(maxUploadBytes));
    }
    switch (error.status) {
      case 415:
        return new AttachmentUploadError("unsupported", "Formato de arquivo não suportado.");
      case 401:
      case 403:
      case 404:
        return new AttachmentUploadError("forbidden", "Você não pode enviar arquivos aqui.");
      case 503:
        return new AttachmentUploadError(
          "unavailable",
          "O envio de arquivos está indisponível no momento.",
        );
    }
  }
  return new AttachmentUploadError("unknown", "Não foi possível enviar o arquivo.");
}

/**
 * Uploads one file to a channel or a conversation.
 *
 * `maxUploadBytes` is the workspace's effective limit, obtained from
 * `fetchWorkspaceUploadLimit`, or null when the server published none. Checking
 * `file.size` against it here saves the user from spending their upload
 * bandwidth on a request that cannot succeed — it is not a control.
 * file-service re-reads the policy from the destination's own row and counts
 * the bytes it actually receives, so a caller that skips or edits this check is
 * refused all the same, and its 413 wins over this estimate. A null limit
 * therefore skips the pre-flight check and leaves the decision entirely to the
 * service; it never means "no limit".
 *
 * The comparison is `>` on two integers: a file of exactly the limit is
 * accepted, and nothing is rounded or coerced to a float on the way.
 *
 * Exactly one file per request, in the field named `file`: that is the contract
 * the service accepts, and a second part is rejected server-side.
 */
export async function uploadAttachment(
  target: { kind: "channel" | "dm"; id: string },
  file: File,
  maxUploadBytes: number | null,
  signal?: AbortSignal,
): Promise<ChannelAttachment> {
  if (maxUploadBytes !== null && file.size > maxUploadBytes) {
    throw new AttachmentUploadError("too_large", tooLargeMessage(maxUploadBytes));
  }

  const collection = target.kind === "channel" ? "channels" : "dm";
  const form = new FormData();
  form.append("file", file);

  let body: { data: unknown };
  try {
    body = await authenticatedFetch<{ data: unknown }>(
      `${FILES_BASE}/${collection}/${encodeURIComponent(target.id)}/attachments`,
      { method: "POST", body: form, signal },
    );
  } catch (error) {
    // An abort is the caller's own decision, not a failure to report.
    if (error instanceof DOMException && error.name === "AbortError") throw error;
    throw mapUploadError(error, maxUploadBytes);
  }

  const attachment = mapAttachment(body.data);
  if (!attachment) {
    throw new AttachmentUploadError("unknown", "Não foi possível enviar o arquivo.");
  }
  return attachment;
}
