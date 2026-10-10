// file: internal/plugins/maintenance/version_twin_owner_rejected_test.go
// version: 1.0.0
// guid: 5a7d2c18-9e4b-4f03-b6a1-2c8e0d9f4a67
// last-edited: 2026-10-10

package maintenance

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

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
