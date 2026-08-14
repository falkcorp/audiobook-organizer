// file: internal/database/pebble_store_booksig_migrate_test.go
// version: 1.1.0
// guid: 9e51c0d3-2a87-4f16-b74e-5c8203f9a1d6
// last-edited: 2026-08-13

package database

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/cockroachdb/pebble/v2"
	"github.com/stretchr/testify/require"
)

// The migration is the only irreversible step in the #2387 sidecar design, and
// its worst failure mode is silent: a row stripped of its signature with no
// sidecar written leaves all five fields nil, which is EXACTLY the shape
// booksig_recovery_audit classifies as "never had a signature" rather than as
// damage. The op written to detect signature loss cannot see this class. Dedup
// would simply stop matching those books, with nothing logged and nothing to
// audit. So these tests assert the row/sidecar PAIRING, by value, not merely
// that a migration ran.

// migrateSeedLegacyRow writes a pre-#2387 row directly: signature inline, no
// sidecar key. The supported write paths can no longer produce one (CreateBook
// and UpdateBook both strip on the way in), so raw JSON is the only way to
// reconstruct the state all 67,824 production rows are actually in.
//
// extra keys are merged into the row verbatim, so a test can plant a field the
// Book struct does not know about.
func migrateSeedLegacyRow(t *testing.T, store *PebbleStore, id, tag string, extra map[string]any) {
	t.Helper()
	v1, mask, segments, coverage, builtAt := sigFixture(tag)
	row := map[string]any{
		"id":                    id,
		"title":                 "Legacy " + tag,
		"file_path":             "/tmp/migrate_" + tag + ".m4b",
		"book_sig_v1":           v1,
		"book_sig_v1_mask":      mask,
		"book_sig_segments":     segments,
		"book_sig_coverage_pct": coverage,
		"book_sig_built_at":     builtAt.Format("2006-01-02T15:04:05Z"),
	}
	for k, v := range extra {
		row[k] = v
	}
	data, err := json.Marshal(row)
	require.NoError(t, err)
	require.NoError(t, store.db.Set([]byte("book:"+id), data, pebble.Sync))
}

// migrateSidecarRaw returns the raw sidecar bytes, or nil when the key is
// absent. Tests assert on these bytes rather than on a hydrated Book so that
// "the sidecar exists" and "the read fell back to inline" cannot be confused.
func migrateSidecarRaw(t *testing.T, store *PebbleStore, id string) []byte {
	t.Helper()
	val, closer, err := store.db.Get(bookSigKey(id))
	if err == pebble.ErrNotFound {
		return nil
	}
	require.NoError(t, err)
	defer closer.Close()
	return append([]byte(nil), val...)
}

// migrateRequireRowStripped asserts none of the five signature keys, nor the
// signature value itself, survives in the book: row. Warmup reads this row and
// nothing else, so anything left here is a byte the migration failed to save.
func migrateRequireRowStripped(t *testing.T, store *PebbleStore, id, tag string) {
	t.Helper()
	row := string(rawBookRow(t, store, id))
	for _, k := range bookSigJSONKeys {
		require.NotContains(t, row, k,
			"book:%s must not carry %q after migration — warmup pays for every byte in this row", id, k)
	}
	v1, _, _, _, _ := sigFixture(tag)
	require.NotContains(t, row, v1,
		"the signature VALUE must not survive in the row under any key")
}

