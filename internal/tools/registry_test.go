package tools

import (
	"context"
	"testing"
)

func TestRegistry_RegisterAndExecute(t *testing.T) {
	r := NewRegistry()
	r.Register(NewWriteFileTool())
	r.Register(NewReadFileTool(2000))

	if !r.Has("write_file") || !r.Has("read_file") {
		t.Fatal("expected both tools to be registered")
	}
	if r.Has("bash") {
		t.Fatal("did not expect bash to be registered")
	}

	schemas := r.Schemas()
	if len(schemas) != 2 {
		t.Fatalf("Schemas() returned %d entries, want 2", len(schemas))
	}
}

func TestRegistry_ExecuteUnknownTool(t *testing.T) {
	r := NewRegistry()
	res, err := r.Execute(context.Background(), "does_not_exist", nil)
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if res.Success {
		t.Fatal("expected failure for unknown tool")
	}
}

func TestToolResult_ToJSON(t *testing.T) {
	r := &ToolResult{Success: true, Output: "ok"}
	s, err := r.ToJSON()
	if err != nil {
		t.Fatalf("ToJSON returned error: %v", err)
	}
	if s == "" {
		t.Fatal("expected non-empty JSON")
	}
}
