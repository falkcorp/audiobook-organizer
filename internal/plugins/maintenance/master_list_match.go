// file: internal/plugins/maintenance/master_list_match.go
// version: 1.0.0
// guid: bc498082-c7d9-434e-82a6-37e0eb55dd61
// last-edited: 2026-10-23

package maintenance

import (
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/masterlist"
	"github.com/falkcorp/audiobook-organizer/internal/merge"
)

// This file is the matching half of the master-list-import fixer: the library
// index (ISBN, iTunes id and trashed copies on top of the Audible fixer's
// title/ASIN/author index) and the tiers that turn one combined list row into
// a class. The Repairs-lane contract and the plan assembly are in
// master_list_import_fixer.go.

// Plan row classes (Row.Class). Every count of the plan is a filter of the
// rows endpoint (?class=<class>), so every number on the page lists its rows.
// Only matched_high can ever be applied (by PR 2); the others are listed,
// never written, and are also the row's Skipped kind.
const (
	mlMatchedHigh   = "matched_high"
	mlMatchedReview = "matched_review"
	mlAmbiguous     = "ambiguous"
	mlUnmatched     = "unmatched"
	mlDuplicate     = "duplicate_in_list"
	mlInvalid       = "invalid"
	// mlUnreadable: a store read the row's decision needs failed. The row is
	// refused rather than planned from a guess (an unreadable book is not "no
	// book", and "unmatched" would offer to record a book the library has).
	mlUnreadable = "error_unreadable"
)

// Match tiers (the evidence and the stored state name them).
const (
	mlTierASIN      = "asin"
	mlTierISBN      = "isbn13"
	mlTierSource    = "source_id"
	mlTierTitle     = "title_author"
	mlTierSwapped   = "swapped_title"
	mlRuntimeSuffix = "_runtime"
)

// Row flags (repairs.Row.Flags): facts a row carries alongside its class. The
// list-side ones are masterlist's issue codes (bad_identifier, bad_timestamp,
// ...); these are the matcher's.
const (
	mlFlagViaTrashed     = "via_trashed"
	mlFlagTrashedOnly    = "trashed_only"
	mlFlagRuntime        = "runtime_mismatch"
	mlFlagSeries         = "series_mismatch"
	mlFlagJunkTitle      = "junk_title"
	mlFlagAuthorMismatch = "author_mismatch"
	mlFlagSwapped        = "swapped_title"
	mlFlagDuplicateTgt   = "duplicate_target"
	mlFlagPossibleDup    = "possible_duplicate"
)

// mlFutureSlack is how far past now a list time may be (clock skew between
// the list's source and this server) before it is a future time.
const mlFutureSlack = arsFutureSlack

// mlLibrary is the plan's whole-library index: the Audible fixer's index of
// live books (ASIN, title, author, version groups), plus ISBN-13 and iTunes
// persistent id for live books, and the same identifiers for trashed books.
// Trashed books are indexed only so a hit can be followed to its live
// survivor: they are never targets. Built once per plan; read-only afterwards,
// so the per-row workers share it without a lock.
type mlLibrary struct {
	*arsLibrary
	byISBN13 map[string][]string
	byPID    map[string][]string

	trashed         map[string]*database.BookCore
	trashedByASIN   map[string][]string
	trashedByISBN13 map[string][]string
	trashedByPID    map[string][]string

	series map[int]string
}

