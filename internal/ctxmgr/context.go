// Package ctxmgr implements Litegent's context management engine: tool
// output truncation with disk spooling, sliding-window history pruning,
// and progressive conversation summarization. Named ctxmgr (not context)
// to avoid shadowing the standard library's context package, which this
// package also uses for cancellation-aware summarization calls.
//
// See req/spec_doc.md section 2.5.
package ctxmgr

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"litegent/internal/config"
	"litegent/internal/llm"
)

// Summarizer produces a natural-language summary of a slice of evicted
// messages. internal/llm.Client implements this via a sub-call to the
// configured LLM; tests can supply a fake.
type Summarizer interface {
	Summarize(ctx context.Context, messages []llm.Message) (string, error)
}

// Manager applies the three-stage context management pipeline described
// in req/spec_doc.md section 2.5: truncate oversized tool outputs, prune
// the sliding window, and progressively summarize evicted history.
type Manager struct {
	cfg        config.ContextConfig
	summarizer Summarizer
}

// NewManager constructs a Manager. summarizer may be nil, in which case
// stage 3 (summarization) is skipped even if the token budget is
// exceeded — the sliding window eviction in stage 2 still applies.
func NewManager(cfg config.ContextConfig, summarizer Summarizer) *Manager {
	return &Manager{cfg: cfg, summarizer: summarizer}
}

// TruncationInfo describes a truncation that occurred, for the caller to
// log as a context_truncation trace event. A nil *TruncationInfo return
// from TruncateToolOutput means no truncation was necessary.
type TruncationInfo struct {
	SpoolFile    string
	OmittedChars int
}

// TruncateToolOutput is Stage 1. If rawOutput exceeds maxChars, the full
// content is spooled to disk under m.cfg.SpoolDir and a head+notice+tail
// splice is returned instead, along with TruncationInfo for logging.
// Litegent is unconfined (see req/requirements_doc.md NFR-3.4): spoolDir
// is an ordinary configurable default, not an access boundary.
func (m *Manager) TruncateToolOutput(callID, rawOutput string) (string, *TruncationInfo, error) {
	maxChars := m.cfg.MaxToolOutputChars
	if maxChars <= 0 || len(rawOutput) <= maxChars {
		return rawOutput, nil, nil
	}

	spoolDir := m.cfg.SpoolDir
	if spoolDir == "" {
		spoolDir = ".tmp/tool_outputs"
	}
	if err := os.MkdirAll(spoolDir, 0o755); err != nil {
		return "", nil, fmt.Errorf("creating spool dir: %w", err)
	}
	spoolFile := filepath.Join(spoolDir, fmt.Sprintf("output_%s.txt", callID))
	if err := os.WriteFile(spoolFile, []byte(rawOutput), 0o644); err != nil {
		return "", nil, fmt.Errorf("spooling output: %w", err)
	}

	headLen := int(float64(maxChars) * 0.6)
	tailLen := int(float64(maxChars) * 0.3)
	if headLen+tailLen > len(rawOutput) {
		headLen = len(rawOutput)
		tailLen = 0
	}
	omitted := len(rawOutput) - (headLen + tailLen)

	notice := fmt.Sprintf(
		"\n\n[TRUNCATED: %d characters omitted. Full content saved to: %s. Use read_file to inspect specific lines if needed.]\n\n",
		omitted, spoolFile,
	)
	spliced := rawOutput[:headLen] + notice + rawOutput[len(rawOutput)-tailLen:]
	return spliced, &TruncationInfo{SpoolFile: spoolFile, OmittedChars: omitted}, nil
}

// messageLayout indices, per req/spec_doc.md section 2.5:
//
//	0: system prompt (permanent)
//	1: user prompt (permanent)
//	2: optional <context_summary> message
//	3+: active operational dialogue
const (
	idxSystem  = 0
	idxUser    = 1
	idxSummary = 2
	anchorLen  = 2 // system + user are always present once the loop starts
)

// Prepare runs Stage 2 (sliding window pruning) and, when a summarizer is
// configured and the token budget is exceeded, Stage 3 (summarization).
// The system prompt (index 0) and initial user prompt (index 1) are never
// evicted.
func (m *Manager) Prepare(ctx context.Context, messages []llm.Message) ([]llm.Message, error) {
	if len(messages) <= anchorLen {
		return messages, nil
	}

	hasSummary := len(messages) > idxSummary && messages[idxSummary].Role == llm.RoleSummary
	activeStart := anchorLen
	var summary *llm.Message
	if hasSummary {
		activeStart = idxSummary + 1
		s := messages[idxSummary]
		summary = &s
	}
	active := messages[activeStart:]

	if m.cfg.WindowSize > 0 && len(active) > m.cfg.WindowSize {
		evictedCount := len(active) - m.cfg.WindowSize
		evicted := active[:evictedCount]
		kept := active[evictedCount:]

		// Only pay for a summarization LLM sub-call when actually
		// approaching the token budget (req/requirements_doc.md FR-3.4).
		// MaxContextTokens <= 0 means no budget gate is configured, so
		// fall back to always summarizing on window overflow — the same
		// convention TruncateToolOutput uses for MaxToolOutputChars <= 0.
		overBudget := m.cfg.MaxContextTokens <= 0 || EstimateTokens(messages) > m.cfg.MaxContextTokens

		if m.summarizer != nil && overBudget {
			toSummarize := evicted
			if summary != nil {
				// Re-summarize the prior summary alongside newly evicted
				// turns so context is never silently lost.
				toSummarize = append([]llm.Message{*summary}, evicted...)
			}
			text, err := m.summarizer.Summarize(ctx, toSummarize)
			if err != nil {
				return nil, fmt.Errorf("summarizing evicted context: %w", err)
			}
			summary = &llm.Message{Role: llm.RoleSummary, Content: text}
		}
		active = kept
	}

	result := make([]llm.Message, 0, anchorLen+1+len(active))
	result = append(result, messages[idxSystem], messages[idxUser])
	if summary != nil {
		result = append(result, *summary)
	}
	result = append(result, active...)
	return result, nil
}

// EstimateTokens is a lightweight, dependency-free token estimate (~4
// characters per token, a common rule of thumb for English/code text).
// It intentionally avoids pulling in a full tokenizer to keep Litegent's
// dependency footprint minimal.
func EstimateTokens(messages []llm.Message) int {
	chars := 0
	for _, msg := range messages {
		chars += len(msg.Content)
		for _, tc := range msg.ToolCalls {
			chars += len(tc.Function.Name) + len(tc.Function.Arguments)
		}
	}
	return chars / 4
}
