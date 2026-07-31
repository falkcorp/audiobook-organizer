// file: internal/maintenance/jobs/backfill_itunes_positions.go
// version: 1.0.0
// guid: 19a97553-68fc-4ef6-a326-cc9e694d8698
// last-edited: 2026-07-30

package jobs

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"os"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/maintenance"
	"github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/internal/syncapi/progress"
)

func init() { maintenance.Register(&backfillITunesPositionsJob{}) }

// ITunesPositionWholeBookSegmentID is the UserPosition.SegmentID the migrated
// iTunes/Apple Books position is written under.
//
// ABS progress is a single whole-book scalar (`currentTime` in seconds), not a
// per-track value, and Book.ITunesBookmark is likewise one scalar per book —
// there is no track to attribute it to. A dedicated, well-known segment ID
// keeps the migrated row distinguishable from the opaque per-device segment IDs
// legacy playback code writes (see internal/merge/sync_follow.go, which treats
// segment IDs as opaque per-user bookkeeping), which is what makes a re-run's
// "did I already write this?" check exact rather than a guess.
const ITunesPositionWholeBookSegmentID = "abs-whole-book"

// ITunesPositionBackfillUserIDEnv names the environment variable that pins
// which user the migrated positions belong to.
//
// This knob is an env var rather than a job parameter because
// maintenance.MaintenanceJob.Run only receives dryRun — the dispatcher
// (internal/server/maintenance_dispatcher.go) decodes nothing else from the
// request body, so DefaultParams is display-only for the UI.
const ITunesPositionBackfillUserIDEnv = "ABS_ITUNES_POSITION_BACKFILL_USER_ID"

// ITunesPositionBookmarkTitle is the title of the ONE named bookmark this job
// drops at the migrated position, so the owner can see and jump back to where
// Apple Books left them rather than only inheriting an invisible resume point.
//
// Exactly one, and only at the migrated offset. Book.ITunesBookmark is a single
// scalar resume position — the farthest point read — not a bookmark collection,
// so there is nothing to synthesize a list from. The ABS named-bookmark feature
// (internal/database/pebble_store_bookmarks.go, whose own doc comment makes the
// same distinction) is an independent system that clients populate themselves;
// this job must not backfill that keyspace beyond this single courtesy marker.
const ITunesPositionBookmarkTitle = "Imported from Apple Books"

// ITunesPositionProgressFraction returns the ABS `progress` value: a fraction
// in [0.0, 1.0], NEVER a 0-100 percentage.
//
// It is exported because the Phase-6 ABS mediaProgress DTO needs exactly this
// computation and must not re-derive it differently — a percentage leaking into
// `progress` reads as 5000% complete on a client, and an unclamped value >1.0
// is rejected outright by AudioBooth's strict decoder. A non-positive or
// non-finite duration yields 0.0: the fraction is genuinely unknown then, and
// guessing is what trips §1.8.7's null-duration trap.
func ITunesPositionProgressFraction(currentTimeSec, durationSec float64) float64 {
	if durationSec <= 0 || math.IsNaN(durationSec) || math.IsInf(durationSec, 0) {
		return 0
	}
	if math.IsNaN(currentTimeSec) || currentTimeSec <= 0 {
		return 0
	}
	f := currentTimeSec / durationSec
	if f > 1 {
		return 1
	}
	return f
}

