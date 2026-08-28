import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";

import ProfileOverviewPage from "./ProfileOverviewPage";
import * as selfProfile from "./selfProfile";

vi.mock("./selfProfile");
vi.mock("../chat/presence", () => ({ usePresence: () => "online", presenceLabel: () => "Online" }));

describe("ProfileOverviewPage", () => {
  it("shows a loading state, then the identity card once ready", () => {
    vi.mocked(selfProfile.useSelfProfile).mockReturnValue({ status: "loading" });
    const { rerender } = render(<ProfileOverviewPage />);
    expect(screen.getByRole("status")).toBeInTheDocument();

    vi.mocked(selfProfile.useSelfProfile).mockReturnValue({
      status: "ready",
      profile: {
        id: "u1",
        displayName: "Ana",
        jobTitle: "",
        bio: "",
        timezone: "",
        customStatus: "",
      },
    });
    rerender(<ProfileOverviewPage />);
    expect(screen.getByRole("heading", { name: "Ana" })).toBeInTheDocument();
  });

  it("shows a retry-capable error state independent of the sidebar/other sections", () => {
    vi.mocked(selfProfile.useSelfProfile).mockReturnValue({ status: "error" });
    render(<ProfileOverviewPage />);
    expect(screen.getByRole("button", { name: /tentar novamente/i })).toBeInTheDocument();
  });

  it("opens ProfileEditDialog from Editar and AvatarDialog from Trocar foto", async () => {
    vi.mocked(selfProfile.useSelfProfile).mockReturnValue({
      status: "ready",
      profile: {
        id: "u1",
        displayName: "Ana",
        jobTitle: "",
        bio: "",
        timezone: "",
        customStatus: "",
      },
    });
    const user = userEvent.setup();
    render(<ProfileOverviewPage />);
    await user.click(screen.getByRole("button", { name: "Editar" }));
    expect(screen.getByRole("dialog", { name: "Editar perfil" })).toBeInTheDocument();
    await user.keyboard("{Escape}");
    await user.click(screen.getByRole("button", { name: "Trocar foto" }));
    expect(screen.getByRole("dialog", { name: "Trocar foto" })).toBeInTheDocument();
  });
});
