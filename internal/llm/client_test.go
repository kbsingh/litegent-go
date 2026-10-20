package llm

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"litegent/internal/config"
)

func newTestClient(t *testing.T, server *httptest.Server) *Client {
	t.Helper()
	cfg := config.Default()
	cfg.BaseURL = server.URL
	cfg.APIKey = "test-key"
	cfg.Model = "test-model"
	c := NewClient(cfg)
	c.MaxRetries = 2
	return c
}

func TestChatCompletion_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-key" {
			t.Errorf("missing/incorrect Authorization header: %q", r.Header.Get("Authorization"))
		}
		var req ChatRequest
		json.NewDecoder(r.Body).Decode(&req)
		if req.Model != "test-model" {
			t.Errorf("Model = %q, want test-model", req.Model)
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(ChatResponse{
			Choices: []Choice{{Message: Message{Role: RoleAssistant, Content: "hello"}, FinishReason: "stop"}},
			Usage:   Usage{TotalTokens: 42},
		})
	}))
	defer server.Close()

	c := newTestClient(t, server)
	resp, err := c.ChatCompletion(context.Background(), []Message{{Role: RoleUser, Content: "hi"}}, nil)
	if err != nil {
		t.Fatalf("ChatCompletion returned error: %v", err)
	}
	if len(resp.Choices) != 1 || resp.Choices[0].Message.Content != "hello" {
		t.Errorf("unexpected response: %+v", resp)
	}
}

func TestChatCompletion_RetriesOn500ThenSucceeds(t *testing.T) {
	var calls int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&calls, 1)
		if n < 2 {
			w.WriteHeader(http.StatusInternalServerError)
			w.Write([]byte("server error"))
			return
		}
		json.NewEncoder(w).Encode(ChatResponse{Choices: []Choice{{Message: Message{Role: RoleAssistant, Content: "ok"}}}})
	}))
	defer server.Close()

	c := newTestClient(t, server)
	resp, err := c.ChatCompletion(context.Background(), []Message{{Role: RoleUser, Content: "hi"}}, nil)
	if err != nil {
		t.Fatalf("ChatCompletion returned error: %v", err)
	}
	if resp.Choices[0].Message.Content != "ok" {
		t.Errorf("unexpected content: %q", resp.Choices[0].Message.Content)
	}
	if atomic.LoadInt32(&calls) != 2 {
		t.Errorf("expected exactly 2 calls (1 failure + 1 retry), got %d", calls)
	}
}

func TestChatCompletion_AuthErrorFailsImmediately(t *testing.T) {
	var calls int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte("bad key"))
	}))
	defer server.Close()

	c := newTestClient(t, server)
	_, err := c.ChatCompletion(context.Background(), []Message{{Role: RoleUser, Content: "hi"}}, nil)
	if err == nil {
		t.Fatal("expected an error for 401 response")
	}
	if atomic.LoadInt32(&calls) != 1 {
		t.Errorf("expected exactly 1 call (no retry on auth failure), got %d", calls)
	}
}

func TestChatCompletion_ExhaustsRetriesOnPersistentFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()

	c := newTestClient(t, server)
	c.MaxRetries = 1
	start := time.Now()
	_, err := c.ChatCompletion(context.Background(), []Message{{Role: RoleUser, Content: "hi"}}, nil)
	if err == nil {
		t.Fatal("expected an error after exhausting retries")
	}
	if time.Since(start) > 15*time.Second {
		t.Errorf("retry took too long: %v", time.Since(start))
	}
}

func TestChatCompletion_SummaryRoleTranslatedForWire(t *testing.T) {
	var seenRoles []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req ChatRequest
		json.NewDecoder(r.Body).Decode(&req)
		for _, m := range req.Messages {
			seenRoles = append(seenRoles, m.Role)
		}
		json.NewEncoder(w).Encode(ChatResponse{Choices: []Choice{{Message: Message{Role: RoleAssistant, Content: "ok"}}}})
	}))
	defer server.Close()

	c := newTestClient(t, server)
	_, err := c.ChatCompletion(context.Background(), []Message{
		{Role: RoleSystem, Content: "sys"},
		{Role: RoleSummary, Content: "earlier findings"},
		{Role: RoleUser, Content: "hi"},
	}, nil)
	if err != nil {
		t.Fatalf("ChatCompletion returned error: %v", err)
	}
	for _, r := range seenRoles {
		if r == RoleSummary {
			t.Fatalf("RoleSummary must never be sent over the wire, got roles: %v", seenRoles)
		}
	}
}

