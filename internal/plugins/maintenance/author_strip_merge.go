// file: internal/plugins/maintenance/author_strip_merge.go
// version: 1.9.0
// guid: dbd16a1f-eada-4c33-b5c4-6a61ce342396
// last-edited: 2026-09-26

package maintenance

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/dedup"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
)

// --- author-strip-merge ---
//
// 🔴 WHY THIS EXISTS. Measured on the live library 2026-09-03: 2,793 of 19,972
// author rows (14.0%) begin with a digit. They are not people — they are the
// chapter file's own numbering, lifted out of a filename or an ID3 artist tag:
// "001_Celestia", "Track 01", "000m_00s__056m_16s_43h", "001 of 301".
//
// PR #3062 closed the iTunes creation path that mints them. This op repairs the
// rows that path already created; nothing re-normalizes an author name after
// creation, so the forward fix does not touch them.
//
// Three outcomes, and the split between them is the whole design:
//
//   - JUNK -> deleted. dedup.CleanAuthorNameForCreation rejects the name
//     outright. The books keep existing and simply lose a bogus credit; a
//     future scan can recreate the author correctly.
//   - STRIPPED and the residue names an EXISTING author -> merged into it.
//     This is the 603-row "001-147 Kevin J Anderson" cluster, where a real
//     person is wrapped in numbering.
//   - STRIPPED but no existing author has that name -> LEFT ALONE by default,
//     DELETED when delete_unmatched=true.
//
// That last case must never be "finished" by renaming the row. "001_Head of
// the Dragon" strips to "Head of the Dragon", which is a book title, not a
// person. Renaming would launder an obviously-corrupt row into a plausible one
// and take it out of reach of every future audit — the same reasoning
// author_conjunction_repair.go gives for leaving "and Thanks for All the Fish"
// alone. Deleting it is a different act: the row stays obviously wrong right up
// until it is gone, and the books it credited lose a title masquerading as a
// person. It is opt-in rather than the default because the residue CAN name a
// real person the library has no other row for ("kitchener_George Orwell"), so
// the operator reviews the dry run's list before turning it on. Measured on the
// live library 2026-09-04: 812 such rows, 187 distinct residues, every one a
// chapter or book title, a track name, or bitrate shrapnel.

// 🔴 PLACEHOLDERS ARE NOT JUNK (2026-09-25). "Unknown Author", "Unknown",
// "Various", "n/a", "None" and "Audiobook" are all rejected by
// CleanAuthorNameForCreation, because they are on personname's structural-word
// list, and all of them look positional to isPositionalScope for the same
// reason. Before this change the op therefore planned to DELETE them: the dry
// run of 2026-09-25 listed the canonical Unknown Author row (the one
// author-id-repair and the entity handlers fall back to) among its deletes and
// left 244 books authorless. A placeholder is not a corrupt credit; it is the
// library's own way of saying "author not known yet". So:
//
//   - the canonical row (database.UnknownAuthorName, resolved through the same
//     GetAuthorByName call author-id-repair uses) is NEVER deleted or merged;
//   - every OTHER placeholder row, including a duplicate "Unknown Author", is
//     MERGED INTO the canonical row, so its books keep a credit;
//   - with no canonical row, placeholders are left alone and counted. This op
//     does not create the row: creating author rows is another path's job.

// placeholderAuthorNames are the lower-cased, normalized names that mean "no
// known author". Kept here rather than in personname because this op decides
// what to do with them; the list is the subset of personname's structural
// words that stand in for a whole credit rather than for a part of a book.
var placeholderAuthorNames = map[string]bool{
	"unknown author": true, "unknown": true, "various": true,
	"various artists": true, "various authors": true, "va": true,
	"n/a": true, "none": true, "null": true, "audiobook": true,
}

// isPlaceholderAuthorName reports whether name is a "no known author" stand-in.
func isPlaceholderAuthorName(name string) bool {
	return placeholderAuthorNames[strings.ToLower(dedup.NormalizeAuthorName(name))]
}

// trackArtifactRe and embeddedTimecodeRe cover two numbering shapes that
// CleanAuthorNameForCreation ACCEPTS as ordinary names, so the op never saw
// them: a track label glued to its number ("Track01", "Track_01") and a
// chapter-splitter timecode embedded after a filename
// ("Lords of the Sith_418m_07s_"). Both are the same defect as the zero-padded
// shapes this op already handles (chapter-file numbering lifted into an
// artist tag), so they belong in its junk bucket. Bare "NN-NN" numbering is
// already in scope through IsPositionalArtifactName.
var (
	trackArtifactRe    = regexp.MustCompile(`(?i)^\s*track[\s_.-]*\d{1,4}\s*$`)
	embeddedTimecodeRe = regexp.MustCompile(`(?i)(?:^|_)\d{1,4}m_\d{1,2}s(?:_|$)`)
)

// isTrackOrTimecodeArtifact reports whether name carries one of the two
// numbering shapes above.
func isTrackOrTimecodeArtifact(name string) bool {
	return trackArtifactRe.MatchString(name) || embeddedTimecodeRe.MatchString(name)
}

// 🔴 TITLE-AS-AUTHOR (2026-09-25, TODO JUNK-TITLE-AUTHORS). A different
// defect than the numbering shrapnel above: the author ROW is well-formed —
// "Arcane Chef 2" looks like an ordinary name and CleanAuthorNameForCreation
// accepts it — but every book it credits is titled after it: "Arcane Chef 2:
// A LitRPG Adventure" carries author "Arcane Chef 2". The importer filed the
// title where the artist tag was empty or missing, the same class of bug
// PR #3550's parseFilenameForAuthor fix closed at the source (see the
// personname change in that commit) for the filename-parse path; this branch
// repairs the rows that path, or an importer before it, already created.
//
// The guard against false positives is the SECOND half of the rule: an
// author is only flagged when EVERY live book it credits matches the
// title-as-author pattern. A real person who happens to have titled one book
// after themselves keeps other, differently-titled books, and that
// difference is exactly what keeps them off this list — see
// classifyTitleAsAuthor.
var (
	titleAsAuthorPunctRe = regexp.MustCompile(`[^a-z0-9 ]+`)
	titleAsAuthorSpaceRe = regexp.MustCompile(`\s+`)
)

