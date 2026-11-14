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
		filepath.Join(sessionDir, "logs"),
		filepath.Join(sessionDir, "spool"),
	} {
		if _, err := os.Stat(path); err != nil {
			t.Errorf("expected %s: %v", path, err)
		}
	}
}
