// file: internal/itunes/pid_coverage.go
// version: 1.0.0
// guid: bf7e672f-6693-455c-a992-d8b4aa4ca1d0
// last-edited: 2026-09-11
//
// READ-ONLY iTunes track Persistent ID coverage census (TASK-184,
// ITUNES-SMARTCRIT-PARSE). Before anyone promises a "Playlist Items snapshot"
// import of the owner's smart playlists, we need to know how many of the tracks
// those playlists reference can actually be linked back to a row in our DB by
// Persistent ID. This measures that; it does not import anything.
//
// It parses the library (an `iTunes Library.xml` export, or a binary .itl),
// collects every track's Persistent ID, and checks each one against two
// pre-loaded PID sets:
//
//   - book_file level: GetAllBookFilesCore(). PIDs are minted per book_file
//     (TrackProvisioner.Provision → GeneratePIDHex), so this is the linkage that
//     answers the import question. It includes rows whose parent book is
//     soft-deleted.
//   - book level: ListBooksByITunesPID(0, 0). Live books only — both store
//     implementations drop soft-deleted books.
//
// Why not GetBookByITunesPersistentID per track, as the brief suggests: that
// accessor iterates and JSON-decodes every book on every call, so calling it
// per track is O(tracks × books) — billions of decodes on a six-figure library.
// Loading both PID sets once from existing accessors is the same pattern the
// sibling censuses use (pid_integrity.go, cross_type.go build `byPID` from one
// GetAllBookFilesCore call). No new store helper is added. The book-level set
// differs from that accessor in one way: it excludes soft-deleted books.
//
// PIDs are compared upper-cased and trimmed on both sides. XML Persistent IDs
// are upper-case hex while pidToHex (the .itl path) is lower-case; skipping the
// normalization would report 0% coverage and read like a finding.
//
// Nothing here mutates: it opens the library file for reading and calls two
// read accessors on the store. It writes nothing to the DB or to any iTunes file.
package itunes

