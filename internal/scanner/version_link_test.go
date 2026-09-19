// file: internal/scanner/version_link_test.go
// version: 1.1.0
// guid: 8adc2c96-c929-436d-8dae-1fb17ee04210
// last-edited: 2026-09-19

package scanner

import (
	"errors"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// vlStore is a minimal store for the version-link path: it holds rows by ID
// and serves ModifyBook with the same contract PebbleStore does, including
// ErrSkipBookWrite (the row is returned unchanged and the write is not
// counted).
type vlStore struct {
	*database.MockStore
	rows   map[string]*database.Book
	writes int // ModifyBook calls that actually mutated a row
}

func newVLStore(rows ...*database.Book) *vlStore {
	s := &vlStore{MockStore: &database.MockStore{}, rows: map[string]*database.Book{}}
	for _, r := range rows {
		s.rows[r.ID] = r
	}
	s.MockStore.ModifyBookFunc = func(id string, fn func(*database.Book) error) (*database.Book, error) {
		cur, ok := s.rows[id]
		if !ok {
			return nil, nil
		}
		cp := *cur
		if err := fn(&cp); err != nil {
			if errors.Is(err, database.ErrSkipBookWrite) {
				return cur, nil
			}
			return nil, err
		}
		s.rows[id] = &cp
		s.writes++
		return &cp, nil
	}
	return s
}

func useVLStore(t *testing.T, s *vlStore) {
	t.Helper()
	prev := getStore()
	SetStore(s)
	t.Cleanup(func() { SetStore(prev) })
}

func vlPrimary(t *testing.T, b *database.Book) bool {
	t.Helper()
	if b.IsPrimaryVersion == nil {
		t.Fatalf("book %s (%s) has a nil is_primary_version: the scan path must write it explicitly", b.ID, b.FilePath)
	}
	return *b.IsPrimaryVersion
}

// (a) An all-mp3 folder holding the chapter records of ONE book must not
// produce a version group: chapters are parts, not versions. This is the
// shape that produced vg-3112fc4ebc1e715d (27 members, every one written
// explicitly non-primary, so all 27 vanished from the library list).
func TestSmartVersionLink_ChapterSiblingsAreNotVersions(t *testing.T) {
	dir := "/lib/Isaac Asimov/Foundation and Empire"
	siblings := []database.Book{
		{ID: "b1", Title: "Foundation and Empire", FilePath: dir + "/01 Foundation and Empire.mp3", Format: "mp3", Duration: intPtr(1800)},
		{ID: "b2", Title: "Foundation and Empire", FilePath: dir + "/02 Foundation and Empire.mp3", Format: "mp3", Duration: intPtr(1810)},
	}
	store := newVLStore(&siblings[0], &siblings[1])
	useVLStore(t, store)

	newBook := &database.Book{Title: "Foundation and Empire", FilePath: dir + "/03 Foundation and Empire.mp3", Format: "mp3", Duration: intPtr(1795)}
	applySmartVersionLink(newBook, siblings, dir)

	if newBook.VersionGroupID != nil {
		t.Errorf("chapter record joined version group %q; chapters are parts of one book, not versions", *newBook.VersionGroupID)
	}
	if newBook.IsPrimaryVersion != nil {
		t.Errorf("chapter record was written is_primary_version=%v; it belongs to no group and must be left unset", *newBook.IsPrimaryVersion)
	}
	if store.writes != 0 {
		t.Errorf("chapter siblings were written %d time(s); none should be touched", store.writes)
	}
}

// Titles that are themselves positions ("Part 3", "157") are parts too, even
// when the file name says nothing.
func TestSmartVersionLink_PositionTitleIsNotAVersion(t *testing.T) {
	dir := "/lib/Author/Book"
	siblings := []database.Book{
		{ID: "b1", Title: "Part 1", FilePath: dir + "/one.mp3", Format: "mp3"},
	}
	store := newVLStore(&siblings[0])
	useVLStore(t, store)

	newBook := &database.Book{Title: "Part 2", FilePath: dir + "/two.mp3", Format: "mp3"}
	applySmartVersionLink(newBook, siblings, dir)

	if newBook.VersionGroupID != nil {
		t.Errorf("positional title joined version group %q", *newBook.VersionGroupID)
	}
}

// (b) Two genuine copies of one book in different formats still group, and
// the .m4b is the primary.
func TestSmartVersionLink_MixedFormatCopiesGroupWithM4BPrimary(t *testing.T) {
	dir := "/lib/Author/Foundation"
	siblings := []database.Book{
		{ID: "b1", Title: "Foundation", FilePath: dir + "/Foundation.m4b", Format: "m4b", Duration: intPtr(36000)},
	}
	store := newVLStore(&siblings[0])
	useVLStore(t, store)

	newBook := &database.Book{Title: "Foundation", FilePath: dir + "/Foundation.mp3", Format: "mp3", Duration: intPtr(36000)}
	applySmartVersionLink(newBook, siblings, dir)

	if newBook.VersionGroupID == nil {
		t.Fatal("two copies of one book in different formats were not grouped")
	}
	sib := store.rows["b1"]
	if sib.VersionGroupID == nil || *sib.VersionGroupID != *newBook.VersionGroupID {
		t.Fatalf("sibling is in group %v, new row in %q", sib.VersionGroupID, *newBook.VersionGroupID)
	}
	if !vlPrimary(t, sib) {
		t.Error("the .m4b is not the primary: the format preference regressed")
	}
	if vlPrimary(t, newBook) {
		t.Error("the .mp3 was also written primary: the group has two primaries")
	}
}

// (c) Two genuine copies in the SAME format group, with exactly one primary
// elected by the documented rule. The copy-suffix shape ("Foundation
// (1).mp3") is what a second same-format copy looks like.
func TestSmartVersionLink_SameFormatCopiesElectOnePrimary(t *testing.T) {
	dir := "/lib/Author/Foundation"
	siblings := []database.Book{
		{ID: "b1", Title: "Foundation", FilePath: dir + "/Foundation.mp3", Format: "mp3", Duration: intPtr(36000)},
	}
	store := newVLStore(&siblings[0])
	useVLStore(t, store)

	newBook := &database.Book{Title: "Foundation", FilePath: dir + "/Foundation (1).mp3", Format: "mp3", Duration: intPtr(36000)}
	applySmartVersionLink(newBook, siblings, dir)

	if newBook.VersionGroupID == nil {
		t.Fatal("two same-format copies of one book were not grouped")
	}
	sib := store.rows["b1"]
	primaries := 0
	if vlPrimary(t, sib) {
		primaries++
	}
	if vlPrimary(t, newBook) {
		primaries++
	}
	if primaries != 1 {
		t.Fatalf("group has %d primaries, want exactly 1", primaries)
	}
	// Equal durations and unknown sizes: rule 4 gives it to the existing row.
	if !vlPrimary(t, sib) {
		t.Error("on a full tie the existing row must stay primary, not the row being created")
	}
}

// The documented election order is observable: the longer duration wins when
// no member is an .m4b.
func TestSmartVersionLink_LongestDurationWinsWhenNoM4B(t *testing.T) {
	dir := "/lib/Author/Foundation"
	siblings := []database.Book{
		{ID: "b1", Title: "Foundation", FilePath: dir + "/Foundation.mp3", Format: "mp3", Duration: intPtr(36000)},
	}
	store := newVLStore(&siblings[0])
	useVLStore(t, store)

	// 37000 vs 36000 is inside the 5% same-content tolerance.
	newBook := &database.Book{Title: "Foundation", FilePath: dir + "/Foundation (1).mp3", Format: "mp3", Duration: intPtr(37000)}
	applySmartVersionLink(newBook, siblings, dir)

	if newBook.VersionGroupID == nil {
		t.Fatal("two same-format copies of one book were not grouped")
	}
	if !vlPrimary(t, newBook) {
		t.Error("the longer copy is not primary: the election rule regressed")
	}
	if vlPrimary(t, store.rows["b1"]) {
		t.Error("the shorter copy is still primary: the group has two primaries")
	}
}

// Durations that disagree beyond the tolerance are not the same content, so
// they do not group however their formats read.
func TestSmartVersionLink_ConflictingDurationsDoNotGroup(t *testing.T) {
	dir := "/lib/Author/Foundation"
	siblings := []database.Book{
		{ID: "b1", Title: "Foundation", FilePath: dir + "/Foundation.m4b", Format: "m4b", Duration: intPtr(36000)},
	}
	store := newVLStore(&siblings[0])
	useVLStore(t, store)

	newBook := &database.Book{Title: "Foundation", FilePath: dir + "/Foundation.mp3", Format: "mp3", Duration: intPtr(1800)}
	applySmartVersionLink(newBook, siblings, dir)

	if newBook.VersionGroupID != nil {
		t.Errorf("a 30-minute record was linked as a version of a 10-hour book (group %q)", *newBook.VersionGroupID)
	}
}

// A soft-deleted or merged-away sibling is not a group member, so it can
// never be elected primary (which would leave the group with no visible one).
func TestSmartVersionLink_DeadSiblingsAreNotCandidates(t *testing.T) {
	dir := "/lib/Author/Foundation"
	gone := "b0"
	siblings := []database.Book{
		{ID: "b1", Title: "Foundation", FilePath: dir + "/Foundation.m4b", Format: "m4b", Duration: intPtr(36000), MergedIntoBookID: &gone},
	}
	store := newVLStore(&siblings[0])
	useVLStore(t, store)

	newBook := &database.Book{Title: "Foundation", FilePath: dir + "/Foundation.mp3", Format: "mp3", Duration: intPtr(36000)}
	applySmartVersionLink(newBook, siblings, dir)

	if newBook.VersionGroupID != nil {
		t.Errorf("linked to a merged-away sibling (group %q)", *newBook.VersionGroupID)
	}
	if store.writes != 0 {
		t.Errorf("merged-away sibling was written %d time(s)", store.writes)
	}
}

// (d) Running the same folder through the link path twice changes nothing the
// second time: no sibling is rewritten, the group is the same, and the new
// row's flag does not churn.
func TestSmartVersionLink_RescanIsIdempotent(t *testing.T) {
	dir := "/lib/Author/Foundation"
	siblings := []database.Book{
		{ID: "b1", Title: "Foundation", FilePath: dir + "/Foundation.m4b", Format: "m4b", Duration: intPtr(36000)},
	}
	store := newVLStore(&siblings[0])
	useVLStore(t, store)

	first := &database.Book{Title: "Foundation", FilePath: dir + "/Foundation.mp3", Format: "mp3", Duration: intPtr(36000)}
	applySmartVersionLink(first, siblings, dir)
	if first.VersionGroupID == nil {
		t.Fatal("first pass did not group the two copies")
	}
	writesAfterFirst := store.writes

	// Second pass sees the world the first pass left behind: the sibling now
	// carries the group, and the row the first pass created is itself a
	// sibling in the store.
	created := &database.Book{
		ID: "b2", Title: "Foundation", FilePath: first.FilePath, Format: "mp3", Duration: intPtr(36000),
		VersionGroupID: first.VersionGroupID, IsPrimaryVersion: first.IsPrimaryVersion,
	}
	store.rows["b2"] = created
	next := []database.Book{*store.rows["b1"], *created}

	second := &database.Book{Title: "Foundation", FilePath: dir + "/Foundation.mp3", Format: "mp3", Duration: intPtr(36000)}
	applySmartVersionLink(second, next, dir)

	if store.writes != writesAfterFirst {
		t.Errorf("second pass wrote %d more row(s); a rescan must not churn version flags", store.writes-writesAfterFirst)
	}
	if second.VersionGroupID == nil || *second.VersionGroupID != *first.VersionGroupID {
		t.Errorf("second pass landed in group %v, first pass in %q", second.VersionGroupID, *first.VersionGroupID)
	}
	if vlPrimary(t, second) {
		t.Error("second pass crowned a second primary: the incumbent .m4b already holds it")
	}
	if !vlPrimary(t, store.rows["b1"]) {
		t.Error("the incumbent primary lost its flag on rescan")
	}
}

// A group with no member that can be linked must not be minted at all: a
// group of one reads downstream as "this book has other versions" and lists
// none.
func TestSmartVersionLink_NoOrphanGroupWhenLinkFails(t *testing.T) {
	dir := "/lib/Author/Foundation"
	siblings := []database.Book{
		{ID: "b1", Title: "Foundation", FilePath: dir + "/Foundation.m4b", Format: "m4b", Duration: intPtr(36000)},
	}
	store := newVLStore() // b1 is not in the store: the link write cannot land
	useVLStore(t, store)

	newBook := &database.Book{Title: "Foundation", FilePath: dir + "/Foundation.mp3", Format: "mp3", Duration: intPtr(36000)}
	applySmartVersionLink(newBook, siblings, dir)

	if newBook.VersionGroupID != nil {
		t.Errorf("minted orphan version group %q when no sibling link landed", *newBook.VersionGroupID)
	}
}

// The shape the next real scan will hit most often against today's data: the
// sibling already carries a group and that group has NO primary (one of the
// 2,586 such groups measured on 2026-09-19). The new row must join THAT group
// and take primacy, not add another non-primary member to it.
func TestSmartVersionLink_JoiningZeroPrimaryGroupTakesPrimacy(t *testing.T) {
	dir := "/lib/Author/Foundation"
	group := "vg-deadbeefdeadbeef"
	no := false
	siblings := []database.Book{
		{
			ID: "b1", Title: "Foundation", FilePath: dir + "/Foundation.mp3", Format: "mp3",
			Duration: intPtr(36000), VersionGroupID: &group, IsPrimaryVersion: &no,
		},
	}
	store := newVLStore(&siblings[0])
	useVLStore(t, store)

	newBook := &database.Book{Title: "Foundation", FilePath: dir + "/Foundation.m4b", Format: "m4b", Duration: intPtr(36000)}
	applySmartVersionLink(newBook, siblings, dir)

	if newBook.VersionGroupID == nil || *newBook.VersionGroupID != group {
		t.Fatalf("new row landed in group %v, want the sibling's %q", newBook.VersionGroupID, group)
	}
	if !vlPrimary(t, newBook) {
		t.Error("joined a group with no primary and stayed non-primary: the group is still invisible")
	}
	if store.writes != 0 {
		t.Errorf("the already-grouped sibling was rewritten %d time(s)", store.writes)
	}
}
