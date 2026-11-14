# Deliverables Document: Litegent CLI AI Agent

**Project Name**: Litegent  
**Target Platform**: Linux  
**Document Version**: 2.0.0  
**Status**: Current — reflects the shipped implementation  

---

## 1. Overview of Deliverables

The Litegent project deliverables comprise the Go module source code, configuration templates, role-based system prompts, task prompts, skill documentation, test suites, and project documentation.

```
/home/rh-claude/src/litegent_son_py/
├── .gitignore
├── go.mod
├── go.sum
├── README.md
├── install.sh                            # Vet+test gate, then a static release binary
├── run.sh                                # Dev convenience: go build, then exec in place
├── req/                                  # Requirements & Design Docs
│   ├── requirements_doc.md
│   ├── spec_doc.md
│   ├── deliverable_doc.md
│   ├── agent_guide.md
│   ├── README.md
│   ├── user_guide.md
│   └── milestones_doc.md
├── cmd/
│   └── litegent/                         # Thin CLI entry point (package main)
│       └── main.go
├── internal/                             # Core Go packages (not importable outside this module)
│   ├── cliapp/
│   │   ├── cli.go
│   │   └── cli_test.go
│   ├── config/
│   │   ├── config.go
│   │   └── config_test.go
│   ├── agent/
│   │   ├── agent.go
│   │   └── agent_test.go
│   ├── ctxmgr/
│   │   ├── context.go
│   │   └── context_test.go
│   ├── skills/
│   │   ├── skills.go
│   │   └── skills_test.go
│   ├── logging/
│   │   ├── logging.go
│   │   └── logging_test.go
│   ├── llm/
│   │   ├── client.go
│   │   ├── hash.go
│   │   └── client_test.go
│   ├── session/
│   │   ├── session.go
│   │   ├── id.go
│   │   ├── lock.go
│   │   ├── context.go
│   │   ├── words.go
│   │   └── session_test.go
│   ├── progress/
│   │   ├── progress.go
│   │   └── progress_test.go
│   ├── tools/
│   │   ├── registry.go
│   │   ├── registry_test.go
│   │   ├── bash.go
│   │   ├── bash_test.go
│   │   ├── fileops.go
│   │   ├── fileops_test.go
│   │   ├── search.go
│   │   └── search_test.go
│   └── version/
│       └── version.go
├── examples/                             # Production Examples & Templates
│   ├── config.json
│   ├── roles/
│   │   ├── software_engineer.txt
│   │   ├── sysadmin_sre.txt
│   │   ├── security_auditor.txt
│   │   └── data_log_analyst.txt
│   ├── prompts/
│   │   ├── software_engineer_task.txt
│   │   ├── sysadmin_health_check.txt
│   │   ├── security_audit_task.txt
│   │   └── log_analysis_task.txt
│   └── skills/
│       ├── linux_diagnostics.md
│       └── git_workflow.md
```

Note: Go tests are colocated as `_test.go` files alongside the package they test — this is the idiomatic Go convention (`go test ./...` discovers them automatically) and replaces a separate top-level `tests/` directory.

---

## 2. Deliverable Itemization

### D-1: Core Go Module (`cmd/`, `internal/`)

| File Path | Description | Acceptance Criteria |
|---|---|---|
| `go.mod` / `go.sum` | Module definition and dependency lockfile | `module litegent`; pins `github.com/bmatcuk/doublestar/v4` (JSON config parsing and CLI flag parsing use the standard library — no dependency needed) |
| `internal/version/version.go` | Build version string and accessor | Exposes `const Version = "0.1.0"` (overridable via `-ldflags "-X litegent/internal/version.Version=..."`) |
| `cmd/litegent/main.go` | CLI entry point: calls `cliapp.Run` and `os.Exit`s with its result | Contains no business logic beyond argument hand-off and exit code translation |
| `internal/cliapp/cli.go` | Flag parsing (`-s`, `-p`, `-c`, `-k`, `-i`, `-r`, `--resume-last`, `-v`, `--version`, `-h`) via stdlib `flag`, bootstrap and `top_dir` path resolution, session create/resume orchestration, file checks, and the interactive-mode stdin loop | Uses `~/.litegent/config.json` only as the `-c`-omitted bootstrap; derives system/skills/session paths from `top_dir`; creates default files only for new sessions; hard-fails invalid resume; finalizes run records, logs, and context on every exit path; `-i` reads repeated turns from stdin until `/exit`/`/quit`/EOF |
| `internal/config/config.go`, `internal/config/paths.go` | Config structs, env interpolation, path resolution, validation, and default-file generation | Parses JSON; handles `${ENV_VAR}` / `${ENV_VAR:-default}`; resolves `top_dir` (config-directory default when omitted) and `skills_dir`; validates session-relative `log_dir`/`spool_dir`; applies documented defaults; `DefaultJSON`/`EnsureDefaultConfig` create the bootstrap config with an API-key placeholder |
| `internal/llm/client.go`, `internal/llm/hash.go` | `net/http`-based OpenAI-compatible chat completions client and message hashing | Handles tool calls, retries, exponential backoff, structured errors, and stable SHA-256 message hashing |
| `internal/ctxmgr/context.go` | Context management engine | Implements configurable tool-output truncation/spooling, whole-unit sliding window, summarization, and `PrepareResult` metadata |
| `internal/skills/skills.go` | Skill loader and prompt augmenter | Recursively scans `*.md` files, rejects reserved XML wrapper tags, escapes attributes, and injects an `<available_skills>` catalog |
| `internal/session/*.go` | Session identity, layout, locking, manifests, and context persistence | Generates unique 3/4-word IDs; creates/opens per-session directories; atomically persists validated `context.json`; records run/turn metadata; enforces one writer per session |
| `internal/logging/logging.go` | Dual logging engine | Simultaneously generates append-only JSONL event trace and parseable human transcript, including full prompts, assistant output, tool arguments/results, truncation details, compaction events, and run boundaries; recursive secret redaction; synced writes |
| `internal/progress/progress.go` | Live, human-facing progress reporter (`-v`) | Nil-receiver-safe `*Reporter`; streams startup/turn/request/tool-call/result/session lines to stderr with secrets masked; no-ops when disabled; in interactive (`-i`) sessions without `-v` it still emits one compact `\|ToolCall : ...` notice line per tool invocation (single-shot never does) |
| `internal/agent/agent.go` | Core agentic execution loop | `Run`/`RunWithHistory` and `RunInteractive`/`RunInteractiveWithHistory` share `runTurn`; orchestrates LLM queries, tool execution, context compaction and persistence, the loop guard, and stop conditions |
| `internal/tools/registry.go` | Tool registry and OpenAI schema exporter | Registers all built-in tools and aggregates their JSON schemas |
| `internal/tools/bash.go` | Linux shell execution tool | Executes commands with context-based timeout and stream capture; non-zero exit does not crash the agent |
| `internal/tools/fileops.go` | File reading, writing, and surgical editing tools | Implements `read_file`, `write_file`, and `edit_file` with precise string matching |
| `internal/tools/search.go` | File globbing and content grep tools | Implements `glob_search` (via `doublestar`) and `grep_search` with result count limits |

