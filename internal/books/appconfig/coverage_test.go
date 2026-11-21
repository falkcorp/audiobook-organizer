// file: internal/books/appconfig/coverage_test.go
// version: 1.0.0
// guid: 4b9e2d70-6a15-4c83-8e1f-0d3a7c5b2e96
// last-edited: 2026-11-19

package appconfig

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/books/cutover"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	coreconfig "github.com/falkcorp/mediacore/config"
)

// leafPaths are the dotted names of every leaf field of v that the Engine's field
// walk names, descending structs only (a slice, map or scalar is a leaf).
func leafPaths(t reflect.Type, prefix []string, out *[][]string) {
	for i := range t.NumField() {
		name := fieldName(t.Field(i))
		if name == "" {
			continue
		}
		p := append(append([]string(nil), prefix...), name)
		if ft := t.Field(i).Type; ft.Kind() == reflect.Struct && ft.PkgPath() != "time" {
			leafPaths(ft, p, out)
			continue
		}
		*out = append(*out, p)
	}
}

// poke makes the leaf at segs differ from its zero value.
func poke(cfg *config.Config, segs []string) bool {
	f, ok := fieldAt(cfg, segs)
	if !ok || !f.CanSet() {
		return false
	}
	switch f.Kind() {
	case reflect.String:
		f.SetString("batman")
	case reflect.Bool:
		f.SetBool(true)
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		f.SetInt(7)
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		f.SetUint(7)
	case reflect.Float32, reflect.Float64:
		f.SetFloat(7.5)
	case reflect.Slice:
		f.Set(reflect.MakeSlice(f.Type(), 1, 1))
	case reflect.Map:
		f.Set(reflect.MakeMap(f.Type()))
		f.SetMapIndex(reflect.New(f.Type().Key()).Elem(), reflect.New(f.Type().Elem()).Elem())
	default:
		return false
	}
	return true
}

// TestEngine_EveryConfigFieldHasARow: a Mutate that changes any one field of HEAD's
// Config is stored or refused for a stated reason, never refused as "no row covers
// this field". Mutate now persists as the process's own actor, and one uncovered
// field refuses the WHOLE call, so a caller that touches one (a runtime flag, an
// auto-detected setting) would otherwise lose every other change in it.
func TestEngine_EveryConfigFieldHasARow(t *testing.T) {
	// With the residual: the class-i fields (sources, endpoints, plugins) are rows
	// only on an Engine that has somewhere to keep them, as the books module's does.
	e, _, _ := aiEngine(t, "")
	var paths [][]string
	leafPaths(reflect.TypeOf(config.Config{}), nil, &paths)
	if len(paths) < 100 {
		t.Fatalf("walked only %d fields: the walk is broken", len(paths))
	}
	var missing []string
	for _, segs := range paths {
		// A field the table retires (class x) has no key by decision and nothing
		// writes it; one with a row stored in env/file only is not a Mutate target.
		head := strings.Join(segs, ".")
		if r, ok := cutover.Table().Row(head); ok && (r.Class == cutover.ClassDropped || r.Stored == cutover.StoredEnvFile) {
			continue
		}
		if top, ok := cutover.Table().Row(segs[0]); ok && top.Class == cutover.ClassDropped {
			continue
		}
		before := e.Snapshot()
		after := before.Clone()
		if !poke(after, segs) {
			continue
		}
		if _, err := e.diff(before, *after); err != nil && errors.Is(err, coreconfig.ErrUnknownKey) {
			missing = append(missing, strings.Join(segs, "."))
		}
	}
	if len(missing) > 0 {
		t.Fatalf("%d Config fields have no rename-table row, so a Mutate that changes one is refused whole:\n  %s",
			len(missing), strings.Join(missing, "\n  "))
	}
}

// TestEngine_ParseBatchKnobsRoundTrip: HEAD's ai_backend.parse_batch_* are live
// scanner knobs that no ai_endpoints fold carries, so they are keys of their own;
// a value set through Mutate is stored and projected back.
func TestEngine_ParseBatchKnobsRoundTrip(t *testing.T) {
	e, f, _ := aiEngine(t, "")
	if err := e.Mutate(func(c *config.Config) {
		c.AIBackend.ParseBatchSize = 40
		c.AIBackend.ParseBatchTimeoutSeconds = 90
		c.AIBackend.ParseBatchWorkers = 2
	}); err != nil {
		t.Fatal(err)
	}
	got := e.Snapshot().AIBackend
	if got.ParseBatchSize != 40 || got.ParseBatchTimeoutSeconds != 90 || got.ParseBatchWorkers != 2 {
		t.Fatalf("projection lost the batch knobs: %+v", got)
	}
	for _, k := range []string{"modules.books.ai.parse_batch_size", "modules.books.ai.parse_batch_timeout_seconds", "modules.books.ai.parse_batch_workers"} {
		found := false
		for _, a := range f.applied {
			if _, ok := a.Set[coreconfig.Path(k)]; ok {
				found = true
			}
		}
		if !found {
			t.Errorf("%s was not applied to the core store", k)
		}
	}
}
