// file: internal/plugins/maintenance/junk_author_fixer_targets_test.go
// version: 1.1.0
// guid: 5b0e6c1f-8d0a-4c55-9f7e-2a61d3c4b9e8
// last-edited: 2026-09-29

package maintenance

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/repairs"
)

// The junk-author trial of 2026-09-29 (plan op 01M3QT6HZ6843PSZCZ4JNSBYPC)
// proposed 203 clearly-bad applicable relinks whose target passed every junk
// test: the book's narrator, a book or series title, filename / encoder
// shrapnel, a fragment of a publisher, an "Author (Narrator)" credit. They
// reduce to 21 (target, source) shapes; each is replayed here as a fixture
// (not the prod file), and none may be proposed. A refused row stays on the
// plan (held or needs_manual), never vanishes.

// junkBadShape is one (target, source) shape of the trial's bad rows.
type junkBadShape struct {
	name   string // "<bad target> / <source>" as in the trial
	rows   int    // bad applicable rows of this shape in the trial
	junk   string // the junk author the book is credited to
	title  string
	bad    string // the target the trial proposed
	want   string // the target now expected ("" = none proposed)
	build  func(f *junkFixture, s junkBadShape) string
	reason string // substring the held row's skip reason must carry ("" = any)
}

// sibling adds a book credited to author in the version group / series /
// folder of the shape's book.
func vgShape(sibTitle string) func(f *junkFixture, s junkBadShape) string {
	return func(f *junkFixture, s junkBadShape) string {
		vg := "vg-" + s.name
		f.book(junkBookSpec{title: sibTitle, path: "/lib/sib/" + s.name, author: s.bad, vg: vg})
		return f.book(junkBookSpec{title: s.title, path: "/lib/junk/" + s.name, author: s.junk, vg: vg})
	}
}

func folderShape(sibTitle string) func(f *junkFixture, s junkBadShape) string {
	return func(f *junkFixture, s junkBadShape) string {
		dir := "/lib/pzg-" + strings.ReplaceAll(s.name, " ", "")
		f.book(junkBookSpec{title: sibTitle, path: dir + "/sib.m4b", author: s.bad})
		return f.book(junkBookSpec{title: s.title, path: dir + "/junk.m4b", author: s.junk})
	}
}

func seriesShape(seriesName, sibTitle string) func(f *junkFixture, s junkBadShape) string {
	return func(f *junkFixture, s junkBadShape) string {
		f.mkSeries(seriesName, s.bad)
		f.book(junkBookSpec{title: sibTitle, path: "/lib/sib/" + s.name, author: s.bad, series: seriesName})
		return f.book(junkBookSpec{title: s.title, path: "/lib/junk/" + s.name, author: s.junk, series: seriesName})
	}
}

func tagShape(f *junkFixture, s junkBadShape) string {
	return f.book(junkBookSpec{title: s.title, path: "/lib/junk/" + s.name, author: s.junk, tags: map[string]string{"artist": s.bad}})
}

