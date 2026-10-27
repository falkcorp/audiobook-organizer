// file: internal/maintenance/jobs/backfill_file_hashes_yield_test.go
// version: 1.0.0
// guid: 6a4d1e93-0b7c-4f25-8e38-9d2b5c7a1f40
// last-edited: 2026-10-26

package jobs_test

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/maintenance"
	"github.com/falkcorp/audiobook-organizer/internal/maintenance/jobs"
	opsregistry "github.com/falkcorp/audiobook-organizer/internal/operations/registry"
)

// hbNormalized is a report with the library root replaced, so two runs over two
// temp libraries compare equal when they did the same thing.
func hbNormalized(rows []hbReportRow, root string) []hbReportRow {
	out := make([]hbReportRow, len(rows))
	for i, r := range rows {
		r.path = strings.ReplaceAll(r.path, root, "<root>")
		r.reason = strings.ReplaceAll(r.reason, root, "<root>")
		out[i] = r
	}
	return out
}

// hbWantCounts is what an uninterrupted run over hbMixedLibrary(n) reports.
func hbWantCounts(n int) hbCounts {
	want := hbCounts{}
	for i := 0; i < n; i++ {
		switch {
		case i%10 == 0:
			want["hashed"]++
		case i%7 == 0:
			want["skipped_no_path"]++
		default:
			want["not_found"]++
		}
	}
	return want
}

// The hashing pool is an optimisation, not a behaviour: one worker and eight
// produce the same counts, the same report IN THE SAME ORDER, the same stored
// hashes (each written once) and the same per-book recomputes, in apply and in
// dry run, across three chunks. Run under -race this is also what exercises the
// pool's shared state (the report's done set, the reporter, the not-found log
// counter) with real contention.
func TestBackfillFileHashes_WorkerCountDoesNotChangeTheResult(t *testing.T) {
	const total = 1300 // chunks: [0,512) [512,1024) [1024,1300)
	type result struct {
		counts     hbCounts
		rows       []hbReportRow
		hashes     map[string]string
		writes     map[string]int
		recomputes map[string]int
	}
	run := func(t *testing.T, workers string, dryRun bool) result {
		t.Setenv("ABK_HASH_BACKFILL_WORKERS", workers)
		root := hbLibrary(t)
		h := newHBStore(t, hbMixedLibrary(t, root, total)...)
		_, c, report := runHB(t, h.store(), hbRun{opID: "01WORKERS" + workers, dryRun: dryRun})
		rows := hbNormalized(hbReadReport(t, report), root)
		h.mu.Lock()
		defer h.mu.Unlock()
		hashes := map[string]string{}
		for id, v := range h.hashCalls {
			hashes[id] = v
		}
		writes := map[string]int{}
		for id, v := range h.writes {
			writes[id] = v
		}
		recomputes := map[string]int{}
		for id, v := range h.recomputes {
			recomputes[id] = v
		}
		return result{counts: c, rows: rows, hashes: hashes, writes: writes, recomputes: recomputes}
	}
	for _, dryRun := range []bool{false, true} {
		t.Run(fmt.Sprintf("dry_run=%v", dryRun), func(t *testing.T) {
			seq := run(t, "1", dryRun)
			par := run(t, "8", dryRun)

			assert.Equal(t, hbWantCounts(total), seq.counts, "the sequential baseline is the shape of the rows")
			assert.Equal(t, seq.counts, par.counts)
			assert.Equal(t, seq.rows, par.rows, "same report, same order")
			assert.Len(t, par.rows, total)
			assert.Equal(t, seq.hashes, par.hashes)
			assert.Equal(t, seq.writes, par.writes)
			assert.Equal(t, seq.recomputes, par.recomputes)
			if dryRun {
				assert.Empty(t, par.hashes, "a dry run stores nothing")
			} else {
				assert.Len(t, par.hashes, hbWantCounts(total)["hashed"])
				for id, n := range par.writes {
					assert.Equal(t, 1, n, "hash for %s stored %d times", id, n)
				}
			}
		})
	}
}

