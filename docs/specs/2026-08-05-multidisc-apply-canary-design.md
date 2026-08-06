<!-- file: docs/specs/2026-08-05-multidisc-apply-canary-design.md -->
<!-- version: 1.0.0 -->
<!-- guid: 03f6e432-d7ad-49e4-93e4-680940fd99bd -->
<!-- last-edited: 2026-08-05 -->

> ⚠️ **UNVERIFIED DRAFT — symbols not grep-checked.** Authored by an agent on
> 2026-08-05; the adversarial verification pass did not run (the workflow was
> halted by API rate limiting). Treat every code citation as a claim, not a fact.
> The design reasoning and measured production numbers are sound; the code
> references need checking before execution.


# Multidisc apply canary with before/after snapshot — DESIGN

Owner item 3 (2026-08-05). Fragment:
[`todo.d/20260805_220100_multidisc_apply_canary.md`](../../todo.d/20260805_220100_multidisc_apply_canary.md).

Related: [`2026-08-05-review-queue-recommendations-design.md`](2026-08-05-review-queue-recommendations-design.md)
(this workstream depends on it — see §2.3),
[`.claude/notes/2026-08-05-first-aid-architecture.md`](../../.claude/notes/2026-08-05-first-aid-architecture.md),
[`.claude/notes/2026-08-05-unlinked-books-investigation.md`](../../.claude/notes/2026-08-05-unlinked-books-investigation.md).

---

## 1. Problem statement

### 1.1 The numbers

| Quantity | Value | Source |
|---|---|---|
| Pending `regroup.multidisc` holds | **132** | fragment (was 138; the series-guard demoted 6 on 2026-08-05) |
| Holds whose members are individually book-length | **9 of 138** | fragment, measured 2026-08-05 |
| "Confident" multidisc candidates that were actually whole author catalogues | **41 of 43** | [`fs_regroup_shape.go:125-159`](../../internal/itunes/service/fs_regroup_shape.go) doc comment |
| Library size | 44,887 books | first-aid note |
| `review_apply_enabled` in prod | **false** | [`config.go:1917`](../../internal/config/config.go) default; not set in prod |

### 1.2 The apply path hard-deletes

`ApplyMultidisc` ([`regroup_apply.go:78`](../../internal/plugins/maintenance/regroup_apply.go))
calls `combiner.CombineBooks(present, primaryID, nil)` at line 100.
`merge.Service.CombineBooks` ([`service.go:336`](../../internal/merge/service.go))
moves every absorbed book's `book_file` rows onto the survivor and then **hard-deletes
the absorbed `Book` rows** — the file header at `regroup_apply.go:28` says so in as many
words: *"absorbed ROWS are hard-deleted intentionally."*

There is no tombstone to read back. `CombineResult` ([`service.go:309-313`](../../internal/merge/service.go))
carries only `PrimaryID`, `FilesMoved`, `BooksDeleted` — three integers. Once the merge
commits, the *only* possible record of which books existed, what they were called, how
long they were and where their files lived is a record made **before** the call.

That is the whole of this workstream: make that record, make it mandatory, and make the
first real applies small enough to check by ear.

### 1.3 The kind-scoped bulk footgun

`BulkReviewAction` ([`handler.go:253`](../../internal/server/handlers/review/handler.go))
accepts `{"action":"approve","kind":"regroup.multidisc"}` with **no ids**. Lines 273-288
then list *every* pending item of that Kind up to `bulkScanLimit = 100_000`
([`handler.go:328`](../../internal/server/handlers/review/handler.go)) and loop
`approveOne` over all of them (line 294). The frontend wires exactly that shape:
`handleBulkAction(kind, action)` at [`ReviewQueue.tsx:300`](../../web/src/pages/ReviewQueue.tsx)
posts `{action, kind}` and is bound to the bucket-header buttons at lines 412 and 422.

With `review_apply_enabled` flipped on, one click on "Approve all" in the
`regroup.multidisc` bucket header fires 132 `CombineBooks` calls with no confirmation,
no snapshot and no undo.

### 1.4 Absent evidence is not negative evidence — and it is still absent for some holds

`membersAreBookLength` ([`fs_regroup_shape.go:149`](../../internal/itunes/service/fs_regroup_shape.go))
counts unknown durations as **not** book-length, which is correct for a guard but means
a zero-duration group falls straight through the flat branch's
`!membersAreBookLength(members)` condition ([`fs_regroup_shape.go:720`](../../internal/itunes/service/fs_regroup_shape.go))
into a *confident* collapse. The relink op ([`relink_unlinked.go`](../../internal/plugins/maintenance/relink_unlinked.go))
restored durations for the file-shaped population, but nothing guarantees every current
hold's members now carry one.

Worse, the guard is only in the **flat** branch. Read the switch at
[`fs_regroup_shape.go:715-757`](../../internal/itunes/service/fs_regroup_shape.go):

- `case structure == "disc" && discCount*2 > n:` → `KindMultidisc`, **no** book-length check
- `case structure == "flat" && … && !membersAreBookLength(members):` → `KindMultidisc`, checked
- `case (structure == "chapter" || structure == "edition") && folderNamedAfterBook && distinctPrefixes <= 1:` → `KindMultidisc`, **no** book-length check

So the fragment's "9 of 138 have book-length members" is exactly the population the disc
and chapter/edition branches never evaluated. Those 9 are still sitting in the queue and
are the highest-risk rows in it.

---

## 2. Scope

### 2.1 In scope

