import { fireEvent, render, screen } from "@testing-library/react";
import { expect, it, vi } from "vitest";

import { HeaderDM } from "./ChatMessageArea";

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
