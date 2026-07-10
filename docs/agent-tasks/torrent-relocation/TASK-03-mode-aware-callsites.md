<!-- file: docs/agent-tasks/torrent-relocation/TASK-03-mode-aware-callsites.md -->
<!-- version: 1.0.0 -->
<!-- guid: 24555bb0-70bd-48fe-a144-dd60b36da894 -->
<!-- last-edited: 2026-07-10 -->

# TASK-03 — Route the MoveStorage call sites through mode-aware relocation (INIT-5 T3) [⚠ review-critical]

**Gate:** SPEC -> EXECUTE with a hard human gate: T2 is a REAL-DELUGE SPIKE with STOP-FOR-HUMAN sign-off REQUIRED before T3's call-site migration may start. The new relocation mode is config-gated (tri-state: off / physical-move / re-point-only); defaults STAY on today's behavior until the T2 spike is human-approved.
**File-ownership:** none cross-initiative; internally this task touches 6 files across 3 packages — its own collision matrix decides waves. Resolution: ALL 6 files are edited in THIS one worktree as ONE PR (spec Decision 5), so no cross-agent collision exists; `internal/plugins/deluge/centralization.go` and `internal/deluge/import.go` are ALSO touched by TASK-04, which must wait until this PR merges (wave3=TASK-03, wave4=TASK-04).

**Priority:** P1 · **Effort:** M · **Recommended subagent:** Opus/strong-class · semantics-migration subagent, SINGLE-AGENT · **Why:** one shared semantic change across 6 files/3 packages that must land atomically; a wrong branch physically moves or orphans seeding data — coordinator gives line-by-line review · **Depends on:** TASK-02 (with recorded HUMAN sign-off — do not start without it) + TASK-06

## ⛔ START HERE (do this first, exactly)

```bash
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/torrent-relocation-mode-aware-callsites" -b agent/torrent-relocation-mode-aware-callsites origin/main
cd "$REPO/.worktrees/torrent-relocation-mode-aware-callsites"
git rebase origin/main
```

**Precondition check (STOP if any fails):** TASK-02's human sign-off is recorded as the
canonical decision line —
`grep -n '^DECISION: APPROVED' docs/reports/2026-07-torrent-repoint-spike.md` MUST hit.
Do NOT grep for the bare word "Approve": it appears in the AskUserQuestion option labels and in
rejection records, so it matches even when the human REJECTED the spike. The AskUserQuestion
outcome — not option-label text — is the source of truth; the `DECISION: APPROVED` line is its
only machine-checkable record. Also: TASK-06's resolver exists
(`grep -n "func (c \*Config) TorrentRelocation()" internal/config/config.go` hits) and TASK-02's
implementation exists (`grep -n 'func (d \*DelugeClient) UpdateStoragePath' internal/download/deluge.go` hits).

## Goal

