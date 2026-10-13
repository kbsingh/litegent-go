package progress

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

func TestReporter_DisabledWritesNothing(t *testing.T) {
	var buf bytes.Buffer
	r := New(false, &buf)
	r.Startup("gpt-4o", "https://api.openai.com/v1", 6, false)
	r.TurnStart(1, 30, 2)
	r.ToolCall(0, "bash", map[string]any{"command": "ls"})

	if buf.Len() != 0 {
		t.Errorf("expected no output when disabled, got: %q", buf.String())
	}
}

func TestReporter_NilReceiverIsSafe(t *testing.T) {
	var r *Reporter
	// None of these should panic on a nil *Reporter.
	r.Startup("m", "u", 1, true)
	r.TurnStart(1, 10, 1)
	r.ContextPruned(0, 5, 3)
	_ = r.Request(0, 1, 1)
	r.Response(0, time.Now(), 0, 10)
	_ = r.ToolCall(0, "bash", nil)
	r.ToolResult(0, "bash", true, time.Now(), "")
	r.TurnEnd(1, "success", 1)
	r.SessionEnd("success", 1)
}

func TestReporter_EnabledWritesExpectedLines(t *testing.T) {
	var buf bytes.Buffer
	r := New(true, &buf)

	r.Startup("gpt-4o", "https://api.openai.com/v1", 6, true)
	out := buf.String()
	if !strings.Contains(out, "litegent: starting (interactive)") {
		t.Errorf("missing startup mode line, got: %q", out)
	}
	if !strings.Contains(out, "model=gpt-4o") || !strings.Contains(out, "tools=6") {
		t.Errorf("missing startup details, got: %q", out)
	}

	buf.Reset()
	r.TurnStart(2, 30, 5)
	if !strings.Contains(buf.String(), "[turn 2] starting | max_steps=30 context_messages=5") {
		t.Errorf("unexpected TurnStart line: %q", buf.String())
	}

	buf.Reset()
	r.ContextPruned(3, 25, 20)
	if !strings.Contains(buf.String(), "[step 3] context pruned | messages=25->20") {
		t.Errorf("unexpected ContextPruned line: %q", buf.String())
	}

	buf.Reset()
	r.ContextPruned(3, 20, 20) // no change -> no output
	if buf.Len() != 0 {
		t.Errorf("expected no output when message count is unchanged, got: %q", buf.String())
	}

	buf.Reset()
	started := r.Request(4, 10, 6)
	if !strings.Contains(buf.String(), "[step 4] sending request | messages=10 tools=6") {
		t.Errorf("unexpected Request line: %q", buf.String())
	}

	buf.Reset()
	r.Response(4, started, 2, 150)
	if !strings.Contains(buf.String(), "tool_calls=2") || !strings.Contains(buf.String(), "tokens=150") {
		t.Errorf("unexpected Response line: %q", buf.String())
	}

	buf.Reset()
	r.Response(4, started, 0, 20)
	if !strings.Contains(buf.String(), "final_response") {
		t.Errorf("expected final_response kind, got: %q", buf.String())
	}

	buf.Reset()
	toolStart := r.ToolCall(5, "bash", map[string]any{"command": "echo hi"})
	if !strings.Contains(buf.String(), "[step 5] tool bash | args=") {
		t.Errorf("unexpected ToolCall line: %q", buf.String())
	}

	buf.Reset()
	r.ToolResult(5, "bash", false, toolStart, "exit code 1")
	if !strings.Contains(buf.String(), "success=false") || !strings.Contains(buf.String(), "error=exit code 1") {
		t.Errorf("unexpected ToolResult line: %q", buf.String())
	}

	buf.Reset()
	r.TurnEnd(2, "success", 4)
	if !strings.Contains(buf.String(), "[turn 2] ended | outcome=success steps=4") {
		t.Errorf("unexpected TurnEnd line: %q", buf.String())
	}

	buf.Reset()
	r.SessionEnd("success", 3)
	if !strings.Contains(buf.String(), "litegent: session ended | outcome=success turns=3") {
		t.Errorf("unexpected SessionEnd line: %q", buf.String())
	}
}

func TestReporter_MasksSecrets(t *testing.T) {
	var buf bytes.Buffer
	r := New(true, &buf, "sk-supersecretvalue1234")
	r.Startup("gpt-4o", "https://api.openai.com/v1?key=sk-supersecretvalue1234", 1, false)

	out := buf.String()
	if strings.Contains(out, "sk-supersecretvalue1234") {
		t.Errorf("raw secret leaked into progress output: %q", out)
	}
	if !strings.Contains(out, "sk-...1234") {
		t.Errorf("expected masked secret suffix, got: %q", out)
	}
}

func TestClip(t *testing.T) {
	short := "hello"
	if got := clip(short, 10); got != short {
		t.Errorf("clip(%q, 10) = %q, want unchanged", short, got)
	}
	long := strings.Repeat("x", 300)
	got := clip(long, 240)
	if len(got) != 240 {
		t.Errorf("clip length = %d, want 240", len(got))
	}
	if !strings.HasSuffix(got, "...") {
		t.Errorf("expected clipped string to end with ..., got: %q", got[len(got)-10:])
	}
}
