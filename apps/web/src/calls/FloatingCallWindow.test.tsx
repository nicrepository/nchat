import { fireEvent, render } from "@testing-library/react";
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
    const { container } = render(<FloatingCallWindow {...baseProps} localName="Você" />);
    expect(container.querySelector(".floating-call__local-avatar")).toHaveAttribute(
      "aria-label",
      "Você",
    );
    expect(container.querySelector(".floating-call__local-avatar")).toHaveTextContent("V");
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
});
