package tools

import (
	"context"
	"testing"
	"time"
)

func TestBashTool_Success(t *testing.T) {
	tool := NewBashTool(5*time.Second, ".")
	res, err := tool.Execute(context.Background(), map[string]any{"command": "echo hello"})
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if !res.Success {
		t.Fatalf("expected success, got error: %s", res.Error)
	}
	if res.Metadata["exit_code"] != 0 {
		t.Errorf("exit_code = %v, want 0", res.Metadata["exit_code"])
	}
}

func TestBashTool_NonZeroExit(t *testing.T) {
	tool := NewBashTool(5*time.Second, ".")
	res, err := tool.Execute(context.Background(), map[string]any{"command": "exit 7"})
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if res.Success {
		t.Fatal("expected failure for non-zero exit code")
	}
	if res.Metadata["exit_code"] != 7 {
		t.Errorf("exit_code = %v, want 7", res.Metadata["exit_code"])
	}
}

func TestBashTool_Timeout(t *testing.T) {
	tool := NewBashTool(1*time.Second, ".")
	res, err := tool.Execute(context.Background(), map[string]any{"command": "sleep 5", "timeout": 1})
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if res.Success {
		t.Fatal("expected timeout to be reported as failure")
	}
	if res.Metadata["timed_out"] != true {
		t.Errorf("timed_out = %v, want true", res.Metadata["timed_out"])
	}
}

func TestBashTool_StdinIsNonBlocking(t *testing.T) {
	// A command reading from stdin must see immediate EOF, not hang.
	tool := NewBashTool(3*time.Second, ".")
	res, err := tool.Execute(context.Background(), map[string]any{"command": "cat"})
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if res.Metadata["timed_out"] == true {
		t.Fatal("cat should have exited immediately on EOF from /dev/null, not timed out")
	}
}

func TestBashTool_MissingCommand(t *testing.T) {
	tool := NewBashTool(5*time.Second, ".")
	res, err := tool.Execute(context.Background(), map[string]any{})
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if res.Success {
		t.Fatal("expected failure for missing command argument")
	}
}

func TestBashTool_Unconfined_ArbitraryWorkdir(t *testing.T) {
	// Litegent must not reject or rewrite a workdir just because it lives
	// outside wherever the tool happened to default to — it is
	// intentionally unconfined (no path-jail logic of any kind).
	other := t.TempDir()
	tool := NewBashTool(5*time.Second, ".")
	res, err := tool.Execute(context.Background(), map[string]any{
		"command": "pwd",
		"workdir": other,
	})
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if !res.Success {
		t.Fatalf("expected success running in %s, got error: %s", other, res.Error)
	}
}
