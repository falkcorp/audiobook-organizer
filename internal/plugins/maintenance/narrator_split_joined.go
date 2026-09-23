// file: internal/plugins/maintenance/narrator_split_joined.go
// version: 1.1.1
// guid: b580d009-3cf0-45d9-bf1b-18f6e1f6c33d
// last-edited: 2026-09-23

package maintenance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/oklog/ulid/v2"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/internal/util"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
)

// --- split-joined-narrators ---
//
// Until 2026-09-23 most write paths stored a provider's or tag's whole cast as
// ONE narrator entity ("Dorrie Sacks, Jeff Hays, ..."), so ABS showed one chip
// for eight people. The store now keeps the junction per person on every
// write that changes a book's narrator text, but books already linked to a
// joined entity keep that link until something rewrites their narrator. This
// op repairs them: every narrator whose name util.SplitCreditNames breaks into
// two or more people is "joined"; each book crediting one gets that credit
// replaced, in place, by the people it names; and the joined entity, once no
// book links it, is deleted.
//
// The splitter keeps "Surname, Given" whole, so "Le Guin, Ursula" is not a
// joined name. Each credit then goes through util.CleanNarratorCredit with the
// BOOK's authors (owner rules 2026-09-23): translators, editors and
// introducers are dropped, a leading "By:" is stripped, and the book's own
// authors are dropped when a real narrator remains. A credit that is a URL, or
// that names only the book's authors, is NOT split: that book keeps its joined
// credit and is listed under held_for_review, since the text cannot tell a
// self-read from a mis-tagged author.
//
// Guards, as in purge-empty-narrators:
//   - DRY RUN by default. The report lists the joined names, what each splits
//     into, and how many books credit it.
//   - Scan stand-down on apply, with the per-item lease check.
//   - Each book's credits are RE-READ right before its rewrite, so a credit a
//     concurrent writer changed since the bulk read is rewritten from its
//     current value, not the stale one.
//   - A joined entity is deleted only after a live link re-check says zero.
//   - Undo-ledger row before every write, fail closed: "narrator_split" holds
//     the book's credit list before and after, as JSON; "narrator_delete"
//     holds "<id>:<name>" exactly as purge-empty-narrators writes it.
//
// SEQUENTIAL on purpose: each rewrite and each delete is a pebble.Sync commit,
// and DeleteNarrator sweeps the whole junction. The population is small (157
// joined names of 961 in production on 2026-09-22), so a pool would only add
// interleaving with no speed to win.

const splitJoinedSampleLimit = 100

var errSplitJoinedStandDownLost = errors.New("split-joined-narrators: scan stand-down lease lost; remaining work abandoned")

type splitJoinedNarratorsParams struct {
	// Apply, if true, rewrites credits and deletes emptied joined entities.
	// Default false: report only.
	Apply bool `json:"apply"`
	// Limit caps how many books are rewritten in one run (0 = no cap).
	Limit int `json:"limit"`
}

type joinedNarratorSample struct {
	NarratorID int      `json:"narrator_id"`
	Name       string   `json:"name"`
	People     []string `json:"people"`
	Books      int      `json:"books"`
}

type heldSplitCredit struct {
	BookID string `json:"book_id"`
	Name   string `json:"name"`
	Reason string `json:"reason"`
}

type splitJoinedReport struct {
	TotalNarrators int                    `json:"total_narrators"`
	Joined         int                    `json:"joined"`
	JoinedSample   []joinedNarratorSample `json:"joined_sample,omitempty"`
	BooksAffected  int                    `json:"books_affected"`
	// HeldForReview counts (book, joined credit) pairs left unsplit because
	// the credit is junk or names only the book's authors. Reported in the
	// dry run too.
	HeldForReview       int               `json:"held_for_review"`
	HeldForReviewSample []heldSplitCredit `json:"held_for_review_sample,omitempty"`
	// Apply only.
	BooksRewritten   int    `json:"books_rewritten"`
	BooksUnchanged   int    `json:"books_unchanged"`
	BooksFailed      int    `json:"books_failed"`
	JournalFailed    int    `json:"journal_failed"`
	NarratorsDeleted int    `json:"narrators_deleted"`
	NarratorsHeld    int    `json:"narrators_held_still_linked"`
	NarratorsFailed  int    `json:"narrators_delete_failed"`
	Aborted          string `json:"aborted,omitempty"`
}

