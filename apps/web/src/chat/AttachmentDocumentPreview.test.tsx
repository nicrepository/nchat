/**
 * AttachmentDocumentPreview proximity tests (issue #675).
 *
 * Regenerating an expired preview is not a read: it starts real rendering work
 * on the server. A timeline that merely *contains* stale documents must
 * therefore not schedule a re-render of each of them — only proximity may, and
 * only once per document however many cards ask.
 *
 * The first-page fetch is covered here for the same reason: a document far from
 * the scrollport draws its shell and spends nothing.
 */

import { act, render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import AttachmentDocumentPreview from "./AttachmentDocumentPreview";
import {
  AttachmentHydrationContext,
  NEAR_DEBOUNCE_MS,
  PREVIEW_PREFETCH_MARGIN_PX,
  useLazyAttachment,
  type AttachmentGate,
} from "./lazyAttachment";
import type { ChannelAttachment } from "./chatTypes";

const { mockPage, mockRegenerate } = vi.hoisted(() => ({
  mockPage: vi.fn(),
  mockRegenerate: vi.fn(),
}));

vi.mock("./filesApi", () => ({
  fetchDocumentPreviewPage: (...args: unknown[]) => mockPage(...args),
  regenerateDocumentPreview: (...args: unknown[]) => mockRegenerate(...args),
}));

/**
 * The scheduler is left real — the queue's own behaviour is previewScheduler's
 * suite — and only observed, so the priority a document's first page enters
 * with can be asserted rather than inferred from the gate it was handed.
 */
const scheduledPriorities: number[] = [];
vi.mock("./previewScheduler", async (importActual) => {
  const actual = await importActual<typeof import("./previewScheduler")>();
  return {
    ...actual,
    scheduleAttachmentFetch: (
      key: string,
      priority: number,
      signal: AbortSignal,
      run: (signal: AbortSignal) => Promise<Blob>,
    ) => {
      scheduledPriorities.push(priority);
      return actual.scheduleAttachmentFetch(
        key,
        priority as Parameters<typeof actual.scheduleAttachmentFetch>[1],
        signal,
        run,
      );
    },
  };
});

const FAR: AttachmentGate = { proximity: "far", active: false, priority: 1 };
const NEAR: AttachmentGate = { proximity: "near", active: true, priority: 1 };
const VISIBLE: AttachmentGate = { proximity: "visible", active: true, priority: 0 };

function attachment(overrides: Partial<ChannelAttachment> = {}): ChannelAttachment {
  return {
    id: "doc-1",
    filename: "relatorio.pdf",
    contentType: "application/pdf",
    size: 400_000,
    status: "clean",
    previewStatus: "ready",
    createdAt: "2026-07-15T12:00:00.000Z",
    ...overrides,
  };
}

const onOpen = vi.fn();

function renderAt(gate: AttachmentGate, overrides: Partial<ChannelAttachment> = {}) {
  const node = (currentGate: AttachmentGate, current: Partial<ChannelAttachment>) => (
    <AttachmentHydrationContext.Provider value={currentGate}>
      <AttachmentDocumentPreview attachment={attachment(current)} onOpen={onOpen} />
    </AttachmentHydrationContext.Provider>
  );
  const result = render(node(gate, overrides));
  return {
    ...result,
    moveTo: (next: AttachmentGate) => result.rerender(node(next, overrides)),
  };
}

beforeEach(() => {
  mockPage.mockReset().mockResolvedValue(new Blob(["page-1"]));
  mockRegenerate.mockReset().mockResolvedValue(undefined);
  onOpen.mockReset();
  scheduledPriorities.length = 0;
  vi.stubGlobal("URL", {
    ...URL,
    createObjectURL: vi.fn(() => "blob:doc-1"),
    revokeObjectURL: vi.fn(),
  });
});

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("an expired preview", () => {
  it("asks for no regeneration at all while the document is far away", async () => {
    renderAt(FAR, { previewStatus: "expired" });

    // The shell is drawn; nothing is asked of the server for it.
    expect(
      screen.getByTestId("chat-message-attachment-document-loading-doc-1"),
    ).toBeInTheDocument();
    await Promise.resolve();
    expect(mockRegenerate).not.toHaveBeenCalled();
    expect(mockPage).not.toHaveBeenCalled();
  });

  it("regenerates once the document comes near", async () => {
    const { moveTo } = renderAt(FAR, { previewStatus: "expired" });
    expect(mockRegenerate).not.toHaveBeenCalled();

    moveTo(NEAR);

    await waitFor(() => expect(mockRegenerate).toHaveBeenCalledWith("doc-1"));
  });

  it("regenerates for a visible document without waiting for anything", async () => {
    renderAt(VISIBLE, { previewStatus: "expired" });

    await waitFor(() => expect(mockRegenerate).toHaveBeenCalledTimes(1));
  });

  it("regenerates a document once however many cards show it", async () => {
    let resolveRegeneration: (() => void) | undefined;
    mockRegenerate.mockImplementation(
      () =>
        new Promise<void>((resolve) => {
          resolveRegeneration = resolve;
        }),
    );

    render(
      <AttachmentHydrationContext.Provider value={VISIBLE}>
        <AttachmentDocumentPreview
          attachment={attachment({ previewStatus: "expired" })}
          onOpen={onOpen}
        />
        <AttachmentDocumentPreview
          attachment={attachment({ previewStatus: "expired" })}
          onOpen={onOpen}
        />
      </AttachmentHydrationContext.Provider>,
    );

    await waitFor(() => expect(mockRegenerate).toHaveBeenCalled());
    expect(mockRegenerate).toHaveBeenCalledTimes(1);
    resolveRegeneration?.();
  });
});

describe("the first page", () => {
  it("is not fetched for a document far from the scrollport", async () => {
    renderAt(FAR);

    await Promise.resolve();
    expect(mockPage).not.toHaveBeenCalled();
    expect(
      screen.getByTestId("chat-message-attachment-document-loading-doc-1"),
    ).toBeInTheDocument();
  });

  it("is fetched once the document is near", async () => {
    const { moveTo } = renderAt(FAR);

    moveTo(NEAR);

    await waitFor(() => expect(mockPage).toHaveBeenCalledWith("doc-1", 1, expect.any(AbortSignal)));
  });

  it("stays on screen when the document drifts far away again", async () => {
    const { moveTo } = renderAt(VISIBLE);
    await screen.findByTestId("chat-message-attachment-document-doc-1");

    moveTo(FAR);

    expect(screen.getByTestId("chat-message-attachment-document-doc-1")).toBeInTheDocument();
    expect(mockPage).toHaveBeenCalledTimes(1);
  });
});

/**
 * The same document, driven by the real proximity gate (issue #675).
 *
 * The tests above hand the component a gate directly, which answers "what does
 * it do when told it is near" but not "when is it told". Regenerating is
 * server-side work, so the question that matters for a timeline is the one only
 * the composition can answer: a flick of the wheel that carries a stale
 * document through the prefetch region and out again must cost nothing at all.
 *
 * So the gate here is the production one — the real observers, the real
 * debounce — and only the browser's IntersectionObserver is played.
 */
describe("a document carried through the prefetch region by the real gate", () => {
  interface ObserverDouble {
    callback: IntersectionObserverCallback;
    options: IntersectionObserverInit | undefined;
    targets: Set<Element>;
    disconnected: boolean;
  }
  let observers: ObserverDouble[] = [];

  /** Reports an intersection through whichever observer covers that region. */
  function report(region: "near" | "visible", intersecting: boolean) {
    const wanted = region === "near" ? `${PREVIEW_PREFETCH_MARGIN_PX}px 0px` : undefined;
    const observer = observers.find(
      (candidate) => !candidate.disconnected && candidate.options?.rootMargin === wanted,
    );
    if (!observer) throw new Error(`no ${region} observer`);
    const entries = [...observer.targets].map(
      (target) => ({ target, isIntersecting: intersecting }) as IntersectionObserverEntry,
    );
    if (entries.length > 0) {
      act(() => observer.callback(entries, {} as IntersectionObserver));
    }
  }

  function Card({ overrides }: { overrides?: Partial<ChannelAttachment> }) {
    const { ref, gate } = useLazyAttachment();
    return (
      <div ref={ref}>
        <AttachmentHydrationContext.Provider value={gate}>
          <AttachmentDocumentPreview attachment={attachment(overrides)} onOpen={onOpen} />
        </AttachmentHydrationContext.Provider>
      </div>
    );
  }

  beforeEach(() => {
    vi.useFakeTimers();
    observers = [];
    class DrivenIntersectionObserver {
      private readonly entry: ObserverDouble;
      constructor(callback: IntersectionObserverCallback, options?: IntersectionObserverInit) {
        this.entry = { callback, options, targets: new Set<Element>(), disconnected: false };
        observers.push(this.entry);
      }
      observe(target: Element) {
        this.entry.targets.add(target);
      }
      unobserve(target: Element) {
        this.entry.targets.delete(target);
      }
      disconnect() {
        this.entry.disconnected = true;
      }
    }
    vi.stubGlobal("IntersectionObserver", DrivenIntersectionObserver);
  });

  afterEach(() => {
    vi.useRealTimers();
  });

  it("spends nothing on a stale document that comes near and leaves before the debounce", async () => {
    render(<Card overrides={{ previewStatus: "expired" }} />);
    expect(mockRegenerate).not.toHaveBeenCalled();

    // Into the prefetch region — and the debounce has not been served, so the
    // gate is still closed and no work may start.
    report("near", true);
    await act(async () => {
      vi.advanceTimersByTime(NEAR_DEBOUNCE_MS - 1);
    });
    expect(mockRegenerate).not.toHaveBeenCalled();

    // Out again: a fast scroll crossing the region, which is the case this
    // exists for. The pending debounce must go with it.
    report("near", false);
    await act(async () => {
      vi.advanceTimersByTime(NEAR_DEBOUNCE_MS * 10);
    });

    expect(mockRegenerate).not.toHaveBeenCalled();
    expect(mockPage).not.toHaveBeenCalled();
    expect(scheduledPriorities).toEqual([]);
  });

  it("regenerates a stale document that stays near past the debounce", async () => {
    render(<Card overrides={{ previewStatus: "expired" }} />);

    report("near", true);
    await act(async () => {
      vi.advanceTimersByTime(NEAR_DEBOUNCE_MS);
    });

    expect(mockRegenerate).toHaveBeenCalledWith("doc-1");
  });

  it("fetches a merely-near document's page at prefetch priority", async () => {
    render(<Card />);

    report("near", true);
    await act(async () => {
      vi.advanceTimersByTime(NEAR_DEBOUNCE_MS);
    });

    expect(mockPage).toHaveBeenCalledWith("doc-1", 1, expect.any(AbortSignal));
    expect(scheduledPriorities).toEqual([1]);
  });

  it("acts immediately, at top priority, for a document already on screen", async () => {
    render(<Card overrides={{ previewStatus: "expired" }} />);

    // Visible skips the debounce entirely: an artificial delay in front of the
    // reader's eyes is exactly what the region is meant to avoid.
    report("visible", true);
    await act(async () => {
      await Promise.resolve();
    });

    expect(mockRegenerate).toHaveBeenCalledTimes(1);
  });

  it("fetches a visible document's page at top priority", async () => {
    render(<Card />);

    report("visible", true);
    await act(async () => {
      await Promise.resolve();
    });

    expect(scheduledPriorities).toEqual([0]);
  });

  it("regenerates once when the same document is carried near, away and near again", async () => {
    render(<Card overrides={{ previewStatus: "expired" }} />);

    report("near", true);
    await act(async () => {
      vi.advanceTimersByTime(NEAR_DEBOUNCE_MS);
    });
    report("near", false);
    report("near", true);
    await act(async () => {
      vi.advanceTimersByTime(NEAR_DEBOUNCE_MS * 4);
    });

    // The dedupe is what keeps a timeline of stale documents from queueing a
    // second render of the same file on every pass of the wheel.
    expect(mockRegenerate).toHaveBeenCalledTimes(1);
  });
});
