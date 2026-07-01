<!-- file: docs/agent-tasks/system-docs/TASK-06-ops-runbooks.md -->
<!-- version: 1.0.0 -->
<!-- guid: f9607182-3849-4f60-9518-364758 -->
<!-- last-edited: 2026-06-28 -->

# TASK-06 — Operations runbooks doc

**Priority:** P3 · **Effort:** M · **Recommended subagent:** documentation
subagent · **Depends on:** TASK-01

## ⛔ START HERE (do this first, exactly)

```bash
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/sd-runbooks" -b agent/sd-runbooks origin/main
cd "$REPO/.worktrees/sd-runbooks"
git rebase origin/main
```

## Goal

Write `docs/system/runbooks.md`: concrete operator runbooks — build/deploy
(`make build`/`make deploy`/`make deploy-debug`), the systemd drop-in pattern,
the transcription `reparse_only` op, dedup drain, backups/restore, and recovery
(memdb warmup, server restart). Include a **deploy flowchart**.

## Gather (verify against code/repo)

- `Makefile` targets (`grep -E '^[a-z-]+:' Makefile`), `deploy/` (service file,
  drop-in), `CLAUDE.md` (deploy + workflow rules), and the maintenance ops:
  ```bash
  grep -E '^[a-zA-Z_-]+:' Makefile | head -40
  grep -rn "reparse_only\|transcribe-book-intros\|auto-purge\|RecompactDigests" internal/plugins/ | head
  ```
- Production facts (host, paths) are in `CLAUDE.md` / repo docs — do NOT invent
  secrets or tokens; reference how to obtain them, never embed them.

## Required content

- Deploy runbook (build → cross-compile → scp → systemd restart) + a **Mermaid flowchart**.
- Reparse-intros runbook: `POST /api/v1/operations/v2 {def_id:"maintenance.transcribe-book-intros", params:{reparse_only:true}}`.
- Backup/restore, dedup drain (with the data-loss caution from TODO.md CONS-10), and memdb-warmup recovery notes.
- A short "known CI noise" note (mock-freshness drift; flaky backup/scan tests).

## Acceptance criteria

- [ ] `docs/system/runbooks.md` with header, ≥4 runbooks, and a deploy Mermaid flowchart.
- [ ] Commands match the Makefile/ops; no secrets embedded.
- [ ] Cross-linked to `architecture.md`; Mermaid renders.

## Commit message

```
docs(system): operations runbooks + deploy flowchart (DOCS-1)

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>
```

## PR + merge

```bash
git push -u origin agent/sd-runbooks && gh pr create --fill && gh pr merge <number> --rebase
```

## Idempotency / Rollback

Exists already → done. Rollback = delete the file.