// normalizeForTitleAuthorCompare folds case, maps '_' to a space (a common
// filename-tag separator), drops every other punctuation rune, and collapses
// whitespace — a looser comparison than dedup.NormalizeAuthorName, which
// preserves punctuation and case because it is used to key a name INDEX.
// This one exists only to answer "do these two strings name the same thing",
// per the TODO's explicit "case, punctuation, `_`->space, whitespace" rule.
func normalizeForTitleAuthorCompare(s string) string {
	s = strings.ToLower(s)
	s = strings.ReplaceAll(s, "_", " ")
	s = titleAsAuthorPunctRe.ReplaceAllString(s, " ")
	s = titleAsAuthorSpaceRe.ReplaceAllString(s, " ")
	return strings.TrimSpace(s)
}

// seriesPositionLabel returns the book's series-sequence number as text,
// preferring the typed SeriesSequence over the raw string so "2" and "2.0"
// both read as "2" wherever the typed field is populated.
func seriesPositionLabel(b database.BookCore) string {
	if b.SeriesSequence != nil {
		return strconv.Itoa(*b.SeriesSequence)
	}
	if b.SeriesPositionRaw != nil {
		return strings.TrimSpace(*b.SeriesPositionRaw)
	}
	return ""
}

// titleAsAuthorCandidates lists the strings a book's title could plausibly
// have been copied from into the author field: the whole title, the title's
// leading segment before ":" or " - " (subtitle separators — "Arcane Chef 2:
// A LitRPG Adventure" -> "Arcane Chef 2"), and "<series name> <position>"
// when the book belongs to a series ("Arcane Chef" #2 -> "Arcane Chef 2").
func titleAsAuthorCandidates(b database.BookCore, seriesByID map[int]database.Series) []string {
	var cands []string
	if title := strings.TrimSpace(b.Title); title != "" {
		cands = append(cands, title)
		if i := strings.Index(title, ":"); i > 0 {
			cands = append(cands, title[:i])
		}
		if i := strings.Index(title, " - "); i > 0 {
			cands = append(cands, title[:i])
		}
	}
	if b.SeriesID != nil {
		if s, ok := seriesByID[*b.SeriesID]; ok && strings.TrimSpace(s.Name) != "" {
			if pos := seriesPositionLabel(b); pos != "" {
				cands = append(cands, s.Name+" "+pos)
			}
		}
	}
	return cands
}

// bookTitleNamesAuthor reports whether name (normalized) matches any of the
// book's title-as-author candidates (also normalized).
func bookTitleNamesAuthor(name string, b database.BookCore, seriesByID map[int]database.Series) bool {
	norm := normalizeForTitleAuthorCompare(name)
	if norm == "" {
		return false
	}
	for _, c := range titleAsAuthorCandidates(b, seriesByID) {
		if normalizeForTitleAuthorCompare(c) == norm {
			return true
		}
	}
	return false
}

// classifyTitleAsAuthor reports whether author a's name is its books' title
// masquerading as a credit. True only when a has at least one live (not
// soft-deleted) book AND every book it credits, trashed ones included,
// matches the title-as-author pattern — one differently-titled book is proof a
// is a real, distinct credit and takes a out of scope entirely (TODO
// JUNK-TITLE-AUTHORS: "only when the author has no other books whose titles
// differ"). Trashed books count as evidence against but not for: the delete
// (unlinkAndDeleteAuthor) unlinks a from trashed books too, so a trashed
// "The Stand" protects the row exactly like a live one would.
//
// books must be the SAME credit set the delete acts on
// (GetBooksByAuthorIDForRelinkCore: junction ∪ legacy AuthorID, trash
// included). Judging on primary-AuthorID books alone would let a real person
// who is primary only on a self-titled book, and co-author elsewhere, pass
// the gate and then be unlinked from every book (push security review,
// 2026-09-25).
func classifyTitleAsAuthor(a database.Author, books []database.BookCore, seriesByID map[int]database.Series) bool {
	live := 0
	for i := range books {
		if !bookTitleNamesAuthor(a.Name, books[i], seriesByID) {
			return false
		}
		if !books[i].IsSoftDeleted() {
			live++
		}
	}
	return live > 0
}

// authorStripMergeSampleLimit bounds how many per-row decisions are surfaced in
// the report, so a reviewer can eyeball the plan without the report becoming the
// size of the change.
const authorStripMergeSampleLimit = 60

