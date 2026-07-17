<!-- file: docs/agent-tasks/ci-flaky-fixes/TASK-01-mockery-pin.md -->
<!-- version: 1.0.1 -->
<!-- guid: 225e79a8-f304-4648-b595-65bfb87e51d2 -->
<!-- last-edited: 2026-07-03 -->

# TASK-01 — Resolve mockery v2/v3 pin drift; regenerate + commit scoped mocks (mock-freshness)

**✅ DONE** (#1718, 2026-07-01) — This task has been completed. Mockery pinned to v3.7.1 (CI + Makefile + setup script); v2 could not generate the merged-file `.mockery.yaml`. Below is the completed task documentation for reference.

**Priority:** P1 · **Effort:** M · **Recommended subagent:** Sonnet · go-backend subagent · **Depends on:** none — but run this task ALONE or FIRST in the wave (see workstream README collision note); TASK-02/TASK-03 should not run concurrently with this one because it touches mocks repo-wide.

## ⛔ START HERE (do this first, exactly)

```bash
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/cf-mockery-pin" -b agent/cf-mockery-pin origin/main
cd "$REPO/.worktrees/cf-mockery-pin"
git rebase origin/main
```

## Goal

The "Mock Freshness" CI check (job `mocks-check` in `.github/workflows/ci.yml`)
fails on every branch because the mockery version developers run locally
drifts from the version CI pins, producing spurious diffs (`interface{}` →
`any` and similar formatting churn) on every regen. Pick ONE canonical
mockery version, pin it everywhere (CI already has an opinion — match it),
regenerate mocks with that exact version, and commit ONLY the resulting
intended diff.

## Background (verify before editing)

- CI installs and uses a specific pinned version. Confirm it is still current:
  ```bash
  grep -n "mockery" .github/workflows/ci.yml
  ```
  As of this writing that is `go install github.com/vektra/mockery/v3@v3.7.1`
  (see `.github/workflows/ci.yml` around line 75), used to run `mockery` (bare,
  reading `.mockery.yaml`) in the `mocks-check` job.
- The local dev setup script installs a DIFFERENT, unpinned version — this is
  the actual source of the drift:
  ```bash
  grep -n "mockery" scripts/setup-mockery.sh
  ```
  It runs `go install github.com/vektra/mockery/v3@latest`. `@latest` today
  resolves to a mockery v3.x binary (confirmed via `mockery version` on a
  machine with Homebrew's `mockery` — reports `v3.7.1`), which changes output
  formatting (e.g. `interface{}` → `any`) vs the CI-pinned v3.7.1. Anyone who
  runs `make mocks` locally with `@latest` or a Homebrew-installed mockery
  regenerates ALL mocks repo-wide with drifted formatting.
- The Makefile targets that (re)generate/check mocks just invoke whatever
  `mockery` is first on `$PATH` — they do NOT pin a version themselves:
  ```bash
  grep -n "mockery" Makefile
  ```
  (see `mocks:` around line 188 and `mocks-check:` around line 197).
- `.mockery.yaml` (repo root) lists every interface that must be regenerated;
  its header comment says "Mockery configuration (v3)" — re-check whether that
  comment is accurate for the version you pin, and correct it if it is
  misleading (CI pins the `v2` module path at v3.7.1, which uses the modern
  package-based config format some docs informally call "v3-style config";
  don't confuse the CONFIG FORMAT with the MODULE MAJOR VERSION).
- Decide the canonical version: match CI (`v3.7.1`) — CI is the source of
  truth for "what counts as fresh" in the `mocks-check` gate, so local tooling
  must produce byte-identical output to it. Do NOT bump CI to whatever
  Homebrew ships; pin everything to CI's version.

## Step-by-step

1. Install the exact CI-pinned mockery version into a location that won't
   collide with any system/Homebrew `mockery` on `$PATH`:
   ```bash
   go install github.com/vektra/mockery/v3@v3.7.1
   ```
   This installs to `$(go env GOPATH)/bin/mockery`. Confirm `$(go env GOPATH)/bin`
   comes before `/opt/homebrew/bin` (or wherever a stray `mockery` lives) in
   your `$PATH` for this session, e.g.:
   ```bash
   export PATH="$(go env GOPATH)/bin:$PATH"
   which mockery
   ```
2. Fix `scripts/setup-mockery.sh` so it installs the SAME pinned version
   instead of `@latest`:
   ```bash
   grep -n "@latest" scripts/setup-mockery.sh
   ```
   Change `go install github.com/vektra/mockery/v3@latest` to
   `go install github.com/vektra/mockery/v3@v3.7.1`. Also update any
   `mockery --version` sanity-check text/comments in that script to reference
   v3.7.1 explicitly so it's obvious what "correct" looks like.
3. (Optional but recommended) Make the Makefile targets self-documenting about
   the required version — add a comment above `mocks:` and `mocks-check:` in
   `Makefile` noting the pinned version (`v3.7.1`) and pointing at
   `scripts/setup-mockery.sh` for installation, so `make mocks` failing with a
   mismatched binary is easy to diagnose. Do not change the targets'
   behavior (they should keep calling bare `mockery`), just document the pin.
4. With the pinned binary confirmed on `$PATH` (step 1), regenerate mocks:
   ```bash
   mockery
   ```
5. Inspect the diff BEFORE staging anything:
   ```bash
   git status --porcelain -- internal/*/mocks/ internal/ai/mock_*_test.go internal/metadata/mock_*_test.go
   git diff --stat -- internal/*/mocks/ internal/ai/mock_*_test.go internal/metadata/mock_*_test.go
   ```
   Expected outcome: with the correctly pinned v3.7.1 binary, the diff should
   be EMPTY (the committed mocks were already generated with this version) OR
   a small, genuinely-intended diff if an interface actually changed upstream.
   If you see a large diff (hundreds of lines, `interface{}`→`any` churn,
   whitespace-only changes across many files), you are almost certainly still
   running the wrong mockery binary — STOP, re-check `which mockery` and your
   `$PATH`, and re-run from step 1. Never commit a large unscoped diff just to
   make the check pass.
6. Also regenerate and check the `go generate`-based database mock target used
   by `check-mock-fresh`:
   ```bash
   go generate ./internal/database/...
   git status --porcelain -- internal/database/mocks/
   ```
   Same rule: this should be empty or a small intended diff.
7. If any interfaces genuinely changed shape since the mocks were last
   committed (a real, intended diff — not version-drift noise), hand-verify
   each changed mock file compiles and matches the current interface:
   ```bash
   go build ./...
   ```
8. Stage ONLY the mock files (and `scripts/setup-mockery.sh` / `Makefile` /
   `.mockery.yaml` edits from steps 2-3) — do not `git add -A`.
9. Bump the file-header version/`last-edited` on every file you touched
   (`.mockery.yaml`, `scripts/setup-mockery.sh`, `Makefile` if edited). Mock
   files themselves are generated and typically don't carry the manual header
   convention — check the top of `internal/database/mocks/mock_store.go` to
   confirm before adding one; if it already has a mockery-generated header,
   leave that format alone.

## How to test

```bash
export PATH="$(go env GOPATH)/bin:$PATH"
which mockery   # confirm it resolves to the v3.7.1 install, not Homebrew
make mocks-check
make check-mock-fresh 2>/dev/null || (go generate ./internal/database/... && git diff --exit-code internal/database/mocks/)
go build ./...
go vet ./...
```
All three must pass with a clean (or intentionally-committed) diff.

## Acceptance criteria

- [ ] `scripts/setup-mockery.sh` installs the exact CI-pinned mockery version
      (`v3.7.1`), not `@latest`.
- [ ] `make mocks-check` passes locally when run with the pinned binary.
- [ ] `make check-mock-fresh` (or the equivalent `go generate` + `git diff
      --exit-code internal/database/mocks/`) passes locally.
- [ ] The committed mock diff (if any) is scoped to genuinely-changed
      interfaces — no repo-wide `interface{}`→`any` or formatting-only churn.
- [ ] `go build ./...` and `go vet ./...` pass.
- [ ] File headers bumped on every non-generated file touched.

## Commit message

```
fix(ci): pin mockery to v3.7.1 everywhere, resolve mock-freshness drift (mock-freshness)

scripts/setup-mockery.sh was installing @latest (mockery v3.x), which formats
generated mocks differently than the v3.7.1 CI pins in ci.yml. Pin the local
install script to the same version and commit a scoped mock regen so the
Mock Freshness gate is green with either toolchain.

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>
```

## PR + merge

```bash
git push -u origin agent/cf-mockery-pin
gh pr create --fill
gh pr merge <number> --rebase
```

## Idempotency / Rollback

Idempotency check: run `which mockery` (pointing at v3.7.1) then `mockery`
from repo root — if `git diff` is empty, the pin is already correct and this
task is done. Rollback = revert the commit; the previous (broken) pin will
resume failing `mocks-check` as before, which is safe to revert into.
