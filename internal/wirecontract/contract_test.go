// file: internal/wirecontract/contract_test.go
// version: 1.1.0
// guid: 8e3a6b14-0c5d-4f72-9a81-d2b7e4c6f039
// last-edited: 2026-10-18

// Package wirecontract pins the JSON the generated TypeScript types describe
// (07-F1). For each type tygo generates (tygo.yaml) it marshals a fully
// populated, synthetic sample and compares it with a checked-in golden under
// web/src/types/contract/testdata/. web/src/types/contract/contract.test.ts loads the same goldens
// and assigns them to the generated types, so the Go and TypeScript sides are
// tied to one document: a renamed tag changes the golden here and fails the
// TypeScript compile there.
//
// Regenerate the goldens after an intended change with
//
//	UPDATE_WIRE_GOLDEN=1 go test ./internal/wirecontract/
package wirecontract

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/repairs"
)

// goldenDir is under web/ so the frontend build context (which copies only web/)
// has every file the contract test imports.
const goldenDir = "../../web/src/types/contract/testdata"

var sampleTime = time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)

// fill sets every field of v (settable, addressable) to a deterministic
// synthetic value, following pointers and creating one element of every slice
// and map, so that omitempty never hides a field from the golden. Fields tagged
// json:"-" are left alone: they are not on the wire.
func fill(v reflect.Value, name string) {
	switch v.Type() {
	case reflect.TypeOf(time.Time{}):
		v.Set(reflect.ValueOf(sampleTime))
		return
	case reflect.TypeOf(json.RawMessage{}):
		v.Set(reflect.ValueOf(json.RawMessage(`{"synthetic":true}`)))
		return
	}
	switch v.Kind() {
	case reflect.Pointer:
		v.Set(reflect.New(v.Type().Elem()))
		fill(v.Elem(), name)
	case reflect.Struct:
		for i := 0; i < v.NumField(); i++ {
			f := v.Type().Field(i)
			if !f.IsExported() || f.Tag.Get("json") == "-" {
				continue
			}
			fill(v.Field(i), f.Name)
		}
	case reflect.Slice:
		s := reflect.MakeSlice(v.Type(), 1, 1)
		fill(s.Index(0), name)
		v.Set(s)
	case reflect.Map:
		m := reflect.MakeMap(v.Type())
		k := reflect.New(v.Type().Key()).Elem()
		fill(k, "key")
		e := reflect.New(v.Type().Elem()).Elem()
		fill(e, name)
		m.SetMapIndex(k, e)
		v.Set(m)
	case reflect.String:
		v.SetString("synthetic-" + name)
	case reflect.Bool:
		v.SetBool(true)
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		v.SetInt(7)
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		v.SetUint(7)
	case reflect.Float32, reflect.Float64:
		v.SetFloat(1.5)
	case reflect.Interface:
		if v.NumMethod() != 0 {
			panic(fmt.Sprintf("wirecontract: non-empty interface field %s on the wire", name))
		}
		v.Set(reflect.ValueOf("synthetic-" + name)) // an `any` field: tygo emits `any`
	default:
		panic(fmt.Sprintf("wirecontract: unsupported kind %s for %s", v.Kind(), name))
	}
}

func sample[T any]() T {
	var t T
	fill(reflect.ValueOf(&t).Elem(), reflect.TypeOf(t).Name())
	return t
}

// TestTSTypeTagsMatchGoTypes ties each hand-written tstype tag to the Go field
// type it overrides: a tag is only valid on a nil-able map or slice, which is
// the one case where Go's null differs from what tygo infers.
func TestTSTypeTagsMatchGoTypes(t *testing.T) {
	f, ok := reflect.TypeOf(repairs.RowsPage{}).FieldByName("SkippedByKindInClass")
	if !ok {
		t.Fatal("RowsPage.SkippedByKindInClass is gone; update its tstype tag and this test")
	}
	if f.Type != reflect.TypeOf(map[string]int{}) {
		t.Fatalf("SkippedByKindInClass is %s; its tstype tag says { [key: string]: number } | null", f.Type)
	}
	if got := f.Tag.Get("tstype"); got != "{ [key: string]: number } | null" {
		t.Fatalf("tstype tag = %q", got)
	}
	var nilPage repairs.RowsPage
	b, _ := json.Marshal(nilPage)
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	if v, present := m["skipped_by_kind_in_class"]; !present || v != nil {
		t.Fatalf("a nil map must marshal as null (the reason for the tag), got %v present=%v", v, present)
	}
}