1. A durable, on-disk **before-snapshot** format and writer (`internal/applysnapshot`).
2. A **structural gate**: `ApplyMultidisc` cannot reach `CombineBooks` without a
   successful snapshot write.
3. A **canary op** `maintenance.multidisc-apply-canary` with `before` and `after` modes,
   including a per-hold risk ranking so the operator can choose the safest handful.
4. A **server-side refusal** of kind-scoped bulk approve while apply is globally enabled.

### 2.2 Non-goals

- **No undo/rollback of a committed combine.** The snapshot is a *record*, not a
  reversal. Reconstructing hard-deleted rows is out of scope (and rule 4 says the
  remedy for a bad merge is re-association, not deletion — a separate workstream).
- **No changes to the classifier.** Whether `membersAreBookLength` should also run on
  the disc and chapter branches is a real finding (§9.2) but a `regroup-shattered-ai`
  change, not a canary change.
- **No gate on the manual combine endpoint** `POST /api/v1/audiobooks/combine`
  ([`wire_dedup_routes.go:75`](../../internal/server/wire_dedup_routes.go) →
  [`duplicates/handler.go:325`](../../internal/server/handlers/duplicates/handler.go)),
  which reaches `merge.Service.CombineBooks` independently. See D1's scope note.
- **No changes to `ApplyVersionGroup`.** It uses re-fetch-and-patch `UpdateBook` and
  never deletes ([`regroup_apply.go:264-279`](../../internal/plugins/maintenance/regroup_apply.go));
  losing a version link is recoverable, so it does not need the gate. Surgical scope.
- **No flipping of `review_apply_enabled`.** This spec produces the evidence that makes
  flipping it a decision rather than a gamble. The flip itself is an owner action.
- **No new UI beyond one confirmation dialog.** The review queue's per-hold override UI
  belongs to the sibling workstream.
- **No writes anywhere under `books/itunes/**`.** The producer already excludes the
  frozen tree ([`regroup_shattered_ai.go:191`](../../internal/plugins/maintenance/regroup_shattered_ai.go)
  calls `config.UnderFrozenITunesTree`); the canary re-checks and refuses (D9).

### 2.3 Dependency on the recommendations workstream

The fragment declares a dependency on `review-queue-recommendations-and-overrides` so
approval targets one hold at a time. That spec's §7.4 rekeys the handler registry from
Kind to action: `RegisterApplyHandler(kind, fn)` → `RegisterActionHandler(action, fn)`,
and rewrites [`wire_handlers.go:603-607`](../../internal/server/wire_handlers.go) — the
exact five lines this workstream also edits.

**Resolution:** this workstream lands *after* it, and the plan is written against the
post-sibling shape. The third constructor argument is identical either way:

```go
// post-sibling (expected):
combine := maintenanceplugin.ApplyMultidisc(s.Store(), mergeSvc, snapRecorder)
reviewH.RegisterActionHandler(itunesservice.ActionCombine, combine)

// if this workstream lands FIRST, the registration lines are today's:
combine := maintenanceplugin.ApplyMultidisc(s.Store(), mergeSvc, snapRecorder)
reviewH.RegisterApplyHandler(itunesservice.KindMultidisc, combine)
reviewH.RegisterApplyHandler(itunesservice.KindAnthology, combine)
```

Only the registration call differs. Nothing else in this spec depends on which lands first.

---

## 3. Locked decisions

### D1 — The snapshot is a precondition inside `ApplyMultidisc`, not a separate op you are supposed to remember to run

`ApplyMultidisc` gains a third parameter:

```go
func ApplyMultidisc(
    store database.Store,
    combiner bookCombiner,
    rec applysnapshot.Recorder,
) func(context.Context, database.ReviewItem) error
```

and, between `pickPrimary` (line 99) and `CombineBooks` (line 100), it captures and
writes the snapshot. **A nil recorder, or any write error, returns an error and the
combine never runs.**

**Scope of the guarantee, stated exactly.** This makes `CombineBooks` unreachable
**from the review-apply path**. It does **not** make `merge.Service.CombineBooks`
unreachable in general — grepped, there is a second live caller:

```
internal/server/handlers/duplicates/handler.go:325  func (h *Handler) CombineBooks(c *gin.Context)
internal/server/handlers/duplicates/handler.go:351  result, err := ms.CombineBooks(append(req.MergeIDs, req.KeepID), req.KeepID, req.Override)
internal/server/wire_dedup_routes.go:75             protected.POST("/audiobooks/combine", …, duplicatesH.CombineBooks)
```

That is the **manual** combine — a human explicitly naming `keep_id` and `merge_ids` in
one request, with a non-nil `override`. It hard-deletes the same way and is **not** gated
by this work (§2.2). Gating it is defensible follow-up work; it is out of scope here
because this workstream is about the 132-hold queue, and because a hand-typed two-book
combine is a different risk profile from a bulk button that fires 132 of them. Do not
read D1 as "no combine can happen without a snapshot" — read it as "no *review-queue
approve* can."

*Why this shape and not an op you run first.* CLAUDE.md rule 2's instruction is to
"prefer designs where the dangerous call is unreachable." There are three ways into this
closure today — `ApproveReviewItem` → `approveOne` ([`handler.go:154`, `:179`](../../internal/server/handlers/review/handler.go)),
`BulkReviewAction` → `approveOne` ([`handler.go:294`](../../internal/server/handlers/review/handler.go)),
and `ReplayApprovedItems` ([`replay.go:120`](../../internal/server/handlers/review/replay.go)) —
and the sibling workstream is adding per-item action overrides as a fourth. A procedural
"snapshot first" rule has to be re-enforced at each one and re-remembered at every future
call site. A parameter cannot be forgotten: it will not compile.

