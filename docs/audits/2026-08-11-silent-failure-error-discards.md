<!-- file: docs/audits/2026-08-11-silent-failure-error-discards.md -->
<!-- version: 1.0.0 -->
<!-- guid: 3f0b6a41-9c2d-4e17-b8a3-6d51c7e04a92 -->
<!-- last-edited: 2026-08-11 -->

# Silent-failure audit — discarded errors across the Go backend

**Date:** 2026-08-11
**Scope:** `internal/`, `cmd/`, `pkg/` — non-test Go files at `origin/main` = `425958d6`
**Status:** READ-ONLY inventory. No source file was modified. No PR opened.

> User framing: *"we keep finding these, areas of the code where we discard the error
> message creating silent failures, so can you find all of them and fix that shit."*
> This document is the **find** half. The **fix** half is the wave plan at the end.

---

## 0. Method and exact measurements

Every count below is a literal `grep` result at `425958d6`, run inside a clean
worktree. The regexes are given so the numbers can be reproduced and so the
difference between this audit's numbers and any earlier estimate is explainable
rather than mysterious.

| # | Shape | Regex (applied to `internal/ cmd/ pkg/`, `_test.go` excluded) | Count |
|---|---|---|---|
| 1 | Statement-position blank assign | `^[[:space:]]*_ = ` | **1,125** |
| 2 | Tuple error discard | `^[[:space:]]*[A-Za-z_][A-Za-z0-9_.]*, _ :?= ` | **428** |
| 3 | Discarded unmarshal | `^[[:space:]]*_ = .*Unmarshal\(` | **28** |
| 4 | `if err != nil {` + next line exactly `return nil` | `grep -A1` then `grep -c 'return nil$'` | **20** |

### Reconciling with the numbers in the task brief

- Brief said 1,092 for shape 1; measured **1,125**. The regex here anchors on
  optional leading whitespace, so it also picks up `_ =` lines nested inside
  closures and `select` bodies that a stricter `^\t_ = ` anchor misses. Both
  numbers are "right" for their own regex; 1,125 is the number for the regex
  printed above.
- Brief said 416 for shape 2; measured **428**. Same cause: this regex allows a
  dotted receiver on the left (`s.Store, _ := …`), which the narrower one drops.
- Shapes 3 and 4 reproduce **exactly**: 28 and 20.

### What was measured

- The four grep shapes above, across all non-test `.go` files in `internal/`,
  `cmd/`, `pkg/`.
- Full source context (±10–16 lines) read by hand for every site cited in
  sections (a) through (e) below.

### What was NOT checked — do not read this audit as exhaustive

1. **`_test.go` files** — excluded by design.
2. **`web/`** — the whole React/TypeScript frontend. Swallowed `catch {}`,
   `.catch(() => {})`, and ignored non-2xx responses in the UI are a real and
   separate class of silent failure and are **not covered here at all**.
3. **`err != nil` blocks that log at DEBUG and continue.** These are not a grep
   shape — the error is technically "handled" — but at DEBUG level in production
   they are invisible. Not enumerated.
4. **Errors dropped through an interface boundary** — a method whose signature
   returns no error at all, so the discard happened at the type level and leaves
   no `_` to grep for. This is likely a large population and needs a
   signature-level review, not a text search.
5. **`errors.Is`/`errors.As` misclassification** — an error that IS checked but
   compared against the wrong sentinel and therefore takes the "benign" branch.
   Not searched.
6. **`recover()` sites** that swallow a panic without re-raising or recording.
   Not enumerated.
7. **Shape-2 sites individually.** 428 is too many to read one by one in this
   pass; sections below cite the ones surfaced by callee-frequency triage
   (`store.Get*`, `os.Stat`, `strconv.Atoi`, `json.Marshal`). The unreviewed
   remainder is called out in §7.

### Callee frequency — where the 1,125 actually live

This histogram is what makes the problem tractable: **54% of all statement-position
discards are progress/log reporting**, not data operations.

| Count | Callee | Bucket |
|---|---|---|
| 200 | `reporter.Log` | (f) reporting |
| 194 | `reporter.UpdateProgress` | (f) reporting |
| 143 | `progress.Log` | (f) reporting |
| 69 | `progress.UpdateProgress` | (f) reporting |
| 38 | `os.Remove` | (f) mostly benign cleanup |
| 28 | `json.Unmarshal` | **(a)** |
| 21 | `store.UpdateOperationError` | **(b)** |
| 17 | `c.ShouldBindJSON` | **(a)** |
| 16 | `b.Delete` (pebble batch) | **(b)** |
| 15 | `store.UpdateOperationStatus` | **(b)** |
| 15 | `store.CreateOperationResult` | **(b)** |
| 14 | `pm.scanStore.SavePhaseData` | **(b)** |
| 13 | `writeJSON` | (b) response write |
| 12 | `operations.ClearState` | **(b)** |
| 11 | `store.CreateOperationChange` | **(b)** — undo-log writes |
| 10 | `filepath.WalkDir` | **(c)** |
| 8 | `store.DeleteSetting` | (b) |
| 8 | `s.Store` | (b) |
| 7 | `store.SetSetting` | **(b)** |
| 7 | `orgSvc.db.CreateOperationChange` | **(b)** — undo-log writes |
| 7 | `operations.SaveCheckpoint` | **(b)** |
| 6 | `store.UpdateOperationV2Status` | (b) |
| 6 | `iter.Close`, 6 `g.Wait`, 6 `enc.Encode`, 6 `batch.Close` | mixed |
| 5 | `os.WriteFile` | **(b)** |
| 5 | `r.bus.Publish` | (b) |
| 5 | `mfs.activityService.Record` | (b) |

**606 of 1,125 (53.9%) are `reporter.Log` / `reporter.UpdateProgress` /
`progress.Log` / `progress.UpdateProgress`.** Those go in bucket (f) and should be
left alone — see §6 for why, and for the one caveat.

That leaves roughly **519 statement-position discards** that are not
progress-reporting, of which the sections below enumerate the ones with a
nameable consequence.

---

## (a) Discarded parse / unmarshal of external input

**Population: 28 `_ = *.Unmarshal(` + 17 `_ = c.ShouldBindJSON(` = 45 sites.**

This is the highest-severity bucket because the input is attacker- or
client-controlled and the failure mode is *not* "nothing happens" — it is "the
zero value happens", and in this codebase several zero values mean
**more** work, not less.

### (a.1) 🔴 CRITICAL — malformed body silently flips a dry-run to a real apply

These are the ones that hurt. In each, the params struct has a `DryRun` or an
"apply" gate; a body that fails to parse leaves it `false`/zero, and `false` is
the *destructive* setting.

| File:line | Dropped error | Consequence if it fires |
|---|---|---|
| `internal/server/maintenance_dispatcher.go:81` | `c.ShouldBindJSON(&req)` where `req = {DryRun bool}` | A malformed `{"dry_run": true}` body (trailing comma, wrong type, truncated upload) parses to `DryRun=false`. The dispatcher then enqueues `maintenance.job` **for real** at line 93–96. The client asked for a preview and got a mutation. The 202 response is identical either way, so the operator has no signal. |
| `internal/server/handlers/metadata/handler.go:1176` | `c.ShouldBindJSON(&req)` where `req = {Filter{LibraryState,AuthorID,SeriesID}, DryRun bool, Rename bool}` | Double failure. `DryRun` falls to `false` (real apply) **and** all three filter fields fall to `nil`, so the `else` branch at line 1193 runs `store.GetAllBooksCore(0, 0)` — **the entire library**. A request scoped to one author becomes an unfiltered whole-library metadata write. Same escalation shape as `library.organize`. |
| `internal/server/handlers/dedup/handler.go:968` | `c.ShouldBindJSON(&body)` on the **bulk merge** endpoint | Every filter (`EntityType`, `Status`, `Layer`, `MinSimilarity`, `MaxSimilarity`) zeroes. Defaults then fill `Status="pending"` and `EntityType="book"`, and the filter is issued with `Limit: 100000` (line 990). A request meant to merge one narrow layer's candidates becomes **bulk-merge every pending book candidate in the library**. Merges are the hardest operation in this system to undo. |

### (a.2) 🔴 CRITICAL — the 13 v2 operation `Run:` handlers

Every v2 op parses its params with the identical pattern:

```go
Run: func(ctx context.Context, rawParams json.RawMessage, reporter opsregistry.Reporter) error {
    var p someParams
    if len(rawParams) > 0 {
        _ = json.Unmarshal(rawParams, &p)   // <-- error discarded
    }
```

The `len(rawParams) > 0` guard makes this look defensive. It is not: it only
distinguishes "no params" from "params present". A params blob that is present
**and malformed** produces a fully zero-valued `p` and the op runs anyway.

