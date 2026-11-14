// Package agent implements Litegent's core agentic loop: query the LLM,
// dispatch any requested tool calls, feed results back, and repeat until
// the model produces a final response or a safety limit is hit. See
// req/spec_doc.md sections 1 and 3, and req/agent_guide.md section 2.
package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"litegent/internal/config"
	"litegent/internal/ctxmgr"
	"litegent/internal/llm"
	"litegent/internal/logging"
	"litegent/internal/progress"
	"litegent/internal/tools"
)

// Outcome classifies how a Run ended.
type Outcome int

const (
	// OutcomeSuccess means the model produced a final response with no
	// further tool calls.
	OutcomeSuccess Outcome = iota
	// OutcomeMaxStepsExceeded means max_steps was reached without a
	// natural conclusion.
	OutcomeMaxStepsExceeded
	// OutcomeLLMError means a fatal, non-retryable LLM communication
	// error occurred (e.g. authentication failure).
	OutcomeLLMError
	// OutcomeInterrupted means the context was canceled (SIGINT/SIGTERM).
	OutcomeInterrupted
	// OutcomeStorageError means the durable context could not be saved.
	OutcomeStorageError
)

// String renders the outcome as a short label, used in progress/log
// output (e.g. "outcome=success").
func (o Outcome) String() string {
	switch o {
	case OutcomeSuccess:
		return "success"
	case OutcomeMaxStepsExceeded:
		return "max_steps_exceeded"
	case OutcomeLLMError:
		return "llm_error"
	case OutcomeInterrupted:
		return "interrupted"
	case OutcomeStorageError:
		return "storage_error"
	default:
		return "unknown"
	}
}

// Result is the outcome of a full agentic loop run.
type Result struct {
	Outcome            Outcome
	FinalMessage       string
	Steps              int           // agent-loop LLM requests attempted; excludes summarization
	ToolCalls          int           // individual tool executions
	LLMCalls           int           // agent and context-summarization requests
	PromptTokens       int           // provider-reported input tokens, including summaries
	CompletionTokens   int           // provider-reported output tokens, including summaries
	TotalTokens        int           // provider-reported total tokens, including summaries
	TokenUsageComplete bool          // false when any request did not report prompt/completion usage
	Duration           time.Duration // time spent processing the turn; excludes interactive input wait
	Err                error
}

// ContextStore persists the effective conversation context at safe
// boundaries. A nil store means persistence is disabled (used by tests
// and by callers that do not need resume support).
type ContextStore interface {
	SaveContext(messages []llm.Message) error
}

// Agent orchestrates the agentic loop by wiring together the LLM client,
// tool registry, context manager, and dual logger.
type Agent struct {
	Config  config.LitegentConfig
	LLM     *llm.Client
	Tools   *tools.Registry
	Context *ctxmgr.Manager
	Logger  *logging.Logger
	Store   ContextStore

	// Progress optionally streams live, human-facing progress lines
	// (typically to stderr) as the loop runs. A nil Progress is safe —
	// every *progress.Reporter method is nil-receiver-safe — so it's
	// left unset unless the caller (internal/cliapp) opts in via -v.
	Progress *progress.Reporter

	// lastSignature/repeatCount implement the repeated tool-call loop
	// guard (req/requirements_doc.md NFR-3.6 / FR-2.5). Reset at the
	// start of every turn so a stuck pattern in one turn never bleeds
	// into the next (relevant once RunInteractive spans multiple turns).
	lastSignature *callSignature
	repeatCount   int
}

// New constructs an Agent from its collaborators.
func New(cfg config.LitegentConfig, llmClient *llm.Client, registry *tools.Registry, ctxManager *ctxmgr.Manager, logger *logging.Logger) *Agent {
	return &Agent{Config: cfg, LLM: llmClient, Tools: registry, Context: ctxManager, Logger: logger}
}

type callSignature struct {
	name       string
	argsJSON   string
	resultJSON string
}

