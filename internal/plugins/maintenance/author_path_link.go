// file: internal/plugins/maintenance/author_path_link.go
// version: 1.2.0
// guid: 4a1b9de2-6c07-4f35-8b1a-9d2e5c7f0a63
// last-edited: 2026-09-19

package maintenance

import (
	"context"
	"encoding/json"
	"fmt"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/oklog/ulid/v2"

	"github.com/falkcorp/audiobook-organizer/internal/applygate"
	"github.com/falkcorp/audiobook-organizer/internal/authorname"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/linkintegrity"
	"github.com/falkcorp/audiobook-organizer/internal/matcher"
	"github.com/falkcorp/audiobook-organizer/internal/metadata"
	"github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/internal/util"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
)

// --- author-path-link ---
//
// 🔴 WHY THIS EXISTS. 5,756 books carry NO author id at all (measured
// 2026-09-19, and the same number maintenance.author-id-repair reports as
// nil_scalar_books_left_alone -- that op sizes this population and declines to
// act on it by design). For about 45% of them the author is sitting in the
// PATH, on an author row that already exists: the book was organized under
// "<root>/<Author>/<Title>/" and only the database link is missing. This op
// makes that link, and nothing else.
//
// 🔴 WHAT IT WILL AND WILL NOT DO. Six buckets, and only two of them write:
//
//	linked                  a path segment passes the person-shape gate and its
//	                        normalized name resolves, through the author:name
//	                        index, to exactly ONE existing row with more than
//	                        one book. Written.
//	created_row_and_linked  the segment is person-shaped, is nobody's near miss,
//	                        and no row exists. A row is created, then linked.
//	near_miss_existing_row  the derived name is within one or two edits of an
//	                        existing row. REPORTED, NEVER WRITTEN: 266 books sit
//	                        under a misspelled "Christopher Paolin" folder, and
//	                        minting that row would park a twin next to the real
//	                        "Christopher Paolini". The remedy is to fix the
//	                        FOLDER; listing the book ids does not override this
//	                        hold (see BookIDs).
//	suspect_thin_row        the target row has book_count <= 1, the shape of a
//	                        title-fragment "author" row. Skipped.
//	suspect_title_fragment_row
//	                        the target row is one author-title-fragment-scan
//	                        would flag. Linking cannot create such a row but
//	                        can attach books to one. Skipped.
//	suspect_leaf_dir        the only match was the book's OWN folder, not an
//	                        ancestor. Skipped.
//	ambiguous_multi_segment two segments resolve to two different rows. Skipped.
//
// 🔴 NEVER TOUCHED, whatever the path says: a book that already has an author
// id (this op only ever fills a nil scalar, it does not correct a wrong or a
// dangling one -- that is author-id-repair's), a book under books/itunes/**
// (standing hands-off rule on the live iTunes library), and anything
// applygate.IsOwnerManualOnly flags (Doctor Who / Big Finish / Torchwood, which
// the owner curates by hand). None of the three is overridable from params:
// bookIds and pathPrefix narrow a run, they do not unlock a refusal.
//
// 🔴 DERIVATION IS THE SCANNER'S, NOT A SECOND COPY. The segment split and the
// person-shape gate are metadata.SplitPathSegments and
// metadata.LooksLikeAuthorSegment -- the same functions internal/metadata's
// folder parser uses. authorname.ExtractAuthorFromDirectory is deliberately NOT
// used: it reads exactly one segment (filepath.Base(filepath.Dir(path))), so on
// ".../Charles Dickens/1844 - Martin Chuzzlewit/xx.mp3" it reads the title
// folder and gives up. The author is the grandparent, so an ancestor walk is
// required.
//
// 🔴 dry_run DEFAULTS TO TRUE, like every sibling. The dry run does every read
// an apply does -- including the per-book join read -- so its change list is a
// prediction of the apply, not a looser estimate.

const (
	authorPathLinkDefaultSample = 25
	authorPathLinkPageSize      = 5000

	// authorPathLinkNearMissMaxEdits is the near-miss threshold: a derived name
	// within this many edits of an existing row's name is reported rather than
	// created. There is no prior definition of "near miss" in the repo, so this
	// is the definition. Two edits catches the measured population -- a dropped
	// letter ("Christopher Paolin" vs "Christopher Paolini"), a missing
	// trailing period ("L. E. Modesitt Jr") -- without pulling in genuinely
	// different short names. Counts from a run will NOT match the 2026-09-19
	// preview's 280 exactly; that number came from a different ad-hoc script.
	authorPathLinkNearMissMaxEdits = 2

	// authorPathLinkThinRowBooks is the suspect bar. A target row with at most
	// this many books is the shape of a title-fragment row ("Freedom's Dawn",
	// row 43771, book_count 0), and attaching 110 books to one would launder it
	// into a plausible author.
	authorPathLinkThinRowBooks = 1
)

// Per-book outcomes. Every scanned in-scope book gets exactly one.
const (
	authorPathLinkLinked           = "linked"
	authorPathLinkWouldLink        = "would_link"
	authorPathLinkCreatedAndLinked = "created_row_and_linked"
	authorPathLinkWouldCreate      = "would_create_row_and_link"
	authorPathLinkNearMiss         = "near_miss_existing_row"
	authorPathLinkSuspectThin      = "suspect_thin_row"
	authorPathLinkSuspectLeaf      = "suspect_leaf_dir"
	authorPathLinkAmbiguous        = "ambiguous_multi_segment"
	authorPathLinkNotDerivable     = "not_derivable"
	authorPathLinkHasAuthor        = "has_author_left_alone"
	authorPathLinkOwnerManual      = "owner_manual_only"
	authorPathLinkITunesHandsOff   = "itunes_hands_off"
	authorPathLinkExistingCredits  = "existing_credits_left_alone"
	authorPathLinkChangedSinceScan = "changed_since_scan"
	authorPathLinkFailed           = "failed"
	// authorPathLinkCreateDisabled is would_create under create_missing=false:
	// a name with no row, which this run will not mint.
	authorPathLinkCreateDisabled = "would_create_row_but_creation_disabled"
	// authorPathLinkSuspectFragment is a target row that
	// maintenance.author-title-fragment-scan itself would flag -- a title, or a
	// leading-hyphen fragment, sitting where an author should be. Linking
	// cannot create such a row but can ATTACH books to one, which is the
	// pre-apply cross-check the approved design asks for.
	authorPathLinkSuspectFragment = "suspect_title_fragment_row"
)

