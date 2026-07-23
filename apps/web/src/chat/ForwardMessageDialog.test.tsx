import { act, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import { beforeEach, describe, expect, it, vi } from "vitest";

import ForwardMessageDialog from "./ForwardMessageDialog";
import type { Channel, Message } from "./chatTypes";

const { mockForwardChannelMessage } = vi.hoisted(() => ({
  mockForwardChannelMessage:
    vi.fn<
      (
        destinationChannelId: string,
        sourceMessageId: string,
        idempotencyKey: string,
        signal?: AbortSignal,
      ) => Promise<Message>
    >(),
}));

vi.mock("./chatApi", () => ({
  forwardChannelMessage: (
    destinationChannelId: string,
    sourceMessageId: string,
    idempotencyKey: string,
    signal?: AbortSignal,
  ) => mockForwardChannelMessage(destinationChannelId, sourceMessageId, idempotencyKey, signal),
}));

const sourceMessage: Message = {
  id: "source-1",
  senderId: "sender-1",
  senderDisplayName: "Origem",
  senderEmail: "origin@example.test",
  kind: "user",
  bodyText: "Mensagem original",
  bodyFormat: "v3",
  status: "active",
  isRemoved: false,
  createdAt: "2026-07-22T12:00:00Z",
  updatedAt: "2026-07-22T12:00:00Z",
  editCount: 0,
  isEdited: false,
  reactions: [],
  isFavorited: false,
  isForwarded: false,
};

const forwarded: Message = {
  ...sourceMessage,
  id: "forwarded-1",
  senderId: "current-user",
  senderDisplayName: "Usuário atual",
  isForwarded: true,
};

const source = { messageID: sourceMessage.id, sourceChannelID: "current" };

const channels: Channel[] = [
  { id: "current", name: "Atual", type: "public", canWrite: true },
  { id: "destination", name: "Destino", type: "public", canWrite: true },
];

function deferred<T>() {
  let resolve!: (value: T) => void;
  let reject!: (reason?: unknown) => void;
  const promise = new Promise<T>((promiseResolve, promiseReject) => {
    resolve = promiseResolve;
    reject = promiseReject;
  });
  return { promise, resolve, reject };
}

function renderDialog(onClose = vi.fn(), onSuccess = vi.fn(), dialogChannels = channels) {
  render(
    <ForwardMessageDialog
      source={source}
      channels={dialogChannels}
      onClose={onClose}
      onSuccess={onSuccess}
    />,
  );
  return { onClose, onSuccess };
}

async function selectAndSubmit() {
  const dialog = screen.getByRole("dialog", { name: "Encaminhar mensagem" });
  const dialogQueries = within(dialog);
  await userEvent.click(dialogQueries.getByRole("button", { name: "Destino" }));
  await userEvent.dblClick(dialogQueries.getByRole("button", { name: "Encaminhar" }));
  return { dialog, dialogQueries };
}

beforeEach(() => {
  mockForwardChannelMessage.mockReset();
});

describe("ForwardMessageDialog selection", () => {
  it("blocks a destination hidden by the search and submits the newly visible selection", async () => {
    mockForwardChannelMessage.mockResolvedValue(forwarded);
    renderDialog(vi.fn(), vi.fn(), [
      { id: "current", name: "Atual", type: "public", canWrite: true },
      { id: "development", name: "Desenvolvimento", type: "public", canWrite: true },
      { id: "general", name: "Geral", type: "public", canWrite: true },
    ]);
    const dialog = within(screen.getByRole("dialog", { name: "Encaminhar mensagem" }));
    const confirm = dialog.getByRole("button", { name: "Encaminhar" });

    await userEvent.click(dialog.getByRole("button", { name: "Desenvolvimento" }));
    expect(confirm).toBeEnabled();

    await userEvent.type(dialog.getByRole("searchbox", { name: "Buscar canal" }), "Geral");
    expect(dialog.queryByRole("button", { name: "Desenvolvimento" })).not.toBeInTheDocument();
    expect(confirm).toBeDisabled();
    await userEvent.click(confirm);
    expect(mockForwardChannelMessage).not.toHaveBeenCalled();

    await userEvent.click(dialog.getByRole("button", { name: "Geral" }));
    expect(confirm).toBeEnabled();
    await userEvent.click(confirm);

    await waitFor(() =>
      expect(mockForwardChannelMessage).toHaveBeenCalledWith(
        "general",
        source.messageID,
        expect.any(String),
        expect.any(AbortSignal),
      ),
    );
    expect(mockForwardChannelMessage).toHaveBeenCalledTimes(1);
  });

  it("lists only writable destinations and distinguishes empty states", async () => {
    const { rerender } = render(
      <ForwardMessageDialog
        source={source}
        channels={[
          channels[0],
          { id: "read-only", name: "Somente leitura", type: "private", canWrite: false },
        ]}
        onClose={vi.fn()}
        onSuccess={vi.fn()}
      />,
    );
    expect(screen.queryByRole("button", { name: "Somente leitura" })).not.toBeInTheDocument();
    expect(screen.getByText("Não há outros canais disponíveis para encaminhamento.")).toBeVisible();

    rerender(
      <ForwardMessageDialog
        source={source}
        channels={channels}
        onClose={vi.fn()}
        onSuccess={vi.fn()}
      />,
    );
    await userEvent.type(screen.getByRole("searchbox", { name: "Buscar canal" }), "inexistente");
    expect(screen.getByText("Nenhum canal encontrado para a busca.")).toBeVisible();
    expect(screen.getByRole("button", { name: "Encaminhar" })).toBeDisabled();
  });

  it("removes a selected destination when updated props make it non-writable", async () => {
    const onSuccess = vi.fn();
    const view = render(
      <ForwardMessageDialog
        source={source}
        channels={channels}
        onClose={vi.fn()}
        onSuccess={onSuccess}
      />,
    );
    const dialog = within(screen.getByRole("dialog", { name: "Encaminhar mensagem" }));
    await userEvent.click(dialog.getByRole("button", { name: "Destino" }));
    expect(dialog.getByRole("button", { name: "Encaminhar" })).toBeEnabled();

    view.rerender(
      <ForwardMessageDialog
        source={source}
        channels={[channels[0], { ...channels[1], canWrite: false }]}
        onClose={vi.fn()}
        onSuccess={onSuccess}
      />,
    );

    expect(dialog.queryByRole("button", { name: "Destino" })).not.toBeInTheDocument();
    expect(dialog.getByRole("button", { name: "Encaminhar" })).toBeDisabled();
    await userEvent.click(dialog.getByRole("button", { name: "Encaminhar" }));
    expect(mockForwardChannelMessage).not.toHaveBeenCalled();
  });
});

describe("ForwardMessageDialog lifecycle and focus", () => {
  it("does not abort or restore focus when inline callbacks change", async () => {
    let signal: AbortSignal | undefined;
    mockForwardChannelMessage.mockImplementation(
      (_destinationId, _sourceId, _idempotencyKey, requestSignal) => {
        signal = requestSignal;
        return new Promise((_resolve, reject) => {
          requestSignal?.addEventListener(
            "abort",
            () => reject(new DOMException("Aborted", "AbortError")),
            { once: true },
          );
        });
      },
    );
    const firstClose = vi.fn();
    const latestClose = vi.fn();
    const view = render(
      <ForwardMessageDialog
        source={source}
        channels={channels}
        onClose={firstClose}
        onSuccess={vi.fn()}
      />,
    );
    const search = screen.getByRole("searchbox", { name: "Buscar canal" });
    expect(search).toHaveFocus();
    await selectAndSubmit();
    const focusedBeforeRerender = document.activeElement;

    view.rerender(
      <ForwardMessageDialog
        source={source}
        channels={channels}
        onClose={latestClose}
        onSuccess={vi.fn()}
      />,
    );
    expect(signal?.aborted).toBe(false);
    expect(document.activeElement).toBe(focusedBeforeRerender);

    fireEvent.keyDown(document, { key: "Escape" });
    expect(signal?.aborted).toBe(true);
    expect(firstClose).not.toHaveBeenCalled();
    expect(latestClose).toHaveBeenCalledTimes(1);
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
  });

  it("aborts on unmount and restores the previously focused element", async () => {
    const opener = document.createElement("button");
    document.body.append(opener);
    opener.focus();
    let signal: AbortSignal | undefined;
    mockForwardChannelMessage.mockImplementation(
      (_destinationId, _sourceId, _idempotencyKey, requestSignal) => {
        signal = requestSignal;
        return new Promise(() => undefined);
      },
    );
    const view = render(
      <ForwardMessageDialog
        source={source}
        channels={channels}
        onClose={vi.fn()}
        onSuccess={vi.fn()}
      />,
    );
    await selectAndSubmit();

    view.unmount();

    expect(signal?.aborted).toBe(true);
    expect(opener).toHaveFocus();
    opener.remove();
  });

  it("traps Tab in both directions and ignores disabled controls", async () => {
    renderDialog();
    const dialog = within(screen.getByRole("dialog", { name: "Encaminhar mensagem" }));
    await userEvent.click(dialog.getByRole("button", { name: "Destino" }));
    const close = dialog.getByRole("button", { name: "Fechar" });
    const confirm = dialog.getByRole("button", { name: "Encaminhar" });
    confirm.focus();

    fireEvent.keyDown(document, { key: "Tab" });
    expect(close).toHaveFocus();
    close.focus();
    fireEvent.keyDown(document, { key: "Tab", shiftKey: true });
    expect(confirm).toHaveFocus();

    const search = dialog.getByRole("searchbox", { name: "Buscar canal" });
    search.focus();
    fireEvent.keyDown(document, { key: "Tab" });
    expect(search).toHaveFocus();
  });

  it("handles Tab safely when no dialog control is focusable", () => {
    renderDialog();
    const dialog = screen.getByRole("dialog", { name: "Encaminhar mensagem" });
    for (const control of dialog.querySelectorAll<HTMLInputElement | HTMLButtonElement>(
      "input, button",
    )) {
      control.disabled = true;
    }
    expect(() => fireEvent.keyDown(document, { key: "Tab" })).not.toThrow();
  });
});

describe("ForwardMessageDialog cancellation", () => {
  it.each(["button", "backdrop", "escape"] as const)(
    "aborts through %s, restores loading, and permits one new request",
    async (closeMethod) => {
      let firstSignal: AbortSignal | undefined;
      const secondRequest = deferred<Message>();
      mockForwardChannelMessage
        .mockImplementationOnce((_destinationId, _sourceId, _idempotencyKey, signal) => {
          firstSignal = signal;
          return new Promise((_resolve, reject) => {
            signal?.addEventListener(
              "abort",
              () => reject(new DOMException("Aborted", "AbortError")),
              { once: true },
            );
          });
        })
        .mockReturnValueOnce(secondRequest.promise);
      const { onClose, onSuccess } = renderDialog();
      const { dialog, dialogQueries } = await selectAndSubmit();

      expect(mockForwardChannelMessage).toHaveBeenCalledTimes(1);
      expect(dialogQueries.getByRole("button", { name: "Encaminhando…" })).toBeDisabled();

      if (closeMethod === "button") {
        await userEvent.click(dialogQueries.getByRole("button", { name: "Fechar" }));
      } else if (closeMethod === "backdrop") {
        fireEvent.mouseDown(dialog.parentElement!);
      } else {
        fireEvent.keyDown(document, { key: "Escape" });
      }

      expect(firstSignal?.aborted).toBe(true);
      expect(onClose).toHaveBeenCalledTimes(1);
      expect(screen.queryByRole("alert")).not.toBeInTheDocument();
      const restoredConfirm = await dialogQueries.findByRole("button", { name: "Encaminhar" });
      await waitFor(() => expect(restoredConfirm).toBeEnabled());

      await userEvent.dblClick(restoredConfirm);
      await waitFor(() => expect(mockForwardChannelMessage).toHaveBeenCalledTimes(2));
      await act(async () => {
        secondRequest.resolve(forwarded);
        await secondRequest.promise;
      });
      await waitFor(() => expect(onSuccess).toHaveBeenCalledTimes(1));
    },
  );

  it("aborts on close, reopens cleanly, and accepts a new confirmation", async () => {
    let firstSignal: AbortSignal | undefined;
    const secondRequest = deferred<Message>();
    mockForwardChannelMessage
      .mockImplementationOnce((_destinationId, _sourceId, _idempotencyKey, signal) => {
        firstSignal = signal;
        return new Promise((_resolve, reject) => {
          signal?.addEventListener(
            "abort",
            () => reject(new DOMException("Aborted", "AbortError")),
            { once: true },
          );
        });
      })
      .mockReturnValueOnce(secondRequest.promise);
    const onSuccess = vi.fn();

    function Harness() {
      const [open, setOpen] = useState(true);
      return (
        <>
          <button type="button" onClick={() => setOpen(true)}>
            Reabrir modal
          </button>
          {open && (
            <ForwardMessageDialog
              source={source}
              channels={channels}
              onClose={() => setOpen(false)}
              onSuccess={() => {
                onSuccess();
                setOpen(false);
              }}
            />
          )}
        </>
      );
    }

    render(<Harness />);
    let dialogQueries = (await selectAndSubmit()).dialogQueries;
    await userEvent.click(dialogQueries.getByRole("button", { name: "Fechar" }));
    expect(firstSignal?.aborted).toBe(true);
    expect(screen.queryByRole("dialog", { name: "Encaminhar mensagem" })).not.toBeInTheDocument();
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();

    await userEvent.click(screen.getByRole("button", { name: "Reabrir modal" }));
    dialogQueries = (await selectAndSubmit()).dialogQueries;
    expect(mockForwardChannelMessage).toHaveBeenCalledTimes(2);
    expect(mockForwardChannelMessage.mock.calls[1]?.[2]).not.toBe(
      mockForwardChannelMessage.mock.calls[0]?.[2],
    );
    expect(dialogQueries.getByRole("button", { name: "Encaminhando…" })).toBeDisabled();

    await act(async () => {
      secondRequest.resolve(forwarded);
      await secondRequest.promise;
    });
    await waitFor(() => expect(onSuccess).toHaveBeenCalledTimes(1));
  });

  it("does not let a late request from a closed dialog affect a new opening", async () => {
    const firstRequest = deferred<Message>();
    const secondRequest = deferred<Message>();
    let firstSignal: AbortSignal | undefined;
    mockForwardChannelMessage
      .mockImplementationOnce((_destinationId, _sourceId, _idempotencyKey, signal) => {
        firstSignal = signal;
        return firstRequest.promise;
      })
      .mockReturnValueOnce(secondRequest.promise);
    const onSuccess = vi.fn();
    function Harness() {
      const [open, setOpen] = useState(true);
      return (
        <>
          <button type="button" onClick={() => setOpen(true)}>
            Reabrir modal
          </button>
          {open && (
            <ForwardMessageDialog
              source={source}
              channels={channels}
              onClose={() => setOpen(false)}
              onSuccess={() => {
                onSuccess();
                setOpen(false);
              }}
            />
          )}
        </>
      );
    }
    render(<Harness />);
    let dialog = (await selectAndSubmit()).dialogQueries;
    await userEvent.click(dialog.getByRole("button", { name: "Fechar" }));
    expect(firstSignal?.aborted).toBe(true);
    await userEvent.click(screen.getByRole("button", { name: "Reabrir modal" }));
    dialog = (await selectAndSubmit()).dialogQueries;

    await act(async () => {
      firstRequest.resolve(forwarded);
      await firstRequest.promise;
    });
    expect(onSuccess).not.toHaveBeenCalled();
    expect(dialog.getByRole("button", { name: "Encaminhando…" })).toBeDisabled();

    await act(async () => {
      secondRequest.resolve(forwarded);
      await secondRequest.promise;
    });
    await waitFor(() => expect(onSuccess).toHaveBeenCalledTimes(1));
  });
});

describe("ForwardMessageDialog retry", () => {
  it("restores controls after a real HTTP error and permits one retry", async () => {
    const failedRequest = deferred<Message>();
    const retryRequest = deferred<Message>();
    mockForwardChannelMessage
      .mockReturnValueOnce(failedRequest.promise)
      .mockReturnValueOnce(retryRequest.promise);
    const { onSuccess } = renderDialog();
    const { dialogQueries } = await selectAndSubmit();

    failedRequest.reject(new Error("HTTP 500"));
    expect(await dialogQueries.findByRole("alert")).toHaveTextContent(
      "Não foi possível encaminhar a mensagem",
    );
    const retryButton = dialogQueries.getByRole("button", { name: "Encaminhar" });
    expect(retryButton).toBeEnabled();

    await userEvent.dblClick(retryButton);
    expect(mockForwardChannelMessage).toHaveBeenCalledTimes(2);
    expect(mockForwardChannelMessage.mock.calls[1]?.[2]).toBe(
      mockForwardChannelMessage.mock.calls[0]?.[2],
    );
    expect(dialogQueries.getByRole("button", { name: "Encaminhando…" })).toBeDisabled();

    await act(async () => {
      retryRequest.resolve(forwarded);
      await retryRequest.promise;
    });
    await waitFor(() => expect(onSuccess).toHaveBeenCalledTimes(1));
    expect(dialogQueries.getByRole("button", { name: "Encaminhar" })).toBeEnabled();
  });

  it("shows non-abort DOM failures and restores controls", async () => {
    mockForwardChannelMessage.mockRejectedValue(
      new DOMException("Network unavailable", "NetworkError"),
    );
    renderDialog();
    const { dialogQueries } = await selectAndSubmit();

    expect(await dialogQueries.findByRole("alert")).toHaveTextContent(
      "Não foi possível encaminhar a mensagem",
    );
    expect(dialogQueries.getByRole("button", { name: "Encaminhar" })).toBeEnabled();
  });
});
