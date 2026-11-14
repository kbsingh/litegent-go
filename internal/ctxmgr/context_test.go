package ctxmgr

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"litegent/internal/config"
	"litegent/internal/llm"
	"litegent/internal/session"
)

func TestTruncateToolOutput_UnderLimit(t *testing.T) {
	m := NewManager(config.ContextConfig{MaxToolOutputChars: 100, SpoolDir: t.TempDir()}, nil)
	out, info, err := m.TruncateToolOutput("call1", "short output")
	if err != nil {
		t.Fatalf("TruncateToolOutput returned error: %v", err)
	}
	if out != "short output" {
		t.Errorf("expected untouched output, got %q", out)
	}
	if info != nil {
		t.Errorf("expected nil TruncationInfo for output under the limit, got %+v", info)
	}
}

func TestTruncateToolOutput_OverLimit_Spools(t *testing.T) {
	spoolDir := t.TempDir()
	m := NewManager(config.ContextConfig{MaxToolOutputChars: 20, SpoolDir: spoolDir}, nil)
	raw := strings.Repeat("x", 1000)
	out, info, err := m.TruncateToolOutput("call42", raw)
	if err != nil {
		t.Fatalf("TruncateToolOutput returned error: %v", err)
	}
	if !strings.Contains(out, "TRUNCATED") {
		t.Errorf("expected truncation notice in output, got %q", out)
	}
	if len(out) >= len(raw) {
		t.Errorf("expected truncated output to be shorter than raw, got len %d vs raw %d", len(out), len(raw))
	}
	if info == nil || info.OmittedChars <= 0 {
		t.Fatalf("expected non-nil TruncationInfo with omitted chars, got %+v", info)
	}

	spoolFile := filepath.Join(spoolDir, "output_call42.txt")
	spooled, err := os.ReadFile(spoolFile)
	if err != nil {
		t.Fatalf("expected spool file to exist: %v", err)
	}
	if string(spooled) != raw {
		t.Errorf("spooled content does not match raw output")
	}
}

func TestPrepare_NoOpBelowWindowSize(t *testing.T) {
	m := NewManager(config.ContextConfig{WindowSize: 20}, nil)
	messages := []llm.Message{
		{Role: llm.RoleSystem, Content: "sys"},
		{Role: llm.RoleUser, Content: "task"},
		{Role: llm.RoleTool, Content: "result1"},
	}
	res, err := m.Prepare(context.Background(), messages)
	if err != nil {
		t.Fatalf("Prepare returned error: %v", err)
	}
	if len(res.Messages) != len(messages) {
		t.Errorf("expected no pruning below window size, got %d messages", len(res.Messages))
	}
	if res.Evicted != 0 || res.Summarized {
		t.Errorf("unexpected pruning result: %+v", res)
	}
}

func TestPrepare_NeverEvictsSystemOrUserPrompt(t *testing.T) {
	m := NewManager(config.ContextConfig{WindowSize: 2}, nil)
	messages := []llm.Message{
		{Role: llm.RoleSystem, Content: "sys"},
		{Role: llm.RoleUser, Content: "task"},
	}
	for i := 0; i < 10; i++ {
		messages = append(messages, llm.Message{Role: llm.RoleTool, Content: "turn"})
	}

	res, err := m.Prepare(context.Background(), messages)
	if err != nil {
		t.Fatalf("Prepare returned error: %v", err)
	}
	if res.Messages[0].Role != llm.RoleSystem || res.Messages[0].Content != "sys" {
		t.Fatalf("system prompt was evicted or modified: %+v", res.Messages[0])
	}
	if res.Messages[1].Role != llm.RoleUser || res.Messages[1].Content != "task" {
		t.Fatalf("user prompt was evicted or modified: %+v", res.Messages[1])
	}
}

func TestPrepare_SlidingWindowPrunesOldestTurns(t *testing.T) {
	m := NewManager(config.ContextConfig{WindowSize: 3}, nil)
	messages := []llm.Message{
		{Role: llm.RoleSystem, Content: "sys"},
		{Role: llm.RoleUser, Content: "task"},
	}
	for i := 1; i <= 5; i++ {
		messages = append(messages, llm.Message{Role: llm.RoleTool, Content: strconv.Itoa(i)})
	}

	res, err := m.Prepare(context.Background(), messages)
	if err != nil {
		t.Fatalf("Prepare returned error: %v", err)
	}
	active := res.Messages[2:]
	if len(active) != 3 {
		t.Fatalf("expected window size of 3 active turns, got %d: %+v", len(active), active)
	}
	// The 3 most recent turns (3, 4, 5) must be the ones kept.
	if active[0].Content != "3" || active[2].Content != "5" {
		t.Errorf("expected oldest turns evicted, kept turns: %+v", active)
	}
	if res.Evicted != 2 || res.Summarized {
		t.Errorf("pruning metadata = %+v, want Evicted=2 and no summary", res)
	}
}

type fakeSummarizer struct {
	called   bool
	received []llm.Message
}

func (f *fakeSummarizer) Summarize(_ context.Context, messages []llm.Message) (string, error) {
	f.called = true
	f.received = messages
	return "summary text", nil
}

