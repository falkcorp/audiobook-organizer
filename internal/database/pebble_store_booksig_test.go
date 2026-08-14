// file: internal/database/pebble_store_booksig_test.go
// version: 1.0.0
// guid: b6d2417f-90ce-4a83-b512-8e7c04f9a3d1
// last-edited: 2026-08-13

package database

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/cockroachdb/pebble/v2"
	"github.com/stretchr/testify/require"
)

// Moving BookSigV1 and its four companions out of the book: row is a storage
// change with a data-loss failure mode: a book whose signature silently stops
// coming back looks EXACTLY like a book whose signature was wiped, and this
// repo already has an op (booksig_recovery_audit) whose job is to detect that
// state and write book_ver: snapshot data over the live row. So "the read
// returned a Book" proves nothing here — every test below asserts on the five
// field VALUES, and the ones that matter most read through a reopened store so
// an in-process cache cannot manufacture a pass.

// sigFixture returns the five signature fields with distinguishable values, so
// a test can tell "hydrated the sidecar" from "kept whatever was inline".
func sigFixture(tag string) (v1, mask string, segments, coverage int, builtAt time.Time) {
	return "sig-" + tag, "mask-" + tag, 4096, 87,
		time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC)
}

// bookWithSig builds an unsaved Book carrying all five signature fields.
func bookWithSig(tag string) *Book {
	v1, mask, segments, coverage, builtAt := sigFixture(tag)
	// NOT "booksig-"+tag: the row legitimately carries file_hash, and a hash
	// containing the signature's own value as a substring makes the
	// "row must not contain the signature" assertion fail on the hash instead.
	hash := "fhash-" + tag
	return &Book{
		Title:              "Book Sig " + tag,
		FilePath:           "/tmp/booksig_" + tag + ".m4b",
		FileHash:           &hash,
		BookSigV1:          &v1,
		BookSigV1Mask:      &mask,
		BookSigSegments:    &segments,
		BookSigBuiltAt:     &builtAt,
		BookSigCoveragePct: &coverage,
	}
}

// requireSigPresent asserts all five fields survived, by value. Checking only
// BookSigV1 would miss the split-group hazard this design exists to avoid: a
// book carrying V1 but nil BuiltAt (or the reverse) is a state
// booksig_recovery_audit reads as damage.
func requireSigPresent(t *testing.T, b *Book, tag string) {
	t.Helper()
	require.NotNil(t, b, "book must exist")
	wantV1, wantMask, wantSegments, wantCoverage, wantBuiltAt := sigFixture(tag)

	require.NotNil(t, b.BookSigV1, "BookSigV1 must survive the round-trip")
	require.Equal(t, wantV1, *b.BookSigV1)
	require.NotNil(t, b.BookSigV1Mask, "BookSigV1Mask must survive the round-trip")
	require.Equal(t, wantMask, *b.BookSigV1Mask)
	require.NotNil(t, b.BookSigSegments, "BookSigSegments must survive the round-trip")
	require.Equal(t, wantSegments, *b.BookSigSegments)
	require.NotNil(t, b.BookSigCoveragePct, "BookSigCoveragePct must survive the round-trip")
	require.Equal(t, wantCoverage, *b.BookSigCoveragePct)
	require.NotNil(t, b.BookSigBuiltAt, "BookSigBuiltAt must survive the round-trip")
	require.True(t, wantBuiltAt.Equal(*b.BookSigBuiltAt),
		"BookSigBuiltAt: want %v, got %v", wantBuiltAt, *b.BookSigBuiltAt)
}

// bookSigEnv owns the store lifecycle. It exists because these tests reopen
// the database mid-test: a fixture that registered its own t.Cleanup close AND
// a reopen that closed the same handle would double-close it, and Pebble
// panics ("pebble: closed") rather than returning an error — which then eats
// the actual assertion failure. The env keeps exactly one live handle and the
// cleanup closes that one.
type bookSigEnv struct {
	store *PebbleStore
	dir   string
}

func newBookSigEnv(t *testing.T) *bookSigEnv {
	t.Helper()
	env := &bookSigEnv{dir: t.TempDir()}
	store, err := NewPebbleStore(env.dir)
	require.NoError(t, err)
	env.store = store
	t.Cleanup(func() { _ = env.store.Close() })
	return env
}