| File:line | Op ID | Consequence if it fires |
|---|---|---|
| `internal/server/library_core_ops.go:193` | `library.organize` | `p.BookIDs` empty → the code path treats "no selection" as **organize the entire library**, with `CapFilesWrite` — this op moves files on disk. A malformed params blob escalates a 3-book organize into a whole-library file move. **This is the single worst site in the audit.** |
| `internal/server/library_core_ops.go:65` | `library.scan` | `p.FolderPath` nil and `p.ForceUpdate` false → scans the default root instead of the requested folder. Wrong-scope scan; wasted hours; `CapLibraryWrite`. |
| `internal/server/itunes_path_ops.go:107` | `itunes.path-reconcile` | `p.LegacyOpID` empty → `Reconcile(ctx, "", progress)` runs with **no operation ID**, so every subsequent `store.UpdateOperation*` call keyed on that ID no-ops. The op runs but reports no progress and no result; the UI shows a job that never finishes. `CapLibraryWrite`. |
| `internal/server/itunes_path_ops.go:136` | `itunes.path-repair` | Same `LegacyOpID` loss; repair mode/limit params zero, so the repair runs with default (widest) scope. |
| `internal/server/openlibrary_ops.go:49` | `openlibrary.download` | `p.Types` empty → the `for i, dumpType := range p.Types` loop at line 52 body **never executes**, then line 64 reports `"All downloads complete"` at 100%. The op reports **success having done nothing**. This is a false-green: an operator sees "downloads complete" and moves on. |
| `internal/server/openlibrary_ops.go:88` | `openlibrary.import` (second op in file) | Consequence not determined — not read in full in this pass. |
| `internal/server/diagnostics_ops.go:46` | `diagnostics.export` | `p.Category` / `p.Description` empty → the export is generated with an empty category (wrong or empty bundle), and `p.LegacyOpID` empty means the `UpdateOperationResultData` / `UpdateOperationStatus` calls at lines 64–65 write to a nonexistent op row. User downloads the wrong diagnostics bundle, or sees no result at all. |
| `internal/server/folder_autoscan_op.go:55` | `folder.autoscan` | `p.FolderPath` empty → `os.Stat("")` at line 65 fails → returns `"folder does not exist: "` with an empty path. Fails loudly, but with an error message that names no folder, so the operator cannot tell which autoscan broke. Lower severity: it fails rather than escalating. |
| `internal/plugins/maintenance/intro_migrate_single_file.go:122` | maintenance op | Consequence not determined. |
| `internal/plugins/maintenance/extract_wav_clips.go:56` | maintenance op | Consequence not determined. |
| `internal/plugins/maintenance/repair_transcribe_status.go:216` | maintenance op | Consequence not determined. |
| `internal/plugins/maintenance/intro_transcribe.go:127` | maintenance op | Consequence not determined. |
| `internal/plugins/maintenance/auto_match_transcribed.go:62` | maintenance op | Consequence not determined. |
| `internal/plugins/dedup/embed_scan.go:71` | `dedup.embed-scan` | Consequence not determined; note this op writes embeddings, so a zeroed batch/limit param plausibly means a full re-embed. Verify before fixing. |

> ⚠️ **A worktree already exists for this**: `../audiobook-organizer-opsparams`
> on branch `fix/ops-params-silent-unmarshal`. Any wave that touches these files
> must coordinate with it or it will conflict. Confirmed via `git worktree list`.

### (a.3) 🟡 Real but lower blast radius

| File:line | Dropped error | Consequence |
|---|---|---|
| `internal/server/handlers/system/handler.go:510` | `ShouldBindJSON(&req)` where `req = {MaxBackups *int}` | Malformed body → `MaxBackups` nil → falls back to `DefaultBackupConfig()`. Caller asked to retain N backups; a different N is silently used, and **backup rotation may delete backups the caller intended to keep**. Data-loss-adjacent. |
| `internal/server/handlers/entities/handler.go:422` | `ShouldBindJSON(&req)` where `req = {Names []string}` | Malformed body → `Names` empty → falls through to `dedup.SplitCompositeAuthorName(author.Name)` **auto-detect**. The caller supplied explicit split names; the server splits the author a different way and returns 200. Author records are mutated in a way the caller did not ask for. |
| `internal/server/handlers/metadata/handler.go:764` | `ShouldBindJSON(&body)` where `body = {SegmentIDs []string, Rename *bool}` | `SegmentIDs` empty → operates on all segments instead of the requested subset; `Rename` nil → default rename behaviour, which touches filenames. Scope escalation on a file-renaming path. |
| `internal/server/handlers/metadata/handler.go:478` | `ShouldBindJSON(&body)` — search params | All search terms empty → the cache key at line 483 is built from empty strings, so a malformed search **poisons the persistent metadata cache** under a key that looks like a legitimate empty query. Subsequent real searches can hit that poisoned key. |
| `internal/server/handlers/ai.go:578` | `ShouldBindJSON(&reqBody)` — `{Mode string}` | `Mode` empty → defaults to `"groups"`. Caller asked for `"full"`, silently gets the cheaper mode. Correct-looking 200 with a narrower result. |
| `internal/server/handlers/ai.go:609` | `json.Unmarshal(groupsJSON, &dedupGroups)` | Cached dedup groups fail to decode → `dedupGroups` stays empty → line 613 `if len(dedupGroups) == 0` recomputes inline. **This is also a bucket (e) zero-result fallback** — see §5. Consequence: a corrupt cache entry is never reported, just silently paid for with a full recompute every request. |
| `internal/server/handlers/abs/refresh.go:317` | `json.Unmarshal(raw, &body)` in `readJSONBody` | A malformed ABS client body yields a `nil` map that is then **cached under `abs_json_body`** (line 320) and returned to every caller in the request. Every downstream field read sees "absent" rather than "malformed". Affects the Audiobookshelf-compatible API surface, i.e. third-party mobile apps. |
| `internal/deluge/client.go:233` | `json.Unmarshal(result, &connected)` | The Deluge daemon's connected-state reply is unparseable → `connected` stays `false` → the client behaves as if the daemon is disconnected. Torrent-side operations silently no-op with no log line saying why. |
| `internal/database/pebble_quick_queries.go:105` | `json.Unmarshal(val, &entry)` | A corrupt stored row decodes to a zero-valued `entry` that is then returned as if it were real data. **Corruption presented as valid data** — the worst possible failure mode for a store layer. |
| `internal/database/pebble_store_authors.go:701` | `json.Unmarshal(val, &nextID)` | The author ID sequence counter fails to decode → `nextID` = 0 → **ID reuse / collision on the next author insert**. Potential cross-linking of unrelated authors. High severity despite being one line. |
| `internal/database/pebble_activity_store.go:635, 897, 901` and `internal/database/nuts_activity_store.go:671, 924, 928` | `json.Unmarshal` of `DigestDetails` | A corrupt digest-details blob renders as an empty digest in the activity feed. Cosmetic-to-moderate: the user sees an activity entry with no detail and no indication it was corrupt. |
| `internal/reconcile/itunes_heal.go:675` | `json.Unmarshal(params, &cp)` — **checkpoint** | Corrupt checkpoint → zero-valued resume point → the heal **restarts from the beginning** rather than resuming, or worse, resumes at index 0 and re-applies work already applied. Silent duplicate work on an iTunes-writing path. |
| `internal/plugins/deluge/centralization.go:61` | `json.Unmarshal(params, &checkpoint)` | Same checkpoint shape, same consequence, on the centralization path. |
| `cmd/verify-suite/main.go:36` | `json.Unmarshal(b, &info)` | Dev tool only. Low. |

### (a.4) ✅ Correct and deliberate — leave these alone

These already carry a comment explaining the discard, and the reasoning holds:

| File:line | Why it is correct |
|---|---|
| `internal/server/handlers/abs/play.go:184` | Comment at 180–182: the parsed fields (`forceTranscode`, `mediaPlayer`) are hints the server never acts on, and a 400 would make the book look unplayable to an ABS client. Ignoring is the right call. |
| `internal/server/handlers/abs/play.go:375` | Same file, sync endpoint; `defer respondPlainOK(c)` at 368 with an explicit §1.8.6 protocol note. Deliberate. |
| `internal/server/handlers/dedup/handler.go:427` and `:455` | Commented "missing body → dry-run (apply=false)". Here the zero value is the **safe** direction — the inverse of the (a.1) sites. Correct. |
| `internal/server/handlers/dedup/handler.go:1352` | Commented back-compat; and the code immediately validates `keepID` against the candidate pair at 1354 and 400s on mismatch. Correct. |
| `internal/server/handlers/review/replay.go:59` | Commented "no body means a dry run over every kind" — zero value is the safe direction. Correct. |
| `internal/server/itl_rebuild.go:200` | Commented "empty body = all books"… **but** this is a rebuild of the iTunes library file, and "all books" is the widest possible scope. The comment documents the behaviour; it does not make a malformed body's escalation safe. Reclassify as 🟡 — the intent is deliberate, the malformed-input case was probably not considered. |
| `internal/server/deluge_discovery.go:120` | Commented "optional body"; discovery is read-only. Correct. |
| `internal/server/handlers/split_book.go:121` | Commented "body is optional". Consequence of a zeroed body not verified — flag for a second look given `split_book` mutates. |

