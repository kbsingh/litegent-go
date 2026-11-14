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
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"litegent/internal/llm"
)

// Event type constants.
const (
	EventSessionStart       = "session_start"
	EventUserPrompt         = "user_prompt"
	EventLLMRequest         = "llm_request"
	EventLLMResponse        = "llm_response"
	EventToolCall           = "tool_call"
	EventToolResult         = "tool_result"
	EventContextTruncation  = "context_truncation"
	EventContextPruning     = "context_pruning"
	EventContextSummary     = "context_summary"
	EventLoopGuardTriggered = "loop_guard_triggered"
	EventTurnFinish         = "turn_finish"
	EventSessionFinish      = "session_finish"
	EventRunFinish          = "run_finish"
	EventSessionError       = "session_error"
)

const divider = "--------------------------------------------------------------------------------"

// TraceEvent is a single line written to trace_<run-id>.jsonl.
type TraceEvent struct {
	Timestamp string         `json:"timestamp"`
	EventType string         `json:"event_type"`
	Turn      int            `json:"turn"`
	Step      int            `json:"step"`
	Data      map[string]any `json:"data"`
}

// Logger writes the dual audit log for one Litegent session run.
type Logger struct {
	mu          sync.Mutex
	traceFile   *os.File
	sessionFile *os.File
	apiKey      string // redacted from every logged string
	err         error  // first write/sync error, never cleared

	RunID       string
	TracePath   string
	SessionPath string
}

// New creates logDir if needed and opens a run-scoped trace/session pair.
func New(logDir, apiKey string) (*Logger, error) {
	return NewWithRunID(logDir, apiKey, newRunID())
}

// NewWithRunID opens trace_<runID>.jsonl and session_<runID>.log. The run
// ID is chosen by the caller so the session manifest and log filenames can
// share one identifier.
func NewWithRunID(logDir, apiKey, runID string) (*Logger, error) {
	if runID == "" {
		return nil, fmt.Errorf("run id must not be empty")
	}
	if err := os.MkdirAll(logDir, 0o700); err != nil {
		return nil, fmt.Errorf("creating log dir %q: %w", logDir, err)
	}
	tracePath := filepath.Join(logDir, fmt.Sprintf("trace_%s.jsonl", runID))
	sessionPath := filepath.Join(logDir, fmt.Sprintf("session_%s.log", runID))

	traceFile, err := os.OpenFile(tracePath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, fmt.Errorf("opening trace file: %w", err)
	}
	sessionFile, err := os.OpenFile(sessionPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		traceFile.Close()
		return nil, fmt.Errorf("opening session file: %w", err)
	}

	return &Logger{
		traceFile:   traceFile,
		sessionFile: sessionFile,
		apiKey:      apiKey,
		RunID:       runID,
		TracePath:   tracePath,
		SessionPath: sessionPath,
	}, nil
}

var logRunCounter atomic.Uint64

func newRunID() string {
	return fmt.Sprintf(
		"%s-%d-%d",
		time.Now().UTC().Format("20060102T150405_000000000Z"),
		os.Getpid(),
		logRunCounter.Add(1),
	)
}

// Sync flushes both log files to stable storage.
func (l *Logger) Sync() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	var first error
	for _, f := range []*os.File{l.traceFile, l.sessionFile} {
		if f == nil {
			continue
		}
		if err := f.Sync(); err != nil {
			l.recordErr(err)
			if first == nil {
				first = err
			}
		}
	}
	return first
}

// Close syncs and closes both log files.
func (l *Logger) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	var first error
	for _, f := range []*os.File{l.traceFile, l.sessionFile} {
		if f == nil {
			continue
		}
		if err := f.Sync(); err != nil {
			l.recordErr(err)
			if first == nil {
				first = err
			}
		}
		if err := f.Close(); err != nil {
			l.recordErr(err)
			if first == nil {
				first = err
			}
		}
	}
	return first
}

// Err returns the first log write or sync error observed.
func (l *Logger) Err() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.err
}

// recordErr must be called with l.mu held.
func (l *Logger) recordErr(err error) {
	if err != nil && l.err == nil {
		l.err = err
	}
}