// reopen closes the live store and opens a NEW one over the same directory.
//
// This is not ceremony. Every write path here returns the *Book it was handed,
// and the store keeps an in-memory layer; a round-trip that never leaves the
// process can report success while nothing durable was written, which is
// precisely the failure class this change is meant to remove rather than add.
// A reopened store can only answer from bytes on disk.
func (e *bookSigEnv) reopen(t *testing.T) *PebbleStore {
	t.Helper()
	require.NoError(t, e.store.Close())
	fresh, err := NewPebbleStore(e.dir)
	require.NoError(t, err)
	e.store = fresh
	return fresh
}

// rawBookRow returns the raw bytes stored under book:<id>, bypassing every
// getter. Tests that assert what is or is not IN the row must read it this way;
// going through GetBookByID would show the hydrated view and prove nothing
// about the stored bytes.
func rawBookRow(t *testing.T, store *PebbleStore, id string) []byte {
	t.Helper()
	val, closer, err := store.db.Get([]byte("book:" + id))
	require.NoError(t, err)
	defer closer.Close()
	return append([]byte(nil), val...)
}

// TestBookSig_RoundTripsThroughGetBookByID is the baseline: the transparency
// premise of this whole change is that no signature consumer notices it.
func TestBookSig_RoundTripsThroughGetBookByID(t *testing.T) {
	env := newBookSigEnv(t)
	store := env.store

	created, err := store.CreateBook(bookWithSig("create"))
	require.NoError(t, err)

	fresh := env.reopen(t)
	got, err := fresh.GetBookByID(created.ID)
	require.NoError(t, err)
	requireSigPresent(t, got, "create")
}

// TestBookSig_RowNoLongerCarriesTheSignature is the test that proves the
// performance claim, and it is the reason this is a storage change rather than
// a caching one.
//
// The measured win is 580 MB of the books warmup phase (production
// 2026-08-13). Warmup scans book: rows and nothing else, so the signature stops
// costing anything ONLY if it is genuinely absent from the row. A version that
// wrote the sidecar but left the inline copy in place would pass every
// round-trip test above while saving exactly zero bytes.
func TestBookSig_RowNoLongerCarriesTheSignature(t *testing.T) {
	env := newBookSigEnv(t)
	store := env.store

	created, err := store.CreateBook(bookWithSig("rowstrip"))
	require.NoError(t, err)

	row := string(rawBookRow(t, store, created.ID))
	for _, field := range []string{
		"book_sig_v1", "book_sig_v1_mask", "book_sig_segments",
		"book_sig_built_at", "book_sig_coverage_pct",
	} {
		require.NotContains(t, row, field,
			"the book: row must not carry %q — warmup reads this row and the whole point is that it stops paying for the signature", field)
	}
	// And the value itself, in case a future rename keeps the data under a
	// different JSON key.
	require.NotContains(t, row, "sig-rowstrip",
		"the signature VALUE must not appear in the book: row under any key")

	// The sidecar must actually hold it — otherwise this test passes by the
	// data having been destroyed, which is the opposite of the goal.
	val, closer, err := store.db.Get(bookSigKey(created.ID))
	require.NoError(t, err, "the sidecar key must exist")
	defer closer.Close()
	require.Contains(t, string(val), "sig-rowstrip")
}

// TestBookSig_LegacyInlineRowStillReads covers every book already on disk.
//
// Nothing has migrated when this ships: 67,824 production rows carry the
// signature inline and have no sidecar key. The read path must fall back to
// the inline value, or deploying step 1-4 alone would make every existing
// signature vanish — the exact incident booksig_recovery_audit exists to
// repair, caused by the change meant to prevent it.
//
// The row is written as raw JSON because the supported write path can no
// longer produce one: CreateBook now strips these fields on the way in.
func TestBookSig_LegacyInlineRowStillReads(t *testing.T) {
	env := newBookSigEnv(t)
	store := env.store

	const id = "01LEGACYBOOKSIG0000000000"
	legacy := `{"id":"` + id + `","title":"Legacy Inline","file_path":"/tmp/legacy_booksig.m4b",` +
		`"book_sig_v1":"sig-legacy","book_sig_v1_mask":"mask-legacy",` +
		`"book_sig_segments":4096,"book_sig_coverage_pct":87,` +
		`"book_sig_built_at":"2026-08-13T12:00:00Z"}`
	require.NoError(t, store.db.Set([]byte("book:"+id), []byte(legacy), pebble.Sync))

	fresh := env.reopen(t)
	got, err := fresh.GetBookByID(id)
	require.NoError(t, err)
	requireSigPresent(t, got, "legacy")

	// No sidecar was written for it, and the read must not have invented one.
	_, closer, err := fresh.db.Get(bookSigKey(id))
	if err == nil {
		closer.Close()
		t.Fatal("reading a legacy row must not create a sidecar key — the fallback is a read, not a migration")
	}
	require.Equal(t, pebble.ErrNotFound, err)
}

