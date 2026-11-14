# Changelog

All notable changes to Litegent are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased] - 2026-11-14

### Added

#### Persistent sessions
- Every run now creates a unique, human-readable session ID made of 3 or 4
  lowercase English words joined by hyphens, for example:
  - `brave-otter-runs`
  - `swift-otter-runs-quietly`
- Session IDs are unique within `top_dir`; collisions are resolved by atomic
  `mkdir` and regeneration.
- Per-session directory layout:
  ```text
  <top_dir>/<session-id>/
  ├── session.json
  ├── context.json
  ├── .lock
  ├── logs/
  └── spool/
  ```
- Session manifest (`session.json`) records the ID, creation/last-used times,
  run and turn counts, model/base URL, and per-run records with start, finish,
  outcome, and exit code.
- `-r, --resume <session-id>` resumes a specific session.
- `--resume-last` resumes the most recently used valid session.
- Resume restores the durable context blob and continues turn numbering.
- A missing, invalid, corrupt, symlinked, or locked session hard-fails with
  exit code `1`; there is no fallback to a new session.
- An exclusive lock (`.lock`) prevents concurrent writers for one session.
- Resumed sessions warn when the immediately preceding run did not finish.

#### Durable context
- Added a single authoritative `context.json` per session containing the
  effective, post-compaction message list.
- Context is saved atomically (temp file + fsync + rename) after:
  - user input
  - context compaction
  - each complete tool step
  - each final assistant answer
- On errors, interrupts, and shutdown the blob remains at the last valid
  saved boundary; it is never replaced with a partial tool step.
- Context validation enforces:
  - one leading system message
  - summary only at index 2
  - complete assistant tool-call/tool-result pairs
  - no orphaned tool messages or unanswered tool calls
- Context saves are hash-checked with SHA-256, and resume rejects missing,
  corrupt, or hash-mismatched blobs.
- Compaction now evicts whole units; an assistant tool-call message is never
  separated from its tool results.
- Added history-aware agent APIs:
  - `RunWithHistory`
  - `RunInteractiveWithHistory`
- Added a storage-error outcome and hard failure when context persistence
  fails.

#### Complete logging
- The human log now captures the exact flow:
  - session ID, run ID, resumed flag, model, endpoint, working directory
  - full system prompt and injected skills
  - every user input
  - every assistant response
  - every tool call with full JSON arguments
  - every tool result with the full raw output
  - the exact model-visible content, omitted count, and spool path when
    truncation or the loop guard changed the payload
  - context pruning counts and full summaries
  - turn and run boundaries, token usage, outcomes, and errors
- The trace log now emits the same flow as structured events:
  - `session_start`, `user_prompt`, `llm_request`, `llm_response`
  - `tool_call`, `tool_result`
  - `context_truncation`, `context_pruning`, `context_summary`
  - `loop_guard_triggered`
  - `turn_finish`, `session_finish`, `run_finish`, `session_error`
- Every trace event now carries `turn` in addition to `step`.
- The human log uses stable prefixes and delimiters so it can be parsed as
  well as read.
- Added per-run log filenames using a unique run ID:
  - `logs/session_<run-id>.log`
  - `logs/trace_<run-id>.jsonl`

#### Configuration
- Added `top_dir`:
  - defaults to the directory containing the loaded config file
  - for the default bootstrap config this remains `~/.litegent`
  - controls the system prompt, shared skills, sessions, logs, and spool
- Added `skills_dir` (default `skills` under `top_dir`); `-k` still overrides.
- Added `context_config.truncate_tool_outputs` (default `true`) to enable or
  disable tool-output truncation independently of pruning/summarization.
- `max_tool_output_chars` is now required only when truncation is enabled.
- Added `log_dir` and `spool_dir` as session-relative paths:
  - defaults: `logs` and `spool`
  - absolute paths and `..` escapes are rejected