func (r splitJoinedReport) summary() string {
	s := fmt.Sprintf("narrators=%d joined=%d books_affected=%d held_for_review=%d rewritten=%d unchanged=%d failed=%d "+
		"journal_failed=%d joined_deleted=%d joined_held(still linked)=%d joined_delete_failed=%d",
		r.TotalNarrators, r.Joined, r.BooksAffected, r.HeldForReview, r.BooksRewritten, r.BooksUnchanged, r.BooksFailed,
		r.JournalFailed, r.NarratorsDeleted, r.NarratorsHeld, r.NarratorsFailed)
	if r.Aborted != "" {
		s += " ABORTED: " + r.Aborted
	}
	return s
}

func (p *Plugin) splitJoinedNarratorsDef() sdk.OperationDef {
	return sdk.OperationDef{
		ID:          "maintenance.split-joined-narrators",
		Liveness:    sdk.LivenessManual,
		Plugin:      "maintenance",
		DisplayName: "Split joined narrators",
		Description: "Finds narrator entries that are a whole cast in one name (\"A, B & C\"), relinks every " +
			"book crediting one to the individual people, and deletes the joined entry once nothing links " +
			"it. \"Surname, Given\" names are left whole. DRY-RUN BY DEFAULT: pass apply=true to write. " +
			"limit caps books rewritten per run. Holds the scan stand-down on apply. Idempotent.",
		ResumePolicy:    sdk.ResumeDrop,
		DefaultPriority: sdk.PriorityLow,
		ConcurrencyKey:  "maintenance.split-joined-narrators",
		Cancellable:     true,
		Isolate:         false,
		Timeout:         30 * time.Minute,
		Capabilities:    []sdk.Capability{sdk.CapLibraryRead, sdk.CapLibraryWrite},
		Run:             p.runSplitJoinedNarrators,
	}
}

func (p *Plugin) runSplitJoinedNarrators(ctx context.Context, rawParams json.RawMessage, reporter sdk.Reporter) error {
	var params splitJoinedNarratorsParams
	if len(rawParams) > 0 {
		if err := json.Unmarshal(rawParams, &params); err != nil {
			return fmt.Errorf("parse params: %w", err)
		}
	}
	_, err := p.splitJoinedNarrators(ctx, params, reporter)
	return err
}

// joinedNarrators maps each joined narrator's ID to its name: a name the
// credit splitter breaks into two or more pieces.
func joinedNarrators(narrators []database.Narrator) map[int]string {
	out := make(map[int]string)
	for _, n := range narrators {
		pieces := 0
		for _, piece := range util.SplitCreditNames(n.Name) {
			if strings.TrimSpace(piece) != "" {
				pieces++
			}
		}
		if pieces > 1 {
			out[n.ID] = n.Name
		}
	}
	return out
}

