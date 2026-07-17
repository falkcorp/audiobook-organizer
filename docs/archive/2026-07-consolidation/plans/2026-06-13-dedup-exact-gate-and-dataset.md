<!-- file: docs/plans/2026-06-13-dedup-exact-gate-and-dataset.md -->
<!-- version: 1.0.0 -->
<!-- guid: 7f2c9a14-5e63-4b80-9d21-3a8c6f4e1b09 -->
<!-- last-edited: 2026-06-13 -->

# Dedup Exact-Gate + Tuning Dataset (M1) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Stop the live dedup "exact" layer from flagging stub/unscanned files as 100% duplicates, then build the labeled-dataset foundation (feature builder + deterministic catchers + store) and a backfill op that cleans the existing residual.

**Architecture:** Three milestones. **A** adds a file-validity gate to the metadata-based exact emitters in `internal/dedup/engine.go` (root-cause fix for the post-cutover residual). **B** adds `internal/dedup/dataset` (pure feature builder + catcher predicates over a new `database.LabeledExample`) plus a `dedup:label:` Pebble keyspace on `EmbeddingStore`. **C** adds a `dedup.dataset-backfill` UOS op that runs the catchers over all candidates, writes labeled examples, and (with `apply`) suppresses the missing-file / part-vs-whole residual. All additive; dry-run by default.

**Tech Stack:** Go 1.24, PebbleDB (`github.com/cockroachdb/pebble/v2`), the UOS plugin SDK (`pkg/plugin/sdk`), existing `internal/dedup` + `internal/database` packages, `internal/fingerprint/book_signature.go`.

**Spec:** `docs/specs/2026-06-13-dedup-tuning-dataset-design.md`. This plan covers the root-cause fix + spec milestones M1 and (the cleanup half of) M2. Live capture, auto-bug-filing (C5), review UI (C6) and JSONL export (C7) are a follow-up plan.

---

## File Structure

| File | Responsibility | New/Modify |
|---|---|---|
| `internal/dedup/engine.go` | `hasPlausibleAudio` helper + gate in `checkExactTitle`/`checkExactISBN` | Modify |
| `internal/dedup/engine_test.go` | Engine gate tests | Modify |
| `internal/database/dedup_label.go` | `LabeledExample` type + `dedup:label:` store methods | Create |
| `internal/database/dedup_label_test.go` | Store round-trip + filter tests | Create |
| `internal/dedup/dataset/builder.go` | `BuildExample` — pure feature computation per candidate | Create |
| `internal/dedup/dataset/builder_test.go` | Builder unit tests | Create |
| `internal/dedup/dataset/rules.go` | Deterministic catcher predicates | Create |
| `internal/dedup/dataset/rules_test.go` | Catcher unit tests | Create |
| `internal/plugins/dedup/dataset_backfill.go` | `dedup.dataset-backfill` op | Create |
| `internal/plugins/dedup/plugin.go` | Register the new op in the `ops` slice | Modify |

**Boundaries:** the `dataset` package is pure (store-interface in, `database.LabeledExample` out, no side effects). `LabeledExample` lives in `internal/database` (alongside `DedupCandidate`) so the store can marshal it without an import cycle (`database` must not import `dedup`). The op is the only component with side effects.

---

## Milestone A — Root-cause fix: file-validity gate on the exact emitters

**Why:** `checkExactTitle` (`engine.go:815`) emits `layer="exact"`, `sim=1.0` on title+author match with no check that either book has real audio. Verified on prod: 300/300 residual pairs are title-equal, 279/300 have one side that is a stub (<100 KB) or unscanned (`duration=0`). File **size** is the right validity signal — genuine same-file-different-folder duplicates keep a large size even when unscanned (184 MB, `duration=0`), while stub placeholders are 32–182 bytes.

### Task 1: `hasPlausibleAudio` helper

**Files:**
- Modify: `internal/dedup/engine.go` (add helper near `hasUsableTitle`, line ~1050)
- Test: `internal/dedup/engine_test.go`

- [ ] **Step 1: Write the failing test**

Add to `internal/dedup/engine_test.go`:

```go
func TestHasPlausibleAudio(t *testing.T) {
	dur := func(v int) *int { return &v }
	sz := func(v int64) *int64 { return &v }

	cases := []struct {
		name string
		book *database.Book
		want bool
	}{
		{"nil book", nil, false},
		{"32-byte stub, no duration", &database.Book{FileSize: sz(32)}, false},
		{"182-byte stub, no duration", &database.Book{FileSize: sz(182)}, false},
		{"large unscanned copy (genuine dupe)", &database.Book{FileSize: sz(184_741_714)}, true},
		{"positive duration, no size", &database.Book{Duration: dur(3600)}, true},
		{"empty book", &database.Book{}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := hasPlausibleAudio(tc.book); got != tc.want {
				t.Fatalf("hasPlausibleAudio(%s) = %v, want %v", tc.name, got, tc.want)
			}
		})
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/dedup/ -run TestHasPlausibleAudio -v`
Expected: FAIL — `undefined: hasPlausibleAudio`

- [ ] **Step 3: Write minimal implementation**

Add to `internal/dedup/engine.go` (just below `hasUsableTitle`):

