import { fireEvent, render, screen } from "@testing-library/react";
import { expect, it, vi } from "vitest";

import { HeaderChannel, HeaderDM } from "./ChatMessageArea";

it("starts audio or video only for the server-resolved DM counterpart", () => {
  const start = vi.fn(() => true);
  render(
    <HeaderDM
      name="Ana"
      counterpart={{
        userId: "00000000-0000-4000-8000-000000000401",
        displayName: "Ana",
      }}
      onStartCall={start}
    />,
  );
  fireEvent.click(screen.getByRole("button", { name: "Iniciar chamada de áudio" }));
  fireEvent.click(screen.getByRole("button", { name: "Iniciar chamada de vídeo" }));
  expect(start).toHaveBeenNthCalledWith(1, "00000000-0000-4000-8000-000000000401", "audio");
  expect(start).toHaveBeenNthCalledWith(2, "00000000-0000-4000-8000-000000000401", "video");
});

it("does not offer call buttons for a 1:1 DM when onStartCall is unavailable (RF-24 direct call already in progress)", () => {
  render(
    <HeaderDM
      name="Ana"
      counterpart={{ userId: "00000000-0000-4000-8000-000000000401", displayName: "Ana" }}
    />,
  );
  expect(screen.queryByRole("button", { name: /Iniciar chamada/ })).not.toBeInTheDocument();
});

it("RF-24: joins the group's resource room, never the RF-23 1:1 call actions", () => {
  const joinGroupCall = vi.fn();
  render(<HeaderDM name="Equipe" onJoinGroupCall={joinGroupCall} />);

  fireEvent.click(screen.getByRole("button", { name: "Iniciar chamada de áudio" }));
  fireEvent.click(screen.getByRole("button", { name: "Iniciar chamada de vídeo" }));

  expect(joinGroupCall).toHaveBeenNthCalledWith(1, "audio");
  expect(joinGroupCall).toHaveBeenNthCalledWith(2, "video");
});

it("RF-24: a group with a resolved counterpart never happens, but onJoinGroupCall is ignored when counterpart is set", () => {
  const start = vi.fn(() => true);
  const joinGroupCall = vi.fn();
  render(
    <HeaderDM
      name="Ana"
      counterpart={{ userId: "00000000-0000-4000-8000-000000000401", displayName: "Ana" }}
      onStartCall={start}
      onJoinGroupCall={joinGroupCall}
    />,
  );

  fireEvent.click(screen.getByRole("button", { name: "Iniciar chamada de áudio" }));

  expect(start).toHaveBeenCalledOnce();
  expect(joinGroupCall).not.toHaveBeenCalled();
});

it("RF-24: joins the channel's resource room from the channel header", () => {
  const joinCall = vi.fn();
  render(<HeaderChannel name="geral" onJoinCall={joinCall} />);

  fireEvent.click(screen.getByRole("button", { name: "Iniciar chamada de áudio" }));
  fireEvent.click(screen.getByRole("button", { name: "Iniciar chamada de vídeo" }));

  expect(joinCall).toHaveBeenNthCalledWith(1, "audio");
  expect(joinCall).toHaveBeenNthCalledWith(2, "video");
});

it("hides the channel call buttons while a call is already in progress (onJoinCall undefined)", () => {
  render(<HeaderChannel name="geral" />);
  expect(screen.queryByRole("button", { name: /Iniciar chamada/ })).not.toBeInTheDocument();
});
