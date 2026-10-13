// Package llm implements a minimal client for any OpenAI-compatible
// /chat/completions endpoint, talking directly over net/http (no vendor
// SDK dependency). See req/spec_doc.md section 2.3.
package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"strings"
	"time"

	"litegent/internal/config"
)

// Roles used in the internal message representation. RoleSummary is a
// Litegent-internal pseudo-role for the persistent <context_summary>
// message (req/spec_doc.md section 2.5); it is translated to a regular
// "system" message, wrapped in <context_summary> tags, before being sent
// over the wire — no real OpenAI-compatible API accepts "summary" as a role.
const (
	RoleSystem    = "system"
	RoleUser      = "user"
	RoleAssistant = "assistant"
	RoleTool      = "tool"
	RoleSummary   = "summary"
)

// FunctionCall is the function name/arguments payload of a tool call, per
// the OpenAI function-calling wire format. Arguments is a JSON-encoded
// string, not a nested object, matching the API's actual shape.
type FunctionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// ToolCall is a single tool invocation requested by the model.
type ToolCall struct {
	ID       string       `json:"id"`
	Type     string       `json:"type"`
	Function FunctionCall `json:"function"`
}

// Message is Litegent's internal chat message representation, shared by
// internal/agent, internal/ctxmgr, and internal/logging.
type Message struct {
	Role       string     `json:"role"`
	Content    string     `json:"content,omitempty"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
}

// toWire converts internal messages to the wire format accepted by an
// OpenAI-compatible endpoint, translating the internal RoleSummary
// pseudo-role into a regular system message.
func toWire(messages []Message) []Message {
	wire := make([]Message, len(messages))
	for i, m := range messages {
		if m.Role == RoleSummary {
			wire[i] = Message{
				Role:    RoleSystem,
				Content: "<context_summary>\n" + m.Content + "\n</context_summary>",
			}
			continue
		}
		wire[i] = m
	}
	return wire
}

// ChatRequest is the request body sent to POST /chat/completions.
type ChatRequest struct {
	Model       string           `json:"model"`
	Messages    []Message        `json:"messages"`
	Tools       []map[string]any `json:"tools,omitempty"`
	ToolChoice  string           `json:"tool_choice,omitempty"`
	Temperature float64          `json:"temperature"`
	MaxTokens   int              `json:"max_tokens,omitempty"`
}

// Choice is a single completion choice in a ChatResponse.
type Choice struct {
	Index        int     `json:"index"`
	Message      Message `json:"message"`
	FinishReason string  `json:"finish_reason"`
}

// Usage reports token consumption for a single request.
type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// ChatResponse is the response body from POST /chat/completions.
type ChatResponse struct {
	ID      string   `json:"id"`
	Model   string   `json:"model"`
	Choices []Choice `json:"choices"`
	Usage   Usage    `json:"usage"`
}

// APIError represents a non-retryable error response from the LLM
// provider (e.g. authentication failure).
type APIError struct {
	StatusCode int
	Body       string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("llm API error (status %d): %s", e.StatusCode, e.Body)
}

// Client is a minimal OpenAI-compatible chat completions client with
// retry/backoff for transient failures.
type Client struct {
	BaseURL     string
	APIKey      string
	Model       string
	Temperature float64
	MaxTokens   int
	HTTPClient  *http.Client
	MaxRetries  int
}

// NewClient builds a Client from the loaded Litegent configuration.
func NewClient(cfg config.LitegentConfig) *Client {
	return &Client{
		BaseURL:     strings.TrimRight(cfg.BaseURL, "/"),
		APIKey:      cfg.APIKey,
		Model:       cfg.Model,
		Temperature: cfg.Temperature,
		MaxTokens:   cfg.MaxTokens,
		HTTPClient:  &http.Client{Timeout: time.Duration(cfg.Timeout * float64(time.Second))},
		MaxRetries:  3,
	}
}

// retryableStatus reports whether an HTTP status code warrants a retry.
func retryableStatus(code int) bool {
	switch code {
	case http.StatusTooManyRequests, http.StatusInternalServerError,
		http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return true
	default:
		return false
	}
}

// ChatCompletion sends messages and tool declarations to the configured
// LLM endpoint, retrying transient failures (429/5xx/network errors) with
// exponential backoff and jitter (up to MaxRetries). Authentication
// failures (401/403) fail immediately without retry.
func (c *Client) ChatCompletion(ctx context.Context, messages []Message, tools []map[string]any) (*ChatResponse, error) {
	reqBody := ChatRequest{
		Model:       c.Model,
		Messages:    toWire(messages),
		Temperature: c.Temperature,
		MaxTokens:   c.MaxTokens,
	}
	if len(tools) > 0 {
		reqBody.Tools = tools
		reqBody.ToolChoice = "auto"
	}
	body, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("marshaling chat request: %w", err)
	}

	var lastErr error
	for attempt := 0; attempt <= c.MaxRetries; attempt++ {
		if attempt > 0 {
			delay := backoffDelay(attempt)
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(delay):
			}
		}

		resp, err := c.doRequest(ctx, body)
		if err != nil {
			lastErr = err
			continue
		}
		if resp.retryable {
			lastErr = resp.err
			continue
		}
		return resp.chat, resp.err
	}
	return nil, fmt.Errorf("llm request failed after %d retries: %w", c.MaxRetries, lastErr)
}

type requestOutcome struct {
	chat      *ChatResponse
	err       error
	retryable bool
}

func (c *Client) doRequest(ctx context.Context, body []byte) (requestOutcome, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return requestOutcome{}, fmt.Errorf("building request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if c.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.APIKey)
	}

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		// Network-level failure (timeout, connection refused, ...): retryable.
		return requestOutcome{}, err
	}
	defer resp.Body.Close()

	respBody, readErr := io.ReadAll(resp.Body)
	if readErr != nil {
		return requestOutcome{}, fmt.Errorf("reading response body: %w", readErr)
	}

	switch {
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return requestOutcome{err: &APIError{StatusCode: resp.StatusCode, Body: string(respBody)}}, nil
	case retryableStatus(resp.StatusCode):
		return requestOutcome{err: &APIError{StatusCode: resp.StatusCode, Body: string(respBody)}, retryable: true}, nil
	case resp.StatusCode >= 400:
		return requestOutcome{err: &APIError{StatusCode: resp.StatusCode, Body: string(respBody)}}, nil
	}

	var chatResp ChatResponse
	if err := json.Unmarshal(respBody, &chatResp); err != nil {
		return requestOutcome{}, fmt.Errorf("decoding response: %w", err)
	}
	return requestOutcome{chat: &chatResp}, nil
}

// backoffDelay computes exponential backoff with jitter: base * 2^(attempt-1)
// plus up to 25% random jitter, capped at 10s.
func backoffDelay(attempt int) time.Duration {
	const base = 500 * time.Millisecond
	const maxDelay = 10 * time.Second

	delay := base << (attempt - 1)
	if delay > maxDelay || delay <= 0 {
		delay = maxDelay
	}
	jitter := time.Duration(rand.Int63n(int64(delay)/4 + 1))
	return delay + jitter
}

// Summarize implements ctxmgr.Summarizer by asking the model to condense
// evicted conversation history into a short paragraph.
func (c *Client) Summarize(ctx context.Context, messages []Message) (string, error) {
	var transcript strings.Builder
	for _, m := range messages {
		fmt.Fprintf(&transcript, "[%s] %s\n", m.Role, m.Content)
	}

	prompt := []Message{
		{Role: RoleSystem, Content: "Summarize the following agent conversation history concisely, preserving key findings, decisions, and unresolved issues. Respond with only the summary text."},
		{Role: RoleUser, Content: transcript.String()},
	}

	resp, err := c.ChatCompletion(ctx, prompt, nil)
	if err != nil {
		return "", fmt.Errorf("summarization request failed: %w", err)
	}
	if len(resp.Choices) == 0 {
		return "", fmt.Errorf("summarization response contained no choices")
	}
	return resp.Choices[0].Message.Content, nil
}