import (
	"context"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// PIDCoverageStore is the read-only slice of the store the coverage census needs.
type PIDCoverageStore interface {
	// GetAllBookFilesCore returns every book_file row (including those whose
	// parent book is soft-deleted), carrying ITunesPersistentID.
	GetAllBookFilesCore() ([]database.BookFileCore, error)
	// ListBooksByITunesPID returns live books with a non-empty book-level PID;
	// limit 0 means no limit.
	ListBooksByITunesPID(limit, offset int) ([]database.Book, error)
}

// Library source formats reported in PIDCoverageReport.Source.
const (
	PIDCoverageSourceXML = "xml"
	PIDCoverageSourceITL = "itl"
)

// pidCoverageSampleLimit bounds each unresolved-sample list.
const pidCoverageSampleLimit = 25

// coverageTrack is the per-track slice of a parsed library the census needs.
type coverageTrack struct {
	TrackID int
	PID     string // upper-case hex, trimmed; "" when the track carries none
	Name    string
	Artist  string
	Album   string
}

// coveragePlaylist is the per-playlist slice of a parsed library the census needs.
type coveragePlaylist struct {
	IsFolder bool
	IsSmart  bool
	Items    []int // Track IDs, not PIDs
}

// PIDCoverageBucket is one resolution tally over a set of tracks.
//
// ResolvedFileLevel is the number that answers "can a Playlist Items import
// link this track to our data": PIDs are minted per book_file. ResolvedBookLevel
// is a context number (the book-level PID field on live books), and
// ResolvedEither is their union. Unresolved = Tracks − ResolvedEither.
type PIDCoverageBucket struct {
	Tracks                   int     `json:"tracks"`
	ResolvedFileLevel        int     `json:"resolved_file_level"`
	ResolvedFileLevelPercent float64 `json:"resolved_file_level_percent"`
	ResolvedBookLevel        int     `json:"resolved_book_level"`
	ResolvedBookLevelPercent float64 `json:"resolved_book_level_percent"`
	ResolvedEither           int     `json:"resolved_either"`
	ResolvedEitherPercent    float64 `json:"resolved_either_percent"`
	Unresolved               int     `json:"unresolved"`
}

// UnresolvedPIDSample is one track whose PID matched nothing in the DB, carried
// with enough track metadata to spot-check by hand.
type UnresolvedPIDSample struct {
	TrackID int    `json:"track_id"`
	PID     string `json:"pid"`
	Name    string `json:"name,omitempty"`
	Artist  string `json:"artist,omitempty"`
	Album   string `json:"album,omitempty"`
}

// PIDCoverageReport is the full coverage census.
type PIDCoverageReport struct {
	Source string `json:"source"` // PIDCoverageSourceXML | PIDCoverageSourceITL

	// Library-wide. Tracks without a PID are counted separately and left out of
	// the resolution denominator: an absent PID is unknown, not unresolved.
	TotalTracks      int               `json:"total_tracks"`
	TracksWithoutPID int               `json:"tracks_without_pid"`
	Library          PIDCoverageBucket `json:"library"`

	// DB-side context: how many PIDs each index held.
	DBBookFilePIDs int `json:"db_book_file_pids"` // distinct PIDs across book_file rows
	DBBookPIDs     int `json:"db_book_pids"`      // distinct PIDs across live books

	// Playlist shape. A playlist is smart when it carries a Smart Criteria blob
	// (see xml_library.go); folders are excluded from every count below.
	Playlists         int `json:"playlists"`
	SmartPlaylists    int `json:"smart_playlists"`
	SmartWithItems    int `json:"smart_with_items"`    // criteria + a materialized Playlist Items list
	SmartCriteriaOnly int `json:"smart_criteria_only"` // criteria, no items
	RegularWithItems  int `json:"regular_with_items"`  // no criteria, has items

	// Coverage restricted to tracks referenced by SmartWithItems playlists.
	// Items are Track IDs; an ID with no track in the library is DANGLING and is
	// kept out of the bucket, so it cannot deflate the resolution percent.
	SmartItemRefs             int               `json:"smart_item_refs"`              // item references incl. repeats
	SmartDistinctTrackIDs     int               `json:"smart_distinct_track_ids"`     // distinct Track IDs referenced
	SmartDanglingTrackIDs     int               `json:"smart_dangling_track_ids"`     // referenced IDs with no track
	SmartReferencedWithoutPID int               `json:"smart_referenced_without_pid"` // referenced tracks with no PID
	SmartReferenced           PIDCoverageBucket `json:"smart_referenced"`

	// Up to pidCoverageSampleLimit each, ascending Track ID.
	UnresolvedSmartSamples []UnresolvedPIDSample `json:"unresolved_smart_samples"`
	UnresolvedSamples      []UnresolvedPIDSample `json:"unresolved_samples"`

	Notes []string `json:"notes,omitempty"`
}

// ComputePIDCoverage parses the library at libraryPath (an `iTunes Library.xml`
// export or a binary .itl, detected by the .itl "hdfm" magic) and reports how
// many track Persistent IDs resolve to a book_file or a live book in store.
// READ-ONLY.
//
// Use the XML export: the binary .itl parser extracts zero smart playlists from
// real libraries (xml_library.go), so the smart-playlist subset is only
// meaningful from XML. The report carries a note when the source is an .itl.
func ComputePIDCoverage(ctx context.Context, store PIDCoverageStore, libraryPath string) (*PIDCoverageReport, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	isITL, err := hasITLMagic(libraryPath)
	if err != nil {
		return nil, err
	}

	var (
		source    string
		tracks    []coverageTrack
		playlists []coveragePlaylist
	)
	if isITL {
		source = PIDCoverageSourceITL
		lib, perr := ParseITL(libraryPath)
		if perr != nil {
			return nil, perr
		}
		tracks, playlists = coverageFromITL(lib)
	} else {
		source = PIDCoverageSourceXML
		tracks, playlists, err = coverageFromXML(ctx, libraryPath)
		if err != nil {
			return nil, err
		}
	}

	report, err := computePIDCoverage(store, tracks, playlists)
	if err != nil {
		return nil, err
	}
	report.Source = source
	if isITL {
		report.Notes = append(report.Notes,
			"source is a binary .itl: its parser extracts no smart playlists from real libraries, so the smart_* counts are not meaningful; re-run against the iTunes Library.xml export")
	}
	return report, nil
}

// hasITLMagic reports whether the file starts with the binary .itl "hdfm" magic.
func hasITLMagic(path string) (bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return false, fmt.Errorf("open iTunes library: %w", err)
	}
	defer f.Close()
	magic := make([]byte, 4)
	if _, err := io.ReadFull(f, magic); err != nil {
		if err == io.EOF || err == io.ErrUnexpectedEOF {
			return false, nil // too short to be an .itl; let the XML parser report it
		}
		return false, fmt.Errorf("read iTunes library header: %w", err)
	}
	return string(magic) == "hdfm", nil
}

