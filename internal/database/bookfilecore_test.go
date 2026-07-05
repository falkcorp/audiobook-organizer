// file: internal/database/bookfilecore_test.go
// version: 1.0.0
// guid: 29325171-4866-4142-82c6-e010f724ae5e
// last-edited: 2026-07-05

package database

import (
	"reflect"
	"sort"
	"testing"
	"time"
)

// strippedBookFileFields is the authoritative heavy set that stripBookFileForMemdb
// clears before memdb insertion — the exact fields BookFileCore must NOT contain.
// FingerprintFailedAt and AcoustIDFingerprintDurationSec are deliberately absent
// here (they are RETAINED on Core).
var strippedBookFileFields = []string{
	"AcoustIDFingerprint",
	"AcoustIDSeg0",
	"AcoustIDSeg1",
	"AcoustIDSeg2",
	"AcoustIDSeg3",
	"AcoustIDSeg4",
	"AcoustIDSeg5",
	"AcoustIDSeg6",
	"FingerprintDiagnosticJSON",
	"FingerprintFailureDetail",
	"FingerprintFailureReason",
}

func structFieldNames(t reflect.Type) map[string]struct{} {
	names := make(map[string]struct{}, t.NumField())
	for i := 0; i < t.NumField(); i++ {
		names[t.Field(i).Name] = struct{}{}
	}
	return names
}

// TestBookFileCoreIsBookFileMinusHeavyFields asserts the field-name set of
// BookFileCore equals BookFile's set minus EXACTLY the stripped heavy set.
func TestBookFileCoreIsBookFileMinusHeavyFields(t *testing.T) {
	bfNames := structFieldNames(reflect.TypeOf(BookFile{}))
	coreNames := structFieldNames(reflect.TypeOf(BookFileCore{}))

	// (1) Every Core field must exist on BookFile (no invented fields).
	for name := range coreNames {
		if _, ok := bfNames[name]; !ok {
			t.Errorf("BookFileCore has field %q that BookFile lacks", name)
		}
	}

	// (2) The set BookFile - BookFileCore must equal the stripped set exactly.
	var diff []string
	for name := range bfNames {
		if _, ok := coreNames[name]; !ok {
			diff = append(diff, name)
		}
	}
	sort.Strings(diff)

	want := append([]string(nil), strippedBookFileFields...)
	sort.Strings(want)

	if !reflect.DeepEqual(diff, want) {
		t.Errorf("BookFile - BookFileCore field diff mismatch\n got: %v\nwant: %v", diff, want)
	}

	// (3) Guard the retained pair explicitly — they must live on Core.
	for _, kept := range []string{"FingerprintFailedAt", "AcoustIDFingerprintDurationSec"} {
		if _, ok := coreNames[kept]; !ok {
			t.Errorf("expected retained field %q on BookFileCore, but it is missing", kept)
		}
	}
}

// fillNonZero recursively populates every field of the struct pointed to by v
// with a distinct non-zero value, so a later zero-check proves the copy is total.
func fillNonZero(t *testing.T, v reflect.Value) {
	t.Helper()
	ts := time.Date(2026, 7, 5, 12, 0, 0, 0, time.UTC)
	for i := 0; i < v.NumField(); i++ {
		f := v.Field(i)
		if !f.CanSet() {
			t.Fatalf("field %q is not settable", v.Type().Field(i).Name)
		}
		switch f.Kind() {
		case reflect.String:
			f.SetString("v_" + v.Type().Field(i).Name)
		case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
			f.SetInt(int64(i + 1))
		case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
			f.SetUint(uint64(i + 1))
		case reflect.Float32, reflect.Float64:
			f.SetFloat(float64(i) + 0.5)
		case reflect.Bool:
			f.SetBool(true)
		case reflect.Map:
			m := reflect.MakeMap(f.Type())
			m.SetMapIndex(reflect.ValueOf("k_"+v.Type().Field(i).Name), reflect.ValueOf("val"))
			f.Set(m)
		case reflect.Slice:
			s := reflect.MakeSlice(f.Type(), 1, 1)
			s.Index(0).Set(reflect.ValueOf(byte(i + 1)).Convert(f.Type().Elem()))
			f.Set(s)
		case reflect.Ptr:
			// Only *time.Time appears in BookFile.
			if f.Type().Elem() == reflect.TypeOf(time.Time{}) {
				tt := ts.Add(time.Duration(i) * time.Hour)
				f.Set(reflect.ValueOf(&tt))
			} else {
				np := reflect.New(f.Type().Elem())
				f.Set(np)
			}
		case reflect.Struct:
			if f.Type() == reflect.TypeOf(time.Time{}) {
				f.Set(reflect.ValueOf(ts.Add(time.Duration(i) * time.Minute)))
			} else {
				fillNonZero(t, f)
			}
		default:
			t.Fatalf("unhandled field kind %s for field %q", f.Kind(), v.Type().Field(i).Name)
		}
	}
}

// TestBookFileCoreCopiesAllFields populates every BookFile field with a distinct
// non-zero value, projects via Core(), and asserts every BookFileCore field was
// copied from the same-named BookFile field.
func TestBookFileCoreCopiesAllFields(t *testing.T) {
	var bf BookFile
	fillNonZero(t, reflect.ValueOf(&bf).Elem())

	// Sanity: confirm no BookFile field is still zero, so the copy check is meaningful.
	bfv := reflect.ValueOf(bf)
	for i := 0; i < bfv.NumField(); i++ {
		if bfv.Field(i).IsZero() {
			t.Fatalf("test setup failed: BookFile field %q left zero", bfv.Type().Field(i).Name)
		}
	}

	core := bf.Core()
	cv := reflect.ValueOf(core)
	ct := cv.Type()
	for i := 0; i < ct.NumField(); i++ {
		name := ct.Field(i).Name
		coreVal := cv.Field(i).Interface()
		srcVal := bfv.FieldByName(name).Interface()
		if !reflect.DeepEqual(coreVal, srcVal) {
			t.Errorf("Core() field %q not copied: got %v, want %v", name, coreVal, srcVal)
		}
	}
}
