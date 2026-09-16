package converter

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func newTestAudioRunner(t *testing.T, command, work string, timeout time.Duration) *AudioRunner {
	t.Helper()
	runner, err := NewAudioRunner(command, work, timeout)
	if err != nil {
		t.Fatal(err)
	}
	return runner
}

func TestNewAudioRunnerRejectsAnUnresolvableCommand(t *testing.T) {
	_, err := NewAudioRunner("this-command-does-not-exist-anywhere", t.TempDir(), time.Second)
	if err == nil {
		t.Fatal("want an error for a command that cannot be resolved via exec.LookPath")
	}
}

func TestAudioRunnerReturnsMP3AndCleansRequestDirectory(t *testing.T) {
	root := t.TempDir()
	command := filepath.Join(root, "ffmpeg")
	// A fake ffmpeg: writes a minimal MP3 frame to whatever path is its last
	// argument, regardless of what came before it — enough to exercise the
	// runner's own plumbing without needing the real binary.
	script := "#!/bin/sh\nfor last; do :; done\nprintf '\\377\\373\\220\\104MP3-PAYLOAD' > \"$last\"\n"
	writeScript(t, command, script)
	work := filepath.Join(root, "work")
	runner := newTestAudioRunner(t, command, work, 2*time.Second)
	mp3, err := runner.ConvertAudio(context.Background(), AudioFormatOgg, []byte("OggS-source"))
	if err != nil || !strings.HasSuffix(string(mp3), "MP3-PAYLOAD") {
		t.Fatalf("ConvertAudio = %q, %v", mp3, err)
	}
	entries, err := os.ReadDir(work)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("temporary request directories remain: %v", entries)
	}
}

