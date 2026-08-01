<!-- file: .claude/notes/2026-07-31-fable-session-log.md -->
<!-- version: 1.0.0 -->
<!-- guid: 4c9e1f7a-2b8d-4e06-9a3f-1d5c8e7b2a90 -->
<!-- last-edited: 2026-07-31 -->

# Fable ultracode session log — 2026-07-31 (run-to-reset)

Survival-protocol log. Append-only, committed on branch
`chore/fable-session-log-2026-07-31` and pushed with every discrete piece of work.
If this session vanishes mid-task, this file is the recovery point.

Prompt: `.claude/notes/2026-07-31-fable-ultracode-prompt.md` (v3). Phases:
0 = CF Access SSO for iOS app + 2 security fixes (MANDATORY), 1 = merge data-loss
matrix, 2 = playlists + dynamic playlists, 3 = metadata capture/use, 4 = known bugs.

## 21:47 — Session start

- Main clean at `f41e0f59`. 20 stale worktrees confirmed (Phase 4 #6 real).
- Created this log branch/worktree (`.worktrees/fable-session-log`).
- Next: Phase 0 — verify origin-side CF Access code (read-only), then live probe
  of `books.jdfalk.com`, then the two security fixes (bind loopback, rotate
  ABS_JWT_SECRET).

## Status

COMPLETED: 0 —
REMAINING: 5 phases — P0 (SSO + security), P1 (merge matrix), P2 (playlists), P3 (metadata), P4 (known bugs)
BLOCKED: 0 —