func buildMLLibrary(cores []database.BookCore, authors []database.Author, series []database.Series) *mlLibrary {
	lib := &mlLibrary{arsLibrary: buildARSLibrary(cores, authors),
		byISBN13: map[string][]string{}, byPID: map[string][]string{},
		trashed:       map[string]*database.BookCore{},
		trashedByASIN: map[string][]string{}, trashedByISBN13: map[string][]string{}, trashedByPID: map[string][]string{},
		series: map[int]string{}}
	for _, s := range series {
		lib.series[s.ID] = s.Name
	}
	for i := range cores {
		b := &cores[i]
		live := !b.IsSoftDeleted()
		asin, pid := "", ""
		if b.ASIN != nil {
			asin = strings.ToUpper(strings.TrimSpace(*b.ASIN))
		}
		if b.ITunesPersistentID != nil {
			pid = strings.ToUpper(strings.TrimSpace(*b.ITunesPersistentID))
		}
		var isbns []string
		for _, v := range []*string{b.ISBN13, b.ISBN10} {
			if v == nil {
				continue
			}
			if n := masterlist.NormalizeISBN13(*v); n != "" && !slices.Contains(isbns, n) {
				isbns = append(isbns, n)
			}
		}
		if live {
			for _, n := range isbns {
				lib.byISBN13[n] = append(lib.byISBN13[n], b.ID)
			}
			if pid != "" {
				lib.byPID[pid] = append(lib.byPID[pid], b.ID)
			}
			continue
		}
		lib.trashed[b.ID] = b
		if asin != "" {
			lib.trashedByASIN[asin] = append(lib.trashedByASIN[asin], b.ID)
		}
		for _, n := range isbns {
			lib.trashedByISBN13[n] = append(lib.trashedByISBN13[n], b.ID)
		}
		if pid != "" {
			lib.trashedByPID[pid] = append(lib.trashedByPID[pid], b.ID)
		}
	}
	return lib
}

// mlOutcome is the matcher's verdict on one combined list row.
type mlOutcome struct {
	class string
	tier  string
	// target is the one matched book; candidates are the live books a review
	// or ambiguous row lists. trashed are trashed books the evidence names.
	target     string
	candidates []string
	trashed    []string
	flags      []string
	evidence   []string
	reason     string
	// runtimeKnown: both the list and the target gave a runtime, so the
	// runtime check ran. A match without it is planned at review risk.
	runtimeKnown bool
}

func (o *mlOutcome) flag(f string) {
	if !slices.Contains(o.flags, f) {
		o.flags = append(o.flags, f)
	}
}

// mlMatcher classifies combined rows against one library snapshot. One
// matcher serves every worker of a plan; its caches are per call, not shared.
type mlMatcher struct {
	store   OpsStore
	ext     ExternalIDBookLookup
	lib     *mlLibrary
	follows map[string]merge.FlagFollow
}

// mlCall is the per-row scratch: duration reads are cached for the one row.
type mlCall struct {
	m         *mlMatcher
	row       masterlist.Row
	listSec   float64
	durations map[string]float64
}

func (c *mlCall) duration(id string) (float64, error) {
	if d, ok := c.durations[id]; ok {
		return d, nil
	}
	b := c.m.lib.books[id]
	var bd *int
	if b != nil {
		bd = b.Duration
	}
	d, err := arsLocalDuration(c.m.store, id, bd)
	if err != nil {
		return 0, err
	}
	c.durations[id] = d
	return d, nil
}

// settle keeps the candidates whose duration is within ±10% of the list's
// runtime (known on both sides).
func (c *mlCall) settle(cands []string) ([]string, error) {
	var ok []string
	for _, id := range cands {
		d, err := c.duration(id)
		if err != nil {
			return nil, err
		}
		if fits, known := arsRuntime(d, c.listSec); fits && known {
			ok = append(ok, id)
		}
	}
	return ok, nil
}

// idHit is what one identifier tier found: live books, and trashed books for
// a hit the live index lacks.
type idHit struct {
	live, trashed []string
	// note says which identifier hit, for the evidence.
	note string
}

func uniqSorted(in []string) []string {
	out := slices.Clone(in)
	sort.Strings(out)
	return slices.Compact(out)
}

// isbnKeys are the ISBN-13s the row names: its own, and the conversion of its
// ISBN-10 (they differ only on a row flagged identifier_conflict).
func isbnKeys(r masterlist.Row) []string {
	var keys []string
	if r.ISBN13 != "" {
		keys = append(keys, r.ISBN13)
	}
	if r.ISBN10 != "" {
		if d, ok := masterlist.ISBN10To13(r.ISBN10); ok && !slices.Contains(keys, d) {
			keys = append(keys, d)
		}
	}
	return keys
}