// ResolveITunesPositionBackfillUser decides which user owns the migrated
// positions.
//
// The progress store is user-keyed but an iTunes bookmark is not: iTunes/Apple
// Books is a single-person library with no user concept at all, so the mapping
// is genuinely ambiguous the moment more than one user exists. Rather than
// guess silently, the resolution order is:
//
//  1. ABS_ITUNES_POSITION_BACKFILL_USER_ID, if set — and an ID that matches no
//     user is a hard error, not a silent fallback. Migrating a whole library's
//     positions onto the wrong account because of a typo is worse than not
//     running.
//  2. Exactly one user in the store — unambiguous, no warning needed.
//  3. Otherwise the earliest-created user (ties broken by ID so the choice is
//     stable across runs), with a WARN naming the chosen user and the override.
//     "Earliest created" is the owner's own account in practice: it is the
//     account the bootstrap flow creates before any other.
func ResolveITunesPositionBackfillUser(store database.Store) (string, error) {
	users, err := store.ListUsers()
	if err != nil {
		return "", fmt.Errorf("list users: %w", err)
	}
	if len(users) == 0 {
		return "", fmt.Errorf("no users exist — there is no account to migrate iTunes positions onto")
	}

	if override := strings.TrimSpace(os.Getenv(ITunesPositionBackfillUserIDEnv)); override != "" {
		for _, u := range users {
			if u.ID == override {
				return u.ID, nil
			}
		}
		return "", fmt.Errorf("%s=%q matches no existing user", ITunesPositionBackfillUserIDEnv, override)
	}

	if len(users) == 1 {
		return users[0].ID, nil
	}

	sorted := make([]database.User, len(users))
	copy(sorted, users)
	sort.Slice(sorted, func(i, j int) bool {
		if !sorted[i].CreatedAt.Equal(sorted[j].CreatedAt) {
			return sorted[i].CreatedAt.Before(sorted[j].CreatedAt)
		}
		return sorted[i].ID < sorted[j].ID
	})
	chosen := sorted[0]
	slog.Warn("backfill-itunes-positions: more than one user exists and iTunes bookmarks carry no user identity — "+
		"defaulting to the earliest-created account",
		"chosen_user", chosen.ID, "chosen_username", chosen.Username,
		"users", len(users), "override_with", ITunesPositionBackfillUserIDEnv)
	return chosen.ID, nil
}

// backfillITunesPositionsJob migrates Book.ITunesBookmark into the per-user
// progress store so the owner's existing listening positions survive the move
// off iTunes/Apple Books. Starting from a blank slate is an explicit non-goal.
//
// ⚠️ DOWNSTREAM DEPENDENCY — do not run this before the ABS media-progress
// provider is wired. §1.8.1: AudioBooth's MediaProgress.syncFromAPI DELETES any
// local progress row whose bookID is absent from /api/me's mediaProgress list.
// That provider is still nil (internal/server/wire_abs_routes.go logs
// "no media-progress provider is wired"), so real progress records existing
// server-side while /api/me reports an empty list would make a connecting
// client destroy its own local progress. This job is invoke-only — maintenance
// jobs run solely via POST /maintenance/jobs/:job_id and there is no cron path
// to it — which is deliberate, not incidental.
type backfillITunesPositionsJob struct{}

func (j *backfillITunesPositionsJob) ID() string       { return "backfill-itunes-positions" }
func (j *backfillITunesPositionsJob) Name() string     { return "Backfill iTunes/Apple Books Positions" }
func (j *backfillITunesPositionsJob) Category() string { return "itunes" }

func (j *backfillITunesPositionsJob) Description() string {
	return "Migrates each book's saved iTunes/Apple Books bookmark into the per-user progress store, forward-only so a newer position from a device is never rewound"
}

func (j *backfillITunesPositionsJob) DefaultParams() any {
	return struct {
		DryRun bool   `json:"dry_run"`
		UserID string `json:"user_id_env_hint"`
	}{DryRun: false, UserID: ITunesPositionBackfillUserIDEnv}
}

// CanResume is false for the same reason as backfill-sync-ids: every per-book
// decision is recomputed from scratch and the merge policy makes a re-run a
// no-op when nothing changed, so restarting from book 0 IS the resume story.
func (j *backfillITunesPositionsJob) CanResume() bool { return false }

// migrateResult is what one book's worker decided, for counting.
type migrateResult struct {
	outcome positionOutcome
	// hadDuration is false when no authoritative duration was determinable, in
	// which case the position still carried over but the book can never
	// auto-mark finished.
	hadDuration     bool
	bookmarkCreated bool
}

// positionOutcome is the per-book disposition.
type positionOutcome int

const (
	outcomeMigrated positionOutcome = iota
	outcomeNoBookmark
	outcomeNotAccepted
	outcomeUnchanged
	outcomeMissingBook
	outcomeFailed
)

