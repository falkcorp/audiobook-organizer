// file: internal/itunes/service/fs_regroup_recommend_test.go
// version: 1.0.0
// guid: ef64c75d-a456-4afd-94fd-ca99116dc105
// last-edited: 2026-08-06

package itunesservice

import (
	"fmt"
	"strings"
	"testing"
)

// This file covers the recommendation layer added on 2026-08-06 and the
// survivor-title fix that shipped with it. Every case is calibrated against a
// production measurement taken the same day (356 pending holds), recorded in
// PLAN.md's calibration table; the per-case comments give the hold count each
// shape represents so a future change that flips one knows what it is moving.
//
// The paths are real prod shapes, not invented ones. That matters: the whole
// failure mode being fixed here is a classifier that reads plausibly on synthetic
// input and mis-reads the actual library.

// sbD builds a primary single-file ShatterBook with a known runtime.
func sbD(id, path string, durationSec int) ShatterBook {
	b := sb(id, path)
	b.DurationSec = durationSec
	return b
}

// sbDA is sbD plus a display author — needed for the survivor-title author guard,
// which stays inert when no author holds a majority.
func sbDA(id, path string, durationSec int, author string) ShatterBook {
	b := sbD(id, path, durationSec)
	b.Author = author
	return b
}

const (
	fifteenHours = 56736 // 15.76 h — a whole novel (the real "The Rising" runtime)
	tenHours     = 38520 // 10.7 h  — a whole novel
	twoHours     = 7200  // 2 h     — book-length by the 90-min rule
	thirtyMin    = 1800  // 30 min  — a fragment
	nineteenMin  = 1152  // 0.32 h  — the real the-final-strife median
	twentyMin    = 1200  // 20 min  — a fragment
)

// assertRec checks the recommended action and that the reason actually quotes
// numbers. A reason without numbers is a nicer generic string, which is precisely
// the thing this change exists to replace (762 of 777 holds carried one identical
// sentence before it).
func assertRec(t *testing.T, g RegroupGroup, wantAction string) {
	t.Helper()
	if g.RecommendedAction != wantAction {
		t.Errorf("recommendedAction=%q, want %q (reason: %q)",
			g.RecommendedAction, wantAction, g.RecommendationReason)
	}
	if strings.TrimSpace(g.RecommendationReason) == "" {
		t.Errorf("recommendationReason is empty for action %q", g.RecommendedAction)
	}
	if !strings.ContainsAny(g.RecommendationReason, "0123456789") {
		t.Errorf("recommendationReason names no numbers: %q", g.RecommendationReason)
	}
}

// ─── calibration table: ambiguous holds ──────────────────────────────────────

// 137 prod holds. Two members, each a full 15.76 h novel, sharing an edition
// folder. The classifier's Kind is ambiguous (the 762-of-777 flat-ambiguous
// branch); the runtimes say these are two separate books.
func TestRecommend_Ambiguous_MajorityBookLength_Separate(t *testing.T) {
	base := shatterRoot + "/Ian Tregillis/The Rising (Unabridged)/The Rising (Unabridged)"
	books := []ShatterBook{
		sbD("r1", base+"/01.mp3", fifteenHours),
		sbD("r2", base+"/02.mp3", fifteenHours),
	}
	groups, _ := ClassifyShatteredFolders(books)
	g := findGroup(t, groups, "The Rising (Unabridged)")
	if g.Kind != KindAmbiguous {
		t.Fatalf("kind=%q, want %q (the Kind switch must be untouched)", g.Kind, KindAmbiguous)
	}
	assertRec(t, g, ActionSeparate)
	if g.RecommendationEvidence.BookLengthMembers != 2 {
		t.Errorf("bookLengthMembers=%d, want 2", g.RecommendationEvidence.BookLengthMembers)
	}
	if g.RecommendationEvidence.LongestKnownSec != fifteenHours {
		t.Errorf("longestKnownSec=%d, want %d", g.RecommendationEvidence.LongestKnownSec, fifteenHours)
	}
	// The reason must be legible without opening the hold.
	if !strings.Contains(g.RecommendationReason, "15.8 h") {
		t.Errorf("reason should quote the longest runtime, got %q", g.RecommendationReason)
	}
	if g.SurvivorTitle != "The Rising" {
		t.Errorf("survivorTitle=%q, want 'The Rising'", g.SurvivorTitle)
	}
}