func TestWireGoldens(t *testing.T) {
	cases := []struct {
		file string
		val  any
	}{
		{"repairs_row.json", sample[repairs.Row]()},
		{"repairs_row_member.json", sample[repairs.RowMember]()},
		{"repairs_rows_page.json", sample[repairs.RowsPage]()},
		{"repairs_plan_params.json", sample[repairs.PlanParams]()},
		{"repairs_plan_result.json", sample[repairs.PlanResult]()},
		{"repairs_row_result.json", sample[repairs.RowResult]()},
		{"repairs_apply_result.json", sample[repairs.ApplyResult]()},
		{"database_book_core.json", sample[database.BookCore]()},
		{"database_book_file_core.json", sample[database.BookFileCore]()},
		{"database_scan_state.json", sample[database.ScanState]()},
		{"database_stage_status.json", sample[database.StageStatus]()},
		{"database_book_author.json", sample[database.BookAuthor]()},
		{"database_metadata_provenance_entry.json", sample[database.MetadataProvenanceEntry]()},
	}
	for _, c := range cases {
		t.Run(c.file, func(t *testing.T) {
			got, err := json.MarshalIndent(c.val, "", "  ")
			if err != nil {
				t.Fatal(err)
			}
			got = append(got, '\n')
			path := filepath.Join(goldenDir, c.file)
			if os.Getenv("UPDATE_WIRE_GOLDEN") != "" {
				if err := os.WriteFile(path, got, 0o644); err != nil {
					t.Fatal(err)
				}
				return
			}
			want, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("%v (run with UPDATE_WIRE_GOLDEN=1 to create it)", err)
			}
			if !bytes.Equal(got, want) {
				t.Fatalf("%s drifted from the Go struct; regenerate with UPDATE_WIRE_GOLDEN=1 and update web/src/types/generated (make generate-types)\n got: %s\nwant: %s", c.file, got, want)
			}
		})
	}
}

// nilableWithoutOmit lists the slice and map fields of every generated type
// that lack omitempty/omitzero, i.e. that Go marshals as null when nil while
// tygo types them non-null. Each entry says why null cannot reach the wire (or,
// for SkippedByKindInClass, carries a tstype tag saying it can). A new such
// field fails the test until it is normalised server-side and listed here.
var nilableWithoutOmit = map[string]string{
	"repairs.Row.BookIDs":                   "RunPlan and LoadPlan (PlanResult.normalize) replace nil with []",
	"repairs.PlanResult.SkippedByKind":      "RunPlan builds it non-nil; normalize covers stored plans",
	"repairs.PlanResult.Rows":               "RunPlan and normalize replace nil with []",
	"repairs.RowsPage.SkippedByKind":        "copied from a normalized PlanResult",
	"repairs.RowsPage.SkippedByKindInClass": "may be null: tstype tag on the field says so",
	"repairs.RowsPage.Rows":                 "Page initialises []Row{}",
	"repairs.ApplyResult.ByOutcome":         "RunApply initialises it",
	"repairs.ApplyResult.Rows":              "RunApply initialises []RowResult{}",
}

func TestNilableFieldsAreAccountedFor(t *testing.T) {
	types := []reflect.Type{
		reflect.TypeOf(repairs.Row{}), reflect.TypeOf(repairs.RowMember{}),
		reflect.TypeOf(repairs.RowsPage{}), reflect.TypeOf(repairs.RowResult{}),
		reflect.TypeOf(repairs.ApplyResult{}), reflect.TypeOf(repairs.PlanParams{}),
		reflect.TypeOf(repairs.PlanResult{}),
		reflect.TypeOf(database.BookCore{}), reflect.TypeOf(database.BookFileCore{}),
		reflect.TypeOf(database.ScanState{}), reflect.TypeOf(database.StageStatus{}),
		reflect.TypeOf(database.BookAuthor{}), reflect.TypeOf(database.MetadataProvenanceEntry{}),
	}
	seen := map[string]bool{}
	for _, typ := range types {
		pkg := typ.PkgPath()[len(typ.PkgPath())-len(filepath.Base(typ.PkgPath())):]
		for i := 0; i < typ.NumField(); i++ {
			f := typ.Field(i)
			tag := f.Tag.Get("json")
			if !f.IsExported() || tag == "-" || f.Type == reflect.TypeOf(json.RawMessage{}) {
				continue
			}
			if f.Type.Kind() != reflect.Slice && f.Type.Kind() != reflect.Map {
				continue
			}
			if strings.Contains(tag, "omitempty") || strings.Contains(tag, "omitzero") {
				continue
			}
			key := pkg + "." + typ.Name() + "." + f.Name
			seen[key] = true
			if _, ok := nilableWithoutOmit[key]; !ok {
				t.Errorf("%s is a slice/map without omitempty: normalise it server-side and list it in nilableWithoutOmit", key)
			}
		}
	}
	for key := range nilableWithoutOmit {
		if !seen[key] {
			t.Errorf("nilableWithoutOmit lists %s, which is no longer such a field", key)
		}
	}
}
