import { afterEach, describe, expect, it, vi } from "vitest";

import { ApiRequestError } from "../lib/api";

const { mockAuthFetch } = vi.hoisted(() => ({ mockAuthFetch: vi.fn() }));

vi.mock("../lib/authClient", () => ({
  authenticatedFetch: (...args: unknown[]) => mockAuthFetch(...args),
}));

import { fetchAttachmentContent, fetchConversationAttachments, uploadAttachment } from "./filesApi";

afterEach(() => {
  vi.clearAllMocks();
});

describe("fetchChannelAttachments", () => {
  it("targets the files gateway prefix with an encoded channel and the requested limit", async () => {
    mockAuthFetch.mockResolvedValueOnce({ data: { attachments: [] } });

    await fetchConversationAttachments({ kind: "channel", id: "ch 1" }, 5);

    expect(mockAuthFetch).toHaveBeenCalledWith("/api/files/channels/ch%201/attachments?limit=5", {
      method: "GET",
      signal: undefined,
    });
  });

  it("maps the payload and preserves the server's order", async () => {
    mockAuthFetch.mockResolvedValueOnce({
      data: {
        attachments: [
          {
            id: "a-1",
            filename: "recente.pdf",
            contentType: "application/pdf",
            size: 2048,
            status: "clean",
            previewStatus: "ready",
            createdAt: "2026-07-15T12:00:00Z",
          },
          {
            id: "a-2",
            filename: "antigo.png",
            contentType: "image/png",
            size: 10,
            status: "rejected",
            createdAt: "2026-07-14T12:00:00Z",
          },
        ],
      },
    });

    const attachments = await fetchConversationAttachments({ kind: "channel", id: "ch-1" }, 5);

    expect(attachments.map((item) => item.id)).toEqual(["a-1", "a-2"]);
    expect(attachments[0]).toEqual({
      id: "a-1",
      filename: "recente.pdf",
      contentType: "application/pdf",
      size: 2048,
      status: "clean",
      previewStatus: "ready",
      createdAt: "2026-07-15T12:00:00Z",
    });
    expect(attachments[1].status).toBe("rejected");
    // A server that publishes no preview state at all is read as "there is
    // none", never as one the UI could try to load.
    expect(attachments[1].previewStatus).toBe("unsupported");
  });

  it("keeps every preview state the contract defines and refuses the rest", async () => {
    mockAuthFetch.mockResolvedValueOnce({
      data: {
        attachments: [
          { id: "a-1", previewStatus: "pending" },
          { id: "a-2", previewStatus: "ready" },
          { id: "a-3", previewStatus: "failed" },
          { id: "a-4", previewStatus: "unsupported" },
          { id: "a-5", previewStatus: "READY" },
          { id: "a-6", previewStatus: 7 },
          { id: "a-7" },
        ],
      },
    });

    const attachments = await fetchConversationAttachments({ kind: "channel", id: "ch-1" }, 5);

    expect(attachments.map((item) => item.previewStatus)).toEqual([
      "pending",
      "ready",
      "failed",
      "unsupported",
      // Anything outside the closed set falls back to the icon, never to a
      // preview the client would then fail to load.
      "unsupported",
      "unsupported",
      "unsupported",
    ]);
  });

  it("never promotes an unrecognised status to clean", async () => {
    mockAuthFetch.mockResolvedValueOnce({
      data: {
        attachments: [
          { id: "a-1", status: "pending_upload" },
          { id: "a-2" },
          { id: "a-3", status: 42 },
        ],
      },
    });

    const attachments = await fetchConversationAttachments({ kind: "channel", id: "ch-1" }, 5);

    expect(attachments.every((item) => item.status === "pending_scan")).toBe(true);
  });

  it("drops entries that are not usable attachments", async () => {
    mockAuthFetch.mockResolvedValueOnce({
      data: { attachments: [null, "nope", { filename: "sem id" }, { id: "" }, { id: "a-1" }] },
    });

    const attachments = await fetchConversationAttachments({ kind: "channel", id: "ch-1" }, 5);

    expect(attachments.map((item) => item.id)).toEqual(["a-1"]);
    expect(attachments[0].size).toBe(0);
    expect(attachments[0].filename).toBe("");
  });

  it("treats a payload without an attachments array as empty", async () => {
    mockAuthFetch.mockResolvedValueOnce({ data: {} });

    await expect(fetchConversationAttachments({ kind: "channel", id: "ch-1" }, 5)).resolves.toEqual(
      [],
    );
  });

  it("propagates a refusal rather than showing an empty file list", async () => {
    mockAuthFetch.mockRejectedValueOnce(new ApiRequestError(404, "not_found", "not found"));

    await expect(
      fetchConversationAttachments({ kind: "channel", id: "ch-1" }, 5),
    ).rejects.toBeInstanceOf(ApiRequestError);
  });
});

