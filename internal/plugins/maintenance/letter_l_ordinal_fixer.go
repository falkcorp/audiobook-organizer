// file: internal/plugins/maintenance/letter_l_ordinal_fixer.go
// version: 1.4.0
// guid: 9e4b7c21-6a3f-4d58-b1e0-2c8d5f9a3b47
// last-edited: 2026-10-03

package maintenance

import (
	"context"
	"encoding/json"
	"fmt"
	"sync/atomic"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/internal/repairs"
	"github.com/falkcorp/audiobook-organizer/internal/util"
)

// letterLOrdinalFixerID is the Repairs-lane id of the letter-l ordinal fixer.
const letterLOrdinalFixerID = "maintenance.normalize-letter-l-ordinals"

// letterLOrdinalFixer rewrites numbered-book titles whose ordinal was typed
// with the letter l ("l Corinthians", "ll Kings") to arabic digits
// ("1 Corinthians", "2 Kings"). The source folder of the Bible collection
// used the glyph, and the titles inherited it. Only a numbered book name
// after the token qualifies (util.NormalizeLetterLOrdinal), so "l'Étranger"
// is never a row.
//
// The title goes through writeTitleOnly, which journals it and locks it (the
// file tags still carry the letter-l spelling, and a forced rescan would
// write it back otherwise). Undo is the apply operation's revert, which puts
// the title back and lifts the lock; "Undo last apply" refuses the batch.
type letterLOrdinalFixer struct{ p *Plugin }

func newLetterLOrdinalFixer(p *Plugin) *letterLOrdinalFixer { return &letterLOrdinalFixer{p: p} }

var _ repairs.Fixer = (*letterLOrdinalFixer)(nil)

func (f *letterLOrdinalFixer) ID() string    { return letterLOrdinalFixerID }
func (f *letterLOrdinalFixer) Title() string { return "Letter-l ordinals (\"l Corinthians\")" }
func (f *letterLOrdinalFixer) Description() string {
	return "Titles of numbered books whose ordinal is the letter l instead of the digit 1 or roman I " +
		"(\"l Corinthians\", \"ll Kings\"). Rewrites the ordinal to digits (\"1 Corinthians\", \"2 Kings\") and " +
		"leaves the rest of the title alone. Writes the title only, never a user-locked one, and locks the title it " +
		"wrote so a rescan cannot restore the file tag's spelling. Undo with the apply operation's revert, which also " +
		"lifts the lock."
}

// Plan lists every live book whose title uses a letter-l ordinal.
func (f *letterLOrdinalFixer) Plan(ctx context.Context, _ json.RawMessage, rep registry.Reporter) ([]repairs.Row, error) {
	store := f.p.deps.OpsStore()
	if store == nil {
		return nil, fmt.Errorf("database not initialized")
	}
	all, err := store.GetAllBooksCore(0, 0)
	if err != nil {
		return nil, fmt.Errorf("GetAllBooksCore: %w", err)
	}
	var cands []database.BookCore
	for i := range all {
		if all[i].IsSoftDeleted() {
			continue
		}
		if _, ok := util.NormalizeLetterLOrdinal(all[i].Title); ok {
			cands = append(cands, all[i])
		}
	}
	rows := make([]repairs.Row, len(cands))
	var done atomic.Int64
	// Each worker writes only rows[i] for its own i.
	runErr := registry.RunItems(ctx, rep, indexesOf(len(cands)), func(_ context.Context, i int) error {
		defer done.Add(1)
		r, err := f.evaluate(cands[i])
		if err != nil {
			r = repairs.Row{RowID: cands[i].ID, BookIDs: []string{cands[i].ID}, Title: cands[i].Title,
				Skipped: "error", SkipReason: err.Error(), Reason: err.Error(), Risk: repairs.RiskReview}
			r.Fingerprint = junkFingerprint(r, "")
		}
		rows[i] = r
		return nil
	}, registry.RunItemsOptions{
		Concurrency: titleRepairWorkers(),
		ErrMode:     registry.ErrModeCollect,
		Label:       func(_, total int) string { return fmt.Sprintf("Letter-l ordinals %d/%d", done.Load(), total) },
	})
	if runErr != nil && ctx.Err() != nil {
		return nil, ctx.Err()
	}
	return rows, nil
}