// Run executes a single-shot agentic loop starting from systemPrompt and
// userPrompt until the model finishes, max_steps is reached, a fatal LLM
// error occurs, or ctx is canceled. For a persistent multi-turn session,
// see RunInteractive.
func (a *Agent) Run(ctx context.Context, systemPrompt, userPrompt string) Result {
	return a.RunWithHistory(ctx, []llm.Message{{Role: llm.RoleSystem, Content: systemPrompt}}, userPrompt, 1)
}

// RunWithHistory executes a single-shot turn against an existing message
// history. The caller is responsible for having loaded/validated history;
// the new user prompt is appended, logged, and persisted before the first
// LLM request.
func (a *Agent) RunWithHistory(ctx context.Context, history []llm.Message, userPrompt string, turn int) Result {
	started := time.Now()
	if turn <= 0 {
		turn = 1
	}
	messages := append([]llm.Message(nil), history...)
	messages = append(messages, llm.Message{Role: llm.RoleUser, Content: userPrompt})
	_ = a.Logger.LogUserPrompt(turn, userPrompt)
	if err := a.Logger.Err(); err != nil {
		return Result{Outcome: OutcomeStorageError, Duration: time.Since(started), Err: err}
	}
	if err := a.saveContext(turn, 0, messages); err != nil {
		_ = a.Logger.LogTurnEnd(turn, OutcomeStorageError.String(), 0, 0)
		return Result{Outcome: OutcomeStorageError, Duration: time.Since(started), Err: err}
	}
	_, result := a.runTurn(ctx, messages, turn, started)
	return result
}

// RunInteractive drives a persistent, multi-turn session: systemPrompt
// seeds the conversation once, and conversation history (subject to the
// same context management Run uses) persists across turns within this
// Agent. Each turn gets its own independent max_steps budget.
//
// readPrompt is called before every turn to obtain the next user input;
// returning ok=false ends the session cleanly (the caller decides what
// counts as a terminator — EOF, "/exit", "/quit", ...). onTurnResult is
// called after each turn with that turn's Result; returning false from
// onTurnResult ends the session early (e.g. after a fatal error) instead
// of prompting for another turn.
func (a *Agent) RunInteractive(
	ctx context.Context,
	systemPrompt string,
	readPrompt func() (prompt string, ok bool),
	onTurnResult func(turn int, result Result) (cont bool),
) Result {
	return a.RunInteractiveWithHistory(
		ctx,
		[]llm.Message{{Role: llm.RoleSystem, Content: systemPrompt}},
		0,
		readPrompt,
		onTurnResult,
	)
}

// RunInteractiveWithHistory is RunInteractive with an existing history
// and a starting turn number. New prompts are appended, logged, and
// persisted before each turn.
func (a *Agent) RunInteractiveWithHistory(
	ctx context.Context,
	history []llm.Message,
	startTurn int,
	readPrompt func() (prompt string, ok bool),
	onTurnResult func(turn int, result Result) (cont bool),
) Result {
	messages := append([]llm.Message(nil), history...)

	turn := startTurn
	var aggregate Result
	usageComplete := true
	for {
		if err := ctx.Err(); err != nil {
			aggregate.Outcome = OutcomeInterrupted
			aggregate.TokenUsageComplete = aggregate.LLMCalls > 0 && usageComplete
			aggregate.Err = err
			return aggregate
		}

		prompt, ok := readPrompt()
		if !ok {
			// A prompt read that ends because the context was canceled is
			// an interruption, not a clean EOF/exit.
			if err := ctx.Err(); err != nil {
				aggregate.Outcome = OutcomeInterrupted
				aggregate.TokenUsageComplete = aggregate.LLMCalls > 0 && usageComplete
				aggregate.Err = err
				return aggregate
			}
			aggregate.Outcome = OutcomeSuccess
			aggregate.TokenUsageComplete = aggregate.LLMCalls > 0 && usageComplete
			return aggregate
		}
		turnStarted := time.Now()
		turn++
		_ = a.Logger.LogUserPrompt(turn, prompt)
		if err := a.Logger.Err(); err != nil {
			aggregate.Outcome = OutcomeStorageError
			aggregate.Duration += time.Since(turnStarted)
			aggregate.TokenUsageComplete = aggregate.LLMCalls > 0 && usageComplete
			aggregate.Err = err
			return aggregate
		}

		messages = append(messages, llm.Message{Role: llm.RoleUser, Content: prompt})
		if err := a.saveContext(turn, 0, messages); err != nil {
			_ = a.Logger.LogTurnEnd(turn, OutcomeStorageError.String(), 0, 0)
			aggregate.Outcome = OutcomeStorageError
			aggregate.Duration += time.Since(turnStarted)
			aggregate.TokenUsageComplete = aggregate.LLMCalls > 0 && usageComplete
			aggregate.Err = err
			return aggregate
		}

		var result Result
		messages, result = a.runTurn(ctx, messages, turn, turnStarted)
		aggregate.Duration += result.Duration
		aggregate.Steps += result.Steps
		aggregate.ToolCalls += result.ToolCalls
		aggregate.LLMCalls += result.LLMCalls
		aggregate.PromptTokens += result.PromptTokens
		aggregate.CompletionTokens += result.CompletionTokens
		aggregate.TotalTokens += result.TotalTokens
		if result.LLMCalls > 0 && !result.TokenUsageComplete {
			usageComplete = false
		}

		if !onTurnResult(turn, result) {
			aggregate.Outcome = result.Outcome
			aggregate.FinalMessage = result.FinalMessage
			aggregate.Err = result.Err
			aggregate.TokenUsageComplete = aggregate.LLMCalls > 0 && usageComplete
			return aggregate
		}
	}
}

