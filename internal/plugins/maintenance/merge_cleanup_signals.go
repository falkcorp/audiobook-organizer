// file: internal/plugins/maintenance/merge_cleanup_signals.go
// version: 1.3.0
// guid: 67839bc7-cc2f-4877-afbc-e2646b549e49
// last-edited: 2026-11-18

package maintenance

import (
	"fmt"
	"slices"
	"sort"
	"strings"
	"unicode"

	"github.com/falkcorp/audiobook-organizer/internal/authorname"
	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/dedup"
	"github.com/falkcorp/audiobook-organizer/internal/personname"
)

// The signals of the merge-cleanup fixer, and the one pure function that turns
// them into a verdict.
//
// Everything in this file is a pure function of two mcSide values, so each
// threshold edge and each verdict is a table test with no store behind it.
// The fixer (merge_cleanup_fixer.go) fills the sides from live rows at Plan and
// again at Replan; nothing here reads a store.
//
// Every threshold is a constant, deliberately not a plan param: a param would
// make two trials of the same library non-reproducible (owner question 11, the
// recommended answer taken). Durations compare as integer permille so the edges
// (0.95, 0.80, 0.49, 0.51) are exact rather than floating-point accidents.
const (
	// TITLE_MATCH: normalized title similarity at or above this.
	mcTitleMatchMin = 0.9
	// TITLE_DIFF: similarity strictly below this AND word overlap strictly
	// below mcTitleOverlapMax.
	mcTitleDiffMax    = 0.5
	mcTitleOverlapMax = 0.34

	// DUR_EXACT: the two known runtimes are within this many seconds.
	mcDurExactSec = 2
	// SIZE_CLOSE: the byte totals are within this many permille (0.1 percent)
	// of the larger.
	mcSizeClosePermille = 1

	// Runtime ratio, shorter over longer, in permille.
	mcDurClosePermille = 950 // DUR_CLOSE: at or above
	mcDurNearPermille  = 800 // DUR_NEAR: from here up to DUR_CLOSE; below is DUR_FAR (a fragment)
	mcDur2xLoPermille  = 490 // DUR_2X: the ratio of a book to its half
	mcDur2xHiPermille  = 510

	// A runtime under this many seconds is a probe (a sample or an intro), not
	// a book: a ratio against it decides nothing.
	mcProbeMaxSec = 30 * 60

	// HASH_EQ counts only when no more than this many books carry the hash:
	// a hash a dozen books share is a blank or boilerplate file.
	mcHashShareMax = 4
)

// Row kinds (Row.Class) and skip kinds (Row.Skipped). A kind is what the row
// would do; a skip kind is why it does not.
const (
	mcKindUnmerge = "unmerge"
	mcKindSwap    = "swap_keeper"
	mcKindRepoint = "repoint_chain"
	// mcClassNoVerdict is the class of a row the signals could not settle: it
	// is listed with its signals so the owner can see it, and proposes nothing.
	mcClassNoVerdict = "no_verdict"
	// mcClassError is the class of a row whose reads failed.
	mcClassError = "error"

	// From the signals (mcDecide).
	mcSkipRuntimeIncomplete = "runtime_incomplete"
	// A size total is missing where the exact-runtime exclusion needs it.
	mcSkipSizeIncomplete = "size_incomplete"
	// The chapter rows that are off disk would change the verdict.
	mcSkipRuntimeIncludesMissing = "runtime_includes_missing"
	// A title or a credit is a placeholder, or a credit names an author row
	// that does not exist: the metadata is no evidence either way.
	mcSkipPlaceholder    = "placeholder_metadata"
	mcSkipDanglingAuthor = "dangling_author"
	// One book's credit is the other's title: a title/author-swapped book against
	// its correct twin (either way round); the "Fix swapped title and author"
	// fixer owns it.
	mcSkipTitleAuthorSwapped = "title_author_swapped"
	// The live book a repoint would point at is not, by the signals, the same
	// work as the loser.
	mcSkipRepointNotSameWork = "repoint_target_not_same_work"
	mcSkipRuntimeAmbiguous   = "runtime_ambiguous"
	mcSkipSuspicious         = "suspicious"
	mcSkipCannotTell         = "cannot_tell"
	mcSkipMergeLooksCorrect  = "merge_looks_correct"
	mcSkipNoLongerMerged     = "no_longer_merged"
	mcSkipGone               = "gone"
	mcSkipError              = "error"

	// From the chain.
	mcSkipChainOverCap     = "chain_over_cap"
	mcSkipCycleNotHead     = "cycle_not_head"
	mcSkipRepointAmbiguous = "repoint_ambiguous"
	// A live, unmerged book of the dead hop's version group exists that no walk
	// named (the group has no usable primary), so "stands alone" is not shown.
	mcSkipRepointGroupNoPrimary = "repoint_group_no_primary"
	// Another live book is merged into the same dead end: each would stand
	// alone, leaving several live copies of one work.
	mcSkipDeadEndShared = "dead_end_shared"

	// From the gates (merge_cleanup_fixer.go).
	mcSkipHandledByVG          = "handled_by_version_group_repair"
	mcSkipRetiredShell         = "retired_shell"
	mcSkipCrossGroup           = "cross_group"
	mcSkipSwapTargetNotPrimary = "swap_target_not_primary"
	mcSkipSwapNoCrown          = "swap_would_not_crown_keeper"
	mcSkipGroupNeedsHandoff    = "group_needs_handoff"
	mcSkipSyncRedirect         = "sync_redirect_present"
	mcSkipSyncBroken           = "sync_redirect_broken"
	mcSkipOwnerSaysDistinct    = "owner_says_distinct"
	mcSkipVetoUnresolvable     = "owner_veto_unresolvable"
)

