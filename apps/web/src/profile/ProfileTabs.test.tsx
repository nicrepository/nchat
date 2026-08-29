import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
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
    expect(screen.getByRole("tab", { name: "Perfil" })).toHaveClass("profile-tabs__link--active");
    expect(screen.getByRole("tab", { name: "Perfil" })).toHaveAttribute("aria-selected", "true");
    expect(screen.getByRole("tab", { name: "Sessões" })).not.toHaveClass(
      "profile-tabs__link--active",
    );
  });

  it("marks Sessões active on /profile/sessions and not Perfil", () => {
    renderAt("/profile/sessions");
    expect(screen.getByRole("tab", { name: "Sessões" })).toHaveClass("profile-tabs__link--active");
    expect(screen.getByRole("tab", { name: "Perfil" })).not.toHaveClass(
      "profile-tabs__link--active",
    );
  });

  it("renders all four sections as links", () => {
    renderAt("/profile");
    for (const label of ["Perfil", "Notificações", "Segurança", "Sessões"]) {
      expect(screen.getByRole("tab", { name: label })).toHaveAttribute(
        "href",
        label === "Perfil"
          ? "/profile"
          : `/profile/${label === "Notificações" ? "notifications" : label === "Segurança" ? "security" : "sessions"}`,
      );
    }
  });

  it("uses roving focus and arrow keys to select the next URL-backed tab", async () => {
    const user = userEvent.setup();
    renderAt("/profile");
    const profileTab = screen.getByRole("tab", { name: "Perfil" });
    profileTab.focus();

    await user.keyboard("{ArrowRight}");

    expect(screen.getByRole("tab", { name: "Notificações" })).toHaveFocus();
    expect(screen.getByRole("tab", { name: "Notificações" })).toHaveAttribute(
      "aria-selected",
      "true",
    );
  });

  it("supports ArrowLeft, Home and End without local selection state", async () => {
    const user = userEvent.setup();
    renderAt("/profile/sessions");
    const sessionsTab = screen.getByRole("tab", { name: "Sessões" });
    sessionsTab.focus();

    await user.keyboard("{ArrowLeft}");
    expect(screen.getByRole("tab", { name: "Segurança" })).toHaveFocus();
    await user.keyboard("{Home}");
    expect(screen.getByRole("tab", { name: "Perfil" })).toHaveFocus();
    await user.keyboard("{End}");
    expect(screen.getByRole("tab", { name: "Sessões" })).toHaveFocus();
  });
});
