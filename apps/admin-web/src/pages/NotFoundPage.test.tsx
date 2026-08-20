import { render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { describe, expect, it } from "vitest";

import NotFoundPage from "./NotFoundPage";

describe("NotFoundPage", () => {
  // It offers a way back and no fake action, which is the whole point of the
  // page existing instead of a blank shell.
  it("states that the section does not exist and links back", () => {
    render(
      <MemoryRouter>
        <NotFoundPage />
      </MemoryRouter>,
    );

    expect(screen.getByRole("heading", { name: "Seção não disponível" })).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Voltar para a visão geral" })).toHaveAttribute(
      "href",
      "/",
    );
    expect(screen.queryByRole("button")).toBeNull();
  });
});
