import { fireEvent, render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";

import { UserAvatar } from "./UserAvatar";
import { avatarSeed } from "./avatarSeed";

const identity = {
  workspaceId: "workspace-1",
  userId: "user-1",
  displayName: "Caio Almeida",
} as const;

describe("avatarSeed", () => {
  it("scopes a stable user identity to its workspace", () => {
    expect(avatarSeed("workspace-a", "user-x")).toBe("nchat:workspace-a:user-x");
    expect(avatarSeed("workspace-a", "user-x")).toBe("nchat:workspace-a:user-x");
    expect(avatarSeed("workspace-b", "user-x")).toBe("nchat:workspace-b:user-x");
  });
});

describe("UserAvatar", () => {
  it("fits the existing avatar slot without requiring a surface-specific image class", () => {
    const { container } = render(<UserAvatar {...identity} />);
    expect(container.querySelector("img")).toHaveClass("user-avatar__image");
  });
  it("renders a valid same-origin profile photo before the generated fallback", () => {
    const { container } = render(
      <UserAvatar {...identity} avatarUrl="/media/avatar.png" imageClassName="avatar-image" />,
    );

    const image = container.querySelector("img");
    expect(image).toHaveAttribute("src", "/media/avatar.png");
    expect(image).toHaveClass("avatar-image");
    expect(image).toHaveAttribute("referrerpolicy", "no-referrer");
  });

  it.each([undefined, null, "", "javascript:alert(1)", "https://evil.test/avatar.png"])(
    "renders a local Blobatar when avatarUrl is %s",
    (avatarUrl) => {
      const { container } = render(<UserAvatar {...identity} avatarUrl={avatarUrl} />);
      const image = container.querySelector("img");

      expect(image?.getAttribute("src")).toMatch(/^data:image\/svg\+xml/);
    },
  );

  it("switches to Blobatar after the profile photo fails without retrying the same URL", () => {
    const { container, rerender } = render(
      <UserAvatar {...identity} avatarUrl="/media/broken.png" />,
    );
    fireEvent.error(container.querySelector('img[src="/media/broken.png"]')!);

    expect(container.querySelector('img[src="/media/broken.png"]')).not.toBeInTheDocument();
    expect(container.querySelector("img")?.getAttribute("src")).toMatch(/^data:image\/svg\+xml/);

    rerender(<UserAvatar {...identity} avatarUrl="/media/broken.png" displayName="Novo Nome" />);
    expect(container.querySelector('img[src="/media/broken.png"]')).not.toBeInTheDocument();
  });

  it("keeps the same generated avatar when only displayName changes", () => {
    const { container, rerender } = render(<UserAvatar {...identity} />);
    const firstSource = container.querySelector("img")?.getAttribute("src");

    rerender(<UserAvatar {...identity} displayName="Outro Nome" />);

    expect(container.querySelector("img")?.getAttribute("src")).toBe(firstSource);
  });

  it("reproduces the same avatar after remount and distinguishes canonical identities", () => {
    const first = render(<UserAvatar {...identity} />);
    const source = first.container.querySelector("img")?.getAttribute("src");
    first.unmount();
    const next = render(<UserAvatar {...identity} />);
    expect(next.container.querySelector("img")?.getAttribute("src")).toBe(source);
    next.rerender(<UserAvatar {...identity} userId="user-2" />);
    expect(next.container.querySelector("img")?.getAttribute("src")).not.toBe(source);
    next.rerender(<UserAvatar {...identity} workspaceId="workspace-2" />);
    expect(next.container.querySelector("img")?.getAttribute("src")).not.toBe(source);
  });

  it("lets a new profile photo replace a fallback from an earlier failed URL", () => {
    const { container, rerender } = render(
      <UserAvatar {...identity} avatarUrl="/media/broken.png" />,
    );
    fireEvent.error(container.querySelector("img")!);

    rerender(<UserAvatar {...identity} avatarUrl="/media/new.png" />);

    expect(container.querySelector("img")).toHaveAttribute("src", "/media/new.png");
  });

  it("keeps the neutral initials state until canonical IDs are available", () => {
    render(<UserAvatar userId="" workspaceId="" displayName="Caio Almeida" />);

    expect(screen.getByText("CA")).toBeInTheDocument();
    expect(screen.queryByRole("img")).not.toBeInTheDocument();
  });

  it("does not invent a name or avatar while both identity and display name are loading", () => {
    const { container } = render(<UserAvatar userId="" workspaceId="" displayName="" />);
    expect(container.textContent).toBe("");
    expect(container.querySelector("img")).toBeNull();
  });

  it("preserves presence and accessible naming", () => {
    const { container } = render(
      <UserAvatar
        {...identity}
        presence="online"
        size="lg"
        presenceRingColor="rgb(1, 2, 3)"
        alt="Avatar de Caio Almeida"
      />,
    );

    expect(screen.getByRole("img", { name: "Avatar de Caio Almeida" })).toBeInTheDocument();
    expect(screen.getByTestId("presence-dot")).toHaveAttribute("data-presence", "online");
    expect(screen.getByTestId("presence-dot")).toHaveClass("presence-dot--lg");
    expect(screen.getByTestId("presence-dot")).toHaveStyle({ borderColor: "rgb(1, 2, 3)" });
    expect(container.querySelector("svg")).not.toBeInTheDocument();
  });
});