// TestMigrateBookSigToSidecar_PartialSidecarIsHealedNotSkipped covers the one
// way this migration could still delete data: a sidecar holding a STRICT SUBSET
// of the row's five fields. bookSigOf writes a sidecar when ANY field is
// non-nil, so a partial one is constructible — a rollback to a pre-#2387 binary
// writes inline again while an older sidecar lingers. If "sidecar exists" meant
// "leave it alone", stripping the row would drop the fields it lacks from BOTH
// places, and booksig_recovery_audit cannot see that: nil reads as
// "never had one", not as damage.
//
// The seeded sidecar carries only V1+Mask; the row carries all five. Correct
// behaviour is to fill the three missing fields from inline while leaving the
// two it already has at their (authoritative, DIFFERENT) values.
func TestMigrateBookSigToSidecar_PartialSidecarIsHealedNotSkipped(t *testing.T) {
	env := newBookSigEnv(t)
	const id = "partial-sidecar-1"
	migrateSeedLegacyRow(t, env.store, id, "inline", nil)

	// Sidecar predates the row's inline values and covers only two fields.
	newerV1, newerMask, _, _, _ := sigFixture("newer")
	partial, err := json.Marshal(map[string]any{"v1": newerV1, "mask": newerMask})
	require.NoError(t, err)
	require.NoError(t, env.store.db.Set(bookSigKey(id), partial, pebble.Sync))

	outcome, err := env.store.MigrateBookSigToSidecar(id, false)
	require.NoError(t, err)
	require.Equal(t, BookSigMigrateStrippedOnly, outcome)

	migrateRequireRowStripped(t, env.store, id, "inline")

	var got bookSigSidecar
	require.NoError(t, json.Unmarshal(migrateSidecarRaw(t, env.store, id), &got))

	// Present sidecar fields win: NOT overwritten by the inline copy.
	require.NotNil(t, got.V1)
	require.Equal(t, newerV1, *got.V1, "an existing sidecar value must not be downgraded to the inline one")
	require.NotNil(t, got.Mask)
	require.Equal(t, newerMask, *got.Mask)

	// Absent sidecar fields are filled from inline rather than lost with the row.
	_, _, wantSegments, wantCoverage, wantBuiltAt := sigFixture("inline")
	require.NotNil(t, got.Segments, "segments lived ONLY inline; stripping the row must not delete it")
	require.Equal(t, wantSegments, *got.Segments)
	require.NotNil(t, got.CoveragePct, "coverage_pct lived ONLY inline")
	require.Equal(t, wantCoverage, *got.CoveragePct)
	require.NotNil(t, got.BuiltAt, "built_at lived ONLY inline")
	require.True(t, wantBuiltAt.Equal(*got.BuiltAt))

	// And the whole book still reads back intact through the normal path.
	book, err := env.store.GetBookByID(id)
	require.NoError(t, err)
	require.NotNil(t, book.BookSigSegments)
	require.Equal(t, wantSegments, *book.BookSigSegments)
}

// TestMigrateBookSigToSidecar_CompleteSidecarIsLeftByteIdentical is the other
// half of the pair above: when the sidecar already covers every inline field,
// nothing is filled and the key must not be rewritten at all. Without this, the
// heal path could quietly churn every already-migrated book's sidecar.
func TestMigrateBookSigToSidecar_CompleteSidecarIsLeftByteIdentical(t *testing.T) {
	env := newBookSigEnv(t)
	const id = "complete-sidecar-1"
	migrateSeedLegacyRow(t, env.store, id, "inline", nil)

	newerV1, newerMask, segments, coverage, builtAt := sigFixture("newer")
	complete, err := json.Marshal(map[string]any{
		"v1": newerV1, "mask": newerMask, "segments": segments,
		"coverage_pct": coverage, "built_at": builtAt.Format(time.RFC3339),
	})
	require.NoError(t, err)
	require.NoError(t, env.store.db.Set(bookSigKey(id), complete, pebble.Sync))

	outcome, err := env.store.MigrateBookSigToSidecar(id, false)
	require.NoError(t, err)
	require.Equal(t, BookSigMigrateStrippedOnly, outcome)

	migrateRequireRowStripped(t, env.store, id, "inline")
	require.Equal(t, complete, migrateSidecarRaw(t, env.store, id),
		"a sidecar that already covers every inline field must be left untouched, not rewritten")
}

// TestBookSigJSONKeysMatchStructTags is the instrument check for the whole
// migration. bookSigJSONKeys is a hand-maintained list of stored key names; if
// a Book field's json tag is ever renamed, the migration would stop recognizing
// candidates and report "0 books migrated" as SUCCESS — a silent no-op with a
// green result, which is the exact failure family this repo keeps hitting.
// Reflection makes the list impossible to drift from the struct.
func TestBookSigJSONKeysMatchStructTags(t *testing.T) {
	fields := []string{
		"BookSigV1", "BookSigV1Mask", "BookSigSegments",
		"BookSigBuiltAt", "BookSigCoveragePct",
	}
	typ := reflect.TypeOf(Book{})

	want := make(map[string]bool, len(fields))
	for _, name := range fields {
		sf, ok := typ.FieldByName(name)
		require.Truef(t, ok, "Book has no field %s — the sidecar field group changed", name)
		tag := sf.Tag.Get("json")
		require.NotEmpty(t, tag, "Book.%s must have a json tag", name)
		// Strip ",omitempty" and friends.
		for i := 0; i < len(tag); i++ {
			if tag[i] == ',' {
				tag = tag[:i]
				break
			}
		}
		want[tag] = true
	}

	got := make(map[string]bool, len(bookSigJSONKeys))
	for _, k := range bookSigJSONKeys {
		got[k] = true
	}
	require.Equal(t, want, got,
		"bookSigJSONKeys has drifted from Book's struct tags; the migration would silently match nothing")
}