func junkBadShapes() []junkBadShape {
	return []junkBadShape{
		// Class 2: a book or series title.
		{name: "Theft of Swords / version_group", rows: 54, junk: "Rise of Empire 33-54", title: "Rise of Empire 33-54",
			bad: "Theft of Swords", build: vgShape("Theft of Swords"), reason: "title in the library"},
		{name: "Night of the Hunter / version_group", rows: 25, junk: "read by narrator", title: "Unknown Author",
			bad: "Night of the Hunter", build: vgShape("Night of the Hunter"), reason: "title in the library"},
		{name: "Shadows of Self / version_group", rows: 7, junk: "Shadows of Self 15-29", title: "Shadows of Self 15-29",
			bad: "Shadows of Self", build: vgShape("Shadows of Self 01-14"), reason: "own title"},
		{name: "Is It Wrong to Try to Pick / same_folder", rows: 5, junk: "read by narrator",
			title: "[PZG] Is It Wrong to Try to Pick Up Girls in a Dungeon, Vol. 01",
			bad:   "Is It Wrong to Try to Pick", build: folderShape("Vol 02"), reason: "own title"},
		{name: "Is It Wrong to Try to Pick / version_group", rows: 1, junk: "read by narrator",
			title: "Is It Wrong to Try to Pick Up Girls in a Dungeon, Vol. 03",
			bad:   "Is It Wrong to Try to Pick", build: vgShape("Vol 03 copy"), reason: "own title"},
		{name: "Is It Wrong to Try to Pick Up Girls / same_series", rows: 1, junk: "read by narrator",
			title: "Is It Wrong to Try to Pick Up Girls in a Dungeon, Vol. 01",
			bad:   "Is It Wrong to Try to Pick Up Girls", build: seriesShape("DanMachi", "Vol 2"), reason: "own title"},
		{name: "Hero of Another World / same_series", rows: 2, junk: "Hero of Another World_ Summoner of Legend 2",
			title: "Hero of Another World_ Summoner of Legend 2",
			bad:   "Hero of Another World", build: seriesShape("Summoner of Legend", "Summoner 1"), reason: "own title"},
		{name: "Skirmishes / version_group", rows: 1, junk: "- Skirmishes", title: "read by narrator",
			bad: "Skirmishes", build: vgShape("Skirmishes"), reason: "title in the library"},
		{name: "Nucleus / version_group", rows: 1, junk: "Syl_ Nucleus", title: "Syl_ Nucleus - Unknown Author",
			bad: "Nucleus", build: vgShape("Syl 2"), reason: "own title"},
		{name: "Stormcaller / version_group", rows: 1, junk: "Successor of Kukulkan_ Stormcaller",
			title: "Successor of Kukulkan_ Stormcaller", bad: "Stormcaller", build: vgShape("Kukulkan 1"), reason: "own title"},
		// alerts: a sound-effect file credited to its own title, like the
		// junk "click" it sits beside. Caught only because its book is titled
		// "alerts"; the name alone is a lowercase word like "randombluecat".
		{name: "alerts / version_group", rows: 1, junk: "click", title: "click",
			bad: "alerts", build: vgShape("alerts"), reason: "title in the library"},

		// Class 3: fragments and shrapnel.
		{name: "Simon / version_group", rows: 3, junk: "Simon & Schuster", title: "Star Trek - Coda [02] The Ashes Of Tomorrow",
			bad: "Simon", build: vgShape("Coda 2"), reason: "fragment of the junk name"},
		{name: "Graphic Audio [Jon Scieszka / version_group", rows: 1, junk: "Graphic Audio [Jon Scieszka / Steven Weinberg]",
			// Jon Scieszka has no author row (as in prod, or the junk name's
			// own cleaned "Jon Scieszka" would have won): the cut-off sibling
			// credit is junk now and resolves to nobody.
			title: "AstroNuts 02 Mission Two", bad: "Graphic Audio [Jon Scieszka", build: vgShape("AstroNuts 01")},
		{name: "lavf-fate / version_group", rows: 2, junk: "read by narrator", title: "vp3",
			bad: "lavf-fate", build: vgShape("vp3 copy")},
		{name: "chap-26-NOTES-1 / same_series", rows: 1, junk: "chap-01", title: "chap-01",
			bad: "chap-26-NOTES-1", build: seriesShape("Chapters", "chap-26")},
		{name: "Richard.Phillips-the.Rho.Agenda-Once.Dead.Nmr.64.Kbps / version_group", rows: 1, junk: "read by narrator",
			title: "Once Dead- A Rho Agenda Novel", bad: "Richard.Phillips-the.Rho.Agenda-Once.Dead.Nmr.64.Kbps", build: vgShape("Once Dead")},
		{name: "zzJim Butcher / version_group", rows: 2, junk: "CD_ Intro", title: "CD_ Intro",
			bad: "zzJim Butcher", want: "Jim Butcher", build: vgShape("CD_ Intro copy")},
		{name: "zzJim Butcher / file_tags", rows: 1, junk: "read by narrator", title: "Changes",
			bad: "zzJim Butcher", want: "Jim Butcher", build: tagShape},

		// Class 4: "Author (Narrator)".
		{name: "Kevin Hearne (Luke Daniels) / file_tags", rows: 3, junk: "- Intro", title: "Hounded",
			bad: "Kevin Hearne (Luke Daniels)", want: "Kevin Hearne", build: tagShape},
		{name: "Kevin Hearne (Christopher Ragland) / file_tags", rows: 2, junk: "Chapter 30-Acknowledgements", title: "Trapped",
			bad: "Kevin Hearne (Christopher Ragland)", want: "Kevin Hearne", build: tagShape},
	}
}