// TestBookSig_MemdbRoundTripWriteDoesNotWipeIt is the point of the whole
// exercise.
//
// The shape: read a book through a path that strips heavy fields, then write it
// straight back. That is how 67,824 signatures were destroyed once already, and
// it is why UpdateBook carries a hand-maintained preserve-guard whose comment
// asks the next author to "keep both in sync". With the sidecar, a nil incoming
// signature writes no key and deletes nothing, so the wipe stops depending on
// anyone remembering the guard.
func TestBookSig_MemdbRoundTripWriteDoesNotWipeIt(t *testing.T) {
	env := newBookSigEnv(t)
	store := env.store

	created, err := store.CreateBook(bookWithSig("wipe"))
	require.NoError(t, err)

	// Exactly what a caller gets from the production memdb path.
	stripped := stripBookForMemdb(created)
	require.Nil(t, stripped.BookSigV1, "fixture guard: the stripped projection must have no signature")

	stripped.Title = "Retitled By A Projection Write"
	_, err = store.UpdateBook(created.ID, stripped)
	require.NoError(t, err)

	fresh := env.reopen(t)
	got, err := fresh.GetBookByID(created.ID)
	require.NoError(t, err)
	require.Equal(t, "Retitled By A Projection Write", got.Title, "the write itself must have landed")
	requireSigPresent(t, got, "wipe")
}

// TestBookSig_UpdateReplacesTheSignatureWhenOneIsSupplied is the control for
// the test above. "Never lose a signature" is trivially satisfied by "never
// write one", so a rebuild has to be shown to actually take effect —
// acoustid's synthesizeBookSignatureForBook does exactly this read-set-write.
func TestBookSig_UpdateReplacesTheSignatureWhenOneIsSupplied(t *testing.T) {
	env := newBookSigEnv(t)
	store := env.store

	created, err := store.CreateBook(bookWithSig("before"))
	require.NoError(t, err)

	rebuilt, err := store.GetBookByID(created.ID)
	require.NoError(t, err)
	v1, mask, segments, coverage, builtAt := sigFixture("after")
	rebuilt.BookSigV1, rebuilt.BookSigV1Mask = &v1, &mask
	rebuilt.BookSigSegments, rebuilt.BookSigCoveragePct = &segments, &coverage
	rebuilt.BookSigBuiltAt = &builtAt
	_, err = store.UpdateBook(created.ID, rebuilt)
	require.NoError(t, err)

	fresh := env.reopen(t)
	got, err := fresh.GetBookByID(created.ID)
	require.NoError(t, err)
	requireSigPresent(t, got, "after")
}

// TestBookSig_GetAllBooksFullFromHydratesOnBothBranches guards the bulk reader
// that BookSignatureScan depends on.
//
// GetAllBooksFullFrom has two implementations: the memdb branch point-gets each
// ID through GetBookByID (so it inherits hydration), and the direct branch
// decodes rows itself (so it needs its own). BookSignatureScan filters on
// BookSigV1 != nil, so a branch that quietly returns unhydrated books makes the
// scan compare zero pairs and report success — the failure this function's own
// doc comment records happening before, for the same reason.
func TestBookSig_GetAllBooksFullFromHydratesOnBothBranches(t *testing.T) {
	for _, useMemDB := range []bool{true, false} {
		name := "memdb"
		if !useMemDB {
			name = "direct"
		}
		t.Run(name, func(t *testing.T) {
			env := newBookSigEnv(t)
			store := env.store
			created, err := store.CreateBook(bookWithSig("bulk"))
			require.NoError(t, err)

			fresh := env.reopen(t)
			fresh.UseMemDB = useMemDB
			if useMemDB {
				// NewPebbleStore starts the async warmup that publishes the
				// MemStore; wait for the real thing rather than reaching into
				// memPtr, which is documented as warmup-path-only. Taking the
				// production publish path is also what makes this subtest
				// exercise the branch it claims to.
				deadline := time.Now().Add(30 * time.Second)
				for !fresh.IsMemReady() {
					if time.Now().After(deadline) {
						t.Fatal("memdb warmup never published; cannot exercise the memdb branch")
					}
					time.Sleep(10 * time.Millisecond)
				}
			}

			books, err := fresh.GetAllBooksFullFrom("", 0)
			require.NoError(t, err)

			var found *Book
			for i := range books {
				if books[i].ID == created.ID {
					found = &books[i]
					break
				}
			}
			require.NotNil(t, found, "the seeded book must be returned by the %s branch", name)
			requireSigPresent(t, found, "bulk")
		})
	}
}