```go
// minPlausibleAudioBytes is the smallest file size we treat as a real audio
// file. Anything smaller is a placeholder/stub (a 32-byte .url shortcut, a
// 182-byte broken download) that must never anchor an exact-duplicate match.
const minPlausibleAudioBytes = 256 * 1024 // 256 KiB

// hasPlausibleAudio reports whether a book references real audio content rather
// than a stub or a never-scanned placeholder. A book qualifies if it has a
// positive duration OR a file size at/above the plausible-audio floor. This is
// the engine-side counterpart to the dataset missingFile catcher: it stops the
// exact-title / ISBN emitters from flagging "100% duplicate" when one side is a
// 32-byte stub or an unscanned shell. A large unscanned copy (real size, zero
// duration) still qualifies — it is a genuine duplicate, not garbage.
func hasPlausibleAudio(book *database.Book) bool {
	if book == nil {
		return false
	}
	if book.Duration != nil && *book.Duration > 0 {
		return true
	}
	if book.FileSize != nil && *book.FileSize >= minPlausibleAudioBytes {
		return true
	}
	return false
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/dedup/ -run TestHasPlausibleAudio -v`
Expected: PASS (all 6 subtests)

- [ ] **Step 5: Commit**

```bash
git add internal/dedup/engine.go internal/dedup/engine_test.go
git commit -m "feat(dedup): add hasPlausibleAudio file-validity helper"
```

### Task 2: Gate `checkExactTitle`

**Files:**
- Modify: `internal/dedup/engine.go:815-887` (`checkExactTitle`)
- Test: `internal/dedup/engine_test.go`

- [ ] **Step 1: Write the failing test**

Add to `internal/dedup/engine_test.go`. This mirrors the `setupTestEngine` pattern used by `TestEngine_ExactMatch_ISBN`:

```go
func TestEngine_ExactTitle_RejectsStubFile(t *testing.T) {
	engine, mock, es := setupTestEngine(t)
	authorID := "author-1"
	sz := func(v int64) *int64 { return &v }

	real := &database.Book{ID: "book-real", Title: "Iron and Blood", AuthorID: &authorID, FileSize: sz(254_192_471)}
	stub := &database.Book{ID: "book-stub", Title: "Iron and Blood", AuthorID: &authorID, FileSize: sz(32)}
	mock.SetBooksByAuthor(authorID, []database.Book{*real, *stub})

	if err := engine.checkExactTitle(real, "Joshua Dalzelle"); err != nil {
		t.Fatalf("checkExactTitle: %v", err)
	}

	cands, _, err := es.ListCandidates(database.CandidateFilter{Limit: 100})
	if err != nil {
		t.Fatalf("list candidates: %v", err)
	}
	if len(cands) != 0 {
		t.Fatalf("expected 0 candidates (stub side must be rejected), got %d", len(cands))
	}
}

func TestEngine_ExactTitle_KeepsGenuineLargePair(t *testing.T) {
	engine, mock, es := setupTestEngine(t)
	authorID := "author-2"
	sz := func(v int64) *int64 { return &v }

	a := &database.Book{ID: "book-a", Title: "Departure from the Script", AuthorID: &authorID, FileSize: sz(184_741_714)}
	// Genuine same-file copy in another folder: large size, not yet scanned.
	b := &database.Book{ID: "book-b", Title: "Departure from the Script", AuthorID: &authorID, FileSize: sz(184_741_714)}
	mock.SetBooksByAuthor(authorID, []database.Book{*a, *b})

	if err := engine.checkExactTitle(a, "Jae"); err != nil {
		t.Fatalf("checkExactTitle: %v", err)
	}

	cands, _, err := es.ListCandidates(database.CandidateFilter{Limit: 100})
	if err != nil {
		t.Fatalf("list candidates: %v", err)
	}
	if len(cands) != 1 {
		t.Fatalf("expected 1 candidate (genuine large dupe kept), got %d", len(cands))
	}
}
```

> NOTE: if `database.MockStore` does not already expose `SetBooksByAuthor`, add a thin setter mirroring its existing `GetBooksByAuthorID` backing map (check `internal/database/mock_store.go`; the existing `TestEngine_ExactMatch_ISBN` shows the established way to seed books — follow whichever seeding method that test uses, e.g. `mock.AddBook(...)`).

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/dedup/ -run 'TestEngine_ExactTitle_RejectsStubFile|TestEngine_ExactTitle_KeepsGenuineLargePair' -v`
Expected: `TestEngine_ExactTitle_RejectsStubFile` FAILS (1 candidate emitted for the stub pair); the "Keeps" test passes.

- [ ] **Step 3: Add the gate**

In `internal/dedup/engine.go`, in `checkExactTitle`, add the book-level guard after the existing `hasUsableTitle(book.Title)` check (line ~821):

```go
	if !hasUsableTitle(book.Title) {
		return nil
	}
	if !hasPlausibleAudio(book) {
		return nil // stub / unscanned shell — never anchor an exact-title match
	}
```

And inside the `for i := range others` loop, after the existing `hasUsableTitle(other.Title)` check (line ~837):

```go
		if !hasUsableTitle(other.Title) {
			continue
		}
		if !hasPlausibleAudio(other) {
			continue // stub / unscanned shell on the other side
		}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/dedup/ -run 'TestEngine_ExactTitle' -v`
Expected: both PASS. Then run the full package to confirm no regression: `go test ./internal/dedup/...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/dedup/engine.go internal/dedup/engine_test.go
git commit -m "fix(dedup): gate exact-title emitter on file validity (stop stub matches)"
```

### Task 3: Gate `checkExactISBN`

**Files:**
- Modify: `internal/dedup/engine.go:712-768` (`checkExactISBN`)
- Test: `internal/dedup/engine_test.go`

- [ ] **Step 1: Write the failing test**

```go
func TestEngine_ExactISBN_RejectsStubFile(t *testing.T) {
	engine, mock, es := setupTestEngine(t)
	sz := func(v int64) *int64 { return &v }
	isbn := func(v string) *string { return &v }

	real := &database.Book{ID: "r", Title: "X", ISBN13: isbn("9781234567890"), FileSize: sz(120_000_000)}
	stub := &database.Book{ID: "s", Title: "X", ISBN13: isbn("9781234567890"), FileSize: sz(182)}
	mock.SetAllBooks([]database.Book{*real, *stub}) // backs GetAllBooks

	if err := engine.checkExactISBN(real); err != nil {
		t.Fatalf("checkExactISBN: %v", err)
	}
	cands, _, _ := es.ListCandidates(database.CandidateFilter{Limit: 100})
	if len(cands) != 0 {
		t.Fatalf("expected 0 candidates (stub rejected), got %d", len(cands))
	}
}
```

> NOTE: use whichever `GetAllBooks` seeding helper the existing `TestEngine_ExactMatch_ISBN` uses; replace `SetAllBooks` with that.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/dedup/ -run TestEngine_ExactISBN_RejectsStubFile -v`
Expected: FAIL — 1 candidate emitted.

