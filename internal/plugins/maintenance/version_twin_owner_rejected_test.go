// file: internal/plugins/maintenance/version_twin_owner_rejected_test.go
// version: 1.0.0
// guid: 5a7d2c18-9e4b-4f03-b6a1-2c8e0d9f4a67
// last-edited: 2026-10-10

package maintenance

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/audiobooks"
	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/metafetch"
	"github.com/falkcorp/audiobook-organizer/internal/repairs"
)

// The version-twin fixer applies a twin's record onto the primary with
// nobody picking it for the primary. A record the owner rejected for the
// primary is never applied: the row is held at trial, and a plan made
// before the rejection is refused at the write (regression, 2026-10-10).
func TestVersionTwinFixer_OwnerRejectedRecordHolds(t *testing.T) {
	l := newVTLib(t)
	l.book("p", "g-rej", true, nil)
	cand := vtSagaASIN("B0SYNTH021")
	l.appliedTwin("t", "g-rej", cand, nil)

	// Control: with no rejection the row is applicable, so the hold below is
	// the rejection's doing.
	_, rows := l.plan()
	require.Empty(t, rows["g-rej"].Skipped, rows["g-rej"].SkipReason)
	fresh := l.vtFresh("g-rej")

	require.NoError(t, metafetch.RecordRejection(l.st, l.ids["p"], &cand))

	_, rows = l.plan()
	require.Equal(t, vtHoldOwnerRejected, rows["g-rej"].Skipped, rows["g-rej"].SkipReason)

	err := l.fixer.Apply(context.Background(), l.writer(), fresh)
	require.Error(t, err)
	require.True(t, errors.Is(err, repairs.ErrChangedSincePlan), err.Error())
	require.Nil(t, l.get("p").MetadataReviewStatus, "the owner-rejected record was applied")
}

// A rejection of another record of the same title (another ASIN) does not
// hold the row.
func TestVersionTwinFixer_OtherRecordRejectedDoesNotHold(t *testing.T) {
	l := newVTLib(t)
	l.book("p", "g-rej2", true, nil)
	cand := vtSagaASIN("B0SYNTH022")
	l.appliedTwin("t", "g-rej2", cand, nil)
	other := vtSagaASIN("B0SYNTH023")
	require.NoError(t, metafetch.RecordRejection(l.st, l.ids["p"], &other))

	_, rows := l.plan()
	require.Empty(t, rows["g-rej2"].Skipped, rows["g-rej2"].SkipReason)
}

// vtBatCand is a Batman storyline record for the candidate-copy re-rank tests.
func vtBatCand(title, asin string, score float64) metafetch.MetadataCandidate {
	return metafetch.MetadataCandidate{Title: title, Author: "Synthetic Author A", ASIN: asin, Source: "audible", Score: score}
}

// vtCandTwinGroup builds a primary and a candidate-holding twin; the twin's
// cached row lists cands in order.
func vtCandTwinGroup(t *testing.T, l *vtLib, group string, cands []metafetch.MetadataCandidate) {
	t.Helper()
	l.book("p", group, true, nil)
	tid := l.book("t", group, false, nil)
	l.putCache(tid, cands, metafetch.BatchSourceHash(tid, "Synthetic Saga", "Synthetic Author A"))
}

func vtCachedASINs(t *testing.T, l *vtLib, key string) []string {
	t.Helper()
	entry, err := l.st.GetMetadataCache(l.ids[key])
	require.NoError(t, err)
	require.NotNil(t, entry)
	var out []string
	for _, raw := range entry.Candidates {
		var c metafetch.MetadataCandidate
		require.NoError(t, json.Unmarshal(raw, &c))
		out = append(out, c.ASIN)
	}
	return out
}

// A candidate the owner rejected for the primary is not left first in the row
// copied from the twin: the copy is re-ranked by the primary's own
// rejections, re-ordering only (VT-COPY-RERANK).
func TestVersionTwinFixer_CopyCandidatesReranksRejectedLast(t *testing.T) {
	l := newVTLib(t)
	rejected := vtBatCand("Batman: Hush", "B0BATHUSH1", 0.99)
	kept := vtBatCand("Batman: The Long Halloween", "B0BATLONG1", 0.90)
	vtCandTwinGroup(t, l, "g-rr", []metafetch.MetadataCandidate{rejected, kept})
	require.NoError(t, metafetch.RecordRejection(l.st, l.ids["p"], &rejected))

	plan, rows := l.plan()
	require.True(t, rows["g-rr"].Applicable(), "skipped %s: %s", rows["g-rr"].Skipped, rows["g-rr"].SkipReason)
	res := l.apply(plan, false, "g-rr")
	require.Equal(t, 1, res.Applied, "outcomes %v rows %+v", res.ByOutcome, res.Rows)

	require.Equal(t, []string{"B0BATLONG1", "B0BATHUSH1"}, vtCachedASINs(t, l, "p"),
		"the rejected candidate is last, none dropped")
}

