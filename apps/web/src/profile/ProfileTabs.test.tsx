import { render, screen } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router";
import { describe, expect, it } from "vitest";

import ProfileTabs from "./ProfileTabs";

function renderAt(path: string) {
  return render(
    <MemoryRouter initialEntries={[path]}>
      <Routes>
        <Route path="/profile/*" element={<ProfileTabs />} />
      </Routes>
    </MemoryRouter>,
  );
}

describe("ProfileTabs", () => {
  it("marks Perfil active on the exact /profile route", () => {
    renderAt("/profile");
    expect(screen.getByRole("link", { name: "Perfil" })).toHaveClass("profile-tabs__link--active");
    expect(screen.getByRole("link", { name: "Sessões" })).not.toHaveClass("profile-tabs__link--active");
  });

  it("marks Sessões active on /profile/sessions and not Perfil", () => {
    renderAt("/profile/sessions");
    expect(screen.getByRole("link", { name: "Sessões" })).toHaveClass("profile-tabs__link--active");
    expect(screen.getByRole("link", { name: "Perfil" })).not.toHaveClass("profile-tabs__link--active");
  });

  it("renders all four sections as links", () => {
    renderAt("/profile");
    for (const label of ["Perfil", "Notificações", "Segurança", "Sessões"]) {
      expect(screen.getByRole("link", { name: label })).toHaveAttribute(
        "href",
        label === "Perfil" ? "/profile" : `/profile/${label === "Notificações" ? "notifications" : label === "Segurança" ? "security" : "sessions"}`,
      );
    }
  });
});
