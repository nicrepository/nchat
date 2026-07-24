/**
 * Profile API client — avatar upload and removal.
 *
 * The image bytes are sent as multipart/form-data; the server validates,
 * re-encodes and stores them, then returns a same-origin avatar URL. The client
 * never chooses the persisted URL and never sends a user_id — identity comes
 * from the session token via authenticatedFetch.
 */

import { authenticatedFetch } from "../lib/authClient";
import { ApiRequestError } from "../lib/api";

const AUTH_BASE = import.meta.env.VITE_AUTH_API_BASE_URL ?? "/api/auth";

/** Client-side limits mirrored from the server, so obviously-bad files are
 * rejected before a wasted round-trip. The server remains authoritative. */
export const AVATAR_MAX_BYTES = 5 * 1024 * 1024;
export const AVATAR_ACCEPTED_TYPES = ["image/jpeg", "image/png"] as const;

export type AvatarUploadErrorReason = "too_large" | "unsupported" | "forbidden" | "unknown";

export class AvatarUploadError extends Error {
  readonly reason: AvatarUploadErrorReason;
  constructor(reason: AvatarUploadErrorReason, message: string) {
    super(message);
    this.name = "AvatarUploadError";
    this.reason = reason;
  }
}

interface AvatarResponse {
  data: { avatar_url: string };
}

interface SelfProfileResponse {
  data: { id: string; display_name: string; avatar_url?: unknown };
}

export interface SelfProfile {
  id: string;
  displayName: string;
  /** Present only when set and same-origin; a cross-origin value is dropped. */
  avatarUrl?: string;
}

/**
 * Loads the authenticated user's own profile (identity from the session, never a
 * client-supplied id). The avatar URL is run through the same same-origin guard
 * the sidebar uses, so a value that could not be rendered is never returned.
 */
export async function fetchMyProfile(signal?: AbortSignal): Promise<SelfProfile> {
  const res = await authenticatedFetch<SelfProfileResponse>(`${AUTH_BASE}/me`, {
    method: "GET",
    signal,
  });
  return {
    id: res.data.id,
    displayName: res.data.display_name,
    avatarUrl: sameOriginAvatarUrl(res.data.avatar_url),
  };
}

/**
 * Accepts an avatar URL only when it resolves same-origin, mirroring the sidebar
 * normaliser. Belt-and-suspenders: the server already persists only same-origin
 * URLs, but the client must never render a cross-origin image.
 */
function sameOriginAvatarUrl(raw: unknown): string | undefined {
  if (typeof raw !== "string" || raw.trim() === "") return undefined;
  if (typeof window === "undefined") return undefined;
  const origin = window.location?.origin;
  if (typeof origin !== "string" || origin === "" || origin === "null") return undefined;
  try {
    const parsed = new URL(raw, origin);
    if (parsed.protocol !== "http:" && parsed.protocol !== "https:") return undefined;
    if (parsed.origin !== origin) return undefined;
    if (parsed.username !== "" || parsed.password !== "") return undefined;
    return raw;
  } catch {
    return undefined;
  }
}

/**
 * Uploads an avatar image and returns the persisted same-origin URL. Throws an
 * AvatarUploadError with a typed reason on rejection so the UI can show a
 * specific message.
 */
export async function uploadAvatar(file: File, signal?: AbortSignal): Promise<string> {
  const form = new FormData();
  form.append("avatar", file);
  try {
    const res = await authenticatedFetch<AvatarResponse>(`${AUTH_BASE}/me/avatar`, {
      method: "POST",
      body: form,
      signal,
    });
    return res.data.avatar_url;
  } catch (error) {
    throw mapAvatarError(error);
  }
}

/** Removes the current user's avatar. Idempotent on the server (204). */
export async function removeAvatar(signal?: AbortSignal): Promise<void> {
  try {
    await authenticatedFetch<void>(`${AUTH_BASE}/me/avatar`, { method: "DELETE", signal });
  } catch (error) {
    throw mapAvatarError(error);
  }
}

function mapAvatarError(error: unknown): AvatarUploadError {
  if (error instanceof ApiRequestError) {
    switch (error.status) {
      case 413:
        return new AvatarUploadError("too_large", "A imagem é muito grande.");
      case 415:
        return new AvatarUploadError("unsupported", "Formato de imagem não suportado.");
      case 403:
        return new AvatarUploadError("forbidden", "Conta indisponível para esta ação.");
    }
  }
  return new AvatarUploadError("unknown", "Não foi possível atualizar o avatar.");
}