// authorPathLinkDetailed reports whether an outcome's full entry belongs in the
// change list, or only in the counters.
//
// Detail is for what a reader acts on: everything that was or would be written,
// everything held back for review, and every failure. The three "was never a
// candidate" buckets and not_derivable are library-scale -- 70,000-odd books
// between them -- and are counted only.
func authorPathLinkDetailed(outcome string) bool {
	switch outcome {
	case authorPathLinkLinked, authorPathLinkWouldLink,
		authorPathLinkCreatedAndLinked, authorPathLinkWouldCreate, authorPathLinkCreateDisabled,
		authorPathLinkNearMiss, authorPathLinkSuspectThin, authorPathLinkSuspectLeaf,
		authorPathLinkAmbiguous, authorPathLinkExistingCredits, authorPathLinkSuspectFragment,
		authorPathLinkChangedSinceScan, authorPathLinkFailed:
		return true
	}
	return false
}

// Match kinds, recorded on every derived candidate.
const (
	authorPathLinkMatchSegment    = "segment"
	authorPathLinkMatchDashPrefix = "dash_prefix"
)

// authorPathLinkExtraRoots are container segments this library has that
// metadata's own skipSegments map lacks.
//
// 🔴 KEPT LOCAL ON PURPOSE. Adding them to folder_parser.go's skipSegments
// would change how the SCANNER parses every future import, which is a much
// larger blast radius than this op. Here they only stop a storage root from
// being offered to the person-shape gate.
var authorPathLinkExtraRoots = map[string]bool{
	"abooks":           true,
	"itunes":           true,
	"itunes media":     true,
	"read by narrator": true,
	"eb2":              true,
	"personal":         true,
	"usr":              true,
	"share":            true,
	"sounds":           true,
	"moved":            true,
	"imported":         true,
}

// authorPathLinkParams are the op's JSON parameters.
type authorPathLinkParams struct {
	// DryRun defaults to TRUE when absent. dryRun (camelCase) is accepted as an
	// alias; sending both with different values is an error, never a guess.
	DryRun      *bool `json:"dry_run,omitempty"`
	DryRunCamel *bool `json:"dryRun,omitempty"`
	// BookIDs restricts the run to these books. Everything else is not even
	// classified.
	//
	// 🔴 IT IS A SCOPE, NOT AN OVERRIDE. Naming a book does not buy it past any
	// refusal: the manual-only filter (Doctor Who / Big Finish / Torchwood),
	// the iTunes hands-off rule, the near-miss hold and the suspect bars all
	// still apply to a book listed here. So the 266 books under the misspelled
	// "Christopher Paolin" folder cannot be linked to "Christopher Paolini" by
	// listing their ids -- this op only ever writes the author its NAME
	// resolves to, and that name resolves to nothing. The remedy for a near
	// miss is to fix the folder (after which this op links it on the next run),
	// not to hand the op a list. Widening bookIds into a real override is an
	// owner decision, not a detail of this op.
	BookIDs      []string `json:"bookIds,omitempty"`
	BookIDsSnake []string `json:"book_ids,omitempty"`
	// PathPrefix restricts the run to books whose FilePath starts with it.
	PathPrefix      string `json:"pathPrefix,omitempty"`
	PathPrefixSnake string `json:"path_prefix,omitempty"`
	// CreateMissing defaults to TRUE: a person-shaped segment that is nobody's
	// near miss gets a new author row. Set false for a link-only run.
	CreateMissing *bool `json:"create_missing,omitempty"`
	// SampleLimit bounds the logged sample (the full change list is always in
	// the op result). Default 25.
	SampleLimit int `json:"sample_limit,omitempty"`
}

func (p authorPathLinkParams) bookIDs() []string {
	if len(p.BookIDs) > 0 {
		return p.BookIDs
	}
	return p.BookIDsSnake
}

func (p authorPathLinkParams) pathPrefix() string {
	if p.PathPrefix != "" {
		return p.PathPrefix
	}
	return p.PathPrefixSnake
}

// authorPathLinkChange is one book's plan or outcome.
type authorPathLinkChange struct {
	BookID   string `json:"book_id"`
	FilePath string `json:"file_path"`
	// DerivedName is the path segment the gate accepted, verbatim.
	DerivedName string `json:"derived_name,omitempty"`
	MatchKind   string `json:"match_kind,omitempty"`
	// AuthorID is the row this book was (or would be) linked to; 0 when the
	// book is not linkable or the row does not exist yet.
	AuthorID   int    `json:"author_id,omitempty"`
	AuthorName string `json:"author_name,omitempty"`
	// TargetBookCount is the target row's live-book count at scan time, the
	// number the suspect bar is applied to.
	TargetBookCount int `json:"target_book_count,omitempty"`
	// NearestAuthorID/Name/Distance describe the row a near miss nearly hit.
	NearestAuthorID   int    `json:"nearest_author_id,omitempty"`
	NearestAuthorName string `json:"nearest_author_name,omitempty"`
	NearestDistance   int    `json:"nearest_distance,omitempty"`
	// AmbiguousAuthorIDs lists every distinct row the path resolved to, for the
	// ambiguous bucket.
	AmbiguousAuthorIDs []int `json:"ambiguous_author_ids,omitempty"`

	Outcome string `json:"outcome"`
	Applied bool   `json:"applied"`
	Error   string `json:"error,omitempty"`
}

// authorPathLinkCreatedAuthor records one minted row.
type authorPathLinkCreatedAuthor struct {
	AuthorID int    `json:"author_id"`
	Name     string `json:"name"`
	Books    int    `json:"books"`
}

