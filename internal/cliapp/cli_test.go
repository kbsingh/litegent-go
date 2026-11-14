package cliapp

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"litegent/internal/llm"
	"litegent/internal/session"
)

func writeFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// assertRunLogs walks root and requires at least one trace and one human
// session log. It works both before sessions exist (logs under top_dir)
// and after they are introduced (logs under top_dir/<session-id>/logs).
func assertRunLogs(t *testing.T, root string) {
	t.Helper()
	var traces, sessions int
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		switch {
		case strings.HasPrefix(d.Name(), "trace_") && strings.HasSuffix(d.Name(), ".jsonl"):
			traces++
		case strings.HasPrefix(d.Name(), "session_") && strings.HasSuffix(d.Name(), ".log"):
			sessions++
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking %s for logs: %v", root, err)
	}
	if traces == 0 || sessions == 0 {
		t.Fatalf("expected trace and session logs under %s, got traces=%d sessions=%d", root, traces, sessions)
	}
}

func findSessionDirs(t *testing.T, root string) []string {
	t.Helper()
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatalf("reading %s: %v", root, err)
	}
	var dirs []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		dir := filepath.Join(root, e.Name())
		if _, err := os.Stat(filepath.Join(dir, "session.json")); err == nil {
			dirs = append(dirs, dir)
		}
	}
	return dirs
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
	writeFile(t, litegentDir, defaultConfigFileName, `{
  "base_url": "`+server.URL+`",
  "model": "test-model"
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
	assertRunLogs(t, home)
}

func TestRun_CreatesDefaultFiles(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Chdir(t.TempDir()) // keep the default "./logs" out of the repo tree

	var stdout, stderr bytes.Buffer
	code := Run([]string{"-i"}, strings.NewReader(""), &stdout, &stderr)
	if code != ExitSuccess {
		t.Fatalf("exit code = %d, want %d, stderr: %s", code, ExitSuccess, stderr.String())
	}

	configPath := filepath.Join(home, defaultDirName, defaultConfigFileName)
	configData, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("expected default config at %s: %v", configPath, err)
	}
	if !strings.Contains(string(configData), `"${OPENAI_API_KEY}"`) {
		t.Errorf("expected generated config to use an API-key placeholder, got: %s", configData)
	}
	if !strings.Contains(stderr.String(), "created default config at "+configPath) {
		t.Errorf("expected config creation notice on stderr, got: %q", stderr.String())
	}

	systemPath := filepath.Join(home, defaultDirName, defaultSystemPromptFileName)
	systemData, err := os.ReadFile(systemPath)
	if err != nil {
		t.Fatalf("expected default system prompt at %s: %v", systemPath, err)
	}
	if string(systemData) != defaultSystemPrompt {
		t.Errorf("default system prompt = %q, want %q", systemData, defaultSystemPrompt)
	}
	if !strings.Contains(stderr.String(), "created default system prompt at "+systemPath) {
		t.Errorf("expected system-prompt creation notice on stderr, got: %q", stderr.String())
	}

	// A second run must reuse both files rather than rewriting them. Use
	// a custom system prompt so an overwrite would be obvious.
	const customPrompt = "custom prompt from the user"
	if err := os.WriteFile(systemPath, []byte(customPrompt), 0o644); err != nil {
		t.Fatal(err)
	}
	stdout.Reset()
	stderr.Reset()
	code = Run([]string{"-i"}, strings.NewReader(""), &stdout, &stderr)
	if code != ExitSuccess {
		t.Fatalf("second run exit code = %d, want %d, stderr: %s", code, ExitSuccess, stderr.String())
	}
	after, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(configData) {
		t.Error("existing default config was overwritten on the second run")
	}
	afterSystem, err := os.ReadFile(systemPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(afterSystem) != customPrompt {
		t.Error("existing default system prompt was overwritten on the second run")
	}
	if strings.Contains(stderr.String(), "created default config") || strings.Contains(stderr.String(), "created default system prompt") {
		t.Errorf("second run must not report creating either default file again, got: %q", stderr.String())
	}
}

func TestRun_DefaultSystemPromptFallback(t *testing.T) {
	// The config lives in a temp directory and omits top_dir, so top_dir
	// defaults to that directory and system.md is created beside it.

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
	cfgPath := writeFile(t, dir, "config.json", `{
  "base_url": "`+server.URL+`",
  "model": "test-model"
}`)

	var stdout, stderr bytes.Buffer
	code := Run([]string{"-p", promptPath, "-c", cfgPath}, strings.NewReader(""), &stdout, &stderr)
	if code != ExitSuccess {
		t.Fatalf("exit code = %d, want %d, stderr: %s", code, ExitSuccess, stderr.String())
	}
	if seenSystem != defaultSystemPrompt {
		t.Errorf("system prompt = %q, want built-in default %q", seenSystem, defaultSystemPrompt)
	}
	if _, err := os.Stat(filepath.Join(dir, defaultSystemPromptFileName)); err != nil {
		t.Errorf("expected default system prompt beside the config at %s: %v", dir, err)
	}
}

func TestRun_ExplicitSystemPromptDoesNotCreateDefault(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := t.TempDir()
	systemPath := writeFile(t, dir, "system.txt", "explicit system prompt")
	cfgPath := writeFile(t, dir, "config.json", `{"model": "gpt-4o"}`)

	var stdout, stderr bytes.Buffer
	code := Run([]string{"-s", systemPath, "-i", "-c", cfgPath}, strings.NewReader(""), &stdout, &stderr)
	if code != ExitSuccess {
		t.Fatalf("exit code = %d, want %d, stderr: %s", code, ExitSuccess, stderr.String())
	}
	for _, defaultPath := range []string{
		filepath.Join(dir, defaultSystemPromptFileName),
		filepath.Join(home, defaultDirName, defaultSystemPromptFileName),
	} {
		if _, err := os.Stat(defaultPath); err == nil {
			t.Errorf("explicit -s must not create %s", defaultPath)
		}
	}
	if strings.Contains(stderr.String(), "created default system prompt") {
		t.Errorf("explicit -s must not report creating a default prompt, got: %q", stderr.String())
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
	cfgPath := writeFile(t, dir, "config.json", `{
  "base_url": "`+server.URL+`",
  "api_key": "test-key",
  "model": "test-model",
  "max_steps": 5
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

	assertRunLogs(t, dir)
}

func TestRun_DefaultSystemPrompt(t *testing.T) {
	// top_dir defaults to the config's directory, so system.md is created
	// beside config.json and contains the built-in prompt.
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
	promptPath := writeFile(t, dir, "prompt.txt", "task")
	cfgPath := writeFile(t, dir, "config.json", `{
  "base_url": "`+server.URL+`",
  "model": "test-model"
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
	cfgPath := writeFile(t, dir, "config.json", `{
  "base_url": "`+server.URL+`",
  "model": "test-model"
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
	cfgPath := writeFile(t, dir, "config.json", `{"model": "gpt-4o"}`)

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
	cfgPath := writeFile(t, dir, "config.json", `{
  "base_url": "`+server.URL+`",
  "model": "test-model"
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
	cfgPath := writeFile(t, dir, "config.json", `{
  "base_url": "`+server.URL+`",
  "model": "test-model"
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
	cfgPath := writeFile(t, dir, "config.json", `{
  "base_url": "`+server.URL+`",
  "model": "test-model"
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
	cfgPath := writeFile(t, dir, "config.json", `{
  "base_url": "`+server.URL+`",
  "model": "test-model"
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

func TestRun_CustomTopDirHonorsConfig(t *testing.T) {
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
	top := filepath.Join(dir, "custom-top")
	promptPath := writeFile(t, dir, "prompt.txt", "task")
	cfgPath := writeFile(t, dir, "config.json", `{
  "top_dir": "`+top+`",
  "base_url": "`+server.URL+`",
  "model": "test-model"
}`)

	var stdout, stderr bytes.Buffer
	code := Run([]string{"-p", promptPath, "-c", cfgPath}, strings.NewReader(""), &stdout, &stderr)
	if code != ExitSuccess {
		t.Fatalf("exit code = %d, want %d, stderr: %s", code, ExitSuccess, stderr.String())
	}
	if seenSystem != defaultSystemPrompt {
		t.Errorf("system prompt = %q, want built-in default %q", seenSystem, defaultSystemPrompt)
	}
	if _, err := os.Stat(filepath.Join(top, defaultSystemPromptFileName)); err != nil {
		t.Errorf("expected system prompt under custom top_dir: %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, defaultDirName, defaultSystemPromptFileName)); err == nil {
		t.Error("custom top_dir must not create ~/.litegent/system.md")
	}
	if _, err := os.Stat(filepath.Join(home, defaultDirName, defaultConfigFileName)); err == nil {
		t.Error("custom top_dir must not create ~/.litegent/config.json")
	}
	assertRunLogs(t, top)
}

func TestRun_CreatesSessionLayout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(llm.ChatResponse{
			Choices: []llm.Choice{{Message: llm.Message{Role: llm.RoleAssistant, Content: "done"}, FinishReason: "stop"}},
		})
	}))
	defer server.Close()

	dir := t.TempDir()
	promptPath := writeFile(t, dir, "prompt.txt", "task")
	cfgPath := writeFile(t, dir, "config.json", `{
  "base_url": "`+server.URL+`",
  "model": "test-model"
}`)

	var stdout, stderr bytes.Buffer
	code := Run([]string{"-p", promptPath, "-c", cfgPath}, strings.NewReader(""), &stdout, &stderr)
	if code != ExitSuccess {
		t.Fatalf("exit code = %d, want %d, stderr: %s", code, ExitSuccess, stderr.String())
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var sessionDir string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if _, err := os.Stat(filepath.Join(dir, e.Name(), "session.json")); err == nil {
			sessionDir = filepath.Join(dir, e.Name())
			break
		}
	}
	if sessionDir == "" {
		t.Fatalf("expected a session directory under %s, entries: %v", dir, entries)
	}
	if !strings.Contains(stderr.String(), "litegent: session "+filepath.Base(sessionDir)) {
		t.Errorf("startup output missing session id, got: %q", stderr.String())
	}
	for _, path := range []string{
		filepath.Join(sessionDir, "session.json"),
		filepath.Join(sessionDir, "context.json"),
		filepath.Join(sessionDir, "logs"),
		filepath.Join(sessionDir, "spool"),
	} {
		if _, err := os.Stat(path); err != nil {
			t.Errorf("expected %s: %v", path, err)
		}
	}
	info := &session.Info{
		ID:          filepath.Base(sessionDir),
		ContextPath: filepath.Join(sessionDir, "context.json"),
	}
	msgs, err := session.LoadContext(info)
	if err != nil {
		t.Fatalf("LoadContext returned error: %v", err)
	}
	if len(msgs) == 0 || msgs[0].Role != llm.RoleSystem {
		t.Fatalf("context does not start with a system message: %+v", msgs)
	}
	last := msgs[len(msgs)-1]
	if last.Role != llm.RoleAssistant || last.Content != "done" {
		t.Errorf("context does not end with the final assistant answer: %+v", last)
	}
}

func TestRun_ResumeRestoresContextAndAppends(t *testing.T) {
	var mu sync.Mutex
	var requests []llm.ChatRequest
	answer := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req llm.ChatRequest
		json.NewDecoder(r.Body).Decode(&req)
		mu.Lock()
		requests = append(requests, req)
		answer++
		content := fmt.Sprintf("answer %d", answer)
		mu.Unlock()
		json.NewEncoder(w).Encode(llm.ChatResponse{
			Choices: []llm.Choice{{Message: llm.Message{Role: llm.RoleAssistant, Content: content}, FinishReason: "stop"}},
		})
	}))
	defer server.Close()

	dir := t.TempDir()
	cfgPath := writeFile(t, dir, "config.json", `{
  "base_url": "`+server.URL+`",
  "model": "test-model"
}`)
	prompt1 := writeFile(t, dir, "prompt1.txt", "first question")
	prompt2 := writeFile(t, dir, "prompt2.txt", "second question")

	var stdout, stderr bytes.Buffer
	if code := Run([]string{"-p", prompt1, "-c", cfgPath}, strings.NewReader(""), &stdout, &stderr); code != ExitSuccess {
		t.Fatalf("first run failed: code=%d stderr=%s", code, stderr.String())
	}
	sessions := findSessionDirs(t, dir)
	if len(sessions) != 1 {
		t.Fatalf("expected 1 session, got %d", len(sessions))
	}
	sessionID := filepath.Base(sessions[0])

	firstRunRequests := len(requests)
	stdout.Reset()
	stderr.Reset()
	code := Run([]string{"-p", prompt2, "-c", cfgPath, "-r", sessionID}, strings.NewReader(""), &stdout, &stderr)
	if code != ExitSuccess {
		t.Fatalf("resume failed: code=%d stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "resumed session "+sessionID) {
		t.Errorf("expected resumed-session notice, got: %q", stderr.String())
	}
	if got := findSessionDirs(t, dir); len(got) != 1 {
		t.Errorf("resume created a new session: %v", got)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(requests) <= firstRunRequests {
		t.Fatalf("resume made no LLM request")
	}
	req := requests[firstRunRequests]
	var sawFirst, sawAnswer, sawSecond bool
	for _, msg := range req.Messages {
		switch msg.Content {
		case "first question":
			sawFirst = true
		case "answer 1":
			sawAnswer = true
		case "second question":
			sawSecond = true
		}
	}
	if !sawFirst || !sawAnswer || !sawSecond {
		t.Errorf("resumed request missing history: %+v", req.Messages)
	}

	info := &session.Info{Dir: sessions[0], ManifestPath: filepath.Join(sessions[0], "session.json")}
	m, err := session.LoadManifest(info.Dir)
	if err != nil {
		t.Fatal(err)
	}
	if m.TurnCount != 2 {
		t.Errorf("TurnCount = %d, want 2", m.TurnCount)
	}
}

func TestRun_ResumeMissingSessionFails(t *testing.T) {
	dir := t.TempDir()
	cfgPath := writeFile(t, dir, "config.json", `{"model": "gpt-4o"}`)
	promptPath := writeFile(t, dir, "prompt.txt", "task")

	var stdout, stderr bytes.Buffer
	code := Run([]string{"-p", promptPath, "-c", cfgPath, "-r", "brave-otter-runs"}, strings.NewReader(""), &stdout, &stderr)
	if code != ExitConfigOrCLIError {
		t.Fatalf("exit code = %d, want %d", code, ExitConfigOrCLIError)
	}
	if !strings.Contains(stderr.String(), "not found") {
		t.Errorf("expected not-found error, got: %q", stderr.String())
	}
	if dirs := findSessionDirs(t, dir); len(dirs) != 0 {
		t.Errorf("failed resume created session directories: %v", dirs)
	}
}

func TestRun_ResumeInvalidIDFails(t *testing.T) {
	dir := t.TempDir()
	cfgPath := writeFile(t, dir, "config.json", `{"model": "gpt-4o"}`)
	promptPath := writeFile(t, dir, "prompt.txt", "task")

	var stdout, stderr bytes.Buffer
	code := Run([]string{"-p", promptPath, "-c", cfgPath, "-r", "../escape"}, strings.NewReader(""), &stdout, &stderr)
	if code != ExitConfigOrCLIError {
		t.Fatalf("exit code = %d, want %d", code, ExitConfigOrCLIError)
	}
	if dirs := findSessionDirs(t, dir); len(dirs) != 0 {
		t.Errorf("invalid resume created session directories: %v", dirs)
	}
}

func TestRun_ResumeConflictsWithResumeLast(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := Run([]string{"-i", "-r", "brave-otter-runs", "--resume-last"}, strings.NewReader(""), &stdout, &stderr)
	if code != ExitConfigOrCLIError {
		t.Fatalf("exit code = %d, want %d", code, ExitConfigOrCLIError)
	}
	if !strings.Contains(stderr.String(), "conflicts with --resume-last") {
		t.Errorf("expected conflict error, got: %q", stderr.String())
	}
}

func TestRun_ResumeLastSelectsMostRecent(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(llm.ChatResponse{
			Choices: []llm.Choice{{Message: llm.Message{Role: llm.RoleAssistant, Content: "ok"}, FinishReason: "stop"}},
		})
	}))
	defer server.Close()

	dir := t.TempDir()
	cfgPath := writeFile(t, dir, "config.json", `{
  "base_url": "`+server.URL+`",
  "model": "test-model"
}`)
	promptPath := writeFile(t, dir, "prompt.txt", "task")

	var stdout, stderr bytes.Buffer
	if code := Run([]string{"-p", promptPath, "-c", cfgPath}, strings.NewReader(""), &stdout, &stderr); code != ExitSuccess {
		t.Fatalf("first run failed: %s", stderr.String())
	}
	first := findSessionDirs(t, dir)
	if len(first) != 1 {
		t.Fatalf("expected 1 session, got %d", len(first))
	}
	stdout.Reset()
	stderr.Reset()
	if code := Run([]string{"-p", promptPath, "-c", cfgPath}, strings.NewReader(""), &stdout, &stderr); code != ExitSuccess {
		t.Fatalf("second run failed: %s", stderr.String())
	}
	all := findSessionDirs(t, dir)
	if len(all) != 2 {
		t.Fatalf("expected 2 sessions, got %d", len(all))
	}
	var newest string
	for _, d := range all {
		if d != first[0] {
			newest = d
		}
	}
	if newest == "" {
		t.Fatal("could not identify the second session")
	}

	// Make the selection deterministic regardless of clock resolution.
	oldInfo := &session.Info{Dir: first[0], ManifestPath: filepath.Join(first[0], "session.json")}
	newInfo := &session.Info{Dir: newest, ManifestPath: filepath.Join(newest, "session.json")}
	if err := session.UpdateManifest(oldInfo, func(m *session.Manifest) {
		m.LastUsedAt = time.Now().UTC().Add(-time.Hour)
	}); err != nil {
		t.Fatal(err)
	}
	if err := session.UpdateManifest(newInfo, func(m *session.Manifest) {
		m.LastUsedAt = time.Now().UTC()
	}); err != nil {
		t.Fatal(err)
	}

	stdout.Reset()
	stderr.Reset()
	code := Run([]string{"-p", promptPath, "-c", cfgPath, "--resume-last"}, strings.NewReader(""), &stdout, &stderr)
	if code != ExitSuccess {
		t.Fatalf("resume-last failed: code=%d stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "resumed session "+filepath.Base(newest)) {
		t.Errorf("resume-last picked the wrong session, stderr: %q", stderr.String())
	}
}

func TestRun_ResumeWarnsOnSystemPromptDrift(t *testing.T) {
	var mu sync.Mutex
	var lastRequest llm.ChatRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req llm.ChatRequest
		json.NewDecoder(r.Body).Decode(&req)
		mu.Lock()
		lastRequest = req
		mu.Unlock()
		json.NewEncoder(w).Encode(llm.ChatResponse{
			Choices: []llm.Choice{{Message: llm.Message{Role: llm.RoleAssistant, Content: "ok"}, FinishReason: "stop"}},
		})
	}))
	defer server.Close()

	dir := t.TempDir()
	writeFile(t, dir, "system.md", "old system prompt")
	cfgPath := writeFile(t, dir, "config.json", `{
  "base_url": "`+server.URL+`",
  "model": "test-model"
}`)
	promptPath := writeFile(t, dir, "prompt.txt", "task")

	var stdout, stderr bytes.Buffer
	if code := Run([]string{"-p", promptPath, "-c", cfgPath}, strings.NewReader(""), &stdout, &stderr); code != ExitSuccess {
		t.Fatalf("first run failed: %s", stderr.String())
	}
	sessions := findSessionDirs(t, dir)
	if len(sessions) != 1 {
		t.Fatalf("expected 1 session, got %d", len(sessions))
	}
	if err := os.WriteFile(filepath.Join(dir, "system.md"), []byte("new system prompt"), 0o644); err != nil {
		t.Fatal(err)
	}

	stdout.Reset()
	stderr.Reset()
	code := Run([]string{"-p", promptPath, "-c", cfgPath, "-r", filepath.Base(sessions[0])}, strings.NewReader(""), &stdout, &stderr)
	if code != ExitSuccess {
		t.Fatalf("resume failed: %s", stderr.String())
	}
	if !strings.Contains(stderr.String(), "system prompt/skills changed") {
		t.Errorf("expected drift warning, got: %q", stderr.String())
	}
	mu.Lock()
	defer mu.Unlock()
	if len(lastRequest.Messages) == 0 || lastRequest.Messages[0].Content != "old system prompt" {
		t.Errorf("resume did not preserve the stored system prompt: %+v", lastRequest.Messages)
	}
}