Replace every `DelugeMoveEnabled`-guarded `MoveStorage` call with a three-way switch on
`config.AppConfig.TorrentRelocation()` (REUSE TASK-06's resolver and the
`config.RelocationOff/RelocationPhysicalMove/RelocationRePointOnly` constants — do not invent new
mode strings): `off` → skip with the existing skip-log; `physical-move` → the existing concrete
`MoveStorage` call byte-for-byte unchanged; `re-point-only` → CLIENT-AGNOSTIC dispatch AFTER our
own file move has completed, via a new `deluge.GetRelocationClient() download.TorrentClient`
accessor (this is how the master plan's "GetClient() becomes client-aware" lands) with a hard
guard rejecting unvalidated client types. Default behavior is bit-identical to today (the
resolver falls back to the `DelugeMoveEnabled` mapping).

## Background (verify before editing)

- **What "spec" means in this brief:** citations of the form "spec Decision N" / "spec §C3" /
  "spec §Migration" refer to the INIT-5 design spec
  `docs/specs/2026-07-10-torrent-relocation-design.md`, which lives only in the separate
  PLANNING repo `audiobook-organizer-plan-remaining-work` — it is NOT present in the worktree
  you just created. Every cited requirement is restated inline right where it is cited (including
  the §Migration Before/After worked example, inlined in Step 3 below). Treat this brief as
  self-sufficient; do not block on locating the spec.
- The 6 T3 call sites (verified at HEAD `fce58498`): `internal/deluge/import.go:123`;
  `internal/plugins/deluge/import.go:51`; `internal/plugins/deluge/centralization.go:165`;
  `internal/plugins/deluge/path_update.go:130`; `internal/deluge/integration.go:106`
  (`NotifyDelugeMoveStorage` — the fan-out hub; its OWN callers at :148/:174/:186/:193 serve
  version-swap/undo/organize and do NOT change — only the hub's body changes);
  `internal/maintenance/jobs/bulk_deluge_import.go:233`. The master plan's 7th site,
  `internal/download/deluge.go:198` (`SetDownloadPath`, zero production callers), was handled by
  TASK-01 (PHYSICAL doc-comment) + TASK-02 (real re-point impl) — NOT part of this diff.
- **Re-point dispatch (spec §C3):** add `GetRelocationClient() download.TorrentClient` to
  `internal/deluge/integration.go` beside `GetClient()` — a cached singleton resolving
  `download.NewTorrentClientFromConfig(config.AppConfig)` on `cfg.DownloadClient.Torrent.Type`,
  treating empty-type-with-Deluge-configured as `deluge`. No import cycle: `internal/download`
  imports only `internal/config` (verify:
  `grep -rn '"github.com/falkcorp/audiobook-organizer/internal/' internal/download/*.go | grep -v _test`).
- **Hard guard (spec Decision 7):** if `re-point-only` resolves a client type NOT on the
  validated allowlist (v1: `deluge` only, post-spike), skip with a Warn naming the client type
  and the guard — NEVER fall back to a physical move. qBittorrent `setLocation` may physically
  move data; "not default" must not be the only protection.
- Existing skip guards to replace: `internal/deluge/import.go:122` and
  `internal/plugins/deluge/centralization.go:164` (`if cfg.DelugeMoveEnabled && ... != nil {` —
  no else branch; false = silent skip). The hub's own guard is
  `if !config.AppConfig.DelugeMoveEnabled {` inside `NotifyDelugeMoveStorage`.
- **Preserve verbatim:** the hydrate-before-write block
  (`internal/plugins/deluge/centralization.go:141-157` — memdb-slim footgun: full row via
  `GetBookFileByID` before `UpdateBookFile`) and the ProtectedPathCache nil-client guard
  (`internal/deluge/protected_paths.go:96-111`). Do not touch either; any NEW write path you add
  must hydrate the same way.
- Error semantics stay best-effort-log (organize already succeeded); re-point failures log at
  Warn including the mode AND a stable machine-greppable marker `relocation-failed` with the
  torrent hash + old path + new path (a failed re-point leaves the torrent seeding from a
  vacated path — the marker is how an operator finds the drifted set without log archaeology).
  nil client / empty hash = skip, never panic, never disqualify the file operation that already
  succeeded. Add a TODO.md entry for the post-v1 reconciliation/retry op that re-drives
  `relocation-failed` torrents (do not build it in this task).
- **Bulk-run safety (spec §C3, REQUIRED):** the human gate validated the MECHANISM on one
  throwaway torrent — it did not validate a whole-library run. In
  `internal/maintenance/jobs/bulk_deluge_import.go`, the re-point-only path MUST support:
  (a) dry-run-first (report would-re-point count, zero RPCs); (b) a canary limit parameter so
  the first real run is constrained to a small set; (c) start/progress/complete/skip logging +
  an aggregate success/skip/fail summary; (d) recording each torrent's pre-re-point `save_path`
  in the op log/report before re-pointing (manual re-point-back stays possible — config revert
  is forward-only).

- **Re-verify these anchors before editing** — line numbers drift:
  ```bash
  grep -n 'delugeClient.MoveStorage' internal/deluge/import.go                        # ~:123, 1 hit
  grep -n 'p.client.MoveStorage' internal/plugins/deluge/import.go                    # ~:51, 1 hit
  grep -n 'p.client.MoveStorage' internal/plugins/deluge/centralization.go            # ~:165, 1 hit
  grep -n 'p.client.MoveStorage' internal/plugins/deluge/path_update.go               # ~:130, 1 hit
  grep -n 'func NotifyDelugeMoveStorage' internal/deluge/integration.go               # ~:106, 1 hit
  grep -n 'delugeClient.MoveStorage' internal/maintenance/jobs/bulk_deluge_import.go  # ~:233, 1 hit
  grep -n 'func NewTorrentClientFromConfig' internal/download/factory.go              # dispatch factory, ~:14, 1 hit
  grep -n 'DelugeMoveEnabled' internal/config/config.go                               # legacy field, ~:498
  grep -n 'Hydrate the full row before writing back' internal/plugins/deluge/centralization.go  # preserve, 1 hit
  grep -n 'if c.client == nil' internal/deluge/protected_paths.go                     # preserve, 1 hit
  ```
  Zero hits on any grep = STOP and report; do not guess.

## Step-by-step

1. Add `GetRelocationClient()` to `internal/deluge/integration.go` (cached singleton beside
   `GetClient()`, resolution + hard guard per Background). The guard's allowlist is a package
   constant (`repointValidatedClients = map[string]bool{"deluge": true}`) with a comment pointing
   at spec Decision 7 (lifting it for qbittorrent/transmission requires a T2-class validation).
2. Migrate the fan-out hub: in `NotifyDelugeMoveStorage`, replace the
   `!config.AppConfig.DelugeMoveEnabled` early return with the three-way switch on
   `config.AppConfig.TorrentRelocation()`. Keep the empty-hash early return, `GetClient()` nil
   check, and `filepath.Dir` behavior exactly as-is on the physical branch; the re-point branch
   goes through `GetRelocationClient()` + `UpdateStoragePath(ctx, ...)`
   (`context.Background()` with a timeout is acceptable at the hub — it has no ctx today).
3. Migrate the 4 remaining direct Go call sites the same way. The spec §Migration worked example
   for `internal/deluge/import.go:122-123`, reproduced here in full — mirror this shape at each
   site (keep each site's surrounding error handling, logging fields, and hash/nil guards; only
   the mode branch changes):

   ```go
   // Before:
   if cfg.DelugeMoveEnabled && bookFile.DelugeHash != "" && delugeClient != nil {
   	moveErr := delugeClient.MoveStorage([]string{bookFile.DelugeHash}, filepath.Dir(dest))
   	...
   }

   // After:
   switch cfg.TorrentRelocation() {
   case config.RelocationPhysicalMove:
   	if bookFile.DelugeHash != "" && delugeClient != nil {
   		// LEGACY path: byte-for-byte today's concrete call.
   		moveErr := delugeClient.MoveStorage([]string{bookFile.DelugeHash}, filepath.Dir(dest))
   		...
   	}
   case config.RelocationRePointOnly:
   	if bookFile.DelugeHash != "" {
   		// CLIENT-AGNOSTIC path: resolved by cfg.DownloadClient.Torrent.Type,
   		// hard-guarded to the validated allowlist (v1: deluge post-spike).
   		if rc := deluge.GetRelocationClient(); rc != nil {
   			rpErr := rc.UpdateStoragePath(ctx, bookFile.DelugeHash, filepath.Dir(dest))
   			... // Warn + `relocation-failed` marker on error; best-effort
   		}
   	}
   default: // RelocationOff — skip, log as today
   }
   ```
4. Bulk job (`bulk_deluge_import.go`): mode switch as above PLUS the dry-run/canary/counts/
   save_path-recording requirements from Background — this is the one call site that iterates
   thousands of torrents; per-item work already runs under the job's existing pool/loop shape —
   do not remove any existing concurrency.
5. Purely a routing change elsewhere — do not rename functions, reorder logic, change
   signatures, or touch the hydrate block / nil-client guard.
6. Tests: add `TestNotifyDelugeMoveStorageModeMatrix` (off→no RPC; physical-move→MoveStorage;
   re-point-only→UpdateStoragePath), `TestRelocationHappyPathStillMoves` (anti-over-suppression:
   with mode=physical-move, a valid hash+client still issues MoveStorage — proves the new switch
   does not suppress the legacy path), `TestRePointGuardRejectsUnvalidatedClient` (re-point-only
   + client type `qbittorrent` → zero RPCs, Warn, no physical fallback), and
   `TestBulkRePointDryRunAndCounts` (dry-run: zero RPCs + count report; real run: aggregate
   success/skip/fail emitted). Extend existing import tests
   (`grep -n 'DelugeMoveEnabled' internal/deluge/import_test.go` to find them) for the new modes.
   Edge semantics in tests AND assertions: empty hash / nil client / mode `off` ⇒ NO RPC and NO
   error surfaced to the caller (skip, as today).
7. Run the FULL `go test ./... -short` (store-getter/mode migrations break unexpected consumers
   — never a subset), then `make ci`.
8. Bump the file header (version + last-edited) on every file you touch; keep existing guids.

## How to test

```bash
go test ./... -short
make ci
```
staticcheck is red on main (pre-existing backlog #1796) — scope staticcheck to files you changed; the merge gate is Minimal CI green.

## Acceptance criteria

- [ ] `grep -rn "TorrentRelocation()" internal/deluge internal/plugins/deluge internal/maintenance/jobs --include='*.go' | grep -v _test | wc -l` ≥ 6 (hub + 5 direct sites)
- [ ] `grep -rn "DelugeMoveEnabled &&" internal/deluge/import.go internal/plugins/deluge internal/maintenance/jobs/bulk_deluge_import.go` returns 0 hits (old guards replaced)
- [ ] `grep -n "GetRelocationClient" internal/deluge/integration.go` hits (client-aware dispatch exists) and `grep -n "TestNotifyDelugeMoveStorageModeMatrix" internal/deluge` -r hits; matrix covers all 3 modes
- [ ] Anti-over-suppression: `grep -rn "TestRelocationHappyPathStillMoves"` hits and the test is green (physical-move still moves)
- [ ] Hard guard: `grep -rn "TestRePointGuardRejectsUnvalidatedClient"` hits and the test is green (no physical fallback)
- [ ] Bulk safety: `grep -rn "TestBulkRePointDryRunAndCounts"` hits and the test is green; pre-re-point save_path recording asserted
- [ ] `grep -n 'Hydrate the full row before writing back' internal/plugins/deluge/centralization.go` still hits; `grep -n 'if c.client == nil' internal/deluge/protected_paths.go` still hits (guards preserved)
- [ ] TODO.md gained the `relocation-failed` reconciliation-op follow-up entry
- [ ] Full `go test ./... -short` green; `make ci` exits 0 (staticcheck scoped to changed files).
- [ ] File headers bumped on every changed file.

## Commit message

```
feat(deluge): route MoveStorage call sites through tri-state relocation mode (INIT-5 T3)

Replaces DelugeMoveEnabled guards at the fan-out hub and 4 direct call sites +
the bulk import job with the TorrentRelocation() switch (off / physical-move /
re-point-only). Physical-move stays byte-identical; re-point-only dispatches
client-agnostically via GetRelocationClient() (download.TorrentClient, keyed on
DownloadClient.Torrent.Type) behind a validated-client hard guard. Bulk re-point
is dry-run/canary-gated with aggregate counts and pre-re-point save_path
recording. Defaults resolve to today's behavior; re-point-only activates only by
explicit config after the TASK-02 human-approved spike.

Co-Authored-By: Claude <noreply@anthropic.com>
```

## PR + merge

```bash
git push -u origin agent/torrent-relocation-mode-aware-callsites
gh pr create --fill
gh pr merge <number> --rebase
```
⚠ review-critical: coordinator line-by-line review before merge.

## Idempotency / Rollback

If `grep -rn "TorrentRelocation()" internal/deluge/integration.go` hits AND
`grep -n "DelugeMoveEnabled &&" internal/deluge/import.go` returns 0 hits, the migration is
already done — run acceptance instead. Rollback = revert the commit (old guards restored); or
operationally set `torrent_relocation_mode: "physical-move"` / `"off"` — no deploy needed, since
the default mapping preserves legacy behavior either way. NOTE: config revert is forward-only —
already-re-pointed torrents stay re-pointed (the recorded pre-re-point save_paths enable manual
re-point-back). No app-DB data or schema is touched.
