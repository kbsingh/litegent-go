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
- Go 1.22 or newer (only required to build from source; a precompiled `litegent` binary needs no Go toolchain at run time)
- Access to an OpenAI-compatible API endpoint (OpenAI, OpenRouter, DeepSeek, Groq, or local Ollama/vLLM)

### Installation
Clone or navigate to the repository and build Litegent:
```bash
cd /home/rh-claude/src/litegent_son_py
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
./run.sh -s examples/roles/software_engineer.txt -p examples/prompts/software_engineer_task.txt -c examples/config.yaml
```
Use `install.sh` instead when you want the full vet/test gate and a shippable static binary; `run.sh` is purely a local convenience wrapper.

---

## 3. CLI Command Reference

Litegent is executed from the terminal using clear file-based parameters:

```bash
litegent -s <system_prompt_file> -p <user_prompt_file> -c <config_file> [-k <skills_directory>] [-v]
```

Or, for a persistent, multi-turn session (see Section 5.4):

```bash
litegent -s <system_prompt_file> -i -c <config_file> [-k <skills_directory>] [-v]
```

### Argument Descriptions

| Flag | Full Name | Required? | Description |
|---|---|---|---|
| `-s` | `--system` | **Yes** | Path to the file containing the system prompt (agent role, principles, constraints). |
| `-p` | `--prompt` | Yes, unless `-i` is set | Path to the file containing the execution prompt (task instructions, requirements). Conflicts with `-i`. |
| `-c` | `--config` | **Yes** | Path to the configuration file in JSON (`.json`) or YAML (`.yaml`) format. |
| `-k` | `--skills` | No | Path to a directory containing skill Markdown files (`*.md`) to inject into context. |
| `-i` | `--interactive` | No | Run a persistent, multi-turn session, reading task prompts from stdin instead of `-p`. |
| `-v` | `--verbose`| No | Stream live progress to stderr as the loop runs (see Section 5.5). |
| `-h` | `--help` | No | Show help message and command syntax. |

### Output

On success, the agent's final response is printed to stdout followed by a `----====----` separator line — in single-shot and interactive mode alike. Tool-call notices (`|ToolCall : ...`) appear on stderr **only in interactive sessions** (Section 5.4); a single-shot run without `-v` stays silent on stderr apart from fatal errors. With `-v`, detailed per-step progress streams to stderr in both modes (Section 5.5).

---

## 4. Configuration Guide

Litegent accepts configurations in both **YAML** and **JSON**. Environment variables matching `${VAR_NAME}` or `${VAR_NAME:-default}` are automatically interpolated at runtime.

### Complete YAML Configuration Example (`config.yaml`)
```yaml
# LLM Endpoint & Credentials
base_url: "https://api.openai.com/v1"
api_key: "${OPENAI_API_KEY}"
model: "gpt-4o"
temperature: 0.0
max_tokens: 4096
# Thinking/reasoning control: free text forwarded verbatim to your
# endpoint. "default" (or omitted) sends nothing. Use whatever your model
# accepts, e.g. "low"/"medium"/"high" or "enabled"/"disabled".
# thinking_format picks the wire shape when a level is set:
# "reasoning_effort" (default) or "thinking" ({"thinking":{"type":...}}).
thinking_level: "default"
thinking_format: "reasoning_effort"
timeout: 120.0

# Execution Safety Limits
max_steps: 30

# Context Management Engine
manage_context: true
context_config:
  max_context_tokens: 16000
  max_tool_output_chars: 4000
  window_size: 20
  spool_dir: ".tmp/tool_outputs"

# Logging Destination
log_dir: "./logs"

# Built-in Tools Configuration
tools:
  bash:
    enabled: true
    timeout: 60.0
    default_workdir: "."
  file_ops:
    enabled: true
    max_read_lines: 2000
  search:
    enabled: true
    max_search_results: 100
```

A few notes on the thinking settings:
- An empty `thinking_level` behaves exactly like `"default"`: nothing is sent and the model decides.
- An empty `thinking_format` means the `"reasoning_effort"` default; only a *misspelled* format is rejected, and only when a level is actually set.
- Passthrough is opt-in — Litegent never probes your endpoint. If it does not support the parameter, the request fails with the endpoint's own error (exit code `3`), so remove the setting or set it back to `"default"` for such endpoints.

---

## 5. Working with Roles, Prompts & Skills

### 5.1 Role-Based System Prompts (`-s`)
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
Skills are standard Markdown files (`*.md`) located inside a directory passed via `-k`. Litegent discovers all `.md` files in the directory and automatically injects them into the agent's context under `<available_skills>`.

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
litegent -s examples/roles/software_engineer.txt -i -c examples/config.yaml
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

---

## 6. Understanding Context Management

When `manage_context: true` is configured, Litegent actively protects your model's token budget:

1. **Tool Output Truncation**:
   - If a command generates thousands of lines (e.g., `git log` or `cat /var/log/syslog`), outputs exceeding `max_tool_output_chars` are truncated.
   - The full, unmodified output is written to `.tmp/tool_outputs/<call_id>.txt` under `spool_dir` (by default, relative to the working directory — a tidy default, not an access restriction; the agent itself can still read and write anywhere on the host).
   - The agent receives the beginning and end of the output, along with a note pointing to the spooled file for surgical inspection using `read_file`.

