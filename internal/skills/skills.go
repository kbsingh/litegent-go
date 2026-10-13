// Package skills discovers Markdown skill files and assembles them into
// an <available_skills> XML catalog injected into the system prompt.
// See req/spec_doc.md section 2.6.
package skills

import (
	"bufio"
	"bytes"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// BuildSkillsPrompt recursively discovers all *.md files under skillsDir
// and assembles them into an <available_skills> XML block. Litegent is
// unconfined (req/requirements_doc.md NFR-3.4): skillsDir may be any
// path on the host.
func BuildSkillsPrompt(skillsDir string) (string, error) {
	info, err := os.Stat(skillsDir)
	if err != nil {
		return "", fmt.Errorf("skills directory %q: %w", skillsDir, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("skills path %q is not a directory", skillsDir)
	}

	var sb strings.Builder
	sb.WriteString("<available_skills>\n")

	count := 0
	err = filepath.WalkDir(skillsDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(strings.ToLower(d.Name()), ".md") {
			return nil
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("reading skill file %q: %w", path, err)
		}
		title := extractTitle(content, d.Name())
		fmt.Fprintf(&sb, "  <skill name=%q file=%q>\n%s\n  </skill>\n", title, path, string(content))
		count++
		return nil
	})
	if err != nil {
		return "", err
	}

	sb.WriteString("</available_skills>")

	if count == 0 {
		return "", nil
	}
	return sb.String(), nil
}

// extractTitle returns the text of the first level-1 Markdown heading
// ("# Title") in content, or fallback (typically the filename) if none
// is present.
func extractTitle(content []byte, fallback string) string {
	scanner := bufio.NewScanner(bytes.NewReader(content))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if strings.HasPrefix(line, "# ") {
			return strings.TrimSpace(strings.TrimPrefix(line, "#"))
		}
	}
	return fallback
}
