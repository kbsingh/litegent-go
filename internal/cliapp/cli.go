// Package cliapp implements Litegent's command-line interface: argument
// parsing/validation and top-level orchestration of config loading,
// skills injection, tool registration, and the agentic loop. See
// req/spec_doc.md section 2.1.
//
// cmd/litegent/main.go is kept to a single line calling Run and
// os.Exit-ing with its result, so this package remains unit-testable
// (code in package main cannot be imported by tests).
package cliapp

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"litegent/internal/agent"
	"litegent/internal/config"
	"litegent/internal/ctxmgr"
	"litegent/internal/llm"
	"litegent/internal/logging"
	"litegent/internal/progress"
	"litegent/internal/session"
	"litegent/internal/skills"
	"litegent/internal/tools"
	"litegent/internal/version"
)

// Exit codes, per req/spec_doc.md section 2.1.
const (
	ExitSuccess          = 0
	ExitConfigOrCLIError = 1
	ExitMaxStepsExceeded = 2
	ExitLLMAPIError      = 3
	ExitInterrupted      = 130
)

// turnSeparator is printed to stdout after every successful final
// response so turn boundaries are visually unambiguous in the
// transcript. It is presentation only — never added to history.
const turnSeparator = "----====----"

// Bootstrap defaults. When -c is omitted, Litegent still bootstraps from
// ~/.litegent/config.json. Once that config is loaded, top_dir governs
// system.md, skills, sessions, logs, and spool storage.
const (
	defaultDirName              = ".litegent"
	defaultSystemPromptFileName = "system.md"
	defaultConfigFileName       = "config.json"

	// defaultSystemPrompt is used when neither -s nor
	// ~/.litegent/system.md supplies a prompt. It sets baseline
	// expectations for confirmation, backups, clarifying questions,
	// scope discipline, documentation lookup, and follow-up
	// recommendations.
	defaultSystemPrompt = "you are a helpful, considerate, highly experienced technical assistant. " +
		"Always confirm, make backups before making changes, remove ambiguity by asking questions, " +
		"never go beyond the request - stay focused. But when issues and opportunities are discovered, " +
		"recommend followup actions; when possible consult docs"
)

const usage = `litegent - an autonomous, unconfined CLI AI agent for Linux

Usage:
  litegent [-s <system_prompt_file>] -p <user_prompt_file> [-c <config_file>] [-k <skills_directory>] [-v] [-r <session-id> | --resume-last]
  litegent [-s <system_prompt_file>] -i [-c <config_file>] [-k <skills_directory>] [-v] [-r <session-id> | --resume-last]

Flags:
  -s, --system PATH   Path to the system prompt file (default: <top_dir>/system.md, created if absent)
  -p, --prompt PATH   Path to the task prompt file (required unless -i is set)
  -c, --config PATH   Path to the JSON config file (default: ~/.litegent/config.json)
  -k, --skills PATH   Path to a directory of skill Markdown files (optional)
  -i, --interactive   Read repeated task prompts from stdin; conflicts with -p
  -r, --resume ID     Resume an existing session instead of creating a new one
      --resume-last   Resume the most recently used session
  -v, --verbose       Enable verbose progress output to stderr as the loop runs
  -h, --help          Show this help message
      --version       Show version information

Interactive mode (-i): each line read from stdin starts a new turn. Submit
an empty line to re-prompt. Type /exit or /quit, or send EOF (Ctrl+D), to
end the session. Conversation history persists across turns; max_steps
applies independently to each turn.
`

// defaultBootstrapDir returns ~/.litegent, the location used only to
// bootstrap config.json when -c is omitted. After the config is loaded,
// top_dir governs every default path.
func defaultBootstrapDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("determining home directory: %w", err)
	}
	return filepath.Join(home, defaultDirName), nil
}

// defaultConfigPath returns ~/.litegent/config.json, the config used when
// -c/--config is omitted.
func defaultConfigPath() (string, error) {
	dir, err := defaultBootstrapDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, defaultConfigFileName), nil
}