2. **Sliding Window Pruning**:
   - Older intermediate tool exchanges are dropped when the conversation exceeds `window_size`.
   - Your system prompt (`-s`) and the first user message in the conversation (your `-p` file's content in single-shot mode, or the first turn you type in interactive mode) are **permanently anchored** and never pruned.

3. **Progressive Summarization**:
   - Summarization only happens as a *consequence* of the sliding window evicting turns above — it's not a separate trigger. When that eviction happens: if your estimated conversation tokens are still under `max_context_tokens`, the evicted turns are simply dropped (no extra LLM call). Only once the token estimate actually exceeds `max_context_tokens` are the evicted turns summarized into a `<context_summary>` block instead of dropped outright, preserving key findings and decisions without exhausting context limits.

---

## 7. Dual Logging & Auditing

Every Litegent run produces two files in your configured `log_dir` (default: `./logs`):

### 1. `session_<timestamp>.log` (Human-Readable Transcript)
A readable, chronologically ordered narrative log showing:
- Initial user prompt and system parameters.
- Step-by-step LLM thoughts and decisions.
- Tool invocations with formatted arguments.
- Tool execution summaries and exit codes.
- Final conclusions and outputs.

### 2. `trace_<timestamp>.jsonl` (Machine-Readable JSON Lines)
An append-only log capturing every raw event:
- Exact JSON request payloads sent to the LLM (including all messages and tool definitions).
- Exact JSON response payloads from the LLM (including token usage metrics).
- Tool execution latency in milliseconds, exit codes, stdout, and stderr.

**Quick log analysis with `jq`**:
```bash
# View all executed tool commands and exit codes
cat logs/trace_*.jsonl | jq 'select(.event_type=="tool_result") | {tool: .data.tool_name, exit: .data.exit_code, latency: .data.latency_ms}'

# View token consumption per step
cat logs/trace_*.jsonl | jq 'select(.event_type=="llm_response") | {step: .step, total: .data.total_tokens, prompt: .data.prompt_tokens, completion: .data.completion_tokens}'
```

> In interactive mode, `session_finish` is recorded once per **turn** that completes successfully, not once per process — expect one such event per turn in `trace_*.jsonl`, not just one at the very end.

---

## 8. Operational Recipes

### Recipe 1: Fix a Bug in a Project
```bash
litegent \
  -s examples/roles/software_engineer.txt \
  -p examples/prompts/software_engineer_task.txt \
  -c examples/config.yaml
```

### Recipe 2: Run a System Health Audit with Skills
```bash
litegent \
  -s examples/roles/sysadmin_sre.txt \
  -p examples/prompts/sysadmin_health_check.txt \
  -c examples/config.yaml \
  -k examples/skills/
```

### Recipe 3: Execute Against a Local Ollama Instance
Configure `config.yaml`:
```yaml
base_url: "http://localhost:11434/v1"
api_key: "ollama"
model: "qwen2.5-coder:32b"
manage_context: true
```
Run:
```bash
litegent -s system.txt -p prompt.txt -c config.yaml
```

### Recipe 4: Interactive Pairing Session with Live Progress
```bash
litegent \
  -s examples/roles/software_engineer.txt \
  -i \
  -c examples/config.yaml \
  -v
```
Then type one task per line (e.g. `run the tests`, then `now fix the failure you found`), watching live progress on stderr and each turn's answer on stdout. Type `/exit` when done.

---

## 9. Troubleshooting & FAQ

### Q: Why did the agent stop before finishing the task?
Check if `max_steps` was reached. In single-shot mode this exits the process with code `2`. In interactive mode (`-i`), `max_steps` applies per turn — Litegent reports the exhausted turn on stderr but keeps the session running, since conversation history is still valid for your next line. Either way, if the task is complex, increase `max_steps: 50` in your `config.yaml`.

### Q: How do I resolve `AuthenticationError`?
Ensure your `OPENAI_API_KEY` environment variable is set (`export OPENAI_API_KEY="sk-..."`) or referenced correctly in `config.yaml`.

### Q: How can I see command outputs if they were truncated?
Litegent prints the exact spool file path in the logs (e.g., `.tmp/tool_outputs/call_abc123.txt`). You can inspect this file directly with `less` or `cat`.

### Q: Why isn't Litegent sandboxed to my project directory?
By design. Litegent is meant to act like a trusted operator with full access to the box it runs on — useful for sysadmin, security-audit, and cross-repo tasks that a sandboxed tool couldn't do. If you want a narrower blast radius, run it as a dedicated low-privilege user or inside a container/VM you control, and write your system prompt (`-s`) to state the boundaries you want respected. Always review `session_<timestamp>.log` after a run.

### Q: Why did I see a `[LOOP DETECTED]` message in the output?
Litegent noticed the same tool call (same name, same arguments) returning the same result several times in a row and injected a corrective note so the model tries a different approach instead of burning through `max_steps` repeating itself. Check `trace_<timestamp>.jsonl` for the `loop_guard_triggered` event to see exactly what was repeating.
