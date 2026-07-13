// file: internal/database/dataloss_preserve_invariant_test.go
// version: 1.0.0
// guid: d3f4a5b6-7c8d-9e0f-1a2b-preserveinv001
// last-edited: 2026-07-13

package database

import (
	"reflect"
	"testing"
	"time"
)

// wantStrippedCount is the number of Book fields stripBookForMemdb nils and
// that UpdateBook's preserve-on-nil guard must therefore restore. This is a
// deliberate tripwire: if you add a heavy field to stripBookForMemdb (raising
// this count) you MUST also add its preserve-on-nil branch to UpdateBook, then
// bump this constant. If the count changes and this test starts failing, that
// is the signal — do not just bump the number without adding the guard branch.
const wantStrippedCount = 9

// dlFixedTime is a clean, monotonic-clock-free UTC instant so JSON round-trips
// are byte-exact and reflect.DeepEqual on *time.Time fields is reliable.
var dlFixedTime = time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC)

func dlIntPtr(i int) *int       { return &i }
func dlInt64Ptr(i int64) *int64 { return &i }

// dlPopulateNonZero recursively fills every settable field of v with a
// distinctive non-zero sentinel. The whole point of T1 is that a NEW field
// added to Book (and to stripBookForMemdb) is automatically covered — that only
// works if the populator leaves NOTHING at its zero value, so a newly-added
// pointer field is non-nil before the strip and the strip's nil-ing of it shows
// up in the diff. The `default` branch fails loudly so an unhandled field type
// forces this helper (and the test's coverage) to be updated rather than
// silently skipped.
func dlPopulateNonZero(t *testing.T, v reflect.Value, field string) {
	t.Helper()
	switch v.Kind() {
	case reflect.String:
		v.SetString("sentinel-" + field)
	case reflect.Bool:
		v.SetBool(true)
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		v.SetInt(7)
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		v.SetUint(7)
	case reflect.Float32, reflect.Float64:
		v.SetFloat(3.5)
	case reflect.Interface:
		// e.g. MetadataProvenanceEntry.Value (any). A concrete non-nil string
		// is enough to make the field non-zero; strip doesn't touch these.
		v.Set(reflect.ValueOf("sentinel-" + field))
	case reflect.Ptr:
		elem := reflect.New(v.Type().Elem())
		dlPopulateNonZero(t, elem.Elem(), field)
		v.Set(elem)
	case reflect.Slice:
		elem := reflect.New(v.Type().Elem())
		dlPopulateNonZero(t, elem.Elem(), field)
		v.Set(reflect.Append(reflect.MakeSlice(v.Type(), 0, 1), elem.Elem()))
	case reflect.Map:
		m := reflect.MakeMap(v.Type())
		k := reflect.New(v.Type().Key())
		dlPopulateNonZero(t, k.Elem(), field)
		val := reflect.New(v.Type().Elem())
		dlPopulateNonZero(t, val.Elem(), field)
		m.SetMapIndex(k.Elem(), val.Elem())
		v.Set(m)
	case reflect.Struct:
		if v.Type() == reflect.TypeOf(time.Time{}) {
			v.Set(reflect.ValueOf(dlFixedTime))
			return
		}
		for i := 0; i < v.NumField(); i++ {
			if f := v.Field(i); f.CanSet() {
				dlPopulateNonZero(t, f, v.Type().Field(i).Name)
			}
		}
	default:
		t.Fatalf("dlPopulateNonZero: unhandled kind %s for field %q — extend the populator so T1 stays drift-proof", v.Kind(), field)
	}
}

// strippedBookFields returns the indices+names of Book fields that
// stripBookForMemdb changes, derived by diffing a fully-populated Book against
// its stripped copy. This is the SAME source of truth the guard must track —
// the set can never drift from stripBookForMemdb because it is computed from it.
func strippedBookFields(t *testing.T) (full *Book, indices []int, names []string) {
	t.Helper()
	full = &Book{}
	dlPopulateNonZero(t, reflect.ValueOf(full).Elem(), "Book")

	stripped := stripBookForMemdb(full)
	fv, sv := reflect.ValueOf(full).Elem(), reflect.ValueOf(stripped).Elem()
	for i := 0; i < fv.NumField(); i++ {
		if !fv.Field(i).CanInterface() {
			continue
		}
		if !reflect.DeepEqual(fv.Field(i).Interface(), sv.Field(i).Interface()) {
			indices = append(indices, i)
			names = append(names, fv.Type().Field(i).Name)
		}
	}
	return full, indices, names
}