// hits looks one identifier tier up.
func (c *mlCall) hits(tier string) (idHit, error) {
	lib, r := c.m.lib, c.row
	var h idHit
	switch tier {
	case mlTierASIN:
		if r.ASIN == "" {
			return h, nil
		}
		h.live, h.trashed, h.note = lib.byASIN[r.ASIN], lib.trashedByASIN[r.ASIN], "ASIN "+r.ASIN
	case mlTierISBN:
		keys := isbnKeys(r)
		for _, k := range keys {
			h.live = append(h.live, lib.byISBN13[k]...)
			h.trashed = append(h.trashed, lib.trashedByISBN13[k]...)
		}
		h.note = "ISBN-13 " + strings.Join(keys, ", ")
	case mlTierSource:
		var notes []string
		for _, s := range r.Sources {
			if s.SourceID == "" {
				continue
			}
			id := strings.ToUpper(s.SourceID)
			if s.Source == "itunes" {
				h.live = append(h.live, lib.byPID[id]...)
				h.trashed = append(h.trashed, lib.trashedByPID[id]...)
			}
			bid, err := c.m.ext.GetBookByExternalID(s.Source, s.SourceID)
			if err != nil {
				return h, fmt.Errorf("read external id %s:%s: %w", s.Source, s.SourceID, err)
			}
			switch {
			case bid == "":
			case lib.books[bid] != nil:
				h.live = append(h.live, bid)
			case lib.trashed[bid] != nil:
				h.trashed = append(h.trashed, bid)
			}
			notes = append(notes, s.Source+" id "+s.SourceID)
		}
		h.note = strings.Join(notes, ", ")
	}
	h.live, h.trashed = uniqSorted(h.live), uniqSorted(h.trashed)
	return h, nil
}

// survivors follows each trashed book to its live survivor
// (merge.ResolveMergeSurvivor). A trashed book with no live end of its chain
// is dead evidence (noLive). Any other failure is an error: the row is
// refused, never read as "no survivor".
func (c *mlCall) survivors(trashed []string) (live, noLive []string, err error) {
	for _, id := range trashed {
		s, err := merge.ResolveMergeSurvivor(c.m.store, id, c.m.follows)
		switch {
		case errors.Is(err, merge.ErrNoLiveSurvivor):
			noLive = append(noLive, id)
		case err != nil:
			return nil, nil, fmt.Errorf("follow trashed book %s to its survivor: %w", id, err)
		case c.m.lib.books[s] == nil:
			// Live now but not in this plan's snapshot (made after it): not a
			// target this plan can vouch for.
			noLive = append(noLive, id)
		default:
			live = append(live, s)
		}
	}
	return uniqSorted(live), noLive, nil
}

// classify decides one combined row. It reads the library only (no user
// state: nothing is applied in this PR).
func (m *mlMatcher) classify(row masterlist.Row) (mlOutcome, error) {
	c := &mlCall{m: m, row: row, listSec: float64(row.RuntimeMin) * 60, durations: map[string]float64{}}
	var trashedOnly []string

	for _, tier := range []string{mlTierASIN, mlTierISBN, mlTierSource} {
		h, err := c.hits(tier)
		if err != nil {
			return mlOutcome{}, err
		}
		raw, viaTrashed := h.live, false
		var shown []string
		if len(raw) == 0 && len(h.trashed) > 0 {
			live, noLive, err := c.survivors(h.trashed)
			if err != nil {
				return mlOutcome{}, err
			}
			trashedOnly = append(trashedOnly, noLive...)
			if len(live) == 0 {
				continue
			}
			raw, viaTrashed, shown = live, true, h.trashed
		}
		if len(raw) == 0 {
			continue
		}
		return c.resolveID(tier, h.note, raw, viaTrashed, shown)
	}
	return c.byTitle(trashedOnly)
}

