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
	if err := l.LogSessionStart("sess-1", "run-1", false, "gpt-4o", "https://api.openai.com/v1", "/work", "sys prompt"); err != nil {
		t.Fatalf("LogSessionStart returned error: %v", err)
	}

	events := readTraceEvents(t, l.TracePath)
	if len(events) != 1 || events[0].EventType != EventSessionStart {
		t.Fatalf("expected one session_start event, got: %+v", events)
	}
	if events[0].Data["session_id"] != "sess-1" {
		t.Errorf("session_id = %v, want sess-1", events[0].Data["session_id"])
	}
	if events[0].Data["run_id"] != "run-1" {
		t.Errorf("run_id = %v, want run-1", events[0].Data["run_id"])
	}
	if events[0].Data["resumed"] != false {
		t.Errorf("resumed = %v, want false", events[0].Data["resumed"])
	}

	session := readSession(t, l.SessionPath)
	for _, want := range []string{
		"Litegent Session: sess-1",
		"Run: run-1",
		"Resumed: false",
		"Model: gpt-4o",
		"Working Dir: /work",
		"SYSTEM PROMPT",
		"sys prompt",
	} {
		if !strings.Contains(session, want) {
			t.Errorf("session header missing %q: %q", want, session)
		}
	}
}

func TestLogUserPrompt(t *testing.T) {
	l := newTestLogger(t, "")
	if err := l.LogUserPrompt(2, "fix the tests"); err != nil {
		t.Fatalf("LogUserPrompt returned error: %v", err)
	}

	events := readTraceEvents(t, l.TracePath)
	if len(events) != 1 || events[0].EventType != EventUserPrompt || events[0].Turn != 2 {
		t.Fatalf("unexpected user_prompt event: %+v", events)
	}
	session := readSession(t, l.SessionPath)
	if !strings.Contains(session, "TURN 2 | USER INPUT") || !strings.Contains(session, "fix the tests") {
		t.Errorf("user prompt missing from session log: %q", session)
	}
}

func TestLogToolCallAndResult(t *testing.T) {
	l := newTestLogger(t, "")
	l.LogToolCall(1, 2, "call_1", "bash", map[string]any{"command": "echo hi"})
	l.LogToolResult(1, 2, "call_1", "bash", true, 0, "hi\n", "", "", 0, 12.5)

	events := readTraceEvents(t, l.TracePath)
	if len(events) != 2 {
		t.Fatalf("expected 2 trace events, got %d", len(events))
	}
	if events[0].EventType != EventToolCall || events[1].EventType != EventToolResult {
		t.Errorf("unexpected event types: %+v", events)
	}
	if events[1].Data["output"] != "hi\n" {
		t.Errorf("trace output = %v, want raw output", events[1].Data["output"])
	}

	session := readSession(t, l.SessionPath)
	for _, want := range []string{
		"TURN 1 STEP 2 | TOOL CALL bash | call_id=call_1",
		`"command": "echo hi"`,
		"TURN 1 STEP 2 | TOOL RESULT bash",
		"success=true",
		"OUTPUT",
		"hi",
	} {
		if !strings.Contains(session, want) {
			t.Errorf("session log missing %q: %q", want, session)
		}
	}
}

func TestLogTruncatedToolResultShowsRawAndModelInput(t *testing.T) {
	l := newTestLogger(t, "")
	raw := strings.Repeat("x", 100)
	modelVisible := "xxxx[TRUNCATED: 90 characters omitted.]xxxx"
	l.LogToolResult(1, 0, "call_1", "bash", true, 0, raw, modelVisible, "/spool/output_call_1.txt", 90, 1.5)

	session := readSession(t, l.SessionPath)
	for _, want := range []string{
		"OUTPUT",
		raw,
		"TRUNCATION | omitted=90 | spool=/spool/output_call_1.txt",
		"MODEL INPUT",
		modelVisible,
	} {
		if !strings.Contains(session, want) {
			t.Errorf("session log missing %q", want)
		}
	}

	events := readTraceEvents(t, l.TracePath)
	if events[0].Data["model_content"] != modelVisible {
		t.Errorf("trace model_content missing")
	}
	if events[0].Data["spool_file"] != "/spool/output_call_1.txt" {
		t.Errorf("trace spool_file missing")
	}
}

