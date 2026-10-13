# Git Workflow Conventions

Rules to follow whenever a task involves committing changes to a git
repository.

## Rules
1. Always run `git status` before making changes, and again before
   committing, so you know exactly what you're about to stage.
2. After editing files, run `git diff` and review it — confirm only the
   intended lines changed before staging.
3. Stage specific files by name (`git add <path>`) rather than `git add
   -A` or `git add .`, to avoid accidentally committing unrelated or
   sensitive files.
4. Never commit files that look like they contain secrets (`.env`,
   `credentials.json`, private keys). If `git status` shows one, stop and
   flag it instead of adding it.
5. Write commit messages in the imperative mood, with a short summary
   line (≤50 chars) and a body explaining *why* the change was made.
6. If the project's own documentation specifies required commit trailers
   (for example `Coding-Agent`, `LLM`, `Host`, `Directory`), include them
   exactly as specified. A typical trailer block looks like:
   ```text
   Coding-Agent: <name>
   LLM: <model>
   Host: <hostname>
   Directory: <path>
   ```
7. Never use destructive git commands (`push --force`, `reset --hard`,
   `clean -f`) unless the task explicitly and unambiguously asks for one.
8. After committing, run `git status` once more to confirm a clean tree
   and that nothing unexpected was left staged or untracked.
