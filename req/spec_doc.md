# Technical Specification Document: Litegent CLI AI Agent

**Project Name**: Litegent
**Target Platform**: Linux (Ubuntu 22.04+, Debian 12+, RHEL 9+, Fedora 39+, Arch)
**Language & Runtime**: Go 1.26.8+ (standard library `net/http`, `encoding/json`, `context`, `os/exec`, `flag`; `github.com/bmatcuk/doublestar/v4` for recursive `**` glob matching). The shipped `go.mod` declares `go 1.26.8`, and the test suite uses APIs that require a modern toolchain.
**Document Version**: 2.0.0
**Status**: Current — reflects the shipped implementation on `main`/`thinner`

---

## 1. System Architecture

```
                                  +---------------------------------------+
                                  |           Linux CLI Invocation        |
                                  | -s system.txt -p prompt.txt -c cfg    |
                                  |     [-k skills_dir] [-v]              |
                                  |  (or -i instead of -p: interactive)   |
                                  +-------------------+-------------------+
                                                      |
                                                      v
                                  +---------------------------------------+
                                  |   CLI Layer (cmd/litegent/main.go +   |
                                  |      internal/cliapp/cli.go)          |
                                  |  - Flag parsing & validation          |
                                  |  - File existence & permission checks |
                                  |  - Interactive stdin loop (if -i)     |
                                  +-------------------+-------------------+
                                                      |
                                                      v
+---------------------------------------------------------------------------------------------------+
|                           Agent Core (internal/agent/agent.go)                                     |
|                                                                                                   |
|   +-----------------------+  +-----------------------+  +-------------------+  +----------------+  |
|   | Config Engine         |  | Skills Engine         |  | Context Manager   |  | Progress       |  |
|   | (internal/config)     |  | (internal/skills)     |  | (internal/ctxmgr) |  | (internal/     |  |
|   | - Load JSON config    |  | - Recursive scan *.md |  | - Truncate+Spool  |  |  progress)     |  |
|   | - Env substitution    |  | - Ingest to System    |  | - Sliding Window  |  | - Live stderr  |  |
|   +-----------------------+  +-----------------------+  | - Summarization   |  |   progress on  |  |
|                                                          +-------------------+  |   -v, else     |  |
|                                                                                  |   no-op        |  |
|                                                                                  +----------------+  |
|   +-------------------------------------------------------------------------------------------+   |
|   |                         Agentic Loop Engine (runTurn, shared by both modes)               |   |
|   |                                                                                           |   |
|   |   1. Pre-process context (ctxmgr.Manager.Prepare)                                         |   |
|   |   2. Log + report LLM request -> Dispatch to LLM Client (internal/llm)                    |   |
|   |   3. Parse response -> Log + report LLM response                                          |   |
|   |   4. If no tool_calls -> append final message to history, emit it, turn/session ends      |   |
|   |   5. If tool_calls present:                                                               |   |
|   |        For each tool_call:                                                                |   |
|   |          Execute tool via Registry (internal/tools)                                       |   |
|   |          Apply repeated-tool-call loop guard                                              |   |
|   |          Apply truncation if output > max_tool_output_chars                               |   |
|   |          Append ToolMessage -> Log + report tool result                                   |   |
|   |   6. Repeat until max_steps (per turn) or natural completion                               |   |
|   |                                                                                           |   |
|   |   Run(): single turn, seeded with [system, user]. RunInteractive(): seeds [system], then   |   |
|   |   loops turns via a caller-supplied readPrompt/onTurnResult pair, keeping history alive.   |   |
|   +-------------------------------------------------------------------------------------------+   |
+---------------------------------------------------------------------------------------------------+
                     |                                                             |
                     v                                                             v
+------------------------------------------+                 +--------------------------------------+
|          Built-in Tool Suite             |                 |             Dual Logger              |
|            (internal/tools)              |                 |         (internal/logging)           |
|                                          |                 |                                      |
| - bash.go: Host Linux shell runner       |                 | 1. Append-only JSONL Event Traces    |
| - fileops.go: read, write, edit          |                 |    (logs/trace_<run-id>.jsonl)    |
| - search.go: glob_search, grep_search    |                 | 2. Formatted Human Narrative Log     |
| - registry.go: Tool interface + Registry |                 |    (logs/session_<run-id>.log)   |
+------------------------------------------+                 +--------------------------------------+
```

All tools are intentionally **unconfined** (Section 4): none of them restrict paths or commands to the invocation directory.

---

## 2. Module Specifications

### 2.1 CLI Module (`cmd/litegent/main.go` + `internal/cliapp/cli.go`)

Go idiom: `cmd/litegent/main.go` is a single statement — `os.Exit(cliapp.Run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))`. All argument parsing, validation, and orchestration logic lives in `internal/cliapp.Run(argv []string, stdin io.Reader, stdout, stderr io.Writer) int`, where it remains unit-testable (code in `package main` cannot be imported by tests, and the explicit `stdin`/`stdout`/`stderr` parameters let tests substitute `strings.Reader`/`bytes.Buffer` instead of the real OS streams).

#### Interface & Flags
```
litegent [-h] [--version] [-s SYSTEM] -p PROMPT [-c CONFIG] [-k SKILLS] [-v] [-r SESSION_ID | --resume-last]
litegent [-h] [--version] [-s SYSTEM] -i [-c CONFIG] [-k SKILLS] [-v] [-r SESSION_ID | --resume-last]
```
- `-s, --system PATH`: Path to system prompt text file. (Optional; defaults to `<top_dir>/system.md`. For a new session it is created with the built-in default prompt when absent — quoted in `req/user_guide.md` Section 5.1. A resumed session never creates it.)
- `-p, --prompt PATH`: Path to task prompt text file. (Mandatory unless `-i` is set; conflicts with `-i`)
- `-c, --config PATH`: Path to JSON config file. (Optional; bootstraps from `~/.litegent/config.json` when omitted)
- `-k, --skills PATH`: Path to directory of skill Markdown files. (Optional; overrides `skills_dir`, default `<top_dir>/skills`)
- `-i, --interactive`: Run a persistent, multi-turn session reading task prompts from stdin instead of `-p`. (Optional; see 2.1.1)
- `-r, --resume SESSION_ID`: Resume an existing session. Conflicts with `--resume-last`.
- `--resume-last`: Resume the most recently used valid session. Conflicts with `-r`.
- `-v, --verbose`: Stream live progress to stderr as the loop runs (see 2.1.2). The dual audit log (2.7) is always written regardless of `-v`.
- `-h, --help`: Print usage and exit 0.
- `--version`: Print `litegent version <version>` and exit 0.

Flags are parsed with the standard library `flag` package, not a third-party CLI library: `internal/cliapp.parseArgs` registers each short/long pair (`-s`/`--system`, `-p`/`--prompt`, `-c`/`--config`, `-k`/`--skills`, `-v`/`--verbose`, `-i`/`--interactive`, `-h`/`--help`) twice against the same backing struct field via `flag.FlagSet.StringVar`/`BoolVar`, because stdlib `flag` already treats `-x` and `--x` identically — no external CLI-parsing dependency is needed. `--version` is registered once (it has no short form). The flag set uses `flag.ContinueOnError` with output discarded (`fs.SetOutput(io.Discard)`); `Run` prints its own usage text on a parse error.

