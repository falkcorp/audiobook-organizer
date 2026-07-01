<!-- file: docs/agent-tasks/transcription-matching/TASK-03-upgrade-confidence-signal.md -->
<!-- version: 1.0.0 -->
<!-- guid: 5071829b-4e6f-4182-bc3e-0f1a2b3c4d5e -->
<!-- last-edited: 2026-06-28 -->

# TASK-03 — Upgrade-confidence transcription signal

**Priority:** P2 · **Effort:** M · **Recommended subagent:** go-backend subagent ·
**Depends on:** none

## ⛔ START HERE (do this first, exactly)

```bash
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/tm-upgrade-confidence" -b agent/tm-upgrade-confidence origin/main
cd "$REPO/.worktrees/tm-upgrade-confidence"
git rebase origin/main
```

## Goal

The metadata-upgrade job auto-applies a better candidate only when its score
clears a fixed gate (`MinUpgradeConfidence = 0.90`). When the candidate also
**matches the book's transcription exactly**, that is independent corroboration —
so allow a lower score (e.g. 0.85) for transcription-confirmed candidates. This
upgrades more correct books automatically without lowering the bar globally.

## Background (verify line numbers — they drift)

- `internal/metabatch/upgrade.go` — `MetadataUpgradeService.tryUpgradeBook`
  decides whether to upgrade. The constant `MinUpgradeConfidence = 0.90` (~line
  67) gates it; the decision block is ~line 150. Find them:
  ```bash
  grep -n "MinUpgradeConfidence\|func.*tryUpgradeBook\|score\b" internal/metabatch/upgrade.go
  ```
- Reuse `hintsFromBook` + exact-title compare (`util.NormalizeTitle`) and
  `containsCI` from `internal/metafetch/`. If they are unexported, either export a
  small helper or replicate the tiny comparison locally (don't import test code).

## Step-by-step

1. Define a second, lower threshold near the existing constant:
   ```go
   // MinUpgradeConfidenceWithTranscription relaxes the gate when the candidate
   // independently matches the book's audio-derived title/author.
   const MinUpgradeConfidenceWithTranscription = 0.85
   ```
2. In `tryUpgradeBook`, compute whether the candidate matches the transcription
   (exact normalized title; author substring when present), using `hintsFromBook`.
3. Change the gate so the effective threshold is the lower one when transcription
   confirms:
   ```go
   gate := MinUpgradeConfidence
   if transcriptionConfirms {
       gate = MinUpgradeConfidenceWithTranscription
   }
   if score < gate { /* skip upgrade as before */ }
   ```
4. Log which gate was used: `slog.Debug("upgrade gate", "book_id", id, "score", score, "gate", gate, "transcription_confirms", transcriptionConfirms)`.
5. Bump file headers.

## How to test

`internal/metabatch/` test: a candidate with score 0.87 that matches the
transcription IS upgraded; the same score WITHOUT a transcription match is NOT;
a 0.95 candidate is upgraded regardless. Mirror existing upgrade tests.

```bash
go build ./...
go test ./internal/metabatch/ -count=1
go vet ./internal/metabatch/
```

## Acceptance criteria

- [ ] Transcription-confirmed candidates use the 0.85 gate; others keep 0.90.
- [ ] Exact normalized title match required (+ author substring when present).
- [ ] Tests cover 0.87-with-transcription (upgrade), 0.87-without (skip), 0.95 (upgrade).
- [ ] `go test ./internal/metabatch/ -count=1` green; `go vet` clean.
- [ ] File headers bumped.

## Commit message

```
feat(metabatch): relax upgrade gate for transcription-confirmed candidates

A candidate that independently matches the book's audio-derived title/author is
corroborated, so allow a 0.85 upgrade score for it (vs the global 0.90) without
lowering the bar for unconfirmed candidates.

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>
```

## PR + merge

```bash
git push -u origin agent/tm-upgrade-confidence
gh pr create --fill
gh pr merge <number> --rebase
```

## Idempotency

If a transcription-aware gate already exists in `tryUpgradeBook`, this is done.

## Rollback

Revert the commit; the change only affects the upgrade-decision threshold.