// TestBookSigMigrate_LegacyInlineRowMigrates is the happy path, asserted on
// both sides of the pairing: the row must lose the signature AND the sidecar
// must gain it, with the same values.
func TestBookSigMigrate_LegacyInlineRowMigrates(t *testing.T) {
	env := newBookSigEnv(t)
	store := env.store

	const id = "01MIGRATEHAPPY0000000000"
	migrateSeedLegacyRow(t, store, id, "happy", nil)

	outcome, err := store.MigrateBookSigToSidecar(id, false)
	require.NoError(t, err)
	require.Equal(t, BookSigMigrateMigrated, outcome)

	migrateRequireRowStripped(t, store, id, "happy")
	require.NotNil(t, migrateSidecarRaw(t, store, id), "the sidecar key must exist after migration")

	// Read through a REOPENED store: an in-process cache must not be able to
	// manufacture a pass for a change whose whole subject is durable bytes.
	fresh := env.reopen(t)
	got, err := fresh.GetBookByID(id)
	require.NoError(t, err)
	requireSigPresent(t, got, "happy")
}

// TestBookSigMigrate_PreservesUnknownRowFields guards the surgical strip.
//
// The tempting implementation is json.Marshal(stripBookSigForRow(&book)) — but
// unmarshalling into Book silently DROPS any stored field the struct does not
// declare, so that version would rewrite 67,824 rows with more removed than the
// five fields it intends to move. This test plants a field Book knows nothing
// about and requires it to survive byte-for-byte.
func TestBookSigMigrate_PreservesUnknownRowFields(t *testing.T) {
	env := newBookSigEnv(t)
	store := env.store

	const id = "01MIGRATEUNKNOWN00000000"
	migrateSeedLegacyRow(t, store, id, "unknown", map[string]any{
		"a_field_the_struct_does_not_know": "keep-me-42",
	})

	outcome, err := store.MigrateBookSigToSidecar(id, false)
	require.NoError(t, err)
	require.Equal(t, BookSigMigrateMigrated, outcome)

	var row map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(rawBookRow(t, store, id), &row))
	raw, ok := row["a_field_the_struct_does_not_know"]
	require.True(t, ok,
		"migration must not drop stored fields the Book struct does not declare — it is moving a signature, not rewriting the schema")
	require.JSONEq(t, `"keep-me-42"`, string(raw))
}

// TestBookSigMigrate_NewerSidecarSurvivesStaleInline covers the one ordering
// that loses data if handled naively.
//
// Post-#2387 every UpdateBook writes a fresh sidecar AND strips the row, so a
// row still carrying inline data alongside an existing sidecar means the
// sidecar is the NEWER value (bookSigSidecar.applyTo says so explicitly).
// Copying the stale inline copy over it would be a silent downgrade. Reading
// through GetBookByID would make this safe by accident; reading raw bytes,
// which the migration must do, does not.
func TestBookSigMigrate_NewerSidecarSurvivesStaleInline(t *testing.T) {
	env := newBookSigEnv(t)
	store := env.store

	const id = "01MIGRATESTALE0000000000"
	migrateSeedLegacyRow(t, store, id, "stale", nil)

	// A newer sidecar, written independently of the row.
	newV1, newMask, newSegments, newCoverage, newBuiltAt := sigFixture("fresh")
	payload, err := json.Marshal(bookSigSidecar{
		V1: &newV1, Mask: &newMask, Segments: &newSegments,
		BuiltAt: &newBuiltAt, CoveragePct: &newCoverage,
	})
	require.NoError(t, err)
	require.NoError(t, store.db.Set(bookSigKey(id), payload, pebble.Sync))

	outcome, err := store.MigrateBookSigToSidecar(id, false)
	require.NoError(t, err)
	require.Equal(t, BookSigMigrateStrippedOnly, outcome,
		"a row with an existing sidecar must be stripped only, never re-sourced from the stale inline copy")

	// The row loses the stale signature...
	migrateRequireRowStripped(t, store, id, "stale")
	// ...and the sidecar still holds the NEWER values, untouched.
	fresh := env.reopen(t)
	got, err := fresh.GetBookByID(id)
	require.NoError(t, err)
	requireSigPresent(t, got, "fresh")
}