// captureBody returns a handler that echoes one canned response and
// records the raw request body for capturedRequest to assert on.
func captureBody() (http.HandlerFunc, <-chan string) {
	ch := make(chan string, 1)
	return func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		ch <- string(body)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(ChatResponse{Choices: []Choice{{Message: Message{Role: RoleAssistant, Content: "ok"}}}})
	}, ch
}

// decodeRequest re-decodes a captured raw request body into ChatRequest.
func decodeRequest(t *testing.T, raw string) ChatRequest {
	t.Helper()
	var req ChatRequest
	if err := json.Unmarshal([]byte(raw), &req); err != nil {
		t.Fatalf("unmarshaling captured request: %v", err)
	}
	return req
}

func TestChatCompletion_ThinkingDefaultSendsNothing(t *testing.T) {
	handler, bodies := captureBody()
	server := httptest.NewServer(handler)
	defer server.Close()

	cfg := config.Default()
	cfg.BaseURL = server.URL
	cfg.APIKey = "test-key"
	cfg.Model = "test-model"
	c := NewClient(cfg)
	if _, err := c.ChatCompletion(context.Background(), []Message{{Role: RoleUser, Content: "hi"}}, nil); err != nil {
		t.Fatalf("ChatCompletion returned error: %v", err)
	}

	raw := <-bodies
	if strings.Contains(raw, "reasoning_effort") {
		t.Errorf("default thinking_level must not send reasoning_effort, got: %s", raw)
	}
	if strings.Contains(raw, "thinking") {
		t.Errorf("default thinking_level must not send thinking, got: %s", raw)
	}
}

func TestChatCompletion_ThinkingReasoningEffort(t *testing.T) {
	handler, bodies := captureBody()
	server := httptest.NewServer(handler)
	defer server.Close()

	cfg := config.Default()
	cfg.BaseURL = server.URL
	cfg.APIKey = "test-key"
	cfg.Model = "test-model"
	cfg.ThinkingLevel = "High"
	c := NewClient(cfg)
	if c.ThinkingLevel != "High" {
		t.Fatalf("active thinking_level not carried onto the client, got %q", c.ThinkingLevel)
	}
	if _, err := c.ChatCompletion(context.Background(), []Message{{Role: RoleUser, Content: "hi"}}, nil); err != nil {
		t.Fatalf("ChatCompletion returned error: %v", err)
	}

	req := decodeRequest(t, <-bodies)
	if req.ReasoningEffort != "High" {
		t.Errorf("ReasoningEffort = %q, want \"High\" preserved verbatim", req.ReasoningEffort)
	}
	if req.Thinking != nil {
		t.Errorf("thinking object must not be sent in reasoning_effort format, got: %+v", req.Thinking)
	}
}

func TestChatCompletion_ThinkingObject(t *testing.T) {
	handler, bodies := captureBody()
	server := httptest.NewServer(handler)
	defer server.Close()

	cfg := config.Default()
	cfg.BaseURL = server.URL
	cfg.APIKey = "test-key"
	cfg.Model = "test-model"
	cfg.ThinkingLevel = "enabled"
	cfg.ThinkingFormat = config.ThinkingFormatThinking
	c := NewClient(cfg)
	if _, err := c.ChatCompletion(context.Background(), []Message{{Role: RoleUser, Content: "hi"}}, nil); err != nil {
		t.Fatalf("ChatCompletion returned error: %v", err)
	}

	req := decodeRequest(t, <-bodies)
	if req.Thinking == nil || req.Thinking.Type != "enabled" {
		t.Errorf("Thinking = %+v, want {Type: \"enabled\"}", req.Thinking)
	}
	if req.ReasoningEffort != "" {
		t.Errorf("reasoning_effort must not be sent in thinking format, got: %q", req.ReasoningEffort)
	}
}

func TestSummarize(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req ChatRequest
		json.NewDecoder(r.Body).Decode(&req)
		if !strings.Contains(req.Messages[1].Content, "did a thing") {
			t.Errorf("expected transcript to include evicted message content, got: %q", req.Messages[1].Content)
		}
		json.NewEncoder(w).Encode(ChatResponse{Choices: []Choice{{Message: Message{Role: RoleAssistant, Content: "Agent did a thing successfully."}}}})
	}))
	defer server.Close()

	c := newTestClient(t, server)
	summary, err := c.Summarize(context.Background(), []Message{{Role: RoleTool, Content: "did a thing"}})
	if err != nil {
		t.Fatalf("Summarize returned error: %v", err)
	}
	if summary == "" {
		t.Fatal("expected non-empty summary")
	}
}