// coverageFromXML reads an XML export in two existing, memory-bounded passes:
// StreamingParseLibrary for tracks (ParseXMLLibraryPlaylists deliberately skips
// them) and ParseXMLLibraryPlaylists for playlists.
func coverageFromXML(ctx context.Context, path string) ([]coverageTrack, []coveragePlaylist, error) {
	var tracks []coverageTrack
	if _, err := StreamingParseLibrary(ctx, path, func(t *Track) error {
		tracks = append(tracks, coverageTrack{
			TrackID: t.TrackID,
			PID:     normalizeCoveragePID(t.PersistentID),
			Name:    t.Name,
			Artist:  t.Artist,
			Album:   t.Album,
		})
		return nil
	}); err != nil {
		return nil, nil, err
	}
	// StreamingParseLibrary treats cancellation as a clean stop and returns a
	// partial count with a nil error. A partial track list would produce a
	// coverage number that looks real, so a cancelled parse is an error here.
	if err := ctx.Err(); err != nil {
		return nil, nil, fmt.Errorf("iTunes XML track parse cancelled: %w", err)
	}

	lib, err := ParseXMLLibraryPlaylists(path)
	if err != nil {
		return nil, nil, err
	}
	return tracks, playlistsForCoverage(lib.Playlists), nil
}

// coverageFromITL adapts a parsed binary .itl. It uses pidToHex, matching the
// sibling censuses.
func coverageFromITL(lib *ITLLibrary) ([]coverageTrack, []coveragePlaylist) {
	tracks := make([]coverageTrack, 0, len(lib.Tracks))
	for i := range lib.Tracks {
		t := &lib.Tracks[i]
		pid := ""
		if t.PersistentID != ([8]byte{}) {
			pid = normalizeCoveragePID(pidToHex(t.PersistentID))
		}
		tracks = append(tracks, coverageTrack{
			TrackID: t.TrackID, PID: pid, Name: t.Name, Artist: t.Artist, Album: t.Album,
		})
	}
	return tracks, playlistsForCoverage(lib.Playlists)
}

func playlistsForCoverage(in []ITLPlaylist) []coveragePlaylist {
	out := make([]coveragePlaylist, 0, len(in))
	for i := range in {
		out = append(out, coveragePlaylist{IsFolder: in[i].IsFolder, IsSmart: in[i].IsSmart, Items: in[i].Items})
	}
	return out
}

func normalizeCoveragePID(pid string) string {
	return strings.ToUpper(strings.TrimSpace(pid))
}

func coveragePercent(n, d int) float64 {
	if d == 0 {
		return 0 // never NaN: encoding/json refuses NaN, and --full encodes the report
	}
	return 100 * float64(n) / float64(d)
}