describe("fetchConversationAttachments — destino de grupo (issue #441)", () => {
  it("targets the dm collection, never the channel one", async () => {
    mockAuthFetch.mockResolvedValueOnce({ data: { attachments: [] } });

    await fetchConversationAttachments({ kind: "dm", id: "conv 1" }, 5);

    // A group's files live under the conversation resource; asking the channel
    // route for them would be a different destination space entirely.
    expect(mockAuthFetch).toHaveBeenCalledWith("/api/files/dm/conv%201/attachments?limit=5", {
      method: "GET",
      signal: undefined,
    });
  });
});

// ── Upload (RF-32, issue #458) ───────────────────────────────────────────────

/**
 * A File whose size is declared rather than materialised: the limit is 250 MB
 * and no test may allocate anything near it.
 */
function fileOfSize(bytes: number, name = "relatorio.pdf"): File {
  const file = new File(["x"], name, { type: "application/pdf" });
  Object.defineProperty(file, "size", { value: bytes });
  return file;
}

const LIMIT = 8 * 1024 * 1024; // 8 MiB, so the message reads "8 MiB"

function createdAttachment() {
  return {
    data: {
      id: "a-1",
      filename: "relatorio.pdf",
      contentType: "application/pdf",
      size: 1024,
      status: "pending_scan",
      createdAt: "2026-08-03T12:00:00Z",
    },
  };
}

/**
 * Installs a minimal XMLHttpRequest and returns a handle to whichever one the
 * transport opens next. Only the upload's progress channel is needed here.
 */
function startedUpload(): () => { reportProgress: (loaded: number, total: number) => void } {
  let opened: EventTarget | null = null;
  class StubXHR {
    readonly upload: EventTarget;
    constructor() {
      const channel = new EventTarget();
      this.upload = channel;
      opened = channel;
    }
    open() {}
    setRequestHeader() {}
    addEventListener() {}
    send() {}
  }
  vi.stubGlobal("XMLHttpRequest", StubXHR);
  return () => {
    const target = opened;
    if (!target) throw new Error("no upload was opened");
    return {
      reportProgress: (loaded, total) =>
        target.dispatchEvent(
          new ProgressEvent("progress", { loaded, total, lengthComputable: true }),
        ),
    };
  };
}

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("uploadAttachment", () => {
  it("starts the upload for a file below the limit", async () => {
    mockAuthFetch.mockResolvedValueOnce(createdAttachment());

    const attachment = await uploadAttachment(
      { kind: "channel", id: "ch 1" },
      fileOfSize(LIMIT - 1),
      LIMIT,
    );

    expect(attachment.id).toBe("a-1");
    const [url, init] = mockAuthFetch.mock.calls[0] as [string, RequestInit];
    expect(url).toBe("/api/files/channels/ch%201/attachments");
    expect(init.method).toBe("POST");
    expect(init.body).toBeInstanceOf(FormData);
    expect((init.body as FormData).get("file")).toBeInstanceOf(File);
  });

  it("starts the upload for a file exactly at the limit", async () => {
    mockAuthFetch.mockResolvedValueOnce(createdAttachment());

    await expect(
      uploadAttachment({ kind: "dm", id: "dm-1" }, fileOfSize(LIMIT), LIMIT),
    ).resolves.toBeDefined();
    expect(mockAuthFetch).toHaveBeenCalledOnce();
  });

  it("does not start the upload for a file above the limit and names the limit", async () => {
    await expect(
      uploadAttachment({ kind: "channel", id: "ch-1" }, fileOfSize(LIMIT + 1), LIMIT),
    ).rejects.toMatchObject({
      reason: "too_large",
      message: "O arquivo excede o limite permitido de 8 MiB.",
    });
    // The point of the pre-flight check: no bandwidth is spent.
    expect(mockAuthFetch).not.toHaveBeenCalled();
  });

  it("reports a bare 413 from the gateway as an oversized file", async () => {
    // Traefik refuses the body itself, so there is no service error envelope
    // and therefore no domain code — only the status.
    mockAuthFetch.mockRejectedValueOnce(
      new ApiRequestError(413, "unknown_error", "Request Entity Too Large"),
    );

    await expect(
      uploadAttachment({ kind: "channel", id: "ch-1" }, fileOfSize(1024), LIMIT),
    ).rejects.toMatchObject({
      reason: "too_large",
      message: "O arquivo excede o limite permitido de 8 MiB.",
    });
  });

  it("reports a structured 413 from file-service as an oversized file", async () => {
    mockAuthFetch.mockRejectedValueOnce(
      new ApiRequestError(413, "payload_too_large", "file exceeds the configured size limit"),
    );

    await expect(
      uploadAttachment({ kind: "dm", id: "dm-1" }, fileOfSize(1024), LIMIT),
    ).rejects.toMatchObject({ reason: "too_large" });
  });

  it("never surfaces the server's own text", async () => {
    mockAuthFetch.mockRejectedValueOnce(
      new ApiRequestError(500, "internal_error", "pq: connection refused to db-primary.internal"),
    );

    await expect(
      uploadAttachment({ kind: "channel", id: "ch-1" }, fileOfSize(1024), LIMIT),
    ).rejects.toMatchObject({
      reason: "unknown",
      message: "Não foi possível enviar o arquivo.",
    });
  });

  it("keeps the other rejections distinguishable", async () => {
    const cases: Array<[ApiRequestError, string]> = [
      [new ApiRequestError(415, "unsupported_media_type", "x"), "unsupported"],
      [new ApiRequestError(403, "forbidden", "x"), "forbidden"],
      [new ApiRequestError(404, "not_found", "x"), "forbidden"],
      [new ApiRequestError(503, "service_unavailable", "x"), "unavailable"],
    ];
    for (const [error, reason] of cases) {
      mockAuthFetch.mockRejectedValueOnce(error);
      await expect(
        uploadAttachment({ kind: "channel", id: "ch-1" }, fileOfSize(1024), LIMIT),
      ).rejects.toMatchObject({ reason });
    }
  });

  it("skips the pre-flight check when the limit is unknown and lets the service decide", async () => {
    mockAuthFetch.mockResolvedValueOnce(createdAttachment());

    // Far larger than any real policy. With no published limit the client must
    // not refuse locally — refusing would mean inventing a limit — so the
    // request goes out and file-service remains the only thing that decides.
    await expect(
      uploadAttachment({ kind: "channel", id: "ch-1" }, fileOfSize(900 * 1024 * 1024), null),
    ).resolves.toBeDefined();
    expect(mockAuthFetch).toHaveBeenCalledOnce();
  });

  it("names no limit it does not know when the service refuses on size", async () => {
    mockAuthFetch.mockRejectedValueOnce(
      new ApiRequestError(413, "payload_too_large", "file exceeds the configured size limit"),
    );

    await expect(
      uploadAttachment({ kind: "channel", id: "ch-1" }, fileOfSize(1024), null),
    ).rejects.toMatchObject({
      reason: "too_large",
      message: "O arquivo excede o limite permitido.",
    });
  });

  // The upload is the one route that does not ride the default transport: it
  // needs XHR to be able to count the bytes leaving (RF-30). What must not
  // change is where the authentication comes from, so the sender is handed to
  // authenticatedFetch rather than called beside it.
  it("sends through a transport that forwards the caller's progress callback", async () => {
    mockAuthFetch.mockResolvedValueOnce(createdAttachment());
    const onProgress = vi.fn();

    await uploadAttachment(
      { kind: "channel", id: "ch-1" },
      fileOfSize(1024),
      LIMIT,
      undefined,
      onProgress,
    );

    const send = mockAuthFetch.mock.calls[0][3] as (
      url: string,
      init: RequestInit,
    ) => Promise<unknown>;
    expect(typeof send).toBe("function");

    // Driving the transport itself is api.test.ts's job. What is proved here is
    // that the sender reaches XMLHttpRequest at all, and that the bytes it
    // counts are reported to *this* call's callback.
    const upload = startedUpload();
    void send("/api/files/channels/ch-1/attachments", { method: "POST" });
    upload().reportProgress(256, 1024);

    expect(onProgress).toHaveBeenCalledWith({ loaded: 256, total: 1024 });
  });

  it("lets an abort through untouched so a cancel is not shown as a failure", async () => {
    mockAuthFetch.mockRejectedValueOnce(new DOMException("aborted", "AbortError"));

    await expect(
      uploadAttachment({ kind: "channel", id: "ch-1" }, fileOfSize(1024), LIMIT),
    ).rejects.toBeInstanceOf(DOMException);
  });
});

