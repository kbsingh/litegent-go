# Litegent Sessions, Durable Context, and Complete Logging — Milestone Plan

**Status:** Proposed  
**Created:** 2026-11-13  
**Branch:** `sessions`  
**Baseline:** `45ac9a4` (`sessions` == `main` at plan creation)  
**Scope:** Session identity and resume, per-session storage, durable context persistence, complete human/trace logging, and crash-safe shutdown behavior.

This document is the serial execution plan. No implementation is included here. Execute the milestones in order; each milestone must leave the repository buildable, tested, and reviewable on its own.

---

## 1. Target State

### 1.1 Configuration

```json
{
  "top_dir": "~/.litegent",
  "log_dir": "logs",
  "skills_dir": "skills",
  "manage_context": true,
  "context_config": {
    "max_context_tokens": 32000,
    "truncate_tool_outputs": true,
    "max_tool_output_chars": 4000,
    "window_size": 40,
    "spool_dir": "spool"
  }
}
```

Rules:

- `top_dir` defaults to the **directory containing the loaded config file**. For the default bootstrap config, that is `~/.litegent`.
- If `-c` is omitted, the existing behavior is preserved: Litegent creates `~/.litegent/`, writes `~/.litegent/config.json`, and creates/reads `~/.litegent/system.md`.
- If `-c` is supplied and the config omits `top_dir`, `top_dir` is that config file's directory.
- `log_dir` and `spool_dir` are **relative to the session directory** and must not be absolute or escape it.
- `skills_dir` is relative to `top_dir` (absolute values are allowed).
- `manage_context` remains the master switch for pruning/summarization/truncation.
- `truncate_tool_outputs` independently enables or disables tool-output truncation when `manage_context` is true.
- `max_tool_output_chars` is required only when truncation is enabled.
- `window_size` is measured in **messages**, not turns.
- Existing config files are never overwritten.

### 1.2 Session directory layout

```text
~/.litegent/
├── config.json
├── system.md
├── skills/
└── <session-id>/
    ├── session.json
    ├── context.json
    ├── .lock
    ├── logs/
    │   ├── session_<run-id>.log
    │   └── trace_<run-id>.jsonl
    └── spool/
        └── output_<callID>.txt
```

### 1.3 CLI

```text
litegent [-s <file>] -p <file> [-c <file>] [-k <dir>] [-v] [-r <session-id>]
litegent [-s <file>] -i [-c <file>] [-k <dir>] [-v] [-r <session-id> | --resume-last]
```

- No resume flag → always create a new session.
- `-r, --resume <session-id>` → reuse an existing session or hard-fail.
- `--resume-last` → reuse the most recently used valid session or hard-fail.
- `-r` and `--resume-last` are mutually exclusive.
- Resume never falls back to creating a new session.

### 1.4 Session identity

- 3 or 4 lowercase English words joined by hyphens.
- Examples: `brave-otter-runs`, `calm-river-sings`, `swift-otter-runs-quietly`.
- Regex: `^[a-z]+(?:-[a-z]+){2,3}$`, maximum 64 characters.
- Unique within `top_dir`; atomic `mkdir` is the uniqueness test.
- Reserved top-level names are never used: `config.json`, `system.md`, `skills`, `logs`, `spool`, `tmp`, `sessions`.
- Session ID is a human-readable label and resume key, not a secret or authentication token.

### 1.5 Durable context

- One authoritative `context.json` per session.
- Contains the effective message list, including pruning and `<context_summary>`.
- Updated atomically at every valid change, including compaction.
- Loaded on resume and passed directly to the agent.
- Never partially written; invalid on load means hard failure.

### 1.6 Logging

- Human log: complete, readable, parseable chronological flow.
- Trace log: machine-readable parity with the human log.
- Both are written at event time, flushed at meaningful points, and synced/closed on shutdown.
- Tool outputs are never reduced to a 500-character summary in the human log.

---

## 2. Execution Rules

