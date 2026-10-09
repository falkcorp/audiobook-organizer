<!-- file: docs/proposals/2026-10-holistic/10-deluge-cleanup-after-organize.md -->
<!-- version: 1.0.1 -->
<!-- guid: fb992bbf-5e74-4bf5-8d1b-b5d5ee93670a -->
<!-- last-edited: 2026-10-09 -->

# 10: Deluge cleanup after organize (decision D52)

Owner's requirement, verbatim (09-owner-decisions.md, D52): *"I want to be able to remove the copy from deluge and if it's already been organized and moved into the library then we can remove the deluge downloaded version of it."* Accepted parameters: remove the torrent **and its data**, only after seeding reaches **ratio 1.0 or 14 days, whichever comes first** (both are settings). Built as a Repairs-style fixer (trial → approve → apply) on the ops v3 Fixer kind with a declared `Deletes` effect and a journal. It never touches library files or `book_file` rows.

Round-2 author `r5`. Planning only; no code changed. Evidence anchors are `file:line` at commit `ebda30d47`.

## 1. Summary

- **A durable torrent↔book link exists in the schema but nothing live writes it.** `BookVersion.TorrentHash` has a Pebble index (`idx:bv:torrent:`, `pebble_store.go:4842`) and `BookFile` has `DelugeHash`, `DownloadHash`, `DelugeOriginalPath`, `ImportedFromDelugeAt` (`store.go:1029-1041`). But the only writer of `TorrentHash` is `versions.CreateIngestVersion`, whose only caller passes none (`importer/service.go:332-335`); the `/deluge/discover/import` handler echoes `torrent_hash` back and drops it (`deluge_discovery.go:97-109`); and the only setter of `BookFile.DelugeHash` is `MarkFileImportedFromDeluge`, whose only caller is `plugins/deluge/import.go:44`, marked `lint:ignore U1000` (dead). What **is** written on the live import path is `DelugeOriginalPath` + `ImportedFromDelugeAt` (`deluge/import.go:328-330`): a **path** link, not a hash link.
- The import copy is a reflink (`fileops.ReflinkOrCopy`, `deluge/import.go:319`). A FICLONE clone is an independent inode sharing extents, "NOT hardlinks, so a later write to one never reaches the other" (`fileops/reflink.go:60-66`). Deleting the Deluge-side file is therefore safe for the library copy, on the same ZFS dataset (block cloning) and across datasets (where it was a byte copy anyway).
- **The one real hazard is `core.move_storage`.** With `deluge_move_enabled=true`, `ImportToLibraryWith` (`deluge/import.go:372-373`), `deluge.path-update` (`path_update.go:131`) and `NotifyDelugeMoveStorage` point the torrent's `save_path` **at the library directory**. A "remove with data" on such a torrent deletes library files. The fixer must refuse any torrent whose files resolve to a `book_file` path or under `RootDir`. The config key has no default and is `false` in the zero value; the copy model (this fixer) and the seed-from-library model are mutually exclusive, and the owner should pick one (Q6).
- The Deluge client (`internal/deluge/client.go`) has list, get, labels, move and connected. It has **no remove call** and requests no `ratio`, `seeding_time`, `completed_time`, `files` or `file_progress` fields (`client.go:142,200`). PR 0 adds them.
- Verification cannot be "same size as the library file": tag writes change the library copy's bytes and size (`write_tags_safe.go:159-166`). The pristine digest survives in `original_file_hash`, frozen at the first sampled write (`pebble_store_bookfiles.go:2414-2417`), and in `file_hash` for files never tagged since import. Both are `filehash.BookFileHash` (SHA-256 of first 10 MB ‖ last 10 MB ‖ size above 100 MB; whole file below). Hashing the Deluge-side file this way reads at most 20 MB per file and needs no audio decode. A fingerprint match is not available in v1 (the Deluge file has no row and prints are Mac-only); it is listed as a future lane.
- `RunVersionSwap` has zero callers (`grep -rn "RunVersionSwap(" internal | grep -v _test | grep -v "func "` → none). `NotifyDelugeAfterVersionSwap` is only called by it. D14c keeps both; this spec needs neither, and section 7 says what to do with them.
- The fixer is `deluge.cleanup-after-organize`, title **"Deluge copies already in the library"**, under Review → Repairs. Rows are torrents; subjects are the books they fed. Trial lists eligible, held and refused torrents with the reason; the owner approves by row; apply calls `core.remove_torrent(hash, remove_data=true)` inside `Writer.Effect` with a journal row per book and a `delugecleanup:` ledger entry per torrent.
- It writes no `book_file` row and no library file. The only store writes are the journal and the ledger. The write set is declared as `Writes(Deletes(ResExternalData))` so the dispatcher's write-set gate still serializes it against `library.organize` (which writes `ResBookFiles`/`ResFiles`), and the apply takes the scan stand-down like every other fixer (`repairs/engine.go:600-608`).
- 7 PRs: PR 0 client (S), PR 1 write the link on import (M), PR 2 link backfill op (M), PR 3 the fixer (L, needs 05 PR 5+6), PR 4 metrics + alert (S, needs 11 PR 1), PR 5 Repairs row parts + settings UI (S), PR 6 docs (S).

