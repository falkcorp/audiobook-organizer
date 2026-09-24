// file: internal/versionprimary/rank.go
// version: 1.1.0
// guid: c2a14de5-3125-41f3-a4e8-e0126c71293f
// last-edited: 2026-09-24

// Package versionprimary is the ONE rule for which member of a version group
// is its primary. Before 2026-09-24 four places answered that question with
// "the earliest-created live member" (reconcile.ElectMissingPrimaries,
// dedup-books' primary hand-off, vgUnlinkOutliers, regroup-apply's
// pickPrimary). In the common organize-collision pair the earliest member is
// the organized_source copy, which ABS does not list, so a repair that used
// that rule kept the book hidden.
//
// The rule (owner direction 2026-09-24): an m4b with chapters beats an m4b
// without chapters, which beats good metadata, which beats every other
// format. Only a member ABS can show is ever crowned: a live, organized book
// whose files are all present under the library root. When a better copy
// exists only outside the library, the group is HELD for the owner rather
// than crowned with the worse copy.
//
// This file is pure: Elect takes each member's book row and the signals
// signals.go loaded for it, and decides. Nothing here reads the store or the
// disk.
package versionprimary

import (
	"sort"
	"strings"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// Content tiers, best first. TierNone is a member whose content cannot be
// judged: no active book_file row, or an active row whose file is not on
// disk. It never beats anything and never makes a group held.
const (
	TierNone          = 0
	TierOther         = 1 // several files, or a single non-m4b/m4a file
	TierM4BNoChapters = 2 // one m4b/m4a with 0 or 1 chapters, or an unknown count
	TierM4BChapters   = 3 // one m4b/m4a with more than one chapter
)

// ChapterSource says where a member's chapter count came from.
type ChapterSource string

const (
	// ChapterSourceNA: the member is not a single m4b/m4a file, so its
	// chapter count does not affect its tier and was not read.
	ChapterSourceNA ChapterSource = "n/a"
	// ChapterSourceProbe: ffprobe -show_chapters on the file.
	ChapterSourceProbe ChapterSource = "probe"
	// ChapterSourceTable: the chapters:<bookID> row, used only when the
	// probe failed.
	ChapterSourceTable ChapterSource = "table"
	// ChapterSourceUnknown: the probe failed and the table has no row. A
	// missing table row is NOT "no chapters" (organize never writes it), so
	// this ranks as TierM4BNoChapters and is flagged in the report.
	ChapterSourceUnknown ChapterSource = "unknown"
)

// Decision kinds.
const (
	DecisionElect     = "elect"
	DecisionAlreadyOK = "already_ok"
	DecisionHeld      = "held"
)

// Hold reasons. A held group is never written.
const (
	// HoldBetterCopyNotInLibrary: a live member that is not eligible has a
	// strictly higher content tier than every eligible member. Crowning an
	// eligible member would promote the worse copy; the owner decides whether
	// to organize the better one or restore the library copy from it.
	HoldBetterCopyNotInLibrary = "better_copy_not_in_library"
	// HoldNeedsOrganizeOrRestore: no member is eligible at all.
	HoldNeedsOrganizeOrRestore = "needs_organize_or_restore"
)

// Ineligibility reasons, reported per member.
const (
	IneligibleNotLive         = "not_live"
	IneligibleNotOrganized    = "not_organized"
	IneligibleNoActiveFiles   = "no_active_files"
	IneligibleFilesMissing    = "files_missing_on_disk"
	IneligibleOutsideLibrary  = "files_outside_library_root"
	unknownAuthorPenalty      = 1000
	runtimeMatchTolerancePerc = 2
)

// Signals is what signals.go measured for one member. The zero value is a
// member with no active files.
type Signals struct {
	// Live: not soft-deleted, and not a merge loser whose survivor is alive
	// (Electable).
	Live bool
	// ActiveFiles counts the member's book_file rows that are not flagged
	// Missing. Book.FilePath is never consulted: it is stale on renamed
	// books (2026-09-23 sample: 7 of 36 organized rows named a file that did
	// not exist while every active book_file did).
	ActiveFiles int
	// FilesMissingOnDisk counts active rows whose path does not stat as a
	// regular file.
	FilesMissingOnDisk int
	// AllUnderRoot: every active row's path lies under the library root.
	AllUnderRoot bool
	// UnknownAuthorPath: some active row's path has an "Unknown Author"
	// segment below the library root.
	UnknownAuthorPath bool
	// SingleM4B: exactly one active row, and it is an .m4b or .m4a.
	SingleM4B bool
	// Chapters is the chapter count from ChapterSource.
	Chapters      int
	ChapterSource ChapterSource
	// RuntimeSec is the book's runtime from its file rows, set only when
	// every present file's duration is known (database.BookRuntime.Complete).
	RuntimeSec int
	// BitrateKbps is the highest bitrate among the active rows.
	BitrateKbps int
}

// Member is one version-group member as Elect sees it.
type Member struct {
	Book    *database.Book
	Signals Signals
}

// MemberEval is Elect's per-member working, returned for the report.
type MemberEval struct {
	BookID        string        `json:"book_id"`
	Title         string        `json:"title"`
	LibraryState  string        `json:"library_state"`
	StoredPrimary string        `json:"stored_primary"` // "true", "false" or "nil"
	MergedInto    string        `json:"merged_into_book_id,omitempty"`
	Live          bool          `json:"live"`
	Eligible      bool          `json:"eligible"`
	Ineligible    string        `json:"ineligible_reason,omitempty"`
	Tier          int           `json:"tier"`
	Chapters      int           `json:"chapters"`
	ChapterSource ChapterSource `json:"chapter_source"`
	MetadataScore int           `json:"metadata_score"`
	UnknownAuthor bool          `json:"unknown_author_path,omitempty"`
	RuntimeMatch  bool          `json:"runtime_match,omitempty"`
	BitrateKbps   int           `json:"bitrate_kbps,omitempty"`
	ActiveFiles   int           `json:"active_files"`
	FilesMissing  int           `json:"files_missing_on_disk,omitempty"`

	book *database.Book
}

// Decision is Elect's answer for one group.
type Decision struct {
	Kind       string `json:"decision"`
	HoldReason string `json:"hold_reason,omitempty"`
	// WinnerID is the member to crown. Empty when held.
	WinnerID string `json:"winner_id,omitempty"`
	Reason   string `json:"reason"`
	// BestTier is the best content tier over every live member;
	// BestEligibleTier over eligible members only.
	BestTier         int `json:"best_tier"`
	BestEligibleTier int `json:"best_eligible_tier"`
	// MetadataBestID is the eligible member with the best metadata score.
	// When it differs from WinnerID the winner's empty fields are filled
	// from it (CarryOver).
	MetadataBestID string       `json:"metadata_best_id,omitempty"`
	Members        []MemberEval `json:"members"`
}

// Electable reports whether b may be (or count as) its group's primary: not
// soft-deleted, and not a merge loser whose survivor is alive. Moved here
// from internal/reconcile (2026-09-24) so the rule and its liveness test
// live in one package; reconcile calls it.
func Electable(b *database.Book, alive func(id string) bool) bool {
	return ElectableRow(b.IsSoftDeleted(), b.MergedIntoBookID, alive)
}

// ElectableRow is Electable on the raw columns, for BookCore rows.
// MergedIntoBookID is never cleared, so a loser whose survivor was later
// trashed or removed is electable again: otherwise its group could never
// have a primary.
func ElectableRow(softDeleted bool, mergedInto *string, alive func(id string) bool) bool {
	if softDeleted {
		return false
	}
	return mergedInto == nil || *mergedInto == "" || !alive(*mergedInto)
}

// MetadataScore rates how complete a book row's metadata is. It is
// dedup-books' ddBookScore minus that function's created-at term, which
// dedup-books still applies itself (ddBookScore). Keep the weights here: they
// are the ONE scorer both callers share.
func MetadataScore(b *database.Book) int {
	score := 0
	if b.AuthorID != nil {
		score += 100
	}
	if b.SeriesID != nil {
		score += 20
	}
	if hasText(b.Description) {
		score += 10
	}
	if hasText(b.Narrator) {
		score += 5
	}
	if b.Duration != nil {
		score += 5
	}
	if b.ISBN10 != nil || b.ISBN13 != nil || b.ASIN != nil {
		score += 10
	}
	if b.ITunesPersistentID != nil {
		score += 10
	}
	if hasText(b.Publisher) {
		score += 3
	}
	if hasText(b.Language) {
		score += 2
	}
	if hasText(b.Genre) {
		score += 2
	}
	if hasText(b.CoverURL) {
		score += 3
	}
	return score
}

// hasText is ddBookScore's emptiness test, kept byte-for-byte so the
// dedup-books keeper choice does not move: a whitespace-only value counts.
func hasText(s *string) bool { return s != nil && *s != "" }

// nonEmpty is carry-over's test: a whitespace-only value is empty, so it is
// filled rather than kept.
func nonEmpty(s *string) bool { return s != nil && strings.TrimSpace(*s) != "" }

// Tier is a member's content tier from its signals.
func Tier(s Signals) int {
	switch {
	case s.ActiveFiles == 0 || s.FilesMissingOnDisk > 0:
		return TierNone
	case !s.SingleM4B:
		return TierOther
	case s.ChapterSource != ChapterSourceUnknown && s.Chapters > 1:
		return TierM4BChapters
	default:
		return TierM4BNoChapters
	}
}

// Eligible reports whether a member with these signals may be crowned: live,
// organized, and every active file present under the library root. It is
// the test Elect applies, exported for callers that rank candidates outside
// a version group (MATCH-4's survivor choice).
func Eligible(b *database.Book, s Signals) bool { return ineligibleReason(b, s) == "" }

// ineligibleReason returns "" for an eligible member.
func ineligibleReason(b *database.Book, s Signals) string {
	switch {
	case !s.Live:
		return IneligibleNotLive
	case b.LibraryState == nil || *b.LibraryState != "organized":
		return IneligibleNotOrganized
	case s.ActiveFiles == 0:
		return IneligibleNoActiveFiles
	case s.FilesMissingOnDisk > 0:
		return IneligibleFilesMissing
	case !s.AllUnderRoot:
		return IneligibleOutsideLibrary
	}
	return ""
}

func runtimeMatches(b *database.Book, s Signals) bool {
	if b.AudibleRuntimeMin == nil || *b.AudibleRuntimeMin <= 0 || s.RuntimeSec <= 0 {
		return false
	}
	want := *b.AudibleRuntimeMin * 60
	diff := s.RuntimeSec - want
	if diff < 0 {
		diff = -diff
	}
	return diff*100 <= want*runtimeMatchTolerancePerc
}

func storedFlag(flag *bool) string {
	switch {
	case flag == nil:
		return "nil"
	case *flag:
		return "true"
	default:
		return "false"
	}
}

func explicitTrue(b *database.Book) bool { return b.IsPrimaryVersion != nil && *b.IsPrimaryVersion }

func evaluate(m Member) MemberEval {
	b, s := m.Book, m.Signals
	e := MemberEval{
		BookID:        b.ID,
		Title:         b.Title,
		StoredPrimary: storedFlag(b.IsPrimaryVersion),
		Live:          s.Live,
		Tier:          Tier(s),
		Chapters:      s.Chapters,
		ChapterSource: s.ChapterSource,
		MetadataScore: MetadataScore(b),
		UnknownAuthor: s.UnknownAuthorPath,
		RuntimeMatch:  runtimeMatches(b, s),
		BitrateKbps:   s.BitrateKbps,
		ActiveFiles:   s.ActiveFiles,
		FilesMissing:  s.FilesMissingOnDisk,
		book:          b,
	}
	if e.ChapterSource == "" {
		e.ChapterSource = ChapterSourceNA
	}
	if b.LibraryState != nil {
		e.LibraryState = *b.LibraryState
	}
	if b.MergedIntoBookID != nil {
		e.MergedInto = *b.MergedIntoBookID
	}
	if e.UnknownAuthor {
		e.MetadataScore -= unknownAuthorPenalty
	}
	e.Ineligible = ineligibleReason(b, s)
	e.Eligible = e.Ineligible == ""
	return e
}

// tieBreak orders by earliest created, then lowest ID.
func tieBreak(a, b *database.Book) bool {
	switch {
	case a.CreatedAt != nil && b.CreatedAt != nil && !a.CreatedAt.Equal(*b.CreatedAt):
		return a.CreatedAt.Before(*b.CreatedAt)
	case a.CreatedAt != nil && b.CreatedAt == nil:
		return true
	case a.CreatedAt == nil && b.CreatedAt != nil:
		return false
	}
	return a.ID < b.ID
}

// better reports whether a outranks b: tier, then metadata score, then the
// other signals (runtime within 2% of Audible's, higher bitrate, already the
// explicit primary so a re-run changes nothing), then the tie-break.
func better(a, b *MemberEval) bool {
	if a.Tier != b.Tier {
		return a.Tier > b.Tier
	}
	if a.MetadataScore != b.MetadataScore {
		return a.MetadataScore > b.MetadataScore
	}
	if a.RuntimeMatch != b.RuntimeMatch {
		return a.RuntimeMatch
	}
	if a.BitrateKbps != b.BitrateKbps {
		return a.BitrateKbps > b.BitrateKbps
	}
	if ap, bp := explicitTrue(a.book), explicitTrue(b.book); ap != bp {
		return ap
	}
	return tieBreak(a.book, b.book)
}

// metadataBetter orders eligible members by metadata score alone, then the
// same tie-break, to pick the carry-over donor.
func metadataBetter(a, b *MemberEval) bool {
	if a.MetadataScore != b.MetadataScore {
		return a.MetadataScore > b.MetadataScore
	}
	return tieBreak(a.book, b.book)
}

// Elect decides a group. Members are reported ordered by ID. Non-live
// members are reported but never ranked, never counted toward BestTier, and
// never written by the callers.
func Elect(members []Member) Decision {
	evals := make([]MemberEval, 0, len(members))
	for _, m := range members {
		if m.Book == nil {
			continue
		}
		evals = append(evals, evaluate(m))
	}
	sort.Slice(evals, func(i, j int) bool { return evals[i].BookID < evals[j].BookID })
	d := Decision{Members: evals}

	var winner, donor *MemberEval
	for i := range evals {
		e := &evals[i]
		if !e.Live {
			continue
		}
		if e.Tier > d.BestTier {
			d.BestTier = e.Tier
		}
		if !e.Eligible {
			continue
		}
		if e.Tier > d.BestEligibleTier {
			d.BestEligibleTier = e.Tier
		}
		if winner == nil || better(e, winner) {
			winner = e
		}
		if donor == nil || metadataBetter(e, donor) {
			donor = e
		}
	}

	switch {
	case winner == nil:
		d.Kind, d.HoldReason = DecisionHeld, HoldNeedsOrganizeOrRestore
		d.Reason = "no live member is organized with all of its files present under the library root"
		return d
	case d.BestTier > d.BestEligibleTier:
		d.Kind, d.HoldReason = DecisionHeld, HoldBetterCopyNotInLibrary
		d.Reason = "a copy outside the library has better content (tier " + tierName(d.BestTier) +
			") than every library copy (tier " + tierName(d.BestEligibleTier) + ")"
		return d
	}

	d.WinnerID = winner.BookID
	d.MetadataBestID = donor.BookID
	d.Reason = "best eligible member: tier " + tierName(winner.Tier)
	if donor.BookID != winner.BookID {
		d.Reason += "; metadata from " + donor.BookID + " fills its empty fields"
	}
	d.Kind = DecisionAlreadyOK
	for i := range evals {
		e := &evals[i]
		if !e.Live {
			continue
		}
		want := e.BookID == winner.BookID
		if e.book.IsPrimaryVersion == nil || *e.book.IsPrimaryVersion != want {
			d.Kind = DecisionElect
			break
		}
	}
	return d
}

func tierName(t int) string {
	switch t {
	case TierM4BChapters:
		return "m4b_with_chapters"
	case TierM4BNoChapters:
		return "m4b_without_chapters"
	case TierOther:
		return "other_format"
	}
	return "none"
}

// TierName is the report label for a tier.
func TierName(t int) string { return tierName(t) }
