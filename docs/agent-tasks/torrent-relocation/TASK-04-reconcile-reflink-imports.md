<!-- file: docs/agent-tasks/torrent-relocation/TASK-04-reconcile-reflink-imports.md -->
<!-- version: 1.0.0 -->
<!-- guid: 51d13dc3-9be8-4d31-b074-cffd700d0081 -->
<!-- last-edited: 2026-07-10 -->

# TASK-04 — Reconcile the two import impls: make the plugin reflink real (INIT-5 T4)

**Gate:** SPEC -> EXECUTE with a hard human gate: T2 is a REAL-DELUGE SPIKE with STOP-FOR-HUMAN sign-off REQUIRED before T3's call-site migration may start. The new relocation mode is config-gated (tri-state: off / physical-move / re-point-only); defaults STAY on today's behavior until the T2 spike is human-approved.
**File-ownership:** none cross-initiative. Within INIT-5: shares `internal/plugins/deluge/centralization.go` and `internal/deluge/import.go` with TASK-03 — TASK-03 must be merged first (wave3=TASK-03, wave4=TASK-04). (`internal/plugins/deluge/import.go` is NOT in this task's scope — the plugin package's only `reflinkCopy` caller is `centralization.go:132`.) Runs parallel to TASK-05/TASK-07 (disjoint files).

**Priority:** P1 · **Effort:** M · **Recommended subagent:** Sonnet-class · code-consolidation subagent · **Why:** cross-package export/move of platform-specific code with build tags — mechanical but needs judgment on the sharing seam; failures caught by the gate · **Depends on:** TASK-03 — **file-collision serialization ONLY**, not semantics. **Fallback re-parenting:** if the TASK-02 spike REJECTS re-point (`DECISION: REJECTED` in the spike report) and TASK-03 is therefore BLOCKED, this task re-parents: branch from `origin/main` and proceed — it is the core of the spec's hardening-only fallback and must stay reachable.

## ⛔ START HERE (do this first, exactly)

```bash
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/torrent-relocation-reconcile-reflink-imports" -b agent/torrent-relocation-reconcile-reflink-imports origin/main
cd "$REPO/.worktrees/torrent-relocation-reconcile-reflink-imports"
git rebase origin/main
```

## Goal

Remove the always-fail `reflinkCopy` stub in the plugin package and route it to the ONE real,
platform-specific reflink implementation that already exists in the legacy package
(`reflinkCopyOS`), so the plugin centralization path reflinks for real instead of always falling
back to copy. REUSE the existing implementation — do not write new reflink syscall code.

## Background (verify before editing)

- The stub: `internal/plugins/deluge/centralization.go:199-203` —
  `func reflinkCopy(src, dest string) error { ... return fmt.Errorf("reflink not available") }`
  (placeholder that forces fallback).
- The real implementation: `reflinkCopyOS` at `internal/deluge/import_unix.go:23` (with a
  Windows twin in `internal/deluge/import_windows.go` — both are build-tagged; check tags with
  the greps below). It is currently package-private to `internal/deluge`.
- Sharing seam (choose the smallest change): export it as `deluge.ReflinkCopyOS` (rename +
  update the legacy package's internal callers — find them:
  `grep -rn 'reflinkCopyOS' internal/deluge/`), then have the plugin's `reflinkCopy` delegate to
  it. Do NOT copy the syscall body into the plugin package (that recreates the twin-drift this
  task removes).
- **Preserve verbatim:** the hydrate-before-write block
  (`internal/plugins/deluge/centralization.go:141-157`) — full row via `GetBookFileByID` before
  `UpdateBookFile` (memdb-slim footgun). This task must not touch it.
- Fallback semantics stay: when reflink genuinely fails (cross-device, unsupported FS), the
  existing copy fallback must still run — reflink failure is non-fatal, never disqualifying.

