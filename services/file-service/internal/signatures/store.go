package signatures

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

// State is one transaction: active and previous are never separate symlinks.
// Active means published, not loaded or approved by a daemon.
type State struct {
	Active               string    `json:"active"`
	Previous             string    `json:"previous"`
	LastSuccessfulUpdate time.Time `json:"last_successful_update"`
}

type Store struct {
	Root    string
	AckRoot string
}

func readJSON(path string, target any) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Size() > 4096 {
		return ErrUntrusted
	}
	f, err := os.OpenInRoot(filepath.Dir(path), filepath.Base(path))
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	decoder := json.NewDecoder(io.LimitReader(f, 4097))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(target); err != nil {
		return err
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return ErrUntrusted
	}
	return nil
}

func (s Store) State() (State, error) {
	var state State
	err := readJSON(filepath.Join(s.Root, "state.json"), &state)
	if err != nil {
		return state, err
	}
	if !ValidGeneration(state.Active) {
		return state, ErrUntrusted
	}
	if state.Previous != "" && !ValidGeneration(state.Previous) {
		return state, ErrUntrusted
	}
	return state, nil
}

func (s Store) generation(id string) (string, error) {
	if !ValidGeneration(id) {
		return "", ErrUntrusted
	}
	path := filepath.Join(s.Root, id)
	info, err := os.Lstat(path)
	if err != nil {
		return "", err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", ErrUntrusted
	}
	return path, nil
}

// Snapshot verifies again after copying, so a compromised publisher cannot
// swap bytes between a shared-volume check and the consumer's actual load.
func (s Store) Snapshot(ctx context.Context, id, dst string, now time.Time, age time.Duration) (Manifest, error) {
	path, err := s.generation(id)
	if err != nil {
		return Manifest{}, err
	}
	if err = CopyDatabases(path, dst); err != nil {
		return Manifest{}, err
	}
	manifest, err := Verify(ctx, dst, now, age)
	if err != nil {
		return manifest, err
	}
	if manifest.ID() != id {
		return manifest, ErrUntrusted
	}
	return manifest, nil
}

func syncDir(path string) error {
	dir, err := os.OpenInRoot(filepath.Dir(path), filepath.Base(path))
	if err != nil {
		return err
	}
	return errors.Join(dir.Sync(), dir.Close())
}

func atomicJSON(root, name string, value any) error {
	f, err := os.CreateTemp(root, ".transaction-")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(f.Name()) }()
	writeErr := json.NewEncoder(f).Encode(value)
	if err = errors.Join(writeErr, f.Sync(), f.Close()); err != nil {
		return err
	}
	if err = os.Rename(f.Name(), filepath.Join(root, name)); err != nil {
		return err
	}
	return syncDir(root)
}

// WithLock also excludes a second CronJob or a manual rollback. Never remove
// the lock file: unlinking it would create independent lock domains.
func (s Store) WithLock(fn func() error) error {
	f, err := os.OpenFile(filepath.Join(s.Root, ".publisher.lock"), os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	if err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return err
	}
	defer func() { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN) }()
	return fn()
}

// Promote publishes only a verified snapshot. Caller holds WithLock. Candidate
// and root must share a filesystem: cross-device rename fails without a fallback.
func (s Store) Promote(candidate string, m Manifest, now time.Time) error {
	state, err := s.State()
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	id := m.ID()
	if id != state.Active {
		state, err = s.activateCandidate(state, candidate, m)
		if err != nil {
			return err
		}
	}
	state.LastSuccessfulUpdate = now
	return atomicJSON(s.Root, "state.json", state)
}

func (s Store) activateCandidate(state State, candidate string, m Manifest) (State, error) {
	if err := s.checkAdvance(state.Active, candidate, m); err != nil {
		return state, err
	}
	if err := atomicJSON(candidate, "manifest.json", m); err != nil {
		return state, err
	}
	previous, err := s.previousGeneration(state)
	if err != nil {
		return state, err
	}
	if err = s.publishFiles(candidate, m); err != nil {
		return state, err
	}
	state.Previous, state.Active = previous, m.ID()
	if state.Previous == state.Active {
		state.Previous = ""
	}
	return state, nil
}

// Rollback is explicit and one-way. It clears previous, preventing automatic
// oscillation back to the failed generation. Expiry is rechecked at activation.
func (s Store) Rollback(ctx context.Context, now time.Time, age time.Duration) error {
	return s.WithLock(func() error {
		state, err := s.State()
		if err != nil {
			return err
		}
		path, err := s.generation(state.Previous)
		if err != nil {
			return err
		}
		m, err := Verify(ctx, path, now, age)
		if err != nil || m.ID() != state.Previous {
			return ErrUntrusted
		}
		// A rejected generation remains on disk for investigation; never repromote it.
		if err = atomicJSON(s.Root, "rejected-"+state.Active, struct {
			At time.Time `json:"at"`
		}{now}); err != nil {
			return err
		}
		state.Active, state.Previous = state.Previous, ""
		return atomicJSON(s.Root, "state.json", state)
	})
}

// Update obtains only signed CVDs from a private HTTPS mirror. FreshClam's
// cooldown data persists across jobs; failed downloads never touch generations.
func (s Store) Update(ctx context.Context, config string, now time.Time, age time.Duration) error {
	return s.WithLock(func() error {
		work, err := s.acquire(ctx, config)
		if err != nil {
			return err
		}
		candidate, m, err := s.stage(ctx, work, now, age)
		if err != nil {
			return err
		}
		defer func() { _ = os.RemoveAll(candidate) }()
		if err = s.Promote(candidate, m, now); err != nil {
			return err
		}
		return s.prune()
	})
}