type authorStripMergeParams struct {
	// Apply, if true, actually merges and deletes. Default false (report only).
	//
	// 🔴 THIS DELETES AUTHOR ROWS ON A PRODUCTION LIBRARY, so the default must
	// be the harmless one. Mirrors maintenance.purge-empty-authors.
	Apply bool `json:"apply"`

	// Limit caps how many rows are mutated in one run (0 = no cap), so a first
	// apply can be run small and inspected rather than all-or-nothing. The cap
	// applies to the ID-ordered plan across every enabled kind of change, so
	// turning on delete_unmatched changes which rows a limited run reaches.
	Limit int `json:"limit"`

	// DeleteJunk, when true (the DEFAULT), deletes rows the name predicate
	// rejects outright. Set false to perform ONLY the merges — useful for a
	// first apply, where consolidating the unambiguous cases is lower risk than
	// deleting.
	DeleteJunk *bool `json:"delete_junk,omitempty"`

	// DeleteUnmatched, when true (default FALSE), also deletes the rows that
	// carry numbering but whose residue names no existing author — the
	// "stripped, no target" bucket the report otherwise only counts. Off by
	// default because that residue is usually a chapter or book title but can
	// be a person; run apply=false with this set and read the list first.
	// Renaming those rows stays off the table (see the file comment).
	DeleteUnmatched bool `json:"delete_unmatched"`

	// DeleteTitleAsAuthor, when true (default FALSE), deletes the rows
	// classifyTitleAsAuthor flags. Off by default, separately from delete_junk:
	// this class judges a person's name by their book titles, which can be
	// wrong for a real author whose only book is self-titled, so an apply must
	// opt in after reading the report's list.
	DeleteTitleAsAuthor bool `json:"delete_title_as_author"`

	// RelinkTitleAsAuthor, when true (default FALSE) with apply=true,
	// replaces the credit of every book credited to a title-as-author row (or
	// to its numbered twin) with the book's real author, when the library's
	// own evidence names exactly one (see author_strip_merge_relink.go).
	// Preview first: apply=false with this set lists every relink (book,
	// junk author, chosen author, sources) and writes nothing, with the
	// same counts the apply reports. With this AND
	// delete_title_as_author set, a numbered twin all of whose books were
	// relinked is deleted too. Limit does not cap the relinks.
	RelinkTitleAsAuthor bool `json:"relink_title_as_author"`
}

// deleteJunk resolves the tri-state pointer to its default of TRUE.
func (p authorStripMergeParams) deleteJunk() bool {
	return p.DeleteJunk == nil || *p.DeleteJunk
}

type authorStripMergeReport struct {
	TotalAuthors int
	Junk         int
	// TitleAsAuthor counts rows whose name is one of their OWN books' title
	// (or the title's leading segment, or "<series> <position>") and where
	// every live book that row credits matches — see classifyTitleAsAuthor.
	// Deleted only with delete_title_as_author=true; the books keep existing
	// and lose a bogus credit, same as any other junk author (see the file
	// comment on that bucket).
	TitleAsAuthor int
	// TitleAsAuthorUnverified are rows that matched on their primary-author
	// books but whose full credit set could not be read; never deleted.
	TitleAsAuthorUnverified int
	Mergeable               int
	// Ambiguous are rows whose stripped name matches MORE THAN ONE existing
	// author. Reported rather than merged: a name index resolves to one row and
	// silently hides the duplicates, so picking one here would be a guess.
	Ambiguous int
	// StrippedNoTarget are rows that carry numbering but whose residue names no
	// existing author. Left alone on purpose (see the file comment).
	StrippedNoTarget int
	// TargetIsJunk are rows whose stripped name matches an existing author that
	// is ITSELF junk. Merging those would consolidate junk into junk and report
	// it as a success. The source row is left alone. A target is junk when
	// either:
	//   - the name predicate rejects it (dedup.CleanAuthorNameForCreation).
	//     Measured 0 on the live library, and expected to stay so: a row
	//     whose residue is junk is already rejected by the SOURCE check, so
	//     "00 Prologue" is deleted as junk and never reaches a merge; or
	//   - it is title-as-author (classifyTitleAsAuthor on its full credit
	//     set, the same gate the delete path uses), WHATEVER
	//     delete_title_as_author says. "01 Arcane Chef 2" strips to "Arcane
	//     Chef 2", a row whose only book is "Arcane Chef 2: A LitRPG
	//     Adventure"; with the flag off that twin was merged into it, which
	//     consolidated junk into junk (TODO
	//     STRIP-MERGE-TITLE-TARGET-FLAG-OFF, owner decision 2026-09-26).
	TargetIsJunk int
	// TargetUnverified are merges skipped because the target's full credit
	// set could not be read, so whether it is title-as-author is unknown.
	// Fail closed: the source row is left alone.
	TargetUnverified int
	// MergeTargetRemoved are merges dropped because their target is itself
	// deleted or merged away by this same run (dropMergesIntoRemovedRows).
	// Not counted in Mergeable. The source row is left alone.
	MergeTargetRemoved int

	// OutOfScope are rows rejected by the name predicate for a reason that is
	// NOT numbering — publisher and copyright shrapnel. Counted, never touched.
	OutOfScope int
	// Placeholders counts non-canonical placeholder rows ("Unknown",
	// "Various", a duplicate "Unknown Author") planned for a merge into the
	// canonical Unknown Author row. PlaceholdersNoCanonical counts the ones
	// left alone because no canonical row exists. CanonicalUnknownID is the
	// row this run protects (0 when none exists).
	Placeholders            int
	PlaceholdersNoCanonical int
	CanonicalUnknownID      int
	Merged                  int
	Deleted                 int
	BooksTouched            int
	// BooksLeftAuthorless counts books whose EVERY credit is a row this run
	// deletes. Computed in the dry run too, through the same code the apply
	// uses, so the report says what the apply will do to books and not just
	// to author rows. Counted as distinct books, judged against the whole
	// run's delete set: a book credited by two doomed rows is one authorless
	// book, and the dry run must say so even though it never sees the first
	// deletion land.
	BooksLeftAuthorless int
	Failed              int
	Sample              []string

	// Title-as-author relink (author_strip_merge_relink.go). RelinkPlanned
	// books have exactly one real author; RelinkNoCandidate and
	// RelinkConflict books are left alone; the Skipped buckets are the
	// iTunes tree and owner-manual books; RelinkFailed are read or write
	// failures. Relinked counts books written (apply). RelinkNewAuthors is
	// how many distinct author rows the relink created (or would create).
	// TwinDeletes are numbered twins deleted after all their books relinked.
	RelinkPlanned            int
	RelinkNoCandidate        int
	RelinkConflict           int
	RelinkSkippedITunes      int
	RelinkSkippedOwnerManual int
	RelinkFailed             int
	Relinked                 int
	RelinkNewAuthors         int
	TwinDeletes              int
}