// TestBookSigMigrate_NoSignatureCreatesNoSidecar is the negative control that
// catches a candidate detector matching everything.
//
// If the detector returned true unconditionally, every test above would still
// pass while the op reported all 67,824 books "migrated" and littered the
// database with empty sidecars. The dry-run count is the field instrument for
// this (expected: low tens of thousands, from 580 MB / ~22 KB per signature);
// this is the unit-level one.
func TestBookSigMigrate_NoSignatureCreatesNoSidecar(t *testing.T) {
	env := newBookSigEnv(t)
	store := env.store

	created, err := store.CreateBook(&Book{
		Title:    "No Signature At All",
		FilePath: "/tmp/migrate_nosig.m4b",
	})
	require.NoError(t, err)

	outcome, err := store.MigrateBookSigToSidecar(created.ID, false)
	require.NoError(t, err)
	require.Equal(t, BookSigMigrateNotCandidate, outcome)

	require.Nil(t, migrateSidecarRaw(t, store, created.ID),
		"a book with no signature must not get a book_sig: key — an empty sidecar is indistinguishable from a real one to every reader")
}

// TestBookSigMigrate_AllNullValuesStripsOnly covers a row whose five keys are
// present but explicitly null. There is no signature to preserve, so the
// correct action is to strip the keys and create nothing.
func TestBookSigMigrate_AllNullValuesStripsOnly(t *testing.T) {
	env := newBookSigEnv(t)
	store := env.store

	const id = "01MIGRATENULLS0000000000"
	row := `{"id":"` + id + `","title":"Null Sig","file_path":"/tmp/migrate_nulls.m4b",` +
		`"book_sig_v1":null,"book_sig_v1_mask":null,"book_sig_segments":null,` +
		`"book_sig_built_at":null,"book_sig_coverage_pct":null}`
	require.NoError(t, store.db.Set([]byte("book:"+id), []byte(row), pebble.Sync))

	outcome, err := store.MigrateBookSigToSidecar(id, false)
	require.NoError(t, err)
	require.Equal(t, BookSigMigrateStrippedOnly, outcome)
	require.Nil(t, migrateSidecarRaw(t, store, id),
		"all-null signature fields must not manufacture a sidecar")

	stripped := string(rawBookRow(t, store, id))
	for _, k := range bookSigJSONKeys {
		require.NotContains(t, stripped, k)
	}
}

// TestBookSigMigrate_DryRunWritesNothing. The op is dry-run gated because this
// is the irreversible step; a dry run that mutated anything would make the gate
// decorative.
func TestBookSigMigrate_DryRunWritesNothing(t *testing.T) {
	env := newBookSigEnv(t)
	store := env.store

	const id = "01MIGRATEDRYRUN000000000"
	migrateSeedLegacyRow(t, store, id, "dryrun", nil)
	before := rawBookRow(t, store, id)

	outcome, err := store.MigrateBookSigToSidecar(id, true)
	require.NoError(t, err)
	require.Equal(t, BookSigMigrateMigrated, outcome,
		"a dry run must still CLASSIFY the book, or the reported counts mean nothing")

	require.Equal(t, before, rawBookRow(t, store, id),
		"dry run must leave the row byte-identical")
	require.Nil(t, migrateSidecarRaw(t, store, id),
		"dry run must not create a sidecar key")
}

// TestBookSigMigrate_IsIdempotent. The op must be safe to re-run: the first
// pass leaves skipped_raced books behind by design, and a second pass is how
// they get picked up.
func TestBookSigMigrate_IsIdempotent(t *testing.T) {
	env := newBookSigEnv(t)
	store := env.store

	const id = "01MIGRATEIDEMPOTENT00000"
	migrateSeedLegacyRow(t, store, id, "idem", nil)

	first, err := store.MigrateBookSigToSidecar(id, false)
	require.NoError(t, err)
	require.Equal(t, BookSigMigrateMigrated, first)
	afterFirst := migrateSidecarRaw(t, store, id)

	second, err := store.MigrateBookSigToSidecar(id, false)
	require.NoError(t, err)
	require.Equal(t, BookSigMigrateNotCandidate, second,
		"an already-migrated row carries no inline signature and must not be touched again")
	require.Equal(t, afterFirst, migrateSidecarRaw(t, store, id),
		"a re-run must not rewrite the sidecar")

	fresh := env.reopen(t)
	got, err := fresh.GetBookByID(id)
	require.NoError(t, err)
	requireSigPresent(t, got, "idem")
}

