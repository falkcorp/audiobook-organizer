// file: internal/books/cutover/cutoverlist_test.go
// version: 1.0.1
// guid: 0c2d5a52-4b8e-45a8-9e3d-8a2d6a5e9d02
// last-edited: 2026-11-11

package cutover

import (
	"errors"
	"strings"
	"testing"

	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/storage/datamigrate"
)

const (
	booksLevelVersion = 69 // books' next contiguous version: LegacyCap (66) + 3
	secondLevel       = 2
)

// dataStep is a cutover data step of owner, raised to level, behind after.
func dataStep(id string, owner storage.Owner, level int, after ...storage.StepRef) datamigrate.DataMigration {
	return datamigrate.DataMigration{ID: id, Owner: owner, Cutover: true, Compat: storage.Breaking, RaisesLevel: level, After: after}
}

// validList is 03 §3.8's order over this package's constructors: books' level
// step, core's, M2, M8.
func validList() CutoverList {
	m2 := ConfigStep(nil, nil, ConfigStepOptions{LevelStepVersion: booksLevelVersion, DatabasePath: rigStore})
	return CutoverList{
		Target:       storage.Levels{BooksOwner: BooksCutoverLevel, "core": 1},
		FirstVersion: storage.Levels{BooksOwner: booksLevelVersion},
		Entries: []ListEntry{
			LevelEntry(BooksOwner, BooksLevelStep(booksLevelVersion, BooksCutoverLevel)),
			LevelEntry("core/storage", storage.CoreLevelStep(1, 1)),
			DataEntry(m2),
			RewriteEntry(RewriteStep()),
		},
	}
}

func problems(t *testing.T, l CutoverList) []string {
	t.Helper()
	err := ValidateCutoverList(l)
	if err == nil {
		return nil
	}
	var ce *CutoverListError
	if !errors.As(err, &ce) || !errors.Is(err, ErrInvalidCutoverList) {
		t.Fatalf("the error %v is not a *CutoverListError matching ErrInvalidCutoverList", err)
	}
	return ce.Problems
}

func wantProblem(t *testing.T, l CutoverList, sub string) {
	t.Helper()
	ps := problems(t, l)
	for _, p := range ps {
		if strings.Contains(p, sub) {
			return
		}
	}
	t.Errorf("problems %q lack one containing %q", ps, sub)
}