func (r authorStripMergeReport) summary() string {
	return fmt.Sprintf(
		"authors=%d junk=%d title-as-author=%d title-as-author-unverified=%d mergeable=%d ambiguous=%d target-is-junk=%d target-unverified=%d merge-target-removed=%d stripped-no-target=%d out-of-scope=%d placeholders=%d placeholders-no-canonical=%d canonical-unknown-id=%d merged=%d deleted=%d books-touched=%d books-left-authorless=%d failed=%d relink-planned=%d relink-no-candidate=%d relink-conflict=%d relink-skipped-itunes=%d relink-skipped-owner-manual=%d relink-failed=%d relinked=%d relink-new-authors=%d twin-deletes=%d",
		r.TotalAuthors, r.Junk, r.TitleAsAuthor, r.TitleAsAuthorUnverified, r.Mergeable, r.Ambiguous, r.TargetIsJunk, r.TargetUnverified, r.MergeTargetRemoved,
		r.StrippedNoTarget, r.OutOfScope, r.Placeholders, r.PlaceholdersNoCanonical,
		r.CanonicalUnknownID, r.Merged, r.Deleted, r.BooksTouched,
		r.BooksLeftAuthorless, r.Failed,
		r.RelinkPlanned, r.RelinkNoCandidate, r.RelinkConflict, r.RelinkSkippedITunes, r.RelinkSkippedOwnerManual, r.RelinkFailed, r.Relinked, r.RelinkNewAuthors, r.TwinDeletes)
}

func (p *Plugin) authorStripMergeDef() sdk.OperationDef {
	return sdk.OperationDef{
		ID:          "maintenance.author-strip-merge",
		Liveness:    sdk.LivenessManual,
		Plugin:      "maintenance",
		DisplayName: "Strip author numbering and merge",
		Description: "Repairs author rows built out of chapter-file numbering ('001_Celestia', " +
			"'Track 01', '000m_00s__056m_16s_43h'). Strips the numbering; when the residue names " +
			"an existing author the row is MERGED into it ('001-147 Kevin J Anderson'), and rows " +
			"that carry no usable name are DELETED. Rows whose residue matches nothing are left " +
			"alone rather than renamed. Also deletes TITLE-AS-AUTHOR rows: a name matching every " +
			"live book it credits ('Arcane Chef 2' crediting 'Arcane Chef 2: A LitRPG Adventure'), " +
			"never a row that also credits a differently-titled book. Placeholder rows ('Unknown', 'Various', 'n/a', a duplicate " +
			"'Unknown Author') are MERGED INTO the canonical Unknown Author row, which is never " +
			"deleted. Pass delete_unmatched=true to delete those too (review " +
			"the dry run first: 812 on this library, chapter and book titles). Measured 2,793 " +
			"of 19,972 authors on this library. " +
			"REPORT-ONLY BY DEFAULT: pass apply=true to write. Idempotent.",
		// ResumeDrop, not Requeue: this deletes rows, and a half-finished run
		// that silently resumes after a restart is harder to reason about than
		// one that stops and is re-triggered. Re-running is cheap and idempotent.
		ResumePolicy:    sdk.ResumeDrop,
		DefaultPriority: sdk.PriorityLow,
		ConcurrencyKey:  "maintenance.author-strip-merge",
		Cancellable:     true,
		Isolate:         false,
		Timeout:         60 * time.Minute,
		Capabilities:    []sdk.Capability{sdk.CapLibraryRead, sdk.CapLibraryWrite},
		Run:             p.runAuthorStripMerge,
	}
}

// isPositionalScope reports whether a rejected name was rejected because of
// chapter/track numbering, as opposed to the publisher and copyright shrapnel
// that IsDirtyAuthorName also covers. Only the former is this op's business.
func isPositionalScope(name string) bool {
	n := dedup.NormalizeAuthorName(name)
	return dedup.IsPositionalArtifactName(n) || dedup.StripPositionalPrefix(n) != n ||
		isTrackOrTimecodeArtifact(n)
}

// authorStripPlan is one row's decision, computed before anything is written so
// that apply=false and apply=true evaluate exactly the same set.
type authorStripPlan struct {
	from   database.Author
	into   *database.Author // nil => delete
	reason string
}