---

### D-2: Configuration Templates (`examples/`)

| File Path | Description | Acceptance Criteria |
|---|---|---|
| `examples/config.json` | Complete JSON configuration template | Valid JSON matching the `LitegentConfig` struct, with every configuration option documented in `req/user_guide.md` |

---

### D-3: Role System Prompts (`examples/roles/`)

| File Path | Description | Key Focus Areas |
|---|---|---|
| `examples/roles/software_engineer.txt` | Software Engineer Role | Code analysis, test-driven debugging, surgical file edits, verification |
| `examples/roles/sysadmin_sre.txt` | Linux SysAdmin & SRE Role | Host diagnostics, resource triage, systemctl/journalctl, safe operational practices |
| `examples/roles/security_auditor.txt` | Security Auditor Role | Hardcoded secret detection, file permissions, command injection risks, OWASP Top 10 |
| `examples/roles/data_log_analyst.txt` | Data & Log Analyst Role | Regex log extraction, error categorization, frequency metrics, root cause synthesis |

---

### D-4: Paired Task Prompts (`examples/prompts/`)

| File Path | Paired Role | Intended Task |
|---|---|---|
| `examples/prompts/software_engineer_task.txt` | Software Engineer | Inspect unit tests, locate failure, edit source code, and verify `go test` passes |
| `examples/prompts/sysadmin_health_check.txt` | SysAdmin / SRE | Perform CPU, memory, disk, and process health checks and report top consumers |
| `examples/prompts/security_audit_task.txt` | Security Auditor | Scan directory for exposed keys/tokens and unescaped shell invocations |
| `examples/prompts/log_analysis_task.txt` | Data / Log Analyst | Parse application logs, aggregate error rates, and output structured markdown table |

---

### D-5: Sample Skills (`examples/skills/`)

| File Path | Purpose |
|---|---|
| `examples/skills/linux_diagnostics.md` | Standard procedures for checking CPU, memory, disk I/O, network sockets, and journalctl logs |
| `examples/skills/git_workflow.md` | Rules for git status inspection, diff verification, atomic commits, and commit metadata trailers |

---

### D-6: Verification Test Suite (colocated `*_test.go` files)

| Test File | Target Package | Scope |
|---|---|---|
| `internal/cliapp/cli_test.go` | `internal/cliapp` | Argument validation, missing files, invalid flags, exit codes |
| `internal/config/config_test.go` | `internal/config` | JSON parsing, env substitution, default values, validation errors |
| `internal/tools/bash_test.go`, `fileops_test.go`, `search_test.go`, `registry_test.go` | `internal/tools` | Command execution, timeouts, file read/write/edit, glob and grep search |
| `internal/skills/skills_test.go` | `internal/skills` | Recursive directory discovery, Markdown parsing, reserved-tag rejection, attribute escaping, XML catalog injection |
| `internal/ctxmgr/context_test.go` | `internal/ctxmgr` | Truncation thresholds, spool file creation, sliding window pruning, summary merging |
| `internal/logging/logging_test.go` | `internal/logging` | JSONL event schema, human transcript formatting, API key masking |
| `internal/progress/progress_test.go` | `internal/progress` | Enabled/disabled output, nil-receiver safety, line formatting, secret masking |
| `internal/agent/agent_test.go` | `internal/agent` | Mocked LLM tool-calling loop, step count limits, dual-log file generation, multi-turn `RunInteractive` history/loop-guard-reset behavior |

---

### D-7: Project Documentation Suite (`req/` and root)

| Document | Purpose |
|---|---|
| `req/requirements_doc.md` | Functional and non-functional requirements |
| `req/spec_doc.md` | Architectural and technical specifications |
| `req/deliverable_doc.md` | Deliverables inventory and acceptance criteria |
| `req/agent_guide.md` | Operational guide for AI and human developers (including git standards) |
| `req/README.md` & `README.md` | Overview, quickstart, and feature summary |
| `req/user_guide.md` | End-user manual with operational recipes |
| `req/milestones_doc.md` | Phased implementation schedule and verification gates |