// resolveID turns an identifier tier's live hits into one outcome.
func (c *mlCall) resolveID(tier, note string, raw []string, viaTrashed bool, trashed []string) (mlOutcome, error) {
	lib := c.m.lib
	o := mlOutcome{tier: tier, trashed: trashed}
	o.evidence = append(o.evidence, "matched by "+strings.ReplaceAll(tier, "_", " ")+": "+note)
	if viaTrashed {
		o.evidence = append(o.evidence, fmt.Sprintf("only a trashed copy (%s) carries it; followed to its live survivor", strings.Join(trashed, ", ")))
	}
	cands := lib.targets(raw)
	target := ""
	if len(cands) == 1 {
		target = cands[0]
	} else {
		ok, err := c.settle(cands)
		if err != nil {
			return mlOutcome{}, err
		}
		if len(ok) != 1 {
			o.class, o.candidates = mlAmbiguous, cands
			o.reason = fmt.Sprintf("the identifier is on %d primary books and %d of them match the list's runtime", len(cands), len(ok))
			return o, nil
		}
		target, o.tier = ok[0], tier+mlRuntimeSuffix
	}
	o.target, o.candidates = target, []string{target}
	d, err := c.duration(target)
	if err != nil {
		return mlOutcome{}, err
	}
	fits, known := arsRuntime(d, c.listSec)
	o.runtimeKnown = known
	if known {
		o.evidence = append(o.evidence, fmt.Sprintf("local %.0f min vs list %.0f min", d/60, c.listSec/60))
	} else {
		o.evidence = append(o.evidence, "runtime unknown on one side")
	}
	o.class = mlMatchedHigh
	review := func(flag, why string) {
		o.class = mlMatchedReview
		o.flag(flag)
		o.evidence = append(o.evidence, why)
		if o.reason == "" {
			o.reason = why
		}
	}
	title := lib.books[target].Title
	if viaTrashed {
		// Followed through a merge or redirect: the list names a book that is
		// gone, so a person confirms where it went.
		review(mlFlagViaTrashed, fmt.Sprintf("only a trashed copy carried the identifier; its live survivor %q is the match, which a person confirms", title))
	}
	if !fits {
		review(mlFlagRuntime, fmt.Sprintf("the matched book runs %.0f min and the list's edition %.0f min (more than 10%% apart): a different edition, a part, or a wrong identifier", d/60, c.listSec/60))
	}
	// An identifier match whose series number disagrees with the row's title is
	// a book carrying another volume's identifier ("Saga 1" holding "Saga 2"'s).
	if ik, tk := arsKeyOf(c.row.Title), arsKeyOf(title); ik.nums != "" && tk.nums != "" && ik.nums != tk.nums {
		review(mlFlagSeries, fmt.Sprintf("the identifier matches book %q, but its series number (%s) is not the list's (%s): a wrong identifier on that row", title, tk.nums, ik.nums))
	}
	if arsJunkTitle(title) {
		review(mlFlagJunkTitle, fmt.Sprintf("the matched book's title %q is not a title; confirm it is this book", title))
	}
	return o, nil
}

