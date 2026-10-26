// file: internal/maintenance/jobs/backfill_file_hashes_aggregates_test.go
// version: 2.0.0
// guid: 3f6a1c84-b2d7-4e95-8a13-d90e5b7c4f26
// last-edited: 2026-10-26

package jobs_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/database/aggtest"
	"github.com/falkcorp/audiobook-organizer/internal/maintenance/jobs"
)

// hbChunkedWant is how many times each book must be recomputed over a run of
// hbMixedLibrary(total): once per chunk that stores at least one of its hashes.
func hbChunkedWant(total int) map[string]int {
	want := map[string]int{}
	for start := 0; start < total; start += jobs.HashBackfillChunkSize {
		seen := map[string]bool{}
		for i := start; i < min(start+jobs.HashBackfillChunkSize, total); i++ {
			if i%10 == 0 { // hbMixedLibrary hashes every tenth row
				seen[fmt.Sprintf("b%d", i%9)] = true
			}
		}
		for b := range seen {
			want[b]++
		}
	}
	return want
}

// Storing N hashes of one book recomputes the book's aggregates once per chunk,
// not once per hash. The mock's ModifyBookFile (the recomputing variant) fails the
// test, so the per-row variant cannot be used; the counts here are of the
// explicit recompute calls. A dry run stores nothing and recomputes nothing.
func TestBackfillFileHashes_RecomputesEachBookOncePerChunk(t *testing.T) {
	const total = 1300
	root := hbLibrary(t)
	h := newHBStore(t, hbMixedLibrary(t, root, total)...)

	_, c, _ := runHB(t, h.store(), hbRun{opID: "01RECOMPUTEONCE"})

	assert.Equal(t, hbWant(total), c)
	assert.Empty(t, h.perRowRecomputes, "a run with a journal never uses the per-row recomputing store")
	require.Greater(t, h.writes["f0000"]+len(h.writes), 100, "the fixture must store many hashes across few books")
	assert.Equal(t, hbChunkedWant(total), h.recomputes,
		"each book is recomputed once per chunk that stored one of its hashes, however many it stored")

	dry := newHBStore(t, hbMixedLibrary(t, root, total)...)
	runHB(t, dry.store(), hbRun{dryRun: true, opID: "01RECOMPUTEDRY"})
	assert.Empty(t, dry.recomputes, "a dry run stores nothing, so it recomputes nothing")
}

// A book none of whose hashes was written (another writer got there first, or
// the row changed) is not recomputed: only a write can move an aggregate.
func TestBackfillFileHashes_DoesNotRecomputeABookThatWroteNothing(t *testing.T) {
	root := hbLibrary(t)
	h := newHBStore(t,
		database.BookFileCore{ID: "f1", BookID: "b1", FilePath: hbWrite(t, root, "Year One.m4b", "year one bytes")},
		database.BookFileCore{ID: "f2", BookID: "b2", FilePath: hbWrite(t, root, "Hush.m4b", "hush bytes")},
		database.BookFileCore{ID: "f3", BookID: "b3", FilePath: hbWrite(t, root, "Knightfall.m4b", "knightfall bytes")},
	)
	h.beforeModify = func(h *hbStore, id string) {
		switch id {
		case "f2": // another writer hashed it first
			h.edit(id, func(c *database.BookFileCore) { c.FileHash = "someone-elses-hash" })
		case "f3": // the row was repointed
			h.edit(id, func(c *database.BookFileCore) { c.FilePath = "/lib/Gotham/elsewhere.m4b" })
		}
	}

	_, c, _ := runHB(t, h.store(), hbRun{opID: "01WROTENOTHING"})

	assert.Equal(t, hbCounts{"hashed": 2, "busy": 1}, c)
	assert.Equal(t, map[string]int{"b1": 1}, h.recomputes, "only the book whose hash was written is recomputed")
}