// TestBookSigMigrate_SkipsRacedRow exercises the CAS directly.
//
// PebbleStore has no per-book write serialization, so a concurrent UpdateBook
// between our read and our commit would otherwise see its ENTIRE update
// reverted — title, path, everything — not merely the signature. The guard is a
// byte compare immediately before commit. Here the `before` snapshot
// deliberately disagrees with what is stored, which is what a raced row looks
// like from the committer's point of view.
func TestBookSigMigrate_SkipsRacedRow(t *testing.T) {
	env := newBookSigEnv(t)
	store := env.store

	const id = "01MIGRATERACED0000000000"
	migrateSeedLegacyRow(t, store, id, "raced", nil)
	stored := rawBookRow(t, store, id)

	stale := append([]byte(nil), stored...)
	stale = append(stale, ' ') // any difference at all must abort the write

	outcome, err := store.commitBookSigMigration(
		id, []byte("book:"+id), stale, []byte(`{"id":"`+id+`"}`), []byte(`{"v1":"x"}`),
		false, BookSigMigrateMigrated)
	require.NoError(t, err)
	require.Equal(t, BookSigMigrateSkippedRaced, outcome)

	require.Equal(t, stored, rawBookRow(t, store, id),
		"a raced row must be left completely untouched, not partially written")
	require.Nil(t, migrateSidecarRaw(t, store, id),
		"a raced book must not get a sidecar either — the row and sidecar move together or not at all")
}

// TestBookSigMigrate_ConformancePairing is the invariant that makes the
// migration safe to run on production data: across a mixed library, EVERY book
// that had an inline signature ends with a sidecar carrying the same five
// values, and every book that had none ends with no sidecar at all.
//
// The failure this guards — row stripped, sidecar absent — is invisible to
// booksig_recovery_audit, which reads all-five-nil as "never had a signature".
// Nothing else in the system would report it.
func TestBookSigMigrate_ConformancePairing(t *testing.T) {
	env := newBookSigEnv(t)
	store := env.store

	withSig := map[string]string{
		"01MIGRATECONF00000000001": "conf1",
		"01MIGRATECONF00000000002": "conf2",
		"01MIGRATECONF00000000003": "conf3",
	}
	for id, tag := range withSig {
		migrateSeedLegacyRow(t, store, id, tag, nil)
	}

	withoutSig := make([]string, 0, 2)
	for _, title := range []string{"Plain A", "Plain B"} {
		b, err := store.CreateBook(&Book{Title: title, FilePath: "/tmp/" + title + ".m4b"})
		require.NoError(t, err)
		withoutSig = append(withoutSig, b.ID)
	}

	migrated := 0
	for id := range withSig {
		outcome, err := store.MigrateBookSigToSidecar(id, false)
		require.NoError(t, err)
		require.Equal(t, BookSigMigrateMigrated, outcome)
		migrated++
	}
	for _, id := range withoutSig {
		outcome, err := store.MigrateBookSigToSidecar(id, false)
		require.NoError(t, err)
		require.Equal(t, BookSigMigrateNotCandidate, outcome)
	}
	require.Equal(t, len(withSig), migrated)

	fresh := env.reopen(t)

	// had-signature => stripped row AND sidecar with the SAME five values.
	for id, tag := range withSig {
		migrateRequireRowStripped(t, fresh, id, tag)
		require.NotNilf(t, migrateSidecarRaw(t, fresh, id),
			"book %s had a signature; a stripped row with no sidecar is silent data loss no audit can detect", id)
		got, err := fresh.GetBookByID(id)
		require.NoError(t, err)
		requireSigPresent(t, got, tag)
	}

	// no-signature => still no sidecar.
	for _, id := range withoutSig {
		require.Nilf(t, migrateSidecarRaw(t, fresh, id),
			"book %s never had a signature and must not have gained a sidecar", id)
	}
}

// TestAsBookSigMigrateStore_ResolvesPebbleStore. Prod always installs the Bleve
// indexedStore decorator, and a bare `store.(*PebbleStore)` against a wrapped
// store is indistinguishable from an unsupported backend — which is how several
// ops silently no-opped in production. The op must resolve through the helper.
func TestAsBookSigMigrateStore_ResolvesPebbleStore(t *testing.T) {
	env := newBookSigEnv(t)

	require.NotNil(t, AsBookSigMigrateStore(env.store),
		"a *PebbleStore must resolve as a BookSigMigrateStore")
	require.Nil(t, AsBookSigMigrateStore(nil))
	require.Nil(t, AsBookSigMigrateStore(struct{}{}),
		"an unrelated value must not resolve — the op fails loudly rather than reporting 0 books migrated")
}

// TestBookSigMigrateOutcome_String keeps the reported bucket names stable; they
// are what the operator reads to decide whether the apply behaved.
func TestBookSigMigrateOutcome_String(t *testing.T) {
	require.Equal(t, "not_candidate", BookSigMigrateNotCandidate.String())
	require.Equal(t, "migrated", BookSigMigrateMigrated.String())
	require.Equal(t, "stripped_only", BookSigMigrateStrippedOnly.String())
	require.Equal(t, "skipped_raced", BookSigMigrateSkippedRaced.String())
}