- [ ] **Step 3: Add the gate**

In `checkExactISBN`, add an early guard after computing the book's ISBNs (after line ~719) and a per-`other` guard before the `matched` block:

```go
	if bookISBN10 == "" && bookISBN13 == "" && bookASIN == "" {
		return nil
	}
	if !hasPlausibleAudio(book) {
		return nil
	}
```

and inside the loop, right after `if other.ID == book.ID { continue }`:

```go
			if !hasPlausibleAudio(other) {
				continue
			}
```

- [ ] **Step 4: Run tests**

Run: `go test ./internal/dedup/...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/dedup/engine.go internal/dedup/engine_test.go
git commit -m "fix(dedup): gate exact-ISBN emitter on file validity"
```

---

## Milestone B — Dataset foundation (spec M1: C1 builder, C2 catchers, C3 store)

### Task 4: `LabeledExample` type + `dedup:label:` store

**Files:**
- Create: `internal/database/dedup_label.go`
- Test: `internal/database/dedup_label_test.go`

- [ ] **Step 1: Write the failing test**

Create `internal/database/dedup_label_test.go`:

```go
package database

import (
	"os"
	"testing"
)

func newTestEmbeddingStore(t *testing.T) *EmbeddingStore {
	t.Helper()
	dir, err := os.MkdirTemp("", "abk-label-*")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	ps, err := NewPebbleStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ps.Close() })
	return NewEmbeddingStore(ps.DB())
}

func TestLabeledExample_RoundTripAndFilter(t *testing.T) {
	es := newTestEmbeddingStore(t)

	ex := LabeledExample{
		CandidateID:      42,
		EntityAID:        "a",
		EntityBID:        "b",
		Layer:            "exact",
		Label:            "not_dup",
		LabelSource:      "rule",
		LabelReason:      "duration ratio 0.02 — part vs whole",
		FolderRelation:   "sibling_parts",
		SignatureRelation: "unknown",
	}
	if err := es.UpsertLabeledExample(ex); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	got, err := es.GetLabeledExample(42)
	if err != nil || got == nil {
		t.Fatalf("get: %v (nil=%v)", err, got == nil)
	}
	if got.LabelReason != ex.LabelReason || got.Label != "not_dup" {
		t.Fatalf("round-trip mismatch: %+v", got)
	}

	list, err := es.ListLabeledExamples(LabeledExampleFilter{Label: "not_dup", Limit: 10})
	if err != nil || len(list) != 1 {
		t.Fatalf("list by label: err=%v len=%d", err, len(list))
	}
	n, err := es.CountLabeledExamples(LabeledExampleFilter{LabelSource: "rule"})
	if err != nil || n != 1 {
		t.Fatalf("count by source: err=%v n=%d", err, n)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/database/ -run TestLabeledExample_RoundTripAndFilter -v`
Expected: FAIL — `undefined: LabeledExample` (and the store methods).

- [ ] **Step 3: Write the implementation**

Create `internal/database/dedup_label.go`:

