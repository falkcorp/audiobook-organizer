// file: internal/seriesdenumber/property_test.go
// version: 1.0.1
// guid: e41a8c07-5b92-4d3f-a6c8-12f7d09b3e55
// last-edited: 2026-11-17

package seriesdenumber

import (
	"fmt"
	"math/rand"
	"sort"
	"strconv"
	"strings"
	"testing"

	"golang.org/x/text/unicode/norm"
)

// ─── THE PROPERTY ───────────────────────────────────────────────────────────
//
// For any generated library, the books the production pipeline (ParseName ->
// Demote -> CheckRun -> Decide) moves are EXACTLY the books the independent
// oracle (oracle_test.go) says may move, at the position it says. "Exactly" is
// both directions: a book moved that the rule forbids is a wrong move, and a book
// held that the rule allows is a rule that quietly stopped working (the count of
// eligible plans is the owner's measure of whether the fixer does anything).
//
// The generator builds the libraries the review traced the wrong moves from:
//
//   - multi-book sources, alone and inside an otherwise clean run (B1);
//   - runs that are the parts of ONE shattered book: one title, a title that is
//     the base, a part number in the title, a shared ASIN or ISBN, an empty
//     title, the same title in another case, spacing or Unicode form (B2(d));
//   - a number word or a part, format or volume word in the base (B2(a), B2(b));
//   - bases that are a part of the series' own author's name (M1) or another
//     author's whole name;
//   - Unicode: names, titles and authors rendered composed or decomposed, with
//     curly or straight apostrophes, one rendering per library;
//   - collisions with the base series' own members, gaps in the numbering, two
//     series carrying one number, locks, own positions, version groups.
//
// The pipeline below is the op's wiring in miniature: it parses, demotes, groups
// by author and base, builds the run from the siblings, and decides. The
// planner's base and number are the generator's own (how the name was BUILT), not
// anything parsed back out of it, and a few percent are deliberately wrong so the
// planner-disagreement check can fire.

type genFixtures struct {
	titles  []string
	authors []genAuthorSpec
	bases   []string
	hostile []string
}

type genAuthorSpec struct {
	given, initial, surname string
	style                   int // 0 "Given Surname", 1 "Surname, Given", 2 "Given I. Surname"
}

func (a genAuthorSpec) name() string {
	switch {
	case a.style == 1:
		return a.surname + ", " + a.given
	case a.style == 2 && a.initial != "":
		return a.given + " " + a.initial + ". " + a.surname
	}
	return a.given + " " + a.surname
}

// fixtures are Batman titles and characters only.
var fixtures = genFixtures{
	titles: []string{
		"A Death in the Family", "A Lonely Place of Dying", "Under the Red Hood", "The Long Halloween",
		"Dark Victory", "Hush Returns", "Knightfall Rising", "Zero Year", "No Man's Land", "Night of the Owls",
		"City of Owls", "Death of the Family", "Endgame", "Bloom", "Gothtopia", "The Black Mirror", "Court of Shadows",
		"Batman's Legacy", "Cataclysm", "Contagion", "The Cult", "Strange Apparitions", "Gotham Central Nights",
	},
	authors: []genAuthorSpec{
		{"Bruce", "T", "Wayne", 0}, {"Dick", "", "Grayson", 1}, {"Selina", "K", "Kyle", 2},
		{"Alfred", "", "Pennyworth", 0}, {"Renée", "", "Montoya", 0}, {"Barbara", "", "Gordon", 1},
	},
	bases: []string{
		"Court of Owls", "Gotham Knights", "Arkham Cell", "Dark Knight", "Year Zero", "Knightfall", "Batman's Shadow",
		"Détective Comics", "Gotham Café", "Mad Hatter", "Killer Croc", "Batman’s Legacy", "Gotham Can't Sleep",
	},
	// The bases the grammar must refuse: a number word, a part, format or volume
	// word, an abbreviated volume word.
	hostile: []string{
		"Hush Book One", "Hush Ch", "Hush Ep", "Hush Episodes", "Hush Discs", "Hush CDs", "Hush Sides", "Hush Reel",
		"Hush Hour", "Hush Hours", "Hush Vols", "Hush Bd", "Hush Nr", "Hush T", "Hush V", "Hush Chapter", "Hush Tome",
		"Hush Part", "Hush Disc", "Hush Unabridged", "Hush and", "Hush of", "BW", "Hush 2nd", "Hush Edition",
		"V for Vengeance", "Zero Hour", "No Man's Land",
	},
}