// splitCredits returns rows with every joined credit replaced, in its
// position, by the narrators util.CleanNarratorCredit finds in it for this
// book. A joined credit whose verdict is not NarratorCreditPeople is KEPT as
// is and returned in held. resolve maps a person to a narrator ID; it is not
// called for held credits, so a dry run can pass a resolver that only
// records. A narrator credited twice is kept at its first position. Positions
// and roles are renumbered from 0. changed is false when nothing was split.
func splitCredits(bookID string, rows []database.BookNarrator, joined map[int]string, bookAuthors []string,
	resolve func(string) (int, error)) (out []database.BookNarrator, changed bool, held []heldSplitCredit, err error) {
	ordered := append([]database.BookNarrator(nil), rows...)
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].Position < ordered[j].Position })
	var ids []int
	seen := make(map[int]bool)
	add := func(id int) {
		if !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	for _, r := range ordered {
		name, isJoined := joined[r.NarratorID]
		if !isJoined {
			add(r.NarratorID)
			continue
		}
		people, verdict := util.CleanNarratorCredit(name, bookAuthors)
		if verdict != util.NarratorCreditPeople {
			held = append(held, heldSplitCredit{BookID: bookID, Name: name, Reason: splitVerdictReason(verdict)})
			add(r.NarratorID)
			continue
		}
		changed = true
		for _, person := range people {
			id, rerr := resolve(person)
			if rerr != nil {
				return nil, false, held, fmt.Errorf("resolve %q: %w", person, rerr)
			}
			add(id)
		}
	}
	if !changed {
		return rows, false, held, nil
	}
	out = make([]database.BookNarrator, 0, len(ids))
	for i, id := range ids {
		role := "narrator"
		if i > 0 {
			role = "co-narrator"
		}
		out = append(out, database.BookNarrator{BookID: bookID, NarratorID: id, Role: role, Position: i})
	}
	return out, true, held, nil
}

func splitVerdictReason(v util.NarratorCreditVerdict) string {
	switch v {
	case util.NarratorCreditJunk:
		return "not_a_list_of_people"
	case util.NarratorCreditAllAuthors:
		return "names_only_the_books_authors"
	case util.NarratorCreditEmpty:
		return "no_narrator_left_after_cleaning"
	}
	return "unknown"
}

// bookAuthorNamesForSplit reads the names of every author credited on a book
// (join plus author_id column). Fails closed: an incomplete list would turn
// an author into a narrator.
func bookAuthorNamesForSplit(store OpsStore, bookID string) ([]string, error) {
	ids := make(map[int]bool)
	if b, err := store.GetBookByID(bookID); err != nil {
		return nil, fmt.Errorf("read book: %w", err)
	} else if b != nil && b.AuthorID != nil && *b.AuthorID > 0 {
		ids[*b.AuthorID] = true
	}
	links, err := store.GetBookAuthors(bookID)
	if err != nil {
		return nil, fmt.Errorf("read book authors: %w", err)
	}
	for _, l := range links {
		if l.AuthorID > 0 {
			ids[l.AuthorID] = true
		}
	}
	var names []string
	for id := range ids {
		a, err := store.GetAuthorByID(id)
		if err != nil {
			return nil, fmt.Errorf("read author %d: %w", id, err)
		}
		if a != nil {
			names = append(names, a.Name)
		}
	}
	return names, nil
}