```go
// file: internal/database/dedup_label.go
// version: 1.0.0
// guid: 1c4d7a90-2b35-4e68-8f01-9d2a5c7e3b46
// last-edited: 2026-06-13

package database

import (
	"encoding/binary"
	"encoding/json"
	"fmt"

	"github.com/cockroachdb/pebble/v2"
)

// dedupLabelPfx is the Pebble keyspace for labeled dedup examples.
// Key layout: dedup:label:<candidateID, 16-hex> → LabeledExample JSON.
const dedupLabelPfx = "dedup:label:"

// dedupLabelKey renders the fixed-width key for a candidate ID so that range
// scans over the prefix return rows in a stable order.
func dedupLabelKey(candidateID int64) []byte {
	return []byte(fmt.Sprintf("%s%016x", dedupLabelPfx, uint64(candidateID)))
}

// BookFeatures captures the per-book evidence a judge needs. Computed by the
// dataset feature builder, snapshotted at capture time.
type BookFeatures struct {
	Title             string   `json:"title"`
	Author            string   `json:"author"`
	PrimaryPath       string   `json:"primary_path"`
	TotalDurationSec  float64  `json:"total_duration_sec"`
	FileCount         int      `json:"file_count"`
	HasCover          bool     `json:"has_cover"`
	FilesExist        bool     `json:"files_exist"`
	RecordingIDs      []string `json:"recording_ids,omitempty"`
	ITunesPIDPresent  bool     `json:"itunes_pid_present"`
	WholeBookSigPresent bool   `json:"whole_book_sig_present"`
}

// LabeledExample is one labeled dedup candidate pair plus the features behind
// the label. Stored at dedup:label:<candidateID>.
type LabeledExample struct {
	CandidateID int64  `json:"candidate_id"`
	EntityAID   string `json:"entity_a_id"`
	EntityBID   string `json:"entity_b_id"`

	Layer          string       `json:"layer"`
	Band           string       `json:"band,omitempty"`
	Score          float64      `json:"score,omitempty"`
	ScoreBreakdown json.RawMessage `json:"score_breakdown,omitempty"`
	Similarity     *float64     `json:"similarity,omitempty"`

	A BookFeatures `json:"a"`
	B BookFeatures `json:"b"`

	DurationRatio     float64 `json:"duration_ratio"`
	FolderRelation    string  `json:"folder_relation"`    // unrelated|same_dir|a_ancestor_of_b|b_ancestor_of_a|sibling_parts
	SharesRecordingID bool    `json:"shares_recording_id"`
	SignatureRelation string  `json:"signature_relation"` // unknown|match|disjoint|a_contains_b|b_contains_a

	Label       string `json:"label"`        // true_dup|not_dup|unsure
	LabelSource string `json:"label_source"` // rule|itunes_attr|human|llm_judge
	LabelReason string `json:"label_reason"`
	DecidedAt   string `json:"decided_at,omitempty"` // RFC3339; caller-stamped
	FormulaVersion string `json:"formula_version,omitempty"`
}

// LabeledExampleFilter narrows ListLabeledExamples / CountLabeledExamples.
// Empty fields are ignored. Filtering is in-memory over the prefix scan, which
// is fine at dataset scale (tens of thousands of rows).
type LabeledExampleFilter struct {
	Label             string
	LabelSource       string
	Band              string
	FolderRelation    string
	SignatureRelation string
	Limit             int
	Offset            int
}

func (f LabeledExampleFilter) matches(ex *LabeledExample) bool {
	if f.Label != "" && ex.Label != f.Label {
		return false
	}
	if f.LabelSource != "" && ex.LabelSource != f.LabelSource {
		return false
	}
	if f.Band != "" && ex.Band != f.Band {
		return false
	}
	if f.FolderRelation != "" && ex.FolderRelation != f.FolderRelation {
		return false
	}
	if f.SignatureRelation != "" && ex.SignatureRelation != f.SignatureRelation {
		return false
	}
	return true
}

// UpsertLabeledExample writes (or overwrites) a labeled example.
func (s *EmbeddingStore) UpsertLabeledExample(ex LabeledExample) error {
	data, err := json.Marshal(ex)
	if err != nil {
		return fmt.Errorf("marshal labeled example %d: %w", ex.CandidateID, err)
	}
	return s.db.Set(dedupLabelKey(ex.CandidateID), data, pebble.Sync)
}

// GetLabeledExample returns the example for a candidate, or nil if absent.
func (s *EmbeddingStore) GetLabeledExample(candidateID int64) (*LabeledExample, error) {
	val, closer, err := s.db.Get(dedupLabelKey(candidateID))
	if err == pebble.ErrNotFound {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer func() { _ = closer.Close() }()
	var ex LabeledExample
	if err := json.Unmarshal(val, &ex); err != nil {
		return nil, fmt.Errorf("unmarshal labeled example %d: %w", candidateID, err)
	}
	return &ex, nil
}

// ListLabeledExamples returns examples matching the filter (prefix scan).
func (s *EmbeddingStore) ListLabeledExamples(f LabeledExampleFilter) ([]LabeledExample, error) {
	prefix := []byte(dedupLabelPfx)
	upper := append([]byte(dedupLabelPfx[:len(dedupLabelPfx)-1]), dedupLabelPfx[len(dedupLabelPfx)-1]+1)
	iter, err := s.db.NewIter(&pebble.IterOptions{LowerBound: prefix, UpperBound: upper})
	if err != nil {
		return nil, err
	}
	defer func() { _ = iter.Close() }()

	var out []LabeledExample
	skipped := 0
	for iter.First(); iter.Valid(); iter.Next() {
		var ex LabeledExample
		if err := json.Unmarshal(iter.Value(), &ex); err != nil {
			continue // skip a corrupt row rather than abort the scan
		}
		if !f.matches(&ex) {
			continue
		}
		if f.Offset > 0 && skipped < f.Offset {
			skipped++
			continue
		}
		out = append(out, ex)
		if f.Limit > 0 && len(out) >= f.Limit {
			break
		}
	}
	return out, iter.Error()
}

// CountLabeledExamples counts examples matching the filter (Limit/Offset ignored).
func (s *EmbeddingStore) CountLabeledExamples(f LabeledExampleFilter) (int, error) {
	cf := f
	cf.Limit, cf.Offset = 0, 0
	list, err := s.ListLabeledExamples(cf)
	if err != nil {
		return 0, err
	}
	return len(list), nil
}

// ensure binary import is used (key helper alternative); kept for forward use.
var _ = binary.BigEndian
```

> NOTE: drop the trailing `binary` usage line if `gofmt`/`go vet` flags the unused import; it is only there as a reminder that a fixed-width binary key is an alternative to the hex string. The hex key above is self-contained.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/database/ -run TestLabeledExample_RoundTripAndFilter -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/database/dedup_label.go internal/database/dedup_label_test.go
git commit -m "feat(dedup): LabeledExample type + dedup:label Pebble store"
```

### Task 5: Feature builder

**Files:**
- Create: `internal/dedup/dataset/builder.go`
- Test: `internal/dedup/dataset/builder_test.go`

- [ ] **Step 1: Write the failing test**

Create `internal/dedup/dataset/builder_test.go`:

```go
package dataset

