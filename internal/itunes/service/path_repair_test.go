// file: internal/itunes/service/path_repair_test.go
// version: 1.2.0
// guid: 6b7e3d51-c0a3-4ab2-8d6c-7e9c1d4a8f01

package itunesservice

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	dbmocks "github.com/falkcorp/audiobook-organizer/internal/database/mocks"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// withITunesPathMapping installs a mapping covering dir → "Z:/" for
// ComputeITunesPath, restoring the previous mappings on cleanup.
func withITunesPathMapping(t *testing.T, dir string) {
	t.Helper()
	prev := config.AppConfig.ITunes.PathMappings
	config.AppConfig.ITunes.PathMappings = []config.ITunesPathMap{
		{From: "Z:/", To: dir + "/"},
	}
	t.Cleanup(func() { config.AppConfig.ITunes.PathMappings = prev })
}

// noopProgressRepair mirrors the reconciler test helper.
type noopProgressRepair struct{}

func (noopProgressRepair) UpdateProgress(_, _ int, _ string) error { return nil }
func (noopProgressRepair) Log(_, _ string, _ *string) error        { return nil }
func (noopProgressRepair) IsCanceled() bool                        { return false }

// writeFixtureXMLN writes a minimal iTunes XML with N audiobook
// tracks. Each entry is (pid, name, location).
func writeFixtureXMLN(t *testing.T, dir string, tracks []struct{ PID, Name, Location string }) string {
	t.Helper()
	var sb strings.Builder
	sb.WriteString(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple Computer//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Major Version</key><integer>1</integer>
	<key>Minor Version</key><integer>1</integer>
	<key>Tracks</key>
	<dict>
`)
	for i, tr := range tracks {
		fmt.Fprintf(&sb, `		<key>%d</key>
		<dict>
			<key>Track ID</key><integer>%d</integer>
			<key>Persistent ID</key><string>%s</string>
			<key>Name</key><string>%s</string>
			<key>Kind</key><string>Audiobook</string>
			<key>Location</key><string>file://localhost%s</string>
		</dict>
`, i+1, i+1, tr.PID, tr.Name, tr.Location)
	}
	sb.WriteString(`	</dict>
	<key>Playlists</key><array/>
</dict>
</plist>
`)
	p := filepath.Join(dir, "iTunes Library.xml")
	require.NoError(t, os.WriteFile(p, []byte(sb.String()), 0o644))
	return p
}

// writeFixtureXML writes a minimal iTunes XML with two audiobook
// tracks at the given locations and returns the file path.
func writeFixtureXML(t *testing.T, dir, locA, locB string) string {
	t.Helper()
	xml := `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple Computer//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Major Version</key><integer>1</integer>
	<key>Minor Version</key><integer>1</integer>
	<key>Tracks</key>
	<dict>
		<key>1</key>
		<dict>
			<key>Track ID</key><integer>1</integer>
			<key>Persistent ID</key><string>PID_A</string>
			<key>Name</key><string>Track A</string>
			<key>Kind</key><string>Audiobook</string>
			<key>Location</key><string>file://localhost` + locA + `</string>
		</dict>
		<key>2</key>
		<dict>
			<key>Track ID</key><integer>2</integer>
			<key>Persistent ID</key><string>PID_B</string>
			<key>Name</key><string>Track B</string>
			<key>Kind</key><string>Audiobook</string>
			<key>Location</key><string>file://localhost` + locB + `</string>
		</dict>
	</dict>
	<key>Playlists</key><array/>
</dict>
</plist>
`
	p := filepath.Join(dir, "iTunes Library.xml")
	require.NoError(t, os.WriteFile(p, []byte(xml), 0o644))
	return p
}

// ---------------------------------------------------------------------------
// newPathRepairer constructor
// ---------------------------------------------------------------------------

func TestNewPathRepairer(t *testing.T) {
	m := dbmocks.NewMockStore(t)
	cfg := PathRepairConfig{XMLPath: "/tmp/iTunes Library.xml", AudiobookRoot: "/tmp/books"}
	r := newPathRepairer(m, nil, cfg)
	require.NotNil(t, r)
	assert.Equal(t, m, r.store)
	assert.Nil(t, r.enqueuer)
	assert.Equal(t, cfg.XMLPath, r.cfg.XMLPath)
	assert.Equal(t, cfg.AudiobookRoot, r.cfg.AudiobookRoot)
}

// ---------------------------------------------------------------------------
// Repair — tier A: missing track resolved via DB → on-disk path
// ---------------------------------------------------------------------------

func TestRepair_TierA_AutoResolvesMissingTrack(t *testing.T) {
	dir := t.TempDir()
	// locA exists on disk; locB does not — but tier A finds the new
	// path via DB and that new path also exists on disk.
	locA := filepath.Join(dir, "alive.m4b")
	require.NoError(t, os.WriteFile(locA, []byte("a"), 0o644))
	locB := filepath.Join(dir, "vanished.m4b") // never created
	newPath := filepath.Join(dir, "moved.m4b")
	require.NoError(t, os.WriteFile(newPath, []byte("b"), 0o644))

	xmlPath := writeFixtureXML(t, dir, locA, locB)

	m := dbmocks.NewMockStore(t)
	// Single PID → bookID lookup at the worker level; tier A then
	// reads the matching BookFile and finds the new path on disk.
	m.EXPECT().GetBookByExternalID("itunes", "PID_B").
		Return("book-b", nil).Once()
	m.EXPECT().GetBookFiles("book-b").
		Return([]database.BookFile{
			{ID: "f1", FilePath: newPath, ITunesPersistentID: "PID_B"},
		}, nil).Once()
	m.EXPECT().DeleteOperationState("op-tierA").Return(nil).Once()
	m.EXPECT().UpdateOperationResultData("op-tierA", mock.Anything).Return(nil).Once()

	r := newPathRepairer(m, nil, PathRepairConfig{XMLPath: xmlPath})
	res, err := r.repairWithResult(context.Background(), "op-tierA", true, noopProgressRepair{})
	require.NoError(t, err)

	assert.Equal(t, 2, res.XMLTracks)
	assert.Equal(t, 1, res.Missing)
	assert.Equal(t, 1, res.AutoResolved)
	assert.Equal(t, 0, res.NeedsReview)
	assert.Equal(t, 0, res.Unresolved)
	assert.True(t, res.DryRun)
	assert.Equal(t, 0, res.Enqueued, "dry-run must not enqueue")
}

// ---------------------------------------------------------------------------
// Repair — tier A: missing track with no DB mapping → unresolved
// ---------------------------------------------------------------------------

func TestRepair_TierA_NoMappingFallsThrough(t *testing.T) {
	dir := t.TempDir()
	locA := filepath.Join(dir, "alive.m4b")
	require.NoError(t, os.WriteFile(locA, []byte("a"), 0o644))
	locB := filepath.Join(dir, "vanished.m4b")
	xmlPath := writeFixtureXML(t, dir, locA, locB)

	m := dbmocks.NewMockStore(t)
	m.EXPECT().GetBookByExternalID("itunes", "PID_B").
		Return("", nil).Once()
	m.EXPECT().DeleteOperationState("op-noMap").Return(nil).Once()
	m.EXPECT().UpdateOperationResultData("op-noMap", mock.Anything).Return(nil).Once()

	r := newPathRepairer(m, nil, PathRepairConfig{XMLPath: xmlPath})
	res, err := r.repairWithResult(context.Background(), "op-noMap", true, noopProgressRepair{})
	require.NoError(t, err)

	assert.Equal(t, 1, res.Missing)
	assert.Equal(t, 0, res.AutoResolved)
	assert.Equal(t, 1, res.Unresolved)
}

// ---------------------------------------------------------------------------
// Repair — tier B: tier A fails (no DB path), tier B finds via tag scan
// ---------------------------------------------------------------------------

func TestRepair_TierB_RecoversFromStaleDBPath(t *testing.T) {
	dir := t.TempDir()
	locA := filepath.Join(dir, "alive.m4b")
	require.NoError(t, os.WriteFile(locA, []byte("a"), 0o644))
	locB := filepath.Join(dir, "vanished.m4b") // gone in iTunes XML
	xmlPath := writeFixtureXML(t, dir, locA, locB)

	// The DB has stale paths for book-b — tier A returns false.
	// Disk has a moved file under audiobook root that carries the
	// AUDIOBOOK_ORGANIZER_ID tag for book-b.
	root := filepath.Join(dir, "library")
	movedFile := filepath.Join(root, "author/book-b/segment.m4b")
	require.NoError(t, os.MkdirAll(filepath.Dir(movedFile), 0o755))
	require.NoError(t, os.WriteFile(movedFile, []byte("b"), 0o644))

	m := dbmocks.NewMockStore(t)
	// Tier A path: external_id_map → bookID → BookFiles → none on disk;
	// tier A's GetBookByID also lands on a missing file → tier A returns false.
	m.EXPECT().GetBookByExternalID("itunes", "PID_B").Return("book-b", nil).Once()
	m.EXPECT().GetBookFiles("book-b").
		Return([]database.BookFile{{ID: "f1", FilePath: "/disk/STALE.m4b", ITunesPersistentID: "PID_B"}}, nil).Once()
	m.EXPECT().GetBookByID("book-b").
		Return(&database.Book{ID: "book-b", FilePath: "/disk/STALE.m4b"}, nil).Once()
	m.EXPECT().DeleteOperationState("op-tierB").Return(nil).Once()
	m.EXPECT().UpdateOperationResultData("op-tierB", mock.Anything).Return(nil).Once()

	r := newPathRepairer(m, nil, PathRepairConfig{XMLPath: xmlPath, AudiobookRoot: root})
	// Inject deterministic extractor that maps movedFile → book-b.
	r.bookIDExtractor = func(p string) (string, error) {
		if p == movedFile {
			return "book-b", nil
		}
		return "", nil
	}

	res, err := r.repairWithResult(context.Background(), "op-tierB", true, noopProgressRepair{})
	require.NoError(t, err)

	assert.Equal(t, 1, res.Missing)
	assert.Equal(t, 1, res.AutoResolved, "should be resolved by tier B")
	assert.Equal(t, 0, res.Unresolved)
}

// ---------------------------------------------------------------------------
// Repair — tier C: tiers A/B both fail, fuzzy match emits review items
// ---------------------------------------------------------------------------

func TestRepair_TierC_EmitsReviewCandidates(t *testing.T) {
	dir := t.TempDir()
	locA := filepath.Join(dir, "alive.m4b")
	require.NoError(t, os.WriteFile(locA, []byte("a"), 0o644))
	locB := filepath.Join(dir, "Track-B.mp3") // gone in iTunes XML
	xmlPath := writeFixtureXML(t, dir, locA, locB)

	root := filepath.Join(dir, "library")
	// Disk has a candidate file with a similar basename, no embedded tag.
	candidate := filepath.Join(root, "author/Track-B-relocated.mp3")
	require.NoError(t, os.MkdirAll(filepath.Dir(candidate), 0o755))
	require.NoError(t, os.WriteFile(candidate, []byte("b"), 0o644))

	m := dbmocks.NewMockStore(t)
	m.EXPECT().GetBookByExternalID("itunes", "PID_B").Return("", nil).Once()
	m.EXPECT().DeleteOperationState("op-tierC").Return(nil).Once()
	m.EXPECT().UpdateOperationResultData("op-tierC", mock.Anything).Return(nil).Once()

	r := newPathRepairer(m, nil, PathRepairConfig{XMLPath: xmlPath, AudiobookRoot: root})
	r.bookIDExtractor = func(string) (string, error) { return "", nil }

	res, err := r.repairWithResult(context.Background(), "op-tierC", true, noopProgressRepair{})
	require.NoError(t, err)

	assert.Equal(t, 1, res.Missing)
	assert.Equal(t, 0, res.AutoResolved)
	assert.Equal(t, 1, res.NeedsReview)
	require.Len(t, res.NeedsReviewItems, 1)
	assert.Equal(t, "PID_B", res.NeedsReviewItems[0].PID)
	assert.Equal(t, "Track B", res.NeedsReviewItems[0].Title)
	assert.NotEmpty(t, res.NeedsReviewItems[0].Candidates)
	assert.Equal(t, candidate, res.NeedsReviewItems[0].Candidates[0].Path)
}

// ---------------------------------------------------------------------------
// Repair — apply mode: tier A success → DB updated + Enqueuer called
// ---------------------------------------------------------------------------

func TestRepair_ApplyMode_TierA_UpdatesAndEnqueues(t *testing.T) {
	dir := t.TempDir()
	withITunesPathMapping(t, dir)
	locA := filepath.Join(dir, "alive.m4b")
	require.NoError(t, os.WriteFile(locA, []byte("a"), 0o644))
	locB := filepath.Join(dir, "vanished.m4b") // gone
	newPath := filepath.Join(dir, "moved.m4b")
	require.NoError(t, os.WriteFile(newPath, []byte("b"), 0o644))
	xmlPath := writeFixtureXML(t, dir, locA, locB)

	m := dbmocks.NewMockStore(t)
	m.EXPECT().GetBookByExternalID("itunes", "PID_B").Return("book-b", nil).Once()
	bf := database.BookFile{ID: "f1", BookID: "book-b", FilePath: newPath, ITunesPersistentID: "PID_B"}
	m.EXPECT().GetBookFiles("book-b").Return([]database.BookFile{bf}, nil).Once()
	// Tier A returns the bf path; apply path then re-fetches files to do the update.
	m.EXPECT().GetBookFiles("book-b").Return([]database.BookFile{bf}, nil).Once()
	m.EXPECT().UpdateBookFile("f1", mock.MatchedBy(func(updated *database.BookFile) bool {
		// FilePath stays the same (it was already correct); ITunesPath is recomputed.
		return updated.FilePath == newPath && updated.ITunesPath != ""
	})).Return(nil).Once()
	m.EXPECT().RecordPathChange(mock.MatchedBy(func(c *database.BookPathChange) bool {
		return c.BookID == "book-b" && c.NewPath == newPath && c.ChangeType == "itunes_path_repair"
	})).Return(nil).Once()
	m.EXPECT().DeleteOperationState("op-apply").Return(nil).Once()
	m.EXPECT().UpdateOperationResultData("op-apply", mock.Anything).Return(nil).Once()

	enq := &mockEnqueuer{}
	r := newPathRepairer(m, enq, PathRepairConfig{XMLPath: xmlPath})
	res, err := r.repairWithResult(context.Background(), "op-apply", false, noopProgressRepair{})
	require.NoError(t, err)

	assert.Equal(t, 1, res.AutoResolved)
	assert.Equal(t, 1, res.Enqueued)
	assert.False(t, res.DryRun)
	require.Len(t, enq.enqueues, 1)
	assert.Equal(t, "book-b", enq.enqueues[0])
}

// ---------------------------------------------------------------------------
// Repair — end-to-end: one OK / one tier A / one tier B / one tier C in
// a single dry-run, plus the JSON report file lands on disk
// ---------------------------------------------------------------------------

func TestRepair_EndToEnd_AllTiers(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, "library")
	require.NoError(t, os.MkdirAll(root, 0o755))

	// Track-OK: location exists, no work needed.
	okPath := filepath.Join(dir, "ok.m4b")
	require.NoError(t, os.WriteFile(okPath, []byte("ok"), 0o644))

	// Track-A: location missing; tier A finds the new path via DB.
	missingA := filepath.Join(dir, "missing-A.m4b")
	tierAPath := filepath.Join(root, "tier-a/file.m4b")
	require.NoError(t, os.MkdirAll(filepath.Dir(tierAPath), 0o755))
	require.NoError(t, os.WriteFile(tierAPath, []byte("a"), 0o644))

	// Track-B: location missing, DB also stale; tier B finds via tag scan.
	missingB := filepath.Join(dir, "missing-B.m4b")
	tierBPath := filepath.Join(root, "tier-b/file.m4b")
	require.NoError(t, os.MkdirAll(filepath.Dir(tierBPath), 0o755))
	require.NoError(t, os.WriteFile(tierBPath, []byte("b"), 0o644))

	// Track-C: location missing, no DB mapping; tier C emits a fuzzy candidate.
	missingC := filepath.Join(dir, "Unique-Title-C.m4b")
	tierCCandidate := filepath.Join(root, "Unique-Title-C-relocated.m4b")
	require.NoError(t, os.WriteFile(tierCCandidate, []byte("c"), 0o644))

	xmlPath := writeFixtureXMLN(t, dir, []struct{ PID, Name, Location string }{
		{"PID_OK", "Track OK", okPath},
		{"PID_A", "Track A", missingA},
		{"PID_B", "Track B", missingB},
		{"PID_C", "Unique Title C", missingC},
	})

	m := dbmocks.NewMockStore(t)
	// Tier A flow
	m.EXPECT().GetBookByExternalID("itunes", "PID_A").Return("book-a", nil).Once()
	m.EXPECT().GetBookFiles("book-a").Return([]database.BookFile{
		{ID: "fa", FilePath: tierAPath, ITunesPersistentID: "PID_A"},
	}, nil).Once()
	// Tier B flow: PID lookup succeeds, tier A's BookFiles + book are stale.
	m.EXPECT().GetBookByExternalID("itunes", "PID_B").Return("book-b", nil).Once()
	m.EXPECT().GetBookFiles("book-b").Return([]database.BookFile{
		{ID: "fb", FilePath: "/disk/STALE.m4b", ITunesPersistentID: "PID_B"},
	}, nil).Once()
	m.EXPECT().GetBookByID("book-b").Return(&database.Book{ID: "book-b", FilePath: "/disk/STALE.m4b"}, nil).Once()
	// Tier C: no DB mapping
	m.EXPECT().GetBookByExternalID("itunes", "PID_C").Return("", nil).Once()

	reportDir := filepath.Join(dir, "reports")
	m.EXPECT().DeleteOperationState("op-e2e").Return(nil).Once()
	m.EXPECT().UpdateOperationResultData("op-e2e", mock.Anything).Return(nil).Once()

	r := newPathRepairer(m, nil, PathRepairConfig{
		XMLPath:       xmlPath,
		AudiobookRoot: root,
		ReportDir:     reportDir,
	})
	r.bookIDExtractor = func(p string) (string, error) {
		if p == tierBPath {
			return "book-b", nil
		}
		return "", nil
	}

	res, err := r.repairWithResult(context.Background(), "op-e2e", true, noopProgressRepair{})
	require.NoError(t, err)

	assert.Equal(t, 4, res.XMLTracks)
	assert.Equal(t, 3, res.Missing, "OK track skipped; the other 3 are missing")
	assert.Equal(t, 2, res.AutoResolved, "tier A and tier B succeed")
	assert.Equal(t, 1, res.NeedsReview, "tier C emits one")
	assert.Equal(t, 0, res.Unresolved)
	require.Len(t, res.NeedsReviewItems, 1)
	assert.Equal(t, "PID_C", res.NeedsReviewItems[0].PID)

	// Report file written and parses back to the same shape.
	require.NotEmpty(t, res.ReportPath)
	bytesOnDisk, err := os.ReadFile(res.ReportPath)
	require.NoError(t, err)
	var roundtrip iTunesPathRepairResult
	require.NoError(t, json.Unmarshal(bytesOnDisk, &roundtrip))
	assert.Equal(t, res.XMLTracks, roundtrip.XMLTracks)
	assert.Equal(t, res.NeedsReview, roundtrip.NeedsReview)
}

// ---------------------------------------------------------------------------
// Repair — XML parse error returns the error
// ---------------------------------------------------------------------------

func TestRepair_XMLParseError(t *testing.T) {
	m := dbmocks.NewMockStore(t)
	r := newPathRepairer(m, nil, PathRepairConfig{XMLPath: "/nonexistent/itunes.xml"})
	_, err := r.repairWithResult(context.Background(), "op-bad", true, noopProgressRepair{})
	require.Error(t, err)
}