// authorPathLinkResult is the op result.
type authorPathLinkResult struct {
	DryRun        bool           `json:"dry_run"`
	CreateMissing bool           `json:"create_missing"`
	PathPrefix    string         `json:"path_prefix,omitempty"`
	ExplicitIDs   int            `json:"explicit_book_ids,omitempty"`
	BooksScanned  int            `json:"books_scanned"`
	BooksInScope  int            `json:"books_in_scope"`
	SoftDeleted   int            `json:"soft_deleted"`
	EmptyFilePath int            `json:"empty_file_path"`
	AuthorRows    int            `json:"author_rows"`
	Outcomes      map[string]int `json:"outcomes"`
	// Changes is every in-scope book, one entry each, sorted by book id.
	Changes        []authorPathLinkChange        `json:"changes"`
	CreatedAuthors []authorPathLinkCreatedAuthor `json:"created_authors"`
	UndoLedgerRows int                           `json:"undo_ledger_rows"`
}

// authorPathLinkAuthorStore is the author-row half of this op's store surface:
// resolve a derived name to a row, and mint one when it is nobody's near miss.
type authorPathLinkAuthorStore interface {
	GetAllAuthors() ([]database.Author, error)
	GetAuthorByName(name string) (*database.Author, error)
	GetAuthorByID(id int) (*database.Author, error)
	CreateAuthor(name string) (*database.Author, error)
}

// authorPathLinkBookStore is the book half: the library sweep, the per-book
// re-read, the two writes and the undo-ledger row.
type authorPathLinkBookStore interface {
	GetAllBooksCoreComplete(limit, offset int) ([]database.BookCore, error)
	GetBookByID(id string) (*database.Book, error)
	GetBookAuthors(bookID string) ([]database.BookAuthor, error)
	ModifyBookAuthors(bookID string, fn func([]database.BookAuthor) ([]database.BookAuthor, error)) ([]database.BookAuthor, error)
	ModifyBook(id string, fn func(*database.Book) error) (*database.Book, error)
	CreateOperationChange(change *database.OperationChange) error
}

// authorPathLinkStore is the narrow store surface this op needs, per the
// store_slices.go convention of listing methods explicitly. It is the
// composition of the two halves above, so neither piece is wider than the
// interfacebloat bar and each call site can name only the half it uses.
type authorPathLinkStore interface {
	authorPathLinkAuthorStore
	authorPathLinkBookStore
}

func (p *Plugin) authorPathLinkDef() sdk.OperationDef {
	return sdk.OperationDef{
		ID:          "maintenance.author-path-link",
		Liveness:    sdk.LivenessManual,
		Plugin:      "maintenance",
		DisplayName: "Link path-derived authors to author rows",
		Description: "Books with NO author id whose PATH names an author: the person-shaped path segment " +
			"is resolved through the author:name index and the book is linked to that row (join row and " +
			"scalar). A person-shaped name with no row is created and linked; a NEAR MISS of an existing " +
			"row is reported and never created. Suspect targets (book_count<=1, or a match on the book's " +
			"own folder) and ambiguous paths are skipped. Books that already have an author id, books " +
			"under books/itunes/**, and Doctor Who / Big Finish / Torchwood are never touched. Defaults " +
			"to dry_run=true; bookIds and pathPrefix scope a run.",
		// Idempotent: a linked book has a non-nil scalar and is out of the
		// population, so a restart re-plans from current state.
		ResumePolicy:    sdk.ResumeRestart,
		DefaultPriority: sdk.PriorityLow,
		ConcurrencyKey:  "maintenance.author-path-link",
		Writes:          []sdk.Resource{sdk.ResBooks, sdk.ResAuthors},
		Reads:           []sdk.Resource{sdk.ResBooks, sdk.ResAuthors},
		Cancellable:     true,
		Isolate:         false,
		Timeout:         2 * time.Hour,
		Capabilities:    []sdk.Capability{sdk.CapLibraryRead, sdk.CapLibraryWrite},
		Run:             p.runAuthorPathLink,
	}
}

func (p *Plugin) runAuthorPathLink(ctx context.Context, rawParams json.RawMessage, reporter sdk.Reporter) error {
	var params authorPathLinkParams
	if len(rawParams) > 0 {
		if err := json.Unmarshal(rawParams, &params); err != nil {
			return fmt.Errorf("maintenance.author-path-link: decode params: %w", err)
		}
	}
	if params.DryRun != nil && params.DryRunCamel != nil && *params.DryRun != *params.DryRunCamel {
		return fmt.Errorf("maintenance.author-path-link: dry_run=%v and dryRun=%v disagree; send one",
			*params.DryRun, *params.DryRunCamel)
	}
	if len(params.BookIDs) > 0 && len(params.BookIDsSnake) > 0 {
		return fmt.Errorf("maintenance.author-path-link: bookIds and book_ids both sent; send one")
	}
	if params.PathPrefix != "" && params.PathPrefixSnake != "" {
		return fmt.Errorf("maintenance.author-path-link: pathPrefix and path_prefix both sent; send one")
	}
	result, err := p.authorPathLink(ctx, params, reporter)
	if result != nil {
		if serr := registry.ReporterSetResult(reporter, result); serr != nil {
			reporter.Logger().Debug("author-path-link: result not persisted", "err", serr)
		}
	}
	return err
}

// normalizeAuthorNameForLink is the author:name index key: the ONLY comparison
// this op makes between a derived name and an author row. util.NormalizeAuthor
// is named rather than reimplemented because a divergence here would resolve a
// name to a row the index cannot reach.
func normalizeAuthorNameForLink(s string) string { return util.NormalizeAuthor(s) }

// authorPathLinkPersonShaped is the person-shape gate, with one bound this op
// adds on top of the shared one.
//
// metadata.LooksLikeAuthorSegment has an early return in its initials branch:
// a segment containing a "." and two capitals passes BEFORE the 2-to-5 word
// bound is ever applied, so an arbitrarily long title segment gets through --
// "Book 03 - The Hero of Ages (Unabridged) [v1.0]" is eight words with one dot
// and passes it. That branch is shared with the scanner's import path
// (folder_parser.go: tryParseAuthorSegment, parseInnermostSegment and the
// dash-split fallback all rely on it), and widening or narrowing it there would
// change how every future scan parses a folder name -- a much larger blast
// radius than this op. So the word bound is applied HERE instead, where it can
// only ever cost this op a candidate. Fixing the shared branch is worth doing
// on its own, with the scanner's tests in scope; it is not this PR.
func authorPathLinkPersonShaped(name string) bool {
	if !metadata.LooksLikeAuthorSegment(name) {
		return false
	}
	if n := len(strings.Fields(name)); n < 2 || n > 5 {
		return false
	}
	return true
}

