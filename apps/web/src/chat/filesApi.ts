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

import { authenticatedFetch } from "../lib/authClient";
import type { AttachmentStatus, ChannelAttachment } from "./chatTypes";

const FILES_BASE = import.meta.env.VITE_FILES_API_BASE_URL ?? "/api/files";

interface AttachmentResponse {
  id?: unknown;
  filename?: unknown;
  contentType?: unknown;
  size?: unknown;
  status?: unknown;
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