// saveContext persists the current message list when a store is
// configured. Persistence failures are logged here and returned to the
// caller, which aborts with OutcomeStorageError.
func (a *Agent) saveContext(turn, step int, messages []llm.Message) error {
	if a.Store == nil {
		return nil
	}
	if err := a.Store.SaveContext(messages); err != nil {
		_ = a.Logger.LogSessionError(turn, step, "saving context: "+err.Error())
		return err
	}
	return nil
}

// runTurn executes the agentic loop for a single turn against the given
// message history (which already includes this turn's user message),
// returning the updated history (including the assistant's final answer,
// so a subsequent turn in an interactive session sees it) alongside the
// turn's Result. turn is used only for progress/log labeling.
func (a *Agent) runTurn(ctx context.Context, messages []llm.Message, turn int, started time.Time) ([]llm.Message, Result) {
	// A stuck pattern from a previous turn must never suppress a
	// legitimate repeated call in a new one.
	a.lastSignature = nil
	a.repeatCount = 0

	a.Progress.TurnStart(turn, a.Config.MaxSteps, len(messages))

	var promptTokens, completionTokens, totalTokens int
	var steps, toolCalls, llmCalls int
	usageComplete := true
	resultFor := func(outcome Outcome, err error) Result {
		return Result{
			Outcome:            outcome,
			Steps:              steps,
			ToolCalls:          toolCalls,
			LLMCalls:           llmCalls,
			PromptTokens:       promptTokens,
			CompletionTokens:   completionTokens,
			TotalTokens:        totalTokens,
			TokenUsageComplete: llmCalls > 0 && usageComplete,
			Duration:           time.Since(started),
			Err:                err,
		}
	}
	addUsage := func(usage llm.Usage, reported bool) {
		if !reported {
			usageComplete = false
			return
		}
		promptTokens += usage.PromptTokens
		completionTokens += usage.CompletionTokens
		totalTokens += usage.TotalTokens
	}
	step := 0
	for ; step < a.Config.MaxSteps; step++ {
		if err := ctx.Err(); err != nil {
			_ = a.Logger.LogSessionError(turn, step, "interrupted: "+err.Error())
			_ = a.Logger.LogTurnEnd(turn, OutcomeInterrupted.String(), step, totalTokens)
			return messages, resultFor(OutcomeInterrupted, err)
		}
		if err := a.Logger.Err(); err != nil {
			return messages, resultFor(OutcomeStorageError, err)
		}

		if a.Config.ManageContext {
			before := len(messages)
			prepared, err := a.Context.Prepare(ctx, messages)
			if err != nil {
				usageComplete = false
				_ = a.Logger.LogSessionError(turn, step, fmt.Sprintf("context preparation failed: %v", err))
				_ = a.Logger.LogTurnEnd(turn, OutcomeLLMError.String(), step, totalTokens)
				return messages, resultFor(OutcomeLLMError, err)
			}
			if prepared.Summarized {
				llmCalls++
				addUsage(prepared.SummaryUsage, prepared.SummaryUsageReported)
			}
			messages = prepared.Messages
			if prepared.Evicted > 0 {
				_ = a.Logger.LogContextPruning(turn, step, before, len(messages), prepared.Evicted)
			}
			if prepared.Summarized {
				_ = a.Logger.LogContextSummary(turn, step, prepared.Summary, prepared.SummaryUsage, prepared.SummaryUsageReported)
			}
			a.Progress.ContextPruned(step, before, len(messages))
			if prepared.Evicted > 0 || prepared.Summarized {
				if err := a.saveContext(turn, step, messages); err != nil {
					_ = a.Logger.LogTurnEnd(turn, OutcomeStorageError.String(), step, totalTokens)
					return messages, resultFor(OutcomeStorageError, err)
				}
			}
		}

		contextSHA, _ := llm.HashMessages(messages)
		_ = a.Logger.LogLLMRequest(turn, step, len(messages), ctxmgr.EstimateTokens(messages), contextSHA)
		reqStarted := a.Progress.Request(step, len(messages), len(a.Tools.Schemas()))
		llmCalls++
		steps++
		resp, err := a.LLM.ChatCompletion(ctx, messages, a.Tools.Schemas())
		if err != nil {
			usageComplete = false
			_ = a.Logger.LogSessionError(turn, step, fmt.Sprintf("LLM request failed: %v", err))
			_ = a.Logger.LogTurnEnd(turn, OutcomeLLMError.String(), step, totalTokens)
			return messages, resultFor(OutcomeLLMError, err)
		}
		addUsage(resp.Usage, resp.UsageReported)
		if len(resp.Choices) == 0 {
			_ = a.Logger.LogSessionError(turn, step, "LLM response contained no choices")
			_ = a.Logger.LogTurnEnd(turn, OutcomeLLMError.String(), step, totalTokens)
			return messages, resultFor(OutcomeLLMError, fmt.Errorf("empty choices in LLM response"))
		}

		choice := resp.Choices[0]
		_ = a.Logger.LogLLMResponse(turn, step, choice.Message.Content, choice.Message.ToolCalls, resp.Usage)
		a.Progress.Response(step, reqStarted, len(choice.Message.ToolCalls), resp.Usage.TotalTokens)

		if len(choice.Message.ToolCalls) == 0 {
			// Preserve the assistant's final answer in history: in a
			// multi-turn session the next turn must still see it.
			messages = append(messages, choice.Message)
			if err := a.saveContext(turn, step, messages); err != nil {
				_ = a.Logger.LogTurnEnd(turn, OutcomeStorageError.String(), step+1, totalTokens)
				return messages, resultFor(OutcomeStorageError, err)
			}
			_ = a.Logger.LogFinish(turn, step, choice.Message.Content, step+1, totalTokens)
			_ = a.Logger.LogTurnEnd(turn, OutcomeSuccess.String(), step+1, totalTokens)
			a.Progress.TurnEnd(turn, OutcomeSuccess.String(), step+1)
			if err := a.Logger.Err(); err != nil {
				return messages, resultFor(OutcomeStorageError, err)
			}
			result := resultFor(OutcomeSuccess, nil)
			result.FinalMessage = choice.Message.Content
			return messages, result
		}

		messages = append(messages, choice.Message)

		for _, call := range choice.Message.ToolCalls {
			toolCalls++
			toolMsg := a.dispatchToolCall(ctx, turn, step, call)
			messages = append(messages, toolMsg)
		}
		if err := a.saveContext(turn, step, messages); err != nil {
			_ = a.Logger.LogTurnEnd(turn, OutcomeStorageError.String(), step+1, totalTokens)
			return messages, resultFor(OutcomeStorageError, err)
		}
	}

	if err := a.saveContext(turn, step, messages); err != nil {
		_ = a.Logger.LogTurnEnd(turn, OutcomeStorageError.String(), step, totalTokens)
		return messages, resultFor(OutcomeStorageError, err)
	}
	_ = a.Logger.LogSessionError(turn, step, fmt.Sprintf("max_steps (%d) exceeded without a final response", a.Config.MaxSteps))
	_ = a.Logger.LogTurnEnd(turn, OutcomeMaxStepsExceeded.String(), step, totalTokens)
	a.Progress.TurnEnd(turn, OutcomeMaxStepsExceeded.String(), step)
	if err := a.Logger.Err(); err != nil {
		return messages, resultFor(OutcomeStorageError, err)
	}
	return messages, resultFor(OutcomeMaxStepsExceeded, nil)
}

