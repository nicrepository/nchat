/**
 * useChatEditor unit tests — hook-level coverage.
 *
 * Uses renderHook to test state transitions and branch paths directly
 * (result.status === "stale", catch, canSend=false guard, sending flag).
 * Complements the integration tests in ChatMessageArea.test.tsx.
 */

import { act, renderHook, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { useChatEditor } from "./useChatEditor";

import type { SendResult } from "./useMessages";

// ── Helpers ───────────────────────────────────────────────────────────────────

type OnSend = (body: string) => Promise<SendResult>;
const mockOnSend = vi.fn() as ReturnType<typeof vi.fn> & OnSend;

const defaults = {
  placeholder: "Mensagem...",
  disabled: false,
  bodyFormat: "v2" as const,
  onSend: mockOnSend,
};

/** Wait for the editor to be initialised by useEditor (async on first render). */
async function waitForEditor(result: { current: ReturnType<typeof useChatEditor> }) {
  await waitFor(() => expect(result.current.editor).not.toBeNull(), { timeout: 3000 });
}

/** Insert text into the editor and wait for canSend to flip to true. */
async function fill(result: { current: ReturnType<typeof useChatEditor> }, text: string) {
  act(() => {
    result.current.editor!.commands.insertContent(text);
  });
  await waitFor(() => expect(result.current.canSend).toBe(true));
}

// ── Tests ─────────────────────────────────────────────────────────────────────

beforeEach(() => {
  mockOnSend.mockReset();
});

describe("useChatEditor — initial state", () => {
  it("starts with editor null, canSend false, sending false", () => {
    const { result } = renderHook(() => useChatEditor(defaults));
    // Before first effect: editor is null (useEditor is async).
    expect(result.current.canSend).toBe(false);
    expect(result.current.sending).toBe(false);
  });

  it("editor becomes non-null after mount", async () => {
    const { result } = renderHook(() => useChatEditor(defaults));
    await waitForEditor(result);
    expect(result.current.editor).not.toBeNull();
  });

  it("canSend=false when editor is empty", async () => {
    const { result } = renderHook(() => useChatEditor(defaults));
    await waitForEditor(result);
    expect(result.current.canSend).toBe(false);
  });
});

describe("useChatEditor — canSend guard", () => {
  it("handleSend does nothing when canSend=false (empty editor)", async () => {
    const { result } = renderHook(() => useChatEditor(defaults));
    await waitForEditor(result);

    await act(() => result.current.handleSend());

    expect(mockOnSend).not.toHaveBeenCalled();
  });

  it("handleSend skips onSend when serialized body is whitespace-only", async () => {
    // canSend=true (editor is not empty) but body trims to "" → early return
    const { result } = renderHook(() => useChatEditor(defaults));
    await waitForEditor(result);
    // Insert whitespace; TipTap marks it as non-empty but serializer trims to ""
    act(() => {
      result.current.editor!.commands.insertContent("   ");
    });
    // Wait for hasContent to react (isEmpty may still be false for whitespace)
    await waitFor(() => expect(result.current.editor!.isEmpty).toBe(false));

    await act(() => result.current.handleSend());

    expect(mockOnSend).not.toHaveBeenCalled();
  });

  it("canSend becomes true once editor has content", async () => {
    const { result } = renderHook(() => useChatEditor(defaults));
    await waitForEditor(result);

    act(() => {
      result.current.editor!.commands.insertContent("hello");
    });

    await waitFor(() => expect(result.current.canSend).toBe(true));
  });

  it("disabled=true keeps canSend false even with content", async () => {
    const { result } = renderHook(() => useChatEditor({ ...defaults, disabled: true }));
    await waitForEditor(result);

    act(() => {
      result.current.editor!.commands.insertContent("hello");
    });

    // canSend must stay false while disabled, even with content.
    await waitFor(() => expect(result.current.editor?.isEmpty).toBe(false));
    expect(result.current.canSend).toBe(false);
  });

  it("disabled=true: handleSend does not call onSend", async () => {
    const { result } = renderHook(() => useChatEditor({ ...defaults, disabled: true }));
    await waitForEditor(result);
    act(() => {
      result.current.editor!.commands.insertContent("hello");
    });

    await act(() => result.current.handleSend());

    expect(mockOnSend).not.toHaveBeenCalled();
  });
});

describe("useChatEditor — successful send (status: sent)", () => {
  it("calls onSend with serialized markdown", async () => {
    mockOnSend.mockResolvedValue({ status: "sent" });
    const { result } = renderHook(() => useChatEditor(defaults));
    await waitForEditor(result);
    await fill(result, "hello world");

    await act(() => result.current.handleSend());

    expect(mockOnSend).toHaveBeenCalledWith("hello world");
  });

  it("clears editor after status:sent", async () => {
    mockOnSend.mockResolvedValue({ status: "sent" });
    const { result } = renderHook(() => useChatEditor(defaults));
    await waitForEditor(result);
    await fill(result, "clear me");

    await act(() => result.current.handleSend());

    await waitFor(() => expect(result.current.editor!.isEmpty).toBe(true));
  });

  it("canSend returns to false after successful clear", async () => {
    mockOnSend.mockResolvedValue({ status: "sent" });
    const { result } = renderHook(() => useChatEditor(defaults));
    await waitForEditor(result);
    await fill(result, "text");

    await act(() => result.current.handleSend());

    await waitFor(() => expect(result.current.canSend).toBe(false));
  });

  it("blocks editing while sending, then clears on success", async () => {
    let resolveSend!: (result: SendResult) => void;
    mockOnSend.mockReturnValue(new Promise((resolve) => (resolveSend = resolve)));
    const { result } = renderHook(() => useChatEditor(defaults));
    await waitForEditor(result);
    await fill(result, "sent");

    const pending = result.current.handleSend();
    await waitFor(() => expect(result.current.editor!.isEditable).toBe(false));
    await act(async () => resolveSend({ status: "sent" }));
    await pending;

    await waitFor(() => expect(result.current.editor!.isEditable).toBe(true));
    expect(result.current.editor!.isEmpty).toBe(true);
  });
});

describe("useChatEditor — stale send (status: stale)", () => {
  it("preserves editor content on status:stale", async () => {
    mockOnSend.mockResolvedValue({ status: "stale" });
    const { result } = renderHook(() => useChatEditor(defaults));
    await waitForEditor(result);
    await fill(result, "keep this");

    await act(() => result.current.handleSend());

    // clearContent must NOT have been called — content preserved.
    expect(result.current.editor!.isEmpty).toBe(false);
  });

  it("sending resets to false after stale result", async () => {
    mockOnSend.mockResolvedValue({ status: "stale" });
    const { result } = renderHook(() => useChatEditor(defaults));
    await waitForEditor(result);
    await fill(result, "stale msg");

    await act(() => result.current.handleSend());

    await waitFor(() => expect(result.current.sending).toBe(false));
  });
});

describe("useChatEditor — error path (catch branch)", () => {
  it("preserves content when onSend rejects", async () => {
    mockOnSend.mockRejectedValue(new Error("network error"));
    const { result } = renderHook(() => useChatEditor(defaults));
    await waitForEditor(result);
    await fill(result, "retry me");

    await act(() => result.current.handleSend());

    expect(result.current.editor!.isEmpty).toBe(false);
  });

  it("sending resets to false after error", async () => {
    mockOnSend.mockRejectedValue(new Error("fail"));
    const { result } = renderHook(() => useChatEditor(defaults));
    await waitForEditor(result);
    await fill(result, "boom");

    await act(() => result.current.handleSend());

    await waitFor(() => expect(result.current.sending).toBe(false));
  });
});

describe("useChatEditor — sending flag transitions", () => {
  it("sending resets to false after resolving in-flight request", async () => {
    // Verify the sending flag goes false regardless of final resolve value.
    mockOnSend.mockResolvedValue({ status: "sent" });
    const { result } = renderHook(() => useChatEditor(defaults));
    await waitForEditor(result);
    await fill(result, "test");

    await act(() => result.current.handleSend());

    // After settlement, sending must be false (not stuck).
    expect(result.current.sending).toBe(false);
  });

  it("canSend is false while sending is in progress", async () => {
    let resolve!: (v: { status: string }) => void;
    mockOnSend.mockReturnValue(
      new Promise<{ status: string }>((r) => {
        resolve = r;
      }),
    );
    const { result } = renderHook(() => useChatEditor(defaults));
    await waitForEditor(result);
    await fill(result, "in-flight");

    // Start send without awaiting — it's intentionally in-flight.
    const sendPromise = result.current.handleSend();

    // React batches setSending(true); flush and check.
    await act(async () => {
      await Promise.resolve(); // flush microtask queue
    });
    // While the request is pending canSend must be false (sending=true → disabled).
    expect(result.current.canSend).toBe(false);

    // Settle the promise.
    resolve({ status: "sent" });
    await act(async () => {
      await sendPromise;
    });
    await waitFor(() => expect(result.current.sending).toBe(false));
  });
});

// ── useEffect: null-editor guard ──────────────────────────────────────────────

describe("useChatEditor — useEffect guard", () => {
  it("guard `if (!editor) return` in sync useEffect: editor initializes and becomes non-null", async () => {
    // The useEffect has `if (!editor) return` to guard against a null editor
    // during the initial render cycle. This test verifies that after the hook
    // stabilises the editor is available and no error is thrown.
    const { result } = renderHook(() => useChatEditor(defaults));
    await waitForEditor(result);
    expect(result.current.editor).not.toBeNull();
  });
});

// ── bodyFormat switch (CHAT-378) ──────────────────────────────────────────────

type FormatProps = { bodyFormat: "v2" | "v3" };

describe("useChatEditor — bodyFormat switch on the same mounted composer", () => {
  it("v2 → v3 rebuilds the editor and applies the mention channel", async () => {
    const { result, rerender } = renderHook(
      (props: FormatProps) => useChatEditor({ ...defaults, ...props, channelId: "chan-1" }),
      { initialProps: { bodyFormat: "v2" } as FormatProps },
    );
    await waitForEditor(result);
    const v2Editor = result.current.editor;
    expect(v2Editor!.storage.mentionChannelContext).toBeUndefined();

    // Route change DM → channel: bodyFormat flips one render before useEditor
    // swaps the instance, so the effect must not assume the v3 command exists.
    rerender({ bodyFormat: "v3" });

    await waitFor(() =>
      expect(result.current.editor?.storage.mentionChannelContext?.channelId).toBe("chan-1"),
    );
  });

  it("v3 → v2 rebuilds the editor without the mention extension", async () => {
    const { result, rerender } = renderHook(
      (props: FormatProps) => useChatEditor({ ...defaults, ...props, channelId: "chan-1" }),
      { initialProps: { bodyFormat: "v2" } as FormatProps },
    );
    rerender({ bodyFormat: "v3" });
    await waitFor(() =>
      expect(result.current.editor?.storage.mentionChannelContext?.channelId).toBe("chan-1"),
    );

    rerender({ bodyFormat: "v2" });

    await waitFor(() =>
      expect(result.current.editor?.storage.mentionChannelContext).toBeUndefined(),
    );
  });
});