*Why nil is an error and not a silent skip.* A recorder that no-ops when it cannot write
re-opens the exact hole the gate closes, and it would fail open at precisely the moment
disk is full or the directory is unwritable. Fail closed.

### D2 — `ApplyVersionGroup` is NOT gated

Version-group links are `UpdateBook`-based re-fetch-and-patch with no delete
([`regroup_apply.go:264-279`](../../internal/plugins/maintenance/regroup_apply.go)); a
wrong link is undone by clearing `VersionGroupID`. The gate exists for irreversibility,
so applying it here would be cargo-culting. `regroup.anthology` **is** covered, because
[`wire_handlers.go:604-605`](../../internal/server/wire_handlers.go) registers the *same*
`combine` closure for it — anthology also calls `CombineBooks` and also hard-deletes.

### D3 — Snapshot file: append-only JSONL beside the database, one line per hold

Path: `filepath.Join(filepath.Dir(config.AppConfig.DatabasePath), "apply-snapshots", "<kind>-<RFC3339-date>.jsonl")`.

*Why beside the database and not under `RootDir()`.* `ServerDeps` exposes `RootDir()`
([`deps.go:~131`](../../internal/plugins/maintenance/deps.go)) and no database-path
accessor, but `RootDir()` is the **library** root — writing operational records into the
books tree is wrong on its own terms, and part of that tree is the frozen iTunes library
(rule 5). `config.AppConfig` is a package global ([`config.go:786`](../../internal/config/config.go))
and `DatabasePath` ([`config.go:467`](../../internal/config/config.go)) already has its
parent directory validated as existing and writable at startup
([`config.go:1784-1788`](../../internal/config/config.go)). The maintenance package
already imports `internal/config` ([`regroup_shattered_ai.go:47`](../../internal/plugins/maintenance/regroup_shattered_ai.go)),
so nothing new is threaded through `ServerDeps`.

*Empty `DatabasePath`.* There is **no** `viper.SetDefault("database_path", …)` in
`config.go` — the value can be empty in a mis-set config. When it is empty and no
explicit `snapshotDir` override was given, the recorder constructor **returns an error**
and the op/apply fails. Never a silent skip (D1).

*Why JSONL and not one JSON document.* Append-only means a crash mid-run loses at most
the final partial line, and every earlier hold's record is already durable. A single JSON
array would have to be rewritten whole on each append.

*Durability.* Each record is written with `O_APPEND|O_CREATE|O_WRONLY`, followed by
`f.Sync()` **before** `Record` returns. The combine must not start until the bytes are on
the platter.

### D4 — Snapshot contents are exactly the fields needed to reconstruct the human question

Full schema in §4. The fragment names the minimum — "every member book ID, title,
duration, file path, and which ID `pickPrimary` will select" — and this design adds the
per-*file* level, because the after-diff's only exact invariant is file-ID set
containment (D7) and that needs file IDs.

**Fingerprints are recorded as presence + byte length, never as bytes.**
`BookFile.AcoustIDFingerprint` is `[]byte` ([`store.go:~731`](../../internal/database/store.go)),
4 bytes per frame at 8 frames/sec — a 10-hour book is megabytes. Presence and length are
sufficient to prove retention and keep the snapshot readable.

### D5 — Duration evidence is a three-valued field, not a boolean

Per file: `durationRaw` (the stored `BookFile.Duration`) **and** `durationSec`
(`database.NormalizeDurationSec(FileSize, Duration)` — the same call the producer makes
at [`regroup_shattered_ai.go:~153`](../../internal/plugins/maintenance/regroup_shattered_ai.go)).
Per member, `durationEvidence` is one of:

| Value | Meaning |
|---|---|
| `measured` | non-zero and `durationRaw == durationSec` for every file |
| `normalized` | non-zero, but at least one file's raw value was rescaled |
| `absent` | the member's summed `durationSec` is 0 |

*Why the third value.* `NormalizeDurationSec` exists because ~1.9% of historical rows
stored **milliseconds** ([`relink_unlinked.go:300-304`](../../internal/plugins/maintenance/relink_unlinked.go)).
A 1000×-inflated row reads as book-length to the series signal. Collapsing `normalized`
into `measured` would let the risk ranking say "this hold looks like a series" when the
truthful statement is "this hold looks like a series *because of a millisecond row*."

`absent` means **cannot verify** and is ranked as a hazard, never as "safe to merge"
(rule 3). It is the state that made the guard inert across 97.5% of the queue.

### D6 — The canary op is dry-run by default with an explicit `apply` flag it does not own

`maintenance.multidisc-apply-canary` **never merges anything.** Its `before` mode is
read-only over the review queue and the book store; its `after` mode is read-only too.
Applying stays where it already lives — the review HTTP surface, behind
`review_apply_enabled` and (post-sibling) per-item `ids`.

*Why the op does not apply.* Two independent kill switches are better than one, and an
op that both snapshots and merges could be invoked by the scheduler. The op has
`Capabilities: []sdk.Capability{sdk.CapLibraryRead, sdk.CapFilesWrite}` — library **read**
only; `CapFilesWrite` is for the snapshot file itself. No `CapLibraryWrite`, mirroring
how `regroupShatteredAIDef` withholds it at
[`regroup_shattered_ai.go:99-101`](../../internal/plugins/maintenance/regroup_shattered_ai.go).

