// Package progress implements Litegent's optional, human-facing verbose
// progress reporting: concise single-line updates streamed to an
// io.Writer (typically stderr) as the agentic loop runs. This is
// distinct from internal/logging's dual audit log, which is written to
// disk for after-the-fact review — progress is for watching a run live.
package progress

import (
	"fmt"
	"io"
	"strings"
	"time"
)

// Reporter writes concise progress lines when Enabled. The zero value
// (Enabled: false) is safe to use and silently does nothing, as is a nil
// *Reporter — every method is nil-receiver-safe so callers never need to
// guard calls with a nil check.
type Reporter struct {
	Enabled bool
	Writer  io.Writer
	// Secrets lists values (e.g. the API key) to mask out of any
	// reported text, mirroring the redaction internal/logging.Logger
	// performs on the audit log.
	Secrets []string
	// ShowToolCalls emits a compact "|ToolCall : ..." notice line to
	// Writer for every tool invocation, for interactive sessions that
	// are not running verbose — under -v (Enabled) the richer
	// "[step n] tool ..." line already covers it, so the notice is
	// suppressed to avoid duplication.
	ShowToolCalls bool
}

// New constructs a Reporter. Pass enabled=false (e.g. when -v is not
// set) to get a Reporter that exists but never writes anything.
func New(enabled bool, w io.Writer, secrets ...string) *Reporter {
	return &Reporter{Enabled: enabled, Writer: w, Secrets: secrets}
}

func (r *Reporter) write(format string, args ...any) {
	if r == nil || !r.Enabled || r.Writer == nil {
		return
	}
	fmt.Fprintln(r.Writer, r.mask(fmt.Sprintf(format, args...)))
}

// mask replaces every configured secret in s with a masked form that
// preserves the secret's own stub prefix (e.g. "sk-...", "r9_..."),
// mirroring the redaction internal/logging.Logger performs on the audit
// log. Secrets of 7 characters or fewer are masked entirely, since
// revealing a 3+4 character prefix/suffix would expose the whole secret.
func (r *Reporter) mask(s string) string {
	for _, secret := range r.Secrets {
		if secret == "" {
			continue
		}
		masked := "****"
		if len(secret) > 7 {
			masked = secret[:3] + "..." + secret[len(secret)-4:]
		}
		s = strings.ReplaceAll(s, secret, masked)
	}
	return s
}

// Startup reports the session kind, model/endpoint, and tool count.
func (r *Reporter) Startup(model, baseURL string, toolCount int, interactive bool) {
	mode := "single-task"
	if interactive {
		mode = "interactive"
	}
	r.write("litegent: starting (%s)", mode)
	r.write("  model=%s endpoint=%s tools=%d", model, baseURL, toolCount)
}

// TurnStart reports the beginning of one agentic-loop turn.
func (r *Reporter) TurnStart(turn, maxSteps, messageCount int) {
	r.write("[turn %d] starting | max_steps=%d context_messages=%d", turn, maxSteps, messageCount)
}

// ContextPruned reports a sliding-window pruning pass, if it changed
// the message count.
func (r *Reporter) ContextPruned(step, before, after int) {
	if before != after {
		r.write("[step %d] context pruned | messages=%d->%d", step, before, after)
	}
}

// Request reports that an LLM request is being sent, returning a start
// time for the caller to pass to Response for latency measurement.
func (r *Reporter) Request(step, messageCount, toolCount int) time.Time {
	r.write("[step %d] sending request | messages=%d tools=%d", step, messageCount, toolCount)
	return time.Now()
}

// Response reports an LLM response: whether it carried tool calls or a
// final answer, its latency, and token usage.
func (r *Reporter) Response(step int, started time.Time, toolCalls, tokens int) {
	kind := "final_response"
	if toolCalls > 0 {
		kind = fmt.Sprintf("tool_calls=%d", toolCalls)
	}
	r.write("[step %d] response | %s latency=%.2fs tokens=%d", step, kind, time.Since(started).Seconds(), tokens)
}

// ToolCall reports a tool invocation, returning a start time for the
// caller to pass to ToolResult for duration measurement.
func (r *Reporter) ToolCall(step int, name string, args map[string]any) time.Time {
	if r != nil && r.ShowToolCalls && !r.Enabled && r.Writer != nil {
		// Compact interactive notice. Secrets are masked first (so the
		// line/clip truncation below can never defeat exact-key
		// replacement), then only the first physical line of the
		// request is kept, then it is clipped for terminal width.
		preview := clip(firstLine(r.mask(name+" "+fmt.Sprint(args))), 240)
		fmt.Fprintln(r.Writer, "|ToolCall : "+preview)
	}
	r.write("[step %d] tool %s | args=%s", step, name, clip(fmt.Sprint(args), 240))
	return time.Now()
}

// ToolResult reports a tool's outcome.
func (r *Reporter) ToolResult(step int, name string, success bool, started time.Time, errMsg string) {
	extra := ""
	if errMsg != "" {
		extra = " error=" + clip(errMsg, 240)
	}
	r.write("[step %d] result %s | success=%v duration=%.2fs%s", step, name, success, time.Since(started).Seconds(), extra)
}

// TurnEnd reports how one turn concluded.
func (r *Reporter) TurnEnd(turn int, outcome string, steps int) {
	r.write("[turn %d] ended | outcome=%s steps=%d", turn, outcome, steps)
}

// SessionEnd reports the final outcome of the whole session.
func (r *Reporter) SessionEnd(outcome string, turns int) {
	r.write("litegent: session ended | outcome=%s turns=%d", outcome, turns)
}

// clip truncates s to at most limit characters, for including
// potentially large values (tool args, error text) in a single line.
func clip(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	return s[:limit-3] + "..."
}

// firstLine returns s truncated at its first newline, if any. Tool
// arguments frequently embed multi-line content; only the opening line
// is useful as a live one-line preview.
func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