// TestStripBookForMemdb_StrippedSetMatchesGuardCount asserts the derived
// stripped-field set has exactly wantStrippedCount members. A mismatch means
// stripBookForMemdb changed and the UpdateBook guard + this constant need review.
func TestStripBookForMemdb_StrippedSetMatchesGuardCount(t *testing.T) {
	_, _, names := strippedBookFields(t)
	if len(names) != wantStrippedCount {
		t.Fatalf("stripBookForMemdb now strips %d fields %v; want %d. If you added a heavy field, add a preserve-on-nil branch to UpdateBook and bump wantStrippedCount.",
			len(names), names, wantStrippedCount)
	}
}

// TestUpdateBook_PreservesEveryStrippedFieldOnNilIncoming is the crown-jewel
// invariant: for EACH field stripBookForMemdb nils, a memdb-round-trip
// UpdateBook (that field nil, everything else intact) must NOT wipe the stored
// value. Reflection-driven so a newly-stripped field is covered automatically —
// if someone adds a field to stripBookForMemdb but forgets UpdateBook's guard,
// the subtest for that field fails here.
func TestUpdateBook_PreservesEveryStrippedFieldOnNilIncoming(t *testing.T) {
	store, cleanup := setupPebbleTestDB(t)
	defer cleanup()

	full, indices, names := strippedBookFields(t)
	if len(indices) == 0 {
		t.Fatal("no stripped fields derived — populator or strip changed")
	}

	for k, idx := range indices {
		name := names[k]
		t.Run(name, func(t *testing.T) {
			// Seed a book carrying real (non-nil) values in every heavy field.
			created, err := store.CreateBook(&Book{Title: "seed-" + name, FilePath: "/lib/seed-" + name + ".m4b"})
			if err != nil {
				t.Fatalf("CreateBook: %v", err)
			}
			seed := *full
			seed.ID = created.ID
			seed.FilePath = "/lib/seed-" + name + ".m4b"
			if _, err := store.UpdateBook(created.ID, &seed); err != nil {
				t.Fatalf("UpdateBook (seed): %v", err)
			}
			seeded, err := store.GetBookByID(created.ID)
			if err != nil || seeded == nil {
				t.Fatalf("GetBookByID (seeded): err=%v got=%v", err, seeded)
			}
			want := reflect.ValueOf(seeded).Elem().Field(idx).Interface()
			if isNilValue(reflect.ValueOf(want)) {
				t.Fatalf("seed did not populate field %q (got nil) — cannot test preservation", name)
			}

			// Simulate the memdb/projection round trip: exactly this ONE field
			// nil, everything else carried through unchanged.
			proj := *seeded
			reflect.ValueOf(&proj).Elem().Field(idx).Set(reflect.Zero(reflect.ValueOf(&proj).Elem().Field(idx).Type()))
			if _, err := store.UpdateBook(created.ID, &proj); err != nil {
				t.Fatalf("UpdateBook (projection): %v", err)
			}

			got, err := store.GetBookByID(created.ID)
			if err != nil || got == nil {
				t.Fatalf("GetBookByID (final): err=%v got=%v", err, got)
			}
			final := reflect.ValueOf(got).Elem().Field(idx).Interface()
			if !reflect.DeepEqual(final, want) {
				t.Errorf("field %q WIPED by memdb-round-trip UpdateBook: got %#v, want %#v (add a preserve-on-nil branch to UpdateBook)", name, final, want)
			}
		})
	}
}

func isNilValue(v reflect.Value) bool {
	switch v.Kind() {
	case reflect.Ptr, reflect.Slice, reflect.Map, reflect.Interface, reflect.Chan, reflect.Func:
		return v.IsNil()
	default:
		return false
	}
}
