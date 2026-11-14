package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWordListsAreValid(t *testing.T) {
	lists := map[string][]string{
		"adjectives": adjectives,
		"nouns":      nouns,
		"verbs":      verbs,
		"adverbs":    adverbs,
	}
	for name, words := range lists {
		if len(words) < 32 {
			t.Errorf("%s list has only %d words, want at least 32", name, len(words))
		}
		seen := make(map[string]struct{}, len(words))
		for _, w := range words {
			if w == "" || w != strings.ToLower(w) || strings.ContainsAny(w, " -") {
				t.Errorf("%s contains invalid word %q", name, w)
			}
			if _, ok := seen[w]; ok {
				t.Errorf("%s contains duplicate word %q", name, w)
			}
			seen[w] = struct{}{}
		}
	}
}

func TestValidateID(t *testing.T) {
	valid := []string{
		"brave-otter-runs",
		"swift-otter-runs-quietly",
		"calm-river-sings",
	}
	for _, id := range valid {
		if err := ValidateID(id); err != nil {
			t.Errorf("ValidateID(%q) = %v, want nil", id, err)
		}
	}

	invalid := []string{
		"",
		"brave",
		"brave-otter",
		"brave-otter-runs-quietly-now",
		"Brave-otter-runs",
		"brave_otter_runs",
		"../brave-otter-runs",
		"brave/otter/runs",
		"brave--otter-runs",
		"brave-otter-runs-",
		"config.json",
		"skills",
		strings.Repeat("a", maxIDLength+1),
	}
	for _, id := range invalid {
		if err := ValidateID(id); err == nil {
			t.Errorf("ValidateID(%q) = nil, want error", id)
		}
	}
}

func TestGeneratorFormat(t *testing.T) {
	g := NewGenerator()
	for i := 0; i < 200; i++ {
		id := g.ID()
		if err := ValidateID(id); err != nil {
			t.Fatalf("generated invalid id %q: %v", id, err)
		}
		if n := len(strings.Split(id, "-")); n != 3 && n != 4 {
			t.Fatalf("generated id %q has %d words, want 3 or 4", id, n)
		}
	}
}

func TestCreateLayoutAndManifest(t *testing.T) {
	top := t.TempDir()
	info, err := create(top, Options{
		LogSubdir:   "logs",
		SpoolSubdir: "spool",
		Model:       "test-model",
		BaseURL:     "https://example.invalid/v1",
	}, func() string { return "brave-otter-runs" })
	if err != nil {
		t.Fatalf("create returned error: %v", err)
	}

	if info.ID != "brave-otter-runs" {
		t.Errorf("ID = %q", info.ID)
	}
	if want := filepath.Join(top, info.ID); info.Dir != want {
		t.Errorf("Dir = %q, want %q", info.Dir, want)
	}
	for _, path := range []string{info.Dir, info.LogDir, info.SpoolDir, info.ManifestPath} {
		if _, err := os.Stat(path); err != nil {
			t.Errorf("expected %s to exist: %v", path, err)
		}
	}
	if info.RunID == "" {
		t.Error("RunID must not be empty")
	}

	m, err := LoadManifest(info.Dir)
	if err != nil {
		t.Fatalf("LoadManifest returned error: %v", err)
	}
	if m.ID != info.ID || m.Version != manifestVersion {
		t.Errorf("manifest = %+v, want id %q version %d", m, info.ID, manifestVersion)
	}
	if m.RunCount != 1 || m.TurnCount != 0 {
		t.Errorf("manifest counts = run %d turn %d, want 1/0", m.RunCount, m.TurnCount)
	}
	if m.Model != "test-model" || m.BaseURL != "https://example.invalid/v1" {
		t.Errorf("manifest metadata = %+v", m)
	}
}

func TestCreateRetriesOnCollision(t *testing.T) {
	top := t.TempDir()
	first := "brave-otter-runs"
	if err := os.Mkdir(filepath.Join(top, first), 0o700); err != nil {
		t.Fatal(err)
	}

	ids := []string{first, "calm-river-sings"}
	calls := 0
	info, err := create(top, Options{}, func() string {
		id := ids[calls]
		calls++
		return id
	})
	if err != nil {
		t.Fatalf("create returned error: %v", err)
	}
	if info.ID != "calm-river-sings" {
		t.Errorf("ID = %q, want calm-river-sings", info.ID)
	}
	if calls != 2 {
		t.Errorf("generator called %d times, want 2", calls)
	}
}

func TestCreateRejectsUnsafeSubdir(t *testing.T) {
	top := t.TempDir()
	_, err := create(top, Options{LogSubdir: "../escape"}, func() string {
		return "brave-otter-runs"
	})
	if err == nil {
		t.Fatal("expected an error for an escaping log subdir, got nil")
	}
	entries, readErr := os.ReadDir(top)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if len(entries) != 0 {
		t.Errorf("failed create left %d entries behind", len(entries))
	}
}

func TestLoadManifestRejectsMismatch(t *testing.T) {
	dir := t.TempDir()
	sessionDir := filepath.Join(dir, "brave-otter-runs")
	if err := os.Mkdir(sessionDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := writeManifest(filepath.Join(sessionDir, ManifestName), Manifest{
		Version: manifestVersion,
		ID:      "different-session-id",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadManifest(sessionDir); err == nil {
		t.Fatal("expected manifest/directory id mismatch to fail")
	}
}

func TestNewRunIDUnique(t *testing.T) {
	a := NewRunID()
	b := NewRunID()
	if a == "" || b == "" {
		t.Fatal("NewRunID returned an empty id")
	}
	if a == b {
		t.Errorf("NewRunID returned duplicate ids: %q", a)
	}
}
