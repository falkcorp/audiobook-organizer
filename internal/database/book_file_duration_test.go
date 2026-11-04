// file: internal/database/book_file_duration_test.go
// version: 1.0.1
// guid: ee8a4990-c4f8-4bcd-89f1-80ce6d3ca3ca
// last-edited: 2026-11-04

package database

import (
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/testutil/durtest"
)

// TestDurationAccessorsAgreeAcrossBookFileAndCore pins the two spellings of the
// duration accessors together so they cannot drift apart.
func TestDurationAccessorsAgreeAcrossBookFileAndCore(t *testing.T) {
	for _, d := range []int{-5, 0, 1, 27000} {
		f := BookFile{Duration: Secs(d)}
		c := BookFileCore{Duration: Secs(d)}
		fs, fk := f.DurationSec()
		cs, ck := c.DurationSec()
		if fs != cs || fk != ck {
			t.Errorf("d=%d: BookFile (%d,%v) != Core (%d,%v)", d, fs, fk, cs, ck)
		}
		if fk != (d > 0) {
			t.Errorf("d=%d: known=%v want %v", d, fk, d > 0)
		}
		if f.DurationState() != c.DurationState() {
			t.Errorf("d=%d: state %v != %v", d, f.DurationState(), c.DurationState())
		}
		want := DurationUnprobed
		if d > 0 {
			want = DurationKnown
		}
		if f.DurationState() != want {
			t.Errorf("d=%d: state %v want %v", d, f.DurationState(), want)
		}
	}
}

func TestSetMeasuredDurationAndResetAgree(t *testing.T) {
	var f BookFile
	var c BookFileCore
	for _, bad := range []int{0, -1} {
		if f.SetMeasuredDuration(bad) == nil || c.SetMeasuredDuration(bad) == nil {
			t.Fatalf("sec=%d must be rejected", bad)
		}
		if f.Duration != nil || c.Duration != nil {
			t.Fatalf("rejected set must not write")
		}
	}
	if err := f.SetMeasuredDuration(90); err != nil {
		t.Fatal(err)
	}
	if err := c.SetMeasuredDuration(90); err != nil {
		t.Fatal(err)
	}
	if durtest.Sec(f.Duration) != 90 || durtest.Sec(c.Duration) != 90 || f.DurationState() != DurationKnown {
		t.Fatalf("set failed: %d %d", durtest.Sec(f.Duration), durtest.Sec(c.Duration))
	}
	f.ResetDuration()
	c.ResetDuration()
	if _, ok := f.DurationSec(); ok || f.DurationState() != DurationUnprobed {
		t.Fatal("BookFile reset failed")
	}
	if _, ok := c.DurationSec(); ok || c.DurationState() != DurationUnprobed {
		t.Fatal("Core reset failed")
	}
}