**Bucket (a) totals: 45 sites — 16 critical, 16 real-but-lower, 8 correct-and-deliberate, 5 consequence-not-determined.**

---

## (b) Discarded write / persist errors

**Population (statement-position `_ =` on a call that writes state):**

| Count | Callee family | Files |
|---|---|---|
| 22 | `CreateOperationChange` (**the undo log**) | 8 files |
| 21 | `store.UpdateOperationError` | many |
| 15 | `store.UpdateOperationStatus` | many |
| 15 | `store.CreateOperationResult` | many |
| 14 | `scanStore.SavePhaseData` | `internal/aiscan/pipeline.go` (all 14) |
| 12 | `operations.ClearState` | 8 files |
| 7 | `operations.SaveCheckpoint` | 5 files |
| 16 | `b.Delete` / `tx.Delete` / `tx.Put` (pebble batch) | `internal/database/*` |
| 11 | `os.WriteFile` / `os.Rename` / `os.Chmod` / `os.Link` / `copyFile` | 8 files |
| 8 + 7 | `store.DeleteSetting` / `store.SetSetting` | several |

### (b.1) 🔴 CRITICAL — discarded **rollback** and **restore** errors

A failed rollback whose error is discarded is strictly worse than no rollback at
all, because the returned error tells the caller a *different* story than what is
on disk.

| File:line | Dropped error | Consequence if it fires |
|---|---|---|
| `internal/fileops/safe_operations.go:113` | `copyFile(op.backupPath, op.targetPath)` — the rollback after a failed `copyFile(original → target)` | The target file is **already partially overwritten** at this point. If the restore also fails, the function returns `"failed to copy file: …"` — an error that reads like "nothing happened". The caller (organizer, itunes importer) sees a copy failure and moves on, leaving a **truncated or half-written audiobook file at `targetPath`** with the user's original backup still sitting at `backupPath` and nothing pointing at it. Silent data corruption. |
| `internal/fileops/safe_operations.go:136` | Same `copyFile` restore, this time after a **checksum mismatch** | Identical, and worse in intent: the code just proved the target is corrupt (`checksum mismatch`), tries to restore, and ignores whether the restore worked. Returns `"checksum mismatch: operation failed integrity check"`. A known-corrupt file is left in place while the error message implies the operation was rejected. |
| `internal/itunes/itl_safe_write.go:285` | `os.Rename(backupPath, path)` — restoring the original iTunes `.itl` after the atomic rename failed | The comment on line 284 says it exactly: *"original is otherwise lost."* If the restore rename fails, the live iTunes library file **does not exist at `path` at all** — it is sitting at `path + ".bak-<timestamp>"`. The returned error claims `"(restored original)"`, which is a **false statement in the error string itself**. Given the July-5 iTunes corruption incident, this is the highest-value single fix in the audit. `books/itunes/**` is hands-off per project rules, which makes an undetected loss here maximally expensive. |

### (b.2) 🔴 HIGH — the undo log (22 sites)

`CreateOperationChange` is how organize / rename / dedup-merge record what they
did so the operation can be undone. Every one of the 22 call sites discards the
write error.

Sites: `internal/organizer/rename.go:183, 216, 258`;
`internal/organizer/service.go:636, 656, 677, 690, 775, 1007, 1016`;
`internal/server/entities_ops.go:128, 162`;
`internal/server/duplicates_helpers.go:189, 205, 243, 264`;
`internal/server/handlers/organize.go:279`;
`internal/dedup/series_dedup.go:441, 518, 541, 586`;
`internal/dedup/book_dedup.go:462`.

**Consequence:** the file has already been moved on disk (`rename.go:205` moves
it, *then* line 216 records the undo entry). If the undo-log write fails, the
move succeeded and is now **permanently unundoable** — and nothing anywhere says
so. The user later clicks Undo, the system replays the changes it *did* record,
and produces a **partial undo**: some files restored, some left at their new
paths, with no error. A partial undo of a file-move operation is worse than no
undo, because the library is now in a state neither the user nor the system has
a record of.

`internal/dedup/*_dedup.go` sites are the same shape after a **merge**, which is
the least reversible operation in the product.

### (b.3) 🟠 MEDIUM–HIGH — checkpoint / resume state (19 sites)

`operations.SaveCheckpoint` (7) + `operations.ClearState` (12) + `SaveOperationParams`.

Sites include `internal/itunes/service/importer.go:407, 417, 497, 504, 509`;
`internal/server/metadata_ops.go:861, 915`;
`internal/maintenance/jobs/recompute_book_aggregates.go:170, 185`;
`internal/maintenance/jobs/backfill_file_hashes.go:78, 84`;
`internal/server/server_lifecycle.go:105, 114, 169, 233, 249`;
`internal/itunes/service/path_repair.go:472`; `path_reconcile.go:152`;
`internal/organizer/service.go:139, 142`.

**Consequence, `SaveCheckpoint` failing:** a long-running op (iTunes import,
bulk write-back, hash backfill — these run for hours) crashes or is restarted,
resumes from a **stale checkpoint**, and **re-does work already done**. For
`backfill_file_hashes` that is wasted hours. For `itunes_import` at line 407,
re-processing already-imported groups is how duplicate book rows get created.

**Consequence, `ClearState` failing:** the op finished, but its checkpoint row
survives. On next startup `server_lifecycle.go` sees leftover state for a
completed operation and can **resume an operation that already completed**. The
five `server_lifecycle.go` sites are precisely the recovery path, so a discard
there means recovery silently fails to clean up after itself, compounding every
restart.

### (b.4) 🟠 MEDIUM — AI scan phase data (14 sites, all one file)

`internal/aiscan/pipeline.go:393, 404, 444, 455, 508, 518, 541, 551, 605, 683, 721, 725, 881, 894`
— every `SavePhaseData` call discards its error.

**Consequence:** the phase's input/output/suggestions are not persisted, but the
very next line (`UpdatePhaseStatus(..., "complete", "")`, e.g. line 394) marks
the phase **complete anyway** and that error *is* checked. The result is a scan
recorded as successfully complete with **no data attached** — the UI shows a
finished AI scan with zero suggestions, indistinguishable from "the AI found
nothing". The user acts on "no duplicates found" when the truth is "we failed to
save what we found." This is the most expensive-to-diagnose shape in the audit.

Note lines 393/444/508/541 additionally discard `json.Marshal` errors on the line
above (`emptySuggestions, _ := json.Marshal(...)`) — bucket (c) overlap.

### (b.5) 🟡 MEDIUM — operation status/result/error reporting (≈57 sites)

`store.UpdateOperationError` (21), `store.UpdateOperationStatus` (15),
`store.CreateOperationResult` (15), `store.UpdateOperationV2Status` (6).

**Consequence:** the operation itself succeeds or fails correctly, but the row the
UI reads to render the activity bell / operations list is not updated. The job
appears **stuck at its last-reported percentage forever**, or a failed job never
shows its error text. Users then re-trigger the "stuck" job — which for
non-idempotent ops (organize, merge) means doing it twice.

Special case: `internal/server/diagnostics_ops.go:58, 64, 65` write to
`p.LegacyOpID`, which as noted in (a.2) may itself be an empty string from the
discarded unmarshal. Two silent failures stacked.

### (b.6) 🟡 MEDIUM — settings and filesystem writes

| File:line | Dropped error | Consequence |
|---|---|---|
| `internal/versions/fs.go:137` | `os.Chmod(dst, srcInfo.Mode())` | Copied version file keeps default perms. Per the project's Linux-ACL rule (`0775`/`0664` required or ACLs silently fail), a wrong mode here means a later process **cannot write the file** and fails for an unrelated-looking reason. |
| `internal/itunes/itl.go:1049` | `os.Chmod(path, 0664)` | Explicitly commented `best-effort — may fail if not owner`. ✅ **Correct and deliberate**, given the comment names the expected failure. |
| `internal/plugins/maintenance/extract_wav_clips.go:147` | `os.Link(dest, contentDest)` | Hardlink to the content-addressed location not created; the clip exists only at `dest`. Later lookups by content hash miss it and re-extract. Wasted work, not loss. |
| `store.SetSetting` (7) / `store.DeleteSetting` (8) | write failure | A settings change the user made in the UI is not persisted, while the UI shows it applied. On next reload the old value returns — reads to the user as "the app forgot my setting". |
| `cmd/dedup_bench*.go` (5 × `os.WriteFile`) | benchmark artifact writes | Dev-tooling only. 🟢 Low. |

