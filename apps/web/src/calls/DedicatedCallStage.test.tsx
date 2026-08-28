import { fireEvent, render, screen, within } from "@testing-library/react";
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
    {
      identity: "user-a",
      displayName: "Ana Souza",
      hasVideo: false,
      hasAudio: true,
      avatarUrl: "https://x/a.png",
    },
    { identity: "user-b", displayName: "Bruno Lima", hasVideo: false, hasAudio: false },
  ],
  controls: baseControls,
  onMinimize: vi.fn(),
  hasLocalVideo: false,
  localSeed: "user-1",
  localParticipantId: "user-1",
  localDisplayName: "Caio Almeida (você)",
  localInitials: "CA",
  activeSpeakerId: null,
};

const directPeer = {
  identity: "peer-1",
  seed: "peer-1",
  displayName: "Davi Rocha",
  avatarUrl: "https://x/peer.png",
  hasVideo: false,
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

  it("uses the passed-in localInitials verbatim, never derived from the (você)-suffixed localDisplayName (issue #612 blocker)", () => {
    const { container } = render(
      <DedicatedCallStage {...baseProps} localDisplayName="Ana (você)" localInitials="A" />,
    );
    const tiles = container.querySelectorAll(".dedicated-call__tile");
    const localAvatar = tiles[0]!.querySelector(".dedicated-call__avatar")!;
    expect(localAvatar.textContent).toBe("A");
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
            hasAudio: true,
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

  describe("dedicated direct remote tile (issue #612 follow-up)", () => {
    it("shows the direct peer's real name and avatar in the remote tile when camera is off", () => {
      const { container } = render(
        <DedicatedCallStage
          {...baseProps}
          remoteDirect={{
            identity: "peer-1",
            seed: "peer-1",
            displayName: "Ana Souza",
            avatarUrl: "https://x/peer.png",
            hasVideo: false,
          }}
        />,
      );
      const tiles = container.querySelectorAll(".dedicated-call__tile");
      // Local tile is first, remote-direct tile is second.
      expect(tiles[1]).toHaveTextContent("Ana Souza");
      expect(tiles[1]!.querySelector("img")).toHaveAttribute("src", "https://x/peer.png");
    });

    it("falls back to deterministic initials in the remote tile when the peer avatar is missing or broken", () => {
      const { container, rerender } = render(
        <DedicatedCallStage
          {...baseProps}
          remoteDirect={{
            identity: "peer-1",
            seed: "peer-1",
            displayName: "Ana Souza",
            hasVideo: false,
          }}
        />,
      );
      let tile = container.querySelectorAll(".dedicated-call__tile")[1]!;
      expect(tile.querySelector("img")).not.toBeInTheDocument();
      expect(tile).toHaveTextContent("AS");

      rerender(
        <DedicatedCallStage
          {...baseProps}
          remoteDirect={{
            identity: "peer-1",
            seed: "peer-1",
            displayName: "Ana Souza",
            avatarUrl: "https://x/broken.png",
            hasVideo: false,
          }}
        />,
      );
      tile = container.querySelectorAll(".dedicated-call__tile")[1]!;
      fireEvent.error(tile.querySelector("img")!);
      expect(tile.querySelector("img")).not.toBeInTheDocument();
      expect(tile).toHaveTextContent("AS");
    });

    it("is decorative (aria-hidden) since the visible name is adjacent in the same tile", () => {
      const { container } = render(
        <DedicatedCallStage
          {...baseProps}
          remoteDirect={{
            identity: "peer-1",
            seed: "peer-1",
            displayName: "Ana Souza",
            avatarUrl: "https://x/peer.png",
            hasVideo: false,
          }}
        />,
      );
      const tile = container.querySelectorAll(".dedicated-call__tile")[1]!;
      expect(tile.querySelector(".dedicated-call__avatar")).toHaveAttribute("aria-hidden", "true");
    });

    it("camera-on: renders no avatar fallback in the remote tile, only the video container", () => {
      const bindVideo = vi.fn();
      const { container } = render(
        <DedicatedCallStage
          {...baseProps}
          remoteDirect={{
            identity: "peer-1",
            seed: "peer-1",
            displayName: "Ana Souza",
            avatarUrl: "https://x/peer.png",
            hasVideo: true,
            bindVideo,
          }}
        />,
      );
      const tile = container.querySelectorAll(".dedicated-call__tile")[1]!;
      expect(tile.querySelector(".dedicated-call__avatar")).not.toBeInTheDocument();
      expect(tile.querySelector(".dedicated-call__media")).toBeInTheDocument();
    });

    it("never renders a remote-direct tile for a channel/group resource call", () => {
      const { container } = render(
        <DedicatedCallStage {...baseProps} title="Equipe Infra" remoteDirect={undefined} />,
      );
      // Only the local tile plus the two baseProps.participants tiles — no
      // extra remote-direct tile ever appears when it wasn't wired in.
      expect(container.querySelectorAll(".dedicated-call__tile")).toHaveLength(3);
    });
  });

  describe("active speaker presentation", () => {
    it("highlights a resource video tile by canonical participant identity", () => {
      const { container } = render(
        <DedicatedCallStage
          {...baseProps}
          activeSpeakerId="user-a"
          participants={[
            { identity: "user-a", displayName: "Ana Souza", hasVideo: true, hasAudio: true },
          ]}
        />,
      );

      const tile = screen.getByText("Ana Souza").closest("article")!;
      expect(tile).toHaveClass("call-speaker-surface--active");
      expect(tile.querySelector(".dedicated-call__avatar")).not.toBeInTheDocument();
      expect(container.querySelectorAll(".call-speaker-surface--active")).toHaveLength(1);
    });

    it("keeps camera-off initials and profile-avatar fallbacks highlighted", () => {
      const view = render(<DedicatedCallStage {...baseProps} activeSpeakerId="user-b" />);
      let tile = screen.getByText("Bruno Lima").closest("article")!;
      expect(tile).toHaveClass("call-speaker-surface--active");
      expect(tile).toHaveTextContent("BL");

      view.rerender(<DedicatedCallStage {...baseProps} activeSpeakerId="user-a" />);
      tile = screen.getByText("Ana Souza").closest("article")!;
      expect(tile).toHaveClass("call-speaker-surface--active");
      expect(tile.querySelector("img")).toHaveAttribute("src", "https://x/a.png");
    });

    it("highlights the local participant by current-user identity", () => {
      const { container } = render(<DedicatedCallStage {...baseProps} activeSpeakerId="user-1" />);

      expect(screen.getByText("Caio Almeida (você)").closest("article")).toHaveClass(
        "call-speaker-surface--active",
      );
      expect(container.querySelectorAll(".call-speaker-surface--active")).toHaveLength(1);
    });

    it("highlights the direct remote participant by explicit identity", () => {
      const { container } = render(
        <DedicatedCallStage
          {...baseProps}
          activeSpeakerId="peer-1"
          participants={[]}
          remoteDirect={{
            identity: "peer-1",
            seed: "peer-1",
            displayName: "Davi Rocha",
            hasVideo: false,
          }}
        />,
      );

      expect(screen.getByText("Davi Rocha").closest("article")).toHaveClass(
        "call-speaker-surface--active",
      );
      expect(container.querySelectorAll(".call-speaker-surface--active")).toHaveLength(1);
    });

    it("moves and clears the highlight without leaving duplicate or stale tiles", () => {
      const view = render(<DedicatedCallStage {...baseProps} activeSpeakerId="user-a" />);
      expect(screen.getByText("Ana Souza").closest("article")).toHaveClass(
        "call-speaker-surface--active",
      );

      view.rerender(<DedicatedCallStage {...baseProps} activeSpeakerId="user-b" />);
      expect(screen.getByText("Ana Souza").closest("article")).not.toHaveClass(
        "call-speaker-surface--active",
      );
      expect(screen.getByText("Bruno Lima").closest("article")).toHaveClass(
        "call-speaker-surface--active",
      );
      expect(document.querySelectorAll(".call-speaker-surface--active")).toHaveLength(1);

      view.rerender(<DedicatedCallStage {...baseProps} activeSpeakerId={null} />);
      expect(document.querySelectorAll(".call-speaker-surface--active")).toHaveLength(0);

      view.rerender(
        <DedicatedCallStage
          {...baseProps}
          activeSpeakerId="user-a"
          participants={baseProps.participants.filter(({ identity }) => identity !== "user-a")}
        />,
      );
      expect(document.querySelectorAll(".call-speaker-surface--active")).toHaveLength(0);
    });

    it("never marks a screen-share tile and exposes a microphone cue with the speaker's full name", () => {
      const { container } = render(
        <DedicatedCallStage
          {...baseProps}
          activeSpeakerId="user-a"
          bindScreenShare={vi.fn()}
          screenShareName="Ana Souza"
        />,
      );

      expect(container.querySelector(".dedicated-call__tile--screen")).not.toHaveClass(
        "call-speaker-surface--active",
      );
      const cue = screen.getByLabelText("Ana Souza está falando");
      expect(cue).toHaveClass("call-speaker-indicator");
      expect(cue).toHaveAttribute("role", "img");
      expect(cue).toHaveTextContent("mic");
      expect(cue.querySelector(".material-symbols-outlined")).toHaveAttribute(
        "aria-hidden",
        "true",
      );
    });
  });

  describe("resource screen-share participant sidebar (issue #643)", () => {
    it("keeps the normal resource layout when no screen share is active", () => {
      const { container } = render(<DedicatedCallStage {...baseProps} resourceCall />);

      expect(container.querySelector(".dedicated-call__grid")).toBeInTheDocument();
      expect(
        container.querySelector(".dedicated-call__screen-share-layout"),
      ).not.toBeInTheDocument();
      expect(
        screen.queryByRole("complementary", { name: "Participantes" }),
      ).not.toBeInTheDocument();
    });

    it.each([
      { localScreenShareActive: true, label: "Sua tela" },
      { bindScreenShare: vi.fn(), screenShareName: "Ana Souza", label: "Tela de Ana Souza" },
    ])("uses the side-by-side resource layout for $label", (shareProps) => {
      const { container } = render(
        <DedicatedCallStage {...baseProps} resourceCall {...shareProps} />,
      );

      expect(container.querySelector(".dedicated-call")).toHaveClass(
        "dedicated-call--screen-share",
      );
      expect(container.querySelector(".dedicated-call__screen-share-layout")).toBeInTheDocument();
      expect(screen.getByText(shareProps.label)).toBeInTheDocument();
      expect(screen.getByRole("complementary", { name: "Participantes" })).toHaveAttribute(
        "tabindex",
        "0",
      );
      expect(screen.getByLabelText("Controles da chamada")).toBeInTheDocument();
    });

    it("keeps local share precedence over a simultaneous remote share", () => {
      render(
        <DedicatedCallStage
          {...baseProps}
          resourceCall
          localScreenShareActive
          bindLocalScreenShare={vi.fn()}
          bindScreenShare={vi.fn()}
          screenShareName="Ana Souza"
        />,
      );

      expect(screen.getByText("Sua tela")).toBeInTheDocument();
      expect(screen.queryByText("Tela de Ana Souza")).not.toBeInTheDocument();
      expect(document.querySelectorAll(".dedicated-call__tile--screen")).toHaveLength(1);
    });

    it("shows the local and remote roster with accessible media states", () => {
      render(
        <DedicatedCallStage
          {...baseProps}
          resourceCall
          bindScreenShare={vi.fn()}
          remoteScreenShareParticipantId="user-a"
          activeSpeakerId="user-a"
        />,
      );

      const sidebar = screen.getByRole("complementary", { name: "Participantes" });
      expect(sidebar).toHaveTextContent(baseProps.localDisplayName);
      expect(sidebar).toHaveTextContent("Ana Souza");
      expect(sidebar).toHaveTextContent("Bruno Lima");
      expect(sidebar.querySelectorAll(".dedicated-call__participant")).toHaveLength(3);
      expect(sidebar.querySelector(".call-speaker-surface--active")).toHaveTextContent("Ana Souza");
      expect(within(sidebar).getByLabelText("Ana Souza est\u00e1 falando")).toBeInTheDocument();
      expect(within(sidebar).getByLabelText("Ana Souza: microfone ligado")).toBeInTheDocument();
      expect(
        within(sidebar).getByLabelText("Ana Souza: c\u00e2mera desligada"),
      ).toBeInTheDocument();
      expect(
        within(sidebar).getByLabelText("Ana Souza est\u00e1 compartilhando a tela"),
      ).toBeInTheDocument();
      expect(within(sidebar).getByLabelText("Bruno Lima: microfone desligado")).toBeInTheDocument();
    });

    it("updates and clears the derived sidebar without stale participants or speakers", () => {
      const view = render(
        <DedicatedCallStage
          {...baseProps}
          resourceCall
          bindScreenShare={vi.fn()}
          activeSpeakerId="user-a"
        />,
      );
      expect(screen.getByRole("complementary", { name: "Participantes" })).toHaveTextContent(
        "Ana Souza",
      );

      view.rerender(
        <DedicatedCallStage
          {...baseProps}
          resourceCall
          bindScreenShare={vi.fn()}
          participants={[
            { identity: "user-c", displayName: "Carla Dias", hasVideo: true, hasAudio: true },
          ]}
          activeSpeakerId="user-c"
        />,
      );
      const sidebar = screen.getByRole("complementary", { name: "Participantes" });
      expect(sidebar).not.toHaveTextContent("Ana Souza");
      expect(sidebar).toHaveTextContent("Carla Dias");
      expect(sidebar.querySelectorAll(".call-speaker-surface--active")).toHaveLength(1);

      view.rerender(
        <DedicatedCallStage
          {...baseProps}
          resourceCall
          bindScreenShare={vi.fn()}
          participants={[
            { identity: "user-c", displayName: "Carla Dias", hasVideo: true, hasAudio: true },
          ]}
          activeSpeakerId={null}
        />,
      );
      expect(sidebar.querySelectorAll(".call-speaker-surface--active")).toHaveLength(0);
    });

    it("removes the special layout immediately when sharing ends", () => {
      const view = render(
        <DedicatedCallStage {...baseProps} resourceCall bindScreenShare={vi.fn()} />,
      );

      view.rerender(<DedicatedCallStage {...baseProps} resourceCall />);
      expect(
        screen.queryByRole("complementary", { name: "Participantes" }),
      ).not.toBeInTheDocument();
      expect(
        document.querySelector(".dedicated-call__screen-share-layout"),
      ).not.toBeInTheDocument();
      expect(document.querySelector(".dedicated-call__grid")).toBeInTheDocument();
    });

    it("continues using the resource roster, not remoteDirect, for resource screen share", () => {
      const { container } = render(
        <DedicatedCallStage
          {...baseProps}
          resourceCall
          bindScreenShare={vi.fn()}
          remoteDirect={directPeer}
        />,
      );

      const sidebar = screen.getByRole("complementary", { name: "Participantes" });
      expect(sidebar).toHaveTextContent(baseProps.localDisplayName);
      expect(sidebar).toHaveTextContent("Ana Souza");
      expect(sidebar).toHaveTextContent("Bruno Lima");
      expect(sidebar).not.toHaveTextContent("Davi Rocha");
      expect(container.querySelectorAll(".dedicated-call__participant")).toHaveLength(3);
    });
  });

  describe("direct screen-share participant sidebar (issue #661)", () => {
    it("keeps the normal direct layout when no screen share is active", () => {
      const { container } = render(
        <DedicatedCallStage {...baseProps} participants={[]} remoteDirect={directPeer} />,
      );

      expect(container.querySelector(".dedicated-call__grid")).toBeInTheDocument();
      expect(
        container.querySelector(".dedicated-call__screen-share-layout"),
      ).not.toBeInTheDocument();
      expect(
        screen.queryByRole("complementary", { name: "Participantes" }),
      ).not.toBeInTheDocument();
      expect(screen.getByText("Davi Rocha")).toBeInTheDocument();
      expect(screen.getByText("Davi Rocha").closest(".dedicated-call__grid")).toBeInTheDocument();
    });

    it("uses the shared screen-share layout for a direct local share", () => {
      const { container } = render(
        <DedicatedCallStage
          {...baseProps}
          participants={[]}
          remoteDirect={directPeer}
          localScreenShareActive
          bindLocalScreenShare={vi.fn()}
        />,
      );

      expect(container.querySelector(".dedicated-call")).toHaveClass(
        "dedicated-call--screen-share",
      );
      expect(container.querySelector(".dedicated-call__screen-stage")).toHaveTextContent(
        "Sua tela",
      );
      const sidebar = screen.getByRole("complementary", { name: "Participantes" });
      expect(sidebar.querySelectorAll(".dedicated-call__participant")).toHaveLength(2);
      expect(within(sidebar).getByText(baseProps.localDisplayName)).toBeInTheDocument();
      expect(within(sidebar).getByText("Davi Rocha")).toBeInTheDocument();
      expect(screen.getAllByText("Davi Rocha")).toHaveLength(1);
      expect(screen.getByLabelText("Controles da chamada")).toBeInTheDocument();
    });

    it("uses the same direct sidebar layout for a remote share without duplicating the peer", () => {
      const { container } = render(
        <DedicatedCallStage
          {...baseProps}
          participants={[]}
          remoteDirect={directPeer}
          bindScreenShare={vi.fn()}
          screenShareName="Davi Rocha"
          remoteScreenShareParticipantId="peer-1"
        />,
      );

      expect(container.querySelector(".dedicated-call")).toHaveClass(
        "dedicated-call--screen-share",
      );
      expect(container.querySelector(".dedicated-call__screen-stage")).toHaveTextContent(
        "Tela de Davi Rocha",
      );
      const sidebar = screen.getByRole("complementary", { name: "Participantes" });
      expect(sidebar.querySelectorAll(".dedicated-call__participant")).toHaveLength(2);
      expect(within(sidebar).getByText("Davi Rocha")).toBeInTheDocument();
      expect(screen.getAllByText("Davi Rocha")).toHaveLength(1);
    });

    it("keeps local share precedence over a transient direct remote share", () => {
      render(
        <DedicatedCallStage
          {...baseProps}
          participants={[]}
          remoteDirect={directPeer}
          localScreenShareActive
          bindLocalScreenShare={vi.fn()}
          bindScreenShare={vi.fn()}
          screenShareName="Davi Rocha"
        />,
      );

      expect(screen.getByText("Sua tela")).toBeInTheDocument();
      expect(screen.queryByText("Tela de Davi Rocha")).not.toBeInTheDocument();
      expect(document.querySelectorAll(".dedicated-call__tile--screen")).toHaveLength(1);
    });

    it("restores the normal direct grid immediately when local sharing stops", () => {
      const view = render(
        <DedicatedCallStage
          {...baseProps}
          participants={[]}
          remoteDirect={directPeer}
          localScreenShareActive
          bindLocalScreenShare={vi.fn()}
        />,
      );

      view.rerender(
        <DedicatedCallStage {...baseProps} participants={[]} remoteDirect={directPeer} />,
      );

      expect(
        screen.queryByRole("complementary", { name: "Participantes" }),
      ).not.toBeInTheDocument();
      expect(
        document.querySelector(".dedicated-call__screen-share-layout"),
      ).not.toBeInTheDocument();
      expect(document.querySelector(".dedicated-call__grid")).toBeInTheDocument();
      expect(screen.getByText("Davi Rocha").closest(".dedicated-call__grid")).toBeInTheDocument();
    });

    it("restores the normal direct grid immediately when remote sharing is removed", () => {
      const view = render(
        <DedicatedCallStage
          {...baseProps}
          participants={[]}
          remoteDirect={directPeer}
          bindScreenShare={vi.fn()}
          screenShareName="Davi Rocha"
        />,
      );

      view.rerender(
        <DedicatedCallStage {...baseProps} participants={[]} remoteDirect={directPeer} />,
      );

      expect(
        screen.queryByRole("complementary", { name: "Participantes" }),
      ).not.toBeInTheDocument();
      expect(document.querySelector(".dedicated-call__tile--screen")).not.toBeInTheDocument();
      expect(document.querySelector(".dedicated-call__grid")).toBeInTheDocument();
      expect(screen.getAllByText("Davi Rocha")).toHaveLength(1);
    });

    it("preserves the direct peer video binding when the camera is on", () => {
      const bindVideo = vi.fn();
      render(
        <DedicatedCallStage
          {...baseProps}
          participants={[]}
          remoteDirect={{ ...directPeer, hasVideo: true, bindVideo }}
          bindScreenShare={vi.fn()}
          screenShareName="Davi Rocha"
        />,
      );

      const sidebar = screen.getByRole("complementary", { name: "Participantes" });
      const peerTile = within(sidebar).getByText("Davi Rocha").closest("article")!;
      expect(peerTile.querySelector(".dedicated-call__avatar")).not.toBeInTheDocument();
      expect(bindVideo).toHaveBeenCalledWith(expect.any(HTMLDivElement));
    });

    it("keeps the direct peer avatar fallback when the camera is off", () => {
      render(
        <DedicatedCallStage
          {...baseProps}
          participants={[]}
          remoteDirect={{ ...directPeer, avatarUrl: undefined }}
          bindScreenShare={vi.fn()}
          screenShareName="Davi Rocha"
        />,
      );

      const sidebar = screen.getByRole("complementary", { name: "Participantes" });
      const peerTile = within(sidebar).getByText("Davi Rocha").closest("article")!;
      expect(peerTile).toHaveTextContent("DR");
      expect(peerTile.querySelector(".dedicated-call__avatar")).toHaveAttribute(
        "aria-hidden",
        "true",
      );
    });

    it("keeps active-speaker presentation for local and direct remote sidebar tiles", () => {
      const view = render(
        <DedicatedCallStage
          {...baseProps}
          participants={[]}
          remoteDirect={directPeer}
          bindScreenShare={vi.fn()}
          activeSpeakerId="user-1"
        />,
      );

      expect(screen.getByText(baseProps.localDisplayName).closest("article")).toHaveClass(
        "call-speaker-surface--active",
      );

      view.rerender(
        <DedicatedCallStage
          {...baseProps}
          participants={[]}
          remoteDirect={directPeer}
          bindScreenShare={vi.fn()}
          activeSpeakerId="peer-1"
        />,
      );
      expect(screen.getByText("Davi Rocha").closest("article")).toHaveClass(
        "call-speaker-surface--active",
      );
      expect(document.querySelectorAll(".call-speaker-surface--active")).toHaveLength(1);
    });

    it("exposes authoritative direct camera/share status and omits unknown microphone status", () => {
      render(
        <DedicatedCallStage
          {...baseProps}
          participants={[]}
          remoteDirect={directPeer}
          bindScreenShare={vi.fn()}
          screenShareName="Davi Rocha"
          remoteScreenShareParticipantId="peer-1"
        />,
      );

      const sidebar = screen.getByRole("complementary", { name: "Participantes" });
      expect(
        within(sidebar).getByLabelText("Davi Rocha: c\u00e2mera desligada"),
      ).toBeInTheDocument();
      expect(
        within(sidebar).getByLabelText("Davi Rocha est\u00e1 compartilhando a tela"),
      ).toBeInTheDocument();
      expect(
        within(sidebar).queryByLabelText("Davi Rocha: microfone ligado"),
      ).not.toBeInTheDocument();
      expect(
        within(sidebar).queryByLabelText("Davi Rocha: microfone desligado"),
      ).not.toBeInTheDocument();
    });

    it("does not enable the direct screen-share layout without an authoritative remote peer", () => {
      const { container } = render(
        <DedicatedCallStage {...baseProps} participants={[]} bindScreenShare={vi.fn()} />,
      );

      expect(container.querySelector(".dedicated-call__grid")).toBeInTheDocument();
      expect(
        screen.queryByRole("complementary", { name: "Participantes" }),
      ).not.toBeInTheDocument();
    });
  });
});
