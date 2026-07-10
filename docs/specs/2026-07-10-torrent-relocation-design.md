<!-- file: docs/specs/2026-07-10-torrent-relocation-design.md -->
<!-- version: 1.0.0 -->
<!-- guid: 29285d3c-78ba-4e2e-8cc4-8c5b30639150 -->
<!-- last-edited: 2026-07-10 -->

# Torrent Hardening + Client-Agnostic Relocation — Design Spec

**Status:** Draft <!-- flip to: Approved — ready for implementation planning, at Gate 2 -->
**Scope:** Go backend (`internal/deluge`, `internal/plugins/deluge`, `internal/download`,
`internal/maintenance/jobs`, `internal/config`) + one Settings-UI field.
Explicit follow-ups: acquisition/indexers/auto-grab are OUT (future initiative).
**Parent task:** INIT-5 (master plan `.claude/notes/2026-07-10-remaining-work-master-plan.md`)

---

## Motivation

Today, when the organize/undo/version-swap/import pipelines move an audiobook file, they ALSO ask
Deluge to move it — a second, physical move of data the app already relocated:

- `Client.MoveStorage` (`internal/deluge/client.go:215-220`) issues `core.move_storage`, which is
  a **physical move on Deluge's side** ("moves a torrent's data to a new location on disk").
  Verify: `grep -n 'func (c \*Client) MoveStorage' internal/deluge/client.go`.
- We reflink/copy the file first, THEN ask Deluge to move it again → the double-move breakage
  (data copied twice, or Deluge moving a path we just vacated).
- **7 MoveStorage call sites** (all verified 2026-07-10 at HEAD `fce58498`):
  `internal/deluge/import.go:123`, `internal/plugins/deluge/import.go:51`,
  `internal/plugins/deluge/centralization.go:165`, `internal/plugins/deluge/path_update.go:130`,
  `internal/deluge/integration.go:106` (`NotifyDelugeMoveStorage`, the fan-out hub whose own call
  sites at :148/:174/:186/:193 serve version-swap, undo, and organize),
  `internal/maintenance/jobs/bulk_deluge_import.go:233`, and `internal/download/deluge.go:198`
  (a THIRD separate Deluge client — the RPC lives inside `SetDownloadPath`, no method literally
  named MoveStorage exists there; it has zero production callers today).
- `DelugeMoveEnabled=false` (`internal/config/config.go:498`; skip guards at
  `internal/deluge/import.go:122` and `internal/plugins/deluge/centralization.go:164`) just SKIPS
  relocation — the torrent then seeds from the old, now-empty path. "Re-point without moving" is a
  genuinely NEW third mode, not either existing setting.
- The abstraction is fragmented — the repo has TWO half-wired client abstractions already:
  1. `internal/plugin.DownloadClient` (`internal/plugin/plugin.go:47-52`) declares
     `MoveStorage(torrentHash, newPath string) error` but has ZERO implementors — and it CANNOT
     cheaply gain one: it embeds `internal/plugin.Plugin`
     (Capabilities/Init/Shutdown/HealthCheck, `plugin.go:25-33`), while the Deluge plugin
     (`internal/plugins/deluge`) implements a DIFFERENT base interface, `pkg/plugin/sdk.Plugin`
     (ID/Name/Version/Register only). Two separate plugin frameworks. Verify zero implementors:
     `grep -rn 'plugin\.DownloadClient' --include='*.go' internal/ pkg/` → only the declaration
     file `internal/plugin/plugin.go` (the string `DownloadClient` alone also matches the
     unrelated config struct field `config.AppConfig.DownloadClient` — do not grep for the bare
     word).
  2. `internal/download.TorrentClient` (`internal/download/client.go:54`) — a WORKING family:
     real `NewDelugeClient` + `NewQBittorrentClient`, a config-keyed factory
     (`NewTorrentClientFromConfig`, `internal/download/factory.go:14`, switch on
     `cfg.DownloadClient.Torrent.Type`), ctx-aware RPC plumbing, and a re-point-shaped method
     slot (`SetDownloadPath` — currently PHYSICAL: it issues `core.move_storage` /
     qBittorrent `setLocation`). Zero production callers of the factory today, but the
     implementations are real and tested (`internal/download/download_test.go`).
  Every relocation path holds a concrete `*deluge.Client` directly.