// TestJunkAuthorFixer_TrialBadTargetsNeverProposed replays the trial's bad
// shapes (classes 2-4; class 1, the narrator, has its own test below).
func TestJunkAuthorFixer_TrialBadTargetsNeverProposed(t *testing.T) {
	f := newJunkFixture(t)
	// Real authors the fixed rows resolve to.
	f.book(junkBookSpec{title: "Storm Front", path: "/lib/Jim Butcher/Storm Front", author: "Jim Butcher"})
	f.book(junkBookSpec{title: "Tricked", path: "/lib/Kevin Hearne/Tricked", author: "Kevin Hearne"})
	shapes := junkBadShapes()
	books := map[string]string{}
	for _, s := range shapes {
		books[s.name] = s.build(f, s)
	}
	plan := f.plan()
	for _, s := range shapes {
		t.Run(s.name, func(t *testing.T) {
			r := f.row(plan, s.junk, books[s.name])
			require.NotNil(t, r, "the junk row must stay on the plan")
			require.NotEqual(t, s.bad, r.Proposed["author"], "bad target proposed: %s", r.Reason)
			if s.want != "" {
				require.Empty(t, r.Skipped, r.SkipReason)
				require.Equal(t, s.want, r.Proposed["author"], r.Reason)
				return
			}
			applicable := r.Skipped == "" && r.Proposed["decision"] != junkAuthorDecUnlink
			require.False(t, applicable, "a relink was proposed: %v %s", r.Proposed, r.Reason)
			if s.reason != "" {
				require.Equal(t, junkAuthorSkipAmbiguous, r.Skipped, "a refused row is held, not unlinked: %v %s", r.Proposed, r.Reason)
				require.Contains(t, r.SkipReason, s.bad)
				require.Contains(t, r.SkipReason, s.reason)
			}
		})
	}
}

// Class 1: "Jennsen, GS_ 08 Rubicon (Amaranthe 08)" -> "Pyper Down", the
// narrator, from the folder parser (88 rows). The narrator is known from the
// book's file tags, or only from the library (she narrates more books than
// she is credited with). Either way she is refused and the row held; with
// "G. S. Jennsen" in the library the junk name's own surname-first head wins.
func TestJunkAuthorFixer_TrialNarratorFromPathNeverProposed(t *testing.T) {
	const junk = "Jennsen, GS_ 08 Rubicon (Amaranthe 08)"
	cases := []struct {
		name  string
		setup func(f *junkFixture) map[string]string
		want  string
	}{
		{name: "narrator in the file tags", setup: func(*junkFixture) map[string]string {
			return map[string]string{"narrator": "Pyper Down"}
		}},
		{name: "narrator in the file performer tag", setup: func(*junkFixture) map[string]string {
			return map[string]string{"PERFORMER": "Pyper Down"}
		}},
		{name: "narrator only in the library", setup: func(f *junkFixture) map[string]string {
			for i := range 3 {
				f.book(junkBookSpec{title: fmt.Sprintf("Other %d", i), path: fmt.Sprintf("/lib/o/%d", i), author: "Ann Other", narrator: "Pyper Down"})
			}
			return nil
		}},
		{name: "author known: cleaned surname-first name", want: "G. S. Jennsen", setup: func(f *junkFixture) map[string]string {
			f.book(junkBookSpec{title: "Starshine", path: "/lib/G. S. Jennsen/Starshine", author: "G. S. Jennsen", narrator: "Pyper Down"})
			return nil
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newJunkFixture(t)
			// Pyper Down is an author row with a (mis-credited) book of her own:
			// the old existing-author exception let her through.
			f.book(junkBookSpec{title: "Voice Work", path: "/lib/v/1", author: "Pyper Down"})
			tags := c.setup(f)
			id := f.book(junkBookSpec{title: "8-06 Rubicon (Amaranthe 08) — 06", path: "/lib/Pyper Down/Amaranthe 08 Rubicon", author: junk, tags: tags})
			r := f.row(f.plan(), junk, id)
			require.NotNil(t, r)
			require.NotEqual(t, "Pyper Down", r.Proposed["author"], r.Reason)
			if c.want != "" {
				require.Empty(t, r.Skipped, r.SkipReason)
				require.Equal(t, c.want, r.Proposed["author"], r.Reason)
				require.Equal(t, junkAuthorSrcCleaned, r.Proposed["source"])
				return
			}
			require.Equal(t, junkAuthorSkipAmbiguous, r.Skipped, "held, not unlinked: %v %s", r.Proposed, r.Reason)
			require.Contains(t, r.SkipReason, "Pyper Down")
		})
	}
}

