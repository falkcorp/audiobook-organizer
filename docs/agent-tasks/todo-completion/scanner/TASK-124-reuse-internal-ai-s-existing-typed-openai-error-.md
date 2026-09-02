<!-- file: docs/agent-tasks/todo-completion/scanner/TASK-124-reuse-internal-ai-s-existing-typed-openai-error-.md -->
<!-- version: 1.1.0 -->
<!-- guid: 3fd5deec-0a8c-4b8a-a4de-c474c09db312 -->
<!-- last-edited: 2026-09-02 -->

# TASK-124 — Reuse internal/ai's existing typed OpenAI error classification in scanner.isPermanentAIFailure instead of re-parsing error text (TODO.md L4852)

> **Status 2026-09-02:** ✅ DONE — PR #2756 merged 2026-08-23 (1ddaa840d).

**Priority:** P2 · **Effort:** M · **Recommended subagent:** Sonnet-class · scanner subagent · **Why:** Not pure mechanical: requires reasoning about which of ai_failure.go's marker strings become provably redundant once errors.As(*ai.PermanentError) is checked first (400/401/403/404 already covers invalid_api_key/account_deactivated by status code) versus which still need a text fallback (Anthropic-style markers are currently dead code since aiParser is OpenAI-only; Ollama-via-baseURL errors may not always come back as structured *openai.Error and could still need the text fallback) — a judgment call, not a copy-paste. · **Depends on:** none · **Wave:** 1