- Two import implementations exist; the plugin's local `reflinkCopy`
  (`internal/plugins/deluge/centralization.go:199-203`) is an always-fail stub — only the legacy
  package (`reflinkCopyOS`, `internal/deluge/import_unix.go:23`) reflinks for real.

**Goal:** OUR app moves the files; the torrent client only updates its recorded storage path.
The NEW re-point path is client-agnostic (routed through `internal/download.TorrentClient`,
resolved by configured client type — Deluge now; qBittorrent/Transmission behind a validation
guard). The LEGACY physical-move path stays bit-identical and Deluge-concrete.

## Goals

- A re-point-only relocation semantic (`UpdateStoragePath`) distinct from physical
  `MoveStorage`/`SetDownloadPath` and from the `DelugeMoveEnabled=false` silent skip.
- A validated Deluge implementation of re-point (real-Deluge spike FIRST — this is the risk).
- All 7 MoveStorage call sites routed through a single mode-aware seam. The re-point branch
  dispatches through `download.TorrentClient` (client-agnostic — this is how the master plan's
  "GetClient() becomes client-aware" lands); the physical-move branch keeps today's concrete
  `*deluge.Client.MoveStorage` calls byte-for-byte (zero risk to the legacy path).
- One real reflink/import implementation instead of two (one of which is an always-fail stub).
- Tri-state config: `off` / `physical-move` / `re-point-only`; Settings UI + docs.
- qBittorrent + Transmission re-point support in the SAME `internal/download` family (P2,
  additive, hard-guarded until validated) — no third abstraction.
- Observability for any bulk re-point: dry-run-first, canary, aggregate counts, and per-failure
  markers (see C3).

## Non-goals (v1)

- Acquisition, indexers, search, auto-grab — deferred (locked out of scope by the master plan).
- Changing the default relocation behavior before the T2 spike is human-approved — the default
  maps exactly to today's `DelugeMoveEnabled` behavior until then.
- Extending `internal/plugin.DownloadClient` — it is vestigial (zero implementors, wrong plugin
  framework for the Deluge plugin). T1 leaves it untouched except a doc-comment marking it
  vestigial and pointing at `internal/download.TorrentClient`.
- Migrating the usenet side of `internal/download` (SABnzbd etc.) — untouched.
- Removing `MoveStorage` — physical move remains a supported explicit mode.
- Enabling re-point-only for qBittorrent/Transmission by default — their no-move semantics stay
  hard-guarded until a T2-class validation per client (see C5).

## Decisions (locked during design)

1. **Who moves the data:** WE move; the client only re-points (locked by the master plan; the
   losing alternative — keep asking the client to `core.move_storage` — is the double-move bug).
2. **Interface home:** extend the EXISTING `internal/download.TorrentClient`
   (`internal/download/client.go:54`) with `UpdateStoragePath` — NOT `internal/plugin.DownloadClient`
   and NOT a new third interface. Rationale: (a) `plugin.DownloadClient` is technically infeasible
   as a home — it embeds `internal/plugin.Plugin` (Capabilities/Init/Shutdown/HealthCheck) while
   the Deluge plugin implements `pkg/plugin/sdk.Plugin`; making it the first implementor means
   bridging two plugin frameworks, far beyond additive scope. (b) The `internal/download` family
   already has real Deluge + qBittorrent clients, ctx-aware plumbing, a config-keyed factory, and
   an interface-satisfaction test — the smallest reuse move. The master plan §INIT-5 T1 explicitly
   allowed "plugin.DownloadClient (or a new TorrentClient)"; we take the TorrentClient branch and
   REUSE the existing one instead of inventing a third family.
3. **Deluge re-point mechanism:** decided BY THE SPIKE (T2), not this spec — two candidates are
   protocolized in "T2 spike protocol" below; neither is presumed to win.
4. **Config shape:** ONE new tri-state field `torrent_relocation_mode` with empty-value fallback
   derived from `DelugeMoveEnabled` (true→`physical-move`, false→`off`). `DelugeMoveEnabled` is
   kept and honored for backward compat; it is NOT removed in v1 — it is **deprecated, slated for
   removal in a future cleanup once `TorrentRelocationMode` is the sole knob** (say so in its doc
   comment so the dual-field state is intentional and time-boxed). Flipping the default to
   `re-point-only` is a post-spike human decision, not any task's deliverable. NO new client-type
   config key: the client type for re-point dispatch is the EXISTING
   `cfg.DownloadClient.Torrent.Type` (single source of truth; empty + Deluge configured resolves
   to Deluge).
