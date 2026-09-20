// file: internal/reconcile/elect_primaries_exclude_test.go
// version: 1.0.0
// guid: 1c6f0b2a-5d83-4a71-9f2c-7b4e83d1a905
// last-edited: 2026-09-19

package reconcile

import (
	"fmt"
	"slices"
	"testing"
	"time"
)

// electCensusChapterGroups is the 2026-09-19 census's list of version groups
// whose members are the CHAPTER FILES of one book rather than copies of it —
// the 12 tier-1 groups (§4.1, including the seven Asimov "(f04) Foundation"
// groups) plus the 3 tier-2 numbered pairs (§4.2) of
// .claude/notes/elect-primaries-dryrun-and-band-census-2026-09-19.md.
//
// Electing in any of them crowns a chapter as the book, which is the only
// thing that blocked the whole-library apply. This list is the operator's
// exclude list, pinned here so the mechanism is tested against the real ids
// and not a stand-in.
var electCensusChapterGroups = []string{
	// Tier 1 — 3+ members, one folder, one title, one format.
	"vg-3112fc4ebc1e715d",
	"vg-108615cd250b7fca",
	"vg-de1925f720584970",
	"vg-2a8f3b896cd11b55",
	"vg-a59d6ea5e7671554",
	"vg-e91dd1c5df7edf38",
	"vg-dcd91fa447ba21a8",
	"vg-b0210c7c5a18dd35",
	"01KNDCE5GWKYA6HZKZ0CVHARR3",
	"vg-6dcbc2d456c17d82",
	"vg-1399d6d47b734a41",
	"vg-168a012c2d7b62b2",
	// Tier 2 — numbered chapter pairs inside the 622-group band.
	"vg-54dff1ed7c73b7c4",
	"vg-2e6854d0842b9306",
	"vg-032bd1d67071e3e6",
}

// addElectBookAt is addElectBook plus the file path, which the census used to
// tell a chapter run from a copy. The election itself never reads FilePath —
// see the "no automatic shape predicate" note in ElectMissingPrimaries — so
// the paths here document the shape rather than drive the outcome.
func (f *electFakeStore) addElectBookAt(id, title, gid string, primary bool, created time.Time, filePath string) {
	f.addElectBook(id, title, gid, primary, created)
	f.byID[id].FilePath = filePath
	members := f.byGroup[gid]
	for i := range members {
		if members[i].ID == id {
			members[i].FilePath = filePath
		}
	}
}

// TestElectMissingPrimaries_ExcludedGroupIsHeldAndCounted is the core of the
// new parameter: a named group is skipped, reported by id, and counted — and
// a group that is NOT named still elects, so the exclusion is a filter rather
// than an off switch.
func TestElectMissingPrimaries_ExcludedGroupIsHeldAndCounted(t *testing.T) {
	base := time.Date(2026, 4, 4, 0, 0, 0, 0, time.UTC)
	store := newElectFakeStore()

	// Held: a three-chapter run of one book, the shape from the census.
	store.addElectBookAt("hold-a", "Foundation And", "vg-hold", false, base,
		"/books/Asimov/Foundation And/01 Foundation And.mp3")
	store.addElectBookAt("hold-b", "Foundation And", "vg-hold", false, base.Add(time.Hour),
		"/books/Asimov/Foundation And/02 Foundation And.mp3")
	store.addElectBookAt("hold-c", "Foundation And", "vg-hold", false, base.Add(2*time.Hour),
		"/books/Asimov/Foundation And/03 Foundation And.mp3")

	// Not held: two genuine copies of one book, which must still be repaired.
	store.addElectBookAt("keep-a", "Shadowfever", "vg-keep", false, base,
		"/books/Moning/Shadowfever/Shadowfever.m4b")
	store.addElectBookAt("keep-b", "Shadowfever", "vg-keep", false, base.Add(time.Hour),
		"/books/Moning/Shadowfever/Shadowfever (1).m4b")

	res, err := ElectMissingPrimaries(store, false, []string{"vg-hold"})
	if err != nil {
		t.Fatalf("ElectMissingPrimaries: %v", err)
	}

	for _, id := range []string{"hold-a", "hold-b", "hold-c"} {
		if _, ok := store.updated[id]; ok {
			t.Errorf("%s was crowned despite vg-hold being excluded", id)
		}
	}
	if _, ok := store.updated["keep-a"]; !ok {
		t.Errorf("keep-a (earliest of an unexcluded group) was not elected; wrote %v", keysOfElectUpdated(store))
	}
	if res.Elected != 1 {
		t.Errorf("Elected = %d, want 1", res.Elected)
	}
	if res.GroupsExcluded != 1 {
		t.Errorf("GroupsExcluded = %d, want 1", res.GroupsExcluded)
	}
	if res.BooksExcluded != 3 {
		t.Errorf("BooksExcluded = %d, want 3", res.BooksExcluded)
	}
	if !slices.Equal(res.ExcludedApplied, []string{"vg-hold"}) {
		t.Errorf("ExcludedApplied = %v, want [vg-hold]", res.ExcludedApplied)
	}
	if len(res.ExcludedUnmatched) != 0 || len(res.ExcludedNotCandidate) != 0 {
		t.Errorf("unexpected unmatched=%v not_candidate=%v", res.ExcludedUnmatched, res.ExcludedNotCandidate)
	}
	// An excluded group is still a group the scan found electing no primary:
	// the flagged total must not move, or an operator diffing this run against
	// the unfiltered dry run sees a number that changed for no visible reason.
	if res.GroupsWithoutPrimary != 2 {
		t.Errorf("GroupsWithoutPrimary = %d, want 2 (excluded groups stay in the flagged total)", res.GroupsWithoutPrimary)
	}
	if res.BooksTrapped != 5 {
		t.Errorf("BooksTrapped = %d, want 5", res.BooksTrapped)
	}
	if got := res.GroupsWithoutPrimary - res.GroupsExcluded - res.SkippedConcurrent -
		res.SkippedVanished - res.SkippedNoEligible - res.SkippedWinnerChanged - res.Errors; got != res.Elected {
		t.Errorf("counters do not reconcile: flagged-excluded-skips-errors = %d, Elected = %d", got, res.Elected)
	}
}

