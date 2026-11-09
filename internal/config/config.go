// Package config loads and validates the Litegent JSON configuration
// file, applying documented defaults and environment variable
// interpolation. See req/spec_doc.md section 2.2.
package config

import (
	"encoding/json"
	"fmt"
	"os"
	"regexp"
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
	MaxContextTokens   int    `json:"max_context_tokens"`
	MaxToolOutputChars int    `json:"max_tool_output_chars"`
	WindowSize         int    `json:"window_size"`
	SpoolDir           string `json:"spool_dir"`
}

// LitegentConfig is the full configuration schema accepted via -c/--config.
type LitegentConfig struct {
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
	Tools                ToolsConfig   `json:"tools"`
}

// Default returns the documented default configuration. Load starts from
// this value and unmarshals the user-supplied file on top of it, so
// omitted keys retain sane defaults instead of zero-valuing to false/0/"".
func Default() LitegentConfig {
	return LitegentConfig{
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
			MaxContextTokens:   16000,
			MaxToolOutputChars: 4000,
			WindowSize:         20,
			SpoolDir:           ".tmp/tool_outputs",
		},
		LogDir: "./logs",
		Tools: ToolsConfig{
			Bash:    ToolBashConfig{Enabled: true, Timeout: 60.0, DefaultWorkdir: "."},
			FileOps: ToolFileOpsConfig{Enabled: true, MaxReadLines: 2000},
			Search:  ToolSearchConfig{Enabled: true, MaxSearchResults: 100},
		},
	}
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
// variable references, and validates the result.
func Load(path string) (LitegentConfig, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return LitegentConfig{}, fmt.Errorf("reading config %q: %w", path, err)
	}

	// Start from the documented defaults, then unmarshal the user's JSON
	// on top so omitted keys keep their defaults instead of zeroing out.
	cfg := Default()
	if err := json.Unmarshal([]byte(interpolateEnv(string(raw))), &cfg); err != nil {
		return LitegentConfig{}, fmt.Errorf("parsing config %q: %w", path, err)
	}

	if err := cfg.Validate(); err != nil {
		return LitegentConfig{}, fmt.Errorf("invalid config %q: %w", path, err)
	}

	return cfg, nil
}

// Validate checks that required fields are present and sane. It never
// attempts to validate that paths live under any particular directory —
// Litegent is intentionally unconfined (req/requirements_doc.md NFR-3.4).
func (c LitegentConfig) Validate() error {
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
	if c.ManageContext {
		if c.ContextConfig.MaxContextTokens <= 0 {
			return fmt.Errorf("context_config.max_context_tokens must be positive, got %d", c.ContextConfig.MaxContextTokens)
		}
		if c.ContextConfig.MaxToolOutputChars <= 0 {
			return fmt.Errorf("context_config.max_tool_output_chars must be positive, got %d", c.ContextConfig.MaxToolOutputChars)
		}
		if c.ContextConfig.WindowSize <= 0 {
			return fmt.Errorf("context_config.window_size must be positive, got %d", c.ContextConfig.WindowSize)
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
