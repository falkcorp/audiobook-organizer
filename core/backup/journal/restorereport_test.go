// file: core/backup/journal/restorereport_test.go
// version: 0.1.0
// guid: 3b8e0f64-d1a7-4c52-9e83-70a5c2f6b149
// last-edited: 2026-11-21

package journal

import (
	"slices"
	"testing"
	"time"
)

// TestRestoreReport_RoundTripsAndEmptyDeletes: the report a hook run recorded is
// read back; a later run with nothing to report replaces it; a record that does
// not decode is an error, not an empty report.
func TestRestoreReport_RoundTripsAndEmptyDeletes(t *testing.T) {
	fx := newFixture(t)
	ks := fx.ks
	ctx := t.Context()
	if got, err := ReadRestoreReport(ctx, ks); err != nil || !got.Empty() {
		t.Fatalf("nothing recorded: %+v, %v", got, err)
	}
	want := RestoreReport{Kept: []string{"u-oracle"}, Review: []string{"u-nightwing", "u-robin"}}
	if err := PutRestoreReport(ctx, ks, want, time.Now()); err != nil {
		t.Fatal(err)
	}
	got, err := ReadRestoreReport(ctx, ks)
	if err != nil || !slices.Equal(got.Kept, want.Kept) || !slices.Equal(got.Review, want.Review) {
		t.Fatalf("read %+v, %v, want %+v", got, err, want)
	}
	if err := PutRestoreReport(ctx, ks, RestoreReport{}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if got, err := ReadRestoreReport(ctx, ks); err != nil || !got.Empty() {
		t.Fatalf("after an empty report: %+v, %v", got, err)
	}
	if err := ks.Set(ctx, []byte(restoreReportKey), []byte("not json")); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadRestoreReport(ctx, ks); err == nil {
		t.Fatal("a record that does not decode read as an empty report")
	}
}
