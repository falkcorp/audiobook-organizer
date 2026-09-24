// file: internal/versionprimary/rank_test.go
// version: 1.0.0
// guid: 18620ce0-4bca-4216-a65a-507cf54533a0
// last-edited: 2026-09-24

package versionprimary

import (
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

func strp(s string) *string { return &s }
func intp(i int) *int       { return &i }
func boolp(b bool) *bool    { return &b }

// organizedBook is an eligible-looking row; signals decide eligibility.
func organizedBook(id string, created time.Time) *database.Book {
	c := created
	return &database.Book{ID: id, Title: "T " + id, LibraryState: strp("organized"), CreatedAt: &c}
}

// good is the signals of a member ABS can show.
func good(tier int) Signals {
	s := Signals{Live: true, ActiveFiles: 1, AllUnderRoot: true}
	switch tier {
	case TierM4BChapters:
		s.SingleM4B, s.Chapters, s.ChapterSource = true, 12, ChapterSourceProbe
	case TierM4BNoChapters:
		s.SingleM4B, s.Chapters, s.ChapterSource = true, 1, ChapterSourceProbe
	case TierOther:
		s.ActiveFiles = 3
	}
	return s
}

var t0 = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

func richMetadata(b *database.Book) *database.Book {
	b.AuthorID = intp(7)
	b.SeriesID = intp(3)
	b.Description = strp("a description")
	b.Narrator = strp("A Narrator")
	b.ASIN = strp("B000TEST00")
	return b
}

func TestTier_OrderingAndInputs(t *testing.T) {
	cases := []struct {
		name string
		s    Signals
		want int
	}{
		{"no active files", Signals{}, TierNone},
		{"file missing on disk", Signals{ActiveFiles: 1, FilesMissingOnDisk: 1, SingleM4B: true, Chapters: 9, ChapterSource: ChapterSourceProbe}, TierNone},
		{"multi file", Signals{ActiveFiles: 2}, TierOther},
		{"single mp3", Signals{ActiveFiles: 1}, TierOther},
		{"m4b 0 chapters", Signals{ActiveFiles: 1, SingleM4B: true, ChapterSource: ChapterSourceProbe}, TierM4BNoChapters},
		{"m4b 1 chapter", Signals{ActiveFiles: 1, SingleM4B: true, Chapters: 1, ChapterSource: ChapterSourceProbe}, TierM4BNoChapters},
		{"m4b 2 chapters", Signals{ActiveFiles: 1, SingleM4B: true, Chapters: 2, ChapterSource: ChapterSourceTable}, TierM4BChapters},
		{"m4b unknown", Signals{ActiveFiles: 1, SingleM4B: true, ChapterSource: ChapterSourceUnknown}, TierM4BNoChapters},
	}
	for _, tc := range cases {
		if got := Tier(tc.s); got != tc.want {
			t.Errorf("%s: Tier = %d, want %d", tc.name, got, tc.want)
		}
	}
}

// Each tier beats the one below it, whatever the metadata says.
func TestElect_TierBeatsMetadata(t *testing.T) {
	for _, pair := range [][2]int{{TierM4BChapters, TierM4BNoChapters}, {TierM4BNoChapters, TierOther}} {
		hi := organizedBook("B", t0.Add(time.Hour))
		lo := richMetadata(organizedBook("A", t0))
		d := Elect([]Member{{Book: lo, Signals: good(pair[1])}, {Book: hi, Signals: good(pair[0])}})
		if d.Kind != DecisionElect || d.WinnerID != "B" {
			t.Fatalf("tier %d vs %d: got %+v, want B elected", pair[0], pair[1], d)
		}
		if d.MetadataBestID != "A" {
			t.Fatalf("metadata best = %q, want A (the richer row donates)", d.MetadataBestID)
		}
	}
}

// Inside one tier the metadata score decides, not creation order.
func TestElect_MetadataBreaksTiesInsideATier(t *testing.T) {
	older := organizedBook("A", t0)
	newer := richMetadata(organizedBook("B", t0.Add(time.Hour)))
	d := Elect([]Member{{Book: older, Signals: good(TierM4BChapters)}, {Book: newer, Signals: good(TierM4BChapters)}})
	if d.WinnerID != "B" {
		t.Fatalf("winner = %q, want B (better metadata, same tier)", d.WinnerID)
	}
	if d.MetadataBestID != "B" {
		t.Fatalf("metadata best = %q, want B", d.MetadataBestID)
	}
}

// Members ABS cannot show never win over an eligible member, even with a
// better tier... except that a better tier outside the library HOLDS the
// group, which the next test covers. Here the ineligible rows have the SAME
// tier and better metadata.
func TestElect_IneligibleMembersNeverWin(t *testing.T) {
	lib := organizedBook("LIB", t0.Add(time.Hour))

	src := richMetadata(organizedBook("SRC", t0))
	src.LibraryState = strp("organized_source")
	imp := richMetadata(organizedBook("IMP", t0))
	imp.LibraryState = strp("imported")
	miss := richMetadata(organizedBook("MISS", t0))
	missSig := good(TierM4BChapters)
	missSig.FilesMissingOnDisk = 1
	outside := richMetadata(organizedBook("OUT", t0))
	outSig := good(TierM4BChapters)
	outSig.AllUnderRoot = false
	noFiles := richMetadata(organizedBook("NOF", t0))
	merged := richMetadata(organizedBook("MRG", t0))

	d := Elect([]Member{
		{Book: lib, Signals: good(TierM4BChapters)},
		{Book: src, Signals: good(TierM4BChapters)},
		{Book: imp, Signals: good(TierM4BChapters)},
		{Book: miss, Signals: missSig},
		{Book: outside, Signals: outSig},
		{Book: noFiles, Signals: Signals{Live: true}},
		{Book: merged, Signals: func() Signals { s := good(TierM4BChapters); s.Live = false; return s }()},
	})
	if d.Kind != DecisionElect || d.WinnerID != "LIB" {
		t.Fatalf("got %+v, want LIB elected", d)
	}
	reasons := map[string]string{}
	for _, m := range d.Members {
		reasons[m.BookID] = m.Ineligible
	}
	want := map[string]string{
		"LIB": "", "SRC": IneligibleNotOrganized, "IMP": IneligibleNotOrganized,
		"MISS": IneligibleFilesMissing, "OUT": IneligibleOutsideLibrary,
		"NOF": IneligibleNoActiveFiles, "MRG": IneligibleNotLive,
	}
	for id, r := range want {
		if reasons[id] != r {
			t.Errorf("%s ineligible reason = %q, want %q", id, reasons[id], r)
		}
	}
}

// An Unknown Author path is eligible but loses to any member with a real
// author path in the same tier, even one with less metadata.
func TestElect_UnknownAuthorPathLosesInsideATier(t *testing.T) {
	ua := richMetadata(organizedBook("UA", t0))
	uaSig := good(TierM4BChapters)
	uaSig.UnknownAuthorPath = true
	plain := organizedBook("PL", t0.Add(time.Hour))
	d := Elect([]Member{{Book: ua, Signals: uaSig}, {Book: plain, Signals: good(TierM4BChapters)}})
	if d.WinnerID != "PL" {
		t.Fatalf("winner = %q, want PL", d.WinnerID)
	}
	// Alone, it is still crowned: a penalty is not an ineligibility.
	d = Elect([]Member{{Book: ua, Signals: uaSig}})
	if d.WinnerID != "UA" {
		t.Fatalf("lone Unknown Author member: winner = %q, want UA", d.WinnerID)
	}
}

// The organized_source copy has chapters and the library copy does not:
// HELD, not crowned with the worse copy.
func TestElect_HeldWhenBestTierIsOnlyOutsideTheLibrary(t *testing.T) {
	src := organizedBook("SRC", t0)
	src.LibraryState = strp("organized_source")
	lib := organizedBook("LIB", t0.Add(time.Hour))
	d := Elect([]Member{{Book: src, Signals: good(TierM4BChapters)}, {Book: lib, Signals: good(TierM4BNoChapters)}})
	if d.Kind != DecisionHeld || d.HoldReason != HoldBetterCopyNotInLibrary || d.WinnerID != "" {
		t.Fatalf("got %+v, want held better_copy_not_in_library", d)
	}
	if d.BestTier != TierM4BChapters || d.BestEligibleTier != TierM4BNoChapters {
		t.Fatalf("tiers = %d/%d", d.BestTier, d.BestEligibleTier)
	}
	// A non-LIVE better copy (merged away) does not hold the group.
	src.MergedIntoBookID = strp("LIB")
	srcSig := good(TierM4BChapters)
	srcSig.Live = false
	d = Elect([]Member{{Book: src, Signals: srcSig}, {Book: lib, Signals: good(TierM4BNoChapters)}})
	if d.Kind != DecisionElect || d.WinnerID != "LIB" {
		t.Fatalf("merged-away better copy: got %+v, want LIB elected", d)
	}
}

func TestElect_HeldWhenNoMemberIsEligible(t *testing.T) {
	imp := organizedBook("IMP", t0)
	imp.LibraryState = strp("imported")
	d := Elect([]Member{{Book: imp, Signals: good(TierOther)}})
	if d.Kind != DecisionHeld || d.HoldReason != HoldNeedsOrganizeOrRestore {
		t.Fatalf("got %+v, want held needs_organize_or_restore", d)
	}
}

// Applying the decision and electing again changes nothing.
func TestElect_ReRunIsStable(t *testing.T) {
	a := organizedBook("A", t0)
	b := organizedBook("B", t0.Add(time.Hour))
	members := []Member{{Book: a, Signals: good(TierM4BChapters)}, {Book: b, Signals: good(TierM4BChapters)}}
	d := Elect(members)
	if d.Kind != DecisionElect || d.WinnerID != "A" {
		t.Fatalf("first run: %+v", d)
	}
	a.IsPrimaryVersion, b.IsPrimaryVersion = boolp(true), boolp(false)
	d2 := Elect(members)
	if d2.Kind != DecisionAlreadyOK || d2.WinnerID != "A" {
		t.Fatalf("second run: %+v, want already_ok A", d2)
	}
	// A nil flag on the loser is not OK: readers disagree on nil.
	b.IsPrimaryVersion = nil
	if d3 := Elect(members); d3.Kind != DecisionElect {
		t.Fatalf("nil loser flag: %+v, want elect", d3)
	}
}

// With everything else equal the current explicit primary keeps the crown,
// so a re-run never flips between equal copies.
func TestElect_IncumbentWinsExactTie(t *testing.T) {
	a := organizedBook("A", t0)
	b := organizedBook("B", t0.Add(time.Hour))
	b.IsPrimaryVersion = boolp(true)
	d := Elect([]Member{{Book: a, Signals: good(TierM4BChapters)}, {Book: b, Signals: good(TierM4BChapters)}})
	if d.WinnerID != "B" {
		t.Fatalf("winner = %q, want incumbent B", d.WinnerID)
	}
}

func TestElect_RuntimeAndBitrateBreakMetadataTies(t *testing.T) {
	a := organizedBook("A", t0)
	b := organizedBook("B", t0.Add(time.Hour))
	a.AudibleRuntimeMin, b.AudibleRuntimeMin = intp(600), intp(600)
	sa, sb := good(TierM4BChapters), good(TierM4BChapters)
	sa.RuntimeSec = 600 * 60 * 90 / 100 // 10% short
	sb.RuntimeSec = 600*60 + 300        // within 2%
	if d := Elect([]Member{{Book: a, Signals: sa}, {Book: b, Signals: sb}}); d.WinnerID != "B" {
		t.Fatalf("runtime match: winner = %q, want B", d.WinnerID)
	}
	sa.RuntimeSec, sb.RuntimeSec = 0, 0
	sa.BitrateKbps, sb.BitrateKbps = 64, 128
	if d := Elect([]Member{{Book: a, Signals: sa}, {Book: b, Signals: sb}}); d.WinnerID != "B" {
		t.Fatalf("bitrate: winner = %q, want B", d.WinnerID)
	}
}

func TestMetadataScore_MatchesDedupWeights(t *testing.T) {
	b := &database.Book{}
	if MetadataScore(b) != 0 {
		t.Fatalf("empty row scored %d", MetadataScore(b))
	}
	full := richMetadata(&database.Book{})
	full.Duration = intp(1)
	full.ITunesPersistentID = strp("X")
	full.Publisher, full.Language, full.Genre, full.CoverURL = strp("p"), strp("l"), strp("g"), strp("c")
	if got := MetadataScore(full); got != 100+20+10+5+5+10+10+3+2+2+3 {
		t.Fatalf("full row scored %d", got)
	}
	// No created-at term: two rows differing only in CreatedAt score the same.
	x, y := organizedBook("X", t0), organizedBook("Y", t0.Add(1000*time.Hour))
	if MetadataScore(x) != MetadataScore(y) {
		t.Fatal("MetadataScore must not depend on CreatedAt")
	}
}

func TestPlanAndApplyCarryOver_FillsOnlyEmptyFields(t *testing.T) {
	winner := &database.Book{ID: "W", Description: strp("keep me"), Narrator: strp("  ")}
	donor := &database.Book{ID: "D", Description: strp("donor desc"), Narrator: strp("Donor Narrator"),
		Publisher: strp("Pub"), AuthorID: intp(4), ITunesPersistentID: strp("PID"), Duration: intp(99)}
	p := PlanCarryOver(winner, donor)
	fills := map[string]string{}
	for _, f := range p.Fills {
		fills[f.Field] = f.Value
	}
	if len(fills) != 2 || fills["narrator"] != "Donor Narrator" || fills["publisher"] != "Pub" {
		t.Fatalf("fills = %v", fills)
	}
	if len(p.Conflicts) != 1 || p.Conflicts[0].Field != "description" {
		t.Fatalf("conflicts = %+v", p.Conflicts)
	}
	if len(p.NotCarried) != 1 || p.NotCarried[0] != "author_id=4" {
		t.Fatalf("not carried = %v", p.NotCarried)
	}
	got := ApplyCarryOver(winner, donor)
	if len(got) != 2 || *winner.Description != "keep me" || *winner.Narrator != "Donor Narrator" || *winner.Publisher != "Pub" {
		t.Fatalf("applied %v -> %+v", got, winner)
	}
	if winner.ITunesPersistentID != nil || winner.Duration != nil || winner.AuthorID != nil {
		t.Fatal("file-identity and join-backed fields must never be carried")
	}
}