var (
	cleanTails = []string{"%02d", "%d", "Book %d", "Vol. %d", "Volume %d", "#%d", "No. %d", "Bk %d"}
	otherTails = []string{"Part %d", "Disc %d", "[%02d]", "%d of 3", "%d.5", "10%d", "%03d", "%d-%d", "(%d)"}
	cleanSeps  = []string{" ", " - ", ": ", ", ", " – ", " — "}
)

func tailFor(rng *rand.Rand, n int, hostile bool) string {
	tail := cleanTails[rng.Intn(len(cleanTails))]
	if hostile {
		tail = otherTails[rng.Intn(len(otherTails))]
	}
	if strings.Count(tail, "%") == 2 {
		return fmt.Sprintf(tail, n, n+1)
	}
	return fmt.Sprintf(tail, n)
}

// titleSource hands out titles whose keys are all different within a library.
type titleSource struct {
	rng *rand.Rand
	n   int
}

var titleSuffix = []string{"", " Alpha", " Bravo", " Charlie", " Delta", " Echo", " Foxtrot", " Golf", " Hotel"}

func (t *titleSource) next() string {
	i := t.n
	t.n++
	return fixtures.titles[i%len(fixtures.titles)] + titleSuffix[(i/len(fixtures.titles))%len(titleSuffix)]
}

// authorOf builds the author and the keys the oracle compares a base against,
// from how the name was constructed.
func authorOf(spec genAuthorSpec, id int) pAuthor {
	a := pAuthor{ID: id, Name: spec.name(), PartKeys: map[string]bool{}, FullKeys: map[string]bool{}}
	for _, k := range []string{spec.given, spec.surname, spec.given + " " + spec.surname} {
		a.PartKeys[oLetters(k)] = true
	}
	switch {
	case spec.style == 1:
		a.FullKeys[oLetters(spec.surname+" "+spec.given)] = true
		a.FullKeys[oLetters(spec.given+" "+spec.surname)] = true
	case spec.style == 2 && spec.initial != "":
		a.PartKeys[oLetters(spec.given+" "+spec.initial+" "+spec.surname)] = true
		a.FullKeys[oLetters(spec.given+" "+spec.initial+" "+spec.surname)] = true
	default:
		a.FullKeys[oLetters(spec.given+" "+spec.surname)] = true
	}
	return a
}