// defaultSystemPromptPath returns <top_dir>/system.md, the system prompt
// used when -s/--system is omitted.
func defaultSystemPromptPath(topDir string) string {
	return filepath.Join(topDir, defaultSystemPromptFileName)
}

// ensureDefaultSystemPrompt writes content to path when path is absent.
// It creates the parent directory, never overwrites an existing file, and
// returns created=true only when this call wrote the file. O_EXCL makes
// concurrent first runs safe: the loser observes created=false.
func ensureDefaultSystemPrompt(path, content string) (created bool, err error) {
	if _, statErr := os.Stat(path); statErr == nil {
		return false, nil
	} else if !errors.Is(statErr, fs.ErrNotExist) {
		return false, fmt.Errorf("checking system prompt %q: %w", path, statErr)
	}

	if dir := filepath.Dir(path); dir != "." && dir != "" {
		if mkErr := os.MkdirAll(dir, 0o755); mkErr != nil {
			return false, fmt.Errorf("creating system prompt directory %q: %w", dir, mkErr)
		}
	}

	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		if errors.Is(err, fs.ErrExist) {
			return false, nil
		}
		return false, fmt.Errorf("creating default system prompt %q: %w", path, err)
	}

	if _, err := f.WriteString(content); err != nil {
		f.Close()
		os.Remove(path)
		return false, fmt.Errorf("writing default system prompt %q: %w", path, err)
	}
	if err := f.Close(); err != nil {
		os.Remove(path)
		return false, fmt.Errorf("closing default system prompt %q: %w", path, err)
	}
	return true, nil
}

// cliArgs holds parsed command-line flags.
type cliArgs struct {
	systemPath  string
	promptPath  string
	configPath  string
	skillsPath  string
	resumeID    string
	resumeLast  bool
	verbose     bool
	interactive bool
	showHelp    bool
	showVersion bool
}

// parseArgs registers each flag under both its short and long name against
// the same backing field. Stdlib flag already treats "-x" and "--x"
// identically, so this is all that's needed for the short/long aliasing
// pflag used to provide — no external dependency required.
func parseArgs(argv []string) (*cliArgs, error) {
	fs := flag.NewFlagSet("litegent", flag.ContinueOnError)
	fs.SetOutput(io.Discard)

	args := &cliArgs{}
	fs.StringVar(&args.systemPath, "s", "", "path to system prompt file (default: ~/.litegent/system.md)")
	fs.StringVar(&args.systemPath, "system", "", "path to system prompt file (default: ~/.litegent/system.md)")
	fs.StringVar(&args.promptPath, "p", "", "path to task prompt file")
	fs.StringVar(&args.promptPath, "prompt", "", "path to task prompt file")
	fs.StringVar(&args.configPath, "c", "", "path to JSON config file (default: ~/.litegent/config.json)")
	fs.StringVar(&args.configPath, "config", "", "path to JSON config file (default: ~/.litegent/config.json)")
	fs.StringVar(&args.skillsPath, "k", "", "path to skills directory")
	fs.StringVar(&args.skillsPath, "skills", "", "path to skills directory")
	fs.StringVar(&args.resumeID, "r", "", "resume an existing session by id")
	fs.StringVar(&args.resumeID, "resume", "", "resume an existing session by id")
	fs.BoolVar(&args.resumeLast, "resume-last", false, "resume the most recently used session")
	fs.BoolVar(&args.verbose, "v", false, "enable verbose output")
	fs.BoolVar(&args.verbose, "verbose", false, "enable verbose output")
	fs.BoolVar(&args.interactive, "i", false, "read repeated task prompts from stdin")
	fs.BoolVar(&args.interactive, "interactive", false, "read repeated task prompts from stdin")
	fs.BoolVar(&args.showHelp, "h", false, "show help")
	fs.BoolVar(&args.showHelp, "help", false, "show help")
	fs.BoolVar(&args.showVersion, "version", false, "show version")

	if err := fs.Parse(argv); err != nil {
		return nil, err
	}
	return args, nil
}

