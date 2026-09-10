import { ApiRequestError } from "../lib/api";
import { authenticatedFetch } from "../lib/authClient";

/**
 * The notification-service PushSubscription contract (issue #745), and nothing
 * else. It knows the wire shape; it does not know when a subscription should be
 * created, repaired or removed — that is the reconciler's job.
 *
 * The responses deliberately carry no `endpoint`, `p256dh` or `auth`: #745 does
 * not return them, so nothing read here can leak a capability URL or a key into
 * a snapshot, a log or an error report. Only the POST body carries them, in one
 * direction, and it is never logged.
 */

const NOTIFICATIONS_BASE = import.meta.env.VITE_NOTIFICATIONS_API_BASE_URL ?? "/api/notifications";

const SUBSCRIPTIONS_URL = `${NOTIFICATIONS_BASE}/push/subscriptions`;

/** `active` is deliverable; `invalid` was retired by the provider; `disabled` was switched off by its owner. */
export type PushSubscriptionStatus = "active" | "invalid" | "disabled";

/** One row of the caller's own subscriptions, as #745 chooses to describe it. */
export interface PushSubscriptionRecord {
  id: string;
  deviceId: string;
  status: PushSubscriptionStatus;
}

/** What a browser has to present to register or re-register itself. */
export interface PushSubscriptionRegistration {
  deviceId: string;
  endpoint: string;
  p256dh: string;
  auth: string;
}

interface PushSubscriptionRowResponse {
  id: string;
  device_id: string;
  status: PushSubscriptionStatus;
}

interface PushSubscriptionListResponse {
  data: { subscriptions: PushSubscriptionRowResponse[] };
}

interface PushSubscriptionResponse {
  data: PushSubscriptionRowResponse;
}

function fromResponse(row: PushSubscriptionRowResponse): PushSubscriptionRecord {
  return { id: row.id, deviceId: row.device_id, status: row.status };
}

/**
 * The endpoint this browser holds already belongs to another instance or user.
 *
 * #745 refuses to move an endpoint between owners — whoever holds the row holds
 * the right to push to that browser — and names the recovery: cancel the local
 * subscription and register the new one that produces.
 */
export function isPushEndpointConflict(error: unknown): boolean {
  return error instanceof ApiRequestError && error.code === "push_endpoint_conflict";
}

/** The caller's own subscriptions, across every browser and device they use. */
export async function listPushSubscriptions(
  signal?: AbortSignal,
): Promise<PushSubscriptionRecord[]> {
  const response = await authenticatedFetch<PushSubscriptionListResponse>(SUBSCRIPTIONS_URL, {
    method: "GET",
    signal,
  });
  return response.data.subscriptions.map(fromResponse);
}

/**
 * Registers or re-registers this device. Idempotent by contract: an identical
 * retry is the same row, and a changed endpoint or key pair replaces what the
 * row held rather than adding a second one.
 */
export async function registerPushSubscription(
  input: PushSubscriptionRegistration,
  signal?: AbortSignal,
): Promise<PushSubscriptionRecord> {
  const response = await authenticatedFetch<PushSubscriptionResponse>(SUBSCRIPTIONS_URL, {
    method: "POST",
    body: JSON.stringify({
      device_id: input.deviceId,
      endpoint: input.endpoint,
      p256dh: input.p256dh,
      auth: input.auth,
    }),
    signal,
  });
  return fromResponse(response.data);
}

/**
 * Switches off one of the caller's own subscriptions.
 *
 * A 404 covers "gone" and "not yours" alike, and neither is worth surfacing:
 * the caller re-reads the list afterwards and converges either way.
 */
export async function deletePushSubscription(id: string, signal?: AbortSignal): Promise<void> {
  try {
    await authenticatedFetch<void>(`${SUBSCRIPTIONS_URL}/${encodeURIComponent(id)}`, {
      method: "DELETE",
      signal,
    });
  } catch (error) {
    if (error instanceof ApiRequestError && error.status === 404) return;
    throw error;
  }
}