**Bucket (b) totals: ≈133 sites reviewed by family — 3 critical, 22 high (undo log), 19 checkpoint, 14 aiscan, ≈57 op-status, ≈18 settings/fs.**

---

## (c) Errors swallowed into an empty / zero result the caller cannot distinguish from "legitimately nothing"

This is the largest bucket and the hardest to fix mechanically, because the fix
is usually a **signature change**, not a one-line edit.

Two shapes feed it:

- **Shape 4** — `if err != nil { return nil }`: **20 sites, all read.**
- **Shape 2** — `x, _ := store.GetSomething(...)`: **428 sites**, of which ~170 are
  `Get*`/`List*`/`Count*` calls on a store. **Not all read** — see §7.

### (c.1) 🔴 CRITICAL — a corruption guard that fails OPEN

| File:line | Dropped error | Consequence |
|---|---|---|
| `internal/itunes/service/importer.go:58` | `os.Stat(itlPath)` error → `return nil` | This function is the **ITL-conflict check**: it exists to refuse a write when the `.itl` was modified underneath us since our last read. `return nil` here means **"no conflict"**. If the stat fails for any reason (permissions, NFS blip, path race), the guard reports all-clear and the writer proceeds to overwrite the live iTunes library file. This is a safety check that fails **open**, on the exact file the project rules mark hands-off, in the exact failure class as the July-5 corruption. It should fail closed. |

### (c.2) 🔴 HIGH — "nothing found" that is actually "the lookup broke"

| File:line | Dropped error | Consequence |
|---|---|---|
| `internal/scanner/scanner.go:1637` (`findPlaylistGroupings`) | `os.ReadDir(dirPath)` → `return nil` | No CUE/M3U groupings found → **each audio file in the directory is imported as its own separate book** instead of being grouped into one multi-file book. This is precisely the library-fragmentation shape this project has fought before. A transient directory-read error permanently fragments a book. |
| `internal/scanner/scanner.go:1597` (`parseM3UFile`) | `os.ReadFile(m3uPath)` → `return nil` | The playlist exists but is unreadable → returns "references no files" → the grouping it defines is silently dropped, same fragmentation outcome. |
| `internal/metadata/assemble.go:273` (`listAudioFiles`) | `os.ReadDir(dirPath)` → `return nil` | Directory read fails → "this book has no audio files" → assembly/agree-title logic sees an empty set and reaches a wrong conclusion about the book with no error. |
| `internal/metabatch/candidates.go:83` (`LatestMatchedBookIDs`) | `store.GetRecentOperations(5000)` → `return nil` | Returns "no book has been matched yet". The caller uses this to **exclude already-matched books** from a batch metadata fetch. A nil return therefore means **re-fetch metadata for the entire library** — thousands of external API calls, rate-limit burn, and possible re-overwrite of already-good metadata. Scope escalation from a read error. |
| `internal/maintenance/jobs/relink_report.go:143` and `internal/maintenance/jobs/relink_missing_to_itunes.go:232` | `os.ReadDir(iTunesRoot)` → `return nil` | The relink candidate search returns "no matching directories". The report then says **there is nothing to relink**. A permissions or mount hiccup on the iTunes root makes a report claim the library is clean. The operator closes the ticket. |
| `internal/server/duplicates_helpers.go:313` (`computeSeriesNormalizeActions`) | `store.GetAllSeries()` → `return nil` | The series-normalize **preview** returns an empty action list. The UI renders "no changes needed". Same false-clean. |
| `internal/itunes/pid_repair.go:197` (`fetchFullBookFile`) | `store.GetBookFiles()` → `return nil` | The PID repair silently skips that book file. Repair reports fewer repairs than it should, with no failure count. |
| `internal/openlibrary/store.go:384` (`editionsForWork`) | `LookupWork` → `return nil` | "This work has no editions" instead of "the OL store lookup failed". Metadata match quality silently degrades. |
| `internal/tools/ollama_daemon.go:100` (`StopWhenIdle`) | `d.readPID()` → `return nil` | Returns **success** from a stop function without stopping anything. The Ollama process keeps running and holding GPU memory; the caller believes it shut down. |
| `internal/httputil/parse.go:37` (`ParseQueryIntPtr`) | `strconv.Atoi` → `return nil` | `?limit=abc` is treated identically to `limit` not being supplied — the caller's default applies. Minor, but it means a client typo silently changes pagination rather than 400ing. Consequence is bounded; listed for completeness. |
| `internal/server/handlers/abs/refresh.go:299` | `json.Marshal(raw)` → `return nil` | `deviceInfo` silently dropped from an ABS session. Session telemetry incomplete; no user-visible break. 🟢 Low. |

### (c.3) ✅ Correct and deliberate in shape 4

| File:line | Why it is correct |
|---|---|
| `internal/search/bleve_translator.go:115` | Comment at 111–112 states it explicitly: nil means "cannot tell" and every caller degrades to the previous behaviour rather than to a wrong answer. Textbook. |
| `internal/database/pebble_store_bookfiles.go:768` | Commented `ErrNotFound for pre-index rows; fall back to the scan` — the nil is a documented control-flow signal. |
| `internal/database/pebble_store.go:213` (`readCachedLibraryStats`) and `internal/database/pebble_quick_queries.go:137` (`readCachedQuickQuery`) | Both are cache reads whose nil means "cache miss → recompute". Failing open to a recompute is the safe direction. ✅ **But note** the companion discard at `pebble_quick_queries.go:105` (`_ = json.Unmarshal(val, &entry)`) is **not** safe — see (a.3). Same file, opposite verdict. |
| `internal/server/cache_snapshotter.go:25` | Prometheus gather failure → no snapshots this tick. Metrics-only. |
| `internal/database/pebble_store_bookfiles.go:793` | Iterator-create failure in an explicitly-documented O(N) legacy fallback path. Low. |
| `internal/maintenance/jobs/cleanup_organize_mess.go:52` | `filepath.Walk` callback returning nil on a per-entry error = "skip this entry, keep walking". Standard and correct **as a walk policy** — but it is uncounted, so see (d). |

### (c.4) 🟠 The shape-2 store-read discards worth naming

Selected from the ~170 `x, _ := store.Get*` sites. Each returns a nil/empty value
that the following code reads as a definitive negative fact.