// authorPathLinkCandidate is one gate-passing segment of a book's path.
type authorPathLinkCandidate struct {
	Name   string
	Kind   string
	IsLeaf bool
}

// authorPathLinkCandidates walks a book's path outside-in and returns every
// segment that passes the person-shape gate, shallowest first.
//
// The file segment is dropped, container roots are dropped (metadata's own map
// plus authorPathLinkExtraRoots), and each surviving segment contributes both
// itself and the left side of a " - " split ("Michael Grant - Gone (7-9)" ->
// "Michael Grant"). IsLeaf marks a candidate derived from the DEEPEST surviving
// directory -- the book's own folder -- which is never trusted on its own.
func authorPathLinkCandidates(filePath string) []authorPathLinkCandidate {
	segs := metadata.SplitPathSegments(filePath)
	// Drop the filename and any remaining storage roots.
	dirs := make([]string, 0, len(segs))
	for i, s := range segs {
		if i == len(segs)-1 && linkintegrity.IsAudioFile(s) {
			continue
		}
		if authorPathLinkExtraRoots[strings.ToLower(strings.TrimSpace(s))] {
			continue
		}
		dirs = append(dirs, s)
	}
	out := make([]authorPathLinkCandidate, 0, len(dirs)*2)
	for i, seg := range dirs {
		leaf := i == len(dirs)-1
		add := func(name, kind string) {
			name = strings.TrimSpace(name)
			if name == "" || authorname.IsPlaceholder(name) {
				return
			}
			if !authorPathLinkPersonShaped(name) {
				return
			}
			out = append(out, authorPathLinkCandidate{Name: name, Kind: kind, IsLeaf: leaf})
		}
		add(seg, authorPathLinkMatchSegment)
		if left, _, ok := strings.Cut(seg, " - "); ok {
			add(left, authorPathLinkMatchDashPrefix)
		}
	}
	return out
}

// authorPathLinkIndex is the frozen classification input: the author rows by
// normalized name, and each row's live-book count.
//
// 🔴 FROZEN BEFORE ANY WRITE, on purpose. book_count has no column -- it is
// "live books whose scalar AuthorID names this row", counted over the single
// GetAllBooksCoreComplete snapshot. If it were recomputed as the run proceeded,
// books linked early would inflate a row past the suspect bar and change how
// LATER books classify, so a dry run would stop predicting the apply.
type authorPathLinkIndex struct {
	byNormalized map[string]database.Author
	bookCount    map[int]int
	// titleFragment marks the normalized names whose row author-title-fragment-scan
	// would flag: a title sitting where an author should be. Never a link target.
	titleFragment map[string]bool
	// normalizedNames is the near-miss search space, bucketed by name length so
	// a lookup compares against a few hundred names rather than all 14,701.
	byLength map[int][]string
}

func (idx *authorPathLinkIndex) nearest(name string) (database.Author, int, bool) {
	norm := normalizeAuthorNameForLink(name)
	best, bestDist := database.Author{}, authorPathLinkNearMissMaxEdits+1
	for l := len(norm) - authorPathLinkNearMissMaxEdits; l <= len(norm)+authorPathLinkNearMissMaxEdits; l++ {
		for _, cand := range idx.byLength[l] {
			d := matcher.LevenshteinDistance(norm, cand)
			if d < bestDist || (d == bestDist && idx.byNormalized[cand].ID < best.ID) {
				best, bestDist = idx.byNormalized[cand], d
			}
		}
	}
	if bestDist > authorPathLinkNearMissMaxEdits {
		return database.Author{}, 0, false
	}
	return best, bestDist, true
}

// authorPathLinkClassify decides one book's bucket from the frozen index alone.
// It performs no store read, so it is safe to call from any worker.
func authorPathLinkClassify(b *database.BookCore, idx *authorPathLinkIndex) authorPathLinkChange {
	ch := authorPathLinkChange{BookID: b.ID, FilePath: b.FilePath}
	if b.AuthorID != nil {
		ch.Outcome = authorPathLinkHasAuthor
		ch.AuthorID = *b.AuthorID
		return ch
	}
	if authorPathLinkIsITunes(b.FilePath) {
		ch.Outcome = authorPathLinkITunesHandsOff
		return ch
	}
	if applygate.IsOwnerManualOnly(b.FilePath, "") {
		ch.Outcome = authorPathLinkOwnerManual
		return ch
	}

	candidates := authorPathLinkCandidates(b.FilePath)
	if len(candidates) == 0 {
		ch.Outcome = authorPathLinkNotDerivable
		return ch
	}

	// Exact matches first: shallowest wins, but two DIFFERENT rows anywhere in
	// the path is ambiguous and nothing is written.
	var matched *authorPathLinkCandidate
	var matchedAuthor database.Author
	ids := map[int]struct{}{}
	for i := range candidates {
		a, ok := idx.byNormalized[normalizeAuthorNameForLink(candidates[i].Name)]
		if !ok {
			continue
		}
		ids[a.ID] = struct{}{}
		if matched == nil {
			matched, matchedAuthor = &candidates[i], a
		}
	}
	if len(ids) > 1 {
		ch.Outcome = authorPathLinkAmbiguous
		for id := range ids {
			ch.AmbiguousAuthorIDs = append(ch.AmbiguousAuthorIDs, id)
		}
		sort.Ints(ch.AmbiguousAuthorIDs)
		return ch
	}
	if matched != nil {
		ch.DerivedName, ch.MatchKind = matched.Name, matched.Kind
		ch.AuthorID, ch.AuthorName = matchedAuthor.ID, matchedAuthor.Name
		ch.TargetBookCount = idx.bookCount[matchedAuthor.ID]
		switch {
		case matched.IsLeaf:
			ch.Outcome = authorPathLinkSuspectLeaf
		case idx.titleFragment[normalizeAuthorNameForLink(matchedAuthor.Name)]:
			ch.Outcome = authorPathLinkSuspectFragment
		case ch.TargetBookCount <= authorPathLinkThinRowBooks:
			ch.Outcome = authorPathLinkSuspectThin
		default:
			ch.Outcome = authorPathLinkWouldLink
		}
		return ch
	}

	// No exact hit. The shallowest non-leaf candidate is the derived name; a
	// leaf-only derivation is not trusted enough to mint a row from.
	var derived *authorPathLinkCandidate
	for i := range candidates {
		if !candidates[i].IsLeaf {
			derived = &candidates[i]
			break
		}
	}
	if derived == nil {
		ch.Outcome = authorPathLinkSuspectLeaf
		ch.DerivedName = candidates[0].Name
		ch.MatchKind = candidates[0].Kind
		return ch
	}
	ch.DerivedName, ch.MatchKind = derived.Name, derived.Kind
	if near, dist, ok := idx.nearest(derived.Name); ok {
		ch.Outcome = authorPathLinkNearMiss
		ch.NearestAuthorID, ch.NearestAuthorName, ch.NearestDistance = near.ID, near.Name, dist
		return ch
	}
	ch.Outcome = authorPathLinkWouldCreate
	return ch
}

