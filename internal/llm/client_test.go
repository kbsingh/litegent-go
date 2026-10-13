package llm

import (
	"context"
	"encoding/json"
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
