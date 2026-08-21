import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";

import { _resetCSRFToken } from "../api/client";
import { errorResponse, jsonResponse, renderWithSession } from "../test/harness";
import SecurityPage from "./SecurityPage";

const READ = ["admin.security.read"];
const MANAGE = ["admin.security.read", "admin.security.manage"];

const BOUNDS = { min: 1, max: 600, default: 60, unit: "messages_per_minute" };
const WORKSPACE = { id: "w1", slug: "default", name: "NChat", status: "active" };

function listResponse(limit = 60) {
  return jsonResponse({
    data: {
      policies: [{ workspace: WORKSPACE, message_rate_limit_per_minute: limit }],
      bounds: BOUNDS,
      pagination: { next_cursor: null, has_more: false },
    },
  });
}

afterEach(() => {
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
  _resetCSRFToken();
});

describe("SecurityPage", () => {
  it("renders each workspace's limit with the unit and the server's range", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(listResponse()));
    renderWithSession(<SecurityPage />, READ);

    expect(await screen.findByRole("heading", { name: "NChat" })).toBeInTheDocument();
    // The unit is visible, not implied: "60" alone is ambiguous.
    expect(screen.getByText("msg/min")).toBeInTheDocument();
    expect(screen.getByText(/Entre 1 e 600/)).toBeInTheDocument();
    expect(screen.getByLabelText("Mensagens por minuto, por usuário")).toHaveValue("60");
  });

  it("explains why the minimum is not zero", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(listResponse()));
    renderWithSession(<SecurityPage />, READ);

    expect(await screen.findByText(/nunca sirva de silenciamento/)).toBeInTheDocument();
  });

  it("is read-only without the managing capability", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(listResponse()));
    renderWithSession(<SecurityPage />, READ);

    await screen.findByRole("heading", { name: "NChat" });
    expect(screen.queryByRole("button", { name: "Salvar" })).not.toBeInTheDocument();
    expect(screen.getByText(/Somente leitura/)).toBeInTheDocument();
  });

  it("refuses a value outside the server's range before sending it", async () => {
    const fetchMock = vi.fn().mockResolvedValue(listResponse());
    vi.stubGlobal("fetch", fetchMock);
    renderWithSession(<SecurityPage />, MANAGE);

    const input = await screen.findByLabelText("Mensagens por minuto, por usuário");
    await userEvent.clear(input);
    await userEvent.type(input, "0");

    expect(screen.getByRole("alert")).toHaveTextContent("entre 1 e 600");
    expect(screen.getByRole("button", { name: "Salvar" })).toBeDisabled();
    expect(input).toHaveAttribute("aria-invalid", "true");
    expect(fetchMock.mock.calls.filter((call) => call[1]?.method === "PATCH")).toHaveLength(0);
  });

  it("refuses input a Number() coercion would have accepted", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(listResponse()));
    renderWithSession(<SecurityPage />, MANAGE);

    const input = await screen.findByLabelText("Mensagens por minuto, por usuário");
    await userEvent.clear(input);
    await userEvent.type(input, "1.5");
    expect(screen.getByRole("alert")).toHaveTextContent("inteiros");
  });

  // A limit at the ceiling is permitted and worth saying out loud: permitted
  // and advisable are not the same claim.
  it("warns before a dangerous but permitted value is saved", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(listResponse()));
    renderWithSession(<SecurityPage />, MANAGE);

    const input = await screen.findByLabelText("Mensagens por minuto, por usuário");
    await userEvent.clear(input);
    await userEvent.type(input, "600");

    expect(screen.getByTestId("admin-policy-warning")).toHaveTextContent("teto permitido");
    expect(screen.getByRole("button", { name: "Salvar" })).toBeEnabled();
  });

  it("saves a valid value and confirms what was stored", async () => {
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(listResponse())
      .mockResolvedValueOnce(
        jsonResponse({
          data: {
            policy: { workspace: WORKSPACE, message_rate_limit_per_minute: 30 },
            bounds: BOUNDS,
          },
        }),
      )
      .mockResolvedValue(listResponse(30));
    vi.stubGlobal("fetch", fetchMock);
    renderWithSession(<SecurityPage />, MANAGE);

    const input = await screen.findByLabelText("Mensagens por minuto, por usuário");
    await userEvent.clear(input);
    await userEvent.type(input, "30");
    await userEvent.click(screen.getByRole("button", { name: "Salvar" }));

    expect(await screen.findByTestId("admin-feedback")).toHaveTextContent(
      "Limite salvo: 30 mensagens/minuto",
    );
    const patch = fetchMock.mock.calls.find((call) => call[1]?.method === "PATCH");
    expect(JSON.parse(String(patch?.[1]?.body))).toEqual({ message_rate_limit_per_minute: 30 });
  });

  it("reports a rejected save as a failure, not a success", async () => {
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(listResponse())
      .mockResolvedValue(errorResponse(400, "bad_request"));
    vi.stubGlobal("fetch", fetchMock);
    renderWithSession(<SecurityPage />, MANAGE);

    const input = await screen.findByLabelText("Mensagens por minuto, por usuário");
    await userEvent.clear(input);
    await userEvent.type(input, "30");
    await userEvent.click(screen.getByRole("button", { name: "Salvar" }));

    await waitFor(() => expect(screen.getAllByRole("alert").length).toBeGreaterThan(0));
  });

  // Naming what cannot be changed here is the alternative to offering a field
  // that would store a number nobody reads.
  it("names the controls that are not editable at runtime", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(listResponse()));
    renderWithSession(<SecurityPage />, READ);

    const fixed = await screen.findByRole("heading", {
      name: "Controles que não são editáveis aqui",
    });
    expect(fixed).toBeInTheDocument();
    expect(screen.getByText(/Janela do limite/)).toBeInTheDocument();
    expect(screen.getAllByText(/exige rollout/).length).toBeGreaterThan(0);
  });

  it("separates a refusal from a failure", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(errorResponse(403)));
    renderWithSession(<SecurityPage />, READ);

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
            pagination: { next_cursor: null, has_more: false },
          },
        }),
      ),
    );
    renderWithSession(<SecurityPage />, READ);

    expect(await screen.findByText("Nenhum workspace encontrado.")).toBeInTheDocument();
  });

  it("does not submit the same save twice", async () => {
    let settle: (value: Response) => void = () => {};
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(listResponse())
      .mockImplementationOnce(() => new Promise<Response>((resolve) => (settle = resolve)))
      .mockResolvedValue(listResponse(30));
    vi.stubGlobal("fetch", fetchMock);
    renderWithSession(<SecurityPage />, MANAGE);

    const input = await screen.findByLabelText("Mensagens por minuto, por usuário");
    await userEvent.clear(input);
    await userEvent.type(input, "30");
    const save = screen.getByRole("button", { name: "Salvar" });
    await userEvent.click(save);
    await userEvent.click(screen.getByRole("button", { name: "Salvando…" })).catch(() => {});

    expect(fetchMock.mock.calls.filter((call) => call[1]?.method === "PATCH")).toHaveLength(1);
    settle(
      jsonResponse({
        data: {
          policy: { workspace: WORKSPACE, message_rate_limit_per_minute: 30 },
          bounds: BOUNDS,
        },
      }),
    );
    await waitFor(() => expect(within(document.body).queryByText("Salvando…")).toBeNull());
  });
});