// stage owns creation and verification of a complete, unpublished generation.
func (s Store) stage(ctx context.Context, work string, now time.Time, age time.Duration) (candidate string, m Manifest, err error) {
	for _, name := range []string{"main.cld", "daily.cld", "bytecode.cld"} {
		if _, err := os.Lstat(filepath.Join(work, name)); !errors.Is(err, os.ErrNotExist) {
			return "", Manifest{}, ErrUntrusted
		}
	}
	candidate, err = os.MkdirTemp(s.Root, ".candidate-")
	if err != nil {
		return "", Manifest{}, err
	}
	stagingDir := candidate
	defer func() {
		if err != nil {
			_ = os.RemoveAll(stagingDir)
		}
	}()
	if err = CopyDatabases(work, candidate); err != nil {
		return "", Manifest{}, err
	}
	m, err = Verify(ctx, candidate, now, age)
	if err != nil {
		return "", Manifest{}, err
	}
	if _, err = os.Lstat(filepath.Join(s.Root, "rejected-"+m.ID())); !errors.Is(err, os.ErrNotExist) {
		return "", Manifest{}, ErrUntrusted
	}

	if err = atomicJSON(candidate, "manifest.json", m); err != nil {
		return "", Manifest{}, err
	}
	return candidate, m, nil
}

// Only successful publication may reclaim obsolete generations. Consumers have
// private snapshots; a concurrent startup interrupted by reclamation fails closed.
func (s Store) prune() error {
	state, err := s.State()
	if err != nil {
		return err
	}
	loaded, err := s.loadedGeneration()
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	keep := map[string]bool{state.Active: true, state.Previous: true, loaded: true}
	entries, err := os.ReadDir(s.Root)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		id := entry.Name()
		if !ValidGeneration(id) || keep[id] {
			continue
		}
		if err = os.RemoveAll(filepath.Join(s.Root, id)); err != nil {
			return err
		}
	}
	return syncDir(s.Root)
}

// Only a publisher holding the lock may reclaim interrupted, unpublished work.
func (s Store) recoverCandidates() error {
	entries, err := os.ReadDir(s.Root)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), ".candidate-") {
			continue
		}
		if err = os.RemoveAll(filepath.Join(s.Root, entry.Name())); err != nil {
			return err
		}
	}
	return nil
}

type acknowledgement struct {
	Generation string    `json:"generation"`
	LoadedAt   time.Time `json:"loaded_at"`
}

func (s Store) ackRoot() string {
	if s.AckRoot != "" {
		return s.AckRoot
	}
	return filepath.Join(s.Root, "ack")
}
func (s Store) loadedGeneration() (string, error) {
	var ack acknowledgement
	if err := readJSON(filepath.Join(s.ackRoot(), "loaded.json"), &ack); err != nil {
		return "", err
	}
	if !ValidGeneration(ack.Generation) || ack.LoadedAt.IsZero() {
		return "", ErrUntrusted
	}
	return ack.Generation, nil
}

// Acknowledge writes only the consumer-owned mount, after loading and synthetic
// checks. It is retention evidence, never authorization for another daemon.
func (s Store) Acknowledge(e Evidence) error {
	if !ValidGeneration(e.Generation) || e.LoadedAt.IsZero() {
		return ErrUntrusted
	}
	if err := os.MkdirAll(s.ackRoot(), 0700); err != nil {
		return err
	}
	return atomicJSON(s.ackRoot(), "loaded.json", acknowledgement{e.Generation, e.LoadedAt})
}

func (s Store) previousGeneration(state State) (string, error) {
	loaded, err := s.loadedGeneration()
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	if loaded != "" {
		if _, err = s.generation(loaded); err != nil {
			return "", err
		}
		return loaded, nil
	}
	if state.Previous != "" {
		return state.Previous, nil
	}
	return state.Active, nil
}

// A crash after directory rename but before state commit is recoverable without
// replacing immutable data. An existing directory must match the verified set.
func (s Store) publishFiles(candidate string, m Manifest) error {
	path, err := s.generation(m.ID())
	if errors.Is(err, os.ErrNotExist) {
		if err = os.Rename(candidate, filepath.Join(s.Root, m.ID())); err != nil {
			return err
		}
		return syncDir(s.Root)
	}
	if err != nil {
		return err
	}
	for i, name := range databaseNames {
		sum, err := databaseHash(filepath.Join(path, name))
		if err != nil || sum != m.Hashes[i] {
			return ErrUntrusted
		}
	}
	return nil
}

func (s Store) acquire(ctx context.Context, config string) (string, error) {
	if err := s.recoverCandidates(); err != nil {
		return "", err
	}
	if err := os.MkdirAll(s.ackRoot(), 0700); err != nil {
		return "", err
	}
	work := filepath.Join(s.Root, "download")
	if err := os.MkdirAll(work, 0700); err != nil {
		return "", err
	}
	if err := Run(ctx, "freshclam", "--config-file="+config, "--datadir="+work); err != nil {
		return "", err
	}

	return work, nil
}

// ConsumerLock serializes snapshot+activation+ack with publication/GC. It opens
// the publisher lock read-only, so the consumer's published mount stays read-only.
func (s Store) ConsumerLock() (func(), error) {
	f, err := os.OpenFile(filepath.Join(s.Root, ".publisher.lock"), os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	if err = syscall.Flock(int(f.Fd()), syscall.LOCK_SH|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		return nil, err
	}
	var once sync.Once
	return func() { once.Do(func() { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN); _ = f.Close() }) }, nil
}