// The real store: N hashes of one book, stored through the job, cost exactly one
// RecomputeBookAggregates for that book (the recomputing ModifyBookFile would cost
// N), and the hashes are stored.
func TestBackfillFileHashes_RealStoreRecomputesEachBookOnce(t *testing.T) {
	ps, err := database.NewPebbleStore(t.TempDir())
	require.NoError(t, err)
	ps.WaitForWarmup()
	t.Cleanup(func() { _ = ps.Close() })
	root := hbLibrary(t)

	books := map[string]*database.Book{}
	fileIDs := map[string][]string{}
	for _, title := range []string{"The Long Halloween", "Year One"} {
		b, berr := ps.CreateBook(&database.Book{Title: title, FilePath: filepath.Join(root, "Gotham", title)})
		require.NoError(t, berr)
		books[title] = b
		for i := 0; i < 6; i++ {
			name := fmt.Sprintf("%s %02d.m4b", title, i+1)
			path := hbWrite(t, root, name, fmt.Sprintf("%s bytes %d", title, i))
			f := &database.BookFile{BookID: b.ID, FilePath: path, Format: "m4b", Duration: 100, FileSize: int64(len(title) + 10)}
			require.NoError(t, ps.CreateBookFile(f))
			fileIDs[title] = append(fileIDs[title], f.ID)
		}
	}
	readLogs := aggtest.Capture(t)

	_, c, _ := runHB(t, ps, hbRun{opID: "01REALSTOREONCE"})

	assert.Equal(t, hbCounts{"hashed": 12}, c)
	for title, b := range books {
		assert.Equal(t, 1, aggtest.CountInvocations(readLogs(), b.ID),
			"%s: six stored hashes must cost one recompute of the book, not six", title)
		for _, id := range fileIDs[title] {
			got, gerr := ps.GetBookFileByID(b.ID, id)
			require.NoError(t, gerr)
			assert.NotEmpty(t, got.FileHash, "the hash must be stored")
		}
	}
}

// A book whose aggregate recompute fails persistently does not stop the run (D45).
// Its hashes are stored and its rows recorded, every other book is still
// recomputed, the failure is logged once per chunk (not once per book or row), the
// chunk journals are cleared as normal, and the result line names the count and
// the failed book id.
func TestBackfillFileHashes_APersistentlyFailingRecomputeDoesNotStopTheRun(t *testing.T) {
	const total, opID = 1300, "01RECOMPUTEFAILS"
	root := hbLibrary(t)
	h := newHBStore(t, hbMixedLibrary(t, root, total)...)
	h.recomputeErr = func(book string) error {
		if book == "b0" {
			return errors.New("pebble is on fire")
		}
		return nil
	}

	rep, c, report := runHB(t, h.store(), hbRun{opID: opID})

	assert.Equal(t, hbWant(total), c, "the run finishes with the totals of an uninterrupted one")
	assert.Len(t, hbDistinctIDs(t, hbReadReport(t, report)), total, "every row is recorded")
	for id, n := range h.writes {
		assert.Equal(t, 1, n, "hash for %s stored %d times: the failed book's hashes are kept, not redone", id, n)
	}
	want := hbChunkedWant(total)
	assert.Equal(t, want, h.recomputes, "every book, the failing one included, is attempted once per chunk that wrote it")
	assert.Greater(t, len(want), 1, "the fixture must have other books to recompute")
	_, hasJournal := h.state[opID+":params"]
	assert.False(t, hasJournal, "the chunk journal is cleared as normal")

	summary := rep.logs[len(rep.logs)-1]
	assert.Contains(t, summary, "WARNING: the aggregates of 1 books could not be recomputed")
	assert.Contains(t, summary, "books b0)")
	assert.NotContains(t, summary, "b1,", "only the failed book is named")
	n := 0
	for _, l := range rep.logs {
		if strings.Contains(l, "Could not recompute the aggregates of 1 books") {
			n++
		}
	}
	assert.Equal(t, want["b0"], n, "one error per chunk that hit the failing book")
}

