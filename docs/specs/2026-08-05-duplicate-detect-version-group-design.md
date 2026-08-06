<!-- file: docs/specs/2026-08-05-duplicate-detect-version-group-design.md -->
<!-- version: 1.0.0 -->
<!-- guid: b4df2a8c-aeae-4969-ba81-5368a61332c3 -->
<!-- last-edited: 2026-08-05 -->

> ⚠️ **UNVERIFIED DRAFT — symbols not grep-checked.** This document was authored by
> an agent on 2026-08-05 but the adversarial verification pass (which grep-verifies
> that every cited function, struct field, op ID and file path actually exists) did
> NOT run — the workflow was halted by API rate limiting before stage 2.
>
> **Treat every code citation as a claim, not a fact.** The most common failure mode
> in generated plans is a confidently-cited symbol that does not exist. Verify before
> executing. The design reasoning and the measured production numbers are still sound
> and were drawn from real observations; the code references are what needs checking.


# Design — Duplicate detection, combine-by-template, version-group ("The Successors" class)

**Workstream slug:** `duplicate-detect-version-group`
**Tier:** First Aid tier 2 (escalation) + tier 3 (fixer)
**Source fragment:** `todo.d/20260805_220300_first_aid_library_validate_repair.md:59-69` ("Never delete — re-associate")
**Architecture:** `.claude/notes/2026-08-05-first-aid-architecture.md` (§"Never delete — re-associate", decisions D2/D3/D5)
**Measurements:** `.claude/notes/2026-08-05-unlinked-books-investigation.md` (§"The Successors — a third shape")

---

## 1. Problem statement

### 1.1 The measured case

Measured on prod 2026-08-05 (`.claude/notes/2026-08-05-unlinked-books-investigation.md:87-108`):

Book `01KQAXKG7HYMT44GRNCGSJBXG1` at `/mnt/bigdata/books/newbooks/audiobooks/The Successors`
is a **correctly assembled** Warhammer anthology: 13 `book_file` rows, track numbers 1–13
in order, each carrying its own story title, 10h42m total. Its only defect is that
`Book.Title` reads **"The Empty Place"** — story 1's name, inherited at import.

Coexisting with it, in the same library:

| Cohort | Rows | Files | Note |
|---|---|---|---|
| shattered single-story rows under `/books/audiobook-organizer/…` | ~8 | ~8 | one story each |
| `01KQGD0Z4GV9KN77W3VVAQMM7Q` | 1 | 4 | every file named `2.mp3`; durations 2474/1620/3018/868 s match reference tracks **3, 4, 6, 13** |
| other debris rows | balance of 11 | balance of 17 | — |
| **debris total** | **11** | **17** | covers **12 of 13** reference tracks (track 7 absent); **5** files are internally redundant (two files claim one reference track) |
| rows under frozen `books/itunes/**` | 10 | — | **hands off**, must never be touched |

So the user sees 11+ tiles all reading "The Successors" next to one tile reading
"The Empty Place" which is the only correct row in the set.

### 1.2 Why the existing machinery gets this class wrong

- `maintenance.regroup-shattered-ai` only ever proposes **combine** (`regroup.multidisc`,
  `regroup.anthology`) or **link editions** (`regroup.version-group`)
  (`internal/itunes/service/fs_regroup_shape.go:70-74`). Neither is right here: combining
  debris *into* the reference would pour 17 partly-redundant files onto a book that is
  already correct; separating does nothing.
- The debris and the reference live in **different folders**, so the folder-grouping
  classifier never sees them as one group at all
  (`fs_regroup_shape.go:23-31` — the grouping key is the book folder).
- `maintenance.orphan-book-files-cleanup`, `reconcile-scan` and `file-integrity-check`
  all report every one of these rows healthy — each row has a file, and the file exists
  (`.claude/notes/2026-08-05-unlinked-books-investigation.md:76-85`).
- Deleting the debris rows is **not idempotent**: rescan regenerates a book for any file
  no `book_file` row claims, so the rows come straight back
  (`internal/linkintegrity/report.go:60-70`).

### 1.3 What this workstream builds

Three things, in dependency order:

1. **`internal/dupmatch`** — a pure, I/O-free package that answers "do these debris
   tracks map onto that reference book's track list?", matching **by duration**, never
   by guessing boundaries from filenames.
2. **`maintenance.detect-redundant-copies`** — a tier-2 detector op. Whole-library,
   bounded worker pool, **writes zero book/file rows**; its only writes are idempotent
   review-queue holds of a new Kind `firstaid.redundant-copy`.
3. **`ApplyRedundantCopy`** — the tier-3 fixer, registered as the review-queue apply
   handler for that Kind: combine the debris into ONE book using the reference's track
   list as a template, then version-group that combined book with the reference,
   primary = most complete.

---

## 2. Locked decisions

Each decision names the evidence that forced it.

### LD1 — Match by **duration**, never by filename boundaries

