// file: internal/database/dedup_strict_read_test.go
// version: 1.0.0
// guid: 0f6b1d2a-7c43-4e5b-9a18-3d2e6c4b8f71
// last-edited: 2026-10-01

package database

import (
	"testing"

	"github.com/cockroachdb/pebble/v2"
)

// TestListLabeledExamplesStrict_FailsOnACorruptRow: the lenient listing skips
// an unreadable label; the strict one errors, so a not_dup verdict it cannot
// read is never taken for no verdict.
func TestListLabeledExamplesStrict_FailsOnACorruptRow(t *testing.T) {
	es := newTestLabelStore(t)
	if err := es.UpsertLabeledExample(LabeledExample{CandidateID: 1, EntityAID: "a", EntityBID: "b", Label: "not_dup"}); err != nil {
		t.Fatal(err)
	}
	if err := es.db.Set([]byte(dedupLabelPfx+"00000000000000ff"), []byte("{not json"), pebble.Sync); err != nil {
		t.Fatal(err)
	}
	got, err := es.ListLabeledExamples(LabeledExampleFilter{Label: "not_dup"})
	if err != nil || len(got) != 1 {
		t.Fatalf("lenient: got %d, err %v; want 1, nil", len(got), err)
	}
	if _, err := es.ListLabeledExamplesStrict(LabeledExampleFilter{Label: "not_dup"}); err == nil {
		t.Fatal("strict listing read past a corrupt label")
	}
}

// TestTerminalCandidatesStrict lists only terminal statuses of the entity
// type, and errors on an unreadable record.
func TestTerminalCandidatesStrict(t *testing.T) {
	es := newTestLabelStore(t)
	for _, c := range []DedupCandidate{
		{EntityType: "book", EntityAID: "a", EntityBID: "b", Layer: "exact", Status: "dismissed"},
		{EntityType: "book", EntityAID: "c", EntityBID: "d", Layer: "exact", Status: "merged"},
		{EntityType: "book", EntityAID: "e", EntityBID: "f", Layer: "exact", Status: "pending"},
		{EntityType: "author", EntityAID: "1", EntityBID: "2", Layer: "exact", Status: "dismissed"},
	} {
		if err := es.UpsertCandidate(c); err != nil {
			t.Fatal(err)
		}
	}
	got, err := es.TerminalCandidatesStrict("book")
	if err != nil {
		t.Fatal(err)
	}
	pairs := map[string]bool{}
	for _, c := range got {
		pairs[c.EntityAID+c.EntityBID] = true
	}
	if len(got) != 2 || !pairs["ab"] || !pairs["cd"] {
		t.Fatalf("got %+v; want the dismissed and merged book pairs", got)
	}
	if err := es.db.Set([]byte(dedupRecPfx+"00000000000fffff"), []byte("{not json"), pebble.Sync); err != nil {
		t.Fatal(err)
	}
	if _, err := es.TerminalCandidatesStrict("book"); err == nil {
		t.Fatal("strict scan read past a corrupt candidate")
	}
}