Validation order in `Run`: (1) parse flags, (2) handle `-h`/`--version` early-exit, (3) reject `-i` + `-p` together and `-r` + `--resume-last` together, (4) check required flags (`-p` only if `!interactive`) and report any missing flag, (5) resolve the config path (`-c`, else `~/.litegent/config.json`, creating that default file when absent) and load/validate it, (6) resolve the system prompt (`-s`, else `<top_dir>/system.md`; create it on a new session, never on resume) and read it, (7) read the task prompt file (skipped if `-i`), (8) load skills from `-k` or `skills_dir`, (9) create or resume the session, load its context on resume, and resolve log/spool paths, (10) construct the logger, tool registry, LLM client, context manager, progress reporter, and `agent.Agent`, (11) run the agentic loop (single-shot or interactive) under a cancellable context with graceful signal handling, and (12) finalize the run record, logs, and context on every exit path.

#### Bootstrap Configuration and `top_dir`
`~/.litegent/` remains the bootstrap home when `-c` is omitted:
- `config.json`: bootstrap configuration, created with documented defaults when absent.
- `system.md`: default system prompt for new sessions. When `-s` is omitted and the file is absent, it is created with the built-in default prompt; an existing file is never overwritten.

Once the config is loaded, `top_dir` governs system prompt, skills, sessions, logs, and spool:
- `top_dir` defaults to the directory containing the loaded config file. For the bootstrap config that is `~/.litegent`.
- If `-c /custom/config.json` is supplied and omits `top_dir`, the effective top directory is `/custom`.
- If `-c` is omitted and `~/.litegent/config.json` does not exist, `Run` creates the directory and file with the documented example values, prints a one-line notice to stderr, and continues.
- The generated file uses `${OPENAI_API_KEY}` as its API-key placeholder, never a real credential, and an existing file is never overwritten.

`log_dir` and `spool_dir` are session-relative and are resolved after the session is created or resumed. `skills_dir` is resolved against `top_dir`. Litegent remains unconfined for tools; these path rules are storage organization, not an access boundary.

##### 2.1.1 Interactive Mode (`-i`)

`internal/cliapp.runInteractive` drives the session:
- It wraps `stdin` in a `bufio.Reader` and prints `> ` to stderr before each read.
- Each blocking `ReadString('\n')` call runs in its own goroutine, selected against the cancellable root `context.Context`; if the context is canceled (SIGINT/SIGTERM) while waiting on input, the session ends immediately with `ExitInterrupted` even though the stdin read itself can't be forcibly unblocked (the goroutine is simply abandoned — harmless, since the process exits right after).
- A blank line (after trimming whitespace) re-prompts without starting a turn or contacting the LLM.
- A line that trims to `/exit` or `/quit` (case-insensitive) ends the session cleanly.
- Any other non-blank line becomes that turn's prompt, passed to `agent.Agent.RunInteractive` (2.8).
- After each turn, the turn's `Result` is handled by an `onTurnResult` callback: `OutcomeSuccess` prints `FinalMessage` to stdout, followed by the `----====----` turn separator, and continues; `OutcomeMaxStepsExceeded` reports to stderr and continues; `OutcomeInterrupted`, `OutcomeStorageError`, and `OutcomeLLMError` print to stderr and end the session (return `false` from the callback).
- The CLI-level exit code is `ExitInterrupted` for `OutcomeInterrupted`, `ExitLLMAPIError` for `OutcomeLLMError`, `ExitConfigOrCLIError` for `OutcomeStorageError`, and `ExitSuccess` for everything else (including a session that ended after one or more turns hit `OutcomeMaxStepsExceeded`, since the session itself still ended cleanly via `/exit`/EOF).

##### 2.1.2 Progress Reporting (`-v`, `internal/progress`)

`cliapp.Run` always constructs a `*progress.Reporter` (`progress.New(args.verbose, stderr, cfg.APIKey)`) and assigns it to `Agent.Progress` — whether or not `-v` was passed. `*progress.Reporter` is nil-receiver-safe and internally gated on `Enabled`, so every call site in `internal/agent` can call it unconditionally with no `if a.Progress != nil` guards, and it silently does nothing when `-v` is absent — with one deliberate exception: in interactive sessions the compact tool-call notice described next still prints without `-v`. See 2.8 for exactly which loop events it reports.

For interactive sessions (`-i`), `cliapp.Run` additionally sets `Reporter.ShowToolCalls = true`: each tool invocation then emits one compact stderr line of the form `|ToolCall : <first line of the tool request>` (tool name plus decoded arguments) so the user can see tools working without `-v`. The line is built by masking configured secrets first, then truncating to the first physical line, then clipping to 240 characters — that order guarantees the clip can never defeat exact-key masking. The notice is suppressed when `Enabled` is true, since the verbose `[step n] tool ...` line already reports the call in more detail, and it is never emitted in single-shot mode — `ShowToolCalls` is set only when `-i` was passed. The resulting stderr visibility:

| Mode | Without `-v` | With `-v` |
|---|---|---|
| Interactive (`-i`) | `> ` prompt + compact `\|ToolCall : ...` notices | Full progress stream (compact notice suppressed) |
| Single-shot (`-p`) | Silent apart from fatal errors | Full progress stream |

#### Exit Codes
| Code | Constant | Meaning |
|---|---|---|
| `0` | `ExitSuccess` | Agent completed task(s) successfully, or an interactive session ended cleanly via `/exit`/`/quit`/EOF |
| `1` | `ExitConfigOrCLIError` | Missing/conflicting flags, file not found, or invalid configuration |
| `2` | `ExitMaxStepsExceeded` | Single-shot mode only: reached `max_steps` without natural completion |
| `3` | `ExitLLMAPIError` | Fatal LLM communication error (e.g. repeated authentication failure) |
| `130`| `ExitInterrupted` | Process canceled by `SIGINT` (Ctrl+C) or `SIGTERM` |

```go
const (
    ExitSuccess          = 0
    ExitConfigOrCLIError = 1
    ExitMaxStepsExceeded = 2
    ExitLLMAPIError      = 3
    ExitInterrupted      = 130
)
```

---

### 2.2 Configuration Module (`internal/config/config.go`)

#### Models & Typing
Plain Go structs with `json` struct tags. `Load` parses the file with the standard library `encoding/json` package — JSON is the only supported configuration format — after environment interpolation. Validation is performed with an explicit `(LitegentConfig) Validate() error` method rather than a schema library, keeping the dependency footprint minimal.

