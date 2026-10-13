package cliapp

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"litegent/internal/llm"
)

func writeFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestRun_Version(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := Run([]string{"--version"}, strings.NewReader(""), &stdout, &stderr)
	if code != ExitSuccess {
		t.Fatalf("exit code = %d, want %d", code, ExitSuccess)
	}
	if !strings.Contains(stdout.String(), "litegent version") {
		t.Errorf("expected version string, got: %q", stdout.String())
	}
}

func TestRun_Help(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := Run([]string{"--help"}, strings.NewReader(""), &stdout, &stderr)
	if code != ExitSuccess {
		t.Fatalf("exit code = %d, want %d", code, ExitSuccess)
	}
	if !strings.Contains(stdout.String(), "Usage:") {
		t.Errorf("expected usage text, got: %q", stdout.String())
	}
}

func TestRun_MissingRequiredFlags(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := Run([]string{}, strings.NewReader(""), &stdout, &stderr)
	if code != ExitConfigOrCLIError {
		t.Fatalf("exit code = %d, want %d", code, ExitConfigOrCLIError)
	}
	if !strings.Contains(stderr.String(), "missing required flag") {
		t.Errorf("expected missing-flag error, got: %q", stderr.String())
	}
}

func TestRun_MissingSystemFile(t *testing.T) {
	dir := t.TempDir()
	promptPath := writeFile(t, dir, "prompt.txt", "do the thing")
	cfgPath := writeFile(t, dir, "config.yaml", "model: gpt-4o\n")

	var stdout, stderr bytes.Buffer
	code := Run([]string{
		"-s", filepath.Join(dir, "does-not-exist.txt"),
		"-p", promptPath,
		"-c", cfgPath,
	}, strings.NewReader(""), &stdout, &stderr)
	if code != ExitConfigOrCLIError {
		t.Fatalf("exit code = %d, want %d", code, ExitConfigOrCLIError)
	}
}

func TestRun_InvalidConfig(t *testing.T) {
	dir := t.TempDir()
	systemPath := writeFile(t, dir, "system.txt", "you are an agent")
	promptPath := writeFile(t, dir, "prompt.txt", "do the thing")
	cfgPath := writeFile(t, dir, "config.yaml", "max_steps: -5\n")

	var stdout, stderr bytes.Buffer
	code := Run([]string{"-s", systemPath, "-p", promptPath, "-c", cfgPath}, strings.NewReader(""), &stdout, &stderr)
	if code != ExitConfigOrCLIError {
		t.Fatalf("exit code = %d, want %d", code, ExitConfigOrCLIError)
	}
}

func TestRun_MissingSkillsDir(t *testing.T) {
	dir := t.TempDir()
	systemPath := writeFile(t, dir, "system.txt", "you are an agent")
	promptPath := writeFile(t, dir, "prompt.txt", "do the thing")
	cfgPath := writeFile(t, dir, "config.yaml", "model: gpt-4o\n")

	var stdout, stderr bytes.Buffer
	code := Run([]string{
		"-s", systemPath, "-p", promptPath, "-c", cfgPath,
		"-k", filepath.Join(dir, "no-such-skills-dir"),
	}, strings.NewReader(""), &stdout, &stderr)
	if code != ExitConfigOrCLIError {
		t.Fatalf("exit code = %d, want %d", code, ExitConfigOrCLIError)
	}
}