1. Work strictly in the order below. Do not begin a milestone until the previous milestone's Definition of Done is satisfied.
2. Each milestone is a separate commit (or a small, coherent commit series) on `sessions`.
3. Do not push, force-push, merge, or delete branches unless explicitly authorized.
4. Every milestone must pass `gofmt`, `go test ./...`, and `go vet ./...` before it is considered complete.
5. Tests must use temporary directories. They must never write to the real `~/.litegent`, the repository root, or the user's current working directory.
6. User-visible help text is updated in the milestone that introduces the feature. Full documentation is completed in Milestone 7.
7. If a design change is required, update this document first, then implement.

---

## 3. Milestone Summary

| # | Milestone | Depends on | Primary outcome |
|---|---|---|---|
| 1 | Configuration foundation | — | New schema, defaults, validation, truncation toggle |
| 2 | Session identity and storage | 1 | Every run creates a session and stores artifacts inside it |
| 3 | Logging completeness | 2 | Human/trace logs capture the exact flow |
| 4 | Durable context blob | 2, 3 | `context.json` is always valid and updated on every change |
| 5 | Resume | 2, 4 | `-r` and `--resume-last` restore and continue sessions |
| 6 | Durability and failure hardening | 3, 4, 5 | Ctrl+C, timeout, panic, crash, and write failures preserve content |
| 7 | Documentation and migration | 1–6 | Docs, examples, and migration notes match the implementation |
| 8 | Final verification | 1–7 | Full automated and manual acceptance pass |

### Delivery Status

| Milestone | Status | Commit |
|---|---|---|
| 1. Configuration foundation | Complete | `056572a` |
| 2. Session identity and storage | Complete | `ac95d5c` |
| 3. Logging completeness | Complete | `6576066` |
| 4. Durable context blob | Complete | `251aa4b` |
| 5. Resume | Complete | `582d500` |
| 6. Durability and failure hardening | Complete | `e5a9527` |
| 7. Documentation and migration | Complete | `28e1eb8` |
| 8. Final verification | Complete | verified at `28e1eb8` |

---

## 4. Cross-Cutting Acceptance Criteria

These apply to the completed program and are verified again in Milestone 8.

- [ ] New runs create a unique 3- or 4-word session ID.
- [ ] Session artifacts live under `top_dir/<session-id>/`.
- [ ] `-r <id>` resumes the named session; `--resume-last` resumes the newest.
- [ ] Missing, invalid, symlinked, or corrupt sessions hard-fail without creating anything.
- [ ] Resumed runs load `context.json` and continue the prior conversation.
- [ ] Context is updated after user input, compaction, complete tool steps, final answers, errors, and interrupts.
- [ ] Compaction updates `context.json` before the next LLM request.
- [ ] The human log captures all system/user/assistant/tool inputs and outputs.
- [ ] The trace log carries equivalent machine-readable data.
- [ ] Ctrl+C, SIGTERM, LLM timeout, tool timeout, panic, and disk-full failures do not silently discard content.
- [ ] Killing the process leaves the logs written so far and the last valid context blob.
- [ ] An interrupted previous run is detectable on resume.
- [ ] Config defaults and validation match Section 1.1.
- [ ] Documentation and examples match the implementation.
- [ ] `go test ./...`, `go vet ./...`, and `go test -race ./...` pass.

---

## 5. Milestone 1 — Configuration Foundation

### Goal

Introduce the configuration schema, defaults, path rules, and truncation toggle that all later milestones depend on. Do not add sessions yet.

### Work

- Add `top_dir`:
  - Default to the directory containing the loaded config file.
  - With no `-c`, the config file is `~/.litegent/config.json`, so the default remains `~/.litegent` exactly as today.
  - Expand `~`.
  - Make absolute; relative values resolve from the invocation directory.
  - Reject an explicitly empty value.
- Bootstrap exception:
  - The only fixed `~/.litegent` production reference is the default config path used when `-c` is omitted.
  - `config.json` and `system.md` remain co-located in that directory.
- Reorder CLI startup:
  - Load and validate the config first.
  - Resolve `top_dir` from that config.
  - Only then resolve/create `<top_dir>/system.md`.
  - This is required because `top_dir` lives inside the config.
- Add `skills_dir`:
  - Default `skills`, resolved relative to `top_dir`.
  - Absolute paths allowed.
  - Missing default directory is treated as “no skills”; an explicit `-k` path remains strict.
