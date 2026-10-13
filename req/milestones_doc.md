# Implementation Milestones: Litegent CLI AI Agent

**Project Name**: Litegent
**Target Environment**: Linux
**Document Version**: 2.0.0
**Status**: All milestones below are complete on `main`/`thinner`. This document now serves as the historical build log and the reference order for reproducing the build from scratch — each milestone's Verification Gate must pass before moving to the next.

---

## 1. Roadmap & Phase Summary

```
+-----------------------------------------------------------------------------------------+
|                                    ROADMAP OVERVIEW                                      |
|                                                                                           |
|  [Phase 0] Documentation Suite & Git Init                          [ COMPLETED ]         |
|       │                                                                                  |
|  [Phase 1] Project Scaffolding & Configuration Engine              [ COMPLETED ]         |
|       │                                                                                  |
|  [Phase 2] Built-in Tools Engine (bash, fileops, search)           [ COMPLETED ]         |
|       │                                                                                  |
|  [Phase 3] Context Management Engine (truncate, window, summary)   [ COMPLETED ]         |
|       │                                                                                  |
|  [Phase 4] Skills Loader & Dual Logging Engine                     [ COMPLETED ]         |
|       │                                                                                  |
|  [Phase 5] LLM Client & Core Agentic Loop Engine                   [ COMPLETED ]         |
|       │                                                                                  |
|  [Phase 6] CLI Integration, Roles, Prompts & Config Templates      [ COMPLETED ]         |
|       │                                                                                  |
|  [Phase 7] Full Verification, End-to-End Testing & Release Gate    [ COMPLETED ]         |
|       │                                                                                  |
|  [Phase 8] Simplification Pass (dependency/dead-code cleanup)      [ COMPLETED ]         |
|       │                                                                                  |
|  [Phase 9] Interactive Sessions, Live Progress & Dev Tooling       [ COMPLETED ]         |
+-----------------------------------------------------------------------------------------+
```

---

## 2. Detailed Milestone Specifications

### Milestone 0: Documentation Suite & Git Initialization
- **Status**: Completed
- **Objectives**:
  - Initialize Git repository at `/home/rh-claude/src/litegent_son_py`.
  - Configure `.gitignore` for Go build artifacts, binaries, logs, and caches.
  - Author complete specifications: `requirements_doc.md`, `spec_doc.md`, `deliverable_doc.md`, `agent_guide.md`, `README.md`, `user_guide.md`, and this document.
- **Git Commit Target**: `docs: specify Litegent as an unconfined Go CLI agent` (trailers: `Coding-Agent`, `LLM`, `Host`, `Directory` — see Section 3).

---

### Milestone 1: Project Scaffolding & Configuration Engine
- **Status**: Completed
- **Objectives**:
  - Author `go.mod` declaring `module litegent` and pinning dependencies (`gopkg.in/yaml.v3`, `github.com/bmatcuk/doublestar/v4`).
  - Create the package skeleton: `cmd/litegent/` and `internal/{cliapp,config,agent,ctxmgr,skills,logging,llm,tools,version}/`.
  - Implement `internal/config/config.go` supporting both JSON and YAML configuration formats (a single `yaml.Unmarshal` call handles both, since JSON is valid YAML).
  - Implement environment variable interpolation (supporting `${VAR_NAME}` and `${VAR_NAME:-default}`) via `regexp`.
  - Add comprehensive unit tests in `internal/config/config_test.go`.
- **Deliverables**: `go.mod`, `go.sum`, `internal/version/version.go`, `internal/config/config.go`, `internal/config/config_test.go`.
- **Verification Gate**: `go test ./internal/config/... -v` passes 100% (valid configs, missing files, env var expansion, validation errors).
- **Git Commit Target**: `feat(config): implement configuration engine with env var substitution`

---

### Milestone 2: Built-in Native Tools Engine
- **Status**: Completed
- **Objectives**:
  - Implement native Linux tools without external server or MCP dependencies, unconfined (no path jail — see `req/requirements_doc.md` NFR-3.4).
  - `internal/tools/bash.go`: Command execution via `exec.CommandContext` with timeouts, stream capture, empty stdin, and pager/prompt-neutralizing env overrides (NFR-3.5).
  - `internal/tools/fileops.go`: Line-offset `read_file`, auto-mkdir `write_file`, and exact-matching `edit_file`.
  - `internal/tools/search.go`: `glob_search` (via `doublestar`, supporting `**`) and regex `grep_search`.
  - `internal/tools/registry.go`: Unified `Registry` implementing the shared `Tool` interface and OpenAI schema aggregation.
  - Add unit tests in `internal/tools/*_test.go`.
- **Deliverables**: `internal/tools/registry.go` (+ `registry_test.go`), `bash.go` (+ `bash_test.go`), `fileops.go` (+ `fileops_test.go`), `search.go` (+ `search_test.go`).
- **Verification Gate**: `go test ./internal/tools/... -v` passes (execution timeouts, file read/write/edit, search filters).
- **Git Commit Target**: `feat(tools): implement native bash, fileops, and search tools`