// A sibling naming the book's narrator is refused even when the narrator has
// author books of their own ("[PZG]" rips: 146 such trial rows went to Suzie
// Yeung, Travis Baldree, Bryce Papenbrook...). Tags and the provider keep
// the self-narrated exception (TestJunkAuthorFixer_SelfNarratedIsRelinked).
func TestJunkAuthorFixer_SiblingNarratorIsRefused(t *testing.T) {
	f := newJunkFixture(t)
	f.book(junkBookSpec{title: "Dungeon Crawler Carl", path: "/lib/tb/dcc", author: "Travis Baldree"})
	f.book(junkBookSpec{title: "Vol 01", path: "/lib/sib/1", author: "Travis Baldree", vg: "vg-pzg"})
	id := f.book(junkBookSpec{title: "Vol 01 copy", path: "/lib/junk/1", author: "read by narrator", narrator: "Travis Baldree", vg: "vg-pzg"})
	r := f.row(f.plan(), "read by narrator", id)
	require.NotNil(t, r)
	require.NotEqual(t, "Travis Baldree", r.Proposed["author"], r.Reason)
	require.Equal(t, junkAuthorSkipAmbiguous, r.Skipped, r.Reason)
	require.Contains(t, r.SkipReason, "the book's narrator")
}

// Real authors and pen names the new refusals must not touch.
func TestJunkAuthorFixer_TrialRealTargetsStillProposed(t *testing.T) {
	f := newJunkFixture(t)
	f.book(junkBookSpec{title: "The Primal Hunter", path: "/lib/Zogarth/tph1", author: "Zogarth"})
	zog := f.book(junkBookSpec{title: "The Primal Hunter 9 - A LitRPG Adventure", path: "/lib/j/zog", author: "A LitRPG Adventure"})
	f.setProvider(zog, "The Primal Hunter 9 - A LitRPG Adventure", "Zogarth")

	f.book(junkBookSpec{title: "Reborn as a Space Mercenary 2", path: "/lib/r/2", author: "Ryuto", vg: "vg-ryuto"})
	ryu := f.book(junkBookSpec{title: "Reborn as a Space Mercenary", path: "/lib/j/ryu", author: "read by narrator", vg: "vg-ryuto"})

	f.mkSeries("The Last Legend Reborn", "Borgy60")
	f.book(junkBookSpec{title: "The Last Legend Reborn, Book 1", path: "/lib/b/1", author: "Borgy60", series: "The Last Legend Reborn"})
	bor := f.book(junkBookSpec{title: "The_Last_Legend_Reborn,_Book_2_by_Borgy60", path: "/lib/j/bor", author: "read by narrator",
		series: "The Last Legend Reborn"})

	// A series row named after a real author (swapped fields in prod).
	f.mkSeries("Brent Weeks", "Brent Weeks")
	f.book(junkBookSpec{title: "The Black Prism", path: "/lib/bw/1", author: "Brent Weeks", vg: "vg-bw", series: "Brent Weeks"})
	// ...and a live book whose title and series are not his name: the
	// evidence that exempts a person-shaped work name (workTarget).
	f.book(junkBookSpec{title: "The Way of Shadows", path: "/lib/bw/2", author: "Brent Weeks"})
	bw := f.book(junkBookSpec{title: "The Black Prism (copy)", path: "/lib/j/bw", author: "read by narrator", vg: "vg-bw"})

	plan := f.plan()
	for _, c := range []struct{ book, want string }{
		{zog, "Zogarth"}, {ryu, "Ryuto"}, {bor, "Borgy60"}, {bw, "Brent Weeks"},
	} {
		var r *repairs.Row
		for _, j := range []string{"A LitRPG Adventure", "read by narrator"} {
			if _, ok := f.authors[j]; ok {
				if rr := f.row(plan, j, c.book); rr != nil {
					r = rr
				}
			}
		}
		require.NotNil(t, r, c.want)
		require.Empty(t, r.Skipped, "%s: %s", c.want, r.SkipReason)
		require.Equal(t, c.want, r.Proposed["author"], r.Reason)
	}
}

