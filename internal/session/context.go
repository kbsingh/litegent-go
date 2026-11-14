package session

import (
	"encoding/json"
	"fmt"
	"os"
	"time"

	"litegent/internal/llm"
)

const contextVersion = 1

// Context is the single authoritative context blob for a session. It
// always contains the effective, post-compaction message list.
type Context struct {
	Version        int           `json:"version"`
	SessionID      string        `json:"session_id"`
	UpdatedAt      time.Time     `json:"updated_at"`
	MessagesSHA256 string        `json:"messages_sha256"`
	Messages       []llm.Message `json:"messages"`
}

// Store implements the agent's durable-context interface for one session.
type Store struct {
	Info *Info
}

// SaveContext persists messages as the session's current effective context.
func (s *Store) SaveContext(messages []llm.Message) error {
	if s == nil || s.Info == nil {
		return fmt.Errorf("session store is not initialized")
	}
	return SaveContext(s.Info, messages)
}

// LoadContext loads the session's current effective context.
func (s *Store) LoadContext() ([]llm.Message, error) {
	if s == nil || s.Info == nil {
		return nil, fmt.Errorf("session store is not initialized")
	}
	return LoadContext(s.Info)
}

// SaveContext validates messages, computes their hash, and atomically
// replaces context.json. A failure leaves the previous blob untouched.
func SaveContext(info *Info, messages []llm.Message) error {
	if info == nil {
		return fmt.Errorf("session info must not be nil")
	}
	if err := ValidateMessages(messages); err != nil {
		return fmt.Errorf("refusing to save invalid context: %w", err)
	}
	hash, err := llm.HashMessages(messages)
	if err != nil {
		return err
	}
	ctx := Context{
		Version:        contextVersion,
		SessionID:      info.ID,
		UpdatedAt:      time.Now().UTC(),
		MessagesSHA256: hash,
		Messages:       messages,
	}
	data, err := json.MarshalIndent(ctx, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling context: %w", err)
	}
	data = append(data, '\n')
	if err := writeFileAtomic(info.ContextPath, data, 0o600); err != nil {
		return fmt.Errorf("writing context %q: %w", info.ContextPath, err)
	}
	return nil
}

// LoadContext reads, validates, and hash-checks a session's context blob.
func LoadContext(info *Info) ([]llm.Message, error) {
	if info == nil {
		return nil, fmt.Errorf("session info must not be nil")
	}
	data, err := os.ReadFile(info.ContextPath)
	if err != nil {
		return nil, fmt.Errorf("reading context %q: %w", info.ContextPath, err)
	}
	var ctx Context
	if err := json.Unmarshal(data, &ctx); err != nil {
		return nil, fmt.Errorf("parsing context %q: %w", info.ContextPath, err)
	}
	if ctx.Version != contextVersion {
		return nil, fmt.Errorf("unsupported context version %d", ctx.Version)
	}
	if ctx.SessionID != info.ID {
		return nil, fmt.Errorf("context session id %q does not match %q", ctx.SessionID, info.ID)
	}
	if err := ValidateMessages(ctx.Messages); err != nil {
		return nil, fmt.Errorf("invalid context %q: %w", info.ContextPath, err)
	}
	hash, err := llm.HashMessages(ctx.Messages)
	if err != nil {
		return nil, err
	}
	if ctx.MessagesSHA256 != "" && ctx.MessagesSHA256 != hash {
		return nil, fmt.Errorf("context %q hash mismatch", info.ContextPath)
	}
	return ctx.Messages, nil
}

// ValidateMessages enforces the structural invariants required for a
// resumable conversation: one leading system message, optional summary at
// index 2, and complete assistant tool-call/tool-result pairs.
func ValidateMessages(messages []llm.Message) error {
	if len(messages) == 0 {
		return fmt.Errorf("context must contain at least the system message")
	}
	if messages[0].Role != llm.RoleSystem {
		return fmt.Errorf("first message must have role %q, got %q", llm.RoleSystem, messages[0].Role)
	}

	pending := map[string]struct{}{}
	for i, msg := range messages {
		switch msg.Role {
		case llm.RoleSystem:
			if i != 0 {
				return fmt.Errorf("system message is only allowed at index 0")
			}
			if len(pending) > 0 {
				return fmt.Errorf("system message appears before pending tool results")
			}
		case llm.RoleSummary:
			if i != 2 {
				return fmt.Errorf("summary message is only allowed at index 2")
			}
			if len(pending) > 0 {
				return fmt.Errorf("summary message appears before pending tool results")
			}
		case llm.RoleUser:
			if len(pending) > 0 {
				return fmt.Errorf("user message at index %d appears before pending tool results", i)
			}
		case llm.RoleAssistant:
			if len(pending) > 0 {
				return fmt.Errorf("assistant message at index %d appears before pending tool results", i)
			}
			for _, tc := range msg.ToolCalls {
				if tc.ID == "" {
					return fmt.Errorf("assistant tool call at index %d has an empty id", i)
				}
				if _, exists := pending[tc.ID]; exists {
					return fmt.Errorf("duplicate tool call id %q", tc.ID)
				}
				pending[tc.ID] = struct{}{}
			}
		case llm.RoleTool:
			if msg.ToolCallID == "" {
				return fmt.Errorf("tool message at index %d has an empty tool_call_id", i)
			}
			if _, ok := pending[msg.ToolCallID]; !ok {
				return fmt.Errorf("tool message at index %d references unknown or already-answered call %q", i, msg.ToolCallID)
			}
			delete(pending, msg.ToolCallID)
		default:
			return fmt.Errorf("message at index %d has unsupported role %q", i, msg.Role)
		}
	}
	if len(pending) > 0 {
		return fmt.Errorf("context ends with %d unanswered tool call(s)", len(pending))
	}
	return nil
}
