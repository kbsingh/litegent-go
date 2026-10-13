package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"litegent/internal/config"
	"litegent/internal/ctxmgr"
	"litegent/internal/llm"
	"litegent/internal/logging"
	"litegent/internal/tools"
)

// scriptedServer serves a pre-programmed sequence of ChatResponses,
// repeating the last one once exhausted, and records every decoded
// request for later inspection.
type scriptedServer struct {
	mu        sync.Mutex
	responses []llm.ChatResponse
	idx       int
	requests  []llm.ChatRequest
}

func newScriptedServer(responses ...llm.ChatResponse) *scriptedServer {
	return &scriptedServer{responses: responses}
}

func (s *scriptedServer) handler(w http.ResponseWriter, r *http.Request) {
	var req llm.ChatRequest
	json.NewDecoder(r.Body).Decode(&req)

	s.mu.Lock()
	s.requests = append(s.requests, req)
	i := s.idx
	if i >= len(s.responses) {
		i = len(s.responses) - 1
	}
	s.idx++
	resp := s.responses[i]
	s.mu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

func (s *scriptedServer) requestCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.requests)
}

func (s *scriptedServer) lastRequest() llm.ChatRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.requests[len(s.requests)-1]
}

// fakeTool is a controllable Tool implementation for testing the loop
// without shelling out.
type fakeTool struct {
	name  string
	calls int
	fn    func(callNum int, args map[string]any) *tools.ToolResult
}

func (f *fakeTool) Name() string { return f.name }
func (f *fakeTool) Schema() map[string]any {
	return map[string]any{"type": "function", "function": map[string]any{"name": f.name}}
}
func (f *fakeTool) Execute(_ context.Context, args map[string]any) (*tools.ToolResult, error) {
	f.calls++
	return f.fn(f.calls, args), nil
}

func newTestAgent(t *testing.T, server *scriptedServer, registry *tools.Registry, cfgOverride func(*config.LitegentConfig)) *Agent {
	t.Helper()
	httpServer := httptest.NewServer(http.HandlerFunc(server.handler))
	t.Cleanup(httpServer.Close)

	cfg := config.Default()
	cfg.BaseURL = httpServer.URL
	cfg.MaxSteps = 5
	if cfgOverride != nil {
		cfgOverride(&cfg)
	}

	llmClient := llm.NewClient(cfg)
	llmClient.MaxRetries = 0

	logger, err := logging.New(t.TempDir(), cfg.APIKey)
	if err != nil {
		t.Fatalf("logging.New returned error: %v", err)
	}
	t.Cleanup(func() { logger.Close() })

	ctxManager := ctxmgr.NewManager(cfg.ContextConfig, llmClient)

	return New(cfg, llmClient, registry, ctxManager, logger)
}

func toolCallResponse(callID, toolName, argsJSON string) llm.ChatResponse {
	return llm.ChatResponse{
		Choices: []llm.Choice{{
			Message: llm.Message{
				Role: llm.RoleAssistant,
				ToolCalls: []llm.ToolCall{{
					ID:   callID,
					Type: "function",
					Function: llm.FunctionCall{
						Name:      toolName,
						Arguments: argsJSON,
					},
				}},
			},
			FinishReason: "tool_calls",
		}},
	}
}

func finalResponse(content string) llm.ChatResponse {
	return llm.ChatResponse{
		Choices: []llm.Choice{{
			Message:      llm.Message{Role: llm.RoleAssistant, Content: content},
			FinishReason: "stop",
		}},
	}
}

func TestAgent_CompletesAfterToolCall(t *testing.T) {
	server := newScriptedServer(
		toolCallResponse("call_1", "echo_tool", `{"text":"hi"}`),
		finalResponse("Done!"),
	)
	registry := tools.NewRegistry()
	registry.Register(&fakeTool{name: "echo_tool", fn: func(n int, args map[string]any) *tools.ToolResult {
		return &tools.ToolResult{Success: true, Output: "hi"}
	}})

	a := newTestAgent(t, server, registry, nil)
	result := a.Run(context.Background(), "system prompt", "user task")

	if result.Outcome != OutcomeSuccess {
		t.Fatalf("Outcome = %v, want OutcomeSuccess (err: %v)", result.Outcome, result.Err)
	}
	if result.FinalMessage != "Done!" {
		t.Errorf("FinalMessage = %q, want %q", result.FinalMessage, "Done!")
	}
	if result.Steps != 2 {
		t.Errorf("Steps = %d, want 2", result.Steps)
	}
}

