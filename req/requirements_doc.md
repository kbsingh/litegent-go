# Requirements Document: Litegent CLI AI Agent

**Project Name**: Litegent  
**Target Platform**: Linux (x86_64, aarch64)  
**Document Version**: 2.0.0  
**Status**: Current — reflects the shipped implementation  

---

## 1. Executive Overview

**Litegent** is a lightweight, autonomous command-line AI agent designed specifically for Linux operating environments. It implements an autonomous agentic loop capable of reasoning, executing native host tools, observing feedback, and iterating until a task is completely resolved.

Litegent eliminates the operational complexity of external microservices or Model Context Protocol (MCP) servers by packaging built-in, native developer and sysadmin tools. It features robust context management mechanisms (output truncation, disk spooling, sliding-window message pruning, and conversation summarization) to operate reliably within bounded token constraints. Furthermore, it records an audit log of every LLM and system interaction in both raw machine-readable JSONL and human-readable transcript formats.

---

## 2. Problem Statement & Objectives

### 2.1 Problem Statement
Modern AI coding and operations agents often suffer from:
1. **Excessive Runtime Bloat**: Dependency on external background daemon servers (like MCP) introducing latency, fragility, and installation barriers.
2. **Context Blowup & Token Exhaustion**: Subprocess outputs (such as `git diff`, `go test`, `journalctl`, or `find`) quickly exceed LLM context windows, resulting in truncated context or API context length errors.
3. **Lack of Interaction Auditability**: Black-box execution loops make debugging agent mistakes or inspecting raw LLM payloads difficult.
4. **Rigid Interface Patterns**: Difficulty in parameterizing agents cleanly for automated CI/CD or scripting via explicit file inputs.

### 2.2 Project Objectives
- Provide a single, self-contained CLI executable on Linux.
- Decouple execution inputs into explicit file inputs: system prompt (`-s`), task prompt (`-p`), configuration (`-c`), and optional skills (`-k`).
- Run a self-directed agentic loop until completion without requiring interactive human hand-holding.
- Deliver built-in developer tools (bash execution, file manipulation, file search) with zero external server dependencies.
- Ensure context resilience through a unified `manage_context` engine.
- Deliver dual-mode persistence for all LLM and tool interactions (JSONL traces + human session transcripts).

---

## 3. Stakeholder & User Personas

1. **Software Engineers**: Require an autonomous CLI agent to investigate test failures, refactor codebases, patch bugs, and verify changes with local linters/tests.
2. **Linux SysAdmins & SREs**: Require an agent to triage service health, monitor system resource pressure, investigate logs, and troubleshoot incidents.
3. **Security Auditors**: Require an agent to audit file permissions, scan for secrets/keys, and review configurations without sending sensitive data to unauthorized third parties.
4. **CI/CD Pipelines & Automation Scripts**: Need a deterministic, non-interactive CLI agent that consumes file-based prompts and produces structured audit trails with standard exit codes.

---

## 4. Functional Requirements

### 4.1 Command-Line Interface (CLI)

- **FR-1.1: Invocation Syntax**  
  The agent must be executable via the compiled `litegent` binary (installed on `PATH` or invoked by path), or via `go run ./cmd/litegent` during development.
  
- **FR-1.2: System Prompt File (`-s, --system`)**  
  - Must accept a mandatory path pointing to a readable file containing the system prompt.
  - If the file does not exist or is not readable, Litegent must exit immediately with an error message and exit code `1`.

- **FR-1.3: User Prompt File (`-p, --prompt`)**  
  - Must accept a mandatory path pointing to a readable file containing the user's execution prompt.
  - If the file does not exist or is not readable, Litegent must exit immediately with an error message and exit code `1`.

- **FR-1.4: Configuration File (`-c, --config`)**  
  - Must accept a mandatory path pointing to a valid configuration file formatted in either JSON or YAML.
  - If the file syntax is invalid or required fields are missing, Litegent must exit immediately with a descriptive schema validation error and exit code `1`.

