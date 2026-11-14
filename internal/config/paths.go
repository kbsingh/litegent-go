package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ExpandPath expands a leading "~" to the current user's home directory
// and returns an absolute, cleaned path. It is used for top_dir and
// skills_dir, which may be supplied as either absolute or home-relative
// paths.
func ExpandPath(path string) (string, error) {
	if path == "" {
		return "", fmt.Errorf("path must not be empty")
	}
	if path == "~" || strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("determining home directory: %w", err)
		}
		if path == "~" {
			path = home
		} else {
			path = filepath.Join(home, strings.TrimPrefix(path, "~/"))
		}
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolving path %q: %w", path, err)
	}
	return filepath.Clean(abs), nil
}

// ValidateRelativePath rejects empty, absolute, home-relative, and
// escaping paths. Session-relative settings such as log_dir and spool_dir
// must stay inside the session directory.
func ValidateRelativePath(label, path string) error {
	if path == "" {
		return fmt.Errorf("%s must not be empty", label)
	}
	if filepath.IsAbs(path) {
		return fmt.Errorf("%s must be relative to the session directory, got %q", label, path)
	}
	if path == "~" || strings.HasPrefix(path, "~/") {
		return fmt.Errorf("%s must be relative to the session directory, got %q", label, path)
	}
	clean := filepath.Clean(path)
	if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return fmt.Errorf("%s must not escape the session directory, got %q", label, path)
	}
	return nil
}

// ResolveUnder joins a validated relative path to base. It is the single
// place callers should use when turning a session-relative setting into an
// on-disk path.
func ResolveUnder(base, rel string) (string, error) {
	if base == "" {
		return "", fmt.Errorf("base directory must not be empty")
	}
	if err := ValidateRelativePath("relative path", rel); err != nil {
		return "", err
	}
	return filepath.Join(base, filepath.Clean(rel)), nil
}