- Add `context_config.truncate_tool_outputs`:
  - Default `true`.
  - Truncation runs only when `manage_context && truncate_tool_outputs`.
  - `max_tool_output_chars` is validated only when truncation is effectively enabled.
- Change defaults:
  - `max_context_tokens`: `16000` → `32000`.
  - `window_size`: `20` → `40`.
  - `log_dir`: `logs` (session-relative from Milestone 2).
  - `spool_dir`: `spool` (session-relative from Milestone 2).
- Add a safe relative-path resolver:
  - Reject absolute paths, `..` components, and empty values where required.
  - Reuse it for session-relative log/spool paths in later milestones.
- Until sessions exist, resolve runtime log/spool paths under `top_dir` so the application remains coherent. Milestone 2 replaces the base with the session directory.
- Wire `truncate_tool_outputs` into the agent’s tool-dispatch path.
- Update `Default()` and `DefaultJSON()` so generated configs contain the new schema.
- Add config and agent tests.
- Migrate existing CLI tests:
  - Replace absolute `log_dir`/`spool_dir` values with a temporary `top_dir` and relative paths.
  - Add a helper that locates run log files under the test `top_dir`.
  - Add a test proving a custom `top_dir` does not create or read `$HOME/.litegent/system.md`.
  - Add a test proving no `-c` still creates both defaults under `$HOME/.litegent`.

### Deliverables

- `internal/config/config.go` and tests.
- `internal/agent/agent.go` truncation gate and tests.
- `internal/cliapp/cli.go` temporary path-base wiring.
- Updated help text where defaults are mentioned.

### Definition of Done

- [ ] `top_dir`, `skills_dir`, and `truncate_tool_outputs` load with documented defaults.
- [ ] `max_context_tokens` defaults to `32000`; `window_size` defaults to `40`.
- [ ] Absolute or escaping `log_dir`/`spool_dir` values are rejected with clear errors.
- [ ] Truncation can be disabled while pruning/summarization remain active.
- [ ] `max_tool_output_chars: 0` is accepted when truncation is disabled and rejected when enabled.
- [ ] Generated default config loads successfully.
- [ ] Existing configs without the new keys load with defaults.
- [ ] Tests do not create `logs/`, `spool/`, or session directories in the repo.
- [ ] `gofmt`, `go test ./...`, and `go vet ./...` pass.

### Verification

```bash
gofmt -l cmd internal
go test ./...
go vet ./...
```

---

## 6. Milestone 2 — Session Identity and Storage

### Goal

Every non-help/non-version run creates a unique session, and logs, traces, spool files, and the manifest live inside it. Resume is not exposed yet.

### Work

- Add `internal/session`:
  - Curated adjective/noun/verb/adverb word lists.
  - 3- or 4-word ID generation.
  - ID validation and reserved-name handling.
  - Atomic creation under `top_dir` with retry on collision.
  - `Info` type carrying `ID`, `Dir`, `ManifestPath`, `ContextPath`, `LogDir`, `SpoolDir`, `LockPath`.
  - `session.json` manifest schema version 1:
    - `id`, `created_at`, `last_used_at`, `run_count`, `turn_count`, `model`, `base_url`.
  - Directory permissions `0700`; manifest `0600`.
  - Run ID generation using UTC timestamp plus PID (or equivalent uniqueness).
  - Lock-file plumbing (acquisition behavior completed in Milestone 5).
- CLI:
  - Create a session only after config validation and prompt/system-prompt/skills resolution succeed.
  - Do not create a session for `--help`, `--version`, or argument errors.
  - Join `log_dir` and `spool_dir` to the session directory.
  - Wire logger and context manager to the session paths.
  - Print the session ID on startup:
    - `litegent: session <id> (<dir>)`
  - Use the session ID in the log header and `session_start` event.
  - Use run IDs in log and trace filenames.
- Skills:
  - When `-k` is omitted, use `skills_dir` from config.
  - A missing default skills directory means no skills.
  - A missing explicit `-k` directory remains a hard error.
- Tests:
  - ID format, reserved names, collision retry, uniqueness.
  - Directory/manifest creation.
  - Logs/traces/spool under the session.
  - No session on help/version/validation failure.
  - Skills fallback and explicit override.