// genLibrary builds one library from rng. render turns every generated string
// into its stored spelling.
func genLibrary(rng *rand.Rand, render func(string) string) pLib {
	lib := pLib{Targets: map[string]*pTarget{}}
	specs := map[int]genAuthorSpec{}
	nAuth := 2 + rng.Intn(2)
	perm := rng.Perm(len(fixtures.authors))
	for i := range nAuth {
		spec := fixtures.authors[perm[i]]
		specs[i+1] = spec
		a := authorOf(spec, i+1)
		a.Name = render(a.Name)
		lib.Authors = append(lib.Authors, a)
	}
	titles := &titleSource{rng: rng}
	nextSeries, nextBook := 100, 1
	newBook := func(title string) pBook {
		b := pBook{ID: "b" + strconv.Itoa(nextBook), Title: render(title)}
		nextBook++
		switch rng.Intn(10) {
		case 0:
			b.Seq = 90 + rng.Intn(9) // a position of its own that is not the name's
		}
		if rng.Intn(25) == 0 {
			b.Locked = true
		}
		return b
	}

	for cluster, nc := 0, 1+rng.Intn(4); cluster < nc; cluster++ {
		authorID := 1 + rng.Intn(nAuth)
		spec := specs[authorID]
		var base string
		hostile := false
		switch r := rng.Intn(100); {
		case r < 55:
			base = fixtures.bases[rng.Intn(len(fixtures.bases))]
		case r < 75:
			base = fixtures.hostile[rng.Intn(len(fixtures.hostile))]
			hostile = true
		default: // a part of the series' own author's name, or another author's whole name
			switch rng.Intn(5) {
			case 0:
				base = spec.surname
			case 1:
				base = spec.given
			case 2:
				base = spec.given + " " + spec.surname
			case 3:
				base = specs[1+rng.Intn(nAuth)].name()
			default:
				other := specs[1+rng.Intn(nAuth)]
				base = other.surname + " " + other.given
			}
			hostile = true
		}
		base = render(base)

		// The numbers of the cluster and what each series holds.
		kind := rng.Intn(100)
		var nums []int
		start := 1 + rng.Intn(9)
		switch {
		case kind < 45: // a run
			for k, n := 0, 3+rng.Intn(3); k < n; k++ {
				nums = append(nums, start+k)
			}
		case kind < 60: // a run with a gap: a run of three then another piece
			nums = []int{start, start + 1, start + 2, start + 4, start + 5}
		case kind < 70: // a pair, below the run minimum
			nums = []int{start, start + 1}
		case kind < 78: // a lone series
			nums = []int{start}
		case kind < 85: // two series carrying one number inside a run
			nums = []int{start, start + 1, start + 1, start + 2}
		default:
			for k, n := 0, 3+rng.Intn(2); k < n; k++ {
				nums = append(nums, start+k)
			}
		}
		shatter := -1
		if kind >= 85 || rng.Intn(100) < 25 {
			shatter = rng.Intn(9)
		}
		partTitle := titles.next()
		var asin, isbn string
		for i, n := range nums {
			id := nextSeries
			nextSeries++
			tail := tailFor(rng, n, rng.Intn(100) < 8)
			name := render(base + cleanSeps[rng.Intn(len(cleanSeps))] + tail)
			if hostile && rng.Intn(100) < 40 {
				name = render(base + " " + fmt.Sprintf("%02d", n))
			}
			s := pSeries{ID: id, Name: name, Author: authorID, ClaimBase: base, ClaimN: n}
			if rng.Intn(100) < 3 {
				s.ClaimN = n + 1 // the planner read it differently
			}
			if rng.Intn(100) < 2 {
				s.Author = 0 // no author
			}
			ownIsN := rng.Intn(100) < 12 // the books carry the number the name carries
			nBooks := 1
			switch r := rng.Intn(100); {
			case r < 6:
				nBooks = 0
			case r < 14:
				nBooks = 2 + rng.Intn(3) // a multi-book source
			}
			distinct := nBooks
			for k := 0; k < nBooks; k++ {
				title := titles.next()
				switch shatter {
				case 0: // one title for every part
					title = partTitle
				case 1: // the title is the base
					if i == 0 {
						title = base
					}
				case 2: // a part number in the title
					title = partTitle + " Part " + strconv.Itoa(n)
				case 3: // the base and a number
					title = base + " " + fmt.Sprintf("%02d", n)
				case 4: // one ASIN under every part
					if asin == "" {
						asin = "B00" + strings.ToUpper(strconv.FormatInt(int64(rng.Intn(1<<30)), 36))
					}
				case 5: // two parts share an ISBN, one with hyphens
					if i < 2 {
						isbn = "978-0-306-40615-7"
					}
				case 6: // an empty title
					if i == 1 {
						title = ""
					}
				case 7: // the same title, another case and spacing
					title = strings.ToUpper(partTitle)
					if i%2 == 1 {
						title = "  " + partTitle + " "
					}
				case 8: // the same title, composed in one part and decomposed in another
					title = partTitle
					if i%2 == 1 {
						title = norm.NFD.String("Zoë " + partTitle)
					} else {
						title = norm.NFC.String("Zoë " + partTitle)
					}
				}
				b := newBook(title)
				if ownIsN && b.Seq == 0 {
					b.Seq = n
				}
				if shatter == 4 {
					b.ASIN = asin
					if i%2 == 1 {
						b.ASIN = strings.ToLower(asin)
					}
				}
				if shatter == 5 && i < 2 {
					b.ISBN = isbn
					if i == 1 {
						b.ISBN = "9780306406157"
					}
				}
				// Versions: the second book of the source shares the first's group
				// now and then, so it stands for one book with two versions.
				if k > 0 && rng.Intn(100) < 45 {
					b.Group = s.Books[0].Group
					if b.Group == "" {
						s.Books[0].Group = "vg" + strconv.Itoa(id)
						b.Group = s.Books[0].Group
					}
					b.Title = s.Books[0].Title
					distinct--
				}
				s.Books = append(s.Books, b)
			}
			_ = distinct
			lib.Series = append(lib.Series, s)
		}

		// The base series' own members.
		if rng.Intn(100) < 30 {
			t := &pTarget{Exists: true, TargetID: nextSeries, BaseSeries: base}
			nextSeries++
			for k, n := 0, rng.Intn(3); k < n; k++ {
				t.Positions = append(t.Positions, 1+rng.Intn(12))
			}
			if rng.Intn(100) < 5 {
				t.Unseen = 1 + rng.Intn(2)
			}
			lib.Targets[targetKey(authorID, base)] = t
		} else if rng.Intn(100) < 6 {
			lib.Targets[targetKey(authorID, base)] = &pTarget{Elsewhere: true}
		}
	}
	return lib
}