func TestPrepare_SummarizesEvictedTurns(t *testing.T) {
	fs := &fakeSummarizer{}
	m := NewManager(config.ContextConfig{WindowSize: 2}, fs)
	messages := []llm.Message{
		{Role: llm.RoleSystem, Content: "sys"},
		{Role: llm.RoleUser, Content: "task"},
		{Role: llm.RoleTool, Content: "old1"},
		{Role: llm.RoleTool, Content: "old2"},
		{Role: llm.RoleTool, Content: "recent1"},
		{Role: llm.RoleTool, Content: "recent2"},
	}

	res, err := m.Prepare(context.Background(), messages)
	if err != nil {
		t.Fatalf("Prepare returned error: %v", err)
	}
	if !fs.called {
		t.Fatal("expected summarizer to be called when window size is exceeded")
	}
	if res.Messages[2].Role != llm.RoleSummary || res.Messages[2].Content != "summary text" {
		t.Fatalf("expected <context_summary> at index 2, got: %+v", res.Messages[2])
	}
	if !res.Summarized || res.Summary != "summary text" || res.Evicted != 2 {
		t.Errorf("summary metadata = %+v", res)
	}
}

func TestPrepare_DoesNotSummarizeUnderTokenBudget(t *testing.T) {
	fs := &fakeSummarizer{}
	// WindowSize is small enough to overflow, but MaxContextTokens is huge
	// relative to the tiny test messages, so the token budget is never
	// actually exceeded.
	m := NewManager(config.ContextConfig{WindowSize: 2, MaxContextTokens: 1_000_000}, fs)
	messages := []llm.Message{
		{Role: llm.RoleSystem, Content: "sys"},
		{Role: llm.RoleUser, Content: "task"},
		{Role: llm.RoleTool, Content: "old1"},
		{Role: llm.RoleTool, Content: "old2"},
		{Role: llm.RoleTool, Content: "recent1"},
		{Role: llm.RoleTool, Content: "recent2"},
	}

	res, err := m.Prepare(context.Background(), messages)
	if err != nil {
		t.Fatalf("Prepare returned error: %v", err)
	}
	if fs.called {
		t.Fatal("expected summarizer NOT to be called when under the token budget")
	}
	// Evicted turns should simply be dropped: system, user, then the 2
	// most recent turns, no <context_summary> message in between.
	if len(res.Messages) != 4 {
		t.Fatalf("expected 4 messages (system, user, 2 kept turns), got %d: %+v", len(res.Messages), res.Messages)
	}
	if res.Messages[2].Role == llm.RoleSummary {
		t.Error("did not expect a <context_summary> message when under budget")
	}
	if res.Summarized || res.Evicted != 2 {
		t.Errorf("expected eviction without summary, got %+v", res)
	}
}

func TestEstimateTokens(t *testing.T) {
	messages := []llm.Message{{Role: llm.RoleUser, Content: strings.Repeat("a", 400)}}
	got := EstimateTokens(messages)
	if got != 100 {
		t.Errorf("EstimateTokens() = %d, want 100 (400 chars / 4)", got)
	}
}

func TestPrepare_DoesNotSplitToolCallPairs(t *testing.T) {
	m := NewManager(config.ContextConfig{WindowSize: 3}, nil)
	messages := []llm.Message{
		{Role: llm.RoleSystem, Content: "sys"},
		{Role: llm.RoleUser, Content: "task"},
		{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{{
			ID:       "call_1",
			Type:     "function",
			Function: llm.FunctionCall{Name: "bash", Arguments: `{}`},
		}}},
		{Role: llm.RoleTool, ToolCallID: "call_1", Content: "result"},
		{Role: llm.RoleAssistant, Content: "first answer"},
		{Role: llm.RoleUser, Content: "second task"},
		{Role: llm.RoleAssistant, Content: "second answer"},
	}

	res, err := m.Prepare(context.Background(), messages)
	if err != nil {
		t.Fatalf("Prepare returned error: %v", err)
	}
	if res.Evicted == 0 {
		t.Fatal("expected pruning to occur")
	}
	if err := session.ValidateMessages(res.Messages); err != nil {
		t.Fatalf("prepared context is structurally invalid: %v", err)
	}
	if res.Messages[2].Role == llm.RoleTool {
		t.Errorf("prepared context starts with an orphaned tool message: %+v", res.Messages)
	}
}

func TestPrepare_KeepsLastUnitWhenItExceedsWindow(t *testing.T) {
	m := NewManager(config.ContextConfig{WindowSize: 1}, nil)
	messages := []llm.Message{
		{Role: llm.RoleSystem, Content: "sys"},
		{Role: llm.RoleUser, Content: "task"},
		{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{{
			ID:       "call_1",
			Type:     "function",
			Function: llm.FunctionCall{Name: "bash", Arguments: `{}`},
		}}},
		{Role: llm.RoleTool, ToolCallID: "call_1", Content: "result"},
		{Role: llm.RoleAssistant, Content: "final"},
	}

	res, err := m.Prepare(context.Background(), messages)
	if err != nil {
		t.Fatalf("Prepare returned error: %v", err)
	}
	if err := session.ValidateMessages(res.Messages); err != nil {
		t.Fatalf("prepared context is structurally invalid: %v", err)
	}
	if last := res.Messages[len(res.Messages)-1]; last.Role != llm.RoleAssistant || last.Content != "final" {
		t.Errorf("expected the final assistant message to be retained, got: %+v", res.Messages)
	}
}
