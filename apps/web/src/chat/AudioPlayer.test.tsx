/**
 * AudioPlayer tests (issue #670): the parts that are the same whether the
 * source is a fetched attachment or a recorder's local preview blob —
 * play/pause, seek, rate cycling with persistence, and the one-player-per-tab
 * rule. AttachmentAudio.test.tsx covers the fetch-on-demand wiring around it.
 */

import { fireEvent, render, screen } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import AudioPlayer from "./AudioPlayer";

beforeEach(() => {
  vi.spyOn(HTMLMediaElement.prototype, "play").mockResolvedValue(undefined);
  vi.spyOn(HTMLMediaElement.prototype, "pause").mockImplementation(() => {});
  window.localStorage.clear();
});

afterEach(() => {
  vi.restoreAllMocks();
});

function player(id: string, src: string | null = "blob:one") {
  return (
    <AudioPlayer
      label={`Áudio ${id}`}
      src={src}
      loading={false}
      failed={false}
      onRequestLoad={() => undefined}
      testIdPrefix={`p-${id}`}
    />
  );
}

describe("AudioPlayer", () => {
  it("asks the caller to load bytes on the first press when there is no src yet", () => {
    const onRequestLoad = vi.fn();
    render(
      <AudioPlayer
        label="Áudio"
        src={null}
        loading={false}
        failed={false}
        onRequestLoad={onRequestLoad}
        testIdPrefix="p"
      />,
    );
    fireEvent.click(screen.getByTestId("p-playpause"));
    expect(onRequestLoad).toHaveBeenCalledTimes(1);
  });

  it("cycles playback rate 1x -> 1.5x -> 2x -> 1x and persists the choice", () => {
    render(player("a"));
    const rateBtn = screen.getByTestId("p-a-rate");
    expect(rateBtn).toHaveTextContent("1x");

    fireEvent.click(rateBtn);
    expect(rateBtn).toHaveTextContent("1.5x");
    fireEvent.click(rateBtn);
    expect(rateBtn).toHaveTextContent("2x");
    fireEvent.click(rateBtn);
    expect(rateBtn).toHaveTextContent("1x");

    expect(window.localStorage.getItem("nchat.audioPlaybackRate")).toBe("1");
  });

  it("starts a new player at the previously chosen rate", () => {
    window.localStorage.setItem("nchat.audioPlaybackRate", "2");
    render(player("b"));
    expect(screen.getByTestId("p-b-rate")).toHaveTextContent("2x");
  });

  it("falls back to 1x for a corrupt stored rate", () => {
    window.localStorage.setItem("nchat.audioPlaybackRate", "not-a-number");
    render(player("c"));
    expect(screen.getByTestId("p-c-rate")).toHaveTextContent("1x");
  });

  it("pauses the other player when a second one starts (one active player per tab)", () => {
    render(
      <>
        {player("x")}
        {player("y")}
      </>,
    );
    const audioX = screen.getByTestId("p-x-audio-el") as HTMLAudioElement;
    const audioY = screen.getByTestId("p-y-audio-el") as HTMLAudioElement;
    const pauseX = vi.spyOn(audioX, "pause");

    fireEvent.play(audioX);
    fireEvent.play(audioY);

    expect(pauseX).toHaveBeenCalledTimes(1);
  });
});
