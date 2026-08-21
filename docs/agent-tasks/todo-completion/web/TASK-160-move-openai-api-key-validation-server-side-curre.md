<!-- file: docs/agent-tasks/todo-completion/web/TASK-160-move-openai-api-key-validation-server-side-curre.md -->
<!-- version: 1.0.0 -->
<!-- guid: b634935c-c9e8-457e-a1a3-929fdde8ac7c -->
<!-- last-edited: 2026-08-21 -->

# TASK-160 — Move OpenAI API key validation server-side (currently sent from the browser) (SEC-9)

**Priority:** P2 · **Effort:** M · **Recommended subagent:** Sonnet-class · web subagent · **Why:** A small, well-scoped new backend endpoint plus a frontend call-site swap — standard proxy-validation pattern, not architecturally tricky, but touches both Go and TS so worth sonnet over haiku for the cross-stack coordination. · **Depends on:** none · **Wave:** 1

Source: `TODO.md` line 376 as of commit 46628240 (later edits shift lines) — re-find it with `grep -n -F "**SEC-9: the OpenAI API key is sent from the brows" TODO.md` (line numbers drift; the grep is built from the line's own text). Scope file: `scope-01.json`.

## ⛔ START HERE (do this first, exactly)

```bash
# ⛔ START HERE — do not touch code before this block succeeds
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/web-160-move-openai-api-key-validation-server-side-curre" -b agent/web-160-move-openai-api-key-validation-server-side-curre origin/main
cd "$REPO/.worktrees/web-160-move-openai-api-key-validation-server-side-curre"
git rebase origin/main
npm ci --prefix web   # frontend task: Playwright/vitest must come from the worktree
```

(Protocol also in `docs/agent-tasks/ORCHESTRATION.md` — the inline block above is authoritative for this task.)

## Goal

Add a POST /api/v1/setup/validate-openai-key (or similarly-named) backend endpoint that receives the user's typed OpenAI key in the request body, calls https://api.openai.com/v1/models server-side with it, and returns {"valid": true/false} — then change WelcomeWizard.tsx to call this new backend endpoint instead of api.openai.com directly, so the raw key never appears in the browser's network log, an extension's request-access hooks, or a corporate TLS-inspecting proxy.

## Background (verify before editing)

- Sibling findings from the same audit (SEC-1, SEC-3, SEC-4, TOOL-2, TOOL-8) are already confirmed fixed per this scope's L397 item, so this codebase does actively work through this audit's backlog — SEC-9 is the one still-live finding from the 2026-08-12 spot-check.
- The user-visible wizard flow must not change — the fix is purely about WHERE the api.openai.com call happens (server vs. browser), not the UX.

- **Re-verify these anchors before editing** — line numbers drift; a zero-hit grep means STOP and report:
  ```bash
  grep -n "api.openai.com/v1/models" web/src/components/wizard/WelcomeWizard.tsx   # 1 hit ~L160 — the browser calls api.openai.com directly with the raw key
  grep -rln 'openai' internal/server/handlers/*.go   # 0 hits, or only unrelated config-plumbing hits — confirm none is a validation ENDPOINT — no server-side validation endpoint exists yet
  ```

### Reuse — don't invent

- Use `existing setup-wizard handler file(s) this new endpoint should live alongside` in `internal/server/handlers/ (search for the wizard/setup handler)` (verify: `grep -rln 'setup\|wizard' internal/server/handlers/*.go | grep -v _test`) — do NOT write a parallel helper.

## Step-by-step

1. Read web/src/components/wizard/WelcomeWizard.tsx around lines 147-160 to see the exact current request/response shape expected by the wizard's validation UI (loading state, success/failure messaging).
2. Add a new Go handler (in whichever file houses the setup/wizard's other backend endpoints — locate via the reuse grep above) that accepts `{"api_key": "..."}` POST body, makes the server-side call to https://api.openai.com/v1/models with `Authorization: Bearer <key>`, and returns `{"valid": bool}` (translate a 401 from OpenAI into valid:false, a network/5xx error into a distinct error response so the wizard can show 'could not verify' vs 'invalid key').
3. Register the new route in whatever file wires up the other setup-wizard routes.
4. In WelcomeWizard.tsx, replace the direct `fetch('https://api.openai.com/v1/models', ...)` call with a call to the new backend endpoint (using this repo's existing API client pattern rather than a raw fetch, if one exists — check for an apiClient/axios wrapper already used elsewhere in web/src).
5. Never log the raw key server-side; ensure the new Go handler does not slog.Info/Debug the key value itself.

Then, always:
- Keep the change purely transform — do not touch adjacent code, do not reorder imports beyond the formatter, do not change signatures unless a step above says so explicitly.
- Bump the file header (`version` + `last-edited: 2026-08-21`) on every file you touch; keep existing guids. New files get a fresh guid (`uuidgen | tr A-Z a-z`).
- Add a changelog fragment `changelog.d/20260821_web_160.md` (NO file header; format per `changelog.d/README.md`: a `### Fixed|Changed|Added` heading, a `####` title, one paragraph).
- Do NOT edit `TODO.md` — the coordinator closes the source item in one commit per wave (every brief in a wave would otherwise collide on it). In your final report, state the exact `TODO.md` line text to check off. Never add new TODO items directly — use a `todo.d/` fragment (no header).

### Edge-case semantics (conservative defaults — treat unknown as unknown, never as disqualifying)

- Rate limiting from OpenAI on repeated validation attempts during setup (user retyping a key several times): the server-side endpoint inherits whatever OpenAI's own rate limit does; no special handling needed beyond passing through OpenAI's error faithfully.
- A key that is syntactically well-formed but revoked: OpenAI's /v1/models call itself already distinguishes this (401), no extra client-side format-checking needed.

## Tests

- Go: internal/server/handlers/<file>_test.go — TestValidateOpenAIKey_ValidKey (mock the OpenAI call via an httptest server, assert valid:true) and TestValidateOpenAIKey_InvalidKey (mock a 401, assert valid:false) and TestValidateOpenAIKey_NetworkError (mock a timeout/5xx, assert a distinguishable error, not silently valid:false).
- Web: web/src/components/wizard/WelcomeWizard.test.tsx (or wherever this repo's wizard tests live) — assert the component now calls the local backend endpoint, not api.openai.com, using a network mock/spy.

Anti-over-suppression: N/A

## How to test

```bash
npm --prefix web run lint && npm --prefix web test
```
Do NOT use `make ci` as the gate: it is red on `main` from 10 pre-existing staticcheck findings unrelated to this task. Run `staticcheck ./<changed-pkg>/...` and fix only findings in files you touched. A failing test in a package you did not change is not yours — report it, do not fix it.

## Acceptance criteria

- [ ] `grep -n 'api.openai.com' web/src/components/wizard/WelcomeWizard.tsx` returns 0 hits.
- [ ] `go test ./internal/server/handlers/... -run ValidateOpenAIKey -v` passes.
- [ ] `npm --prefix web test -- WelcomeWizard` passes.
- [ ] Anti-over-suppression: N/A
- [ ] Edge cases above hold (nil/empty/unknown never disqualify; a test asserts it where a filter/guard is added).
- [ ] Gate green: `npm --prefix web run lint && npm --prefix web test` exits 0; `go vet`/lint clean.
- [ ] File headers bumped on every changed file (`grep -n "last-edited: 2026-08-21" <file>` hits for each).
- [ ] Changelog fragment present: `test -f changelog.d/20260821_web_160.md`.

## Commit message

```
refactor(web): Move OpenAI API key validation server-side (currently sent f (SEC-9)

<why the change was needed; what it protects; what it deliberately does NOT change>

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>
```

## Done

STOP — report done with exact counts (`COMPLETED: n — ...` / `REMAINING: n — ...` / `BLOCKED: n — ...`); the coordinator owns push/PR/merge. Do NOT run `git push`, `gh pr`, or any merge.

## Idempotency / Rollback

If the symbol already lives at its NEW location and is absent from the old one (re-run the re-verify greps above), the move is already done — run acceptance instead. Rollback = `git revert` the commit; behaviour at the old site is restored.

## Coordinator notes

Cross-reference item L397 (todo_line 397) in this scope — SEC-9 is the one still-open finding from that audit's otherwise-mostly-fixed backlog.