func TestRun_EndToEndSuccess(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req llm.ChatRequest
		json.NewDecoder(r.Body).Decode(&req)

		w.Header().Set("Content-Type", "application/json")
		// First call: request a bash tool call. Second call: finish.
		hasToolResult := false
		for _, m := range req.Messages {
			if m.Role == llm.RoleTool {
				hasToolResult = true
			}
		}
		if !hasToolResult {
			json.NewEncoder(w).Encode(llm.ChatResponse{
				Choices: []llm.Choice{{
					Message: llm.Message{
						Role: llm.RoleAssistant,
						ToolCalls: []llm.ToolCall{{
							ID: "call_1", Type: "function",
							Function: llm.FunctionCall{Name: "bash", Arguments: `{"command":"echo hello-from-litegent"}`},
						}},
					},
					FinishReason: "tool_calls",
				}},
			})
			return
		}
		json.NewEncoder(w).Encode(llm.ChatResponse{
			Choices: []llm.Choice{{
				Message:      llm.Message{Role: llm.RoleAssistant, Content: "All done."},
				FinishReason: "stop",
			}},
		})
	}))
	defer server.Close()

	dir := t.TempDir()
	systemPath := writeFile(t, dir, "system.txt", "you are a helpful agent")
	promptPath := writeFile(t, dir, "prompt.txt", "say hello")
	logDir := filepath.Join(dir, "logs")
	cfgPath := writeFile(t, dir, "config.yaml", `
base_url: "`+server.URL+`"
api_key: "test-key"
model: "test-model"
max_steps: 5
log_dir: "`+logDir+`"
`)

	var stdout, stderr bytes.Buffer
	code := Run([]string{"-s", systemPath, "-p", promptPath, "-c", cfgPath}, strings.NewReader(""), &stdout, &stderr)
	if code != ExitSuccess {
		t.Fatalf("exit code = %d, want %d, stderr: %s", code, ExitSuccess, stderr.String())
	}
	if !strings.Contains(stdout.String(), "All done.") {
		t.Errorf("expected final message on stdout, got: %q", stdout.String())
	}

	entries, err := os.ReadDir(logDir)
	if err != nil {
		t.Fatalf("expected log directory to be created: %v", err)
	}
	if len(entries) < 2 {
		t.Errorf("expected at least a trace and session log file, got %d entries", len(entries))
	}
}

func TestRun_SkillsInjectedIntoSystemPrompt(t *testing.T) {
	var seenSystem string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req llm.ChatRequest
		json.NewDecoder(r.Body).Decode(&req)
		if len(req.Messages) > 0 {
			seenSystem = req.Messages[0].Content
		}
		json.NewEncoder(w).Encode(llm.ChatResponse{
			Choices: []llm.Choice{{Message: llm.Message{Role: llm.RoleAssistant, Content: "done"}, FinishReason: "stop"}},
		})
	}))
	defer server.Close()

	dir := t.TempDir()
	systemPath := writeFile(t, dir, "system.txt", "base system prompt")
	promptPath := writeFile(t, dir, "prompt.txt", "task")
	skillsDir := filepath.Join(dir, "skills")
	os.MkdirAll(skillsDir, 0o755)
	writeFile(t, skillsDir, "git.md", "# Git Workflow\nAlways check status first.\n")
	logDir := filepath.Join(dir, "logs")
	cfgPath := writeFile(t, dir, "config.yaml", `
base_url: "`+server.URL+`"
model: "test-model"
log_dir: "`+logDir+`"
`)

	var stdout, stderr bytes.Buffer
	code := Run([]string{"-s", systemPath, "-p", promptPath, "-c", cfgPath, "-k", skillsDir}, strings.NewReader(""), &stdout, &stderr)
	if code != ExitSuccess {
		t.Fatalf("exit code = %d, want %d, stderr: %s", code, ExitSuccess, stderr.String())
	}
	if !strings.Contains(seenSystem, "base system prompt") || !strings.Contains(seenSystem, "Git Workflow") {
		t.Errorf("expected system prompt to include both base text and injected skill, got: %q", seenSystem)
	}
}

func TestRun_InteractiveConflictsWithPrompt(t *testing.T) {
	dir := t.TempDir()
	systemPath := writeFile(t, dir, "system.txt", "you are an agent")
	promptPath := writeFile(t, dir, "prompt.txt", "do the thing")
	cfgPath := writeFile(t, dir, "config.yaml", "model: gpt-4o\n")

	var stdout, stderr bytes.Buffer
	code := Run([]string{"-s", systemPath, "-p", promptPath, "-c", cfgPath, "-i"}, strings.NewReader(""), &stdout, &stderr)
	if code != ExitConfigOrCLIError {
		t.Fatalf("exit code = %d, want %d", code, ExitConfigOrCLIError)
	}
	if !strings.Contains(stderr.String(), "conflicts with -p") {
		t.Errorf("expected conflict error, got: %q", stderr.String())
	}
}

func TestRun_InteractiveDoesNotRequirePromptFile(t *testing.T) {
	dir := t.TempDir()
	systemPath := writeFile(t, dir, "system.txt", "you are an agent")
	logDir := filepath.Join(dir, "logs")
	cfgPath := writeFile(t, dir, "config.yaml", "model: gpt-4o\nlog_dir: \""+logDir+"\"\n")

	var stdout, stderr bytes.Buffer
	// No LLM endpoint reachable, but stdin is empty (immediate EOF), so
	// the session should end cleanly before ever calling the LLM.
	code := Run([]string{"-s", systemPath, "-i", "-c", cfgPath}, strings.NewReader(""), &stdout, &stderr)
	if code != ExitSuccess {
		t.Fatalf("exit code = %d, want %d, stderr: %s", code, ExitSuccess, stderr.String())
	}
}