// Several failing books are one logged error per chunk, all named in the result.
func TestBackfillFileHashes_FailedRecomputesAreOneLogLineAndAllNamedInTheResult(t *testing.T) {
	const total, opID = 20, "01SEVERALFAIL"
	root := hbLibrary(t)
	h := newHBStore(t, hbMixedLibrary(t, root, total)...) // one chunk; rows 0 and 10 hash, books b0 and b1
	h.recomputeErr = func(string) error { return errors.New("pebble is on fire") }

	rep, c, _ := runHB(t, h.store(), hbRun{opID: opID})

	assert.Equal(t, hbWant(total), c)
	assert.Contains(t, rep.logs[len(rep.logs)-1], "WARNING: the aggregates of 2 books could not be recomputed")
	assert.Contains(t, rep.logs[len(rep.logs)-1], "books b0,b1)")
	n := 0
	for _, l := range rep.logs {
		if strings.Contains(l, "Could not recompute the aggregates of 2 books") {
			n++
		}
	}
	assert.Equal(t, 1, n, "one aggregated line for the chunk, not one per book")
}

// A run killed after its hashes are stored but before the recompute leaves
// stale aggregates; the replay recomputes every book of the journal's hashed rows
// once, including rows it finds already stored.
func TestBackfillFileHashes_ReplayRecomputesTheBooksOfRowsAlreadyStored(t *testing.T) {
	const total, opID = 20, "01KILLBEFORERECOMPUTE"
	root := hbLibrary(t)
	h := newHBStore(t, hbMixedLibrary(t, root, total)...)
	errKill := errors.New("killed")
	restore := jobs.SetHashBackfillCommitHook(func(stage string, start int) error {
		if stage == "after-store" {
			return errKill
		}
		return nil
	})
	_, err := hbRunJob(t, h.store(), hbRun{opID: opID})
	restore()
	require.ErrorIs(t, err, errKill)
	require.Len(t, h.writes, 2, "the dead run stored the two hashes")
	require.Empty(t, h.recomputes, "and died before recomputing")

	_, c, _ := runHB(t, h.store(), hbRun{opID: opID})

	assert.Equal(t, hbWant(total), c)
	// hbMixedLibrary hashes rows 0 and 10: books b0 and b1.
	assert.Equal(t, map[string]int{"b0": 1, "b1": 1}, h.recomputes, "the replay recomputes each stored book once")
}

// The chunk journal holds only the hashes about to be stored. Not-found, read
// error and no-path rows store nothing, so they are not journaled: after a kill
// the resume decides them again and counts them once.
func TestBackfillFileHashes_JournalHoldsOnlyTheHashedOutcomes(t *testing.T) {
	const total, opID = 1300, "01JOURNALHASHEDONLY"
	root := hbLibrary(t)
	h := newHBStore(t, hbMixedLibrary(t, root, total)...)
	errKill := errors.New("killed")
	restore := jobs.SetHashBackfillCommitHook(func(stage string, start int) error {
		if stage == "after-journal" && start == jobs.HashBackfillChunkSize {
			return errKill
		}
		return nil
	})
	_, err := hbRunJob(t, h.store(), hbRun{opID: opID})
	restore()
	require.ErrorIs(t, err, errKill)

	var j struct {
		Entries []struct {
			Bucket string `json:"bucket"`
			FileID string `json:"file_id"`
		} `json:"entries"`
	}
	require.NoError(t, json.Unmarshal(h.state[opID+":params"], &j))
	var want []string
	for i := jobs.HashBackfillChunkSize; i < 2*jobs.HashBackfillChunkSize; i++ {
		if i%10 == 0 {
			want = append(want, fmt.Sprintf("f%04d", i))
		}
	}
	var got []string
	for _, e := range j.Entries {
		assert.Equal(t, "hashed", e.Bucket, "only hashed outcomes belong in the journal")
		got = append(got, e.FileID)
	}
	assert.Equal(t, want, got)

	_, c, report := runHB(t, h.store(), hbRun{opID: opID})
	assert.Equal(t, hbWant(total), c, "the rows left out of the journal are decided again and counted once")
	assert.Len(t, hbDistinctIDs(t, hbReadReport(t, report)), total)
}