func (j *backfillITunesPositionsJob) Run(ctx context.Context, store database.Store, reporter maintenance.ProgressReporter, dryRun bool) error {
	userID, err := ResolveITunesPositionBackfillUser(store)
	if err != nil {
		return fmt.Errorf("backfill-itunes-positions: %w", err)
	}

	// ListBookIDs, not GetAllBooksFrom: the latter's memdb fast path silently
	// caps a page at 2× the requested limit, so a paginated walk can miss books
	// — and a missed book here is a listening position quietly not migrated.
	ids, err := store.ListBookIDs()
	if err != nil {
		return fmt.Errorf("backfill-itunes-positions: list book ids: %w", err)
	}
	reporter.SetTotal(len(ids))

	// The courtesy bookmark is best-effort: a store that does not implement the
	// bookmark capability must not stop the resume positions — which are the
	// actual requirement — from migrating.
	bookmarkStore := database.AsBookmarkStore(store)
	if bookmarkStore == nil {
		slog.Warn("backfill-itunes-positions: store does not implement the bookmark capability — " +
			"resume positions will migrate but no courtesy bookmark will be dropped")
	}

	slog.Info("backfill-itunes-positions: starting", "books", len(ids), "user", userID,
		"dry_run", dryRun, "concurrency", BackfillConcurrency())

	var migrated, noBookmark, notAccepted, unchanged, missingBook, failed, noDuration, bookmarks atomic.Int64

	rep := &absBackfillReporter{ctx: ctx, inner: reporter}
	runErr := registry.RunItems(ctx, rep, ids, func(_ context.Context, bookID string) error {
		res := j.migrateOne(store, bookmarkStore, userID, bookID, dryRun)
		if res.outcome == outcomeMigrated && !res.hadDuration {
			noDuration.Add(1)
		}
		if res.bookmarkCreated {
			bookmarks.Add(1)
		}
		switch res.outcome {
		case outcomeMigrated:
			migrated.Add(1)
		case outcomeNoBookmark:
			noBookmark.Add(1)
		case outcomeNotAccepted:
			notAccepted.Add(1)
		case outcomeUnchanged:
			unchanged.Add(1)
		case outcomeMissingBook:
			missingBook.Add(1)
		case outcomeFailed:
			failed.Add(1)
		}
		return nil
	}, registry.RunItemsOptions{
		// Per-book work partitions cleanly by book ID — two workers can never
		// touch the same (user, book) rows — so the pool needs no extra
		// locking. Bounded at NumCPU: a bare sequential loop over the whole
		// library is the pattern behind this repo's 3-hour single-core stall.
		Concurrency: BackfillConcurrency(),
		ErrMode:     registry.ErrModeCollect,
		Label:       func(i, total int) string { return fmt.Sprintf("book %d/%d", i+1, total) },
	})

	summary := fmt.Sprintf(
		"backfill-itunes-positions complete (dry_run=%t, user=%s): migrated=%d no_bookmark=%d "+
			"not_accepted=%d unchanged=%d missing_book=%d failed=%d no_duration=%d bookmarks=%d of %d books",
		dryRun, userID, migrated.Load(), noBookmark.Load(), notAccepted.Load(), unchanged.Load(),
		missingBook.Load(), failed.Load(), noDuration.Load(), bookmarks.Load(), len(ids))
	slog.Info(summary)
	reporter.Log("info", summary, nil)

	return runErr
}

