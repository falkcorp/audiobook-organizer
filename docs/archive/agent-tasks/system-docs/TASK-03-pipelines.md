<!-- file: docs/agent-tasks/system-docs/TASK-03-pipelines.md -->
<!-- version: 1.0.0 -->
<!-- guid: c6374859-0516-4c4d-9285-1031425360ab -->
<!-- last-edited: 2026-06-28 -->

# TASK-03 — Pipelines doc (scan → organize → metadata → dedup → fingerprint)

**Priority:** P3 · **Effort:** M · **Recommended subagent:** documentation
subagent (code-exploration subagent first) · **Depends on:** TASK-01

## ⛔ START HERE (do this first, exactly)

```bash
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/sd-pipelines" -b agent/sd-pipelines origin/main
cd "$REPO/.worktrees/sd-pipelines"
git rebase origin/main
```

## Goal

Write `docs/system/pipelines.md`: the end-to-end data pipelines — scan → metadata
extract → organize → metadata fetch/apply → fingerprint → dedup, plus the
transcription intro pipeline. Include a **sequence diagram** of the scan→apply
flow and a **state-machine diagram** of a book's metadata-review lifecycle
(`nil` → `matched`/`no_match`).

## Gather (verify against code)

- `internal/scanner/`, `internal/metafetch/` (fetch/apply), `internal/dedup/`,
  `internal/fingerprint/`, `internal/plugins/maintenance/intro_transcribe.go`,
  `internal/transcribe/parse.go`.
  ```bash
  grep -rn "func.*Scan\|FetchMetadataForBook\|ApplyMetadataCandidate\|ScanBookDuplicates\|ComputeFingerprint" internal/ | head
  ```

## Required content

- A prose walkthrough of each stage and what it reads/writes.
- A **Mermaid sequence diagram**: import path → scanner → metadata → store → UI.
- A **Mermaid state diagram**: `MetadataReviewStatus` lifecycle.
- The transcription path: 90s clip → Whisper → `ParseAudiobookIntro` → Transcribed* fields → matching boosts (link to the agent-tasks transcription-matching workstream).
- Cross-links to `architecture.md`, `storage.md`.

## Acceptance criteria

- [ ] `docs/system/pipelines.md` with header, stage walkthrough, a sequence diagram, AND a state-machine diagram (2 diagrams).
- [ ] Grounded in code; cross-linked; Mermaid renders.

## Commit message

```
docs(system): pipelines (scan→metadata→dedup→fingerprint) + diagrams (DOCS-1)

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>
```

## PR + merge

```bash
git push -u origin agent/sd-pipelines && gh pr create --fill && gh pr merge <number> --rebase
```

## Idempotency / Rollback

Exists already → done. Rollback = delete the file.
