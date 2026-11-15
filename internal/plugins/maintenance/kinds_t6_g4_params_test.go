// file: internal/plugins/maintenance/kinds_t6_g4_params_test.go
// version: 1.0.0
// guid: 55548022-1158-4861-888f-6ff5f548c53e
// last-edited: 2026-11-15

package maintenance

import (
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/tasks"
	"github.com/stretchr/testify/require"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
)

// runNativeDurationBackfill runs the native duration-backfill kind over s with the
// start-request mode and the params as sent.
func runNativeDurationBackfill(t *testing.T, s *database.PebbleStore, mode tasks.Mode, params string) (durationBackfillResult, error) {
	t.Helper()
	p := New(fakeDeps{store: s})
	deps := t6g4RealDeps(p, &t6g4Calls{}, new([]tasks.Writer))
	_, rc := newT6G4RC(durationBackfillKind, mode, &t6g4RunWriter{name: "native"})
	raw, err := runT6G4Kind(t, p.durationBackfillKind(deps, nil), rc, params)
	var out durationBackfillResult
	if len(raw) > 0 {
		require.NoError(t, json.Unmarshal(raw, &out))
	}
	return out, err
}

// HEAD refuses skipAgeDays in unknownRows mode by the key's presence in the raw
// params, so a null value is refused as well as a number, zero included. The
// native shell once typed the field as *int, which decodes null to nil: a
// request HEAD refuses ran and examined rows.
func TestDurationBackfillNative_UnknownRowsRefusesSkipAgeDaysWhateverItsValue(t *testing.T) {
	stubFFprobe(t, "echo 30.0")
	for _, v := range []string{"null", "30", "0"} {
		t.Run(v, func(t *testing.T) {
			s := newRepairPebble(t)
			dir := t.TempDir()
			book := urBook(t, s, "Batman: Year One", dir)
			row := urFile(t, s, book.ID, filepath.Join(dir, "01.mp3"), nil)

			_, err := runNativeDurationBackfill(t, s, tasks.ModeLive, `{"mode":"unknownRows","skipAgeDays":`+v+`}`)
			require.ErrorContains(t, err, "skipAgeDays has no effect")
			require.Nil(t, urRow(t, s, book.ID, row.ID).Duration, "a refused run measures nothing")
		})
	}
}

// The native params map onto HEAD's: skipAgeDays sets the age window of the
// book-grain run, an absent or null value keeps HEAD's 90-day default (HEAD
// decodes null into an int as a no-op), and a non-number is refused rather than
// silently replaced by the default.
func TestDurationBackfillNative_SkipAgeDaysMapsOntoTheBookGrainRun(t *testing.T) {
	verified := time.Now().AddDate(0, 0, -10)
	for _, tc := range []struct {
		name, params string
		wantRecent   int
	}{
		{"absent keeps the 90 day default", `{}`, 1},
		{"null keeps the default", `{"skipAgeDays":null}`, 1},
		{"a window shorter than the stamp's age re-examines the book", `{"skipAgeDays":5}`, 0},
		{"zero disables age skipping", `{"skipAgeDays":0}`, 0},
		{"a window longer than the stamp's age skips the book", `{"skipAgeDays":30}`, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newRepairPebble(t)
			b, err := s.CreateBook(&database.Book{Title: "Batman: The Long Halloween", FilePath: t.TempDir(), Duration: new(100), DurationVerifiedAt: &verified})
			require.NoError(t, err)
			require.NotNil(t, b.DurationVerifiedAt, "the fixture's stamp must survive CreateBook")

			out, err := runNativeDurationBackfill(t, s, tasks.ModePreview, tc.params)
			require.NoError(t, err)
			require.Equal(t, tc.wantRecent, out.RecentlyVerified)
		})
	}

	s := newRepairPebble(t)
	_, err := runNativeDurationBackfill(t, s, tasks.ModePreview, `{"skipAgeDays":"soon"}`)
	require.Error(t, err, "a non-number skipAgeDays is refused")
}