// ─── the production pipeline, in miniature ──────────────────────────────────

func productionDecide(lib pLib) map[string]pMove {
	names := make([]string, 0, len(lib.Authors))
	byID := map[int]pAuthor{}
	for _, a := range lib.Authors {
		names = append(names, a.Name)
		byID[a.ID] = a
	}
	authors := NewAuthorSet(names)

	type plan struct {
		s    pSeries
		p    Parsed
		keep bool
	}
	plans := map[int]*plan{}
	var order []int
	for _, s := range lib.Series {
		parsed, _, ok := ParseName(s.Name)
		if !ok {
			continue
		}
		pl := &plan{s: s, p: parsed}
		_, has := byID[s.Author]
		cand := Candidate{
			FromName: s.Name, Base: s.ClaimBase, Position: s.ClaimN, AuthorID: s.Author,
			AuthorMissing: s.Author != 0 && !has, AuthorName: byID[s.Author].Name, Authors: authors,
		}
		_, demoted := Demote(cand)
		pl.keep = !demoted
		plans[s.ID] = pl
		order = append(order, s.ID)
	}

	// Group by author and base (the op's grouping), siblings are the same-author
	// series with this base that hold a book.
	groups := map[string][]int{}
	var gkeys []string
	for _, id := range order {
		pl := plans[id]
		k := targetKey(pl.s.Author, pl.p.Base)
		if _, seen := groups[k]; !seen {
			gkeys = append(gkeys, k)
		}
		groups[k] = append(groups[k], id)
	}
	sort.Strings(gkeys)

	facts := func(s pSeries) []Facts {
		var out []Facts
		for _, b := range s.Books {
			out = append(out, Facts{ID: b.ID, Identity: b.Group, Title: b.Title, ASIN: b.ASIN, ISBN13: b.ISBN})
		}
		return out
	}

	moves := map[string]pMove{}
	for _, k := range gkeys {
		ids := groups[k]
		first := plans[ids[0]]
		g := Group{BaseName: first.p.Base, AuthorID: first.s.Author}
		if t := lib.Targets[k]; t != nil {
			if t.Exists {
				g.TargetID = t.TargetID
				g.UnseenExisting = t.Unseen
				for i, pos := range t.Positions {
					g.Existing = append(g.Existing, Book{ID: fmt.Sprintf("t%d-%d", t.TargetID, i), Seq: &pos})
				}
			} else if t.Elsewhere {
				g.BaseElsewhere = "series 9 under author 9"
			}
		}
		var runSeries []RunSeries
		for _, id := range ids {
			pl := plans[id]
			if len(pl.s.Books) > 0 {
				runSeries = append(runSeries, RunSeries{ID: id, N: pl.p.N, Books: facts(pl.s)})
			}
		}
		var decided []int
		for _, id := range ids {
			pl := plans[id]
			if !pl.keep {
				continue
			}
			self := RunSeries{ID: id, N: pl.p.N, Books: facts(pl.s)}
			src := Source{FromID: id, FromName: pl.s.Name, Position: pl.p.N, Run: CheckRun(first.p.Base, self, runSeries)}
			for _, b := range pl.s.Books {
				sb := Book{ID: b.ID, Identity: b.Group}
				if b.Seq != 0 {
					v := b.Seq
					sb.Seq = &v
				}
				if b.Locked {
					sb.LockedBy = "series_name"
				}
				src.Books = append(src.Books, sb)
			}
			g.Sources = append(g.Sources, src)
			decided = append(decided, id)
		}
		res := Decide(g)
		for _, sr := range res.Sources {
			for _, m := range sr.Moves {
				moves[m.BookID] = pMove{NewSeq: m.NewSeq}
			}
		}
		_ = decided
	}
	return moves
}

