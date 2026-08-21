import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";

import QueryStates from "./QueryStates";

const noop = () => {};

describe("QueryStates", () => {
  it("announces loading with a skeleton", () => {
    render(
      <QueryStates status="loading" message="" empty="vazio" isEmpty={false} onRetry={noop} />,
    );
    expect(screen.getByRole("status")).toHaveTextContent("Carregando…");
  });

  // The whole point of this component: these four are different answers and
  // must not read as one another.
  it("keeps a refusal apart from a failure and from an empty result", () => {
    const { rerender } = render(
      <QueryStates
        status="forbidden"
        message="Você não tem permissão para esta seção."
        empty="vazio"
        isEmpty={false}
        onRetry={noop}
      />,
    );
    expect(screen.getByRole("alert")).toHaveTextContent("não tem permissão");
    expect(screen.queryByRole("button", { name: "Tentar novamente" })).not.toBeInTheDocument();

    rerender(
      <QueryStates
        status="network"
        message="Falha de rede."
        empty="vazio"
        isEmpty={false}
        onRetry={noop}
      />,
    );
    expect(screen.getByRole("alert")).toHaveTextContent("Falha de rede.");
    expect(screen.getByRole("button", { name: "Tentar novamente" })).toBeInTheDocument();

    rerender(<QueryStates status="ready" message="" empty="Nada aqui." isEmpty onRetry={noop} />);
    expect(screen.getByText("Nada aqui.")).toBeInTheDocument();
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
  });

  it("retries on demand", async () => {
    const onRetry = vi.fn();
    render(
      <QueryStates status="error" message="boom" empty="vazio" isEmpty={false} onRetry={onRetry} />,
    );
    await userEvent.click(screen.getByRole("button", { name: "Tentar novamente" }));
    expect(onRetry).toHaveBeenCalledTimes(1);
  });

  it("renders nothing when the data is present", () => {
    const { container } = render(
      <QueryStates status="ready" message="" empty="vazio" isEmpty={false} onRetry={noop} />,
    );
    expect(container).toBeEmptyDOMElement();
  });
});