// TestCutoverList_LevelsFirstContiguousOneM8Last: over the list built from
// this package's constructors in 03 §3.8's order the check passes, and so does
// a two-flip list; a list with no level step for a raised owner, a data step
// before its flip's level step, a gap in an owner's versions, two M8s, or M8
// before a data step fails.
func TestCutoverList_LevelsFirstContiguousOneM8Last(t *testing.T) {
	t.Run("the list of this row passes", func(t *testing.T) {
		if ps := problems(t, validList()); len(ps) != 0 {
			t.Fatalf("the list built from this row's constructors has problems: %q", ps)
		}
	})

	t.Run("a two-flip list passes", func(t *testing.T) {
		l := validList()
		second := dataStep("books.second-flip", BooksOwner, secondLevel, BooksLevelStepRef(booksLevelVersion+1))
		l.Target[BooksOwner] = secondLevel
		l.Entries = []ListEntry{
			l.Entries[0], l.Entries[1], l.Entries[2],
			LevelEntry(BooksOwner, BooksLevelStep(booksLevelVersion+1, secondLevel, BooksLevelStepRef(booksLevelVersion))),
			DataEntry(second),
			l.Entries[3],
		}
		if ps := problems(t, l); len(ps) != 0 {
			t.Fatalf("a second flip at the next version, ordered before its data step, has problems: %q", ps)
		}
	})

	t.Run("no level step for an owner the build raises", func(t *testing.T) {
		l := validList()
		l.Entries = append(l.Entries[:1:1], l.Entries[2:]...) // no core level step
		wantProblem(t, l, "core is raised to 1 by this build but the list has no level step for it")
	})

	t.Run("no level step at all for books", func(t *testing.T) {
		l := validList()
		l.Entries = l.Entries[1:]
		wantProblem(t, l, "books is raised to 1 by this build but the list has no level step for it")
		wantProblem(t, l, "the level step books.level-1 is not ordered before it")
	})

	t.Run("a data step ordered before its level step", func(t *testing.T) {
		l := validList()
		l.Entries = []ListEntry{l.Entries[2], l.Entries[0], l.Entries[1], l.Entries[3]}
		wantProblem(t, l, "is not ordered before it")
	})

	t.Run("a data step that does not name the level step in its After", func(t *testing.T) {
		l := validList()
		l.Entries[2] = DataEntry(ConfigStep(nil, nil, ConfigStepOptions{LevelStepVersion: booksLevelVersion + 5, DatabasePath: rigStore}))
		wantProblem(t, l, "does not name books.level-1")
	})

	t.Run("a gap in an owner's versions", func(t *testing.T) {
		l := validList()
		l.Target[BooksOwner] = secondLevel
		l.Entries = []ListEntry{
			l.Entries[0], l.Entries[1], l.Entries[2],
			LevelEntry(BooksOwner, BooksLevelStep(booksLevelVersion+2, secondLevel)),
			DataEntry(dataStep("books.second-flip", BooksOwner, secondLevel, BooksLevelStepRef(booksLevelVersion+2))),
			l.Entries[3],
		}
		wantProblem(t, l, "are not contiguous: version 71 follows version 69")
	})

	t.Run("books' first level step is not its next contiguous version", func(t *testing.T) {
		l := validList()
		l.FirstVersion[BooksOwner] = booksLevelVersion - 1
		wantProblem(t, l, "first level step is version 69, want its next contiguous version 68")
	})

	t.Run("two M8s", func(t *testing.T) {
		l := validList()
		l.Entries = append(l.Entries, RewriteEntry(RewriteStep()))
		wantProblem(t, l, "2 M8 steps are registered")
		wantProblem(t, l, "M8 is entry 3 of 5")
	})

	t.Run("no M8", func(t *testing.T) {
		l := validList()
		l.Entries = l.Entries[:3]
		wantProblem(t, l, "no M8")
	})

	t.Run("M8 before a data step", func(t *testing.T) {
		l := validList()
		l.Entries = []ListEntry{l.Entries[0], l.Entries[1], l.Entries[3], l.Entries[2]}
		wantProblem(t, l, "must be the last step")
	})

	t.Run("M8 that is not books.rewrite", func(t *testing.T) {
		l := validList()
		l.Entries[3] = RewriteEntry(storage.NewRewriteStep(BooksOwner, "books.rewrite-2"))
		wantProblem(t, l, "the one fixed ID, D15")
	})

	t.Run("a data step that is not a cutover step", func(t *testing.T) {
		l := validList()
		bad := dataStep("books.not-cutover", BooksOwner, BooksCutoverLevel, BooksLevelStepRef(booksLevelVersion))
		bad.Cutover = false
		l.Entries[2] = DataEntry(bad)
		wantProblem(t, l, "is not a cutover step")
	})

	t.Run("level steps that do not rise", func(t *testing.T) {
		l := validList()
		l.Entries = []ListEntry{
			l.Entries[0], l.Entries[1], l.Entries[2],
			LevelEntry(BooksOwner, BooksLevelStep(booksLevelVersion+1, BooksCutoverLevel)),
			l.Entries[3],
		}
		wantProblem(t, l, "do not rise")
	})

	t.Run("a level step run that guards no data step", func(t *testing.T) {
		l := validList()
		l.Target[BooksOwner] = secondLevel
		l.Entries = []ListEntry{
			l.Entries[0], l.Entries[1], l.Entries[2],
			LevelEntry(BooksOwner, BooksLevelStep(booksLevelVersion+1, secondLevel, BooksLevelStepRef(booksLevelVersion))),
			l.Entries[3],
		}
		wantProblem(t, l, "guard no data step before M8")
	})

	t.Run("a level step that is not named for its level", func(t *testing.T) {
		l := validList()
		m := BooksLevelStep(booksLevelVersion, BooksCutoverLevel)
		m.Name = "books.flip"
		l.Entries[0] = LevelEntry(BooksOwner, m)
		wantProblem(t, l, "is not a level step")
	})
}
