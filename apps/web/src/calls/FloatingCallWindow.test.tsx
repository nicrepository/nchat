import { fireEvent, render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";

import FloatingCallWindow from "./FloatingCallWindow";

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
  title: "Caio Almeida",
  status: "connected" as const,
  participantCount: 2,
  controls: baseControls,
  onExpand: vi.fn(),
  hasRemoteVideo: false,
  remoteSeed: "peer-1",
  hasLocalVideo: false,
  localSeed: "user-1",
  localName: "Ana Souza (você)",
  localInitials: "AS",
};

describe("FloatingCallWindow", () => {
  it("shows the local participant's real name with (você), never a bare Você replacing it", () => {
    const { container } = render(<FloatingCallWindow {...baseProps} />);
    expect(container.querySelector(".floating-call__local-avatar")).toHaveAttribute(
      "aria-label",
      "Ana Souza (você)",
    );
  });

  it("falls back to a bare Você when there is no local name yet", () => {
    const { container } = render(
      <FloatingCallWindow {...baseProps} localName="Você" localInitials="V" />,
    );
    expect(container.querySelector(".floating-call__local-avatar")).toHaveAttribute(
      "aria-label",
      "Você",
    );
    expect(container.querySelector(".floating-call__local-avatar")).toHaveTextContent("V");
  });

  it("derives initials from the raw one-word name, never 'A(' from the (você) suffix (issue #612 blocker)", () => {
    const { container } = render(
      <FloatingCallWindow {...baseProps} localName="Ana (você)" localInitials="A" />,
    );
    const avatar = container.querySelector(".floating-call__local-avatar")!;
    expect(avatar.textContent).toBe("A");
  });

  it("renders the local avatar image when localAvatarUrl is set", () => {
    const { container } = render(
      <FloatingCallWindow {...baseProps} localAvatarUrl="https://x/local.png" />,
    );
    expect(container.querySelector(".floating-call__local-avatar img")).toHaveAttribute(
      "src",
      "https://x/local.png",
    );
  });

  it("falls back to deterministic initials when the local avatar fails to load", () => {
    const { container } = render(
      <FloatingCallWindow {...baseProps} localAvatarUrl="https://x/broken.png" />,
    );
    fireEvent.error(container.querySelector(".floating-call__local-avatar img")!);
    expect(container.querySelector(".floating-call__local-avatar img")).not.toBeInTheDocument();
    expect(container.querySelector(".floating-call__local-avatar")).toHaveTextContent("AS");
  });

  it("renders the remote avatar image for a direct call when avatarUrl is set", () => {
    const { container } = render(
      <FloatingCallWindow {...baseProps} avatarUrl="https://x/remote.png" />,
    );
    expect(container.querySelector(".floating-call__avatar img")).toHaveAttribute(
      "src",
      "https://x/remote.png",
    );
  });

  it("falls back to initials from the title when there is no remote avatar", () => {
    const { container } = render(<FloatingCallWindow {...baseProps} />);
    expect(container.querySelector(".floating-call__avatar")).toHaveTextContent("CA");
  });

  it("camera-on: renders no avatar fallback for either party", () => {
    const { container } = render(
      <FloatingCallWindow {...baseProps} hasLocalVideo hasRemoteVideo />,
    );
    expect(container.querySelector(".floating-call__avatar")).not.toBeInTheDocument();
    expect(container.querySelector(".floating-call__local-avatar")).not.toBeInTheDocument();
  });

  describe("active speaker presentation", () => {
    it("highlights the local preview with video or avatar fallback", () => {
      const view = render(
        <FloatingCallWindow
          {...baseProps}
          hasLocalVideo
          activeSpeaker={{ kind: "local", name: "Ana Souza (você)" }}
        />,
      );
      expect(document.querySelector(".floating-call__local")).toHaveClass(
        "call-speaker-surface--active",
      );

      view.rerender(
        <FloatingCallWindow
          {...baseProps}
          hasLocalVideo={false}
          localAvatarUrl="https://x/local.png"
          activeSpeaker={{ kind: "local", name: "Ana Souza (você)" }}
        />,
      );
      expect(document.querySelector(".floating-call__local")).toHaveClass(
        "call-speaker-surface--active",
      );
      expect(document.querySelector(".floating-call__local-avatar img")).toHaveAttribute(
        "src",
        "https://x/local.png",
      );
    });

    it("highlights the direct remote presentation with video or avatar fallback", () => {
      const view = render(
        <FloatingCallWindow
          {...baseProps}
          hasRemoteVideo
          activeSpeaker={{ kind: "direct-remote", name: "Caio Almeida" }}
        />,
      );
      expect(document.querySelector(".floating-call__remote-participant")).toHaveClass(
        "call-speaker-surface--active",
      );

      view.rerender(
        <FloatingCallWindow
          {...baseProps}
          hasRemoteVideo={false}
          avatarUrl="https://x/remote.png"
          activeSpeaker={{ kind: "direct-remote", name: "Caio Almeida" }}
        />,
      );
      expect(document.querySelector(".floating-call__remote-participant")).toHaveClass(
        "call-speaker-surface--active",
      );
      expect(document.querySelector(".floating-call__avatar img")).toHaveAttribute(
        "src",
        "https://x/remote.png",
      );
    });

    it("uses only the compact cue for a resource participant not individually presented", () => {
      render(
        <FloatingCallWindow
          {...baseProps}
          title="Equipe Infra"
          activeSpeaker={{ kind: "resource-remote", name: "Bruno Lima" }}
        />,
      );

      expect(document.querySelector(".floating-call__remote-participant")).not.toHaveClass(
        "call-speaker-surface--active",
      );
      expect(screen.getByLabelText("Bruno Lima está falando")).toHaveTextContent("Bruno Lima");
    });

    it("moves and clears one highlight without duplicating active speakers", () => {
      const view = render(
        <FloatingCallWindow
          {...baseProps}
          activeSpeaker={{ kind: "local", name: "Ana Souza (você)" }}
        />,
      );
      expect(document.querySelectorAll(".call-speaker-surface--active")).toHaveLength(1);

      view.rerender(
        <FloatingCallWindow
          {...baseProps}
          activeSpeaker={{ kind: "direct-remote", name: "Caio Almeida" }}
        />,
      );
      expect(document.querySelector(".floating-call__local")).not.toHaveClass(
        "call-speaker-surface--active",
      );
      expect(document.querySelector(".floating-call__remote-participant")).toHaveClass(
        "call-speaker-surface--active",
      );
      expect(document.querySelectorAll(".call-speaker-surface--active")).toHaveLength(1);

      view.rerender(<FloatingCallWindow {...baseProps} activeSpeaker={undefined} />);
      expect(document.querySelectorAll(".call-speaker-surface--active")).toHaveLength(0);
      expect(document.querySelector(".floating-call__speaker")).not.toBeInTheDocument();
    });

    it("communicates speaking with a visible microphone icon and an accessible full-name label", () => {
      render(
        <FloatingCallWindow
          {...baseProps}
          activeSpeaker={{ kind: "direct-remote", name: "Caio Almeida" }}
        />,
      );

      const cue = screen.getByLabelText("Caio Almeida está falando");
      expect(cue).toHaveClass("floating-call__speaker");
      expect(cue).toHaveTextContent("Caio Almeida está falando");
      expect(cue.querySelector(".material-symbols-outlined")).toHaveTextContent("mic");
      expect(cue.querySelector(".material-symbols-outlined")).toHaveAttribute(
        "aria-hidden",
        "true",
      );
    });
  });
});
