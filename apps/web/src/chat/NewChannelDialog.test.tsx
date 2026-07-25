import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ComponentProps } from "react";
import { beforeEach, describe, expect, it, vi } from "vitest";

import { ApiRequestError } from "../lib/api";
import type { Channel } from "./chatTypes";
import NewChannelDialog from "./NewChannelDialog";

const { mockCreateChannel } = vi.hoisted(() => ({
  mockCreateChannel:
    vi.fn<
      (
        input: { slug: string; displayName: string; type: "public" | "private" },
        signal?: AbortSignal,
      ) => Promise<Channel>
    >(),
}));

vi.mock("./chatApi", () => ({
  createChannel: (
    input: { slug: string; displayName: string; type: "public" | "private" },
    signal?: AbortSignal,
  ) => mockCreateChannel(input, signal),
}));

function deferred<T>() {
  let resolve!: (value: T) => void;
  let reject!: (reason?: unknown) => void;
  const promise = new Promise<T>((done, fail) => {
    resolve = done;
    reject = fail;
  });
  return { promise, resolve, reject };
}

function renderDialog(overrides: Partial<ComponentProps<typeof NewChannelDialog>> = {}) {
  const props = { onClose: vi.fn(), onCreated: vi.fn(), ...overrides };
  render(<NewChannelDialog {...props} />);
  return props;
}

const createdChannel: Channel = {
  id: "ch-1",
  name: "Infraestrutura",
  type: "public",
  canWrite: true,
};

const nameField = () => screen.getByLabelText(/nome do canal/i);
const slugField = () => screen.getByLabelText(/identificador/i);
const submitButton = () => screen.getByRole("button", { name: /criar canal/i });

beforeEach(() => {
  vi.clearAllMocks();
});

describe("NewChannelDialog — nominal flow", () => {
  it("creates a channel from the typed name and hands the ID back", async () => {
    mockCreateChannel.mockResolvedValue(createdChannel);
    const props = renderDialog();

    fireEvent.change(nameField(), { target: { value: "Infraestrutura" } });
    fireEvent.click(submitButton());

    await waitFor(() => expect(props.onCreated).toHaveBeenCalledWith("ch-1"));
    expect(mockCreateChannel).toHaveBeenCalledTimes(1);
    expect(mockCreateChannel.mock.calls[0][0]).toEqual({
      slug: "infraestrutura",
      displayName: "Infraestrutura",
      type: "public",
    });
  });

  it("derives the slug from the name until the user edits it, then leaves it alone", async () => {
    mockCreateChannel.mockResolvedValue(createdChannel);
    renderDialog();

    fireEvent.change(nameField(), { target: { value: "Operações Críticas" } });
    expect(slugField()).toHaveValue("operacoes-criticas");

    fireEvent.change(slugField(), { target: { value: "ops" } });
    fireEvent.change(nameField(), { target: { value: "Outro nome" } });
    expect(slugField()).toHaveValue("ops");

    fireEvent.click(submitButton());
    await waitFor(() => expect(mockCreateChannel).toHaveBeenCalled());
    expect(mockCreateChannel.mock.calls[0][0].slug).toBe("ops");
  });

  it("sends the selected channel type", async () => {
    mockCreateChannel.mockResolvedValue({ ...createdChannel, type: "private" });
    renderDialog();

    fireEvent.change(nameField(), { target: { value: "Diretoria" } });
    fireEvent.click(screen.getByRole("radio", { name: /privado/i }));
    fireEvent.click(submitButton());

    await waitFor(() => expect(mockCreateChannel).toHaveBeenCalled());
    expect(mockCreateChannel.mock.calls[0][0].type).toBe("private");
  });
});

describe("NewChannelDialog — validation", () => {
  it("keeps submit unavailable until a name is typed", () => {
    renderDialog();
    expect(submitButton()).toBeDisabled();
    fireEvent.change(nameField(), { target: { value: "Infra" } });
    expect(submitButton()).toBeEnabled();
  });

  it("reports the reserved slug without calling the API", async () => {
    renderDialog();

    fireEvent.change(nameField(), { target: { value: "Geral" } });
    fireEvent.click(submitButton());

    expect(await screen.findByRole("alert")).toHaveTextContent(/reservado/i);
    expect(mockCreateChannel).not.toHaveBeenCalled();
  });

  it("reports a malformed slug without calling the API", async () => {
    renderDialog();

    fireEvent.change(nameField(), { target: { value: "Infra" } });
    fireEvent.change(slugField(), { target: { value: "-infra" } });
    fireEvent.click(submitButton());

    expect(await screen.findByRole("alert")).toHaveTextContent(/minúsculas/i);
    expect(mockCreateChannel).not.toHaveBeenCalled();
  });
});

