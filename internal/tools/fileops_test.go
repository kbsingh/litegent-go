package tools

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestReadFileTool_Basic(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "file.txt")
	os.WriteFile(path, []byte("line1\nline2\nline3\n"), 0o644)

	tool := NewReadFileTool(2000)
	res, err := tool.Execute(context.Background(), map[string]any{"file_path": path})
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if !res.Success {
		t.Fatalf("expected success, got error: %s", res.Error)
	}
	want := "1: line1\n2: line2\n3: line3\n4: \n"
	if res.Output != want {
		t.Errorf("Output = %q, want %q", res.Output, want)
	}
	if res.Metadata["total_lines"] != 4 { // trailing empty line from split
		t.Errorf("total_lines = %v, want 4", res.Metadata["total_lines"])
	}
}

func TestReadFileTool_OffsetLimit(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "file.txt")
	os.WriteFile(path, []byte("a\nb\nc\nd\ne\n"), 0o644)

	tool := NewReadFileTool(2000)
	res, err := tool.Execute(context.Background(), map[string]any{"file_path": path, "offset": 2, "limit": 2})
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	want := "2: b\n3: c\n"
	if res.Output != want {
		t.Errorf("Output = %q, want %q", res.Output, want)
	}
}

func TestReadFileTool_NotFound(t *testing.T) {
	tool := NewReadFileTool(2000)
	res, err := tool.Execute(context.Background(), map[string]any{"file_path": "/nonexistent/file.txt"})
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if res.Success {
		t.Fatal("expected failure for missing file")
	}
}

func TestWriteFileTool_CreatesParentDirs(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nested", "deep", "file.txt")

	tool := NewWriteFileTool()
	res, err := tool.Execute(context.Background(), map[string]any{"file_path": path, "content": "hello"})
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if !res.Success {
		t.Fatalf("expected success, got error: %s", res.Error)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("file was not written: %v", err)
	}
	if string(got) != "hello" {
		t.Errorf("file content = %q, want %q", got, "hello")
	}
}

func TestWriteFileTool_Overwrites(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "file.txt")
	os.WriteFile(path, []byte("old"), 0o644)

	tool := NewWriteFileTool()
	_, err := tool.Execute(context.Background(), map[string]any{"file_path": path, "content": "new"})
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	got, _ := os.ReadFile(path)
	if string(got) != "new" {
		t.Errorf("file content = %q, want %q", got, "new")
	}
}

func TestEditFileTool_SingleReplace(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "file.txt")
	os.WriteFile(path, []byte("hello world"), 0o644)

	tool := NewEditFileTool()
	res, err := tool.Execute(context.Background(), map[string]any{
		"file_path": path, "old_string": "world", "new_string": "go",
	})
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if !res.Success {
		t.Fatalf("expected success, got error: %s", res.Error)
	}
	got, _ := os.ReadFile(path)
	if string(got) != "hello go" {
		t.Errorf("file content = %q, want %q", got, "hello go")
	}
}

func TestEditFileTool_NotFound(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "file.txt")
	os.WriteFile(path, []byte("hello world"), 0o644)

	tool := NewEditFileTool()
	res, err := tool.Execute(context.Background(), map[string]any{
		"file_path": path, "old_string": "missing", "new_string": "x",
	})
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if res.Success {
		t.Fatal("expected failure when old_string is absent")
	}
}

func TestEditFileTool_AmbiguousWithoutReplaceAll(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "file.txt")
	os.WriteFile(path, []byte("foo foo foo"), 0o644)

	tool := NewEditFileTool()
	res, err := tool.Execute(context.Background(), map[string]any{
		"file_path": path, "old_string": "foo", "new_string": "bar",
	})
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if res.Success {
		t.Fatal("expected failure for ambiguous match without replace_all")
	}
}

func TestEditFileTool_ReplaceAll(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "file.txt")
	os.WriteFile(path, []byte("foo foo foo"), 0o644)

	tool := NewEditFileTool()
	res, err := tool.Execute(context.Background(), map[string]any{
		"file_path": path, "old_string": "foo", "new_string": "bar", "replace_all": true,
	})
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if !res.Success {
		t.Fatalf("expected success, got error: %s", res.Error)
	}
	got, _ := os.ReadFile(path)
	if string(got) != "bar bar bar" {
		t.Errorf("file content = %q, want %q", got, "bar bar bar")
	}
}

func TestFileOps_Unconfined(t *testing.T) {
	// Writing outside of any notion of a "project root" must succeed —
	// there is no path jail.
	other := t.TempDir()
	path := filepath.Join(other, "sibling-tree", "file.txt")

	tool := NewWriteFileTool()
	res, err := tool.Execute(context.Background(), map[string]any{"file_path": path, "content": "x"})
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if !res.Success {
		t.Fatalf("expected unconfined write to succeed, got error: %s", res.Error)
	}
}