func (p *Plugin) runAuthorStripMerge(ctx context.Context, rawParams json.RawMessage, reporter sdk.Reporter) error {
	store := p.deps.OpsStore()
	if store == nil {
		return fmt.Errorf("database not initialized")
	}

	var params authorStripMergeParams
	if len(rawParams) > 0 {
		if err := json.Unmarshal(rawParams, &params); err != nil {
			return fmt.Errorf("parse params: %w", err)
		}
	}
	log := reporter.Logger()
	log.Info("author-strip-merge start",
		"apply", params.Apply, "delete_junk", params.deleteJunk(),
		"delete_unmatched", params.DeleteUnmatched, "delete_title_as_author", params.DeleteTitleAsAuthor,
		"relink_title_as_author", params.RelinkTitleAsAuthor, "limit", params.Limit)

	_ = reporter.UpdateProgress(0, 2, "Listing authors…")
	authors, err := store.GetAllAuthors()
	if err != nil {
		return fmt.Errorf("list authors: %w", err)
	}

	// Resolve merge targets from an in-memory index rather than one
	// GetAuthorByName per candidate: 2,793 round trips to answer a question the
	// author list already contains.
	//
	// The index maps to a SLICE, not a single row. A name -> id index silently
	// resolves duplicates to one row, and this library has them (two "Unknown
	// Author" rows, ids 54845 and 54846). A merge target chosen that way is a
	// guess; ambiguous names are reported instead.
	byName := make(map[string][]database.Author, len(authors))
	for _, a := range authors {
		key := dedup.NormalizeAuthorName(a.Name)
		byName[key] = append(byName[key], a)
	}

	// booksByAuthorID and seriesByID back the title-as-author check below.
	// One full-library read each, same pattern maintenance.repair-junk-titles
	// uses for its own book-title sweep, rather than a per-author query: with
	// ~20k author rows a per-author fetch is the "2,793 round trips" this op's
	// byName index above already exists to avoid, just on the books side.
	allBooksCore, err := store.GetAllBooksCore(0, 0)
	if err != nil {
		return fmt.Errorf("list books: %w", err)
	}
	booksByAuthorID := make(map[int][]database.BookCore, len(authors))
	for i := range allBooksCore {
		if allBooksCore[i].AuthorID == nil {
			continue
		}
		id := *allBooksCore[i].AuthorID
		booksByAuthorID[id] = append(booksByAuthorID[id], allBooksCore[i])
	}
	allSeries, err := store.GetAllSeries()
	if err != nil {
		return fmt.Errorf("list series: %w", err)
	}
	seriesByID := make(map[int]database.Series, len(allSeries))
	for _, s := range allSeries {
		seriesByID[s.ID] = s
	}

	// isTitleAsAuthor is THE title-as-author gate, used both to classify a
	// row for deletion and to refuse it as a merge target, so the two can
	// never judge one row differently. A cheap prefilter on the
	// primary-AuthorID index, then confirmation on the full credit set the
	// delete would act on (see classifyTitleAsAuthor). A credit-read error is
	// returned: the caller must not act on the row. Memoized by author ID,
	// because a row can be judged once as a source and again as the target
	// of every numbered twin that strips to its name.
	type titleVerdict struct {
		yes bool
		err error
	}
	titleVerdicts := map[int]titleVerdict{}
	isTitleAsAuthor := func(a database.Author) (bool, error) {
		if v, ok := titleVerdicts[a.ID]; ok {
			return v.yes, v.err
		}
		var v titleVerdict
		if classifyTitleAsAuthor(a, booksByAuthorID[a.ID], seriesByID) {
			credits, cErr := store.GetBooksByAuthorIDForRelinkCore(a.ID)
			if cErr != nil {
				v.err = cErr
			} else {
				v.yes = classifyTitleAsAuthor(a, credits, seriesByID)
			}
		}
		titleVerdicts[a.ID] = v
		return v.yes, v.err
	}

	report := authorStripMergeReport{TotalAuthors: len(authors)}
	var plans []authorStripPlan
	// titleRows are the rows classified title-as-author; twins are numbered
	// rows refused a merge because their target is one. Both are relink
	// sources below.
	var titleRows, twins []database.Author
	tombstoneWarned := false

	// The canonical placeholder, resolved the same way author-id-repair
	// resolves its fallback so both ops agree on which row it is. A lookup
	// error stops the run: without knowing which row to protect, the
	// classification below cannot be trusted not to delete it.
	var canonicalUnknown *database.Author
	if u, uErr := store.GetAuthorByName(database.UnknownAuthorName); uErr != nil {
		return fmt.Errorf("resolve %q author: %w", database.UnknownAuthorName, uErr)
	} else if u != nil && u.ID > 0 {
		canonicalUnknown = u
		report.CanonicalUnknownID = u.ID
	}

	_ = reporter.UpdateProgress(1, 2, "Classifying author names…")
	for _, a := range authors {
		if canonicalUnknown != nil && a.ID == canonicalUnknown.ID {
			continue // the canonical placeholder is never touched
		}
		if isPlaceholderAuthorName(a.Name) {
			if canonicalUnknown == nil {
				report.PlaceholdersNoCanonical++
				continue
			}
			report.Placeholders++
			into := *canonicalUnknown
			plans = append(plans, authorStripPlan{from: a, into: &into, reason: "placeholder"})
			continue
		}
		if isTitle, cErr := isTitleAsAuthor(a); cErr != nil {
			report.TitleAsAuthorUnverified++
			log.Warn("author-strip-merge: cannot read credits for title-as-author candidate; leaving it",
				"author_id", a.ID, "err", cErr)
			continue
		} else if isTitle {
			report.TitleAsAuthor++
			titleRows = append(titleRows, a)
			if params.DeleteTitleAsAuthor {
				plans = append(plans, authorStripPlan{from: a, reason: "title-as-author"})
			}
			continue
		}
		cleaned, ok := dedup.CleanAuthorNameForCreation(a.Name)
		if ok && isTrackOrTimecodeArtifact(a.Name) {
			// Accepted by the creation gate, but carries track or timecode
			// numbering this op owns; judge it as junk below.
			ok = false
		}
		if !ok {
			// SCOPE GUARD. CleanAuthorNameForCreation also rejects the
			// publisher and copyright shrapnel IsDirtyAuthorName was built for
			// — 1,073 rows on this library, including "Penguin Books" and
			// "Alex A. Ryans - translator". Those are a different defect and
			// some of them name real people; deleting them here would be a
			// silent scope expansion far past the numbering this op exists for.
			if !isPositionalScope(a.Name) {
				report.OutOfScope++
				continue
			}
			report.Junk++
			if params.deleteJunk() {
				plans = append(plans, authorStripPlan{from: a, reason: "junk"})
			}
			continue
		}
		if cleaned == dedup.NormalizeAuthorName(a.Name) {
			continue // ordinary name, nothing to do
		}

		candidates := byName[dedup.NormalizeAuthorName(cleaned)]
		// Never treat the row itself as its own merge target.
		var targets []database.Author
		for _, c := range candidates {
			if c.ID != a.ID {
				targets = append(targets, c)
			}
		}
		switch {
		case len(targets) == 0:
			report.StrippedNoTarget++
			if params.DeleteUnmatched {
				plans = append(plans, authorStripPlan{from: a, reason: "unmatched"})
			}
		case len(targets) > 1:
			report.Ambiguous++
		default:
			// The target must pass the same judgement as the source. Otherwise
			// "00 Prologue" merges into the existing junk row "Prologue" and
			// the op reports a successful merge for work that consolidated one
			// junk row into another.
			if _, targetOK := dedup.CleanAuthorNameForCreation(targets[0].Name); !targetOK {
				report.TargetIsJunk++
				continue
			}
			// A title-as-author target is junk too, whatever
			// delete_title_as_author says: with the flag off the target
			// survives this run, and merging the numbered twin into it
			// would consolidate junk into junk (see TargetIsJunk).
			if isTitle, cErr := isTitleAsAuthor(targets[0]); cErr != nil {
				report.TargetUnverified++
				log.Warn("author-strip-merge: cannot read merge target's credits; not merging",
					"from_id", a.ID, "from", a.Name, "into_id", targets[0].ID, "err", cErr)
				continue
			} else if isTitle {
				report.TargetIsJunk++
				twins = append(twins, a)
				log.Info("author-strip-merge: not merging into a title-as-author row",
					"from_id", a.ID, "from", a.Name, "into_id", targets[0].ID, "into", targets[0].Name)
				continue
			}
			report.Mergeable++
			t := targets[0]
			plans = append(plans, authorStripPlan{from: a, into: &t, reason: "merge"})
		}
	}

	// Title-as-author relink. Runs before any delete so a relinked book
	// carries its real author when the junk row's delete re-reads its
	// credits; a report-only run with the flag set computes the same
	// decisions without writing, so its plan and counts match the apply's.
	relinkWrite := params.Apply && params.RelinkTitleAsAuthor
	var relinkedBooks map[string]bool
	if params.RelinkTitleAsAuthor && len(titleRows)+len(twins) > 0 {
		_ = reporter.UpdateProgress(1, 2, "Relinking title-as-author books…")
		junkIDs := make(map[int]bool, len(titleRows)+len(twins))
		junkRows := append(append([]database.Author{}, titleRows...), twins...)
		for _, a := range junkRows {
			junkIDs[a.ID] = true
		}
		idx := newTitleRelinkIndex(allBooksCore, authors, seriesByID, junkIDs)
		creator := newAuthorPathLinkCreator(store, !relinkWrite)
		rel, rErr := relinkTitleAsAuthorBooks(ctx, store, creator, junkRows, &idx, relinkWrite, log)
		if rErr != nil {
			return rErr
		}
		for _, r := range rel.Relinks {
			switch r.Outcome {
			case titleRelinkOutcomeRelink:
				report.RelinkPlanned++
				if relinkWrite {
					report.Relinked++
				}
			case titleRelinkOutcomeNoCandidate:
				report.RelinkNoCandidate++
			case titleRelinkOutcomeConflict:
				report.RelinkConflict++
			case titleRelinkOutcomeITunes:
				report.RelinkSkippedITunes++
			case titleRelinkOutcomeOwnerManual:
				report.RelinkSkippedOwnerManual++
			case titleRelinkOutcomeFailed:
				report.RelinkFailed++
			}
		}
		report.RelinkNewAuthors = len(rel.NewAuthors)
		for _, na := range rel.NewAuthors {
			log.Info("author-strip-merge relink author row", "author_id", na.AuthorID, "name", na.Name, "books", na.Books, "created", relinkWrite)
		}
		if params.RelinkTitleAsAuthor {
			relinkedBooks = rel.RelinkedBooks
			// A numbered twin whose every book now carries its real author
			// holds no credit worth keeping: delete it with the title rows.
			// Only then: its books' titles were never judged, so deleting it
			// with any book still credited would strip a credit no gate
			// checked.
			if params.DeleteTitleAsAuthor {
				for _, tw := range twins {
					if rel.AllRelinked[tw.ID] {
						report.TwinDeletes++
						plans = append(plans, authorStripPlan{from: tw, reason: "title-as-author-twin"})
					}
				}
			}
		}
	}

	plans = dropMergesIntoRemovedRows(plans, &report, log)

	// Deterministic order so a limited run is reproducible and a dry run
	// describes the same prefix the apply will take.
	sort.Slice(plans, func(i, j int) bool { return plans[i].from.ID < plans[j].from.ID })
	if params.Limit > 0 && len(plans) > params.Limit {
		plans = plans[:params.Limit]
	}

	// Every row this run will delete, so that "is this book left authorless"
	// is judged the same way whether or not the earlier deletes have landed
	// yet. Without this the apply sees row A's removal before it evaluates row
	// B and the dry run does not — and the dry run is the number the operator
	// reads before enabling the flag.
	doomed := make(map[int]bool, len(plans))
	for _, pl := range plans {
		if pl.into == nil {
			doomed[pl.from.ID] = true
		}
	}
	authorlessBooks := map[string]struct{}{}

	for i, pl := range plans {
		if ctx.Err() != nil {
			// A destructive op that stops at 700/812 must say where it
			// stopped; the summary is the only record of what landed.
			log.Info("author-strip-merge cancelled", "at", i, "of", len(plans), "summary", report.summary())
			return ctx.Err()
		}
		if i%25 == 0 {
			_ = reporter.UpdateProgress(i, len(plans), "Applying author repairs…")
		}
		if canonicalUnknown != nil && pl.from.ID == canonicalUnknown.ID {
			// Unreachable through the classifier above; kept so that a future
			// planning branch cannot delete or merge away the fallback row.
			continue
		}
		if !params.Apply && pl.reason == "unmatched" {
			// The control on delete_unmatched is "read the list first", and a
			// 60-line sample is not the list. Report-only runs log every row
			// the flag would delete; the apply keeps the sample so its log
			// stays the size of a summary, not of the change.
			log.Info("author-strip-merge would delete unmatched row",
				"author_id", pl.from.ID, "name", pl.from.Name)
		}
		if len(report.Sample) < authorStripMergeSampleLimit {
			if pl.into != nil {
				report.Sample = append(report.Sample,
					fmt.Sprintf("merge %q -> %q", pl.from.Name, pl.into.Name))
			} else {
				report.Sample = append(report.Sample,
					fmt.Sprintf("delete %q (%s)", pl.from.Name, pl.reason))
			}
		}

		if pl.into != nil {
			if !params.Apply {
				continue
			}
			// mergeAuthorInto rewrites the book_authors junction, moves the
			// denormalized book.AuthorID when it named the row being removed,
			// and only then deletes. Reused rather than reimplemented: the
			// AuthorID step is exactly the one whose absence stranded ~212
			// authors' books in the 2026-08-24 dangling-AuthorID incident.
			n, err := p.mergeAuthorInto(ctx, pl.from, *pl.into, false, log)
			report.BooksTouched += n
			if err != nil {
				report.Failed++
				log.Warn("author-strip-merge: merge failed",
					"from_id", pl.from.ID, "from", pl.from.Name, "into", pl.into.Name, "err", err)
				continue
			}
			// Redirect any reference that still names the old id. Without this
			// a stale AuthorID resolves to nothing; with it, reads self-heal.
			// Resolved through a capability assertion rather than widened onto
			// OpsStore: the tombstone is a nice-to-have on top of a merge that
			// has already succeeded, and one extra method on a store interface
			// is a cost the whole codebase pays.
			if ts, tsOK := any(store).(interface {
				CreateAuthorTombstone(oldID, canonicalID int) error
			}); tsOK {
				if err := ts.CreateAuthorTombstone(pl.from.ID, pl.into.ID); err != nil {
					log.Warn("author-strip-merge: tombstone write failed; merge stands but stale refs will not self-heal",
						"from_id", pl.from.ID, "into_id", pl.into.ID, "err", err)
				}
			} else if !tombstoneWarned {
				// Say so once. A type assertion that never matches because a
				// decorator in the chain does not forward the method looks
				// exactly like one that had nothing to do, and the merges would
				// still be reported as complete.
				tombstoneWarned = true
				log.Warn("author-strip-merge: store does not expose CreateAuthorTombstone; merges will not leave a redirect for stale author ids")
			}
			report.Merged++
			continue
		}

		// Deletes go through the same function in both modes: with dryRun
		// set it reads every credit and reports what it WOULD do, so the
		// authorless count in a report-only run is the apply's own number
		// rather than a second estimate that could drift from it.
		n, authorless, err := p.unlinkAndDeleteAuthor(ctx, pl.from, doomed, !params.Apply, log)
		// Partial counts are still real work (or real prediction): a row that
		// failed on its third book did rewrite two, and the report must not
		// shrink because of it.
		for _, id := range authorless {
			if relinkedBooks[id] {
				// Relinked to its real author (in a report-only run, will
				// be), so the delete does not leave it authorless.
				continue
			}
			authorlessBooks[id] = struct{}{}
		}
		if params.Apply {
			report.BooksTouched += n
		}
		if err != nil {
			report.Failed++
			log.Warn("author-strip-merge: delete failed",
				"author_id", pl.from.ID, "name", pl.from.Name, "reason", pl.reason, "err", err)
			continue
		}
		if params.Apply {
			report.Deleted++
		}
	}
	report.BooksLeftAuthorless = len(authorlessBooks)

	_ = reporter.UpdateProgress(len(plans), len(plans), report.summary())
	log.Info("author-strip-merge done", "summary", report.summary())
	for _, s := range report.Sample {
		log.Info("author-strip-merge plan", "change", s)
	}
	if !params.Apply {
		log.Info("author-strip-merge: REPORT ONLY — pass apply=true to write these changes")
	}
	return nil
}