func TestAgent_MaxStepsExceeded(t *testing.T) {
	// Always request a (varying) tool call, never finishing naturally.
	responses := make([]llm.ChatResponse, 0, 5)
	for i := 0; i < 5; i++ {
		responses = append(responses, toolCallResponse("call", "counter_tool", `{"n":`+string(rune('0'+i))+`}`))
	}
	server := newScriptedServer(responses...)
	registry := tools.NewRegistry()
	registry.Register(&fakeTool{name: "counter_tool", fn: func(n int, args map[string]any) *tools.ToolResult {
		return &tools.ToolResult{Success: true, Output: "result " + string(rune('0'+n))}
	}})

	a := newTestAgent(t, server, registry, func(c *config.LitegentConfig) { c.MaxSteps = 3 })
	result := a.Run(context.Background(), "system", "task")

	if result.Outcome != OutcomeMaxStepsExceeded {
		t.Fatalf("Outcome = %v, want OutcomeMaxStepsExceeded", result.Outcome)
	}
	if result.Steps != 3 {
		t.Errorf("Steps = %d, want 3", result.Steps)
	}
}

func TestAgent_LoopGuardTriggersOnRepeatedIdenticalCalls(t *testing.T) {
	responses := []llm.ChatResponse{
		toolCallResponse("c1", "stuck_tool", `{"x":1}`),
		toolCallResponse("c2", "stuck_tool", `{"x":1}`),
		toolCallResponse("c3", "stuck_tool", `{"x":1}`),
		toolCallResponse("c4", "stuck_tool", `{"x":1}`),
		finalResponse("gave up"),
	}
	server := newScriptedServer(responses...)
	registry := tools.NewRegistry()
	registry.Register(&fakeTool{name: "stuck_tool", fn: func(n int, args map[string]any) *tools.ToolResult {
		return &tools.ToolResult{Success: false, Error: "always fails the same way"}
	}})

	a := newTestAgent(t, server, registry, func(c *config.LitegentConfig) {
		c.MaxSteps = 5
		c.MaxRepeatedToolCalls = 2
	})
	result := a.Run(context.Background(), "system", "task")

	if result.Outcome != OutcomeSuccess {
		t.Fatalf("Outcome = %v, want OutcomeSuccess, err: %v", result.Outcome, result.Err)
	}

	// At least one later request must carry the loop-guard notice in its
	// most recent tool message.
	found := false
	for _, req := range server.requests {
		for _, m := range req.Messages {
			if m.Role == llm.RoleTool && strings.Contains(m.Content, "[LOOP DETECTED]") {
				found = true
			}
		}
	}
	if !found {
		t.Error("expected a [LOOP DETECTED] notice to appear in a subsequent request after repeated identical calls")
	}
}

func TestAgent_TruncatesLargeToolOutput(t *testing.T) {
	server := newScriptedServer(
		toolCallResponse("call_1", "big_tool", `{}`),
		finalResponse("ok"),
	)
	registry := tools.NewRegistry()
	bigOutput := strings.Repeat("x", 10000)
	registry.Register(&fakeTool{name: "big_tool", fn: func(n int, args map[string]any) *tools.ToolResult {
		return &tools.ToolResult{Success: true, Output: bigOutput}
	}})

	a := newTestAgent(t, server, registry, func(c *config.LitegentConfig) {
		c.ContextConfig.MaxToolOutputChars = 100
		c.ContextConfig.SpoolDir = t.TempDir()
	})
	result := a.Run(context.Background(), "system", "task")

	if result.Outcome != OutcomeSuccess {
		t.Fatalf("Outcome = %v, want OutcomeSuccess, err: %v", result.Outcome, result.Err)
	}

	last := server.lastRequest()
	found := false
	for _, m := range last.Messages {
		if m.Role == llm.RoleTool && strings.Contains(m.Content, "TRUNCATED") {
			found = true
		}
	}
	if !found {
		t.Error("expected truncated tool output to carry a TRUNCATED notice in the next request")
	}
}

func TestAgent_InterruptedContext(t *testing.T) {
	server := newScriptedServer(toolCallResponse("c1", "slow_tool", `{}`))
	registry := tools.NewRegistry()
	registry.Register(&fakeTool{name: "slow_tool", fn: func(n int, args map[string]any) *tools.ToolResult {
		return &tools.ToolResult{Success: true, Output: "ok"}
	}})

	a := newTestAgent(t, server, registry, nil)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already canceled before Run starts

	result := a.Run(ctx, "system", "task")
	if result.Outcome != OutcomeInterrupted {
		t.Fatalf("Outcome = %v, want OutcomeInterrupted", result.Outcome)
	}
}