// 24 prod holds. Same folder shape, but every member is a 30-minute fragment.
func TestRecommend_Ambiguous_AllFragments_Combine(t *testing.T) {
	base := shatterRoot + "/Ian Tregillis/The Rising (Unabridged)/The Rising (Unabridged)"
	books := []ShatterBook{
		sbD("f1", base+"/01.mp3", thirtyMin),
		sbD("f2", base+"/02.mp3", thirtyMin),
	}
	groups, _ := ClassifyShatteredFolders(books)
	g := findGroup(t, groups, "The Rising (Unabridged)")
	if g.Kind != KindAmbiguous {
		t.Fatalf("kind=%q, want %q", g.Kind, KindAmbiguous)
	}
	assertRec(t, g, ActionCombine)
	if g.RecommendationEvidence.MedianKnownSec != thirtyMin {
		t.Errorf("medianKnownSec=%d, want %d", g.RecommendationEvidence.MedianKnownSec, thirtyMin)
	}
}

// 5 prod holds. Two whole books and two fragments in one folder — neither side a
// majority, so the members disagree about what they are and a human must look.
func TestRecommend_Ambiguous_MixedRuntimes_InsufficientEvidence(t *testing.T) {
	base := shatterRoot + "/Various/Mixed Bag (Unabridged)/Mixed Bag (Unabridged)"
	books := []ShatterBook{
		sbD("m1", base+"/Prologue.mp3", twoHours),
		sbD("m2", base+"/Interlude.mp3", twoHours),
		sbD("m3", base+"/Epilogue.mp3", twentyMin),
		sbD("m4", base+"/Coda.mp3", twentyMin),
	}
	groups, _ := ClassifyShatteredFolders(books)
	g := findGroup(t, groups, "Mixed Bag (Unabridged)")
	if g.Kind != KindAmbiguous {
		t.Fatalf("kind=%q, want %q", g.Kind, KindAmbiguous)
	}
	assertRec(t, g, ActionInsufficientEvidence)
	if !strings.Contains(g.RecommendationReason, "disagree") {
		t.Errorf("reason should say the members disagree, got %q", g.RecommendationReason)
	}
	ev := g.RecommendationEvidence
	if ev.BookLengthMembers != 2 || ev.DurationsKnown != 4 {
		t.Errorf("evidence=%+v, want bookLengthMembers=2 durationsKnown=4", ev)
	}
}

// 56 prod holds. No member has a known runtime. The reason must say the EVIDENCE
// is missing — not that the members are unrelated, which would be a claim the
// classifier cannot support.
func TestRecommend_Ambiguous_NoDurations_InsufficientEvidence(t *testing.T) {
	base := shatterRoot + "/Ian Tregillis/The Rising (Unabridged)/The Rising (Unabridged)"
	books := []ShatterBook{
		sb("z1", base+"/01.mp3"),
		sb("z2", base+"/02.mp3"),
	}
	groups, _ := ClassifyShatteredFolders(books)
	g := findGroup(t, groups, "The Rising (Unabridged)")
	assertRec(t, g, ActionInsufficientEvidence)
	if !strings.Contains(g.RecommendationReason, "known runtime") {
		t.Errorf("reason should blame the missing runtimes, got %q", g.RecommendationReason)
	}
	if g.RecommendationEvidence.DurationsKnown != 0 {
		t.Errorf("durationsKnown=%d, want 0", g.RecommendationEvidence.DurationsKnown)
	}
}

