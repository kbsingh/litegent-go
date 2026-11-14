package session

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync/atomic"
	"time"

	"litegent/internal/config"
)

const (
	// ManifestName is the per-session metadata file.
	ManifestName = "session.json"
	// ContextName is the single authoritative context blob.
	ContextName = "context.json"
	// LockName is the advisory lock file used to prevent concurrent
	// writers for the same session.
	LockName = ".lock"

	manifestVersion = 1
	maxCreateTries  = 100
)

// Options controls the per-session subdirectories and metadata recorded
// when a session is created.
type Options struct {
	LogSubdir   string
	SpoolSubdir string
	Model       string
	BaseURL     string
}

// Info describes the on-disk layout for one session run.
type Info struct {
	ID           string
	RunID        string
	Dir          string
	ManifestPath string
	ContextPath  string
	LogDir       string
	SpoolDir     string
	LockPath     string
}

// Manifest is the durable metadata for a session. It intentionally never
// contains prompts, message content, or credentials.
type Manifest struct {
	Version    int       `json:"version"`
	ID         string    `json:"id"`
	CreatedAt  time.Time `json:"created_at"`
	LastUsedAt time.Time `json:"last_used_at"`
	RunCount   int       `json:"run_count"`
	TurnCount  int       `json:"turn_count"`
	Model      string    `json:"model,omitempty"`
	BaseURL    string    `json:"base_url,omitempty"`
}

var runCounter atomic.Uint64

// NewRunID returns a filename-safe unique identifier for one invocation.
func NewRunID() string {
	return fmt.Sprintf(
		"%s-%d-%d",
		time.Now().UTC().Format("20060102T150405_000000000Z"),
		os.Getpid(),
		runCounter.Add(1),
	)
}

// Create makes a new session under topDir. It generates candidate IDs and
// claims one with an atomic mkdir, so concurrent processes cannot select
// the same ID. On any later initialization failure the new directory is
// removed.
func Create(topDir string, opts Options) (*Info, error) {
	gen := NewGenerator()
	return create(topDir, opts, gen.ID)
}

func create(topDir string, opts Options, nextID func() string) (*Info, error) {
	if topDir == "" {
		return nil, fmt.Errorf("top_dir must not be empty")
	}
	if err := os.MkdirAll(topDir, 0o700); err != nil {
		return nil, fmt.Errorf("creating top_dir %q: %w", topDir, err)
	}

	for attempt := 0; attempt < maxCreateTries; attempt++ {
		id := nextID()
		if err := ValidateID(id); err != nil {
			return nil, fmt.Errorf("generated session id: %w", err)
		}
		if _, reserved := reservedIDs[id]; reserved {
			continue
		}

		dir := filepath.Join(topDir, id)
		err := os.Mkdir(dir, 0o700)
		if errors.Is(err, fs.ErrExist) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("creating session %q: %w", id, err)
		}

		info, err := initialize(dir, id, opts)
		if err != nil {
			_ = os.RemoveAll(dir)
			return nil, err
		}
		return info, nil
	}
	return nil, fmt.Errorf("could not generate a unique session id after %d attempts", maxCreateTries)
}

func initialize(dir, id string, opts Options) (*Info, error) {
	logSubdir := opts.LogSubdir
	if logSubdir == "" {
		logSubdir = "logs"
	}
	spoolSubdir := opts.SpoolSubdir
	if spoolSubdir == "" {
		spoolSubdir = "spool"
	}

	logDir, err := config.ResolveUnder(dir, logSubdir)
	if err != nil {
		return nil, fmt.Errorf("resolving session log directory: %w", err)
	}
	spoolDir, err := config.ResolveUnder(dir, spoolSubdir)
	if err != nil {
		return nil, fmt.Errorf("resolving session spool directory: %w", err)
	}
	for _, path := range []string{logDir, spoolDir} {
		if err := os.MkdirAll(path, 0o700); err != nil {
			return nil, fmt.Errorf("creating session directory %q: %w", path, err)
		}
	}

	now := time.Now().UTC()
	manifest := Manifest{
		Version:    manifestVersion,
		ID:         id,
		CreatedAt:  now,
		LastUsedAt: now,
		RunCount:   1,
		TurnCount:  0,
		Model:      opts.Model,
		BaseURL:    opts.BaseURL,
	}
	manifestPath := filepath.Join(dir, ManifestName)
	if err := writeManifest(manifestPath, manifest); err != nil {
		return nil, err
	}

	return &Info{
		ID:           id,
		RunID:        NewRunID(),
		Dir:          dir,
		ManifestPath: manifestPath,
		ContextPath:  filepath.Join(dir, ContextName),
		LogDir:       logDir,
		SpoolDir:     spoolDir,
		LockPath:     filepath.Join(dir, LockName),
	}, nil
}

// LoadManifest reads and validates a session manifest from a session
// directory.
func LoadManifest(dir string) (Manifest, error) {
	path := filepath.Join(dir, ManifestName)
	data, err := os.ReadFile(path)
	if err != nil {
		return Manifest{}, fmt.Errorf("reading session manifest %q: %w", path, err)
	}
	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return Manifest{}, fmt.Errorf("parsing session manifest %q: %w", path, err)
	}
	if m.Version != manifestVersion {
		return Manifest{}, fmt.Errorf("unsupported session manifest version %d", m.Version)
	}
	wantID := filepath.Base(filepath.Clean(dir))
	if m.ID != wantID {
		return Manifest{}, fmt.Errorf("session manifest id %q does not match directory %q", m.ID, wantID)
	}
	return m, nil
}

func writeManifest(path string, m Manifest) error {
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling session manifest: %w", err)
	}
	data = append(data, '\n')
	if err := writeFileAtomic(path, data, 0o600); err != nil {
		return fmt.Errorf("writing session manifest %q: %w", path, err)
	}
	return nil
}

// writeFileAtomic writes data through a same-directory temp file and an
// atomic rename, syncing the file before the rename. Readers therefore
// see either the previous complete file or the new complete file.
func writeFileAtomic(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	f, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)

	if err := f.Chmod(mode); err != nil {
		f.Close()
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	return nil
}