// Replan re-reads the book; a book that is gone or already fixed comes back
// skipped with a different fingerprint (changed_since_plan), never an error.
func (f *letterLOrdinalFixer) Replan(_ context.Context, _ json.RawMessage, planned repairs.Row, _ registry.Reporter) (repairs.Row, error) {
	store := f.p.deps.OpsStore()
	if store == nil {
		return repairs.Row{}, fmt.Errorf("database not initialized")
	}
	b, err := store.GetBookByID(planned.RowID)
	if err != nil {
		return repairs.Row{}, fmt.Errorf("read book %s: %w", planned.RowID, err)
	}
	if b == nil || b.IsSoftDeleted() {
		r := repairs.Row{RowID: planned.RowID, BookIDs: []string{planned.RowID}, Skipped: junkSkipGone,
			SkipReason: "the book no longer exists", Risk: repairs.RiskLow}
		r.Fingerprint = junkFingerprint(r, "gone")
		return r, nil
	}
	return f.evaluate(b.Core())
}

func (f *letterLOrdinalFixer) evaluate(b database.BookCore) (repairs.Row, error) {
	r := repairs.Row{RowID: b.ID, BookIDs: []string{b.ID}, Title: b.Title, Risk: repairs.RiskLow,
		Current: map[string]string{"title": b.Title}}
	fixed, ok := util.NormalizeLetterLOrdinal(b.Title)
	if !ok {
		r.Skipped, r.SkipReason = junkSkipNotJunk, "the title no longer uses a letter-l ordinal"
		r.Reason = r.SkipReason
		r.Fingerprint = junkFingerprint(r, "")
		return r, nil
	}
	states, err := f.p.deps.OpsStore().GetMetadataFieldStates(b.ID)
	if err != nil {
		return repairs.Row{}, fmt.Errorf("read field states of %s: %w", b.ID, err)
	}
	for i := range states {
		if states[i].Field == "title" && states[i].HasUserOverride() {
			r.Skipped, r.SkipReason = lockHold(&states[i], "title")
			r.Reason = r.SkipReason
			r.Fingerprint = junkFingerprint(r, "")
			return r, nil
		}
	}
	if why := legacyStateUnreadable(f.p.deps.OpsStore(), b.ID, states); why != "" {
		r.Skipped, r.SkipReason = junkSkipNeedsManual, why
		r.Reason = r.SkipReason
		r.Fingerprint = junkFingerprint(r, "")
		return r, nil
	}
	if len(states) == 0 {
		// A blob-only book's title lock lives in the blob.
		locks, lerr := database.LoadFieldLocks(f.p.deps.OpsStore(), b.ID)
		if lerr != nil {
			return repairs.Row{}, fmt.Errorf("read field locks of %s: %w", b.ID, lerr)
		}
		if locks.Locked(database.FieldKeyTitle) {
			r.Skipped, r.SkipReason = junkSkipUserLocked, "the title carries a user override (pre-migration state); it is never rewritten"
			r.Reason = r.SkipReason
			r.Fingerprint = junkFingerprint(r, "")
			return r, nil
		}
	}
	r.Proposed = map[string]string{"title": fixed}
	r.Reason = "the ordinal is the letter l, not the digit 1 / roman I"
	r.Detail = &junkDecision{bookID: b.ID, oldTitle: b.Title, newTitle: fixed}
	r.Fingerprint = junkFingerprint(r, "letter-l")
	return r, nil
}

// Apply writes the title only, while it is still the planned one.
func (f *letterLOrdinalFixer) Apply(_ context.Context, w *repairs.Writer, fresh repairs.Row) error {
	d, ok := fresh.Detail.(*junkDecision)
	if !ok || d == nil {
		return fmt.Errorf("%s: row %s carries no decision", letterLOrdinalFixerID, fresh.RowID)
	}
	return writeTitleOnly(w, f.p.deps.OpsStore(), d.bookID, d.oldTitle, d.newTitle)
}
