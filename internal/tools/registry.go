// Package tools implements Litegent's built-in, native tool suite (bash,
// file operations, and search) with zero external MCP server dependency.
//
// Litegent is intentionally unconfined (see req/requirements_doc.md
// NFR-3.4): none of these tools restrict paths or commands to the
// invocation directory. They operate with the full filesystem and
// process privileges of the invoking user across the entire host.
package tools

import (
	"context"
	"encoding/json"
	"fmt"
)

// ToolResult is the standardized outcome of a tool execution. A tool
// failure is represented by Success=false plus a human-readable Error,
// never by a Go error — a failed tool call must be fed back to the model
// as a normal message so it can self-correct, not crash the process.
type ToolResult struct {
	Success  bool           `json:"success"`
	Output   string         `json:"output"`
	Error    string         `json:"error,omitempty"`
	Metadata map[string]any `json:"metadata,omitempty"`
}

// ToJSON serializes the result for inclusion in a tool-result message.
func (r *ToolResult) ToJSON() (string, error) {
	b, err := json.Marshal(r)
	if err != nil {
		return "", fmt.Errorf("marshaling tool result: %w", err)
	}
	return string(b), nil
}

// Tool is the interface every built-in tool implements.
type Tool interface {
	// Name returns the tool's function-calling name (e.g. "bash").
	Name() string
	// Schema returns the OpenAI function-calling JSON schema for this tool.
	Schema() map[string]any
	// Execute runs the tool with the given arguments. A Go error return is
	// reserved for unexpected/internal faults; expected failure modes
	// (command not found, file missing, ambiguous replacement, ...) should
	// be reported via ToolResult.Error with Success=false instead.
	Execute(ctx context.Context, args map[string]any) (*ToolResult, error)
}

// Registry holds the set of enabled tools and dispatches calls by name.
type Registry struct {
	tools map[string]Tool
	order []string // preserves registration order for stable schema listing
}

// NewRegistry returns an empty tool registry.
func NewRegistry() *Registry {
	return &Registry{tools: make(map[string]Tool)}
}

// Register adds a tool to the registry. Registering a tool under a name
// that already exists replaces the previous one.
func (r *Registry) Register(t Tool) {
	if _, exists := r.tools[t.Name()]; !exists {
		r.order = append(r.order, t.Name())
	}
	r.tools[t.Name()] = t
}

// Schemas returns the OpenAI function-calling schema for every registered
// tool, in registration order.
func (r *Registry) Schemas() []map[string]any {
	schemas := make([]map[string]any, 0, len(r.order))
	for _, name := range r.order {
		schemas = append(schemas, r.tools[name].Schema())
	}
	return schemas
}

// Has reports whether a tool with the given name is registered.
func (r *Registry) Has(name string) bool {
	_, ok := r.tools[name]
	return ok
}

// Execute dispatches a tool call by name. An unknown tool name yields a
// failed ToolResult (not a Go error) so the model can observe the mistake
// and self-correct.
func (r *Registry) Execute(ctx context.Context, name string, args map[string]any) (*ToolResult, error) {
	t, ok := r.tools[name]
	if !ok {
		return &ToolResult{Success: false, Error: fmt.Sprintf("unknown tool: %s", name)}, nil
	}
	return t.Execute(ctx, args)
}

// functionSchema builds the OpenAI function-calling JSON schema shared by
// every tool's Schema() method, collapsing the repeated
// {"type":"function","function":{...,"parameters":{...}}} nesting into a
// single call.
func functionSchema(name, description string, properties map[string]any, required []string) map[string]any {
	return map[string]any{
		"type": "function",
		"function": map[string]any{
			"name":        name,
			"description": description,
			"parameters": map[string]any{
				"type":       "object",
				"properties": properties,
				"required":   required,
			},
		},
	}
}

// stringArg extracts a required string argument, returning ok=false (not
// an error) if absent or of the wrong type, so callers can build a clean
// ToolResult.Error.
func stringArg(args map[string]any, key string) (string, bool) {
	v, ok := args[key]
	if !ok {
		return "", false
	}
	s, ok := v.(string)
	return s, ok
}

// requireStringArg extracts a required, non-empty string argument. On
// success it returns the value and a nil *ToolResult; on failure it
// returns a ready-to-return failure ToolResult so callers can write:
//
//	path, errRes := requireStringArg(args, "file_path")
//	if errRes != nil {
//	    return errRes, nil
//	}
func requireStringArg(args map[string]any, key string) (string, *ToolResult) {
	v, ok := stringArg(args, key)
	if !ok || v == "" {
		return "", &ToolResult{Success: false, Error: "missing required argument: " + key}
	}
	return v, nil
}

// requireAnyStringArg extracts a required string argument, but unlike
// requireStringArg it accepts an empty string as valid — for fields where
// "" is semantically meaningful (e.g. write_file's content, edit_file's
// old_string/new_string).
func requireAnyStringArg(args map[string]any, key string) (string, *ToolResult) {
	v, ok := stringArg(args, key)
	if !ok {
		return "", &ToolResult{Success: false, Error: "missing required argument: " + key}
	}
	return v, nil
}

// orDefault returns v if it is positive, otherwise def. Used by tool
// constructors to clamp a zero/negative configured limit to a sane
// default.
func orDefault(v, def int) int {
	if v <= 0 {
		return def
	}
	return v
}

// optStringArg extracts an optional string argument, returning def if the
// key is absent or of the wrong type.
func optStringArg(args map[string]any, key, def string) string {
	v, ok := args[key]
	if !ok {
		return def
	}
	s, ok := v.(string)
	if !ok {
		return def
	}
	return s
}

// optIntArg extracts an optional integer argument. JSON numbers decode as
// float64 via encoding/json and as int/float64 when passed directly from
// Go test code, so both are handled.
func optIntArg(args map[string]any, key string, def int) int {
	v, ok := args[key]
	if !ok {
		return def
	}
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	default:
		return def
	}
}

// optBoolArg extracts an optional boolean argument, returning def if the
// key is absent or of the wrong type.
func optBoolArg(args map[string]any, key string, def bool) bool {
	v, ok := args[key]
	if !ok {
		return def
	}
	b, ok := v.(bool)
	if !ok {
		return def
	}
	return b
}