```go
type ToolBashConfig struct {
    Enabled        bool    `json:"enabled"`
    Timeout        float64 `json:"timeout"`
    DefaultWorkdir string  `json:"default_workdir"`
}

type ToolFileOpsConfig struct {
    Enabled      bool `json:"enabled"`
    MaxReadLines int  `json:"max_read_lines"`
}

type ToolSearchConfig struct {
    Enabled          bool `json:"enabled"`
    MaxSearchResults int  `json:"max_search_results"`
}

type ToolsConfig struct {
    Bash    ToolBashConfig    `json:"bash"`
    FileOps ToolFileOpsConfig `json:"file_ops"`
    Search  ToolSearchConfig  `json:"search"`
}

type ContextConfig struct {
    MaxContextTokens    int    `json:"max_context_tokens"`
    TruncateToolOutputs bool   `json:"truncate_tool_outputs"`
    MaxToolOutputChars  int    `json:"max_tool_output_chars"`
    WindowSize          int    `json:"window_size"`
    SpoolDir            string `json:"spool_dir"`
}

type LitegentConfig struct {
    TopDir               string        `json:"top_dir"`
    BaseURL              string        `json:"base_url"`
    APIKey               string        `json:"api_key"`
    Model                string        `json:"model"`
    Temperature          float64       `json:"temperature"`
    MaxTokens            int           `json:"max_tokens"`
    ThinkingLevel        string        `json:"thinking_level"`
    ThinkingFormat       string        `json:"thinking_format"`
    Timeout              float64       `json:"timeout"`
    MaxSteps             int           `json:"max_steps"`
    MaxRepeatedToolCalls int           `json:"max_repeated_tool_calls"`
    ManageContext        bool          `json:"manage_context"`
    ContextConfig        ContextConfig `json:"context_config"`
    LogDir               string        `json:"log_dir"`
    SkillsDir            string        `json:"skills_dir"`
    Tools                ToolsConfig   `json:"tools"`
}

// Default returns the documented default configuration. Load starts from
// this value and unmarshals the user-supplied file on top of it, so
// omitted keys retain sane defaults instead of zero-valuing to false/0/"".
func Default() LitegentConfig {
    return LitegentConfig{
        TopDir:               "~/.litegent",
        BaseURL:              "https://api.openai.com/v1",
        APIKey:               os.Getenv("OPENAI_API_KEY"),
        Model:                "gpt-4o",
        Temperature:          0.0,
        MaxTokens:            4096,
        ThinkingLevel:        "default",
        ThinkingFormat:       ThinkingFormatReasoningEffort,
        Timeout:              120.0,
        MaxSteps:             30,
        MaxRepeatedToolCalls: 3,
        ManageContext:        true,
        ContextConfig: ContextConfig{
            MaxContextTokens:    32000,
            TruncateToolOutputs: true,
            MaxToolOutputChars:  4000,
            WindowSize:          40,
            SpoolDir:            "spool",
        },
        LogDir:    "logs",
        SkillsDir: "skills",
        Tools: ToolsConfig{
            Bash:    ToolBashConfig{Enabled: true, Timeout: 60.0, DefaultWorkdir: "."},
            FileOps: ToolFileOpsConfig{Enabled: true, MaxReadLines: 2000},
            Search:  ToolSearchConfig{Enabled: true, MaxSearchResults: 100},
        },
    }
}
```

`Load` resolves `top_dir` (`~` expansion, absolute; config-directory default when omitted) and `skills_dir` (absolute or relative to `top_dir`). `Validate() error` rejects an empty or non-absolute resolved `TopDir`, an empty `BaseURL` or `Model`, a non-positive `MaxSteps`, a negative `MaxRepeatedToolCalls`, an unsafe relative `LogDir`, and — only when `ManageContext` is true — a non-positive `MaxContextTokens` or `WindowSize`; when `TruncateToolOutputs` is also true, `MaxToolOutputChars` must be positive and `SpoolDir` must be a safe relative path. It also validates `ThinkingFormat` only when thinking is active. `ThinkingLevel` remains free text passed through verbatim. `MaskedAPIKey()` renders `<first 3 chars>...<last 4 chars>`, or `****` for keys of 7 characters or fewer.

#### Default File Generation
`DefaultJSON() ([]byte, error)` renders the documented defaults as indented JSON with `api_key` set to the literal placeholder `${OPENAI_API_KEY}` — never the caller's environment-resolved secret. `EnsureDefaultConfig(path) (created bool, err error)` creates the parent directory and writes the file with `O_CREATE|O_EXCL` when absent, returning `created=true` only for the process that won the creation race. Existing files are never overwritten.

In `internal/cliapp`, `ensureDefaultSystemPrompt(path, content)` applies the same create-if-absent/O_EXCL discipline to `<top_dir>/system.md`, writing the built-in prompt text. A successful first-run creation is reported with a one-line stderr notice. A resumed session never calls this function; its stored system message is authoritative.

#### Environment Variable Interpolation
Configuration files can include strings matching `${ENV_VAR}` or `${ENV_VAR:-default}`. `Load` performs regex substitution on the raw file bytes, then unmarshals the result:
```go
var envPattern = regexp.MustCompile(`\$\{([A-Za-z0-9_]+)(?::-([^}]*))?\}`)

func interpolateEnv(raw string) string {
    return envPattern.ReplaceAllStringFunc(raw, func(match string) string {
        groups := envPattern.FindStringSubmatch(match)
        name, fallback := groups[1], groups[2]
        if v, ok := os.LookupEnv(name); ok {
            return v
        }
        return fallback
    })
}
```

---

### 2.3 LLM Client Wrapper (`internal/llm/client.go`)

Litegent targets any OpenAI-compatible `/chat/completions` endpoint directly over `net/http.Client` — no vendor SDK dependency is required for a single REST endpoint shape. `internal/llm` also owns the shared `Message`/`ToolCall`/`FunctionCall` wire types used by `internal/agent`, `internal/ctxmgr`, and `internal/logging`.

```go
const (
    RoleSystem    = "system"
    RoleUser      = "user"
    RoleAssistant = "assistant"
    RoleTool      = "tool"
    RoleSummary   = "summary" // internal-only; translated to a system message before hitting the wire
)

type FunctionCall struct {
    Name      string `json:"name"`
    Arguments string `json:"arguments"` // JSON-encoded string, per the OpenAI wire format
}

type ToolCall struct {
    ID       string       `json:"id"`
    Type     string       `json:"type"`
    Function FunctionCall `json:"function"`
}

type Message struct {
    Role       string     `json:"role"`
    Content    string     `json:"content,omitempty"`
    ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
    ToolCallID string     `json:"tool_call_id,omitempty"`
}
```

`RoleSummary` is a Litegent-internal pseudo-role for the persistent `<context_summary>` message (2.5); no real OpenAI-compatible API accepts `"summary"` as a role, so `toWire(messages []Message) []Message` rewrites any `RoleSummary` message into `{Role: RoleSystem, Content: "<context_summary>\n" + m.Content + "\n</context_summary>"}` immediately before marshaling a request — the rewrite never touches the caller's own slice.

