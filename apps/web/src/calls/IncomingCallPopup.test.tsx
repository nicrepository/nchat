import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";

import IncomingCallPopup from "./IncomingCallPopup";

const baseProps = {
  name: "Caio Almeida",
  callType: "video" as const,
  onAccept: vi.fn(),
  onReject: vi.fn(),
};

describe("IncomingCallPopup — identity", () => {
  it("renders the caller's real name", () => {
    render(<IncomingCallPopup {...baseProps} />);
    expect(screen.getByText("Caio Almeida")).toBeInTheDocument();
  });

  it("shows two-letter initials, not a single raw character, when there is no avatar", () => {
    const { container } = render(<IncomingCallPopup {...baseProps} />);
    expect(container.querySelector(".incoming-call__avatar")).toHaveTextContent("CA");
  });

  it("renders the personalized avatar image when avatarUrl is set", () => {
    const { container } = render(<IncomingCallPopup {...baseProps} avatarUrl="https://x/a.png" />);
    expect(container.querySelector("img")).toHaveAttribute("src", "https://x/a.png");
  });

  it("falls back to initials, not a broken-image glyph, when the avatar fails to load", () => {
    const { container } = render(
      <IncomingCallPopup {...baseProps} avatarUrl="https://x/broken.png" />,
    );
    fireEvent.error(container.querySelector("img")!);
    expect(container.querySelector("img")).not.toBeInTheDocument();
    expect(container.querySelector(".incoming-call__avatar")).toHaveTextContent("CA");
  });

  it("marks the avatar as decorative — the adjacent name already identifies the person", () => {
    const { container } = render(<IncomingCallPopup {...baseProps} />);
    expect(container.querySelector(".incoming-call__avatar")).toHaveAttribute(
      "aria-hidden",
      "true",
    );
  });
});

describe("IncomingCallPopup — call type", () => {
  it("labels an audio call distinctly, with an Atender action", () => {
    render(<IncomingCallPopup {...baseProps} callType="audio" />);
    expect(screen.getByText("Chamada de voz")).toBeInTheDocument();
    expect(screen.queryByText("Chamada de vídeo")).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: /Atender/ })).toHaveTextContent("Atender");
  });

  it("labels a video call distinctly, with an Atender com câmera action", () => {
    render(<IncomingCallPopup {...baseProps} callType="video" />);
    expect(screen.getByText("Chamada de vídeo")).toBeInTheDocument();
    expect(screen.queryByText("Chamada de voz")).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: /Atender/ })).toHaveTextContent("Atender com câmera");
  });

  it("never renders the legacy generic-target prefix", () => {
    render(<IncomingCallPopup {...baseProps} />);
    expect(screen.queryByText(/^DM ·/)).not.toBeInTheDocument();
    expect(screen.queryByText(/^Grupo ·/)).not.toBeInTheDocument();
    expect(screen.queryByText(/^Canal ·/)).not.toBeInTheDocument();
  });
});

describe("IncomingCallPopup — actions", () => {
  it("calls onReject exactly once per click", () => {
    const onReject = vi.fn();
    render(<IncomingCallPopup {...baseProps} onReject={onReject} />);
    fireEvent.click(screen.getByRole("button", { name: /Recusar/ }));
    expect(onReject).toHaveBeenCalledOnce();
  });

  it("calls onAccept exactly once per click", () => {
    const onAccept = vi.fn();
    render(<IncomingCallPopup {...baseProps} onAccept={onAccept} />);
    fireEvent.click(screen.getByRole("button", { name: /Atender/ }));
    expect(onAccept).toHaveBeenCalledOnce();
  });

  it("exposes a full, unambiguous accessible name for reject", () => {
    render(<IncomingCallPopup {...baseProps} />);
    expect(
      screen.getByRole("button", { name: "Recusar chamada de Caio Almeida" }),
    ).toBeInTheDocument();
  });

  it("exposes a full, unambiguous accessible name for accept — audio", () => {
    render(<IncomingCallPopup {...baseProps} callType="audio" />);
    expect(
      screen.getByRole("button", { name: "Atender chamada de Caio Almeida" }),
    ).toBeInTheDocument();
  });

  it("exposes a full, unambiguous accessible name for accept — video", () => {
    render(<IncomingCallPopup {...baseProps} callType="video" />);
    expect(
      screen.getByRole("button", { name: "Atender com câmera a chamada de vídeo de Caio Almeida" }),
    ).toBeInTheDocument();
  });

  it("keeps the video accept accessible name starting with its exact visual label (WCAG Label in Name)", () => {
    render(<IncomingCallPopup {...baseProps} callType="video" />);
    const button = screen.getByRole("button", { name: /^Atender com câmera/ });
    expect(button).toHaveTextContent("Atender com câmera");
    expect(button.getAttribute("aria-label")).toMatch(/^Atender com câmera/);
    expect(button.getAttribute("aria-label")).toContain("Caio Almeida");
  });

  it("uses native, non-disabled buttons in the ready state", () => {
    render(<IncomingCallPopup {...baseProps} />);
    for (const button of screen.getAllByRole("button")) {
      expect(button).toHaveAttribute("type", "button");
      expect(button).not.toBeDisabled();
    }
  });
});

