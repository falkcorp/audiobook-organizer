// file: internal/seriesdenumber/decide_test.go
// version: 1.1.0
// guid: 9f1c3e58-27a4-4b60-8d93-e6a05b72c149
// last-edited: 2026-11-16

package seriesdenumber

import (
	"slices"
	"testing"
)

func ip(n int) *int { return &n }

func movedIDs(r SourceResult) []string {
	var ids []string
	for _, m := range r.Moves {
		ids = append(ids, m.BookID)
	}
	slices.Sort(ids)
	return ids
}

func heldIDs(r SourceResult) []string {
	var ids []string
	for _, h := range r.Held {
		ids = append(ids, h.BookID)
	}
	slices.Sort(ids)
	return ids
}

func TestDecide_PlainMoveFillsMissingPosition(t *testing.T) {
	res := Decide(Group{
		TargetID: 90, BaseName: "Knightfall", AuthorID: 3,
		Sources: []Source{{FromID: 41, FromName: "Knightfall 05", Position: 5, Books: []Book{{ID: "b1"}}}},
	})
	if got := res.Sources[0].Moves; len(got) != 1 || got[0] != (Move{BookID: "b1", FromSeriesID: 41, NewSeq: 5, SetSeq: true}) {
		t.Fatalf("moves = %+v", got)
	}
	if res.CreateTarget {
		t.Fatalf("CreateTarget true for an existing target")
	}
}

// B: a base series is created only when a book will actually move into it.
func TestDecide_CreateTargetOnlyWhenABookMoves(t *testing.T) {
	cases := []struct {
		name string
		g    Group
		want bool
	}{
		{"no books at all: nothing is created", Group{BaseName: "Hush", Sources: []Source{{FromID: 1, Position: 1}}}, false},
		{"a source with a book creates the base", Group{BaseName: "Hush", Sources: []Source{{FromID: 1, Position: 1, Books: []Book{{ID: "b1"}}}}}, true},
		{"every book held: nothing is created", Group{BaseName: "Hush", Sources: []Source{{FromID: 1, Position: 1, Books: []Book{{ID: "b1", Seq: ip(9)}}}}}, false},
		{"existing target is never 'created'", Group{TargetID: 7, BaseName: "Hush", Sources: []Source{{FromID: 1, Position: 1, Books: []Book{{ID: "b1"}}}}}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Decide(tc.g).CreateTarget; got != tc.want {
				t.Fatalf("CreateTarget = %v, want %v", got, tc.want)
			}
		})
	}
}

// C: a position the book already holds that differs is never overwritten.
func TestDecide_OwnPositionDifferingIsHeld(t *testing.T) {
	res := Decide(Group{
		TargetID: 90, BaseName: "Knightfall", AuthorID: 3,
		Sources: []Source{{FromID: 41, Position: 5, Books: []Book{{ID: "b1", Seq: ip(2)}, {ID: "b2", Seq: ip(5)}}}},
	})
	r := res.Sources[0]
	if !slices.Equal(movedIDs(r), []string{"b2"}) {
		t.Fatalf("moved = %v, want only b2 (already at 5)", movedIDs(r))
	}
	if !slices.Equal(heldIDs(r), []string{"b1"}) || r.Held[0].Code != HoldOwnPosition || r.Held[0].Reason == "" {
		t.Fatalf("held = %+v, want b1 held as own_position_differs with a reason", r.Held)
	}
	for _, m := range r.Moves {
		if m.BookID == "b2" && m.SetSeq {
			t.Errorf("b2 already holds 5; the move must not write a position")
		}
	}
}

// C: several books of one numbered series would land on one position.
func TestDecide_SeveralMoversAtOnePositionAreAllHeld(t *testing.T) {
	res := Decide(Group{
		TargetID: 90, BaseName: "Court of Owls", AuthorID: 3,
		Sources: []Source{{FromID: 41, Position: 2, Books: []Book{{ID: "b1"}, {ID: "b2"}}}},
	})
	r := res.Sources[0]
	if len(r.Moves) != 0 || !slices.Equal(heldIDs(r), []string{"b1", "b2"}) {
		t.Fatalf("moves=%+v held=%+v, want both held", r.Moves, r.Held)
	}
	if r.Held[0].Code != HoldCollision {
		t.Fatalf("code = %s, want %s", r.Held[0].Code, HoldCollision)
	}
	if res.CreateTarget {
		t.Fatalf("CreateTarget true although nothing moves")
	}
}

// C: several plans of the set land on one position of one base.
func TestDecide_MoversFromDifferentSourcesAtOnePosition(t *testing.T) {
	res := Decide(Group{
		TargetID: 90, BaseName: "Court of Owls", AuthorID: 3,
		Sources: []Source{
			{FromID: 41, Position: 7, Books: []Book{{ID: "b1"}}},
			{FromID: 42, Position: 7, Books: []Book{{ID: "b2"}}},
			{FromID: 43, Position: 8, Books: []Book{{ID: "b3"}}},
		},
	})
	if len(res.Sources[0].Moves) != 0 || len(res.Sources[1].Moves) != 0 {
		t.Fatalf("both position-7 movers must be held: %+v %+v", res.Sources[0], res.Sources[1])
	}
	if !slices.Equal(movedIDs(res.Sources[2]), []string{"b3"}) {
		t.Fatalf("the uncontested position 8 must still move: %+v", res.Sources[2])
	}
}

