package tools

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"time"
)

// nonInteractiveEnv lists environment overrides layered on top of the
// inherited environment for every bash invocation. They neutralize
// pagers, editors, and confirmation prompts so a command can never block
// the agentic loop waiting on input that will never arrive (see
// req/requirements_doc.md NFR-3.5).
var nonInteractiveEnv = []string{
	"GIT_PAGER=cat",
	"PAGER=cat",
	"GIT_TERMINAL_PROMPT=0",
	"DEBIAN_FRONTEND=noninteractive",
	"TERM=dumb",
}

// BashTool executes shell commands. It is intentionally unconfined: the
// command and workdir may reference any path the host OS permissions
// allow (see req/requirements_doc.md NFR-3.4).
type BashTool struct {
	DefaultTimeout time.Duration
	DefaultWorkdir string
}

// NewBashTool constructs a BashTool with the given defaults.
func NewBashTool(defaultTimeout time.Duration, defaultWorkdir string) *BashTool {
	return &BashTool{DefaultTimeout: defaultTimeout, DefaultWorkdir: defaultWorkdir}
}

func (t *BashTool) Name() string { return "bash" }

func (t *BashTool) Schema() map[string]any {
	return functionSchema("bash",
		"Execute a bash shell command in a subshell. Runs non-interactively (no stdin, pagers disabled) and is not confined to any directory. Returns exit code, stdout, and stderr.",
		map[string]any{
			"command": map[string]any{"type": "string", "description": "The command string to execute."},
			"workdir": map[string]any{"type": "string", "description": "Optional working directory. May be any path on the host."},
			"timeout": map[string]any{"type": "integer", "description": "Timeout in seconds (default: 60)."},
		},
		[]string{"command"},
	)
}

func (t *BashTool) Execute(ctx context.Context, args map[string]any) (*ToolResult, error) {
	command, errRes := requireStringArg(args, "command")
	if errRes != nil {
		return errRes, nil
	}

	workdir := optStringArg(args, "workdir", t.DefaultWorkdir)
	timeoutSecs := optIntArg(args, "timeout", int(t.DefaultTimeout.Seconds()))
	if timeoutSecs <= 0 {
		timeoutSecs = 60
	}
	timeout := time.Duration(timeoutSecs) * time.Second

	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(runCtx, "bash", "-c", command)
	if workdir != "" {
		cmd.Dir = workdir
	}
	// cmd.Stdin left at its zero value (nil): os/exec reads that as
	// /dev/null, so interactive input never blocks the loop.
	cmd.Env = append(os.Environ(), nonInteractiveEnv...)
	// exec.CommandContext already sets cmd.Cancel to kill the process on
	// context cancellation; WaitDelay bounds how long we wait for the
	// subprocess's I/O to drain afterward (CommandContext leaves this at
	// 0 — wait forever — which this overrides).
	cmd.WaitDelay = 2 * time.Second

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	runErr := cmd.Run()
	timedOut := runCtx.Err() == context.DeadlineExceeded

	exitCode := 0
	if runErr != nil {
		if exitErr, ok := runErr.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else if !timedOut {
			// Truly unexpected failure to even start the process (e.g. bash
			// missing). Still return a structured failure, not a Go error.
			return &ToolResult{Success: false, Error: runErr.Error()}, nil
		}
	}

	metadata := map[string]any{
		"exit_code": exitCode,
		"stdout":    stdout.String(),
		"stderr":    stderr.String(),
		"timed_out": timedOut,
	}

	output := stdout.String()
	if stderr.Len() > 0 {
		output += "\n[stderr]\n" + stderr.String()
	}

	if timedOut {
		return &ToolResult{
			Success:  false,
			Output:   output,
			Error:    "command timed out after " + timeout.String(),
			Metadata: metadata,
		}, nil
	}

	return &ToolResult{
		Success:  exitCode == 0,
		Output:   output,
		Metadata: metadata,
	}, nil
}
