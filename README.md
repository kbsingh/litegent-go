# Litegent: Autonomous Linux CLI AI Agent

**⚠️ DANGER — NO GUARDRAILS: Litegent gives the connected LLM complete, unrestricted access to this machine.** There is no sandbox, no path jail, no permission prompts, and no confirmation step — any command the model decides to run, and any file it decides to read, write, or delete, it simply does, with the full privileges of the user running Litegent. Only run this with a model and prompt you trust, on a machine/account whose blast radius you've explicitly accepted.

> **Litegent** is a lightweight, autonomous command-line AI agent written in Go for Linux environments. It executes an agentic loop — single-task or as a persistent interactive session — against any OpenAI-compatible LLM endpoint, equipped with built-in developer tools, robust context management, skills injection, live progress reporting, and comprehensive dual-format interaction logging. It is intentionally **unconfined**: there is no sandbox or path jail, by design.

---

## What Litegent Does (In Brief)

Litegent takes a system prompt and a configuration file, plus either a one-shot task prompt or an interactive session flag, and an optional skills directory:
1. **`-s <file>`**: System prompt defining the agent's role, behavioral principles, and operating constraints. (Always required.)
2. **`-p <file>`**: Task prompt for a single, non-interactive run. Required unless `-i` is set.
3. **`-c <file>`**: Configuration file (JSON or YAML) defining model parameters, timeouts, logging settings, and context controls. (Always required.)
4. **`-i`** *(optional)*: Run a persistent, multi-turn interactive session instead — task prompts are read one line at a time from stdin until `/exit`, `/quit`, or EOF. Conflicts with `-p`.
5. **`-k <dir>`** *(optional)*: Skills directory containing Markdown files with domain runbooks, procedures, and guidelines, injected into the system prompt.
6. **`-v`** *(optional)*: Stream live, human-facing progress to stderr as the loop runs, in addition to the files below.

When launched, Litegent:
- Ingests the system prompt and augments it with any discovered Markdown skills.
- Enters an **agentic loop** that invokes the LLM, parses requested tool calls, executes native tools directly on the host system, and feeds outputs back to the model — either once (single-shot) or repeatedly across turns with persistent conversation history (interactive, `-i`).
- Automatically protects LLM token budgets via **`manage_context`** (output truncation with disk spooling, sliding-window history pruning, and token-budget-gated conversation summarization).
- Operates with **zero external MCP servers** by relying on built-in native tools:
  - `bash`: Linux command execution with timeout, non-interactive stdin, and pager/prompt neutralization.
  - `read_file`, `write_file`, `edit_file`: Exact, surgical file manipulation.
  - `glob_search`, `grep_search`: Fast filesystem pattern and content searches.
- Guards against getting stuck: a repeated-tool-call loop guard injects a corrective notice if the same call keeps producing the same result.
- Runs **unconfined**: none of the above tools are restricted to the invocation directory or any project root — they can read, write, and execute anywhere the host OS permissions allow. The dual audit log (below) is the accountability mechanism in place of a sandbox.
- Continuously maintains a **dual audit log**:
  - `trace_<timestamp>.jsonl`: Machine-readable, append-only trace of every API request, response, tool call, and tool result.
  - `session_<timestamp>.log`: Formatted human-readable narrative transcript of the agent's thoughts, actions, and outputs.
- Concludes autonomously when the task (or interactive session) is resolved, or a safety limit (`max_steps`, per turn) is reached.

Ships as a single statically-linked Go binary (`./install.sh`) with no runtime dependencies; `./run.sh` is a quick `go build`-then-run dev convenience wrapper.

---

## Documentation Quick Links

Complete technical documentation is located under `req/`:
- **[Requirements Document](req/requirements_doc.md)** — functional and non-functional requirements.
- **[Technical Specifications](req/spec_doc.md)** — architecture, module-by-module implementation detail, data flow.
- **[Deliverables Document](req/deliverable_doc.md)** — file-by-file inventory and acceptance criteria.
- **[Agent Guide (Engineering & Git Standards)](req/agent_guide.md)** — guidelines for AI and human developers.
- **[User Guide](req/user_guide.md)** — installation, CLI reference, configuration, recipes, FAQ.
- **[Milestones Implementation Plan](req/milestones_doc.md)** — the build order, reproducible from scratch.

These documents are kept complete and current enough to rebuild Litegent from scratch without reading the source first.
