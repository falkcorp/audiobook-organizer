// file: internal/merge/combine_undo_restore_rule_test.go
// version: 1.0.0
// guid: 24aaf1dd-a2da-4467-a16b-aa81eaa00a53
// last-edited: 2026-10-01

package merge

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/versionprimary/vptest"
)

func combineUndoFixture(t *testing.T) *vptest.Fixture {
	t.Helper()
	f := vptest.New(t)
	prev := config.AppConfig.RootDir
	config.AppConfig.RootDir = f.Root
	t.Cleanup(func() { config.AppConfig.RootDir = prev })
	return f
}

// Adversarial review of #3649, finding 4: the combine undo yielded only to a
// live member whose flag was explicitly true. A group whose primary has a nil
// flag (which every visibility path reads as primary) got the absorbed book
// back as a second primary. It now uses the restore paths' rule,
// versionprimary.IncumbentExcept + YieldToIncumbent.
func TestCombineUndo_YieldsToANilFlagIncumbent(t *testing.T) {
	f := combineUndoFixture(t)
	s := f.Book(t, vptest.Spec{ID: "s"})
	a := f.Book(t, vptest.Spec{ID: "a", Group: "g-a", Primary: "true"})
	sib := f.Book(t, vptest.Spec{ID: "sib", Group: "g-a", Primary: "false"})

	svc := NewService(f.S)
	res, err := svc.CombineBooks([]string{s, a}, s, nil)
	require.NoError(t, err)
	// While a was absorbed, sib became the group's primary with a nil flag.
	_, err = f.S.ModifyBook(sib, func(b *database.Book) error {
		b.IsPrimaryVersion = nil
		return nil
	})
	require.NoError(t, err)
	// No library root from here: the undo's own hand-off holds the group, so
	// what the undo wrote for a is what stays.
	config.AppConfig.RootDir = ""

	_, err = svc.UndoCombine(res.JournalID)
	require.NoError(t, err)
	require.Equal(t, "false", f.Flag(t, a), "the restored shell must yield to the nil-flag incumbent, not come back as a second primary")
	require.Equal(t, "nil", f.Flag(t, sib))
}

// Adversarial review of #3649, findings 2 and 5: the undo applied the restore
// rule while the absorbed book's files were still on the survivor, so a rule
// that requires present files to keep "organized" would bring every undone
// shell back "imported" (out of ABS). The rule runs after the files are back.
func TestCombineUndo_OrganizedShellComesBackOrganized(t *testing.T) {
	f := combineUndoFixture(t)
	s := f.Book(t, vptest.Spec{ID: "s", Primary: "nil"})
	a := f.Book(t, vptest.Spec{ID: "a", Primary: "nil"})

	svc := NewService(f.S)
	res, err := svc.CombineBooks([]string{s, a}, s, nil)
	require.NoError(t, err)
	_, err = svc.UndoCombine(res.JournalID)
	require.NoError(t, err)

	b, err := f.S.GetBookByID(a)
	require.NoError(t, err)
	require.False(t, b.IsSoftDeleted())
	require.Equal(t, "organized", *b.LibraryState, "an undone shell whose files are back inside the root stays organized")
	require.True(t, database.ABSLibraryFilter().Matches(b))
}

// The same rule labels an undone shell "imported" when the files it gets back
// are outside the library root.
func TestCombineUndo_ShellOutsideTheRootComesBackImported(t *testing.T) {
	f := combineUndoFixture(t)
	s := f.Book(t, vptest.Spec{ID: "s", Primary: "nil"})
	a := f.Book(t, vptest.Spec{ID: "a", Primary: "nil", Outside: true})

	svc := NewService(f.S)
	res, err := svc.CombineBooks([]string{s, a}, s, nil)
	require.NoError(t, err)
	_, err = svc.UndoCombine(res.JournalID)
	require.NoError(t, err)

	b, err := f.S.GetBookByID(a)
	require.NoError(t, err)
	require.Equal(t, "imported", *b.LibraryState)
}