| File:line | Dropped error | Consequence |
|---|---|---|
| `internal/importer/collision.go:70` | `store.GetBookByFileHash(hash)` | `existing == nil` reads as **"this file is not already in the library"**, so no `file_hash` collision candidate is appended (line 72). The pre-import collision check therefore **fails to warn** that the file is already present, and the user confirms an import that creates a duplicate. Line 93 (`books, _ := store.GetAllBooksCore(0, 0)`) does the same for the title-match check — a read error there suppresses **every** title collision candidate. |
| `internal/merge/collision.go:26` | `store.GetBookByID(id)` | This is the `BookTitle()` display helper — a read error yields an **empty title string** in the collision report. The user is shown a collision against a book with a blank name. Cosmetic-to-confusing; it does **not** affect whether the merge proceeds. 🟢 Lower than it first appears. |
| `internal/undo/engine.go:309, 356` | `store.GetBookByID(c.BookID)` | This is the undo **conflict preflight**, not the apply. A read error makes `book == nil`, which the code reports as `Reason: "book deleted"` (line 313). The user is told, falsely and specifically, that their book was deleted — and the change is listed as a conflict rather than replayed. A transient store error is rendered as a **destructive-sounding fact**. Pairs badly with (b.2), where the undo log itself may already be incomplete. |
| `internal/database/pebble_store_playback.go:107` | `p.GetUserBookState(...)` (`prev`) | `prev` is used only at line 113 to delete the **stale status secondary-index entry**. The state row itself is written unconditionally at line 109, so the position is *not* lost. The consequence is a **leaked index entry**: the book stays indexed under its old status forever, so filtering by status ("Finished", "In Progress") returns it under **both** statuses. Wrong counts and duplicate rows in filtered views. |
| `internal/readstatus/readstatus.go:144` | `store.GetUserBookState(...)` | A read error makes `existing` nil → line 150 builds a **fresh** `UserBookState` instead of copying the existing one → the subsequent write **discards every previously-set field** on that state (progress, manual-status flag, timestamps). This one *is* user-visible state loss. |
| `internal/readstatus/readstatus.go:52` | `store.GetUserBookState(...)` | Error → `existing` nil → if the book also has no positions, line 55 returns "nothing to record" and the recompute is skipped. A book with real state is treated as having none for that call. |
| `internal/itunes/service/position_sync.go:85, 118` | `GetUserPosition` / `GetUserBookState` | These guards are `if existing != nil { continue }` — i.e. "don't overwrite what the user already has". A read error makes `existing` nil, so the guard **does not fire** and the code seeds the position/status from the iTunes bookmark, **overwriting the user's real playback position**. The discard defeats the exact protection the line was written to provide. Line 91 (`files, _`) additionally causes a silent skip when the file list fails to load. |
| `internal/server/bootstrap.go:435` | `store.GetRoleByName("admin")` | The nil case is **intentional** per the comment at 433–434: fall back to the literal role name `"admin"`. But an *error* is conflated with *absent*. If the real admin role exists under a different ID, the bootstrapped admin user is created with a **dangling role reference** (line 441), i.e. an admin account with no resolvable permissions. Consequence: operator lockout on first boot. Deliberate-by-comment for the absent case; not considered for the error case. |
| `internal/server/handlers/auth.go:517, 519` | `GetRoleByName` / `GetRoleByID` | A role assignment silently does not happen. The API returns success. Privilege silently absent. |
| `internal/quarantine/service.go:240` and `internal/database/pebble_store_quarantine.go:95` | `GetScanFailCount(...)` | Error → count 0 → the book **never crosses the quarantine threshold**. A book that fails to scan repeatedly is never quarantined, so it retries forever. |
| `internal/config/persistence.go:867` | `store.GetSetting("maintenance_window_migrated")` | Error → "not migrated" → the migration **re-runs on every start**. |
| `internal/dedup/engine.go:306, 513, 1896, 1949, 4059, 4096` | `GetBookByID` / `GetBookFiles` / `GetBookFileByAcoustID` | A zero-valued book/file is fed into the similarity scorer. The pair is scored against empty title/author/duration fields, producing a **wrong dedup verdict** — either a missed duplicate or a false positive that a human then approves. |
| `internal/database/pebble_store_stats.go:471` | `p.GetAllBooksCore(0, 0)` | Dashboard statistics computed from an empty book list → the UI shows **0 books**. Alarming and wrong; the user assumes data loss. |
| `internal/sysinfo/service.go:132` and `internal/diagnostics/service.go:280–282` | `GetDashboardStats` / `CountPrimaryBooks` / `CountAuthors` / `CountSeries` | Same: zeros rendered as facts. |
| `internal/server/handlers/operations/handler.go:131, 137, 148` | `c.GetRawData()` | **Also bucket (a)** — the raw request body is read with the error discarded, then parsed. A truncated upload becomes an empty body. |

**Bucket (c) totals: 20 shape-4 sites (all read: 1 critical, 10 high/medium, 6 correct-and-deliberate, 3 low) + ~170 shape-2 store reads, of which 25 named above.**

---

## (d) `continue` / `break` on error inside a loop, with no counter and no log

**Measured: 78 sites** matching `if err != nil {` (optionally `|| x == nil`)
immediately followed by a bare `continue`, across 78 distinct locations in
~60 files.

The defining property: the loop finishes, reports a success count, and **the
denominator is silently wrong**. Nothing anywhere records how many items were
skipped or why. "Processed 4,812 books" is indistinguishable from "processed
4,812 books and silently skipped 300."

### (d.1) The confirmed anchor site

`internal/server/server.go:822` (the brief cited :820; the `if` is at **822**,
the `continue` at **823**):

```go
dbBook, err := server.store.GetBookByFilePath(books[i].FilePath)
if err != nil || dbBook == nil {
    continue
}
```

This is inside `AutoOrganizeFn`, the post-scan auto-organize hook. A DB lookup
error and a genuinely absent book are collapsed into the same branch. Neither is
counted nor logged, and the loop maintains an `organized` counter (line 816) that
only counts successes. **Consequence:** after a scan, some newly-scanned books are
silently never auto-organized. The user sees "organized N" and has no way to learn
that M books were skipped because the store errored. The two failure modes need
opposite responses — an absent book is normal, a store error means auto-organize
should probably abort — and the code cannot tell them apart. Note the *very next*
error in the same loop (line 826) **is** logged, which shows the omission is an
oversight rather than a policy.

### (d.2) Same shape, notable sites

Every one of these collapses "lookup failed" into "row doesn't exist":

| File:line | Loop |
|---|---|
| `internal/server/folder_autoscan_op.go:94, 123` | post-autoscan per-book handling — same hook shape as (d.1) |
| `internal/server/handlers/filesystem.go:283, 287` | per-path DB resolution during a filesystem browse |
| `internal/reconcile/itunes_heal.go:323, 327` | **iTunes heal** — a book/bookfile that fails to load is silently not healed; the heal reports success |
| `internal/dedup/engine.go:601` | `otherBook == nil` → the candidate pair is silently dropped from scoring |
| `internal/operations/registry/watchdog.go:74` | the **watchdog** skips an op row it cannot read — the watchdog is the thing that is supposed to notice stuck ops |
| `internal/server/middleware/absauth.go:627` | role lookup fails → the ABS auth loop skips that role. Security-relevant: a permission is silently not granted (fails closed, which is the safe direction, but invisibly). |
| `internal/database/embedding_store.go:831, 872, 986, 1033, 1215, 1293, 1347` | **7 sites in one file** — embedding rows that fail to decode are dropped from every vector scan. Silently shrinks the candidate set the dedup engine searches, which is the exact "losing more index data" hazard from bucket (e). |
| `internal/database/pebble_store.go:559, 2738, 3067`, `pebble_store_works.go:197`, `pebble_store_authors.go:877, 885`, `pebble_store_tags.go:609`, `pebble_store_playlists.go:404`, `pebble_store_playback.go:171`, `pebble_store_itunes.go:36`, `pebble_store_externalids.go:90`, `pebble_store_bookfiles.go:745, 1569`, `pebble_store_auth.go:349`, `pebble_store_abssession.go:234` | **15 sites** — list/iterate methods on the primary store. A corrupt or unreadable row is dropped from the returned slice with no error and no count. **Every list endpoint in the product can silently under-report.** This is the single highest-leverage cluster in bucket (d). |
| `internal/backup/backup.go:312` | ⚠️ **Corrected on verification.** This is `ListBackups`, not the backup *writer*: `entry.Info()` fails → that archive is omitted from the returned list. Consequence: an existing `.tar.gz` backup becomes **invisible in the UI**, and any retention/rotation logic driven off this list does not count it. Line 317 (`checksum, _ := calculateFileChecksum(...)`) additionally reports an **empty checksum as if it were computed**. 🟡 Medium — not the "incomplete backup" it looks like at first glance. |
| `internal/covers/covers.go:113`, `internal/covers/history.go:54` | cover files skipped silently |
| `internal/maintenance/jobs/backfill_book_files.go:48`, `bulk_fetch_metadata.go`, `revert_metadata_fetch.go:64` | maintenance jobs under-report their denominators |
| `internal/mtls/config.go:161, 165, 169` | ⚠️ **Corrected on verification.** This is `CheckCertExpiry`, not cert loading. A cert that cannot be read (161), PEM-decoded (165), or X.509-parsed (169) is skipped, so it produces **no expiry warning**. Consequence: an expiring or already-expired certificate is silently never warned about, and mTLS breaks at renewal time with zero advance notice. The function returns an empty warning list, which reads as "all certificates are healthy". Textbook bucket-(c) false-clean, filed here because the mechanism is a bare `continue`. |
| `internal/itunes/plist_parser.go:347`, `internal/itunes/mhoh_encoding_audit.go:265`, `internal/itunes/library_activity.go:54`, `internal/itunes/service/transfer.go:182` | iTunes parse/transfer loops drop entries |

### (d.3) Related: discarded worker-pool results