The debris fragment `01KQGD0Z4GV9KN77W3VVAQMM7Q` has four files **all named `2.mp3`**.
Filenames carry zero boundary information here. Their durations (2474/1620/3018/868 s)
identify tracks 3/4/6/13 unambiguously. The fragment
(`todo.d/20260805_220300_first_aid_library_validate_repair.md:65-67`) makes this
explicit: "matching by duration instead of guessing boundaries from filenames (the path
that produced the 41-of-43 near-miss)".

Filename/title stems are used **only as corroborators**, never as the primary key.

### LD2 — Duration **source class** is part of the match, and unknown never matches

There are two duration sources on a `book_file` row and they are not interchangeable:

| Source | Field | Trust |
|---|---|---|
| fingerprint | `BookFile.AcoustIDFingerprintDurationSec` (`internal/database/store.go:735`) | real decode duration measured by fpcalc; authoritative |
| container | `BookFile.Duration` (`internal/database/store.go:717`), passed through `database.NormalizeDurationSec` (`internal/database/duration_sanity.go:61`) | container metadata; **historically ~2× too short for m4b/m4a** — the entire reason `maintenance.duration-reextract` exists (`internal/plugins/maintenance/duration_reextract.go:8-13`) |

Rules:

- **both sides fingerprint** → tolerance `TolFingerprintSec` (ships at 2, locked by the
  §6 dry-run histogram) → tier `confirmed`.
- **mixed, or both container** → tolerance `TolContainerSec` (ships at 5) **and at least
  one non-duration corroborator required** → tier `probable`.
- **either side unknown** (`AcoustIDFingerprintDurationSec == 0` and `Duration <= 0`) →
  **NO MATCH**. Not "refuted": counted as `skipped-no-duration`, reported by file ID,
  and the report names `maintenance.duration-reextract` as the op that produces the
  missing input.

> 🔴 This is CLAUDE-rule 3 ("absent evidence must never mean negative evidence")
> implemented, not gestured at. The prior violation cost a whole review queue: a
> `DurationSec == 0` silently disabled `membersAreBookLength` across 97.5% of it
> (`internal/linkintegrity/report.go:20-29`).

### LD3 — Frozen iTunes rows are excluded from **both** the debris set and the reference set

`books/itunes/**` is externally managed and marked Frozen
(`internal/config/itunes_libraries.go:83-98`). `regroup_shattered_ai.go:191` already
excludes it at the source, and that fix removed 561 of 777 bogus ambiguous holds
(commit `786aa73d`).

Excluding it from the **reference** side too is not obvious but is required: a frozen
book chosen as version-group primary would take an `UpdateBook` on a frozen row
(`ApplyVersionGroup` mutates `VersionGroupID` + `IsPrimaryVersion`,
`regroup_apply.go:267-279`). Test both `BookFile.FilePath` **and** `BookFile.ITunesPath`,
mirroring `regroup_shattered_ai.go:191`.

Excluded rows are **counted and reported per hold** (`excludedFrozen`), never silently
dropped — otherwise the operator cannot tell "we ignored 10 iTunes rows" from "there
were no iTunes rows".

### LD4 — The reference is **never** a member of the `CombineBooks` call

`merge.Service.CombineBooks` hard-deletes every absorbed row and moves its files onto
the survivor (`internal/merge/service.go:336-437`). If the reference ever appears in
`bookIDs`, the correct 13-track assembly is destroyed — either it absorbs 17 redundant
debris files, or (worse, if it is not `primaryID`) it is itself deleted.

Sequence is therefore strictly: **combine debris-only → then version-group the debris
survivor with the untouched reference.** An explicit guard asserts
`referenceID ∉ debrisBookIDs` and errors before any write; a unit test pins it.

**Why hard-deleting the 10 absorbed *debris rows* is not the forbidden delete.** CLAUDE
rule 4 forbids deleting to resolve a duplicate because rescan regenerates a book for any
file no `book_file` row claims. Here the files move first, and `CombineBooks` refuses the
delete if the row still owns files:
`"book %s still owns %d files after move; aborting delete"` (`internal/merge/service.go:430`).
Every file keeps an owning `book_file` row throughout, so there is nothing for rescan to
regenerate. The rule is about orphaning **files**, not about **rows**.

### LD5 — The detector consumes **stored** durations only. No ffprobe.

The measured case works from stored values: the fragment's durations
(2474/1620/3018/868) are already in the DB. Consequences of this decision, all
deliberate:

- the op declares `sdk.CapLibraryRead` only — **not** `CapFilesRead` — so it cannot
  touch the filesystem even by accident;
- a whole-library pass stays a pure-DB pass (fast enough to run repeatedly during
  calibration);
- probing is left to the tier-2 duration-probe workstream and to the First Aid
  orchestrator's missing-input triggering, which is the correct owner
  (`.claude/notes/2026-08-05-first-aid-architecture.md:42-66`).

Do not "fix" this later by adding an ffprobe fallback inside the detector. If durations
are missing, run `maintenance.duration-reextract` and re-run the detector — that is the
convergence property the whole First Aid design rests on.

