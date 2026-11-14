package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"litegent/internal/llm"
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

func TestResumeExistingSession(t *testing.T) {
	top := t.TempDir()
	info, err := create(top, Options{}, func() string { return "brave-otter-runs" })
	if err != nil {
		t.Fatal(err)
	}
	if err := SaveContext(info, []llm.Message{{Role: llm.RoleSystem, Content: "system"}}); err != nil {
		t.Fatal(err)
	}
	if err := info.Close(); err != nil {
		t.Fatal(err)
	}

	resumed, err := Resume(top, info.ID, Options{})
	if err != nil {
		t.Fatalf("Resume returned error: %v", err)
	}
	defer resumed.Close()
	if resumed.ID != info.ID || resumed.Dir != info.Dir {
		t.Errorf("resumed session mismatch: %+v", resumed)
	}
	m, err := LoadManifest(resumed.Dir)
	if err != nil {
		t.Fatal(err)
	}
	if m.RunCount != 2 {
		t.Errorf("RunCount = %d, want 2", m.RunCount)
	}
}

func TestResumeMissingSession(t *testing.T) {
	if _, err := Resume(t.TempDir(), "brave-otter-runs", Options{}); err == nil {
		t.Fatal("expected missing session to fail")
	}
}

func TestResumeInvalidID(t *testing.T) {
	if _, err := Resume(t.TempDir(), "../escape", Options{}); err == nil {
		t.Fatal("expected invalid id to fail")
	}
}

func TestResumeRejectsSymlink(t *testing.T) {
	top := t.TempDir()
	target := filepath.Join(top, "target")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(top, "brave-otter-runs")); err != nil {
		t.Fatal(err)
	}
	if _, err := Resume(top, "brave-otter-runs", Options{}); err == nil {
		t.Fatal("expected symlinked session to fail")
	}
}

func TestResumeRefusesLockedSession(t *testing.T) {
	top := t.TempDir()
	info, err := create(top, Options{}, func() string { return "brave-otter-runs" })
	if err != nil {
		t.Fatal(err)
	}
	defer info.Close()
	if err := SaveContext(info, []llm.Message{{Role: llm.RoleSystem, Content: "system"}}); err != nil {
		t.Fatal(err)
	}

	if _, err := Resume(top, info.ID, Options{}); err == nil {
		t.Fatal("expected locked session to be refused")
	}
}

func TestResumeRequiresContext(t *testing.T) {
	top := t.TempDir()
	info, err := create(top, Options{}, func() string { return "brave-otter-runs" })
	if err != nil {
		t.Fatal(err)
	}
	if err := info.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := Resume(top, info.ID, Options{}); err == nil {
		t.Fatal("expected session without context to fail")
	}
}

func TestLatestID(t *testing.T) {
	top := t.TempDir()
	first, err := create(top, Options{}, func() string { return "brave-otter-runs" })
	if err != nil {
		t.Fatal(err)
	}
	if err := SaveContext(first, []llm.Message{{Role: llm.RoleSystem, Content: "system"}}); err != nil {
		t.Fatal(err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	second, err := create(top, Options{}, func() string { return "calm-river-sings" })
	if err != nil {
		t.Fatal(err)
	}
	if err := SaveContext(second, []llm.Message{{Role: llm.RoleSystem, Content: "system"}}); err != nil {
		t.Fatal(err)
	}
	if err := second.Close(); err != nil {
		t.Fatal(err)
	}

	if err := UpdateManifest(first, func(m *Manifest) {
		m.LastUsedAt = time.Now().UTC().Add(-time.Hour)
	}); err != nil {
		t.Fatal(err)
	}
	if err := UpdateManifest(second, func(m *Manifest) {
		m.LastUsedAt = time.Now().UTC()
	}); err != nil {
		t.Fatal(err)
	}

	id, err := LatestID(top)
	if err != nil {
		t.Fatalf("LatestID returned error: %v", err)
	}
	if id != "calm-river-sings" {
		t.Errorf("LatestID = %q, want calm-river-sings", id)
	}
}

func TestSetTurnCount(t *testing.T) {
	top := t.TempDir()
	info, err := create(top, Options{}, func() string { return "brave-otter-runs" })
	if err != nil {
		t.Fatal(err)
	}
	defer info.Close()
	if err := SetTurnCount(info, 7); err != nil {
		t.Fatal(err)
	}
	m, err := LoadManifest(info.Dir)
	if err != nil {
		t.Fatal(err)
	}
	if m.TurnCount != 7 {
		t.Errorf("TurnCount = %d, want 7", m.TurnCount)
	}
}

func TestFinishRunAndIncompleteDetection(t *testing.T) {
	top := t.TempDir()
	info, err := create(top, Options{}, func() string { return "brave-otter-runs" })
	if err != nil {
		t.Fatal(err)
	}
	if err := SaveContext(info, []llm.Message{{Role: llm.RoleSystem, Content: "system"}}); err != nil {
		t.Fatal(err)
	}
	// Leave the first run unfinished, then resume.
	if err := info.Close(); err != nil {
		t.Fatal(err)
	}
	resumed, err := Resume(top, info.ID, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if !resumed.PreviousRunUnfinished {
		t.Error("PreviousRunUnfinished = false, want true")
	}
	if err := FinishRun(resumed, "success", 0); err != nil {
		t.Fatal(err)
	}
	m, err := LoadManifest(resumed.Dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Runs) != 2 {
		t.Fatalf("expected 2 run records, got %d", len(m.Runs))
	}
	last := m.Runs[len(m.Runs)-1]
	if last.RunID != resumed.RunID || last.FinishedAt == nil || last.Outcome != "success" || last.ExitCode == nil || *last.ExitCode != 0 {
		t.Errorf("unexpected finished run record: %+v", last)
	}
	if err := resumed.Close(); err != nil {
		t.Fatal(err)
	}

	next, err := Resume(top, info.ID, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer next.Close()
	if next.PreviousRunUnfinished {
		t.Error("PreviousRunUnfinished = true, want false after a completed run")
	}
}
