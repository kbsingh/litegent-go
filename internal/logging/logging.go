// Package logging implements Litegent's dual audit log: an append-only
// machine-readable JSONL event trace and a formatted human-readable
// session transcript. Because Litegent runs unconfined (no sandbox —
// see req/requirements_doc.md NFR-3.4), this log is the system's primary
// safety and accountability control, so every LLM and tool interaction
// must be captured here. See req/spec_doc.md section 2.7.
package logging

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Event type constants, per req/requirements_doc.md FR-6.2.
const (
	EventSessionStart       = "session_start"
	EventLLMRequest         = "llm_request"
	EventLLMResponse        = "llm_response"
	EventToolCall           = "tool_call"
	EventToolResult         = "tool_result"
	EventContextTruncation  = "context_truncation"
	EventContextPruning     = "context_pruning"
	EventContextSummary     = "context_summary"
	EventLoopGuardTriggered = "loop_guard_triggered"
	EventSessionFinish      = "session_finish"
	EventSessionError       = "session_error"
)

// TraceEvent is a single line written to trace_<timestamp>.jsonl.
type TraceEvent struct {
	Timestamp string         `json:"timestamp"`
	EventType string         `json:"event_type"`
	Step      int            `json:"step"`
	Data      map[string]any `json:"data"`
}

// Logger writes the dual audit log for one Litegent session.
type Logger struct {
	mu          sync.Mutex
	traceFile   *os.File
	sessionFile *os.File
	apiKey      string // redacted from every logged string; never empty-checked against ""

	TracePath   string
	SessionPath string
}

// New creates logDir if needed and opens trace_<ts>.jsonl and
// session_<ts>.log for the given UTC timestamp tag. apiKey, if non-empty,
// is redacted (masked) out of every value written to either file.
func New(logDir, apiKey string) (*Logger, error) {
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		return nil, fmt.Errorf("creating log dir %q: %w", logDir, err)
	}
	ts := time.Now().UTC().Format("20060102_150405")
	tracePath := filepath.Join(logDir, fmt.Sprintf("trace_%s.jsonl", ts))
	sessionPath := filepath.Join(logDir, fmt.Sprintf("session_%s.log", ts))

	traceFile, err := os.OpenFile(tracePath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, fmt.Errorf("opening trace file: %w", err)
	}
	sessionFile, err := os.OpenFile(sessionPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		traceFile.Close()
		return nil, fmt.Errorf("opening session file: %w", err)
	}

	return &Logger{
		traceFile:   traceFile,
		sessionFile: sessionFile,
		apiKey:      apiKey,
		TracePath:   tracePath,
		SessionPath: sessionPath,
	}, nil
}

// Close flushes and closes both log files.
func (l *Logger) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	err1 := l.traceFile.Close()
	err2 := l.sessionFile.Close()
	if err1 != nil {
		return err1
	}
	return err2
}

func nowUTC() string { return time.Now().UTC().Format(time.RFC3339Nano) }

// redact masks any occurrence of the configured API key in s. The mask
// preserves the key's own stub prefix (e.g. "sk-...", "r9_..."); keys of
// 7 characters or fewer are masked entirely, since revealing a 3+4
// character prefix/suffix would expose the whole key.
func (l *Logger) redact(s string) string {
	if l.apiKey == "" || !strings.Contains(s, l.apiKey) {
		return s
	}
	masked := "****"
	if len(l.apiKey) > 7 {
		masked = l.apiKey[:3] + "..." + l.apiKey[len(l.apiKey)-4:]
	}
	return strings.ReplaceAll(s, l.apiKey, masked)
}

func (l *Logger) redactValue(v any) any {
	switch s := v.(type) {
	case string:
		return l.redact(s)
	default:
		return v
	}
}

func (l *Logger) logTrace(eventType string, step int, data map[string]any) error {
	redacted := make(map[string]any, len(data))
	for k, v := range data {
		redacted[k] = l.redactValue(v)
	}
	ev := TraceEvent{Timestamp: nowUTC(), EventType: eventType, Step: step, Data: redacted}
	b, err := json.Marshal(ev)
	if err != nil {
		return fmt.Errorf("marshaling trace event: %w", err)
	}

	l.mu.Lock()
	defer l.mu.Unlock()
	_, err = l.traceFile.Write(append(b, '\n'))
	return err
}

func (l *Logger) writeSession(s string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	_, err := l.sessionFile.WriteString(l.redact(s))
	return err
}

// LogSessionStart records the session header in both logs.
func (l *Logger) LogSessionStart(sessionID, model, baseURL string) error {
	if err := l.logTrace(EventSessionStart, 0, map[string]any{
		"session_id": sessionID, "model": model, "base_url": baseURL,
	}); err != nil {
		return err
	}
	header := fmt.Sprintf(
		"%s\nLitegent Session: %s\nStarted: %s\nModel: %s\nBase URL: %s\n%s\n",
		strings.Repeat("=", 80), sessionID, nowUTC(), model, baseURL, strings.Repeat("=", 80),
	)
	return l.writeSession(header)
}