| File:line | Dropped error | Verdict |
|---|---|---|
| `internal/reconcile/reconcile.go:196, 297, 889, 1379` | `_ = g.Wait()` | ✅ **Correct and deliberate** — each carries an inline comment stating the policy ("per-file hash errors are skipped", "counted, not fatal"). The policy is stated; the counting claim should be spot-checked but the discard is intentional. |
| `internal/server/handlers/abs/userdata.go:520` | `_ = g.Wait()` | ✅ Commented "no goroutine returns an error". Correct. |
| `internal/itunes/pid_repair.go:311` | `_ = g.Wait()` | 🟠 **No comment.** Every per-book error inside the PID-repair pool is discarded with no stated policy. Consequence: PID repair reports completion while an unknown number of books failed. |
| `internal/server/handlers/metadata/handler.go:866`, `internal/metadata/enhanced.go:247, 813` | `_ = registry.RunItems(...)` | 🟠 The pool's aggregate error is dropped. `enhanced.go` is the tag-writing path, so a worker that failed to write tags to a file is invisible. |
| `internal/fingerprint/fpcalc.go:293`, `internal/audio/sample.go:91, 97` | `_ = cmd.Wait()` on ffmpeg | 🟡 Subprocess exit status discarded. A failed ffmpeg invocation is treated as a successful one; the caller then reads a truncated or absent output file. Consequence depends on downstream checks — **not verified in this pass**. |

**Bucket (d) totals: 78 bare-continue sites + 9 pool-result discards. 6 of the 9 pool discards are correct-and-deliberate; the 78 continues include 0 that are documented.**

---

## (e) Fallbacks that trigger only on ZERO results

The project has already been bitten by this shape once: *a fallback that triggers
only on zero results treats "found something" as "found everything", and losing
more index data returned a more correct answer.*

The defining defect: the fallback condition is `len(results) == 0`, and
`len(results) == 0` is **also what an error produces**. So the fallback cannot
distinguish "the source has no match" from "the source is down", and — worse — a
partial failure that returns 1 of 50 results **suppresses the fallback entirely**.

### (e.1) 🔴 The metadata-fetch fallback ladder

`internal/metafetch/service_fetch.go` — a six-deep cascade, lines
**146, 165, 173, 182, 191, 207, 211, 230**, repeated again at **366, 373, 383**:

```go
results, searchErr = ctxSearch.SearchByContext(ctx, sctx)
if searchErr != nil { slog.Warn(...) }        // logged, then ignored
if len(results) == 0 && currentAuthor != "" {  // <-- error and empty are the same thing
    results, searchErr = src.SearchByTitleAndAuthor(ctx, searchTitle, currentAuthor)
    ...
```

Two distinct defects:

1. **Error ≡ empty.** Every rung tests `len(results) == 0`. A 429 rate-limit, a
   context timeout, or an auth failure produces exactly the same signal as "this
   source genuinely doesn't have this book", so the ladder burns through all six
   rungs against a source that is down, then records the book as **"no metadata
   found"**. The user sees an unmatched book and blames their library, not the API
   outage. `searchErr` *is* captured into `lastErr` on some rungs (177, 185, 196)
   and dropped on others (168) — inconsistently.
2. **Partial success suppresses the ladder.** If a rung returns 1 poor result out
   of an expected 50, `len(results) != 0` and **no fallback runs at all**. Getting
   *less* data (zero) triggers a broader, better search than getting *some* data.
   This is verbatim the failure the project already recorded.

### (e.2) 🔴 The same ladder with the error not even captured

`internal/metafetch/isbn.go:181–184` (`searchSourceForISBN`) and
`:205–208` (`searchSourceForASIN`):

```go
if author != "" {
    results, _ = src.SearchByTitleAndAuthor(ctx, title, author)   // error dropped
}
if len(results) == 0 {
    results, _ = src.SearchByTitle(ctx, title)                    // error dropped
}
```

Both errors are discarded outright (bucket (b)/(c) overlap) and the zero-result
test is then used as the fallback trigger. **Consequence:** ISBN/ASIN backfill
records "no ISBN found" for a book whose source lookup simply failed. Those
negative results then feed the dedup exact-match layer, which relies on ISBN
equality — so a transient network error permanently weakens duplicate detection
for that book.

### (e.3) 🟠 Cache-cold fallback that hides cache corruption

`internal/server/handlers/ai.go:602–615`:

```go
groupsJSON, err := json.Marshal(groupsRaw)
if err == nil {
    _ = json.Unmarshal(groupsJSON, &dedupGroups)   // error dropped
}
...
if len(dedupGroups) == 0 {
    // Cache is cold — compute dedup groups inline
```

A **corrupt** cache entry decodes to zero groups, which the code labels "cache is
cold". Consequence: the expensive inline recompute runs on every request forever,
and the corruption is never reported or evicted. Performance degradation with no
diagnosable cause.

### (e.4) ✅ Correct — zero-result fallbacks with a non-error trigger

| File:line | Why it is correct |
|---|---|
| `internal/reconcile/reconcile.go:528` | The zero-test is on `len(extSet)`, built from **config**, not from an I/O call. There is no error to lose. Correct. |
| `internal/plugins/maintenance/intro_transcribe.go:904` | Guarded by `if n != 0` first, so "no BookFile rows" is distinguished from "rows exist but none are audio" before the fallback to `Book.FilePath` fires. Correct, and an example of the right pattern for the rest of the bucket. |

**Bucket (e) totals: 13 fallback rungs across 3 files are error-blind (11 in `service_fetch.go`, 2 pairs in `isbn.go`, 1 in `ai.go`); 2 zero-result fallbacks verified correct.** `internal/metafetch/service_search.go:684` and `internal/audiobooks/service_filtering.go:844, 891` also match the shape and were **not** read — see §7.

---

## (f) Genuinely benign — separated so the real list is not drowned

These are counted so they can be **excluded** from any sweep. Do not "fix" them;
a sweep that touches them will be 700 lines of noise around 100 lines of signal.

| Count | Pattern | Verdict |
|---|---|---|
| **606** | `_ = reporter.Log(...)` (200), `_ = reporter.UpdateProgress(...)` (194), `_ = progress.Log(...)` (143), `_ = progress.UpdateProgress(...)` (69) | ✅ **Benign — leave alone.** These are progress/log emitters on the operations reporter. Their only failure mode is "the progress bar didn't update", and handling the error would mean logging about a failure to log. **Caveat:** they are 54% of the population, so they are also why the raw count of 1,125 badly overstates the problem. Any future lint rule must exempt them by name or it will be turned off within a week. |
| **38** | `_ = os.Remove(...)` / `os.RemoveAll(...)` | ✅ Mostly benign temp-file cleanup. **One exception**: `internal/itunes/itl_safe_write.go:267, 280, 286` remove the staged `.itl.new` on a rejected write — a leaked `.itl.new` next to a live library is confusing but not destructive. 🟢 Low. |
| **54** | `_ = <x>.Close()` — `f.Close`, `iter.Close`, `batch.Close`, `resp.Body.Close`, `ln.Close`, `w.Close` | ✅ Benign for **read** handles and iterators. ⚠️ **Not benign for a written file**: a `Close()` error on a file you just wrote is where a delayed-write failure surfaces. Not separated in this pass — the 54 were counted, not individually classified as read vs write. Flagged in §7. |
| **13** | `_ = writeJSON(...)` / **6** `_ = enc.Encode(...)` / **2** `_ = w.Write(...)` | ✅ Benign. A failed HTTP response write means the client disconnected; there is nobody left to tell. |
| **16** | `_ = b.Delete(...)` / `tx.Delete` / `tx.Put` on a Pebble batch | 🟡 **Probably benign, unverified.** Pebble's `Batch.Delete`/`Put` only error on a batch that is already closed/too large; the error that actually matters is on `Commit`. Whether every one of these 16 is followed by a checked `Commit` was **not verified**. |
| **5** | `_ = os.WriteFile(...)` in `cmd/dedup_bench*.go` | ✅ Benchmark artifacts in dev tooling. |
| **~10** | `_ = <flag>.MarkFlagRequired(...)` in `cmd/` | ✅ Cobra setup; errors only on a programmer typo caught at first run. |

**Bucket (f) total: ≈742 of the 1,125 statement-position discards are benign or
near-benign.** The genuinely actionable statement-position population is
**≈383**.

---

## Summary table

| Bucket | Sites found | Critical | High/Medium | Correct & deliberate | Consequence not determined |
|---|---|---|---|---|---|
| (a) parse/unmarshal of external input | 45 | 16 | 16 | 8 | 5 |
| (b) write/persist errors | ≈133 | 3 | ≈128 | 1 (`itl.go:1049`) | 0 |
| (c) error → indistinguishable empty result | 20 (shape 4, all read) + ~170 (shape 2, 25 read) | 1 | 34 | 6 | ~145 unread |
| (d) continue/break with no counter or log | 78 + 9 pool discards | 2 (the 15-site pebble-store list cluster, `mtls/config.go` expiry check) | ≈70 | 6 (`g.Wait` with policy comments) | 3 (`cmd.Wait` on ffmpeg) |
| (e) zero-result-only fallbacks | 13 error-blind rungs / 3 files | 2 files | 1 file | 2 | 3 sites unread |
| (f) benign cleanup | ≈742 | — | — | ≈742 | 16 (`b.Delete` on batch) + 54 (`Close`, read-vs-write unclassified) |