import (
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// fakeStore implements BuilderStore for tests.
type fakeStore struct {
	books map[string]*database.Book
	files map[string][]database.BookFile
}

func (f *fakeStore) GetBook(id string) (*database.Book, error)              { return f.books[id], nil }
func (f *fakeStore) GetBookFiles(id string) ([]database.BookFile, error)    { return f.files[id], nil }

func TestBuildExample_PartVsWholeFeatures(t *testing.T) {
	whole := &database.Book{ID: "whole", Title: "The Crafter's Defense"}
	part := &database.Book{ID: "part", Title: "The Crafter's Defense"}
	fs := &fakeStore{
		books: map[string]*database.Book{"whole": whole, "part": part},
		files: map[string][]database.BookFile{
			"whole": {{BookID: "whole", FilePath: "/lib/Crafter/whole.m4b", Duration: 36000}},
			"part":  {{BookID: "part", FilePath: "/lib/Crafter/part-1.m4b", Duration: 1200}},
		},
	}
	cand := database.DedupCandidate{ID: 7, EntityAID: "whole", EntityBID: "part", Layer: "exact"}

	ex, err := BuildExample(fs, cand)
	if err != nil {
		t.Fatalf("BuildExample: %v", err)
	}
	if ex.A.TotalDurationSec != 36000 || ex.B.TotalDurationSec != 1200 {
		t.Fatalf("durations: a=%v b=%v", ex.A.TotalDurationSec, ex.B.TotalDurationSec)
	}
	wantRatio := 1200.0 / 36000.0
	if ex.DurationRatio < wantRatio-1e-9 || ex.DurationRatio > wantRatio+1e-9 {
		t.Fatalf("duration ratio = %v, want %v", ex.DurationRatio, wantRatio)
	}
	if ex.FolderRelation != "same_dir" {
		t.Fatalf("folder relation = %q, want same_dir", ex.FolderRelation)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/dedup/dataset/ -run TestBuildExample_PartVsWholeFeatures -v`
Expected: FAIL — package/`BuildExample` not defined.

- [ ] **Step 3: Write the implementation**

Create `internal/dedup/dataset/builder.go`:

```go
// file: internal/dedup/dataset/builder.go
// version: 1.0.0
// guid: 4a91c7e0-6d83-4b25-9f10-2c5a8e7d4b31
// last-edited: 2026-06-13

// Package dataset builds labeled dedup examples and runs deterministic catchers
// over them. Pure: a store interface in, a database.LabeledExample out, no
// side effects. This is the audit CLI's per-pair logic promoted to a reusable,
// unit-tested package (spec C1/C2).
package dataset

import (
	"path/filepath"
	"strings"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// BuilderStore is the narrow store surface BuildExample needs.
type BuilderStore interface {
	GetBook(id string) (*database.Book, error)
	GetBookFiles(id string) ([]database.BookFile, error)
}

// BuildExample loads both books once and computes every feature for the pair.
func BuildExample(store BuilderStore, cand database.DedupCandidate) (database.LabeledExample, error) {
	ex := database.LabeledExample{
		CandidateID: cand.ID,
		EntityAID:   cand.EntityAID,
		EntityBID:   cand.EntityBID,
		Layer:       cand.Layer,
		Band:        cand.Band,
		Similarity:  cand.Similarity,
	}

	a, aFiles, err := loadSide(store, cand.EntityAID)
	if err != nil {
		return ex, err
	}
	b, bFiles, err := loadSide(store, cand.EntityBID)
	if err != nil {
		return ex, err
	}
	ex.A = buildFeatures(a, aFiles)
	ex.B = buildFeatures(b, bFiles)

	ex.DurationRatio = durationRatio(ex.A.TotalDurationSec, ex.B.TotalDurationSec)
	ex.FolderRelation = folderRelation(ex.A.PrimaryPath, ex.B.PrimaryPath)
	ex.SharesRecordingID = sharesAny(ex.A.RecordingIDs, ex.B.RecordingIDs)
	ex.SignatureRelation = signatureRelation(a, b)
	return ex, nil
}

func loadSide(store BuilderStore, id string) (*database.Book, []database.BookFile, error) {
	bk, err := store.GetBook(id)
	if err != nil {
		return nil, nil, err
	}
	files, err := store.GetBookFiles(id)
	if err != nil {
		return bk, nil, err
	}
	return bk, files, nil
}

func buildFeatures(bk *database.Book, files []database.BookFile) database.BookFeatures {
	f := database.BookFeatures{FileCount: len(files)}
	if bk != nil {
		f.Title = bk.Title
		f.WholeBookSigPresent = bk.BookSigV1 != nil && *bk.BookSigV1 != ""
		if bk.CoverURL != nil && *bk.CoverURL != "" {
			f.HasCover = true
		}
	}
	var total float64
	allExist := len(files) > 0
	for i := range files {
		fl := &files[i]
		if f.PrimaryPath == "" && fl.FilePath != "" {
			f.PrimaryPath = fl.FilePath
		}
		// Prefer fpcalc-measured duration; fall back to container duration.
		if fl.AcoustIDFingerprintDurationSec > 0 {
			total += fl.AcoustIDFingerprintDurationSec
		} else if fl.Duration > 0 {
			total += float64(fl.Duration)
		}
		if fl.AcoustIDOnlineRecordingID != "" {
			f.RecordingIDs = append(f.RecordingIDs, fl.AcoustIDOnlineRecordingID)
		}
		if fl.ITunesPersistentID != "" {
			f.ITunesPIDPresent = true
		}
	}
	f.TotalDurationSec = total
	f.FilesExist = allExist
	return f
}

func durationRatio(a, b float64) float64 {
	if a <= 0 || b <= 0 {
		return 0
	}
	lo, hi := a, b
	if lo > hi {
		lo, hi = hi, lo
	}
	return lo / hi
}

// folderRelation classifies how two primary paths sit relative to each other.
func folderRelation(a, b string) string {
	if a == "" || b == "" {
		return "unrelated"
	}
	da, db := filepath.Dir(a), filepath.Dir(b)
	if da == db {
		return "same_dir"
	}
	if isAncestor(da, db) {
		return "a_ancestor_of_b"
	}
	if isAncestor(db, da) {
		return "b_ancestor_of_a"
	}
	return "unrelated"
}

func isAncestor(anc, desc string) bool {
	anc = strings.TrimRight(anc, "/")
	return anc != "" && strings.HasPrefix(desc, anc+"/")
}

func sharesAny(a, b []string) bool {
	set := make(map[string]struct{}, len(a))
	for _, x := range a {
		set[x] = struct{}{}
	}
	for _, y := range b {
		if _, ok := set[y]; ok {
			return true
		}
	}
	return false
}

// signatureRelation reports the whole-book-signature relationship. M1 ships the
// presence/match cases (same-coordinate similarity); offset/subsequence
// containment (a_contains_b / b_contains_a) is deferred (spec C2 note) and
// returns "unknown" until implemented.
func signatureRelation(a, b *database.Book) string {
	if a == nil || b == nil {
		return "unknown"
	}
	if a.BookSigV1 == nil || *a.BookSigV1 == "" || b.BookSigV1 == nil || *b.BookSigV1 == "" {
		return "unknown"
	}
	// Wired in Task 6 via fingerprint.BookSignatureSimilarity; until then,
	// equal raw signatures are a definite match.
	if *a.BookSigV1 == *b.BookSigV1 {
		return "match"
	}
	return "unknown"
}
```

> NOTE: confirm field names against `internal/database/store.go`: `Book.CoverURL` (`cover_url`), `BookFile.AcoustIDFingerprintDurationSec`, `BookFile.AcoustIDOnlineRecordingID`, `BookFile.ITunesPersistentID`, `BookFile.Duration`. The audit CLI (`tools/cmd/dedup-dataset-audit/main.go:94-110`) uses exactly these; mirror it. If `Book.CoverURL` differs, adjust `HasCover`.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/dedup/dataset/ -run TestBuildExample_PartVsWholeFeatures -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/dedup/dataset/builder.go internal/dedup/dataset/builder_test.go
git commit -m "feat(dedup): dataset feature builder (BuildExample)"
```

### Task 6: Deterministic catchers + signature similarity wiring

**Files:**
- Create: `internal/dedup/dataset/rules.go`
- Modify: `internal/dedup/dataset/builder.go` (wire real signature similarity)
- Test: `internal/dedup/dataset/rules_test.go`

- [ ] **Step 1: Write the failing test**

Create `internal/dedup/dataset/rules_test.go`:

```go
package dataset

import (
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

func TestCatchers(t *testing.T) {
	cases := []struct {
		name       string
		ex         database.LabeledExample
		wantFires  bool
		wantLabel  string
	}{
		{
			name: "part vs whole by duration ratio",
			ex: database.LabeledExample{
				A: database.BookFeatures{TotalDurationSec: 36000, FilesExist: true},
				B: database.BookFeatures{TotalDurationSec: 1200, FilesExist: true},
				DurationRatio: 1200.0 / 36000.0,
			},
			wantFires: true, wantLabel: "not_dup",
		},
		{
			name: "missing file one side",
			ex: database.LabeledExample{
				A: database.BookFeatures{FilesExist: true, TotalDurationSec: 100},
				B: database.BookFeatures{FilesExist: false},
			},
			wantFires: true, wantLabel: "not_dup",
		},
		{
			name: "whole-book signature match => true_dup",
			ex: database.LabeledExample{
				A: database.BookFeatures{FilesExist: true, WholeBookSigPresent: true, TotalDurationSec: 36000},
				B: database.BookFeatures{FilesExist: true, WholeBookSigPresent: true, TotalDurationSec: 36000},
				SignatureRelation: "match", DurationRatio: 1.0,
			},
			wantFires: true, wantLabel: "true_dup",
		},
		{
			name: "no rule fires",
			ex: database.LabeledExample{
				A: database.BookFeatures{FilesExist: true, TotalDurationSec: 36000},
				B: database.BookFeatures{FilesExist: true, TotalDurationSec: 35900},
				DurationRatio: 35900.0 / 36000.0, SignatureRelation: "unknown",
			},
			wantFires: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			label, reason, fires := Classify(tc.ex)
			if fires != tc.wantFires {
				t.Fatalf("fires=%v want %v (reason=%q)", fires, tc.wantFires, reason)
			}
			if fires && label != tc.wantLabel {
				t.Fatalf("label=%q want %q", label, tc.wantLabel)
			}
		})
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/dedup/dataset/ -run TestCatchers -v`
Expected: FAIL — `undefined: Classify`.

- [ ] **Step 3: Write the catchers**

Create `internal/dedup/dataset/rules.go`:

```go
// file: internal/dedup/dataset/rules.go
// version: 1.0.0
// guid: 9e2b4c71-3a85-4d60-8f29-1b7c6a4e5d02
// last-edited: 2026-06-13

package dataset

import (
	"fmt"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// partVsWholeRatioMax — below this duration ratio (and both durations known) a
// pair is a part matched against a whole book, not a duplicate.
const partVsWholeRatioMax = 0.5

// Classify runs the deterministic catchers in priority order and returns the
// first firing rule's (label, reason, fires). The order matters: the strong
// positive (signature match) and the hard negatives (missing file, part vs
// whole) take precedence over weaker signals.
func Classify(ex database.LabeledExample) (label, reason string, fires bool) {
	if l, r, ok := wholeBookSignatureMatch(ex); ok {
		return l, r, true
	}
	if l, r, ok := missingFile(ex); ok {
		return l, r, true
	}
	if l, r, ok := partVsWhole(ex); ok {
		return l, r, true
	}
	return "", "", false
}

// wholeBookSignatureMatch: both sides have a whole-book signature and it matches
// => true_dup. This is the auto-positive oracle.
func wholeBookSignatureMatch(ex database.LabeledExample) (string, string, bool) {
	if ex.A.WholeBookSigPresent && ex.B.WholeBookSigPresent && ex.SignatureRelation == "match" {
		return "true_dup", "whole-book signatures match", true
	}
	return "", "", false
}

// missingFile: either side has no resolvable files => not_dup (never merge a
// candidate whose file is gone — "we have to actually have the file").
func missingFile(ex database.LabeledExample) (string, string, bool) {
	if !ex.A.FilesExist {
		return "not_dup", "side A has no resolvable files", true
	}
	if !ex.B.FilesExist {
		return "not_dup", "side B has no resolvable files", true
	}
	return "", "", false
}

// partVsWhole: both durations known and the ratio is below the floor => a part
// matched against the whole book.
func partVsWhole(ex database.LabeledExample) (string, string, bool) {
	if ex.A.TotalDurationSec > 0 && ex.B.TotalDurationSec > 0 && ex.DurationRatio > 0 && ex.DurationRatio < partVsWholeRatioMax {
		return "not_dup", fmt.Sprintf("duration ratio %.3f — part vs whole", ex.DurationRatio), true
	}
	return "", "", false
}
```

- [ ] **Step 4: Wire real signature similarity in the builder**

In `internal/dedup/dataset/builder.go`, replace the `signatureRelation` body's raw-equality fallback with the real comparator:

```go
import (
	// ...existing imports...
	"github.com/falkcorp/audiobook-organizer/internal/fingerprint"
)

// sigMatchThreshold — BookSignatureSimilarity at/above this is a content match.
const sigMatchThreshold = 0.95

func signatureRelation(a, b *database.Book) string {
	if a == nil || b == nil ||
		a.BookSigV1 == nil || *a.BookSigV1 == "" ||
		b.BookSigV1 == nil || *b.BookSigV1 == "" {
		return "unknown"
	}
	sim, err := fingerprint.BookSignatureSimilarity(*a.BookSigV1, *b.BookSigV1)
	if err != nil {
		return "unknown"
	}
	if sim >= sigMatchThreshold {
		return "match"
	}
	return "disjoint"
}
```

> NOTE: confirm `fingerprint.BookSignatureSimilarity(a, b string) (float64, error)` (it is at `internal/fingerprint/book_signature.go:131`). Offset/subsequence containment (`a_contains_b`) stays unimplemented in M1 per the spec C2 note.

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./internal/dedup/dataset/...`
Expected: PASS (both builder and rules tests).

- [ ] **Step 6: Commit**

```bash
git add internal/dedup/dataset/rules.go internal/dedup/dataset/rules_test.go internal/dedup/dataset/builder.go
git commit -m "feat(dedup): deterministic catchers + whole-book signature similarity"
```

---

## Milestone C — Backfill op (cleans the existing residual)

### Task 7: `dedup.dataset-backfill` op

**Files:**
- Create: `internal/plugins/dedup/dataset_backfill.go`
- Modify: `internal/plugins/dedup/plugin.go` (register the op)

- [ ] **Step 1: Write the op**

Create `internal/plugins/dedup/dataset_backfill.go`, mirroring `purge_legacy_fp.go`'s structure (def + run, dry-run default, `{"apply":true}`):

```go
// file: internal/plugins/dedup/dataset_backfill.go
// version: 1.0.0
// guid: 2d6f8a13-7c40-4e92-8b15-9a3e5c7d2f64
// last-edited: 2026-06-13

// Package dedup — op dedup.dataset-backfill (spec C4 backfill).
//
// Iterates all pending candidates, builds a LabeledExample for each, runs the
// deterministic catchers, and writes the labeled example. With apply=true, any
// candidate a catcher labels not_dup is suppressed (status -> "dismissed") so
// the residual part-vs-whole / missing-file false positives leave the queue.
// Dry-run by default: reports counts, writes nothing.
package dedup

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/dedup/dataset"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
)

type datasetBackfillParams struct {
	Apply bool `json:"apply"`
}

func (p *Plugin) datasetBackfillDef() sdk.OperationDef {
	return sdk.OperationDef{
		ID:          "dedup.dataset-backfill",
		Plugin:      "dedup",
		DisplayName: "Backfill dedup tuning dataset",
		Description: "Builds a labeled example per pending candidate, runs catchers, " +
			"and (apply=true) suppresses rule-labeled not_dup candidates. Dry-run by default.",
		ResumePolicy:    sdk.ResumeDrop,
		DefaultPriority: sdk.PriorityNormal,
		ConcurrencyKey:  "dedup.dataset-backfill",
		Cancellable:     true,
		Timeout:         60 * time.Minute,
		Capabilities:    []sdk.Capability{sdk.CapLibraryRead, sdk.CapLibraryWrite},
		Run:             p.runDatasetBackfill,
	}
}

// builderAdapter satisfies dataset.BuilderStore using the plugin's main store.
type builderAdapter struct{ store database.Store }

func (b builderAdapter) GetBook(id string) (*database.Book, error) {
	return b.store.GetBook(id)
}
func (b builderAdapter) GetBookFiles(id string) ([]database.BookFile, error) {
	return b.store.GetBookFiles(id)
}

func (p *Plugin) runDatasetBackfill(ctx context.Context, rawParams json.RawMessage, reporter sdk.Reporter) error {
	if p.embeddingStore == nil || p.store == nil {
		return fmt.Errorf("stores not available")
	}
	var params datasetBackfillParams
	if len(rawParams) > 0 {
		if err := json.Unmarshal(rawParams, &params); err != nil {
			return fmt.Errorf("parse params: %w", err)
		}
	}
	adapter := builderAdapter{store: p.store}

	_ = reporter.UpdateProgress(0, 2, "Loading pending candidates…")
	cands, _, err := p.embeddingStore.ListCandidates(database.CandidateFilter{Status: "pending", Limit: 1_000_000})
	if err != nil {
		return fmt.Errorf("list candidates: %w", err)
	}

	var examined, labeled, suppressed, notDup, trueDup int
	for i := range cands {
		if reporter.IsCanceled() {
			return context.Canceled
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		c := cands[i]
		examined++

		ex, err := dataset.BuildExample(adapter, c)
		if err != nil {
			reporter.Logger().Warn("dataset-backfill: build error", "candidate_id", c.ID, "error", err)
			continue
		}
		if label, reason, fires := dataset.Classify(ex); fires {
			ex.Label = label
			ex.LabelSource = "rule"
			ex.LabelReason = reason
			switch label {
			case "not_dup":
				notDup++
			case "true_dup":
				trueDup++
			}
		}

		if params.Apply {
			if err := p.embeddingStore.UpsertLabeledExample(ex); err != nil {
				reporter.Logger().Error("dataset-backfill: upsert label error", "candidate_id", c.ID, "error", err)
			} else {
				labeled++
			}
			if ex.Label == "not_dup" {
				if err := p.embeddingStore.UpdateCandidateStatus(c.ID, "dismissed"); err != nil {
					reporter.Logger().Error("dataset-backfill: suppress error", "candidate_id", c.ID, "error", err)
				} else {
					suppressed++
				}
			}
		}
	}

	summary := fmt.Sprintf("examined=%d not_dup=%d true_dup=%d labeled=%d suppressed=%d (apply=%v)",
		examined, notDup, trueDup, labeled, suppressed, params.Apply)
	reporter.Logger().Info("dataset-backfill complete", "summary", summary)
	_ = reporter.UpdateProgress(2, 2, summary)
	return nil
}
```

> NOTE: confirm `database.Store` exposes `GetBook(id string) (*database.Book, error)` and `GetBookFiles`. `purge_legacy_fp.go` uses `p.store.GetAllBookFiles()` and `p.embeddingStore.ListCandidates`, so the store handle and patterns are established; adjust the getter name if `GetBook` is `GetBookByID`.

- [ ] **Step 2: Register the op**

In `internal/plugins/dedup/plugin.go`, add to the `ops := []sdk.OperationDef{...}` slice (after `p.purgeStaleDef()` / near the other defs):

```go
		p.datasetBackfillDef(),
```

- [ ] **Step 3: Build to verify it compiles + registers**

Run: `go build ./... && go test ./internal/plugins/dedup/...`
Expected: build PASS; existing dedup-plugin tests PASS.

- [ ] **Step 4: Commit**

```bash
git add internal/plugins/dedup/dataset_backfill.go internal/plugins/dedup/plugin.go
git commit -m "feat(dedup): dataset-backfill op (label + suppress residual)"
```

### Task 8: Full verification + format/vet

- [ ] **Step 1: Format and vet**

Run: `gofmt -l internal/dedup internal/database internal/plugins/dedup && go vet ./internal/dedup/... ./internal/database/... ./internal/plugins/dedup/...`
Expected: `gofmt -l` prints nothing; `go vet` clean. If `gofmt -l` lists files, run `gofmt -w` on them and amend.

- [ ] **Step 2: Full package tests**

Run: `go test ./internal/dedup/... ./internal/database/... ./internal/plugins/dedup/...`
Expected: PASS.

- [ ] **Step 3: Commit any format fixes**

```bash
git add -A && git commit -m "chore(dedup): gofmt + vet for dataset milestone" || echo "nothing to format"
```

---

## Deployment / runtime validation (after merge)

1. Deploy (`make deploy`). The engine gate (Milestone A) takes effect for all new scans — no new stub matches.
2. Dry-run the backfill: `POST /api/v1/operations/v2/trigger` (or the existing op-trigger path) for `dedup.dataset-backfill` with no body → poll `/operations/:id/status` → read the `examined / not_dup / suppressed` summary.
3. Approve, then `apply=true` → confirms the residual `exact`/sim=1 pending count drops (the part-vs-whole and missing-file pairs move to `dismissed`).

> NOTE: a generic op-trigger endpoint may be needed for `dedup.dataset-backfill` (the purge op got a dedicated `POST /dedup/purge-legacy-fp` handler). If no generic trigger exists, add a one-line handler + route mirroring `PurgeLegacyFPCandidates` (`internal/server/handlers/dedup/handler.go:1499`, route `wire_handlers.go:858`). This is a small follow-up, not part of the core dataset logic.

---

## Self-Review

**Spec coverage (M1 = C1, C2, C3; plus root-cause fix and C4 backfill):**
- C1 feature builder → Task 5 ✓
- C2 catchers (partVsWhole, missingFile, wholeBookSignatureMatch) → Task 6 ✓ (folderAncestorOrSibling features computed in Task 5; a `folderAncestorOrSibling` catcher is a trivial add over `FolderRelation` — fold into Task 6 if desired, or defer with live capture)
- C3 store → Task 4 ✓
- C4 backfill (cleanup half) → Task 7 ✓
- Root-cause fix (engine gate) → Tasks 1–3 ✓
- **Deferred to follow-up plan:** C4 live capture, C5 auto-bug-filing, C6 review UI, C7 JSONL export. Stated explicitly above.

**Placeholder scan:** All code steps contain complete code. The `> NOTE:` blocks flag field-name confirmations against `store.go` (the one place the executing engineer must verify exact identifiers) — these are verification reminders, not unfinished code.

**Type consistency:** `LabeledExample`/`BookFeatures` (Task 4) are used unchanged by `BuildExample` (Task 5), `Classify` (Task 6), and the backfill op (Task 7). `BuilderStore` (Task 5) is satisfied by `builderAdapter` (Task 7). `signatureRelation` returns the same enum values (`match`/`disjoint`/`unknown`) the catcher reads. `Classify(ex) (label, reason string, fires bool)` signature matches its test and its caller.

**Known follow-up:** add a `folderAncestorOrSibling` catcher and the generic/dedicated op-trigger route; both noted inline.