// authorPathLinkIsITunes reports whether the path runs through the live iTunes
// library, which is hands-off by standing owner rule. Segment equality, not
// substring: a title with "itunes" in it must not match.
//
// It matches "<...>/books/itunes/<...>" specifically -- the ONE iTunes root
// this library has -- rather than any segment named "itunes", because an
// audiobook whose own folder is called "iTunes" would otherwise be excluded
// from a repair it belongs in. A second iTunes root would need adding here.
func authorPathLinkIsITunes(path string) bool {
	segs := strings.FieldsFunc(path, func(r rune) bool { return r == '/' || r == '\\' })
	for i, s := range segs {
		if strings.EqualFold(strings.TrimSpace(s), "itunes") && i > 0 && strings.EqualFold(strings.TrimSpace(segs[i-1]), "books") {
			return true
		}
	}
	return false
}

func (p *Plugin) authorPathLink(ctx context.Context, params authorPathLinkParams, reporter sdk.Reporter) (*authorPathLinkResult, error) {
	store := p.deps.OpsStore()
	if store == nil {
		return nil, fmt.Errorf("database not initialized")
	}
	dryRun := true
	if params.DryRun != nil {
		dryRun = *params.DryRun
	} else if params.DryRunCamel != nil {
		dryRun = *params.DryRunCamel
	}
	createMissing := true
	if params.CreateMissing != nil {
		createMissing = *params.CreateMissing
	}
	sample := params.SampleLimit
	if sample <= 0 {
		sample = authorPathLinkDefaultSample
	}
	log := reporter.Logger()

	res := &authorPathLinkResult{
		DryRun:         dryRun,
		CreateMissing:  createMissing,
		PathPrefix:     params.pathPrefix(),
		ExplicitIDs:    len(params.bookIDs()),
		Outcomes:       map[string]int{},
		Changes:        []authorPathLinkChange{},
		CreatedAuthors: []authorPathLinkCreatedAuthor{},
	}

	// Stand-down for the write phase only, as author-id-repair does: a running
	// scan rewrites book rows underneath an apply. A dry run parks nothing.
	var standDownHolder string
	var standDownHeld bool
	if !dryRun {
		holderID, held, release, sdErr := acquireScanStandDownForApply(ctx, p.deps, reporter, "author-path-link apply")
		if sdErr != nil {
			return res, fmt.Errorf("author-path-link: acquire scan stand-down: %w", sdErr)
		}
		defer release()
		standDownHolder, standDownHeld = holderID, held
	}
	lost := func() bool { return scanStandDownLostForApply(p.deps, standDownHolder, standDownHeld) }
	opID := registry.ReporterOpID(reporter)

	// Named once at the top so this op's whole store requirement is one type;
	// each helper below then takes only the half it uses.
	var linkStore authorPathLinkStore = store

	idx, err := authorPathLinkBuildIndex(linkStore)
	if err != nil {
		return res, err
	}
	res.AuthorRows = len(idx.byNormalized)

	// The scan pass also fills idx.bookCount, so every classification below
	// reads ONE frozen count per author row.
	scoped, err := authorPathLinkScope(ctx, linkStore, params, res, idx, reporter)
	if err != nil {
		return res, err
	}
	res.BooksInScope = len(scoped)

	var mu sync.Mutex
	done := 0
	// record counts every classified book, and keeps the FULL entry only for
	// the outcomes a reader acts on (authorPathLinkDetailed). The library-wide
	// buckets -- 70,000-odd books that already have an author, 2,390 with no
	// author anywhere in the path -- would otherwise put a book id and a full
	// path each into the op result, burying the few hundred rows the dry run
	// exists to show. author_id_repair.go makes the same split by recording an
	// empty struct for its no-op outcomes.
	record := func(ch authorPathLinkChange) {
		mu.Lock()
		defer mu.Unlock()
		done++
		res.Outcomes[ch.Outcome]++
		if authorPathLinkDetailed(ch.Outcome) {
			res.Changes = append(res.Changes, ch)
		}
	}

	// 🔴 CLASSIFICATION IS PARALLEL, and has to be: the near-miss check is a
	// fuzzy string compare against every author row in a +/-2 length band, and
	// it runs for every book with no exact match (~2,500 of them in prod). That
	// is exactly the fuzzy-compare-over-a-whole-library shape CLAUDE.md names.
	// The frozen index is read-only from here on, so workers share it without a
	// lock; only record() needs one.
	var actionable []authorPathLinkChange
	classifyErr := registry.RunItems(ctx, reporter, scoped, func(_ context.Context, b database.BookCore) error {
		ch := authorPathLinkClassify(&b, idx)
		if ch.Outcome == authorPathLinkWouldLink || (ch.Outcome == authorPathLinkWouldCreate && createMissing) {
			mu.Lock()
			actionable = append(actionable, ch)
			mu.Unlock()
			return nil
		}
		if ch.Outcome == authorPathLinkWouldCreate {
			// create_missing=false: nothing will be created, so it does not
			// claim it would be.
			ch.Outcome = authorPathLinkCreateDisabled
		}
		record(ch)
		return nil
	}, registry.RunItemsOptions{
		Concurrency: runtime.NumCPU(),
		// Label runs inside each worker goroutine, so it reads done under mu.
		Label: func(i, total int) string {
			mu.Lock()
			d := done
			mu.Unlock()
			return fmt.Sprintf("Classifying paths %d/%d (decided %d)", i+1, total, d)
		},
	})
	if classifyErr != nil {
		return res, classifyErr
	}
	// The apply order is the book id order, not the order workers finished in.
	sort.Slice(actionable, func(i, j int) bool { return actionable[i].BookID < actionable[j].BookID })

	creator := newAuthorPathLinkCreator(linkStore, dryRun)
	runErr := registry.RunItems(ctx, reporter, actionable, func(ctx context.Context, ch authorPathLinkChange) error {
		out := p.authorPathLinkApplyOne(ch, linkStore, creator, dryRun, opID, lost, res, &mu)
		record(out)
		return nil
	}, registry.RunItemsOptions{
		Concurrency: runtime.NumCPU(),
		// Label runs inside each worker goroutine, so it reads done under mu.
		Label: func(i, total int) string {
			mu.Lock()
			d := done
			mu.Unlock()
			return fmt.Sprintf("Linking path-derived authors %d/%d (classified %d)", i+1, total, d)
		},
	})

	res.CreatedAuthors = creator.created()
	sort.Slice(res.Changes, func(i, j int) bool { return res.Changes[i].BookID < res.Changes[j].BookID })
	if res.Outcomes[authorPathLinkLinked] > 0 || res.Outcomes[authorPathLinkCreatedAndLinked] > 0 {
		p.deps.InvalidateAuthorsCache()
		p.deps.InvalidateDedupCache()
	}

	summary := fmt.Sprintf(
		"author-path-link complete (dry_run=%v): scanned=%d in_scope=%d author_rows=%d outcomes=%v created_rows=%d undo_rows=%d",
		dryRun, res.BooksScanned, res.BooksInScope, res.AuthorRows, res.Outcomes, len(res.CreatedAuthors), res.UndoLedgerRows)
	log.Info("author-path-link: done", "dry_run", dryRun, "sample", firstN(res.Changes, sample))
	_ = reporter.UpdateProgress(1, 1, summary)
	if runErr != nil {
		return res, runErr
	}
	return res, nil
}

