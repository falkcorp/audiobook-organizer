// file: internal/operations/registry/preview.go
// version: 1.0.0
// guid: 1d72f677-0951-40e4-9b15-cb9211c3e50a
// last-edited: 2026-10-18

package registry

import (
	"fmt"
	"reflect"
	"strings"
)

// PreviewKey names the params key an op reads to decide whether a run writes.
type PreviewKey string

// The keys ops preview with today.
const (
	// PreviewDryRun: dry_run=true writes nothing, dry_run=false writes. An
	// omitted dry_run is a preview (opmode.ResolveDryRun).
	PreviewDryRun PreviewKey = "dry_run"
	// PreviewApply: apply=false (or omitted) writes nothing, apply=true
	// writes.
	PreviewApply PreviewKey = "apply"
	// PreviewLive: live=false (or omitted) writes nothing, live=true writes.
	// The acoustid repair/backfill ops use it.
	PreviewLive PreviewKey = "live"
)

// WritesWhenTrue reports whether true is the writing value of the key: apply
// and live are, dry_run is not.
func (k PreviewKey) WritesWhenTrue() bool { return k == PreviewApply || k == PreviewLive }

// PreviewSpec declares how an op previews: the params key whose value makes a
// run write nothing, and the params type the op's Run decodes that key into.
//
// It is a declaration about the op's code, made next to that code. The zero
// value means the op has no preview a caller can ask for by params: it
// ignores every preview key, or its preview lives somewhere else (an HTTP handler
// that calls a different function), or it gates on a key that is neither.
// /api/v3/ops then requires an explicit mode "live" to start it and passes
// params through untouched.
//
// Params is a zero value of the struct the Run decodes into (for an op that
// reads only its mode with opmode.ParseDryRun, opmode.DryRunParams{}). Check
// verifies by reflection that the struct really has a bool or *bool field
// tagged exactly Key, so a declaration cannot name a key the op never
// decodes, and /api/v3/ops never writes a key a strict decoder
// (DisallowUnknownFields) would reject.
type PreviewSpec struct {
	Key    PreviewKey
	Params any
}

// IsZero reports whether no preview is declared.
func (p PreviewSpec) IsZero() bool { return p.Key == "" && p.Params == nil }

// Check returns why the declaration is unusable, nil when it is usable or
// when nothing is declared. A caller that gets an error must treat the op as
// having no preview: an unproven declaration is not a preview.
func (p PreviewSpec) Check() error {
	if p.IsZero() {
		return nil
	}
	switch p.Key {
	case PreviewDryRun, PreviewApply, PreviewLive:
	default:
		return fmt.Errorf("preview key %q is not %q, %q or %q", p.Key, PreviewDryRun, PreviewApply, PreviewLive)
	}
	if p.Params == nil {
		return fmt.Errorf("preview key %q declared without the params type the op decodes it into", p.Key)
	}
	t := reflect.TypeOf(p.Params)
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t.Kind() != reflect.Struct {
		return fmt.Errorf("preview params %s is not a struct", t)
	}
	ft, ok := jsonField(t, string(p.Key), 0)
	if !ok {
		return fmt.Errorf("preview params %s has no field tagged %q", t, p.Key)
	}
	for ft.Kind() == reflect.Pointer {
		ft = ft.Elem()
	}
	if ft.Kind() != reflect.Bool {
		return fmt.Errorf("preview params %s field %q is %s, not bool", t, p.Key, ft)
	}
	return nil
}

// HasJSONField reports whether the declared params type has a field whose JSON
// name is exactly name. False when nothing is declared.
func (p PreviewSpec) HasJSONField(name string) bool {
	if p.Params == nil {
		return false
	}
	t := reflect.TypeOf(p.Params)
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t.Kind() != reflect.Struct {
		return false
	}
	_, ok := jsonField(t, name, 0)
	return ok
}

// jsonField finds the field encoding/json would decode the key name into,
// matching the tag name exactly (not encoding/json's case-insensitive
// fallback: dryRun and dry_run are different keys to every op here). It
// follows embedded structs the way encoding/json flattens them.
func jsonField(t reflect.Type, name string, depth int) (reflect.Type, bool) {
	if depth > 8 {
		return nil, false
	}
	for i := range t.NumField() {
		f := t.Field(i)
		tag, hasTag := f.Tag.Lookup("json")
		tagName, _, _ := strings.Cut(tag, ",")
		if tagName == "-" {
			continue
		}
		if f.Anonymous && tagName == "" {
			et := f.Type
			for et.Kind() == reflect.Pointer {
				et = et.Elem()
			}
			if et.Kind() == reflect.Struct {
				if ft, ok := jsonField(et, name, depth+1); ok {
					return ft, true
				}
				continue
			}
		}
		if !f.IsExported() {
			continue
		}
		key := tagName
		if !hasTag || key == "" {
			key = f.Name
		}
		if key == name {
			return f.Type, true
		}
	}
	return nil, false
}
