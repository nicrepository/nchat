import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";

import ProfileIdentityCard from "./ProfileIdentityCard";
import type { SelfProfile } from "./profileApi";

vi.mock("../chat/presence", () => ({
  usePresence: () => "online",
  presenceLabel: (state: string) => (state === "online" ? "Online" : state),
}));

const profile: SelfProfile = {
  id: "u1",
  displayName: "Ana Costa",
  avatarUrl: "/api/files/avatar.png",
  jobTitle: "Infraestrutura & Segurança",
  bio: "Trabalho com plataforma.",
  timezone: "America/Sao_Paulo",
  customStatus: "🚀 Focada no deploy",
};

describe("ProfileIdentityCard", () => {
  it("shows name, cargo, presence, timezone, bio and custom status", () => {
    render(<ProfileIdentityCard profile={profile} onEdit={vi.fn()} onChangePhoto={vi.fn()} />);
    expect(screen.getByRole("heading", { name: "Ana Costa" })).toBeInTheDocument();
    expect(screen.getByText("Infraestrutura & Segurança")).toBeInTheDocument();
    expect(screen.getByText("Online")).toBeInTheDocument();
    expect(screen.getByText("America/Sao_Paulo")).toBeInTheDocument();
    expect(screen.getByText("Trabalho com plataforma.")).toBeInTheDocument();
    expect(screen.getByText("🚀 Focada no deploy")).toBeInTheDocument();
  });

  it("omits empty optional fields instead of rendering a placeholder", () => {
    render(
      <ProfileIdentityCard
        profile={{ ...profile, jobTitle: "", bio: "", timezone: "", customStatus: "" }}
        onEdit={vi.fn()}
        onChangePhoto={vi.fn()}
      />,
    );
    expect(screen.queryByText("Infraestrutura & Segurança")).not.toBeInTheDocument();
    expect(screen.queryByTestId("profile-identity-timezone")).not.toBeInTheDocument();
  });

  it("shows a 'Sem nome' placeholder and empty initials when displayName is empty", () => {
    render(
      <ProfileIdentityCard
        profile={{ ...profile, displayName: "" }}
        onEdit={vi.fn()}
        onChangePhoto={vi.fn()}
      />,
    );
    expect(screen.getByRole("heading", { name: "Sem nome" })).toBeInTheDocument();
  });

  it("Editar and Trocar foto call their handlers", async () => {
    const user = userEvent.setup();
    const onEdit = vi.fn();
    const onChangePhoto = vi.fn();
    render(<ProfileIdentityCard profile={profile} onEdit={onEdit} onChangePhoto={onChangePhoto} />);
    await user.click(screen.getByRole("button", { name: "Editar" }));
    await user.click(screen.getByRole("button", { name: "Trocar foto" }));
    expect(onEdit).toHaveBeenCalledTimes(1);
    expect(onChangePhoto).toHaveBeenCalledTimes(1);
  });
});