// authorPathLinkBuildIndex freezes the author rows and their book counts.
func authorPathLinkBuildIndex(store authorPathLinkAuthorStore) (*authorPathLinkIndex, error) {
	authors, err := store.GetAllAuthors()
	if err != nil {
		return nil, fmt.Errorf("author-path-link: list authors: %w", err)
	}
	idx := &authorPathLinkIndex{
		byNormalized:  make(map[string]database.Author, len(authors)),
		bookCount:     map[int]int{},
		byLength:      map[int][]string{},
		titleFragment: map[string]bool{},
	}
	for _, a := range authors {
		norm := normalizeAuthorNameForLink(a.Name)
		if norm == "" {
			continue
		}
		prev, seen := idx.byNormalized[norm]
		if !seen {
			idx.byLength[len(norm)] = append(idx.byLength[len(norm)], norm)
			idx.byNormalized[norm] = a
			continue
		}
		if prev.ID == a.ID {
			continue
		}
		// 🔴 A DUPLICATE NORMALIZED NAME IS RESOLVED THROUGH THE INDEX, not by
		// picking one. One name maps to ONE id in author:name, and that id is
		// the one the apply path will re-resolve to; guessing here (the lowest
		// id, say) would make the dry run predict a link the apply then
		// refuses as changed_since_scan, silently. So the tie is broken by
		// asking the index itself -- once per cluster, and there are six in
		// prod. If the read fails, the first row seen is kept and the apply's
		// own re-resolve remains the backstop.
		if canonical, cErr := store.GetAuthorByName(a.Name); cErr == nil && canonical != nil {
			idx.byNormalized[norm] = *canonical
		}
	}
	// A target row whose NAME is itself a title fragment ("Freedom's Dawn") is
	// refused, using author-title-fragment-scan's own predicate rather than a
	// second opinion. Linking cannot CREATE such a row, but it can attach books
	// to one that already exists, which is the cross-check the approved design
	// asks for before an apply.
	for norm, a := range idx.byNormalized {
		if classifyTitleFragmentAuthor(a.Name) != "" {
			idx.titleFragment[norm] = true
		}
	}
	return idx, nil
}

// authorPathLinkUnderPrefix reports whether path is the prefix itself or sits
// beneath it, comparing on a path-separator boundary so a prefix cannot select
// a sibling directory whose name merely starts with the same characters.
func authorPathLinkUnderPrefix(path, prefix string) bool {
	if path == prefix {
		return true
	}
	if !strings.HasSuffix(prefix, "/") {
		prefix += "/"
	}
	return strings.HasPrefix(path, prefix)
}

// authorPathLinkScope pages the whole library once and returns the in-scope
// books, counting what it dropped. GetAllBooksCoreComplete (not the memdb-served
// twin) because this op writes.
func authorPathLinkScope(ctx context.Context, store authorPathLinkBookStore, params authorPathLinkParams, res *authorPathLinkResult, idx *authorPathLinkIndex, reporter sdk.Reporter) ([]database.BookCore, error) {
	wanted := map[string]bool{}
	for _, id := range params.bookIDs() {
		wanted[id] = true
	}
	prefix := params.pathPrefix()

	var scoped []database.BookCore
	for offset := 0; ; offset += authorPathLinkPageSize {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		page, err := store.GetAllBooksCoreComplete(authorPathLinkPageSize, offset)
		if err != nil {
			return nil, fmt.Errorf("author-path-link: list books at offset %d: %w", offset, err)
		}
		for i := range page {
			b := page[i]
			res.BooksScanned++
			if b.IsSoftDeleted() {
				res.SoftDeleted++
				continue
			}
			// book_count is counted here, over EVERY live row and before any
			// scope filter: scoping must not make a thin row look fat, and a
			// row's count must not change as the run links books to it.
			if b.AuthorID != nil {
				idx.bookCount[*b.AuthorID]++
			}
			if strings.TrimSpace(b.FilePath) == "" {
				res.EmptyFilePath++
				continue
			}
			if len(wanted) > 0 && !wanted[b.ID] {
				continue
			}
			// The prefix matches on a SEPARATOR boundary: "/mnt/books/ab"
			// must not scope in "/mnt/books/abooks/...". A prefix that
			// already ends in "/" is used as given.
			if prefix != "" && !authorPathLinkUnderPrefix(b.FilePath, prefix) {
				continue
			}
			scoped = append(scoped, b)
		}
		_ = reporter.UpdateProgress(res.BooksScanned, 0,
			fmt.Sprintf("Scanned %d books, %d in scope", res.BooksScanned, len(scoped)))
		if len(page) < authorPathLinkPageSize {
			break
		}
	}
	return scoped, nil
}