// dispatchToolCall executes a single tool call, applies the loop guard
// and context truncation, logs the result, and returns the tool-role
// message to append to the conversation.
func (a *Agent) dispatchToolCall(ctx context.Context, turn, step int, call llm.ToolCall) llm.Message {
	var args map[string]any
	if call.Function.Arguments != "" {
		if err := json.Unmarshal([]byte(call.Function.Arguments), &args); err != nil {
			args = map[string]any{}
		}
	}

	_ = a.Logger.LogToolCall(turn, step, call.ID, call.Function.Name, args)
	toolStarted := a.Progress.ToolCall(step, call.Function.Name, args)

	start := time.Now()
	result, err := a.Tools.Execute(ctx, call.Function.Name, args)
	latencyMs := float64(time.Since(start).Microseconds()) / 1000.0
	if err != nil {
		result = &tools.ToolResult{Success: false, Error: err.Error()}
	}
	a.Progress.ToolResult(step, call.Function.Name, result.Success, toolStarted, result.Error)

	resultJSON, _ := result.ToJSON()
	output := resultJSON

	// Repeated tool-call loop guard (NFR-3.6): the same (name, args,
	// result) recurring past the threshold gets a corrective notice
	// instead of being forwarded silently.
	sig := callSignature{name: call.Function.Name, argsJSON: call.Function.Arguments, resultJSON: resultJSON}
	if a.lastSignature != nil && *a.lastSignature == sig {
		a.repeatCount++
	} else {
		a.repeatCount = 1
	}
	a.lastSignature = &sig

	loopGuard := false
	if a.Config.MaxRepeatedToolCalls > 0 && a.repeatCount > a.Config.MaxRepeatedToolCalls {
		loopGuard = true
		_ = a.Logger.LogLoopGuardTriggered(turn, step, call.Function.Name, args, a.repeatCount)
		output = fmt.Sprintf(
			"[LOOP DETECTED] This exact call already produced this result %d times in a row. Try a different approach instead of repeating it.\n\n%s",
			a.repeatCount, resultJSON,
		)
	}

	var truncation *ctxmgr.TruncationInfo
	if a.Config.ManageContext && a.Config.ContextConfig.TruncateToolOutputs {
		truncated, info, terr := a.Context.TruncateToolOutput(call.ID, output)
		if terr != nil {
			_ = a.Logger.LogSessionError(turn, step, fmt.Sprintf("tool output truncation failed: %v", terr))
		} else {
			output = truncated
			truncation = info
			if info != nil {
				_ = a.Logger.LogContextTruncation(turn, step, call.ID, info.SpoolFile, info.OmittedChars)
			}
		}
	}

	exitCode := 0
	if ec, ok := result.Metadata["exit_code"].(int); ok {
		exitCode = ec
	} else if !result.Success {
		exitCode = 1
	}

	modelVisible := ""
	if loopGuard || truncation != nil {
		modelVisible = output
	}
	spoolFile := ""
	omittedChars := 0
	if truncation != nil {
		spoolFile = truncation.SpoolFile
		omittedChars = truncation.OmittedChars
	}
	_ = a.Logger.LogToolResult(
		turn, step, call.ID, call.Function.Name,
		result.Success, exitCode, result.Output, modelVisible,
		spoolFile, omittedChars, latencyMs,
	)

	return llm.Message{Role: llm.RoleTool, ToolCallID: call.ID, Content: output}
}