// A single known runtime among four unknowns is not a majority and must not carry
// the group. This is the gap between "no evidence" and "enough evidence" — the
// literal rule "every member with a known duration is short" would combine here
// on one member's say-so.
func TestRecommend_MinorityDurationsKnown_InsufficientEvidence(t *testing.T) {
	base := shatterRoot + "/Various/Sparse Data (Unabridged)/Sparse Data (Unabridged)"
	books := []ShatterBook{
		sbD("s1", base+"/Prologue.mp3", twentyMin),
		sb("s2", base+"/Interlude.mp3"),
		sb("s3", base+"/Epilogue.mp3"),
		sb("s4", base+"/Coda.mp3"),
	}
	groups, _ := ClassifyShatteredFolders(books)
	g := findGroup(t, groups, "Sparse Data (Unabridged)")
	assertRec(t, g, ActionInsufficientEvidence)
	if g.RecommendedAction == ActionCombine {
		t.Fatal("one known short runtime must never carry a destructive combine")
	}
}

// 🔴 The membership itself is unconfirmed. Two DIFFERENT books under two different
// authors, glued into one group only because their original iTunes album tag
// collided. classifyGroup's first switch case already refuses to vouch for this
// grouping; a recommendation that looked only at runtimes answered "combine" here
// (both members are short), which would merge two unrelated books.
//
// Short runtimes are evidence about what the members ARE, not evidence that they
// belong together.
func TestRecommend_MergedViaItunes_NeverCombine(t *testing.T) {
	books := []ShatterBook{
		sbIT("mi1", shatterRoot+"/Author A/Book One/01.mp3", `W:\Audiobooks\Shared Album\01.mp3`),
		sbIT("mi2", shatterRoot+"/Author B/Book Two/01.mp3", `W:\Audiobooks\Shared Album\02.mp3`),
	}
	books[0].DurationSec = thirtyMin
	books[1].DurationSec = thirtyMin
	groups, _ := ClassifyShatteredFolders(books)
	if len(groups) != 1 {
		t.Fatalf("want 1 iTunes-glued group, got %d: %+v", len(groups), groups)
	}
	g := groups[0]
	if g.Kind != KindAmbiguous {
		t.Fatalf("kind=%q, want %q", g.Kind, KindAmbiguous)
	}
	if g.RecommendedAction == ActionCombine {
		t.Fatalf("recommended COMBINE for a grouping the classifier declined to vouch "+
			"for; reason was %q", g.RecommendationReason)
	}
	assertRec(t, g, ActionInsufficientEvidence)
	if !strings.Contains(g.RecommendationReason, "iTunes album") {
		t.Errorf("reason should name the iTunes-album glue, got %q", g.RecommendationReason)
	}
}

// ─── calibration table: multidisc holds ──────────────────────────────────────

// 121 prod holds. The real the-final-strife folder: 12 flat numbered files with a
// 0.32 h median. Genuine chapter fragments.
func TestRecommend_Multidisc_AllFragments_Combine(t *testing.T) {
	base := "/mnt/bigdata/books/newbooks/audiobooks/the-final-strife 01/the-final-strife"
	var books []ShatterBook
	for i := 1; i <= 12; i++ {
		books = append(books, sbD(fmt.Sprintf("tfs%02d", i),
			fmt.Sprintf("%s/the-final-strife-%02d.mp3", base, i), nineteenMin))
	}
	groups, _ := ClassifyShatteredFolders(books)
	g := findGroup(t, groups, "the-final-strife")
	if g.Kind != KindMultidisc || !g.Confident {
		t.Fatalf("kind=%q confident=%v, want multidisc/true", g.Kind, g.Confident)
	}
	assertRec(t, g, ActionCombine)
	ev := g.RecommendationEvidence
	if ev.Members != 12 || ev.DurationsKnown != 12 || ev.BookLengthMembers != 0 {
		t.Errorf("evidence=%+v, want 12 members / 12 known / 0 book-length", ev)
	}
	if ev.NumberedMembers != 12 || ev.Structure != "flat" {
		t.Errorf("evidence=%+v, want 12 numbered / flat structure", ev)
	}
	if g.SurvivorTitle != "the-final-strife" {
		t.Errorf("survivorTitle=%q, want 'the-final-strife'", g.SurvivorTitle)
	}
}

