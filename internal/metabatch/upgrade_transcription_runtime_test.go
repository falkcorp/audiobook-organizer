// file: internal/metabatch/upgrade_transcription_runtime_test.go
// version: 1.0.0
// guid: 0c6a9e14-7d52-4b38-a1f6-93e2b5d80c47
// last-edited: 2026-10-29

package metabatch

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/applygate"
	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// The nightly upgrade lowers its score floor for a candidate the audio intro
// confirms (0.85 instead of 0.90). That confirmation now needs the candidate's
// runtime to fit the book's files, checked LIVE: a candidate cached or searched
// without any transcription stamp is refused all the same when the book is a
// fragment of a much longer one, and goes through when the runtime fits.
func TestUpgrade_TranscriptionConfirmationNeedsLiveRuntime(t *testing.T) {
	const tenHours = 36000
	tests := []struct {
		name    string
		candSec int
		want    bool
	}{
		{"runtime fits", tenHours, true},
		{"book is a fragment of a much longer candidate", 100000, false},
		{"candidate runtime unknown", 0, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := syntheticRankFixture()
			f.book.TranscribedTitle = new(syntheticTitle)
			f.store.GetBookFilesFunc = func(bookID string) ([]database.BookFile, error) {
				return []database.BookFile{{ID: "f1", BookID: bookID, FilePath: f.book.FilePath, FileSize: 288_000_000, Duration: tenHours}}, nil
			}
			c := syntheticCandidate("Audible", 0.87) // above the 0.85 audio floor, under 0.90
			c.DurationSec = tt.candSec
			svc, _ := f.service(c)
			out, err := svc.tryUpgradeBook(context.Background(), f.book.ID, "open_library")
			require.NoError(t, err)
			if tt.want {
				require.True(t, out.Upgraded, "control did not upgrade: %s", out.Reason)
				return
			}
			require.False(t, out.Upgraded)
			require.Contains(t, out.Reason, applygate.ReasonTranscriptionRuntimeUnverified)
			require.Nil(t, f.written(), "nothing is written")
		})
	}
}
