// file: internal/itunes/backfill_resume_test.go
// version: 1.0.0
// guid: 46b22d21-5e8d-4eee-b965-7dd3679ed5f5
// last-edited: 2026-10-30

package itunes

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/walkresume"
)

// mappingCounts counts the mappings written per external ID. The mock appends
// without ignore-existing, so a book or track registered twice shows as 2.
func mappingCounts(m *MockBackfillStore) map[string]int {
	out := map[string]int{}
	for _, mm := range m.externalIDMaps {
		out[mm.ExternalID]++
	}
	return out
}

// A run that yields after its first book-pass checkpoint persists a book-ID
// cursor, and the resumed run starts at the next book: every book is mapped
// exactly once across the two legs, and only the finished leg sets the flag.
func TestBackfillExternalIDs_BookPassYieldsAndResumesAfterCursor(t *testing.T) {
	setITunesXMLPath(t, "")
	m := NewMockBackfillStore()
	const books = 3*backfillChunkSize + 500
	for i := 0; i < books; i++ {
		id := fmt.Sprintf("b%06d", i)
		pid := fmt.Sprintf("pid-%06d", i)
		m.books[id] = database.Book{ID: id, Title: id, ITunesPersistentID: &pid}
	}

	var states []walkresume.State
	first := walkresume.Run{
		Checkpoint: func(st walkresume.State) { states = append(states, st) },
		YieldDue:   func() bool { return true },
	}
	_, err := BackfillExternalIDsOnce(context.Background(), m, nil, walkresume.WithRun(first))
	require.ErrorIs(t, err, walkresume.ErrYield)
	require.Len(t, states, 1, "the yield stops the walk at the first checkpoint")
	require.Equal(t, phaseBooks, states[0].Phase)
	require.Equal(t, fmt.Sprintf("b%06d", backfillChunkSize-1), states[0].Cursor, "the cursor is the last book of the finished chunk")
	require.Equal(t, backfillChunkSize, states[0].Mappings)
	require.Len(t, m.externalIDMaps, backfillChunkSize)
	_, flagged := m.settings[ExternalIDBackfillDoneKey]
	require.False(t, flagged, "a yielded run must not set the done flag")

	var resumed []walkresume.State
	second := walkresume.Run{Resume: states[0], Checkpoint: func(st walkresume.State) { resumed = append(resumed, st) }}
	skipped, err := BackfillExternalIDsOnce(context.Background(), m, nil, walkresume.WithRun(second))
	require.NoError(t, err)
	require.False(t, skipped)

	counts := mappingCounts(m)
	require.Len(t, counts, books)
	for pid, n := range counts {
		require.Equal(t, 1, n, "%s was mapped %d times: the resumed leg reprocessed earlier books", pid, n)
	}
	require.Equal(t, "books_only", m.settings[ExternalIDBackfillDoneKey])
	require.Equal(t, phaseXML, resumed[len(resumed)-1].Phase, "the finished book pass hands a restart to the XML phase")
}

func syntheticAlbumsXML(albums int) string {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple Computer//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict><key>Tracks</key><dict>
`)
	for a := 0; a < albums; a++ {
		for tr := 0; tr < 2; tr++ {
			n := a*2 + tr
			fmt.Fprintf(&b, "<key>%d</key><dict><key>Track ID</key><integer>%d</integer><key>Persistent ID</key><string>T%07d</string><key>Name</key><string>Part %d</string><key>Album</key><string>Album %05d</string><key>Track Number</key><integer>%d</integer></dict>\n", n, n, n, tr+1, a, tr+1)
		}
	}
	b.WriteString("</dict></dict></plist>\n")
	return b.String()
}

// The XML pass checkpoints a track index at an album boundary with the batch
// flushed; a yield there and a resume register every track exactly once.
func TestBackfillITunesTrackPIDs_YieldsAtAlbumBoundaryAndResumesAtIndex(t *testing.T) {
	const albums = 6000 // 12,000 tracks: one checkpoint (at track 10,000), then the tail
	xmlPath := filepath.Join(t.TempDir(), "library.xml")
	require.NoError(t, os.WriteFile(xmlPath, []byte(syntheticAlbumsXML(albums)), 0o600))
	setITunesXMLPath(t, xmlPath)

	m := NewMockBackfillStore()
	for a := 0; a < albums; a++ {
		title := fmt.Sprintf("Album %05d", a)
		id := fmt.Sprintf("b%05d", a)
		m.books[id] = database.Book{ID: id, Title: title}
	}

	var states []walkresume.State
	first := walkresume.Run{
		Checkpoint: func(st walkresume.State) { states = append(states, st) },
		YieldDue: func() bool {
			return len(states) > 0 && states[len(states)-1].Phase == phaseXML && states[len(states)-1].Index > 0
		},
	}
	err := BackfillExternalIDs(context.Background(), m, nil, walkresume.WithRun(first))
	require.True(t, errors.Is(err, walkresume.ErrYield), "err = %v", err)
	last := states[len(states)-1]
	require.Equal(t, phaseXML, last.Phase)
	require.Equal(t, 10000, last.Index, "an album boundary at or after the first 10,000 tracks")
	require.Len(t, m.externalIDMaps, 10000, "the batch is flushed before the checkpoint: everything before the index is written")
	_, flagged := m.settings[ExternalIDBackfillDoneKey]
	require.False(t, flagged)

	require.NoError(t, BackfillExternalIDs(context.Background(), m, nil, walkresume.WithRun(walkresume.Run{Resume: last})))
	counts := mappingCounts(m)
	require.Len(t, counts, albums*2)
	for pid, n := range counts {
		require.Equal(t, 1, n, "%s registered %d times", pid, n)
	}
	require.Equal(t, "true", m.settings[ExternalIDBackfillDoneKey])
}