// authorPathLinkCreator resolves a derived name to an author row, creating it
// at most once per run.
//
// 🔴 HOW A DUPLICATE ROW IS PREVENTED, three deep. (1) database.CreateAuthor is
// itself resolve-or-create: it re-reads the author:name index UNDER the
// nameIdx.author lock and returns the row another goroutine just committed
// rather than minting a second one (pebble_store_authors.go:136; the race it
// fixes was measured on 2026-08-25 as 24 rows for 24 concurrent identical
// calls). (2) This creator serializes its own calls and re-checks
// GetAuthorByName immediately before CreateAuthor, so a row that appeared
// between classification and now is REUSED. (3) The per-run map means 24 books
// deriving one name make one call, not 24.
type authorPathLinkCreator struct {
	store  authorPathLinkAuthorStore
	dryRun bool
	mu     sync.Mutex
	byName map[string]database.Author
	minted map[int]*authorPathLinkCreatedAuthor
	// wouldMint is the dry run's twin of minted, keyed by normalized name
	// because the rows have no id yet.
	wouldMint map[string]*authorPathLinkCreatedAuthor
}

func newAuthorPathLinkCreator(store authorPathLinkAuthorStore, dryRun bool) *authorPathLinkCreator {
	return &authorPathLinkCreator{
		store:     store,
		dryRun:    dryRun,
		byName:    map[string]database.Author{},
		minted:    map[int]*authorPathLinkCreatedAuthor{},
		wouldMint: map[string]*authorPathLinkCreatedAuthor{},
	}
}

// resolveOrCreate returns the row for name, minting it only when no row exists
// AND allowCreate says minting is in order. The bool reports whether this run
// created it.
func (c *authorPathLinkCreator) resolveOrCreate(name string, allowCreate bool) (database.Author, bool, error) {
	norm := normalizeAuthorNameForLink(name)
	c.mu.Lock()
	defer c.mu.Unlock()
	if a, ok := c.byName[norm]; ok {
		if m, minted := c.minted[a.ID]; minted {
			m.Books++
		}
		return a, c.minted[a.ID] != nil, nil
	}
	existing, err := c.store.GetAuthorByName(name)
	if err != nil {
		return database.Author{}, false, err
	}
	if existing != nil {
		c.byName[norm] = *existing
		return *existing, false, nil
	}
	if !allowCreate {
		return database.Author{}, false, nil
	}
	if c.dryRun {
		// A dry run mints nothing, but the preview still has to say how many
		// DISTINCT rows an apply would add and what they would be called --
		// the owner approves a creation count, not a book count. Keyed by the
		// normalized name, so twenty books deriving one name count once.
		if m, ok := c.wouldMint[norm]; ok {
			m.Books++
		} else {
			c.wouldMint[norm] = &authorPathLinkCreatedAuthor{Name: strings.TrimSpace(name), Books: 1}
		}
		return database.Author{}, false, nil
	}
	created, err := c.store.CreateAuthor(name)
	if err != nil {
		return database.Author{}, false, err
	}
	if created == nil || created.ID <= 0 {
		return database.Author{}, false, fmt.Errorf("create author %q returned no row", name)
	}
	c.byName[norm] = *created
	c.minted[created.ID] = &authorPathLinkCreatedAuthor{AuthorID: created.ID, Name: created.Name, Books: 1}
	return *created, true, nil
}