// Review of #3631, blocker 1: a work name that is person-SHAPED ("Vampire
// Hunter D", "Solo Leveling", "Red Rising") is judged by evidence. A mint of
// one (the folder parser reading the series folder) is refused; a relink to
// an existing row of that name is refused unless the row has a live book
// whose title and series are not the name (Joe Haldeman, with a swapped
// "Joe Haldeman" title beside The Forever War, still passes).
func TestJunkAuthorFixer_PersonShapedWorksAreRefused(t *testing.T) {
	f := newJunkFixture(t)
	f.mkSeries("Vampire Hunter D", "Hideyuki Kikuchi")
	vhd := f.book(junkBookSpec{title: "Vampire Hunter D_ Volume 11", path: "/lib/Vampire Hunter D/Vampire Hunter D_ Volume 11",
		author: "Graphic Audio LLC.", series: "Vampire Hunter D"})
	f.mkSeries("Solo Leveling", "Chugong")
	solo := f.book(junkBookSpec{title: "Solo Leveling, Vol. 3", path: "/lib/Solo Leveling/Solo Leveling, Vol. 3", author: "GraphicAudio"})

	f.book(junkBookSpec{title: "Red Rising", path: "/lib/rr/1", author: "Red Rising", vg: "vg-rr"})
	rr := f.book(junkBookSpec{title: "Red Rising (Unabridged)", path: "/lib/j/rr", author: "read by narrator", vg: "vg-rr"})

	f.book(junkBookSpec{title: "The Forever War", path: "/lib/jh/1", author: "Joe Haldeman", vg: "vg-jh"})
	f.book(junkBookSpec{title: "Joe Haldeman", path: "/lib/jh/2", author: "Joe Haldeman"})
	jh := f.book(junkBookSpec{title: "The Forever War (copy)", path: "/lib/j/jh", author: "read by narrator", vg: "vg-jh"})

	plan := f.plan()
	for _, c := range []struct{ junk, book, bad string }{
		{"Graphic Audio LLC.", vhd, "Vampire Hunter D"},
		{"GraphicAudio", solo, "Solo Leveling"},
		{"read by narrator", rr, "Red Rising"},
	} {
		r := f.row(plan, c.junk, c.book)
		require.NotNil(t, r, c.bad)
		require.NotEqual(t, c.bad, r.Proposed["author"], "%s: %s", c.bad, r.Reason)
		require.NotEqual(t, junkAuthorDecCreate, r.Proposed["decision"], "%s: %s", c.bad, r.Reason)
		require.Equal(t, junkAuthorSkipAmbiguous, r.Skipped, "%s: held, not unlinked: %v %s", c.bad, r.Proposed, r.Reason)
		require.Contains(t, r.SkipReason, c.bad)
	}
	r := f.row(plan, "read by narrator", jh)
	require.NotNil(t, r)
	require.Empty(t, r.Skipped, r.SkipReason)
	require.Equal(t, "Joe Haldeman", r.Proposed["author"], r.Reason)
}