func TestLoopGuardTriggered(t *testing.T) {
	l := newTestLogger(t, "")
	l.LogLoopGuardTriggered(2, 3, "bash", map[string]any{"command": "ls"}, 4)

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
	l.LogSessionError(1, 5, "auth failed for key sk-supersecretvalue1234")

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

func TestAPIKeyRedaction_NestedToolArgs(t *testing.T) {
	l := newTestLogger(t, "sk-supersecretvalue1234")
	l.LogToolCall(1, 0, "call_1", "bash", map[string]any{
		"command": "curl -H 'Authorization: Bearer sk-supersecretvalue1234'",
		"nested":  []any{map[string]any{"token": "sk-supersecretvalue1234"}},
	})

	events := readTraceEvents(t, l.TracePath)
	encoded, err := json.Marshal(events[0])
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "sk-supersecretvalue1234") {
		t.Errorf("raw key leaked into nested trace args: %s", encoded)
	}
	session := readSession(t, l.SessionPath)
	if strings.Contains(session, "sk-supersecretvalue1234") {
		t.Errorf("raw key leaked into nested session args: %q", session)
	}
}

func TestAPIKeyRedaction_R9Prefix(t *testing.T) {
	l := newTestLogger(t, "r9_supersecretvalue1234")
	l.LogSessionError(1, 5, "auth failed for key r9_supersecretvalue1234")

	events := readTraceEvents(t, l.TracePath)
	errMsg := events[0].Data["error"].(string)
	if strings.Contains(errMsg, "r9_supersecretvalue1234") {
		t.Errorf("raw r9_ key leaked into trace log: %q", errMsg)
	}
	if !strings.Contains(errMsg, "r9_...1234") {
		t.Errorf("expected r9_ stub in masked key, got: %q", errMsg)
	}

	session := readSession(t, l.SessionPath)
	if strings.Contains(session, "r9_supersecretvalue1234") {
		t.Errorf("raw r9_ key leaked into session log: %q", session)
	}
}

func TestAPIKeyRedaction_ShortKeyFullyMasked(t *testing.T) {
	l := newTestLogger(t, "tinykey") // 7 chars: prefix+suffix reveal would expose it all
	l.LogSessionError(1, 1, "key tinykey rejected")

	events := readTraceEvents(t, l.TracePath)
	errMsg := events[0].Data["error"].(string)
	if strings.Contains(errMsg, "tinykey") {
		t.Errorf("short key leaked into trace log: %q", errMsg)
	}
	if !strings.Contains(errMsg, "****") {
		t.Errorf("expected fully masked short key, got: %q", errMsg)
	}
}

func TestLogContextPruningAndSummary(t *testing.T) {
	l := newTestLogger(t, "")
	l.LogContextPruning(2, 4, 42, 22, 20)
	l.LogContextSummary(2, 4, "important findings")

	events := readTraceEvents(t, l.TracePath)
	if len(events) != 2 {
		t.Fatalf("expected 2 events, got %d", len(events))
	}
	if events[0].EventType != EventContextPruning || events[1].EventType != EventContextSummary {
		t.Errorf("unexpected event types: %+v", events)
	}
	session := readSession(t, l.SessionPath)
	if !strings.Contains(session, "CONTEXT PRUNED | messages=42->22 evicted=20") {
		t.Errorf("pruning line missing: %q", session)
	}
	if !strings.Contains(session, "CONTEXT SUMMARY") || !strings.Contains(session, "important findings") {
		t.Errorf("summary block missing: %q", session)
	}
}

func TestLogFinish(t *testing.T) {
	l := newTestLogger(t, "")
	l.LogFinish(2, 10, "Task complete.", 10, 5000)

	session := readSession(t, l.SessionPath)
	if !strings.Contains(session, "TURN 2 | FINAL RESPONSE") || !strings.Contains(session, "Task complete.") {
		t.Errorf("expected final response in session log: %q", session)
	}
}
