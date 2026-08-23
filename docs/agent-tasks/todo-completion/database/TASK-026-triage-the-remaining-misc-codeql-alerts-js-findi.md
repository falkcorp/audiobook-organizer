<!-- file: docs/agent-tasks/todo-completion/database/TASK-026-triage-the-remaining-misc-codeql-alerts-js-findi.md -->
<!-- version: 2.0.0 -->
<!-- guid: 9d660fdc-9cfe-4c0e-aefd-01e22c186ea2 -->
<!-- last-edited: 2026-08-23 -->

# TASK-026 — Triage the remaining misc CodeQL alerts: JS findings, uncontrolled-allocation-size FP, and the drifted clear-text-logging FP (SEC-CODEQL-BACKLOG)

**Priority:** P2 · **Effort:** M · **Recommended subagent:** Sonnet-class · database subagent · **Why:** A grab-bag of small, mostly-independent findings; each is individually mechanical but re-locating the drifted clear-text-logging alert requires some detective work across the code-scanning API. · **Depends on:** none · **Wave:** 1

Source: `TODO.md` line 2595 as of commit 46628240 (later edits shift lines) — re-find it with `grep -n -F "**SEC-CODEQL-BACKLOG**" TODO.md` (line numbers drift; the grep is built from the line's own text). Scope file: `scope-04.json`.

## ⛔ START HERE (do this first, exactly)

```bash
# ⛔ START HERE — do not touch code before this block succeeds
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/database-026-triage-the-remaining-misc-codeql-alerts-js-findi" -b agent/database-026-triage-the-remaining-misc-codeql-alerts-js-findi origin/main
cd "$REPO/.worktrees/database-026-triage-the-remaining-misc-codeql-alerts-js-findi"
git rebase origin/main
# Go task: do NOT run 'go work init .' — it breaks the build (ambiguous genproto imports)
```

(Protocol also in `docs/agent-tasks/ORCHESTRATION.md` — the inline block above is authoritative for this task.)

> ## ⚠️ REWRITTEN 2026-08-23 — parts (1) and (3) rested on a FALSE premise
>
> **`lgtm[]` suppresses NOTHING in this repo.** It is the legacy LGTM.com
> mechanism, which GitHub code scanning never adopted. Measured twice: on
> PR #2781 the markers were removed and all four affected alerts stayed open
> across the merge (only an API dismissal closed #1429/#1105); and directly on
> 2026-08-23, `internal/audiobooks/service_mutation.go:63` carries
> `// lgtm[go/path-injection]` on that exact line **today** while alert **#1104**
> for that exact `path:line` is still `open`. Reproduce:
> ```bash
> grep -n 'lgtm\[' internal/audiobooks/service_mutation.go
> gh api /repos/falkcorp/audiobook-organizer/code-scanning/alerts/1104 -q '.state'
> ```
>
> Everywhere this brief says "add an `lgtm[...]` comment", the real action is
> **dismiss via the code-scanning API**:
> ```bash
> gh api -X PATCH /repos/falkcorp/audiobook-organizer/code-scanning/alerts/<N> \
>   -f state=dismissed -f dismissed_reason='false positive' \
>   -f dismissed_comment='<why, max 280 bytes>'
> ```
> Then **read the alert back** and confirm `state == "dismissed"` — an accepted
> PATCH is not proof, and a dismissal here has reappeared before (#1094 was
> dismissed and #1105 immediately reappeared for the same sink). Resolve alerts
> by **PATH**, not by a remembered number.
>
> A code comment explaining the reasoning is still worth adding as
> documentation. It is **not** the suppression and must never be reported as
> having handled a finding. Running this brief as originally written would have
> marked findings "handled" while they stayed open — worse than leaving them
> alone, because the next reader would trust the marker.

## Goal

(1) Re-verify that the clamp logic near `out := make([]BookSummary, 0, cap0)` in internal/database/memdb_summaries.go really does bound the allocation (find it BY TEXT — the ~line 154 anchor is stale), then dismiss the go/uncontrolled-allocation-size alert via the code-scanning API citing that clamp. Add a plain code comment recording the reasoning as documentation, but do not mistake the comment for the suppression. (2) Query the GitHub code-scanning API for alert on server.go's original go/clear-text-logging finding to get its current file:line (it has moved since the alert's line-151 snapshot); re-verify the %T-does-not-render-values reasoning still applies at its new location, then either re-affirm the suppression there or dismiss the stale alert in the UI if the flagged code was deleted entirely. (3) Pull the 3 js/remote-property-injection, 2 js/trivial-conditional, and 1 js/insecure-temporary-file alerts from the code-scanning API (their files were not named in the TODO item) and triage each individually — these are lower severity ('high'/'none') and were explicitly deprioritized behind the Go findings in the item's suggested order.

## Background (verify before editing)

- The item explicitly separates 2 already-assessed false positives (server.go clear-text-logging, memdb_summaries.go uncontrolled-allocation-size) from everything else, "so nobody re-derives it" — but the clear-text-logging one has since drifted out of existence at its cited location, so the FP status needs re-anchoring, not re-deriving from scratch.

- **Re-verify these anchors before editing** — line numbers drift; a zero-hit grep means STOP and report:
  ```bash
  grep -n 'cap0 > 4096' internal/database/memdb_summaries.go   # 1 hit at L151 — uncontrolled-allocation-size is clamped (cap0 <= 4096)
  grep -n 'lgtm\|nosec\|nolint' internal/database/memdb_summaries.go   # 0 hits — no suppression comment anywhere in the file — no CodeQL suppression comment (lgtm/nosec/nolint) exists near the allocation
  grep -n '%T.*Store()\|Sprintf(\"%T' internal/server/server.go   # 0 hits — the code has moved or been refactored since the alert was filed — the originally-cited clear-text-logging line no longer exists as described
  grep -rn 'Sprintf(\"%T\"' internal/server/   # hits in server_lifecycle.go:1178 and handlers/system/handler.go:553, neither is 's.Store()' — the %T pattern does still exist elsewhere in the server package
  ```

### Reuse — don't invent

- No existing helper identified; do not invent new constants for concepts that already have a name — grep first.

## Step-by-step

1. Find the allocation in internal/database/memdb_summaries.go BY TEXT (`grep -n 'make(\[\]BookSummary'`), read the clamp logic that is claimed to bound it, and confirm the bound actually holds for an attacker-influenced input — do not accept "a clamp exists" as "the allocation is bounded". Then dismiss the alert via the API per the banner, and read it back.
2. Use `gh api /repos/<org>/<repo>/code-scanning/alerts/662` (or the correct alert number for the clear-text-logging finding) to get its current `most_recent_instance.location` file/line, which will show where CodeQL now sees the flagged pattern (if it still exists) or that the alert auto-closed (if the flagged code was deleted).
3. If the alert is still open at a new location, re-verify the %T-argument reasoning holds there (the dynamic-type-name-only argument does not depend on which function calls it), then dismiss it via the API at its CURRENT location. Add the explanatory comment at the real line, not the stale server.go:360.
4. Pull the JS alert list via the code-scanning API filtered by rule (js/remote-property-injection, js/trivial-conditional, js/insecure-temporary-file) to get their actual files, since the TODO item didn't name them.
5. Triage each JS alert individually — read the flagged code, decide fix vs. suppress, same pattern as the Go findings in parts 1-4 of this todo_line.

Then, always:
- Keep the change purely transform — do not touch adjacent code, do not reorder imports beyond the formatter, do not change signatures unless a step above says so explicitly.
- Bump the file header (`version` + `last-edited: 2026-08-21`) on every file you touch; keep existing guids. New files get a fresh guid (`uuidgen | tr A-Z a-z`).
- Add a changelog fragment `changelog.d/20260821_database_026.md` (NO file header; format per `changelog.d/README.md`: a `### Fixed|Changed|Added` heading, a `####` title, one paragraph).
- Do NOT edit `TODO.md` — the coordinator closes the source item in one commit per wave (every brief in a wave would otherwise collide on it). In your final report, state the exact `TODO.md` line text to check off. Never add new TODO items directly — use a `todo.d/` fragment (no header).

### Edge-case semantics (conservative defaults — treat unknown as unknown, never as disqualifying)

- If the code-scanning API shows the clear-text-logging alert already auto-closed (common when the flagged line is deleted by a refactor), no action is needed beyond confirming closure — do not manually add a suppression for code that no longer exists.

## Tests

- N/A for dismissal-only changes (no behaviour change).
- For any JS finding that gets a real code fix: whatever test framework covers that file (Vitest for web/, or the relevant CI script's own test harness).

Anti-over-suppression: N/A

## How to test

```bash
go build ./... && go vet ./... && go test ./internal/database/... ./internal/server/... ./internal/server/handlers/system/... -count=1
```
Do NOT use `make ci` as the gate: it is red on `main` from 10 pre-existing staticcheck findings unrelated to this task. Run `staticcheck ./<changed-pkg>/...` and fix only findings in files you touched. A failing test in a package you did not change is not yours — report it, do not fix it.

## Acceptance criteria

- [ ] The go/uncontrolled-allocation-size alert reads back as `state == "dismissed"` from the code-scanning API, with the clamp cited in `dismissed_comment`.
- [ ] Zero `lgtm[` markers added: `git diff | grep -c 'lgtm\['` returns 0.
- [ ] Every alert triaged in parts (2) and (3) ends either fixed, dismissed-and-read-back, or explicitly reported as left-open with a reason. None left silent.
- [ ] The clear-text-logging alert (whatever its current alert number/location) is either re-suppressed at its real current location or confirmed auto-closed — not left silently stale at a line that no longer exists.
- [ ] Anti-over-suppression: N/A
- [ ] Edge cases above hold (nil/empty/unknown never disqualify; a test asserts it where a filter/guard is added).
- [ ] Gate green: `go build ./... && go vet ./... && go test ./internal/database/... ./internal/server/... ./internal/server/handlers/system/... -count=1` exits 0; `go vet`/lint clean.
- [ ] File headers bumped on every changed file (`grep -n "last-edited: 2026-08-21" <file>` hits for each).
- [ ] Changelog fragment present: `test -f changelog.d/20260821_database_026.md`.

## Commit message

```
refactor(database): Triage the remaining misc CodeQL alerts: JS findings, uncont (SEC-CODEQL-BACKLOG)

<why the change was needed; what it protects; what it deliberately does NOT change>

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>
```

## Done

STOP — report done with exact counts (`COMPLETED: n — ...` / `REMAINING: n — ...` / `BLOCKED: n — ...`); the coordinator owns push/PR/merge. Do NOT run `git push`, `gh pr`, or any merge.

## Idempotency / Rollback

If the symbol already lives at its NEW location and is absent from the old one (re-run the re-verify greps above), the move is already done — run acceptance instead. Rollback = `git revert` the commit; behaviour at the old site is restored.

## Coordinator notes

Lowest priority sub-part of the CodeQL backlog per the item's own suggested order (criticals, then zipslip/path-injection, then log-injection sweep decision, then re-check the gate) — do this last.
