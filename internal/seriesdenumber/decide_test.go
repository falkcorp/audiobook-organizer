// file: internal/seriesdenumber/decide_test.go
// version: 2.1.0
// guid: 9f1c3e58-27a4-4b60-8d93-e6a05b72c149
// last-edited: 2026-11-17

package seriesdenumber

import (
	"slices"
	"strings"
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
		Sources: []Source{{SiblingRun: true, FromID: 41, FromName: "Knightfall 05", Position: 5, Books: []Book{{ID: "b1"}}}},
	})
	if got := res.Sources[0].Moves; len(got) != 1 || got[0] != (Move{BookID: "b1", FromSeriesID: 41, Identity: "b:b1", NewSeq: 5, SetSeq: true}) {
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
		{"no books at all: nothing is created", Group{BaseName: "Hush", Sources: []Source{{SiblingRun: true, FromID: 1, Position: 1}}}, false},
		{"a source with a book creates the base", Group{BaseName: "Hush", Sources: []Source{{SiblingRun: true, FromID: 1, Position: 1, Books: []Book{{ID: "b1"}}}}}, true},
		{"every book held: nothing is created", Group{BaseName: "Hush", Sources: []Source{{SiblingRun: true, FromID: 1, Position: 1, Books: []Book{{ID: "b1", Seq: ip(9)}}}}}, false},
		{"existing target is never 'created'", Group{TargetID: 7, BaseName: "Hush", Sources: []Source{{SiblingRun: true, FromID: 1, Position: 1, Books: []Book{{ID: "b1"}}}}}, false},
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
		Sources: []Source{{SiblingRun: true, FromID: 41, Position: 5, Books: []Book{{ID: "b1", Seq: ip(2)}, {ID: "b2", Seq: ip(5)}}}},
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
		Sources: []Source{{SiblingRun: true, FromID: 41, Position: 2, Books: []Book{{ID: "b1"}, {ID: "b2"}}}},
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
			{SiblingRun: true, FromID: 41, Position: 7, Books: []Book{{ID: "b1"}}},
			{SiblingRun: true, FromID: 42, Position: 7, Books: []Book{{ID: "b2"}}},
			{SiblingRun: true, FromID: 43, Position: 8, Books: []Book{{ID: "b3"}}},
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
			{SiblingRun: true, FromID: 41, Position: 4, Books: []Book{{ID: "b1"}}},
			{SiblingRun: true, FromID: 42, Position: 5, Books: []Book{{ID: "b2"}}},
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
		Sources:  []Source{{SiblingRun: true, FromID: 41, Position: 4, Books: []Book{{ID: "b1", Seq: ip(4)}}}},
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
		Sources: []Source{{SiblingRun: true, FromID: 41, Position: 5, Books: []Book{
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
		Sources: []Source{{SiblingRun: true, FromID: 41, Position: 5, Books: []Book{
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
		Sources: []Source{{SiblingRun: true, FromID: 41, Position: 5, Books: []Book{{ID: "b1"}}}},
	})
	if len(res.Sources[0].Moves) != 0 || len(res.Sources[0].Held) != 0 {
		t.Fatalf("got %+v, want nothing", res.Sources[0])
	}
}

func TestDecide_ResultsFollowSourceOrder(t *testing.T) {
	res := Decide(Group{
		TargetID: 90, BaseName: "Hush", AuthorID: 3,
		Sources: []Source{
			{SiblingRun: true, FromID: 41, Position: 1, Books: []Book{{ID: "a"}}},
			{SiblingRun: true, FromID: 42, Position: 2, Books: []Book{{ID: "b"}}},
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
		Sources: []Source{{SiblingRun: true, FromID: 3, Position: 7, Books: []Book{{ID: "y1"}}}},
	})
	r := res.Sources[0]
	if len(r.Moves) != 0 || len(r.Held) != 1 || r.Held[0].Code != HoldChainedTarget || r.Held[0].Reason == "" {
		t.Fatalf("got %+v, want y1 held as %s with a reason", r, HoldChainedTarget)
	}
	if res.CreateTarget {
		t.Fatalf("CreateTarget true although nothing moves")
	}
}

// A book with no position of its own is moved only when the siblings corroborate
// the name's number. The lone series is HELD, with its reason, and nothing is
// created for it.
func TestDecide_LoneUncorroboratedSeriesIsHeldWithItsReason(t *testing.T) {
	res := Decide(Group{
		BaseName: "Court of Owls", AuthorID: 3,
		Sources: []Source{{FromID: 41, FromName: "Court of Owls 05", Position: 5, Books: []Book{{ID: "b1"}}}},
	})
	r := res.Sources[0]
	if len(r.Moves) != 0 || res.CreateTarget {
		t.Fatalf("moves = %+v createTarget = %v, want nothing", r.Moves, res.CreateTarget)
	}
	if len(r.Held) != 1 || r.Held[0].Code != HoldUncorroborated || r.Held[0].Reason == "" {
		t.Fatalf("held = %+v, want b1 held as %s with a reason", r.Held, HoldUncorroborated)
	}
}

// The book's own stored position corroborates the name on its own.
func TestDecide_OwnPositionEqualToTheNameCorroborates(t *testing.T) {
	res := Decide(Group{
		BaseName: "Court of Owls", AuthorID: 3,
		Sources: []Source{{FromID: 41, Position: 5, Books: []Book{{ID: "b1", Seq: ip(5)}}}},
	})
	r := res.Sources[0]
	if len(r.Moves) != 1 || r.Moves[0].SetSeq || len(r.Held) != 0 {
		t.Fatalf("moves %+v held %+v, want b1 moved with its position kept", r.Moves, r.Held)
	}
}

// One version at the name's position corroborates the whole identity group.
func TestDecide_OneVersionAtTheNamesPositionCorroboratesTheGroup(t *testing.T) {
	res := Decide(Group{
		BaseName: "Court of Owls", AuthorID: 3,
		Sources: []Source{{FromID: 41, Position: 5, Books: []Book{
			{ID: "primary", Identity: "g1", Seq: ip(5)}, {ID: "rip", Identity: "g1"},
		}}},
	})
	if got := movedIDs(res.Sources[0]); !slices.Equal(got, []string{"primary", "rip"}) {
		t.Fatalf("moved = %v held = %+v, want both versions", got, res.Sources[0].Held)
	}
}

// A locked book is held, with its whole identity group, whatever else is true.
func TestDecide_LockedBookHoldsItsWholeGroup(t *testing.T) {
	for _, lock := range []string{"series_name", "series_position"} {
		t.Run(lock, func(t *testing.T) {
			res := Decide(Group{
				TargetID: 90, BaseName: "Court of Owls", AuthorID: 3,
				Sources: []Source{{SiblingRun: true, FromID: 41, Position: 5, Books: []Book{
					{ID: "primary", Identity: "g1", LockedBy: lock}, {ID: "rip", Identity: "g1"}, {ID: "other", Identity: "g2", Seq: ip(5)},
				}}},
			})
			r := res.Sources[0]
			if !slices.Equal(heldIDs(r), []string{"primary", "rip"}) {
				t.Fatalf("held = %v, want the whole locked group", heldIDs(r))
			}
			for _, h := range r.Held {
				if h.Code != HoldLocked || !strings.Contains(h.Reason, lock) {
					t.Errorf("held %+v, want %s naming %s", h, HoldLocked, lock)
				}
			}
			if !slices.Equal(movedIDs(r), []string{"other"}) {
				t.Fatalf("moved = %v, want only the unlocked group", movedIDs(r))
			}
		})
	}
}

// Locks that cannot be read fail closed.
func TestDecide_UnreadableLocksHoldTheBook(t *testing.T) {
	res := Decide(Group{
		TargetID: 90, BaseName: "Court of Owls", AuthorID: 3,
		Sources: []Source{{SiblingRun: true, FromID: 41, Position: 5, Books: []Book{{ID: "b1", LockUnreadable: "boom"}}}},
	})
	r := res.Sources[0]
	if len(r.Moves) != 0 || len(r.Held) != 1 || r.Held[0].Code != HoldLocked {
		t.Fatalf("moves %+v held %+v, want b1 held as %s", r.Moves, r.Held, HoldLocked)
	}
}

// A base that exists only under another author row is never created a second
// time: every book of every source is held with the named code, and nothing is
// created.
func TestDecide_BaseUnderAnotherAuthorHoldsEverythingAndCreatesNothing(t *testing.T) {
	res := Decide(Group{
		TargetID: 0, BaseName: "Knightfall", AuthorID: 3, BaseElsewhere: "series 12 under author 7",
		Sources: []Source{
			{SiblingRun: true, FromID: 41, FromName: "Knightfall 05", Position: 5, Books: []Book{{ID: "b1", Identity: "i1"}}},
			{SiblingRun: true, FromID: 42, FromName: "Knightfall 06", Position: 6, Books: []Book{{ID: "b2", Identity: "i2"}, {ID: "b3", Identity: "i3"}}},
		},
	})
	if res.CreateTarget {
		t.Fatalf("a base was created beside one under another author")
	}
	for si, want := range [][]string{{"b1"}, {"b2", "b3"}} {
		if got := movedIDs(res.Sources[si]); len(got) != 0 {
			t.Errorf("source %d moved %v", si, got)
		}
		if got := heldIDs(res.Sources[si]); !slices.Equal(got, want) {
			t.Errorf("source %d held %v, want %v", si, got, want)
		}
		for _, h := range res.Sources[si].Held {
			if h.Code != HoldBaseElsewhere || !strings.Contains(h.Reason, "series 12 under author 7") {
				t.Errorf("held %+v: want code %q and a reason naming the other series", h, HoldBaseElsewhere)
			}
		}
	}
}

// BaseElsewhere is about creating: an existing target already is the base.
func TestDecide_BaseElsewhereIsIgnoredWhenTheTargetExists(t *testing.T) {
	res := Decide(Group{
		TargetID: 90, BaseName: "Knightfall", AuthorID: 3, BaseElsewhere: "series 12 under author 7",
		Sources: []Source{{SiblingRun: true, FromID: 41, FromName: "Knightfall 05", Position: 5, Books: []Book{{ID: "b1", Identity: "i1"}}}},
	})
	if got := movedIDs(res.Sources[0]); !slices.Equal(got, []string{"b1"}) {
		t.Fatalf("moved %v, want [b1]", got)
	}
}

// Books counts versions of one book once.
func TestSourceResult_BooksCountsVersionsOnce(t *testing.T) {
	r := SourceResult{Moves: []Move{
		{BookID: "a1", Identity: "a"}, {BookID: "a2", Identity: "a"}, {BookID: "b1", Identity: "b"},
	}}
	if got := r.Books(); got != 2 {
		t.Fatalf("Books = %d, want 2", got)
	}
	if got := (SourceResult{}).Books(); got != 0 {
		t.Fatalf("empty Books = %d, want 0", got)
	}
}