### D7 — The after-diff gates on file-ID **set containment**, not on a file **count** identity

Every `BookFile.ID` recorded in the before-snapshot for a hold MUST be present on the
survivor afterwards. That is exact and needs no model of the combine's internals.

The count delta is **reported, not asserted.** `CombineBooks` can legitimately change the
file count in ways the snapshot cannot predict: `ensureOwnFile`
([`service.go:497`](../../internal/merge/service.go)) materializes the survivor's own
virtual file when it has none, and `attachVirtualFile` ([`service.go:511`](../../internal/merge/service.go))
looks the absorbed member's path up with `GetBookFileByPath` and, when a row already
exists **under a different owner**, *moves* that row rather than creating one. So the
arithmetic depends on rows outside the snapshot. The diff therefore records three numbers
side by side and lets a human read them:

- `filesBefore` — Σ member `fileCount` from the snapshot (exact)
- `filesMovedReported` — `CombineResult.FilesMoved` ([`service.go:311`](../../internal/merge/service.go))
- `filesAfter` — `len(store.GetBookFiles(survivorID))`

### D8 — Kind-scoped bulk **approve** is refused while apply is globally enabled

In `BulkReviewAction`: when `len(req.IDs) == 0` (kind-scoped), `req.Action == "approve"`,
the resolved target has a registered handler, **and** `h.applyGloballyEnabled()` is true
→ respond `409` with code `BULK_APPLY_REQUIRES_IDS`.

*Why no count threshold.* A magic number (`> 10 → refuse`) is a tuning parameter nobody
can defend and it drifts. The boolean is exact: with the switch **off**, kind-scoped bulk
is a bookkeeping operation — it is how the queue gets reviewed *before* apply, and
`ReplayApprovedItems` exists specifically to make those decisions count later
([`replay.go:30-46`](../../internal/server/handlers/review/replay.go)) — so it stays
allowed. With the switch **on**, the same click is 132 hard-deleting merges, and the
fragment says never. `reject` is unaffected in both states.

### D9 — The canary refuses to write its snapshot inside the frozen iTunes tree

Before opening the file the recorder calls `config.UnderFrozenITunesTree(dir)`
([`itunes_libraries.go:98`](../../internal/config/itunes_libraries.go)) and returns an
error if it is true. Under a sane config this can never fire; it exists so that a
mis-set `database_path` cannot turn a safety feature into a rule-5 violation.

Separately, the canary counts members whose `FilePath` or first file's `ITunesPath` is
under the frozen tree and reports the count. It should be **0** — the producer already
excludes them at [`regroup_shattered_ai.go:191`](../../internal/plugins/maintenance/regroup_shattered_ai.go) —
so a nonzero value means a stale hold predating that fix and is a hard blocker.

### D10 — Concurrency: member resolution is a bounded worker pool over holds

`before` mode fans out across **holds** with
`registry.RunItems(ctx, reporter, ids, fn, registry.RunItemsOptions{Concurrency: runtime.NumCPU(), ProgressTotal: len(ids), ErrMode: registry.ErrModeCollect, Label: …})`
— the same call shape `relink_unlinked.go:117-156` uses.

*Why a pool for only 132 holds.* Per hold the work is `len(members)` × (`GetBookByID` +
`GetBookFiles`), so the real item count is in the thousands of DB reads, and the CLAUDE.md
mandate is to design for concurrency *from the start* rather than bolt it on. Sharding by
hold (not by member) keeps each worker's set disjoint. The pass is **read-only**, so
there is no shared-mutation hazard; results are collected under one mutex, exactly as
`relink_unlinked.go:113-147` does.

The **write** of the JSONL file stays single-threaded after the scan, so line order is
deterministic (sorted by hold ID) and two dry runs diff cleanly.

### D11 — Counts must reconcile, with no silent filtering

Both modes emit a `RECONCILE:` log line and reuse `linkintegrity.Report` /
`linkintegrity.PhaseResult` — the same types `relink_unlinked.go:165-224` uses, including
its `report.UnreconciledPhases()` check that **fails the op** when a phase does not
balance. The identity is:

```
holdsExamined == holdsSnapshotted + holdsSkipped + holdsErrored
```

with every skip carrying a reason string. `after` mode reconciles the same way over the
snapshot's records.

### D12 — The snapshot is keyed by `ReviewItem.ID`, and re-recording is allowed

`Recorder.Has(itemID)` reports whether a record already exists, but a second `Record` for
the same item **appends a new line** rather than being suppressed. Approve → fail →
re-approve is a real sequence (`ReplayApprovedItems` leaves failed items retryable,
[`replay.go:125-127`](../../internal/server/handlers/review/replay.go)), and the second
attempt's world-state may differ from the first's — `presentMembers`
([`regroup_apply.go:338`](../../internal/plugins/maintenance/regroup_apply.go)) may
resolve fewer members. Both attempts are evidence. Readers take the **last** record for a
given item ID.

---

## 4. Data model

### 4.1 Package `internal/applysnapshot`

```go
// Recorder captures an irreversible apply's inputs before it runs.
type Recorder interface {
    // Record durably writes one snapshot. It MUST fsync before returning nil.
    // A non-nil error MUST prevent the caller from performing the apply.
    Record(ctx context.Context, s Snapshot) error
    // Has reports whether any record for itemID exists in this recorder's file.
    // Consumer: `after` mode, to separate "this hold was applied but never
    // snapshotted" (a gate failure — investigate) from "this hold was never
    // applied" (expected). Not used to suppress a re-record — see D12.
    Has(itemID string) (bool, error)
    // Path returns the file being appended to (for logs and the op report).
    Path() string
}
```

