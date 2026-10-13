// Package config loads and validates the Litegent configuration file
// (JSON or YAML), applying documented defaults and environment variable
// interpolation. See req/spec_doc.md section 2.2.
package config

import (
	"fmt"
	"os"
	"regexp"

	"gopkg.in/yaml.v3"
)

// ToolBashConfig controls the bash tool.
type ToolBashConfig struct {
	Enabled        bool    `yaml:"enabled" json:"enabled"`
	Timeout        float64 `yaml:"timeout" json:"timeout"`
	DefaultWorkdir string  `yaml:"default_workdir" json:"default_workdir"`
}

// ToolFileOpsConfig controls read_file/write_file/edit_file.
type ToolFileOpsConfig struct {
	Enabled      bool `yaml:"enabled" json:"enabled"`
	MaxReadLines int  `yaml:"max_read_lines" json:"max_read_lines"`
}

// ToolSearchConfig controls glob_search/grep_search.
type ToolSearchConfig struct {
	Enabled          bool `yaml:"enabled" json:"enabled"`
	MaxSearchResults int  `yaml:"max_search_results" json:"max_search_results"`
}

// ToolsConfig aggregates per-tool configuration.
type ToolsConfig struct {
	Bash    ToolBashConfig    `yaml:"bash" json:"bash"`
	FileOps ToolFileOpsConfig `yaml:"file_ops" json:"file_ops"`
	Search  ToolSearchConfig  `yaml:"search" json:"search"`
}

// ContextConfig controls the context management engine.
type ContextConfig struct {
	MaxContextTokens   int    `yaml:"max_context_tokens" json:"max_context_tokens"`
	MaxToolOutputChars int    `yaml:"max_tool_output_chars" json:"max_tool_output_chars"`
	WindowSize         int    `yaml:"window_size" json:"window_size"`
	SpoolDir           string `yaml:"spool_dir" json:"spool_dir"`
}

// LitegentConfig is the full configuration schema accepted via -c/--config.
type LitegentConfig struct {
	BaseURL              string        `yaml:"base_url" json:"base_url"`
	APIKey               string        `yaml:"api_key" json:"api_key"`
	Model                string        `yaml:"model" json:"model"`
	Temperature          float64       `yaml:"temperature" json:"temperature"`
	MaxTokens            int           `yaml:"max_tokens" json:"max_tokens"`
	Timeout              float64       `yaml:"timeout" json:"timeout"`
	MaxSteps             int           `yaml:"max_steps" json:"max_steps"`
	MaxRepeatedToolCalls int           `yaml:"max_repeated_tool_calls" json:"max_repeated_tool_calls"`
	ManageContext        bool          `yaml:"manage_context" json:"manage_context"`
	ContextConfig        ContextConfig `yaml:"context_config" json:"context_config"`
	LogDir               string        `yaml:"log_dir" json:"log_dir"`
	LogLevel             string        `yaml:"log_level" json:"log_level"`
	Tools                ToolsConfig   `yaml:"tools" json:"tools"`
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
		LogDir:   "./logs",
		LogLevel: "INFO",
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

// Load reads the config file at path (JSON or YAML — JSON is valid YAML,
// so a single yaml.Unmarshal call handles both formats), interpolates
// environment variable references, and validates the result.
func Load(path string) (LitegentConfig, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return LitegentConfig{}, fmt.Errorf("reading config %q: %w", path, err)
	}

	// Valid JSON is also valid YAML, so this single yaml.Unmarshal call
	// transparently handles both configuration formats accepted by
	// -c/--config.
	cfg := Default()
	if err := yaml.Unmarshal([]byte(interpolateEnv(string(raw))), &cfg); err != nil {
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
	return nil
}

// MaskedAPIKey returns the API key masked for safe display in logs, e.g.
// "sk-...wxyz". Short or empty keys are masked entirely.
func (c LitegentConfig) MaskedAPIKey() string {
	const visible = 4
	if len(c.APIKey) <= visible {
		return "****"
	}
	return "sk-..." + c.APIKey[len(c.APIKey)-visible:]
}