- Preserved bootstrap behavior:
  - no `-c` creates/reads `~/.litegent/config.json`
  - `system.md` stays beside the config in that default case
  - `-c /custom/config.json` defaults `top_dir` to `/custom` when omitted

#### Durability and recovery
- Added coordinated run finalization on success, max steps, LLM error,
  interrupt, and panic.
- Added signal handling:
  - first SIGINT/SIGTERM cancels gracefully
  - second signal forces exit after a best-effort log sync
- Added panic recovery with stack logging and context/run finalization.
- Added `Logger.Sync`, `Logger.Err`, and synced close.
- Context and manifest writes are fsynced and atomically replaced.
- Added manifest run records with unfinished-run detection on resume.

### Changed

- Successful turn separators now include agent steps, individual tool-call
  count, prompt-to-completion duration, and provider-reported input/output
  token totals. Unsuccessful turns report their outcome and metrics on stderr
  without the success separator; max-step output shows the configured limit.
- Token totals include context-summarization calls. When a provider omits usage
  for any LLM call in a turn, displayed input/output totals are `n/a` rather
  than misleading zeros.
- `steps` is the number of normal agent-loop LLM requests attempted;
  summarization requests are included in token totals but not in `steps`.

- Raised `max_context_tokens` from `16000` to `32000`.
- Raised `window_size` from `20` to `40`.
- `window_size` is now explicitly documented and tested as a count of
  **messages**, not turns.
- Tool-output truncation is gated on both `manage_context` and
  `truncate_tool_outputs`.
- Tool results are no longer reduced to a 500-character log summary; the human
  and trace logs record the full raw output and the exact model-visible
  content.
- `log_dir` and `spool_dir` moved from working-directory-relative locations to
  per-session locations under `top_dir`.
- System prompt resolution moved after config loading so `top_dir` can govern
  `<top_dir>/system.md`.
- Resumed sessions preserve the stored system prompt and warn if the current
  prompt/skills differ.
- Session log filenames changed from timestamp-only names to per-run IDs.
- Session directory and artifact permissions tightened:
  - session directories: `0700`
  - manifest, context, log, and spool files: `0600`

### Fixed

- Fixed context pruning splitting an assistant tool-call message from its
  tool results, which could produce an invalid message sequence.
- Fixed cancellation while waiting at the interactive prompt being reported as
  a clean success instead of `interrupted` / exit code `130`.
- Fixed user prompts and system prompts not appearing in the human log.
- Fixed full tool outputs being lost from logs after the 500-character
  summary.
- Fixed API-key redaction only covering top-level strings; nested tool
  arguments are now redacted recursively.
- Fixed log filename collisions between runs by using unique run IDs.
- Fixed session directory collisions by using atomic `mkdir` and retry.
- Fixed context persistence so partial tool-call steps are never saved.

### Security

- Session directories use `0700`; manifest/context/log files use `0600`.
- Session IDs are strictly validated before filesystem use.
- Symlinked session directories are rejected on resume.
- Relative `log_dir` and `spool_dir` values are validated to stay inside the
  session directory.
- Concurrent writers for one session are refused by an exclusive lock.
- Recursive API-key redaction covers nested maps and slices.

### Documentation

- Added `req/milestones_doc.md`: serial delivery plan with per-milestone
  definitions of done and completion status.
- Updated `req/spec_doc.md`:
  - config schema/defaults
  - `top_dir` bootstrap and path rules
  - session package and layout
  - durable context blob
  - complete logging schema
  - execution/data flow
  - durability and shutdown behavior
- Updated `req/requirements_doc.md`:
  - session identity, layout, resume, and hard-failure requirements
  - durable context requirements
  - complete human/trace logging requirements
  - configurable truncation and new defaults
  - signal/panic/finalization requirements
- Updated `req/agent_guide.md`:
  - session package responsibilities
  - whole-unit pruning
  - context persistence rules
  - complete logging rules
- Updated `req/user_guide.md`:
  - session IDs and examples
  - resume/resume-last usage
  - session directory layout
  - new config fields and defaults
  - context compaction and durable context
  - complete log formats and `jq` recipes
  - migration notes
