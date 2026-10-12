// file: internal/server/series_prune_golden_test.go
// version: 1.0.0
// guid: f0e3653a-d221-478a-89b2-2d7da58ed8af
// last-edited: 2026-10-11

package server

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// The series prune's per-group merge and per-orphan delete were extracted
// into internal/dedup (series_prune_core.go) so the Repairs fixer
// dedup.series-prune can apply one row at a time. That extraction is meant
// to change nothing: this golden was recorded from executeSeriesPrune and
// computeSeriesPrunePreview BEFORE the move and must stay byte-identical
// after it. Regenerate only for an intended behaviour change:
//
//	UPDATE_SERIES_PRUNE_GOLDEN=1 go test ./internal/server -run TestSeriesPrune_GoldenOutcome
const seriesPruneGoldenPath = "testdata/series_prune.golden"

// seriesPruneRecordingProgress keeps every progress line.
type seriesPruneRecordingProgress struct{ lines []string }

func (p *seriesPruneRecordingProgress) UpdateProgress(cur, total int, msg string) error {
	p.lines = append(p.lines, fmt.Sprintf("progress %d/%d %s", cur, total, msg))
	return nil
}
func (p *seriesPruneRecordingProgress) Log(level, message string, _ *string) error {
	p.lines = append(p.lines, level+" "+message)
	return nil
}
func (p *seriesPruneRecordingProgress) IsCanceled() bool { return false }

// seriesPruneGolden is the normalised outcome: no ULIDs, every list sorted
// (the op walks its duplicate groups in map order).
type seriesPruneGolden struct {
	Preview  json.RawMessage   `json:"preview"`
	Error    string            `json:"error"`
	Series   []string          `json:"series"`
	Books    map[string]string `json:"books"`
	Changes  []string          `json:"changes"`
	Progress []string          `json:"progress"`
}

// newSeriesPruneGoldenLibrary builds a synthetic library covering every
// branch of the prune: a clean merge (with a non-primary version), a merge
// whose merged-away series a trashed book still holds, a held twin, plain,
// trashed-only and held orphans, and an all-empty duplicate pair.
func newSeriesPruneGoldenLibrary(t *testing.T) *database.PebbleStore {
	t.Helper()
	st, err := database.NewPebbleStore(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	author := func(name string) *int {
		a, err := st.CreateAuthor(name)
		require.NoError(t, err)
		return &a.ID
	}
	// CreateSeries returns the existing row for a name already indexed, so a
	// duplicate is made the way production got them: create under another
	// name, then rename onto the first (UpdateSeriesName keeps the
	// collision).
	series := func(name string, authorID *int) int {
		s, err := st.CreateSeries("tmp "+name, authorID)
		require.NoError(t, err)
		require.NoError(t, st.UpdateSeriesName(s.ID, name))
		return s.ID
	}
	book := func(id, title string, seriesID int, trashed, nonPrimary bool) {
		sid := seriesID
		b := &database.Book{ID: id, Title: title, SeriesID: &sid, FilePath: "/srv/library/" + id + ".m4b", Format: "m4b"}
		if trashed {
			v := true
			b.MarkedForDeletion = &v
		}
		if nonPrimary {
			v := false
			b.IsPrimaryVersion = &v
		}
		_, err := st.CreateBook(b)
		require.NoError(t, err)
	}
	a, b := author("Author A"), author("Author B")

	s1 := series("Series One", a)
	s2 := series("series one", a)
	book("b01", "Book One", s1, false, false)
	book("b02", "Book Two", s1, false, false)
	book("b03", "Book Three", s2, false, false)
	book("b04", "Book Three (alternate)", s2, false, true)

	s3 := series("Series Two", b)
	s4 := series("series two ", b)
	book("b05", "Book Five", s3, false, false)
	book("b06", "Book Six", s4, true, false)

	s5 := series("Series Three", nil)
	s6 := series("series three", nil)
	book("b07", "Book Seven", s6, false, false)
	require.NoError(t, st.HoldSeries(s5, "maintenance.author-named-series"))

	series("Series Four", nil)
	s8 := series("Series Five", nil)
	book("b08", "Book Eight", s8, true, false)
	s9 := series("Series Six", nil)
	require.NoError(t, st.HoldSeries(s9, "maintenance.author-named-series"))

	series("Series Seven", b)
	series("SERIES SEVEN", b)
	return st
}

func seriesPruneGoldenSnapshot(t *testing.T, st *database.PebbleStore, preview *seriesPrunePreviewResult,
	runErr error, progress []string) seriesPruneGolden {
	t.Helper()
	sort.Slice(preview.Groups, func(i, j int) bool {
		if preview.Groups[i].Type != preview.Groups[j].Type {
			return preview.Groups[i].Type < preview.Groups[j].Type
		}
		return preview.Groups[i].CanonicalID < preview.Groups[j].CanonicalID
	})
	for i := range preview.Groups {
		sort.Ints(preview.Groups[i].MergeIDs)
	}
	pj, err := json.Marshal(preview)
	require.NoError(t, err)
	g := seriesPruneGolden{Preview: pj, Books: map[string]string{}}
	if runErr != nil {
		g.Error = runErr.Error()
	}
	all, err := st.GetAllSeries()
	require.NoError(t, err)
	for _, s := range all {
		g.Series = append(g.Series, fmt.Sprintf("%d %q held=%t", s.ID, s.Name, database.SeriesHeld(s)))
	}
	sort.Strings(g.Series)
	for _, id := range []string{"b01", "b02", "b03", "b04", "b05", "b06", "b07", "b08"} {
		bk, err := st.GetBookByID(id)
		require.NoError(t, err)
		require.NotNil(t, bk, id)
		g.Books[id] = "nil"
		if bk.SeriesID != nil {
			g.Books[id] = fmt.Sprint(*bk.SeriesID)
		}
	}
	changes, err := st.GetOperationChanges("op-golden")
	require.NoError(t, err)
	for _, c := range changes {
		g.Changes = append(g.Changes, fmt.Sprintf("%s|%s|%s|%s|%s", c.ChangeType, c.BookID, c.FieldName, c.OldValue, c.NewValue))
	}
	sort.Strings(g.Changes)
	g.Progress = append([]string(nil), progress...)
	sort.Strings(g.Progress)
	return g
}

func TestSeriesPrune_GoldenOutcome(t *testing.T) {
	st := newSeriesPruneGoldenLibrary(t)
	preview, err := computeSeriesPrunePreview(st)
	require.NoError(t, err)

	s := newSeriesPruneServer(t)
	prog := &seriesPruneRecordingProgress{}
	runErr := s.executeSeriesPrune(context.Background(), st, prog, "op-golden")

	got, err := json.MarshalIndent(seriesPruneGoldenSnapshot(t, st, preview, runErr, prog.lines), "", "  ")
	require.NoError(t, err)
	got = append(got, '\n')
	if os.Getenv("UPDATE_SERIES_PRUNE_GOLDEN") == "1" {
		require.NoError(t, os.WriteFile(seriesPruneGoldenPath, got, 0o644))
		return
	}
	want, err := os.ReadFile(seriesPruneGoldenPath)
	require.NoError(t, err, "golden missing; run with UPDATE_SERIES_PRUNE_GOLDEN=1 once, before changing the prune")
	require.Equal(t, string(want), string(got))
}
