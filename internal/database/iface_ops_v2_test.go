// file: internal/database/iface_ops_v2_test.go
// version: 1.1.0
// guid: 9b41c7de-25a8-4f6c-b0e3-71d84af52c19
// last-edited: 2026-09-10

package database

import (
	"reflect"
	"sort"
	"testing"
)

// TestOpV2LifecycleStore_MethodSetUnchanged pins the exact expanded method set
// of OpV2LifecycleStore across the composition split. The split exists only to
// satisfy interfacebloat's 8-entry cap on a single declaration; if it ever
// changes what the interface REQUIRES, every fake in the repo shifts silently.
func TestOpV2LifecycleStore_MethodSetUnchanged(t *testing.T) {
	typ := reflect.TypeOf((*OpV2LifecycleStore)(nil)).Elem()
	var got []string
	for i := 0; i < typ.NumMethod(); i++ {
		got = append(got, typ.Method(i).Name)
	}
	sort.Strings(got)
	want := []string{
		// DeleteOperationV2 added 2026-09-10 for the Activity page's Discard
		// button (Registry.Discard); the fakes were widened deliberately.
		"DeleteOperationV2",
		"GetOperationV2",
		"IncrementResumeCountV2",
		"InsertOperationV2",
		"RepairOpsV2MissingCompletedAt",
		"ResetOperationV2ForResume",
		"SetOperationV2Result",
		"SetOperationV2StatusIfQueued",
		"UpdateOperationV2Params",
		"UpdateOperationV2Status",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("method set drifted\n got: %v\nwant: %v", got, want)
	}
}