### 4.2 `Snapshot` — one review hold, one JSONL line

```go
type Snapshot struct {
    SchemaVersion int       `json:"schema_version"` // 1
    CapturedAt    time.Time `json:"captured_at"`
    Origin        string    `json:"origin"`        // "apply-gate" | "canary-before"

    // ── the review hold, verbatim ────────────────────────────────────────
    ItemID    string `json:"item_id"`    // database.ReviewItem.ID
    Kind      string `json:"kind"`       // ReviewItem.Kind
    DedupKey  string `json:"dedup_key"`  // ReviewItem.DedupKey
    FolderRef string `json:"folder_ref"` // ReviewItem.FolderRef
    Status    string `json:"status"`     // ReviewItem.Status at capture
    Summary   string `json:"summary"`    // ReviewItem.Summary
    Payload   string `json:"payload"`    // ReviewItem.Payload, raw JSON string

    // ── the decoded proposal ─────────────────────────────────────────────
    Folder         string   `json:"folder"`          // regroupPayload.Folder
    ProposedAction string   `json:"proposed_action"` // regroupPayload.ProposedAction
    SurvivorTitle  string   `json:"survivor_title"`  // regroupPayload.SurvivorTitle
    Confidence     string   `json:"confidence"`      // regroupPayload.Confidence
    MemberBookIDs  []string `json:"member_book_ids"` // regroupPayload.MemberBookIDs, as written
    PayloadFiles   []string `json:"payload_files"`   // regroupPayload.Files
    DiscNumbers    []int    `json:"disc_numbers,omitempty"`
    TrackNumbers   []int    `json:"track_numbers,omitempty"`

    // ── what the apply will actually do ──────────────────────────────────
    PresentBookIDs []string `json:"present_book_ids"` // presentMembers() result
    ChosenPrimary  string   `json:"chosen_primary"`   // pickPrimary(PresentBookIDs)
    PrimaryRule    string   `json:"primary_rule"`     // "smallest-ULID" (regroup_apply.go:360-372)

    // ── the members, fully ───────────────────────────────────────────────
    Members []Member `json:"members"`

    // ── derived risk signals (see §5) ────────────────────────────────────
    Risk RiskSignals `json:"risk"`
}
```

`Payload` is stored **verbatim as a string**, not re-marshalled. A future `regroupPayload`
field this struct does not know about still survives in the record.

### 4.3 `Member`

```go
type Member struct {
    BookID   string `json:"book_id"`
    Present  bool   `json:"present"`  // resolved and not soft-deleted
    Absence  string `json:"absence,omitempty"` // "not-found" | "soft-deleted"

    Title    string `json:"title"`     // Book.Title
    FilePath string `json:"file_path"` // Book.FilePath (virtual/shell path)

    AuthorID       *int `json:"author_id,omitempty"`       // Book.AuthorID
    SeriesID       *int `json:"series_id,omitempty"`       // Book.SeriesID
    SeriesSequence *int `json:"series_sequence,omitempty"` // Book.SeriesSequence
    AuthorName     string `json:"author_name,omitempty"`   // resolved via GetAuthorByID
    SeriesName     string `json:"series_name,omitempty"`   // resolved via GetSeriesByID

    BookDurationSnapshot *int    `json:"book_duration_snapshot,omitempty"` // Book.Duration
    LibraryState         *string `json:"library_state,omitempty"`
    VersionGroupID       *string `json:"version_group_id,omitempty"`
    IsPrimaryVersion     *bool   `json:"is_primary_version,omitempty"`
    MarkedForDeletion    *bool   `json:"marked_for_deletion,omitempty"`

    FileCount        int    `json:"file_count"`         // len(GetBookFiles)
    DurationSec      int    `json:"duration_sec"`       // Σ NormalizeDurationSec over files
    DurationEvidence string `json:"duration_evidence"`  // measured | normalized | absent (D5)
    UnderFrozenTree  bool   `json:"under_frozen_tree"`  // D9

    Files []File `json:"files"`
}
```

`AuthorName`/`SeriesName` come from `GetAuthorByID(id int)`
([`iface_author.go:13`](../../internal/database/iface_author.go)) and `GetSeriesByID(id int)`
([`iface_series.go:10`](../../internal/database/iface_series.go)) — `Book` itself stores
only the integer IDs ([`store.go:125-126`](../../internal/database/store.go)). Lookup
failure leaves the name empty and is **not** an error: the ID is the durable part.

### 4.4 `File`

```go
type File struct {
    ID               string `json:"id"`                 // BookFile.ID — the after-gate key (D7)
    FilePath         string `json:"file_path"`
    OriginalFilename string `json:"original_filename,omitempty"`
    ITunesPath       string `json:"itunes_path,omitempty"`
    DurationRaw      int    `json:"duration_raw"`       // BookFile.Duration as stored
    DurationSec      int    `json:"duration_sec"`       // NormalizeDurationSec(FileSize, Duration)
    FileSize         int64  `json:"file_size"`
    DiscNumber       int    `json:"disc_number"`
    TrackNumber      int    `json:"track_number"`
    Format           string `json:"format,omitempty"`
    FileHash         string `json:"file_hash,omitempty"`
    FingerprintBytes int    `json:"fingerprint_bytes"`  // len(AcoustIDFingerprint), never the bytes (D4)
}
```

### 4.5 `RiskSignals`