// mcSide is everything the signals read of one book.
type mcSide struct {
	ID    string
	Title string
	// AltTitles are the book's alternative titles, as stored.
	AltTitles []string
	// Credits are the credited names as stored (normalized, in credit order):
	// what the owner reads and what a swapped title is compared against.
	Credits []string
	// Authors are the individual names the credits hold, composites split,
	// case- and punctuation-folded: the set two books are compared on. A
	// placeholder name and a dangling credit are not in it.
	Authors []string
	// CreditsPlaceholder: a credit is a placeholder name (Unknown ...).
	CreditsPlaceholder bool
	// CreditsDangling counts credits whose author id has no author row.
	CreditsDangling int
	// SeriesID and SeriesSeq are the book's series and its position in it.
	SeriesID  *int
	SeriesSeq *int
	ASIN      string
	// GroupID is the version group ("" none); Primary is the stored primary
	// flag as "true", "false" or "nil".
	GroupID string
	Primary string
	// Files counts the book's present (not missing) book_file rows.
	Files int
	// Runtime is database.ComputeBookRuntime over the book and its file rows.
	// Never Book.Duration: that is a partial sum for a multi-file book.
	Runtime database.BookRuntime
	// TotalBytes is the byte total of the counted rows; BytesComplete says it
	// is a total (every counted row has a size).
	TotalBytes    int64
	BytesComplete bool
	// RuntimeAll, AllTotalBytes and AllBytesComplete are the same measures over
	// every file row, the chapters that are off disk included. The verdict is
	// made on Runtime (what is on disk); RuntimeAll only says whether the
	// missing rows could change it.
	RuntimeAll       database.BookRuntime
	AllTotalBytes    int64
	AllBytesComplete bool
	// HashFile is the hash of the book's only present file ("" for a book with
	// several files or none); HashShare is how many books carry that hash.
	// HashOrig is that file's original (pre tag-write) hash and HashOrigShare
	// its share count: an organized copy keeps its source's hash there.
	HashFile      string
	HashShare     int
	HashOrig      string
	HashOrigShare int
}