// computePIDCoverage is the pure core, testable without a library file. The
// per-track work is two map lookups, so it runs in one goroutine; the store is
// read exactly twice, up front, regardless of library size.
func computePIDCoverage(store PIDCoverageStore, tracks []coverageTrack, playlists []coveragePlaylist) (*PIDCoverageReport, error) {
	files, err := store.GetAllBookFilesCore()
	if err != nil {
		return nil, fmt.Errorf("load book_file PIDs: %w", err)
	}
	filePIDs := make(map[string]struct{}, len(files))
	for i := range files {
		if pid := normalizeCoveragePID(files[i].ITunesPersistentID); pid != "" {
			filePIDs[pid] = struct{}{}
		}
	}
	books, err := store.ListBooksByITunesPID(0, 0)
	if err != nil {
		return nil, fmt.Errorf("load book PIDs: %w", err)
	}
	bookPIDs := make(map[string]struct{}, len(books))
	for i := range books {
		if books[i].ITunesPersistentID == nil {
			continue
		}
		if pid := normalizeCoveragePID(*books[i].ITunesPersistentID); pid != "" {
			bookPIDs[pid] = struct{}{}
		}
	}

	// Deterministic sample order regardless of parse order.
	sorted := make([]coverageTrack, len(tracks))
	copy(sorted, tracks)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].TrackID < sorted[j].TrackID })

	report := &PIDCoverageReport{
		TotalTracks:            len(sorted),
		DBBookFilePIDs:         len(filePIDs),
		DBBookPIDs:             len(bookPIDs),
		UnresolvedSmartSamples: []UnresolvedPIDSample{},
		UnresolvedSamples:      []UnresolvedPIDSample{},
	}

	// Playlist shape, and the Track IDs referenced by smart-with-items playlists.
	smartRefs := make(map[int]struct{})
	for _, p := range playlists {
		if p.IsFolder {
			continue
		}
		report.Playlists++
		switch {
		case p.IsSmart && len(p.Items) > 0:
			report.SmartPlaylists++
			report.SmartWithItems++
			report.SmartItemRefs += len(p.Items)
			for _, id := range p.Items {
				smartRefs[id] = struct{}{}
			}
		case p.IsSmart:
			report.SmartPlaylists++
			report.SmartCriteriaOnly++
		case len(p.Items) > 0:
			report.RegularWithItems++
		}
	}
	report.SmartDistinctTrackIDs = len(smartRefs)

	seenTrackID := make(map[int]struct{}, len(sorted))
	for _, t := range sorted {
		seenTrackID[t.TrackID] = struct{}{}
		_, referenced := smartRefs[t.TrackID]
		if t.PID == "" {
			report.TracksWithoutPID++
			if referenced {
				report.SmartReferencedWithoutPID++
			}
			continue
		}
		_, inFile := filePIDs[t.PID]
		_, inBook := bookPIDs[t.PID]
		tally(&report.Library, inFile, inBook)
		if referenced {
			tally(&report.SmartReferenced, inFile, inBook)
		}
		if inFile || inBook {
			continue
		}
		s := UnresolvedPIDSample(t) // identical field sets; conversion ignores tags
		if len(report.UnresolvedSamples) < pidCoverageSampleLimit {
			report.UnresolvedSamples = append(report.UnresolvedSamples, s)
		}
		if referenced && len(report.UnresolvedSmartSamples) < pidCoverageSampleLimit {
			report.UnresolvedSmartSamples = append(report.UnresolvedSmartSamples, s)
		}
	}
	for id := range smartRefs {
		if _, ok := seenTrackID[id]; !ok {
			report.SmartDanglingTrackIDs++
		}
	}

	finishBucket(&report.Library)
	finishBucket(&report.SmartReferenced)
	return report, nil
}

func tally(b *PIDCoverageBucket, inFile, inBook bool) {
	b.Tracks++
	if inFile {
		b.ResolvedFileLevel++
	}
	if inBook {
		b.ResolvedBookLevel++
	}
	if inFile || inBook {
		b.ResolvedEither++
	}
}

func finishBucket(b *PIDCoverageBucket) {
	b.Unresolved = b.Tracks - b.ResolvedEither
	b.ResolvedFileLevelPercent = coveragePercent(b.ResolvedFileLevel, b.Tracks)
	b.ResolvedBookLevelPercent = coveragePercent(b.ResolvedBookLevel, b.Tracks)
	b.ResolvedEitherPercent = coveragePercent(b.ResolvedEither, b.Tracks)
}
