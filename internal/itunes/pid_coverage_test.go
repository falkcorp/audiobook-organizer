// file: internal/itunes/pid_coverage_test.go
// version: 1.0.0
// guid: 4b5990a2-9a20-4b60-b852-64e4f8edc841
// last-edited: 2026-09-11

package itunes

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// coverageStoreFake is a read-only PIDCoverageStore. It records the paging
// arguments so a test can pin that the census asks for every book (limit 0).
type coverageStoreFake struct {
	files    []database.BookFileCore
	books    []database.Book
	filesErr error
	booksErr error

	booksCalls  int
	booksLimit  int
	booksOffset int
}

func (f *coverageStoreFake) GetAllBookFilesCore() ([]database.BookFileCore, error) {
	return f.files, f.filesErr
}

func (f *coverageStoreFake) ListBooksByITunesPID(limit, offset int) ([]database.Book, error) {
	f.booksCalls++
	f.booksLimit, f.booksOffset = limit, offset
	return f.books, f.booksErr
}

func covBook(id, pid string) database.Book {
	return database.Book{ID: id, ITunesPersistentID: &pid}
}

type covTrackFx struct {
	id   int
	pid  string // "" omits the Persistent ID key
	name string
}

type covPlaylistFx struct {
	name   string
	pid    string
	folder bool
	smart  bool
	items  []int
}