### Deliverables

- `internal/session/session.go`
- `internal/session/id.go`
- `internal/session/words.go`
- `internal/session/session_test.go`
- CLI wiring and tests.
- Logging run-ID support.

### Definition of Done

- [ ] Every successful run creates `top_dir/<session-id>/`.
- [ ] The session ID matches the documented format and is unique.
- [ ] Collisions trigger regeneration rather than overwriting.
- [ ] Reserved top-level names are never generated.
- [ ] `session.json` exists and contains the documented fields.
- [ ] `logs/`, `spool/`, and `context.json` paths are inside the session.
- [ ] Log and trace filenames use a unique run ID.
- [ ] `-k` overrides `skills_dir`; missing default skills dir is not an error.
- [ ] No session is created for help, version, or CLI/config errors.
- [ ] `gofmt`, `go test ./...`, and `go vet ./...` pass.

### Verification

```bash
gofmt -l cmd internal
go test ./...
go vet ./...
```

---

## 7. Milestone 3 — Logging Completeness

### Goal

Make the human log and trace log capture the exact flow: all inputs, assistant output, tool calls, tool results, compaction events, and turn/run boundaries.

### Work

- Logger API:
  - Add `turn` to every event and to `TraceEvent`.
  - Add session header fields: session ID, run ID, resumed flag, model, base URL, working directory, full system prompt and injected skills.
  - Add a `user_prompt` event and human-log block.
  - Group assistant content with its tool calls.
  - Log full tool output, not a 500-character summary.
  - When truncation occurred, log:
    - full raw output
    - omitted character count
    - spool path
    - exact model-visible content
  - Log context pruning and summaries with full text.
  - Log turn start, turn end, run end, errors, and interruptions.
  - Add stable, parseable prefixes and delimiters to the human log.
  - Recursive API-key redaction for nested maps, slices, and strings.
- Context manager:
  - Change `Prepare` to return a structured result:
    - `Messages`, `Evicted`, `Summarized`, `Summary`.
  - Update tests and call sites.
- Agent:
  - Pass turn numbers to all logger calls.
  - Log user prompts, assistant content, tool calls, and tool results at the correct points.
  - Log compaction events returned by `Prepare`.
- Trace parity:
  - Add full user prompt, assistant content/tool calls, full tool output, and context events.
  - Include turn numbers on every event.
- Tests:
  - Assert human/trace contents for a scripted run.
  - Assert full output survives when not truncated and both raw/model-visible output survive when truncated.
  - Assert summaries and pruning counts appear.
  - Assert nested secrets are redacted.
  - Assert stable delimiters and prefixes.

### Deliverables

- `internal/logging/logging.go` and tests.
- `internal/ctxmgr/context.go` and tests.
- `internal/agent/agent.go` logging call sites and tests.

### Definition of Done

- [ ] The human log contains the full system prompt, every user input, every assistant response, every tool call with full arguments, and every tool result.
- [ ] Truncated results show raw output, exact model input, omitted count, and spool path.
- [ ] Compaction events show evicted counts and full summary text.
- [ ] Turn and run boundaries are explicit.
- [ ] The trace log contains equivalent machine-readable events.
- [ ] Nested tool arguments and results are redacted.
- [ ] Existing log consumers/tests are updated for the new format.
- [ ] `gofmt`, `go test ./...`, and `go vet ./...` pass.

### Verification

```bash
gofmt -l cmd internal
go test ./...
go vet ./...
```

---

## 8. Milestone 4 — Durable Context Blob

### Goal

Persist the effective conversation context in one authoritative `context.json`, update it at every safe change, and update it immediately after compaction.

### Work

- Add `internal/session/context.go`:
  - Schema version 1:
    - `version`, `session_id`, `updated_at`, `messages_sha256`, `messages`.
  - Atomic save:
    - Marshal, validate, write temp file in the session directory, `Sync()`, close, rename, `Sync()` the directory.
  - Load and validate:
    - supported version
    - matching session ID
    - non-empty messages
    - system message first
    - summary only at index 2
    - no empty roles
    - every tool message matches a preceding assistant tool call
    - no dangling tool calls
  - `HashMessages` for log correlation.
