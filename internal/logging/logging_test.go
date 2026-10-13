package logging

import (
	"bufio"
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func newTestLogger(t *testing.T, apiKey string) *Logger {
	t.Helper()
	dir := t.TempDir()
	l, err := New(dir, apiKey)
	if err != nil {
		t.Fatalf("New returned error: %v", err)
	}
	t.Cleanup(func() { l.Close() })
	return l
}

func readTraceEvents(t *testing.T, path string) []TraceEvent {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("opening trace file: %v", err)
	}
	defer f.Close()

	var events []TraceEvent
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		var ev TraceEvent
		if err := json.Unmarshal(scanner.Bytes(), &ev); err != nil {
			t.Fatalf("unmarshaling trace line %q: %v", scanner.Text(), err)
		}
		events = append(events, ev)
	}
	return events
}

func readSession(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading session file: %v", err)
	}
	return string(b)
}

func TestLogSessionStart(t *testing.T) {
	l := newTestLogger(t, "")
	if err := l.LogSessionStart("sess-1", "gpt-4o", "https://api.openai.com/v1"); err != nil {
		t.Fatalf("LogSessionStart returned error: %v", err)
	}

	events := readTraceEvents(t, l.TracePath)
	if len(events) != 1 || events[0].EventType != EventSessionStart {
		t.Fatalf("expected one session_start event, got: %+v", events)
	}
	if events[0].Data["session_id"] != "sess-1" {
		t.Errorf("session_id = %v, want sess-1", events[0].Data["session_id"])
	}

	session := readSession(t, l.SessionPath)
	if !strings.Contains(session, "Litegent Session: sess-1") || !strings.Contains(session, "Model: gpt-4o") {
		t.Errorf("session header missing expected fields: %q", session)
	}
}

func TestLogToolCallAndResult(t *testing.T) {
	l := newTestLogger(t, "")
	l.LogToolCall(1, "call_1", "bash", map[string]any{"command": "echo hi"})
	l.LogToolResult(1, "call_1", "bash", true, 0, "hi\n", 12.5)

	events := readTraceEvents(t, l.TracePath)
	if len(events) != 2 {
		t.Fatalf("expected 2 trace events, got %d", len(events))
	}
	if events[0].EventType != EventToolCall || events[1].EventType != EventToolResult {
		t.Errorf("unexpected event types: %+v", events)
	}

	session := readSession(t, l.SessionPath)
	if !strings.Contains(session, "Tool Call:") || !strings.Contains(session, "bash(") {
		t.Errorf("expected formatted tool call in session log: %q", session)
	}
	if !strings.Contains(session, "Tool Result (exit_code=0)") {
		t.Errorf("expected tool result line in session log: %q", session)
	}
}

func TestLoopGuardTriggered(t *testing.T) {
	l := newTestLogger(t, "")
	l.LogLoopGuardTriggered(3, "bash", map[string]any{"command": "ls"}, 4)

	events := readTraceEvents(t, l.TracePath)
	if len(events) != 1 || events[0].EventType != EventLoopGuardTriggered {
		t.Fatalf("expected loop_guard_triggered event, got: %+v", events)
	}
	if events[0].Data["repeat_count"].(float64) != 4 {
		t.Errorf("repeat_count = %v, want 4", events[0].Data["repeat_count"])
	}

	session := readSession(t, l.SessionPath)
	if !strings.Contains(session, "[LOOP DETECTED]") {
		t.Errorf("expected loop detection notice in session log: %q", session)
	}
}

func TestAPIKeyRedaction(t *testing.T) {
	l := newTestLogger(t, "sk-supersecretvalue1234")
	l.LogSessionError(5, "auth failed for key sk-supersecretvalue1234")

	events := readTraceEvents(t, l.TracePath)
	errMsg := events[0].Data["error"].(string)
	if strings.Contains(errMsg, "sk-supersecretvalue1234") {
		t.Errorf("raw API key leaked into trace log: %q", errMsg)
	}
	if !strings.Contains(errMsg, "sk-...1234") {
		t.Errorf("expected masked key suffix in trace log, got: %q", errMsg)
	}

	session := readSession(t, l.SessionPath)
	if strings.Contains(session, "sk-supersecretvalue1234") {
		t.Errorf("raw API key leaked into session log: %q", session)
	}
}

func TestLogFinish(t *testing.T) {
	l := newTestLogger(t, "")
	l.LogFinish(10, "Task complete.", 10, 5000)

	session := readSession(t, l.SessionPath)
	if !strings.Contains(session, "Final Response:") || !strings.Contains(session, "Task complete.") {
		t.Errorf("expected final response in session log: %q", session)
	}
	if !strings.Contains(session, "Total Steps: 10") {
		t.Errorf("expected total steps in session log: %q", session)
	}
}