5. **T3 lands as ONE PR:** the call-site migration changes a shared semantic (mode routing) across
   6 files / 3 packages (the 7th site, `internal/download/deluge.go`, is handled by T1/T2) —
   splitting it across agents would leave main in a mixed-semantics state between merges. Single
   worktree, single PR, single agent.
6. **Preserve two load-bearing guards verbatim:** the hydrate-before-write block
   (`internal/plugins/deluge/centralization.go:141-157` — memdb-slim footgun) and the
   ProtectedPathCache nil-client guard (`internal/deluge/protected_paths.go:96-111`). Any new
   relocation write path must hydrate the full row (`GetBookFileByID`) before `UpdateBookFile`.
7. **Non-Deluge re-point is hard-guarded:** the mode router REJECTS `re-point-only` (skip +
   Warn, never a physical fallback) when `cfg.DownloadClient.Torrent.Type` selects a client whose
   no-move behavior has not passed a T2-class validation. v1 allowlist: `deluge` only (post-spike).
   qBittorrent `setLocation` MAY physically move when data exists at the source — "not default"
   must not be the only thing between an operator and an unvalidated physical move.

## Data model

```go
// internal/config/config.go — NEW (T6). Placed next to DelugeMoveEnabled
// (grep -n 'DelugeMoveEnabled' internal/config/config.go).

// RelocationMode selects what the torrent client is asked to do after the
// app relocates a book file that a torrent is seeding.
type RelocationMode string

const (
	// RelocationOff: do not contact the torrent client (today's
	// DelugeMoveEnabled=false behavior — the torrent keeps its old path).
	RelocationOff RelocationMode = "off"
	// RelocationPhysicalMove: legacy behavior — ask the client to physically
	// move data (core.move_storage). Today's DelugeMoveEnabled=true.
	RelocationPhysicalMove RelocationMode = "physical-move"
	// RelocationRePointOnly: the app moved the data; the client only updates
	// its recorded storage path (UpdateStoragePath). NEW.
	RelocationRePointOnly RelocationMode = "re-point-only"
)

// Config field (string in JSON for forward compat):
//   TorrentRelocationMode string `json:"torrent_relocation_mode"` // "", "off", "physical-move", "re-point-only"

// TorrentRelocation resolves the effective mode. Empty/unknown values fall
// back to the DelugeMoveEnabled-derived legacy mapping — defaults STAY on
// today's behavior until the T2 spike is human-approved.
func (c *Config) TorrentRelocation() RelocationMode {
	switch RelocationMode(c.TorrentRelocationMode) {
	case RelocationOff, RelocationPhysicalMove, RelocationRePointOnly:
		return RelocationMode(c.TorrentRelocationMode)
	}
	if c.DelugeMoveEnabled {
		return RelocationPhysicalMove
	}
	return RelocationOff
}
```

```go
// internal/download/client.go — T1 extends the EXISTING TorrentClient interface
// (grep -n 'SetDownloadPath(ctx context.Context' internal/download/client.go).
// Existing methods (Connect/GetTorrent/GetUploadStats/SetDownloadPath/RemoveTorrent/...)
// are unchanged; SetDownloadPath gets a doc-comment hardening: PHYSICAL move.
type TorrentClient interface {
	// ... existing methods unchanged ...

	// UpdateStoragePath re-points the client's recorded storage path WITHOUT
	// any physical move — the caller has already moved the data to newPath.
	// Implementations MUST be fail-closed on RPC errors: on any RPC-level
	// failure the old path stays registered and the torrent is left in its
	// prior state. (Residual, mechanism-A-only risk: a process crash inside a
	// remove-before-re-add window can orphan the torrent — no recovery code
	// can run through a crash. Documented in the T2 spike protocol; NOT
	// promisable away by this interface.)
	UpdateStoragePath(ctx context.Context, id, newPath string) error
}

// Typed sentinel returned by implementations that cannot re-point (yet):
var ErrRePointUnsupported = errors.New("re-point-only relocation not supported by this client")
```

