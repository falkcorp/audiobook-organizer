// file: internal/plugins/maintenance/kinds_resources.go
// version: 1.1.0
// guid: 3f9b6d21-5a84-4e07-b1c3-7d2e08a46f59
// last-edited: 2026-11-16

package maintenance

import (
	"reflect"
	"slices"

	"github.com/falkcorp/audiobook-organizer/internal/maintenance/resources"
	"github.com/falkcorp/audiobook-organizer/pkg/ops"
)

// The method-to-resource table itself, with THE RULE and every traced row, is
// internal/maintenance/resources (Table); the jobs' kinds read the same rows.
// t6WriterResources is this package's copy of it, built once; every recording
// spy, every Effects test and F5-18's gated writers read a row of it by method
// name.
var t6WriterResources = resources.Table()

// t6RowsUnion is the sorted, de-duplicated union of the given rows, as a new
// non-nil slice (resources.Union).
func t6RowsUnion(rows ...[]ops.Resource) []ops.Resource {
	return resources.Union(rows...)
}

// t6WriterView is the rows of t6WriterResources for the methods of the given
// writer interfaces: a group's view of the shared table. rename sends a method
// name to the qualified row it means on these interfaces (SetRaw is a different
// key family per group); a method with no row is left out, so the group's
// coverage test, which compares the view's keys with the interfaces' methods,
// fails for it. The rows are the shared slices: a view never copies a row, so it
// cannot drift from the table.
//
// A capability getter, a no-argument method returning (interface, bool) such as
// UserStateSyncWrites, is not a write itself: it hands out the interface the
// writes go through. The view walks the returned interface's methods too (and
// skips the getter), so a write that only exists behind a capability is in the
// view and in every coverage test built from it.
func t6WriterView(rename map[string]string, writers ...reflect.Type) map[string][]ops.Resource {
	view := map[string][]ops.Resource{}
	for _, name := range t6WriterMethods(writers...) {
		key := name
		if q, ok := rename[name]; ok {
			key = q
		}
		if row, ok := t6WriterResources[key]; ok {
			view[name] = row
		}
	}
	return view
}

// t6WriterMethods is the sorted, de-duplicated names of every write method of the
// given writer interfaces, walking capability getters (see t6WriterView) and not
// listing the getters themselves. A coverage test compares it with a view's keys.
func t6WriterMethods(writers ...reflect.Type) []string {
	var names []string
	var walk func(w reflect.Type)
	walk = func(w reflect.Type) {
		for i := range w.NumMethod() {
			m := w.Method(i)
			if capIface, ok := t6CapabilityGetter(m); ok {
				walk(capIface)
				continue
			}
			names = append(names, m.Name)
		}
	}
	for _, w := range writers {
		walk(w)
	}
	slices.Sort(names)
	return slices.Compact(names)
}

// t6CapabilityGetter reports whether the interface method m is a capability
// getter (no arguments, results (interface, bool)) and returns the interface it
// hands out.
func t6CapabilityGetter(m reflect.Method) (reflect.Type, bool) {
	ft := m.Type
	if ft.NumIn() != 0 || ft.NumOut() != 2 || ft.Out(1).Kind() != reflect.Bool || ft.Out(0).Kind() != reflect.Interface {
		return nil, false
	}
	return ft.Out(0), true
}