func nowUTC() string { return time.Now().UTC().Format(time.RFC3339Nano) }

// redact masks any occurrence of the configured API key in s.
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

// redactValue recursively masks strings inside maps and slices. Tool
// arguments are nested structures, so top-level-only redaction would leak
// secrets embedded in command arguments.
func (l *Logger) redactValue(v any) any {
	switch x := v.(type) {
	case string:
		return l.redact(x)
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, val := range x {
			out[k] = l.redactValue(val)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, val := range x {
			out[i] = l.redactValue(val)
		}
		return out
	case []string:
		out := make([]string, len(x))
		for i, val := range x {
			out[i] = l.redact(val)
		}
		return out
	default:
		return v
	}
}

func (l *Logger) logTrace(turn int, eventType string, step int, data map[string]any) error {
	redacted := make(map[string]any, len(data))
	for k, v := range data {
		redacted[k] = l.redactValue(v)
	}
	ev := TraceEvent{Timestamp: nowUTC(), EventType: eventType, Turn: turn, Step: step, Data: redacted}
	b, err := json.Marshal(ev)
	if err != nil {
		return fmt.Errorf("marshaling trace event: %w", err)
	}

	l.mu.Lock()
	defer l.mu.Unlock()
	_, err = l.traceFile.Write(append(b, '\n'))
	l.recordErr(err)
	return err
}

func (l *Logger) writeSession(s string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	_, err := l.sessionFile.WriteString(l.redact(s))
	l.recordErr(err)
	return err
}

// writeBlock writes a timestamped header followed by a fixed-delimiter
// content block. The stable structure keeps the human log readable and
// parseable.
func (l *Logger) writeBlock(header, content string) error {
	if content == "" {
		content = "(empty)"
	}
	return l.writeSession(fmt.Sprintf("[%s] %s\n%s\n%s\n%s\n", nowUTC(), header, divider, content, divider))
}

func (l *Logger) writeJSONBlock(header string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		data = []byte(fmt.Sprint(v))
	}
	return l.writeBlock(header, string(data))
}

// LogSessionStart records the session header, full system prompt, and run
// metadata in both logs.
func (l *Logger) LogSessionStart(sessionID, runID string, resumed bool, model, baseURL, workDir, systemPrompt string) error {
	if err := l.logTrace(0, EventSessionStart, 0, map[string]any{
		"session_id":    sessionID,
		"run_id":        runID,
		"resumed":       resumed,
		"model":         model,
		"base_url":      baseURL,
		"work_dir":      workDir,
		"system_prompt": systemPrompt,
	}); err != nil {
		return err
	}

	header := fmt.Sprintf(
		"%s\nLitegent Session: %s\nRun: %s\nResumed: %t\nStarted: %s\nModel: %s\nBase URL: %s\nWorking Dir: %s\n%s\n",
		strings.Repeat("=", 80), sessionID, runID, resumed, nowUTC(), model, baseURL, workDir, strings.Repeat("=", 80),
	)
	if err := l.writeSession(header); err != nil {
		return err
	}
	return l.writeBlock("SYSTEM PROMPT", systemPrompt)
}

// LogUserPrompt records the exact user input for a turn.
func (l *Logger) LogUserPrompt(turn int, prompt string) error {
	if err := l.logTrace(turn, EventUserPrompt, 0, map[string]any{"prompt": prompt}); err != nil {
		return err
	}
	return l.writeBlock(fmt.Sprintf("TURN %d | USER INPUT", turn), prompt)
}

// LogLLMRequest records the effective request shape before the call.
func (l *Logger) LogLLMRequest(turn, step, messageCount, tokenEstimate int, contextSHA string) error {
	data := map[string]any{
		"message_count":  messageCount,
		"token_estimate": tokenEstimate,
	}
	if contextSHA != "" {
		data["context_sha256"] = contextSHA
	}
	if err := l.logTrace(turn, EventLLMRequest, step, data); err != nil {
		return err
	}
	line := fmt.Sprintf("[%s] TURN %d STEP %d | LLM REQUEST | messages=%d tokens~=%d",
		nowUTC(), turn, step, messageCount, tokenEstimate)
	if contextSHA != "" {
		line += " context=sha256:" + contextSHA
	}
	return l.writeSession(line + "\n")
}