**Actionable, non-benign, with a named consequence: ≈120 sites.**
**Actionable but unread (shape-2 residue): ~145 sites.**

---

## Ranked fix plan — waves ordered by blast radius

Rules used to build the waves:

1. **Every wave's file set is disjoint from every other wave's.** A file appears in
   exactly one wave, even when it has defects from several buckets — that wave
   fixes all of them. Verified by the collision matrix below.
2. Waves are ordered by blast radius: irreversible data loss first, cosmetic last.
3. **Wave 0 is not parallel** — it lands first, alone, because every later wave
   depends on the helper and the lint exemption list.

### ⚠️ Pre-existing worktree collision

`git worktree list` shows an **active** worktree
`../audiobook-organizer-silentfail`'s sibling `../audiobook-organizer-opsparams`
on branch **`fix/ops-params-silent-unmarshal`**. That branch is already working on
exactly the Wave 3 file set. **Wave 3 must not be dispatched until that branch has
landed or been abandoned**, or the two will conflict on every file. Check its state
before scheduling.

### Wave 0 — prerequisites (land alone, first)

Files: `internal/errhandling/` (new), `.golangci.yml` (or wherever `errcheck` is
configured), `docs/`.

- Add a `MustLog(err error, msg string, kv ...any)` style helper so a "we chose to
  continue" discard becomes one call instead of five lines. Every later wave uses it.
- Add a skipped-item counter type for bucket (d) loops (`skipped++` plus a single
  summary log at loop exit), so waves 6/8/12 do not each invent one.
- Configure `errcheck` with an explicit exclude list for the bucket-(f) callees
  (`reporter.Log`, `reporter.UpdateProgress`, `progress.Log`,
  `progress.UpdateProgress`, `*.Close` on read handles, `writeJSON`, `enc.Encode`).
  **Do this before enabling the linter, not after** — an un-exempted linter reports
  1,125 findings, and a linter that reports 1,125 findings gets disabled.

> Per the project's own recorded lesson: make the **tool** not care first, add a
> check on the artifact second, write the prose last. Wave 0 is that first step.

### Wave 1 — 🔴 irreversible data loss (highest blast radius)

Files (disjoint): `internal/fileops/safe_operations.go`,
`internal/itunes/itl_safe_write.go`, `internal/itunes/service/importer.go`.

- `safe_operations.go:113, 136` — a failed rollback must be surfaced in the returned
  error, not swallowed. The current messages actively mislead.
- `itl_safe_write.go:285` — the error string says `(restored original)` while the
  restore result is discarded. Either verify the restore or stop claiming it.
- `importer.go:58` — make the ITL-conflict guard **fail closed**: a stat error must
  be "cannot verify, refuse to write", not "no conflict".
- `importer.go:407, 417, 497, 504, 509` — checkpoint writes (same file, so folded
  into this wave rather than Wave 9).

(`internal/backup/` was in this wave in an earlier draft and has been **moved to
Wave 13** — verification showed `backup.go:312` is in `ListBackups`, not the backup
writer, so it is a visibility bug rather than a data-loss one.)

### Wave 2 — 🔴 request-param escalation on mutating HTTP endpoints

Files: `internal/server/maintenance_dispatcher.go`,
`internal/server/handlers/metadata/handler.go`,
`internal/server/handlers/dedup/handler.go`,
`internal/server/handlers/system/handler.go`,
`internal/server/handlers/entities/handler.go`.

Rule to apply: **if a struct field gates destructiveness or scope, a parse error is
a 400.** Where the zero value is the *safe* direction (dedup `Apply`, replay dry-run),
keep the discard and keep the comment. Do not blanket-change the file.

### Wave 3 — 🔴 v2 operation param unmarshal (13 sites) — ⛔ BLOCKED, see collision note

Files: `internal/server/library_core_ops.go`, `internal/server/itunes_path_ops.go`,
`internal/server/openlibrary_ops.go`, `internal/server/diagnostics_ops.go`,
`internal/server/folder_autoscan_op.go`,
`internal/plugins/maintenance/{intro_migrate_single_file,extract_wav_clips,repair_transcribe_status,intro_transcribe,auto_match_transcribed}.go`,
`internal/plugins/dedup/embed_scan.go`.

The fix is one shape repeated 13 times: `if err := json.Unmarshal(...); err != nil {
return fmt.Errorf("invalid params for <op>: %w", err) }`. The op registry already
returns errors from `Run:`, so the plumbing exists. Highest priority within the wave
is `library_core_ops.go:193` (`library.organize` → whole-library file move).
Also fix `folder_autoscan_op.go:94, 123` (bucket (d)) while in that file.

### Wave 4 — 🔴 undo log (22 sites) — the operation is unundoable and nobody knows

Files: `internal/organizer/rename.go`, `internal/organizer/service.go`,
`internal/server/entities_ops.go`, `internal/server/duplicates_helpers.go`,
`internal/server/handlers/organize.go`, `internal/dedup/series_dedup.go`,
`internal/dedup/book_dedup.go`, `internal/undo/engine.go`.

The physical change already happened by the time `CreateOperationChange` runs, so
returning an error is usually wrong — the correct fix is to **record and surface**
the failure: increment an `undo_log_failures` counter on the operation result and
mark the operation as **partially-undoable** so the UI can warn before an undo is
attempted. `undo/engine.go:309, 356` is the replay side of the same defect and
belongs in the same wave.

### Wave 5 — 🟠 guards defeated by a discarded read (playback / read status)

Files: `internal/database/pebble_store_playback.go`,
`internal/readstatus/readstatus.go`,
`internal/itunes/service/position_sync.go`.

In all three files a `nil` result is used as the signal "there is no prior state,
so it is safe to write". A read **error** produces the same `nil`, so the guard
does not fire. `position_sync.go:85, 118` overwrite the user's real playback
position with the iTunes bookmark; `readstatus.go:144` rebuilds a fresh state and
discards the existing one; `pebble_store_playback.go:107` leaks a stale status
index entry so a book appears under two statuses at once. Fail the write on a read
error instead. Includes `pebble_store_playback.go:171` (bucket (d)) so this file
stays out of Wave 6.

### Wave 6 — 🟠 store list methods silently dropping rows (15+7 = 22 sites)

Files: `internal/database/pebble_store.go`, `pebble_store_works.go`,
`pebble_store_authors.go`, `pebble_store_tags.go`, `pebble_store_playlists.go`,
`pebble_store_itunes.go`, `pebble_store_externalids.go`,
`pebble_store_bookfiles.go`, `pebble_store_auth.go`, `pebble_store_abssession.go`,
`pebble_store_quick_queries.go` *(note: actual filename `pebble_quick_queries.go`)*,
`embedding_store.go`, `pebble_store_stats.go`.

Highest leverage in the audit by site count. Every list method should count dropped
rows and log a single summary (`slog.Warn("dropped N unreadable rows", ...)`) rather
than returning a short slice silently. `embedding_store.go`'s 7 sites matter most —
a shrinking vector index is the "losing index data" hazard.

### Wave 7 — 🟠 zero-result fallback ladders

Files: `internal/metafetch/service_fetch.go`, `internal/metafetch/isbn.go`,
`internal/metafetch/service_search.go`, `internal/server/handlers/ai.go`.

The fix is structural, not a one-liner: change each rung's condition from
`len(results) == 0` to `searchErr == nil && len(results) == 0`, and record a
distinct "source unavailable" outcome so a book is never marked *unmatchable* on
the strength of an outage. `ai.go` gets its (a.3) and (e.3) defects fixed here.
Read `service_search.go:684` first — it was not read in this pass.

### Wave 8 — 🟠 filesystem reads whose failure fragments the library

Files: `internal/scanner/scanner.go`, `internal/metadata/assemble.go`,
`internal/maintenance/jobs/relink_report.go`,
`internal/maintenance/jobs/relink_missing_to_itunes.go`,
`internal/metabatch/candidates.go`.

`scanner.go:1597, 1637` are the fragmentation risk; `candidates.go:83` is the
scope-escalation risk (nil → re-fetch the whole library). Change these signatures to
return `([]T, error)` — the nil-on-error shape cannot be fixed in place.

### Wave 9 — 🟡 checkpoint / resume state

