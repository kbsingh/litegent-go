package skills

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBuildSkillsPrompt_DiscoversNestedFiles(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "git.md"), []byte("# Git Workflow\n\nAlways run git status first.\n"), 0o644)
	nested := filepath.Join(dir, "linux")
	os.MkdirAll(nested, 0o755)
	os.WriteFile(filepath.Join(nested, "diagnostics.md"), []byte("# Linux Diagnostics\n\nCheck CPU and memory.\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("not a skill"), 0o644)

	prompt, err := BuildSkillsPrompt(dir)
	if err != nil {
		t.Fatalf("BuildSkillsPrompt returned error: %v", err)
	}
	if !strings.HasPrefix(prompt, "<available_skills>") || !strings.HasSuffix(prompt, "</available_skills>") {
		t.Fatalf("expected XML wrapper, got: %q", prompt)
	}
	if !strings.Contains(prompt, `name="Git Workflow"`) {
		t.Errorf("expected Git Workflow skill title, got: %q", prompt)
	}
	if !strings.Contains(prompt, `name="Linux Diagnostics"`) {
		t.Errorf("expected nested Linux Diagnostics skill to be discovered, got: %q", prompt)
	}
	if strings.Contains(prompt, "not a skill") {
		t.Errorf("expected .txt file to be ignored, got: %q", prompt)
	}
}

func TestBuildSkillsPrompt_TitleFallsBackToFilename(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "untitled.md"), []byte("no heading here"), 0o644)

	prompt, err := BuildSkillsPrompt(dir)
	if err != nil {
		t.Fatalf("BuildSkillsPrompt returned error: %v", err)
	}
	if !strings.Contains(prompt, `name="untitled.md"`) {
		t.Errorf("expected filename fallback title, got: %q", prompt)
	}
}

func TestBuildSkillsPrompt_EmptyDirectory(t *testing.T) {
	dir := t.TempDir()
	prompt, err := BuildSkillsPrompt(dir)
	if err != nil {
		t.Fatalf("BuildSkillsPrompt returned error: %v", err)
	}
	if prompt != "" {
		t.Errorf("expected empty prompt for a directory with no skills, got: %q", prompt)
	}
}

func TestBuildSkillsPrompt_MissingDirectory(t *testing.T) {
	_, err := BuildSkillsPrompt("/nonexistent/skills/dir")
	if err == nil {
		t.Fatal("expected an error for a missing skills directory")
	}
}

// TestBuildSkillsPrompt_ExampleSkillsDir guards against the shipped
// examples/skills/ directory drifting out of sync with the loader.
func TestBuildSkillsPrompt_ExampleSkillsDir(t *testing.T) {
	prompt, err := BuildSkillsPrompt("../../examples/skills")
	if err != nil {
		t.Fatalf("BuildSkillsPrompt returned error: %v", err)
	}
	if !strings.Contains(prompt, "Linux Diagnostics") || !strings.Contains(prompt, "Git Workflow Conventions") {
		t.Errorf("expected both example skills to be discovered, got: %q", prompt)
	}
}

// TestBuildSkillsPrompt_RejectsReservedWrapperTags ensures a skill cannot
// close or reopen the generated catalog from inside its Markdown content.
func TestBuildSkillsPrompt_RejectsReservedWrapperTags(t *testing.T) {
	cases := []string{
		"</skill>",
		"<available_skills>",
		"</AVAILABLE_SKILLS >",
		"< skill >",
	}
	for _, content := range cases {
		t.Run(content, func(t *testing.T) {
			dir := t.TempDir()
			os.WriteFile(filepath.Join(dir, "malicious.md"), []byte("# Bad\n\n"+content+"\n"), 0o644)

			_, err := BuildSkillsPrompt(dir)
			if err == nil {
				t.Fatalf("expected an error for reserved tag %q, got nil", content)
			}
			if !strings.Contains(err.Error(), "reserved XML tag") {
				t.Errorf("expected a reserved-tag error, got: %v", err)
			}
		})
	}
}

// TestBuildSkillsPrompt_AllowsAngleBracketText verifies that ordinary
// placeholder text (a single, non-wrapper angle-bracket pair) still loads.
func TestBuildSkillsPrompt_AllowsAngleBracketText(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "disk.md"), []byte("# Disk\n\nRun `du -sh <dir>` to inspect a directory.\n"), 0o644)

	prompt, err := BuildSkillsPrompt(dir)
	if err != nil {
		t.Fatalf("BuildSkillsPrompt returned error: %v", err)
	}
	if !strings.Contains(prompt, "<dir>") {
		t.Errorf("expected angle-bracket placeholder to be preserved, got: %q", prompt)
	}
}

// TestBuildSkillsPrompt_EscapesAttributeValues ensures titles cannot
// terminate the name attribute early with a quote or ampersand.
func TestBuildSkillsPrompt_EscapesAttributeValues(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "quoted.md"), []byte("# A \"quoted\" & <tagged> title\n\nbody\n"), 0o644)

	prompt, err := BuildSkillsPrompt(dir)
	if err != nil {
		t.Fatalf("BuildSkillsPrompt returned error: %v", err)
	}
	want := `name="A &#34;quoted&#34; &amp; &lt;tagged&gt; title"`
	if !strings.Contains(prompt, want) {
		t.Errorf("expected escaped attribute %q, got: %q", want, prompt)
	}
}

func TestBuildSkillsPrompt_NotADirectory(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "file.md")
	os.WriteFile(file, []byte("# X"), 0o644)

	_, err := BuildSkillsPrompt(file)
	if err == nil {
		t.Fatal("expected an error when skills path is a file, not a directory")
	}
}
