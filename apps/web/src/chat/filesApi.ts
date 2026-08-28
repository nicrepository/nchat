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

import { ApiRequestError, apiUpload, type ResponseParser, type UploadProgress } from "../lib/api";
import { authenticatedFetch } from "../lib/authClient";
import { formatUploadLimit } from "../lib/uploadLimit";
import {
  parseAttachmentPreviewStatus,
  parseAttachmentStatus,
  parseAudioKind,
  type ChannelAttachment,
} from "./chatTypes";

const FILES_BASE = import.meta.env.VITE_FILES_API_BASE_URL ?? "/api/files";

interface AttachmentResponse {
  id?: unknown;
  filename?: unknown;
  contentType?: unknown;
  size?: unknown;
  status?: unknown;
  previewStatus?: unknown;
  createdAt?: unknown;
  audioKind?: unknown;
  durationMs?: unknown;
}

interface AttachmentsEnvelope {
  data: { attachments?: unknown };
}

// The two status parsers live in chatTypes so chatApi, which reads the same
// two lifecycle values off a message payload, uses exactly these rules.

function mapAttachment(raw: unknown): ChannelAttachment | undefined {
  if (typeof raw !== "object" || raw === null) return undefined;
  const item = raw as AttachmentResponse;
  if (typeof item.id !== "string" || item.id === "") return undefined;
  const audioKind = parseAudioKind(item.audioKind);
  return {
    id: item.id,
    filename: typeof item.filename === "string" ? item.filename : "",
    contentType: typeof item.contentType === "string" ? item.contentType : "",
    size: typeof item.size === "number" && Number.isFinite(item.size) ? item.size : 0,
    status: parseAttachmentStatus(item.status),
    previewStatus: parseAttachmentPreviewStatus(item.previewStatus),
    createdAt: typeof item.createdAt === "string" ? item.createdAt : "",
    ...(audioKind ? { audioKind } : {}),
    ...(typeof item.durationMs === "number" &&
    Number.isFinite(item.durationMs) &&
    item.durationMs > 0
      ? { durationMs: item.durationMs }
      : {}),
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
 * A preview that is not servable answers 409, an attachment the scan has not
 * approved answers 403 and a caller that cannot see it answers 404 — all three
 * surface as ApiRequestError, and all three mean the same thing to the UI: draw
 * the fallback.
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

export interface DocumentPreviewManifest {
  attachmentId: string;
  kind: "pages" | "sheets";
  pageCount: number;
  labels: string[];
}

export async function fetchDocumentPreviewManifest(
  attachmentId: string,
  signal?: AbortSignal,
): Promise<DocumentPreviewManifest> {
  const response = await authenticatedFetch<{ data: DocumentPreviewManifest }>(
    `${FILES_BASE}/attachments/${encodeURIComponent(attachmentId)}/document-preview`,
    { method: "GET", signal },
  );
  return response.data;
}

export async function fetchDocumentPreviewPage(
  attachmentId: string,
  page: number,
  signal?: AbortSignal,
): Promise<Blob> {
  return authenticatedFetch<Blob>(
    `${FILES_BASE}/attachments/${encodeURIComponent(attachmentId)}/document-preview/pages/${page}`,
    { method: "GET", signal },
    (response) => response.blob(),
  );
}

export async function regenerateDocumentPreview(
  attachmentId: string,
  signal?: AbortSignal,
): Promise<void> {
  await authenticatedFetch(
    `${FILES_BASE}/attachments/${encodeURIComponent(attachmentId)}/document-preview/regenerate`,
    { method: "POST", signal },
  );
}

/**
 * A spreadsheet/CSV preview's one bounded page (task #494's sheet phase) —
 * the server's own sheetPreview shape, never arbitrary JSON. Cell values are
 * always strings: the server never sends a number, a formula or anything
 * this client would have to further interpret.
 */
export interface DocumentPreviewSheet {
  columns: string[];
  rows: string[][];
  truncatedRows: boolean;
  truncatedColumns: boolean;
  totalRowsRead: number;
}

/**
 * Fetches a sheet-kind preview's one page as parsed JSON, the sibling of
 * fetchDocumentPreviewPage for the shape a spreadsheet/CSV preview actually
 * is. Same URL, same route, same authorization and scan gate — the manifest's
 * own `kind` is what tells the caller which of the two fetchers to use, so
 * there is no guessing and no second route.
 */
export async function fetchDocumentPreviewSheet(
  attachmentId: string,
  page: number,
  signal?: AbortSignal,
): Promise<DocumentPreviewSheet> {
  return authenticatedFetch<DocumentPreviewSheet>(
    `${FILES_BASE}/attachments/${encodeURIComponent(attachmentId)}/document-preview/pages/${page}`,
    { method: "GET", signal },
    (response) => response.json() as Promise<DocumentPreviewSheet>,
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
 * A file the scan has not cleared answers 403 and one the caller cannot see
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
 * `maxUploadBytes` is the workspace's effective limit from the canonical
 * sidebar snapshot, or null when the server published none. Checking
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
 *
 * `onProgress` is called with the bytes actually handed to the network. It is
 * the only source of a percentage anywhere in this feature — when the transport
 * reports nothing, the caller shows an indeterminate state rather than a number
 * it made up. The request travels over apiUpload for exactly this reason; the
 * authentication and the 401 refresh-and-retry are still authenticatedFetch's,
 * which is what keeps the upload from growing a second copy of them.
 */
export interface VoiceMessageUploadOptions {
  purpose: "voice_message";
  /** Client-measured wall-clock recording length. Display only — see filesApi module doc. */
  durationMs: number;
}

export async function uploadAttachment(
  target: { kind: "channel" | "dm"; id: string },
  file: File,
  maxUploadBytes: number | null,
  signal?: AbortSignal,
  onProgress?: (progress: UploadProgress) => void,
  voiceOptions?: VoiceMessageUploadOptions,
): Promise<ChannelAttachment> {
  if (maxUploadBytes !== null && file.size > maxUploadBytes) {
    throw new AttachmentUploadError("too_large", tooLargeMessage(maxUploadBytes));
  }

  const collection = target.kind === "channel" ? "channels" : "dm";
  const form = new FormData();
  form.append("purpose", voiceOptions?.purpose ?? "message_draft");
  if (voiceOptions && Number.isFinite(voiceOptions.durationMs) && voiceOptions.durationMs > 0) {
    form.append("duration_ms", String(Math.round(voiceOptions.durationMs)));
  }
  form.append("file", file);

  let body: { data: unknown };
  try {
    body = await authenticatedFetch<{ data: unknown }>(
      `${FILES_BASE}/${collection}/${encodeURIComponent(target.id)}/attachments`,
      { method: "POST", body: form, signal },
      undefined,
      <R>(requestUrl: string, requestInit: RequestInit, parse?: ResponseParser<R>) =>
        apiUpload<R>(requestUrl, requestInit, onProgress, parse),
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

/** Best-effort cancellation for an unpublished message draft. */
export async function deleteAttachmentDraft(attachmentId: string): Promise<void> {
  await authenticatedFetch(
    `${FILES_BASE}/attachments/${encodeURIComponent(attachmentId)}`,
    { method: "DELETE" },
    async () => undefined,
  );
}
