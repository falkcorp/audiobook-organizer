// file: internal/dedup/comma_split_corpus_test.go
// version: 1.0.0
// guid: 4e7a2c95-8d10-4f68-b3c7-1a5e9d2f6b84
// last-edited: 2026-08-14

package dedup

import "testing"

// TestSplitCompositeAuthorName_TitleClauseCorpus pins C414: the comma branch
// must not mint author rows from book-title clauses. The corpus is the three
// real rows this defect created, plus the credit-list shapes that MUST keep
// splitting (the leading-conjunction contract), plus the "Last, First" form
// that must stay unsplit.
func TestSplitCompositeAuthorName_TitleClauseCorpus(t *testing.T) {
	// Title clauses — the split must be REFUSED (nil or 1 part), leaving the
	// composite visibly broken rather than laundering a fragment into a name.
	refuse := []string{
		"So Long, and Thanks for All the Fish",            // minted row 46595
		"The Princess Bride, and the Farm Boy (DBY)",      // minted row 46989
		"Steal Like an Artist: Be Creative, and Make Better Decisions", // row 47193's shape (subtitle punctuation)
		"A Game of Thrones, A Clash of Kings, A Storm of Swords", // series list, 4+ word clauses
	}
	for _, name := range refuse {
		if got := SplitCompositeAuthorName(name); len(got) > 1 {
			t.Errorf("SplitCompositeAuthorName(%q) = %v, want refusal (title clause)", name, got)
		}
	}

	// Credit lists — MUST still split, including the trailing "and Name" the
	// normalizer strips.
	if got := SplitCompositeAuthorName("Paul McGann, India Fisher, and Conrad Westmaas"); len(got) != 3 {
		t.Errorf("credit list with 'and': got %v, want 3 parts", got)
	}
	if got := SplitCompositeAuthorName("Terry Pratchett, Neil Gaiman"); len(got) != 2 {
		t.Errorf("plain credit pair: got %v, want 2 parts", got)
	}
	if got := SplitCompositeAuthorName("James S. A. Corey, Daniel Abraham"); len(got) != 2 {
		t.Errorf("4-word name: got %v, want 2 parts", got)
	}

	// "Last, First" must stay unsplit (single-word second part).
	if got := SplitCompositeAuthorName("Sanderson, Brandon"); len(got) > 1 {
		t.Errorf("Last, First split into %v", got)
	}
}
