import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";

import type { HealthState } from "../api/observabilityApi";
import { HEALTH_STATE_ORDER, presentState } from "../lib/healthStatus";
import HealthStateBadge from "./HealthStateBadge";

describe("HealthStateBadge", () => {
  it("names every state in words", () => {
    for (const state of HEALTH_STATE_ORDER) {
      const { unmount } = render(<HealthStateBadge state={state} />);
      expect(screen.getByText(presentState(state).label)).toBeInTheDocument();
      unmount();
    }
  });

  it("carries a shape as well as a colour, and hides it from screen readers", () => {
    // The shape is what distinguishes the states on a monochrome screen; the
    // word is what a screen reader announces, so announcing the shape too
    // would be noise.
    const { container } = render(<HealthStateBadge state="unavailable" />);
    const mark = container.querySelector(".admin-health-badge__mark");
    expect(mark).not.toBeNull();
    expect(mark).toHaveAttribute("aria-hidden", "true");
    expect(mark?.textContent).toBe(presentState("unavailable").mark);
  });

  it("renders an unrecognised state as unknown rather than as blank", () => {
    render(<HealthStateBadge state={"invented" as HealthState} />);
    expect(screen.getByText("Desconhecido")).toBeInTheDocument();
  });
});
