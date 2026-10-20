# Technical Specification Document: Litegent CLI AI Agent

**Project Name**: Litegent
**Target Platform**: Linux (Ubuntu 22.04+, Debian 12+, RHEL 9+, Fedora 39+, Arch)
**Language & Runtime**: Go 1.22+ (standard library `net/http`, `encoding/json`, `context`, `os/exec`, `flag`; `gopkg.in/yaml.v3` for YAML parsing; `github.com/bmatcuk/doublestar/v4` for recursive `**` glob matching). The shipped `go.mod` pins `go 1.26.8` as the toolchain directive (the version `go mod init` stamped), but any Go 1.22+ toolchain builds and passes the full test suite.
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
|   | - Load JSON/YAML      |  | - Recursive scan *.md |  | - Truncate+Spool  |  |  progress)     |  |
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
| - fileops.go: read, write, edit          |                 |    (logs/trace_<timestamp>.jsonl)    |
| - search.go: glob_search, grep_search    |                 | 2. Formatted Human Narrative Log     |
| - registry.go: Tool interface + Registry |                 |    (logs/session_<timestamp>.log)   |
+------------------------------------------+                 +--------------------------------------+
```

All tools are intentionally **unconfined** (Section 4): none of them restrict paths or commands to the invocation directory.

---

## 2. Module Specifications

### 2.1 CLI Module (`cmd/litegent/main.go` + `internal/cliapp/cli.go`)

Go idiom: `cmd/litegent/main.go` is a single statement — `os.Exit(cliapp.Run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))`. All argument parsing, validation, and orchestration logic lives in `internal/cliapp.Run(argv []string, stdin io.Reader, stdout, stderr io.Writer) int`, where it remains unit-testable (code in `package main` cannot be imported by tests, and the explicit `stdin`/`stdout`/`stderr` parameters let tests substitute `strings.Reader`/`bytes.Buffer` instead of the real OS streams).

#### Interface & Flags
```
litegent [-h] [--version] -s SYSTEM -p PROMPT -c CONFIG [-k SKILLS] [-v]
litegent [-h] [--version] -s SYSTEM -i -c CONFIG [-k SKILLS] [-v]
```
- `-s, --system PATH`: Path to system prompt text file. (Mandatory)
- `-p, --prompt PATH`: Path to task prompt text file. (Mandatory unless `-i` is set; conflicts with `-i`)
- `-c, --config PATH`: Path to JSON or YAML config file. (Mandatory)
- `-k, --skills PATH`: Path to directory of skill Markdown files. (Optional)
- `-i, --interactive`: Run a persistent, multi-turn session reading task prompts from stdin instead of `-p`. (Optional; see 2.1.1)
- `-v, --verbose`: Stream live progress to stderr as the loop runs (see 2.1.2). The dual audit log (2.7) is always written regardless of `-v`.
- `-h, --help`: Print usage and exit 0.
- `--version`: Print `litegent version <version>` and exit 0.

Flags are parsed with the standard library `flag` package, not a third-party CLI library: `internal/cliapp.parseArgs` registers each short/long pair (`-s`/`--system`, `-p`/`--prompt`, `-c`/`--config`, `-k`/`--skills`, `-v`/`--verbose`, `-i`/`--interactive`, `-h`/`--help`) twice against the same backing struct field via `flag.FlagSet.StringVar`/`BoolVar`, because stdlib `flag` already treats `-x` and `--x` identically — no external CLI-parsing dependency is needed. `--version` is registered once (it has no short form). The flag set uses `flag.ContinueOnError` with output discarded (`fs.SetOutput(io.Discard)`); `Run` prints its own usage text on a parse error.

Validation order in `Run`: (1) parse flags, (2) handle `-h`/`--version` early-exit, (3) reject `-i` + `-p` together, (4) check required flags (`-s` always; `-p` only if `!interactive`; `-c` always) and report all missing flags in one error, (5) read the system prompt file, (6) read the task prompt file (skipped if `-i`), (7) load and validate the config file, (8) stat + load the skills directory if `-k` was given, (9) construct the logger, tool registry, LLM client, context manager, progress reporter, and `agent.Agent`, (10) run the agentic loop (single-shot or interactive) under a `signal.NotifyContext`-derived cancellable context.

##### 2.1.1 Interactive Mode (`-i`)

`internal/cliapp.runInteractive` drives the session:
- It wraps `stdin` in a `bufio.Reader` and prints `> ` to stderr before each read.
- Each blocking `ReadString('\n')` call runs in its own goroutine, selected against the cancellable root `context.Context`; if the context is canceled (SIGINT/SIGTERM) while waiting on input, the session ends immediately with `ExitInterrupted` even though the stdin read itself can't be forcibly unblocked (the goroutine is simply abandoned — harmless, since the process exits right after).
- A blank line (after trimming whitespace) re-prompts without starting a turn or contacting the LLM.
- A line that trims to `/exit` or `/quit` (case-insensitive) ends the session cleanly.
- Any other non-blank line becomes that turn's prompt, passed to `agent.Agent.RunInteractive` (2.8).
- After each turn, the turn's `Result` is handled by an `onTurnResult` callback: `OutcomeSuccess` prints `FinalMessage` to stdout, followed by the `----====----` turn separator, and continues; `OutcomeMaxStepsExceeded` reports to stderr and continues (the session is not terminated — history remains valid for the next turn); `OutcomeInterrupted` and `OutcomeLLMError` both print to stderr and end the session (return `false` from the callback).
- The CLI-level exit code is `ExitInterrupted` for `OutcomeInterrupted`, `ExitLLMAPIError` for `OutcomeLLMError`, and `ExitSuccess` for everything else (including a session that ended after one or more turns hit `OutcomeMaxStepsExceeded`, since the session itself still ended cleanly via `/exit`/EOF).

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
Plain Go structs with `yaml`/`json` struct tags. Because valid JSON is also valid YAML, a single `yaml.Unmarshal` call (via `gopkg.in/yaml.v3`, called directly from `Load` — there is no separate wrapper file) transparently parses both configuration formats. Validation is performed with an explicit `(LitegentConfig) Validate() error` method rather than a schema library, keeping the dependency footprint minimal.

```go
type ToolBashConfig struct {
    Enabled        bool    `yaml:"enabled" json:"enabled"`
    Timeout        float64 `yaml:"timeout" json:"timeout"`
    DefaultWorkdir string  `yaml:"default_workdir" json:"default_workdir"`
}