// describe prints a library compactly enough to read in a failure.
func describe(lib pLib) string {
	var b strings.Builder
	for _, a := range lib.Authors {
		fmt.Fprintf(&b, "author %d %q\n", a.ID, a.Name)
	}
	for _, s := range lib.Series {
		fmt.Fprintf(&b, "series %d author %d %q claim(%q,%d)", s.ID, s.Author, s.Name, s.ClaimBase, s.ClaimN)
		for _, k := range s.Books {
			fmt.Fprintf(&b, " [%s grp=%q title=%q seq=%d asin=%q isbn=%q lock=%v]", k.ID, k.Group, k.Title, k.Seq, k.ASIN, k.ISBN, k.Locked)
		}
		b.WriteByte('\n')
	}
	for k, t := range lib.Targets {
		fmt.Fprintf(&b, "target %s %+v\n", k, *t)
	}
	return b.String()
}

func renderer(form norm.Form, curly bool) func(string) string {
	return func(s string) string {
		if curly {
			s = strings.ReplaceAll(s, "'", "’")
		} else {
			s = strings.NewReplacer("’", "'", "‘", "'").Replace(s)
		}
		return form.String(s)
	}
}

const propertyLibraries = 6000

// TestProperty_ProductionMovesExactlyWhatTheOracleAllows is the whole property.
func TestProperty_ProductionMovesExactlyWhatTheOracleAllows(t *testing.T) {
	rng := rand.New(rand.NewSource(20261117))
	coverage := map[string]int{}
	movedBooks := 0
	for iter := range propertyLibraries {
		form := norm.NFC
		if rng.Intn(2) == 0 {
			form = norm.NFD
		}
		lib := genLibrary(rng, renderer(form, rng.Intn(2) == 0))
		want, why := oracleDecide(lib)
		got := productionDecide(lib)
		for _, r := range why {
			key := r
			if i := strings.Index(key, ":"); i > 0 && strings.HasPrefix(key, "grammar") {
				key = key[:i]
			}
			coverage[key]++
		}
		movedBooks += len(want)

		for id, w := range want {
			g, ok := got[id]
			if !ok {
				t.Fatalf("library %d: book %s is allowed to move to %d by the rule and was held\n%s", iter, id, w.NewSeq, describe(lib))
			}
			if g.NewSeq != w.NewSeq {
				t.Fatalf("library %d: book %s moved to %d, the rule says %d\n%s", iter, id, g.NewSeq, w.NewSeq, describe(lib))
			}
		}
		for id, g := range got {
			if _, ok := want[id]; !ok {
				t.Fatalf("library %d: book %s moved to %d but the rule forbids it\n%s", iter, id, g.NewSeq, describe(lib))
			}
		}
	}

	// The generator must reach every shape, or the property says nothing about it.
	for _, shape := range []string{
		"moves", "multi_book_source", "run_short", "run_rejected:title_repeats", "run_rejected:title_is_base",
		"run_rejected:shared_identifier", "run_rejected:member_has_no_title", "run_rejected:member_holds_several_books",
		"run_rejected:two_series_one_number", "author:own_name_part", "author:other_full_name", "locked",
		"own_position_differs", "collision", "planner_disagrees", "grammar", "target_unseen", "base_elsewhere",
	} {
		if coverage[shape] < 25 {
			t.Errorf("the generator reached %q only %d times in %d libraries", shape, coverage[shape], propertyLibraries)
		}
	}
	if movedBooks < 500 {
		t.Errorf("only %d books moved: the generator does not reach the move path", movedBooks)
	}
	t.Logf("%d libraries, %d books moved; coverage %v", propertyLibraries, movedBooks, coverage)
}

// ─── Decide on its own ──────────────────────────────────────────────────────
//
// The pipeline property cannot tell Decide's own rules from CheckRun's: a source
// holding several books is refused twice (CheckRun rejects the run it is a member
// of, and Decide holds the source), so removing either layer alone leaves the
// pipeline's answers unchanged. This property feeds Decide run verdicts of its
// own choosing, including a confirmed run for a source that holds several books,
// which is what a caller with a bug would hand it, and checks the rule with an
// oracle that knows nothing of CheckRun.

