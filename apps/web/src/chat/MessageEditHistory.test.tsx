import { act, cleanup, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import type { MessageEditHistoryEntry } from "./chatTypes";
import MessageEditHistory from "./MessageEditHistory";

type HistoryPage = { entries: MessageEditHistoryEntry[]; nextCursor?: string };

const mockGetMessageHistory = vi.hoisted(() =>
  vi.fn<(messageId: string, opts: { cursor?: string; limit?: number }) => Promise<HistoryPage>>(),
);

vi.mock("./chatApi", () => ({
  getMessageHistory: (messageId: string, opts: { cursor?: string; limit?: number }) =>
    mockGetMessageHistory(messageId, opts),
}));

function entry(body: string, versionedAt: string): MessageEditHistoryEntry {
  return { body, bodyFormat: 2, versionedAt };
}

function deferred<T>() {
  let resolve!: (value: T) => void;
  let reject!: (reason?: unknown) => void;
  const promise = new Promise<T>((done, fail) => {
    resolve = done;
    reject = fail;
  });
  return { promise, resolve, reject };
}

beforeEach(() => mockGetMessageHistory.mockReset());
afterEach(cleanup);

describe("MessageEditHistory", () => {
  it("shows an initial network error and replaces the empty state after retry", async () => {
    mockGetMessageHistory.mockRejectedValueOnce(new Error("network")).mockResolvedValueOnce({
      entries: [entry("Versão recuperada", "2026-07-13T12:00:00Z")],
    });

    render(<MessageEditHistory messageId="msg-1" onClose={vi.fn()} />);

    expect(await screen.findByRole("alert")).toHaveTextContent(
      "Não foi possível carregar o histórico.",
    );
    await userEvent.click(screen.getByRole("button", { name: "Tentar novamente" }));

    expect(await screen.findByText("Versão recuperada")).toBeInTheDocument();
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
    expect(mockGetMessageHistory).toHaveBeenLastCalledWith("msg-1", {
      cursor: undefined,
      limit: 50,
    });
  });

  it("shows the empty-history state without pagination", async () => {
    mockGetMessageHistory.mockResolvedValue({ entries: [] });

    render(<MessageEditHistory messageId="msg-1" onClose={vi.fn()} />);

    expect(await screen.findByText("Nenhuma edição anterior.")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Carregar mais" })).not.toBeInTheDocument();
  });

  it("keeps existing entries and exposes loading while fetching the next page", async () => {
    const nextPage = deferred<HistoryPage>();
    mockGetMessageHistory
      .mockResolvedValueOnce({
        entries: [entry("Versão recente", "2026-07-13T12:00:00Z")],
        nextCursor: "cursor-2",
      })
      .mockReturnValueOnce(nextPage.promise);

    render(<MessageEditHistory messageId="msg-1" onClose={vi.fn()} />);

    expect(await screen.findByText("Versão recente")).toBeInTheDocument();
    const loadMore = screen.getByRole("button", { name: "Carregar mais" });
    await userEvent.click(loadMore);

    expect(loadMore).toBeDisabled();
    expect(screen.getByText("Carregando histórico…")).toBeInTheDocument();

    act(() => {
      nextPage.resolve({
        entries: [entry("Versão inicial", "2026-07-13T11:00:00Z")],
      });
    });

    expect(await screen.findByText("Versão inicial")).toBeInTheDocument();
    expect(screen.getByText("Versão recente")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Carregar mais" })).not.toBeInTheDocument();
    expect(mockGetMessageHistory).toHaveBeenLastCalledWith("msg-1", {
      cursor: "cursor-2",
      limit: 50,
    });
  });

  it("preserves loaded entries and retries the failed page with the same cursor", async () => {
    mockGetMessageHistory
      .mockResolvedValueOnce({
        entries: [entry("Versão recente", "2026-07-13T12:00:00Z")],
        nextCursor: "cursor-2",
      })
      .mockRejectedValueOnce(new Error("network"))
      .mockResolvedValueOnce({
        entries: [entry("Versão inicial", "2026-07-13T11:00:00Z")],
      });

    render(<MessageEditHistory messageId="msg-1" onClose={vi.fn()} />);

    expect(await screen.findByText("Versão recente")).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Carregar mais" }));

    expect(await screen.findByRole("alert")).toHaveTextContent(
      "Não foi possível carregar o histórico.",
    );
    expect(screen.getByText("Versão recente")).toBeInTheDocument();

    await userEvent.click(screen.getByRole("button", { name: "Tentar novamente" }));

    expect(await screen.findByText("Versão inicial")).toBeInTheDocument();
    expect(screen.getByText("Versão recente")).toBeInTheDocument();
    expect(mockGetMessageHistory).toHaveBeenLastCalledWith("msg-1", {
      cursor: "cursor-2",
      limit: 50,
    });
  });

  it("closes on Escape or backdrop interaction, but not inside the dialog", async () => {
    mockGetMessageHistory.mockResolvedValue({ entries: [] });
    const onClose = vi.fn();

    render(<MessageEditHistory messageId="msg-1" onClose={onClose} />);
    const dialog = await screen.findByRole("dialog");

    await userEvent.click(dialog);
    expect(onClose).not.toHaveBeenCalled();

    await userEvent.keyboard("a");
    expect(onClose).not.toHaveBeenCalled();

    await userEvent.keyboard("{Escape}");
    expect(onClose).toHaveBeenCalledTimes(1);

    await userEvent.click(dialog.parentElement!);
    expect(onClose).toHaveBeenCalledTimes(2);
  });

  it("ignores a late initial response after the history dialog is unmounted", async () => {
    const successfulRequest = deferred<HistoryPage>();
    mockGetMessageHistory.mockReturnValueOnce(successfulRequest.promise);
    const first = render(<MessageEditHistory messageId="msg-1" onClose={vi.fn()} />);

    first.unmount();
    await act(async () => {
      successfulRequest.resolve({ entries: [entry("Tardia", "2026-07-13T12:00:00Z")] });
      await successfulRequest.promise;
    });

    const failedRequest = deferred<HistoryPage>();
    mockGetMessageHistory.mockReturnValueOnce(failedRequest.promise);
    const second = render(<MessageEditHistory messageId="msg-2" onClose={vi.fn()} />);

    second.unmount();
    await act(async () => {
      failedRequest.reject(new Error("late network error"));
      await expect(failedRequest.promise).rejects.toThrow("late network error");
    });

    expect(document.querySelector('[role="dialog"]')).not.toBeInTheDocument();
  });
});