// Run parses argv, wires together config/skills/tools/logging/agent, and
// executes the agentic loop (single-shot, or interactive if -i is set),
// writing output to stdout/stderr and reading interactive prompts from
// stdin. It returns the process exit code.
func Run(argv []string, stdin io.Reader, stdout, stderr io.Writer) int {
	args, err := parseArgs(argv)
	if err != nil {
		fmt.Fprintf(stderr, "litegent: %v\n\n%s", err, usage)
		return ExitConfigOrCLIError
	}
	if args.showHelp {
		fmt.Fprint(stdout, usage)
		return ExitSuccess
	}
	if args.showVersion {
		fmt.Fprintf(stdout, "litegent version %s\n", version.Version)
		return ExitSuccess
	}

	if args.interactive && args.promptPath != "" {
		fmt.Fprintf(stderr, "litegent: -i/--interactive conflicts with -p/--prompt\n\n%s", usage)
		return ExitConfigOrCLIError
	}
	if args.resumeID != "" && args.resumeLast {
		fmt.Fprintf(stderr, "litegent: -r/--resume conflicts with --resume-last\n\n%s", usage)
		return ExitConfigOrCLIError
	}

	var missing []string
	if !args.interactive && args.promptPath == "" {
		missing = append(missing, "-p/--prompt")
	}
	if len(missing) > 0 {
		fmt.Fprintf(stderr, "litegent: missing required flag(s): %v\n\n%s", missing, usage)
		return ExitConfigOrCLIError
	}

	// Resolve the config path first: top_dir lives inside the config, so
	// system.md and all other defaults cannot be resolved until it loads.
	// When -c is omitted we retain the historical bootstrap behavior:
	// ~/.litegent/config.json is created with documented defaults.
	configPath := args.configPath
	if configPath == "" {
		path, pathErr := defaultConfigPath()
		if pathErr != nil {
			fmt.Fprintf(stderr, "litegent: resolving default config path: %v\n", pathErr)
			return ExitConfigOrCLIError
		}
		configPath = path
		created, ensureErr := config.EnsureDefaultConfig(configPath)
		if ensureErr != nil {
			fmt.Fprintf(stderr, "litegent: preparing default config: %v\n", ensureErr)
			return ExitConfigOrCLIError
		}
		if created {
			fmt.Fprintf(stderr, "litegent: created default config at %s\n", configPath)
		}
	}
	cfg, err := config.Load(configPath)
	if err != nil {
		fmt.Fprintf(stderr, "litegent: loading config: %v\n", err)
		return ExitConfigOrCLIError
	}

	// Resolve the system prompt: an explicit -s always wins. For a new
	// session, ensure <top_dir>/system.md exists, creating it with the
	// built-in prompt on first use. A resumed session never creates a new
	// default prompt; its stored system message is authoritative.
	resuming := args.resumeID != "" || args.resumeLast
	systemPrompt := defaultSystemPrompt
	systemPath := args.systemPath
	if systemPath == "" {
		systemPath = defaultSystemPromptPath(cfg.TopDir)
		if !resuming {
			created, ensureErr := ensureDefaultSystemPrompt(systemPath, defaultSystemPrompt)
			if ensureErr != nil {
				fmt.Fprintf(stderr, "litegent: preparing default system prompt: %v\n", ensureErr)
				return ExitConfigOrCLIError
			}
			if created {
				fmt.Fprintf(stderr, "litegent: created default system prompt at %s\n", systemPath)
			}
		}
	}
	if systemPath != "" {
		systemBytes, readErr := os.ReadFile(systemPath)
		if readErr != nil {
			if resuming && args.systemPath == "" && errors.Is(readErr, fs.ErrNotExist) {
				// No prompt beside the config; the stored session prompt
				// remains authoritative after it is loaded.
			} else {
				fmt.Fprintf(stderr, "litegent: reading system prompt: %v\n", readErr)
				return ExitConfigOrCLIError
			}
		} else {
			systemPrompt = string(systemBytes)
		}
	}

	var promptBytes []byte
	if !args.interactive {
		promptBytes, err = os.ReadFile(args.promptPath)
		if err != nil {
			fmt.Fprintf(stderr, "litegent: reading task prompt: %v\n", err)
			return ExitConfigOrCLIError
		}
	}

	// Resolve skills: an explicit -k always wins. Otherwise use
	// <top_dir>/skills when configured, treating a missing default
	// directory as "no skills" rather than an error.
	skillsPath := args.skillsPath
	explicitSkills := skillsPath != ""
	if skillsPath == "" {
		skillsPath = cfg.SkillsDir
	}
	if skillsPath != "" {
		if _, statErr := os.Stat(skillsPath); statErr != nil {
			if explicitSkills || !errors.Is(statErr, fs.ErrNotExist) {
				fmt.Fprintf(stderr, "litegent: skills directory: %v\n", statErr)
				return ExitConfigOrCLIError
			}
		} else {
			skillsXML, skillsErr := skills.BuildSkillsPrompt(skillsPath)
			if skillsErr != nil {
				fmt.Fprintf(stderr, "litegent: loading skills: %v\n", skillsErr)
				return ExitConfigOrCLIError
			}
			if skillsXML != "" {
				systemPrompt = systemPrompt + "\n\n" + skillsXML
			}
		}
	}

	opts := session.Options{
		LogSubdir:   cfg.LogDir,
		SpoolSubdir: cfg.ContextConfig.SpoolDir,
		Model:       cfg.Model,
		BaseURL:     cfg.BaseURL,
	}
	var (
		sess      *session.Info
		store     *session.Store
		history   []llm.Message
		startTurn int
	)
	if resuming {
		id := args.resumeID
		if args.resumeLast {
			id, err = session.LatestID(cfg.TopDir)
			if err != nil {
				fmt.Fprintf(stderr, "litegent: finding latest session: %v\n", err)
				return ExitConfigOrCLIError
			}
		}
		sess, err = session.Resume(cfg.TopDir, id, opts)
		if err != nil {
			fmt.Fprintf(stderr, "litegent: resuming session: %v\n", err)
			return ExitConfigOrCLIError
		}
		defer sess.Close()
		store = &session.Store{Info: sess}
		history, err = store.LoadContext()
		if err != nil {
			fmt.Fprintf(stderr, "litegent: loading session context: %v\n", err)
			return ExitConfigOrCLIError
		}
		storedSystemPrompt := history[0].Content
		if storedSystemPrompt != systemPrompt {
			fmt.Fprintf(stderr, "litegent: warning: system prompt/skills changed since session %s was created; using the stored prompt\n", sess.ID)
		}
		systemPrompt = storedSystemPrompt
		manifest, manifestErr := session.LoadManifest(sess.Dir)
		if manifestErr != nil {
			fmt.Fprintf(stderr, "litegent: loading session manifest: %v\n", manifestErr)
			return ExitConfigOrCLIError
		}
		startTurn = manifest.TurnCount
		fmt.Fprintf(stderr, "litegent: resumed session %s (%s)\n", sess.ID, sess.Dir)
	} else {
		sess, err = session.Create(cfg.TopDir, opts)
		if err != nil {
			fmt.Fprintf(stderr, "litegent: creating session: %v\n", err)
			return ExitConfigOrCLIError
		}
		defer sess.Close()
		store = &session.Store{Info: sess}
		history = []llm.Message{{Role: llm.RoleSystem, Content: systemPrompt}}
		if err := store.SaveContext(history); err != nil {
			fmt.Fprintf(stderr, "litegent: initializing context: %v\n", err)
			return ExitConfigOrCLIError
		}
		fmt.Fprintf(stderr, "litegent: session %s (%s)\n", sess.ID, sess.Dir)
	}
	cfg.LogDir = sess.LogDir
	cfg.ContextConfig.SpoolDir = sess.SpoolDir

	logger, err := logging.NewWithRunID(sess.LogDir, cfg.APIKey, sess.RunID)
	if err != nil {
		fmt.Fprintf(stderr, "litegent: initializing logger: %v\n", err)
		return ExitConfigOrCLIError
	}
	defer logger.Close()

	registry := buildRegistry(cfg)
	llmClient := llm.NewClient(cfg)
	ctxManager := ctxmgr.NewManager(cfg.ContextConfig, llmClient)
	ag := agent.New(cfg, llmClient, registry, ctxManager, logger)
	ag.Store = store
	prog := progress.New(args.verbose, stderr, cfg.APIKey)
	prog.ShowToolCalls = args.interactive
	ag.Progress = prog

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	workDir, _ := os.Getwd()
	if err := logger.LogSessionStart(sess.ID, sess.RunID, resuming, cfg.Model, cfg.BaseURL, workDir, systemPrompt); err != nil && args.verbose {
		fmt.Fprintf(stderr, "litegent: warning: failed to write session start: %v\n", err)
	}
	prog.Startup(cfg.Model, cfg.BaseURL, len(registry.Schemas()), args.interactive)
	if args.verbose {
		fmt.Fprintf(stderr, "litegent: logs: %s and %s\n", logger.TracePath, logger.SessionPath)
	}

	if args.interactive {
		return runInteractive(ctx, ag, history, startTurn, sess, cfg, prog, logger, stdin, stdout, stderr)
	}

	result := ag.RunWithHistory(ctx, history, string(promptBytes), startTurn+1)
	if result.Outcome == agent.OutcomeSuccess || result.Outcome == agent.OutcomeMaxStepsExceeded {
		_ = session.SetTurnCount(sess, startTurn+1)
	}
	prog.SessionEnd(result.Outcome.String(), result.Steps)

	exitCode := ExitSuccess
	switch result.Outcome {
	case agent.OutcomeSuccess:
		fmt.Fprintln(stdout, result.FinalMessage)
		fmt.Fprintln(stdout, turnSeparator)
	case agent.OutcomeMaxStepsExceeded:
		fmt.Fprintf(stderr, "litegent: max_steps (%d) exceeded without a final response\n", cfg.MaxSteps)
		exitCode = ExitMaxStepsExceeded
	case agent.OutcomeInterrupted:
		fmt.Fprintln(stderr, "litegent: interrupted")
		exitCode = ExitInterrupted
	case agent.OutcomeStorageError:
		fmt.Fprintf(stderr, "litegent: storage error: %v\n", result.Err)
		exitCode = ExitConfigOrCLIError
	default: // agent.OutcomeLLMError and any unforeseen outcome
		fmt.Fprintf(stderr, "litegent: fatal LLM error: %v\n", result.Err)
		exitCode = ExitLLMAPIError
	}
	_ = logger.LogRunEnd(result.Outcome.String(), exitCode, result.Steps, result.TotalTokens)
	return exitCode
}