func TestAgent_FatalLLMErrorStopsLoop(t *testing.T) {
	httpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer httpServer.Close()

	cfg := config.Default()
	cfg.BaseURL = httpServer.URL
	llmClient := llm.NewClient(cfg)
	llmClient.MaxRetries = 0
	logger, _ := logging.New(t.TempDir(), "")
	defer logger.Close()
	ctxManager := ctxmgr.NewManager(cfg.ContextConfig, llmClient)
	a := New(cfg, llmClient, tools.NewRegistry(), ctxManager, logger)

	result := a.Run(context.Background(), "system", "task")
	if result.Outcome != OutcomeLLMError {
		t.Fatalf("Outcome = %v, want OutcomeLLMError", result.Outcome)
	}
	if result.Err == nil {
		t.Error("expected a non-nil Err for OutcomeLLMError")
	}
}

func TestAgent_Run_PreservesFinalAssistantMessageInHistory(t *testing.T) {
	// Regression test: runTurn must append the assistant's final answer
	// to the returned message history (not just report it as
	// Result.FinalMessage), otherwise a subsequent turn in an
	// interactive session would lose it entirely.
	server := newScriptedServer(finalResponse("the answer"))
	a := newTestAgent(t, server, tools.NewRegistry(), nil)

	messages := []llm.Message{
		{Role: llm.RoleSystem, Content: "system"},
		{Role: llm.RoleUser, Content: "task"},
	}
	updated, result := a.runTurn(context.Background(), messages, 1)
	if result.Outcome != OutcomeSuccess {
		t.Fatalf("Outcome = %v, want OutcomeSuccess", result.Outcome)
	}
	last := updated[len(updated)-1]
	if last.Role != llm.RoleAssistant || last.Content != "the answer" {
		t.Fatalf("expected final assistant message appended to history, got: %+v", last)
	}
}

func TestAgent_RunInteractive_MultipleTurnsPreserveHistory(t *testing.T) {
	server := newScriptedServer(
		finalResponse("Reply 1"),
		finalResponse("Reply 2"),
	)
	a := newTestAgent(t, server, tools.NewRegistry(), nil)

	prompts := []string{"hello", "world"}
	promptIdx := 0
	readPrompt := func() (string, bool) {
		if promptIdx >= len(prompts) {
			return "", false
		}
		p := prompts[promptIdx]
		promptIdx++
		return p, true
	}

	var turnResults []Result
	onTurnResult := func(turn int, result Result) bool {
		turnResults = append(turnResults, result)
		return true
	}

	result := a.RunInteractive(context.Background(), "system prompt", readPrompt, onTurnResult)

	if result.Outcome != OutcomeSuccess {
		t.Fatalf("Outcome = %v, want OutcomeSuccess, err: %v", result.Outcome, result.Err)
	}
	if result.Steps != 2 {
		t.Errorf("Steps (turn count) = %d, want 2", result.Steps)
	}
	if len(turnResults) != 2 {
		t.Fatalf("expected 2 turn results, got %d", len(turnResults))
	}
	if turnResults[0].FinalMessage != "Reply 1" || turnResults[1].FinalMessage != "Reply 2" {
		t.Errorf("unexpected turn replies: %+v", turnResults)
	}

	// The second request must carry the first turn's user prompt AND
	// assistant reply, proving history persists across turns.
	if len(server.requests) != 2 {
		t.Fatalf("expected 2 LLM requests, got %d", len(server.requests))
	}
	secondReq := server.requests[1]
	var sawFirstPrompt, sawFirstReply bool
	for _, m := range secondReq.Messages {
		if m.Role == llm.RoleUser && m.Content == "hello" {
			sawFirstPrompt = true
		}
		if m.Role == llm.RoleAssistant && m.Content == "Reply 1" {
			sawFirstReply = true
		}
	}
	if !sawFirstPrompt || !sawFirstReply {
		t.Errorf("expected second request to include first turn's prompt and reply, messages: %+v", secondReq.Messages)
	}
}

