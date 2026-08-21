import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";

import Pagination from "./Pagination";

describe("Pagination", () => {
  it("is a named landmark that states the position as text", () => {
    render(
      <Pagination
        count={25}
        hasMore
        canGoBack={false}
        busy={false}
        onNext={vi.fn()}
        onPrevious={vi.fn()}
      />,
    );
    expect(screen.getByRole("navigation", { name: "Paginação" })).toBeInTheDocument();
    // Not implied by which button is greyed out.
    expect(screen.getByRole("status")).toHaveTextContent("25 registros nesta página");
    expect(screen.getByRole("status")).toHaveTextContent("há mais páginas");
  });

  it("says when it is the last page", () => {
    render(
      <Pagination
        count={1}
        hasMore={false}
        canGoBack
        busy={false}
        onNext={vi.fn()}
        onPrevious={vi.fn()}
      />,
    );
    expect(screen.getByRole("status")).toHaveTextContent("1 registro nesta página");
    expect(screen.getByRole("status")).toHaveTextContent("última página");
    expect(screen.getByRole("button", { name: "Próxima página" })).toBeDisabled();
  });

  it("moves in both directions", async () => {
    const onNext = vi.fn();
    const onPrevious = vi.fn();
    render(
      <Pagination
        count={5}
        hasMore
        canGoBack
        busy={false}
        onNext={onNext}
        onPrevious={onPrevious}
      />,
    );
    await userEvent.click(screen.getByRole("button", { name: "Próxima página" }));
    await userEvent.click(screen.getByRole("button", { name: "Página anterior" }));
    expect(onNext).toHaveBeenCalledTimes(1);
    expect(onPrevious).toHaveBeenCalledTimes(1);
  });

  // Paging while a mutation is applying would refetch a page the mutation is
  // about to change.
  it("locks while a mutation is running", () => {
    render(<Pagination count={5} hasMore canGoBack busy onNext={vi.fn()} onPrevious={vi.fn()} />);
    expect(screen.getByRole("button", { name: "Próxima página" })).toBeDisabled();
    expect(screen.getByRole("button", { name: "Página anterior" })).toBeDisabled();
  });
});
