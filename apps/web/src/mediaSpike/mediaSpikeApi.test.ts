import { afterEach, describe, expect, it, vi } from "vitest";

import { requestSpikeToken } from "./mediaSpikeApi";

const mockFetch = vi.fn<typeof fetch>();
vi.stubGlobal("fetch", mockFetch);

afterEach(() => {
  vi.resetAllMocks();
});

describe("requestSpikeToken", () => {
  it("posts the room and participant fields to the media-service", async () => {
    mockFetch.mockResolvedValue(
      new Response(
        JSON.stringify({
          data: {
            serverUrl: "ws://127.0.0.1:7880",
            token: "participant-token",
            room: "spike-1to1",
            identity: "browser-a",
            expiresInSeconds: 300,
          },
        }),
        { status: 200, headers: { "Content-Type": "application/json" } },
      ),
    );
    const controller = new AbortController();

    const result = await requestSpikeToken(
      { room: "spike-1to1", identity: "browser-a", name: "Browser A" },
      controller.signal,
    );

    expect(result.token).toBe("participant-token");
    expect(mockFetch).toHaveBeenCalledWith(
      "/api/media/spike/token",
      expect.objectContaining({
        method: "POST",
        body: JSON.stringify({
          room: "spike-1to1",
          identity: "browser-a",
          name: "Browser A",
        }),
        signal: controller.signal,
      }),
    );
  });

  it("propagates a safe endpoint error", async () => {
    mockFetch.mockResolvedValue(
      new Response(JSON.stringify({ error: { code: "bad_request", message: "invalid room" } }), {
        status: 400,
        headers: { "Content-Type": "application/json" },
      }),
    );

    await expect(
      requestSpikeToken({ room: "other", identity: "browser-a", name: "" }),
    ).rejects.toMatchObject({ status: 400, code: "bad_request", message: "invalid room" });
  });
});