```go
type RiskSignals struct {
    MemberCount         int    `json:"member_count"`
    PresentCount        int    `json:"present_count"`
    TotalDurationSec    int    `json:"total_duration_sec"`
    LongMembers         int    `json:"long_members"`          // members with DurationSec >= 5400
    MajorityBookLength  bool   `json:"majority_book_length"`  // LongMembers*2 > PresentCount
    ZeroDurationMembers int    `json:"zero_duration_members"`
    NormalizedMembers   int    `json:"normalized_members"`
    DistinctSeriesIDs   int    `json:"distinct_series_ids"`
    FrozenTreeMembers   int    `json:"frozen_tree_members"`
    AlreadyMultiFile    int    `json:"already_multi_file"`    // members with FileCount > 1
    Band                string `json:"band"`                  // "red" | "amber" | "green"
}
```

`5400` is `90*60`, the same threshold as `bookLengthSec`
([`fs_regroup_shape.go:~124`](../../internal/itunes/service/fs_regroup_shape.go)), and
`MajorityBookLength` reproduces `membersAreBookLength`'s strict-majority rule
([`fs_regroup_shape.go:149-167`](../../internal/itunes/service/fs_regroup_shape.go)).
Both are **re-implemented in `applysnapshot`, not imported** — see §9.1 for why, and for
the consequence.

---

## 5. Risk banding

Evaluated per hold in `before` mode. Bands are advisory ordering for a human, never an
automatic action.

| Band | Condition | Reading |
|---|---|---|
| **red** | `MajorityBookLength` **or** `FrozenTreeMembers > 0` **or** `DistinctSeriesIDs > 1` | This is the 41-of-43 shape, a rule-5 violation, or a group spanning series. Do not approve. |
| **amber** | `ZeroDurationMembers > 0` **or** `LongMembers > 0` **or** `NormalizedMembers > 0` **or** `AlreadyMultiFile > 0` | Cannot verify, or a partial series signal, or a millisecond row, or a member that is already a real multi-file book. Needs a listen. |
| **green** | none of the above, and every member has `DurationEvidence == "measured"` | Positive duration evidence, all members chapter-length. Canary candidates come from here. |

**A hold with any `absent` member can never be green.** Absent duration is "cannot
verify" (rule 3) — it is precisely the state that made the guard inert across 97.5% of
the queue, and it must not be laundered into a green light by the absence of a red flag.

---

## 6. Op definition

```
ID:              maintenance.multidisc-apply-canary
Plugin:          maintenance
DisplayName:     Multidisc apply canary (before/after snapshot · read-only)
ResumePolicy:    sdk.ResumeDrop
DefaultPriority: sdk.PriorityNormal
ConcurrencyKey:  maintenance.multidisc-apply-canary
Cancellable:     true
Isolate:         false
Timeout:         30 * time.Minute
Capabilities:    sdk.CapLibraryRead, sdk.CapFilesWrite   // FilesWrite = the snapshot file only
Schedule:        (none — manual, per first-aid decision D3)
```

Registered by appending `p.multidiscApplyCanaryDef()` to the slice in `(*Plugin).Register`
([`plugin.go:32`](../../internal/plugins/maintenance/plugin.go)), next to
`p.regroupShatteredAIDef()` at line 108.

### 6.1 Params

```go
type canaryParams struct {
    Mode        string   `json:"mode"`        // "before" (default) | "after"
    Kinds       []string `json:"kinds"`       // default: [regroup.multidisc]
    Status      string   `json:"status"`      // default: pending; "after" defaults to applied
    IDs         []string `json:"ids"`         // restrict to these ReviewItem IDs
    SnapshotDir string   `json:"snapshotDir"` // override D3's derived directory
    SnapshotFile string  `json:"snapshotFile"`// "after" mode: read this file (default: newest for kind)
}
```

There is **no `apply` param** (D6).

### 6.2 `before` mode

1. `store.ListReviewItems(database.ReviewFilter{Status: params.Status, Kind: k, Limit: 0})`
   for each kind ([`iface_review.go:24`](../../internal/database/iface_review.go)); filter
   to `params.IDs` when given.
2. `registry.RunItems` over hold IDs at `runtime.NumCPU()` (D10). Per hold:
   `decodeRegroupPayload`-equivalent decode, `GetBookByID` + `GetBookFiles` per member,
   presence/soft-delete classification mirroring `presentMembers`
   ([`regroup_apply.go:338-358`](../../internal/plugins/maintenance/regroup_apply.go)),
   `pickPrimary` over the present set, duration normalization, risk banding.
3. Sort by `ItemID`, write every snapshot through the recorder (single-threaded).
4. Emit the `RECONCILE:` line and the band histogram; fail on `UnreconciledPhases()`.

### 6.3 `after` mode

1. Load the snapshot file; take the **last** record per `ItemID` (D12).
2. For each record, re-read `GetBookByID(ChosenPrimary)` and
   `GetBookFiles(ChosenPrimary)`, plus `GetBookByID` for every absorbed member ID.
3. Emit one `Diff` per record:

