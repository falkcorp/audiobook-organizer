<!-- file: docs/agent-tasks/todo-completion/config/TASK-018-fix-ai-backend-local-base-url-hardcoded-develope.md -->
<!-- version: 1.0.0 -->
<!-- guid: 88482b0e-beb9-4cd4-88ad-dd5e6f1774d3 -->
<!-- last-edited: 2026-08-21 -->

# TASK-018 — Fix ai_backend.local_base_url hardcoded developer LAN IP default (CFG-AUDIT)

**Priority:** P2 · **Effort:** S · **Recommended subagent:** Sonnet-class · config subagent · **Why:** Straightforward default-value fix, but needs to check EffectiveLLMMode's fallback behavior doesn't silently break for people relying on the current default resolving locally on THIS owner's LAN (unlikely to be relied upon by anyone else, but verify). · **Depends on:** none · **Wave:** 2

Source: `TODO.md` line 1317 as of commit 46628240 (later edits shift lines) — re-find it with `grep -n -F "**CFG-AUDIT**" TODO.md` (line numbers drift; the grep is built from the line's own text). Scope file: `scope-03.json`.

## ⛔ START HERE (do this first, exactly)

```bash
# ⛔ START HERE — do not touch code before this block succeeds
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/config-018-fix-ai-backend-local-base-url-hardcoded-develope" -b agent/config-018-fix-ai-backend-local-base-url-hardcoded-develope origin/main
cd "$REPO/.worktrees/config-018-fix-ai-backend-local-base-url-hardcoded-develope"
git rebase origin/main
# Go task: do NOT run 'go work init .' — it breaks the build (ambiguous genproto imports)
```

(Protocol also in `docs/agent-tasks/ORCHESTRATION.md` — the inline block above is authoritative for this task.)

## Goal

Change the fresh-install default for ai_backend.local_base_url from the owner's specific LAN IP (192.168.0.20) to empty string, so a fresh install on someone else's network does not silently attempt local-LLM mode against a dead endpoint. Document the previous default's origin (developer's own Ollama host) in a comment instead of shipping it as the default.

## Background (verify before editing)

- config.go:577's mode-selection logic treats ANY non-empty LocalBaseURL as 'use local mode' — there is no separate 'is this actually reachable' check before committing to that mode.

- **Re-verify these anchors before editing** — line numbers drift; a zero-hit grep means STOP and report:
  ```bash
  grep -n 'ai_backend.local_base_url", "http' internal/config/config.go   # 1 hit at L1575 — fresh-install default is a specific developer LAN IP
  grep -n '192.168.0.20' internal/config/config.go   # ≥2 hits, including one inside ResetToDefaults around L2432 — ResetToDefaults repeats the same hardcoded IP
  grep -n 'AIBackend.LocalBaseURL' internal/config/config.go   # hit at L577 inside a mode-selection conditional — non-empty LocalBaseURL affects mode selection
  ```

### Reuse — don't invent

- No existing helper identified; do not invent new constants for concepts that already have a name — grep first.

## Step-by-step

1. internal/config/config.go:1575 — change `viper.SetDefault("ai_backend.local_base_url", "http://192.168.0.20:11434/v1")` to `viper.SetDefault("ai_backend.local_base_url", "")`.
2. internal/config/config.go:~2432 — change the ResetToDefaults() literal `LocalBaseURL: "http://192.168.0.20:11434/v1"` to `LocalBaseURL: ""`.
3. Add a code comment at both sites noting the previous hardcoded value was a specific developer's LAN address and should never be a shipped default again.
4. Read internal/config/config.go:577's EffectiveLLMMode / EffectiveEmbeddingMode logic to confirm an empty LocalBaseURL cleanly falls through to whatever the non-local default mode is (cloud/OpenAI or disabled) rather than erroring.

Then, always:
- Keep the change purely transform — do not touch adjacent code, do not reorder imports beyond the formatter, do not change signatures unless a step above says so explicitly.
- Bump the file header (`version` + `last-edited: 2026-08-21`) on every file you touch; keep existing guids. New files get a fresh guid (`uuidgen | tr A-Z a-z`).
- Add a changelog fragment `changelog.d/20260821_config_018.md` (NO file header; format per `changelog.d/README.md`: a `### Fixed|Changed|Added` heading, a `####` title, one paragraph).
- Do NOT edit `TODO.md` — the coordinator closes the source item in one commit per wave (every brief in a wave would otherwise collide on it). In your final report, state the exact `TODO.md` line text to check off. Never add new TODO items directly — use a `todo.d/` fragment (no header).

### Edge-case semantics (conservative defaults — treat unknown as unknown, never as disqualifying)

- The owner's own deployment currently relies on this default being pre-filled; after this change their config.yaml/env must explicitly set AI_BACKEND_LOCAL_BASE_URL or the persisted db snapshot must already carry the value (check persistence.go for whether it was ever explicitly saved, or only ever came from this default) before deploying, or local-LLM mode will silently stop being selected on next boot.

## Tests

- internal/config/config_test.go — assert fresh-install default for ai_backend.local_base_url is empty string.
- internal/config/*_test.go covering EffectiveLLMMode — add/confirm a case for LocalBaseURL="" resolves to the intended non-local mode.

Anti-over-suppression: N/A

## How to test

```bash
go build ./... && go vet ./... && go test ./internal/config/... -count=1
```
Do NOT use `make ci` as the gate: it is red on `main` from 10 pre-existing staticcheck findings unrelated to this task. Run `staticcheck ./<changed-pkg>/...` and fix only findings in files you touched. A failing test in a package you did not change is not yours — report it, do not fix it.

## Acceptance criteria

- [ ] `grep -n '192.168.0.20' internal/config/config.go` returns 0 hits.
- [ ] Anti-over-suppression: N/A
- [ ] Edge cases above hold (nil/empty/unknown never disqualify; a test asserts it where a filter/guard is added).
- [ ] Gate green: `go build ./... && go vet ./... && go test ./internal/config/... -count=1` exits 0; `go vet`/lint clean.
- [ ] File headers bumped on every changed file (`grep -n "last-edited: 2026-08-21" <file>` hits for each).
- [ ] Changelog fragment present: `test -f changelog.d/20260821_config_018.md`.

## Commit message

```
fix(config): Fix ai_backend.local_base_url hardcoded developer LAN IP def (CFG-AUDIT)

<why the change was needed; what it protects; what it deliberately does NOT change>

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>
```

## Done

STOP — report done with exact counts (`COMPLETED: n — ...` / `REMAINING: n — ...` / `BLOCKED: n — ...`); the coordinator owns push/PR/merge. Do NOT run `git push`, `gh pr`, or any merge.

## Idempotency / Rollback

If the symbol already lives at its NEW location and is absent from the old one (re-run the re-verify greps above), the move is already done — run acceptance instead. Rollback = `git revert` the commit; behaviour at the old site is restored.

## Coordinator notes

Flag this one for prod-deploy coordination per edge_cases above — verify the owner's prod snapshot has an explicit local_base_url before shipping, per 'never flip a config default that prod silently depends on' caution.