Source: `TODO.md` line 4852 as of commit 46628240 (later edits shift lines) — re-find it with `grep -n -F "**Give the AI parser typed provider errors.**" TODO.md` (line numbers drift; the grep is built from the line's own text). Scope file: `scope-09.json`.

## ⛔ START HERE (do this first, exactly)

```bash
# ⛔ START HERE — do not touch code before this block succeeds
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/scanner-124-reuse-internal-ai-s-existing-typed-openai-error-" -b agent/scanner-124-reuse-internal-ai-s-existing-typed-openai-error- origin/main
cd "$REPO/.worktrees/scanner-124-reuse-internal-ai-s-existing-typed-openai-error-"
git rebase origin/main
# Go task: do NOT run 'go work init .' — it breaks the build (ambiguous genproto imports)
```

(Protocol also in `docs/agent-tasks/ORCHESTRATION.md` — the inline block above is authoritative for this task.)

## Goal

Change internal/scanner/isPermanentAIFailure to check `errors.As(err, &permErr)` for `*ai.PermanentError` FIRST (reusing the classification internal/ai/retry.go's DoWithRetry already computed via the real openai-go SDK error type), falling back to the existing substring-marker list only for errors that are not an *ai.PermanentError (e.g. errors that never went through DoWithRetry, or a non-structured error from an OpenAI-compatible-but-not-quite endpoint like Ollama's baseURL mode).

## Background (verify before editing)

- isPermanentAIError's switch (internal/ai/retry.go:45-52) currently covers: StatusCode 400/401/403/404 (always permanent) and StatusCode 429 with Code==\"insufficient_quota\" specifically. It does NOT check for 'credit_balance_exhausted' or 'account_deactivated' by Code — those are two of ai_failure.go's marker strings that may or may not map to a status code already in the 400/401/403/404 bucket; this needs verification against real OpenAI API error payloads (or the openai-go SDK's own documented error codes) rather than assumption before deciding to drop them from the fallback list.
- The Anthropic-style markers ('authentication_error', 'permission_error') in ai_failure.go are dead code today: `aiParser` is concretely `*ai.OpenAIParser` (scanner.go:782), never an Anthropic client — but the marker-list fallback is still worth keeping as defense-in-depth for the Ollama-via-OpenAI-baseURL path (scanner.go:798, ai.NewOpenAIParserWithBaseURL), whose errors may not always deserialize into a structured *openai.Error the same way OpenAI's own API does.

- **Re-verify these anchors before editing** — line numbers drift; a zero-hit grep means STOP and report:
  ```bash
  grep -n "strings.Contains\|todo.d/20260816-typed-ai-provider-errors" internal/scanner/ai_failure.go   # 2 hits ~L18, L53 — ai_failure.go still substring-matches, with a comment pointing at a todo fragment
  grep -n "func isPermanentAIError" -A 13 internal/ai/retry.go   # 1 hit ~L40, body uses `var apiErr *openai.Error; errors.As(err, &apiErr)`, switches on apiErr.StatusCode and apiErr.Code — internal/ai already has typed classification via errors.As against *openai.Error
  git log -1 --format=%ai bf110a3a -- internal/ai/retry.go; git log -1 --format=%ai a662ec0c -- internal/scanner/ai_failure.go   # bf110a3a: 2026-07-03; a662ec0c: 2026-08-16 — the typed classification predates ai_failure.go by 6 weeks
  grep -n "type PermanentError struct\|isPermanentAIError(err)" internal/ai/retry.go   # 2 hits ~L22, L84; L84 body is `if isPermanentAIError(err) { return \u0026PermanentError{Err: err} }` — DoWithRetry wraps a detected-permanent error in the exported *ai.PermanentError before it propagates out of ParseBatch
  grep -n "var aiParser \*ai.OpenAIParser" internal/scanner/scanner.go   # 1 hit ~L782 — the scanner's aiParser is concretely *ai.OpenAIParser (an openai-go SDK client), so *openai.Error is the right, reachable type
  find todo.d -iname "*typed-ai-provider*"   # 0 hits in todo.d/ — the fragment this comment points at no longer exists in todo.d/
  grep -n "typed provider errors" TODO.md   # 1 hit in TODO.md at L4852 — the fragment was already folded into TODO.md at this exact item
  ```

### Reuse — don't invent

- Use `ai.PermanentError (exported, wraps the classified error, satisfies errors.As)` in `internal/ai/retry.go` (verify: `grep -n "type PermanentError struct" internal/ai/retry.go`) — do NOT write a parallel helper.
- Use `isPermanentAIError's *openai.Error / errors.As pattern (unexported, but the pattern to mirror or export)` in `internal/ai/retry.go` (verify: `grep -n "func isPermanentAIError" internal/ai/retry.go`) — do NOT write a parallel helper.

## Step-by-step

1. Verify, via the openai-go SDK source or OpenAI's public API error-code documentation, whether 'credit_balance_exhausted' and 'account_deactivated' error responses carry an HTTP status already inside isPermanentAIError's 400/401/403/404 bucket (they almost certainly do — billing/auth errors are 4xx) — if confirmed, they are provably redundant with the typed check and can be dropped from ai_failure.go's marker list; if NOT confirmed, keep them in the text-fallback list.
2. In internal/scanner/ai_failure.go, import \"errors\" and the internal/ai package.
3. Rewrite isPermanentAIFailure: first do `var permErr *ai.PermanentError; if errors.As(err, &permErr) { return true }`, THEN fall through to the existing `for _, marker := range permanentAIFailureMarkers { if strings.Contains(msg, marker) { return true } }` loop as a fallback for errors that are not *ai.PermanentError.
4. Trim permanentAIFailureMarkers down to only the entries NOT provably covered by isPermanentAIError's status-code switch, per step 1's finding — at minimum this removes '401 Unauthorized' and '403 Forbidden' (directly covered by StatusCode 401/403) if step 1 confirms the SDK's *openai.Error.Error() string format doesn't already get caught pre-wrap in some other way.
5. Update the doc comment (currently citing the now-stale todo.d fragment path) to reference internal/ai/retry.go's isPermanentAIError/PermanentError directly instead.
6. Bump both files' version headers if either is touched beyond ai_failure.go (retry.go should not need changes for this item).

Then, always:
- Keep the change purely transform — do not touch adjacent code, do not reorder imports beyond the formatter, do not change signatures unless a step above says so explicitly.
- Bump the file header (`version` + `last-edited: 2026-08-21`) on every file you touch; keep existing guids. New files get a fresh guid (`uuidgen | tr A-Z a-z`).
- Add a changelog fragment `changelog.d/20260821_scanner_124.md` (NO file header; format per `changelog.d/README.md`: a `### Fixed|Changed|Added` heading, a `####` title, one paragraph).
- Do NOT edit `TODO.md` — the coordinator closes the source item in one commit per wave (every brief in a wave would otherwise collide on it). In your final report, state the exact `TODO.md` line text to check off. Never add new TODO items directly — use a `todo.d/` fragment (no header).

### Edge-case semantics (conservative defaults — treat unknown as unknown, never as disqualifying)

- err is nil: isPermanentAIFailure's existing `if err == nil { return false }` guard must stay first, before the errors.As check (errors.As(nil, ...) is safe but the early return is clearer and matches existing style).
- err is a *ai.PermanentError wrapping ANOTHER *ai.PermanentError (double-wrap, shouldn't happen but errors.As unwraps recursively so it is harmless either way) — no special handling needed, errors.As already walks the chain.
- err comes from the Ollama-via-baseURL path and is a plain fmt.Errorf-wrapped string with no *openai.Error anywhere in its chain: falls through to the substring-marker fallback exactly as today — behavior-preserving for that path.

## Tests

- internal/scanner/ai_failure_test.go: add TestIsPermanentAIFailure_TypedPermanentError — wrap a fake error in &ai.PermanentError{Err: errors.New(\"whatever\")} and assert isPermanentAIFailure returns true even when the wrapped message contains none of the substring markers (proves the typed path is checked, not just the text path).
- Existing TestIsPermanentAIFailure-style table tests (grep -n \"func Test\" internal/scanner/ai_failure_test.go) must keep passing for the substring-fallback cases that remain in the marker list after trimming.
- Anti-over-suppression: TestIsPermanentAIFailure_TransientErrorNotFlagged — a plain network-timeout error (not *ai.PermanentError, no marker substring) must still return false, so the phase keeps retrying transient failures instead of aborting on everything.

Anti-over-suppression test: `TestIsPermanentAIFailure_TransientErrorNotFlagged` — a known-good input still passes with the new guard active.

## How to test

```bash
go build ./... && go vet ./... && go test ./internal/scanner/... -count=1
```
Do NOT use `make ci` as the gate: it is red on `main` from 10 pre-existing staticcheck findings unrelated to this task. Run `staticcheck ./<changed-pkg>/...` and fix only findings in files you touched. A failing test in a package you did not change is not yours — report it, do not fix it.

## Acceptance criteria

- [ ] go build ./internal/scanner/... ./internal/ai/... exits 0.
- [ ] go test ./internal/scanner/... -run TestIsPermanentAIFailure -v exits 0 including the new typed-error test.
- [ ] grep -n "errors.As(err, &permErr)" internal/scanner/ai_failure.go returns 1 hit.
- [ ] Anti-over-suppression test: `TestIsPermanentAIFailure_TransientErrorNotFlagged` — a known-good input still passes with the new guard active.
- [ ] Edge cases above hold (nil/empty/unknown never disqualify; a test asserts it where a filter/guard is added).
- [ ] Gate green: `go build ./... && go vet ./... && go test ./internal/scanner/... -count=1` exits 0; `go vet`/lint clean.
- [ ] File headers bumped on every changed file (`grep -n "last-edited: 2026-08-21" <file>` hits for each).
- [ ] Changelog fragment present: `test -f changelog.d/20260821_scanner_124.md`.

## Commit message

```
refactor(scanner): Reuse internal/ai's existing typed OpenAI error classificati (TODO L4852)

<why the change was needed; what it protects; what it deliberately does NOT change>

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>
```

## Done

STOP — report done with exact counts (`COMPLETED: n — ...` / `REMAINING: n — ...` / `BLOCKED: n — ...`); the coordinator owns push/PR/merge. Do NOT run `git push`, `gh pr`, or any merge.

## Idempotency / Rollback

If the symbol already lives at its NEW location and is absent from the old one (re-run the re-verify greps above), the move is already done — run acceptance instead. Rollback = `git revert` the commit; behaviour at the old site is restored.

## Coordinator notes

This is a 'hand-rolled = weaker copy' finding (see this repo's own standing feedback lesson of that name): the correct fix already exists in a sibling file and predates the workaround by 6 weeks; the workaround's own comment even names the right destination (a typed error) without noticing it had already arrived. Flagging per CLAUDE.md's 'Fix It Right: depth' — the minimal patch would be to just add one more substring marker; the correct fix is to consume the classification that already exists.