```go
type Diff struct {
    ItemID           string   `json:"item_id"`
    SurvivorID       string   `json:"survivor_id"`
    SurvivorPresent  bool     `json:"survivor_present"`
    SurvivorSoftDeleted bool  `json:"survivor_soft_deleted"`
    FilesBefore      int      `json:"files_before"`
    FilesAfter       int      `json:"files_after"`
    MissingFileIDs   []string `json:"missing_file_ids"`     // D7 — MUST be empty
    ExtraFileIDs     []string `json:"extra_file_ids"`       // reported, not asserted
    DurationBeforeSec int     `json:"duration_before_sec"`
    DurationAfterSec  int     `json:"duration_after_sec"`
    FingerprintedBefore int   `json:"fingerprinted_before"`
    FingerprintedAfter  int   `json:"fingerprinted_after"`  // MUST be >= before
    AbsorbedGone     []string `json:"absorbed_gone"`
    AbsorbedSurvived []string `json:"absorbed_survived"`    // unexpected; investigate
    TrackNumbers     []int    `json:"track_numbers"`
    TrackOrderOK     bool     `json:"track_order_ok"`       // 1..N contiguous & unique, OR guard fired
    Verdict          string   `json:"verdict"`              // "ok" | "attention"
}
```

`AbsorbedGone` is determined by `GetBookByID(id) == (nil, nil)`. That contract is
verified: `PebbleStore.GetBookByID` returns `nil, nil` on `pebble.ErrNotFound`
([`pebble_store.go:753-758`](../../internal/database/pebble_store.go)).

`TrackOrderOK` tolerates "no numbering happened": `applyDiscTrackNumbers` returns `(0, nil)`
without writing when the payload has no disc/track arrays, or when **any** survivor file
already carried disc/track metadata — the group-level guard at
[`regroup_apply.go:155-162`](../../internal/plugins/maintenance/regroup_apply.go).

---

## 7. API surface change

Only one, in `BulkReviewAction` ([`handler.go:253`](../../internal/server/handlers/review/handler.go)),
implementing D8. Inserted after the existing `req.Kind == "" && len(req.IDs) == 0` guard
at lines 267-270:

```go
if req.Action == "approve" && len(req.IDs) == 0 && h.applyGloballyEnabled() {
    if _, hasHandler := h.applyHandlerFor(req.Kind); hasHandler {
        httputil.RespondWithError(c, http.StatusConflict,
            "review apply is enabled: kind-scoped bulk approve would execute every pending "+
                "item of this kind. Pass explicit 'ids' instead.",
            "BULK_APPLY_REQUIRES_IDS")
        return
    }
}
```

Post-sibling this becomes `h.actionHandlerFor(resolvedAction)` per that spec's §7.4; the
condition is otherwise identical.

🐛 **Pre-existing bug found while verifying this signature.** The real declaration is
`RespondWithError(c *gin.Context, statusCode int, message string, code string)`
([`httputil/respond.go:18`](../../internal/httputil/respond.go)) — **message third, code
fourth**. [`replay.go:106-108`](../../internal/server/handlers/review/replay.go) passes
them the other way round:

```go
httputil.RespondWithError(c, http.StatusConflict, "REVIEW_APPLY_DISABLED",
    "review apply is globally disabled; enable review_apply_enabled before replaying approved items")
```

so that 409's JSON body currently reports `error: "REVIEW_APPLY_DISABLED"` and
`code: "review apply is globally disabled; …"` — the two fields are swapped. Cosmetic (no
data risk) but it is in the same function family this workstream touches; the plan fixes
it as a one-line drive-by in S4.

Frontend ([`ReviewQueue.tsx:300`](../../web/src/pages/ReviewQueue.tsx)): `handleBulkAction`
surfaces the 409's message verbatim through the existing `addNotification` error path, and
the bucket-header Approve button (line 412) gains a `window.confirm` naming the bucket's
pending count. Cosmetic — the server refusal is the load-bearing half.

---

## 8. Failure modes

| # | Failure | Consequence | Mitigation |
|---|---|---|---|
| F1 | Snapshot directory unwritable / disk full | Apply must not proceed | `Record` errors → `ApplyMultidisc` returns the error → item lands in `failed` with a reason; `ReplayApprovedItems` leaves it retryable ([`replay.go:125-127`](../../internal/server/handlers/review/replay.go)) |
| F2 | `DatabasePath` empty in config | Nowhere to write | Recorder constructor errors at wire time; the op errors; nothing silently no-ops (D3) |
| F3 | Snapshot written, then combine fails mid-way | Orphan record for a merge that partly happened | Acceptable and desirable — the record describes the pre-state either way. `presentMembers` makes the retry idempotent ([`regroup_apply.go:87-98`](../../internal/plugins/maintenance/regroup_apply.go)) |
| F4 | Hold is days old; members merged away since | Fewer members than the payload lists | Already handled: `presentMembers` skips not-found and `MarkedForDeletion`; the snapshot records both `MemberBookIDs` (as proposed) and `PresentBookIDs` (as will be acted on) so the divergence is visible |
| F5 | Two approves race on overlapping holds | Double-merge | `CombineBooks` takes `mergeSerializeMu` for the whole read-modify-write ([`service.go:~348`](../../internal/merge/service.go)). The canary op never mutates, so it adds no new race |
| F6 | Snapshot fsync succeeds but the file is later deleted | Record lost | Out of scope; the file lives beside the database and inherits its backup policy |
| F7 | `after` mode run against the wrong snapshot file | Bogus diff | Records carry `CapturedAt`, `ItemID`, `DedupKey`, `SchemaVersion`; `after` reports the file path and capture window in its summary |
| F8 | An operator flips `review_apply_enabled` and clicks a bucket header anyway | 132 merges | D8 refuses server-side; the click cannot reach `approveOne` |
| F9 | Green band assigned to a hold with absent durations | The exact 2026-08-05 inertness bug, laundered | §5 makes `absent` disqualifying for green by construction, and `ZeroDurationMembers` is printed in the op summary |
| F10 | Fingerprint lost during combine | Silent data loss | `FingerprintedAfter >= FingerprintedBefore` in the after-gate. (The apply path is already designed against this — nil override means the survivor row is never rewritten, [`regroup_apply.go:24-28`](../../internal/plugins/maintenance/regroup_apply.go) — so a regression here means that guarantee broke) |