type ToolFileOpsConfig struct {
    Enabled      bool `yaml:"enabled" json:"enabled"`
    MaxReadLines int  `yaml:"max_read_lines" json:"max_read_lines"`
}

type ToolSearchConfig struct {
    Enabled          bool `yaml:"enabled" json:"enabled"`
    MaxSearchResults int  `yaml:"max_search_results" json:"max_search_results"`
}

type ToolsConfig struct {
    Bash    ToolBashConfig    `yaml:"bash" json:"bash"`
    FileOps ToolFileOpsConfig `yaml:"file_ops" json:"file_ops"`
    Search  ToolSearchConfig  `yaml:"search" json:"search"`
}

type ContextConfig struct {
    MaxContextTokens   int    `yaml:"max_context_tokens" json:"max_context_tokens"`
    MaxToolOutputChars int    `yaml:"max_tool_output_chars" json:"max_tool_output_chars"`
    WindowSize         int    `yaml:"window_size" json:"window_size"`
    SpoolDir           string `yaml:"spool_dir" json:"spool_dir"`
}

type LitegentConfig struct {
    BaseURL              string        `yaml:"base_url" json:"base_url"`
    APIKey               string        `yaml:"api_key" json:"api_key"`
    Model                string        `yaml:"model" json:"model"`
    Temperature          float64       `yaml:"temperature" json:"temperature"`
    MaxTokens            int           `yaml:"max_tokens" json:"max_tokens"`
    ThinkingLevel        string        `yaml:"thinking_level" json:"thinking_level"`
    ThinkingFormat       string        `yaml:"thinking_format" json:"thinking_format"`
    Timeout              float64       `yaml:"timeout" json:"timeout"`
    MaxSteps             int           `yaml:"max_steps" json:"max_steps"`
    MaxRepeatedToolCalls int           `yaml:"max_repeated_tool_calls" json:"max_repeated_tool_calls"`
    ManageContext        bool          `yaml:"manage_context" json:"manage_context"`
    ContextConfig        ContextConfig `yaml:"context_config" json:"context_config"`
    LogDir               string        `yaml:"log_dir" json:"log_dir"`
    Tools                ToolsConfig   `yaml:"tools" json:"tools"`
}