describe("fetchAttachmentContent", () => {
  it("requests the content route with an encoded id and no credential in the URL", async () => {
    mockAuthFetch.mockResolvedValueOnce(new Blob(["bytes"]));

    await fetchAttachmentContent("a 1");

    const [url, init, parse] = mockAuthFetch.mock.calls[0];
    expect(url).toBe("/api/files/attachments/a%201/content");
    expect(url).not.toContain("?");
    expect(url).not.toMatch(/token|bearer|authorization/i);
    expect(init).toEqual({ method: "GET", signal: undefined });
    // The response is read as a Blob: that is the only shape a media element
    // can be given without a URL that carries credentials.
    expect(parse).toBeTypeOf("function");
  });

  it("passes the abort signal through so a request can be cancelled", async () => {
    mockAuthFetch.mockResolvedValueOnce(new Blob(["bytes"]));
    const controller = new AbortController();

    await fetchAttachmentContent("a-1", controller.signal);

    expect(mockAuthFetch.mock.calls[0][1]).toEqual({
      method: "GET",
      signal: controller.signal,
    });
  });

  it("propagates a refusal so the caller can draw its fallback", async () => {
    mockAuthFetch.mockRejectedValueOnce(new ApiRequestError(403, "file_not_scanned", "nope"));

    await expect(fetchAttachmentContent("a-1")).rejects.toBeInstanceOf(ApiRequestError);
  });
});