- Agent:
  - Add a `ContextStore` interface.
  - Add `RunWithHistory` and `RunInteractiveWithHistory`.
  - Track the last structurally valid message list.
  - Save context after:
    - initial system message
    - user prompt
    - `Prepare`/compaction
    - complete tool step (assistant tool calls plus all results)
    - final assistant answer
    - error, interruption, max-steps, or shutdown
  - Treat context persistence failures as fatal.
- CLI:
  - Initialize `context.json` for a new session with `[system]`.
  - Pass the session store to the agent.
  - Update `turn_count` in the manifest when a turn completes.
- Logging:
  - Add `context_sha256` to `llm_request` events and human request lines.
- Tests:
  - Save/load round-trip.
  - Compaction replaces the blob before the next request.
  - Invalid blobs are rejected for each validation rule.
  - Atomic-write failure leaves the previous file intact.
  - Context hash matches the marshaled message list.

### Deliverables

- `internal/session/context.go` and tests.
- Agent history/resume APIs and tests.
- CLI initialization and tests.

### Definition of Done

- [ ] Every new session has a valid `context.json` containing the system message before the first LLM request.
- [ ] User prompts, compaction results, complete tool steps, and final answers are persisted.
- [ ] Compaction updates the blob before the next LLM request.
- [ ] The blob contains the effective context, not a stale pre-pruning list.
- [ ] Save/load round-trips exactly.
- [ ] Every validation rule has a failing test and a clear error.
- [ ] A failed save leaves the prior blob intact and aborts the run.
- [ ] Context hashes appear in the logs.
- [ ] Resume is not yet exposed, but the load path is tested.
- [ ] `gofmt`, `go test ./...`, and `go vet ./...` pass.

### Verification

```bash
gofmt -l cmd internal
go test ./...
go vet ./...
```

---

## 9. Milestone 5 — Resume

### Goal

Expose `-r/--resume <id>` and `--resume-last`, load the persisted context, and continue the conversation without creating a new session.

### Work

- CLI flags:
  - `-r, --resume <session-id>`
  - `--resume-last`
  - Mutually exclusive; clear CLI error when both are supplied.
- Session validation:
  - ID format regex.
  - Directory exists and is not a symlink.
  - Manifest exists, parses, and `id` matches.
  - Context blob exists, parses, and `session_id` matches.
  - Lock is acquired; if held, hard-fail with “session is already in use”.
- Resume flow:
  - Load `context.json`.
  - Pass the message list to `RunWithHistory`/`RunInteractiveWithHistory`.
  - Continue turn numbering from `session.json`’s `turn_count`.
  - Single-shot: append the new `-p` prompt as the next user turn.
  - Interactive: continue prompting normally.
  - Do not re-add the system prompt or re-run compaction on load.
- System prompt handling:
  - Use the system message stored in `context.json`.
  - Compare its hash against the current `-s`/default prompt and skills.
  - Warn on drift but preserve the stored prompt.
- `--resume-last`:
  - Scan valid manifests under `top_dir`.
  - Select the greatest `last_used_at`; tie-break by ID.
  - No valid sessions → hard-fail.
- Logging:
  - Resumed run header shows `Resumed: yes`, prior run count, restored message/turn counts, and context hash.
- Failure behavior:
  - Missing, invalid, corrupt, symlinked, or locked sessions exit `1`.
  - No new session directory or log file is created on failure.
- Tests:
  - End-to-end resume with a mock LLM proves prior messages are present in the first request.
  - `--resume-last` chooses the newest manifest.
  - Every invalid-session case exits `1` and creates nothing.
  - Lock contention refuses the second opener.
  - System prompt drift warns but does not replace the stored prompt.

### Deliverables

- CLI flags, validation, and resume flow.
- Lock implementation.
- Resume integration tests.
- Help text updates.

### Definition of Done

- [ ] `-r <id>` reuses the session and does not generate a new ID.
- [ ] `--resume-last` selects the most recently used valid session.
- [ ] Loaded context is used in the first LLM request of the resumed run.
- [ ] Turn numbers continue across runs.
- [ ] Missing/invalid sessions exit `1` and create nothing.
- [ ] Symlinked session directories are rejected.
- [ ] Concurrent resume of the same session is refused by the lock.
- [ ] System prompt drift produces a warning and preserves the original prompt.
- [ ] Resumed interactive and single-shot modes both work.
- [ ] `gofmt`, `go test ./...`, and `go vet ./...` pass.

