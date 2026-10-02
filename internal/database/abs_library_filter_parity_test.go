// file: internal/database/abs_library_filter_parity_test.go
// version: 1.0.0
// guid: 55a97246-46b1-44df-9aa4-676e9268e930
// last-edited: 2026-10-01

package database

import (
	"fmt"
	"testing"
	"time"
)

// MatchesCore must answer exactly as Matches for the same row: maintenance
// scope builders page BookCore rows and the ABS surface reads Book rows, and a
// column the predicate reads that BookCore does not carry (or ToBook does not
// copy) would make the two disagree silently. Every combination of the
// columns the predicate reads, against ABSLibraryFilter and filters that
// exercise each other field.
func TestBookSummaryFilter_MatchesCoreParity(t *testing.T) {
	bools := []*bool{nil, new(false), new(true)}
	states := []*string{nil, strp(""), strp("organized"), strp("imported"), strp("deleted"), strp("Organized")}
	at := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	quarantine := []*time.Time{nil, &at}
	merged := []*string{nil, strp("survivor")}
	review := []*string{nil, strp("pending"), strp("approved")}
	filters := map[string]BookSummaryFilter{
		"abs":            ABSLibraryFilter(),
		"zero":           {},
		"non-primary":    {IsPrimaryVersion: new(false)},
		"trash only":     {MarkedForDeletion: new(true)},
		"live only":      {MarkedForDeletion: new(false)},
		"imported":       {LibraryState: "imported"},
		"review pending": {ReviewStatus: "pending"},
		"restricted":     {RestrictToIDs: map[string]struct{}{"b": {}}},
		// A Predicate reads any column; the merge pointer and the pre-trash
		// state are the ones the restore rule touches.
		"predicate": {Predicate: func(b *Book) bool {
			return b.MergedIntoBookID == nil && b.PreTrashLibraryState == nil
		}},
	}
	n := 0
	for fname, f := range filters {
		for _, primary := range bools {
			for _, del := range bools {
				for _, st := range states {
					for _, q := range quarantine {
						for _, r := range review {
							for _, m := range merged {
								b := &Book{ID: "b", Title: "T", IsPrimaryVersion: primary, MarkedForDeletion: del,
									LibraryState: st, QuarantinedAt: q, MetadataReviewStatus: r, MergedIntoBookID: m,
									PreTrashLibraryState: st}
								core := b.Core()
								if got, want := f.MatchesCore(&core), f.Matches(b); got != want {
									t.Errorf("%s: MatchesCore=%v Matches=%v for %s", fname, got, want, describe(b))
								}
								n++
							}
						}
					}
				}
			}
		}
	}
	if f := ABSLibraryFilter(); f.MatchesCore(nil) || f.Matches(nil) {
		t.Errorf("a nil row matched")
	}
	if n == 0 {
		t.Fatal("no combinations checked")
	}
}

func describe(b *Book) string {
	d := func(p any) string {
		switch v := p.(type) {
		case *bool:
			if v == nil {
				return "nil"
			}
			return fmt.Sprint(*v)
		case *string:
			if v == nil {
				return "nil"
			}
			return fmt.Sprintf("%q", *v)
		}
		return "?"
	}
	return fmt.Sprintf("primary=%s deleted=%s state=%s quarantined=%v review=%s merged=%s",
		d(b.IsPrimaryVersion), d(b.MarkedForDeletion), d(b.LibraryState), b.QuarantinedAt != nil, d(b.MetadataReviewStatus), d(b.MergedIntoBookID))
}
