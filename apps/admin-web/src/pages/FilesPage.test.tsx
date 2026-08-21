import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";

import { _resetCSRFToken } from "../api/client";
import { errorResponse, jsonResponse, renderWithSession } from "../test/harness";
import FilesPage from "./FilesPage";

const READ = ["admin.infrastructure.read"];
const MANAGE = ["admin.infrastructure.read", "admin.infrastructure.manage"];

const MIB = 1024 * 1024;
const BOUNDS = { min: MIB, max: 512 * MIB, default: 250 * MIB, unit: "bytes", step: MIB };
const WORKSPACE = { id: "w1", slug: "default", name: "NChat", status: "active" };

function listResponse(bytes = 250 * MIB) {
  return jsonResponse({
    data: {
      policies: [{ workspace: WORKSPACE, max_upload_bytes: bytes }],
      bounds: BOUNDS,
      gateway_hard_cap_bytes: 512 * MIB + 8192,
      deployment_managed: ["malware_scanning", "upload_concurrency"],
      pagination: { next_cursor: null, has_more: false },
    },
  });
}

afterEach(() => {
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
  _resetCSRFToken();
});

describe("FilesPage", () => {
  it("edits whole MiB and shows the unit and the server's range", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(listResponse()));
    renderWithSession(<FilesPage />, READ);

    expect(await screen.findByRole("heading", { name: "NChat" })).toBeInTheDocument();
    expect(screen.getByText("MiB")).toBeInTheDocument();
    expect(screen.getByLabelText("Tamanho máximo por arquivo")).toHaveValue("250");
    expect(screen.getByText(/Entre 1 e 512 MiB/)).toBeInTheDocument();
  });

  it("names the controls this console cannot change, including the gateway ceiling", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(listResponse()));
    renderWithSession(<FilesPage />, READ);

    expect(await screen.findByText(/Teto do gateway/)).toBeInTheDocument();
    expect(screen.getByText(/Verificação de malware/)).toBeInTheDocument();
    expect(screen.getByText(/uploads simultâneos/)).toBeInTheDocument();
  });

  it("falls back to the control's id when this build does not know its label", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue(
        jsonResponse({
          data: {
            policies: [{ workspace: WORKSPACE, max_upload_bytes: 250 * MIB }],
            bounds: BOUNDS,
            gateway_hard_cap_bytes: 512 * MIB,
            deployment_managed: ["some_future_control"],
            pagination: { next_cursor: null, has_more: false },
          },
        }),
      ),
    );
    renderWithSession(<FilesPage />, READ);

    // Showing the raw id beats silently shortening the list of protections.
    expect(await screen.findByText("some_future_control")).toBeInTheDocument();
  });

  it("is read-only without the managing capability", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(listResponse()));
    renderWithSession(<FilesPage />, READ);

    await screen.findByRole("heading", { name: "NChat" });
    expect(screen.queryByRole("button", { name: "Salvar" })).not.toBeInTheDocument();
    expect(screen.getByText(/Somente leitura/)).toBeInTheDocument();
  });

  it("refuses a value outside the range before sending it", async () => {
    const fetchMock = vi.fn().mockResolvedValue(listResponse());
    vi.stubGlobal("fetch", fetchMock);
    renderWithSession(<FilesPage />, MANAGE);

    const input = await screen.findByLabelText("Tamanho máximo por arquivo");
    await userEvent.clear(input);
    await userEvent.type(input, "513");

    expect(screen.getByRole("alert")).toHaveTextContent("entre 1 e 512 MiB");
    expect(screen.getByRole("button", { name: "Salvar" })).toBeDisabled();
    expect(fetchMock.mock.calls.filter((call) => call[1]?.method === "PATCH")).toHaveLength(0);
  });

  it("refuses a fractional MiB rather than rounding it", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(listResponse()));
    renderWithSession(<FilesPage />, MANAGE);

    const input = await screen.findByLabelText("Tamanho máximo por arquivo");
    await userEvent.clear(input);
    await userEvent.type(input, "1.5");

    expect(screen.getByRole("alert")).toHaveTextContent("inteiros de MiB");
  });

  it("refuses zero, so a size control never switches attachments off", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(listResponse()));
    renderWithSession(<FilesPage />, MANAGE);

    const input = await screen.findByLabelText("Tamanho máximo por arquivo");
    await userEvent.clear(input);
    await userEvent.type(input, "0");

    expect(screen.getByRole("alert")).toHaveTextContent("entre 1 e 512 MiB");
  });

  it("warns at the ceiling, which is permitted", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(listResponse()));
    renderWithSession(<FilesPage />, MANAGE);

    const input = await screen.findByLabelText("Tamanho máximo por arquivo");
    await userEvent.clear(input);
    await userEvent.type(input, "512");

    expect(screen.getByTestId("admin-policy-warning")).toHaveTextContent("teto permitido");
    expect(screen.getByRole("button", { name: "Salvar" })).toBeEnabled();
  });

  // The "atual" label is the value the *backend* confirmed. It advanced only on
  // success, and on failure it must keep showing what is really stored.
  it("advances the current-value label only after the server confirms", async () => {
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(listResponse(50 * MIB))
      .mockResolvedValue(
        jsonResponse({
          data: { policy: { workspace: WORKSPACE, max_upload_bytes: 100 * MIB }, bounds: BOUNDS },
        }),
      );
    vi.stubGlobal("fetch", fetchMock);
    renderWithSession(<FilesPage />, MANAGE);

    expect(await screen.findByText(/atual: 50 MiB/)).toBeInTheDocument();

    const input = screen.getByLabelText("Tamanho máximo por arquivo");
    await userEvent.clear(input);
    await userEvent.type(input, "100");
    // Typing alone changes nothing: "atual" is not a preview of the form.
    expect(screen.getByText(/atual: 50 MiB/)).toBeInTheDocument();

    await userEvent.click(screen.getByRole("button", { name: "Salvar" }));

    expect(await screen.findByTestId("admin-feedback")).toHaveTextContent("Limite salvo: 100 MiB");
    expect(screen.getByText(/atual: 100 MiB/)).toBeInTheDocument();
    expect(input).toHaveValue("100");
    expect(screen.queryByText(/atual: 50 MiB/)).not.toBeInTheDocument();
  });

  it("keeps the current-value label at the last confirmed value when a save fails", async () => {
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(listResponse(50 * MIB))
      .mockResolvedValue(errorResponse(409, "conflict"));
    vi.stubGlobal("fetch", fetchMock);
    renderWithSession(<FilesPage />, MANAGE);

    const input = await screen.findByLabelText("Tamanho máximo por arquivo");
    await userEvent.clear(input);
    await userEvent.type(input, "100");
    await userEvent.click(screen.getByRole("button", { name: "Salvar" }));

    await waitFor(() => expect(screen.getAllByRole("alert").length).toBeGreaterThan(0));
    // Nothing was applied, and the screen does not pretend otherwise.
    expect(screen.getByText(/atual: 50 MiB/)).toBeInTheDocument();
    expect(screen.queryByText(/atual: 100 MiB/)).not.toBeInTheDocument();
  });

  it("saves in bytes and confirms what was stored", async () => {
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(listResponse())
      .mockResolvedValue(
        jsonResponse({
          data: { policy: { workspace: WORKSPACE, max_upload_bytes: 100 * MIB }, bounds: BOUNDS },
        }),
      );
    vi.stubGlobal("fetch", fetchMock);
    renderWithSession(<FilesPage />, MANAGE);

    const input = await screen.findByLabelText("Tamanho máximo por arquivo");
    await userEvent.clear(input);
    await userEvent.type(input, "100");
    await userEvent.click(screen.getByRole("button", { name: "Salvar" }));

    expect(await screen.findByTestId("admin-feedback")).toHaveTextContent("Limite salvo: 100 MiB");
    const patch = fetchMock.mock.calls.find((call) => call[1]?.method === "PATCH");
    // The conversion happens at the edge and is exact; the server validates it
    // again independently.
    expect(JSON.parse(String(patch?.[1]?.body))).toEqual({ max_upload_bytes: 104857600 });
  });

  // A stored value that is not a whole MiB cannot be shown in this field
  // without being changed, so the form refuses to edit rather than rounding it
  // into a limit nobody typed.
  it("refuses to edit a stored value that is not a whole MiB", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(listResponse(1572864)));
    renderWithSession(<FilesPage />, MANAGE);

    expect(await screen.findByRole("alert")).toHaveTextContent("1572864 bytes");
    expect(screen.queryByLabelText("Tamanho máximo por arquivo")).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Salvar" })).not.toBeInTheDocument();
  });

  it("reports a rejected save", async () => {
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(listResponse())
      .mockResolvedValue(errorResponse(400, "bad_request"));
    vi.stubGlobal("fetch", fetchMock);
    renderWithSession(<FilesPage />, MANAGE);

    const input = await screen.findByLabelText("Tamanho máximo por arquivo");
    await userEvent.clear(input);
    await userEvent.type(input, "100");
    await userEvent.click(screen.getByRole("button", { name: "Salvar" }));

    await waitFor(() => expect(screen.getAllByRole("alert").length).toBeGreaterThan(0));
  });

  it("separates a refusal from a failure", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(errorResponse(403)));
    renderWithSession(<FilesPage />, READ);

    expect(await screen.findByRole("alert")).toHaveTextContent("não tem permissão");
  });

  it("says plainly when there is no workspace", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue(
        jsonResponse({
          data: {
            policies: [],
            bounds: BOUNDS,
            gateway_hard_cap_bytes: 512 * MIB,
            deployment_managed: [],
            pagination: { next_cursor: null, has_more: false },
          },
        }),
      ),
    );
    renderWithSession(<FilesPage />, READ);

    expect(await screen.findByText("Nenhum workspace encontrado.")).toBeInTheDocument();
  });
});
