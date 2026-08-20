import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";

import EnvironmentBadge from "./EnvironmentBadge";

describe("EnvironmentBadge", () => {
  it.each(["DEVELOPMENT", "STAGING", "PRODUCTION"] as const)(
    "renders %s as text",
    (environment) => {
      render(<EnvironmentBadge environment={environment} />);

      const badge = screen.getByTestId("admin-environment");
      expect(badge).toHaveTextContent(environment);
      expect(badge).toHaveAttribute("data-environment", environment);
      // The state is never carried by colour alone: a class exists, and so does
      // the word.
      expect(badge.className).toContain(`admin-env--${environment.toLowerCase()}`);
    },
  );

  it("announces what the value means", () => {
    render(<EnvironmentBadge environment="PRODUCTION" />);
    expect(screen.getByText("Ambiente:")).toBeInTheDocument();
  });
});

// A label this build does not know is shown verbatim rather than blank: an
// operator must never see an empty environment indicator.
it("falls back to the raw value for an unknown environment", () => {
  render(<EnvironmentBadge environment={"QA" as never} />);
  expect(screen.getByTestId("admin-environment")).toHaveTextContent("QA");
});