// mcSignals are the facts the verdict is made from, named as in the owner's
// audit of 2026-11-17.
type mcSignals struct {
	TitleSim     float64
	TitleOverlap float64

	TitleMatch bool
	// TitleAltMatch: the primary titles differ but an alternative title of one
	// matches a title of the other. It stops TITLE_DIFF and vetoes an unmerge;
	// it never makes two books the same work by itself.
	TitleAltMatch bool
	TitleDiff     bool
	// Transposed: one book's credit is the other's title (a title/author-swapped
	// book against its correct twin; see mcTransposed).
	Transposed    bool
	AuthDiff      bool
	SeriesPosDiff bool
	HashEq        bool
	// ASINDiff is a supporting signal only: two different ASINs may be two
	// editions of one work (the parked asin_conflict plan), so it never
	// decides or holds a row; it is reported.
	ASINDiff bool

	// RuntimeKnown: both runtimes are complete. Every duration signal below is
	// false without it.
	RuntimeKnown bool
	// RatioPermille is shorter over longer in permille (0 when not known).
	RatioPermille int
	// TargetShorter is true when the target's runtime is the smaller one.
	TargetShorter bool
	DurExact      bool
	SizeClose     bool
	DurClose      bool
	DurNear       bool
	DurFar        bool
	Dur2x         bool
}

// Names lists the signals that fired, in a fixed order, for the row's evidence.
func (s mcSignals) Names() []string {
	var out []string
	for _, n := range []struct {
		on   bool
		name string
	}{
		{s.TitleMatch, "TITLE_MATCH"}, {s.TitleAltMatch, "TITLE_ALT_MATCH"}, {s.TitleDiff, "TITLE_DIFF"}, {s.AuthDiff, "AUTH_DIFF"},
		{s.Transposed, "TRANSPOSED"},
		{s.SeriesPosDiff, "SERIES_POS_DIFF"}, {s.HashEq, "HASH_EQ"}, {s.ASINDiff, "ASIN_DIFF"},
		{s.DurClose, "DUR_CLOSE"}, {s.DurNear, "DUR_NEAR"}, {s.DurFar, "DUR_FAR"}, {s.Dur2x, "DUR_2X"},
		{s.DurExact, "DUR_EXACT"}, {s.SizeClose, "SIZE_CLOSE"},
	} {
		if n.on {
			out = append(out, n.name)
		}
	}
	return out
}

// mcTitleSimilarity is 1 - editdistance/length over the titles folded the way
// the dedup engine folds them (case, quotes, punctuation, a leading article),
// on runes. Two empty titles are 0, not 1: no title is no evidence.
func mcTitleSimilarity(a, b string) float64 {
	x, y := []rune(dedup.NormalizeTitle(a)), []rune(dedup.NormalizeTitle(b))
	if len(x) == 0 || len(y) == 0 {
		return 0
	}
	longest := max(len(x), len(y))
	return 1 - float64(mcEditDistance(x, y))/float64(longest)
}

func mcEditDistance(a, b []rune) int {
	prev := make([]int, len(b)+1)
	cur := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		cur[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev, cur = cur, prev
	}
	return prev[len(b)]
}

// mcTitleOverlap is the Jaccard overlap of the two titles' words (after the
// same folding). No words on either side is 0.
func mcTitleOverlap(a, b string) float64 {
	wa := strings.Fields(dedup.NormalizeTitle(a))
	wb := strings.Fields(dedup.NormalizeTitle(b))
	if len(wa) == 0 || len(wb) == 0 {
		return 0
	}
	set := map[string]bool{}
	for _, w := range wa {
		set[w] = true
	}
	inter, union := 0, len(set)
	seen := map[string]bool{}
	for _, w := range wb {
		if seen[w] {
			continue
		}
		seen[w] = true
		if set[w] {
			inter++
		} else {
			union++
		}
	}
	return float64(inter) / float64(union)
}

// mcAuthorsDiffer is true when both credit lists are known and share no name.
// A side with no credits is missing evidence, never a difference.
func mcAuthorsDiffer(a, b []string) bool {
	if len(a) == 0 || len(b) == 0 {
		return false
	}
	have := map[string]bool{}
	for _, n := range a {
		have[n] = true
	}
	for _, n := range b {
		if have[n] {
			return false
		}
	}
	return true
}

// mcSideTitles are the titles a side can be compared on: its title, then its
// alternative titles, with empty and placeholder ones left out (a placeholder
// matches every other placeholder, and says nothing about a book).
func mcSideTitles(s mcSide) (primary string, all []string) {
	if t := strings.TrimSpace(s.Title); t != "" && !authorname.IsPlaceholderTitle(t) {
		primary = t
		all = append(all, t)
	}
	for _, a := range s.AltTitles {
		if a = strings.TrimSpace(a); a != "" && !authorname.IsPlaceholderTitle(a) {
			all = append(all, a)
		}
	}
	return primary, all
}

