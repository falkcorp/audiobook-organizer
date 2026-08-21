<!-- file: docs/agent-tasks/todo-completion/ci-tooling/TASK-010-pin-sha256-checksums-for-dockerfile-fetched-utfc.md -->
<!-- version: 1.0.0 -->
<!-- guid: f0881295-210e-4132-bd94-5560868a697a -->
<!-- last-edited: 2026-08-21 -->

# TASK-010 — Pin SHA256 checksums for Dockerfile-fetched utfcpp/taglib tarballs (SEC-8)

**Priority:** P2 · **Effort:** S · **Recommended subagent:** Haiku-class · ci-tooling subagent · **Why:** mechanical: download once, record the known-good sha256, add a verification step — no design decision needed since base images are already pinned by the project's own convention · **Depends on:** none · **Wave:** 1

Source: `TODO.md` line 4206 as of commit 46628240 (later edits shift lines) — re-find it with `grep -n -F "**SEC-8 residue**" TODO.md` (line numbers drift; the grep is built from the line's own text). Scope file: `scope-07.json`.

## ⛔ START HERE (do this first, exactly)

```bash
# ⛔ START HERE — do not touch code before this block succeeds
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/ci-tooling-010-pin-sha256-checksums-for-dockerfile-fetched-utfc" -b agent/ci-tooling-010-pin-sha256-checksums-for-dockerfile-fetched-utfc origin/main
cd "$REPO/.worktrees/ci-tooling-010-pin-sha256-checksums-for-dockerfile-fetched-utfc"
git rebase origin/main
# Go task: do NOT run 'go work init .' — it breaks the build (ambiguous genproto imports)
```

(Protocol also in `docs/agent-tasks/ORCHESTRATION.md` — the inline block above is authoritative for this task.)

## Goal

Add SHA256 verification for both fetched tarballs (utfcpp v4.0.6, taglib v2.0.2) in the Dockerfile so a compromised/tampered upstream release fails the build loudly instead of silently compiling into the image.

## Background (verify before editing)

- The base images in this Dockerfile are already pinned by digest/tag per the TODO item's own note ('base images are pinned') — only these two dependency tarballs are unverified.

- **Re-verify these anchors before editing** — line numbers drift; a zero-hit grep means STOP and report:
  ```bash
  grep -n "curl -sL https://github.com/nemtrif/utfcpp" Dockerfile   # 1 hit at L39 — utfcpp is fetched via curl|tar with no checksum step
  grep -n "curl -sL https://github.com/taglib/taglib" Dockerfile   # 1 hit at L41 — taglib is fetched via curl|tar with no checksum step
  ```

### Reuse — don't invent

- No existing helper identified; do not invent new constants for concepts that already have a name — grep first.

## Step-by-step

1. Download https://github.com/nemtrif/utfcpp/archive/refs/tags/v4.0.6.tar.gz and https://github.com/taglib/taglib/releases/download/v2.0.2/taglib-2.0.2.tar.gz locally, compute `sha256sum` for each, and record the two hex digests.
2. Rewrite Dockerfile lines 39 and 41 from `curl -sL <url> | tar xz` to a two-step form: `curl -sL -o utfcpp.tar.gz <url> && echo "<sha256>  utfcpp.tar.gz" | sha256sum -c - && tar xzf utfcpp.tar.gz` (and the equivalent for taglib), removing the intermediate .tar.gz files afterward alongside the existing `rm -rf /tmp/taglib-build` cleanup at line 64.
3. Bump the file-header version comment at the top of the Dockerfile if one exists (check first few lines) per this repo's mandatory file-header rule.

Then, always:
- Keep the change purely additive — do not touch adjacent code, do not reorder imports beyond the formatter, do not change signatures unless a step above says so explicitly.
- Bump the file header (`version` + `last-edited: 2026-08-21`) on every file you touch; keep existing guids. New files get a fresh guid (`uuidgen | tr A-Z a-z`).
- Add a changelog fragment `changelog.d/20260821_ci-tooling_010.md` (NO file header; format per `changelog.d/README.md`: a `### Fixed|Changed|Added` heading, a `####` title, one paragraph).
- Do NOT edit `TODO.md` — the coordinator closes the source item in one commit per wave (every brief in a wave would otherwise collide on it). In your final report, state the exact `TODO.md` line text to check off. Never add new TODO items directly — use a `todo.d/` fragment (no header).

### Edge-case semantics (conservative defaults — treat unknown as unknown, never as disqualifying)

- If either upstream release is ever re-tagged/re-uploaded with different bytes (rare but has happened to GitHub release/archive tarballs), the pinned hash correctly fails the build — that is the intended behavior, not a bug to work around.

## Tests

- make build (or the Docker build step in CI) — the build must still succeed with the correct hashes, and a deliberately-wrong hash (manual local test only, not committed) must fail the build.

Anti-over-suppression: N/A

## How to test

```bash
git diff --check && grep -L 'last-edited: ' $(git diff --name-only origin/main -- '*.md' '*.yml' '*.py' '*.sh') ; echo 'docs/tooling task: header check only'
```
If `make ci` is too slow for iteration, first run `go build ./... && go vet ./<changed-pkg>/... && go test ./<changed-pkg>/... -count=1` (or `npm --prefix web test -- <file>` for web), then the full gate once before reporting done.

## Acceptance criteria

- [ ] docker build . (or make's equivalent target) completes successfully with the new checksum steps in place; grep -n "sha256sum -c" Dockerfile returns 2 hits.
- [ ] Anti-over-suppression: N/A
- [ ] Edge cases above hold (nil/empty/unknown never disqualify; a test asserts it where a filter/guard is added).
- [ ] Gate green: `git diff --check && grep -L 'last-edited: ' $(git diff --name-only origin/main -- '*.md' '*.yml' '*.py' '*.sh') ; echo 'docs/tooling task: header check only'` exits 0; `go vet`/lint clean.
- [ ] File headers bumped on every changed file (`grep -n "last-edited: 2026-08-21" <file>` hits for each).
- [ ] Changelog fragment present: `test -f changelog.d/20260821_ci-tooling_010.md`.

## Commit message

```
feat(ci-tooling): Pin SHA256 checksums for Dockerfile-fetched utfcpp/taglib ta (SEC-8)

<why the change was needed; what it protects; what it deliberately does NOT change>

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>
```

## Done

STOP — report done with exact counts (`COMPLETED: n — ...` / `REMAINING: n — ...` / `BLOCKED: n — ...`); the coordinator owns push/PR/merge. Do NOT run `git push`, `gh pr`, or any merge.

## Idempotency / Rollback

If the first acceptance check below already passes at HEAD (`docker build . (or make's equivalent target) completes successfully with the new checksum steps in place; grep -n "sha256sum -c" Dockerfile returns 2 hits.`), this task is already applied — run the acceptance checks instead of re-applying. Rollback = `git revert` the single commit; pre-existing behaviour is untouched (purely additive change).

## Coordinator notes

(none)
