// file: internal/repairs/owner_itunes_db_only_test.go
// version: 1.0.0
// guid: c3c492b1-08e3-4790-a372-04f8183fa12e
// last-edited: 2026-10-07

package repairs

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// Row.OwnerITunesDatabaseOnly lifts the books/itunes path check for the
// books it names, and only on an owner row applied by the owner. Every
// other case keeps refusing (docs/plans/2026-10-07-stale-itunes-path.md D8).
func TestGuardRowBooksFor_OwnerITunesDatabaseOnly(t *testing.T) {
	s := newMemStore()
	s.add("twin", "Placeholder 02", "/books/itunes/iTunes Media/Audiobooks/Placeholder/02.mp3", nil)
	s.add("other", "Placeholder 03", "/books/itunes/iTunes Media/Audiobooks/Placeholder/03.mp3", nil)
	s.add("frag", "Placeholder 02", "/lib/Placeholder/02/02.mp3", nil)
	s.add("dw", "Doctor Who: Placeholder", "/books/itunes/iTunes Media/Audiobooks/Placeholder/dw.mp3", nil)
	ownerRow := func(ids, dbOnly []string) Row {
		return Row{RowID: "owner:p", BookIDs: ids, Skipped: SkipITunes, OwnerApplicable: true, OwnerITunesDatabaseOnly: dbOnly}
	}
	guard := func(row Row, extra []string, owner bool) string {
		t.Helper()
		k, why, err := GuardRowBooksFor(testFixer{}, s, nil, nil, NewPathResolver(), row, extra, owner)
		require.NoError(t, err)
		if k == "" {
			return ""
		}
		return k + ": " + why
	}

	require.Empty(t, guard(ownerRow([]string{"frag", "twin"}, []string{"twin"}), nil, true),
		"the listed twin of an owner row, applied by the owner, passes")

	require.Contains(t, guard(ownerRow([]string{"frag", "twin"}, []string{"twin"}), nil, false), SkipITunes,
		"a bulk / non-owner apply of the same row refuses")
	require.Contains(t, guard(ownerRow([]string{"frag", "twin", "other"}, []string{"twin"}), nil, true), "other",
		"an iTunes-tree book not on the list still refuses")
	applicable := ownerRow([]string{"frag", "twin"}, []string{"twin"})
	applicable.Skipped = ""
	require.Contains(t, guard(applicable, nil, true), SkipITunes,
		"an applicable (bulk) row carrying the list still refuses")
	notOwner := ownerRow([]string{"frag", "twin"}, []string{"twin"})
	notOwner.OwnerApplicable = false
	require.Contains(t, guard(notOwner, nil, true), SkipITunes,
		"a row the owner may not apply still refuses")
	require.Contains(t, guard(ownerRow([]string{"frag"}, []string{"twin"}), []string{"twin"}, true), SkipITunes,
		"a listed id outside the row's books is not lifted (extra ids)")
	require.Contains(t, guard(ownerRow([]string{"frag", "dw"}, []string{"dw"}), nil, true), SkipOwnerManual,
		"a Doctor Who twin is still held for the owner by hand")
}
