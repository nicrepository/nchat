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

  // Regression tests for issue #693: short WebM voice messages (MediaRecorder
  // output) can report `HTMLMediaElement.duration === Infinity` from
  // `loadedmetadata`, and `ended` never syncs the visible progress to the end.
  describe("issue #693: WebM voice message duration/progress", () => {
    it("does not let an Infinity native duration overwrite a finite durationHint", () => {
      render(
        <AudioPlayer
          label="Mensagem de voz"
          src="blob:voice"
          loading={false}
          failed={false}
          onRequestLoad={() => undefined}
          durationHint={4}
          testIdPrefix="p-voice"
        />,
      );

      expect(screen.getByTestId("p-voice-time")).toHaveTextContent("0:00 / 0:04");
      expect(screen.getByTestId("p-voice-seek")).toHaveAttribute("max", "4");

      const audioEl = screen.getByTestId("p-voice-audio-el") as HTMLAudioElement;
      Object.defineProperty(audioEl, "duration", { value: Infinity, configurable: true });
      fireEvent.loadedMetadata(audioEl);

      expect(screen.getByTestId("p-voice-time")).toHaveTextContent("0:00 / 0:04");
      expect(screen.getByTestId("p-voice-seek")).toHaveAttribute("max", "4");
    });

    it("syncs the visible progress to the end when playback ends", () => {
      render(
        <AudioPlayer
          label="Mensagem de voz"
          src="blob:voice"
          loading={false}
          failed={false}
          onRequestLoad={() => undefined}
          durationHint={4}
          testIdPrefix="p-voice2"
        />,
      );

      const audioEl = screen.getByTestId("p-voice2-audio-el") as HTMLAudioElement;
      Object.defineProperty(audioEl, "duration", { value: 4, configurable: true });
      fireEvent.loadedMetadata(audioEl);

      Object.defineProperty(audioEl, "currentTime", { value: 3.8, configurable: true });
      fireEvent.timeUpdate(audioEl);
      fireEvent.ended(audioEl);

      expect(screen.getByTestId("p-voice2-seek")).toHaveValue("4");
      expect(screen.getByTestId("p-voice2-time")).toHaveTextContent("0:04 / 0:04");
    });

    it("lets a finite native duration replace the hint", () => {
      render(
        <AudioPlayer
          label="Mensagem de voz"
          src="blob:voice"
          loading={false}
          failed={false}
          onRequestLoad={() => undefined}
          durationHint={4}
          testIdPrefix="p-voice3"
        />,
      );

      const audioEl = screen.getByTestId("p-voice3-audio-el") as HTMLAudioElement;
      Object.defineProperty(audioEl, "duration", { value: 4.2, configurable: true });
      fireEvent.loadedMetadata(audioEl);

      expect(screen.getByTestId("p-voice3-seek")).toHaveAttribute("max", "4.2");
      expect(screen.getByTestId("p-voice3-time")).toHaveTextContent("0:00 / 0:04");
    });

    it("ignores invalid native duration values and keeps the last valid duration", () => {
      render(
        <AudioPlayer
          label="Mensagem de voz"
          src="blob:voice"
          loading={false}
          failed={false}
          onRequestLoad={() => undefined}
          durationHint={4}
          testIdPrefix="p-voice4"
        />,
      );

      const audioEl = screen.getByTestId("p-voice4-audio-el") as HTMLAudioElement;
      for (const invalid of [Infinity, NaN, 0, -1]) {
        Object.defineProperty(audioEl, "duration", { value: invalid, configurable: true });
        fireEvent.loadedMetadata(audioEl);
        fireEvent.durationChange(audioEl);
      }

      expect(screen.getByTestId("p-voice4-seek")).toHaveAttribute("max", "4");
      expect(screen.getByTestId("p-voice4-time")).toHaveTextContent("0:00 / 0:04");
    });

    it("picks up a duration that only becomes finite on a later durationchange", () => {
      render(
        <AudioPlayer
          label="Mensagem de voz"
          src="blob:voice"
          loading={false}
          failed={false}
          onRequestLoad={() => undefined}
          durationHint={4}
          testIdPrefix="p-voice5"
        />,
      );

      const audioEl = screen.getByTestId("p-voice5-audio-el") as HTMLAudioElement;
      Object.defineProperty(audioEl, "duration", { value: Infinity, configurable: true });
      fireEvent.loadedMetadata(audioEl);
      expect(screen.getByTestId("p-voice5-seek")).toHaveAttribute("max", "4");

      Object.defineProperty(audioEl, "duration", { value: 5.5, configurable: true });
      fireEvent.durationChange(audioEl);

      expect(screen.getByTestId("p-voice5-seek")).toHaveAttribute("max", "5.5");
      expect(screen.getByTestId("p-voice5-time")).toHaveTextContent("0:00 / 0:05");
    });

    it("moves the seek bar proportionally on an intermediate timeupdate", () => {
      render(
        <AudioPlayer
          label="Mensagem de voz"
          src="blob:voice"
          loading={false}
          failed={false}
          onRequestLoad={() => undefined}
          durationHint={4}
          testIdPrefix="p-voice6"
        />,
      );

      const audioEl = screen.getByTestId("p-voice6-audio-el") as HTMLAudioElement;
      Object.defineProperty(audioEl, "duration", { value: 4, configurable: true });
      fireEvent.loadedMetadata(audioEl);

      Object.defineProperty(audioEl, "currentTime", { value: 2, configurable: true });
      fireEvent.timeUpdate(audioEl);

      expect(screen.getByTestId("p-voice6-seek")).toHaveValue("2");
      expect(screen.getByTestId("p-voice6-time")).toHaveTextContent("0:02 / 0:04");
    });
  });
});
