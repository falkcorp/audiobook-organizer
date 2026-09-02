// file: internal/dedup/author_determinism_test.go
// version: 1.3.1
// guid: 8b1e47c6-2a95-4d03-be71-5c9f28a4d016
// last-edited: 2026-09-02

package dedup

import (
	"fmt"
	"sort"
	"strings"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// determinismCorpus builds an author set that exercises phase 3 specifically:
// last names that are SIMILAR but not equal, so the cross-bucket comparison
// actually runs, paired with matching first names so the pairs group rather
// than being rejected downstream.
func determinismCorpus() []database.Author {
	// Families of near-identical surnames. Membership is NOT transitive: only
	// 15 of the 21 intra-family pairs actually score >= 0.95, which is what
	// phase 3 gates on. "Andersen"/"Andersson", "Kowalskii"/"Kowalsky",
	// "Petersen"/"Petersonn", "Johanssen"/"Johanson" and
	// "Mikkelson"/"Mikkelsenn" all land at ~0.93 and are NOT similar to each
	// other, even though each is similar to a third spelling in its family.
	//
	// This is recorded because the original comment here claimed every family
	// member scores >= 0.95 against every other, which is false. It does NOT
	// explain the stranding documented on wantGoldenGroups below -- that comes
	// purely from greedy `used` marking, and would happen even if the families
	// were fully connected. "Kowalski" is similar to BOTH "Kowalskii" and
	// "Kowalsky"; it is simply already used by the time the second pair is
	// reached.
	families := [][]string{
		{"Anderson", "Andersen", "Andersson", "Andersonn"},
		{"Kowalski", "Kowalskii", "Kowalsky"},
		{"Petersen", "Peterson", "Petersonn"},
		{"Johansson", "Johanssen", "Johanson"},
		{"Mikkelsen", "Mikkelson", "Mikkelsenn"},
		{"Christiansen", "Christianson", "Christiansenn"},
	}
	firsts := []string{"John", "Maria", "Erik", "Anna", "Lars", "Sofia", "Peter", "Elena"}

	var authors []database.Author
	id := 1
	for _, fam := range families {
		for _, last := range fam {
			for _, first := range firsts {
				authors = append(authors, database.Author{
					ID:   id,
					Name: first + " " + last,
				})
				id++
			}
		}
	}
	// Padding: unrelated authors that must never be grouped, so the corpus is
	// not entirely made of duplicates.
	for _, n := range []string{
		"Ursula Le Guin", "Terry Pratchett", "Isaac Asimov", "Octavia Butler",
		"China Mieville", "Ann Leckie", "Iain Banks", "Gene Wolfe",
	} {
		authors = append(authors, database.Author{ID: id, Name: n})
		id++
	}
	return authors
}

// serializeGroups renders a result set into a stable string so two runs can be
// compared exactly -- including the ORDER groups came back in, which is the
// property the sorted bucket iteration exists to make reproducible.
func serializeGroups(groups []AuthorDedupGroup) string {
	var b strings.Builder
	for _, g := range groups {
		variants := make([]string, 0, len(g.Variants))
		for _, v := range g.Variants {
			variants = append(variants, fmt.Sprintf("%d:%s", v.ID, v.Name))
		}
		// Variant order within a group is not what this test pins; group
		// membership and group order are.
		sort.Strings(variants)
		fmt.Fprintf(&b, "canonical=%d:%s books=%d suggested=%q variants=[%s]\n",
			g.Canonical.ID, g.Canonical.Name, g.BookCount, g.SuggestedName,
			strings.Join(variants, ","))
	}
	return b.String()
}

// TestFindDuplicateAuthorsIsDeterministic asserts that repeated runs over
// identical input produce byte-identical results.
//
// This is the guard for two separate things. Ranging lastNameBuckets directly
// used to make phase 3's greedy grouping depend on Go's randomized map order,
// so the same library produced different suggestions on consecutive scans.
// And phase 3's similarity scan now runs across all cores, so a lost write or
// a worker racing on shared state would surface here as run-to-run drift.
//
// Run with -race to get the second guarantee properly.
func TestFindDuplicateAuthorsIsDeterministic(t *testing.T) {
	authors := determinismCorpus()
	bookCount := func(id int) int { return id%7 + 1 }

	const runs = 8
	first := serializeGroups(FindDuplicateAuthors(authors, 0.85, bookCount))
	if strings.TrimSpace(first) == "" {
		t.Fatal("corpus produced no duplicate groups; this test would prove nothing")
	}

	for i := 1; i < runs; i++ {
		got := serializeGroups(FindDuplicateAuthors(authors, 0.85, bookCount))
		if got != first {
			t.Fatalf("run %d differs from run 0.\n--- run 0 ---\n%s\n--- run %d ---\n%s",
				i, first, i, got)
		}
	}
	t.Logf("%d runs identical; %d groups", runs, strings.Count(first, "\n"))
}

// wantGoldenGroups is the exact grouping determinismCorpus produces. It was
// captured from the sorted-serial baseline -- the commit that fixed the map
// iteration order but had not yet been sharded, which is the earliest commit
// whose output is stable enough to capture at all -- and verified
// byte-identical at every later stage of the phase-3 rework (prefilter, then
// sharded scan). It is pinned in full rather than summarized so that a change
// in WHICH authors get grouped, not merely how many groups come back, fails as
// a readable diff.
//
// NOTE ON A PRE-EXISTING LIMITATION, DELIBERATELY PINNED AS-IS: every one of
// the 56 groups below has exactly two members, and the corpus's odd-sized
// surname families each strand a spelling entirely. Kowalsky, Petersonn,
// Johanssen, Mikkelson and Christianson appear zero times in this constant --
// 40 of the corpus's 152 family authors are never offered for merging at all.
// The four-member Anderson family, by contrast, is fully covered, as two pairs.
//
// The mechanism is greedy pairing, not a size cap. When a pair is grouped, both
// authors are marked in `used`, and the outer loop skips used authors, so an
// author that has already been paired can never pull in a third spelling. Even-
// sized families therefore pair off completely and odd-sized families leave one
// spelling with no unused partner.
//
// A group can never exceed two members AT ALL -- not merely in this corpus.
// author.go carries an append-to-existing-canonical branch that looks like it
// grows a group past two, but it is unreachable for every input: reaching it
// requires !used[pi], while the branch itself requires pi to be some group's
// canonical, and every group-creation path marks its canonical used (author.go
// :1263, :1137, :1382). `used` is never cleared. So the two conditions are
// mutually exclusive and the branch is dead code.
//
// The user-visible consequence is worth stating plainly: author dedup
// structurally CANNOT offer a three-way merge of surname spellings. It offers
// pairs, and a library with three spellings of one name needs two passes -- if
// the third is offered at all, which per the stranding above it may not be.
//
// This behavior predates the O(n^2) work and is orthogonal to it -- the
// cross-commit equivalence check proves the output is identical before and
// after -- so it is recorded here rather than
// changed. If transitive grouping is ever implemented, this constant is the
// thing that should fail.
const wantGoldenGroups = `canonical=9:John Andersen books=5 suggested="" variants=[1:John Anderson]
canonical=10:Maria Andersen books=7 suggested="" variants=[2:Maria Anderson]
canonical=11:Erik Andersen books=9 suggested="" variants=[3:Erik Anderson]
canonical=12:Anna Andersen books=11 suggested="" variants=[4:Anna Anderson]
canonical=13:Lars Andersen books=13 suggested="" variants=[5:Lars Anderson]
canonical=6:Sofia Anderson books=8 suggested="" variants=[14:Sofia Andersen]
canonical=15:Peter Andersen books=3 suggested="" variants=[7:Peter Anderson]
canonical=16:Elena Andersen books=5 suggested="" variants=[8:Elena Anderson]
canonical=25:John Andersonn books=9 suggested="" variants=[17:John Andersson]
canonical=26:Maria Andersonn books=11 suggested="" variants=[18:Maria Andersson]
canonical=27:Erik Andersonn books=13 suggested="" variants=[19:Erik Andersson]
canonical=20:Anna Andersson books=8 suggested="" variants=[28:Anna Andersonn]
canonical=29:Lars Andersonn books=3 suggested="" variants=[21:Lars Andersson]
canonical=30:Sofia Andersonn books=5 suggested="" variants=[22:Sofia Andersson]
canonical=31:Peter Andersonn books=7 suggested="" variants=[23:Peter Andersson]
canonical=32:Elena Andersonn books=9 suggested="" variants=[24:Elena Andersson]
canonical=145:John Christiansenn books=10 suggested="" variants=[129:John Christiansen]
canonical=146:Maria Christiansenn books=12 suggested="" variants=[130:Maria Christiansen]
canonical=147:Erik Christiansenn books=7 suggested="" variants=[131:Erik Christiansen]
canonical=148:Anna Christiansenn books=9 suggested="" variants=[132:Anna Christiansen]
canonical=149:Lars Christiansenn books=4 suggested="" variants=[133:Lars Christiansen]
canonical=150:Sofia Christiansenn books=6 suggested="" variants=[134:Sofia Christiansen]
canonical=151:Peter Christiansenn books=8 suggested="" variants=[135:Peter Christiansen]
canonical=152:Elena Christiansenn books=10 suggested="" variants=[136:Elena Christiansen]
canonical=81:John Johansson books=12 suggested="" variants=[97:John Johanson]
canonical=82:Maria Johansson books=7 suggested="" variants=[98:Maria Johanson]
canonical=83:Erik Johansson books=9 suggested="" variants=[99:Erik Johanson]
canonical=84:Anna Johansson books=4 suggested="" variants=[100:Anna Johanson]
canonical=85:Lars Johansson books=6 suggested="" variants=[101:Lars Johanson]
canonical=86:Sofia Johansson books=8 suggested="" variants=[102:Sofia Johanson]
canonical=87:Peter Johansson books=10 suggested="" variants=[103:Peter Johanson]
canonical=88:Elena Johansson books=12 suggested="" variants=[104:Elena Johanson]
canonical=41:John Kowalskii books=13 suggested="" variants=[33:John Kowalski]
canonical=42:Maria Kowalskii books=8 suggested="" variants=[34:Maria Kowalski]
canonical=43:Erik Kowalskii books=3 suggested="" variants=[35:Erik Kowalski]
canonical=44:Anna Kowalskii books=5 suggested="" variants=[36:Anna Kowalski]
canonical=45:Lars Kowalskii books=7 suggested="" variants=[37:Lars Kowalski]
canonical=46:Sofia Kowalskii books=9 suggested="" variants=[38:Sofia Kowalski]
canonical=47:Peter Kowalskii books=11 suggested="" variants=[39:Peter Kowalski]
canonical=48:Elena Kowalskii books=13 suggested="" variants=[40:Elena Kowalski]
canonical=121:John Mikkelsenn books=4 suggested="" variants=[105:John Mikkelsen]
canonical=122:Maria Mikkelsenn books=6 suggested="" variants=[106:Maria Mikkelsen]
canonical=123:Erik Mikkelsenn books=8 suggested="" variants=[107:Erik Mikkelsen]
canonical=124:Anna Mikkelsenn books=10 suggested="" variants=[108:Anna Mikkelsen]
canonical=125:Lars Mikkelsenn books=12 suggested="" variants=[109:Lars Mikkelsen]
canonical=126:Sofia Mikkelsenn books=7 suggested="" variants=[110:Sofia Mikkelsen]
canonical=127:Peter Mikkelsenn books=9 suggested="" variants=[111:Peter Mikkelsen]
canonical=128:Elena Mikkelsenn books=4 suggested="" variants=[112:Elena Mikkelsen]
canonical=65:John Peterson books=5 suggested="" variants=[57:John Petersen]
canonical=66:Maria Peterson books=7 suggested="" variants=[58:Maria Petersen]
canonical=67:Erik Peterson books=9 suggested="" variants=[59:Erik Petersen]
canonical=68:Anna Peterson books=11 suggested="" variants=[60:Anna Petersen]
canonical=69:Lars Peterson books=13 suggested="" variants=[61:Lars Petersen]
canonical=62:Sofia Petersen books=8 suggested="" variants=[70:Sofia Peterson]
canonical=71:Peter Peterson books=3 suggested="" variants=[63:Peter Petersen]
canonical=72:Elena Peterson books=5 suggested="" variants=[64:Elena Petersen]
`

// TestFindDuplicateAuthorsGoldenShape asserts the full grouping against a
// checked-in golden. The parallel scan in phase 3 is required to leave this
// untouched: it changes only how the similar-name pairs are found, never which
// ones are. A count-based assertion would not catch a regression that merged or
// split groups while keeping the total, which is exactly the failure mode
// sharding a greedy algorithm could introduce.
func TestFindDuplicateAuthorsGoldenShape(t *testing.T) {
	authors := determinismCorpus()
	bookCount := func(id int) int { return id%7 + 1 }
	got := serializeGroups(FindDuplicateAuthors(authors, 0.85, bookCount))

	if got != wantGoldenGroups {
		gotLines := strings.Split(strings.TrimRight(got, "\n"), "\n")
		wantLines := strings.Split(strings.TrimRight(wantGoldenGroups, "\n"), "\n")
		t.Errorf("grouping changed: got %d groups, want %d", len(gotLines), len(wantLines))
		for i := 0; i < len(gotLines) || i < len(wantLines); i++ {
			var g, w string
			if i < len(gotLines) {
				g = gotLines[i]
			}
			if i < len(wantLines) {
				w = wantLines[i]
			}
			if g != w {
				t.Errorf("line %d:\n  got:  %s\n  want: %s", i+1, g, w)
			}
		}
	}

	// The eight padding authors are mutually dissimilar and must never appear
	// as a canonical with variants. Asserted separately from the golden so the
	// intent survives any future regeneration of the constant.
	for _, solo := range []string{"Le Guin", "Pratchett", "Asimov", "Butler",
		"Mieville", "Leckie", "Banks", "Wolfe"} {
		for line := range strings.SplitSeq(got, "\n") {
			if strings.Contains(line, solo) && strings.Contains(line, "variants=[") &&
				!strings.Contains(line, "variants=[]") {
				t.Errorf("padding author %q was grouped with variants: %s", solo, line)
			}
		}
	}
}