Both existing implementors (`DelugeClient` in `internal/download/deluge.go`, `QBittorrentClient`
in `internal/download/qbittorrent.go`) MUST gain the method in T1 (the interface-satisfaction
test `TestTorrentClientInterface`, `internal/download/download_test.go:18`, breaks the build
otherwise): fail-closed stubs returning `ErrRePointUnsupported` until T2 (Deluge) / a future
validation (qBittorrent) replace them.

### Persistence

- Config only: `torrent_relocation_mode` in the app config blob (same store as
  `deluge_move_enabled`). No new keyspaces; no app-DB schema changes. (Deluge-side torrent state
  is a different matter — see Rollback.)

## Components

### C1. Interface extension (`internal/download/client.go`, `internal/download/deluge.go`, `internal/download/qbittorrent.go`) — T1

Add `UpdateStoragePath(ctx, id, newPath) error` + `ErrRePointUnsupported` to
`download.TorrentClient`. Add fail-closed stubs (return `ErrRePointUnsupported`) to BOTH existing
implementors. Doc-harden `SetDownloadPath` on the interface and both implementors: PHYSICAL move
(`core.move_storage` / qBittorrent `setLocation`), pointing at `UpdateStoragePath` for re-point.
Doc-comment `internal/plugin.DownloadClient` as vestigial (zero implementors, different plugin
framework — do NOT extend it; see Decision 2). No delegation/mapping work exists in this task —
the implementors already satisfy the rest of the interface.

### C2. Deluge re-point implementation (`internal/download/deluge.go`) — T2, gated

`func (d *DelugeClient) UpdateStoragePath(ctx context.Context, id, newPath string) error` using
the spike-validated mechanism (see "T2 spike protocol"), reusing the existing ctx-aware `d.call`
RPC plumbing — no new RPC layer. **Fail-closed on RPC errors:** capture state first; on any
RPC failure, restore/leave the OLD path registered and return the error — never leave the torrent
removed or half-re-pointed by a handled error. (Crash-in-remove-window orphan risk is a residual,
mechanism-A-only failure mode — recorded in the spike report, not claimed away.)
**Default: not wired to any pipeline** until T3.

### C3. Mode-aware routing (`internal/deluge/integration.go` + 5 sibling call sites) — T3

`NotifyDelugeMoveStorage` (`internal/deluge/integration.go:106`) and the 5 direct call sites stop
branching on `DelugeMoveEnabled` alone and switch on `config.AppConfig.TorrentRelocation()`:

- `off` → skip (log, as today).
- `physical-move` → existing concrete `MoveStorage` call, byte-for-byte unchanged.
- `re-point-only` → NEW client-agnostic dispatch AFTER our own move has completed: a new
  `deluge.GetRelocationClient() download.TorrentClient` accessor (cached singleton beside
  `GetClient()`; resolves via `download.NewTorrentClientFromConfig` on
  `cfg.DownloadClient.Torrent.Type`, treating empty-type-with-Deluge-configured as `deluge`),
  then `rc.UpdateStoragePath(ctx, hash, dir)`. **Hard guard (Decision 7):** if the resolved
  client type is not on the validated allowlist (v1: `deluge` post-spike), skip with a Warn
  naming the client type and the guard — never fall back to a physical move. No import cycle:
  `internal/download` imports only `internal/config`.

`GetClient()` stays the accessor for the legacy physical path. Error handling stays
best-effort-log at the fan-out hub (organize already succeeded), but re-point failures log at
Warn with the mode named AND a stable machine-greppable marker `relocation-failed` including the
torrent hash, old path, and new path — so a drifted set (torrent still seeding from a vacated
path) is findable without log archaeology. A reconciliation/retry op that re-drives
`relocation-failed` torrents is an explicit post-v1 follow-up (recorded in TODO by T3, not built
in v1).

**Bulk-run safety (first production activation):** the human gate validates the MECHANISM on one
throwaway torrent; it does not validate a whole-library run. The bulk path
(`internal/maintenance/jobs/bulk_deluge_import.go:233`) in `re-point-only` mode MUST therefore:
(a) support dry-run-first (report the would-re-point count, zero RPCs); (b) support a canary
limit (first real run constrained to a small set) before any whole-library pass; (c) emit
start/progress/complete/skip logging plus an aggregate success/skip/fail count summary (house
logging rule); (d) record each torrent's pre-re-point `save_path` in the op log/report before
re-pointing it (manual re-point-back becomes possible — see Rollback).