- **FR-1.5: Skills Directory (`-k, --skills`)**  
  - Shall accept an optional path pointing to a directory containing skill documentation files.
  - If specified, Litegent must recursively discover all Markdown (`*.md`) files in this directory.
  - If the directory does not exist when `-k` is passed, Litegent must exit with an error.

- **FR-1.6: Informational Flags**  
  - Shall support `--help` to display concise usage instructions.
  - Shall support `--version` to output the current version string.

- **FR-1.7: Interactive Mode (`-i, --interactive`)**  
  - Shall accept an optional flag that switches Litegent into a persistent, multi-turn session instead of a single task.
  - Conflicts with `-p/--prompt`: passing both must exit immediately with an error and exit code `1`.
  - When `-i` is set, `-p` is not required; task prompts are instead read one at a time from stdin.

---

### 4.2 Agentic Loop Execution

- **FR-2.1: Loop Initialization**  
  In single-shot mode (no `-i`), the agent must initialize the dialogue context with:
  1. System message containing the content from `-s`, augmented by loaded skills from `-k` (if provided).
  2. User message containing the content from `-p`.

  In interactive mode (`-i`), initialization instead seeds only the system message; the first (and every subsequent) user message is appended per turn as it's read from stdin — see FR-7.1.

- **FR-2.2: Autonomous Iteration**  
  The agent must loop continuously through the following stages, shared identically by single-shot and interactive mode (one iteration of this loop is one "turn" in FR-7.1):
  1. Inspect and compact context if `manage_context` is enabled.
  2. Transmit conversation history and tool declarations to the LLM backend.
  3. Parse the LLM response.
  4. If the response contains one or more tool calls:
     a. Execute each tool with the provided arguments.
     b. Capture execution output (stdout, stderr, exit code, errors).
     c. Record tool results into conversation history as tool messages.
     d. Iterate back to step 1.
  5. If the response contains no tool calls:
     a. Append the assistant's final response to conversation history, then emit its content to stdout.
     b. In single-shot mode, terminate the process with exit code `0`. In interactive mode, the turn concludes and the session prompts for the next turn (FR-7.1) instead of terminating.

- **FR-2.3: Step Safety Limit (`max_steps`)**  
  The loop must respect a configurable `max_steps` ceiling (default: `30`). If reached without a final response, Litegent must halt, notify the user on stderr, and exit with code `2`.

- **FR-2.4: Signal Handling & Cancellation**  
  Litegent must trap `SIGINT` (Ctrl+C) and `SIGTERM`, terminate any active subshell processes cleanly, flush all in-memory logs to disk, and exit with code `130`.

- **FR-2.5: Repeated Tool-Call Loop Guard**  
  The loop must track recent `(tool_name, arguments, result)` triples. When the same triple recurs more than `max_repeated_tool_calls` consecutive times (default: `3`), Litegent must append a synthetic tool-result note (e.g. `[LOOP DETECTED] This exact call already produced this result. Try a different approach.`) instead of forwarding the repeated result silently, and record a `loop_guard_triggered` trace event.

- **FR-2.6: Thinking/Reasoning Passthrough**  
  The configuration must expose `thinking_level` (free text) and `thinking_format` (`"reasoning_effort"` default | `"thinking"`). When `thinking_level` is omitted, empty, or the reserved value `"default"`, no thinking parameter is sent and the model decides. Any other value must be forwarded to the LLM endpoint verbatim — no trimming, lowercasing, or translation — using exactly one wire shape: `{"reasoning_effort": "<level>"}` or `{"thinking": {"type": "<level>"}}`. The setting applies to every chat-completions request in the session, including context-summarization sub-calls. This is strictly opt-in passthrough: Litegent must not probe for endpoint support or automatically fall back; an endpoint that rejects the parameter surfaces its normal error. `thinking_format` is validated (and must be one of the two known values) only when a level is active.

---

### 4.3 Context Management Capabilities (`manage_context`)

- **FR-3.1: Master Switch**  
  A top-level configuration boolean `manage_context` shall enable or disable the entire context management subsystem. When `manage_context` is `false`, conversation history is forwarded to the LLM unmodified without truncation or window pruning.

