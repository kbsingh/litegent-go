# Agent Guide: Engineering, Architecture & Developer Instructions

**Project Name**: Litegent
**Target Environment**: Linux
**Target Audience**: AI Coding Agents & Human Software Engineers
**Document Version**: 2.0.0
**Status**: Current — reflects the shipped implementation

---

## 1. Core Mandates for AI Coding Agents & Developers

When contributing to, implementing, or extending Litegent, all developers (both autonomous AI coding agents and human engineers) must strictly comply with the following principles.

### 1.1 Git Repository & Version Control Discipline

1. **Repository Scope**: A Git repository must be initialized at `/home/rh-claude/src/litegent_son_py` to track all project code, configurations, tests, and documentation.
2. **Regular, Atomic Check-ins**:
   - Code must be committed regularly at the completion of each logical milestone or discrete sub-task (never batch multiple unrelated phases into a single unreviewed commit).
   - Before committing, inspect `git status` and `git diff` to ensure no unexpected temporary files, secrets, or logs are staged.
3. **Mandatory Git Commit Message Metadata**:
   Every git commit must follow the standard imperative commit convention and **must include the following four metadata trailers**, in addition to any attribution lines a specific coding harness requires:
   - `Coding-Agent`: The identifier of the coding agent performing the commit (e.g., `opencode`, `claude-code`).
   - `LLM`: The exact model ID powering the agent (e.g., `google/gemini-3.8-flash`, `claude-sonnet-5-5`).
   - `Host`: The machine hostname where the change is made.
   - `Directory`: The absolute working directory path (e.g., `/home/rh-claude/src/litegent_son_py`).

   **Standard Commit Message Format**:
   ```text
   <type>(<scope>): <concise imperative summary (max 50 chars)>

   <Detailed paragraph explaining what was changed, why the change was made,
   and how it was verified. Highlight any edge cases or design tradeoffs.>

   Coding-Agent: <agent identifier>
   LLM: <model id>
   Host: <hostname>
   Directory: /home/rh-claude/src/litegent_son_py
   ```

---

### 1.2 Engineering Quality, Security & Maintainability

#### 1. Code Cleanliness & Idiomatic Standards
- Write modern, idiomatic Go 1.26.8+ code: run `gofmt` (or `goimports`) on every file and keep `go vet ./...` clean.
- Favor Go's static type system and explicit error returns (`if err != nil { return fmt.Errorf("...: %w", err) }`) over hidden control flow; do not use panics for expected error conditions.
- Ensure all exported packages, types, and functions carry a standard Go doc comment (starting with the identifier name) that explains *why* logic exists rather than stating the obvious.
- Avoid unnecessary external dependencies: lean on Go's standard library (`net/http`, `encoding/json`, `os/exec`, `context`, `path/filepath`, `regexp`, `flag`, `bufio`, `log/slog`) complemented only by a small, well-justified set of third-party packages — currently just one: `github.com/bmatcuk/doublestar/v4` for recursive `**` glob matching (`path/filepath.Glob` has no recursive support). Configuration files are JSON only, parsed with the standard library `encoding/json`. CLI flag parsing uses the standard library `flag` package — register each short/long alias twice against the same backing field, since stdlib `flag` already treats `-x`/`--x` identically.
- Before reaching for a stdlib function, check whether it already does what you're about to hand-roll — e.g. `exec.CommandContext` already kills the process on context cancellation by default; don't reassign `cmd.Cancel` to the same behavior.

