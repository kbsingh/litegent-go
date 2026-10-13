package tools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

// --- read_file ---------------------------------------------------------

// ReadFileTool reads a file's contents with line offset/limit bounds. It
// is unconfined: file_path may be any path on the host.
type ReadFileTool struct {
	DefaultLimit int
}

func NewReadFileTool(defaultLimit int) *ReadFileTool {
	return &ReadFileTool{DefaultLimit: orDefault(defaultLimit, 2000)}
}

func (t *ReadFileTool) Name() string { return "read_file" }

func (t *ReadFileTool) Schema() map[string]any {
	return functionSchema("read_file",
		"Read file contents with line offset and count limit. Not confined to any directory.",
		map[string]any{
			"file_path": map[string]any{"type": "string", "description": "Absolute or relative file path."},
			"offset":    map[string]any{"type": "integer", "description": "Start line (1-indexed, default 1)."},
			"limit":     map[string]any{"type": "integer", "description": "Max lines to return (default 2000)."},
		},
		[]string{"file_path"},
	)
}

func (t *ReadFileTool) Execute(_ context.Context, args map[string]any) (*ToolResult, error) {
	path, errRes := requireStringArg(args, "file_path")
	if errRes != nil {
		return errRes, nil
	}
	offset := optIntArg(args, "offset", 1)
	if offset < 1 {
		offset = 1
	}
	limit := optIntArg(args, "limit", t.DefaultLimit)

	raw, err := os.ReadFile(path)
	if err != nil {
		return &ToolResult{Success: false, Error: err.Error()}, nil
	}

	text := string(raw)
	if !utf8.ValidString(text) {
		// Fall back to a byte-for-byte Latin-1 (ISO-8859-1) interpretation
		// rather than failing outright on non-UTF-8 files.
		runes := make([]rune, len(raw))
		for i, b := range raw {
			runes[i] = rune(b)
		}
		text = string(runes)
	}

	lines := strings.Split(text, "\n")
	total := len(lines)

	start := offset - 1
	if start > total {
		start = total
	}
	end := start + limit
	if end > total {
		end = total
	}

	var sb strings.Builder
	for i := start; i < end; i++ {
		fmt.Fprintf(&sb, "%d: %s\n", i+1, lines[i])
	}

	return &ToolResult{
		Success: true,
		Output:  sb.String(),
		Metadata: map[string]any{
			"total_lines": total,
			"offset":      offset,
			"count":       end - start,
		},
	}, nil
}

// --- write_file ---------------------------------------------------------

// WriteFileTool creates or overwrites a file, auto-creating parent
// directories. It is unconfined: file_path may be any path on the host.
type WriteFileTool struct{}

func NewWriteFileTool() *WriteFileTool { return &WriteFileTool{} }

func (t *WriteFileTool) Name() string { return "write_file" }

func (t *WriteFileTool) Schema() map[string]any {
	return functionSchema("write_file",
		"Create or overwrite a file with provided content. Automatically creates parent directories. Not confined to any directory.",
		map[string]any{
			"file_path": map[string]any{"type": "string", "description": "Path to destination file."},
			"content":   map[string]any{"type": "string", "description": "Text content to write."},
		},
		[]string{"file_path", "content"},
	)
}

func (t *WriteFileTool) Execute(_ context.Context, args map[string]any) (*ToolResult, error) {
	path, errRes := requireStringArg(args, "file_path")
	if errRes != nil {
		return errRes, nil
	}
	content, errRes := requireAnyStringArg(args, "content")
	if errRes != nil {
		return errRes, nil
	}

	if dir := filepath.Dir(path); dir != "." && dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return &ToolResult{Success: false, Error: err.Error()}, nil
		}
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return &ToolResult{Success: false, Error: err.Error()}, nil
	}

	abs, _ := filepath.Abs(path)
	return &ToolResult{
		Success: true,
		Output:  fmt.Sprintf("wrote %d bytes to %s", len(content), abs),
		Metadata: map[string]any{
			"bytes_written": len(content),
			"path":          abs,
		},
	}, nil
}

// --- edit_file ------------------------------------------------------------

// EditFileTool performs exact substring replacement in a file. It is
// unconfined: file_path may be any path on the host.
type EditFileTool struct{}

func NewEditFileTool() *EditFileTool { return &EditFileTool{} }

func (t *EditFileTool) Name() string { return "edit_file" }

func (t *EditFileTool) Schema() map[string]any {
	return functionSchema("edit_file",
		"Perform exact substring replacement in a file. Not confined to any directory.",
		map[string]any{
			"file_path":   map[string]any{"type": "string", "description": "Path to file to edit."},
			"old_string":  map[string]any{"type": "string", "description": "Exact text substring to replace."},
			"new_string":  map[string]any{"type": "string", "description": "New replacement text."},
			"replace_all": map[string]any{"type": "boolean", "description": "Replace all instances if true. Default false."},
		},
		[]string{"file_path", "old_string", "new_string"},
	)
}

func (t *EditFileTool) Execute(_ context.Context, args map[string]any) (*ToolResult, error) {
	path, errRes := requireStringArg(args, "file_path")
	if errRes != nil {
		return errRes, nil
	}
	oldString, errRes := requireAnyStringArg(args, "old_string")
	if errRes != nil {
		return errRes, nil
	}
	newString, errRes := requireAnyStringArg(args, "new_string")
	if errRes != nil {
		return errRes, nil
	}
	replaceAll := optBoolArg(args, "replace_all", false)

	raw, err := os.ReadFile(path)
	if err != nil {
		return &ToolResult{Success: false, Error: err.Error()}, nil
	}
	content := string(raw)

	count := strings.Count(content, oldString)
	if count == 0 {
		return &ToolResult{Success: false, Error: "old_string not found in file"}, nil
	}
	if count > 1 && !replaceAll {
		return &ToolResult{Success: false, Error: fmt.Sprintf("old_string matches %d locations; pass replace_all=true or provide more context to make it unique", count)}, nil
	}

	n := 1
	if replaceAll {
		n = -1
	}
	updated := strings.Replace(content, oldString, newString, n)

	if err := os.WriteFile(path, []byte(updated), 0o644); err != nil {
		return &ToolResult{Success: false, Error: err.Error()}, nil
	}

	replaced := count
	if !replaceAll {
		replaced = 1
	}
	return &ToolResult{
		Success: true,
		Output:  fmt.Sprintf("replaced %d occurrence(s) in %s", replaced, path),
		Metadata: map[string]any{
			"replacements": replaced,
		},
	}, nil
}
