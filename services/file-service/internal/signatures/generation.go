package signatures

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

var databaseNames = []string{"main.cvd", "daily.cvd", "bytecode.cvd"}

const maxDatabaseBytes = 512 << 20

// Manifest hashes bind the complete set. They detect storage corruption;
// authenticity is independently checked with sigtool on every activation.
type Manifest struct {
	Engine       string    `json:"engine"`
	Hashes       [3]string `json:"hashes"`
	DailyVersion int64     `json:"daily_version"`
	DatabaseTime time.Time `json:"database_time"`
	VerifiedAt   time.Time `json:"verified_at"`
}

func (m Manifest) ID() string {
	// Verification time must not turn an unchanged download into a new generation.
	m.VerifiedAt = time.Time{}
	data, _ := json.Marshal(m)
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}

// Run uses fixed tools and argument arrays, never a shell. No remote or content
// derived output is returned to service logs.
func Run(ctx context.Context, tool string, args ...string) error {
	switch tool {
	case "freshclam", "sigtool", "clamscan":
	default:
		return ErrUntrusted
	}

	cmd := exec.CommandContext(ctx, tool, args...) //nolint:gosec // Fixed tool allowlist; internal argument arrays, no shell or upload inputs.
	if tool == "clamscan" {
		cmd.Stdin = strings.NewReader("NChat signature compatibility probe\n")
	}
	cmd.Env = []string{"PATH=/usr/local/bin:/usr/bin:/bin", "HOME=/tmp", "TMPDIR=/tmp", "LC_ALL=C"}
	if ca := os.Getenv("CURL_CA_BUNDLE"); ca != "" {
		cmd.Env = append(cmd.Env, "CURL_CA_BUNDLE="+ca)
	}
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("signature tool %s failed", tool)
	}
	return nil
}

func engineMatches(ctx context.Context) error {
	out, err := exec.CommandContext(ctx, "clamd", "--version").Output()
	if err != nil || !strings.HasPrefix(string(out), "ClamAV "+Engine+"/") {
		return ErrUntrusted
	}
	return nil
}

// Verify accepts only independently signed full CVDs. CLDs rely on incremental
// provenance and are deliberately unsupported in this private CVD mirror model.
func Verify(ctx context.Context, dir string, now time.Time, maxAge time.Duration) (Manifest, error) {
	m, err := readManifest(ctx, dir, now)
	if err != nil {
		return m, err
	}
	evidence := Evidence{Generation: m.ID(), Engine: m.Engine, DailyVersion: m.DailyVersion, DatabaseTime: m.DatabaseTime, LoadedAt: now}
	if err := evidence.Valid(now, maxAge); err != nil {
		return m, err
	}
	return m, Run(ctx, "clamscan", "--database="+dir, "--official-db-only=yes", "--no-summary", "-")
}

func readManifest(ctx context.Context, dir string, now time.Time) (Manifest, error) {
	m := Manifest{Engine: Engine, VerifiedAt: now}
	if err := engineMatches(ctx); err != nil {
		return m, err
	}
	for i, name := range databaseNames {
		path := filepath.Join(dir, name)
		sum, err := databaseHash(path)
		if err != nil {
			return m, err
		}
		if err = Run(ctx, "sigtool", "--info="+path); err != nil {
			return m, err
		}
		version, timestamp, err := databaseHeader(path)
		if err != nil {
			return m, err
		}
		m.Hashes[i] = sum
		if name == "daily.cvd" {
			m.DailyVersion, m.DatabaseTime = version, timestamp
		}
	}

	return m, nil
}

func databaseHash(path string) (string, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() || info.Size() < 512 || info.Size() > maxDatabaseBytes {
		return "", ErrUntrusted
	}
	f, err := os.OpenInRoot(filepath.Dir(path), filepath.Base(path))
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	if _, err = io.Copy(h, io.LimitReader(f, maxDatabaseBytes+1)); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func databaseHeader(path string) (int64, time.Time, error) {
	f, err := os.OpenInRoot(filepath.Dir(path), filepath.Base(path))
	if err != nil {
		return 0, time.Time{}, err
	}
	defer func() { _ = f.Close() }()
	header := make([]byte, 512)
	if _, err = io.ReadFull(f, header); err != nil {
		return 0, time.Time{}, err
	}
	fields := strings.Split(strings.TrimSpace(string(header)), ":")
	if len(fields) != 9 || fields[0] != "ClamAV-VDB" {
		return 0, time.Time{}, ErrUntrusted
	}
	version, e1 := boundedHeaderNumber(fields[2], 1, 1<<63-1)
	if e1 != nil {
		return 0, time.Time{}, e1
	}
	_, e2 := boundedHeaderNumber(fields[4], 0, FunctionalityLevel)
	if e2 != nil {
		return 0, time.Time{}, e2
	}
	stamp, e3 := boundedHeaderNumber(fields[8], 1, 1<<63-1)
	if e3 != nil {
		return 0, time.Time{}, e3
	}
	return version, time.Unix(stamp, 0).UTC(), nil
}

// CopyDatabases creates a private snapshot with an exact allowlist. Callers hold
// the publisher lock or read immutable generations; unlisted files are never loaded.
func CopyDatabases(src, dst string) error {
	if err := os.MkdirAll(dst, 0700); err != nil {
		return err
	}
	for _, name := range databaseNames {
		path := filepath.Join(src, name)
		if _, err := databaseHash(path); err != nil {
			return err
		}
		if err := copyDatabase(path, filepath.Join(dst, name)); err != nil {
			return err
		}
	}
	return nil
}

func copyDatabase(src, dst string) error {
	in, err := os.OpenInRoot(filepath.Dir(src), filepath.Base(src))
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	root, err := os.OpenRoot(filepath.Dir(dst))
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	out, err := root.OpenFile(filepath.Base(dst), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0400)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(out, io.LimitReader(in, maxDatabaseBytes+1))
	syncErr := out.Sync()
	return errors.Join(copyErr, syncErr, out.Close())
}

func boundedHeaderNumber(value string, minValue, maxValue int64) (int64, error) {
	n, err := strconv.ParseInt(value, 10, 64)
	if err != nil || n < minValue || n > maxValue {
		return 0, ErrUntrusted
	}
	return n, nil
}