// A chunk with no hash to store writes no journal at all.
func TestBackfillFileHashes_AChunkWithoutHashesWritesNoJournal(t *testing.T) {
	const opID = "01NOHASHESNOJOURNAL"
	root := hbLibrary(t)
	h := newHBStore(t,
		database.BookFileCore{ID: "f1", BookID: "b1", FilePath: hbPath(root, "Year One.m4b")},
		database.BookFileCore{ID: "f2", BookID: "b1", FilePath: hbPath(root, "Hush.m4b")},
		database.BookFileCore{ID: "f3", BookID: "b2", FilePath: "  "},
	)
	errKill := errors.New("killed")
	restore := jobs.SetHashBackfillCommitHook(func(stage string, start int) error {
		if stage == "after-journal" {
			return errKill
		}
		return nil
	})
	_, err := hbRunJob(t, h.store(), hbRun{opID: opID})
	restore()
	require.ErrorIs(t, err, errKill)

	_, hasJournal := h.state[opID+":params"]
	assert.False(t, hasJournal, "nothing was to be stored, so nothing was journaled")
}

// A share that drops mid-run makes every later path under it answer not-found.
// A chunk that found files absent under the root has the mount checked again
// before its rows are recorded; the run stops instead of listing a library of
// missing files, and the earlier chunk's rows stay.
func TestBackfillFileHashes_StopsWhenTheRootStopsLookingMounted(t *testing.T) {
	const total, opID = 1100, "01SHAREDROPPED"
	root := hbLibrary(t)
	h := newHBStore(t, hbMixedLibrary(t, root, total)...)
	restore := jobs.SetHashBackfillCommitHook(func(stage string, start int) error {
		if stage == "after-commit" && start == 0 {
			return os.RemoveAll(filepath.Join(root, "Gotham")) // the share drops after chunk 0
		}
		return nil
	})
	defer restore()

	_, err := hbRunJob(t, h.store(), hbRun{opID: opID})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "no longer looks mounted")
	assert.Contains(t, err.Error(), fmt.Sprintf("chunk at row %d", jobs.HashBackfillChunkSize))
	reportFile := filepath.Join(root, ".reports", "backfill-file-hashes-"+opID+".tsv")
	rows := hbReadReport(t, reportFile)
	assert.Len(t, rows, jobs.HashBackfillChunkSize, "only the chunk read before the share dropped is recorded")
	for _, r := range rows {
		assert.Less(t, r.fileID, fmt.Sprintf("f%04d", jobs.HashBackfillChunkSize), "no row of the chunk read after the drop")
	}
	assert.False(t, strings.Contains(err.Error(), "<nil>"))
}

// A repeated file ID does not exempt its line from validation: a duplicate row
// carrying a bucket the op never writes is a corrupt report, refused by line
// number, not accepted and kept.
func TestBackfillFileHashes_ResumeRefusesAGarbageBucketOnADuplicateFileID(t *testing.T) {
	root := hbLibrary(t)
	h := newHBStore(t, database.BookFileCore{ID: "f1", BookID: "b1", FilePath: hbPath(root, "Year One.m4b")})
	const opID = "01DUPGARBAGE"
	require.NoError(t, os.MkdirAll(filepath.Join(root, ".reports"), 0o775))
	report := "bucket\tfile_id\tbook_id\tpath\treason\n" +
		"not-found\tf1\tb1\t/lib/Gotham/Year One.m4b\tabsent\n" +
		"root-shell\tf1\tb1\t/lib/Gotham/Year One.m4b\tabsent\n"
	require.NoError(t, os.WriteFile(filepath.Join(root, ".reports", "backfill-file-hashes-"+opID+".tsv"), []byte(report), 0o664))

	_, err := hbRunJob(t, h.store(), hbRun{opID: opID})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "line 3")
	assert.Contains(t, err.Error(), "unknown bucket")
}

// A store that applied the write and then failed (the commit landed, its fsync
// did not) is recorded as a write error, but its hash IS stored, so its book is
// still recomputed: nothing would revisit it later, because every later run skips
// a row that has a hash.
func TestBackfillFileHashes_AStoreThatFailedAfterApplyingStillRecomputesItsBook(t *testing.T) {
	root := hbLibrary(t)
	h := newHBStore(t,
		database.BookFileCore{ID: "f1", BookID: "b1", FilePath: hbWrite(t, root, "Year One.m4b", "year one bytes")},
		database.BookFileCore{ID: "f2", BookID: "b2", FilePath: hbWrite(t, root, "Hush.m4b", "hush bytes")},
	)
	h.failAfterApply = func(id string) error {
		if id == "f1" {
			return errors.New("fsync failed after the commit")
		}
		return nil
	}

	_, c, report := runHB(t, h.store(), hbRun{opID: "01FSYNCFAILS"})

	assert.Equal(t, hbCounts{"hashed": 1, "write_errors": 1}, c)
	assert.Equal(t, map[string][]string{"write-error": {"f1"}, "hashed": {"f2"}}, hbBuckets(hbReadReport(t, report)))
	assert.Equal(t, 1, h.writes["f1"], "the write did land")
	assert.Equal(t, map[string]int{"b1": 1, "b2": 1}, h.recomputes, "the book of the failed-after-apply row is recomputed too")
}