- **FR-3.2: Tool Output Truncation & Spooling**  
  When `manage_context` is `true`:
  - Any single tool return exceeding `max_tool_output_chars` (default: 4000 characters) must be truncated.
  - The entire untruncated output must be written to a temporary spool file under `spool_dir` (default: `.tmp/tool_outputs/<call_id>.txt` within the working directory).
  - The tool message returned to the model must include:
    1. The head of the output (e.g. first 2000 characters).
    2. A truncation notice: `[Truncated: <X> characters omitted. Full content saved to <path>. Use read_file to inspect specific lines if needed.]`.
    3. The tail of the output (e.g. last 1000 characters).

- **FR-3.3: Sliding Window History**  
  When `manage_context` is `true`:
  - When the message list length exceeds `window_size` turns (default: 20):
    - System prompt (index 0) and initial user prompt (index 1) **must never be dropped**.
    - The oldest intermediate tool execution turns must be evicted from the active context.

- **FR-3.4: Conversation Summarization**  
  When `manage_context` is `true` and the sliding window (FR-3.3) evicts turns:
  - If `max_context_tokens` is configured as a positive value and the pre-eviction estimated token count does **not** exceed it, the evicted turns must simply be dropped — no summarization call is made, avoiding an unnecessary LLM round-trip on every window overflow.
  - Otherwise (the estimate exceeds `max_context_tokens`, or `max_context_tokens` is unset/non-positive), the evicted message turns must be passed to a summarization prompt, and the resulting summary must be maintained as a persistent `<context_summary>` message placed directly after the first user message in the conversation.
  - Summarization must never be triggered independently of a window eviction — it is a consequence of FR-3.3, not a standalone token-count trigger.

---

### 4.4 Built-in Tool Suite (No MCP)

Litegent shall provide native Go implementations of the following core tools:

- **FR-4.1: `bash`**  
  - Runs shell commands asynchronously in a Linux subshell, unconfined: `workdir` and the command itself may reference any path on the host (see NFR-3.4).
  - Runs non-interactively per NFR-3.5 (empty stdin, pager/prompt neutralization) so interactive commands fail fast instead of hanging.
  - Parameters: `command` (string, required), `workdir` (string, optional), `timeout` (integer, optional, default: 60s).
  - Returns: `exit_code`, `stdout`, `stderr`, `timed_out`.

- **FR-4.2: `read_file`**  
  - Reads text files line-by-line with optional slice bounds.
  - Parameters: `file_path` (string, required), `offset` (integer, optional, default: 1), `limit` (integer, optional, default: 2000).
  - Returns: line-numbered content string, total file lines, slice offset, count.

- **FR-4.3: `write_file`**  
  - Writes text content to disk, automatically creating missing parent directories.
  - Parameters: `file_path` (string, required), `content` (string, required).
  - Returns: bytes written, absolute destination path.

- **FR-4.4: `edit_file`**  
  - Performs exact string matching and replacement.
  - Parameters: `file_path` (string, required), `old_string` (string, required), `new_string` (string, required), `replace_all` (boolean, optional, default: false).
  - Returns: number of matches replaced, confirmation of file update.
  - Errors: Fails cleanly if `old_string` is not found or matches multiple times when `replace_all` is false.

- **FR-4.5: `glob_search`**  
  - Matches filenames against glob patterns.
  - Parameters: `pattern` (string, required), `path` (string, optional, default: `.` ).
  - Returns: list of matching relative file paths and match count.

- **FR-4.6: `grep_search`**  
  - Searches file contents using regex across files in a path.
  - Parameters: `pattern` (string, required), `path` (string, optional, default: `.`), `include` (string, optional).
  - Returns: list of matches with file path, line number, and line content.

---

### 4.5 Skills Management (`-k`)

- **FR-5.1: File Format**  
  Skills must be authored in standard Markdown (`*.md`).