### LD6 — Version-group primary = **most complete**, ties to earliest ULID

Owner decision D2 (`.claude/notes/2026-08-05-first-aid-architecture.md:76-77`).
Completeness is measured as **(matched template slots, then file count)** — never as
summed duration, which a single mis-tagged long file distorts. Reference 13 slots beats
combined-debris 12 slots, so the reference wins and stays primary.

> ⚠️ `ApplyVersionGroup` does **not** currently implement D2. `regroup_apply.go:258-263`
> picks the smallest ULID unconditionally — "earliest ULID", full stop. It cannot be
> reused as-is, and it must not be copy-pasted: its three hard-won invariants
> (group-reuse, cross-group refusal `regroup_apply.go:236-245`, stale-primary demotion
> `regroup_apply.go:286-312`) would then exist twice and drift. §4.4 extracts them into
> one helper with the primary selector injected.

### LD7 — Template numbering is a **new** step, not `applyDiscTrackNumbers`

`applyDiscTrackNumbers` has a group-level bail-out: if **any** survivor file already
carries a non-zero disc or track number, the whole set is left alone
(`regroup_apply.go:155-162`). That is correct for its own use case and fatal for ours:
`maintenance.relink-unlinked-books` stamps `track = i+1` on every file it creates for a
directory-shaped book (`internal/plugins/maintenance/relink_unlinked.go:355`), so most
combined debris survivors arrive already numbered and the reused function would silently
number nothing.

The new step is per-file, not group-level, and its doc comment must say why — or someone
will "simplify" it back into the shared helper.

### LD8 — One review Kind, `firstaid.redundant-copy`

A second Kind (e.g. `firstaid.internally-redundant`) would need a second frontend label
and a second apply handler with no defined action. The internally-redundant files are
reported **inside** the one hold's payload (`internallyRedundant: [{fileId,
duplicateOfTrack}]`) and are a declared non-goal to resolve (§7).

### LD9 — Dry-run by default; there is no apply flag on the detector at all

Owner decision D3 (`.claude/notes/2026-08-05-first-aid-architecture.md:78-80`). The
detector is a review-queue producer, exactly like `regroup-shattered-ai`
(`internal/plugins/maintenance/regroup_shattered_ai.go:16-19`). The apply path is the
review queue's own approve button, which is itself gated by the global switch
`config.ReviewApplyEnabled` — **default OFF and not set in prod**. Shipping the apply
handler therefore changes nothing in prod until that switch is deliberately flipped
(`internal/server/handlers/review/handler.go:16-23`, `internal/server/wire_handlers.go:588`).

### LD10 — Every count reconciles

`examined == matched + unmatched + skipped-no-duration + skipped-frozen + skipped-short + errors`,
asserted in-process and logged as a `RECONCILE:` line, mirroring
`relink_unlinked.go:216-224` and `regroup_shattered_ai.go:214-218`. A run whose numbers
do not add up returns an error rather than reporting success.

---

## 3. Data model

### 3.1 `internal/dupmatch` (new package, pure, no I/O)

```go
// DurSource records WHICH stored field a duration came from. Comparing a
// fingerprint duration against a container duration is not comparing like with like.
type DurSource string

const (
    SourceFingerprint DurSource = "fingerprint" // BookFile.AcoustIDFingerprintDurationSec
    SourceContainer   DurSource = "container"   // BookFile.Duration via NormalizeDurationSec
    SourceUnknown     DurSource = "unknown"     // neither present -> never matches (LD2)
)

// Track is the slim per-file view the matcher reasons over. Deliberately NOT a
// database.BookFile: the index holds one of these per file for the whole library
// (~275K rows), so it must stay small.
type Track struct {
    FileID    string
    BookID    string
    Path      string
    TitleStem string // linkintegrity.TitleStem(BookFile.Title), falling back to basename
    Author    string // Book-level display author, for corroboration only
    DurSec    int
    Source    DurSource
    FileSize  int64
}

type Tier string

const (
    TierConfirmed Tier = "confirmed" // both sides fingerprint, |delta| <= TolFingerprintSec
    TierProbable  Tier = "probable"  // mixed/container within TolContainerSec + >=1 corroborator
)

// Assignment is one debris file placed (or contested) against one reference slot.
type Assignment struct {
    DebrisFileID  string
    DebrisBookID  string
    RefTrackIndex int // 1-based index into the reference's ordered track list
    RefFileID     string
    DeltaSec      int
    Tier          Tier
    Corroborators []string // subset of {"title-stem","fingerprint","author","filesize"}
    Redundant     bool     // lost the contest for this slot; keeps RefTrackIndex for reporting
}

type SkipReason string