// 🔴 THE NEAR-MISS. 3 prod holds. The disc branch of classifyGroup never evaluates
// the series guard, so two whole 10.7 h novels in Disc 1 / Disc 2 folders come out
// as a CONFIDENT multidisc collapse. Approving that merges distinct novels through
// an apply path that hard-deletes the absorbed Book rows.
//
// The Kind assertion is as load-bearing as the action assertion: it proves the Kind
// switch was NOT perturbed by this change (design decision D1), which is the
// cheapest regression guard available here.
func TestRecommend_Multidisc_MajorityBookLength_Separate_NearMiss(t *testing.T) {
	base := shatterRoot + "/Jonathan Moeller/Sevenfold Sword"
	books := []ShatterBook{
		sbD("sw1", base+"/Disc 1/track.mp3", tenHours),
		sbD("sw2", base+"/Disc 2/track.mp3", tenHours),
	}
	groups, _ := ClassifyShatteredFolders(books)
	g := findGroup(t, groups, "Sevenfold Sword")
	if g.Kind != KindMultidisc || !g.Confident {
		t.Fatalf("kind=%q confident=%v, want multidisc/true — the Kind switch must be unchanged",
			g.Kind, g.Confident)
	}
	assertRec(t, g, ActionSeparate)
	if g.RecommendationEvidence.Structure != "disc" {
		t.Errorf("structure=%q, want disc", g.RecommendationEvidence.Structure)
	}
	if g.SurvivorTitle != "Sevenfold Sword" {
		t.Errorf("survivorTitle=%q, want 'Sevenfold Sword'", g.SurvivorTitle)
	}
}

// The same near-miss through the CHAPTER branch, which likewise never evaluates the
// series guard: `<Book>/<Book> - N/file` shells holding whole novels.
func TestRecommend_MultidiscChapterShells_BookLength_Separate(t *testing.T) {
	base := shatterRoot + "/Michael J. Sullivan/Riyria Revelations"
	var books []ShatterBook
	for i := 1; i <= 6; i++ {
		books = append(books, sbD(fmt.Sprintf("rr%d", i),
			fmt.Sprintf("%s/Riyria Revelations - %d/01.mp3", base, i), tenHours))
	}
	groups, _ := ClassifyShatteredFolders(books)
	g := findGroup(t, groups, "Riyria Revelations")
	if g.Kind != KindMultidisc || !g.Confident {
		t.Fatalf("kind=%q confident=%v, want multidisc/true", g.Kind, g.Confident)
	}
	assertRec(t, g, ActionSeparate)
}

// 🔴 THE SINGLE MOST IMPORTANT CASE. The 41-of-43 shape from the 2026-08-05 dry-run:
// an iTunes author folder of numbered sequels, flat, numbered, n >= 4, one title
// stem — a CONFIDENT multidisc collapse — with every runtime missing.
//
// membersAreBookLength counts unknown members as NOT book-length, so the series
// guard is inert here and the Kind stays "confident". The recommendation is the
// symmetric half of that rule: missing data must not fire a COLLAPSE either. This
// must be insufficient-evidence, NEVER combine.
func TestRecommend_ZeroDurationFlatNumbered_NeverCombine(t *testing.T) {
	base := shatterRoot + "/iTunes Media/Audiobooks/Rick Cole"
	books := []ShatterBook{
		sb("ss1", base+"/Super Sales on Super Heroes.mp3"),
		sb("ss2", base+"/Super Sales on Super Heroes 2.mp3"),
		sb("ss3", base+"/Super Sales on Super Heroes 3.mp3"),
		sb("ss4", base+"/Super Sales on Super Heroes 4.mp3"),
		sb("ss5", base+"/Super Sales on Super Heroes 5.mp3"),
		sb("ss6", base+"/Super Sales on Super Heroes 6.mp3"),
	}
	groups, _ := ClassifyShatteredFolders(books)
	g := findGroup(t, groups, "Rick Cole")
	if g.Kind != KindMultidisc || !g.Confident {
		t.Fatalf("kind=%q confident=%v, want multidisc/true (guard inert without runtimes)",
			g.Kind, g.Confident)
	}
	if g.RecommendedAction == ActionCombine {
		t.Fatalf("zero-duration group recommended COMBINE — an absent duration is not "+
			"evidence; reason was %q", g.RecommendationReason)
	}
	assertRec(t, g, ActionInsufficientEvidence)
}