#### 2. Dual Maintainability (Human & AI Agents)
- Code must be structured and documented so that it is instantly comprehensible and safely modifiable by both human software engineers and AI coding agents.
- Maintain single-responsibility modularity, one Go package per concern:
  - `cmd/litegent/main.go`: Only wires flag parsing to `os.Exit`; no business logic.
  - `internal/cliapp/`: Only handles command-line argument parsing, validation, routing, and the interactive stdin loop's I/O.
  - `internal/config/`: Only handles configuration parsing, environment interpolation, and validation.
  - `internal/agent/`: Only orchestrates the agentic loop lifecycle (single-shot and interactive) — no knowledge of stdin/stdout or CLI text commands.
  - `internal/ctxmgr/`: Only implements context budgeting, output truncation, sliding window, and summarization.
  - `internal/skills/`: Only handles Markdown skill ingestion.
  - `internal/logging/`: Only handles the dual-mode, on-disk audit log (JSONL trace + human transcript).
  - `internal/progress/`: Only handles the live, human-facing stderr progress stream (`-v`) — a presentation concern, independent of and in addition to `internal/logging`'s on-disk audit trail. Never required for correctness: every method is nil-receiver-safe and a disabled `*Reporter` is a true no-op.
  - `internal/llm/`: Only manages HTTP communication with the LLM provider; also owns the shared `Message`/`ToolCall` wire types and message hashing.
  - `internal/session/`: Only owns session identity, directory layout, locking, manifests, and durable context persistence.
  - `internal/tools/`: Individual tool implementations isolated from the agent runtime, each behind the shared `Tool` interface, sharing small helpers (`functionSchema`, `requireStringArg`, etc.) from `registry.go` to avoid repeating schema/validation boilerplate.

#### 3. Operate Unconfined — Safety Through Auditability, Not Restriction
- **No Sandbox, No Path Jail**: Litegent is intentionally unconfined. The agent and its tools operate with the full filesystem and process privileges of the invoking user across the entire host — not just the working directory. `read_file`, `write_file`, `edit_file`, `glob_search`, `grep_search`, and `bash` must never reject, rewrite, or refuse a path for being "outside the project."
  - `spool_dir` and `log_dir` are session-relative paths (defaults `spool` and `logs`) under `<top_dir>/<session-id>/`; they exist for tidy per-session storage, not as tool access boundaries.
  - Because there is no sandbox, the dual audit log (Section 4) is the real safety control: every tool call and its exact arguments and output must be captured so any action taken anywhere on the host is fully reconstructable after the fact.
- **No Cleartext Secrets**: Never commit or log API keys or bearer tokens. All API keys in runtime logs and progress output must be masked as `<first 3 chars>...<last 4 chars>`, preserving the key's own stub prefix (e.g., `sk-...1234`, `r9_...7890`); keys of 7 characters or fewer must be masked entirely (`****`), since a 3+4 character reveal would expose the whole key.
- **Safe, Non-Interactive Subprocess Execution**:
  - All shell commands executed via the `bash` tool must run with an explicit `context.WithTimeout`-derived deadline and a short `cmd.WaitDelay`, to eliminate orphaned or runaway processes. Let `exec.CommandContext`'s default `cmd.Cancel` behavior (kill the process) stand rather than reassigning it to the same thing.
  - `cmd.Stdin` must be left at its zero value (`nil`, never inherited from the parent terminal) so it reads as immediate `EOF`, preventing hangs on commands that expect interactive input.
  - Layer `GIT_PAGER=cat`, `PAGER=cat`, `GIT_TERMINAL_PROMPT=0`, `DEBIAN_FRONTEND=noninteractive`, and `TERM=dumb` onto the subprocess environment so pagers, editors, and confirmation prompts never block the loop (e.g., `git log`, `git diff`, `apt-get install`).
  - The same "never block forever" principle applies to interactive-mode stdin reads: run the blocking read in a goroutine selected against the cancellable root context so `SIGINT`/`SIGTERM` can still end the session while it's waiting at the prompt.
- **Repeated Tool-Call Loop Guard**:
  - The agentic loop (`internal/agent`) must track a `(tool, args, result)` signature per turn and, when the same one recurs more than `max_repeated_tool_calls` (default `3`) times in a row, inject a `[LOOP DETECTED]` corrective note and log a `loop_guard_triggered` trace event — so a stuck pattern is interrupted long before `max_steps` is exhausted. This state must reset at the start of every turn so a pattern from one interactive turn never falsely flags the next turn's first call.
- **Fail-Safe Tool Contracts**:
  - A tool failure (such as a command exiting with code 1 or a file not found) must **never** crash the Litegent process. It must return a structured `ToolResult{Success: false, Error: "..."}` so the LLM can observe the failure and attempt self-correction. Go errors returned from a tool's `Execute` method should be reserved for truly unexpected/internal faults; expected failure modes belong in `ToolResult.Error`.