// dropMergesIntoRemovedRows removes every merge whose target is itself the
// source of another plan in this run, and counts it in
// report.MergeTargetRemoved (and out of report.Mergeable).
//
// 🔴 WHY. Every plan removes its `from` row: a delete drops it, a merge folds
// it into its target and then drops it. A merge into such a row cannot be
// made safe by ordering. With the target deleted first, mergeAuthorInto
// (which never checks that `into` still exists) writes the deleted row's ID
// into book.AuthorID and the junction: the 2026-08-24 dangling-AuthorID
// incident. With the merge first, the target's delete re-reads its credits
// and unlinks the merged-in books too, which the title-as-author gate never
// judged (it judged the target's credits before the merge). So the delete
// set and the merge-target set are made disjoint here, at planning time and
// before Limit, which keeps the dry run's plan and counts identical to the
// apply's.
//
// The shape this was written for, "01 Arcane Chef 2" stripping to "Arcane
// Chef 2" (a row the same run deletes as title-as-author), no longer reaches
// here: the planner refuses a title-as-author target as target-is-junk
// before a merge is planned, flag on or off. This remains the backstop for
// any other plan that removes a merge's target (a junk or unmatched delete,
// a placeholder merge).
//
// One pass on purpose: a merge dropped here leaves its source alive, which
// could in principle re-admit a merge into that source, but that source still
// carries the numbering this op exists to remove, so merging into it would be
// junk into junk. The dropped rows are reported and left for a later run.
func dropMergesIntoRemovedRows(plans []authorStripPlan, report *authorStripMergeReport, log *slog.Logger) []authorStripPlan {
	removed := make(map[int]string, len(plans))
	for _, pl := range plans {
		removed[pl.from.ID] = pl.reason
	}
	kept := plans[:0]
	for _, pl := range plans {
		if pl.into != nil {
			if reason, gone := removed[pl.into.ID]; gone {
				report.MergeTargetRemoved++
				if pl.reason == "merge" {
					report.Mergeable--
				}
				log.Info("author-strip-merge: not merging into a row this run removes",
					"from_id", pl.from.ID, "from", pl.from.Name,
					"into_id", pl.into.ID, "into", pl.into.Name, "into_reason", reason)
				continue
			}
		}
		kept = append(kept, pl)
	}
	return kept
}