- **FR-5.2: Parsing & Ingestion**  
  Litegent must recursively find all `*.md` files in the provided directory, extract their title (`# Title`) and content, and assemble an XML block:
  ```xml
  <available_skills>
    <skill name="Skill Title" file="path/to/skill.md">
      ... skill instructions ...
    </skill>
  </available_skills>
  ```
- **FR-5.3: Prompt Augmentation**  
  The assembled skills block must be seamlessly injected into the initial system prompt before loop initiation.

---

### 4.6 Dual Interaction Logging

- **FR-6.1: Destination Directory**  
  All session logs must be saved under the directory specified by `log_dir` in configuration (default: `./logs/`). If the directory does not exist, it must be created automatically.
  
- **FR-6.2: JSONL Trace File (`trace_<timestamp>.jsonl`)**  
  Must record an append-only JSON line for every system event:
  - Event types: `session_start`, `llm_request`, `llm_response`, `tool_call`, `tool_result`, `context_truncation` (only emitted when a tool output was actually truncated), `loop_guard_triggered` (only emitted when the loop guard actually fires), `session_finish` (once per successfully completed turn — in interactive mode this fires once per turn, not once per process), `session_error` (fatal error, interruption, or a turn exceeding `max_steps`).
  - Every line must include: `timestamp` (ISO 8601 UTC), `event_type`, `step` (int), and an event-specific `data` payload.
  - Sensitive API keys must be masked wherever their literal text could appear, not only in a dedicated field.

- **FR-6.3: Human-Readable Session Log (`session_<timestamp>.log`)**  
  Must record a clean, sequential narrative transcript:
  - Header: Session ID, start timestamp, model name, base URL.
  - Per Step: Step number, LLM thought/reasoning, tool calls with formatted parameters, tool results with summary counts.
  - Footer: Final response, completion timestamp, total steps, total token usage.

---

### 4.7 Interactive Multi-Turn Sessions (`-i`)

- **FR-7.1: Persistent Conversation**  
  When `-i` is set, the agent must initialize the dialogue once with the system prompt (augmented by skills, if provided), then repeatedly read a task prompt from stdin, run it to completion, and prompt for the next one — all within the same process, model-context history, and dual log.

- **FR-7.2: Per-Turn Step Budget**  
  `max_steps` must apply independently to each turn; exceeding it must report the failure for that turn only and continue the session (not terminate it), since conversation history remains valid for the next turn.

- **FR-7.3: Session Termination**  
  Typing `/exit` or `/quit` (case-insensitive), or sending EOF (e.g. Ctrl+D) on stdin, must end the session cleanly with exit code `0`. A fatal LLM error (e.g. authentication failure) or `SIGINT`/`SIGTERM` must end the session immediately with the corresponding exit code (`3` or `130`).

- **FR-7.4: Blank Input**  
  An empty line must simply re-prompt without starting a turn or contacting the LLM.

- **FR-7.5: Turn Visibility (Separators & Tool-Call Notices)**  
  - Every successful final response must be followed on stdout by the separator line `----====----`, in both single-shot and interactive mode, so turn boundaries are visually unambiguous. The separator is presentation only and must never be added to conversation history.
  - In interactive sessions without `-v`, each tool invocation must emit exactly one compact stderr line of the form `|ToolCall : <first line of the tool request>` (tool name plus decoded arguments), produced by masking configured secrets first, then truncating to the first physical line, then clipping to 240 characters — so truncation can never defeat secret masking. The notice is suppressed under `-v`, where the richer per-tool progress line already reports the call.

---

### 4.8 Verbose Progress Reporting (`-v`)

- **FR-8.1: Live Progress Stream**  
  When `-v` is set, Litegent must stream concise, single-line progress updates to stderr as the loop runs — distinct from and in addition to the dual audit log (Section 4.6), which is written to disk for after-the-fact review.
  - Must report: session/turn startup (mode, model, endpoint, tool count), per-turn start, context pruning when the message count changes, per-step LLM request/response timing and tool-call count, per-tool-call arguments and per-result success/duration, turn completion, and session completion.
  - Configured secrets (e.g. the API key) must be masked in all progress output.
  - When `-v` is not set, stderr stays quiet apart from fatal error messages, the interactive `> ` prompt, and the interactive tool-call notice required by FR-7.5.