// Review of #3631, blocker 2, exactly as prod holds book
// 01M0TGRGRS988X5S6R9XGBSE5V: no narrator anywhere, no tags, and the reader
// credited as author of more books than she is known to narrate. The path is
// Narrator/Author/Book; the outer folder is refused because a deeper one is
// the junk name's own author ("Jennsen").
func TestJunkAuthorFixer_PathNarratorAboveAuthorFolder(t *testing.T) {
	const junk = "Jennsen, GS_ 08 Rubicon (Amaranthe 08)"
	f := newJunkFixture(t)
	for i := range 3 {
		f.book(junkBookSpec{title: fmt.Sprintf("Mis-credited %d", i), path: fmt.Sprintf("/lib/pd/%d", i), author: "Pyper Down"})
	}
	id := f.book(junkBookSpec{title: "8-06 Rubicon (Amaranthe 08) — 06",
		path: "/mnt/bigdata/books/audiobook-organizer/Pyper Down/Jennsen/Jennsen, GS_ 08 Rubicon (Amaranthe 08)", author: junk})
	r := f.row(f.plan(), junk, id)
	require.NotNil(t, r)
	require.NotEqual(t, "Pyper Down", r.Proposed["author"], r.Reason)
	require.Equal(t, junkAuthorSkipAmbiguous, r.Skipped, "held, not unlinked: %v %s", r.Proposed, r.Reason)
	require.Contains(t, r.SkipReason, "a folder above \"Jennsen\"")
}

// Review of #3631, minor 7: the surname-first clean carries initials only;
// a provider naming someone else wins over it.
func TestJunkAuthorFixer_SurnameFirstYieldsToProvider(t *testing.T) {
	const junk = "Jennsen, GS_ 08 Rubicon (Amaranthe 08)"
	f := newJunkFixture(t)
	f.book(junkBookSpec{title: "Starshine", path: "/lib/gsj/1", author: "G. S. Jennsen"})
	f.book(junkBookSpec{title: "Mercury Rising", path: "/lib/gs/1", author: "Glynn Stewart"})
	id := f.book(junkBookSpec{title: "Rubicon", path: "/lib/j/rub", author: junk})
	f.setProvider(id, "Rubicon", "Glynn Stewart")
	r := f.row(f.plan(), junk, id)
	require.NotNil(t, r)
	require.Empty(t, r.Skipped, r.SkipReason)
	require.Equal(t, "Glynn Stewart", r.Proposed["author"], r.Reason)
	require.Equal(t, junkAuthorSrcProvider, r.Proposed["source"])
}

// Review of #3631, major 3: Apply's backstop runs the plan's work test
// against the index as it is at apply. Plan and Replan see "Zogarth" as a
// pen name; a book titled "Zogarth" lands before apply and the index is
// rebuilt; Apply refuses and writes nothing.
func TestJunkAuthorFixer_ApplyBackstopRefusesANewWorkTitle(t *testing.T) {
	f := newJunkFixture(t)
	f.book(junkBookSpec{title: "The Primal Hunter", path: "/lib/Zogarth/tph1", author: "Zogarth"})
	id := f.book(junkBookSpec{title: "The Primal Hunter 9 - A LitRPG Adventure", path: "/lib/j/zog", author: "A LitRPG Adventure"})
	f.setProvider(id, "The Primal Hunter 9 - A LitRPG Adventure", "Zogarth")
	plan := f.plan()
	row := f.row(plan, "A LitRPG Adventure", id)
	require.NotNil(t, row)
	require.Equal(t, "Zogarth", row.Proposed["author"], row.Reason)
	fresh, err := f.fixer.Replan(context.Background(), nil, *row, &fakeReporter{})
	require.NoError(t, err)
	require.Equal(t, row.Fingerprint, fresh.Fingerprint)
	before := f.credits(id)

	f.book(junkBookSpec{title: "Zogarth", path: "/lib/o/z", author: "Ann Other"})
	f.fixer.idxMu.Lock()
	f.fixer.idx = nil
	f.fixer.idxMu.Unlock()

	w := repairs.NewWriter(f.s, f.s, f.fixer.ID(), "bulk_update", "repairs-").WithJournal(f.s, f.s, junkTestOpID).WithCredits(f.s)
	err = f.fixer.Apply(context.Background(), w, fresh)
	require.Error(t, err)
	require.True(t, errors.Is(err, repairs.ErrChangedSincePlan), err)
	require.Contains(t, err.Error(), "title in the library")
	require.Equal(t, before, f.credits(id), "nothing written")
}