// With nothing rejected for the primary the copy keeps the twin's order.
func TestVersionTwinFixer_CopyCandidatesNoRejectionKeepsOrder(t *testing.T) {
	l := newVTLib(t)
	a := vtBatCand("Batman: Year One", "B0BATYEAR1", 0.99)
	b := vtBatCand("Batman: The Killing Joke", "B0BATJOKE1", 0.90)
	vtCandTwinGroup(t, l, "g-rr2", []metafetch.MetadataCandidate{a, b})

	plan, _ := l.plan()
	res := l.apply(plan, false, "g-rr2")
	require.Equal(t, 1, res.Applied, "outcomes %v rows %+v", res.ByOutcome, res.Rows)
	require.Equal(t, []string{"B0BATYEAR1", "B0BATJOKE1"}, vtCachedASINs(t, l, "p"))
}

// The journaled stamp does not depend on candidate order, so the op revert
// after a re-ranked copy still restores the prior (absent) cache row.
func TestVersionTwinFixer_RevertAfterRerankedCopy(t *testing.T) {
	l := newVTLib(t)
	rejected := vtBatCand("Batman: Hush", "B0BATHUSH2", 0.99)
	kept := vtBatCand("Batman: Knightfall", "B0BATKNIG1", 0.90)
	vtCandTwinGroup(t, l, "g-rr3", []metafetch.MetadataCandidate{rejected, kept})
	require.NoError(t, metafetch.RecordRejection(l.st, l.ids["p"], &rejected))

	plan, _ := l.plan()
	res := l.apply(plan, false, "g-rr3")
	require.Equal(t, 1, res.Applied, "outcomes %v rows %+v", res.ByOutcome, res.Rows)
	require.Equal(t, []string{"B0BATKNIG1", "B0BATHUSH2"}, vtCachedASINs(t, l, "p"))

	rev, err := audiobooks.NewRevertService(l.st).RevertOperation(vtTestOpID)
	require.NoError(t, err)
	require.Zero(t, rev.Failed, "%+v", rev)
	entry, err := l.st.GetMetadataCache(l.ids["p"])
	require.NoError(t, err)
	require.True(t, entry == nil || len(entry.Candidates) == 0, "the copied row is gone after the revert")
}

// vtRejectScanFailStore fails the rejection scan once armed.
type vtRejectScanFailStore struct {
	*database.PebbleStore
	armed atomic.Bool
}

func (s *vtRejectScanFailStore) ScanPrefix(prefix string) ([]database.KVPair, error) {
	if s.armed.Load() && strings.HasPrefix(prefix, metafetch.RejectedCandidatePrefix) {
		return nil, errors.New("rejection scan unavailable")
	}
	return s.PebbleStore.ScanPrefix(prefix)
}

// A failed rejection read is an error, never a ranking as if nothing were
// rejected: the copy (already written) is journaled so it stays revertible,
// and the row is reported partially applied.
func TestVersionTwinFixer_CopyCandidatesRejectionReadFailure(t *testing.T) {
	l := newVTLib(t)
	fs := &vtRejectScanFailStore{PebbleStore: l.st}
	l.p.deps = fakeDeps{store: fs}
	vtCandTwinGroup(t, l, "g-rr4", []metafetch.MetadataCandidate{
		vtBatCand("Batman: Hush", "B0BATHUSH3", 0.99), vtBatCand("Batman: Zero Year", "B0BATZERO1", 0.90)})

	plan, _ := l.plan()
	fs.armed.Store(true)
	res := l.apply(plan, false, "g-rr4")
	require.Equal(t, 0, res.Applied, "outcomes %v rows %+v", res.ByOutcome, res.Rows)
	require.Len(t, res.Rows, 1)
	require.Contains(t, res.Rows[0].Error, repairs.ErrPartiallyApplied.Error())
	changes, err := l.st.GetOperationChanges(vtTestOpID)
	require.NoError(t, err)
	require.Len(t, changes, 1, "the written copy is journaled so the op revert reaches it")
}