// migrateOne carries one book's iTunes resume position onto (userID, bookID).
//
// Every failure path logs the book ID and returns outcomeFailed rather than
// returning nil quietly: a silently skipped book is a listening position the
// owner loses without ever being told.
func (j *backfillITunesPositionsJob) migrateOne(store database.Store, bookmarkStore database.BookmarkStore, userID, bookID string, dryRun bool) migrateResult {
	book, err := store.GetBookByID(bookID)
	if err != nil {
		slog.Warn("backfill-itunes-positions: get book failed", "book", bookID, "err", err)
		return migrateResult{outcome: outcomeFailed}
	}
	if book == nil {
		slog.Warn("backfill-itunes-positions: book id listed but not readable", "book", bookID)
		return migrateResult{outcome: outcomeMissingBook}
	}

	// ITunesBookmark is MILLISECONDS and is a single scalar per book. nil means
	// the book was never opened — writing a zero position there would fabricate
	// progress the user never had, so it is skipped, not zeroed. A zero or
	// negative value is the same "not started" signal.
	if book.ITunesBookmark == nil || *book.ITunesBookmark <= 0 {
		return migrateResult{outcome: outcomeNoBookmark}
	}
	positionSec := float64(*book.ITunesBookmark) / 1000.0

	durationSec, hadDuration := j.authoritativeDurationSec(store, book)
	if !hadDuration {
		slog.Warn("backfill-itunes-positions: no authoritative duration — position carries over "+
			"but the book can never auto-mark finished (§5b/§1.8.7)", "book", bookID)
	}

	existingPositions, err := store.ListUserPositionsForBook(userID, bookID)
	if err != nil {
		slog.Warn("backfill-itunes-positions: list existing positions failed", "book", bookID, "err", err)
		return migrateResult{outcome: outcomeFailed, hadDuration: hadDuration}
	}
	existingState, err := store.GetUserBookState(userID, bookID)
	if err != nil {
		slog.Warn("backfill-itunes-positions: get existing state failed", "book", bookID, "err", err)
		return migrateResult{outcome: outcomeFailed, hadDuration: hadDuration}
	}

	server := j.serverProgress(existingPositions, existingState, durationSec)
	incoming := progress.Progress{
		CurrentTime: positionSec,
		Duration:    durationSec,
		UpdatedAtMs: j.incomingUpdatedAtMs(book, server.UpdatedAtMs),
	}

	// MergeIncoming, never a bare write: it is what applies §5's newer-wins and
	// forward-only rules, so a user who already listened further on a device
	// cannot be rewound, and it is what derives IsFinished with §5b's ≥2s
	// tolerance instead of an epsilon that would leave a finished book at 99%
	// forever.
	merged, accepted := progress.MergeIncoming(server, incoming)
	if !accepted {
		// The stored position is newer-or-equal AND at least as far along.
		// Still repair a state row that drifted out of sync with it, so a
		// half-applied earlier run is self-healing rather than permanently
		// stale — but never touch the position itself.
		return migrateResult{
			outcome:     j.repairStateOnly(store, userID, bookID, server, existingState, dryRun),
			hadDuration: hadDuration,
		}
	}

	if dryRun {
		return migrateResult{outcome: outcomeMigrated, hadDuration: hadDuration}
	}

	// Position first, state second: the position is the load-bearing datum (the
	// actual resume point); state is derived from it and is repairable by a
	// re-run via repairStateOnly above.
	if err := store.SetUserPosition(userID, bookID, ITunesPositionWholeBookSegmentID, merged.CurrentTime); err != nil {
		slog.Warn("backfill-itunes-positions: write position failed", "book", bookID, "err", err)
		return migrateResult{outcome: outcomeFailed, hadDuration: hadDuration}
	}
	if err := store.SetUserBookState(j.desiredState(userID, bookID, merged, existingState)); err != nil {
		slog.Warn("backfill-itunes-positions: write user-book state failed (position was written)",
			"book", bookID, "err", err)
		return migrateResult{outcome: outcomeFailed, hadDuration: hadDuration}
	}
	slog.Debug("backfill-itunes-positions: migrated", "book", bookID, "user", userID,
		"position_sec", merged.CurrentTime, "duration_sec", merged.Duration,
		"is_finished", merged.IsFinished)

	return migrateResult{
		outcome:         outcomeMigrated,
		hadDuration:     hadDuration,
		bookmarkCreated: j.dropCourtesyBookmark(bookmarkStore, userID, bookID, merged.CurrentTime),
	}
}

// dropCourtesyBookmark writes the single named bookmark described by
// ITunesPositionBookmarkTitle at the migrated offset, and reports whether it
// wrote one.
//
// Deliberately best-effort and never fatal: the resume position is the
// requirement, this marker is a courtesy, so a bookmark-store failure is logged
// and the book still counts as migrated. It only runs on an accepted migration,
// which is also what makes it idempotent in practice — a re-run's merge rejects
// before reaching here, so CreateBookmark's UpdatedAt is not re-stamped.
//
// itemID is the Book ULID, the same identity the UserPosition/UserBookState
// rows written above use. Mapping to a sync_item syncID belongs to the DTO
// layer that eventually serves these to a client, not to two different
// identities inside one migration.
func (j *backfillITunesPositionsJob) dropCourtesyBookmark(bookmarkStore database.BookmarkStore, userID, bookID string, timeSec float64) bool {
	if bookmarkStore == nil {
		return false
	}
	if err := bookmarkStore.CreateBookmark(progress.Bookmark{
		UserID:  userID,
		ItemID:  bookID,
		TimeSec: timeSec,
		Title:   ITunesPositionBookmarkTitle,
	}); err != nil {
		slog.Warn("backfill-itunes-positions: courtesy bookmark failed (the resume position was still migrated)",
			"book", bookID, "user", userID, "err", err)
		return false
	}
	return true
}

