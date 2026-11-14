// Package config loads and validates the Litegent JSON configuration
// file, applying documented defaults and environment variable
// interpolation. See req/spec_doc.md section 2.2.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// ToolBashConfig controls the bash tool.
type ToolBashConfig struct {
	Enabled        bool    `json:"enabled"`
	Timeout        float64 `json:"timeout"`
	DefaultWorkdir string  `json:"default_workdir"`
}

// ToolFileOpsConfig controls read_file/write_file/edit_file.
type ToolFileOpsConfig struct {
	Enabled      bool `json:"enabled"`
	MaxReadLines int  `json:"max_read_lines"`
}

// ToolSearchConfig controls glob_search/grep_search.
type ToolSearchConfig struct {
	Enabled          bool `json:"enabled"`
	MaxSearchResults int  `json:"max_search_results"`
}

// Thinking wire formats selectable via thinking_format when
// thinking_level is active. The level value itself is free text passed
// through to the endpoint verbatim — different models accept different
// values (e.g. "low"/"medium"/"high" for reasoning_effort,
// "enabled"/"disabled" for thinking).
const (
	ThinkingFormatReasoningEffort = "reasoning_effort"
	ThinkingFormatThinking        = "thinking"
)

// ToolsConfig aggregates per-tool configuration.
type ToolsConfig struct {
	Bash    ToolBashConfig    `json:"bash"`
	FileOps ToolFileOpsConfig `json:"file_ops"`
	Search  ToolSearchConfig  `json:"search"`
}

// ContextConfig controls the context management engine.
type ContextConfig struct {
	MaxContextTokens    int    `json:"max_context_tokens"`
	TruncateToolOutputs bool   `json:"truncate_tool_outputs"`
	MaxToolOutputChars  int    `json:"max_tool_output_chars"`
	WindowSize          int    `json:"window_size"`
	SpoolDir            string `json:"spool_dir"`
}

// LitegentConfig is the full configuration schema accepted via -c/--config.
type LitegentConfig struct {
	TopDir               string        `json:"top_dir"`
	BaseURL              string        `json:"base_url"`
	APIKey               string        `json:"api_key"`
	Model                string        `json:"model"`
	Temperature          float64       `json:"temperature"`
	MaxTokens            int           `json:"max_tokens"`
	ThinkingLevel        string        `json:"thinking_level"`
	ThinkingFormat       string        `json:"thinking_format"`
	Timeout              float64       `json:"timeout"`
	MaxSteps             int           `json:"max_steps"`
	MaxRepeatedToolCalls int           `json:"max_repeated_tool_calls"`
	ManageContext        bool          `json:"manage_context"`
	ContextConfig        ContextConfig `json:"context_config"`
	LogDir               string        `json:"log_dir"`
	SkillsDir            string        `json:"skills_dir"`
	Tools                ToolsConfig   `json:"tools"`
}

// Default returns the documented default configuration. Load starts from
// this value and unmarshals the user-supplied file on top of it, so
// omitted keys retain sane defaults instead of zero-valuing to false/0/"".
func Default() LitegentConfig {
	return LitegentConfig{
		// TopDir is a bootstrap default. Load replaces it with the
		// directory containing the config file when top_dir is omitted.
		TopDir:               "~/.litegent",
		BaseURL:              "https://api.openai.com/v1",
		APIKey:               os.Getenv("OPENAI_API_KEY"),
		Model:                "gpt-4o",
		Temperature:          0.0,
		MaxTokens:            4096,
		ThinkingLevel:        "default",
		ThinkingFormat:       ThinkingFormatReasoningEffort,
		Timeout:              120.0,
		MaxSteps:             30,
		MaxRepeatedToolCalls: 3,
		ManageContext:        true,
		ContextConfig: ContextConfig{
			MaxContextTokens:    32000,
			TruncateToolOutputs: true,
			MaxToolOutputChars:  4000,
			WindowSize:          40,
			SpoolDir:            "spool",
		},
		LogDir:    "logs",
		SkillsDir: "skills",
		Tools: ToolsConfig{
			Bash:    ToolBashConfig{Enabled: true, Timeout: 60.0, DefaultWorkdir: "."},
			FileOps: ToolFileOpsConfig{Enabled: true, MaxReadLines: 2000},
			Search:  ToolSearchConfig{Enabled: true, MaxSearchResults: 100},
		},
	}
}