// ─── calibration table: anthology and version-group holds ────────────────────

// 1 prod hold. An anthology marker on the folder plus distinct story titles = one
// published book with one ISBN; the members are short stories, not novels.
func TestRecommend_Anthology_Fragments_Combine(t *testing.T) {
	base := shatterRoot + "/Rod Serling/Twilight Zone Anthology"
	books := []ShatterBook{
		sbD("a1", base+"/01 - The Monsters.mp3", thirtyMin),
		sbD("a2", base+"/02 - Time Enough.mp3", thirtyMin),
		sbD("a3", base+"/03 - Nightmare.mp3", thirtyMin),
		sbD("a4", base+"/04 - The Howling Man.mp3", thirtyMin),
	}
	groups, _ := ClassifyShatteredFolders(books)
	g := findGroup(t, groups, "Twilight Zone Anthology")
	if g.Kind != KindAnthology {
		t.Fatalf("kind=%q, want %q", g.Kind, KindAnthology)
	}
	assertRec(t, g, ActionCombine)
	if g.RecommendationEvidence.DistinctStems != 4 {
		t.Errorf("distinctStems=%d, want 4", g.RecommendationEvidence.DistinctStems)
	}
}

// 1 prod hold. An Abridged + Unabridged pair whose members are each a whole 10.7 h
// novel.
//
// 🔴 THIS IS THE CASE THAT FORCED A FIFTH ACTION. Both editions are book-length, so
// the runtime rule alone answers ActionSeparate — true in the narrow sense (they are
// two distinct recordings) but operationally wrong: leaving them unlinked is exactly
// the state the hold exists to fix. And because step 4 dispatches on the CHOSEN
// action rather than on Kind, "separate" would make ApplyVersionGroup unreachable —
// the one apply path that destroys nothing would become the one that never runs.
//
// KindVersionGroup therefore overrides the runtime recommendation with
// ActionVersionGroup. Safe in a way the reverse would not be: the classifier reached
// this Kind only via explicit abridged/unabridged markers PLUS a dominant shared
// title, which is positive identity evidence runtime does not have.
func TestRecommend_VersionGroup_OverridesRuntimeSeparate(t *testing.T) {
	base := shatterRoot + "/Frank Herbert/Dune"
	books := []ShatterBook{
		sbD("v1", base+"/Dune (Unabridged)/01.mp3", tenHours),
		sbD("v2", base+"/Dune (Abridged)/01.mp3", tenHours),
	}
	groups, _ := ClassifyShatteredFolders(books)
	g := findGroup(t, groups, "Dune")
	if g.Kind != KindVersionGroup {
		t.Fatalf("kind=%q, want %q", g.Kind, KindVersionGroup)
	}
	assertRec(t, g, ActionVersionGroup)
	if g.SurvivorTitle != "Dune" {
		t.Errorf("survivorTitle=%q, want 'Dune'", g.SurvivorTitle)
	}
	// The evidence block must still carry the real runtimes: the override changes
	// the verdict, not the numbers the human is shown.
	if g.RecommendationEvidence.BookLengthMembers != 2 {
		t.Errorf("bookLengthMembers=%d, want 2 — evidence must survive the override",
			g.RecommendationEvidence.BookLengthMembers)
	}
}

