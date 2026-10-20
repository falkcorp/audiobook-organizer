// file: internal/database/iface_ops_v2_test.go
// version: 1.4.1
// guid: 9b41c7de-25a8-4f6c-b0e3-71d84af52c19
// last-edited: 2026-10-19

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
	typ := reflect.TypeFor[OpV2LifecycleStore]()
	var got []string
	for method := range typ.Methods() {
		got = append(got, method.Name)
	}
	sort.Strings(got)
	want := []string{
		// DeleteOperationV2 added 2026-09-10 for the Activity page's Discard
		// button (Registry.Discard); the fakes were widened deliberately.
		"DeleteOperationV2",
		"GetOperationV2",
		"IncrementResumeCountV2",
		"InsertOperationV2",
		// MarkOperationV2ManualRetry added 2026-09-12 for the same-row Retry of
		// an interrupted op (Registry.RetryInterrupted): it moves the
		// restart-strike baseline so a manual retry is not force-dropped as a
		// boot loop. The fakes were widened deliberately.
		"MarkOperationV2ManualRetry",
		"RepairOpsV2MissingCompletedAt",
		"ResetOperationV2ForResume",
		"SetOperationV2Result",
		"UpdateOperationV2Params",
		"UpdateOperationV2Status",
		// UpdateOperationV2StatusIf added 2026-10-10: Registry.Cancel and the
		// worker's scan stand-down pickup gate write a status decided from an
		// earlier read, so they need a compare-and-set that carries a message
		// and completed_at. It REPLACED SetOperationV2StatusIfQueued (now
		// UpdateOperationV2StatusIf(id, "queued", s, nil, nil, nil)), so
		// database.Store's flattened width did not grow.
		"UpdateOperationV2StatusIf",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("method set drifted\n got: %v\nwant: %v", got, want)
	}
}