- Updated `req/deliverable_doc.md` with the new session package and file
  inventory.
- Updated `README.md` and `req/README.md` with session/resume and artifact
  layout.
- Updated `examples/config.json` to the new schema.

### Tests

- Added session unit tests:
  - ID format, reserved names, collision retry
  - directory/manifest creation
  - resume success and hard failures
  - symlink rejection
  - lock contention
  - latest-session selection
  - run finalization and incomplete-run detection
  - context save/load, validation, hash mismatch, atomic failure behavior
- Added logging tests:
  - complete session header/system prompt
  - user prompt capture
  - full tool output and truncation/model-input blocks
  - context pruning/summary events
  - recursive nested secret redaction
  - write-failure recording
- Added agent tests:
  - context saves at safe boundaries
  - storage failure aborts before the LLM call
  - history-aware runs
  - truncation disabled
  - cancellation during prompt read
- Added CLI tests:
  - new session layout and startup output
  - resume by ID and resume-last
  - missing/invalid resume hard failure
  - mutual exclusion of resume flags
  - system-prompt drift warning
  - incomplete previous-run warning
  - interactive resume history
  - session spool for truncated output
  - config-directory `top_dir` default
  - custom `top_dir` not touching `~/.litegent`
  - run-record finalization

### Migration notes

- Existing `~/.litegent/config.json` is never overwritten. New defaults apply
  to omitted keys only; edit or regenerate the file to adopt 32000/40 and the
  new paths.
- Absolute `log_dir` and `spool_dir` values now fail validation. Use a
  session-relative subdirectory such as `logs` or `spool`.
- Existing relative `log_dir`/`spool_dir` values now resolve inside each
  session directory.
- Old global log and spool directories are not migrated or deleted.
- Existing sessions are not imported; new runs create session directories.
- Session IDs are labels, not secrets. Use filesystem permissions to protect
  session data.
- Resuming with a different system prompt or skills set warns and preserves
  the stored prompt for conversation continuity.

### Build size

Release-style build command used for both measurements:

```bash
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build \
  -trimpath \
  -ldflags "-s -w -X litegent/internal/version.Version=size-test" \
  ./cmd/litegent
```

| Build | Size | Human |
|---|---:|---:|
| Baseline `45ac9a4` | 6,357,154 bytes | 6.06 MiB |
| Current sessions work | 6,422,690 bytes | 6.13 MiB |
| Delta | **+65,536 bytes** | **+1.03%** |

The current `bin/litegent` release build is 6,422,690 bytes on linux/arm64.

### Implementation commits

```text
c6702ee docs: add sessions and durable context milestone plan
056572a feat(config): add top_dir, skills_dir, and truncation toggle
ac95d5c feat(session): create per-run session directories and manifests
6576066 feat(logging): capture complete turn, tool, and context flow
251aa4b feat(context): persist a validated context blob at safe boundaries
582d500 feat(session): add resume and resume-last with context restore
e5a9527 feat(session): harden shutdown, run records, and write failures
21be979 fix(agent): treat cancelled prompt reads as interruptions
28e1eb8 docs: document sessions, durable context, and complete logging
e4efb5d docs: mark delivery milestones complete
58811dc docs: clarify spool payload versus raw tool output
838a7e0 fix(ctxmgr): restrict spool files to session-only permissions
```

### Verification

- `gofmt -l cmd internal` - clean
- `go test ./...` - pass
- `go vet ./...` - clean
- `go test -race ./...` - pass
- `./install.sh` - pass (static linux/arm64 binary built)
- Manual smoke checks:
  - new session layout/logs/context
  - resume by ID and resume-last
  - missing-resume hard failure
  - Ctrl+C graceful interruption (exit `130`)
  - SIGKILL leaves an unfinished run and last valid context; resume warns and
    continues
  - truncated tool output writes a session spool file and both raw/model-visible
    log blocks
