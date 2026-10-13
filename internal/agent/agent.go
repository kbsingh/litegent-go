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
	default:
		return "unknown"
	}
}

// Result is the outcome of a full agentic loop run.
type Result struct {
	Outcome      Outcome
	FinalMessage string
	Steps        int
	TotalTokens  int
	Err          error
}

// Agent orchestrates the agentic loop by wiring together the LLM client,
// tool registry, context manager, and dual logger.
type Agent struct {
	Config  config.LitegentConfig
	LLM     *llm.Client
	Tools   *tools.Registry
	Context *ctxmgr.Manager
	Logger  *logging.Logger

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
	messages := []llm.Message{
		{Role: llm.RoleSystem, Content: systemPrompt},
		{Role: llm.RoleUser, Content: userPrompt},
	}
	_, result := a.runTurn(ctx, messages, 1)
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
	messages := []llm.Message{{Role: llm.RoleSystem, Content: systemPrompt}}

	turn := 0
	for {
		if err := ctx.Err(); err != nil {
			return Result{Outcome: OutcomeInterrupted, Steps: turn, Err: err}
		}

		prompt, ok := readPrompt()
		if !ok {
			return Result{Outcome: OutcomeSuccess, Steps: turn}
		}
		turn++

		messages = append(messages, llm.Message{Role: llm.RoleUser, Content: prompt})

		var result Result
		messages, result = a.runTurn(ctx, messages, turn)

		if !onTurnResult(turn, result) {
			return result
		}
	}
}

// runTurn executes the agentic loop for a single turn against the given
// message history (which already includes this turn's user message),
// returning the updated history (including the assistant's final answer,
// so a subsequent turn in an interactive session sees it) alongside the
// turn's Result. turn is used only for progress/log labeling.
func (a *Agent) runTurn(ctx context.Context, messages []llm.Message, turn int) ([]llm.Message, Result) {
	// A stuck pattern from a previous turn must never suppress a
	// legitimate repeated call in a new one.
	a.lastSignature = nil
	a.repeatCount = 0

	a.Progress.TurnStart(turn, a.Config.MaxSteps, len(messages))

	totalTokens := 0
	step := 0
	for ; step < a.Config.MaxSteps; step++ {
		if err := ctx.Err(); err != nil {
			a.Logger.LogSessionError(step, "interrupted: "+err.Error())
			return messages, Result{Outcome: OutcomeInterrupted, Steps: step, TotalTokens: totalTokens, Err: err}
		}

		if a.Config.ManageContext {
			before := len(messages)
			prepared, err := a.Context.Prepare(ctx, messages)
			if err != nil {
				a.Logger.LogSessionError(step, fmt.Sprintf("context preparation failed: %v", err))
				return messages, Result{Outcome: OutcomeLLMError, Steps: step, TotalTokens: totalTokens, Err: err}
			}
			messages = prepared
			a.Progress.ContextPruned(step, before, len(messages))
		}

		a.Logger.LogLLMRequest(step, ctxmgr.EstimateTokens(messages))
		reqStarted := a.Progress.Request(step, len(messages), len(a.Tools.Schemas()))
		resp, err := a.LLM.ChatCompletion(ctx, messages, a.Tools.Schemas())
		if err != nil {
			a.Logger.LogSessionError(step, fmt.Sprintf("LLM request failed: %v", err))
			return messages, Result{Outcome: OutcomeLLMError, Steps: step, TotalTokens: totalTokens, Err: err}
		}
		if len(resp.Choices) == 0 {
			a.Logger.LogSessionError(step, "LLM response contained no choices")
			return messages, Result{Outcome: OutcomeLLMError, Steps: step, TotalTokens: totalTokens, Err: fmt.Errorf("empty choices in LLM response")}
		}

		choice := resp.Choices[0]
		totalTokens += resp.Usage.TotalTokens
		a.Logger.LogLLMResponse(step, choice.Message.Content, resp.Usage.PromptTokens, resp.Usage.CompletionTokens, resp.Usage.TotalTokens)
		a.Progress.Response(step, reqStarted, len(choice.Message.ToolCalls), resp.Usage.TotalTokens)

		if len(choice.Message.ToolCalls) == 0 {
			// Preserve the assistant's final answer in history: in a
			// multi-turn session the next turn must still see it.
			messages = append(messages, choice.Message)
			a.Logger.LogFinish(step, choice.Message.Content, step+1, totalTokens)
			a.Progress.TurnEnd(turn, OutcomeSuccess.String(), step+1)
			return messages, Result{Outcome: OutcomeSuccess, FinalMessage: choice.Message.Content, Steps: step + 1, TotalTokens: totalTokens}
		}

		messages = append(messages, choice.Message)

		for _, call := range choice.Message.ToolCalls {
			toolMsg := a.dispatchToolCall(ctx, step, call)
			messages = append(messages, toolMsg)
		}
	}

	a.Logger.LogSessionError(step, fmt.Sprintf("max_steps (%d) exceeded without a final response", a.Config.MaxSteps))
	a.Progress.TurnEnd(turn, OutcomeMaxStepsExceeded.String(), step)
	return messages, Result{Outcome: OutcomeMaxStepsExceeded, Steps: step, TotalTokens: totalTokens}
}

// dispatchToolCall executes a single tool call, applies the loop guard
// and context truncation, logs the result, and returns the tool-role
// message to append to the conversation.
func (a *Agent) dispatchToolCall(ctx context.Context, step int, call llm.ToolCall) llm.Message {
	var args map[string]any
	if call.Function.Arguments != "" {
		if err := json.Unmarshal([]byte(call.Function.Arguments), &args); err != nil {
			args = map[string]any{}
		}
	}

	a.Logger.LogToolCall(step, call.ID, call.Function.Name, args)
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

	if a.Config.MaxRepeatedToolCalls > 0 && a.repeatCount > a.Config.MaxRepeatedToolCalls {
		a.Logger.LogLoopGuardTriggered(step, call.Function.Name, args, a.repeatCount)
		output = fmt.Sprintf(
			"[LOOP DETECTED] This exact call already produced this result %d times in a row. Try a different approach instead of repeating it.\n\n%s",
			a.repeatCount, resultJSON,
		)
	}

	if a.Config.ManageContext {
		truncated, info, terr := a.Context.TruncateToolOutput(call.ID, output)
		if terr == nil {
			output = truncated
			if info != nil {
				a.Logger.LogContextTruncation(step, call.ID, info.SpoolFile, info.OmittedChars)
			}
		}
	}

	exitCode := 0
	if ec, ok := result.Metadata["exit_code"].(int); ok {
		exitCode = ec
	} else if !result.Success {
		exitCode = 1
	}
	summary := result.Output
	const summaryLimit = 500
	if len(summary) > summaryLimit {
		summary = summary[:summaryLimit] + "... (see trace log for full output)"
	}
	a.Logger.LogToolResult(step, call.ID, call.Function.Name, result.Success, exitCode, summary, latencyMs)

	return llm.Message{Role: llm.RoleTool, ToolCallID: call.ID, Content: output}
}
