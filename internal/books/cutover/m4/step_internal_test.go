// file: internal/books/cutover/m4/step_internal_test.go
// version: 1.2.0
// guid: e7b2f094-1d6a-4c83-9a50-3c8d4f1e6b27
// last-edited: 2026-11-18

package m4

import (
	"reflect"
	"strings"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/books/store"
)

// jsonFields maps a struct's JSON names to the Go kind of the field, looking
// through pointers, slices of bytes and raw messages (json.RawMessage and
// []byte both decode "any JSON" or base64; the frozen copy is held to the
// same family, not the same spelling).
func jsonFields(t *testing.T, typ reflect.Type) map[string]reflect.Kind {
	t.Helper()
	for typ.Kind() == reflect.Pointer {
		typ = typ.Elem()
	}
	out := map[string]reflect.Kind{}
	for i := range typ.NumField() {
		f := typ.Field(i)
		tag, _, _ := strings.Cut(f.Tag.Get("json"), ",")
		if tag == "" || tag == "-" {
			continue
		}
		ft := f.Type
		for ft.Kind() == reflect.Pointer {
			ft = ft.Elem()
		}
		out[tag] = ft.Kind()
	}
	return out
}

// TestFrozenRowsMatchTheStore holds every frozen row type to the store's own:
// each JSON name the step reads exists in the store's type, with the same kind
// of value. A store type that renamed or retyped a field after the copy was
// taken fails here instead of decoding to the zero value in production. (The
// converse is deliberate: the step reads a subset, and json ignores the rest.)
func TestFrozenRowsMatchTheStore(t *testing.T) {
	cases := []struct {
		name          string
		frozen, store any
	}{
		{"run", runRow{}, store.RunRecord{}},
		{"actor", headActor{}, store.Actor{}},
		{"progress", progRow{}.Progress, store.ProgressSnapshot{}},
		{"transition", transitionRow{}, store.Transition{}},
		{"intent", intentRow{}, store.Intent{}},
		{"item", itemRow{}, store.ItemOutcome{}},
		{"log", logRow{}, store.LogLine{}},
		{"checkpoint", ckptRow{}, store.Checkpoint{}},
		{"v1 operation", op1Row{}, store.Operation{}},
		{"v1 log", opLog1Row{}, store.OperationLog{}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			frozen, head := jsonFields(t, reflect.TypeOf(c.frozen)), jsonFields(t, reflect.TypeOf(c.store))
			for name, kind := range frozen {
				hk, ok := head[name]
				if !ok {
					t.Errorf("the frozen copy reads %q, which the store's %T has no field for", name, c.store)
					continue
				}
				// A raw message ([]byte of kind Slice) may stand for any JSON value
				// the store keeps as a typed field, and the reverse.
				if hk != kind && kind != reflect.Slice && hk != reflect.Slice {
					t.Errorf("%q: the frozen copy holds a %v, the store's field is a %v", name, kind, hk)
				}
			}
		})
	}
	// The hot half has no exported type; it is RunRecord's own progress fields.
	head := jsonFields(t, reflect.TypeOf(store.RunRecord{}))
	for name := range jsonFields(t, reflect.TypeOf(progRow{})) {
		if _, ok := head[name]; !ok {
			t.Errorf("progRow reads %q, which the store's RunRecord (the hot half's source) has no field for", name)
		}
	}
}

// TestFrozenV2RowsMatchTheStore is the same hold for the v2 rows, which have no
// JSON tags: the Go field names are the stored keys, so each field of the frozen
// copy must exist by name, with the same type, in the store's row.
func TestFrozenV2RowsMatchTheStore(t *testing.T) {
	cases := []struct {
		name          string
		frozen, store any
	}{
		{"log", opLogV2{}, store.OpLogV2Row{}},
		{"error", opErrV2{}, store.OpErrorV2Row{}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ft, st := reflect.TypeOf(c.frozen), reflect.TypeOf(c.store)
			for i := range ft.NumField() {
				f := ft.Field(i)
				sf, ok := st.FieldByName(f.Name)
				switch {
				case !ok:
					t.Errorf("the frozen copy reads %q, which %v has no field for", f.Name, st)
				case sf.Type != f.Type:
					t.Errorf("%q: the frozen copy holds a %v, the store's field is a %v", f.Name, f.Type, sf.Type)
				}
				if f.Tag.Get("json") != "" || sf.Tag.Get("json") != "" {
					t.Errorf("%q has a JSON tag: the key is no longer the Go name this test holds the copy to", f.Name)
				}
			}
		})
	}
}