// unlinkAndDeleteAuthor removes a junk author from every book that credits it
// and then deletes the row, returning the number of books rewritten and the
// IDs of those books left with no credit once every row in doomed (the run's
// whole delete set) is gone. With dryRun set it walks the same credits and
// returns the same answer without writing anything. The write itself removes
// only from's credit: rows still doomed but not yet processed keep theirs
// until their own turn, so their primary-author rewrite still finds the book.
//
// This is mergeAuthorInto's shape without a destination, and it exists because
// store.DeleteAuthor alone is NOT safe for an author that has books: it sweeps
// the book_authors junction but leaves the denormalized book.AuthorID pointing
// at the row it just deleted. That single missing step is the whole mechanism
// of the 2026-08-24 incident, where two entity handlers stranded ~212 authors'
// books behind ids that no longer resolve.
//
// A book losing its only author is the intended outcome, not a failure: the
// credit being removed was never a person, and a book with no author is honest
// where one named "Track 01" is a repair job. A future scan recreates it
// correctly, now that the creation path is gated.
func (p *Plugin) unlinkAndDeleteAuthor(ctx context.Context, from database.Author, doomed map[int]bool, dryRun bool, log *slog.Logger) (unlinked int, authorless []string, err error) {
	store := p.deps.OpsStore()

	// ForRelink, not WithRole: the trash is included, because DeleteAuthor
	// below sweeps the author out of trashed books' junction rows too.
	books, err := store.GetBooksByAuthorIDForRelinkCore(from.ID)
	if err != nil {
		return 0, nil, fmt.Errorf("get books for author %d: %w", from.ID, err)
	}

	for _, book := range books {
		if ctx.Err() != nil {
			return unlinked, authorless, ctx.Err()
		}
		bookAuthors, err := store.GetBookAuthors(book.ID)
		if err != nil {
			// Do NOT fall through to DeleteAuthor after this: dropping the row
			// while a book still credits it is precisely the orphaning this
			// function exists to avoid.
			return unlinked, authorless, fmt.Errorf("get book authors for %s: %w", book.ID, err)
		}

		var remaining []database.BookAuthor
		survivors := 0
		for _, ba := range bookAuthors {
			if ba.AuthorID == from.ID {
				continue
			}
			if !doomed[ba.AuthorID] {
				survivors++
			}
			ba.Position = len(remaining)
			remaining = append(remaining, ba)
		}
		// A book is authorless only if nothing survives in the junction AND
		// its denormalized primary does not name some third, living author
		// (junction and AuthorID are known to diverge on this library; a book
		// whose primary is not being touched keeps that credit).
		keepsPrimary := book.AuthorID != nil && *book.AuthorID != from.ID && !doomed[*book.AuthorID]
		if survivors == 0 && !keepsPrimary {
			authorless = append(authorless, book.ID)
		}
		if dryRun {
			unlinked++
			continue
		}
		if err := store.SetBookAuthors(book.ID, remaining); err != nil {
			return unlinked, authorless, fmt.Errorf("set book authors for %s: %w", book.ID, err)
		}

		// Move the denormalized primary off the row being deleted: promote the
		// first surviving credit, or clear it when nothing is left. Hydrate the
		// full row rather than writing the BookCore projection, whose heavy
		// fields are nil and whose guard-preserved Author would still name the
		// deleted row (STOREFID W5d-1).
		if book.AuthorID != nil && *book.AuthorID == from.ID {
			// Every failure below returns BEFORE DeleteAuthor. The junction
			// row is already gone, but the author row still exists, and
			// GetBooksByAuthorIDForRelinkCore unions the junction with the
			// legacy AuthorID, so a re-run finds this book again and retries
			// the rewrite. Logging and deleting anyway would leave AuthorID
			// pointing at a row that no longer exists — the exact 2026-08-24
			// mechanism — while the summary reported failed=0.
			var promoted *database.Author
			if len(remaining) > 0 {
				survivor, err := store.GetAuthorByID(remaining[0].AuthorID)
				if err != nil {
					return unlinked, authorless, fmt.Errorf("load surviving author %d for %s: %w", remaining[0].AuthorID, book.ID, err)
				}
				promoted = survivor
			}
			// Rewrite only AuthorID/Author, under the book's write lock
			// (ModifyBook), so a column another writer commits meanwhile is
			// not reverted (audit A1#15). The fresh row decides: a book whose
			// primary already moved off the deleted author is left alone.
			written, err := store.ModifyBook(book.ID, func(full *database.Book) error {
				if full.AuthorID == nil || *full.AuthorID != from.ID {
					return database.ErrSkipBookWrite
				}
				full.AuthorID = nil
				full.Author = nil
				if promoted != nil {
					id := promoted.ID
					full.AuthorID = &id
					full.Author = promoted
				}
				return nil
			})
			if err != nil {
				return unlinked, authorless, fmt.Errorf("rewrite primary author of %s: %w", book.ID, err)
			}
			if written == nil {
				return unlinked, authorless, fmt.Errorf("hydrate book %s for primary rewrite: not found", book.ID)
			}
		}
		unlinked++
	}

	if dryRun {
		return unlinked, authorless, nil
	}
	if err := database.VerifyAuthorUnlinked(store, from.ID); err != nil {
		return unlinked, authorless, err
	}
	if err := store.DeleteAuthor(from.ID); err != nil {
		return unlinked, authorless, fmt.Errorf("delete author %d: %w", from.ID, err)
	}
	return unlinked, authorless, nil
}
