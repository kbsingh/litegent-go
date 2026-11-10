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

func TestRun_DefaultConfigLocation(t *testing.T) {
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

	home := t.TempDir()
	t.Setenv("HOME", home)
	litegentDir := filepath.Join(home, defaultDirName)
	if err := os.MkdirAll(litegentDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, litegentDir, defaultSystemPromptFileName, "home system prompt")
	logDir := filepath.Join(home, "logs")
	writeFile(t, litegentDir, defaultConfigFileName, `{
  "base_url": "`+server.URL+`",
  "model": "test-model",
  "log_dir": "`+logDir+`"
}`)
	promptPath := writeFile(t, home, "prompt.txt", "task")

	var stdout, stderr bytes.Buffer
	code := Run([]string{"-p", promptPath}, strings.NewReader(""), &stdout, &stderr)
	if code != ExitSuccess {
		t.Fatalf("exit code = %d, want %d, stderr: %s", code, ExitSuccess, stderr.String())
	}
	if seenSystem != "home system prompt" {
		t.Errorf("system prompt = %q, want the ~/.litegent/system.md contents", seenSystem)
	}
	if entries, err := os.ReadDir(logDir); err != nil || len(entries) == 0 {
		t.Errorf("expected logs under the config's log_dir, err=%v entries=%d", err, len(entries))
	}
}

func TestRun_DefaultConfigMissing(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	promptPath := writeFile(t, home, "prompt.txt", "task")

	var stdout, stderr bytes.Buffer
	code := Run([]string{"-p", promptPath}, strings.NewReader(""), &stdout, &stderr)
	if code != ExitConfigOrCLIError {
		t.Fatalf("exit code = %d, want %d", code, ExitConfigOrCLIError)
	}
	wantPath := filepath.Join(home, defaultDirName, defaultConfigFileName)
	if !strings.Contains(stderr.String(), "no default config found") || !strings.Contains(stderr.String(), wantPath) {
		t.Errorf("expected actionable default-config error mentioning %q, got: %q", wantPath, stderr.String())
	}
}

func TestRun_DefaultSystemPromptFallback(t *testing.T) {
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

	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := t.TempDir()
	promptPath := writeFile(t, dir, "prompt.txt", "task")
	logDir := filepath.Join(dir, "logs")
	cfgPath := writeFile(t, dir, "config.json", `{
  "base_url": "`+server.URL+`",
  "model": "test-model",
  "log_dir": "`+logDir+`"
}`)

	var stdout, stderr bytes.Buffer
	code := Run([]string{"-p", promptPath, "-c", cfgPath}, strings.NewReader(""), &stdout, &stderr)
	if code != ExitSuccess {
		t.Fatalf("exit code = %d, want %d, stderr: %s", code, ExitSuccess, stderr.String())
	}
	if seenSystem != defaultSystemPrompt {
		t.Errorf("system prompt = %q, want built-in default %q", seenSystem, defaultSystemPrompt)
	}
}

