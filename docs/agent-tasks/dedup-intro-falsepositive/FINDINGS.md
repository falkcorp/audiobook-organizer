<!-- file: docs/agent-tasks/dedup-intro-falsepositive/FINDINGS.md -->
<!-- version: 1.0.0 -->
<!-- guid: f0a2c4e6-8b1d-4f3a-9c2e-7d5b3a1f9e0c -->
<!-- last-edited: 2026-06-28 -->

# TASK-01 Findings: Intro/Outro Fingerprint False Positives

## Scope Boundary

No production or live library query was run for this investigation. The task
permits read-only code inspection and existing reports, and requires operator
approval before production access. Therefore, live counts below are represented
as exact read-only queries/operators can run against the authoritative Pebble
store.

## 1. Exact Match Path

There are two AcoustID paths to distinguish:

1. Direct `acoustid` candidate emission, which is the path that turns a shared
   file fingerprint into a persisted book-pair candidate:
   - `internal/dedup/engine.go:2856-2883`: local `emit(bookAID, bookBID, sim)`
     canonicalizes/deduplicates pairs in memory, suppresses same-directory
     chapter siblings, then calls `embedStore.UpsertCandidate` with
     `Layer: "acoustid"` and `Status: "pending"`.
   - `internal/dedup/engine.go:2933-2949`: Tier-0 whole-file LSH lookup uses
     `LookupAcoustIDCandidates`, refines with
     `fingerprint.WholeFileSimilarity`, and emits when similarity is at least
     `fingerprint.FuzzyMinSimilarity`.
   - `internal/dedup/engine.go:2974-2978`: Tier-1 exact segment lookup calls
     `bookStore.GetBookFileByAcoustID(seg)` and emits `sim=1.0` when the hit
     belongs to another book.
   - `internal/database/pebble_store.go:9762-9783`: `GetBookFileByAcoustID`
     reads the `book_file_acoustid:<fingerprint>` secondary index and returns
     the indexed `BookFile`.

2. Unified scoring AcoustID signal collection, which currently re-scores
   already existing pending pairs rather than discovering all pair candidates:
   - `internal/dedup/engine.go:399-429`: `runUnifiedScoringForBook` loads
     pending book candidates with `ListCandidatesForEntity`; if no pending
     candidates exist, it returns.
   - `internal/dedup/engine.go:451-457`: for each file in the anchor book,
     calls `CollectExactAcoustID`.
   - `internal/dedup/collectors_acoustid.go:90-145`: `CollectExactAcoustID`
     checks legacy `AcoustIDSeg0..6`, skips empty/degenerate fingerprints,
     calls `GetBookFileByAcoustID`, and emits `SigExactAcoustID` at confidence
     `0.99` for a different-book hit.
   - `internal/dedup/engine.go:561-570`: filters AcoustID signals to the
     current candidate book ID.
   - `internal/dedup/engine.go:577-599`: `ComposeScore` is run, then
     `UpsertCandidate` persists the scored candidate.
   - `internal/dedup/engine.go:641-690`: `bestLayerFromSignals` maps
     `SigExactAcoustID` / `SigLSHAcoustID` to legacy layer `"acoustid"`.

The direct false-positive risk is the direct AcoustID scan path. It does not
check file duration, boilerplate file titles, or book-level ISBN/ASIN divergence
before `emit`.

## 2. Duration Histogram Query

Existing safe endpoints are insufficient for this histogram:

- `GET /api/v1/dedup/candidates?entity_type=book&status=pending&layer=acoustid&include_books=true&limit=...`
  lists candidate rows, but not participating `BookFile` rows or durations
  (`internal/server/handlers/dedup/handler.go:104-182`).
- `GET /api/v1/dedup/candidates/export?format=json&entity_type=book&status=pending&layer=acoustid`
  exports candidate rows up to 100k, but still lacks file-level duration
  (`internal/server/handlers/dedup/handler.go:428-477`).
- `GET /api/v1/maintenance/acoustid-stats` reports fingerprint coverage only
  (`internal/server/maintenance_fixups.go:551-564`,
  `internal/database/pebble_store.go:10837-10903`).

Run this exact read-only operator report against a quiescent copy/snapshot of
the Pebble DB, not a live mutable store:

