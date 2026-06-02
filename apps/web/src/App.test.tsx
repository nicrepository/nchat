import { render, screen } from "@testing-library/react";
import { beforeEach, describe, expect, it } from "vitest";

import App from "./App";
import { clearTokens } from "./lib/authSession";

beforeEach(() => {
  clearTokens();
  window.history.pushState({}, "", "/");
});

describe("App", () => {
  it("renders the login page when user is not authenticated", async () => {
    render(<App />);
    expect(await screen.findByRole("heading", { name: /entrar no nic chat/i })).toBeInTheDocument();
  });
});