// serverProgress builds the merge policy's "server" side from what is already
// stored.
//
// CurrentTime is max(PositionSeconds) across every segment row, not the
// most-recently-written row that GetUserPosition would return. That is
// deliberately more conservative than §5's live-PATCH rule: legacy rows are
// per-device/per-segment (internal/merge/sync_follow.go calls segment IDs
// "opaque per-user bookkeeping") and are not directly comparable to a
// whole-book scalar, so a migration must treat the furthest of them as the
// position it must not rewind. UpdatedAtMs likewise takes the newest stamp
// available, which only makes the incoming clamp stricter.
func (j *backfillITunesPositionsJob) serverProgress(positions []database.UserPosition, state *database.UserBookState, durationSec float64) progress.Progress {
	out := progress.Progress{Duration: durationSec}
	for _, p := range positions {
		if p.PositionSeconds > out.CurrentTime {
			out.CurrentTime = p.PositionSeconds
		}
		if ms := p.UpdatedAt.UnixMilli(); !p.UpdatedAt.IsZero() && ms > out.UpdatedAtMs {
			out.UpdatedAtMs = ms
		}
	}
	if state != nil {
		out.IsFinished = state.Status == database.UserBookStatusFinished
		for _, ts := range []time.Time{state.LastActivityAt, state.UpdatedAt} {
			if ms := ts.UnixMilli(); !ts.IsZero() && ms > out.UpdatedAtMs {
				out.UpdatedAtMs = ms
			}
		}
	}
	return out
}

// incomingUpdatedAtMs picks the ms epoch that stands in for "when iTunes last
// knew this position", then clamps it to the server's existing stamp.
//
// The stamp itself comes from real data — ITunesLastPlayed, then
// ITunesDateAdded, then the book's own row timestamps — never time.Now(): a
// fresh stamp would win §5's newer-wins branch outright and let a stale iTunes
// bookmark overwrite a device's genuinely newer position, which is precisely
// the rewind this job must not cause. (It would also beat AudioBooth's own
// truncated-seconds comparison client-side and make the client discard its
// newer local value.)
//
// The clamp is the belt to that braces: when the server already has a stamp,
// the incoming one is capped at it, so the merge can only ever accept via the
// forward-only branch — i.e. only when the iTunes position is strictly further
// along. This is an honest statement about the data, not a trick: any
// server-side progress row was written by a live device after the library was
// imported, so the iTunes bookmark is by construction not newer.
//
// A zero result would be worse than useless: §1.7.3 #1 notes an absent
// updatedAt makes the server lose every conversation about conflicts, because
// clients compare it against their own wall clock. Hence the final fallback to
// 1 ms rather than 0.
func (j *backfillITunesPositionsJob) incomingUpdatedAtMs(book *database.Book, serverUpdatedAtMs int64) int64 {
	var chosen int64
	for _, ts := range []*time.Time{book.ITunesLastPlayed, book.ITunesDateAdded, book.UpdatedAt, book.CreatedAt} {
		if ts == nil || ts.IsZero() {
			continue
		}
		if ms := ts.UnixMilli(); ms > 0 {
			chosen = ms
			break
		}
	}
	if chosen <= 0 {
		chosen = 1
	}
	if serverUpdatedAtMs > 0 && chosen > serverUpdatedAtMs {
		chosen = serverUpdatedAtMs
	}
	return chosen
}