---

### Milestone 3: Context Management Engine
- **Status**: Completed
- **Objectives**:
  - Implement `internal/ctxmgr/context.go` controlled by `ManageContext bool`.
  - Implement `Manager.TruncateToolOutput` with temporary file spooling to `.tmp/tool_outputs/` inside the working directory.
  - Implement sliding-window pruning preserving the system prompt (index 0) and first user turn (index 1).
  - Implement conversation summarization, gated on both window overflow and the token budget actually being exceeded (see `req/spec_doc.md` 2.5 — this gating was corrected during Milestone 8).
  - Add unit tests in `internal/ctxmgr/context_test.go`.
- **Deliverables**: `internal/ctxmgr/context.go`, `internal/ctxmgr/context_test.go`.
- **Verification Gate**: `go test ./internal/ctxmgr/... -v` — oversized outputs are truncated and spooled; sliding window never prunes system or first-turn anchors; summarization is correctly gated on the token budget.
- **Git Commit Target**: `feat(ctxmgr): implement context management with output truncation and sliding window`

---

### Milestone 4: Skills Loader & Dual Logging Engine
- **Status**: Completed
- **Objectives**:
  - Implement `internal/skills/skills.go` to recursively discover Markdown files and format into `<available_skills>` XML.
  - Implement `internal/logging/logging.go`: structured, append-only `trace_<timestamp>.jsonl`; formatted, human-readable `session_<timestamp>.log`; API key redaction in both.
  - Add unit tests in `internal/skills/skills_test.go` and `internal/logging/logging_test.go`.
- **Deliverables**: `internal/skills/skills.go` (+ test), `internal/logging/logging.go` (+ test).
- **Verification Gate**: `go test ./internal/skills/... ./internal/logging/... -v` — skill discovery and generated JSONL/transcript logs validated.
- **Git Commit Target**: `feat(logging,skills): add dual-format logger and markdown skills loader`

---

### Milestone 5: LLM Client & Core Agentic Loop
- **Status**: Completed
- **Objectives**:
  - Implement `internal/llm/client.go`: `net/http`-based OpenAI-compatible client with retry logic, exponential backoff, and token usage tracking.
  - Implement `internal/agent/agent.go`: Agentic loop that coordinates context management, LLM calls, tool execution, and termination detection.
  - Implement the repeated tool-call loop guard: detect `(tool, args, result)` triples recurring more than `max_repeated_tool_calls` times consecutively and inject a `[LOOP DETECTED]` corrective note + `loop_guard_triggered` trace event.
  - Handle `max_steps` cutoff and graceful signal termination (`SIGINT`/`SIGTERM`) via `signal.NotifyContext`.
  - Add tests in `internal/agent/agent_test.go` using an `httptest.Server` to mock LLM responses.
- **Deliverables**: `internal/llm/client.go` (+ test), `internal/agent/agent.go` (+ test).
- **Verification Gate**: `go test ./internal/agent/... -v` — multi-step tool calls, context updates, logged interactions, clean stop conditions.
- **Git Commit Target**: `feat(agent): implement core agentic loop with tool dispatch and state machine`

---

### Milestone 6: CLI Integration, Examples, Roles & Prompts
- **Status**: Completed
- **Objectives**:
  - Implement `internal/cliapp/cli.go` integrating all components with `-s`, `-p`, `-c`, and `-k` flags, plus a thin `cmd/litegent/main.go` wrapper.
  - Populate production examples under `examples/`: `config.yaml`/`config.json`, 4 role system prompts, 4 paired task prompts, 2 sample skills.
  - Add CLI integration tests in `internal/cliapp/cli_test.go`.
- **Deliverables**: `cmd/litegent/main.go`, `internal/cliapp/cli.go` (+ test), `examples/` hierarchy.
- **Verification Gate**: `go test ./internal/cliapp/... -v`; `go run ./cmd/litegent --help` shows the correct interface.
- **Git Commit Target**: `feat(cli): complete CLI entry point and populate production examples`

---

### Milestone 7: Full Verification & Release Gate
- **Status**: Completed
- **Objectives**:
  - Execute the full end-to-end test suite across all packages.
  - Add `install.sh`: gate on `go vet`/`go test`, then build a single statically-linked (`CGO_ENABLED=0`) release binary.
  - Perform a live execution test of the actual built binary (not just `go test`) against a throwaway local mock OpenAI-compatible server.
  - Inspect generated dual logs (`trace_*.jsonl` and `session_*.log`) to ensure format compliance.
- **Deliverables**: `install.sh`; a verified, deployable repository.
- **Verification Gate**: `go build ./...`, `go vet ./...`, `gofmt -l .` all clean; `go test ./... -v` all green; live binary run produces a well-formed dual log and exits 0.
- **Git Commit Target**: `chore: add install.sh and complete end-to-end verification`

---

