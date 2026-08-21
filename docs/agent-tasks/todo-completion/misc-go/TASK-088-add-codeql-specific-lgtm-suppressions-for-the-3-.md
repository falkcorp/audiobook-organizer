<!-- file: docs/agent-tasks/todo-completion/misc-go/TASK-088-add-codeql-specific-lgtm-suppressions-for-the-3-.md -->
<!-- version: 1.0.0 -->
<!-- guid: 5d671d77-5251-46b1-a14a-f4b2833f052f -->
<!-- last-edited: 2026-08-21 -->

# TASK-088 — Add CodeQL-specific lgtm suppressions for the 3 already-justified go/disabled-certificate-check findings (SEC-CODEQL-BACKLOG)

**Priority:** P2 · **Effort:** S · **Recommended subagent:** Sonnet-class · misc-go subagent · **Why:** Small, well-understood — add one comment line per site; the design/risk judgment is already done, only the CodeQL-specific suppression syntax is missing. · **Depends on:** none · **Wave:** 1

Source: `TODO.md` line 2595 as of commit 46628240 (later edits shift lines) — re-find it with `grep -n -F "**SEC-CODEQL-BACKLOG**" TODO.md` (line numbers drift; the grep is built from the line's own text). Scope file: `scope-04.json`.

## ⛔ START HERE (do this first, exactly)

```bash
# ⛔ START HERE — do not touch code before this block succeeds
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/misc-go-088-add-codeql-specific-lgtm-suppressions-for-the-3-" -b agent/misc-go-088-add-codeql-specific-lgtm-suppressions-for-the-3- origin/main
cd "$REPO/.worktrees/misc-go-088-add-codeql-specific-lgtm-suppressions-for-the-3-"
git rebase origin/main
# Go task: do NOT run 'go work init .' — it breaks the build (ambiguous genproto imports)
```

(Protocol also in `docs/agent-tasks/ORCHESTRATION.md` — the inline block above is authoritative for this task.)

## Goal

For internal/mtls/provisioning.go:142 (the one that 'matters more' per the item's own framing — it is production code, not a one-off tool) confirm the existing #nosec justification still holds (single bootstrap-only use, never in normal operation), then add `// lgtm[go/disabled-certificate-check]` immediately above the flagged tls.Config line, referencing the existing comment rather than duplicating it. For the two tools/cmd one-offs, add the same lgtm annotation with a one-line note that these are operator-run migration tools, not server code.

## Background (verify before editing)

- Distinguish the 'matters more' production site (internal/mtls/provisioning.go:142) from the two low-stakes one-off tool sites (tools/cmd/merge-split-books, tools/cmd/reconcile-paths) — the item explicitly ranks them differently.

- **Re-verify these anchors before editing** — line numbers drift; a zero-hit grep means STOP and report:
  ```bash
  sed -n '138,143p' internal/mtls/provisioning.go   # '#nosec G402 -- bootstrap-only: InsecureSkipVerify is required during initial mTLS cert provisioning...' — provisioning.go's InsecureSkipVerify already has a detailed justification comment
  grep -n 'InsecureSkipVerify' tools/cmd/merge-split-books/main.go tools/cmd/reconcile-paths/main.go   # 2 hits, both with '//nolint:gosec' — both tools/cmd one-offs already have nolint suppressions
  ```

### Reuse — don't invent

- No existing helper identified; do not invent new constants for concepts that already have a name — grep first.

## Step-by-step

1. Open internal/mtls/provisioning.go and read the full #nosec comment at line ~140-141 to confirm it is still accurate (still bootstrap-only, still single-use).
2. Add `// lgtm[go/disabled-certificate-check]` on the line immediately above `&tls.Config{InsecureSkipVerify: true, ...}`.
3. Repeat for tools/cmd/merge-split-books/main.go:93 and tools/cmd/reconcile-paths/main.go:117, adding a one-line comment noting operator-run tool context if none already exists nearby.

Then, always:
- Keep the change purely transform — do not touch adjacent code, do not reorder imports beyond the formatter, do not change signatures unless a step above says so explicitly.
- Bump the file header (`version` + `last-edited: 2026-08-21`) on every file you touch; keep existing guids. New files get a fresh guid (`uuidgen | tr A-Z a-z`).
- Add a changelog fragment `changelog.d/20260821_misc-go_088.md` (NO file header; format per `changelog.d/README.md`: a `### Fixed|Changed|Added` heading, a `####` title, one paragraph).
- Do NOT edit `TODO.md` — the coordinator closes the source item in one commit per wave (every brief in a wave would otherwise collide on it). In your final report, state the exact `TODO.md` line text to check off. Never add new TODO items directly — use a `todo.d/` fragment (no header).

### Edge-case semantics (conservative defaults — treat unknown as unknown, never as disqualifying)

- If re-reading provisioning.go's context reveals the bootstrap-only claim no longer holds (e.g. the code path is now reachable outside first-install), do NOT add the suppression — escalate as a real finding instead.

## Tests

- N/A — comment-only change, no behavior change.

Anti-over-suppression: N/A

## How to test

```bash
make ci
```
If `make ci` is too slow for iteration, first run `go build ./... && go vet ./<changed-pkg>/... && go test ./<changed-pkg>/... -count=1` (or `npm --prefix web test -- <file>` for web), then the full gate once before reporting done.

## Acceptance criteria

- [ ] grep -c 'lgtm\[go/disabled-certificate-check\]' across the 3 files returns 3.
- [ ] Anti-over-suppression: N/A
- [ ] Edge cases above hold (nil/empty/unknown never disqualify; a test asserts it where a filter/guard is added).
- [ ] Gate green: `make ci` exits 0; `go vet`/lint clean.
- [ ] File headers bumped on every changed file (`grep -n "last-edited: 2026-08-21" <file>` hits for each).
- [ ] Changelog fragment present: `test -f changelog.d/20260821_misc-go_088.md`.

## Commit message

```
refactor(misc-go): Add CodeQL-specific lgtm suppressions for the 3 already-just (SEC-CODEQL-BACKLOG)

<why the change was needed; what it protects; what it deliberately does NOT change>

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>
```

## Done

STOP — report done with exact counts (`COMPLETED: n — ...` / `REMAINING: n — ...` / `BLOCKED: n — ...`); the coordinator owns push/PR/merge. Do NOT run `git push`, `gh pr`, or any merge.

## Idempotency / Rollback

If the symbol already lives at its NEW location and is absent from the old one (re-run the re-verify greps above), the move is already done — run acceptance instead. Rollback = `git revert` the commit; behaviour at the old site is restored.

## Coordinator notes

This assumes CodeQL alert-dismissal happens via in-code lgtm comments in this repo's workflow; if the repo instead dismisses via the GitHub code-scanning UI, the equivalent action is to dismiss alerts #<id> there with 'used in code, but not exploitable' and the same justification text — confirm which mechanism this repo's CodeQL workflow actually honors before proceeding.