// LogLLMResponse records the assistant content and token usage. Tool calls
// are recorded separately by LogToolCall so execution can be correlated.
func (l *Logger) LogLLMResponse(turn, step int, content string, toolCalls []llm.ToolCall, usage llm.Usage) error {
	data := map[string]any{
		"thought":           content,
		"prompt_tokens":     usage.PromptTokens,
		"completion_tokens": usage.CompletionTokens,
		"total_tokens":      usage.TotalTokens,
	}
	if len(toolCalls) > 0 {
		calls := make([]map[string]any, 0, len(toolCalls))
		for _, tc := range toolCalls {
			calls = append(calls, map[string]any{
				"id":        tc.ID,
				"type":      tc.Type,
				"name":      tc.Function.Name,
				"arguments": tc.Function.Arguments,
			})
		}
		data["tool_calls"] = calls
	}
	if err := l.logTrace(turn, EventLLMResponse, step, data); err != nil {
		return err
	}
	if content == "" {
		content = "(no content)"
	}
	return l.writeBlock(fmt.Sprintf("TURN %d STEP %d | ASSISTANT", turn, step), content)
}

// LogToolCall records a tool invocation before it executes.
func (l *Logger) LogToolCall(turn, step int, callID, name string, args map[string]any) error {
	if err := l.logTrace(turn, EventToolCall, step, map[string]any{
		"call_id":   callID,
		"tool_name": name,
		"args":      args,
	}); err != nil {
		return err
	}
	return l.writeJSONBlock(
		fmt.Sprintf("TURN %d STEP %d | TOOL CALL %s | call_id=%s", turn, step, name, callID),
		args,
	)
}

// LogToolResult records the full raw output, plus the exact model-visible
// content when truncation or the loop guard changed it.
func (l *Logger) LogToolResult(turn, step int, callID, name string, success bool, exitCode int, rawOutput, modelVisible, spoolFile string, omittedChars int, latencyMs float64) error {
	data := map[string]any{
		"call_id":    callID,
		"tool_name":  name,
		"success":    success,
		"exit_code":  exitCode,
		"latency_ms": latencyMs,
		"output":     rawOutput,
	}
	if modelVisible != "" {
		data["model_content"] = modelVisible
	}
	if spoolFile != "" {
		data["spool_file"] = spoolFile
		data["omitted_chars"] = omittedChars
	}
	if err := l.logTrace(turn, EventToolResult, step, data); err != nil {
		return err
	}

	header := fmt.Sprintf(
		"TURN %d STEP %d | TOOL RESULT %s | call_id=%s success=%t exit_code=%d latency_ms=%.1f",
		turn, step, name, callID, success, exitCode, latencyMs,
	)
	if err := l.writeSession(fmt.Sprintf("[%s] %s\n", nowUTC(), header)); err != nil {
		return err
	}
	if err := l.writeBlock("OUTPUT", rawOutput); err != nil {
		return err
	}
	if modelVisible != "" {
		meta := fmt.Sprintf("TRUNCATION | omitted=%d | spool=%s", omittedChars, spoolFile)
		if spoolFile == "" {
			meta = "MODEL INPUT DIFFERS FROM RAW OUTPUT"
		}
		if err := l.writeSession(fmt.Sprintf("[%s] %s\n", nowUTC(), meta)); err != nil {
			return err
		}
		return l.writeBlock("MODEL INPUT", modelVisible)
	}
	return nil
}

// LogContextTruncation records that a tool output was truncated/spooled.
func (l *Logger) LogContextTruncation(turn, step int, callID, spoolFile string, omittedChars int) error {
	return l.logTrace(turn, EventContextTruncation, step, map[string]any{
		"call_id":       callID,
		"spool_file":    spoolFile,
		"omitted_chars": omittedChars,
	})
}

