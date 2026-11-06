package signatureguard

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/nicrepository/nchat/services/file-service/internal/scanner"
	"github.com/nicrepository/nchat/services/file-service/internal/signatures"
)

var ErrGenerationChanged = errors.New("signature generation changed")

// SyntheticPayload is the standard EICAR test string, never malware. Runtime
// activation is permitted only in an explicitly approved isolated test context.
const SyntheticPayload = `X5O!P%@AP[4\PZX54(P^)7CC)7}$EICAR-STANDARD-ANTIVIRUS-TEST-FILE!$H+H*`

// Serve runs one generation, exits on publication change, and lets the container
// restart with a new snapshot. No hot reload, second engine or Kubernetes token.
func Serve(ctx context.Context, store signatures.Store, baseConfig, listen string, age time.Duration) error {
	for {
		floor, err := waitClock(ctx, store)
		if err != nil {
			return err
		}
		err = serveCycle(ctx, store, baseConfig, listen, age, floor)
		if !errors.Is(err, signatures.ErrClockRollback) {
			return err
		}
		slog.Error("signature cycle closed", "error_type", "clock_rollback")
	}
}

func serveCycle(ctx context.Context, store signatures.Store, baseConfig, listen string, age time.Duration, floor time.Time) error {
	release, err := store.ConsumerLock()
	if err != nil {
		return err
	}
	defer release()
	floor, err = beginClock(store, floor)
	if err != nil {
		return err
	}
	state, err := store.State()
	if err != nil {
		return err
	}
	snapshot, err := os.MkdirTemp("/tmp", "clamav-snapshot-")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(snapshot) }()
	manifest, err := store.Snapshot(ctx, state.Active, snapshot, time.Now().UTC(), age)
	if err != nil {
		return err
	}
	config, err := privateConfig(baseConfig, snapshot)
	if err != nil {
		return err
	}
	childCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cmd := exec.CommandContext(childCtx, "clamd", "--foreground", "--config-file="+config) //nolint:gosec // Fixed executable and private generated config; no shell or attachment input.
	if err = cmd.Start(); err != nil {
		return errors.New("signature engine start failed")
	}
	done := make(chan struct{})
	go func() { _ = cmd.Wait(); close(done) }()
	defer func() { cancel(); <-done }()
	local, err := scanner.NewLocal(filepath.Join(snapshot, "clamd.sock"), 300*time.Second)
	if err != nil {
		return err
	}
	guard, err := activateGuard(ctx, store, local, state, manifest, age, floor)
	if err != nil {
		return err
	}
	release()
	return serveGeneration(ctx, store, guard, listen, done)
}

func activateGuard(ctx context.Context, store signatures.Store, local *scanner.Clamd, state signatures.State, manifest signatures.Manifest, age time.Duration, floor time.Time) (*Guard, error) {
	if err := waitLoaded(ctx, local, manifest); err != nil {
		return nil, err
	}
	guard := &Guard{Scanner: local, MaxAge: age, LastUpdate: state.LastSuccessfulUpdate, Evidence: signatures.Evidence{
		Generation: state.Active, Engine: manifest.Engine, DailyVersion: manifest.DailyVersion,
		DatabaseTime: manifest.DatabaseTime, LoadedAt: time.Now().UTC(),
	}}
	guard.lastClock = floor.UnixNano()
	guard.checkpoint = store.CheckpointClock
	if !guard.ready() {
		return nil, errors.Join(signatures.ErrUntrusted, guard.failure())
	}
	if err := store.Acknowledge(guard.Evidence); err != nil {
		return nil, err
	}
	return guard, nil
}

func privateConfig(base, snapshot string) (string, error) {
	source, err := os.OpenInRoot(filepath.Dir(base), filepath.Base(base))
	if err != nil {
		return "", err
	}
	defer func() { _ = source.Close() }()
	data, err := io.ReadAll(io.LimitReader(source, 65537))
	if err != nil {
		return "", err
	}
	if len(data) > 65536 {
		return "", signatures.ErrUntrusted
	}
	if err = validateScanLimits(string(data)); err != nil {
		return "", err
	}
	var lines []string
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) > 0 && privateDirective(fields[0]) {
			continue
		}
		lines = append(lines, line)
	}
	lines = append(lines, "DatabaseDirectory "+snapshot, "LocalSocket "+filepath.Join(snapshot, "clamd.sock"),
		"LocalSocketMode 600", "SelfCheck 0", "ConcurrentDatabaseReload no", "OfficialDatabaseOnly yes")
	config := filepath.Join(snapshot, "clamd.conf")
	root, err := os.OpenRoot(snapshot)
	if err != nil {
		return "", err
	}
	defer func() { _ = root.Close() }()
	return config, root.WriteFile("clamd.conf", []byte(strings.Join(lines, "\n")+"\n"), 0600)
}