// TestElectMissingPrimaries_CensusChapterGroupsAreNotCrowned is the proof the
// task asks for: the 15 groups the 2026-09-19 census identified as chapter
// runs end up with NO member crowned when they are passed as the exclude
// list, while an ordinary group in the same run is repaired.
//
// Member shapes mirror the census, deliberately including both forms: the
// numbered runs (Asimov "01/02/21 … .mp3") and the folder-shaped rows whose
// file path is an extensionless directory ("Logan Jacobs", "Immortal Mana").
// The second form is why the exclusion is an explicit list — a filename-shape
// predicate sees nothing positional in it.
func TestElectMissingPrimaries_CensusChapterGroupsAreNotCrowned(t *testing.T) {
	base := time.Date(2026, 4, 4, 0, 0, 0, 0, time.UTC)
	store := newElectFakeStore()

	numbered := map[string]bool{
		"vg-3112fc4ebc1e715d": true, "vg-108615cd250b7fca": true,
		"vg-de1925f720584970": true, "vg-2a8f3b896cd11b55": true,
		"vg-a59d6ea5e7671554": true, "vg-e91dd1c5df7edf38": true,
		"vg-dcd91fa447ba21a8": true, "vg-54dff1ed7c73b7c4": true,
		"vg-2e6854d0842b9306": true, "vg-032bd1d67071e3e6": true,
	}
	var memberIDs []string
	for gi, gid := range electCensusChapterGroups {
		for mi := range 3 {
			id := fmt.Sprintf("m-%02d-%d", gi, mi)
			path := fmt.Sprintf("/books/g%02d/Whole Book", gi) // folder-shaped row
			if numbered[gid] {
				path = fmt.Sprintf("/books/g%02d/%02d Whole Book.mp3", gi, mi+1)
			}
			store.addElectBookAt(id, "Whole Book", gid, false, base.Add(time.Duration(mi)*time.Hour), path)
			memberIDs = append(memberIDs, id)
		}
	}
	// Control: an ordinary primary-less group that must still be repaired, so
	// a mechanism that simply elected nothing cannot pass this test.
	store.addElectBookAt("control-a", "Control", "vg-control", false, base,
		"/books/Control/Control.m4b")
	store.addElectBookAt("control-b", "Control", "vg-control", false, base.Add(time.Hour),
		"/books/Control/Control (1).m4b")

	res, err := ElectMissingPrimaries(store, false, electCensusChapterGroups)
	if err != nil {
		t.Fatalf("ElectMissingPrimaries: %v", err)
	}

	for _, id := range memberIDs {
		if _, ok := store.updated[id]; ok {
			t.Errorf("%s was crowned: a chapter-run group from the census was elected", id)
		}
	}
	if res.GroupsExcluded != len(electCensusChapterGroups) {
		t.Errorf("GroupsExcluded = %d, want %d", res.GroupsExcluded, len(electCensusChapterGroups))
	}
	if res.BooksExcluded != 3*len(electCensusChapterGroups) {
		t.Errorf("BooksExcluded = %d, want %d", res.BooksExcluded, 3*len(electCensusChapterGroups))
	}
	if len(res.ExcludedApplied) != len(electCensusChapterGroups) {
		t.Errorf("ExcludedApplied = %v, want all %d ids", res.ExcludedApplied, len(electCensusChapterGroups))
	}
	if res.Elected != 1 {
		t.Errorf("Elected = %d, want 1 (the control group only)", res.Elected)
	}
	if _, ok := store.updated["control-a"]; !ok {
		t.Errorf("the control group was not repaired; wrote %v", keysOfElectUpdated(store))
	}
	for _, s := range res.Samples {
		if slices.Contains(electCensusChapterGroups, s.VersionGroupID) {
			t.Errorf("excluded group %s appears in the preview samples", s.VersionGroupID)
		}
	}
}