```go
ps, _ := database.NewPebbleStore(pebblePath)
defer ps.Close()
es := database.NewEmbeddingStore(ps.DB())

cands, total, _ := es.ListCandidates(database.CandidateFilter{
    EntityType: "book",
    Status:     "pending",
    Layer:      "acoustid",
    Limit:      1000000,
})

hist := map[string]int{"<30s": 0, "30-60s": 0, "60-120s": 0, ">120s": 0}
titles := map[string]int{}
isbnAsinCompared := 0
isbnAsinDifferent := 0

for _, c := range cands {
    a, _ := ps.GetBookByID(c.EntityAID)
    b, _ := ps.GetBookByID(c.EntityBID)
    af, _ := ps.GetBookFiles(c.EntityAID)
    bf, _ := ps.GetBookFiles(c.EntityBID)

    matchingShortSideTitles := matchingAcoustIDFileTitles(af, bf)
    for _, mt := range matchingShortSideTitles {
        bucket := durationBucket(mt.DurationSec)
        hist[bucket]++
        if mt.DurationSec < 120 {
            titles[normalizedTitleOrFilename(mt)]++
        }
    }

    if hasAnyID(a) && hasAnyID(b) {
        isbnAsinCompared++
        if deref(a.ISBN10) != deref(b.ISBN10) ||
           deref(a.ISBN13) != deref(b.ISBN13) ||
           deref(a.ASIN) != deref(b.ASIN) {
            isbnAsinDifferent++
        }
    }
}

fmt.Printf("total_acoustid_candidates=%d\n", total)
fmt.Printf("duration_histogram=%+v\n", hist)
fmt.Printf("top_short_titles=%+v\n", topN(titles, 50))
fmt.Printf("isbn_asin_mismatch=%d/%d %.2f%%\n",
    isbnAsinDifferent, isbnAsinCompared,
    100*float64(isbnAsinDifferent)/float64(isbnAsinCompared))
```

Implementation detail for `matchingAcoustIDFileTitles`: treat two files as
matched if either whole-file similarity satisfies the same engine predicate used
at `internal/dedup/engine.go:2943-2948`, or any useful legacy segment string
matches exactly as in `internal/dedup/engine.go:2956-2978`. Use
`BookFile.Duration` first, falling back to
`BookFile.AcoustIDFingerprintDurationSec` (`internal/database/store.go:687` and
`:704-707`).

Expected output shape:

```text
total_acoustid_candidates=N
duration_histogram=map[<30s:A 30-60s:B 60-120s:C >120s:D]
top_short_titles=[{"title":"...","count":N}, ...]
isbn_asin_mismatch=X/Y Z.ZZ%
```

## 3. Recurring Boilerplate Titles

Because no live corpus query was run, the confirmed output must come from the
operator query above. Seed TASK-03 with this concrete normalized blocklist and
replace/extend it with `top_short_titles` from the report:

- `this is audible`
- `audible hopes you have enjoyed this program`
- `audible hopes you have enjoyed this book`
- `audible studios presents`
- `audible presents`
- `this is an audible original`
- `end credits`
- `credits`
- `opening credits`
- `closing credits`
- `intro`
- `introduction`
- `outro`
- `epilogue music`
- `publisher introduction`
- `publisher's note`
- `produced by audible studios`
- `recorded books presents`
- `graphic audio presents`
- `brilliance audio presents`

TASK-03 should normalize case, punctuation, apostrophes, and surrounding track
numbers before comparing. It should match exact normalized titles and a small
set of anchored phrases like `^this is audible\b`, not arbitrary substring
matches inside real chapter titles.

## 4. ISBN/ASIN Mismatch Query

The same read-only operator report above computes the sample rate. Compare all
non-empty fields that exist on both books:

- `Book.ISBN10` at `internal/database/store.go:139`
- `Book.ISBN13` at `internal/database/store.go:140`
- `Book.ASIN` at `internal/database/store.go:141`

Recommended output metric:

```text
isbn_asin_mismatch = pairs_with_any_shared_short_fingerprint_and_conflicting_nonempty_id /
                     pairs_with_any_id_on_both_sides
```

For TASK-04, treat one known conflicting non-empty external ID as enough to
block a short-file-only AcoustID candidate. If the two books share at least one
non-empty ISBN10, ISBN13, or ASIN, allow the candidate to continue through normal
scoring. If one or both sides have no external IDs, do not block solely on IDs;
use the duration and title gates instead.

## 5. Recommendations

- TASK-02 duration cutoff: skip direct AcoustID candidate emission for any
  matching file where either participating file is `< 60` seconds. Keep a
  diagnostic counter for `60-120` seconds, but do not block those initially
  unless the operator histogram shows a dominant boilerplate cluster above 60s.
- TASK-03 title blocklist: apply the normalized blocklist in section 3 to
  `BookFile.Title` and then `OriginalFilename` fallback before direct AcoustID
  `emit`. Block when either participating matched file has a blocklisted short
  title and is `< 120` seconds.
- TASK-04 ID gate: before direct AcoustID `emit`, load both `Book` rows and
  compare `ISBN10`, `ISBN13`, and `ASIN`. If any field is non-empty on both
  sides and differs, suppress the candidate when the only acoustic evidence is a
  short-file match. Do not suppress when at least one non-empty ID field matches.
- Preserve the same-directory suppression already present at
  `internal/dedup/engine.go:2861-2870`.
- Add tests around `AcoustIDScan` direct emission, not only
  `CollectExactAcoustID`, because the direct path is where the candidate row is
  first created.
