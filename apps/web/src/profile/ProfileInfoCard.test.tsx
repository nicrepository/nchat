import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";

import ProfileInfoCard from "./ProfileInfoCard";
import type { SelfProfile } from "./profileApi";

const profile: SelfProfile = {
  id: "u1",
  displayName: "Ana",
  jobTitle: "Engenheira",
  timezone: "America/Sao_Paulo",
  bio: "",
  customStatus: "",
};

describe("ProfileInfoCard", () => {
  it("renders job title and timezone as a two-column definition list", () => {
    render(<ProfileInfoCard profile={profile} />);
    expect(screen.getByText("Cargo")).toBeInTheDocument();
    expect(screen.getByText("Engenheira")).toBeInTheDocument();
    expect(screen.getByText("Fuso horário")).toBeInTheDocument();
    expect(screen.getByText("America/Sao_Paulo")).toBeInTheDocument();
  });

  it("omits a row entirely when its value is unset, rather than showing a placeholder", () => {
    render(<ProfileInfoCard profile={{ ...profile, timezone: "" }} />);
    expect(screen.queryByText("Fuso horário")).not.toBeInTheDocument();
  });
});