// Default returns the documented default configuration. Load starts from
// this value and unmarshals the user-supplied file on top of it, so
// omitted keys retain sane defaults instead of zero-valuing to false/0/"".
func Default() LitegentConfig {
    return LitegentConfig{
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
            MaxContextTokens:   16000,
            MaxToolOutputChars: 4000,
            WindowSize:         20,
            SpoolDir:           ".tmp/tool_outputs",
        },
        LogDir: "./logs",
        Tools: ToolsConfig{
            Bash:    ToolBashConfig{Enabled: true, Timeout: 60.0, DefaultWorkdir: "."},
            FileOps: ToolFileOpsConfig{Enabled: true, MaxReadLines: 2000},
            Search:  ToolSearchConfig{Enabled: true, MaxSearchResults: 100},
        },
    }
}
```

`Validate() error` rejects an empty `BaseURL` or `Model`, a non-positive `MaxSteps`, a negative `MaxRepeatedToolCalls`, and — only when `ManageContext` is true — a non-positive `MaxContextTokens` or `MaxToolOutputChars` or `WindowSize`. It also rejects, but only when thinking is active, a `ThinkingFormat` other than `"reasoning_effort"` or `"thinking"` (an empty format means the `"reasoning_effort"` default). The level itself is never validated: `ThinkingLevel` is free text passed through verbatim, and `(LitegentConfig) ThinkingActive() bool` reports whether it carries an explicit non-`"default"` value that must be sent. A `(LitegentConfig) MaskedAPIKey() string` helper renders `<first 3 chars>...<last 4 chars>`, preserving the key's own stub prefix (e.g. `sk-...wxyz`, `r9_...wxyz`); keys of 7 characters or fewer are masked entirely (`****`), since a 3+4 character reveal would expose the whole key. Callers use it anywhere a key must be shown without leaking it.

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
Stage 2: Sliding Window Pruner (Prepare — retains System [0], first user turn [1], and last N turns)
    │
    ▼
Stage 3: Conversation Summarizer (Prepare — only when Stage 2 evicted something AND the token
    │     budget is actually exceeded; otherwise evicted turns are just dropped)
    ▼
Clean Messages Ready for LLM
```

#### Stage 1: Tool Output Truncation — `(m *Manager) TruncateToolOutput(callID, rawOutput string) (string, *TruncationInfo, error)`
```go
type TruncationInfo struct {
    SpoolFile    string
    OmittedChars int
}
```
If `m.cfg.MaxToolOutputChars <= 0` or `len(rawOutput)` is already within it, returns `(rawOutput, nil, nil)` unchanged — a `nil` `*TruncationInfo` means "no truncation happened," which `internal/agent` uses to decide whether to log a `context_truncation` trace event. Otherwise: `os.MkdirAll(spoolDir, 0o755)` (defaulting to `.tmp/tool_outputs` if `SpoolDir` is empty — an ordinary default, not an access boundary, per Section 4), writes the full `rawOutput` to `<spoolDir>/output_<callID>.txt`, then splices `headLen = 0.6 * maxChars` head characters + a notice naming the omitted character count and the spool file path + `tailLen = 0.3 * maxChars` tail characters.

#### Stage 2 & 3 — `(m *Manager) Prepare(ctx context.Context, messages []llm.Message) ([]llm.Message, error)`
Message layout: `messages[0]` = system prompt (permanent), `messages[1]` = the first user message in the conversation — single-shot mode's `-p` content, or interactive mode's first turn (permanent), an optional `messages[2]` = `<context_summary>` (role `llm.RoleSummary`), and everything after that is active dialogue subject to pruning.