---

## 2. Agentic Loop Mechanics

### 2.1 The Agent State Machine

Both `Agent.Run` (single-shot) and `Agent.RunInteractive` (persistent multi-turn) delegate to the same per-turn loop (`runTurn`):

```
                  ┌────────────────────────┐
                  │      INITIALIZE        │
                  │ - Parse CLI flags      │
                  │ - Load Config & Skills │
                  │ - Init Messages        │  (Run: [system,user] — RunInteractive: [system])
                  └───────────┬────────────┘
                              │
                              ▼
               ┌──────────────────────────────┐
               │   NEW TURN (RunInteractive)   │◄──────────────────────────────┐
               │ - readPrompt() for next turn │  (Run calls runTurn once)      │
               │ - reset loop-guard state     │                                │
               └──────────────┬───────────────┘                                │
                              │                                                │
                              ▼                                                │
               ┌──────────────────────────────┐                                │
               │    PREPARE CONTEXT           │◄───────────────────┐           │
               │ - Truncate excessive outputs │                    │           │
               │ - Prune sliding window       │                    │           │
               │ - Summarize only if window   │                    │           │
               │   overflowed AND over budget │                    │           │
               └──────────────┬───────────────┘                    │           │
                              │                                    │           │
                              ▼                                    │           │
               ┌──────────────────────────────┐                    │           │
               │       QUERY LLM              │                    │           │
               │ - Send messages + tool specs │                    │           │
               │ - Record JSONL request trace │                    │           │
               └──────────────┬───────────────┘                    │           │
                              │                                    │           │
                              ▼                                    │           │
               ┌──────────────────────────────┐                    │           │
               │       EVALUATE RESPONSE      │                    │           │
               │ - Record JSONL response trace│                    │           │
               └──────────────┬───────────────┘                    │           │
                              │                                    │           │
              Tool Calls? ────┴──── No Tool Calls                  │           │
             │                      │                              │           │
             ▼                      ▼                              │           │
┌───────────────────────────┐ ┌──────────────────────────────────┐ │           │
│     DISPATCH TOOLS        │ │      TURN/SESSION CONCLUDES       │ │           │
│ - Loop-guard check        │ │ - Append final msg to history     │ │           │
│ - Execute native tool     │ │ - Output final assistant message  │ │           │
│ - Capture output & errors │ │ - Run(): exit 0. RunInteractive(): │ │          │
│ - Append ToolMessages     │ │   onTurnResult() decides continue │ │          │
└─────────────┬─────────────┘ │   vs. end; if continuing, loop to │──┘          │
              │                │   NEW TURN with history intact    │            │
              │                └──────────────────────────────────┘            │
              └─────────────────────────────────────────────────────────────────┘
```