// LogContextPruning records a sliding-window eviction.
func (l *Logger) LogContextPruning(turn, step, before, after, evicted int) error {
	if err := l.logTrace(turn, EventContextPruning, step, map[string]any{
		"before":  before,
		"after":   after,
		"evicted": evicted,
	}); err != nil {
		return err
	}
	return l.writeSession(fmt.Sprintf(
		"[%s] TURN %d STEP %d | CONTEXT PRUNED | messages=%d->%d evicted=%d\n",
		nowUTC(), turn, step, before, after, evicted,
	))
}

// LogContextSummary records a newly produced context summary and the
// provider-reported usage for its summarization request.
func (l *Logger) LogContextSummary(turn, step int, summary string, usage llm.Usage, usageReported bool) error {
	data := map[string]any{
		"summary":        summary,
		"usage_reported": usageReported,
	}
	if usageReported {
		data["prompt_tokens"] = usage.PromptTokens
		data["completion_tokens"] = usage.CompletionTokens
		data["total_tokens"] = usage.TotalTokens
	}
	if err := l.logTrace(turn, EventContextSummary, step, data); err != nil {
		return err
	}
	header := fmt.Sprintf("TURN %d STEP %d | CONTEXT SUMMARY", turn, step)
	if usageReported {
		header += fmt.Sprintf(" | tokens_in=%d tokens_out=%d", usage.PromptTokens, usage.CompletionTokens)
	} else {
		header += " | tokens=n/a"
	}
	return l.writeBlock(header, summary)
}

// LogLoopGuardTriggered records that the repeated tool-call guard fired.
func (l *Logger) LogLoopGuardTriggered(turn, step int, toolName string, args map[string]any, repeatCount int) error {
	if err := l.logTrace(turn, EventLoopGuardTriggered, step, map[string]any{
		"tool_name":    toolName,
		"args":         args,
		"repeat_count": repeatCount,
	}); err != nil {
		return err
	}
	return l.writeSession(fmt.Sprintf(
		"[%s] TURN %d STEP %d | [LOOP DETECTED] %s repeated %d times with the same result\n",
		nowUTC(), turn, step, toolName, repeatCount,
	))
}

// LogFinish records the final assistant answer for a successful turn.
func (l *Logger) LogFinish(turn, step int, finalMessage string, totalSteps, totalTokens int) error {
	if err := l.logTrace(turn, EventSessionFinish, step, map[string]any{
		"final_message": finalMessage,
		"total_steps":   totalSteps,
		"total_tokens":  totalTokens,
	}); err != nil {
		return err
	}
	return l.writeBlock(fmt.Sprintf("TURN %d | FINAL RESPONSE", turn), finalMessage)
}

// LogTurnEnd records the outcome of one turn.
func (l *Logger) LogTurnEnd(turn int, outcome string, steps, tokens int) error {
	if err := l.logTrace(turn, EventTurnFinish, steps, map[string]any{
		"outcome": outcome,
		"steps":   steps,
		"tokens":  tokens,
	}); err != nil {
		return err
	}
	return l.writeSession(fmt.Sprintf(
		"[%s] TURN %d END | outcome=%s steps=%d tokens=%d\n",
		nowUTC(), turn, outcome, steps, tokens,
	))
}

// LogRunEnd records the outcome of the whole process invocation.
func (l *Logger) LogRunEnd(outcome string, exitCode, totalSteps, totalTokens int) error {
	if err := l.logTrace(0, EventRunFinish, totalSteps, map[string]any{
		"outcome":      outcome,
		"exit_code":    exitCode,
		"total_steps":  totalSteps,
		"total_tokens": totalTokens,
	}); err != nil {
		return err
	}
	return l.writeSession(fmt.Sprintf(
		"[%s] RUN END | outcome=%s exit_code=%d steps=%d tokens=%d\n",
		nowUTC(), outcome, exitCode, totalSteps, totalTokens,
	))
}

// LogSessionError records a fatal or turn-ending error.
func (l *Logger) LogSessionError(turn, step int, errMsg string) error {
	if err := l.logTrace(turn, EventSessionError, step, map[string]any{"error": errMsg}); err != nil {
		return err
	}
	return l.writeSession(fmt.Sprintf(
		"[%s] TURN %d STEP %d | SESSION ERROR: %s\n",
		nowUTC(), turn, step, errMsg,
	))
}