// LogLLMRequest records that the agent is about to call the LLM.
func (l *Logger) LogLLMRequest(step, tokenEstimate int) error {
	if err := l.logTrace(EventLLMRequest, step, map[string]any{"token_estimate": tokenEstimate}); err != nil {
		return err
	}
	return l.writeSession(fmt.Sprintf("[%s] STEP %d | LLM Request (tokens: ~%d)\n", nowUTC(), step, tokenEstimate))
}

// LogLLMResponse records the model's reply, including its "thought"
// (the natural-language content accompanying/standing in for tool calls)
// and token usage.
func (l *Logger) LogLLMResponse(step int, thought string, promptTokens, completionTokens, totalTokens int) error {
	if err := l.logTrace(EventLLMResponse, step, map[string]any{
		"thought":           thought,
		"prompt_tokens":     promptTokens,
		"completion_tokens": completionTokens,
		"total_tokens":      totalTokens,
	}); err != nil {
		return err
	}
	if thought == "" {
		return nil
	}
	return l.writeSession(fmt.Sprintf("[%s] STEP %d | LLM Thought:\n%s\n", nowUTC(), step, thought))
}

// LogToolCall records a tool invocation before it executes.
func (l *Logger) LogToolCall(step int, callID, name string, args map[string]any) error {
	if err := l.logTrace(EventToolCall, step, map[string]any{
		"call_id": callID, "tool_name": name, "args": args,
	}); err != nil {
		return err
	}
	return l.writeSession(fmt.Sprintf("[%s] STEP %d | Tool Call:\n%s\n", nowUTC(), step, formatCall(name, args)))
}

// LogToolResult records the outcome of a tool call.
func (l *Logger) LogToolResult(step int, callID, name string, success bool, exitCode int, summary string, latencyMs float64) error {
	if err := l.logTrace(EventToolResult, step, map[string]any{
		"call_id": callID, "tool_name": name, "success": success,
		"exit_code": exitCode, "latency_ms": latencyMs,
	}); err != nil {
		return err
	}
	return l.writeSession(fmt.Sprintf("[%s] STEP %d | Tool Result (exit_code=%d):\n%s\n", nowUTC(), step, exitCode, summary))
}

// LogContextTruncation records that a tool output was truncated/spooled.
func (l *Logger) LogContextTruncation(step int, callID, spoolFile string, omittedChars int) error {
	return l.logTrace(EventContextTruncation, step, map[string]any{
		"call_id": callID, "spool_file": spoolFile, "omitted_chars": omittedChars,
	})
}

// LogContextSummary records that evicted history was summarized.
func (l *Logger) LogContextSummary(step int, summary string) error {
	return l.logTrace(EventContextSummary, step, map[string]any{"summary": summary})
}

// LogLoopGuardTriggered records that the repeated tool-call guard fired.
func (l *Logger) LogLoopGuardTriggered(step int, toolName string, args map[string]any, repeatCount int) error {
	if err := l.logTrace(EventLoopGuardTriggered, step, map[string]any{
		"tool_name": toolName, "args": args, "repeat_count": repeatCount,
	}); err != nil {
		return err
	}
	return l.writeSession(fmt.Sprintf("[%s] STEP %d | [LOOP DETECTED] %s repeated %d times with the same result\n", nowUTC(), step, toolName, repeatCount))
}

// LogFinish records successful, natural session completion.
func (l *Logger) LogFinish(step int, finalMessage string, totalSteps, totalTokens int) error {
	if err := l.logTrace(EventSessionFinish, step, map[string]any{
		"final_message": finalMessage, "total_steps": totalSteps, "total_tokens": totalTokens,
	}); err != nil {
		return err
	}
	footer := fmt.Sprintf(
		"%s\nFinal Response:\n%s\nCompleted: %s\nTotal Steps: %d\nTotal Tokens: %d\n%s\n",
		strings.Repeat("=", 80), finalMessage, nowUTC(), totalSteps, totalTokens, strings.Repeat("=", 80),
	)
	return l.writeSession(footer)
}

// LogSessionError records a fatal session-ending error.
func (l *Logger) LogSessionError(step int, errMsg string) error {
	if err := l.logTrace(EventSessionError, step, map[string]any{"error": errMsg}); err != nil {
		return err
	}
	return l.writeSession(fmt.Sprintf("[%s] STEP %d | SESSION ERROR: %s\n", nowUTC(), step, errMsg))
}

// formatCall renders a tool call as a readable function-call expression,
// e.g. bash(command="go test ./..."). Argument keys are sorted so the
// same call always renders identically across log lines/runs (map
// iteration order would otherwise be randomized per the Go spec).
func formatCall(name string, args map[string]any) string {
	keys := make([]string, 0, len(args))
	for k := range args {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s=%q", k, fmt.Sprint(args[k])))
	}
	return fmt.Sprintf("%s(%s)", name, strings.Join(parts, ", "))
}