// ─── deriveSurvivorTitle / survivorTitleFor regressions ──────────────────────

// The observed prod failure: a hold under the AUTHOR folder `C. T. Phipps` was
// labelled "C. T. Phipps". The members' own dominant stem names the book.
func TestSurvivorTitle_AuthorFolder_UsesMemberStem(t *testing.T) {
	base := shatterRoot + "/C. T. Phipps"
	var books []ShatterBook
	for i := 1; i <= 5; i++ {
		books = append(books, sbDA(fmt.Sprintf("ph%d", i),
			fmt.Sprintf("%s/Wraith Knight %02d.mp3", base, i), tenHours, "C. T. Phipps"))
	}
	groups, _ := ClassifyShatteredFolders(books)
	g := findGroup(t, groups, "C. T. Phipps")
	if g.SurvivorTitle == "C. T. Phipps" {
		t.Fatal("survivorTitle is the AUTHOR name — the bug this fix exists for")
	}
	if g.SurvivorTitle != "Wraith Knight" {
		t.Errorf("survivorTitle=%q, want 'Wraith Knight'", g.SurvivorTitle)
	}
	// Five whole novels in one author folder: separate, not combine.
	assertRec(t, g, ActionSeparate)
}

// The folder says "Vol. 01"; every member file says "Vol. 9". The folder carries a
// stale number, so the members win.
func TestSurvivorTitle_WrongVolumeInFolder_MembersWin(t *testing.T) {
	base := shatterRoot + "/Jim Butcher/The Dresden Files Vol. 01"
	var books []ShatterBook
	for i := 1; i <= 4; i++ {
		books = append(books, sbD(fmt.Sprintf("df%d", i),
			fmt.Sprintf("%s/The Dresden Files Vol. 9 - %02d.mp3", base, i), thirtyMin))
	}
	groups, _ := ClassifyShatteredFolders(books)
	g := findGroup(t, groups, "The Dresden Files Vol. 01")
	if g.SurvivorTitle != "The Dresden Files Vol. 9" {
		t.Errorf("survivorTitle=%q, want 'The Dresden Files Vol. 9' (members carry the right volume)",
			g.SurvivorTitle)
	}
}

// The Coraline shape: the FOLDER is the book and the files are generically named
// ("01 - Chapter.mp3"). Preferring the member stem unconditionally would label this
// group "Chapter" — a fresh instance of the bug being fixed. The generic-title
// guard sends it back to the folder name.
func TestSurvivorTitle_GenericMemberStem_FallsBackToFolder(t *testing.T) {
	base := shatterRoot + "/Neil Gaiman/Coraline"
	var books []ShatterBook
	for i := 1; i <= 8; i++ {
		books = append(books, sbD(fmt.Sprintf("co%d", i),
			fmt.Sprintf("%s/%02d - Chapter.mp3", base, i), twentyMin))
	}
	groups, _ := ClassifyShatteredFolders(books)
	g := findGroup(t, groups, "Coraline")
	if g.SurvivorTitle != "Coraline" {
		t.Errorf("survivorTitle=%q, want 'Coraline' (generic stem must not win)", g.SurvivorTitle)
	}
}