// yieldCtx is a run context with the operation ID and a deadline `in` from now.
func yieldCtx(t *testing.T, opID string, in time.Duration) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(maintenance.WithOperationID(context.Background(), opID), in)
	t.Cleanup(cancel)
	return ctx
}

func hbRunCtx(t *testing.T, ctx context.Context, store maintenance.JobStore, dryRun bool) (*noopReporter, error) {
	t.Helper()
	j, err := maintenance.Get("backfill-file-hashes")
	require.NoError(t, err)
	rep := &noopReporter{}
	return rep, j.Run(ctx, store, rep, dryRun)
}

// A run that is near its deadline stops BETWEEN chunks, restarts under the same
// operation id as often as it takes, and ends with exactly the report and stored
// hashes an uninterrupted run has: each attempt's report is a whole number of
// chunks and a contiguous prefix of the rows, no row is skipped or counted twice,
// each hash is stored once. Run in apply and in dry run (where the report is the
// only progress record).
func TestBackfillFileHashes_YieldsBeforeTheDeadlineAndResumesExactly(t *testing.T) {
	const total = 1300 // chunks: [0,512) [512,1024) [1024,1300)
	for _, dryRun := range []bool{false, true} {
		t.Run(fmt.Sprintf("dry_run=%v", dryRun), func(t *testing.T) {
			// The margin exceeds the whole deadline, so every attempt that has made
			// progress yields at its first chunk boundary.
			defer jobs.SetHashBackfillYieldMargin(time.Hour)()
			root := hbLibrary(t)
			h := newHBStore(t, hbMixedLibrary(t, root, total)...)
			const opID = "01YIELDRESUME"
			reportFile := filepath.Join(root, ".reports", "backfill-file-hashes-"+opID+".tsv")

			var onDisk []int // report rows after each attempt
			var lastRep *noopReporter
			attempts := 0
			for {
				attempts++
				require.LessOrEqual(t, attempts, 5, "the run must finish: a yield without progress would loop")
				rep, err := hbRunCtx(t, yieldCtx(t, opID, 30*time.Minute), h.store(), dryRun)
				lastRep = rep
				if err == nil {
					break
				}
				require.ErrorIs(t, err, maintenance.ErrContinueInPlace)
				require.ErrorIs(t, err, opsregistry.ErrContinueInPlace, "the job's sentinel IS the registry's")
				rows := hbReadReport(t, reportFile)
				onDisk = append(onDisk, len(rows))
				assert.Zero(t, len(rows)%jobs.HashBackfillChunkSize, "a pause leaves whole chunks on disk")
				for i, r := range rows { // a contiguous prefix: nothing unfinished was claimed, nothing skipped
					require.Equal(t, fmt.Sprintf("f%04d", i), r.fileID, "report row %d", i)
				}
				assert.NotNil(t, h.state[opID], "the started marker survives a pause: this operation is not finished")
				for _, l := range rep.logs {
					assert.NotContains(t, l, "hashed=", "a paused attempt must not write the run's summary")
				}
			}

			assert.Equal(t, 3, attempts, "three chunks, one per attempt")
			assert.Equal(t, []int{512, 1024}, onDisk)
			c, report := hbSummary(t, lastRep)
			assert.Equal(t, hbWantCounts(total), c, "the last attempt's summary counts the whole operation")
			assert.NotContains(t, lastRep.logs[len(lastRep.logs)-1], "counts cover only")
			rows := hbReadReport(t, report)
			assert.Len(t, rows, total)
			assert.Len(t, hbDistinctIDs(t, rows), total)
			for i, r := range rows {
				require.Equal(t, fmt.Sprintf("f%04d", i), r.fileID, "report row %d", i)
			}
			if dryRun {
				assert.Empty(t, h.hashed())
			} else {
				assert.Len(t, h.hashed(), hbWantCounts(total)["hashed"])
				for id, n := range h.writes {
					assert.Equal(t, 1, n, "hash for %s stored %d times", id, n)
				}
			}
		})
	}
}