// TestBookSig_DeleteRemovesTheSidecar. DeleteBook has accumulated explicit
// teardown for the work index and all three hash indexes, each added after a
// writer got ahead of it and left rows resolving to a hard-deleted book. A
// sidecar left behind is a smaller problem (bytes nobody reads, not a phantom
// row) but it is the same omission, so it gets the same test.
func TestBookSig_DeleteRemovesTheSidecar(t *testing.T) {
	env := newBookSigEnv(t)
	store := env.store

	created, err := store.CreateBook(bookWithSig("delete"))
	require.NoError(t, err)
	_, closer, err := store.db.Get(bookSigKey(created.ID))
	require.NoError(t, err, "fixture guard: the sidecar must exist before the delete")
	closer.Close()

	require.NoError(t, store.DeleteBook(created.ID))

	_, closer, err = store.db.Get(bookSigKey(created.ID))
	if err == nil {
		closer.Close()
		t.Fatal("DeleteBook left the book_sig: key behind")
	}
	require.Equal(t, pebble.ErrNotFound, err)
}

// TestBookSig_NoBookIsEverBuiltButWiped pins the invariant that justified
// moving all five fields together rather than only the two large ones.
//
// booksig_recovery_audit classifies a book as damaged on
// `BookSigBuiltAt != nil && BookSigV1 == nil`, and its remedy is writing
// book_ver: snapshot data over the live row. If BuiltAt had stayed inline while
// V1 moved to the sidecar, every reader that does not hydrate would present
// that exact shape and the audit would "recover" healthy books en masse. With
// the whole group in one place the un-hydrated state is all-five-nil, which the
// audit correctly reads as "never had one".
func TestBookSig_NoBookIsEverBuiltButWiped(t *testing.T) {
	env := newBookSigEnv(t)
	store := env.store

	created, err := store.CreateBook(bookWithSig("invariant"))
	require.NoError(t, err)

	// The un-hydrated view: decode the stored row directly, skipping the
	// sidecar entirely. This is what any reader that forgets to hydrate sees.
	var unhydrated Book
	require.NoError(t, json.Unmarshal(rawBookRow(t, store, created.ID), &unhydrated))

	require.False(t,
		unhydrated.BookSigBuiltAt != nil && unhydrated.BookSigV1 == nil,
		"the un-hydrated row reads as built-then-wiped, which booksig_recovery_audit "+
			"treats as damage and repairs by overwriting the live row from a snapshot")
	require.Nil(t, unhydrated.BookSigBuiltAt,
		"BookSigBuiltAt must move to the sidecar with the rest of the group, not stay inline")
}

// TestBookSig_WarmupNoLongerReadsTheSignature closes the loop on the
// measurement that motivated this change.
//
// The production log reports discarded_field_mb[book_sig_v1_and_mask]=580.
// That number is charged inside the books-phase callback from the decoded row,
// so it goes to zero if and only if warmup genuinely stops seeing the
// signature. This asserts the mechanism the 580 MB was measured with, which is
// stronger than timing a startup.
func TestBookSig_WarmupNoLongerReadsTheSignature(t *testing.T) {
	env := newBookSigEnv(t)
	store := env.store

	// A signature big enough that any leak into the row would round up to a
	// nonzero megabyte rather than hiding under the ceiling division.
	big := strings.Repeat("s", 3*1024*1024)
	mask := strings.Repeat("m", 512*1024)
	segments, coverage := 4096, 87
	builtAt := time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC)
	hash := "booksig-warmup"
	_, err := store.CreateBook(&Book{
		Title:              "Warmup Sig",
		FilePath:           "/tmp/booksig_warmup.m4b",
		FileHash:           &hash,
		BookSigV1:          &big,
		BookSigV1Mask:      &mask,
		BookSigSegments:    &segments,
		BookSigBuiltAt:     &builtAt,
		BookSigCoveragePct: &coverage,
	})
	require.NoError(t, err)

	mem, err := NewMemStore()
	require.NoError(t, err)
	require.NoError(t, mem.WarmFromPebble(context.Background(), store))

	byField := mem.LastWarmupDiscardedByField()
	require.Zero(t, byField[DiscardFieldBookSignature],
		"warmup still reads %d MB of book signature; the sidecar is not keeping it out of the book: scan",
		byField[DiscardFieldBookSignature])
}