// Direct table test on the selector, covering the guards that are hard to reach
// end-to-end (a group must EMIT a hold to be observable, and the worst titles come
// from folders that emit for unrelated reasons).
func TestSurvivorTitleFor_Table(t *testing.T) {
	cases := []struct {
		name         string
		folderName   string
		namedAfter   bool
		stem         string
		stemMajority bool
		authorNorm   string
		want         string
	}{
		{
			name: "folder named after book wins", folderName: "Cage of Souls (Unabridged)",
			namedAfter: true, stem: "Cage of Souls", stemMajority: true, want: "Cage of Souls",
		},
		{
			name: "bare ordinal folder is rejected", folderName: "Volume 1",
			namedAfter: true, stem: "Volume 1", stemMajority: true, want: "",
		},
		{
			name:       "author name folder with no usable stem is rejected",
			folderName: "C. T. Phipps", namedAfter: false, stem: "", stemMajority: false,
			authorNorm: normTitle("C. T. Phipps"), want: "",
		},
		{
			name:       "author name is rejected even when the stem agrees",
			folderName: "C. T. Phipps", namedAfter: false, stem: "C. T. Phipps",
			stemMajority: true, authorNorm: normTitle("C. T. Phipps"), want: "",
		},
		{
			name: "generic stem falls back to the folder", folderName: "Coraline",
			namedAfter: false, stem: "Chapter", stemMajority: true, want: "Coraline",
		},
		{
			name: "bare number stem falls back to the folder", folderName: "Coraline",
			namedAfter: false, stem: "07", stemMajority: true, want: "Coraline",
		},
		{
			name:       "stale folder volume loses to the members",
			folderName: "The Dresden Files Vol. 01", namedAfter: false,
			stem: "The Dresden Files Vol. 9", stemMajority: true,
			want: "The Dresden Files Vol. 9",
		},
		{
			name:       "a plurality stem is not a majority, folder used",
			folderName: "Assorted Works", namedAfter: false, stem: "One Odd Book",
			stemMajority: false, want: "Assorted Works",
		},
		{
			name: "nothing trustworthy yields empty", folderName: "Disc 3",
			namedAfter: false, stem: "Track", stemMajority: true, want: "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := survivorTitleFor(tc.folderName, tc.namedAfter, tc.stem, tc.stemMajority, tc.authorNorm)
			if got != tc.want {
				t.Errorf("survivorTitleFor(%q, %v, %q, %v, %q) = %q, want %q",
					tc.folderName, tc.namedAfter, tc.stem, tc.stemMajority, tc.authorNorm, got, tc.want)
			}
		})
	}
}

// The median must ignore unknown (zero) runtimes: including them would make the
// reason sentence quote a number no member actually has.
func TestGatherEvidence_MedianExcludesUnknowns(t *testing.T) {
	members := []memberInfo{
		{book: ShatterBook{DurationSec: 0}},
		{book: ShatterBook{DurationSec: 0}},
		{book: ShatterBook{DurationSec: 600}},
		{book: ShatterBook{DurationSec: 1200}},
		{book: ShatterBook{DurationSec: 6000}},
	}
	ev := gatherEvidence(members, 1, 5, "flat")
	if ev.DurationsKnown != 3 {
		t.Errorf("durationsKnown=%d, want 3", ev.DurationsKnown)
	}
	if ev.MedianKnownSec != 1200 {
		t.Errorf("medianKnownSec=%d, want 1200 (zeros excluded)", ev.MedianKnownSec)
	}
	if ev.LongestKnownSec != 6000 {
		t.Errorf("longestKnownSec=%d, want 6000", ev.LongestKnownSec)
	}
	if ev.BookLengthMembers != 1 {
		t.Errorf("bookLengthMembers=%d, want 1", ev.BookLengthMembers)
	}
}

// duplicate-of is part of the closed vocabulary but nothing in this package emits
// it — deciding "this folder duplicates an existing book" needs cross-book evidence
// classifyGroup does not have. Pin that so a future change is deliberate.
func TestRecommend_NeverEmitsDuplicateOf(t *testing.T) {
	base := shatterRoot + "/Various/Whatever"
	books := []ShatterBook{
		sbD("dz1", base+"/01 - One.mp3", thirtyMin),
		sbD("dz2", base+"/02 - Two.mp3", thirtyMin),
		sbD("dz3", base+"/03 - Three.mp3", tenHours),
		sbD("dz4", base+"/04 - Four.mp3", thirtyMin),
	}
	groups, _ := ClassifyShatteredFolders(books)
	for _, g := range groups {
		if g.RecommendedAction == ActionDuplicateOf {
			t.Errorf("classifyGroup emitted %q, which it has no evidence for", ActionDuplicateOf)
		}
	}
}