---

## 5. Non-Functional Requirements

### 5.1 Performance
- **NFR-1.1**: CLI startup latency (parsing arguments and loading configuration) must be under 150 milliseconds.
- **NFR-1.2**: Tool execution overhead (dispatching and returning tool results) must add no more than 10 milliseconds of internal latency beyond the tool process time.

### 5.2 Reliability & Fault Tolerance
- **NFR-2.1**: LLM API network timeouts or 5xx server errors must be retried with exponential backoff (up to 3 retries) before raising a failure.
- **NFR-2.2**: Tool execution errors (e.g. command not found, syntax error in bash, file not found) must never crash the Litegent process; the error string must be returned to the LLM as a tool result so the agent can self-correct.

### 5.3 Security & Safety
- **NFR-3.1**: Subprocess execution must enforce timeouts to prevent orphaned, hanging processes.
- **NFR-3.2**: Configuration parser must support environment variable references (`${OPENAI_API_KEY}`) to avoid storing cleartext secrets on disk.
- **NFR-3.3**: Raw API keys must never be written to JSONL trace logs or console output.
- **NFR-3.4: Unconfined Host Operation**: Litegent is intentionally **not** sandboxed. It operates with the full filesystem and process privileges of the invoking user, across the entire host — not just the invocation directory. No path jail, working-directory confinement, or tool-level access restriction is applied to `bash`, `read_file`, `write_file`, `edit_file`, `glob_search`, or `grep_search`; any of these may address any absolute or relative path, including locations outside the invocation directory, that the OS permissions allow. `spool_dir` and `log_dir` default to paths under the working directory purely for tidy organization, not as an access boundary — they are freely reconfigurable to any path. The comprehensive dual audit log (Section 4.6) is the system's primary safety and accountability control in place of sandboxing; operators are expected to scope the system prompt and run Litegent under a user account whose privileges already match their risk tolerance.
- **NFR-3.5: Non-Interactive Subprocess Execution**: To prevent the agentic loop from stalling on a blocked subprocess, every `bash` invocation must run non-interactively: standard input must never be inherited from a controlling terminal (it must read as immediately empty/EOF), and common interactive behaviors (pagers, editors, confirmation prompts) must be neutralized via environment overrides (e.g. `GIT_PAGER=cat`, `PAGER=cat`, `GIT_TERMINAL_PROMPT=0`, `DEBIAN_FRONTEND=noninteractive`, `TERM=dumb`) layered on top of the inherited environment. The configured `timeout` remains the hard backstop if a command blocks regardless.
- **NFR-3.6: Repeated Tool-Call Loop Guard**: The agentic loop must detect when an identical tool call (same name and arguments) produces an identical result more than `max_repeated_tool_calls` times in a row (default: `3`) and inject a corrective notice into the conversation instead of silently continuing, so the model is nudged to change strategy well before `max_steps` is exhausted.

### 5.4 Maintainability & Compatibility
- **NFR-4.1**: Written in modern Go 1.22+ following idiomatic Go conventions (`gofmt`-clean, passes `go vet`), with exported types, functions, and packages documented via standard Go doc comments.
- **NFR-4.2**: Compatible with all major OpenAI-compatible API providers (OpenAI, OpenRouter, DeepSeek, Groq, Ollama, vLLM).

---

## 6. Success Metrics & Verification Criteria

1. **Autonomous Task Completion**: The agent runs start-to-finish on standard tasks without manual intervention.
2. **Context Window Adherence**: Under high-volume command output (e.g. >100,000 characters), token consumption remains within defined ceilings without API context length errors.
3. **Audit Completeness**: 100% of LLM calls, tool inputs, and tool outputs are captured in both JSONL and transcript logs.
4. **Zero MCP Overhead**: No background MCP daemon or network transport layer is needed to execute bash or file operations.