// C: a book already at that position of the target.
func TestDecide_ExistingBookAtPositionHoldsTheMover(t *testing.T) {
	res := Decide(Group{
		TargetID: 90, BaseName: "Court of Owls", AuthorID: 3,
		Existing: []Book{{ID: "e1", Seq: ip(4)}, {ID: "e2", Seq: ip(9)}, {ID: "e3"}},
		Sources: []Source{
			{FromID: 41, Position: 4, Books: []Book{{ID: "b1"}}},
			{FromID: 42, Position: 5, Books: []Book{{ID: "b2"}}},
		},
	})
	if len(res.Sources[0].Moves) != 0 || len(res.Sources[0].Held) != 1 || res.Sources[0].Held[0].Code != HoldCollision {
		t.Fatalf("mover onto occupied position 4 must be held: %+v", res.Sources[0])
	}
	if !slices.Equal(movedIDs(res.Sources[1]), []string{"b2"}) {
		t.Fatalf("mover onto free position 5 must move: %+v", res.Sources[1])
	}
}

// A mover that already holds the position it is going to still collides with
// a book already there.
func TestDecide_PreNumberedMoverCollidesWithExisting(t *testing.T) {
	res := Decide(Group{
		TargetID: 90, BaseName: "Court of Owls", AuthorID: 3,
		Existing: []Book{{ID: "e1", Seq: ip(4)}},
		Sources:  []Source{{FromID: 41, Position: 4, Books: []Book{{ID: "b1", Seq: ip(4)}}}},
	})
	if len(res.Sources[0].Moves) != 0 {
		t.Fatalf("moves = %+v, want none", res.Sources[0].Moves)
	}
}

// Versions of one book hold ONE position and move together; they must not read
// as a collision.
func TestDecide_VersionsOfOneBookMoveTogether(t *testing.T) {
	res := Decide(Group{
		TargetID: 90, BaseName: "Court of Owls", AuthorID: 3,
		Existing: []Book{{ID: "e1", Seq: ip(9), Identity: "vg-e"}, {ID: "e1-alt", Seq: ip(9), Identity: "vg-e"}},
		Sources: []Source{{FromID: 41, Position: 5, Books: []Book{
			{ID: "b1", Identity: "vg-b"}, {ID: "b1-alt", Identity: "vg-b"},
		}}},
	})
	if !slices.Equal(movedIDs(res.Sources[0]), []string{"b1", "b1-alt"}) {
		t.Fatalf("both versions must move: %+v", res.Sources[0])
	}
}

// If any version fails a check, all versions are held: a book is never split
// across the numbered series and the base.
func TestDecide_VersionHeldHoldsTheWholeBook(t *testing.T) {
	res := Decide(Group{
		TargetID: 90, BaseName: "Court of Owls", AuthorID: 3,
		Sources: []Source{{FromID: 41, Position: 5, Books: []Book{
			{ID: "b1", Identity: "vg-b"}, {ID: "b1-alt", Seq: ip(2), Identity: "vg-b"},
		}}},
	})
	if len(res.Sources[0].Moves) != 0 || !slices.Equal(heldIDs(res.Sources[0]), []string{"b1", "b1-alt"}) {
		t.Fatalf("whole book must be held: %+v", res.Sources[0])
	}
}

// The numbered series that already is the target is skipped.
func TestDecide_SourceThatIsTheTargetIsSkipped(t *testing.T) {
	res := Decide(Group{
		TargetID: 41, BaseName: "Court of Owls", AuthorID: 3,
		Sources: []Source{{FromID: 41, Position: 5, Books: []Book{{ID: "b1"}}}},
	})
	if len(res.Sources[0].Moves) != 0 || len(res.Sources[0].Held) != 0 {
		t.Fatalf("got %+v, want nothing", res.Sources[0])
	}
}

func TestDecide_ResultsFollowSourceOrder(t *testing.T) {
	res := Decide(Group{
		TargetID: 90, BaseName: "Hush", AuthorID: 3,
		Sources: []Source{
			{FromID: 41, Position: 1, Books: []Book{{ID: "a"}}},
			{FromID: 42, Position: 2, Books: []Book{{ID: "b"}}},
		},
	})
	if res.Sources[0].FromID != 41 || res.Sources[1].FromID != 42 {
		t.Fatalf("sources out of order: %+v", res.Sources)
	}
	if res.MovedBooks() != 2 {
		t.Fatalf("MovedBooks = %d, want 2", res.MovedBooks())
	}
}

// A base that is itself a numbered series folding elsewhere this run takes no
// books: they would be stranded when it is merged away.
func TestDecide_ChainedTargetHoldsEverything(t *testing.T) {
	res := Decide(Group{
		TargetID: 2, BaseName: "Alpha 05", TargetIsSource: true,
		Sources: []Source{{FromID: 3, Position: 7, Books: []Book{{ID: "y1"}}}},
	})
	r := res.Sources[0]
	if len(r.Moves) != 0 || len(r.Held) != 1 || r.Held[0].Code != HoldChainedTarget || r.Held[0].Reason == "" {
		t.Fatalf("got %+v, want y1 held as %s with a reason", r, HoldChainedTarget)
	}
	if res.CreateTarget {
		t.Fatalf("CreateTarget true although nothing moves")
	}
}