- **Re-verify these anchors before editing** — line numbers drift:
  ```bash
  grep -n 'func reflinkCopy' internal/plugins/deluge/centralization.go   # the stub, ~:199, 1 hit
  grep -n 'reflinkCopyOS' internal/deluge/import_unix.go                 # real impl, ~:23, >=1 hit
  grep -rn 'reflinkCopyOS' internal/deluge/                              # all legacy callers to update
  grep -rn 'reflinkCopy(' internal/plugins/deluge/                       # plugin call sites of the stub
  grep -n 'Hydrate the full row before writing back' internal/plugins/deluge/centralization.go  # preserve, 1 hit
  head -5 internal/deluge/import_unix.go internal/deluge/import_windows.go  # confirm build tags
  ```
  Zero hits on any of the first two = STOP and report; do not guess.

## Step-by-step

1. Export the real implementation: rename `reflinkCopyOS` → `ReflinkCopyOS` in
   `internal/deluge/import_unix.go` AND `internal/deluge/import_windows.go` (both build-tag
   variants must keep identical signatures), updating every caller inside `internal/deluge`
   (from the grep above — includes `internal/deluge/import.go`).
2. In `internal/plugins/deluge/centralization.go`, replace the stub body with a delegation to
   `deluge.ReflinkCopyOS(src, dest)` (import the legacy package), or delete the local wrapper
   and call it directly at the plugin call sites — pick whichever leaves fewer indirections;
   the "reflink not available" placeholder string must be gone either way.
3. Purely a consolidation — do not change fallback-copy logic, error wrapping semantics at call
   sites, the hydrate block, or anything in TASK-03's mode switches.
4. Tests in `internal/plugins/deluge/centralization_test.go`: (a) plugin path invokes the shared
   impl (on a same-FS temp dir where reflink may succeed OR return a real FS error — assert it
   no longer returns the placeholder error string); (b) fallback-preserved: when reflink errors,
   the copy fallback still produces the file (reflink failure is non-fatal — spelled here and in
   acceptance). Anti-over-suppression: N/A (no filter/guard/veto/skip/dedupe path is added).
5. Run full `go test ./... -short` (cross-package rename), then `make ci`.
6. Bump the file header (version + last-edited) on every file you touch; keep existing guids.

## How to test

```bash
go test ./... -short
make ci
```
staticcheck is red on main (pre-existing backlog #1796) — scope staticcheck to files you changed; the merge gate is Minimal CI green.

## Acceptance criteria

- [ ] `grep -n "reflink not available" internal/plugins/deluge/centralization.go` returns 0 hits (stub gone)
- [ ] `grep -rn "ReflinkCopyOS" internal/plugins/deluge/ internal/deluge/ --include='*.go' | grep -v _test | wc -l` ≥ 3 (exported impl + legacy caller + plugin caller)
- [ ] Fallback-preserved test green: reflink error still yields a copied file (non-fatal)
- [ ] `grep -n 'Hydrate the full row before writing back' internal/plugins/deluge/centralization.go` still hits
- [ ] Anti-over-suppression: N/A
- [ ] Full `go test ./... -short` green; `make ci` exits 0 (staticcheck scoped to changed files).
- [ ] File headers bumped on every changed file.

## Commit message

```
refactor(deluge): share the real reflink impl with the plugin path, drop the always-fail stub (INIT-5 T4)

The plugin package's reflinkCopy was a placeholder that always forced the copy
fallback; only internal/deluge reflinked for real. Exports ReflinkCopyOS from
the legacy package (unix + windows build variants) and delegates the plugin
path to it, eliminating the twin. Copy fallback semantics unchanged.

Co-Authored-By: Claude <noreply@anthropic.com>
```

## PR + merge

```bash
git push -u origin agent/torrent-relocation-reconcile-reflink-imports
gh pr create --fill
gh pr merge <number> --rebase
```

## Idempotency / Rollback

If `grep -n "ReflinkCopyOS" internal/plugins/deluge/centralization.go` hits AND
`grep -n "reflink not available" internal/plugins/deluge/centralization.go` returns 0 hits, the
move is already done — run acceptance instead. Rollback = revert the commit; the plugin path
returns to always-fallback-copy (slower but correct); no data or schema is touched.