// DefaultJSON renders the documented default configuration as indented
// JSON suitable for writing to disk. The API key is replaced with an
// environment placeholder so a generated file never embeds the caller's
// real credential.
func DefaultJSON() ([]byte, error) {
	cfg := Default()
	cfg.APIKey = "${OPENAI_API_KEY}"

	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshaling default config: %w", err)
	}
	return append(data, '\n'), nil
}

// EnsureDefaultConfig creates path (and its parent directory) with the
// documented example configuration when path does not already exist. It
// returns created=true only when this call wrote the file; an existing
// file is never overwritten. Creation uses O_EXCL so two concurrent
// processes cannot clobber each other.
func EnsureDefaultConfig(path string) (created bool, err error) {
	if _, statErr := os.Stat(path); statErr == nil {
		return false, nil
	} else if !errors.Is(statErr, fs.ErrNotExist) {
		return false, fmt.Errorf("checking config %q: %w", path, statErr)
	}

	if dir := filepath.Dir(path); dir != "." && dir != "" {
		if mkErr := os.MkdirAll(dir, 0o755); mkErr != nil {
			return false, fmt.Errorf("creating config directory %q: %w", dir, mkErr)
		}
	}

	data, err := DefaultJSON()
	if err != nil {
		return false, err
	}

	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		if errors.Is(err, fs.ErrExist) {
			return false, nil // another process created it first
		}
		return false, fmt.Errorf("creating default config %q: %w", path, err)
	}

	if _, err := f.Write(data); err != nil {
		f.Close()
		os.Remove(path)
		return false, fmt.Errorf("writing default config %q: %w", path, err)
	}
	if err := f.Close(); err != nil {
		os.Remove(path)
		return false, fmt.Errorf("closing default config %q: %w", path, err)
	}
	return true, nil
}

// envPattern matches ${VAR} and ${VAR:-default} references.
var envPattern = regexp.MustCompile(`\$\{([A-Za-z0-9_]+)(?::-([^}]*))?\}`)

// interpolateEnv substitutes ${VAR} / ${VAR:-default} references in raw
// config file text with the corresponding environment variable value (or
// the fallback when the variable is unset), so secrets never need to be
// stored in cleartext on disk.
func interpolateEnv(raw string) string {
	return envPattern.ReplaceAllStringFunc(raw, func(match string) string {
		groups := envPattern.FindStringSubmatch(match)
		name, fallback := groups[1], groups[2]
		if v, ok := os.LookupEnv(name); ok {
			return v
		}
		return fallback
	})
}

// Load reads the JSON config file at path, interpolates environment
// variable references, resolves top_dir, and validates the result.
//
// top_dir defaults to the directory containing the config file. For the
// default bootstrap config at ~/.litegent/config.json this preserves the
// historical behavior of keeping config.json and system.md together.
func Load(path string) (LitegentConfig, error) {
	absPath, err := filepath.Abs(path)
	if err != nil {
		return LitegentConfig{}, fmt.Errorf("resolving config path %q: %w", path, err)
	}
	raw, err := os.ReadFile(absPath)
	if err != nil {
		return LitegentConfig{}, fmt.Errorf("reading config %q: %w", path, err)
	}
	data := []byte(interpolateEnv(string(raw)))

	// Start from the documented defaults, then unmarshal the user's JSON
	// on top so omitted keys keep their defaults instead of zeroing out.
	cfg := Default()
	if err := json.Unmarshal(data, &cfg); err != nil {
		return LitegentConfig{}, fmt.Errorf("parsing config %q: %w", path, err)
	}

	// Distinguish an omitted top_dir from an explicitly supplied value.
	// Omitted means "use the config file's directory"; explicit (including
	// an empty string) is validated below.
	var presence struct {
		TopDir *string `json:"top_dir"`
	}
	if err := json.Unmarshal(data, &presence); err != nil {
		return LitegentConfig{}, fmt.Errorf("parsing config %q: %w", path, err)
	}
	if presence.TopDir == nil {
		cfg.TopDir = filepath.Dir(absPath)
	}

	if err := cfg.resolvePaths(); err != nil {
		return LitegentConfig{}, fmt.Errorf("invalid config %q: %w", path, err)
	}
	if err := cfg.Validate(); err != nil {
		return LitegentConfig{}, fmt.Errorf("invalid config %q: %w", path, err)
	}

	return cfg, nil
}