### 2.2 Loop Invariants
1. **System Prompt Permanence**: The system prompt (`messages[0]`) containing foundational behavioral rules and loaded skills must never be modified or evicted.
2. **First-Turn User Anchor**: The first user message in the conversation (`messages[1]` — single-shot `-p` content, or interactive mode's first turn) must never be evicted during sliding window pruning.
3. **Per-Turn Bounded Execution**: Each turn must enforce `step < max_steps`. In single-shot mode, exceeding it terminates the process with exit code `2`. In interactive mode, exceeding it ends only that turn (reported on stderr); the session continues with history intact, since `max_steps` is a per-turn budget, not a session-wide one.
4. **Loop-Guard State Is Per-Turn**: The repeated-tool-call signature/count must reset at the start of every turn.

---

## 3. Context Management Specifications (`manage_context`)

Context management is critical to prevent token exhaustion and context window overflow:

### 3.1 Tool Output Truncation
- Truncation runs only when `manage_context && truncate_tool_outputs`; `max_tool_output_chars` defaults to 4000.
- When a tool return is oversized:
  - Save the complete model-facing tool-result payload into `<session-dir>/<spool_dir>/output_<call_id>.txt` (default `spool/` inside the session). The human log still records the raw tool output separately.
  - Return a spliced response with 60% head, a truncation banner naming the omitted count and spool path, and 30% tail.
  - Log both the full raw output and the exact model-visible content.

### 3.2 Sliding Window Pruning
- Retain the most recent whole units up to `window_size` **messages** (default 40).
- An assistant tool-call message is always evicted together with its tool results; never split a pair.
- Always retain at least the most recent unit, even if it alone exceeds the window.

### 3.3 Progressive Summarization — Gated on the Actual Token Budget
- Summarization is **not** a standalone trigger; it only happens as a consequence of sliding-window eviction (3.2), and only when the pre-eviction token estimate exceeds `max_context_tokens` (default 32000).
- Under budget, evicted messages are dropped without a summarization call.
- When summarization runs, evicted messages plus any prior summary are condensed into `messages[2]`.
- Don't regress to unconditional summarization.

### 3.4 Durable Context Blob
- The effective message list must be saved atomically to `<session-dir>/context.json` after user input, after compaction, after a complete tool step, and after a final answer.
- Context must be validated before save and after load: system first, summary only at index 2, complete tool-call/result pairs.
- Compaction must save the new context before the next request.
- Resume must load and validate the blob; missing/corrupt blobs hard-fail.

---

## 4. Dual Logging Specifications

Every run writes two distinct per-run log files under `<session-dir>/<log_dir>/` (default `logs/`). Because Litegent is unconfined (no sandbox), these logs are the real safety/accountability mechanism — see Section 1.2.3.

### 4.1 JSONL Event Log (`trace_<run-id>.jsonl`)
- Machine-readable, append-only JSON objects, one per line.
- Events: `session_start`, `user_prompt`, `llm_request`, `llm_response`, `tool_call`, `tool_result`, `context_truncation`, `context_pruning`, `context_summary`, `loop_guard_triggered`, `turn_finish`, `session_finish`, `run_finish`, `session_error`.
- Every event includes `timestamp`, `event_type`, `turn`, `step`, and an event-specific `data` payload.
- `tool_result` carries the full raw output plus `model_content`/spool metadata when truncation or the loop guard changed what the model saw.
- Sensitive API keys must be redacted recursively, including nested tool arguments and results.

### 4.2 Human-Readable Session Log (`session_<run-id>.log`)
- Stable, parseable headers and delimiters for every turn, step, assistant response, tool call, tool result, compaction event, and run boundary.
- Full system prompt/skills, user inputs, assistant content, full tool arguments, full raw tool output, exact model-visible content when truncated, summaries, and errors.
- Tool-call argument order must be deterministic (JSON) so the same call renders identically across runs.
- Readable in any standard pager (`less`, `cat`, editor) without parsing JSON.

### 4.3 Durable Context (`context.json`)
- One authoritative blob per session, updated atomically whenever the effective context changes.
- Must be validated and hash-checked; compaction must persist the new list before the next request.
- Resume loads this blob instead of reconstructing history from logs.

### 4.3 Live Progress (`-v`, distinct from the files above)
- `internal/progress.Reporter` streams concise lines to stderr as the loop runs — a presentation convenience, not part of the audit trail. It must never be the only record of what happened; the dual log above is always written regardless of `-v`.
- Must mask secrets the same way the dual log does.

---

## 5. Verification & Testing Requirements

Before marking any milestone or task complete:
1. **Unit Tests**: Every package must have comprehensive unit tests in a colocated `*_test.go` file (idiomatic Go convention — no separate top-level `tests/` directory).
2. **Integration Tests**: End-to-end agentic flows must be validated using mock LLM responses (e.g., an `httptest.Server` standing in for the OpenAI-compatible endpoint). Multi-turn behavior (history persistence, per-turn loop-guard reset) must be tested explicitly, not just single-shot flows.
3. **Execution Command**: All tests must execute cleanly using `go test ./... -v`, and `go vet ./...` and `gofmt -l .` must report zero issues.
4. **Live Smoke Test**: Before shipping a binary (`./install.sh`), run the actual compiled binary — not just `go test` — against a throwaway local mock LLM server and inspect the resulting `trace_*.jsonl`/`session_*.log` for format compliance.
5. **Clean Git Tree**: No untracked files, temp files, or leftover artifacts.
