package config

import (
	"os"
	"path/filepath"
	"testing"
)

func writeTemp(t *testing.T, name, content string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("writing temp file: %v", err)
	}
	return path
}

func TestLoad_YAML_AppliesDefaults(t *testing.T) {
	path := writeTemp(t, "config.yaml", `
model: "gpt-4o-mini"
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if cfg.Model != "gpt-4o-mini" {
		t.Errorf("Model = %q, want gpt-4o-mini", cfg.Model)
	}
	if cfg.BaseURL != "https://api.openai.com/v1" {
		t.Errorf("BaseURL default not applied, got %q", cfg.BaseURL)
	}
	if cfg.MaxSteps != 30 {
		t.Errorf("MaxSteps default not applied, got %d", cfg.MaxSteps)
	}
	if cfg.MaxRepeatedToolCalls != 3 {
		t.Errorf("MaxRepeatedToolCalls default not applied, got %d", cfg.MaxRepeatedToolCalls)
	}
	if cfg.ContextConfig.SpoolDir != ".tmp/tool_outputs" {
		t.Errorf("SpoolDir default not applied, got %q", cfg.ContextConfig.SpoolDir)
	}
}

func TestLoad_JSON(t *testing.T) {
	path := writeTemp(t, "config.json", `{"model": "gpt-4o", "max_steps": 10}`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if cfg.Model != "gpt-4o" {
		t.Errorf("Model = %q, want gpt-4o", cfg.Model)
	}
	if cfg.MaxSteps != 10 {
		t.Errorf("MaxSteps = %d, want 10", cfg.MaxSteps)
	}
}

func TestLoad_MissingFile(t *testing.T) {
	_, err := Load("/nonexistent/path/does-not-exist.yaml")
	if err == nil {
		t.Fatal("expected an error for a missing config file, got nil")
	}
}

func TestLoad_EnvVarInterpolation(t *testing.T) {
	t.Setenv("TEST_LITEGENT_API_KEY", "sk-secret-value")
	path := writeTemp(t, "config.yaml", `
api_key: "${TEST_LITEGENT_API_KEY}"
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if cfg.APIKey != "sk-secret-value" {
		t.Errorf("APIKey = %q, want sk-secret-value", cfg.APIKey)
	}
}

func TestLoad_EnvVarFallback(t *testing.T) {
	os.Unsetenv("TEST_LITEGENT_UNSET_VAR")
	path := writeTemp(t, "config.yaml", `
model: "${TEST_LITEGENT_UNSET_VAR:-fallback-model}"
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if cfg.Model != "fallback-model" {
		t.Errorf("Model = %q, want fallback-model", cfg.Model)
	}
}

func TestLoad_InvalidConfig_NegativeMaxSteps(t *testing.T) {
	path := writeTemp(t, "config.yaml", `
max_steps: -1
`)
	if _, err := Load(path); err == nil {
		t.Fatal("expected validation error for negative max_steps, got nil")
	}
}

func TestLoad_InvalidYAML(t *testing.T) {
	path := writeTemp(t, "config.yaml", "not: valid: yaml: [")
	if _, err := Load(path); err == nil {
		t.Fatal("expected a parse error for invalid YAML, got nil")
	}
}

func TestLoad_ThinkingDefaults(t *testing.T) {
	path := writeTemp(t, "config.yaml", `model: "gpt-4o"`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if cfg.ThinkingLevel != "default" {
		t.Errorf("ThinkingLevel default = %q, want \"default\"", cfg.ThinkingLevel)
	}
	if cfg.ThinkingFormat != ThinkingFormatReasoningEffort {
		t.Errorf("ThinkingFormat default = %q, want %q", cfg.ThinkingFormat, ThinkingFormatReasoningEffort)
	}
	if cfg.ThinkingActive() {
		t.Error("default thinking_level must be inactive (send nothing)")
	}
}

func TestLoad_ThinkingLevelFreeText(t *testing.T) {
	path := writeTemp(t, "config.yaml", `
thinking_level: "High Effort!"
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("free-text thinking_level must load without validation, got: %v", err)
	}
	if cfg.ThinkingLevel != "High Effort!" {
		t.Errorf("ThinkingLevel = %q, want it preserved verbatim", cfg.ThinkingLevel)
	}
	if !cfg.ThinkingActive() {
		t.Error("explicit non-default thinking_level must be active")
	}
}

func TestLoad_ThinkingExplicitDefaultInactive(t *testing.T) {
	path := writeTemp(t, "config.yaml", `
thinking_level: "default"
thinking_format: "thinking"
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if cfg.ThinkingActive() {
		t.Error("explicit \"default\" thinking_level must be inactive (send nothing)")
	}
}

func TestLoad_ThinkingFormatValid(t *testing.T) {
	path := writeTemp(t, "config.yaml", `
thinking_level: "high"
thinking_format: "thinking"
`)
	if _, err := Load(path); err != nil {
		t.Fatalf("thinking format \"thinking\" must validate, got: %v", err)
	}
}

func TestLoad_ThinkingFormatInvalid(t *testing.T) {
	path := writeTemp(t, "config.yaml", `
thinking_level: "high"
thinking_format: "bogus"
`)
	if _, err := Load(path); err == nil {
		t.Fatal("expected validation error for unknown thinking_format, got nil")
	}
}

func TestLoad_ThinkingFormatNotValidatedWhenInactive(t *testing.T) {
	path := writeTemp(t, "config.yaml", `
thinking_level: "default"
thinking_format: "bogus"
`)
	if _, err := Load(path); err != nil {
		t.Fatalf("thinking_format must be ignored when the level is inactive, got: %v", err)
	}
}

func TestMaskedAPIKey(t *testing.T) {
	cfg := LitegentConfig{APIKey: "sk-abcdefgh1234wxyz"}
	got := cfg.MaskedAPIKey()
	want := "sk-...wxyz"
	if got != want {
		t.Errorf("MaskedAPIKey() = %q, want %q", got, want)
	}
}

func TestMaskedAPIKey_R9Prefix(t *testing.T) {
	cfg := LitegentConfig{APIKey: "r9_abcdefgh1234wxyz"}
	got := cfg.MaskedAPIKey()
	want := "r9_...wxyz"
	if got != want {
		t.Errorf("MaskedAPIKey() = %q, want %q", got, want)
	}
}

func TestMaskedAPIKey_Boundary(t *testing.T) {
	// 7 characters: a 3-char prefix plus 4-char suffix would reveal the
	// entire key, so it must be fully masked. 8 is the shortest key that
	// can safely show prefix and suffix.
	if got := (LitegentConfig{APIKey: "1234567"}).MaskedAPIKey(); got != "****" {
		t.Errorf("7-char key masked as %q, want ****", got)
	}
	if got := (LitegentConfig{APIKey: "12345678"}).MaskedAPIKey(); got != "123...5678" {
		t.Errorf("8-char key masked as %q, want 123...5678", got)
	}
}

func TestMaskedAPIKey_Short(t *testing.T) {
	cfg := LitegentConfig{APIKey: "abc"}
	if got := cfg.MaskedAPIKey(); got != "****" {
		t.Errorf("MaskedAPIKey() = %q, want ****", got)
	}
}

// TestLoad_ExampleConfigs guards against the shipped examples/config.yaml
// and examples/config.json drifting out of sync with the schema.
func TestLoad_ExampleConfigs(t *testing.T) {
	for _, path := range []string{"../../examples/config.yaml", "../../examples/config.json"} {
		t.Run(path, func(t *testing.T) {
			t.Setenv("OPENAI_API_KEY", "sk-test-value-for-example-config")
			cfg, err := Load(path)
			if err != nil {
				t.Fatalf("Load(%q) returned error: %v", path, err)
			}
			if cfg.Model == "" {
				t.Error("expected a non-empty model in the example config")
			}
			if cfg.MaxRepeatedToolCalls != 3 {
				t.Errorf("MaxRepeatedToolCalls = %d, want 3", cfg.MaxRepeatedToolCalls)
			}
		})
	}
}
