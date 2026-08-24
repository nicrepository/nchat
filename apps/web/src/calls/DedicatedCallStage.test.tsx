import { fireEvent, render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";

import DedicatedCallStage from "./DedicatedCallStage";

const baseControls = {
  microphoneEnabled: true,
  cameraEnabled: true,
  screenShareEnabled: false,
  pendingControl: null,
  onMicrophone: vi.fn(),
  onCamera: vi.fn(),
  onScreenShare: vi.fn(),
  onEnd: vi.fn(),
};

const baseProps = {
  title: "Equipe Infra",
  status: "connected" as const,
  participantCount: 3,
  participants: [
    { identity: "user-a", displayName: "Ana Souza", hasVideo: false, avatarUrl: "https://x/a.png" },
    { identity: "user-b", displayName: "Bruno Lima", hasVideo: false },
  ],
  controls: baseControls,
  onMinimize: vi.fn(),
  hasLocalVideo: false,
  localSeed: "user-1",
  localDisplayName: "Caio Almeida (você)",
};

describe("DedicatedCallStage", () => {
  it("renders the local participant's real name with (você)", () => {
    render(<DedicatedCallStage {...baseProps} />);
    expect(screen.getByText("Caio Almeida (você)")).toBeInTheDocument();
  });

  it("renders each participant's own resolved name, not a shared resource name", () => {
    render(<DedicatedCallStage {...baseProps} />);
    expect(screen.getByText("Ana Souza")).toBeInTheDocument();
    expect(screen.getByText("Bruno Lima")).toBeInTheDocument();
  });

  it("renders a participant's own avatar image when avatarUrl is set", () => {
    const { container } = render(<DedicatedCallStage {...baseProps} />);
    const tiles = container.querySelectorAll(".dedicated-call__tile");
    expect(tiles[1]!.querySelector("img")).toHaveAttribute("src", "https://x/a.png");
  });

  it("falls back to that participant's own deterministic initials when they have no avatar", () => {
    const { container } = render(<DedicatedCallStage {...baseProps} />);
    const tiles = container.querySelectorAll(".dedicated-call__tile");
    expect(tiles[2]!.querySelector("img")).not.toBeInTheDocument();
    expect(tiles[2]).toHaveTextContent("BL");
  });

  it("renders the local avatar image when localAvatarUrl is set", () => {
    const { container } = render(
      <DedicatedCallStage {...baseProps} localAvatarUrl="https://x/local.png" />,
    );
    const tiles = container.querySelectorAll(".dedicated-call__tile");
    expect(tiles[0]!.querySelector("img")).toHaveAttribute("src", "https://x/local.png");
  });

  it("falls back to deterministic initials, no broken image, when a participant avatar fails to load", () => {
    const { container } = render(<DedicatedCallStage {...baseProps} />);
    const tiles = container.querySelectorAll(".dedicated-call__tile");
    fireEvent.error(tiles[1]!.querySelector("img")!);
    expect(tiles[1]!.querySelector("img")).not.toBeInTheDocument();
    expect(tiles[1]).toHaveTextContent("AS");
  });

  it("camera-on: renders no avatar fallback for that tile", () => {
    const { container } = render(
      <DedicatedCallStage
        {...baseProps}
        hasLocalVideo
        participants={[
          {
            identity: "user-a",
            displayName: "Ana Souza",
            hasVideo: true,
            avatarUrl: "https://x/a.png",
          },
        ]}
      />,
    );
    const tiles = container.querySelectorAll(".dedicated-call__tile");
    expect(tiles[0]!.querySelector(".dedicated-call__avatar")).not.toBeInTheDocument();
    expect(tiles[1]!.querySelector(".dedicated-call__avatar")).not.toBeInTheDocument();
  });

  describe("dedicated direct header identity (issue #612 blocker fix)", () => {
    it("shows the direct peer's real name and avatar in the header", () => {
      const { container } = render(
        <DedicatedCallStage
          {...baseProps}
          title="Ana Souza"
          headerAvatar={{ seed: "peer-1", avatarUrl: "https://x/peer.png" }}
        />,
      );
      expect(container.querySelector(".dedicated-call__header strong")).toHaveTextContent(
        "Ana Souza",
      );
      const headerAvatar = container.querySelector(".dedicated-call__header-avatar")!;
      expect(headerAvatar.querySelector("img")).toHaveAttribute("src", "https://x/peer.png");
    });

    it("falls back to deterministic initials in the header when the peer avatar is missing or broken", () => {
      const { container, rerender } = render(
        <DedicatedCallStage {...baseProps} title="Ana Souza" headerAvatar={{ seed: "peer-1" }} />,
      );
      let headerAvatar = container.querySelector(".dedicated-call__header-avatar")!;
      expect(headerAvatar.querySelector("img")).not.toBeInTheDocument();
      expect(headerAvatar).toHaveTextContent("AS");

      rerender(
        <DedicatedCallStage
          {...baseProps}
          title="Ana Souza"
          headerAvatar={{ seed: "peer-1", avatarUrl: "https://x/broken.png" }}
        />,
      );
      headerAvatar = container.querySelector(".dedicated-call__header-avatar")!;
      fireEvent.error(headerAvatar.querySelector("img")!);
      expect(headerAvatar.querySelector("img")).not.toBeInTheDocument();
      expect(headerAvatar).toHaveTextContent("AS");
    });

    it("is decorative (aria-hidden) since the visible title already names the peer", () => {
      const { container } = render(
        <DedicatedCallStage
          {...baseProps}
          title="Ana Souza"
          headerAvatar={{ seed: "peer-1", avatarUrl: "https://x/peer.png" }}
        />,
      );
      expect(container.querySelector(".dedicated-call__header-avatar")).toHaveAttribute(
        "aria-hidden",
        "true",
      );
    });

    it("never shows a header avatar for a channel/group resource call", () => {
      const { container } = render(
        <DedicatedCallStage {...baseProps} title="Equipe Infra" headerAvatar={undefined} />,
      );
      expect(container.querySelector(".dedicated-call__header-avatar")).not.toBeInTheDocument();
    });
  });
});
