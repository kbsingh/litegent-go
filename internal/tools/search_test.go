package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func setupSearchTree(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	mustWrite := func(rel, content string) {
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mustWrite("main.go", "package main\n\nfunc main() {}\n")
	mustWrite("pkg/util.go", "package pkg\n\n// TODO: refactor\nfunc Util() {}\n")
	mustWrite("README.md", "# Title\nTODO: write docs\n")
	return dir
}

func TestGlobSearchTool_Recursive(t *testing.T) {
	dir := setupSearchTree(t)
	tool := NewGlobSearchTool(100)
	res, err := tool.Execute(context.Background(), map[string]any{"pattern": "**/*.go", "path": dir})
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if !res.Success {
		t.Fatalf("expected success, got error: %s", res.Error)
	}
	if !strings.Contains(res.Output, "main.go") || !strings.Contains(res.Output, "util.go") {
		t.Errorf("expected both .go files in output, got: %q", res.Output)
	}
	if strings.Contains(res.Output, "README.md") {
		t.Errorf("did not expect README.md to match *.go pattern: %q", res.Output)
	}
}

func TestGlobSearchTool_ResultCap(t *testing.T) {
	dir := setupSearchTree(t)
	tool := NewGlobSearchTool(1)
	res, err := tool.Execute(context.Background(), map[string]any{"pattern": "**/*.go", "path": dir})
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if res.Metadata["match_count"] != 1 {
		t.Errorf("match_count = %v, want 1", res.Metadata["match_count"])
	}
	if res.Metadata["truncated"] != true {
		t.Errorf("truncated = %v, want true", res.Metadata["truncated"])
	}
}

func TestGrepSearchTool_Basic(t *testing.T) {
	dir := setupSearchTree(t)
	tool := NewGrepSearchTool(100)
	res, err := tool.Execute(context.Background(), map[string]any{"pattern": "TODO", "path": dir})
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if !res.Success {
		t.Fatalf("expected success, got error: %s", res.Error)
	}
	if res.Metadata["match_count"] != 2 {
		t.Errorf("match_count = %v, want 2, output: %q", res.Metadata["match_count"], res.Output)
	}
}

func TestGrepSearchTool_IncludeFilter(t *testing.T) {
	dir := setupSearchTree(t)
	tool := NewGrepSearchTool(100)
	res, err := tool.Execute(context.Background(), map[string]any{"pattern": "TODO", "path": dir, "include": "*.go"})
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if res.Metadata["match_count"] != 1 {
		t.Errorf("match_count = %v, want 1 (only pkg/util.go matches *.go), output: %q", res.Metadata["match_count"], res.Output)
	}
}

func TestGrepSearchTool_InvalidRegex(t *testing.T) {
	dir := setupSearchTree(t)
	tool := NewGrepSearchTool(100)
	res, err := tool.Execute(context.Background(), map[string]any{"pattern": "(unclosed", "path": dir})
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if res.Success {
		t.Fatal("expected failure for invalid regex")
	}
}

func TestGrepSearchTool_SkipsBinaryFiles(t *testing.T) {
	dir := t.TempDir()
	binPath := filepath.Join(dir, "data.bin")
	os.WriteFile(binPath, []byte("TODO\x00binary\x00garbage"), 0o644)

	tool := NewGrepSearchTool(100)
	res, err := tool.Execute(context.Background(), map[string]any{"pattern": "TODO", "path": dir})
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if res.Metadata["match_count"] != 0 {
		t.Errorf("expected binary file to be skipped, match_count = %v", res.Metadata["match_count"])
	}
}

func TestSearch_Unconfined(t *testing.T) {
	// Searching a directory unrelated to any "project root" must work —
	// there is no path jail.
	dir := setupSearchTree(t)
	tool := NewGlobSearchTool(100)
	res, err := tool.Execute(context.Background(), map[string]any{"pattern": "*.md", "path": dir})
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if !res.Success || res.Metadata["match_count"] != 1 {
		t.Fatalf("expected to find README.md, got: %+v", res)
	}
}
