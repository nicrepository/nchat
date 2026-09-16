import { render, screen } from "@testing-library/react";
import { MemoryRouter, Outlet, Route, Routes, useOutletContext } from "react-router";
import { describe, expect, it } from "vitest";

import ProfileSettingsShell from "./ProfileSettingsShell";

function ContextProbe() {
  const context = useOutletContext<{ marker?: string } | null>();
  return <div>marker: {context?.marker ?? "none"}</div>;
}

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

  it("forwards the shell context it received to its own children (issue #729)", () => {
    render(
      <MemoryRouter initialEntries={["/profile/notifications"]}>
        <Routes>
          <Route element={<Outlet context={{ marker: "from-app-shell" }} />}>
            <Route path="/profile" element={<ProfileSettingsShell />}>
              <Route path="notifications" element={<ContextProbe />} />
            </Route>
          </Route>
        </Routes>
      </MemoryRouter>,
    );

    expect(screen.getByText("marker: from-app-shell")).toBeInTheDocument();
  });

  it("renders its children unharmed when there is no parent context to forward", () => {
    render(
      <MemoryRouter initialEntries={["/profile/security"]}>
        <Routes>
          <Route path="/profile" element={<ProfileSettingsShell />}>
            <Route path="security" element={<ContextProbe />} />
          </Route>
        </Routes>
      </MemoryRouter>,
    );

    expect(screen.getByText("marker: none")).toBeInTheDocument();
  });
});