// An attempt that has made no progress does not yield, however close the
// deadline: a restart that did nothing would be restarted forever. Here the first
// chunk is already hashed (nothing to count), so the pause comes after the second.
func TestBackfillFileHashes_NoYieldWithoutProgress(t *testing.T) {
	defer jobs.SetHashBackfillYieldMargin(time.Hour)()
	root := hbLibrary(t)
	files := hbMixedLibrary(t, root, 1300)
	for i := range files[:jobs.HashBackfillChunkSize] {
		files[i].FileHash = "have" // chunk 1: every row already hashed, so nothing is counted
	}
	h := newHBStore(t, files...)
	const opID = "01YIELDNOPROGRESS"

	_, err := hbRunCtx(t, yieldCtx(t, opID, 30*time.Minute), h.store(), true)

	require.ErrorIs(t, err, maintenance.ErrContinueInPlace)
	rows := hbReadReport(t, filepath.Join(root, ".reports", "backfill-file-hashes-"+opID+".tsv"))
	require.Len(t, rows, jobs.HashBackfillChunkSize, "chunk 1 counted nothing and did not pause; chunk 2 did the work and paused")
	assert.Equal(t, fmt.Sprintf("f%04d", jobs.HashBackfillChunkSize), rows[0].fileID)
}

// A run that has no deadline, or no operation id to continue under, or is on its
// last chunk, never yields: it just runs.
func TestBackfillFileHashes_NoYieldWhenItCannotContinue(t *testing.T) {
	defer jobs.SetHashBackfillYieldMargin(time.Hour)()
	for _, tc := range []struct {
		name string
		ctx  func(t *testing.T) context.Context
		rows int
	}{
		{"no deadline", func(*testing.T) context.Context {
			return maintenance.WithOperationID(context.Background(), "01YIELDNODEADLINE")
		}, 1300},
		{"no operation id", func(t *testing.T) context.Context {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
			t.Cleanup(cancel)
			return ctx
		}, 1300},
		{"last chunk", func(t *testing.T) context.Context { return yieldCtx(t, "01YIELDLAST", 30*time.Minute) }, 500},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := hbLibrary(t)
			h := newHBStore(t, hbMixedLibrary(t, root, tc.rows)...)

			rep, err := hbRunCtx(t, tc.ctx(t), h.store(), true)

			require.NoError(t, err)
			c, _ := hbSummary(t, rep)
			assert.Equal(t, hbWantCounts(tc.rows), c)
		})
	}
}

// The margin grows with the slowest chunk: a chunk that takes a while must still
// finish inside the deadline, so the pause comes earlier the slower chunks are.
// Here the margin floor is 1ms but a chunk takes 150ms, so with 400ms to the
// deadline the run pauses after the first chunk.
func TestBackfillFileHashes_YieldMarginScalesWithTheSlowestChunk(t *testing.T) {
	defer jobs.SetHashBackfillYieldMargin(time.Millisecond)()
	defer jobs.SetHashBackfillCommitHook(func(stage string, _ int) error {
		if stage == "after-commit" {
			time.Sleep(150 * time.Millisecond)
		}
		return nil
	})()
	root := hbLibrary(t)
	h := newHBStore(t, hbMixedLibrary(t, root, 1300)...)
	const opID = "01YIELDSCALES"

	_, err := hbRunCtx(t, yieldCtx(t, opID, 400*time.Millisecond), h.store(), true)

	require.ErrorIs(t, err, maintenance.ErrContinueInPlace, "a run that waited for the deadline would have been cancelled mid-chunk")
	assert.False(t, errors.Is(err, context.DeadlineExceeded))
	rows := hbReadReport(t, filepath.Join(root, ".reports", "backfill-file-hashes-"+opID+".tsv"))
	assert.Len(t, rows, jobs.HashBackfillChunkSize)
}

// The registry only honours a yield for a def that resumes in place, so the job
// must declare it.
func TestBackfillFileHashes_PolicyResumesInPlace(t *testing.T) {
	j, err := maintenance.Get("backfill-file-hashes")
	require.NoError(t, err)
	assert.Equal(t, opsregistry.ResumeRestart, j.Policy().ResumePolicy)
	assert.Equal(t, opsregistry.LivenessManual, j.Policy().Liveness, "the watchdog still kills a run that stops reporting")
	assert.Equal(t, 4*time.Hour, j.Policy().Timeout, "the per-attempt cap that kills a stuck run is unchanged")
}
