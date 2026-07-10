<!-- file: docs/agent-tasks/torrent-relocation/README.md -->
<!-- version: 1.0.0 -->
<!-- guid: b1c5be05-8f18-49db-8c83-5fc3d70e45fe -->
<!-- last-edited: 2026-07-10 -->

# Workstream — Torrent Hardening + Client-Agnostic Relocation (INIT-5)

Stop asking the torrent client to physically move files: the app moves them, the client only
re-points its recorded storage path. The re-point path is client-agnostic behind the EXISTING
`internal/download.TorrentClient` seam (extended with `UpdateStoragePath`; NOT the vestigial
`internal/plugin.DownloadClient`, which lives in a different plugin framework than the Deluge
plugin), with a tri-state config mode. From INIT-5 of
`.claude/notes/2026-07-10-remaining-work-master-plan.md` and
`docs/specs/2026-07-10-torrent-relocation-design.md`; taskboard:
`docs/plans/2026-07-10-torrent-relocation.md`.

**Gate (verbatim, applies to every task):** SPEC -> EXECUTE with a hard human gate: T2 is a
REAL-DELUGE SPIKE with STOP-FOR-HUMAN sign-off REQUIRED before T3's call-site migration may
start. The new relocation mode is config-gated (tri-state: off / physical-move / re-point-only);
defaults STAY on today's behavior until the T2 spike is human-approved.

The gate's machine-checkable record is the canonical line
`DECISION: APPROVED mechanism <A|B> via AskUserQuestion YYYY-MM-DD` in
`docs/reports/2026-07-torrent-repoint-spike.md` — written only after the human answers the
AskUserQuestion. Never gate on the bare word "Approve" (it appears in option labels and in
rejection records).

| Task | TODO id | Title | Priority | Effort | Tier | Wave |
|------|---------|-------|----------|--------|------|------|
| TASK-01 | INIT-5 T1 | Extend download.TorrentClient with UpdateStoragePath + fail-closed stubs | P1 | S | Sonnet-class | 1 |
| TASK-06 | INIT-5 T6 | Tri-state relocation config + Settings UI + docs | P1 | M | Sonnet-class | 1 |
| TASK-02 | INIT-5 T2 | Real-Deluge re-point spike + UpdateStoragePath impl ⚠ STOP-FOR-HUMAN | P0 | L | Opus/strong-class + operator | 2 |
| TASK-03 | INIT-5 T3 | Route the MoveStorage call sites through mode-aware relocation ⚠ | P1 | M | Opus/strong-class | 3 |
| TASK-04 | INIT-5 T4 | Reconcile the two import impls: make the plugin reflink real | P1 | M | Sonnet-class | 4 |
| TASK-05 | INIT-5 T5 | qBittorrent re-point + Transmission client in internal/download (guarded) | P2 | M | Sonnet-class | 4 |
| TASK-07 | INIT-5 T7 | Close the deferred undo re-point item: mode-matrix cells in existing suites | P2 | S | Sonnet-class | 4 |

## Ground rules

- Go backend (`internal/deluge`, `internal/plugins/deluge`, `internal/download`,
  `internal/maintenance/jobs`, `internal/config`) + one Settings-UI field.
  No acquisition/indexers — out of scope.
- Briefs are MODE=standalone: each task is its own worktree + branch + PR +
  `gh pr merge --rebase`. Never commit to main.
- Build + test gate for every task in this workstream:
  ```bash
  make ci
  ```
  staticcheck is red on main (pre-existing backlog #1796) — scope staticcheck to files you
  changed; the merge gate is Minimal CI green.
- **Verify every file:line anchor with `grep` before editing** — line numbers in each brief are
  a starting point, not a guarantee. Zero hits = STOP and report.
- Preserve the hydrate-before-write block (`internal/plugins/deluge/centralization.go`, grep
  `'Hydrate the full row before writing back'`) and the ProtectedPathCache nil-client guard
  (`internal/deluge/protected_paths.go`, grep `'if c.client == nil'`) in every task that goes
  near those files.
- Re-point-only for non-Deluge client types is HARD-GUARDED (allowlist: `deluge` post-spike,
  spec Decision 7) — no task lifts the guard; that requires a future per-client T2-class
  validation.

## Collision / wave note

Computed from the tasks' Exact-files lists (full matrix in the taskboard plan):

| Shared file | Tasks that touch it | Resolution |
|---|---|---|
| `internal/download/deluge.go` | TASK-01, TASK-02 | serialize: wave1=TASK-01 (stub), wave2=TASK-02 (real impl) |
| `internal/download/qbittorrent.go` | TASK-01, TASK-05 | serialize: wave1=TASK-01 (stub), wave4=TASK-05 (guarded impl) |
| `internal/plugins/deluge/centralization.go` | TASK-03, TASK-04 | serialize: wave3=TASK-03, wave4=TASK-04 (collision-only edge) |
| `internal/deluge/import.go` | TASK-03, TASK-04 | serialize: wave3=TASK-03, wave4=TASK-04 (collision-only edge) |

(`internal/plugins/deluge/import.go`: TASK-03 only — TASK-04 does not touch it (the plugin
package's sole reflink caller is `centralization.go:132`). `internal/config/config.go`: TASK-06
only — TASK-05 adds no config key.)

| Wave | Tasks | Prereq | Parallel-safe because |
|---|---|---|---|
| 1 | TASK-01, TASK-06 | none | disjoint file sets (see collision table) |
| 2 | TASK-02 | wave 1 merged + siblings rebased | single agent; shares `internal/download/deluge.go` with TASK-01 (merged) |
| — | **HUMAN GATE** | TASK-02 spike evidence → real AskUserQuestion sign-off, recorded as `^DECISION: APPROVED` in the spike report | T3 must not start without it |
| 3 | TASK-03 | `^DECISION: APPROVED` + TASK-06 merged | single agent, one PR across its 6 files |
| 4 | TASK-04, TASK-05, TASK-07 | wave 3 merged + siblings rebased | disjoint file sets (see collision table). TASK-05 SKIPPED if the spike rejected re-point |

**Fallback (spike REJECTED):** TASK-03 is BLOCKED; TASK-04 re-parents (branch from
`origin/main` — its T03 edge is file-collision serialization only) and still ships as the
hardening-only fallback; TASK-05 is skipped; TASK-07 shrinks to the closure note +
off/physical-move cells. See the taskboard plan's "Fallback re-parenting" note.

Execution modes (from the taskboard plan): W1/W4 = SERIAL WAVES (coordinator-driven) — 2 resp. 3
non-mechanically-similar tasks (< the ≥3-similar /parallel-sweep trigger), disjoint files allow
concurrent dispatch within the wave; W2/W3 = SINGLE-AGENT (strong model) — live-daemon spike
resp. atomic 6-file semantic change.

The coordinator + worker protocol is embedded verbatim in
[`docs/plans/2026-07-10-torrent-relocation.md`](../../plans/2026-07-10-torrent-relocation.md)
§"Coordinator protocol" — under coordinator dispatch it overrides the briefs' own PR+merge
sections.