func (p *Plugin) splitJoinedNarrators(ctx context.Context, params splitJoinedNarratorsParams, reporter sdk.Reporter) (splitJoinedReport, error) {
	store := p.deps.OpsStore()
	if store == nil {
		return splitJoinedReport{}, fmt.Errorf("database not initialized")
	}
	if params.Limit < 0 {
		return splitJoinedReport{}, fmt.Errorf("split-joined-narrators: limit must be >= 0, got %d", params.Limit)
	}
	log := reporter.Logger()
	log.Info("split-joined-narrators start", "apply", params.Apply, "limit", params.Limit)

	_ = reporter.UpdateProgress(0, 3, "Listing narrators…")
	narrators, err := store.ListNarrators()
	if err != nil {
		return splitJoinedReport{}, fmt.Errorf("list narrators: %w", err)
	}
	joined := joinedNarrators(narrators)
	report := splitJoinedReport{TotalNarrators: len(narrators), Joined: len(joined)}
	if len(joined) == 0 {
		_ = reporter.UpdateProgress(3, 3, "no joined narrators — "+report.summary())
		return report, nil
	}

	_ = reporter.UpdateProgress(1, 3, "Finding books that credit joined narrators…")
	ids := make(map[int]bool, len(joined))
	for id := range joined {
		ids[id] = true
	}
	books, err := database.BooksLinkingNarrators(store, ids)
	if err != nil {
		return report, fmt.Errorf("split-joined-narrators: %w", err)
	}
	report.BooksAffected = len(books)

	perNarrator := make(map[int]int)
	for _, rows := range books {
		counted := make(map[int]bool)
		for _, r := range rows {
			if ids[r.NarratorID] && !counted[r.NarratorID] {
				counted[r.NarratorID] = true
				perNarrator[r.NarratorID]++
			}
		}
	}
	joinedList := make([]database.Narrator, 0, len(joined))
	for _, n := range narrators {
		if _, ok := joined[n.ID]; ok {
			joinedList = append(joinedList, n)
		}
	}
	sort.Slice(joinedList, func(i, j int) bool {
		if perNarrator[joinedList[i].ID] != perNarrator[joinedList[j].ID] {
			return perNarrator[joinedList[i].ID] > perNarrator[joinedList[j].ID]
		}
		return joinedList[i].ID < joinedList[j].ID
	})
	for _, n := range joinedList {
		if len(report.JoinedSample) >= splitJoinedSampleLimit {
			break
		}
		report.JoinedSample = append(report.JoinedSample, joinedNarratorSample{
			NarratorID: n.ID, Name: n.Name, People: util.SplitCreditNames(n.Name), Books: perNarrator[n.ID],
		})
	}

	// Per-book review pass, run in the dry run AND before an apply so both
	// report the same held set. It resolves nothing: the resolver only hands
	// out placeholder IDs so splitCredits can dedupe.
	sortedBooks := make([]string, 0, len(books))
	for id := range books {
		sortedBooks = append(sortedBooks, id)
	}
	sort.Strings(sortedBooks)
	authorsByBook := make(map[string][]string, len(books))
	placeholder := make(map[string]int)
	dryResolve := func(name string) (int, error) {
		if id, ok := placeholder[name]; ok {
			return id, nil
		}
		placeholder[name] = -1 - len(placeholder)
		return placeholder[name], nil
	}
	for _, bookID := range sortedBooks {
		authors, aerr := bookAuthorNamesForSplit(store, bookID)
		if aerr != nil {
			return report, fmt.Errorf("split-joined-narrators: book %s: %w", bookID, aerr)
		}
		authorsByBook[bookID] = authors
		_, _, heldHere, _ := splitCredits(bookID, books[bookID], joined, authors, dryResolve)
		report.HeldForReview += len(heldHere)
		for _, h := range heldHere {
			if len(report.HeldForReviewSample) < splitJoinedSampleLimit {
				report.HeldForReviewSample = append(report.HeldForReviewSample, h)
			}
		}
	}
	log.Info("split-joined-narrators classified", "joined", report.Joined, "books_affected", report.BooksAffected,
		"held_for_review", report.HeldForReview, "sample", report.JoinedSample,
		"held_for_review_sample", report.HeldForReviewSample)

	if !params.Apply {
		_ = reporter.UpdateProgress(3, 3, "DRY RUN (nothing written) — "+report.summary())
		return report, nil
	}

	holderID, held, release, sdErr := acquireScanStandDownForApply(ctx, p.deps, reporter, "split-joined-narrators apply")
	if sdErr != nil {
		return report, fmt.Errorf("split-joined-narrators: could not acquire scan stand-down; refusing to write: %w", sdErr)
	}
	defer release()
	opID := registry.ReporterOpID(reporter)
	defer func() {
		if report.BooksRewritten > 0 || report.NarratorsDeleted > 0 {
			p.deps.InvalidateAuthorsCache()
		}
	}()

	bookIDs := sortedBooks
	if params.Limit > 0 && len(bookIDs) > params.Limit {
		bookIDs = bookIDs[:params.Limit]
	}

	resolved := make(map[string]int)
	resolve := func(name string) (int, error) {
		if id, ok := resolved[name]; ok {
			return id, nil
		}
		n, err := store.CreateNarrator(name)
		if err != nil {
			return 0, err
		}
		if n == nil {
			return 0, fmt.Errorf("store returned no narrator")
		}
		resolved[name] = n.ID
		return n.ID, nil
	}

	_ = reporter.UpdateProgress(2, 3, fmt.Sprintf("Rewriting %d books…", len(bookIDs)))
	prog := sdk.NewProgress(reporter, len(bookIDs))
	prog.Start(fmt.Sprintf("Rewriting %d books…", len(bookIDs)))
	for i, bookID := range bookIDs {
		if err := ctx.Err(); err != nil {
			prog.Done("cancelled — " + report.summary())
			return report, err
		}
		if scanStandDownLostForApply(p.deps, holderID, held) {
			report.Aborted = errSplitJoinedStandDownLost.Error()
			prog.Done(report.summary())
			return report, errSplitJoinedStandDownLost
		}
		// Re-read: the bulk scan may be minutes old.
		current, rerr := store.GetBookNarrators(bookID)
		if rerr != nil {
			report.BooksFailed++
			log.Warn("split-joined-narrators: re-read failed, book skipped", "book_id", bookID, "err", rerr)
			continue
		}
		next, changed, _, serr := splitCredits(bookID, current, joined, authorsByBook[bookID], resolve)
		if serr != nil {
			report.BooksFailed++
			log.Warn("split-joined-narrators: resolve failed, book skipped", "book_id", bookID, "err", serr)
			continue
		}
		if !changed {
			report.BooksUnchanged++
			continue
		}
		before, _ := json.Marshal(current)
		after, _ := json.Marshal(next)
		if jerr := store.CreateOperationChange(&database.OperationChange{
			ID:          ulid.Make().String(),
			OperationID: opID,
			BookID:      bookID,
			ChangeType:  "narrator_split",
			FieldName:   "book_narrators",
			OldValue:    string(before),
			NewValue:    string(after),
		}); jerr != nil {
			report.JournalFailed++
			log.Warn("split-joined-narrators: undo-ledger write failed, book NOT rewritten", "book_id", bookID, "err", jerr)
			continue
		}
		if werr := store.SetBookNarrators(bookID, next); werr != nil {
			report.BooksFailed++
			log.Warn("split-joined-narrators: rewrite failed (its ledger row was already written)", "book_id", bookID, "err", werr)
			continue
		}
		report.BooksRewritten++
		if (i+1)%50 == 0 {
			prog.StepN(i+1, fmt.Sprintf("Rewrote %d/%d…", report.BooksRewritten, len(bookIDs)))
		}
	}

	// Delete joined entities nothing links any more. A limited run leaves
	// some linked; they are held, and the next run finishes them.
	for _, n := range joinedList {
		if err := ctx.Err(); err != nil {
			prog.Done("cancelled — " + report.summary())
			return report, err
		}
		if scanStandDownLostForApply(p.deps, holderID, held) {
			report.Aborted = errSplitJoinedStandDownLost.Error()
			prog.Done(report.summary())
			return report, errSplitJoinedStandDownLost
		}
		links, lerr := database.NarratorLinkCount(store, n.ID)
		if lerr != nil {
			prog.Done(report.summary())
			return report, fmt.Errorf("split-joined-narrators: re-check narrator %d before delete: %w", n.ID, lerr)
		}
		if links > 0 {
			report.NarratorsHeld++
			continue
		}
		if jerr := store.CreateOperationChange(&database.OperationChange{
			ID:          ulid.Make().String(),
			OperationID: opID,
			ChangeType:  "narrator_delete",
			FieldName:   "narrator",
			OldValue:    fmt.Sprintf("%d:%s", n.ID, n.Name),
			NewValue:    "split_joined",
		}); jerr != nil {
			report.JournalFailed++
			log.Warn("split-joined-narrators: undo-ledger write failed, narrator NOT deleted", "narrator_id", n.ID, "err", jerr)
			continue
		}
		if derr := store.DeleteNarrator(n.ID); derr != nil {
			report.NarratorsFailed++
			log.Warn("split-joined-narrators: delete failed", "narrator_id", n.ID, "err", derr)
			continue
		}
		report.NarratorsDeleted++
	}

	log.Info("split-joined-narrators complete", "summary", report.summary())
	prog.Done(report.summary())
	return report, nil
}
