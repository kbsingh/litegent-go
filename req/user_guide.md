# User Guide: Operating Litegent

**Project Name**: Litegent  
**Target Environment**: Linux  
**Document Version**: 2.0.0  
**Status**: Current — reflects the shipped implementation  

---

## 1. Introduction

**Litegent** is an autonomous CLI AI agent designed for Linux systems. Unlike simple chatbots, Litegent runs a goal-directed **agentic loop**: it formulates a plan, executes host tools (such as running shell commands, reading, writing, and editing files, or searching directory trees), analyzes the output, and iterates autonomously until your objective is fulfilled.

Litegent requires **zero external servers or MCP daemons**—all tools are built-in and executed locally under your user permissions.

> **⚠ Litegent is intentionally unconfined.** It does not sandbox itself to the current directory: `bash`, `read_file`, `write_file`, `edit_file`, and the search tools can touch anything your user account can touch, anywhere on the machine. There is no path jail. The system prompt you supply via `-s` is the real behavioral boundary, and the dual logs (Section 7) are your audit trail of exactly what it did. Run it as a user whose privileges you're comfortable granting to an autonomous agent, and review the logs after each run.

---

## 2. Installation & Quick Setup

### System Prerequisites
- Linux OS (Ubuntu 22.04+, Debian 12+, Fedora 39+, RHEL 9+, Arch Linux)
- Go 1.26.8 or newer (only required to build from source; a precompiled `litegent` binary needs no Go toolchain at run time)
- Access to an OpenAI-compatible API endpoint (OpenAI, OpenRouter, DeepSeek, Groq, or local Ollama/vLLM)

### Installation
Clone or navigate to the repository and build Litegent:
```bash
cd /path/to/litegent-go
go build -o litegent ./cmd/litegent
```
Or install it onto your `$GOPATH/bin` (typically on `PATH`) in one step:
```bash
go install ./cmd/litegent
```
Or ship a release-quality static binary (runs `go vet`/`go test` first, then builds with `CGO_ENABLED=0` into `./bin/litegent`):
```bash
./install.sh
```
Verify the installation:
```bash
litegent --version
```

### Quick Dev Loop (`run.sh`)
While iterating on Litegent itself, `./run.sh` rebuilds (`go build`, which is a fast no-op when nothing changed) and runs the binary in one step, forwarding all arguments:
```bash
./run.sh -s examples/roles/software_engineer.txt -p examples/prompts/software_engineer_task.txt -c examples/config.json
```
Use `install.sh` instead when you want the full vet/test gate and a shippable static binary; `run.sh` is purely a local convenience wrapper.

---

## 3. CLI Command Reference

Litegent is executed from the terminal using clear file-based parameters:

```bash
litegent [-s <system_prompt_file>] -p <user_prompt_file> [-c <config_file>] [-k <skills_directory>] [-v] [-r <session-id> | --resume-last]
```

Or, for a persistent, multi-turn session (see Section 5.4):

```bash
litegent [-s <system_prompt_file>] -i [-c <config_file>] [-k <skills_directory>] [-v] [-r <session-id> | --resume-last]
```

Without a resume flag, every invocation creates a new session. Use `-r` or `--resume-last` to continue an existing one (Section 5.6).

### Argument Descriptions

| Flag | Full Name | Required? | Description |
|---|---|---|---|
| `-s` | `--system` | No | Path to the file containing the system prompt (agent role, principles, constraints). Defaults to `<top_dir>/system.md`; for a new session that file is created with the built-in default prompt when absent (see Section 5.1). |
| `-p` | `--prompt` | Yes, unless `-i` is set | Path to the file containing the execution prompt (task instructions, requirements). Conflicts with `-i`. |
| `-c` | `--config` | No | Path to the JSON configuration file (`.json`). When omitted, the bootstrap config is `~/.litegent/config.json`. `top_dir` in that file governs session and default-file locations. |
| `-k` | `--skills` | No | Path to a directory containing skill Markdown files (`*.md`) to inject into context. Overrides `skills_dir` from config; defaults to `<top_dir>/skills` when that directory exists. |
| `-i` | `--interactive` | No | Run a persistent, multi-turn session, reading task prompts from stdin instead of `-p`. |
| `-r` | `--resume` | No | Resume an existing session by ID. Conflicts with `--resume-last`. |
| | `--resume-last` | No | Resume the most recently used valid session. Conflicts with `-r`. |
| `-v` | `--verbose`| No | Stream live progress to stderr as the loop runs (see Section 5.5). |
| `-h` | `--help` | No | Show help message and command syntax. |

