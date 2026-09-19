// file: internal/dedup/split_book_fill_empty.go
// version: 1.0.0
// guid: 668a8c6b-8d6c-4b88-a86b-aa3127cfc705
// last-edited: 2026-09-19

package dedup

import (
	"fmt"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/merge"
)

// FillEmptyPlan is what a merge would copy onto the keep: for each of ASIN,
// narrator, series and author, the value the sources agree on, but only where
// the keep's own field is empty. Conflicts names fields the keep lacks on
// which two sources disagree; a merge with any conflict is refused, because
// picking one would be a guess and dropping both would lose data at the purge.
type FillEmptyPlan struct {
	Values    merge.CombineFillEmpty
	Fields    []string
	Conflicts []string
}

// fillEmptyStore is what PlanFillEmpty reads.
type fillEmptyStore interface {
	GetBookAuthors(bookID string) ([]database.BookAuthor, error)
}

// PlanFillEmpty computes the fill-empty plan. It writes nothing; the chapter
// preview calls it to report fills and conflicts, and the merge calls it again
// before its first write.
func PlanFillEmpty(store fillEmptyStore, keep *database.Book, srcs []*database.Book) (FillEmptyPlan, error) {
	var plan FillEmptyPlan
	strEmpty := func(p *string) bool { return p == nil || *p == "" }
	pickStr := func(field string, keepVal *string, get func(*database.Book) *string) *string {
		if !strEmpty(keepVal) {
			return nil
		}
		var val *string
		for _, s := range srcs {
			v := get(s)
			if strEmpty(v) {
				continue
			}
			if val != nil && *val != *v {
				plan.Conflicts = append(plan.Conflicts, fmt.Sprintf("%s: sources disagree (%q vs %q)", field, *val, *v))
				return nil
			}
			val = v
		}
		if val != nil {
			plan.Fields = append(plan.Fields, field)
			c := *val
			return &c
		}
		return nil
	}
	plan.Values.ASIN = pickStr("asin", keep.ASIN, func(b *database.Book) *string { return b.ASIN })
	plan.Values.Narrator = pickStr("narrator", keep.Narrator, func(b *database.Book) *string { return b.Narrator })

	if keep.SeriesID == nil {
		var from *database.Book
		for _, s := range srcs {
			if s.SeriesID == nil {
				continue
			}
			if from != nil && (*from.SeriesID != *s.SeriesID || !sameIntPtr(from.SeriesSequence, s.SeriesSequence)) {
				plan.Conflicts = append(plan.Conflicts, "series: sources disagree")
				from = nil
				break
			}
			from = s
		}
		if from != nil {
			id := *from.SeriesID
			plan.Values.SeriesID = &id
			if from.SeriesSequence != nil && keep.SeriesSequence == nil {
				seq := *from.SeriesSequence
				plan.Values.SeriesSequence = &seq
			}
			plan.Fields = append(plan.Fields, "series")
		}
	}

	if keep.AuthorID == nil {
		keepAuthors, err := store.GetBookAuthors(keep.ID)
		if err != nil {
			return plan, fmt.Errorf("read keep authors: %w", err)
		}
		if len(keepAuthors) == 0 {
			var from *database.Book
			for _, s := range srcs {
				if s.AuthorID == nil {
					continue
				}
				if from != nil && *from.AuthorID != *s.AuthorID {
					plan.Conflicts = append(plan.Conflicts, "author: sources disagree")
					from = nil
					break
				}
				from = s
			}
			if from != nil {
				authors, err := store.GetBookAuthors(from.ID)
				if err != nil {
					return plan, fmt.Errorf("read source authors: %w", err)
				}
				id := *from.AuthorID
				plan.Values.AuthorID = &id
				for _, a := range authors {
					a.BookID = keep.ID
					plan.Values.Authors = append(plan.Values.Authors, a)
				}
				plan.Fields = append(plan.Fields, "author")
			}
		}
	}
	return plan, nil
}

func sameIntPtr(a, b *int) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

// applyFillEmpty writes plan.Values onto the keep through ModifyBook, only into
// fields that are STILL empty and not user-locked, and returns what it actually
// wrote (for the journal). An unreadable lock set fills nothing (fail closed).
func applyFillEmpty(store Store, keepID string, plan FillEmptyPlan) (*merge.CombineFillEmpty, error) {
	if plan.Values.Empty() {
		return nil, nil
	}
	locks, err := database.LoadFieldLocks(store, keepID)
	if err != nil {
		return nil, fmt.Errorf("read field locks: %w", err)
	}
	v := plan.Values
	filled := &merge.CombineFillEmpty{}
	_, err = store.ModifyBook(keepID, func(b *database.Book) error {
		*filled = merge.CombineFillEmpty{}
		if v.ASIN != nil && (b.ASIN == nil || *b.ASIN == "") && !locks.Locked(database.FieldKeyASIN) {
			b.ASIN, filled.ASIN = v.ASIN, v.ASIN
		}
		if v.Narrator != nil && (b.Narrator == nil || *b.Narrator == "") && !locks.Locked(database.FieldKeyNarrator) {
			b.Narrator, filled.Narrator = v.Narrator, v.Narrator
		}
		if v.SeriesID != nil && b.SeriesID == nil && !locks.Locked(database.FieldKeySeriesName) {
			b.SeriesID, filled.SeriesID = v.SeriesID, v.SeriesID
			if v.SeriesSequence != nil && b.SeriesSequence == nil && !locks.Locked(database.FieldKeySeriesPosition) {
				b.SeriesSequence, filled.SeriesSequence = v.SeriesSequence, v.SeriesSequence
			}
		}
		if v.AuthorID != nil && b.AuthorID == nil && !locks.Locked(database.FieldKeyAuthorName) {
			b.AuthorID, filled.AuthorID = v.AuthorID, v.AuthorID
		}
		if filled.Empty() {
			return database.ErrSkipBookWrite
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if filled.AuthorID != nil && len(v.Authors) > 0 {
		if err := store.SetBookAuthors(keepID, v.Authors); err != nil {
			return filled, fmt.Errorf("set keep authors: %w", err)
		}
		filled.Authors = v.Authors
	}
	if filled.Empty() {
		return nil, nil
	}
	return filled, nil
}
