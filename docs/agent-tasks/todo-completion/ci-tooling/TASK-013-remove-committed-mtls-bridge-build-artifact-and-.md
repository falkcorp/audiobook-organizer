<!-- file: docs/agent-tasks/todo-completion/ci-tooling/TASK-013-remove-committed-mtls-bridge-build-artifact-and-.md -->
<!-- version: 1.0.0 -->
<!-- guid: 9d4e2a31-8034-4b41-a5cd-27da16f25ea9 -->
<!-- last-edited: 2026-08-21 -->

# TASK-013 — Remove committed mtls-bridge build artifact and gitignore it (REPO-SIZE-1)

**Priority:** P2 · **Effort:** S · **Recommended subagent:** Haiku-class · ci-tooling subagent · **Why:** git rm + two-line gitignore add + a small size-guard addition to an existing hook script; no ambiguity · **Depends on:** none · **Wave:** 1

Source: `TODO.md` line 10632 as of commit 46628240 (later edits shift lines) — re-find it with `grep -n -F "**REPO-SIZE-1 decision**" TODO.md` (line numbers drift; the grep is built from the line's own text). Scope file: `scope-15.json`.

## ⛔ START HERE (do this first, exactly)

```bash
# ⛔ START HERE — do not touch code before this block succeeds
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/ci-tooling-013-remove-committed-mtls-bridge-build-artifact-and-" -b agent/ci-tooling-013-remove-committed-mtls-bridge-build-artifact-and- origin/main
cd "$REPO/.worktrees/ci-tooling-013-remove-committed-mtls-bridge-build-artifact-and-"
git rebase origin/main
# Go task: do NOT run 'go work init .' — it breaks the build (ambiguous genproto imports)
```

(Protocol also in `docs/agent-tasks/ORCHESTRATION.md` — the inline block above is authoritative for this task.)

## Goal

Stop the compiled mtls-bridge binary (and its Windows twin) from living in git history going forward, per owner-approved Option (d) forward-only hygiene (docs/plans/2026-07-10-repo-size-history-rewrite-plan.md L195-223): remove the tracked binary from HEAD, gitignore it and its Windows counterpart, and add a pre-commit size guard so a rebuilt binary can never be re-added silently.

## Background (verify before editing)

- Option (d) forward-only hygiene was explicitly adopted by the owner (`docs/plans/2026-07-10-repo-size-history-rewrite-plan.md:223` 'Adopt Option (d): forward-only hygiene + GitHub Support gc. Do not rewrite history.') — this scout run's decision #1 restates the same call.
- Step 2 of Option (d)'s GitHub-side steps (`docs/plans/2026-07-10-repo-size-history-rewrite-plan.md:206-207`) literally says 'Remove the live mtls-bridge binary from HEAD; add it + build outputs to .gitignore; enable push protection / a pre-commit size guard.'
- `git ls-files -s mtls-bridge` confirms the binary (9,291,954 bytes, arm64 Mach-O) is still tracked at HEAD.
- `Makefile:580-582` also has `build-mtls-bridge-windows` producing `mtls-bridge.exe` — not currently tracked (`git ls-files | grep mtls-bridge.exe` = 0 hits) but should be gitignored preemptively.
- `scripts/setup-git-hooks.sh` installs a pre-commit hook (`.git/hooks/pre-commit` via `--git-common-dir`) that currently only blocks committing named credential files and secret-pattern diffs — no file-size guard exists (`grep -n size scripts/setup-git-hooks.sh` = 0 hits).

- **Re-verify these anchors before editing** — line numbers drift; a zero-hit grep means STOP and report:
  ```bash
  git ls-files -s mtls-bridge   # 1 hit, mode 100755 — mtls-bridge is a tracked Mach-O binary at repo root
  grep -n 'build-mtls-bridge:' -A2 Makefile   # 1 hit ~L575-577, -o mtls-bridge — Makefile builds this exact file
  grep -n mtls-bridge .gitignore   # 0 hits — .gitignore has no entry for it
  ```

### Reuse — don't invent

- Use `pre-commit hook installer` in `scripts/setup-git-hooks.sh` (verify: `grep -n HOOK_FILE scripts/setup-git-hooks.sh`) — do NOT write a parallel helper.

## Step-by-step

1. Run `git rm --cached mtls-bridge` (do not delete the local file if the owner wants to keep it for local dev; or `git rm mtls-bridge` if it should also vanish from the working tree — prefer `git rm --cached` plus a `chmod`-friendly local rebuild via `make build-mtls-bridge`).
2. In `.gitignore`, add a new stanza (near the existing `build/`/`dist/`/`bin/` block around L57-198) with `/mtls-bridge` and `/mtls-bridge.exe`.
3. In `scripts/setup-git-hooks.sh`, extend the heredoc pre-commit hook (the `cat > "$HOOK_FILE" <<'EOF' ... EOF` block) with a size guard: iterate `git diff --cached --name-only --diff-filter=A` (newly added files), stat each staged blob's size via `git cat-file -s :<path>`, and reject the commit (exit 1) if any single added file exceeds e.g. 5MB, printing the offending path(s) and instructing the author to use Git LFS or externalize it instead.
4. Bump the file-header version on `scripts/setup-git-hooks.sh` (currently 1.2.0) per repo convention.
5. Update `.standards`-style version headers are N/A for `.gitignore` (no header convention for it in this repo — verify with `head -5 .gitignore`).

Then, always:
- Keep the change purely removal — do not touch adjacent code, do not reorder imports beyond the formatter, do not change signatures unless a step above says so explicitly.
- Bump the file header (`version` + `last-edited: 2026-08-21`) on every file you touch; keep existing guids. New files get a fresh guid (`uuidgen | tr A-Z a-z`).
- Add a changelog fragment `changelog.d/20260821_ci-tooling_013.md` (NO file header; format per `changelog.d/README.md`: a `### Fixed|Changed|Added` heading, a `####` title, one paragraph).
- Do NOT edit `TODO.md` — the coordinator closes the source item in one commit per wave (every brief in a wave would otherwise collide on it). In your final report, state the exact `TODO.md` line text to check off. Never add new TODO items directly — use a `todo.d/` fragment (no header).

### Edge-case semantics (conservative defaults — treat unknown as unknown, never as disqualifying)

- A legitimately large file the repo already tracks via Git LFS (`.gitattributes` has `*.m4b`/`*.m4a`/`*.mp3`/`*.flac`/`*.png`/`*.webm` LFS rules) must not be blocked by the new guard — only check files NOT already covered by an LFS filter pattern, or check the staged blob is not an LFS pointer.
- A worktree where `.git/hooks` isn't the shared common-dir hooks path (already handled by the script's `--git-common-dir` usage, per its own header comment) — no new risk introduced.