### Verification

```bash
gofmt -l cmd internal
go test ./...
go vet ./...
```

---

## 10. Milestone 6 — Durability and Failure Hardening

### Goal

Guarantee that Ctrl+C, SIGTERM, LLM/tool timeouts, panics, write failures, and hard crashes do not silently lose valuable content.

### Work

- Logger durability:
  - Keep event-time, unbuffered writes.
  - Add `Sync()` to the logger.
  - Sync after meaningful events: user input, LLM response, tool result, compaction, turn end.
  - `Close()` syncs before closing.
  - Propagate write errors to the caller and abort the run.
- Context/manifest durability:
  - Context saves fsync the file and directory.
  - Manifest updates are atomic and fsynced.
  - Save failures abort with an explicit storage error.
- Run records:
  - `session.json` records each run:
    - `run_id`, `started_at`, `finished_at`, `outcome`, `exit_code`.
  - A missing `finished_at` marks an interrupted/crashed run.
- Shutdown coordinator:
  - Normal success.
  - Max steps.
  - LLM error.
  - Interrupt.
  - Panic.
  - Every path:
    1. Save last valid context.
    2. Finalize the run record.
    3. Log the run footer.
    4. Sync and close logs/manifest.
- Signals:
  - First SIGINT/SIGTERM triggers graceful cancellation.
  - Second signal forces immediate exit after a best-effort sync.
- Panic handling:
  - Top-level recover in the CLI.
  - Log panic value and stack.
  - Save context and finalize the run.
  - Return a nonzero exit code without bypassing cleanup.
- Timeouts:
  - Verify LLM HTTP timeout, tool timeout, and summarization timeout paths all save the last valid context.
- Incomplete-run detection:
  - On resume, if the previous run has no `finished_at`, warn and continue from the last valid context.
- Fault-injection tests:
  - Failing log writer.
  - Failing context writer.
  - Canceled context.
  - Slow/timing-out LLM server.
  - Lock contention.
  - Panic path.
  - Interrupted-run manifest.

### Deliverables

- Logging sync/error propagation.
- Shutdown/finalization code.
- Manifest run records and incomplete-run detection.
- Fault-injection tests.
- Manual signal/crash test notes.

### Definition of Done

- [ ] Ctrl+C logs the interruption, saves context, finalizes the manifest, syncs/closes logs, and exits `130`.
- [ ] SIGTERM follows the same graceful path.
- [ ] A second signal forces exit without hanging.
- [ ] Panic logs the value and stack, saves context, finalizes the run, and exits nonzero.
- [ ] LLM and tool timeouts preserve the last valid context.
- [ ] SIGKILL leaves all event-time log content and the last valid context; resume warns about the incomplete run.
- [ ] Disk-full/write-failure tests prove the prior context remains valid and the run aborts loudly.
- [ ] No logger write error is ignored.
- [ ] Resume reports incomplete previous runs.
- [ ] `go test -race ./...` passes.
- [ ] Manual Ctrl+C and kill acceptance checks pass.

### Verification

```bash
gofmt -l cmd internal
go test ./...
go vet ./...
go test -race ./...
```

---

## 11. Milestone 7 — Documentation, Examples, and Migration

### Goal

Make the documentation, examples, and migration guidance describe the shipped behavior exactly.

### Work

- Update `req/spec_doc.md`:
  - `internal/session` package and APIs.
  - Session lifecycle, ID generation, directory layout, manifest schema.
  - Context blob schema, save points, validation, compaction behavior.
  - CLI flags and startup output.
  - Logging formats and trace event schema.
  - Config schema/defaults/path rules.
  - Durability and shutdown semantics.
- Update `req/requirements_doc.md`:
  - Session creation/uniqueness.
  - Resume and hard-fail behavior.
  - Per-session storage.
  - Context persistence and compaction updates.
  - Complete human/trace logging.
  - Crash/interrupt/timeout durability requirements.
