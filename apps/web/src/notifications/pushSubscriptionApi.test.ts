import { beforeEach, describe, expect, it, vi } from "vitest";

import { ApiRequestError } from "../lib/api";
import { authenticatedFetch } from "../lib/authClient";
import {
  deletePushSubscription,
  isPushEndpointConflict,
  listPushSubscriptions,
  registerPushSubscription,
} from "./pushSubscriptionApi";

vi.mock("../lib/authClient");

const URL = "/api/notifications/push/subscriptions";

beforeEach(() => {
  vi.mocked(authenticatedFetch).mockReset();
});

describe("listPushSubscriptions", () => {
  it("maps the envelope, keeping non-active rows a client has to react to", async () => {
    vi.mocked(authenticatedFetch).mockResolvedValueOnce({
      data: {
        subscriptions: [
          { id: "sub-1", device_id: "device-a", status: "active" },
          { id: "sub-2", device_id: "device-b", status: "invalid" },
        ],
      },
    });

    await expect(listPushSubscriptions()).resolves.toEqual([
      { id: "sub-1", deviceId: "device-a", status: "active" },
      { id: "sub-2", deviceId: "device-b", status: "invalid" },
    ]);
    expect(authenticatedFetch).toHaveBeenCalledWith(URL, { method: "GET", signal: undefined });
  });

  it("propagates a failure instead of reporting an empty list", async () => {
    vi.mocked(authenticatedFetch).mockRejectedValueOnce(
      new ApiRequestError(503, "unavailable", "down"),
    );
    await expect(listPushSubscriptions()).rejects.toBeInstanceOf(ApiRequestError);
  });
});

describe("registerPushSubscription", () => {
  it("sends the snake_case contract body and maps the row back", async () => {
    vi.mocked(authenticatedFetch).mockResolvedValueOnce({
      data: { id: "sub-1", device_id: "device-a", status: "active" },
    });

    await expect(
      registerPushSubscription({
        deviceId: "device-a",
        endpoint: "https://push.example.com/s/abc",
        p256dh: "BP256",
        auth: "AUTH",
      }),
    ).resolves.toEqual({ id: "sub-1", deviceId: "device-a", status: "active" });

    expect(authenticatedFetch).toHaveBeenCalledWith(URL, {
      method: "POST",
      body: JSON.stringify({
        device_id: "device-a",
        endpoint: "https://push.example.com/s/abc",
        p256dh: "BP256",
        auth: "AUTH",
      }),
      signal: undefined,
    });
  });

  it("sends no field naming a user or a workspace", async () => {
    vi.mocked(authenticatedFetch).mockResolvedValueOnce({
      data: { id: "sub-1", device_id: "device-a", status: "active" },
    });
    await registerPushSubscription({
      deviceId: "device-a",
      endpoint: "https://push.example.com/s/abc",
      p256dh: "BP256",
      auth: "AUTH",
    });

    const body = vi.mocked(authenticatedFetch).mock.calls[0][1].body as string;
    expect(Object.keys(JSON.parse(body) as object)).toEqual([
      "device_id",
      "endpoint",
      "p256dh",
      "auth",
    ]);
  });
});

describe("deletePushSubscription", () => {
  it("encodes the id into the path", async () => {
    vi.mocked(authenticatedFetch).mockResolvedValueOnce(undefined);
    await deletePushSubscription("sub 1/../2");
    expect(authenticatedFetch).toHaveBeenCalledWith(`${URL}/sub%201%2F..%2F2`, {
      method: "DELETE",
      signal: undefined,
    });
  });

  it("treats a 404 as already gone", async () => {
    vi.mocked(authenticatedFetch).mockRejectedValueOnce(
      new ApiRequestError(404, "not_found", "gone"),
    );
    await expect(deletePushSubscription("sub-1")).resolves.toBeUndefined();
  });

  it("propagates any other failure", async () => {
    vi.mocked(authenticatedFetch).mockRejectedValueOnce(
      new ApiRequestError(500, "internal", "boom"),
    );
    await expect(deletePushSubscription("sub-1")).rejects.toBeInstanceOf(ApiRequestError);
  });
});

describe("isPushEndpointConflict", () => {
  it("recognises only the contract's own conflict code", () => {
    expect(
      isPushEndpointConflict(new ApiRequestError(409, "push_endpoint_conflict", "taken")),
    ).toBe(true);
    expect(isPushEndpointConflict(new ApiRequestError(409, "other_conflict", "taken"))).toBe(false);
    expect(isPushEndpointConflict(new Error("push_endpoint_conflict"))).toBe(false);
  });
});