`Prepare` is a no-op if `len(messages) <= 2`. Otherwise: if `len(active) > m.cfg.WindowSize` (where `active` is everything after the anchors and any existing summary), the oldest `len(active) - WindowSize` messages become `evicted`. A summarization call is only made when **both**:
1. `m.summarizer != nil`, and
2. `overBudget` — computed as `m.cfg.MaxContextTokens <= 0 || EstimateTokens(messages) > m.cfg.MaxContextTokens` (pre-eviction estimate; `MaxContextTokens <= 0` is treated as "no budget gate configured," so it falls back to always summarizing on overflow — the same convention Stage 1 uses for `MaxToolOutputChars <= 0`).

When a summarization call is made, it covers the newly evicted messages plus any prior `<context_summary>` content (so a summary is never silently dropped by a later eviction), and the result replaces `messages[2]` as the new `<context_summary>`. **When the window overflows but the token budget is not exceeded, the evicted turns are simply dropped with no summarization call** — this avoids an LLM round-trip on every window overflow regardless of actual token pressure (this gating was a deliberate fix; an earlier version always summarized on overflow, leaving `MaxContextTokens` effectively unused).

`EstimateTokens(messages []llm.Message) int` is a dependency-free heuristic: total character count (message `Content` plus each tool call's `Function.Name`+`Function.Arguments`) divided by 4 — not a real tokenizer, intentionally, to keep the dependency footprint minimal.

---

### 2.6 Skills Loader (`internal/skills/skills.go`)

`BuildSkillsPrompt(skillsDir string) (string, error)`:
- Returns an error if `skillsDir` doesn't exist or isn't a directory.
- `filepath.WalkDir(skillsDir, ...)`, matching files whose lowercased name ends in `.md`.
- Extracts the title from the first line matching `# <Title>` (via a line-by-line scan with `bufio.Scanner`); falls back to the filename if no such heading exists.
- Assembles `<available_skills>\n  <skill name=%q file=%q>\n<full file content>\n  </skill>\n...\n</available_skills>` — one `<skill>` block per discovered file, in filesystem walk order.
- Returns `("", nil)` (empty string, no error) if zero `.md` files were found, so callers can skip injection entirely.

`internal/cliapp.Run` appends the result (if non-empty) to the system prompt text with a `"\n\n"` separator before passing it to the agent.

---

### 2.7 Dual Logging Subsystem (`internal/logging/logging.go`)

Because Litegent runs unconfined with no sandbox (Section 4), this log is the system's primary safety and accountability control — every LLM and tool interaction is captured here regardless of `-v`.

#### Session Lifecycle
`logging.New(logDir, apiKey string) (*Logger, error)`:
- `os.MkdirAll(logDir, 0o755)`.
- Timestamp tag `ts := time.Now().UTC().Format("20060102_150405")`.
- Opens `trace_{ts}.jsonl` and `session_{ts}.log` with `O_APPEND|O_CREATE|O_WRONLY`, exposed as `Logger.TracePath`/`Logger.SessionPath`.
- `apiKey`, if non-empty, is redacted out of every string value written to either file — not just in a dedicated "api_key" field, but anywhere the literal key text would otherwise appear (e.g. inside an error message). The mask preserves the key's own stub prefix (`<first 3 chars>...<last 4 chars>`, e.g. `sk-...1234`, `r9_...1234`); keys of 7 characters or fewer are masked entirely (`****`).

#### JSONL Trace Events (`trace_{ts}.jsonl`)
```go
type TraceEvent struct {
    Timestamp string         `json:"timestamp"` // ISO 8601 UTC, time.RFC3339Nano
    EventType string         `json:"event_type"`
    Step      int            `json:"step"`
    Data      map[string]any `json:"data"`
}
```
One JSON line per event, written under a mutex. The event types actually emitted by the current implementation, each via a dedicated `Logger.Log*` method called from `internal/cliapp`/`internal/agent`:

| `event_type` | Emitted by | When |
|---|---|---|
| `session_start` | `cliapp.Run` | Once, before the loop starts (single-shot or interactive) |
| `llm_request` | `agent.runTurn` | Before every `ChatCompletion` call |
| `llm_response` | `agent.runTurn` | After every `ChatCompletion` call |
| `tool_call` | `agent.dispatchToolCall` | Before executing each tool call |
| `tool_result` | `agent.dispatchToolCall` | After executing each tool call |
| `context_truncation` | `agent.dispatchToolCall` | Only when `ctxmgr.TruncateToolOutput` actually truncated something |
| `loop_guard_triggered` | `agent.dispatchToolCall` | Only when the repeated-tool-call loop guard fires |
| `session_finish` | `agent.runTurn` | Once per **turn** that completes with `OutcomeSuccess` — in interactive mode this fires once per turn, not once per process |
| `session_error` | `agent.runTurn` | Once per turn/run that ends via `OutcomeLLMError`, `OutcomeInterrupted`, or `OutcomeMaxStepsExceeded` |

(`internal/logging` also defines `EventContextPruning`/`EventContextSummary` constants and a `LogContextSummary` method, but nothing in the current agent loop calls them — the context manager's sliding-window eviction and summarization, Section 2.5, happen silently from the trace log's perspective today. Their effect is only visible indirectly: fewer/different messages in the next `llm_request`'s implied history, and a `<context_summary>` system message if one was produced.)

#### Human Transcript (`session_{ts}.log`)
Plain text, written under the same mutex as the JSONL file. Header on `LogSessionStart`:
```text
================================================================================
Litegent Session: <session_id>
Started: <ISO8601 UTC>
Model: <model>
Base URL: <base_url>
================================================================================
```
Per-event lines (reusing the trace event's timestamp/step):
```text
[2026-10-03T14:32:10.123456789Z] STEP 4 | LLM Request (tokens: ~3420)
[2026-10-03T14:32:12.456789012Z] STEP 4 | LLM Thought:
Inspecting test results to locate failing assertions.
[2026-10-03T14:32:12.456999999Z] STEP 4 | Tool Call:
bash(command="go test ./internal/auth/... -v")
[2026-10-03T14:32:13.789012345Z] STEP 4 | Tool Result (exit_code=1):
--- FAIL: TestTokenExpiry (0.00s)
```
`bash(command="...")`-style tool-call lines are built by `formatCall`, which sorts the argument keys before formatting so the same call always renders identically across runs (Go map iteration order is otherwise randomized; this has no effect on the JSONL file, since `encoding/json` already sorts map keys when marshaling). A `[LOOP DETECTED] <tool> repeated <n> times with the same result` line is written whenever the loop guard fires. Footer on `LogFinish`:
```text
================================================================================
Final Response:
<final message text>
Completed: <ISO8601 UTC>
Total Steps: <n>
Total Tokens: <n>
================================================================================
```

---

### 2.8 Agentic Loop & Interactive Sessions (`internal/agent/agent.go`)

```go
type Outcome int
const (
    OutcomeSuccess Outcome = iota
    OutcomeMaxStepsExceeded
    OutcomeLLMError
    OutcomeInterrupted
)
func (o Outcome) String() string // "success" | "max_steps_exceeded" | "llm_error" | "interrupted" | "unknown"

type Result struct {
    Outcome      Outcome
    FinalMessage string // only meaningful when Outcome == OutcomeSuccess
    Steps        int    // single-shot: steps taken; RunInteractive's own return: turns processed
    TotalTokens  int
    Err          error  // populated for OutcomeLLMError and OutcomeInterrupted
}

type Agent struct {
    Config   config.LitegentConfig
    LLM      *llm.Client
    Tools    *tools.Registry
    Context  *ctxmgr.Manager
    Logger   *logging.Logger
    Progress *progress.Reporter // nil-safe; defaults to nil (disabled) if never set

    lastSignature *callSignature // repeated-tool-call loop guard state, reset every turn
    repeatCount   int
}

func New(cfg config.LitegentConfig, llmClient *llm.Client, registry *tools.Registry, ctxManager *ctxmgr.Manager, logger *logging.Logger) *Agent

func (a *Agent) Run(ctx context.Context, systemPrompt, userPrompt string) Result
func (a *Agent) RunInteractive(
    ctx context.Context,
    systemPrompt string,
    readPrompt func() (prompt string, ok bool),
    onTurnResult func(turn int, result Result) (cont bool),
) Result
```

`Run` and `RunInteractive` both delegate to a shared, unexported `runTurn(ctx, messages []llm.Message, turn int) ([]llm.Message, Result)`:
- `Run` seeds `messages` with `[{RoleSystem, systemPrompt}, {RoleUser, userPrompt}]` and calls `runTurn(ctx, messages, 1)` once, discarding the returned messages and keeping only the `Result`.
- `RunInteractive` seeds `messages` with just `[{RoleSystem, systemPrompt}]`, then loops: call `readPrompt()` (returning `ok=false` ends the session with `Result{Outcome: OutcomeSuccess, Steps: <turns so far>}`); append the prompt as a `{RoleUser, ...}` message; call `runTurn(ctx, messages, turn)`, keeping the returned (possibly pruned/summarized) messages as the new `messages` for the next turn; call `onTurnResult(turn, result)` — returning `false` ends the session immediately, returning that turn's `Result` as the overall one.

`runTurn` itself, for each `turn`:
1. Resets `a.lastSignature = nil; a.repeatCount = 0` — a stuck pattern from a previous turn must never suppress (or spuriously flag) a call in a new one.
2. Reports `Progress.TurnStart(turn, MaxSteps, len(messages))`.
3. Loops `step` from 0 while `step < Config.MaxSteps`:
   - Returns `OutcomeInterrupted` immediately if `ctx.Err() != nil`.
   - If `Config.ManageContext`, calls `Context.Prepare` and reports `Progress.ContextPruned(step, before, after)`.
   - Logs + reports the LLM request, calls `LLM.ChatCompletion`; a transport/API error or an empty `Choices` slice both yield `OutcomeLLMError`.
   - Logs + reports the LLM response.
   - If the response has no tool calls: **appends the assistant's final message to `messages`** (so a later turn in an interactive session still sees it — single-shot `Run` doesn't need this since the process exits right after, but it costs nothing extra), logs/reports the finish, and returns `OutcomeSuccess` with that content as `FinalMessage`.
   - Otherwise appends the assistant's tool-calling message, then calls `dispatchToolCall` for each requested call in order (sequentially, not concurrently — see Section 4 item 6 for why), appending each resulting tool message.
4. If the loop exhausts `MaxSteps` without a final response: logs a `session_error`, reports `Progress.TurnEnd(turn, "max_steps_exceeded", step)`, and returns `OutcomeMaxStepsExceeded`.

`dispatchToolCall(ctx, step, call)`:
1. Unmarshals `call.Function.Arguments` (a JSON string) into `map[string]any`; a malformed payload degrades to an empty args map rather than erroring.
2. Logs + reports the tool call (the `Progress.ToolCall` report also emits the compact interactive `|ToolCall : ` notice when `ShowToolCalls` is set and `-v` is off), then calls `Tools.Execute`.
3. Computes a `callSignature{name, argsJSON, resultJSON}` and compares it to `a.lastSignature`: equal → `a.repeatCount++`, otherwise reset to `1`; always updates `a.lastSignature`. If `Config.MaxRepeatedToolCalls > 0 && repeatCount > MaxRepeatedToolCalls`, the tool output is prefixed with a `[LOOP DETECTED] ...` notice and a `loop_guard_triggered` event is logged, **instead of** forwarding the raw repeated result.
4. If `Config.ManageContext`, passes the (possibly loop-guard-annotated) output through `Context.TruncateToolOutput`, logging a `context_truncation` event if truncation actually occurred.
5. Logs + reports the tool result (output truncated to 500 chars in the log/transcript summary, regardless of the context-management truncation above, which operates on what's sent back to the model).
6. Returns a `{Role: llm.RoleTool, ToolCallID: call.ID, Content: output}` message.

`internal/cliapp.runInteractive` supplies the `readPrompt`/`onTurnResult` callbacks (stdin reading, `/exit`/`/quit` handling, stdout/stderr output); `Agent` itself has no knowledge of stdin, stdout, or CLI text commands.

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
     ├─► reject -i + -p together
     ├─► check required flags (-s always, -p unless -i, -c always)
     ├─► systemBytes := os.ReadFile(args.systemPath)
     ├─► promptBytes := os.ReadFile(args.promptPath)         // skipped if -i
     ├─► cfg, err := config.Load(args.configPath)
     ├─► if -k: skillsXML, err := skills.BuildSkillsPrompt(args.skillsPath)
     │          systemPrompt += "\n\n" + skillsXML            // if non-empty
     ├─► logger, err := logging.New(cfg.LogDir, cfg.APIKey)
     ├─► registry := buildRegistry(cfg)                       // registers enabled tools
     ├─► llmClient := llm.NewClient(cfg)
     ├─► ctxManager := ctxmgr.NewManager(cfg.ContextConfig, llmClient)
     ├─► ag := agent.New(cfg, llmClient, registry, ctxManager, logger)
     ├─► prog := progress.New(args.verbose, stderr, cfg.APIKey); ag.Progress = prog
     ├─► ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
     ├─► logger.LogSessionStart(sessionID, cfg.Model, cfg.BaseURL)
     ├─► prog.Startup(cfg.Model, cfg.BaseURL, len(registry.Schemas()), args.interactive)
     │
     ├───► if args.interactive:
     │        return runInteractive(ctx, ag, systemPrompt, cfg, prog, stdin, stdout, stderr)
     │            │
     │            ├─► result := ag.RunInteractive(ctx, systemPrompt, readPrompt, onTurnResult)
     │            ├─► prog.SessionEnd(result.Outcome.String(), result.Steps)
     │            └─► map result.Outcome -> exit code (interrupted/llm_error/else success)
     │
     └───► else (single-shot):
              result := ag.Run(ctx, systemPrompt, string(promptBytes))
              prog.SessionEnd(result.Outcome.String(), 1)
              switch result.Outcome:
                 Success            -> print result.FinalMessage to stdout, print the ----====---- turn separator, return 0
                MaxStepsExceeded   -> print to stderr, return 2
                Interrupted        -> print to stderr, return 130
                LLMError (default) -> print to stderr, return 3

ag.Run(ctx, systemPrompt, userPrompt) / ag.RunInteractive(ctx, systemPrompt, readPrompt, onTurnResult)
     │
     └─► both call the shared runTurn(ctx, messages, turn) loop described in Section 2.8
              │
              ├─► ctxmgr.Manager.Prepare(ctx, messages)         // Stage 2/3, if manage_context
              ├─► logger.LogLLMRequest() + progress.Request()
              ├─► llm.Client.ChatCompletion(ctx, messages, registry.Schemas())
              ├─► logger.LogLLMResponse() + progress.Response()
              │
              ├───► no tool_calls: append final message, logger.LogFinish(), return OutcomeSuccess
              │
              └───► tool_calls present: for each call,
                       tools.Registry.Execute(ctx, call.Function.Name, args)
                       loop-guard check -> ctxmgr.Manager.TruncateToolOutput()
                       logger.LogToolResult() + progress.ToolResult()
                       append {RoleTool, ToolCallID, Content} to messages
                     loop back to Prepare for the next step
```

---

## 4. Execution Model & Operational Safety

Litegent deliberately runs **unconfined**: there is no sandbox, chroot, container, or path jail anywhere in the design. It operates with the full filesystem and process privileges of the invoking user across the entire host. Safety is achieved through auditability and operational robustness (never getting stuck), not through restriction.

1. **Unconfined Host Access**:
   - `internal/tools` performs no boundary checks. `bash`, `read_file`, `write_file`, `edit_file`, `glob_search`, and `grep_search` accept and resolve any absolute or relative path the OS permissions allow — including paths well outside the invocation directory.
   - `spool_dir` (default `.tmp/tool_outputs`) and `log_dir` (default `./logs`) are ordinary configuration defaults chosen for tidy organization, not enforced boundaries; both accept any path.
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
   - `defer`-based cleanup on the log file handles (`Logger.Close`) and the signal context's `cancel` function ensure logs are flushed and closed even under `SIGINT`/`SIGTERM`.