func privateDirective(name string) bool {
	switch name {
	case "TCPSocket", "TCPAddr", "LocalSocket", "LocalSocketMode", "DatabaseDirectory", "SelfCheck", "ConcurrentDatabaseReload", "OfficialDatabaseOnly":
		return true
	default:
		return false
	}
}

func waitLoaded(ctx context.Context, local *scanner.Clamd, m signatures.Manifest) error {
	loadCtx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		version, err := local.Version(loadCtx)
		if err == nil {
			return probeLoaded(loadCtx, local, version, m)
		}
		select {
		case <-loadCtx.Done():
			return loadCtx.Err()
		case <-ticker.C:
		}
	}
}

func probeLoaded(ctx context.Context, local *scanner.Clamd, version string, m signatures.Manifest) error {
	if !strings.HasPrefix(version, fmt.Sprintf("ClamAV %s/%d/", m.Engine, m.DailyVersion)) {
		return signatures.ErrUntrusted
	}
	if err := local.Ping(ctx); err != nil {
		return err
	}
	return synthetic(ctx, local)
}

func synthetic(ctx context.Context, local scanner.Scanner) error {
	verdict, err := local.Scan(ctx, strings.NewReader(SyntheticPayload))
	if err != nil || verdict != scanner.VerdictInfected {
		return signatures.ErrUntrusted
	}
	verdict, err = local.Scan(ctx, strings.NewReader("NChat isolated benign signature probe\n"))
	if err != nil || verdict != scanner.VerdictClean {
		return signatures.ErrUntrusted
	}
	return nil
}

func serveGeneration(ctx context.Context, store signatures.Store, guard *Guard, listen string, engineDone <-chan struct{}) error {
	listener, err := net.Listen("tcp", listen)
	if err != nil {
		return err
	}
	server := &http.Server{Handler: guard, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 300 * time.Second, WriteTimeout: 305 * time.Second, IdleTimeout: 5 * time.Second, MaxHeaderBytes: 4096}
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	fault := guard.faultSignal()
	clockPoll := time.NewTicker(time.Second)
	defer clockPoll.Stop()
	poll := time.NewTicker(30 * time.Second)
	defer poll.Stop()
	defer shutdownGuard(server, guard)

	for {
		select {
		case err = <-done:
			return err
		case <-fault:
			_ = server.Close() // Invalidate request contexts before orderly child cleanup.
			return guard.failure()
		case <-clockPoll.C:
			_ = guard.ready()
		case <-engineDone:
			return errors.New("signature engine exited")
		case <-ctx.Done():
			return ctx.Err()
		case <-poll.C:
			if err = checkPublication(store, guard); err != nil {
				return err
			}
		}
	}
}

func shutdownGuard(server *http.Server, guard *Guard) {
	guard.Close()
	drain, cancel := context.WithTimeout(context.Background(), 310*time.Second)
	defer cancel()
	if server.Shutdown(drain) != nil {
		_ = server.Close()
	}
}

func checkPublication(store signatures.Store, guard *Guard) error {
	state, err := store.State()
	if err != nil {
		return errors.New("signature publication unavailable")
	}
	if state.Active != guard.Evidence.Generation {
		return ErrGenerationChanged
	}
	guard.recordUpdate(state.LastSuccessfulUpdate)
	return nil
}

// Refuse unsafe defaults if the mounted versioned configuration is incomplete.
func validateScanLimits(data string) error {
	required := map[string]string{"AlertExceedsMax": "yes", "MaxScanTime": "420000", "StreamMaxLength": "512M", "MaxFileSize": "512M", "MaxScanSize": "1024M", "MaxThreads": "4", "MaxRecursion": "12", "MaxFiles": "10000"}
	found := make(map[string]string)
	for _, line := range strings.Split(data, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 || strings.HasPrefix(fields[0], "#") {
			continue
		}
		if _, critical := required[fields[0]]; !critical {
			continue
		}
		if _, duplicate := found[fields[0]]; duplicate {
			return signatures.ErrUntrusted
		}
		found[fields[0]] = strings.Join(fields[1:], " ")
	}
	if !maps.Equal(found, required) {
		return signatures.ErrUntrusted
	}
	return nil
}