### Milestone 8: Simplification Pass
- **Status**: Completed
- **Objectives**:
  - Fix a real correctness gap found by audit: `ContextConfig.MaxContextTokens` was defined/defaulted/validated but never consumed by `ctxmgr.Manager.Prepare` — summarization fired on every window overflow regardless of token pressure. Gated it on the actual token estimate (Section 2.5 of `req/spec_doc.md`).
  - Remove the unused `llm.Message.Name` field.
  - De-duplicate `internal/tools`: add `functionSchema`/`requireStringArg`/`requireAnyStringArg`/`orDefault` helpers in `registry.go`, collapsing repeated JSON-schema and argument-validation boilerplate across all 6 tools.
  - Remove two `os/exec` field assignments in `bash.go` that only duplicated `exec.CommandContext`'s own defaults.
  - Inline `internal/config/yaml.go`'s one-function wrapper into `config.go`; delete the file.
  - Make `logging.formatCall`'s argument ordering deterministic (sort keys before formatting).
  - Fix `grep_search` opening every candidate file twice (binary-sniff + content scan) — now a single `os.Open` with `bufio.Reader.Peek`.
  - Replace `github.com/spf13/pflag` with the standard library `flag` package in `internal/cliapp` (each short/long flag pair registers the same backing field under two names); run `go mod tidy` to drop the dependency and correctly mark the remaining two as direct.
- **Deliverables**: Modified `internal/ctxmgr/context.go`, `internal/llm/client.go`, `internal/tools/*.go`, `internal/config/config.go` (merged `yaml.go`), `internal/logging/logging.go`, `internal/cliapp/cli.go`, `go.mod`/`go.sum`.
- **Verification Gate**: `go build ./...`, `go vet ./...`, `gofmt -l .` clean; `go test ./... -v` green (including a new `TestPrepare_DoesNotSummarizeUnderTokenBudget`); `./install.sh` full gate + static binary build; live end-to-end smoke test against a mock LLM server confirming the tool-call round trip and deterministic log formatting.
- **Git Commit Target**: `refactor: simplify tool/config/log code, fix dead config field, drop pflag`

---

### Milestone 9: Interactive Sessions, Live Progress & Dev Tooling
- **Status**: Completed
- **Objectives**:
  - Add `-i/--interactive`: a persistent, multi-turn session mode. `internal/agent.Agent` gains `RunInteractive`, sharing a new `runTurn` helper with the existing single-shot `Run`. Conversation history persists across turns (subject to the same `manage_context` pipeline); each turn gets its own independent `max_steps` budget; the repeated-tool-call loop guard resets every turn.
  - Fix a bug surfaced by this work: `runTurn` must append the assistant's final answer to the message history before returning (previously only tool-calling turns were persisted into history) — required for a later interactive turn to see an earlier turn's answer.
  - `internal/cliapp.runInteractive`: reads stdin line-by-line (prompting with `> ` on stderr), treats `/exit`/`/quit` (case-insensitive) or EOF as session end, and runs the blocking stdin read in a goroutine selected against the cancellable root context so `SIGINT`/`SIGTERM` still works while waiting on input.
  - Add `-v/--verbose` live progress reporting: new `internal/progress` package, a nil-receiver-safe `*Reporter` streaming concise, secret-masked stderr lines (startup, per-turn/per-step timing, tool-call args/results, turn/session completion) as the loop runs — distinct from the file-based dual audit log.
  - Add `run.sh`: `go build` into `./bin/litegent` then `exec` it in place, forwarding all arguments — a dev-convenience wrapper distinct from `install.sh`'s full vet/test gate + static release binary.
  - `cliapp.Run`'s signature gains an `io.Reader` parameter for stdin (needed for interactive mode); all call sites (`main.go`, tests) updated.
- **Deliverables**: `internal/progress/progress.go` (+ test), modified `internal/agent/agent.go` (+ new tests), modified `internal/cliapp/cli.go` (+ new tests), modified `cmd/litegent/main.go`, `run.sh`.
- **Verification Gate**: `go test ./... -v` green (new `RunInteractive`/progress/interactive-CLI tests, including multi-turn history persistence, early-stop on fatal error, clean EOF handling, and loop-guard-reset-per-turn); `go vet ./...`; `gofmt -l .` clean; `./install.sh` full gate + static binary; a live end-to-end interactive-mode run of the built binary against a mock LLM server confirming persisted history (growing message count across turns), live progress streaming, and a clean `/exit`.
- **Git Commit Target**: `feat(agent,cli): add interactive sessions, live progress, and run.sh`

---

## 3. Mandatory Git Commit Metadata Specification

Every commit made during implementation **must** include the following trailers, in addition to any attribution lines a specific coding harness requires:
```text
<type>(<scope>): <subject line>

<Body explanation>

Coding-Agent: <identifier of the coding agent, e.g. opencode or claude-code>
LLM: <exact model ID powering the agent, e.g. google/gemini-3.8-flash or claude-sonnet-5-5>
Host: <machine hostname>
Directory: /home/rh-claude/src/litegent_son_py
```