### C4. Import-impl reconciliation (`internal/plugins/deluge/centralization.go`, `internal/deluge/import*.go`) — T4

Replace the always-fail `reflinkCopy` stub (`centralization.go:199-203`) with the real
platform-specific implementation (export/share `reflinkCopyOS` from `internal/deluge/import_unix.go:23`
+ the Windows twin), so the plugin path reflinks for real instead of always falling back.
The plugin package's only stub caller is `centralization.go:132` —
`internal/plugins/deluge/import.go` does NOT call `reflinkCopy` and is NOT touched by T4.
Hydrate-before-write (`centralization.go:141-157`) and the nil-client guard
(`internal/deluge/protected_paths.go:96-111`) are preserved byte-for-byte.

### C5. qBittorrent re-point + Transmission client (`internal/download/qbittorrent.go`, NEW `internal/download/transmission.go`, `internal/download/factory.go`) — T5, P2 additive, gated

REUSE the existing family — no `internal/plugins/qbittorrent/`, no `internal/plugins/transmission/`,
no third abstraction, no new config key:

- **qBittorrent:** implement `UpdateStoragePath` on the EXISTING `QBittorrentClient` via the
  EXISTING `setLocation` request shape (`internal/download/qbittorrent.go:181` already speaks
  `/api/v2/torrents/setLocation` — copy its request/auth shape, do not rewrite the client).
  Caveat in code AND tests: `setLocation` MAY physically move data when the source still exists;
  our contract is "we already moved the files". The C3 hard guard keeps `re-point-only`
  unreachable for qBittorrent until a T2-class validation against a real qBittorrent passes.
- **Transmission:** NEW `internal/download/transmission.go` implementing the full
  `download.TorrentClient`: session-id handshake (409 → `X-Transmission-Session-Id`),
  `torrent-get` for status, `torrent-set-location {"move": true}` for `SetDownloadPath`
  (physical), `{"move": false}` for `UpdateStoragePath` (native no-move re-point). Same hard
  guard: not on the allowlist until validated against a real Transmission.
- **Factory:** extend the `NewTorrentClientFromConfig` switch with `"transmission"`. Client type
  stays `cfg.DownloadClient.Torrent.Type` — the existing single source of truth; NO parallel
  selector key.