## 2. Findings

| ID | Finding | Evidence | Confidence | Impact |
|---|---|---|---|---|
| F1 | Client RPCs: `auth.login`, `core.get_torrents_status` (fields `hash,name,save_path,state,progress,label,total_size`), `core.get_torrent_status` (5 fields), `label.get_labels`, `core.move_storage`, `web.connected`. No remove, no ratio/seed-time/files. | `internal/deluge/client.go:121-236`; `torrentFields` at `:142`; `GetTorrent` fields at `:200` | high | PR 0 extends the client |
| F2 | Auth is Web UI password + cookie session; config from `deluge_web_url`/`deluge_web_password`, falling back to `download_client.torrent.deluge.{host,port,password}`; password defaults to `deluge`. One process-wide client. | `internal/deluge/integration.go:34-67`; `config.go:1667-1671,241-247` | high | Fixer reuses `GetClient()`; no new auth |
| F3 | `BookVersion.TorrentHash` is never populated by a live path: sole writer `CreateIngestVersion` (`ingest.go:78-84`), sole caller passes no hash. | `internal/importer/service.go:332-335`; `grep -rn "CreateIngestVersion(" internal \| grep -v _test` → 1 caller | high | No hash link today |
| F4 | `/deluge/discover/import` binds `torrent_hash`, calls `ImportFile{FilePath, Organize:false}` and echoes the hash in the response; `ImportFileRequest` has no hash field. | `internal/server/deluge_discovery.go:84-109`; `importer/service.go:121-137` | high | PR 1 fixes the import path |
| F5 | `BookFile.DelugeHash` is set only by `MarkFileImportedFromDeluge`; its only caller is dead (`lint:ignore U1000`). `GetBookFilesNeedingDelugeImportCore` filters `DelugeHash != "" && ImportedFromDelugeAt == nil`, which nothing produces. | `pebble_store_mark_import.go:36,71`; `plugins/deluge/import.go:21-22,44`; `pebble_store_bookfiles.go:1129`; `memdb_reads.go:1308` | high | The bulk import route is inert for new rows (not this spec's job to fix; noted for 04) |
| F6 | The live import writes `DelugeOriginalPath=src`, `FilePath=dest`, `ImportedFromDelugeAt=now` on the existing row (repoint, not a new row). This is the durable path link. | `internal/deluge/import.go:326-332` | high | Candidate resolution L2 works today |
| F7 | Copy is `fileops.ReflinkOrCopy`: FICLONE (independent inode, shared extents) or byte copy. Destination never truncated. | `deluge/import.go:300-323`; `fileops/reflink.go:60-84`; `reflink_linux.go:16-19` | high | Deleting the source never affects the library copy |
| F8 | Owner's probes (memory note, 2026-09-23): FICLONE works inside the books dataset; across datasets FICLONE fails EXDEV and the fallback is a full byte copy. Either way the library file owns its bytes. | memory `project_auto_organize_backup_cross_dataset_copy` | medium (prod probe, not re-run) | ZFS semantics confirm F7 |
| F9 | `move_storage` into the library: `ImportToLibraryWith` calls it when `DelugeMoveEnabled && DelugeHash != ""`; `deluge.path-update` on `book.relocated`; `NotifyDelugeAfterOrganize`/`AfterVersionSwap`. `DelugeHash` is empty today (F5), so the import-path call never fires, but `path-update` uses `BookVersion.TorrentHash` (also empty, F3). | `deluge/import.go:369-380`; `path_update.go:119-137`; `integration.go:106-176` | high | Once PR 1 writes hashes, `deluge_move_enabled=true` would start moving torrents into the library. E3 guard + Q6 |
| F10 | Protected-path cache: every active torrent's `save_path` is protected (5-min TTL); `Loaded()` false means "unknown", and writers must not proceed. | `internal/deluge/protected_paths.go:19-57` | high | Fixer requires `Loaded()`; calls `Invalidate()` after apply |
| F11 | Hash columns: `file_hash` = `filehash.BookFileHash` (sampled above 100 MB); `original_file_hash` frozen at first sampled write with `Kind=sampled`; `post_metadata_hash` whole-file SHA-256 after a tag write. Tag writes update `file_hash` to the new bytes. | `filehash.go:54,68,71-81`; `write_tags_safe.go:85-97,159-170`; `pebble_store_bookfiles.go:2414-2417` | high | Verification algorithm §3.3 |
| F12 | `original_file_hash` has two historical writers with two algorithms; only `OriginalFileHashKind == sampled` is trustworthy. | `file_provenance.go:57-69` (TODO-ORIGHASH-SPLIT) | high | Verification trusts `Kind=sampled` only |
| F13 | Provenance chain (`FileEvent`, append-only, keyed by `BookFileID`, carries `Path` and a `FileDigest` with `SHA256Chunk`, `SizeBytes`, `TorrentHash`). | `file_provenance.go:43-120` | high | Candidate resolution L3; verification source (c) |
| F14 | `RunVersionSwap` has no callers; `NotifyDelugeAfterVersionSwap` is called only from it; swap moves the old primary into `.versions/<id>/` and tells Deluge to follow. | `versions/swap.go:48,221`; grep in §1 | high | D14c: keep; irrelevant to eligibility except as a `.versions/` path under RootDir (E3 covers it) |
| F15 | Repairs apply takes the scan stand-down for every fixer unless it implements `NoScanStandDown`; organize's key is `library.organize`, scan's is `library.scan` (never split). | `repairs/engine.go:600-608`; `repairs/standdown.go:26-45`; `library_core_ops.go:88,304` | high | §3.7 concurrency |
| F16 | Deluge op IDs in the ledger: `deluge.centralize`, `deluge.path-update`, `deluge.protected-paths-sync`, `maintenance.bulk-deluge-import`. The plugin registers only when a client and cache exist. | `server/testdata/op_ids.golden:68-70,120`; `plugins/deluge/plugin.go:46-59` | high | New ID `deluge.cleanup-after-organize` (+ `deluge.link-backfill`) added to the ledger |
| F17 | Repairs UI: `GET /api/v1/repairs` lists fixers by `ID/Title/Description`; the lane is `RepairsPanel.tsx` with Compact/Details/Grouped views; rows carry `Current/Proposed/Evidence/Members/Class`. | `wire_repairs_routes.go:25-33`; `repairs/fixer.go:66-120,174-190`; `web/src/components/review/RepairsPanel.tsx`, `repairs/RowParts.tsx` | high | §3.9 placement |
| F18 | Deluge Web API (the reference the client cites, `client.go:14`): `core.get_torrents_status` accepts any libtorrent status key, including `ratio`, `seeding_time`, `active_time`, `time_added`, `completed_time`, `is_finished`, `is_seed`, `files`, `file_progress`, `file_priorities`, `total_done`; `core.remove_torrent(torrent_id, remove_data) -> bool`; Deluge 2.x adds `core.remove_torrents(torrent_ids, remove_data) -> [[id, msg], ...]` for failures. | Deluge docs (external); not probed against prod (ban on prod calls) | medium | PR 0 pins these in an httptest fake; the first trial on the sandbox confirms the field names |

Counts used above were measured with the greps shown; none is an estimate.

## 3. Proposed specification

### 3.1 Definition (ops v3 Fixer kind)

```go
// internal/plugins/deluge/cleanup_fixer.go (new)
func cleanupAfterOrganize(d Deps) ops.Definition {
	return ops.Fixer[CleanupParams, torrentCandidate]("deluge.cleanup-after-organize", ops.FixerSpec[CleanupParams, torrentCandidate]{
		Common: ops.Common{
			Title: "Deluge copies already in the library",
			Help:  "Lists seeded torrents whose every audio file has a verified copy in the library, and removes the torrent and its downloaded data from Deluge after the seeding threshold. Library files and book_file rows are never touched.",
			// Reads book_file rows and the provenance chain; the only store
			// writes are the journal and the delugecleanup: ledger. Deletes is
			// declared so oplint flags the def for owner review and the
			// write-set gate serializes it against organize (ResFiles).
			Effects:    ops.Writes(ops.Deletes(ops.ResExternalData)).AlsoReads(ops.ResBookFiles, ops.ResBooks, ops.ResProvenance),
			Permission: auth.PermSettingsManage,
			Exclusive:  "deluge.cleanup-after-organize",
			Timeout:    2 * time.Hour,
			Uses:       nil, // no Mac lane in v1: hashing is head+tail, no decode
		},
		Candidates:  d.listTorrents,      // one Deluge round trip; Source.Items = torrents
		Load:        d.loadTorrent,       // core.get_torrent_status by hash
		Evaluate:    d.evaluateTorrent,   // §3.2-3.5; plan AND apply-time re-check
		Apply:       d.removeTorrent,     // §3.6
		PartitionBy: func(r ops.Row) string { return r.ID }, // one torrent, one worker
		AfterApply:  d.invalidateProtectedPaths,
		Guards:      []ops.Guard{guardITunes, guardOwnerManual},
	})
}
```

`ResExternalData` is a new resource name ("bytes the app does not own: a download client's data"); it is added in 05's resource list (dependency, §6). If 05 prefers to reuse `ResFiles`, the gate behaviour is the same and the name is the only change.

`CleanupParams` (all optional; defaults from settings): `label`, `min_ratio`, `max_seed_days`, `min_age_hours`, `remove_data`, `limit`.

### 3.2 Candidate resolution: torrent file → `book_file`

For each torrent returned by `core.get_torrents_status({label: <label>}, fields)`, for each entry in `files`, the absolute path is `filepath.Join(save_path, file.path)`. The row for that path is found in this order, first hit wins, and the source is recorded in `Row.Evidence`:

| Order | Link | Lives today? | Evidence |
|---|---|---|---|
| L1 | `BookFile.DelugeHash == torrent.hash` (set by PR 1 on import; PR 2 backfills) | after PR 1/2 | `memIdxDelugeHash` index exists (`memdb_schema.go:300`) |
| L2 | `BookFile.DelugeOriginalPath == abs path` | yes | `deluge/import.go:328` |
| L3 | provenance chain: any `FileEvent.Path == abs path` → `BookFileID` | yes, where recorded | `file_provenance.go` |
| L4 | legacy: a `book_file` under `RootDir` with the same basename and `FileSize == file.size` (candidate only; must still pass §3.3) | yes | — |

A torrent file that resolves to **no** row is `unlinked`; a torrent with any unlinked audio file is never eligible (owner rule: partial torrents never eligible). Non-audio files (`.nfo`, `.jpg`, `.cue`, `.txt`) do not block; they are listed in the row as "discarded with the data" (Q1).

### 3.3 Verification ("verified in the library")

A linked pair (torrent file `T`, row `R`) is **verified** when all of:

1. `R.FilePath` is under `cfg.RootDir` (owner: "moved into the library"), `os.Stat` succeeds, and it is not under any static protected prefix.
2. `T` exists at the Deluge path, `stat.Size == file.size` from the torrent, and the torrent's `file_progress[i] == 1.0` (fully downloaded).
3. `H := filehash.BookFileHash(T)` (head+tail+size; whole file below 100 MB; no decode, at most 20 MB read) equals one of, in order:
   - (a) `R.FileHash` — the library copy is byte-identical (never tagged since import);
   - (b) `R.OriginalFileHash` with `R.OriginalFileHashKind == FileHashKindSampled` — the pristine pre-tag digest (F11, F12);
   - (c) the earliest `FileEvent.Digest.SHA256Chunk` in `R`'s provenance chain whose `SizeBytes == file.size`.
   The match kind is written to `Evidence` ("hash equal: original_file_hash (sampled)").
4. No `book_file` row anywhere names `T`'s path or a path under the torrent's content directory (nothing still references the seeding copy). This is the E3 guard against the `move_storage` model (F9) and against rows that were never imported.

Not accepted: path equality alone (owner's rule), `original_file_hash` with empty `Kind` (F12), `post_metadata_hash` (post-write bytes). A pair with an empty `FileHash`, no sampled original and no provenance digest is `unverifiable` and the row is **held** with "no pristine hash on the row; run the hash backfill", not refused: the hold clears after a backfill.

**Fingerprint lane (future, not v1).** `AcoustIDSeg0`/`AcoustIDFingerprint` on `R` could be compared with a print of `T`, but a print needs a decode, which only the Mac lane may do (charter ban 6). A v2 may declare `Uses: []ops.Lane{ops.LaneMac}` and send `T` to the Mac workers for a print when (a)-(c) all fail. Listed in §7, not planned here.

### 3.4 Eligibility

A torrent row is `eligible` only when every one of these holds; the first failure is the row's `Hold`/`Skipped` reason:

| Rule | Condition | Hold reason (shown in the row) |
|---|---|---|
| E1 | `is_finished && progress == 100` and state in {Seeding, Paused, Queued}; not Downloading, Checking, Error, Moving | `not finished` / `state=<s>` |
| E2 | every audio file verified per §3.3 | `unlinked: n files` / `unverified: n files` / `unverifiable: n files` (Retry hold) |
| E3 | no file of the torrent is under `RootDir`, and no `book_file` names a path under the torrent's content dir | `torrent seeds from the library (deluge_move_enabled); refuse` |
| E4 | seeding rule §3.5 met | `seeding: ratio 0.63 < 1.00, 9d of 14d` (Retry hold) |
| E5 | `now - completed_time >= min_age_hours` (default 24 h; fallback `time_added` when `completed_time == 0`) | `completed <24h ago` (Retry hold) |
| E6 | Deluge reachable and `ProtectedPathCache.Loaded()` | plan fails fast with no rows |
| E7 | the torrent's save path is not under a static protected prefix (iTunes, `protected_paths`) | `under protected prefix` |
| E8 | no `delugecleanup:<hash>` ledger entry with `outcome=removed` | `already removed on <date>` (only possible if Deluge still lists it) |
| E9 | framework guards: no subject book is owner-manual or iTunes-only | framework `Skipped` |

Rows are classed `eligible`, `held` (Retry holds: E2 unverifiable, E4, E5) and `refused` (E1, E2 unlinked/unverified, E3, E7, E8). Only `eligible` rows can be approved.

### 3.5 Seeding rule and settings

Eligible when `ratio >= deluge_cleanup_min_ratio` **or** `seeded_for >= deluge_cleanup_max_seed_days`, whichever comes first, where `seeded_for = now - completed_time` (fallback `time_added`). `seeding_time` (libtorrent's active-seeding counter) is recorded in the row but not used for the threshold: a paused torrent should still age out at 14 days, which is what the owner asked for.

| Key | Type | Default | Meaning |
|---|---|---|---|
| `deluge_cleanup_enabled` | bool | `false` | The fixer registers only when true (like the plugin's client gate). Off until the first trial is reviewed. |
| `deluge_cleanup_min_ratio` | float | `1.0` | Ratio threshold. |
| `deluge_cleanup_max_seed_days` | int | `14` | Age threshold, from `completed_time`. |
| `deluge_cleanup_min_age_hours` | int | `24` | Never remove within this of completion, even at ratio (Q7). |
| `deluge_cleanup_label` | string | `deluge_discovery_label` | Only torrents with this label are listed; empty = all torrents. |
| `deluge_cleanup_remove_data` | bool | `true` | `remove_data` argument to Deluge. Owner-accepted default. `false` removes only the torrent entry. |

All six live in `internal/config/config.go` next to `deluge_move_enabled` (`:1671`), with viper defaults, and are exposed on `DelugeSettingsTab.tsx` (which today shows only `deluge_web_url`).

### 3.6 Apply and the journal

Per approved row, on one worker (PartitionBy = torrent hash):

1. The framework re-runs `Evaluate` (§3.2-3.5) and refuses the row on a fingerprint change (`changed_since_plan`). `Row.Inputs` are: torrent hash; the sorted list of (file path, size); each matched `book_file` id with the hash column that matched and its value; `library_ok=true`; `ratio_met`/`age_met` **as booleans**, not the raw ratio, so a ratio that keeps rising between trial and apply does not invalidate the plan.
2. `w.Effect(ctx, ops.EffectSpec{Kind: "deluge.remove_torrent", Subject: torrent hash, Books: row.Subjects}, fn)`. The Writer writes the intent row, then `fn`:
   - `ok, err := client.RemoveTorrent(hash, params.RemoveData)`; a `false`/error fails the row (nothing else happens);
   - post-check: `os.Stat` every library file of the row; a missing file is a **run-stopping** error (`OnItemError: Stop`), logged at error level with the hash and paths, and counted in `deluge_cleanup_removed_total{outcome="library_file_missing"}`; by F7 this cannot happen, and the check is the proof that it did not;
   - `fn` returns one history row per book: `change_type="deluge_cleanup"`, `field="deluge_torrent"`, `old=<hash>`, `new=""`, with `Before`/`After` JSON `{hash, name, save_path, files:[{path,size}], ratio, seeded_days, remove_data, bytes_freed}`;
   - the Writer records the history rows and clears the intent.
3. The ledger entry `delugecleanup:<hash>` → `{op_id, removed_at, remove_data, bytes_freed, books:[...], files:[...], outcome}` is written through the same Effect (it is the fixer's own key family, documented in `docs/database-pebble-schema.md`). It is what a later trial reads for E8 and what "what did we delete and why" answers from the store alone.
4. `AfterApply`: `ProtectedPathCache.Invalidate()` so the save path leaves the protected set now, not at the 5-minute TTL; enqueue nothing else.

**No `book_file` write.** `DelugeOriginalPath`, `DelugeHash` and `ImportedFromDelugeAt` stay as provenance (the file digest ledger says `TorrentHash` "is often the only link back to the pristine original", `file_provenance.go:94-97`). A "data removed" fact lives in the ledger, not on the row. `opstest.Conformance` proves Preview performs zero writes and every history row follows its write.

### 3.7 Concurrency and stand-down

- Apply holds the scan stand-down like every fixer (`engine.go:608`; v3 `rc.StandDown()`), because a scan that walks the Deluge directory mid-removal would mark rows missing. The fixer does **not** implement `NoScanStandDown`.
- The write-set gate (`Writes(Deletes(ResExternalData))` vs organize's `ResBookFiles`/`ResFiles`) keeps apply out of a running `library.organize`; `Exclusive` stays the def's own id (one apply at a time). The scan key is not touched (charter ban).
- Candidates (plan) is read-only and may run during a scan; it holds no stand-down.
- Deluge calls: `Network(2, "deluge web ui is single-threaded")`; hashing: CPU-bound, the framework's default pool, with `ItemTimeout: 10m` (a torrent with 80 files × 20 MB).

### 3.8 Failure handling

| Case | Plan | Apply |
|---|---|---|
| Deluge unreachable | run fails fast: "deluge unreachable: <err>", zero rows, `deluge_reachable=0` | every remaining row `retry_later`; nothing removed |
| Protected cache never loaded | run fails fast (`Loaded()==false` means unknown) | same |
| Torrent already gone at apply (`Load` not found) | — | row settles as `already_removed`; ledger written with `outcome=already_removed`; no history row |
| `remove_torrent` returns false / RPC error | — | row failed, `outcome=rpc_failed`; the next trial lists it again |
| Deluge removed the entry but reports data not deleted | — | row settles `outcome=removed_data_remains`; the next trial shows the directory as class `orphan_data`, **held, never auto-deleted** (the app deletes nothing on disk itself) |
| Library file missing after removal | — | run stops; error log + metric; owner alerted |
| Partial torrent (any audio file unlinked/unverified) | refused (E2) | re-evaluated: refused |
| Row changed since plan (new file in the torrent, row repointed) | — | `changed_since_plan` |
| Same torrent files shared by two torrents (cross-seed) | both rows list the other hash in `Evidence`; E3 passes; removing one with data deletes the other's files: the second row is then `refused: files missing` | first remove succeeds; second refused at re-evaluate |

### 3.9 Observability (OTel instruments per 11)

| Instrument (OTel name) | Kind | Attributes | Prometheus name |
|---|---|---|---|
| `audiobook_organizer.deluge_cleanup.candidates` | observable gauge (last plan) | `class` ∈ {eligible, held, refused, orphan_data} | `audiobook_organizer_deluge_cleanup_candidates` |
| `audiobook_organizer.deluge_cleanup.removed` | counter | `outcome` ∈ {removed, already_removed, rpc_failed, removed_data_remains, library_file_missing} | `..._removed_total` |
| `audiobook_organizer.deluge_cleanup.bytes_freed` | counter, unit `By` | — | `..._bytes_freed_bytes_total` |
| `audiobook_organizer.deluge.rpc` | counter | `method`, `outcome` | `audiobook_organizer_deluge_rpc_total` |
| `audiobook_organizer.deluge.rpc.duration` | histogram, unit `s` | `method` | `..._rpc_duration_seconds` |
| `audiobook_organizer.deluge.reachable` | observable gauge | — | `audiobook_organizer_deluge_reachable` |

Alert (PR 4): `deluge_cleanup_removed_total{outcome="library_file_missing"} > 0` → critical; `deluge_reachable == 0 for 30m` → warning. Panel on the overnight dashboard: candidates by class, removed per day, bytes freed.

### 3.10 UI placement

Review → Repairs lane, fixer **"Deluge copies already in the library"** (id `deluge.cleanup-after-organize`), description: *"Seeded torrents whose audio files all have a verified copy in the library. Approving a row removes the torrent and its downloaded data from Deluge once it has seeded to ratio 1.0 or for 14 days. Library files are never touched."* Row text: title = torrent name; `Members` = the books; `Current` = `{ratio: "1.23", seeded: "16d", files: "12 audio + 2 other", size: "1.4 GB"}`; `Proposed` = `{deluge: "remove torrent + data"}`; `Evidence` = one line per file ("`01.m4b` → book_file 01J… hash equal: original_file_hash (sampled)"), plus the hold reason. Class tallies in the lane header: eligible / held / refused / orphan data. The Settings → Deluge tab gains the six keys.

## 4. Implementation plan

| PR | Title | Files | Tests | Rollback | Size | Depends on |
|---|---|---|---|---|---|---|
| 0 | Deluge client: status fields + remove | `internal/deluge/client.go` (fields `ratio, seeding_time, time_added, completed_time, is_finished, files, file_progress, file_priorities, total_done` on `TorrentStatus`; `GetTorrentDetail(hash)`; `RemoveTorrent(hash, removeData) (bool, error)`; `ListTorrentsByLabelFiltered`), `client_test.go` | httptest JSON-RPC fake: field list sent, `remove_torrent` params, false result → error | revert; additive | S | — |
| 1 | Write the torrent link on import | `internal/importer/service.go` (`ImportFileRequest.TorrentHash`, passed to `CreateIngestVersion` and set on the new row's `DelugeHash`), `internal/server/deluge_discovery.go` (pass `req.TorrentHash`), `internal/deluge/import.go` (`ImportToLibraryWith` resolves the hash by `save_path` prefix from `ListTorrents` when `bookFile.DelugeHash==""` and sets it), `internal/versions/ingest.go` (doc), tests in each | import with hash → `GetBookVersionByTorrentHash` finds it; `DelugeHash` set; `MoveStorage` NOT called when `deluge_move_enabled=false` (regression for F9) | revert; fields already exist, no migration | M | 0 |
| 2 | `deluge.link-backfill` (Batch) | `internal/plugins/deluge/link_backfill.go` (new): Source = torrents; Item = §3.2 L2-L4 + §3.3 hash check, then `w.ModifyBookFile` sets `DelugeHash` (journaled; this op is the one place a row is written, and only that field); `plugin.go` (OperationDefs), `server/testdata/op_ids.golden` | opstest: preview writes nothing; live sets `DelugeHash` only on verified pairs; unverified pairs counted `skipped:unverified` | revert; `DelugeHash` is additive | M | 0, 05 PR 5-6 (or v2 `RunItems` with `Concurrency` set, if built before) |
| 3 | The fixer | `internal/plugins/deluge/cleanup_fixer.go` (new), `cleanup_eval.go` (resolution + verification, pure functions), `cleanup_ledger.go` (`delugecleanup:` keys), `internal/config/config.go` (6 keys), `internal/database/keyfamilies.go` + `docs/database-pebble-schema.md`, `internal/plugins/deluge/plugin.go`, `internal/server/testdata/op_ids.golden`, `pkg/ops` resource `ResExternalData` | opstest `Conformance`; table tests for E1-E9 (each hold reason once); E3 test: torrent under RootDir → refused; post-check test: fake client that deletes the library file → run stops, metric incremented; cross-seed test; `-race` with `Workers(4)` | revert; keys default off; ledger keys are additive | L | 0, 1, 05 PR 5, 6, 7 |
| 4 | Metrics + alert + panel | `internal/plugins/deluge/metrics.go` (new, OTel via `telemetry.Meter`), `deploy/prometheus/alert-rules.yml`, `deploy/grafana/dashboards/audiobook-organizer-overnight.json` | series names in the 11 contract golden; alert rule unit test (promtool in CI if present, else a YAML parse test) | revert | S | 3, 11 PR 1 |
| 5 | UI | `web/src/components/review/repairs/RowParts.tsx` (torrent row: file list, ratio/age chips), `web/src/components/settings/DelugeSettingsTab.tsx` (6 keys), `web/src/services/api.ts` types | Vitest for the row parts; Playwright: trial → rows → approve one → apply (fake Deluge) | revert | S | 3, 05 PR 10 |
| 6 | Docs + changelog + executive summary | `docs/AI-REFERENCE.md`, `changelog.d/`, `docs/executive-summaries/2026-MM-DD-deluge-cleanup-executive-summary.md`, this doc's status line | — | — | S | 3 |

Order: 0 → 1 → (2 ‖ 3 once 05 PR 5-6 land) → 4 → 5 → 6. PR 0 and 1 can ship this week; they are prerequisites that also fix F3-F5 for the existing discovery flow.

## 5. Risks and what must not break

- **Library files.** Guaranteed by F7 (reflink/copy semantics), E3 (no torrent under RootDir), §3.3 rule 4 (no row names the Deluge path) and the post-check that stops the run. Test: a fake client whose `RemoveTorrent` deletes the library file must stop the run with the metric incremented.
- **`deluge_move_enabled` model conflict (F9).** With hashes written (PR 1), enabling that key starts pointing torrents at the library; those torrents become permanently refused (E3) and their `save_path` protects the library directory from tag writes (F10). Q6 asks the owner to retire the key or accept that the two models exclude each other per torrent.
- **Hash column ambiguity (F12).** Only `Kind=sampled` is trusted; an empty kind is `unverifiable` (held), never a match.
- **Cross-seeded torrents** sharing files: the second removal is refused at re-evaluate because its files are gone; nothing in the library is affected. Listed in `Evidence` so the owner sees it at trial.
- **Deluge's `remove_data` scope.** libtorrent deletes the torrent's own files and then empty parent directories; it does not delete unrelated files in a shared `save_path`. Medium confidence from the docs; the first sandbox trial (`:8485` per the dedup-sandbox note) confirms before prod.
- **`original_file_hash` never recorded** for books imported before the sampled writer existed: those rows are held until the hash backfill runs. The trial report's `unverifiable` count is the owner's measure of how much backfill is needed; it is not a reason to loosen §3.3.
- **Protected cache TTL.** `Invalidate()` after apply; until then the removed save path is still "protected", which only makes tag writes to it refuse (harmless).
- **Never:** write `book_file` rows from the fixer, delete orphan directories, run during a scan without the stand-down, or split the scan key.

## 6. Dependencies

- **05 (ops v3):** Fixer kind, `Writer.Effect`, `Deletes`, `opstest.Conformance`, the catalog (`deluge.Ops` is already in sdk-api §11); resource name `ResExternalData` (or reuse `ResFiles`). PR 3 waits on 05 PR 5, 6, 7; PR 5 on 05 PR 10.
- **11 (metrics):** `telemetry.Meter` and the series contract golden (11 PR 1) before PR 4.
- **04 (census):** F5 shows `maintenance.bulk-deluge-import` and `GetBookFilesNeedingDelugeImportCore` are inert for new rows; the census owns that decision (prune or repair). This spec does not depend on it.
- **03 (Repairs lane):** row parts land on the Compact/Details/Grouped views shipped in `f7211eb39`.
- **D14c:** `RunVersionSwap` and `NotifyDelugeAfterVersionSwap` are kept; see Q6 for the model choice that decides whether they should ever call `move_storage`.

## 7. Open questions for the owner

| # | Question | Recommended answer |
|---|---|---|
| Q1 | Do non-audio sidecar files (`.nfo`, `.jpg`, `.cue`) block eligibility? | **No.** They are listed in the row as "discarded with the data". Only audio files (the scanner's extension set) must be verified. |
| Q2 | Which torrents are in scope: only the discovery label, or all? | **The label by default** (`deluge_cleanup_label` = `deluge_discovery_label`), with empty meaning all; the trial report shows the count either way. |
| Q3 | Must the book be in `organized` state, or is "every file under RootDir" enough? | **Under RootDir is enough.** The owner's words were "moved into the library"; state flags have reverted before (scanner-reverts-state note) and should not gate a deletion decision. |
| Q4 | When Deluge removes the entry but leaves data, should the app delete the directory? | **No.** The app never deletes files on disk; the trial lists `orphan_data` rows for the owner. |
| Q5 | Build a v2 `repairs.Fixer` now rather than wait for 05 PR 5-6? | **Wait.** The v2 Writer has no delete/effect method and no declared write set, so a v2 version cannot declare `Deletes` or be gated against organize; it would be rewritten in 05 12D. PR 0-1 ship now and are useful on their own. |
| Q6 | Copy model (this fixer) vs seed-from-library model (`deluge_move_enabled`, `path-update`, swap notify)? They exclude each other per torrent. | **Copy model.** Keep `deluge_move_enabled=false` (its current value), mark it deprecated in Settings with the sentence "torrents moved into the library are never cleaned up", and keep the notify code per D14c but behind that key. The end goal note says every book lives in the library folder; seeding from it would make the library a Deluge save path and protect it from tag writes. |
| Q7 | Add `deluge_cleanup_min_age_hours` (24 h) beyond the two owner thresholds? | **Yes.** A torrent that completes and hits ratio 1.0 within hours is common on a seeded tracker; 24 h gives the import, scan and hash backfill time to settle so the trial is verifying a finished import, not a moving one. |
| Q8 | Fingerprint lane for rows with no pristine hash? | **Later**, as a v2 with `Uses: LaneMac`, only if the `unverifiable` count after the hash backfill is still large. Measure first. |