### Output

On success, the agent's final response is printed to stdout followed by a `----====----` separator line — in single-shot and interactive mode alike. At startup Litegent always prints the active session and directory to stderr, for example:

```text
litegent: session brave-otter-runs (/home/user/.litegent/brave-otter-runs)
litegent: resumed session brave-otter-runs (/home/user/.litegent/brave-otter-runs)
```

Tool-call notices (`|ToolCall : ...`) appear on stderr **only in interactive sessions** (Section 5.4); a single-shot run without `-v` otherwise stays silent on stderr apart from the session line and fatal errors. With `-v`, detailed per-step progress streams to stderr in both modes (Section 5.5).

---

## 4. Configuration Guide

Litegent accepts **JSON configuration files only**. Environment variables matching `${VAR_NAME}` or `${VAR_NAME:-default}` are automatically interpolated at runtime.

When `-c` is omitted, Litegent bootstraps from `~/.litegent/config.json`. When `-c` is supplied, that file is authoritative. `top_dir` comes from the loaded config; if omitted, it defaults to the directory containing the config file. For the default bootstrap config that is `~/.litegent`, so the historical layout is preserved:

```text
~/.litegent/
├── config.json   # bootstrap configuration (used when -c is omitted)
├── system.md     # default system prompt (created on first run if absent)
├── skills/       # optional shared skills directory
└── brave-otter-runs/
    └── ...        # per-session logs, spool, context, and manifest
```

If `-c` is omitted and `~/.litegent/config.json` does not exist, Litegent creates it (and `~/.litegent/` itself) with the documented example values, then continues. The generated file uses `"${OPENAI_API_KEY}"` as its API-key placeholder, so no real credential is written, and an existing config is never overwritten. For a new session, if `-s` is omitted and `<top_dir>/system.md` does not exist, Litegent writes the built-in default prompt there. A resumed session never creates or replaces its stored system prompt.

`log_dir` and `spool_dir` are **relative to the session directory** and must not be absolute or escape it. `skills_dir` is relative to `top_dir` (or may be absolute).

### Complete JSON Configuration Example (`config.json`)
```json
{
  "top_dir": "~/.litegent",
  "base_url": "https://api.openai.com/v1",
  "api_key": "${OPENAI_API_KEY}",
  "model": "gpt-4o",
  "temperature": 0.0,
  "max_tokens": 4096,
  "thinking_level": "default",
  "thinking_format": "reasoning_effort",
  "timeout": 120.0,
  "max_steps": 30,
  "max_repeated_tool_calls": 3,
  "manage_context": true,
  "context_config": {
    "max_context_tokens": 32000,
    "truncate_tool_outputs": true,
    "max_tool_output_chars": 4000,
    "window_size": 40,
    "spool_dir": "spool"
  },
  "log_dir": "logs",
  "skills_dir": "skills",
  "tools": {
    "bash": {
      "enabled": true,
      "timeout": 60.0,
      "default_workdir": "."
    },
    "file_ops": {
      "enabled": true,
      "max_read_lines": 2000
    },
    "search": {
      "enabled": true,
      "max_search_results": 100
    }
  }
}
```

Key fields: `top_dir` is the root for sessions, system prompt, and shared skills; `base_url` and `api_key` identify the endpoint; `model`, `temperature`, `max_tokens`, and `timeout` control requests; `max_steps` caps agentic iterations; `max_repeated_tool_calls` controls the loop guard; `manage_context` and `context_config` drive truncation, pruning, and summarization; `log_dir` and `spool_dir` are session-relative; `skills_dir` is top-dir-relative; and `tools` enables/configures the built-in tool groups. See `examples/config.json` for the same template.

Notes on context settings:
- `truncate_tool_outputs` independently enables tool-output truncation. It only takes effect when `manage_context` is true.
- `max_tool_output_chars` is required only when truncation is enabled.
- `window_size` is measured in **messages**, not turns.
- `max_context_tokens` gates summarization after a window eviction.

A few notes on the thinking settings:
- An empty `thinking_level` behaves exactly like `"default"`: nothing is sent and the model decides.
- An empty `thinking_format` means the `"reasoning_effort"` default; only a *misspelled* format is rejected, and only when a level is actually set.
- Passthrough is opt-in — Litegent never probes your endpoint. If it does not support the parameter, the request fails with the endpoint's own error (exit code `3`), so remove the setting or set it back to `"default"` for such endpoints.

---

## 5. Working with Roles, Prompts & Skills

