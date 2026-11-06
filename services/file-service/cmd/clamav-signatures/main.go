// clamav-signatures is an isolated signature publisher or immutable scan guardian.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"

	"github.com/nicrepository/nchat/services/file-service/internal/signatureguard"
	"github.com/nicrepository/nchat/services/file-service/internal/signatures"
)

func main() {
	err := run()
	if errors.Is(err, signatureguard.ErrGenerationChanged) {
		_, _ = fmt.Fprintln(os.Stdout, "ClamAV generation changed; controlled restart requested")
		return
	}
	if errors.Is(err, context.Canceled) {
		return
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "ClamAV signature lifecycle failed; approvals remain closed")
		os.Exit(1)
	}
	_, _ = fmt.Fprintln(os.Stdout, "ClamAV signature lifecycle completed")
}

func run() error {
	mode := flag.String("mode", "", "update, serve or rollback")
	root := flag.String("store", "/signatures", "generation store")
	ack := flag.String("ack", "", "consumer acknowledgement mount")
	config := flag.String("clamd-config", "/etc/clamav/clamd.conf", "versioned scan limits")
	mirror := flag.String("mirror", "", "approved private HTTPS CVD-only mirror")
	listen := flag.String("listen", ":3310", "internal guardian endpoint")
	maxAge := flag.Duration("max-age", 0, "human-approved maximum daily CVD age; required")
	syntheticApproved := flag.Bool("isolated-synthetic-approved", false, "explicitly approved isolated EICAR activation")
	flag.Parse()
	if *mode == "install" {
		return install()
	}
	if *maxAge <= 0 || *maxAge > 365*24*time.Hour {
		return signatures.ErrUntrusted
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	store := signatures.Store{Root: *root, AckRoot: *ack}
	switch *mode {
	case "serve":
		if store.AckRoot == "" {
			store.AckRoot = "/ack"
		}
		if !*syntheticApproved {
			return signatures.ErrUntrusted
		}
		return signatureguard.Serve(ctx, store, *config, *listen, *maxAge)
	case "rollback":
		return store.Rollback(ctx, time.Now().UTC(), *maxAge)
	case "update":
		return update(ctx, store, *mirror, *maxAge)
	default:
		return signatures.ErrUntrusted
	}
}

func update(ctx context.Context, store signatures.Store, mirror string, age time.Duration) error {
	if !validMirror(mirror) {
		return signatures.ErrUntrusted
	}
	dir, err := os.MkdirTemp("/tmp", "freshclam-config-")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(dir) }()
	config := filepath.Join(dir, "freshclam.conf")
	contents := "DatabaseOwner clamav\nPrivateMirror " + mirror + "\nScriptedUpdates no\nTestDatabases yes\nBytecode yes\nMaxAttempts 2\nConnectTimeout 10\nReceiveTimeout 60\n"
	if err = os.WriteFile(config, []byte(contents), 0600); err != nil {
		return err
	}
	updateCtx, cancel := context.WithTimeout(ctx, 20*time.Minute)
	defer cancel()
	return store.Update(updateCtx, config, time.Now().UTC(), age)
}

// install is the binary-only init container; the official runtime mounts tools read-only.
func install() error {
	source, err := os.Open("/proc/self/exe")
	if err != nil {
		return err
	}
	defer func() { _ = source.Close() }()
	target, err := os.CreateTemp("/tools", ".install-")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(target.Name()) }()
	_, copyErr := io.Copy(target, source)
	if copyErr != nil {
		_ = target.Close()
		return copyErr
	}
	if err = target.Chmod(0500); err != nil {
		_ = target.Close()
		return err
	}
	if err = target.Sync(); err != nil {
		_ = target.Close()
		return err
	}
	if err = target.Close(); err != nil {
		return err
	}
	return os.Rename(target.Name(), "/tools/clamav-signatures")
}

func validMirror(mirror string) bool {
	if !regexp.MustCompile(`^https://[A-Za-z0-9.-]+(?::443)?(?:/[A-Za-z0-9_/-]*)?$`).MatchString(mirror) {
		return false
	}
	target, err := url.Parse(mirror)
	if err != nil {
		return false
	}
	host := strings.ToLower(target.Hostname())
	return host != "clamav.net" && !strings.HasSuffix(host, ".clamav.net")
}
