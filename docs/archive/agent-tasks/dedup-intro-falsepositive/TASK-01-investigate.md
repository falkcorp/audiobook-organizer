<!-- file: docs/agent-tasks/dedup-intro-falsepositive/TASK-01-investigate.md -->
<!-- version: 1.0.0 -->
<!-- guid: b5c7d9e1-3e5f-4b7c-9d1e-4f6a8b0c2d4e -->
<!-- last-edited: 2026-06-28 -->

# TASK-01 — Investigate & quantify the intro/outro false-positive class

**Priority:** P1 · **Effort:** M · **Recommended subagent:** code-exploration
subagent (read-only) · **Depends on:** none

## ⛔ START HERE (do this first, exactly)

This task is **read-only / analysis** — it produces a short findings doc, not code
changes. Still work in a worktree so the findings doc can be committed:

```bash
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/di-investigate" -b agent/di-investigate origin/main
cd "$REPO/.worktrees/di-investigate"
git rebase origin/main
```

## Goal

Produce a precise picture of the intro/outro false-positive class so TASK-02/03/04
use correct thresholds and lists. Answer, with numbers and exact code paths:

1. **Where** does the exact dedup layer turn a shared file fingerprint into a
   candidate pair? (function + file:line in `internal/dedup/`.)
2. **How many** current candidate pairs are explained by short files? Bucket the
   files participating in exact matches by duration (`<30s`, `30–60s`, `60–120s`,
   `>120s`).
3. **Which titles** recur on the short-clip side (publisher boilerplate to
   blocklist in TASK-03)?
4. **Do paired books differ by ISBN/ASIN?** Sample candidate pairs and report how
   often the two books have different ISBN10/ISBN13/ASIN (supports TASK-04).
5. Recommended thresholds/lists for TASK-02 (duration cutoff), TASK-03 (title
   blocklist), TASK-04 (which id fields to compare).

## How to gather (read-only)

- Map the code path:
  ```bash
  grep -rn "exact\|AcoustID\|chromaprint\|LSH\|DurationSec\|GetDuplicate" internal/dedup/ | head -60
  ```
- If a read-only query/CLI exists for dedup candidates or AcoustID stats, use it
  (e.g. an existing `GetAcoustIDStats` or a dedup candidates endpoint). Do NOT
  mutate anything. Do NOT run anything against production unless the operator
  explicitly tells you to; prefer reading code + existing reports.
- If you cannot get live counts safely, document the EXACT query/endpoint the
  operator should run, and provide expected output shape.

## Deliverable

Write `docs/agent-tasks/dedup-intro-falsepositive/FINDINGS.md` with:
- the exact match-path call chain (file:line),
- the duration histogram (or the precise query to produce it),
- the recurring boilerplate titles,
- the ISBN/ASIN-mismatch rate among sampled pairs,
- concrete recommended values for TASK-02 (cutoff seconds), TASK-03 (title list),
  TASK-04 (id fields + behavior).

Include the standard markdown file header (file/version/guid/last-edited).

## Acceptance criteria

- [ ] `FINDINGS.md` exists with all five answers, each backed by a code path or a query.
- [ ] No source/behaviour changes (analysis only).
- [ ] Recommended thresholds/lists are concrete (numbers + strings), not vague.

## Commit message

```
docs(dedup): investigate intro/outro fingerprint false-positive class

Findings doc quantifying the short-clip dedup false positives: match-path call
chain, file-duration histogram, recurring boilerplate titles, and ISBN/ASIN
mismatch rate among paired books. Feeds the TASK-02/03/04 fixes.

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>
```

## PR + merge

```bash
git push -u origin agent/di-investigate
gh pr create --fill
gh pr merge <number> --rebase
```

## Idempotency / Rollback

If `FINDINGS.md` already exists and answers all five, this is done. Rollback = delete the doc.