func TestRun_MissingSystemFile(t *testing.T) {
	dir := t.TempDir()
	promptPath := writeFile(t, dir, "prompt.txt", "do the thing")
	cfgPath := writeFile(t, dir, "config.json", `{"model": "gpt-4o"}`)

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
	cfgPath := writeFile(t, dir, "config.json", `{"max_steps": -5}`)

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
	cfgPath := writeFile(t, dir, "config.json", `{"model": "gpt-4o"}`)

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
	cfgPath := writeFile(t, dir, "config.json", `{
  "base_url": "`+server.URL+`",
  "api_key": "test-key",
  "model": "test-model",
  "max_steps": 5,
  "log_dir": "`+logDir+`"
}`)

	var stdout, stderr bytes.Buffer
	code := Run([]string{"-s", systemPath, "-p", promptPath, "-c", cfgPath}, strings.NewReader(""), &stdout, &stderr)
	if code != ExitSuccess {
		t.Fatalf("exit code = %d, want %d, stderr: %s", code, ExitSuccess, stderr.String())
	}
	if !strings.Contains(stdout.String(), "All done.") {
		t.Errorf("expected final message on stdout, got: %q", stdout.String())
	}
	if !strings.Contains(stdout.String(), "All done.\n----====----\n") {
		t.Errorf("expected turn separator after the final message, got: %q", stdout.String())
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
	cfgPath := writeFile(t, dir, "config.json", `{
  "base_url": "`+server.URL+`",
  "model": "test-model",
  "log_dir": "`+logDir+`"
}`)

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
	cfgPath := writeFile(t, dir, "config.json", `{"model": "gpt-4o"}`)

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
	cfgPath := writeFile(t, dir, "config.json", `{"model": "gpt-4o", "log_dir": "`+logDir+`"}`)

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
	cfgPath := writeFile(t, dir, "config.json", `{
  "base_url": "`+server.URL+`",
  "model": "test-model",
  "log_dir": "`+logDir+`"
}`)

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
	if n := strings.Count(out, "----====----"); n != 2 {
		t.Errorf("expected one separator per printed reply (2), got %d", n)
	}
}

func TestRun_InteractiveToolCallNotices(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req llm.ChatRequest
		json.NewDecoder(r.Body).Decode(&req)
		w.Header().Set("Content-Type", "application/json")
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
							Function: llm.FunctionCall{Name: "bash", Arguments: `{"command":"echo notice-check"}`},
						}},
					},
					FinishReason: "tool_calls",
				}},
			})
			return
		}
		json.NewEncoder(w).Encode(llm.ChatResponse{
			Choices: []llm.Choice{{Message: llm.Message{Role: llm.RoleAssistant, Content: "turn done"}, FinishReason: "stop"}},
		})
	}))
	defer server.Close()

	dir := t.TempDir()
	systemPath := writeFile(t, dir, "system.txt", "you are an agent")
	logDir := filepath.Join(dir, "logs")
	cfgPath := writeFile(t, dir, "config.json", `{
  "base_url": "`+server.URL+`",
  "model": "test-model",
  "log_dir": "`+logDir+`"
}`)

	run := func(verbose bool) (string, string) {
		args := []string{"-s", systemPath, "-i", "-c", cfgPath}
		if verbose {
			args = append(args, "-v")
		}
		var stdout, stderr bytes.Buffer
		code := Run(args, strings.NewReader("do it\n/exit\n"), &stdout, &stderr)
		if code != ExitSuccess {
			t.Fatalf("exit code = %d, stderr: %s", code, stderr.String())
		}
		return stdout.String(), stderr.String()
	}

	stdout, stderr := run(false)
	if !strings.Contains(stderr, "|ToolCall : bash") || !strings.Contains(stderr, "notice-check") {
		t.Errorf("expected compact tool-call notice on stderr, got: %q", stderr)
	}
	if !strings.Contains(stdout, "turn done\n----====----\n") {
		t.Errorf("expected reply followed by turn separator, got: %q", stdout)
	}

	stdout, stderr = run(true)
	if strings.Contains(stderr, "|ToolCall :") {
		t.Errorf("compact notice must be suppressed under -v, got: %q", stderr)
	}
	if !strings.Contains(stderr, "[step 0] tool bash") {
		t.Errorf("verbose tool line missing under -v, got: %q", stderr)
	}
	if !strings.Contains(stdout, "turn done\n----====----\n") {
		t.Errorf("expected reply followed by turn separator under -v, got: %q", stdout)
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
	cfgPath := writeFile(t, dir, "config.json", `{
  "base_url": "`+server.URL+`",
  "model": "test-model",
  "log_dir": "`+logDir+`"
}`)

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
	cfgPath := writeFile(t, dir, "config.json", `{
  "base_url": "`+server.URL+`",
  "model": "test-model",
  "log_dir": "`+logDir+`"
}`)

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