### 5.1 Role-Based System Prompts (`-s`)
If `-s` is omitted, Litegent uses `<top_dir>/system.md`, creating it with the built-in prompt if absent on a new session:

> you are a helpful, considerate, highly experienced technical assistant. Always confirm, make backups before making changes, remove ambiguity by asking questions, never go beyond the request - stay focused. But when issues and opportunities are discovered, recommend followup actions; when possible consult docs

Litegent comes with ready-to-use role templates located in `examples/roles/`:

- **Software Engineer (`software_engineer.txt`)**: Instructs the agent to explore codebases, inspect tests, make surgical edits with `edit_file`, and run test suites.
- **SysAdmin / SRE (`sysadmin_sre.txt`)**: Instructs the agent to run read-only diagnostics (`ps`, `free`, `df`, `journalctl`), investigate resource bottlenecks safely, and verify service status.
- **Security Auditor (`security_auditor.txt`)**: Instructs the agent to scan repositories for hardcoded credentials, check file permissions, and report vulnerabilities with structured remediation steps.
- **Data / Log Analyst (`data_log_analyst.txt`)**: Instructs the agent to extract error signatures, compute occurrence frequencies, and produce markdown summary reports.

### 5.2 Task Prompts (`-p`)
Task prompts should state the desired outcome, boundaries, and expected verification clearly.

**Example Task Prompt**:
```text
Run the unit tests in internal/auth/auth_test.go.
Identify any failing assertions.
Read the implementation in internal/auth/auth.go and fix the bug using edit_file.
Re-run `go test ./internal/auth/...` to verify that all tests pass cleanly.
```

### 5.3 Skills Directory (`-k`)
Skills are standard Markdown files (`*.md`) located inside a directory passed via `-k`, or in `skills_dir` from configuration (default `<top_dir>/skills`). `-k` always wins. Litegent discovers all `.md` files in the directory and automatically injects them into the agent's context under `<available_skills>`. A missing default skills directory means “no skills”; a missing explicit `-k` path is an error.

**Example Skill File (`skills/git_workflow.md`)**:
```markdown
# Git Workflow Conventions

## Rules
1. Always run `git status` before making changes.
2. Inspect `git diff` after editing files to verify only intended lines changed.
3. Every commit message must include standard metadata trailers:
   Coding-Agent: <name>
   LLM: <model>
   Host: <hostname>
   Directory: <path>
```

### 5.4 Interactive Mode (`-i`)
Pass `-i` instead of `-p` to start a persistent, multi-turn session instead of a single task:
```bash
litegent -s examples/roles/software_engineer.txt -i -c examples/config.json
```
Litegent prints a `> ` prompt on stderr and reads one line at a time from stdin:
- Each non-blank line starts a new turn; Litegent runs it to completion and prints the final response to stdout, followed by a `----====----` separator line, before prompting again — so turn boundaries are always visually clear.
- While a turn runs, every tool invocation prints a compact one-line notice to stderr so you can see the agent working:
  ```text
  |ToolCall : bash map[command:go test ./...]
  ```
  Only the first line of the tool request is shown (long arguments are clipped), and configured secrets are masked. Under `-v` these notices are suppressed in favor of the fuller per-tool progress lines (Section 5.5).
- An empty line just re-prompts without starting a turn.
- Type `/exit` or `/quit` (case-insensitive), or send EOF (Ctrl+D), to end the session cleanly.
- `Ctrl+C` interrupts the session immediately, even while waiting at the prompt.

Conversation history persists across turns — the agent remembers what you asked and what it found in earlier turns — and is still subject to the same `manage_context` truncation, sliding-window pruning, and summarization as single-shot mode. `max_steps` applies independently to each turn: if one turn exhausts its step budget, Litegent reports that on stderr and the session continues so you can try a different approach.

### 5.5 Verbose Progress (`-v`)
Pass `-v` (works in both single-shot and interactive mode) to stream live, single-line progress updates to stderr as the loop runs — distinct from the dual log files (Section 7), which are always written regardless of `-v`:
```text
litegent: starting (interactive)
  model=gpt-4o endpoint=https://api.openai.com/v1 tools=6
> [turn 1] starting | max_steps=30 context_messages=2
[step 0] sending request | messages=2 tools=6
[step 0] response | tool_calls=1 latency=0.84s tokens=312
[step 0] tool bash | args=map[command:go test ./...]
[step 0] result bash | success=true duration=1.21s
[step 1] sending request | messages=4 tools=6
[step 1] response | final_response latency=0.52s tokens=98
[turn 1] ended | outcome=success steps=2
```
Your configured API key (and any other configured secret) is masked out of every progress line.