// mcTransposed reports whether one book's credit is the other's title: the
// signature of a title/author-swapped book against its correct twin. It looks
// for one direction, not both. The population "Fix swapped title and author"
// repairs stores a junk title (a "read by ..." credit) and the book's real
// title in the author field, so the real title is on the correct twin as its
// TITLE while the swapped book's own title matches nothing of the twin's;
// asking for the mirror as well would let exactly that shape through as a
// wrong-work pair. The question is symmetric (swapping the arguments asks the
// same thing). Two books with the same title are never transposed (a book named
// after its own author matches itself), and a placeholder on either side is no
// evidence. A pair of different works where one's author is the other's title is
// rare and costs nothing to hold.
func mcTransposed(a, b mcSide) bool {
	at, bt := strings.TrimSpace(a.Title), strings.TrimSpace(b.Title)
	if at == "" || bt == "" || authorname.IsPlaceholderTitle(at) || authorname.IsPlaceholderTitle(bt) {
		return false
	}
	if mcTitleSimilarity(at, bt) >= mcTitleMatchMin {
		return false
	}
	matches := func(title string, side mcSide) bool {
		for _, n := range slices.Concat(side.Credits, side.Authors) {
			if !authorname.IsPlaceholderAuthor(n) && mcTitleSimilarity(title, n) >= mcTitleMatchMin {
				return true
			}
		}
		return false
	}
	return matches(at, b) || matches(bt, a)
}

// mcHashEq reports HASH_EQ: both books are one file each, and a hash of one
// (the file's hash, or its original pre-tag-write hash) is a hash of the
// other, shared by few books. An organized copy keeps its source's hash as its
// original hash, so comparing only the current hashes would lose the one case
// where the copy and its source are certainly the same audio.
func mcHashEq(l, t mcSide) bool {
	if l.Files != 1 || t.Files != 1 {
		return false
	}
	type hs struct {
		hash  string
		share int
	}
	of := func(s mcSide) []hs {
		return []hs{{s.HashFile, s.HashShare}, {s.HashOrig, s.HashOrigShare}}
	}
	for _, a := range of(l) {
		for _, b := range of(t) {
			if a.hash != "" && a.hash == b.hash && a.share <= mcHashShareMax && b.share <= mcHashShareMax {
				return true
			}
		}
	}
	return false
}

// mcComputeSignals reads two sides (l the loser, t the target) into signals.
func mcComputeSignals(l, t mcSide) mcSignals {
	var s mcSignals
	lp, lAll := mcSideTitles(l)
	tp, tAll := mcSideTitles(t)
	// The primary titles decide TITLE_MATCH; a title matched only through an
	// alternative title is TITLE_ALT_MATCH. TITLE_DIFF needs every title of
	// one side to differ from every title of the other.
	primarySim := 0.0
	if lp != "" && tp != "" {
		primarySim = mcTitleSimilarity(lp, tp)
	}
	for _, a := range lAll {
		for _, b := range tAll {
			s.TitleSim = max(s.TitleSim, mcTitleSimilarity(a, b))
			s.TitleOverlap = max(s.TitleOverlap, mcTitleOverlap(a, b))
		}
	}
	s.TitleMatch = primarySim >= mcTitleMatchMin
	s.TitleAltMatch = !s.TitleMatch && s.TitleSim >= mcTitleMatchMin
	s.TitleDiff = lp != "" && tp != "" && s.TitleSim < mcTitleDiffMax && s.TitleOverlap < mcTitleOverlapMax
	s.Transposed = mcTransposed(l, t) // symmetric: swapping the arguments asks the same question
	s.AuthDiff = mcAuthorsDiffer(l.Authors, t.Authors)
	// A position differs only inside one series: two series say nothing about
	// which volume a book is.
	if l.SeriesID != nil && t.SeriesID != nil && *l.SeriesID == *t.SeriesID &&
		l.SeriesSeq != nil && t.SeriesSeq != nil && *l.SeriesSeq != *t.SeriesSeq {
		s.SeriesPosDiff = true
	}
	s.ASINDiff = l.ASIN != "" && t.ASIN != "" && !strings.EqualFold(l.ASIN, t.ASIN)
	s.HashEq = mcHashEq(l, t)

	ls, lok := l.Runtime.KnownSeconds()
	ts, tok := t.Runtime.KnownSeconds()
	s.RuntimeKnown = lok && tok
	if !s.RuntimeKnown {
		return s
	}
	short, long := min(ls, ts), max(ls, ts)
	s.TargetShorter = ts < ls
	s.RatioPermille = short * 1000 / long
	// The edges compare by cross-multiplication, not the truncated permille:
	// 799.9 is not 800.
	atLeast := func(p int) bool { return short*1000 >= long*p }
	s.DurClose = atLeast(mcDurClosePermille)
	s.DurNear = atLeast(mcDurNearPermille) && !s.DurClose
	s.DurFar = !atLeast(mcDurNearPermille)
	s.Dur2x = atLeast(mcDur2xLoPermille) && short*1000 <= long*mcDur2xHiPermille
	s.DurExact = long-short <= mcDurExactSec
	if l.BytesComplete && t.BytesComplete && l.TotalBytes > 0 && t.TotalBytes > 0 {
		bigger, smaller := max(l.TotalBytes, t.TotalBytes), min(l.TotalBytes, t.TotalBytes)
		s.SizeClose = (bigger-smaller)*1000 <= bigger*mcSizeClosePermille
	}
	return s
}