describe("IncomingCallPopup — non-modal / focus", () => {
  it("never steals focus on mount (non-modal, no autofocus)", () => {
    const external = document.createElement("input");
    document.body.appendChild(external);
    external.focus();
    render(<IncomingCallPopup {...baseProps} />);
    expect(document.activeElement).toBe(external);
    document.body.removeChild(external);
  });

  it("presents the card as a non-modal dialog", () => {
    render(<IncomingCallPopup {...baseProps} />);
    const popup = screen.getByRole("dialog", { name: "Chamada recebida" });
    expect(popup).toHaveAttribute("aria-modal", "false");
  });
});

describe("IncomingCallPopup — identity loading", () => {
  it("shows an accessible preparing status and hides accept/reject", () => {
    render(<IncomingCallPopup {...baseProps} identityStatus="loading" />);
    expect(screen.getByRole("status")).toHaveTextContent("Preparando");
    expect(screen.queryByRole("button", { name: /Recusar/ })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /Atender/ })).not.toBeInTheDocument();
  });
});

describe("IncomingCallPopup — identity error / retry", () => {
  it("shows an accessible error message and a retry button, hiding accept/reject", () => {
    render(<IncomingCallPopup {...baseProps} identityStatus="error" onRetryIdentity={vi.fn()} />);
    expect(screen.getByRole("alert")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Tentar novamente" })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /Recusar/ })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /^Atender/ })).not.toBeInTheDocument();
  });

  it("calls onRetryIdentity when retry is clicked", () => {
    const retry = vi.fn(() => Promise.resolve());
    render(<IncomingCallPopup {...baseProps} identityStatus="error" onRetryIdentity={retry} />);
    fireEvent.click(screen.getByRole("button", { name: "Tentar novamente" }));
    expect(retry).toHaveBeenCalledOnce();
  });

  it("fires only one retry operation for a double click while the promise is pending", async () => {
    const retry = vi.fn(() => Promise.resolve());
    render(<IncomingCallPopup {...baseProps} identityStatus="error" onRetryIdentity={retry} />);
    const button = screen.getByRole("button", { name: "Tentar novamente" });
    fireEvent.click(button);
    fireEvent.click(button);
    expect(retry).toHaveBeenCalledOnce();
    await Promise.resolve();
  });

  it("disables retry while the retry promise is pending", () => {
    const retry = vi.fn(() => Promise.resolve());
    render(<IncomingCallPopup {...baseProps} identityStatus="error" onRetryIdentity={retry} />);
    const button = screen.getByRole("button", { name: "Tentar novamente" });
    fireEvent.click(button);
    expect(button).toBeDisabled();
  });

  it("releases the retry state on resolve, allowing another attempt", async () => {
    const retry = vi.fn(() => Promise.resolve());
    render(<IncomingCallPopup {...baseProps} identityStatus="error" onRetryIdentity={retry} />);
    const button = screen.getByRole("button", { name: "Tentar novamente" });
    fireEvent.click(button);
    await waitFor(() => expect(button).not.toBeDisabled());
    fireEvent.click(button);
    expect(retry).toHaveBeenCalledTimes(2);
  });

  it("releases the retry state on rejection too, without an unhandled rejection", async () => {
    const retry = vi.fn(() => Promise.reject(new Error("boom")));
    render(<IncomingCallPopup {...baseProps} identityStatus="error" onRetryIdentity={retry} />);
    const button = screen.getByRole("button", { name: "Tentar novamente" });
    fireEvent.click(button);
    await waitFor(() => expect(button).not.toBeDisabled());
  });

  it("safely ignores a retry click when no retry handler is provided", () => {
    render(<IncomingCallPopup {...baseProps} identityStatus="error" />);
    expect(() =>
      fireEvent.click(screen.getByRole("button", { name: "Tentar novamente" })),
    ).not.toThrow();
  });
});

describe("IncomingCallPopup — semantic structure", () => {
  it("gives the popup an accessible name distinct from the caller name", () => {
    render(<IncomingCallPopup {...baseProps} />);
    expect(screen.getByRole("dialog", { name: "Chamada recebida" })).toBeInTheDocument();
  });

  it("keeps caller name and call type available in the accessible tree", () => {
    render(<IncomingCallPopup {...baseProps} callType="audio" />);
    expect(screen.getByText("Caio Almeida")).toBeVisible();
    expect(screen.getByText("Chamada de voz")).toBeVisible();
  });
});