const (
    SkipNoDuration      SkipReason = "no-duration"       // LD2: run maintenance.duration-reextract
    SkipTooShort        SkipReason = "below-min-length"  // duration alone is not evidence
    SkipAmbiguousBucket SkipReason = "ambiguous-bucket"  // >MaxBucketFanout candidates at this length
    SkipNoSlot          SkipReason = "no-matching-slot"
    SkipNoCorroborator  SkipReason = "single-file-no-corroborator"
)

type Skip struct {
    FileID string
    Reason SkipReason
}

type TemplateResult struct {
    Assignments   []Assignment // includes Redundant ones
    Skipped       []Skip
    CoveredSlots  []int // sorted, distinct, non-redundant
    MissingSlots  []int // reference slots no debris file claimed
    Histogram     DeltaHistogram
}

type Config struct {
    TolFingerprintSec int // default 2   — LOCKED only after the §6 histogram gate
    TolContainerSec   int // default 5
    ProbeToleranceSec int // default 10  — histogram width; >= both tolerances above
    MinMatchableSec   int // default 60  — below this, duration alone is not evidence
    MaxBucketFanout   int // default 64  — see LD-index trap below
}

// MatchTemplate is the whole matcher. Deterministic: identical inputs -> identical output.
func MatchTemplate(ref []Track, debris []Track, cfg Config) TemplateResult

// DeltaHistogram buckets |delta| in seconds so a dry run can show WHERE the mass sits
// before a tolerance is locked. Render() is deterministic and diff-friendly.
type DeltaHistogram map[int]int
func (h DeltaHistogram) Add(deltaSec int)
func (h DeltaHistogram) Render() string
func (h DeltaHistogram) FractionWithin(sec int) float64