// resolvePaths expands top_dir and resolves skills_dir against it.
// log_dir and spool_dir intentionally remain session-relative; they are
// joined to the session directory at runtime.
func (c *LitegentConfig) resolvePaths() error {
	topDir, err := ExpandPath(c.TopDir)
	if err != nil {
		return fmt.Errorf("resolving top_dir: %w", err)
	}
	c.TopDir = topDir

	if c.SkillsDir == "" {
		return nil
	}
	if filepath.IsAbs(c.SkillsDir) || c.SkillsDir == "~" || strings.HasPrefix(c.SkillsDir, "~/") {
		c.SkillsDir, err = ExpandPath(c.SkillsDir)
	} else {
		c.SkillsDir, err = ResolveUnder(c.TopDir, c.SkillsDir)
	}
	if err != nil {
		return fmt.Errorf("resolving skills_dir: %w", err)
	}
	return nil
}

// Validate checks that required fields are present and sane. Path-related
// checks are limited to the values that must remain inside the session
// directory; Litegent itself is intentionally unconfined.
func (c LitegentConfig) Validate() error {
	if c.TopDir == "" {
		return fmt.Errorf("top_dir must not be empty")
	}
	if !filepath.IsAbs(c.TopDir) {
		return fmt.Errorf("top_dir must be absolute after resolution, got %q", c.TopDir)
	}
	if c.BaseURL == "" {
		return fmt.Errorf("base_url must not be empty")
	}
	if c.Model == "" {
		return fmt.Errorf("model must not be empty")
	}
	if c.MaxSteps <= 0 {
		return fmt.Errorf("max_steps must be positive, got %d", c.MaxSteps)
	}
	if c.MaxRepeatedToolCalls < 0 {
		return fmt.Errorf("max_repeated_tool_calls must not be negative, got %d", c.MaxRepeatedToolCalls)
	}
	if err := ValidateRelativePath("log_dir", c.LogDir); err != nil {
		return err
	}
	if c.SkillsDir != "" && !filepath.IsAbs(c.SkillsDir) {
		return fmt.Errorf("skills_dir must be absolute after resolution, got %q", c.SkillsDir)
	}
	if c.ManageContext {
		if c.ContextConfig.MaxContextTokens <= 0 {
			return fmt.Errorf("context_config.max_context_tokens must be positive, got %d", c.ContextConfig.MaxContextTokens)
		}
		if c.ContextConfig.WindowSize <= 0 {
			return fmt.Errorf("context_config.window_size must be positive, got %d", c.ContextConfig.WindowSize)
		}
		if c.ContextConfig.TruncateToolOutputs {
			if c.ContextConfig.MaxToolOutputChars <= 0 {
				return fmt.Errorf("context_config.max_tool_output_chars must be positive, got %d", c.ContextConfig.MaxToolOutputChars)
			}
			if err := ValidateRelativePath("context_config.spool_dir", c.ContextConfig.SpoolDir); err != nil {
				return err
			}
		} else if c.ContextConfig.SpoolDir != "" {
			if err := ValidateRelativePath("context_config.spool_dir", c.ContextConfig.SpoolDir); err != nil {
				return err
			}
		}
	}
	if c.ThinkingActive() {
		switch c.ThinkingFormat {
		case "", ThinkingFormatReasoningEffort, ThinkingFormatThinking:
		default:
			return fmt.Errorf("thinking_format must be %q or %q when thinking_level is set, got %q",
				ThinkingFormatReasoningEffort, ThinkingFormatThinking, c.ThinkingFormat)
		}
	}
	return nil
}

// ThinkingActive reports whether thinking_level carries an explicit
// value that must be passed through to the endpoint. "default" (and the
// empty string) mean "send nothing and let the model decide".
func (c LitegentConfig) ThinkingActive() bool {
	return c.ThinkingLevel != "" && c.ThinkingLevel != "default"
}

// MaskedAPIKey returns the API key masked for safe display, preserving
// the key's own stub prefix, e.g. "sk-...wxyz" or "r9_...wxyz". Keys of
// 7 characters or fewer are masked entirely: revealing a 3-character
// prefix and 4-character suffix would expose the whole key.
func (c LitegentConfig) MaskedAPIKey() string {
	if len(c.APIKey) <= 7 {
		return "****"
	}
	return c.APIKey[:3] + "..." + c.APIKey[len(c.APIKey)-4:]
}
