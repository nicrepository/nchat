import { render, screen } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router";
import { describe, expect, it } from "vitest";

import ProfileSettingsShell from "./ProfileSettingsShell";

describe("ProfileSettingsShell", () => {
  it("renders the tabs and the matched child route", () => {
    render(
      <MemoryRouter initialEntries={["/profile/notifications"]}>
        <Routes>
          <Route path="/profile" element={<ProfileSettingsShell />}>
            <Route path="notifications" element={<div>Notifications content</div>} />
          </Route>
        </Routes>
      </MemoryRouter>,
    );
    expect(screen.getByRole("navigation", { name: "Seções da conta" })).toBeInTheDocument();
    expect(screen.getByText("Notifications content")).toBeInTheDocument();
  });
});