// runInteractive drives a persistent, multi-turn session: it reads one
// line at a time from stdin (prompting with "> " on stderr), treats
// "/exit"/"/quit" (case-insensitive) or EOF as the end of the session,
// and prints each turn's final response to stdout as it completes.
// Conversation history persists across turns via agent.Agent.RunInteractive;
// each turn gets its own independent max_steps budget.
func runInteractive(ctx context.Context, ag *agent.Agent, history []llm.Message, startTurn int, sess *session.Info, cfg config.LitegentConfig, prog *progress.Reporter, logger *logging.Logger, stdin io.Reader, stdout, stderr io.Writer) int {
	reader := bufio.NewReader(stdin)

	readPrompt := func() (string, bool) {
		type lineResult struct {
			line string
			err  error
		}
		for {
			fmt.Fprint(stderr, "> ")

			lineCh := make(chan lineResult, 1)
			go func() {
				line, err := reader.ReadString('\n')
				lineCh <- lineResult{line, err}
			}()

			select {
			case <-ctx.Done():
				return "", false
			case res := <-lineCh:
				trimmed := strings.TrimSpace(res.line)
				if trimmed != "" {
					switch strings.ToLower(trimmed) {
					case "/exit", "/quit":
						return "", false
					}
					return trimmed, true
				}
				if res.err != nil {
					return "", false // EOF (or other read error) with nothing left to process
				}
				// Blank line: re-prompt without starting an empty turn.
			}
		}
	}

	onTurnResult := func(turn int, result agent.Result) bool {
		switch result.Outcome {
		case agent.OutcomeSuccess:
			_ = session.SetTurnCount(sess, turn)
			fmt.Fprintln(stdout, result.FinalMessage)
			fmt.Fprintln(stdout, turnSeparator)
			return true
		case agent.OutcomeMaxStepsExceeded:
			_ = session.SetTurnCount(sess, turn)
			fmt.Fprintf(stderr, "litegent: turn %d: max_steps (%d) exceeded without a final response\n", turn, cfg.MaxSteps)
			return true // the session continues; the user can try a different approach
		case agent.OutcomeInterrupted:
			fmt.Fprintln(stderr, "litegent: interrupted")
			return false
		case agent.OutcomeStorageError:
			fmt.Fprintf(stderr, "litegent: turn %d: storage error: %v\n", turn, result.Err)
			return false
		default: // agent.OutcomeLLMError and any unforeseen outcome
			fmt.Fprintf(stderr, "litegent: turn %d: fatal LLM error: %v\n", turn, result.Err)
			return false // a fatal LLM error isn't recoverable mid-session
		}
	}

	result := ag.RunInteractiveWithHistory(ctx, history, startTurn, readPrompt, onTurnResult)
	prog.SessionEnd(result.Outcome.String(), result.Steps)

	exitCode := ExitSuccess
	switch result.Outcome {
	case agent.OutcomeInterrupted:
		exitCode = ExitInterrupted
	case agent.OutcomeLLMError:
		exitCode = ExitLLMAPIError
	case agent.OutcomeStorageError:
		exitCode = ExitConfigOrCLIError
	default: // a session that ends via EOF/"/exit" or after a recoverable max_steps turn is a clean exit
		exitCode = ExitSuccess
	}
	_ = logger.LogRunEnd(result.Outcome.String(), exitCode, result.Steps, result.TotalTokens)
	return exitCode
}

// buildRegistry registers the enabled built-in tools per cfg.Tools. All
// tools are unconfined (req/requirements_doc.md NFR-3.4) — registration
// here only toggles availability, never restricts scope.
func buildRegistry(cfg config.LitegentConfig) *tools.Registry {
	registry := tools.NewRegistry()

	if cfg.Tools.Bash.Enabled {
		timeout := time.Duration(cfg.Tools.Bash.Timeout * float64(time.Second))
		registry.Register(tools.NewBashTool(timeout, cfg.Tools.Bash.DefaultWorkdir))
	}
	if cfg.Tools.FileOps.Enabled {
		registry.Register(tools.NewReadFileTool(cfg.Tools.FileOps.MaxReadLines))
		registry.Register(tools.NewWriteFileTool())
		registry.Register(tools.NewEditFileTool())
	}
	if cfg.Tools.Search.Enabled {
		registry.Register(tools.NewGlobSearchTool(cfg.Tools.Search.MaxSearchResults))
		registry.Register(tools.NewGrepSearchTool(cfg.Tools.Search.MaxSearchResults))
	}

	return registry
}