func TestProperty_DecideAloneNeverSplitsASourceOrTrustsABareName(t *testing.T) {
	rng := rand.New(rand.NewSource(7))
	states := []RunState{RunNone, RunConfirmed, RunConfirmed, RunConfirmed, RunRejected}
	moved, multiConfirmed := 0, 0
	for iter := range 4000 {
		g := Group{BaseName: "Court of Owls", AuthorID: 1}
		var exists []int
		if rng.Intn(3) == 0 {
			g.TargetID = 500
			for k, n := 0, rng.Intn(3); k < n; k++ {
				pos := 1 + rng.Intn(8)
				exists = append(exists, pos)
				g.Existing = append(g.Existing, Book{ID: "t" + strconv.Itoa(k), Seq: &pos})
			}
		}
		type srcFacts struct {
			idents int
			locked bool
			differ bool
			state  RunState
			n      int
		}
		var facts []srcFacts
		nextBook := 0
		for si, n := 0, 1+rng.Intn(5); si < n; si++ {
			pos := 1 + rng.Intn(8)
			f := srcFacts{state: states[rng.Intn(len(states))], n: pos}
			src := Source{FromID: si + 1, FromName: "x", Position: pos, Run: RunVerdict{State: f.state}}
			f.idents = 1
			if rng.Intn(4) == 0 {
				f.idents = 2 + rng.Intn(2)
			}
			for k := 0; k < f.idents; k++ {
				for v, nv := 0, 1+rng.Intn(2); v < nv; v++ {
					nextBook++
					b := Book{ID: "b" + strconv.Itoa(nextBook), Identity: "grp" + strconv.Itoa(nextBook-v)}
					if v > 0 {
						b.Identity = src.Books[len(src.Books)-1].Identity
					}
					if rng.Intn(8) == 0 {
						b.LockedBy = "series_position"
						f.locked = true
					}
					switch rng.Intn(6) {
					case 0:
						other := 1 + rng.Intn(8)
						if other == pos {
							other = pos%8 + 1
						}
						b.Seq = &other
						f.differ = true
					case 1: // its own position is the number in the name
						same := pos
						b.Seq = &same
					}
					src.Books = append(src.Books, b)
				}
			}
			// Versions of one book share an identity: count the distinct ones from
			// what was built.
			seen := map[string]bool{}
			for _, b := range src.Books {
				seen[b.Identity] = true
			}
			f.idents = len(seen)
			facts = append(facts, f)
			g.Sources = append(g.Sources, src)
		}

		res := Decide(g)

		// The oracle, per source: more than one distinct book holds the whole
		// source; then per version set, a lock or a differing own position holds
		// the set; a run that is not confirmed holds it; a position another mover
		// or an existing member already holds holds every mover at it.
		want := map[string]int{}
		at := map[int][]string{}
		for si, s := range g.Sources {
			f := facts[si]
			if f.idents > 1 {
				if f.state == RunConfirmed {
					multiConfirmed++
				}
				continue
			}
			if f.state != RunConfirmed {
				continue
			}
			bad := false
			for _, b := range s.Books {
				if b.LockedBy != "" || (b.Seq != nil && *b.Seq != s.Position) {
					bad = true
				}
			}
			if bad {
				continue
			}
			for _, b := range s.Books {
				at[s.Position] = append(at[s.Position], b.ID)
			}
		}
		for pos, ids := range at {
			holders := map[string]bool{}
			for _, id := range ids {
				for _, s := range g.Sources {
					for _, b := range s.Books {
						if b.ID == id {
							holders[b.identity()] = true
						}
					}
				}
			}
			taken := 0
			for _, e := range exists {
				if e == pos {
					taken++
				}
			}
			if len(holders)+taken > 1 {
				continue
			}
			for _, id := range ids {
				want[id] = pos
			}
		}
		got := map[string]int{}
		for _, sr := range res.Sources {
			for _, m := range sr.Moves {
				got[m.BookID] = m.NewSeq
			}
		}
		moved += len(got)
		if fmt.Sprint(sortedMap(got)) != fmt.Sprint(sortedMap(want)) {
			t.Fatalf("iteration %d: Decide moved %v, the rule says %v\ngroup: %+v", iter, sortedMap(got), sortedMap(want), g)
		}
	}
	if moved < 300 || multiConfirmed < 100 {
		t.Errorf("the generator reached %d moved books and %d multi-book sources with a confirmed run: too few to say anything", moved, multiConfirmed)
	}
}

func sortedMap(m map[string]int) []string {
	out := make([]string, 0, len(m))
	for k, v := range m {
		out = append(out, k+"="+strconv.Itoa(v))
	}
	sort.Strings(out)
	return out
}