#### Request Schema
```json
{
  "model": "gpt-4o",
  "messages": [ ... ],
  "tools": [ ... ],
  "tool_choice": "auto",
  "temperature": 0.0,
  "max_tokens": 4096
}
```
`tools`/`tool_choice` are only included when the registry has at least one tool registered. When thinking is active, the request additionally carries **exactly one** of these two mutually exclusive fields — never both:
```json
"reasoning_effort": "high"
"thinking": { "type": "enabled" }
```
The two are the thinking/reasoning wire shapes, selected by `thinking_format` (`"reasoning_effort"` default | `"thinking"`), and neither is sent when `thinking_level` is omitted, empty, or `"default"`. The level value is free text forwarded verbatim (no trimming, lowercasing, or translation); `NewClient` only copies it onto the `Client` when `config.LitegentConfig.ThinkingActive()` is true, so every `ChatCompletion` call — including `Summarize` sub-calls — carries it once it is active. This is opt-in passthrough, not provider compatibility: each shape is sent exactly as configured, and an endpoint that does not support it returns its normal error (Litegent does not probe or auto-fallback).

#### Error Handling & Retries
- `ChatCompletion(ctx, messages, tools)` retries on HTTP `429`, `500`, `502`, `503`, `504` and on any network-level transport error from `http.Client.Do` (timeouts, connection refused, DNS failures, etc.) — up to `Client.MaxRetries` additional attempts (default `3`, so 4 attempts total).
- `401`/`403` responses return an `*APIError` immediately, with **no** retry.
- Other `4xx`/non-retryable responses also return an `*APIError` immediately.
- Backoff between retries: `delay = 500ms * 2^(attempt-1)`, capped at `10s`, plus up to 25% random jitter (`backoffDelay(attempt int) time.Duration`); the wait is interruptible via `ctx.Done()`.
- `Summarize(ctx, messages []Message) (string, error)` is a thin wrapper around `ChatCompletion` with a fixed system instruction ("Summarize the following agent conversation history concisely...") and the evicted messages flattened into one user message; it implements `ctxmgr.Summarizer` and is what `internal/ctxmgr` calls for Stage 3 (2.5).

---

### 2.4 Built-in Tools Engine (`internal/tools/`)

All tools implement a shared `Tool` interface and register with a `Registry`:

```go
type ToolResult struct {
    Success  bool           `json:"success"`
    Output   string         `json:"output"`
    Error    string         `json:"error,omitempty"`
    Metadata map[string]any `json:"metadata,omitempty"`
}

func (r *ToolResult) ToJSON() (string, error) { /* json.Marshal, wrapped with fmt.Errorf on failure */ }

type Tool interface {
    Name() string
    Schema() map[string]any // OpenAI function-calling JSON schema
    Execute(ctx context.Context, args map[string]any) (*ToolResult, error)
}

type Registry struct {
    tools map[string]Tool
    order []string // preserves registration order for a stable Schemas() listing
}

func NewRegistry() *Registry { return &Registry{tools: make(map[string]Tool)} }

func (r *Registry) Register(t Tool) {
    if _, exists := r.tools[t.Name()]; !exists {
        r.order = append(r.order, t.Name())
    }
    r.tools[t.Name()] = t
}

func (r *Registry) Has(name string) bool { _, ok := r.tools[name]; return ok }

// Schemas returns every registered tool's schema in registration order —
// deterministic, unlike ranging over the tools map directly.
func (r *Registry) Schemas() []map[string]any { /* iterates r.order */ }

func (r *Registry) Execute(ctx context.Context, name string, args map[string]any) (*ToolResult, error) {
    t, ok := r.tools[name]
    if !ok {
        return &ToolResult{Success: false, Error: fmt.Sprintf("unknown tool: %s", name)}, nil
    }
    return t.Execute(ctx, args)
}
```

Shared helpers in `registry.go`, used by every tool's `Schema()`/`Execute()` to avoid repeating the same boilerplate six times over:
- `functionSchema(name, description string, properties map[string]any, required []string) map[string]any` builds the `{"type":"function","function":{...}}` nesting.
- `stringArg`/`optStringArg`/`optIntArg`/`optBoolArg` extract typed arguments from the untyped `map[string]any` (JSON numbers decode as `float64`, so `optIntArg` accepts both `float64` and `int`).
- `requireStringArg(args, key) (string, *ToolResult)` returns a ready-to-return failure `ToolResult` (`"missing required argument: <key>"`) when the value is absent **or empty**.
- `requireAnyStringArg(args, key) (string, *ToolResult)` is the same but accepts an empty string as valid — used for fields where `""` is meaningful (`write_file`'s `content`, `edit_file`'s `old_string`/`new_string`).
- `orDefault(v, def int) int` returns `def` when `v <= 0`, used by the `New*Tool` constructors to clamp a zero/negative configured limit.

The JSON function-calling schemas below are the exact wire-format definitions sent to the LLM.

#### Tool Specifications

##### 1. `bash` (`internal/tools/bash.go`)
- **Schema**:
  ```json
  {
    "type": "function",
    "function": {
      "name": "bash",
      "description": "Execute a bash shell command in a subshell. Runs non-interactively (no stdin, pagers disabled) and is not confined to any directory. Returns exit code, stdout, and stderr.",
      "parameters": {
        "type": "object",
        "properties": {
          "command": { "type": "string", "description": "The command string to execute." },
          "workdir": { "type": "string", "description": "Optional working directory. May be any path on the host." },
          "timeout": { "type": "integer", "description": "Timeout in seconds (default: 60)." }
        },
        "required": ["command"]
      }
    }
  }
  ```