// desiredState renders a merged Progress into the store's UserBookState shape.
//
// Two representation notes worth stating out loud:
//   - UserBookState.ProgressPct is an int 0-100 (this store's long-standing
//     shape). The authoritative value derived here is the 0.0-1.0 fraction from
//     ITunesPositionProgressFraction — which is what the ABS `progress` field
//     must carry — and the percentage is a lossy rendering of it at the store
//     boundary, not the other way round.
//   - LastActivityAt carries the merged ms epoch exactly (time.UnixMilli
//     round-trips ms losslessly), and is the field the ABS DTO should report as
//     lastUpdate. UserPosition.UpdatedAt cannot serve that role:
//     PebbleStore.SetUserPosition stamps it time.Now() unconditionally.
//
// TotalListenedSeconds is deliberately left as-is: a bookmark is a position,
// not an amount of audio listened, and inventing one would corrupt listening
// stats. A manual status override (StatusManual) is likewise preserved — it is
// a user decision this migration has no standing to overrule.
func (j *backfillITunesPositionsJob) desiredState(userID, bookID string, merged progress.Progress, existing *database.UserBookState) *database.UserBookState {
	state := database.UserBookState{UserID: userID, BookID: bookID}
	if existing != nil {
		state = *existing
		state.UserID = userID
		state.BookID = bookID
	}

	fraction := ITunesPositionProgressFraction(merged.CurrentTime, merged.Duration)
	state.ProgressPct = int(math.Round(fraction * 100))
	if state.ProgressPct < 0 {
		state.ProgressPct = 0
	}
	if state.ProgressPct > 100 {
		state.ProgressPct = 100
	}

	if !state.StatusManual {
		if merged.IsFinished {
			state.Status = database.UserBookStatusFinished
			state.ProgressPct = 100
		} else {
			state.Status = database.UserBookStatusInProgress
		}
	}
	// Never render an unfinished book as 100%: rounding a fraction like 0.9997
	// (3 s short of a 9975 s book — outside §5b's 2 s tolerance, so genuinely
	// not finished) up to 100 would contradict the status beside it. The
	// authoritative fraction is untouched; this only affects the lossy
	// percentage rendering.
	if state.Status != database.UserBookStatusFinished && state.ProgressPct >= 100 {
		state.ProgressPct = 99
	}
	state.LastSegmentID = ITunesPositionWholeBookSegmentID
	state.LastActivityAt = time.UnixMilli(merged.UpdatedAtMs).UTC()
	return &state
}

// repairStateOnly runs when the merge rejected the incoming position (the
// stored one is newer-or-equal and at least as far along). The position is left
// strictly untouched; only a state row that disagrees with the stored position
// is rewritten, so a run that died between the two writes heals on the next
// run instead of leaving the book permanently mis-labelled.
func (j *backfillITunesPositionsJob) repairStateOnly(store database.Store, userID, bookID string, server progress.Progress, existing *database.UserBookState, dryRun bool) positionOutcome {
	if server.CurrentTime <= 0 {
		// Nothing stored to describe.
		return outcomeNotAccepted
	}
	derived := server
	derived.IsFinished = server.IsFinished || progress.IsWithinFinishedTolerance(server.CurrentTime, server.Duration)
	if existing != nil && derived.UpdatedAtMs > 0 {
		// Keep the existing stamp — this is a repair, not a new observation.
		derived.UpdatedAtMs = existing.LastActivityAt.UnixMilli()
		if derived.UpdatedAtMs <= 0 {
			derived.UpdatedAtMs = server.UpdatedAtMs
		}
	}
	want := j.desiredState(userID, bookID, derived, existing)

	if existing != nil &&
		existing.Status == want.Status &&
		existing.ProgressPct == want.ProgressPct &&
		existing.LastActivityAt.Equal(want.LastActivityAt) {
		return outcomeUnchanged
	}
	if existing != nil && existing.Status == "" && want.Status == "" {
		return outcomeUnchanged
	}
	if dryRun {
		return outcomeNotAccepted
	}
	if err := store.SetUserBookState(want); err != nil {
		slog.Warn("backfill-itunes-positions: repair user-book state failed", "book", bookID, "err", err)
		return outcomeFailed
	}
	slog.Info("backfill-itunes-positions: repaired a user-book state that had drifted from its stored position",
		"book", bookID, "user", userID, "status", want.Status, "progress_pct", want.ProgressPct)
	return outcomeNotAccepted
}

// authoritativeDurationSec returns the ONE duration per book that §5b requires
// be used consistently for progress math, and whether one was determinable.
//
// Preference order is the spec's recommendation: the sum of track durations,
// "since it matches the timeline clients actually seek within (and matches real
// ABS startOffset values exactly)". Book.Duration (the container's own value)
// is the fallback — the same book legitimately reports three different
// durations from three sources (container / last-chapter-end / track-sum,
// ~52 ms apart on the measured Odyssey fixture), and mixing them across the
// fraction and the finished check is what produces a book stuck at 99%.
func (j *backfillITunesPositionsJob) authoritativeDurationSec(store database.Store, book *database.Book) (float64, bool) {
	files, err := store.GetBookFiles(book.ID)
	if err != nil {
		slog.Warn("backfill-itunes-positions: list book files failed, falling back to Book.Duration",
			"book", book.ID, "err", err)
	} else {
		var sum int
		for _, f := range files {
			if f.Duration > 0 {
				sum += f.Duration
			}
		}
		if sum > 0 {
			return float64(sum), true
		}
	}
	if book.Duration != nil && *book.Duration > 0 {
		return float64(*book.Duration), true
	}
	return 0, false
}
