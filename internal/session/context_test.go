package session

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"litegent/internal/llm"
)

func testInfo(t *testing.T) *Info {
	t.Helper()
	info, err := create(t.TempDir(), Options{}, func() string { return "brave-otter-runs" })
	if err != nil {
		t.Fatalf("create returned error: %v", err)
	}
	return info
}

func validContextMessages() []llm.Message {
	return []llm.Message{
		{Role: llm.RoleSystem, Content: "system"},
		{Role: llm.RoleUser, Content: "task"},
		{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{{
			ID:   "call_1",
			Type: "function",
			Function: llm.FunctionCall{
				Name:      "bash",
				Arguments: `{"command":"echo hi"}`,
			},
		}}},
		{Role: llm.RoleTool, ToolCallID: "call_1", Content: `{"success":true}`},
		{Role: llm.RoleAssistant, Content: "done"},
	}
}

func TestSaveLoadContextRoundTrip(t *testing.T) {
	info := testInfo(t)
	messages := validContextMessages()
	if err := SaveContext(info, messages); err != nil {
		t.Fatalf("SaveContext returned error: %v", err)
	}
	loaded, err := LoadContext(info)
	if err != nil {
		t.Fatalf("LoadContext returned error: %v", err)
	}
	if !reflect.DeepEqual(loaded, messages) {
		t.Errorf("loaded context differs:\n got: %+v\nwant: %+v", loaded, messages)
	}
	if _, err := os.Stat(info.ContextPath); err != nil {
		t.Errorf("context file missing: %v", err)
	}
}

func TestSaveContextRejectsOrphanToolMessage(t *testing.T) {
	info := testInfo(t)
	messages := []llm.Message{
		{Role: llm.RoleSystem, Content: "system"},
		{Role: llm.RoleUser, Content: "task"},
		{Role: llm.RoleTool, ToolCallID: "missing", Content: "result"},
	}
	if err := SaveContext(info, messages); err == nil {
		t.Fatal("expected orphan tool message to be rejected")
	}
}

func TestSaveContextRejectsDanglingToolCall(t *testing.T) {
	info := testInfo(t)
	messages := []llm.Message{
		{Role: llm.RoleSystem, Content: "system"},
		{Role: llm.RoleUser, Content: "task"},
		{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{{ID: "call_1", Function: llm.FunctionCall{Name: "bash"}}}},
	}
	if err := SaveContext(info, messages); err == nil {
		t.Fatal("expected dangling tool call to be rejected")
	}
}

func TestSaveContextRejectsSummaryOutsideIndex2(t *testing.T) {
	info := testInfo(t)
	messages := []llm.Message{
		{Role: llm.RoleSystem, Content: "system"},
		{Role: llm.RoleUser, Content: "task"},
		{Role: llm.RoleAssistant, Content: "done"},
		{Role: llm.RoleSummary, Content: "summary"},
	}
	if err := SaveContext(info, messages); err == nil {
		t.Fatal("expected misplaced summary to be rejected")
	}
}

func TestSaveContextInvalidDoesNotOverwrite(t *testing.T) {
	info := testInfo(t)
	valid := validContextMessages()
	if err := SaveContext(info, valid); err != nil {
		t.Fatal(err)
	}
	invalid := []llm.Message{{Role: llm.RoleUser, Content: "no system"}}
	if err := SaveContext(info, invalid); err == nil {
		t.Fatal("expected invalid save to fail")
	}
	loaded, err := LoadContext(info)
	if err != nil {
		t.Fatalf("previous context should remain loadable: %v", err)
	}
	if !reflect.DeepEqual(loaded, valid) {
		t.Error("invalid save overwrote the previous valid context")
	}
}

func TestLoadContextRejectsHashMismatch(t *testing.T) {
	info := testInfo(t)
	if err := SaveContext(info, validContextMessages()); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(info.ContextPath)
	if err != nil {
		t.Fatal(err)
	}
	tampered := strings.Replace(string(data), `"content": "task"`, `"content": "changed"`, 1)
	if tampered == string(data) {
		t.Fatal("test setup failed to modify context")
	}
	if err := os.WriteFile(info.ContextPath, []byte(tampered), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadContext(info); err == nil {
		t.Fatal("expected hash mismatch to be rejected")
	}
}

func TestLoadContextRejectsWrongSessionID(t *testing.T) {
	info := testInfo(t)
	if err := SaveContext(info, validContextMessages()); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(info.ContextPath)
	if err != nil {
		t.Fatal(err)
	}
	tampered := strings.Replace(string(data), `"session_id": "brave-otter-runs"`, `"session_id": "other-session"`, 1)
	if tampered == string(data) {
		t.Fatal("test setup failed to modify context")
	}
	if err := os.WriteFile(info.ContextPath, []byte(tampered), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadContext(info); err == nil {
		t.Fatal("expected session id mismatch to be rejected")
	}
}

func TestLoadContextMissing(t *testing.T) {
	info := testInfo(t)
	if err := os.Remove(info.ContextPath); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	if _, err := LoadContext(info); err == nil {
		t.Fatal("expected missing context to fail")
	}
}

func TestValidateMessagesAllowsInitialStates(t *testing.T) {
	if err := ValidateMessages([]llm.Message{{Role: llm.RoleSystem, Content: "system"}}); err != nil {
		t.Errorf("system-only context must be valid: %v", err)
	}
	if err := ValidateMessages([]llm.Message{
		{Role: llm.RoleSystem, Content: "system"},
		{Role: llm.RoleUser, Content: "task"},
	}); err != nil {
		t.Errorf("system+user context must be valid: %v", err)
	}
	if err := ValidateMessages([]llm.Message{
		{Role: llm.RoleSystem, Content: "system"},
		{Role: llm.RoleUser, Content: "task"},
		{Role: llm.RoleSummary, Content: "summary"},
		{Role: llm.RoleUser, Content: "next"},
	}); err != nil {
		t.Errorf("summary at index 2 must be valid: %v", err)
	}
}

func TestContextPathIsInsideSession(t *testing.T) {
	info := testInfo(t)
	if filepath.Dir(info.ContextPath) != info.Dir {
		t.Errorf("ContextPath %q is not directly inside session dir %q", info.ContextPath, info.Dir)
	}
}