- Update `req/agent_guide.md`:
  - Implementation rules for session paths and logging.
  - Definition of valid context boundaries.
  - Failure-handling rules.
- Update `req/user_guide.md`:
  - Session ID explanation and examples.
  - New/resume/resume-last usage.
  - Directory layout.
  - Log reading/parsing examples.
  - Config examples and migration notes.
- Update `README.md`:
  - Session overview.
  - New artifact layout.
- Update `examples/config.json` to the target schema.
- Update `req/deliverable_doc.md` file inventory for new files.
- Update `req/README.md` index description if needed.
- Migration notes:
  - Existing `~/.litegent/config.json` is not overwritten.
  - Existing configs keep old numeric values until edited or regenerated.
  - Absolute `log_dir`/`spool_dir` values now fail validation.
  - Old global logs/spool directories are not migrated or deleted.
  - Session directories are new; no automatic import of old logs.
  - System prompt/skills drift is warned about, not silently replaced.

### Definition of Done

- [ ] No stale defaults remain: `16000`, `window_size: 20`, `./logs`, `.tmp/tool_outputs`.
- [ ] No documentation calls `window_size` a turn count; it is messages.
- [ ] All new CLI flags and config keys are documented.
- [ ] Session directory/manifest/context examples are accurate.
- [ ] Human and trace log formats are documented with examples.
- [ ] Durability/failure semantics are documented.
- [ ] Migration notes cover existing configs and old logs/spool.
- [ ] `examples/config.json` loads successfully.
- [ ] `req/deliverable_doc.md` lists the new session files.
- [ ] Documentation links remain valid.

### Verification

```bash
grep -RIn "16000\|window_size.*20\|\.tmp/tool_outputs\|log_dir.*\./logs" req README.md examples || true
go test ./...
```

---

## 12. Milestone 8 — Final Verification and Release Readiness

### Goal

Prove the complete feature set works and no regressions remain.

### Automated verification

```bash
gofmt -l cmd internal
go test ./...
go vet ./...
go test -race ./...
./install.sh
git status --short
```

Expected:

- `gofmt -l` prints nothing.
- All tests pass.
- `go vet` is clean.
- Race tests pass.
- `install.sh` builds and tests successfully.
- `git status` shows only intended files.

### Manual acceptance matrix

| # | Scenario | Expected |
|---|---|---|
| 1 | New single-shot run | 3/4-word session ID printed; `session.json`, `context.json`, `logs/`, `spool/` exist |
| 2 | New interactive run | Same layout; each turn logged with user input and final output |
| 3 | Resume by ID | Same directory; no new ID; prior context appears in first request; new run logs created |
| 4 | `--resume-last` | Newest valid session is resumed |
| 5 | Missing session | Exit `1`; clear error; no new directory |
| 6 | Invalid/traversal ID | Exit `1`; no filesystem access outside `top_dir` |
| 7 | Ctrl+C mid-run | Interruption logged; context saved; manifest finalized; exit `130`; resume works |
| 8 | SIGKILL mid-run | Logs up to last event; last valid context; resume warns about incomplete run |
| 9 | LLM timeout | Error logged; last valid context saved; exit `3` |
| 10 | Tool timeout | Structured result logged; step persisted; run continues/ends cleanly |
| 11 | Truncation enabled | Oversized output spooled; raw + model-visible content logged |
| 12 | Truncation disabled | Full output sent and logged; no spool file for that call |
| 13 | Custom `max_tool_output_chars` | Boundary behaves as configured |
| 14 | Skills default | `top_dir/skills` loaded when present |
| 15 | `-k` override | Explicit skills path wins |
| 16 | System prompt drift on resume | Warning; stored system prompt preserved |
| 17 | Concurrent resume | Second process hard-fails on lock |
| 18 | Existing config | Loads with new-key defaults; no overwrite |
| 19 | Corrupt context blob | Resume exits `1` with a validation error |
| 20 | Compaction | Context blob updated before next request; summary/pruning logged |

### Definition of Done

- [ ] All automated checks pass.
- [ ] Every manual acceptance scenario passes.
- [ ] No session, log, spool, or context artifacts are created in the repository by tests.
- [ ] Documentation links and examples are valid.
- [ ] The final commit history on `sessions` is coherent and reviewable.
- [ ] No push has occurred without explicit authorization.
- [ ] Follow-up items are recorded in Section 15.