Files: `internal/server/server_lifecycle.go`, `internal/server/metadata_ops.go`,
`internal/maintenance/jobs/recompute_book_aggregates.go`,
`internal/maintenance/jobs/backfill_file_hashes.go`,
`internal/itunes/service/path_repair.go`,
`internal/itunes/service/path_reconcile.go`,
`internal/organizer/checkpoint.go`,
`internal/server/metadata_batch_candidates.go`.

(`internal/itunes/service/importer.go` deliberately excluded — it is Wave 1.)

### Wave 10 — 🟡 AI scan phase data (14 sites, single file)

File: `internal/aiscan/pipeline.go`.

A phase must not be marked `complete` when its `SavePhaseData` failed. Trivially
parallelisable because it is one file; also fixes `pipeline.go:825` (bucket (d))
and the `json.Marshal` discards on lines 392/403/etc.

### Wave 11 — 🟡 auth / roles / mTLS (security-adjacent, all fail-closed but invisible)

Files: `internal/server/bootstrap.go`, `internal/server/handlers/auth.go`,
`internal/server/middleware/absauth.go`, `internal/mtls/config.go`.

None of these fail open, so the blast radius is "a permission or certificate is
silently absent" rather than "a permission is silently granted". Still needs a log
line at minimum. `bootstrap.go:435` (admin role) is the one that can lock an
operator out.

### Wave 12 — 🟡 post-scan hooks, import/merge collision, quarantine

Files: `internal/server/server.go`, `internal/importer/collision.go`,
`internal/merge/collision.go`, `internal/quarantine/service.go`,
`internal/database/pebble_store_quarantine.go`,
`internal/server/handlers/filesystem.go`, `internal/reconcile/itunes_heal.go`,
`internal/operations/registry/watchdog.go`, `internal/itunes/pid_repair.go`.

`server.go:822` is the anchor site from the brief. `importer/collision.go:70, 93`
is where a swallowed read suppresses a duplicate-import **warning** — arguably
belongs higher. `merge/collision.go:26` was downgraded on verification (it is a
title-display helper, not a merge gate) and is included only because the file is
small and adjacent.

### Wave 13 — 🟢 operation status/result reporting, settings, low-severity

Files: everything remaining under `internal/server/*_ops.go` not already claimed,
`internal/backup/`, `internal/config/persistence.go`, `internal/versions/`, `internal/covers/`,
`internal/openlibrary/store.go`, `internal/sysinfo/service.go`,
`internal/diagnostics/service.go`, `internal/tools/ollama_daemon.go`,
`internal/httputil/parse.go`, `internal/deluge/`, `internal/sweep/`,
`internal/plugins/maintenance/` (files not claimed by Wave 3).

### Collision matrix

| File / dir | Wave |
|---|---|
| `internal/fileops/`, `internal/itunes/itl_safe_write.go`, `internal/itunes/service/importer.go` | 1 |
| `internal/server/maintenance_dispatcher.go`, `internal/server/handlers/{metadata,dedup,system,entities}/handler.go` | 2 |
| `internal/server/{library_core_ops,itunes_path_ops,openlibrary_ops,diagnostics_ops,folder_autoscan_op}.go`, `internal/plugins/maintenance/{5 files}`, `internal/plugins/dedup/embed_scan.go` | 3 |
| `internal/organizer/{rename,service}.go`, `internal/server/{entities_ops,duplicates_helpers}.go`, `internal/server/handlers/organize.go`, `internal/dedup/{series_dedup,book_dedup}.go`, `internal/undo/` | 4 |
| `internal/database/pebble_store_playback.go`, `internal/readstatus/`, `internal/itunes/service/position_sync.go` | 5 |
| `internal/database/pebble_store*.go` (except `_playback`, `_quarantine`), `internal/database/{pebble_quick_queries,embedding_store}.go` | 6 |
| `internal/metafetch/`, `internal/server/handlers/ai.go` | 7 |
| `internal/scanner/scanner.go`, `internal/metadata/assemble.go`, `internal/maintenance/jobs/relink_*.go`, `internal/metabatch/` | 8 |
| `internal/server/{server_lifecycle,metadata_ops,metadata_batch_candidates}.go`, `internal/maintenance/jobs/{recompute_book_aggregates,backfill_file_hashes}.go`, `internal/itunes/service/path_*.go`, `internal/organizer/checkpoint.go` | 9 |
| `internal/aiscan/pipeline.go` | 10 |
| `internal/server/bootstrap.go`, `internal/server/handlers/auth.go`, `internal/server/middleware/absauth.go`, `internal/mtls/` | 11 |
| `internal/server/server.go`, `internal/{importer,merge}/collision.go`, `internal/quarantine/`, `internal/database/pebble_store_quarantine.go`, `internal/server/handlers/filesystem.go`, `internal/reconcile/itunes_heal.go`, `internal/operations/registry/watchdog.go`, `internal/itunes/pid_repair.go` | 12 |
| everything else | 13 |

**No file appears in two waves.** Waves 1–2 and 4–13 can all run in parallel once
Wave 0 has landed. Wave 3 is gated on `fix/ops-params-silent-unmarshal`.

Suggested execution: Wave 0 alone → then Waves 1, 2, 4, 5 in parallel (the
data-loss tier) → then 6, 7, 8, 10 → then 9, 11, 12, 13. Wave 3 slots in whenever
its blocking branch clears.

---

## 7. Residual — what this audit did NOT establish

Repeated and expanded from §0, because a wave plan invites the reader to treat the
list above as complete. It is not.

1. **~145 of the 428 shape-2 (`x, _ :=`) sites were never read.** They were
   triaged by callee name only. The 25 named in (c.4) are the ones whose call site
   was read in full. The remainder is the single largest known gap.
2. **`web/` — the entire frontend.** Not touched. Swallowed promise rejections and
   ignored non-2xx responses are the same class of defect and are likely numerous.
3. **Errors lost at an interface boundary** (a method that returns no error at all).
   Ungreppable; needs a signature review. Probably a large population.
4. **`recover()` sites** — not enumerated.
5. **`errors.Is`/`errors.As` against the wrong sentinel** — not searched.
6. **`err != nil` blocks that log at DEBUG and continue** — invisible in production
   but not a grep shape; not enumerated.
7. **The 54 `_ = x.Close()` sites were counted but not split into read vs write
   handles.** A `Close()` error on a written file is where a delayed-write failure
   surfaces, and any such site is a real bucket-(b) defect misfiled under (f).
8. **The 16 `_ = batch.Delete/Put` sites** — not verified that each is followed by a
   checked `Commit`.
9. **`internal/metafetch/service_search.go:684`, `internal/audiobooks/service_filtering.go:844, 891`**
   match the bucket-(e) shape and were not read.
10. **The 3 `cmd.Wait()` discards on ffmpeg subprocesses**
    (`internal/fingerprint/fpcalc.go:293`, `internal/audio/sample.go:91, 97`) —
    whether a downstream check catches the missing output was not verified.
11. **The 5 maintenance-plugin op params in (a.2)** have no determined consequence;
    each needs its params struct read before its Wave 3 fix is written.
12. **Nothing here was executed.** Every consequence is derived from reading the
    code, not from reproducing the failure. Before fixing a site, confirm the
    failure mode is reachable — several may be dead paths.

### 7.1 Five claims in this document were wrong on first pass

A verification pass re-read the source for every claim that had been inferred from
a grep line rather than from the surrounding function. **Five of the ~120 named
consequences were wrong** and are marked "⚠️ Corrected on verification" in place:

| Site | First (wrong) claim | Verified claim |
|---|---|---|
| `internal/backup/backup.go:312` | "an incomplete backup reports success" | it is `ListBackups`; an existing backup becomes invisible |
| `internal/mtls/config.go:161, 169` | "mTLS starts with fewer trusted certs" | it is `CheckCertExpiry`; an expiring cert is never warned about |
| `internal/merge/collision.go:26` | "the merge proceeds when it should have been blocked" | it is a title-display helper; the merge gate is elsewhere |
| `internal/database/pebble_store_playback.go:107` | "the user's saved position is overwritten" | the row is written regardless; a **stale status index entry** leaks |
| `internal/undo/engine.go:309` | "the change is silently skipped → partial undo" | it is the conflict **preflight**; a read error is reported to the user as `"book deleted"` |

Two claims were *strengthened* by the same pass: `internal/itunes/service/position_sync.go:85, 118`
turned out to be guards whose entire purpose is defeated by the discard, and
`internal/importer/collision.go` has a **second** site (line 93) that suppresses
every title-collision candidate.

**Implication for whoever executes the waves:** a grep line plus a callee name is
not enough to write a fix. The error rate on inference-from-grep in this audit was
roughly **1 in 24**. Read the enclosing function before changing any site,
particularly the ones still marked "consequence not determined".