describe("NewChannelDialog — API errors", () => {
  // A denial and an outage are different problems with different fixes, so the
  // status must reach the user as distinct wording rather than one generic line.
  it.each([
    [403, /permissão/i],
    [401, /sessão/i],
    [409, /já existe/i],
    [400, /revise/i],
    [429, /aguarde/i],
    [0, /conexão/i],
  ])("explains status %i in its own terms", async (status, expected) => {
    mockCreateChannel.mockRejectedValue(new ApiRequestError(status, "err", "server detail"));
    const props = renderDialog();

    fireEvent.change(nameField(), { target: { value: "Infra" } });
    fireEvent.click(submitButton());

    const alert = await screen.findByRole("alert");
    expect(alert).toHaveTextContent(expected);
    // Server-provided text is never echoed back into the UI.
    expect(alert).not.toHaveTextContent(/server detail/i);
    expect(props.onCreated).not.toHaveBeenCalled();
  });

  it("keeps the dialog open with the fields intact so a retry costs one click", async () => {
    mockCreateChannel.mockRejectedValueOnce(new ApiRequestError(500, "err", "boom"));
    const props = renderDialog();

    fireEvent.change(nameField(), { target: { value: "Infra" } });
    fireEvent.click(submitButton());
    await screen.findByRole("alert");

    expect(props.onClose).not.toHaveBeenCalled();
    expect(nameField()).toHaveValue("Infra");

    mockCreateChannel.mockResolvedValueOnce(createdChannel);
    fireEvent.click(submitButton());
    await waitFor(() => expect(props.onCreated).toHaveBeenCalledWith("ch-1"));
  });
});

describe("NewChannelDialog — double submit", () => {
  it("sends exactly one request when the button is clicked repeatedly", async () => {
    const pending = deferred<Channel>();
    mockCreateChannel.mockReturnValue(pending.promise);
    renderDialog();

    fireEvent.change(nameField(), { target: { value: "Infra" } });
    fireEvent.click(submitButton());
    fireEvent.click(submitButton());
    fireEvent.click(submitButton());

    expect(mockCreateChannel).toHaveBeenCalledTimes(1);
    expect(submitButton()).toBeDisabled();
    expect(submitButton()).toHaveAttribute("aria-busy", "true");

    await act(async () => {
      pending.resolve(createdChannel);
      await pending.promise;
    });
  });

  it("refuses to close while a creation is in flight", async () => {
    const pending = deferred<Channel>();
    mockCreateChannel.mockReturnValue(pending.promise);
    const props = renderDialog();

    fireEvent.change(nameField(), { target: { value: "Infra" } });
    fireEvent.click(submitButton());

    fireEvent.keyDown(screen.getByRole("dialog"), { key: "Escape" });
    expect(props.onClose).not.toHaveBeenCalled();

    await act(async () => {
      pending.resolve(createdChannel);
      await pending.promise;
    });
  });
});

// The dialog is a real <form>, so Enter from any field submits it. jsdom does
// not implement implicit form submission on its own — userEvent does, which is
// why these use it instead of fireEvent.
describe("NewChannelDialog — keyboard submission", () => {
  it("creates the channel when Enter is pressed in the name field", async () => {
    const user = userEvent.setup();
    mockCreateChannel.mockResolvedValue(createdChannel);
    const props = renderDialog();

    await user.click(nameField());
    await user.keyboard("Infraestrutura{Enter}");

    await waitFor(() => expect(props.onCreated).toHaveBeenCalledWith("ch-1"));
    expect(mockCreateChannel).toHaveBeenCalledTimes(1);
    expect(mockCreateChannel.mock.calls[0][0]).toEqual({
      slug: "infraestrutura",
      displayName: "Infraestrutura",
      type: "public",
    });
  });

  it("creates the channel when Enter is pressed in the slug field", async () => {
    const user = userEvent.setup();
    mockCreateChannel.mockResolvedValue(createdChannel);
    const props = renderDialog();

    await user.click(nameField());
    await user.keyboard("Infra");
    await user.click(slugField());
    await user.keyboard("{Enter}");

    await waitFor(() => expect(props.onCreated).toHaveBeenCalledWith("ch-1"));
    expect(mockCreateChannel).toHaveBeenCalledTimes(1);
  });

  it("does not submit on Enter while the name is still empty", async () => {
    const user = userEvent.setup();
    const props = renderDialog();

    await user.click(nameField());
    await user.keyboard("{Enter}");

    expect(mockCreateChannel).not.toHaveBeenCalled();
    expect(props.onCreated).not.toHaveBeenCalled();
  });

  // A keyboard submission must not be able to bypass a validation a click honours.
  it("reports the reserved slug on Enter without calling the API", async () => {
    const user = userEvent.setup();
    renderDialog();

    await user.click(nameField());
    await user.keyboard("Geral{Enter}");

    expect(await screen.findByRole("alert")).toHaveTextContent(/reservado/i);
    expect(mockCreateChannel).not.toHaveBeenCalled();
  });

  it("sends one request when Enter is pressed repeatedly during a creation", async () => {
    const user = userEvent.setup();
    const pending = deferred<Channel>();
    mockCreateChannel.mockReturnValue(pending.promise);
    renderDialog();

    await user.click(nameField());
    await user.keyboard("Infra{Enter}");
    await user.keyboard("{Enter}{Enter}");

    expect(mockCreateChannel).toHaveBeenCalledTimes(1);

    await act(async () => {
      pending.resolve(createdChannel);
      await pending.promise;
    });
  });

  // Enter submits; Escape still closes. The two must not have merged.
  it("still closes on Escape without submitting", async () => {
    const user = userEvent.setup();
    const props = renderDialog();

    await user.click(nameField());
    await user.keyboard("Infra{Escape}");

    expect(props.onClose).toHaveBeenCalled();
    expect(mockCreateChannel).not.toHaveBeenCalled();
  });
});
