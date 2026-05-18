import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";

import App from "./App";

describe("App", () => {
  it("renders the NChat title", () => {
    render(<App />);

    expect(screen.getByRole("heading", { name: "NChat" })).toBeInTheDocument();
  });

  it("renders the bootstrap status", () => {
    render(<App />);

    expect(screen.getByText("MVP bootstrap ready")).toBeInTheDocument();
  });
});
