import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";

import HomePage from "./HomePage";

describe("HomePage", () => {
  it("renders authenticated app shell placeholder", () => {
    render(<HomePage />);
    expect(screen.getByRole("heading", { name: /NChat/i })).toBeInTheDocument();
    expect(screen.getByText(/autenticado/i)).toBeInTheDocument();
  });
});