// TestElectMissingPrimaries_DryRunWithExclusionWritesNothing pins that adding
// the parameter did not disturb the dry run: it still previews and still
// writes nothing, and it reports the exclusion so an operator can check the
// list BEFORE authorising the apply — which is the whole point of previewing.
func TestElectMissingPrimaries_DryRunWithExclusionWritesNothing(t *testing.T) {
	base := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	store := newElectFakeStore()
	store.addElectBook("dry-hold", "Dry Hold", "vg-dry-hold", false, base)
	store.addElectBook("dry-keep", "Dry Keep", "vg-dry-keep", false, base)

	res, err := ElectMissingPrimaries(store, true, []string{"vg-dry-hold"})
	if err != nil {
		t.Fatalf("ElectMissingPrimaries: %v", err)
	}
	if len(store.updated) != 0 {
		t.Errorf("dry run wrote %d books, want 0", len(store.updated))
	}
	if res.Elected != 0 {
		t.Errorf("Elected = %d, want 0 on a dry run", res.Elected)
	}
	if res.GroupsExcluded != 1 || !slices.Equal(res.ExcludedApplied, []string{"vg-dry-hold"}) {
		t.Errorf("GroupsExcluded = %d, ExcludedApplied = %v, want 1 / [vg-dry-hold]",
			res.GroupsExcluded, res.ExcludedApplied)
	}
	if len(res.Samples) != 1 || res.Samples[0].VersionGroupID != "vg-dry-keep" {
		t.Errorf("Samples = %v, want the unexcluded group only", res.Samples)
	}
}

// TestElectMissingPrimaries_UnknownAndInertExcludeIDsAreReported covers the
// failure mode that makes an exclude list dangerous: an id that protects
// nothing looks exactly like one that does. A typo'd or stale id comes back
// in ExcludedUnmatched, and an id naming a group that was not a candidate
// anyway comes back in ExcludedNotCandidate, so an operator can tell the
// difference without reading the group by hand.
func TestElectMissingPrimaries_UnknownAndInertExcludeIDsAreReported(t *testing.T) {
	base := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	store := newElectFakeStore()
	store.addElectBook("real-a", "Real", "vg-real", false, base)
	// Already elects a primary: excluding it holds nothing back.
	store.addElectBook("done-a", "Done", "vg-done", true, base)
	store.addElectBook("done-b", "Done 2", "vg-done", false, base.Add(time.Hour))

	res, err := ElectMissingPrimaries(store, true, []string{
		"vg-real", "vg-done", "vg-typo-not-in-library", " ", "VG-REAL",
	})
	if err != nil {
		t.Fatalf("ElectMissingPrimaries: %v", err)
	}
	if !slices.Equal(res.ExcludedApplied, []string{"vg-real"}) {
		t.Errorf("ExcludedApplied = %v, want [vg-real]", res.ExcludedApplied)
	}
	if !slices.Equal(res.ExcludedNotCandidate, []string{"vg-done"}) {
		t.Errorf("ExcludedNotCandidate = %v, want [vg-done]", res.ExcludedNotCandidate)
	}
	// "VG-REAL" is a case-flipped paste of a real id and must NOT silently
	// count as protecting vg-real: ids are matched exactly, so it is reported
	// as matching nothing.
	if !slices.Equal(res.ExcludedUnmatched, []string{"VG-REAL", "vg-typo-not-in-library"}) {
		t.Errorf("ExcludedUnmatched = %v, want [VG-REAL vg-typo-not-in-library]", res.ExcludedUnmatched)
	}
}