- **Execution Logic**: Derives a `context.WithTimeout` from the configured timeout (argument `timeout`, falling back to `BashTool.DefaultTimeout`, falling back to 60s if that's also non-positive), runs the command via `exec.CommandContext(ctx, "bash", "-c", command)` with `cmd.Dir` set to `workdir` (any path — not confined to any tree), and captures `cmd.Stdout`/`cmd.Stderr` into separate `bytes.Buffer`s. `cmd.Stdin` is left at its zero value (`nil`), which `os/exec` already reads as `/dev/null`, so a command trying to read stdin gets immediate EOF instead of hanging. `cmd.Cancel` is left unset, relying on `exec.CommandContext`'s own default (kill the process via `Process.Kill()` when the context is done) rather than reassigning it explicitly. `cmd.WaitDelay` is set to `2 * time.Second` — this one is *not* a stdlib default; it bounds how long `cmd.Run()` waits for the subprocess's I/O to drain after cancellation before giving up. `cmd.Env` is `os.Environ()` plus `GIT_PAGER=cat`, `PAGER=cat`, `GIT_TERMINAL_PROMPT=0`, `DEBIAN_FRONTEND=noninteractive`, `TERM=dumb` appended on top. On timeout, `Success: false` with an `Error` describing the timeout; otherwise `Success: exitCode == 0`, with `Output` being stdout (plus a `\n[stderr]\n...` suffix if stderr is non-empty) and `Metadata` carrying `exit_code`, `stdout`, `stderr`, `timed_out`.

##### 2. `read_file` (`internal/tools/fileops.go`)
- **Schema**:
  ```json
  {
    "type": "function",
    "function": {
      "name": "read_file",
      "description": "Read file contents with line offset and count limit. Not confined to any directory.",
      "parameters": {
        "type": "object",
        "properties": {
          "file_path": { "type": "string", "description": "Absolute or relative file path." },
          "offset": { "type": "integer", "description": "Start line (1-indexed, default 1)." },
          "limit": { "type": "integer", "description": "Max lines to return (default 2000)." }
        },
        "required": ["file_path"]
      }
    }
  }
  ```
- **Execution Logic**: Reads the file via `os.ReadFile`; if the bytes are not valid UTF-8 (`utf8.ValidString`), falls back to a byte-for-byte Latin-1 (ISO-8859-1) interpretation (each byte becomes one rune) rather than failing. Splits on `\n` and formats the `[offset-1, offset-1+limit)` slice (clamped to the actual line count) as `<line_number>: <content>\n` per line. Returns `total_lines`, `offset`, and `count` in `Metadata`.

##### 3. `write_file` (`internal/tools/fileops.go`)
- **Schema**:
  ```json
  {
    "type": "function",
    "function": {
      "name": "write_file",
      "description": "Create or overwrite a file with provided content. Automatically creates parent directories. Not confined to any directory.",
      "parameters": {
        "type": "object",
        "properties": {
          "file_path": { "type": "string", "description": "Path to destination file." },
          "content": { "type": "string", "description": "Text content to write." }
        },
        "required": ["file_path", "content"]
      }
    }
  }
  ```
- **Execution Logic**: `os.MkdirAll(filepath.Dir(path), 0o755)` (skipped if the directory component is `.` or empty), then `os.WriteFile(path, []byte(content), 0o644)`. Returns `bytes_written` and the absolute `path` in `Metadata`.

##### 4. `edit_file` (`internal/tools/fileops.go`)
- **Schema**:
  ```json
  {
    "type": "function",
    "function": {
      "name": "edit_file",
      "description": "Perform exact substring replacement in a file. Not confined to any directory.",
      "parameters": {
        "type": "object",
        "properties": {
          "file_path": { "type": "string", "description": "Path to file to edit." },
          "old_string": { "type": "string", "description": "Exact text substring to replace." },
          "new_string": { "type": "string", "description": "New replacement text." },
          "replace_all": { "type": "boolean", "description": "Replace all instances if true. Default false." }
        },
        "required": ["file_path", "old_string", "new_string"]
      }
    }
  }
  ```
- **Execution Logic**: Counts occurrences of `old_string` with `strings.Count`. Fails with `"old_string not found in file"` if the count is 0; fails with a message naming the match count if it's `> 1` and `replace_all` is false. Otherwise `strings.Replace(content, old, new, n)` with `n = -1` when `replace_all` is true, else `n = 1`, then writes the file back. Returns `replacements` in `Metadata`.

##### 5. `glob_search` (`internal/tools/search.go`)
- **Schema**:
  ```json
  {
    "type": "function",
    "function": {
      "name": "glob_search",
      "description": "Search directory trees using glob patterns (supports recursive '**'). Not confined to any directory.",
      "parameters": {
        "type": "object",
        "properties": {
          "pattern": { "type": "string", "description": "Glob pattern (e.g. '**/*.go')." },
          "path": { "type": "string", "description": "Directory to search from (default '.')." }
        },
        "required": ["pattern"]
      }
    }
  }
  ```
- **Execution Logic**: `doublestar.Glob(os.DirFS(root), pattern)` — Go's standard `path/filepath.Glob` has no `**` support, hence the dependency. Results are truncated to `MaxResults` (default 100) with `truncated: true` reported in `Metadata` alongside `match_count`.

##### 6. `grep_search` (`internal/tools/search.go`)
- **Schema**:
  ```json
  {
    "type": "function",
    "function": {
      "name": "grep_search",
      "description": "Search file contents using regular expressions. Not confined to any directory.",
      "parameters": {
        "type": "object",
        "properties": {
          "pattern": { "type": "string", "description": "Regex pattern." },
          "path": { "type": "string", "description": "Directory or file to search (default '.')." },
          "include": { "type": "string", "description": "Glob filter for filenames (e.g. '*.go')." }
        },
        "required": ["pattern"]
      }
    }
  }
  ```
- **Execution Logic**: Compiles `pattern` with `regexp.Compile` (an invalid pattern yields a failed `ToolResult`, not a Go error). Walks the tree with `filepath.WalkDir`; unreadable entries are skipped rather than aborting the whole search. If `include` is set, filters filenames via `doublestar.Match(include, d.Name())` (a bare-filename match, so the heavier `**`-aware matcher isn't strictly required here, but it's already an import for `glob_search`). For each remaining file: opens it once, wraps it in a `bufio.Reader`, `Peek`s the first 512 bytes to sniff for a NUL byte (the same heuristic `net/http.DetectContentType` uses) and skips the file if one is found, then — without closing and reopening — hands the same reader to `bufio.NewScanner` (with a 1MB max line length) to scan for matches. This is one `os.Open` per file, not two. Stops early once `MaxResults` (default 100) matches are collected. Returns matches as `path:line: text` lines plus `match_count` in `Metadata`.

---

### 2.5 Context Management Module (`internal/ctxmgr/context.go`)

Named `ctxmgr` (not `context`) to avoid shadowing the standard library's `context` package, which this module also imports for cancellation-aware summarization calls.

```go
type Summarizer interface {
    Summarize(ctx context.Context, messages []llm.Message) (string, error)
}

type Manager struct {
    cfg        config.ContextConfig
    summarizer Summarizer // may be nil: summarization is then simply skipped
}

func NewManager(cfg config.ContextConfig, summarizer Summarizer) *Manager
```

#### Pipeline Stages
```
Raw Messages
    │
    ▼
Stage 1: Tool Output Truncator (TruncateToolOutput — spools to disk if > max_tool_output_chars)
    │
    ▼
Stage 2: Sliding Window Pruner (Prepare — retains System [0], first user message [1], and the
    │     most recent whole units up to window_size messages)
    │
    ▼
Stage 3: Conversation Summarizer (Prepare — only when Stage 2 evicted something AND the token
    │     budget is actually exceeded; otherwise evicted messages are just dropped)
    ▼
PrepareResult -> agent logs pruning/summary and atomically saves context.json
```

#### Stage 1: Tool Output Truncation — `(m *Manager) TruncateToolOutput(callID, rawOutput string) (string, *TruncationInfo, error)`
```go
type TruncationInfo struct {
    SpoolFile    string
    OmittedChars int
}
```
This stage only runs when `manage_context && truncate_tool_outputs`. If `m.cfg.MaxToolOutputChars <= 0` or `len(rawOutput)` is already within it, it returns `(rawOutput, nil, nil)` unchanged — a `nil` `*TruncationInfo` means "no truncation happened," which `internal/agent` uses to decide whether to log a `context_truncation` trace event. Otherwise it writes the full model-facing payload to `<session-spool-dir>/output_<callID>.txt` (default `spool/` inside the session), then splices a 60% head + truncation notice + 30% tail. The human log separately records the raw tool output as well as this exact model-visible content.

#### Stage 2 & 3 — `(m *Manager) Prepare(ctx context.Context, messages []llm.Message) (PrepareResult, error)`
```go
type PrepareResult struct {
    Messages   []llm.Message
    Evicted    int
    Summarized bool
    Summary    string
}
```
Message layout: `messages[0]` = system prompt (permanent), `messages[1]` = the first user message in the conversation — single-shot mode's `-p` content, or interactive mode's first turn (permanent), an optional `messages[2]` = `<context_summary>` (role `llm.RoleSummary`), and everything after that is active dialogue subject to pruning.

`Prepare` is a no-op if `len(messages) <= 2`. Otherwise, when `active` exceeds `window_size` **messages** (default 40), it computes an eviction count over whole units: an assistant message with tool calls is grouped with its following tool results, and every other message is its own unit. At least the most recent unit is always retained, even if it alone exceeds the window, so a tool-call/result pair is never split. A summarization call is only made when **both** `m.summarizer != nil` and the pre-eviction token estimate exceeds `MaxContextTokens` (default 32000). When summarization runs, the newly evicted messages plus any prior summary are condensed and replace the summary at index 2. When eviction occurs under budget, the messages are dropped without a summarization call.

The agent uses `PrepareResult` to log `context_pruning` and `context_summary`, then atomically saves the resulting message list to `<session-dir>/context.json` before the next LLM request.

`EstimateTokens(messages []llm.Message) int` is a dependency-free heuristic: total character count (message `Content` plus each tool call's `Function.Name`+`Function.Arguments`) divided by 4 — not a real tokenizer, intentionally, to keep the dependency footprint minimal.

---

### 2.6 Skills Loader (`internal/skills/skills.go`)

`BuildSkillsPrompt(skillsDir string) (string, error)`:
- Returns an error if `skillsDir` doesn't exist or isn't a directory.
- `filepath.WalkDir(skillsDir, ...)`, matching files whose lowercased name ends in `.md`.
- Extracts the title from the first line matching `# <Title>` (via a line-by-line scan with `bufio.Scanner`); falls back to the filename if no such heading exists.
- Validates each file before embedding it: content containing the catalog's own wrapper tags (`<skill>`, `</skill>`, `<available_skills>`, `</available_skills>`, case-insensitive, whitespace-tolerant) is rejected with a descriptive error, so a misplaced XML fragment cannot close or reopen the catalog and corrupt subsequent skills. Ordinary angle-bracket placeholder text such as `<dir>` is allowed.
- Escapes skill titles and paths for XML attributes, so quotes or ampersands in a filename cannot terminate an attribute early.
- Assembles `<available_skills>\n  <skill name="..." file="...">\n<full file content>\n  </skill>\n...\n</available_skills>` — one `<skill>` block per discovered file, in filesystem walk order.
- Returns `("", nil)` (empty string, no error) if zero `.md` files were found, so callers can skip injection entirely.

`internal/cliapp.Run` appends the result (if non-empty) to the system prompt text with a `"\n\n"` separator before passing it to the agent.

---

### 2.7 Dual Logging Subsystem (`internal/logging/logging.go`)

Because Litegent runs unconfined with no sandbox (Section 4), this log is the system's primary safety and accountability control — every LLM and tool interaction is captured here regardless of `-v`.

#### Session Lifecycle
`logging.NewWithRunID(logDir, apiKey, runID string) (*Logger, error)`:
- Creates the session log directory with mode `0700`.
- Opens `trace_<runID>.jsonl` and `session_<runID>.log` with `O_APPEND|O_CREATE|O_WRONLY`, mode `0600`.
- Exposes `Logger.TracePath`, `Logger.SessionPath`, `Logger.RunID`.
- `apiKey`, if non-empty, is recursively redacted out of every string value in maps/slices and every human-log block. The mask preserves the key's stub prefix (`sk-...1234`, `r9_...1234`); keys of 7 characters or fewer are fully masked.
- `Sync()` flushes both files; `Close()` syncs then closes. Every write records the first error in `Logger.Err()`, which callers check to avoid silently losing audit data.

#### JSONL Trace Events (`trace_<run-id>.jsonl`)
```go
type TraceEvent struct {
    Timestamp string         `json:"timestamp"`
    EventType string         `json:"event_type"`
    Turn      int            `json:"turn"`
    Step      int            `json:"step"`
    Data      map[string]any `json:"data"`
}
```
Events: `session_start`, `user_prompt`, `llm_request`, `llm_response`, `tool_call`, `tool_result`, `context_truncation`, `context_pruning`, `context_summary`, `loop_guard_triggered`, `turn_finish`, `session_finish`, `run_finish`, `session_error`.

Notable payloads:
- `session_start`: session/run ID, resumed flag, model, base URL, working directory, full system prompt.
- `user_prompt`: full user input.
- `llm_request`: message count, token estimate, context SHA-256.
- `llm_response`: assistant content, tool calls, token usage.
- `tool_call`: full decoded arguments.
- `tool_result`: full raw output, success/exit/latency, plus `model_content` and spool metadata when truncation or the loop guard changed what the model saw.

#### Human Transcript (`session_<run-id>.log`)
Plain text, written under the same mutex as the JSONL file. Stable prefixes and delimiters make the flow parseable:

```text
================================================================================
Litegent Session: brave-otter-runs
Run: 20261114T101500_123456789Z-4242-1
Resumed: false
Started: 2026-11-14T10:15:00Z
Model: gpt-4o
Base URL: https://api.openai.com/v1
Working Dir: /home/user/project
================================================================================
SYSTEM PROMPT
--------------------------------------------------------------------------------
<full system prompt + skills>
--------------------------------------------------------------------------------
TURN 1 | USER INPUT
--------------------------------------------------------------------------------
<full prompt>
--------------------------------------------------------------------------------
[T] TURN 1 STEP 0 | LLM REQUEST | messages=2 tokens~=123 context=sha256:...
[T] TURN 1 STEP 0 | ASSISTANT
--------------------------------------------------------------------------------
<assistant content or "(no content)">
--------------------------------------------------------------------------------
[T] TURN 1 STEP 0 | TOOL CALL bash | call_id=call_1
{
  "command": "go test ./..."
}
[T] TURN 1 STEP 0 | TOOL RESULT bash | call_id=call_1 success=true exit_code=0 latency_ms=12.3
[T] OUTPUT
--------------------------------------------------------------------------------
<full raw output>
--------------------------------------------------------------------------------
TURN 1 END | outcome=success steps=2 tokens=...
RUN END | outcome=success exit_code=0 steps=... tokens=...
```

When truncation occurred, the transcript additionally records a `TRUNCATION | omitted=<n> | spool=<path>` line and a `MODEL INPUT` block containing the exact content sent to the model. Context compaction records `CONTEXT PRUNED | messages=<before>-><after> evicted=<n>` and a `CONTEXT SUMMARY` block.

---

### 2.8 Agentic Loop & Interactive Sessions (`internal/agent/agent.go`)

```go
type Outcome int
const (
    OutcomeSuccess Outcome = iota
    OutcomeMaxStepsExceeded
    OutcomeLLMError
    OutcomeInterrupted
    OutcomeStorageError // context/log persistence failed
)
func (o Outcome) String() string // "success" | "max_steps_exceeded" | "llm_error" | "interrupted" | "storage_error" | "unknown"

type Result struct {
    Outcome      Outcome
    FinalMessage string // only meaningful when Outcome == OutcomeSuccess
    Steps        int
    TotalTokens  int
    Err          error  // populated for error outcomes
}

type ContextStore interface {
    SaveContext(messages []llm.Message) error
}

type Agent struct {
    Config   config.LitegentConfig
    LLM      *llm.Client
    Tools    *tools.Registry
    Context  *ctxmgr.Manager
    Logger   *logging.Logger
    Progress *progress.Reporter // nil-safe; defaults to nil (disabled) if never set
    Store    ContextStore       // nil disables persistence (tests/non-resume callers)

    lastSignature *callSignature // repeated-tool-call loop guard state, reset every turn
    repeatCount   int
}

func New(cfg config.LitegentConfig, llmClient *llm.Client, registry *tools.Registry, ctxManager *ctxmgr.Manager, logger *logging.Logger) *Agent

func (a *Agent) Run(ctx context.Context, systemPrompt, userPrompt string) Result
func (a *Agent) RunWithHistory(ctx context.Context, history []llm.Message, userPrompt string, turn int) Result
func (a *Agent) RunInteractive(
    ctx context.Context,
    systemPrompt string,
    readPrompt func() (prompt string, ok bool),
    onTurnResult func(turn int, result Result) (cont bool),
) Result
func (a *Agent) RunInteractiveWithHistory(ctx context.Context, history []llm.Message, startTurn int, readPrompt func() (prompt string, ok bool), onTurnResult func(turn int, result Result) (cont bool)) Result
```

`Run` and `RunInteractive` are wrappers over the history-aware variants:
- `Run` seeds `[system]` and calls `RunWithHistory`, which appends the user prompt, logs it, saves context, and calls `runTurn`.
- `RunInteractive` seeds `[system]` and calls `RunInteractiveWithHistory`, which starts at `startTurn`, appends each prompt, logs/saves it, and calls `runTurn`.
- A resumed session passes its loaded context and manifest turn count into the history-aware variants.

`runTurn` itself, for each `turn`:
1. Resets `a.lastSignature = nil; a.repeatCount = 0` — a stuck pattern from a previous turn must never suppress (or spuriously flag) a call in a new one.
2. Reports `Progress.TurnStart(turn, MaxSteps, len(messages))`.
3. Loops `step` from 0 while `step < Config.MaxSteps`:
   - Returns `OutcomeInterrupted` immediately if `ctx.Err() != nil`.
   - Checks `Logger.Err()`; a failed audit write yields `OutcomeStorageError`.
   - If `Config.ManageContext`, calls `Context.Prepare`, logs `context_pruning`/`context_summary` from the returned `PrepareResult`, reports `Progress.ContextPruned`, and saves the compacted context before the request when eviction or summarization occurred.
   - Logs + reports the LLM request with the context SHA-256, calls `LLM.ChatCompletion`; a transport/API error or an empty `Choices` slice both yield `OutcomeLLMError`.
   - Logs + reports the LLM response.
   - If the response has no tool calls: **appends the assistant's final message to `messages`**, saves context, logs/reports the finish, and returns `OutcomeSuccess`.
   - Otherwise appends the assistant's tool-calling message, then calls `dispatchToolCall` for each requested call in order, appending each resulting tool message. After all results for the step are appended, context is saved. A storage failure yields `OutcomeStorageError`.
4. If the loop exhausts `MaxSteps` without a final response: saves context, logs a `session_error`, reports `Progress.TurnEnd(turn, "max_steps_exceeded", step)`, and returns `OutcomeMaxStepsExceeded`.

`dispatchToolCall(ctx, step, call)`:
1. Unmarshals `call.Function.Arguments` (a JSON string) into `map[string]any`; a malformed payload degrades to an empty args map rather than erroring.
2. Logs + reports the tool call (the `Progress.ToolCall` report also emits the compact interactive `|ToolCall : ` notice when `ShowToolCalls` is set and `-v` is off), then calls `Tools.Execute`.
3. Computes a `callSignature{name, argsJSON, resultJSON}` and compares it to `a.lastSignature`: equal → `a.repeatCount++`, otherwise reset to `1`; always updates `a.lastSignature`. If `Config.MaxRepeatedToolCalls > 0 && repeatCount > MaxRepeatedToolCalls`, the tool output is prefixed with a `[LOOP DETECTED] ...` notice and a `loop_guard_triggered` event is logged, **instead of** forwarding the raw repeated result.
4. If `Config.ManageContext && Config.ContextConfig.TruncateToolOutputs`, passes the (possibly loop-guard-annotated) output through `Context.TruncateToolOutput`, logging a `context_truncation` event if truncation actually occurred.
5. Logs + reports the tool result with the full raw output, success/exit/latency, and—when truncation or the loop guard changed it—the exact model-visible content plus spool/omitted metadata.
6. Returns a `{Role: llm.RoleTool, ToolCallID: call.ID, Content: output}` message. The caller saves the complete step to `context.json` after all tool results are appended.

`internal/cliapp.runInteractive` supplies the `readPrompt`/`onTurnResult` callbacks (stdin reading, `/exit`/`/quit` handling, stdout/stderr output); `Agent` itself has no knowledge of stdin, stdout, or CLI text commands.

---

### 2.9 Persistent Sessions (`internal/session/`)

`internal/session` owns session identity, layout, locking, manifests, and context persistence.

```go
type Info struct {
    ID, RunID, Dir, ManifestPath, ContextPath, LogDir, SpoolDir, LockPath string
    Lock *Lock
    PreviousRunUnfinished bool
}
type Manifest struct {
    Version int; ID string; CreatedAt, LastUsedAt time.Time
    RunCount, TurnCount int
    Model, BaseURL string
    Runs []RunRecord
}
type RunRecord struct {
    RunID string; StartedAt time.Time
    FinishedAt *time.Time; Outcome string; ExitCode *int
}

func Create(topDir string, opts Options) (*Info, error)
func Resume(topDir, id string, opts Options) (*Info, error)
func LatestID(topDir string) (string, error)
func FinishRun(info *Info, outcome string, exitCode int) error
func SetTurnCount(info *Info, turn int) error
func SaveContext(info *Info, messages []llm.Message) error
func LoadContext(info *Info) ([]llm.Message, error)
func ValidateMessages(messages []llm.Message) error
```

- IDs are 3 or 4 lowercase English words joined by hyphens, generated from embedded curated lists. `Create` claims an ID with an atomic `os.Mkdir(dir, 0700)`, retrying on collision up to 100 times.
- `Resume` validates the ID and manifest, rejects symlinks, acquires an exclusive `flock` on `<session>/.lock` (plus an in-process lock), verifies that `context.json` exists, appends a new run record, and returns the session layout.
- `LatestID` scans valid session manifests under `top_dir` and returns the one with the newest `LastUsedAt`.
- `previousRunUnfinished` is true when the immediately preceding run record has no `FinishedAt`; the CLI warns on resume.
- `context.json` schema:
  ```json
  {
    "version": 1,
    "session_id": "brave-otter-runs",
    "updated_at": "...",
    "messages_sha256": "...",
    "messages": [ ... ]
  }
  ```
- `SaveContext` validates the message sequence, computes a SHA-256 over the canonical message JSON, writes to a same-directory temp file with `0600`, syncs, and atomically renames over `context.json`. The previous blob remains intact if anything fails.
- `ValidateMessages` requires one leading system message, optional summary only at index 2, complete assistant tool-call/result pairs, and no orphaned tool messages or unanswered calls.
- `FinishRun` records `finished_at`, outcome, and exit code for the current run; unfinished records are preserved for diagnostics.

---

## 3. Data Flow & Execution Sequence

```
User CLI Invocation
     │
     ▼
main() [cmd/litegent/main.go]
     │
     └─► os.Exit(cliapp.Run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))

cliapp.Run(argv, stdin, stdout, stderr) int
     │
     ├─► args, err := parseArgs(argv)                       // flag.FlagSet, stdlib only
     ├─► handle -h / --version (early return)
     ├─► reject -i + -p together; reject -r + --resume-last together
     ├─► check required flags (-p unless -i)
     ├─► resolve config path (-c or ~/.litegent/config.json) and load/validate
     ├─► cfg.TopDir -> resolve system prompt (-s or <top_dir>/system.md; create only on new)
     ├─► promptBytes := os.ReadFile(args.promptPath)         // skipped if -i
     ├─► skillsPath := -k or cfg.SkillsDir; load if present
     ├─► if resuming: LatestID/Resume -> load context.json -> warn on prompt drift
     │   else:        session.Create -> save initial [system] context
     ├─► cfg.LogDir/SpoolDir = session paths
     ├─► logger := logging.NewWithRunID(session.LogDir, cfg.APIKey, session.RunID)
     ├─► registry/llmClient/ctxManager/agent; ag.Store = session store
     ├─► prog := progress.New(args.verbose, stderr, cfg.APIKey)
     ├─► ctx, cancel := context.WithCancel; signal.Notify(SIGINT/SIGTERM)
     ├─► logger.LogSessionStart(session.ID, runID, resumed, model, baseURL, cwd, systemPrompt)
     ├─► prog.Startup(...)
     │
     ├───► if interactive:
     │        result := ag.RunInteractiveWithHistory(ctx, history, startTurn, readPrompt, onTurnResult)
     │
     └───► else single-shot:
              result := ag.RunWithHistory(ctx, history, string(promptBytes), startTurn+1)
     │
     └─► finalizeRun: LogRunEnd, Sync logs, FinishRun(manifest run record), return exit code

runTurn(ctx, messages, turn)
     ├─► ctxmgr.Manager.Prepare -> PrepareResult; log pruning/summary; save context if changed
     ├─► logger.LogLLMRequest(context SHA) + progress.Request()
     ├─► llm.Client.ChatCompletion(...)
     ├─► logger.LogLLMResponse(...) + progress.Response()
     ├─► no tool_calls: append final assistant, save context, LogFinish, return success
     └─► tool_calls: execute each, log full args/result, append tool messages,
         save context after the complete step, loop
```

---

## 4. Execution Model & Operational Safety

Litegent deliberately runs **unconfined**: there is no sandbox, chroot, container, or path jail anywhere in the design. It operates with the full filesystem and process privileges of the invoking user across the entire host. Safety is achieved through auditability and operational robustness (never getting stuck), not through restriction.

1. **Unconfined Host Access**:
   - `internal/tools` performs no boundary checks. `bash`, `read_file`, `write_file`, `edit_file`, `glob_search`, and `grep_search` accept and resolve any absolute or relative path the OS permissions allow — including paths well outside the invocation directory.
   - `spool_dir` (default `spool`) and `log_dir` (default `logs`) are session-relative directories under `<top_dir>/<session-id>/`; they keep session artifacts tidy but are not access boundaries for tools.
2. **Host Command Safety & Non-Interactive Execution**:
   - Commands executed through `bash` run under the privileges of the executing user, in any `workdir` requested.
   - A `context.WithTimeout` deadline, `exec.CommandContext`'s default `cmd.Cancel` behavior (kill on cancellation), and an explicit `cmd.WaitDelay` grace period together prevent orphaned or zombie shell processes regardless of what the command does.
   - `cmd.Stdin` is left at its zero value so `os/exec` redirects it from `/dev/null` — any command that tries to read from stdin receives immediate `EOF` instead of hanging the agentic loop.
   - The subprocess environment is the inherited environment plus overrides that neutralize common interactive behaviors: `GIT_PAGER=cat`, `PAGER=cat`, `GIT_TERMINAL_PROMPT=0`, `DEBIAN_FRONTEND=noninteractive`, `TERM=dumb`. This stops commands like `git log`, `git diff`, or `apt-get install` from invoking a pager/editor or blocking on a confirmation prompt.
3. **Repeated Tool-Call Loop Guard** (`internal/agent`):
   - The agent loop keeps a rolling `(tool_name, args_json, result_json)` signature per turn. When the same signature repeats more than `MaxRepeatedToolCalls` (default `3`) times consecutively, the loop injects a `[LOOP DETECTED]` notice in place of the raw repeated result and emits a `loop_guard_triggered` trace event. This state resets at the start of every turn, so a pattern from one interactive turn never falsely flags the first call of the next.
4. **Interruptibility, Including Mid-Prompt**:
   - `internal/cliapp` derives its root context from `signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)`, so `SIGINT`/`SIGTERM` cancel it at any point.
   - In interactive mode, the blocking stdin read happens in a goroutine selected against that same context, so Ctrl+C still terminates the session (`ExitInterrupted`) even while waiting at the `> ` prompt, not just while a turn is actively running.
 5. **Secret Masking**:
    - `LitegentConfig.APIKey` is masked everywhere it could otherwise leak: the dual audit log (`internal/logging.Logger.redact`), the config display helper (`LitegentConfig.MaskedAPIKey`), and the live progress stream (`internal/progress.Reporter.mask`) all scrub the literal key out of any string before writing. All three share one rule: preserve the key's own stub prefix (`<first 3 chars>...<last 4 chars>`, e.g. `sk-...1234`, `r9_...1234`) and mask keys of 7 characters or fewer entirely (`****`).
6. **Clean Exit Handlers**:
   - Every exit path calls `finalizeRun`, which writes the run footer, syncs logs, and records the run outcome/exit code in `session.json`.
   - The agent saves `context.json` at safe boundaries and after final answers; compaction saves it before the next request.
   - `defer` cleanup closes the session lock and log handles. A second signal forces exit after a best-effort log sync. Panics are recovered at the CLI layer, logged with a stack trace, and finalized.