func TestRun_InteractiveMultipleTurns(t *testing.T) {
	var turnCount int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req llm.ChatRequest
		json.NewDecoder(r.Body).Decode(&req)
		var userTurns int
		for _, m := range req.Messages {
			if m.Role == llm.RoleUser {
				userTurns++
			}
		}
		turnCount = userTurns
		json.NewEncoder(w).Encode(llm.ChatResponse{
			Choices: []llm.Choice{{
				Message:      llm.Message{Role: llm.RoleAssistant, Content: fmt.Sprintf("reply to turn %d", userTurns)},
				FinishReason: "stop",
			}},
		})
	}))
	defer server.Close()

	dir := t.TempDir()
	systemPath := writeFile(t, dir, "system.txt", "you are an agent")
	logDir := filepath.Join(dir, "logs")
	cfgPath := writeFile(t, dir, "config.yaml", `
base_url: "`+server.URL+`"
model: "test-model"
log_dir: "`+logDir+`"
`)

	stdin := strings.NewReader("first question\nsecond question\n/exit\n")
	var stdout, stderr bytes.Buffer
	code := Run([]string{"-s", systemPath, "-i", "-c", cfgPath}, stdin, &stdout, &stderr)
	if code != ExitSuccess {
		t.Fatalf("exit code = %d, want %d, stderr: %s", code, ExitSuccess, stderr.String())
	}
	if turnCount != 2 {
		t.Errorf("expected 2 user turns to reach the server, got %d", turnCount)
	}
	out := stdout.String()
	if !strings.Contains(out, "reply to turn 1") || !strings.Contains(out, "reply to turn 2") {
		t.Errorf("expected both turn replies on stdout, got: %q", out)
	}
}

func TestRun_InteractiveEOFEndsSessionCleanly(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(llm.ChatResponse{
			Choices: []llm.Choice{{Message: llm.Message{Role: llm.RoleAssistant, Content: "ok"}, FinishReason: "stop"}},
		})
	}))
	defer server.Close()

	dir := t.TempDir()
	systemPath := writeFile(t, dir, "system.txt", "you are an agent")
	logDir := filepath.Join(dir, "logs")
	cfgPath := writeFile(t, dir, "config.yaml", `
base_url: "`+server.URL+`"
model: "test-model"
log_dir: "`+logDir+`"
`)

	// No trailing /exit — just EOF after one line.
	stdin := strings.NewReader("only question")
	var stdout, stderr bytes.Buffer
	code := Run([]string{"-s", systemPath, "-i", "-c", cfgPath}, stdin, &stdout, &stderr)
	if code != ExitSuccess {
		t.Fatalf("exit code = %d, want %d, stderr: %s", code, ExitSuccess, stderr.String())
	}
	if !strings.Contains(stdout.String(), "ok") {
		t.Errorf("expected the single turn's reply on stdout, got: %q", stdout.String())
	}
}

func TestRun_InteractiveVerboseProgress(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(llm.ChatResponse{
			Choices: []llm.Choice{{Message: llm.Message{Role: llm.RoleAssistant, Content: "ok"}, FinishReason: "stop"}},
		})
	}))
	defer server.Close()

	dir := t.TempDir()
	systemPath := writeFile(t, dir, "system.txt", "you are an agent")
	logDir := filepath.Join(dir, "logs")
	cfgPath := writeFile(t, dir, "config.yaml", `
base_url: "`+server.URL+`"
model: "test-model"
log_dir: "`+logDir+`"
`)

	stdin := strings.NewReader("hello\n/exit\n")
	var stdout, stderr bytes.Buffer
	code := Run([]string{"-s", systemPath, "-i", "-c", cfgPath, "-v"}, stdin, &stdout, &stderr)
	if code != ExitSuccess {
		t.Fatalf("exit code = %d, want %d, stderr: %s", code, ExitSuccess, stderr.String())
	}
	errOut := stderr.String()
	if !strings.Contains(errOut, "litegent: starting (interactive)") {
		t.Errorf("expected interactive startup progress line, got: %q", errOut)
	}
	if !strings.Contains(errOut, "[turn 1] starting") {
		t.Errorf("expected turn-start progress line, got: %q", errOut)
	}
	if !strings.Contains(errOut, "session ended") {
		t.Errorf("expected session-end progress line, got: %q", errOut)
	}
}