func (c *authorPathLinkCreator) created() []authorPathLinkCreatedAuthor {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]authorPathLinkCreatedAuthor, 0, len(c.minted)+len(c.wouldMint))
	for _, m := range c.minted {
		out = append(out, *m)
	}
	// A dry run's entries carry AuthorID 0 -- the row does not exist -- so the
	// list is the rows an apply WOULD add, one per distinct name.
	for _, m := range c.wouldMint {
		out = append(out, *m)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].AuthorID != out[j].AuthorID {
			return out[i].AuthorID < out[j].AuthorID
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// authorPathLinkApplyOne re-verifies one book against the durable row and, on an
// apply, writes it.
//
// 🔴 WRITE ORDER: JOIN ROW FIRST, SCALAR SECOND. There is no batch spanning
// ModifyBookAuthors and ModifyBook, so a crash can land between them. Scalar
// first would leave a book with a non-nil AuthorID and no credit -- and the
// next run's "AuthorID == nil" filter would never select it again, so the
// missing join would be permanent and invisible. Join first leaves AuthorID
// nil, so the next run re-selects the book.
//
// 🔴 AND RE-SELECTING IT IS NOT ENOUGH ON ITS OWN. Until 2026-09-19 the credit
// read below returned existing_credits_left_alone for ANY non-empty join, which
// meant a book stranded between the two writes was stranded FOREVER: this op
// refused it on every re-run and author-id-repair, which only acts on non-nil
// dangling scalars, never saw it. With ~2,748 write pairs in the first apply, a
// single transient store error was enough. The credit read now recognises this
// op's own half-write and resumes it; see the comment there.
//
// A single Pebble batch across both keys -- what the approved design asked for
// -- was examined and NOT taken: the book write goes through
// updateBookLockedMode, which maintains the secondary indexes, the memdb
// projection and the signature sidecar, and a combined primitive would have to
// duplicate or re-plumb that whole path. That is a store change far wider than
// this op, and it is the right follow-up; the resume above makes the invariant
// hold without it.
func (p *Plugin) authorPathLinkApplyOne(
	ch authorPathLinkChange,
	store authorPathLinkBookStore,
	creator *authorPathLinkCreator,
	dryRun bool,
	opID string,
	lost func() bool,
	res *authorPathLinkResult,
	mu *sync.Mutex,
) authorPathLinkChange {
	fail := func(err error) authorPathLinkChange {
		ch.Outcome, ch.Error = authorPathLinkFailed, err.Error()
		return ch
	}

	// The durable row decides, not the snapshot.
	full, err := store.GetBookByID(ch.BookID)
	if err != nil {
		return fail(err)
	}
	if full == nil || full.AuthorID != nil {
		ch.Outcome = authorPathLinkChangedSinceScan
		return ch
	}

	// A book already carrying credits keeps them: SetBookAuthors semantics are
	// replace-the-array, and a nil scalar beside a real co-author credit is a
	// different repair from this one.
	//
	// 🔴 EXCEPT THE ONE CREDIT THIS OP ITSELF WRITES. The join row and the
	// scalar are two commits with no batch around them (see the write-order
	// note above), so a crash or a transient store error between them leaves
	// exactly one credit -- {this book, target author, role "author", position
	// 0} -- beside a nil scalar. Treating that as "already has credits" would
	// strand the book forever: no re-run of this op would finish it, and
	// author-id-repair only acts on NON-nil dangling scalars, so nothing else
	// would either. A single credit of exactly that shape, naming exactly the
	// author this book's path derives, is therefore RESUMED: the join write is
	// skipped and the scalar write completes the pair. Any other credit -- two
	// rows, a co-author role, a non-zero position, a different author -- is
	// somebody else's data and is left alone.
	joins, err := store.GetBookAuthors(ch.BookID)
	if err != nil {
		return fail(err)
	}
	resuming := false
	switch {
	case len(joins) == 0:
	case len(joins) == 1 && joins[0].Role == "author" && joins[0].Position == 0:
		resuming = true
	default:
		ch.Outcome = authorPathLinkExistingCredits
		return ch
	}

	// Resolve the target through the name index, never through the snapshot id.
	// A half-written book must NOT mint a row: its credit already names one, so
	// creation is allowed only when there is no credit to contradict.
	author, minted, err := creator.resolveOrCreate(ch.DerivedName, !resuming)
	if err != nil {
		return fail(err)
	}
	if resuming && author.ID != joins[0].AuthorID {
		// The credit names someone other than the path's author: not this op's
		// half-write, so it is left alone.
		ch.Outcome = authorPathLinkExistingCredits
		return ch
	}
	switch {
	case dryRun && author.ID == 0:
		// Would be created; the row does not exist yet, so there is no id.
		ch.Outcome = authorPathLinkWouldCreate
		return ch
	case author.ID == 0:
		return fail(fmt.Errorf("author %q did not resolve", ch.DerivedName))
	case ch.AuthorID != 0 && author.ID != ch.AuthorID:
		// The name now resolves to a different row than at scan time.
		ch.Outcome = authorPathLinkChangedSinceScan
		return ch
	}
	ch.AuthorID, ch.AuthorName = author.ID, author.Name
	if dryRun {
		if minted {
			ch.Outcome = authorPathLinkWouldCreate
		} else if ch.Outcome == authorPathLinkWouldCreate {
			// The row exists after all (created between the snapshot and now).
			ch.Outcome = authorPathLinkWouldLink
		}
		return ch
	}
	if lost() {
		return fail(fmt.Errorf("scan stand-down lease lost; refusing to keep writing"))
	}

	// (1) the credit. A resumed half-write already has it; re-writing would be
	// a no-op anyway, and the callback refuses to touch a non-empty array.
	if _, err := store.ModifyBookAuthors(ch.BookID, func(cur []database.BookAuthor) ([]database.BookAuthor, error) {
		if len(cur) > 0 {
			return nil, database.ErrSkipBookAuthorsWrite
		}
		return []database.BookAuthor{{BookID: ch.BookID, AuthorID: author.ID, Role: "author", Position: 0}}, nil
	}); err != nil {
		return fail(err)
	}

	// (2) the scalar, under the book's write lock, and only while it is still
	// nil -- so a column another writer commits meanwhile is not reverted.
	wrote := false
	written, err := store.ModifyBook(ch.BookID, func(cur *database.Book) error {
		if cur.AuthorID != nil {
			return database.ErrSkipBookWrite
		}
		id := author.ID
		cur.AuthorID = &id
		a := author
		cur.Author = &a
		wrote = true
		return nil
	})
	switch {
	case err != nil:
		return fail(err)
	case written == nil:
		return fail(fmt.Errorf("book vanished before the link"))
	case !wrote:
		ch.Outcome = authorPathLinkChangedSinceScan
		return ch
	}
	ch.Applied = true
	if minted {
		ch.Outcome = authorPathLinkCreatedAndLinked
	} else {
		ch.Outcome = authorPathLinkLinked
	}

	// (3) the ledger, AFTER the write it describes -- never before it.
	//
	// 🔴 WHAT THIS ROW IS AND IS NOT. It is a RECORD, not a one-click undo.
	// "author_id" is not in undo/restorable.go's revertableBookFields, so the
	// undo engine counts the row safe and its revert writes nothing; and the
	// join row this op writes alongside the scalar is not recorded at all.
	// Adding author_id to that map would be wrong on its own -- a revert would
	// clear the scalar and leave the credit behind, which is the dangling shape
	// author-id-repair exists to clean up -- so the ledger row is left as the
	// audit trail it actually is, exactly as author-id-repair's is.
	//
	// Reversing a link by hand is therefore: clear the scalar and delete the
	// credit for the book ids this op lists, then (for a row this op minted,
	// which the result names in created_authors) purge-empty-authors removes
	// the row once nothing credits it. A real reversible undo needs a change
	// type that carries both keys; that is a follow-up, not a claim made here.
	if lErr := store.CreateOperationChange(&database.OperationChange{
		ID:          ulid.Make().String(),
		OperationID: opID,
		BookID:      ch.BookID,
		ChangeType:  "metadata_update",
		FieldName:   "author_id",
		OldValue:    "",
		NewValue:    strconv.Itoa(author.ID),
	}); lErr != nil {
		ch.Error = "undo-ledger write failed: " + lErr.Error()
	} else {
		mu.Lock()
		res.UndoLedgerRows++
		mu.Unlock()
	}
	return ch
}
