/**
 * AudioPlayer tests (issue #670): the parts that are the same whether the
 * source is a fetched attachment or a recorder's local preview blob —
 * play/pause, seek, rate cycling with persistence, and the one-player-per-tab
 * rule. AttachmentAudio.test.tsx covers the fetch-on-demand wiring around it.
 */

import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import AudioPlayer, { type AudioDownloadAction } from "./AudioPlayer";

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

  // Issue #740: a voice message can be saved to disk from the player itself.
  // The requirement that shapes every test here is "and nothing else changes":
  // the download is an errand the player runs beside playback, never instead
  // of it.
  describe("issue #740: download control", () => {
    function downloadable(action: Partial<AudioDownloadAction> = {}, prefix = "p-dl") {
      const start = vi.fn<() => Promise<void>>().mockResolvedValue(undefined);
      render(
        <AudioPlayer
          label="Mensagem de voz"
          src="blob:voice"
          loading={false}
          failed={false}
          onRequestLoad={() => undefined}
          durationHint={42}
          download={{
            label: "Baixar mensagem de voz",
            title: "Baixar áudio",
            start,
            ...action,
          }}
          testIdPrefix={prefix}
        />,
      );
      return { start, button: screen.getByTestId(`${prefix}-download`) };
    }

    it("draws no download control when the caller offers none", () => {
      render(
        <AudioPlayer
          label="Áudio: nota.ogg"
          src="blob:one"
          loading={false}
          failed={false}
          onRequestLoad={() => undefined}
          testIdPrefix="p-plain"
        />,
      );
      expect(screen.queryByTestId("p-plain-download")).not.toBeInTheDocument();
    });

    it("places the download between the elapsed time and the speed", () => {
      downloadable();
      const controls = within(screen.getByTestId("p-dl-player")).getAllByTestId(/p-dl-/);
      expect(controls.map((node) => node.dataset.testid)).toEqual([
        "p-dl-audio-el",
        "p-dl-playpause",
        "p-dl-seek",
        "p-dl-time",
        "p-dl-download",
        "p-dl-rate",
      ]);
    });

    it("names the action for assistive technology and for a pointer", () => {
      const { button } = downloadable();
      expect(button).toHaveAccessibleName("Baixar mensagem de voz");
      expect(button).toHaveAttribute("title", "Baixar áudio");
      expect(button.tagName).toBe("BUTTON");
    });

    it("starts the download on Enter and on Space, and keeps focus on the button", async () => {
      const user = userEvent.setup();
      const { start, button } = downloadable();

      button.focus();
      await user.keyboard("{Enter}");
      await waitFor(() => expect(start).toHaveBeenCalledTimes(1));
      expect(button).toHaveFocus();

      await user.keyboard(" ");
      await waitFor(() => expect(start).toHaveBeenCalledTimes(2));
      expect(button).toHaveFocus();
    });

    it("downloads before the first Play without loading or playing anything", () => {
      const onRequestLoad = vi.fn();
      const start = vi.fn<() => Promise<void>>().mockResolvedValue(undefined);
      render(
        <AudioPlayer
          label="Mensagem de voz"
          src={null}
          loading={false}
          failed={false}
          onRequestLoad={onRequestLoad}
          durationHint={42}
          download={{ label: "Baixar mensagem de voz", title: "Baixar áudio", start }}
          testIdPrefix="p-pre"
        />,
      );

      fireEvent.click(screen.getByTestId("p-pre-download"));

      expect(start).toHaveBeenCalledTimes(1);
      expect(onRequestLoad).not.toHaveBeenCalled();
      expect(screen.queryByTestId("p-pre-audio-el")).not.toBeInTheDocument();
    });

    it("leaves playback, position and speed untouched while playing", () => {
      const { start, button } = downloadable({}, "p-live");
      const audioEl = screen.getByTestId("p-live-audio-el") as HTMLAudioElement;
      const pause = vi.spyOn(audioEl, "pause");
      const play = vi.spyOn(audioEl, "play");

      fireEvent.click(screen.getByTestId("p-live-rate"));
      Object.defineProperty(audioEl, "duration", { value: 42, configurable: true });
      fireEvent.loadedMetadata(audioEl);
      Object.defineProperty(audioEl, "currentTime", { value: 12, configurable: true });
      fireEvent.timeUpdate(audioEl);
      fireEvent.play(audioEl);

      fireEvent.click(button);

      expect(start).toHaveBeenCalledTimes(1);
      expect(pause).not.toHaveBeenCalled();
      expect(play).not.toHaveBeenCalled();
      expect(audioEl.currentTime).toBe(12);
      expect(audioEl.playbackRate).toBe(1.5);
      expect(screen.getByTestId("p-live-time")).toHaveTextContent("0:12 / 0:42");
      // Still playing: the button offers to pause, not to resume.
      expect(screen.getByTestId("p-live-playpause")).toHaveAccessibleName("Pausar Mensagem de voz");
    });

    it("still downloads once playback has ended, without rewinding it", () => {
      const { start, button } = downloadable({}, "p-ended");
      const audioEl = screen.getByTestId("p-ended-audio-el") as HTMLAudioElement;
      Object.defineProperty(audioEl, "duration", { value: 42, configurable: true });
      fireEvent.loadedMetadata(audioEl);
      fireEvent.ended(audioEl);

      fireEvent.click(button);

      expect(start).toHaveBeenCalledTimes(1);
      expect(screen.getByTestId("p-ended-time")).toHaveTextContent("0:42 / 0:42");
      expect(screen.getByTestId("p-ended-playpause")).toHaveAccessibleName(
        "Reproduzir Mensagem de voz",
      );
    });

    it("still cycles the speed 1x -> 1.5x -> 2x -> 1x after a download", async () => {
      const { start, button } = downloadable({}, "p-rate");
      fireEvent.click(button);
      await waitFor(() => expect(start).toHaveBeenCalled());

      const rate = screen.getByTestId("p-rate-rate");
      expect(rate).toHaveTextContent("1x");
      fireEvent.click(rate);
      expect(rate).toHaveTextContent("1.5x");
      fireEvent.click(rate);
      expect(rate).toHaveTextContent("2x");
      fireEvent.click(rate);
      expect(rate).toHaveTextContent("1x");
    });

    it("marks only that button busy while it resolves, and refuses a second start", () => {
      let settle = () => {};
      const start = vi
        .fn<() => Promise<void>>()
        .mockImplementation(() => new Promise<void>((resolve) => (settle = resolve)));
      render(
        <AudioPlayer
          label="Mensagem de voz"
          src="blob:voice"
          loading={false}
          failed={false}
          onRequestLoad={() => undefined}
          download={{ label: "Baixar mensagem de voz", title: "Baixar áudio", start }}
          testIdPrefix="p-busy"
        />,
      );

      const button = screen.getByTestId("p-busy-download");
      fireEvent.click(button);
      fireEvent.click(button);

      expect(start).toHaveBeenCalledTimes(1);
      expect(button).toHaveAttribute("aria-busy", "true");
      // Never disabled: disabling a focused control takes the keyboard user's
      // place in the page away from them.
      expect(button).toBeEnabled();
      // Playback controls stay usable throughout.
      expect(screen.getByTestId("p-busy-playpause")).toBeEnabled();
      expect(screen.getByTestId("p-busy-seek")).toBeEnabled();
      expect(screen.getByTestId("p-busy-rate")).toBeEnabled();

      settle();
    });

    it("reports a failure beside the player and allows a retry", async () => {
      const start = vi
        .fn<() => Promise<void>>()
        .mockRejectedValueOnce(new Error("nope"))
        .mockResolvedValueOnce(undefined);
      render(
        <AudioPlayer
          label="Mensagem de voz"
          src="blob:voice"
          loading={false}
          failed={false}
          onRequestLoad={() => undefined}
          durationHint={42}
          download={{ label: "Baixar mensagem de voz", title: "Baixar áudio", start }}
          testIdPrefix="p-fail"
        />,
      );

      fireEvent.click(screen.getByTestId("p-fail-download"));
      const note = await screen.findByTestId("p-fail-download-error");
      expect(note).toHaveTextContent("Não foi possível baixar o áudio.");
      // The player is still a player: nothing was unmounted or reset.
      expect(screen.getByTestId("p-fail-audio-el")).toBeInTheDocument();
      expect(screen.getByTestId("p-fail-time")).toHaveTextContent("0:00 / 0:42");
      expect(screen.getByTestId("p-fail-rate")).toHaveTextContent("1x");

      fireEvent.click(screen.getByTestId("p-fail-download"));
      await waitFor(() => {
        expect(screen.queryByTestId("p-fail-download-error")).not.toBeInTheDocument();
      });
      expect(start).toHaveBeenCalledTimes(2);
    });

    it("stays available for audio the browser cannot decode", () => {
      const start = vi.fn<() => Promise<void>>().mockResolvedValue(undefined);
      render(
        <AudioPlayer
          label="Mensagem de voz"
          src="blob:voice"
          loading={false}
          failed
          onRequestLoad={() => undefined}
          download={{ label: "Baixar mensagem de voz", title: "Baixar áudio", start }}
          testIdPrefix="p-broken"
        />,
      );

      expect(screen.getByTestId("p-broken-playpause")).toBeDisabled();
      fireEvent.click(screen.getByTestId("p-broken-download"));
      expect(start).toHaveBeenCalledTimes(1);
    });
  });
});