// PrimaryCandidate + PickMostComplete implement owner decision D2 (LD6).
type PrimaryCandidate struct {
    BookID        string
    MatchedTracks int // template slots this book covers
    FileCount     int
}
func PickMostComplete(c []PrimaryCandidate) string
```

**`MatchTemplate` algorithm** (deterministic, no map iteration in the output path):

1. Reference slots are 1-based indices into `ref`, which the caller supplies ordered by
   `(TrackNumber, FilePath)`.
2. For each debris track: `Source == SourceUnknown` → `Skip{SkipNoDuration}`;
   `DurSec < MinMatchableSec` → `Skip{SkipTooShort}`.
3. Candidate pairs = every (debris, slot) with `|delta| <= tol(pairSources)` where
   `tol = TolFingerprintSec` if both sides are `SourceFingerprint`, else `TolContainerSec`.
   Every considered delta within `ProbeToleranceSec` is fed to the histogram, whether or
   not it is accepted — that is what makes the histogram usable for calibration.
4. A debris track whose candidate-slot count exceeds `MaxBucketFanout` →
   `Skip{SkipAmbiguousBucket}` (see §5 trap 2).
5. Mixed/container pairs with zero corroborators are dropped.
6. Sort candidate pairs by `(tierRank, |delta|, -FileSize, -DurSec, DebrisFileID)` — a
   total order with no ties, so the result is reproducible.
   `-FileSize, -DurSec` encodes the architecture note's rule: "when two files claim one
   reference track, prefer the longer/larger and hold the loser"
   (`.claude/notes/2026-08-05-first-aid-architecture.md:104-107`).
7. Greedy assign in that order. First claimant of a slot wins; a later claimant of the
   same slot is emitted with `Redundant = true` and its `RefTrackIndex` retained.
8. Debris tracks with no accepted pair → `Skip{SkipNoSlot}`.
9. `CoveredSlots` / `MissingSlots` are derived from non-redundant assignments.

**Corroborators** (computed by the caller, passed on the `Track`, evaluated in the matcher):

| Corroborator | Test |
|---|---|
| `title-stem` | `linkintegrity.TitleStem` of the debris file's `BookFile.Title` (or basename) equals the reference slot's stem, and is non-empty |
| `fingerprint` | both rows have non-empty `AcoustIDFingerprint` and they are byte-identical |
| `author` | debris `Book` author equals reference `Book` author, case-folded, non-empty |
| `filesize` | `\|sizeA - sizeB\| / max(sizeA,sizeB) <= 0.02` and both non-zero |

Absence of a corroborator is never negative evidence — it only fails to *raise* the tier.

### 3.2 Review hold payload (`firstaid.redundant-copy`)

Written by the detector, read by the frontend and by the apply handler. camelCase, per
the existing producer convention (`web/src/lib/reviewPayload.ts:10-15`).

```jsonc
{
  "referenceBookId": "01KQAXKG7HYMT44GRNCGSJBXG1",
  "referenceTitle": "The Empty Place",
  "referencePath": "/mnt/bigdata/books/newbooks/audiobooks/The Successors",
  "referenceTrackCount": 13,
  "referenceDurationSec": 38520,
  "referenceTitleLooksInherited": true,   // Book.Title == track-1 BookFile.Title (report only, §7)
  "debrisBookIds": ["01KQGD0Z4GV9KN77W3VVAQMM7Q", "..."],
  "debrisFileCount": 17,
  "coveredTracks": [1,2,3,4,5,6,8,9,10,11,12,13],
  "missingTracks": [7],
  "assignments": [
    {"fileId":"...","bookId":"...","path":".../2.mp3","refTrack":3,"deltaSec":0,
     "tier":"confirmed","corroborators":["filesize"],"redundant":false}
  ],
  "internallyRedundant": [{"fileId":"...","duplicateOfTrack":6}],
  "skipped": [{"fileId":"...","reason":"no-duration"}],
  "excludedFrozen": ["<bookID>", "..."],
  "proposedAction": "Combine 11 debris rows (17 files) into one book using the 13-track reference as a template, then version-group with 01KQAXKG7HYMT44GRNCGSJBXG1 as primary",
  "confidence": "confirmed",
  "probeToleranceSec": 10,
  "tolFingerprintSec": 2,
  "tolContainerSec": 5
}
```

`confidence` is `"confirmed"` only when **every** non-redundant assignment is
`TierConfirmed`; otherwise `"probable"`.

### 3.3 Review item fields

| Field | Value |
|---|---|
| `Kind` | `firstaid.redundant-copy` |
| `DedupKey` | `sha256("firstaid.redundant-copy|" + referenceBookId)` — one hold per reference book, so re-running upserts rather than duplicating (`database.UpsertReviewItem`, `internal/database/review_store.go:132`) |
| `FolderRef` | the reference book's `FilePath` |
| `Summary` | `"11 rows / 17 files duplicate 12 of 13 tracks of \"The Empty Place\" — combine + version-group"` |

Keying on the **reference book ID** rather than a folder is deliberate: debris is spread
across many folders, and the reference is the only stable identity in the group.

---

## 4. API / op surface

### 4.1 `maintenance.detect-redundant-copies` (new op)

```go
sdk.OperationDef{
    ID:              "maintenance.detect-redundant-copies",
    Plugin:          "maintenance",
    DisplayName:     "Detect redundant copies (dry-run · review-queue producer)",
    ResumePolicy:    sdk.ResumeDrop,
    DefaultPriority: sdk.PriorityLow,
    ConcurrencyKey:  "maintenance.detect-redundant-copies",
    Cancellable:     true,
    Isolate:         false,
    Timeout:         120 * time.Minute,
    Capabilities:    []sdk.Capability{sdk.CapLibraryRead}, // LD5: no CapFilesRead
    Run:             p.runDetectRedundantCopies,
}
```

Params:

```go
type detectRedundantParams struct {
    Limit              int    `json:"limit"`              // cap holds written (0 = all)
    ProbeToleranceSec  int    `json:"probeToleranceSec"`  // default 10
    TolFingerprintSec  int    `json:"tolFingerprintSec"`  // default 2
    TolContainerSec    int    `json:"tolContainerSec"`    // default 5
    MinReferenceTracks int    `json:"minReferenceTracks"` // default 3
    MaxDebrisFiles     int    `json:"maxDebrisFiles"`     // default 8
    MinMatchableSec    int    `json:"minMatchableSec"`    // default 60
    MaxBucketFanout    int    `json:"maxBucketFanout"`    // default 64
    HistogramOnly      bool   `json:"histogramOnly"`      // compute + log the histogram, write ZERO holds
    ReferenceBookID    string `json:"referenceBookId"`    // single-reference canary
}
```

There is no `apply` field — see LD9.

Phases:

| Phase | Work | Concurrency |
|---|---|---|
| 1/5 | `store.ListBookIDs()` (`internal/database/iface_book.go:50`) | — |
| 2/5 | Per book: `GetBookByID` + `GetBookFiles`, build `[]dupmatch.Track`. Skip non-primary, soft-deleted, frozen-iTunes. | `registry.RunItems` at `runtime.NumCPU()` (`internal/operations/registry/run_items.go:82`), `ErrModeCollect`, results collected under a mutex — the exact shape `relink_unlinked.go:117-156` uses |
| 3/5 | Build the duration index over **reference-eligible** books (`len(files) >= MinReferenceTracks`) | single-threaded, pure map build |
| 4/5 | Per debris-eligible book (`1 <= len(files) <= MaxDebrisFiles`): probe the index, tally per candidate reference, pick the best | `registry.RunItems` at `runtime.NumCPU()`; **read-only**, results under a mutex |
| 5/5 | Group debris by reference, run `dupmatch.MatchTemplate` once per reference, `UpsertReviewItem` | single-threaded (writes) |

**Why phase 5 is single-threaded:** it is the only phase that writes, the hold count is
bounded by the number of references (hundreds, not tens of thousands), and
`UpsertReviewItem`/`DeleteReviewItem` share one mutex — a pool would add contention only.
Same reasoning as `reconcileStaleHolds` (`regroup_shattered_ai.go:286-292`).

**Hold-emission threshold**, per debris **book**:

- `>= 2` distinct non-redundant slots matched, **or**
- exactly 1 slot matched **and** at least one corroborator on that assignment
  (`SkipNoCorroborator` otherwise).

The single-file rule exists because single-file debris is where false positives live: one
duration coincidence between two unrelated 40-minute files is entirely plausible; two
independent coincidences against the same reference is not.

### 4.2 `ApplyRedundantCopy(store database.Store, combiner bookCombiner) func(context.Context, database.ReviewItem) error`

Registered in `internal/server/wire_handlers.go` next to the existing regroup handlers
(`wire_handlers.go:598-608`). `bookCombiner` is the existing narrow interface
(`regroup_apply.go:68-70`) — reuse it, do not widen it.

Ordered steps, all of which abort before any write on failure:

1. Decode payload.
2. `GetBookByID(referenceBookId)`; nil or `MarkedForDeletion` → error (the hold is about
   this book; there is nothing to group against).
3. **LD4 guard:** `referenceBookId ∈ debrisBookIds` → error, zero writes.
4. **LD3 re-check:** `config.UnderFrozenITunesTree` on the reference's file paths and on
   each surviving debris book's paths → error listing the offenders, zero writes.
5. `present := presentMembers(store, debrisBookIds)` — reuse the existing helper verbatim
   (`regroup_apply.go:338-358`); it already skips hard-deleted and soft-deleted rows,
   which matters because a hold can be days older than its approve.
6. `len(present) == 0` → log and return nil (already applied; idempotent re-approve).
7. `len(present) >= 2` → `survivorID := pickPrimary(present)` (`regroup_apply.go:364`,
   earliest ULID among the **debris**) then
   `combiner.CombineBooks(present, survivorID, nil)`.
   **`nil` override is mandatory** — a non-nil override is the only thing that makes
   `merge.Service` call `UpdateBook` on the survivor (`internal/merge/service.go:444-457`),
   and that is this repo's dominant incident class.
   `len(present) == 1` → `survivorID = present[0]`, no combine.
8. `applyTemplateTrackNumbers(store, survivorID, payload)` — §4.3.
9. `linkVersionGroup(store, []string{referenceBookId, survivorID}, pick)` — §4.4, where
   `pick` is `dupmatch.PickMostComplete` over
   `{reference: referenceTrackCount, refFileCount}` and
   `{survivor: len(coveredTracks), survivorFileCount}`.
10. Log a single structured line with survivor, group ID, primary, files numbered, files
    left unnumbered.

### 4.3 `applyTemplateTrackNumbers` (new, per LD7)

```go
// applyTemplateTrackNumbers stamps the template's slot numbers onto the combined
// survivor's files. Returns (numbered, leftUnnumbered, error).
//
// NOT applyDiscTrackNumbers: that function bails on the WHOLE group if any file
// already carries disc/track metadata (regroup_apply.go:155-162), and
// relink-unlinked-books stamps track = i+1 on every directory-shaped book it
// repairs (relink_unlinked.go:355). Reusing it here would silently number nothing.
//
// DATA-LOSS SAFETY: re-fetch-and-patch. GetBookFiles returns FULL rows (fingerprint
// intact); we mutate ONLY TrackNumber and DiscNumber and write via UpdateBookFile,
// which itself restores AcoustIDFingerprint on an empty incoming value. Never a
// fresh or partial BookFile write-back.
func applyTemplateTrackNumbers(store database.Store, survivorID string, p redundantCopyPayload) (int, int, error)
```

- Winner assignments (`redundant == false`) → `TrackNumber = refTrack`, `DiscNumber = 0`
  (discs are flattened away by owner decision — `fs_regroup_shape.go:104-110`).
- Redundant + skipped files → `TrackNumber = 0`, `DiscNumber = 0`. Zero means "position
  unknown, deliberately". Writing 0 rather than leaving a stale relink-derived number
  prevents a redundant file presenting as a real track and colliding with its winner.
- Per-file idempotency: skip the write when the row already carries the target values.
- Files on the survivor that the payload does not mention at all are left untouched and
  counted.

### 4.4 `linkVersionGroup` — extracted shared helper (per LD6)

New file `internal/plugins/maintenance/version_group_link.go`:

```go
// linkVersionGroup applies the version-group invariants to a member set, with the
// primary-selection policy INJECTED.
//
// Extracted from ApplyVersionGroup so the three hard-won invariants below exist
// exactly once:
//   - existing-group reuse (idempotent re-approve)                regroup_apply.go:222-224,246-252
//   - REFUSE members spanning two existing groups                 regroup_apply.go:236-245
//   - demote any stale primary across the WHOLE group             regroup_apply.go:286-312
//
// Members are re-fetched and patched (UpdateBook is a full-column replace); only
// VersionGroupID and IsPrimaryVersion are ever mutated.
func linkVersionGroup(
    store database.Store,
    memberIDs []string,
    pick func(books []*database.Book) string,
    logKV ...any,
) (groupID string, primaryID string, err error)
```

`ApplyVersionGroup` becomes a thin wrapper passing `pickEarliestULIDBook` — behaviour
byte-for-byte unchanged, proven by the existing `regroup_apply_test.go` passing without
edits. `ApplyRedundantCopy` passes a closure over `dupmatch.PickMostComplete`.

### 4.5 Frontend

- `web/src/lib/reviewKinds.ts` → add
  `'firstaid.redundant-copy': 'Redundant copies (assembled original exists)'`.
  Without this the queue page renders the fallback title-case
  (`reviewKinds.ts:20-27`) — functional but wrong-looking.
- `web/src/lib/reviewPayload.ts` → the `ReviewPayload` interface already has an index
  signature (`reviewPayload.ts:31`), so the new keys parse without change; add optional
  typed fields `referenceBookId`, `coveredTracks`, `missingTracks`,
  `internallyRedundant`, `excludedFrozen` for the card, and `memberIDs()` gains a
  `debrisBookIds` fallback so the existing member-count rendering works.

No new page or component. The existing `ReviewQueue.tsx` list rendering is sufficient
for v1; a richer card is a follow-up.

---

## 5. Index design and its two traps

The index is `map[int][]indexEntry`, keyed by whole seconds, queried over
`[d - probeTol, d + probeTol]`.

```go
type indexEntry struct {
    BookID    string
    FileID    string
    Slot      int    // 1-based position in the reference's ordered track list
    DurSec    int
    Source    dupmatch.DurSource
    TitleStem string
}
```

**Memory budget.** ~275K `book_file` rows library-wide (fingerprint coverage figure,
`internal/plugins/maintenance/duration_reextract.go:35-38`). Only reference-eligible
books are indexed, so realistically well under that; at ~120 bytes/entry the worst case
is ~35 MB. **Do not store `database.BookFile` structs in the index** — `RawTags` and
`AcoustIDFingerprint` alone would make this hundreds of MB.

**Trap 1 — `DurSec <= 0` must never be indexed or queried.** A zero-duration bucket would
match everything against everything. Enforced by `Source == SourceUnknown` short-circuits
in both the builder and the matcher (LD2).

**Trap 2 — bucket fan-out.** Exactly-30-minute chapters exist all over a library. A
debris file whose candidate-slot count exceeds `MaxBucketFanout` (default 64) is
**skipped with `SkipAmbiguousBucket` and counted**, never resolved by "take the first".
Silently taking the first is precisely the silent-filtering failure rule 6 exists to
prevent, and it would be invisible in the output.

---

## 6. The dry-run acceptance gate (tolerance is NOT locked by this document)

The fragment proposes ±2 s; the 17 measured files showed deltas of 0–1 s. That is one
group. The tolerance is locked only after a whole-library dry run at
`probeToleranceSec: 10` satisfies **all four** of:

**(a) Histogram shape.** ≥95 % of accepted matches at `|delta| <= 1 s`, with a visible
trough between the true-match mode and the noise floor. If the histogram is flat out to
10 s, duration matching is not separating signal from noise at this library's data
quality and the workstream stops here rather than shipping a guess.

**(b) The measured case reproduces exactly.**
- `01KQAXKG7HYMT44GRNCGSJBXG1` is selected as the reference;
- **12 of 13** tracks covered, `missingTracks == [7]`;
- **17** debris files across **11** debris books;
- **5** entries in `internallyRedundant`;
- **10** frozen-iTunes rows in `excludedFrozen` — present and counted, not silently
  dropped.

**(c) Reconciliation.**
`examined == matched + unmatched + skipped-no-duration + skipped-frozen + skipped-short + skipped-ambiguous-bucket + errors`,
on the `RECONCILE:` line, with `examined` tying back to `len(ListBookIDs())`.

**(d) The single-file share is reported separately.** The summary must break the
library-wide match count into "debris books contributing ≥2 slots" vs "debris books
contributing exactly 1 slot (corroborator-gated)". Single-file debris is where false
positives live; if it dominates, the corroborator rule needs tightening before any apply.

---

## 7. Non-goals

1. **Fixing the reference's inherited title.** `01KQAXKG7HYMT44GRNCGSJBXG1` should read
   "The Successors", not "The Empty Place". That is `maintenance.title-repair` /
   `metadata-refresh` territory (tier 3 / tier 4 in the roster,
   `.claude/notes/2026-08-05-first-aid-architecture.md:119-125`). The detector **reports**
   the signal (`referenceTitleLooksInherited`) and changes nothing.
2. **Probing durations.** LD5. Missing durations are reported with
   `maintenance.duration-reextract` named as the producer.
3. **Deleting anything.** No delete disposition exists in `linkintegrity`
   (`internal/linkintegrity/report.go:60-70`) and none is added.
4. **Touching `books/itunes/**`.** LD3.
5. **Resolving the internally-redundant debris files.** They are reported and left with
   `TrackNumber = 0`. Deciding which of two copies of track 6 is "the" copy is a human
   call and there is no forced choice.
6. **Folding this into the dedup subsystem.** Owner decision D5
   (`.claude/notes/2026-08-05-first-aid-architecture.md:83-85`): dedup keeps its own
   queue, gold labels and calibrated thresholds. This shares the *matching idea*, not the
   orchestration. Folding it in wholesale is how 57 ops accumulated.
7. **Orchestration / sequencing / missing-input triggering.** Separate workstream
   (`todo.d/…:31,33-38`).
8. **Auto-apply.** LD9. Approve is a human action behind a globally-off switch.
9. **Chapter-level or fingerprint-only matching.** Fingerprints are a corroborator only;
   65 % of files are unfingerprinted, so a fingerprint-primary design would be inert
   (`MEMORY` → matching + dedup findings, 2026-07-02).

---

## 8. Failure modes

| # | Failure | Detection | Behaviour |
|---|---|---|---|
| F1 | Debris file has no usable duration | `Source == SourceUnknown` | `SkipNoDuration`, counted, file ID reported, `maintenance.duration-reextract` named. **Never** treated as "no match" in the confidence calculation (LD2) |
| F2 | Two debris files claim one reference slot | greedy assign, slot already taken | later claimant → `Redundant = true`, retains `RefTrackIndex`, gets `TrackNumber = 0` on apply, listed in `internallyRedundant` |
| F3 | Reference has an inherited/junk title | `Book.Title == files[0].Title` | `referenceTitleLooksInherited: true`, reported only (§7.1) |
| F4 | A debris row is soft-deleted between hold and approve | `presentMembers` (`regroup_apply.go:350`) | skipped with a reason; if that leaves 0, the apply is a logged no-op |
| F5 | Reference vanished or soft-deleted between hold and approve | step 2 | error → item lands in `failed` with a reason; zero writes |
| F6 | Reference appears in `debrisBookIds` (payload corruption, hand-edit, future bug) | step 3 guard | error, zero writes. Unit-tested |
| F7 | Reference or a debris book is under `books/itunes/**` | LD3 re-check at apply | error listing offenders, zero writes |
| F8 | Reference and debris survivor already sit in **two different** version groups | `linkVersionGroup` cross-group refusal (`regroup_apply.go:236-245`) | error, item → `failed`, human resolves. Never silently merges groups |
| F9 | Re-approve of an already-applied hold | `presentMembers` returns 0 or 1; `linkVersionGroup` reuses the existing group; per-file numbering skips equal values | idempotent no-op |
| F10 | `CombineBooks` fails partway | it aborts before `DeleteBook` if the row still owns files (`internal/merge/service.go:427-431`) | error propagates; item → `failed`; no version-group write happens (step 9 is after step 7) |
| F11 | Duration bucket over-populated (e.g. many exactly-30-min chapters) | `MaxBucketFanout` | `SkipAmbiguousBucket`, counted. Never "take the first" |
| F12 | Whole-library scan silently truncates | `RECONCILE` line + `Report.UnreconciledPhases()` (`internal/linkintegrity/report.go:172-181`) | op returns an error rather than reporting success (LD10) |
| F13 | A single duration coincidence produces a bogus single-file match | corroborator requirement | `SkipNoCorroborator`, counted; the single-file share is reported separately in the summary (gate (d)) |
| F14 | Two concurrent applies touch the same books | `mergeSerializeMu` inside `CombineBooks` (`internal/merge/service.go:353`) serializes the whole merge-family RMW | second waits; `presentMembers` then sees the absorbed rows as gone and no-ops |
| F15 | Op cancelled mid-run | `ctx.Err()` checks in the write loop, `Cancellable: true` | partial holds already upserted are valid (idempotent DedupKey); a re-run completes the set |

---

## 9. Open questions

1. **Is `MaxBucketFanout = 64` right?** It is a guess. The first dry run should log the
   bucket-size distribution so the value can be set from data rather than intuition.
2. **Should a `probable`-confidence hold be applicable at all**, or only `confirmed`?
   Current design lets a human approve either (the switch is off by default anyway). If
   the dry run shows `probable` dominating, consider registering the apply handler only
   for `confirmed` and routing `probable` to a handler-less display-only path, exactly as
   `regroup.ambiguous` is handled today (`regroup_apply.go:19-21`).
3. **Reference eligibility floor.** `MinReferenceTracks = 3` excludes 2-track references.
   Unmeasured; the dry run should report how many candidate references sit at exactly 2.
4. **Should the detector emit a `linkintegrity.Finding` with
   `DispositionVersionGroup`/`DuplicateOfBookID`?** Those fields already exist
   (`internal/linkintegrity/report.go:76-78,112-115`) and are currently unused. Wiring the
   detector's output into the shared `Report` would let the First Aid orchestrator
   aggregate it. Deferred to the orchestrator workstream to avoid coupling this PR to a
   design that is not settled.
5. **Cross-reference debris.** A debris book whose files match slots in *two* different
   reference books is currently assigned to the best-scoring one. Whether it should
   instead be held as ambiguous is unmeasured; the dry run should count how often it
   happens.