## Tests

- Manual: `bash scripts/setup-git-hooks.sh && dd if=/dev/zero of=/tmp/big.bin bs=1M count=6 && cp /tmp/big.bin big.bin && git add big.bin && git commit -m test` — expect the hook to reject the commit citing the size guard; then `git reset HEAD big.bin && rm big.bin`.
- scripts/test-git-hooks.sh already exists (`ls scripts/test-git-hooks.sh`) — extend it with a case for the new size guard, verifying both a small file passes and an oversized file is rejected.

Anti-over-suppression test: `N/A — this is a removal/hygiene task, not a filter/guard whose over-suppression would hide real bugs; the size-guard test case is the guard's own correctness check.` — a known-good input still passes with the new guard active.

## How to test

```bash
git diff --check && grep -L 'last-edited: ' $(git diff --name-only origin/main -- '*.md' '*.yml' '*.py' '*.sh') ; echo 'docs/tooling task: header check only'
```
If `make ci` is too slow for iteration, first run `go build ./... && go vet ./<changed-pkg>/... && go test ./<changed-pkg>/... -count=1` (or `npm --prefix web test -- <file>` for web), then the full gate once before reporting done.

## Acceptance criteria

- [ ] `git ls-files mtls-bridge` returns no output (file no longer tracked).
- [ ] `grep -n 'mtls-bridge' .gitignore` returns 2 hits (mtls-bridge and mtls-bridge.exe).
- [ ] `bash scripts/test-git-hooks.sh` passes including the new size-guard case.
- [ ] Anti-over-suppression test: `N/A — this is a removal/hygiene task, not a filter/guard whose over-suppression would hide real bugs; the size-guard test case is the guard's own correctness check.` — a known-good input still passes with the new guard active.
- [ ] Edge cases above hold (nil/empty/unknown never disqualify; a test asserts it where a filter/guard is added).
- [ ] Gate green: `git diff --check && grep -L 'last-edited: ' $(git diff --name-only origin/main -- '*.md' '*.yml' '*.py' '*.sh') ; echo 'docs/tooling task: header check only'` exits 0; `go vet`/lint clean.
- [ ] File headers bumped on every changed file (`grep -n "last-edited: 2026-08-21" <file>` hits for each).
- [ ] Changelog fragment present: `test -f changelog.d/20260821_ci-tooling_013.md`.

## Commit message

```
refactor(ci-tooling): Remove committed mtls-bridge build artifact and gitignore it (REPO-SIZE-1)

<why the change was needed; what it protects; what it deliberately does NOT change>

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>
```

## Done

STOP — report done with exact counts (`COMPLETED: n — ...` / `REMAINING: n — ...` / `BLOCKED: n — ...`); the coordinator owns push/PR/merge. Do NOT run `git push`, `gh pr`, or any merge.

## Idempotency / Rollback

If the removed symbol/file is already ABSENT at HEAD (re-run the re-verify greps above: zero hits = already removed) AND the replacement is present, the removal is already done — run acceptance instead. Rollback = `git revert` the commit to restore the file + its call sites; no data or schema is touched.

## Coordinator notes

Owner decision #1 (2026-08-21) already settled Option (d) vs history rewrite. The GitHub Support gc ticket (step 1 of the 4-step plan) is an ops action for the owner, not code — do not attempt it. Do NOT touch git history or run filter-repo.
