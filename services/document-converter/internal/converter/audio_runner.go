package converter

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"
)

// AudioFormat is the input container this route knows how to demux before
// re-encoding to MP3. It names a container, not a codec: ffmpeg's own
// demuxer/decoder selection handles the codec inside it.
type AudioFormat string

const (
	AudioFormatOgg  AudioFormat = "ogg"
	AudioFormatWav  AudioFormat = "wav"
	AudioFormatWebM AudioFormat = "webm"
	AudioFormatMP4  AudioFormat = "mp4"
)

func (f AudioFormat) valid() bool {
	switch f {
	case AudioFormatOgg, AudioFormatWav, AudioFormatWebM, AudioFormatMP4:
		return true
	default:
		return false
	}
}

const (
	MaxAudioInputBytes  = 50 << 20
	MaxAudioOutputBytes = 50 << 20
)

// mp3Bitrate is the constant-bitrate encoding target for every MP3 this
// service produces. 128kbps CBR is a broadly compatible default for both
// voice and music, and fixed so output size stays predictable regardless of
// the source's loudness or codec.
const mp3Bitrate = "128k"

// AudioRunner shells out to ffmpeg to re-encode arbitrary audio (or an
// audio-only WebM/MP4 recording) into real MP3. It mirrors LibreOfficeRunner's
// process hygiene exactly: a scratch directory under a fixed, trusted work
// root, a hard timeout, and a process group killed on cancellation so nothing
// outlives the request that started it.
type AudioRunner struct {
	command string
	workDir string
	timeout time.Duration
}

// NewAudioRunner resolves command via exec.LookPath so a misconfigured or
// missing ffmpeg binary fails fast at startup rather than on the first
// request — see NewLibreOfficeRunner's identical reasoning.
func NewAudioRunner(command, workDir string, timeout time.Duration) (*AudioRunner, error) {
	resolved, err := exec.LookPath(command)
	if err != nil {
		return nil, fmt.Errorf("resolve audio converter command %q: %w", command, err)
	}
	return &AudioRunner{command: resolved, workDir: workDir, timeout: timeout}, nil
}

func (r *AudioRunner) ConvertAudio(ctx context.Context, format AudioFormat, audio []byte) ([]byte, error) {
	// r.workDir is a fixed config value set once at process startup, never
	// derived from a request — see runner.go's identical comment on this
	// exact gosec finding.
	if err := os.MkdirAll(r.workDir, 0o700); err != nil { // #nosec G703 -- r.workDir is a trusted startup config value, not request-derived
		return nil, fmt.Errorf("%w: create work root", ErrConversionFailed)
	}
	requestDir, err := os.MkdirTemp(r.workDir, "audio-request-")
	if err != nil {
		return nil, fmt.Errorf("%w: create request directory", ErrConversionFailed)
	}
	defer func() {
		// requestDir is a path os.MkdirTemp itself generated under r.workDir,
		// never anything derived from the request body — see runner.go.
		_ = os.RemoveAll(requestDir) // #nosec G304,G703 -- requestDir comes from os.MkdirTemp above, not from the request
	}()

	requestRoot, err := os.OpenRoot(requestDir)
	if err != nil {
		return nil, fmt.Errorf("%w: open request directory", ErrConversionFailed)
	}
	defer func() { _ = requestRoot.Close() }()

	const outputName = "output.mp3"
	inputName := "input." + string(format)
	if err := requestRoot.WriteFile(inputName, audio, 0o600); err != nil {
		return nil, fmt.Errorf("%w: write input", ErrConversionFailed)
	}

	// ffmpeg is an external process, not something os.Root can sandbox — it
	// needs real filesystem paths on argv. Every component below is either
	// r.workDir (fixed, trusted config) or a name this function generated
	// itself, never anything taken from the request body.
	inputPath := filepath.Join(requestDir, inputName)
	outputPath := filepath.Join(requestDir, outputName)

	commandCtx, cancel := context.WithTimeout(ctx, r.timeout)
	defer cancel()
	// r.command is resolved and verified executable once at startup by
	// NewAudioRunner (never from a request), and every argument below is a
	// fixed flag or a path this function generated itself — never anything
	// taken from the request body.
	cmd := exec.CommandContext(commandCtx, r.command, // #nosec G204,G702 -- see comment above
		"-y", "-nostdin",
		"-i", inputPath,
		"-vn",
		"-acodec", "libmp3lame",
		"-b:a", mp3Bitrate,
		"-f", "mp3",
		outputPath,
	)
	cmd.Dir = requestDir
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	cmd.WaitDelay = time.Second
	if output, err := cmd.CombinedOutput(); err != nil {
		if errors.Is(commandCtx.Err(), context.DeadlineExceeded) {
			return nil, ErrTimeout
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		_ = output // ffmpeg output may echo input metadata; never log it.
		return nil, ErrConversionFailed
	}

	file, err := requestRoot.Open(outputName)
	if err != nil {
		return nil, ErrConversionFailed
	}
	defer func() { _ = file.Close() }()
	mp3, err := io.ReadAll(io.LimitReader(file, MaxAudioOutputBytes+1))
	if err != nil {
		return nil, ErrConversionFailed
	}
	if len(mp3) > MaxAudioOutputBytes {
		return nil, ErrOutputTooLarge
	}
	return mp3, nil
}