// byTitle is tiers 4 and 5: title and author (never applied, so the best
// outcome is matched_review), then a title found only in an author field.
func (c *mlCall) byTitle(trashedOnly []string) (mlOutcome, error) {
	lib := c.m.lib
	o := mlOutcome{trashed: trashedOnly}
	if len(trashedOnly) > 0 {
		o.flag(mlFlagTrashedOnly)
		o.evidence = append(o.evidence, fmt.Sprintf("trashed books carry the identifier but none has a live survivor: %s", strings.Join(trashedOnly, ", ")))
	}
	none := func(why string) (mlOutcome, error) {
		o.class, o.reason = mlUnmatched, why
		if len(trashedOnly) > 0 {
			o.reason = "a trashed book carries the identifier and has no live survivor; " + why
		}
		return o, nil
	}
	key := arsKeyOf(c.row.Title)
	if key.full == "" {
		return none("no live book carries the list's identifiers, and the row has no title to match on")
	}
	rawHits := lib.titleHits(key)
	hits := lib.targets(rawHits)
	var byAuthor []string
	for _, id := range hits {
		names, err := arsBookAuthorNames(c.m.store, lib.arsLibrary, id)
		if err != nil {
			return mlOutcome{}, err
		}
		if arsAuthorsOverlap(names, c.row.Authors) {
			byAuthor = append(byAuthor, id)
		}
	}
	switch {
	case len(byAuthor) > 0:
		return c.resolveTitle(o, byAuthor)
	case len(hits) > 0:
		o.class, o.candidates = mlUnmatched, hits
		o.flag(mlFlagAuthorMismatch)
		o.reason = fmt.Sprintf("%d books carry the title but none shares an author with the list's credit", len(hits))
		return o, nil
	}
	// The title in an author field: a book whose title and author were
	// swapped, or whose title is junk. Review only.
	swapped := lib.targets(lib.byAuthor[key.full])
	var ok []string
	for _, id := range swapped {
		d, err := c.duration(id)
		if err != nil {
			return mlOutcome{}, err
		}
		if fits, _ := arsRuntime(d, c.listSec); fits {
			ok = append(ok, id)
		}
	}
	if len(ok) > 0 {
		o.tier, o.candidates = mlTierSwapped, ok
		o.flag(mlFlagSwapped)
		o.evidence = append(o.evidence, "no book has this title, but the title is the author of a book of the right length (title and author swapped, or a junk title)")
		if len(ok) == 1 {
			o.class, o.target = mlMatchedReview, ok[0]
			o.reason = "matched on a swapped title and author"
		} else {
			o.class = mlAmbiguous
			o.reason = fmt.Sprintf("%d books have the list's title as their author", len(ok))
		}
		return o, nil
	}
	return none("no live book carries the list's identifiers, its title, or its title as an author")
}

// resolveTitle settles the title-and-author hits: the list's series name and
// then its runtime narrow several hits to one; anything left over is ambiguous.
func (c *mlCall) resolveTitle(o mlOutcome, byAuthor []string) (mlOutcome, error) {
	lib := c.m.lib
	o.tier = mlTierTitle
	o.evidence = append(o.evidence, "matched by title and author")
	cands := byAuthor
	if len(cands) > 1 && len(c.row.Series) > 0 {
		var same []string
		for _, id := range cands {
			if b := lib.books[id]; b != nil && b.SeriesID != nil {
				bs := arsKeyOf(lib.series[*b.SeriesID]).full
				for _, rs := range c.row.Series {
					if bs != "" && bs == arsKeyOf(rs.Name).full {
						same = append(same, id)
						break
					}
				}
			}
		}
		if len(same) > 0 && len(same) < len(cands) {
			o.evidence = append(o.evidence, fmt.Sprintf("the series name narrowed %d books to %d", len(cands), len(same)))
			cands = same
		}
	}
	target := ""
	if len(cands) == 1 {
		target = cands[0]
	} else {
		ok, err := c.settle(cands)
		if err != nil {
			return mlOutcome{}, err
		}
		if len(ok) != 1 {
			o.class, o.candidates = mlAmbiguous, cands
			o.reason = fmt.Sprintf("%d primary books match the title and author and %d of them match the list's runtime", len(cands), len(ok))
			return o, nil
		}
		target, o.tier = ok[0], mlTierTitle+mlRuntimeSuffix
	}
	d, err := c.duration(target)
	if err != nil {
		return mlOutcome{}, err
	}
	fits, known := arsRuntime(d, c.listSec)
	o.class, o.target, o.candidates, o.runtimeKnown = mlMatchedReview, target, []string{target}, known
	o.reason = "matched on title and author, which is never applied without a person's confirmation"
	switch {
	case !known:
		o.evidence = append(o.evidence, "runtime unknown on one side")
	case !fits:
		o.flag(mlFlagRuntime)
		o.evidence = append(o.evidence, fmt.Sprintf("the matched book runs %.0f min and the list's edition %.0f min (more than 10%% apart)", d/60, c.listSec/60))
	default:
		o.evidence = append(o.evidence, fmt.Sprintf("local %.0f min vs list %.0f min", d/60, c.listSec/60))
	}
	return o, nil
}