---

## 13. Failure and Durability Acceptance Matrix

| Failure | Logs | Context blob | Manifest | Resume behavior |
|---|---|---|---|---|
| Normal exit | Complete | Saved | Run finished | Continues normally |
| Ctrl+C / SIGTERM | Interruption recorded | Last valid saved | Run finished as interrupted | Resumes from saved context |
| Second signal | Best-effort sync | Last valid | Best-effort finalize | Resumes from last valid |
| SIGKILL / crash | Up to last event | Last valid boundary | Run unfinished | Warns; resumes from last valid |
| LLM timeout | Error recorded | Unchanged/last valid | Run finished as error | Resumes from last valid |
| Tool timeout | Full result recorded | Step saved | Run continues | Resumes with result |
| Panic | Panic + stack recorded | Last valid saved | Run finished as error | Resumes from last valid |
| Disk full | Error recorded if possible | Prior blob intact; run aborts | Best-effort | Resumes from prior blob |
| Lock contention | Error on stderr | Untouched | Untouched | Second opener exits `1` |
| Corrupt context | Error on stderr | Untouched | Untouched | Exits `1` |
| Incomplete previous run | Prior logs retained | Last valid | Unfinished marker | Warning, then resume |

---

## 14. Risks and Mitigations

| Risk | Mitigation |
|---|---|
| Pruning splits an assistant tool-call message from its results | Validate context before save; save only at complete step boundaries |
| Crash between log write and context save | Logs and context are written at meaningful points; unfinished run marker tells the user; logs retain the event |
| Tool side effects replay after an incomplete step | Document at-least-once semantics for the last incomplete step |
| Human/trace logs grow very large | Full-output logging is intentional; retention/rotation is a documented follow-up |
| Context blob cannot restore pruned messages | The blob is the current window by design; prior content remains in run logs |
| Absolute `log_dir`/`spool_dir` configs break | Validation error plus migration note; defaults updated |
| System prompt/skills change between runs | Preserve stored system message; warn on hash drift |
| Session IDs are guessable | They are labels, not secrets; `0700` permissions protect session contents |
| Concurrent resume corrupts context | `flock` on `<session>/.lock`; second opener hard-fails |
| Log/context write failure is silent | All writes are checked; failures abort loudly |
| Tests pollute real home/repo | All tests use `t.TempDir()` and explicit temp `top_dir` |

---

## 15. Out of Scope / Follow-Ups

The following are intentionally deferred and should be recorded as follow-up issues after delivery:

- `--list-sessions`, session deletion, renaming, and retention policies.
- Log rotation and size limits.
- Encryption at rest for session data.
- Remote/shared session storage.
- User-chosen session IDs.
- `--refresh-system` to replace a stored system prompt.
- Reconstructing a full machine-readable transcript from pruned history.
- Multiple simultaneous writers per session (the lock refuses them).
- Power-loss guarantees beyond the documented fsync points.
- Go 1.23 compatibility work.
- UI/TUI session browser.

---

## 16. Final Program Definition of Done

The program is complete when all of the following are true:

- [ ] Milestones 1–8 are complete and their individual definitions of done are satisfied.
- [ ] New runs create a unique, human-readable session ID and a complete session directory.
- [ ] Resume by ID and resume-last restore the exact persisted context and continue turn numbering.
- [ ] `context.json` is always valid, atomically updated, and updated immediately after compaction.
- [ ] The human log is a complete, readable, parseable flow of all inputs and outputs.
- [ ] The trace log provides equivalent machine-readable detail.
- [ ] Ctrl+C, SIGTERM, timeouts, panics, write failures, and hard crashes preserve content to the documented boundary.
- [ ] Incomplete previous runs are detected and reported on resume.
- [ ] Config defaults, validation, examples, and documentation match the implementation.
- [ ] All automated tests, race tests, vet, formatting, and the install script pass.
- [ ] The manual acceptance matrix in Milestone 8 passes.
- [ ] No unauthorized pushes, merges, or branch deletions have occurred.