// writeCoverageLibrary writes a tiny iTunes Library.xml plist in Apple's
// layout (key and dict on separate lines) and returns its path.
func writeCoverageLibrary(t *testing.T, tracks []covTrackFx, playlists []covPlaylistFx) string {
	t.Helper()
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Application Version</key><string>12.13.10.3</string>
	<key>Tracks</key>
	<dict>
`)
	for _, tr := range tracks {
		fmt.Fprintf(&b, "\t\t<key>%d</key>\n\t\t<dict>\n", tr.id)
		fmt.Fprintf(&b, "\t\t\t<key>Track ID</key><integer>%d</integer>\n", tr.id)
		if tr.pid != "" {
			fmt.Fprintf(&b, "\t\t\t<key>Persistent ID</key><string>%s</string>\n", tr.pid)
		}
		fmt.Fprintf(&b, "\t\t\t<key>Name</key><string>%s</string>\n", tr.name)
		b.WriteString("\t\t\t<key>Kind</key><string>Audiobook</string>\n\t\t</dict>\n")
	}
	b.WriteString("\t</dict>\n\t<key>Playlists</key>\n\t<array>\n")
	for _, p := range playlists {
		b.WriteString("\t\t<dict>\n")
		fmt.Fprintf(&b, "\t\t\t<key>Name</key><string>%s</string>\n", p.name)
		fmt.Fprintf(&b, "\t\t\t<key>Playlist Persistent ID</key><string>%s</string>\n", p.pid)
		if p.folder {
			b.WriteString("\t\t\t<key>Folder</key><true/>\n")
		}
		if p.smart {
			b.WriteString("\t\t\t<key>Smart Criteria</key><data>AAECAw==</data>\n")
		}
		if len(p.items) > 0 {
			b.WriteString("\t\t\t<key>Playlist Items</key>\n\t\t\t<array>\n")
			for _, id := range p.items {
				fmt.Fprintf(&b, "\t\t\t\t<dict><key>Track ID</key><integer>%d</integer></dict>\n", id)
			}
			b.WriteString("\t\t\t</array>\n")
		}
		b.WriteString("\t\t</dict>\n")
	}
	b.WriteString("\t</array>\n</dict>\n</plist>\n")

	path := filepath.Join(t.TempDir(), "iTunes Library.xml")
	if err := os.WriteFile(path, []byte(b.String()), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	return path
}

func runCoverage(t *testing.T, store *coverageStoreFake, path string) *PIDCoverageReport {
	t.Helper()
	r, err := ComputePIDCoverage(context.Background(), store, path)
	if err != nil {
		t.Fatalf("ComputePIDCoverage: %v", err)
	}
	if store.booksCalls != 1 || store.booksLimit != 0 || store.booksOffset != 0 {
		t.Fatalf("ListBooksByITunesPID calls=%d limit=%d offset=%d, want exactly one unpaged call (0, 0)",
			store.booksCalls, store.booksLimit, store.booksOffset)
	}
	if _, err := json.Marshal(r); err != nil {
		t.Fatalf("report must JSON-encode (the --full path): %v", err)
	}
	return r
}

func assertBucket(t *testing.T, label string, got, want PIDCoverageBucket) {
	t.Helper()
	if got != want {
		t.Errorf("%s bucket:\n got %+v\nwant %+v", label, got, want)
	}
}

func sampleIDs(s []UnresolvedPIDSample) []int {
	out := make([]int, 0, len(s))
	for _, x := range s {
		out = append(out, x.TrackID)
	}
	return out
}

func TestComputePIDCoverage_AllResolve(t *testing.T) {
	path := writeCoverageLibrary(t,
		[]covTrackFx{
			{101, "AAAA000000000001", "One"},
			{102, "AAAA000000000002", "Two"},
			{103, "AAAA000000000003", "Three"},
		},
		[]covPlaylistFx{{name: "Smart", pid: "1000000000000001", smart: true, items: []int{101, 102, 103}}},
	)
	store := &coverageStoreFake{
		files: []database.BookFileCore{
			bf("f1", "b1", "/lib/1.m4b", "AAAA000000000001"),
			bf("f2", "b2", "/lib/2.m4b", "AAAA000000000002"),
			bf("f3", "b3", "/lib/3.m4b", "AAAA000000000003"),
		},
	}
	r := runCoverage(t, store, path)

	if r.Source != PIDCoverageSourceXML || r.TotalTracks != 3 || r.TracksWithoutPID != 0 {
		t.Fatalf("source=%q total=%d without_pid=%d", r.Source, r.TotalTracks, r.TracksWithoutPID)
	}
	all := PIDCoverageBucket{
		Tracks: 3, ResolvedFileLevel: 3, ResolvedFileLevelPercent: 100,
		ResolvedEither: 3, ResolvedEitherPercent: 100,
	}
	assertBucket(t, "library", r.Library, all)
	assertBucket(t, "smart_referenced", r.SmartReferenced, all)
	if len(r.UnresolvedSamples) != 0 || len(r.UnresolvedSmartSamples) != 0 {
		t.Errorf("no track is unresolved, got samples %v / %v", r.UnresolvedSamples, r.UnresolvedSmartSamples)
	}
}

func TestComputePIDCoverage_NoneResolve(t *testing.T) {
	// Tracks written in descending ID order: samples must still come out ascending.
	path := writeCoverageLibrary(t,
		[]covTrackFx{
			{202, "CCCC000000000002", "Second"},
			{201, "CCCC000000000001", "First"},
		},
		nil,
	)
	unrelated := "DDDD000000000009"
	store := &coverageStoreFake{
		files: []database.BookFileCore{bf("f1", "b1", "/lib/x.m4b", unrelated)},
		books: []database.Book{covBook("b1", unrelated)},
	}
	r := runCoverage(t, store, path)

	assertBucket(t, "library", r.Library, PIDCoverageBucket{Tracks: 2, Unresolved: 2})
	if got := sampleIDs(r.UnresolvedSamples); len(got) != 2 || got[0] != 201 || got[1] != 202 {
		t.Errorf("unresolved samples = %v, want [201 202]", got)
	}
	if r.UnresolvedSamples[0].PID != "CCCC000000000001" || r.UnresolvedSamples[0].Name != "First" {
		t.Errorf("sample carries wrong metadata: %+v", r.UnresolvedSamples[0])
	}
	if r.DBBookFilePIDs != 1 || r.DBBookPIDs != 1 {
		t.Errorf("db pids file=%d book=%d, want 1/1", r.DBBookFilePIDs, r.DBBookPIDs)
	}
}

// TestComputePIDCoverage_PartialMix pins exact counts and percents for every
// resolution path, the case/whitespace normalization on both sides, the
// no-PID bucket, and the playlist-shape and dangling-reference accounting.
func TestComputePIDCoverage_PartialMix(t *testing.T) {
	path := writeCoverageLibrary(t,
		[]covTrackFx{
			{101, "AAAA000000000001", "Book Level Only"},
			{102, "AAAA000000000002", "File Level Only"},
			{103, "AAAA000000000003", "Both"},
			{104, "AAAA000000000004", "Nobody"},
			{105, "", "No PID"},
		},
		[]covPlaylistFx{
			{name: "Folder", pid: "1000000000000001", folder: true, items: []int{101, 102}},
			// 104 repeats and 999 does not exist: refs=5, distinct=4, dangling=1.
			{name: "Smart Materialized", pid: "1000000000000002", smart: true, items: []int{101, 104, 104, 999, 105}},
			{name: "Smart Criteria Only", pid: "1000000000000003", smart: true},
			{name: "Regular", pid: "1000000000000004", items: []int{102, 103}},
		},
	)
	store := &coverageStoreFake{
		files: []database.BookFileCore{
			bf("f2", "b2", "/lib/2.m4b", " aaaa000000000002 "), // lower-case + padding in the DB
			bf("f3", "b3", "/lib/3.m4b", "AAAA000000000003"),
			bf("f9", "b9", "/lib/9.m4b", "BBBB000000000009"), // in the DB, not the library
			bf("f0", "b0", "/lib/0.m4b", ""),                 // no PID at all
		},
		books: []database.Book{
			covBook("b1", "aaaa000000000001"),
			covBook("b3", "AAAA000000000003"),
			{ID: "bnil"}, // nil PID pointer must be skipped, not dereferenced
		},
	}
	r := runCoverage(t, store, path)

	if r.TotalTracks != 5 || r.TracksWithoutPID != 1 {
		t.Errorf("total=%d without_pid=%d, want 5/1", r.TotalTracks, r.TracksWithoutPID)
	}
	if r.DBBookFilePIDs != 3 || r.DBBookPIDs != 2 {
		t.Errorf("db pids file=%d book=%d, want 3/2", r.DBBookFilePIDs, r.DBBookPIDs)
	}
	assertBucket(t, "library", r.Library, PIDCoverageBucket{
		Tracks:            4,
		ResolvedFileLevel: 2, ResolvedFileLevelPercent: 50,
		ResolvedBookLevel: 2, ResolvedBookLevelPercent: 50,
		ResolvedEither: 3, ResolvedEitherPercent: 75,
		Unresolved: 1,
	})

	if r.Playlists != 3 || r.SmartPlaylists != 2 || r.SmartWithItems != 1 ||
		r.SmartCriteriaOnly != 1 || r.RegularWithItems != 1 {
		t.Errorf("playlist shape: playlists=%d smart=%d with_items=%d criteria_only=%d regular=%d",
			r.Playlists, r.SmartPlaylists, r.SmartWithItems, r.SmartCriteriaOnly, r.RegularWithItems)
	}
	if r.SmartItemRefs != 5 || r.SmartDistinctTrackIDs != 4 || r.SmartDanglingTrackIDs != 1 ||
		r.SmartReferencedWithoutPID != 1 {
		t.Errorf("smart refs=%d distinct=%d dangling=%d without_pid=%d, want 5/4/1/1",
			r.SmartItemRefs, r.SmartDistinctTrackIDs, r.SmartDanglingTrackIDs, r.SmartReferencedWithoutPID)
	}
	// Referenced tracks with a PID: 101 (book level) and 104 (unresolved).
	assertBucket(t, "smart_referenced", r.SmartReferenced, PIDCoverageBucket{
		Tracks:            2,
		ResolvedBookLevel: 1, ResolvedBookLevelPercent: 50,
		ResolvedEither: 1, ResolvedEitherPercent: 50,
		Unresolved: 1,
	})
	if got := sampleIDs(r.UnresolvedSamples); len(got) != 1 || got[0] != 104 {
		t.Errorf("unresolved samples = %v, want [104]", got)
	}
	if got := sampleIDs(r.UnresolvedSmartSamples); len(got) != 1 || got[0] != 104 {
		t.Errorf("unresolved smart samples = %v, want [104]", got)
	}
}

func TestComputePIDCoverage_EmptyLibrary(t *testing.T) {
	// A present-but-empty Tracks dict is an empty library, not a parse failure.
	path := writeCoverageLibrary(t, nil, nil)
	r := runCoverage(t, &coverageStoreFake{}, path)

	if r.Source != PIDCoverageSourceXML || r.TotalTracks != 0 || r.Playlists != 0 {
		t.Fatalf("source=%q total=%d playlists=%d", r.Source, r.TotalTracks, r.Playlists)
	}
	assertBucket(t, "library", r.Library, PIDCoverageBucket{})
	assertBucket(t, "smart_referenced", r.SmartReferenced, PIDCoverageBucket{})
	if r.UnresolvedSamples == nil || r.UnresolvedSmartSamples == nil {
		t.Error("sample lists should be empty, not nil, so --full prints [] rather than null")
	}
}

func TestComputePIDCoverage_Errors(t *testing.T) {
	t.Run("no Tracks section", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "not-a-library.xml")
		body := `<?xml version="1.0"?><plist version="1.0"><dict><key>Other</key><string>x</string></dict></plist>`
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := ComputePIDCoverage(context.Background(), &coverageStoreFake{}, path); err == nil {
			t.Fatal("a file with no Tracks section must be an error, not a 0-track report")
		}
	})
	t.Run("missing file", func(t *testing.T) {
		if _, err := ComputePIDCoverage(context.Background(), &coverageStoreFake{}, filepath.Join(t.TempDir(), "nope.xml")); err == nil {
			t.Fatal("expected an error for a missing library file")
		}
	})
	t.Run("cancelled context", func(t *testing.T) {
		path := writeCoverageLibrary(t, []covTrackFx{{1, "AAAA000000000001", "One"}}, nil)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if _, err := ComputePIDCoverage(ctx, &coverageStoreFake{}, path); !errors.Is(err, context.Canceled) {
			t.Fatalf("a cancelled parse must not yield a partial report; err=%v", err)
		}
	})
	path := writeCoverageLibrary(t, []covTrackFx{{1, "AAAA000000000001", "One"}}, nil)
	boom := errors.New("boom")
	t.Run("book_file load error", func(t *testing.T) {
		if _, err := ComputePIDCoverage(context.Background(), &coverageStoreFake{filesErr: boom}, path); !errors.Is(err, boom) {
			t.Fatalf("err=%v, want wrapped boom", err)
		}
	})
	t.Run("book load error", func(t *testing.T) {
		if _, err := ComputePIDCoverage(context.Background(), &coverageStoreFake{booksErr: boom}, path); !errors.Is(err, boom) {
			t.Fatalf("err=%v, want wrapped boom", err)
		}
	})
}

// TestCoverageFromITL pins the .itl adapter: pidToHex is lower-case and must be
// upper-cased to match DB PIDs, and an all-zero PID means "no PID".
func TestCoverageFromITL(t *testing.T) {
	lib := &ITLLibrary{
		Tracks: []ITLTrack{
			{TrackID: 7, PersistentID: [8]byte{0xab, 0xcd, 0, 0, 0, 0, 0, 1}, Name: "Seven"},
			{TrackID: 8, Name: "Zero PID"},
		},
		Playlists: []ITLPlaylist{{Title: "P", IsSmart: true, Items: []int{7}}},
	}
	tracks, playlists := coverageFromITL(lib)
	if len(tracks) != 2 || tracks[0].PID != "ABCD000000000001" || tracks[1].PID != "" {
		t.Fatalf("tracks = %+v", tracks)
	}
	store := &coverageStoreFake{files: []database.BookFileCore{bf("f", "b", "/x.m4b", "abcd000000000001")}}
	r, err := computePIDCoverage(store, tracks, playlists)
	if err != nil {
		t.Fatal(err)
	}
	assertBucket(t, "library", r.Library, PIDCoverageBucket{
		Tracks: 1, ResolvedFileLevel: 1, ResolvedFileLevelPercent: 100, ResolvedEither: 1, ResolvedEitherPercent: 100,
	})
	if r.TracksWithoutPID != 1 || r.SmartWithItems != 1 || r.SmartReferenced.Tracks != 1 {
		t.Errorf("without_pid=%d smart_with_items=%d smart_referenced=%d",
			r.TracksWithoutPID, r.SmartWithItems, r.SmartReferenced.Tracks)
	}
}