Tool-call visibility on stderr, by mode:

| Mode | Without `-v` | With `-v` |
|---|---|---|
| Interactive (`-i`) | Compact `\|ToolCall : ...` notices (Section 5.4) | Full progress stream — compact notices are suppressed to avoid duplication |
| Single-shot (`-p`) | Silent apart from fatal errors | Full progress stream |

### 5.6 Sessions and Resume (`-r`, `--resume-last`)

Every run without a resume flag creates a new session. A session ID is a human-readable, randomly generated 3- or 4-word name such as `brave-otter-runs` or `swift-otter-runs-quietly`. It names the session directory and is the key used to resume:

```bash
# New session
litegent -p task.md -c ~/.litegent/config.json
# litegent: session brave-otter-runs (/home/user/.litegent/brave-otter-runs)

# Resume that session by ID
litegent -i -r brave-otter-runs -c ~/.litegent/config.json

# Resume the most recently used valid session
litegent -i --resume-last -c ~/.litegent/config.json
```

Session layout:

```text
<top_dir>/<session-id>/
├── session.json      # manifest: ID, timestamps, run/turn counts, outcomes
├── context.json      # authoritative effective context blob
├── .lock             # prevents concurrent writers
├── logs/
│   ├── session_<run-id>.log
│   └── trace_<run-id>.jsonl
└── spool/
```

Behavior:

- The session ID is generated once and never reused within a `top_dir`.
- Resuming loads `context.json` and continues the prior conversation; no new session is created.
- `--resume-last` picks the valid session with the newest `last_used_at`.
- A missing, invalid, corrupt, or locked session hard-fails with exit code `1`; there is no fallback to a new session.
- If the stored system prompt or skills differ from the current ones, Litegent warns and keeps the stored prompt for continuity.
- Each run gets its own `session_<run-id>.log` and `trace_<run-id>.jsonl`.
- If a previous run did not finish, the next resume reports that warning and continues from the last valid context.

---

## 6. Understanding Context Management

When `manage_context: true` is configured, Litegent actively protects your model's token budget:

1. **Tool Output Truncation**:
   - Controlled independently by `truncate_tool_outputs` (default `true`). When enabled, outputs exceeding `max_tool_output_chars` are truncated; when disabled, full output is passed through.
   - The full model-facing tool-result payload is written to `<session-dir>/<spool_dir>/output_<call_id>.txt` (default `spool/output_<call_id>.txt` inside the session). The human log records both the raw tool output and the exact model-visible content.
   - The agent receives the beginning and end of the output, along with a note pointing to the spooled file for surgical inspection using `read_file`.
   - Both the full raw output and the exact model-visible content are recorded in the human and trace logs.