Gated on the T2 human sign-off (skip entirely if the spike rejects re-point — see "T2 spike
protocol" (3)). Deluge stays the default.

### C6. Undo/organize/version-swap closure (extend `internal/server/deluge_integration_test.go` + `internal/server/deluge_centralization_test.go`) — T7

The deferred bullet "Torrent move_storage on undo"
(`docs/archive/superpowers/plans/2026-04-15-bulk-organize-undo.md:97,100`) was since implemented:
`undo.RunUndoOperation` is wired to `deluge.NotifyDelugeAfterUndo` (verify:
`grep -n 'NotifyDelugeAfterUndo' internal/server/undo_engine.go`) and organize calls
`NotifyDelugeAfterOrganize` (verify: `grep -rn 'NotifyDelugeAfterOrganize' internal/server/handlers/organize.go`).
**Happy/skip/error coverage of the fan-out helpers ALREADY EXISTS** — do not duplicate it:
`TestNotifyDelugeAfterUndo_{Enabled,Disabled,NoHash,DelugeError}` and
`TestNotifyDelugeAfterVersionSwap` in `internal/server/deluge_integration_test.go`, and
`TestNotifyDelugeAfterOrganize_{CallsMoveStorage,SkipsWhenDisabled,SkipsWhenNoTorrentHash,DelugeErrorIsBestEffort}`
in `internal/server/deluge_centralization_test.go`. What is missing is ONLY the tri-state MODE
dimension. T7 extends those existing suites (reusing their established client fakes) with a
mode-matrix test (3 flows × 3 modes) and records closure of the deferred item. No new
`internal/deluge/integration_test.go` — placing the matrix beside the existing coverage avoids a
duplicate suite.

## T2 spike protocol (REQUIRED READING — the hard human gate)

T2 is a REAL-DELUGE SPIKE, single-agent (strong model), operator-in-loop, never weak-tier.
It runs against the real Deluge instance with a THROWAWAY test torrent only — never a library
torrent. Deluge has **no update-path-only RPC**; re-point must be synthesized.

**Pre-execution confirmation (REQUIRED before any destructive RPC):** before the FIRST
`core.remove_torrent` (or any other destructive call), the agent MUST raise an AskUserQuestion
that echoes the EXACT throwaway torrent hash and name and asks the operator to confirm it is the
throwaway. "Throwaway only" is otherwise a prose boundary the agent self-selects — a wrong
torrent-ID hits a real seeding torrent.

### (1) The two candidate mechanisms

**Mechanism A — remove + re-add with recheck.**
RPC sequence: `core.get_torrent_status(id, [...])` (capture save_path/label/state) → obtain the
`.torrent` filedump (Deluge state dir `<config>/state/<infohash>.torrent` on the daemon host, or
an equivalent RPC-side source the spike identifies) → `core.remove_torrent(id, false)`
(remove_data=false) → `core.add_torrent_file(name, filedumpB64, {"download_location": newDir,
"add_paused": true})` → `core.force_recheck([id])` → poll recheck → `core.resume_torrent([id])`
→ re-apply label via `label.set_torrent(id, label)`.
*Pros:* works on any Deluge 2.x; data is provably untouched (remove_data=false).
*Cons:* needs the `.torrent` blob; a window exists where the torrent is absent (a process crash
inside that window orphans the torrent — a RESIDUAL risk no fail-closed code can remove, since
recovery code cannot run through a crash); per-torrent state (stats, added-time, label) is lost
unless re-applied — **mechanism A irreversibly mutates Deluge-side torrent state even on
success**; fail-closed demands capture-BEFORE-remove and a recovery re-add against the OLD path
if the re-add at the new path fails.

**Mechanism B — move_completed_path + resume.**
RPC sequence: `core.pause_torrent([id])` → `core.set_torrent_options([id],
{"move_completed": false, "move_completed_path": newDir, ...})` plus whatever
download-location option Deluge 2.x accepts without triggering I/O → `core.force_recheck([id])`
→ `core.resume_torrent([id])`.
*Pros:* no remove window; per-torrent state preserved; simpler failure surface.
*Cons:* Deluge 2.x may not accept a download-location change without physically moving (or may
silently trigger a move — DISQUALIFYING); the spike must observe whether the option is accepted
AND whether any file I/O occurs on the Deluge side.

### (2) Validation checklist (against REAL Deluge, throwaway torrent)

- [ ] Torrent seeds from the NEW path after re-point (`core.get_torrent_status` shows the new
      `save_path` AND state returns to Seeding).
- [ ] **ZERO physical move observed on Deluge's side**: record inode numbers (`ls -i`) + mtimes of
      the payload files at the new path before and after; confirm the old path was not recreated;
      confirm no bulk read/write I/O attributable to a move.
- [ ] Recheck completes to 100% with no re-download.
- [ ] **No data loss on failure — fail-closed on RPC errors:** inject a failure mid-sequence
      (e.g. re-add with a bad path / kill the connection); verify the procedure aborts, the OLD
      path remains registered (or is restored via the recovery re-add), and payload files are
      intact. Record the crash-window residual (mechanism A) explicitly in the report.

### (3) STOP-for-human decision point

After both mechanisms are exercised (or one is disqualified with evidence), the T2 agent STOPS
and presents: the RPC transcript per mechanism; before/after `core.get_torrent_status` output;
the inode/mtime proof of zero physical move; the failure-injection result; and a recommendation
(A or B) with residual risks. **Sign-off is a REAL AskUserQuestion decision** (a text-reply
approval does not count — see memory `feedback_prod_apply_review_gate`). The AskUserQuestion
OUTCOME — not any option-label text — is the source of truth. Its record in the spike report is
a single canonical decision line, written ONLY after the human answers:
`DECISION: APPROVED mechanism <A|B> via AskUserQuestion YYYY-MM-DD` on approval, or
`DECISION: REJECTED — hardening-only fallback via AskUserQuestion YYYY-MM-DD` on rejection.
(Downstream gate checks grep for `^DECISION: APPROVED` — never for the bare word "Approve",
which also appears in the question's option labels.) T3 MUST NOT start until an APPROVED line
exists. If both mechanisms fail validation (or the human rejects), T3/T5/T7's re-point wiring is
BLOCKED and the initiative falls back to hardening-only (T4, T6-as-documented, docs) — **and the
fallback is re-parented so it stays reachable:** T4's dependency on T3 exists ONLY to serialize
shared-file edits; on rejection, T4 branches directly from `origin/main` (see the plan's
dependency graph note).

## Migration / integration

Call-site shape (T3), mechanical at all 6 T3 sites — Before/After for the guard at
`internal/deluge/import.go:122-123` (verify: `grep -n 'delugeClient.MoveStorage' internal/deluge/import.go`):

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

`internal/download/deluge.go:198` (`SetDownloadPath`, zero production callers) gets its
PHYSICAL doc-comment in T1 and the real `UpdateStoragePath` in T2 — it is not part of T3's diff.

## Milestones

- **M1 — Interface + config (T1, T6).** Additive — no existing behavior changes; both
  `UpdateStoragePath` stubs return `ErrRePointUnsupported`; the tri-state resolves to exactly
  today's behavior when unset.
- **M2 — Deluge re-point validated + implemented (T2).** Additive (not wired). Ends at the
  STOP-for-human gate with spike evidence and the canonical DECISION line.
- **M3 — the ONE behavior-changing milestone (T3).** Call sites route by
  `TorrentRelocationMode` (default **legacy-derived, i.e. unchanged**); `re-point-only` activates
  only when the operator sets it, after M2 sign-off; first bulk activation is dry-run + canary
  gated (C3).
- **M4 — Hardening + scaffolding (T4, T5, T7).** Real reflink in the plugin path; qBittorrent
  re-point + Transmission client in `internal/download` (guarded); mode-matrix tests. Additive.
  On spike REJECTION, M4 shrinks to T4 + T7's existing-coverage note (re-point cells dropped) and
  T5 is skipped.

Each milestone is independently shippable and additive until M3.

## Files modified

| File | Change |
|---|---|
| `internal/download/client.go` | T1: add `UpdateStoragePath` to `TorrentClient` + `ErrRePointUnsupported`; doc-harden `SetDownloadPath` as PHYSICAL |
| `internal/download/deluge.go` | T1: fail-closed stub; T2: real re-point via the spike-validated mechanism |
| `internal/download/qbittorrent.go` | T1: fail-closed stub; T5: `UpdateStoragePath` via existing `setLocation` shape (guarded) |
| `internal/plugin/plugin.go` | T1: doc-comment only — mark `DownloadClient` vestigial (do not extend) |
| `internal/config/config.go` | T6: `TorrentRelocationMode` + `TorrentRelocation()` + `DelugeMoveEnabled` deprecation note |
| `web/` settings component | T6: tri-state selector (locate via `grep -rn 'deluge_move_enabled' web/src`) |
| `internal/deluge/import.go`, `internal/deluge/integration.go` (+ `GetRelocationClient`), `internal/plugins/deluge/import.go`, `internal/plugins/deluge/centralization.go`, `internal/plugins/deluge/path_update.go`, `internal/maintenance/jobs/bulk_deluge_import.go` (+ dry-run/canary/counts) | T3: mode-aware routing (6 files, ONE PR) |
| `internal/plugins/deluge/centralization.go`, `internal/deluge/import.go`, `internal/deluge/import_unix.go`, `internal/deluge/import_windows.go` | T4: real reflink, stub removal (NOT `internal/plugins/deluge/import.go` — it has no reflink call) |
| NEW `internal/download/transmission.go`, `internal/download/factory.go` | T5: Transmission client + factory case |
| `internal/server/deluge_integration_test.go`, `internal/server/deluge_centralization_test.go` | T7: mode-matrix cells added to the EXISTING suites |

## Testing

| Test | Asserts |
|---|---|
| `TestTorrentClientInterface` (existing, `internal/download/download_test.go:18`) | still compiles with the extended interface — both implementors satisfy it |
| `TestUpdateStoragePathStubFailClosed` | T1: both stubs return `ErrRePointUnsupported` (`errors.Is`) |
| `TestTorrentRelocationModeFallback` | unset→legacy mapping (true→physical-move, false→off); explicit values win; unknown→legacy |
| `TestUpdateStoragePathFailClosed` | T2: mock RPC failure mid-sequence → old path still registered, error returned |
| `TestNotifyDelugeMoveStorageModeMatrix` | off→no RPC; physical-move→MoveStorage; re-point-only→UpdateStoragePath (T3) |
| `TestRelocationHappyPathStillMoves` | anti-over-suppression: physical-move mode still issues MoveStorage after T3 |
| `TestRePointGuardRejectsUnvalidatedClient` | T3: re-point-only + non-allowlisted client type → skip + Warn, zero RPCs, no physical fallback |
| `TestBulkRePointDryRunAndCounts` | T3: bulk job dry-run issues zero RPCs + reports counts; real run emits aggregate success/skip/fail |
| `TestPluginReflinkCopyReal` | T4: plugin path reflinks (or falls back gracefully on non-reflink FS), stub gone |
| `TestTransmissionUpdateStoragePathNoMove` | T5: request body carries `"move": false`; non-2xx ⇒ error (fail-closed) |
| `TestUndoOrganizeVersionSwapModeMatrix` | T7: 3 flows × 3 modes drive the correct client call (added to the EXISTING internal/server suites) |

## Rollback

**GATE (verbatim):** SPEC -> EXECUTE with a hard human gate: T2 is a REAL-DELUGE SPIKE with
STOP-FOR-HUMAN sign-off REQUIRED before T3's call-site migration may start. The new relocation
mode is config-gated (tri-state: off / physical-move / re-point-only); defaults STAY on today's
behavior until the T2 spike is human-approved.

- M1/M2 are dormant until opt-in: `UpdateStoragePath` is unreachable from any pipeline until T3,
  and T3's default mode resolves to exactly today's `DelugeMoveEnabled` behavior.
- Config revert (`torrent_relocation_mode: "physical-move"` / `"off"`, no deploy needed) is
  **forward-only**: it stops FUTURE re-points; it CANNOT un-re-point torrents already processed.
- **Mechanism A mutates Deluge-side torrent state irreversibly even on success** (added-time and
  stats reset; label re-applied best-effort; a crash in the remove window orphans the torrent).
  This is real, non-app data mutation and is exactly why the T2 gate + C3 dry-run/canary exist.
  Mitigation for bulk runs: C3(d) records each torrent's pre-re-point `save_path` in the op
  log/report, so a manual re-point-back of any torrent remains possible.
- Code revert: `git revert` any single task's PR (each is one commit chain on its own branch).
- T4 (reflink) reverts to the always-fallback-copy behavior via PR revert; no data implications.
- T5 is additive and guard-locked; Deluge stays the default client type.
- No app-DB schema or data migration in this initiative. (The blanket claim stops there —
  Deluge-side torrent state IS mutated by re-point, per the bullet above.)

## Open questions (resolved — recorded for the plan)

1. ~~Which Deluge re-point mechanism?~~ → decided by the T2 spike (protocol above); spec does not presume.
2. ~~Extend `plugin.DownloadClient` or new interface?~~ → NEITHER: `plugin.DownloadClient` is
   infeasible (embeds the wrong plugin framework's base interface — would not compile against the
   Deluge plugin) and a new interface would be a third family. Extend the EXISTING
   `internal/download.TorrentClient` (Decision 2).
3. ~~Remove `DelugeMoveEnabled`?~~ → keep + honor as fallback in v1; deprecated, removal is a
   named future cleanup (Decision 4).
4. ~~Does the deferred undo bullet still need code?~~ → wiring exists (`NotifyDelugeAfterUndo`)
   AND happy/skip/error tests exist in `internal/server`; what's missing is ONLY mode coverage →
   T7 scope adjusted to closure + mode-matrix cells in the existing suites.
5. ~~qBittorrent setLocation no-move semantics?~~ → NOT resolvable by a unit test alone: the C3
   hard guard rejects `re-point-only` for qBittorrent/Transmission until a T2-class validation
   against the real client passes. Scaffolding stays additive and guard-locked, so uncertainty
   does not gate T1-T4.
