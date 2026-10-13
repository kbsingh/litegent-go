package tools

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/bmatcuk/doublestar/v4"
)

// --- glob_search ---------------------------------------------------------

// GlobSearchTool matches filenames against (possibly recursive "**") glob
// patterns. It is unconfined: path may be any directory on the host.
type GlobSearchTool struct {
	MaxResults int
}

func NewGlobSearchTool(maxResults int) *GlobSearchTool {
	return &GlobSearchTool{MaxResults: orDefault(maxResults, 100)}
}

func (t *GlobSearchTool) Name() string { return "glob_search" }

func (t *GlobSearchTool) Schema() map[string]any {
	return functionSchema("glob_search",
		"Search directory trees using glob patterns (supports recursive '**'). Not confined to any directory.",
		map[string]any{
			"pattern": map[string]any{"type": "string", "description": "Glob pattern (e.g. '**/*.go')."},
			"path":    map[string]any{"type": "string", "description": "Directory to search from (default '.')."},
		},
		[]string{"pattern"},
	)
}

func (t *GlobSearchTool) Execute(_ context.Context, args map[string]any) (*ToolResult, error) {
	pattern, errRes := requireStringArg(args, "pattern")
	if errRes != nil {
		return errRes, nil
	}
	root := optStringArg(args, "path", ".")

	fsys := os.DirFS(root)
	matches, err := doublestar.Glob(fsys, pattern)
	if err != nil {
		return &ToolResult{Success: false, Error: err.Error()}, nil
	}

	truncated := false
	if len(matches) > t.MaxResults {
		matches = matches[:t.MaxResults]
		truncated = true
	}

	return &ToolResult{
		Success: true,
		Output:  strings.Join(matches, "\n"),
		Metadata: map[string]any{
			"match_count": len(matches),
			"truncated":   truncated,
		},
	}, nil
}

// --- grep_search ---------------------------------------------------------

// GrepSearchTool searches file contents using regular expressions. It is
// unconfined: path may be any directory or file on the host.
type GrepSearchTool struct {
	MaxResults int
}

func NewGrepSearchTool(maxResults int) *GrepSearchTool {
	return &GrepSearchTool{MaxResults: orDefault(maxResults, 100)}
}

func (t *GrepSearchTool) Name() string { return "grep_search" }

func (t *GrepSearchTool) Schema() map[string]any {
	return functionSchema("grep_search",
		"Search file contents using regular expressions. Not confined to any directory.",
		map[string]any{
			"pattern": map[string]any{"type": "string", "description": "Regex pattern."},
			"path":    map[string]any{"type": "string", "description": "Directory or file to search (default '.')."},
			"include": map[string]any{"type": "string", "description": "Glob filter for filenames (e.g. '*.go')."},
		},
		[]string{"pattern"},
	)
}

type grepMatch struct {
	Path string `json:"path"`
	Line int    `json:"line"`
	Text string `json:"text"`
}

func (t *GrepSearchTool) Execute(_ context.Context, args map[string]any) (*ToolResult, error) {
	pattern, errRes := requireStringArg(args, "pattern")
	if errRes != nil {
		return errRes, nil
	}
	root := optStringArg(args, "path", ".")
	include := optStringArg(args, "include", "")

	re, err := regexp.Compile(pattern)
	if err != nil {
		return &ToolResult{Success: false, Error: fmt.Sprintf("invalid regex: %v", err)}, nil
	}

	var matches []grepMatch
	walkErr := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // skip unreadable entries rather than aborting the whole search
		}
		if d.IsDir() {
			return nil
		}
		if include != "" {
			ok, _ := doublestar.Match(include, d.Name())
			if !ok {
				return nil
			}
		}
		if len(matches) >= t.MaxResults {
			return fs.SkipAll
		}

		f, err := os.Open(path)
		if err != nil {
			return nil
		}
		defer f.Close()

		// Sniff the first 512 bytes for a NUL byte (the same heuristic
		// net/http.DetectContentType uses) to skip binary files, then
		// scan the same already-open file for matches — one os.Open per
		// file instead of two.
		reader := bufio.NewReader(f)
		peek, _ := reader.Peek(512)
		if bytes.IndexByte(peek, 0) != -1 {
			return nil
		}

		scanner := bufio.NewScanner(reader)
		scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		lineNum := 0
		for scanner.Scan() {
			lineNum++
			line := scanner.Text()
			if re.MatchString(line) {
				matches = append(matches, grepMatch{Path: path, Line: lineNum, Text: line})
				if len(matches) >= t.MaxResults {
					break
				}
			}
		}
		return nil
	})
	if walkErr != nil {
		return &ToolResult{Success: false, Error: walkErr.Error()}, nil
	}

	var sb strings.Builder
	for _, m := range matches {
		fmt.Fprintf(&sb, "%s:%d: %s\n", m.Path, m.Line, m.Text)
	}

	return &ToolResult{
		Success: true,
		Output:  sb.String(),
		Metadata: map[string]any{
			"match_count": len(matches),
		},
	}, nil
}
