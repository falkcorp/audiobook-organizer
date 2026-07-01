<!-- file: docs/agent-tasks/dedup-intro-falsepositive/TASK-02-skip-short-clip-fingerprint.md -->
<!-- version: 1.0.0 -->
<!-- guid: c6d8e0f2-4f60-4c8d-9e2f-5a7b9c1d3e5f -->
<!-- last-edited: 2026-06-28 -->

# TASK-02 — Skip fingerprint compare on short clips (<60s)

**Priority:** P2 · **Effort:** M · **Recommended subagent:** go-backend subagent ·
**Depends on:** TASK-01 (use its recommended cutoff)

## ⛔ START HERE (do this first, exactly)

```bash
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/di-skip-shortclip" -b agent/di-skip-shortclip origin/main
cd "$REPO/.worktrees/di-skip-shortclip"
git rebase origin/main
```

## Goal

Stop the exact dedup layer from treating short audio files as evidence of a
duplicate book. A file under the cutoff (default **60s**, or TASK-01's value) is
almost always a publisher intro/outro, not book content — so its fingerprint must
not produce or strengthen a candidate pair.

## Background (verify before editing)

- The exact-layer match path identified in TASK-01 (function + file:line in
  `internal/dedup/`). Duration is available as
  `BookFile.AcoustIDFingerprintDurationSec` (the memdb-safe proxy) and/or the
  file/book duration. Confirm which field is reliably populated on the match path:
  ```bash
  grep -rn "DurationSec\|Duration\b\|AcoustIDFingerprintDurationSec" internal/dedup/ internal/database/ | head
  ```

## Step-by-step

1. Add a constant near the exact-layer code:
   ```go
   // minFingerprintMatchSeconds: files shorter than this are publisher
   // intro/outro clips, not book content — their fingerprints must not seed or
   // strengthen a duplicate pair. See dedup-intro-falsepositive FINDINGS.md.
   const minFingerprintMatchSeconds = 60
   ```
2. In the exact-layer compare, skip any file whose duration is `> 0` and
   `< minFingerprintMatchSeconds`. Treat unknown/zero duration as NOT-short (don't
   over-skip) — only skip when we positively know it's short.
3. Make sure the skip happens BEFORE the pair is emitted, so short clips never
   create candidates.
4. Log a debug counter of skipped short-clip files per scan.
5. Bump file headers.

## How to test

`internal/dedup/*_test.go`: two books sharing a 20s-fingerprint file produce NO
candidate pair; two books sharing a 2-hour-fingerprint file still pair; a file
with unknown duration is not skipped.

```bash
go build ./...
go test ./internal/dedup/ -count=1
go vet ./internal/dedup/
```

## Acceptance criteria

- [ ] Files with known duration `< 60s` (or TASK-01 cutoff) are excluded from exact-layer matching.
- [ ] Unknown/zero-duration files are NOT skipped (no over-exclusion).
- [ ] Long files still match as before.
- [ ] Tests cover short-skip / long-match / unknown-keep; `go test ./internal/dedup/` green; `go vet` clean.
- [ ] File headers bumped.

## Commit message

```
fix(dedup): skip exact-layer fingerprint match on sub-60s clips

Short publisher intro/outro files share identical chromaprints across unrelated
books and flooded dedup with false positives. Exclude files with a known
duration under 60s from the exact-layer compare; unknown durations are kept.

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>
```

## PR + merge

```bash
git push -u origin agent/di-skip-shortclip
gh pr create --fill
gh pr merge <number> --rebase
```

## Idempotency / Rollback

If a sub-60s skip already exists in the exact layer, this is done. Rollback =
revert the commit (dedup returns to matching all durations).