// The same on the replay path: a run killed before it stored anything, resumed,
// replays the journal; a journaled hash whose store then fails after applying
// still gets its book recomputed.
func TestBackfillFileHashes_ReplayRecomputesTheBookOfAStoreThatFailedAfterApplying(t *testing.T) {
	const opID = "01REPLAYFSYNC"
	root := hbLibrary(t)
	files := []database.BookFileCore{
		{ID: "f1", BookID: "b1", FilePath: hbWrite(t, root, "Year One.m4b", "year one bytes")},
		{ID: "f2", BookID: "b2", FilePath: hbWrite(t, root, "Hush.m4b", "hush bytes")},
	}
	h := newHBStore(t, files...)
	errKill := errors.New("killed")
	restore := jobs.SetHashBackfillCommitHook(func(stage string, start int) error {
		if stage == "after-journal" {
			return errKill
		}
		return nil
	})
	_, err := hbRunJob(t, h.store(), hbRun{opID: opID})
	restore()
	require.ErrorIs(t, err, errKill)
	require.Empty(t, h.writes, "the dead run stored nothing")
	h.failAfterApply = func(id string) error {
		if id == "f1" {
			return errors.New("fsync failed after the commit")
		}
		return nil
	}

	_, c, _ := runHB(t, h.store(), hbRun{opID: opID})

	assert.Equal(t, hbCounts{"hashed": 1, "write_errors": 1}, c)
	assert.Equal(t, map[string]int{"b1": 1, "b2": 1}, h.recomputes, "the replay recomputes the book of the failed-after-apply row")
}

// With no journal there is nothing for a resume to repair a missed recompute
// from, and a later run skips every row that has a hash. So a run with no
// operation ID, or with no library root, stores each row through the recomputing
// ModifyBookFile, which recomputes the book as part of the write, and never takes
// the deferred path.
func TestBackfillFileHashes_WithoutAJournalEachRowIsStoredWithItsRecompute(t *testing.T) {
	for _, tc := range []struct {
		name   string
		opID   string
		noRoot bool
	}{
		{"no operation id", "", false},
		{"no library root", "01NOROOT", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := hbLibrary(t)
			h := newHBStore(t,
				database.BookFileCore{ID: "f1", BookID: "b1", FilePath: hbWrite(t, root, "Year One.m4b", "year one bytes")},
				database.BookFileCore{ID: "f2", BookID: "b1", FilePath: hbWrite(t, root, "Year Two.m4b", "year two bytes")},
				database.BookFileCore{ID: "f3", BookID: "b2", FilePath: hbWrite(t, root, "Hush.m4b", "hush bytes")},
			)
			if tc.noRoot {
				hbConfigRoot(t, "")
			}
			st := h.store()
			deferredCalls := 0
			deferred := st.ModifyBookFileDeferAggregatesFunc
			st.ModifyBookFileDeferAggregatesFunc = func(b, f string, fn func(*database.BookFile) error) (*database.BookFile, error) {
				deferredCalls++
				return deferred(b, f, fn)
			}

			_, c, _ := runHB(t, st, hbRun{opID: tc.opID})

			assert.Equal(t, hbCounts{"hashed": 3}, c)
			assert.Zero(t, deferredCalls, "no journal: the deferred store is not used")
			assert.Equal(t, map[string]int{"b1": 2, "b2": 1}, h.perRowRecomputes, "each stored row recomputed its book as part of the write")
			assert.Empty(t, h.recomputes, "no chunk-level recompute: it would be a second one")
			assert.Equal(t, map[string]int{"f1": 1, "f2": 1, "f3": 1}, h.writes)
		})
	}
}