---

## 9. Missing symbols and honest gaps

### 9.1 `membersAreBookLength` and `bookLengthSec` are unexported

Both live in `package service` ([`fs_regroup_shape.go:149`, `:~124`](../../internal/itunes/service/fs_regroup_shape.go)),
and `membersAreBookLength` takes `[]memberInfo` — an **unexported** struct
([`fs_regroup_shape.go:264`](../../internal/itunes/service/fs_regroup_shape.go)) that
wraps a `ShatterBook` in an unexported `book` field. It cannot be called from
`internal/applysnapshot` or `internal/plugins/maintenance` as it stands.

**Decision:** the canary re-implements the rule (`long*2 > n`, `long` = members with
`DurationSec >= 5400`) rather than exporting the classifier's internals, and the spec
records the duplication here so the next reader knows there are two copies. A test
asserts the threshold constant equals `90*60` so a change to one is visible.

*Alternative considered and rejected:* export `MembersAreBookLength(members []ShatterBook) bool`
and `BookLengthSec` from `internal/itunes/service`. It is the cleaner long-term shape, but
it edits the classifier — the sibling recommendations workstream's territory — during a
release whose entire purpose is to not disturb the classifier. Flagged as follow-up work.

### 9.2 `regroupPayload` has no `Structure` field — this is a real limitation

`RegroupGroup.Structure` exists ([`fs_regroup_shape.go:~176`](../../internal/itunes/service/fs_regroup_shape.go))
but is **not** carried into `regroupPayload` ([`regroup_shattered_ai.go:64-81`](../../internal/plugins/maintenance/regroup_shattered_ai.go)),
so a hold on disk does not say whether it came from the disc, flat, chapter or edition
branch.

Consequence, stated plainly: the canary **cannot** reproduce the recommendations spec's
D7 structural carve-out (*"a `disc`- or `chapter`-structured group whose folder is named
after the book carries independent structural proof and may still recommend combine"*).
A disc-structured hold with long members will be banded **amber** by this canary even
where that spec's recommender would pass it as `combine`. That is a deliberate
false-positive bias: the canary's job is to slow the operator down, not to agree with the
recommender.

Adding `Structure string \`json:"structure,omitempty"\`` to `regroupPayload` is a small
additive change that would close this, but it belongs to whichever workstream is already
editing that struct (the sibling spec's §4.4 adds four fields to it).

### 9.3 The classifier's book-length guard is missing from two branches

Confirmed by reading the switch at [`fs_regroup_shape.go:715-757`](../../internal/itunes/service/fs_regroup_shape.go):
`!membersAreBookLength(members)` appears **only** in the `structure == "flat"` case
(line 720). The `structure == "disc"` case (line 716) and the
`chapter`/`edition` case (line 749) mint `KindMultidisc` with `Confident: true` and never
consult it. The fragment's "9 of 138" is that population.

This spec does not fix it (§2.2 — no classifier changes). It **surfaces** it: every one
of those holds bands red, so an operator working the ranked list meets them first.

### 9.4 There is no `viper.SetDefault("database_path", …)`

Grepped: `database_path` appears at [`config.go:467`, `:1267`, `:1784`, `:1786`](../../internal/config/config.go)
and nowhere else. It is read straight from viper with no default. `cmd/child_mode.go:62-63`
has its own `"audiobooks.pebble"` fallback, which is child-process-specific and not
reachable from the server path. Hence D3's explicit empty-value error.

---

## 10. Open questions

1. **Retention.** Snapshot files accumulate one line per apply forever. Should
   `maintenance.cleanup-old-backups` ([`cleanup.go:198`](../../internal/plugins/maintenance/cleanup.go),
   which removes `.bak-*` under `RootDir()`) learn about them, or are they permanently
   kept? Recommendation: **keep permanently** — the whole point is that they are the only
   record — and revisit if the directory exceeds a few hundred MB.
2. **Should `after` mode be automatic?** A post-apply hook could run the diff immediately.
   Deliberately not specified: the first canary is a handful of holds a human checks by
   ear, and automation would encourage skipping that.
3. **Where does the operator read the diff?** Today: op logs plus the JSONL file over
   SSH. A `GET /api/v1/review/apply-snapshots` endpoint is an obvious follow-up but is not
   needed for the canary itself.
4. **Should `AuthorName`/`SeriesName` resolution be batched?** `GetSeriesByIDs`
   ([`iface_series.go:16`](../../internal/database/iface_series.go)) takes a slice; there
   is no equivalent `GetAuthorsByIDs` in `iface_author.go`. At 132 holds this is not worth
   optimizing, but it would be at 10,000.
5. **What is the acceptable `ExtraFileIDs` count?** D7 reports rather than asserts because
   `attachVirtualFile`'s move-vs-create behavior is state-dependent. After the first
   canary we will know the empirical distribution and can decide whether it can become an
   assertion.

---

## 11. Acceptance gate (summary)

No apply — not one — until a `before` run over all 132 pending `regroup.multidisc` holds
has been written to disk and read. Full numbers, commands and thresholds are in
[`docs/plans/2026-08-05-multidisc-apply-canary-plan.md`](../plans/2026-08-05-multidisc-apply-canary-plan.md) §7.