func TestRun_ResumeInteractiveContinuesHistory(t *testing.T) {
	var mu sync.Mutex
	var requests []llm.ChatRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req llm.ChatRequest
		json.NewDecoder(r.Body).Decode(&req)
		mu.Lock()
		requests = append(requests, req)
		mu.Unlock()
		json.NewEncoder(w).Encode(llm.ChatResponse{
			Choices: []llm.Choice{{Message: llm.Message{Role: llm.RoleAssistant, Content: "ok"}, FinishReason: "stop"}},
		})
	}))
	defer server.Close()

	dir := t.TempDir()
	cfgPath := writeFile(t, dir, "config.json", `{
  "base_url": "`+server.URL+`",
  "model": "test-model"
}`)

	var stdout, stderr bytes.Buffer
	if code := Run([]string{"-i", "-c", cfgPath}, strings.NewReader("first turn\n/exit\n"), &stdout, &stderr); code != ExitSuccess {
		t.Fatalf("first interactive run failed: %s", stderr.String())
	}
	sessions := findSessionDirs(t, dir)
	if len(sessions) != 1 {
		t.Fatalf("expected 1 session, got %d", len(sessions))
	}
	before := len(requests)

	stdout.Reset()
	stderr.Reset()
	code := Run([]string{"-i", "-c", cfgPath, "-r", filepath.Base(sessions[0])}, strings.NewReader("second turn\n/exit\n"), &stdout, &stderr)
	if code != ExitSuccess {
		t.Fatalf("resumed interactive run failed: %s", stderr.String())
	}
	mu.Lock()
	defer mu.Unlock()
	if len(requests) <= before {
		t.Fatal("resumed interactive run made no LLM request")
	}
	req := requests[before]
	var sawFirst, sawSecond bool
	for _, msg := range req.Messages {
		if msg.Role == llm.RoleUser && msg.Content == "first turn" {
			sawFirst = true
		}
		if msg.Role == llm.RoleUser && msg.Content == "second turn" {
			sawSecond = true
		}
	}
	if !sawFirst || !sawSecond {
		t.Errorf("resumed interactive request missing prior or new turn: %+v", req.Messages)
	}
}