2. **Sliding Window Pruning**:
   - Older intermediate exchanges are dropped when the active conversation exceeds `window_size` **messages** (default 40). A tool-using turn typically contributes several messages.
   - Eviction works on whole units: an assistant tool-call message is always evicted with all of its tool results, never split.
   - Your system prompt (`-s`) and the first user message in the conversation (your `-p` file's content in single-shot mode, or the first turn you type in interactive mode) are **permanently anchored** and never pruned.

3. **Progressive Summarization**:
   - Summarization only happens as a *consequence* of the sliding window evicting messages — it's not a separate trigger. When that eviction happens: if your estimated conversation tokens are still under `max_context_tokens` (default 32000), the evicted messages are simply dropped (no extra LLM call). Only once the token estimate actually exceeds `max_context_tokens` are the evicted messages summarized into a `<context_summary>` block instead of dropped outright.
   - The resulting effective context is written atomically to `<session-dir>/context.json` before the next LLM request. Resuming loads that blob directly.

---

## 7. Dual Logging & Auditing

Every Litegent run produces a session directory containing two per-run log files plus durable context:

```text
<top_dir>/<session-id>/
├── context.json
├── logs/
│   ├── session_<run-id>.log
│   └── trace_<run-id>.jsonl
└── spool/
```

`log_dir` and `spool_dir` are session-relative; defaults are `logs` and `spool`.

### 1. `session_<run-id>.log` (Human-Readable Transcript)
A readable, chronologically ordered narrative log showing:
- Session ID, run ID, resumed flag, model, endpoint, working directory, and the full system prompt/skills.
- Every user input, every assistant response, and every tool call with full JSON arguments.
- Every tool result, including the full raw output; when truncation occurred, also the exact model-visible content, omitted count, and spool path.
- Context pruning counts and full summaries.
- Turn and run boundaries, token usage, outcomes, and errors.

### 2. `trace_<run-id>.jsonl` (Machine-Readable JSON Lines)
An append-only log capturing the same flow as structured events:
- `session_start`, `user_prompt`, `llm_request`, `llm_response`
- `tool_call`, `tool_result` (full output plus model-visible content when different)
- `context_truncation`, `context_pruning`, `context_summary`
- `loop_guard_triggered`, `turn_finish`, `session_finish`, `run_finish`, `session_error`

Every event carries `timestamp`, `event_type`, `turn`, `step`, and `data`.

**Quick log analysis with `jq`**:
```bash
# View all executed tool commands and exit codes
cat <session-dir>/logs/trace_*.jsonl | jq 'select(.event_type=="tool_result") | {turn, step, tool: .data.tool_name, exit: .data.exit_code, latency: .data.latency_ms}'

# View token consumption per step
cat <session-dir>/logs/trace_*.jsonl | jq 'select(.event_type=="llm_response") | {turn, step, total: .data.total_tokens, prompt: .data.prompt_tokens, completion: .data.completion_tokens}'

# Inspect context compaction
cat <session-dir>/logs/trace_*.jsonl | jq 'select(.event_type=="context_pruning" or .event_type=="context_summary")'
```

> `session_finish` records a successful final answer per turn; `turn_finish` records every turn outcome, and `run_finish` records the process outcome.

### 3. `context.json` (Durable Context)
The single authoritative context blob. It contains the effective, post-compaction message list and a SHA-256 of that list. It is replaced atomically whenever context changes, including immediately after compaction, and is loaded on resume. Invalid or corrupt blobs are rejected rather than silently reset.

---

## 8. Operational Recipes

### Recipe 1: Fix a Bug in a Project
```bash
litegent \
  -s examples/roles/software_engineer.txt \
  -p examples/prompts/software_engineer_task.txt \
  -c examples/config.json
```

### Recipe 2: Run a System Health Audit with Skills
```bash
litegent \
  -s examples/roles/sysadmin_sre.txt \
  -p examples/prompts/sysadmin_health_check.txt \
  -c examples/config.json \
  -k examples/skills/
```

### Recipe 3: Execute Against a Local Ollama Instance
Configure `config.json`:
```json
{
  "base_url": "http://localhost:11434/v1",
  "api_key": "ollama",
  "model": "qwen2.5-coder:32b",
  "manage_context": true
}
```
Run:
```bash
litegent -s system.txt -p prompt.txt -c config.json
```

### Recipe 4: Interactive Pairing Session with Live Progress
```bash
litegent \
  -s examples/roles/software_engineer.txt \
  -i \
  -c examples/config.json \
  -v
```
Then type one task per line (e.g. `run the tests`, then `now fix the failure you found`), watching live progress on stderr and each turn's answer on stdout. Type `/exit` when done.

---

## 9. Troubleshooting & FAQ

### Q: Why did the agent stop before finishing the task?
Check if `max_steps` was reached. In single-shot mode this exits the process with code `2`. In interactive mode (`-i`), `max_steps` applies per turn — Litegent reports the exhausted turn on stderr but keeps the session running, since conversation history is still valid for your next line. Either way, if the task is complex, increase `"max_steps": 50` in your `config.json`.

### Q: How do I resolve `AuthenticationError`?
Ensure your `OPENAI_API_KEY` environment variable is set (`export OPENAI_API_KEY="sk-..."`) or referenced correctly in `config.json`.

### Q: How can I see command outputs if they were truncated?
Litegent prints the exact spool file path in the logs (e.g., `~/.litegent/brave-otter-runs/spool/output_call_abc123.txt`). You can inspect this file directly with `less` or `cat`.

### Q: Why isn't Litegent sandboxed to my project directory?
By design. Litegent is meant to act like a trusted operator with full access to the box it runs on — useful for sysadmin, security-audit, and cross-repo tasks that a sandboxed tool couldn't do. If you want a narrower blast radius, run it as a dedicated low-privilege user or inside a container/VM you control, and write your system prompt (`-s`) to state the boundaries you want respected. Always review `session_<run-id>.log` after a run.

### Q: Why did I see a `[LOOP DETECTED]` message in the output?
Litegent noticed the same tool call (same name, same arguments) returning the same result several times in a row and injected a corrective note so the model tries a different approach instead of burning through `max_steps` repeating itself. Check `trace_<run-id>.jsonl` for the `loop_guard_triggered` event to see exactly what was repeating.