func TestAudioRunnerKillsTimeoutAndCleansRequestDirectory(t *testing.T) {
	root := t.TempDir()
	command := filepath.Join(root, "ffmpeg")
	writeScript(t, command, "#!/bin/sh\nsleep 5\n")
	work := filepath.Join(root, "work")
	runner := newTestAudioRunner(t, command, work, 20*time.Millisecond)
	started := time.Now()
	_, err := runner.ConvertAudio(context.Background(), AudioFormatOgg, []byte("source"))
	if !errors.Is(err, ErrTimeout) {
		t.Fatalf("error = %v, want timeout", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("timeout returned after %v; child process was not killed", elapsed)
	}
	entries, readErr := os.ReadDir(work)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if len(entries) != 0 {
		t.Fatalf("temporary request directories remain: %v", entries)
	}
}

func TestAudioRunnerReturnsConversionFailedForANonZeroExit(t *testing.T) {
	root := t.TempDir()
	command := filepath.Join(root, "ffmpeg")
	writeScript(t, command, "#!/bin/sh\nexit 1\n")
	work := filepath.Join(root, "work")
	runner := newTestAudioRunner(t, command, work, 2*time.Second)
	_, err := runner.ConvertAudio(context.Background(), AudioFormatWav, []byte("source"))
	if !errors.Is(err, ErrConversionFailed) {
		t.Fatalf("error = %v, want ErrConversionFailed", err)
	}
}

func TestAudioRunnerReturnsConversionFailedWhenNoOutputIsProduced(t *testing.T) {
	root := t.TempDir()
	command := filepath.Join(root, "ffmpeg")
	writeScript(t, command, "#!/bin/sh\nexit 0\n")
	work := filepath.Join(root, "work")
	runner := newTestAudioRunner(t, command, work, 2*time.Second)
	_, err := runner.ConvertAudio(context.Background(), AudioFormatWav, []byte("source"))
	if !errors.Is(err, ErrConversionFailed) {
		t.Fatalf("error = %v, want ErrConversionFailed", err)
	}
}

func TestAudioRunnerReturnsOutputTooLargeOverTheCap(t *testing.T) {
	root := t.TempDir()
	command := filepath.Join(root, "ffmpeg")
	// MaxAudioOutputBytes is 50<<20 (50MiB); 51 * 1MiB blocks of zeros
	// comfortably exceeds it while staying cheap to generate in a test.
	script := "#!/bin/sh\nfor last; do :; done\ndd if=/dev/zero of=\"$last\" bs=1M count=51 2>/dev/null\n"
	writeScript(t, command, script)
	work := filepath.Join(root, "work")
	runner := newTestAudioRunner(t, command, work, 10*time.Second)
	_, err := runner.ConvertAudio(context.Background(), AudioFormatWav, []byte("source"))
	if !errors.Is(err, ErrOutputTooLarge) {
		t.Fatalf("error = %v, want ErrOutputTooLarge", err)
	}
}

func TestAudioRunnerHonoursAnAlreadyCanceledContext(t *testing.T) {
	root := t.TempDir()
	command := filepath.Join(root, "ffmpeg")
	writeScript(t, command, "#!/bin/sh\nsleep 5\n")
	work := filepath.Join(root, "work")
	runner := newTestAudioRunner(t, command, work, 2*time.Second)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := runner.ConvertAudio(ctx, AudioFormatOgg, []byte("source"))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
}

// TestAudioRunnerConvertsRealAudioToDecodableMP3 is the decisive proof this
// feature exists for: real OGG/Vorbis and WAV/PCM sources — neither one MP3
// bytes to begin with — come out the other end as audio ffprobe itself
// reports as codec_name=mp3, not merely a file whose name or Content-Type
// changed. It is skipped, not faked, when ffmpeg/ffprobe are unavailable:
// this is the one test in the suite that must exercise the real binary, since
// every other AudioRunner test above deliberately fakes it.
func TestAudioRunnerConvertsRealAudioToDecodableMP3(t *testing.T) {
	ffmpegPath, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg not available in this environment")
	}
	ffprobePath, err := exec.LookPath("ffprobe")
	if err != nil {
		t.Skip("ffprobe not available in this environment")
	}

	runner := newTestAudioRunner(t, ffmpegPath, filepath.Join(t.TempDir(), "work"), 20*time.Second)

	tests := []struct {
		name   string
		format AudioFormat
		args   []string
	}{
		{"ogg/vorbis source", AudioFormatOgg, []string{"-c:a", "libvorbis", "-f", "ogg"}},
		{"wav/pcm source", AudioFormatWav, []string{"-c:a", "pcm_s16le", "-f", "wav"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			srcPath := filepath.Join(t.TempDir(), "source."+string(test.format))
			args := append([]string{"-y", "-f", "lavfi", "-i", "sine=frequency=440:duration=0.5"}, test.args...)
			args = append(args, srcPath)
			if out, genErr := exec.Command(ffmpegPath, args...).CombinedOutput(); genErr != nil { // #nosec G204 -- ffmpegPath is resolved via exec.LookPath above and every argument is a fixed literal in this test
				t.Fatalf("generate %s fixture: %v\n%s", test.name, genErr, out)
			}
			source, readErr := os.ReadFile(srcPath) // #nosec G304 -- srcPath is built entirely from t.TempDir() and a fixed literal above
			if readErr != nil {
				t.Fatal(readErr)
			}

			mp3, convErr := runner.ConvertAudio(context.Background(), test.format, source)
			if convErr != nil {
				t.Fatalf("ConvertAudio = %v", convErr)
			}
			if !validMP3(mp3) {
				t.Fatalf("output does not start with an MP3 sync word or ID3 tag")
			}

			outPath := filepath.Join(t.TempDir(), "out.mp3")
			if writeErr := os.WriteFile(outPath, mp3, 0o600); writeErr != nil {
				t.Fatal(writeErr)
			}
			probe := exec.Command(ffprobePath, // #nosec G204 -- ffprobePath is resolved via exec.LookPath above and every argument is a fixed literal or outPath, built entirely from t.TempDir()
				"-v", "error", "-show_entries", "stream=codec_name",
				"-of", "default=noprint_wrappers=1:nokey=1", outPath,
			)
			out, probeErr := probe.Output()
			if probeErr != nil {
				t.Fatalf("ffprobe: %v", probeErr)
			}
			if codec := strings.TrimSpace(string(out)); codec != "mp3" {
				t.Fatalf("ffprobe codec_name = %q, want mp3 — the source was not actually re-encoded", codec)
			}
		})
	}
}
