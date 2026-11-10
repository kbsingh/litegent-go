// Package skills discovers Markdown skill files and assembles them into
// an <available_skills> XML catalog injected into the system prompt.
// See req/spec_doc.md section 2.6.
package skills

import (
	"bufio"
	"bytes"
	"fmt"
	"html"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// reservedSkillTag matches the XML wrapper tags that BuildSkillsPrompt
// itself emits. Skill file content is embedded verbatim inside
// <available_skills>, so allowing these sequences through would let a
// single file close (or reopen) the catalog and corrupt every skill that
// follows. The pattern is case-insensitive and tolerant of whitespace
// between "<", "/", and the tag name.
var reservedSkillTag = regexp.MustCompile(`(?i)<\s*/?\s*(?:available_skills|skill)\b`)

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
		if err := validateSkillContent(path, content); err != nil {
			return err
		}
		title := extractTitle(content, d.Name())
		fmt.Fprintf(&sb, "  <skill name=\"%s\" file=\"%s\">\n%s\n  </skill>\n",
			xmlEscapeAttr(title), xmlEscapeAttr(path), string(content))
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

// validateSkillContent rejects any skill file that contains one of the
// catalog's own wrapper tags. This is the guard that keeps a stray or
// malicious "</skill>" (or "<available_skills>") in a Markdown file from
// prematurely closing the generated XML block and corrupting the prompt.
// Ordinary angle-bracket text such as "<dir>" is allowed: it is inert
// placeholder text, not a catalog wrapper tag.
func validateSkillContent(path string, content []byte) error {
	for i, line := range strings.Split(string(content), "\n") {
		if loc := reservedSkillTag.FindStringIndex(line); loc != nil {
			return fmt.Errorf(
				"skill file %q line %d: reserved XML tag %q is not allowed in skill content",
				path, i+1, line[loc[0]:loc[1]],
			)
		}
	}
	return nil
}

// xmlEscapeAttr escapes a value for safe inclusion inside a double-quoted
// XML attribute. Skill titles and paths come from the filesystem, so a
// quote or ampersand in either must not be able to terminate the
// attribute early and break the catalog structure.
func xmlEscapeAttr(s string) string {
	return html.EscapeString(s)
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