func TestAgent_RunInteractive_StopsEarlyOnFatalError(t *testing.T) {
	httpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer httpServer.Close()

	cfg := config.Default()
	cfg.BaseURL = httpServer.URL
	llmClient := llm.NewClient(cfg)
	llmClient.MaxRetries = 0
	logger, _ := logging.New(t.TempDir(), "")
	defer logger.Close()
	ctxManager := ctxmgr.NewManager(cfg.ContextConfig, llmClient)
	a := New(cfg, llmClient, tools.NewRegistry(), ctxManager, logger)

	calls := 0
	readPrompt := func() (string, bool) {
		calls++
		return "hi", true
	}
	var stopped bool
	onTurnResult := func(turn int, result Result) bool {
		stopped = true
		return false // simulate cliapp deciding to stop the session
	}

	result := a.RunInteractive(context.Background(), "system", readPrompt, onTurnResult)
	if result.Outcome != OutcomeLLMError {
		t.Fatalf("Outcome = %v, want OutcomeLLMError", result.Outcome)
	}
	if !stopped {
		t.Error("expected onTurnResult to have been called")
	}
	if calls != 1 {
		t.Errorf("expected readPrompt called exactly once before stopping, got %d", calls)
	}
}

func TestAgent_RunInteractive_EndsCleanlyWhenReadPromptReturnsFalse(t *testing.T) {
	server := newScriptedServer(finalResponse("unused"))
	a := newTestAgent(t, server, tools.NewRegistry(), nil)

	readPrompt := func() (string, bool) { return "", false } // immediate EOF/"/exit"
	onTurnResult := func(turn int, result Result) bool {
		t.Fatal("onTurnResult should not be called when no turn ever runs")
		return false
	}

	result := a.RunInteractive(context.Background(), "system", readPrompt, onTurnResult)
	if result.Outcome != OutcomeSuccess {
		t.Fatalf("Outcome = %v, want OutcomeSuccess", result.Outcome)
	}
	if result.Steps != 0 {
		t.Errorf("Steps (turn count) = %d, want 0", result.Steps)
	}
	if len(server.requests) != 0 {
		t.Errorf("expected no LLM requests when the session ends before any turn, got %d", len(server.requests))
	}
}

func TestAgent_RunInteractive_LoopGuardResetsPerTurn(t *testing.T) {
	// A repeated-call pattern detected in turn 1 must not carry over and
	// immediately trigger in turn 2, which makes an unrelated call.
	responses := []llm.ChatResponse{
		toolCallResponse("c1", "stuck_tool", `{"x":1}`),
		toolCallResponse("c2", "stuck_tool", `{"x":1}`),
		toolCallResponse("c3", "stuck_tool", `{"x":1}`),
		finalResponse("turn 1 done"),
		toolCallResponse("c4", "stuck_tool", `{"x":1}`), // turn 2, first call: must NOT be flagged yet
		finalResponse("turn 2 done"),
	}
	server := newScriptedServer(responses...)
	registry := tools.NewRegistry()
	registry.Register(&fakeTool{name: "stuck_tool", fn: func(n int, args map[string]any) *tools.ToolResult {
		return &tools.ToolResult{Success: false, Error: "always fails the same way"}
	}})

	a := newTestAgent(t, server, registry, func(c *config.LitegentConfig) {
		c.MaxSteps = 5
		c.MaxRepeatedToolCalls = 2
	})

	prompts := []string{"go", "go again"}
	promptIdx := 0
	readPrompt := func() (string, bool) {
		if promptIdx >= len(prompts) {
			return "", false
		}
		p := prompts[promptIdx]
		promptIdx++
		return p, true
	}
	onTurnResult := func(turn int, result Result) bool { return true }

	result := a.RunInteractive(context.Background(), "system", readPrompt, onTurnResult)
	if result.Outcome != OutcomeSuccess {
		t.Fatalf("Outcome = %v, want OutcomeSuccess, err: %v", result.Outcome, result.Err)
	}

	// Requests 0-2 carry turn 1's three stuck_tool rounds (the 3rd one
	// trips the loop guard), request 3 gets turn 1's final response,
	// request 4 carries turn 2's first (and only) stuck_tool call, and
	// request 5 — sent after that call's result is appended — is where
	// its tool-result message would show a loop-guard notice if one
	// were (incorrectly) carried over from turn 1.
	if len(server.requests) != 6 {
		t.Fatalf("expected 6 LLM requests, got %d", len(server.requests))
	}
	turn2FirstToolResultReq := server.requests[5]
	var sawC4Result bool
	for _, m := range turn2FirstToolResultReq.Messages {
		if m.Role == llm.RoleTool && m.ToolCallID == "c4" {
			sawC4Result = true
			if strings.Contains(m.Content, "[LOOP DETECTED]") {
				t.Errorf("loop guard incorrectly carried over from turn 1 into turn 2's first call: %+v", m)
			}
		}
	}
	if !sawC4Result {
		t.Fatal("expected to find call c4's tool result in the request history")
	}
}