// mcVerdict is the outcome of mcDecide. Kind is set when the signals support a
// row kind (mcKindUnmerge, mcKindSwap); Skip is set when the row proposes
// nothing, and may be set with a Kind (the kind the row would be, held by Skip).
type mcVerdict struct {
	Kind    string
	Skip    string
	Why     string
	Signals mcSignals
	// Extra are further holds the verdict also carries, after Skip.
	Extra []mcHoldWhy
}

// mcHoldWhy is one hold: a skip kind and why.
type mcHoldWhy struct{ Skip, Why string }

// mcCreditParts is one stored credit read as the individual names it holds,
// each folded: a composite credit ("A, B", "A and B", "A / B") is split with
// the shared splitter, and every name is reduced to its comparison key (mcFoldName), so "Wayne, Bruce" and "Bruce Wayne" are one
// name. Placeholder names are not dropped here: the caller decides what they
// mean.
func mcCreditParts(credit string) []string {
	names := personname.SplitCompositeAuthorName(credit)
	if len(names) < 2 {
		names = []string{credit}
	}
	var out []string
	for _, n := range names {
		if f := mcFoldName(n); f != "" && !slices.Contains(out, f) {
			out = append(out, f)
		}
	}
	return out
}

// mcFoldName is the comparison key of one name: lower-cased, every run of
// non-letters and non-digits is a word break, and the words are sorted, so
// punctuation and word order ("Wayne, Bruce" against "Bruce Wayne") never
// make one person two. The key is for comparing only; it is never shown.
func mcFoldName(name string) string {
	words := strings.FieldsFunc(strings.ToLower(personname.NormalizeAuthorName(name)), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
	sort.Strings(words)
	return strings.Join(words, " ")
}

// mcUnreliable holds a pair when a fact the signals read is not evidence: a
// credit naming an author row that does not exist, or a placeholder title or
// credit on either side. The governing rule of the preview is that missing or
// ambiguous data holds a row and never classes it, so these never reach the
// verdict. ok is false when both sides are readable.
func mcUnreliable(l, t mcSide) (mcVerdict, bool) {
	v := mcVerdict{Signals: mcComputeSignals(l, t)}
	var holds []mcHoldWhy
	for _, side := range []struct {
		name string
		s    mcSide
	}{{"loser " + l.ID, l}, {"target " + t.ID, t}} {
		if side.s.CreditsDangling > 0 {
			holds = append(holds, mcHoldWhy{mcSkipDanglingAuthor, fmt.Sprintf("the %s has %d credit(s) naming an author row that does not exist, so its credits cannot be compared", side.name, side.s.CreditsDangling)})
		}
	}
	for _, side := range []struct {
		name string
		s    mcSide
	}{{"loser " + l.ID, l}, {"target " + t.ID, t}} {
		switch {
		case authorname.IsPlaceholderTitle(side.s.Title):
			holds = append(holds, mcHoldWhy{mcSkipPlaceholder, fmt.Sprintf("the %s has no real title (%q), so titles say nothing about it", side.name, side.s.Title)})
		case side.s.CreditsPlaceholder:
			holds = append(holds, mcHoldWhy{mcSkipPlaceholder, fmt.Sprintf("the %s is credited to a placeholder name, so its credits say nothing about it", side.name)})
		}
	}
	if len(holds) == 0 {
		return v, false
	}
	v.Skip, v.Why, v.Extra = holds[0].Skip, holds[0].Why, holds[1:]
	return v, true
}

// viewAll is the side as if every chapter row, those off disk included, were
// on disk: the view the verdict was made on before it was made on what is
// really there.
func (s mcSide) viewAll() mcSide {
	if s.RuntimeAll.FilesCounted > 0 {
		s.Runtime, s.TotalBytes, s.BytesComplete = s.RuntimeAll, s.AllTotalBytes, s.AllBytesComplete
	}
	return s
}

// mcOutcomeName names what a verdict amounts to, for a hold's reason.
func mcOutcomeName(v mcVerdict) string {
	switch {
	case v.Skip != "":
		return v.Skip
	case v.Kind != "":
		return "an unheld " + v.Kind
	}
	return "no verdict"
}

// mcDecide turns two books into a verdict: a hold when a fact is not
// evidence (mcUnreliable), otherwise the verdict made on the audio that is
// on disk (mcDecideOn). When chapter rows that are off disk exist and
// counting them would give a different answer, the row is held
// (runtime_includes_missing): the answer then depends on audio the book does
// not have.
func mcDecide(l, t mcSide) mcVerdict {
	if v, bad := mcUnreliable(l, t); bad {
		return v
	}
	on := mcDecideOn(l, t)
	if l.RuntimeAll.FilesMissingUnmatched == 0 && t.RuntimeAll.FilesMissingUnmatched == 0 {
		return on
	}
	all := mcDecideOn(l.viewAll(), t.viewAll())
	if on.Kind == all.Kind && on.Skip == all.Skip {
		return on
	}
	v := on
	v.Kind = on.Kind
	if v.Kind == "" {
		v.Kind = all.Kind
	}
	v.Skip = mcSkipRuntimeIncludesMissing
	v.Why = fmt.Sprintf("the audio on disk gives %s, but counting the chapter rows that are off disk (%d on the loser, %d on the target) gives %s; the answer depends on audio the book does not have",
		mcOutcomeName(on), l.RuntimeAll.FilesMissingUnmatched, t.RuntimeAll.FilesMissingUnmatched, mcOutcomeName(all))
	v.Extra = nil
	if on.Skip != "" {
		v.Extra = append([]mcHoldWhy{{on.Skip, on.Why}}, on.Extra...)
	}
	return v
}

// mcDecideOn turns the signals of a loser l and its live final target t into a
// verdict, on the runtimes the sides carry.
//
//   - TRANSPOSED: one book is the other's title/author-swapped twin. Held
//     (title_author_swapped); "Fix swapped title and author" repairs it.
//   - UNMERGE (wrong work): at least two of TITLE_DIFF, AUTH_DIFF,
//     SERIES_POS_DIFF, and none of TITLE_MATCH, TITLE_ALT_MATCH, HASH_EQ,
//     DUR_EXACT+SIZE_CLOSE. The last needs both runtimes complete, and a
//     complete byte total on both sides when the runtimes are exact, so a
//     missing one holds the row (runtime_incomplete, size_incomplete) rather
//     than guess.
//   - SWAP KEEPER: the two are the same work (TITLE_MATCH, HASH_EQ, or a close
//     runtime with the same author), no wrong-work signal contradicts it, and
//     the target is shorter than 0.80 of the loser, both runtimes complete.
//     DUR_2X and a shorter side under thirty minutes are known false signals:
//     they hold the row (runtime_ambiguous), never decide it.
//   - Anything else is listed and skipped: merge_looks_correct (the same work
//     and not a fragment), suspicious (a wrong-work signal that is not enough,
//     or signals that contradict each other), cannot_tell (no signal).
func mcDecideOn(l, t mcSide) mcVerdict {
	s := mcComputeSignals(l, t)
	v := mcVerdict{Signals: s}
	diffs := 0
	for _, on := range []bool{s.TitleDiff, s.AuthDiff, s.SeriesPosDiff} {
		if on {
			diffs++
		}
	}
	skip := func(kind, why string, a ...any) mcVerdict {
		v.Skip, v.Why = kind, fmt.Sprintf(why, a...)
		return v
	}

	if s.Transposed {
		return skip(mcSkipTitleAuthorSwapped, "one book's credit is the other's title: the shape of a title/author-swapped book and its correct twin; the \"Fix swapped title and author\" repair owns it, not a merge cleanup")
	}
	if diffs >= 2 {
		if s.TitleMatch || s.TitleAltMatch || s.HashEq {
			return skip(mcSkipSuspicious, "%d wrong-work signals (%s) but %s says the two are one work", diffs,
				strings.Join(s.Names(), ", "), mcWhichSameSignal(s))
		}
		if !s.RuntimeKnown {
			v.Kind = mcKindUnmerge
			return skip(mcSkipRuntimeIncomplete, "%d wrong-work signals, but a runtime is incomplete, so the DUR_EXACT+SIZE_CLOSE exclusion cannot be checked", diffs)
		}
		if s.DurExact && !(l.BytesComplete && t.BytesComplete) {
			v.Kind = mcKindUnmerge
			return skip(mcSkipSizeIncomplete, "%d wrong-work signals and the runtimes match to the second, but a file size is missing, so the DUR_EXACT+SIZE_CLOSE exclusion cannot be checked", diffs)
		}
		if s.DurExact && s.SizeClose {
			return skip(mcSkipSuspicious, "%d wrong-work signals but the runtimes and sizes match to the second and the byte", diffs)
		}
		v.Kind = mcKindUnmerge
		return v
	}

	authorsKnown := len(l.Authors) > 0 && len(t.Authors) > 0
	sameWork := s.TitleMatch || s.HashEq || ((s.DurClose || s.DurNear) && authorsKnown && !s.AuthDiff)
	if sameWork {
		if diffs >= 1 && !s.HashEq {
			return skip(mcSkipSuspicious, "the same work by %s, but %s says otherwise", mcWhichSameSignal(s), strings.Join(s.Names(), ", "))
		}
		if !s.RuntimeKnown {
			v.Kind = mcKindSwap
			return skip(mcSkipRuntimeIncomplete, "the same work, but a runtime is incomplete, so which copy is the fragment cannot be told")
		}
		if !s.TargetShorter || !s.DurFar {
			return skip(mcSkipMergeLooksCorrect, "the same work and the target is not a fragment of the loser (ratio %d permille)", s.RatioPermille)
		}
		v.Kind = mcKindSwap
		ts, _ := t.Runtime.KnownSeconds()
		switch {
		case s.Dur2x:
			return skip(mcSkipRuntimeAmbiguous, "the target is about half the loser's runtime (DUR_2X), which is as likely two parts of one work as a fragment")
		case ts < mcProbeMaxSec:
			return skip(mcSkipRuntimeAmbiguous, "the target runs %d s, under thirty minutes: a probe, not a book", ts)
		}
		return v
	}
	if diffs == 1 {
		return skip(mcSkipSuspicious, "one wrong-work signal (%s) and no signal that the two are one work", strings.Join(s.Names(), ", "))
	}
	return skip(mcSkipCannotTell, "no signal says wrong work and none says the same work")
}

// mcWhichSameSignal names the signal that makes two books the same work.
func mcWhichSameSignal(s mcSignals) string {
	switch {
	case s.HashEq:
		return "HASH_EQ"
	case s.TitleMatch:
		return "TITLE_MATCH"
	case s.TitleAltMatch:
		return "TITLE_ALT_MATCH"
	}
	return "a close runtime with the same author"
}
