// file: internal/reconcile/itunes_heal.go
// version: 1.6.0
// guid: 7f3a1b2c-4d5e-6f7a-8b9c-0d1e2f3a4b5c
// last-edited: 2026-07-18

package reconcile

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/falkcorp/audiobook-organizer/internal/acoustid"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/dedup"
	"github.com/falkcorp/audiobook-organizer/internal/fingerprint"
	"github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/internal/transcribe"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
	"howett.net/plist"
)

// resolverFailureCounters (H3, 2026-07 error-correction sweep) tracks
// per-resolver failures inside resolveAmbiguousByAcoustID and
// resolveAmbiguousByTranscription that used to be bare `continue`s —
// indistinguishable from "this candidate just isn't a match", silently
// inflating the `ambiguous` bucket in RunITunesHeal's summary. Shared by
// pointer across the RunItems worker pool (Concurrency: 16), so every field
// is an atomic counter.
type resolverFailureCounters struct {
	fpcalcFailed         atomic.Int64 // FileWholeFingerprint error/nil result
	acoustidLookupFailed atomic.Int64 // ac.Lookup error or empty title
	whisperFailed        atomic.Int64 // TranscribeFirst30s error/empty text
}

// warnRateLimited logs msg at Warn on the first occurrence and every 100th
// occurrence thereafter (n is the post-increment count), so a systemic
// failure (fpcalc missing, AcoustID down, Whisper model unavailable) is
// visible without spamming one line per track across a multi-thousand-track
// heal run.
func warnRateLimited(n int64, msg string, args ...any) {
	if n == 1 || n%100 == 0 {
		slog.Warn(msg, append(args, "occurrence", n)...)
	}
}

// iTunesTrack is the minimal set of fields we need from an iTunes XML track entry.
type iTunesTrack struct {
	Name         string // track/chapter title
	Artist       string // narrator or author
	Album        string // book title (used for disambiguation)
	Location     string // file://localhost/W:/... URL
	PersistentID string // unique iTunes PID
	TrackNumber  int    // chapter order number
}

// ITunesHealResult summarises a completed heal run.
type ITunesHealResult struct {
	TotalTracks int `json:"total_tracks"`
	Missing     int `json:"missing"`      // tracks whose expected path didn't exist
	Healed      int `json:"healed"`       // successfully reflinked/copied
	AlreadyGood int `json:"already_good"` // expected path already existed
	Ambiguous   int `json:"ambiguous"`    // multiple candidates, could not pick one
	NotFound    int `json:"not_found"`    // no candidate on disk at all
	Merged      int `json:"merged"`       // duplicate books collapsed during heal
	Errors      int `json:"errors"`       // reflink/copy failures
}

// ITunesHealParams is the checkpoint state for a heal run.
type ITunesHealParams struct {
	LastProcessedPID string `json:"last_processed_pid,omitempty"`
}

// ParseITunesXML reads the iTunes Library.xml plist and returns all audio tracks.
func ParseITunesXML(xmlPath string) ([]iTunesTrack, error) {
	f, err := os.Open(xmlPath)
	if err != nil {
		return nil, fmt.Errorf("open iTunes XML: %w", err)
	}
	defer f.Close()

	var lib struct {
		Tracks map[string]map[string]any `plist:"Tracks"`
	}
	if err := plist.NewDecoder(f).Decode(&lib); err != nil {
		return nil, fmt.Errorf("parse iTunes plist: %w", err)
	}

	extSet := map[string]bool{
		".m4b": true, ".mp3": true, ".m4a": true,
		".flac": true, ".aac": true, ".ogg": true, ".wma": true,
	}
	var tracks []iTunesTrack
	for _, raw := range lib.Tracks {
		loc, _ := raw["Location"].(string)
		if loc == "" {
			continue
		}
		if !extSet[strings.ToLower(filepath.Ext(loc))] {
			continue
		}
		t := iTunesTrack{Location: loc}
		t.Name, _ = raw["Name"].(string)
		t.Artist, _ = raw["Artist"].(string)
		t.Album, _ = raw["Album"].(string)
		t.PersistentID, _ = raw["Persistent ID"].(string)
		if n, ok := raw["Track Number"].(uint64); ok {
			t.TrackNumber = int(n)
		}
		tracks = append(tracks, t)
	}
	return tracks, nil
}

// TranslateITunesPath converts a file://localhost/W:/... URL to its Linux path.
//
// Translation priority:
//  1. PathMappings from config (first match wins, From is the Windows path prefix).
//  2. Hardcoded W:\ → /mnt/bigdata/books/ (production constant; W:\ IS the NAS root).
//
// Returns the original location unchanged if no translation applies.
func TranslateITunesPath(location string, mappings []config.ITunesPathMap) string {
	s := strings.TrimPrefix(location, "file://localhost")
	s = strings.ReplaceAll(s, "\\", "/")
	if decoded, err := url.PathUnescape(s); err == nil {
		s = decoded
	}
	stripped := strings.TrimPrefix(s, "/")

	for _, m := range mappings {
		from := strings.ReplaceAll(m.From, "\\", "/")
		if from == "" || m.To == "" {
			continue
		}
		from = strings.TrimPrefix(from, "/")
		if strings.HasPrefix(stripped, from) {
			return m.To + stripped[len(from):]
		}
	}

	if strings.HasPrefix(stripped, "W:/") {
		return "/mnt/bigdata/books/" + stripped[3:]
	}

	return location
}

// BuildFileIndex walks dirs in parallel and returns filename → []absolutepath.
func BuildFileIndex(dirs []string, extSet map[string]bool) map[string][]string {
	var mu sync.Mutex
	index := make(map[string][]string, 200_000)

	var wg sync.WaitGroup
	for _, dir := range dirs {
		dir := dir
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
				if err != nil || info.IsDir() {
					return nil
				}
				if !extSet[strings.ToLower(filepath.Ext(path))] {
					return nil
				}
				base := filepath.Base(path)
				mu.Lock()
				index[base] = append(index[base], path)
				mu.Unlock()
				return nil
			})
		}()
	}
	wg.Wait()
	return index
}

// DisambiguateMatch picks the best candidate for a track's expected path.
//
// Scoring (higher = better):
//   - Author directory in candidate path: 10 pts
//   - Album/book title word in candidate path: 1 pt each
//   - Track number embedded in candidate filename: 5 pts
//
// Returns ("", "") when no unique winner can be determined.
func DisambiguateMatch(expectedPath, artist, album string, trackNum int, candidates []string) (string, string) {
	if len(candidates) == 0 {
		return "", ""
	}
	if len(candidates) == 1 {
		return candidates[0], "high"
	}

	parts := strings.Split(expectedPath, "/")
	var expectedAuthor string
	for i, p := range parts {
		if p == "Audiobooks" && i+1 < len(parts) {
			expectedAuthor = strings.ToLower(parts[i+1])
			break
		}
	}

	type entry struct {
		path  string
		score int
	}
	scored := make([]entry, len(candidates))
	for i, h := range candidates {
		hl := strings.ToLower(h)
		score := 0
		if expectedAuthor != "" && strings.Contains(hl, expectedAuthor) {
			score += 10
		}
		for _, w := range titleWords(album) {
			if strings.Contains(hl, w) {
				score++
			}
		}
		if trackNum > 0 {
			base := strings.ToLower(filepath.Base(h))
			if strings.Contains(base, fmt.Sprintf("%03d", trackNum)) ||
				strings.Contains(base, fmt.Sprintf("%02d", trackNum)) {
				score += 5
			}
		}
		scored[i] = entry{h, score}
	}

	best, second := scored[0], entry{score: -1}
	for _, e := range scored[1:] {
		if e.score > best.score {
			second = best
			best = e
		} else if e.score > second.score {
			second = e
		}
	}
	if best.score > second.score {
		conf := "medium"
		if best.score >= 10 {
			conf = "high"
		}
		return best.path, conf
	}
	return "", ""
}

// resolveAmbiguousByDB looks up stored AcoustID fingerprints for each candidate
// from the DB (no fpcalc — data is already there from the backfill).
//
// If all candidates are acoustically identical (similarity ≥ 0.9), they are
// duplicate books from the organize bug. MergeBooks collapses them and returns
// the surviving path. Acoustically distinct candidates return ("", 0).
func resolveAmbiguousByDB(ctx context.Context, store database.Store, candidates []string) (string, int) {
	if len(candidates) == 0 {
		return "", 0
	}
	type row struct {
		bookID string
		fp     []byte
		path   string
	}
	rows := make([]row, 0, len(candidates))
	for _, path := range candidates {
		f, err := store.GetBookFileByPath(path)
		if err != nil || f == nil || len(f.AcoustIDFingerprint) == 0 {
			return "", 0
		}
		rows = append(rows, row{f.BookID, f.AcoustIDFingerprint, path})
	}

	ref := rows[0].fp
	for _, r := range rows[1:] {
		sim, err := fingerprint.WholeFileSimilarity(ref, r.fp)
		if err != nil || sim < 0.9 {
			return "", 0
		}
	}

	keepID := rows[0].bookID
	var dupIDs []string
	seen := map[string]bool{keepID: true}
	for _, r := range rows[1:] {
		if !seen[r.bookID] {
			dupIDs = append(dupIDs, r.bookID)
			seen[r.bookID] = true
		}
	}
	if len(dupIDs) > 0 {
		if _, err := dedup.MergeBooks(ctx, store, "", keepID, dupIDs, nil); err != nil {
			return "", 0
		}
	}
	return rows[0].path, len(dupIDs)
}

// resolveAmbiguousByBookMeta looks up each candidate's Book in the DB and
// scores its title and file path (which encodes the author directory) against
// the iTunes track's Album and Artist via word overlap. Pure DB — no API calls.
func resolveAmbiguousByBookMeta(store database.Store, track iTunesTrack, candidates []string) string {
	albumWords := titleWords(track.Album)
	artistWords := titleWords(track.Artist)
	if len(albumWords) == 0 {
		return ""
	}

	type entry struct {
		path  string
		score int
	}
	scored := make([]entry, 0, len(candidates))
	for _, path := range candidates {
		bf, err := store.GetBookFileByPath(path)
		if err != nil || bf == nil {
			continue
		}
		book, err := store.GetBookByID(bf.BookID)
		if err != nil || book == nil {
			continue
		}

		score := 0
		titleL := strings.ToLower(book.Title)
		for _, w := range albumWords {
			if strings.Contains(titleL, w) {
				score += 2
			}
		}
		// book.FilePath encodes the author directory; extract it for artist matching.
		pathL := strings.ToLower(book.FilePath)
		for _, w := range artistWords {
			if strings.Contains(pathL, w) {
				score++
			}
		}
		scored = append(scored, entry{path, score})
	}

	if len(scored) == 0 {
		return ""
	}
	best, second := scored[0], entry{score: -1}
	for _, e := range scored[1:] {
		if e.score > best.score {
			second = best
			best = e
		} else if e.score > second.score {
			second = e
		}
	}
	if best.score > second.score && best.score >= 4 {
		return best.path
	}
	return ""
}

// resolveAmbiguousByAcoustID fingerprints each candidate (using the stored DB
// fingerprint when available, falling back to fpcalc), submits to AcoustID,
// and picks the candidate whose returned title/artist best matches the iTunes
// track. Only invoked when layers 1–3 fail and an API key is configured.
//
// failures may be nil (tests / call sites that don't care about the counters);
// when non-nil, fpcalc failures and AcoustID lookup failures are tallied and
// rate-limited-Warn logged instead of silently `continue`-ing — before H3
// these were indistinguishable from "no match", inflating `ambiguous`.
func resolveAmbiguousByAcoustID(ctx context.Context, store database.Store, ac *acoustid.Client, track iTunesTrack, candidates []string, failures *resolverFailureCounters) string {
	albumWords := titleWords(track.Album)
	artistWords := titleWords(track.Artist)

	type entry struct {
		path  string
		score int
	}
	scored := make([]entry, 0, len(candidates))

	for _, path := range candidates {
		select {
		case <-ctx.Done():
			return ""
		default:
		}

		var rawFP []byte
		var durationSec int

		if bf, err := store.GetBookFileByPath(path); err == nil && bf != nil && len(bf.AcoustIDFingerprint) > 0 {
			rawFP = bf.AcoustIDFingerprint
			durationSec = int(bf.AcoustIDFingerprintDurationSec)
		} else {
			wf, err := fingerprint.FileWholeFingerprint(path)
			if err != nil || wf == nil {
				if failures != nil {
					n := failures.fpcalcFailed.Add(1)
					warnRateLimited(n, "itunes-heal: fpcalc fallback failed", "path", path, "err", err)
				}
				continue
			}
			rawFP = wf.Raw
			durationSec = int(wf.DurationSec)
		}

		encoded := fingerprint.EncodeWholeFingerprint(rawFP)
		result, err := ac.Lookup(ctx, encoded, durationSec)
		if err != nil || result.Title == "" {
			if failures != nil {
				n := failures.acoustidLookupFailed.Add(1)
				warnRateLimited(n, "itunes-heal: AcoustID lookup failed", "path", path, "err", err)
			}
			continue
		}

		score := 0
		titleL := strings.ToLower(result.Title)
		for _, w := range albumWords {
			if strings.Contains(titleL, w) {
				score += 2
			}
		}
		for _, artist := range result.Artists {
			artistL := strings.ToLower(artist)
			for _, w := range artistWords {
				if strings.Contains(artistL, w) {
					score++
				}
			}
		}
		scored = append(scored, entry{path, score})
	}

	if len(scored) == 0 {
		return ""
	}
	best, second := scored[0], entry{score: -1}
	for _, e := range scored[1:] {
		if e.score > best.score {
			second = best
			best = e
		} else if e.score > second.score {
			second = e
		}
	}
	if best.score > second.score && best.score >= 4 {
		return best.path
	}
	return ""
}

// resolveNotFoundByPID looks up the track's iTunes Persistent ID in the DB to
// find the book's current file path. This is the most reliable not-found
// resolver: if the file was organized to a different name/location, the PID
// stored on the BookFile points directly to it — no filesystem scan needed.
func resolveNotFoundByPID(store database.Store, pid string) string {
	if pid == "" || store == nil {
		return ""
	}
	bf, err := store.GetBookFileByPID(pid)
	if err != nil || bf == nil || bf.FilePath == "" {
		return ""
	}
	if _, err := os.Stat(bf.FilePath); err != nil {
		return ""
	}
	return bf.FilePath
}

// resolveAmbiguousByTranscription transcribes the first 30 seconds of each
// candidate's first audio file and parses the "TITLE by AUTHOR. Read by
// NARRATOR." announcement that every commercial audiobook opens with. The
// candidate whose parsed title/author best matches the iTunes track Album/Artist
// wins — this is definitive when all other layers have tied.
//
// Only called when there are ≤5 candidates (cost: ~5–15 s per candidate with a
// local Whisper model; more candidates indicate a structural problem better
// handled by the triage op).
//
// failures may be nil; when non-nil, Whisper transcription failures are
// tallied and rate-limited-Warn logged instead of a silent `continue` — see
// resolveAmbiguousByAcoustID's doc comment for why this matters (H3).
func resolveAmbiguousByTranscription(ctx context.Context, store database.Store, track iTunesTrack, candidates []string, failures *resolverFailureCounters) string {
	if len(candidates) == 0 || len(candidates) > 5 {
		return ""
	}

	type entry struct {
		path  string
		score int
	}
	scored := make([]entry, 0, len(candidates))

	for _, candidatePath := range candidates {
		select {
		case <-ctx.Done():
			return ""
		default:
		}

		// Find the first audio file of this candidate's book.
		firstFile := candidatePath
		if bf, err := store.GetBookFileByPath(candidatePath); err == nil && bf != nil {
			files, err := store.GetBookFiles(bf.BookID)
			if err == nil && len(files) > 0 {
				if f := pickFirstFile(files); f != "" {
					firstFile = f
				}
			}
		}

		text, err := transcribe.TranscribeFirst30s(ctx, firstFile)
		if err != nil || text == "" {
			if failures != nil {
				n := failures.whisperFailed.Add(1)
				warnRateLimited(n, "itunes-heal: transcription failed", "path", firstFile, "err", err)
			}
			continue
		}

		fields := transcribe.ParseAudiobookIntro(text)
		score := fields.MatchesTrack(track.Album, track.Artist)
		if score > 0 {
			scored = append(scored, entry{candidatePath, score})
		}
	}

	if len(scored) == 0 {
		return ""
	}
	best, second := scored[0], entry{score: -1}
	for _, e := range scored[1:] {
		if e.score > best.score {
			second = best
			best = e
		} else if e.score > second.score {
			second = e
		}
	}
	if best.score > second.score {
		return best.path
	}
	return ""
}

// pickFirstFile returns the FilePath of the first audio file in the slice
// (lowest TrackNumber, then alphabetical). Returns "" if none are audio files.
func pickFirstFile(files []database.BookFile) string {
	extSet := map[string]bool{
		".m4b": true, ".mp3": true, ".m4a": true,
		".flac": true, ".aac": true, ".ogg": true, ".wma": true,
	}
	best := ""
	bestTrack := -1
	for _, f := range files {
		if !extSet[strings.ToLower(filepath.Ext(f.FilePath))] || f.FilePath == "" {
			continue
		}
		tn := f.TrackNumber
		if best == "" || tn < bestTrack || (tn == bestTrack && f.FilePath < best) {
			best = f.FilePath
			bestTrack = tn
		}
	}
	return best
}

// fuzzyFindByAlbum searches the whole file index for a file whose PATH
// contains enough words from the iTunes album/artist to strongly suggest the
// same book. Used for not-found tracks (zero filename matches in the index).
func fuzzyFindByAlbum(track iTunesTrack, fileIndex map[string][]string) string {
	albumWords := titleWords(track.Album)
	if len(albumWords) < 2 {
		return ""
	}

	type entry struct {
		path  string
		score int
	}
	var best entry

	for _, paths := range fileIndex {
		for _, path := range paths {
			pl := strings.ToLower(path)
			score := 0
			for _, w := range albumWords {
				if strings.Contains(pl, w) {
					score += 2
				}
			}
			for _, w := range titleWords(track.Artist) {
				if strings.Contains(pl, w) {
					score++
				}
			}
			if track.TrackNumber > 0 {
				base := strings.ToLower(filepath.Base(path))
				if strings.Contains(base, fmt.Sprintf("%03d", track.TrackNumber)) ||
					strings.Contains(base, fmt.Sprintf("%02d", track.TrackNumber)) {
					score += 5
				}
			}
			if score > best.score {
				best = entry{path, score}
			}
		}
	}
	// Require at least (all album words × 2) − 2 to avoid short-title false positives.
	minScore := len(albumWords)*2 - 2
	if minScore < 8 {
		minScore = 8
	}
	if best.score >= minScore {
		return best.path
	}
	return ""
}

// healTrack creates a reflink (ZFS COW) from src to dst, falling back to a
// regular copy when reflink fails (cross-subvol or non-ZFS).
func healTrack(dst, src string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o775); err != nil {
		return fmt.Errorf("mkdir: %w", err)
	}
	if err := exec.Command("cp", "--reflink=always", src, dst).Run(); err == nil {
		return nil
	}
	if out, err := exec.Command("cp", src, dst).CombinedOutput(); err != nil {
		return fmt.Errorf("cp: %w (%s)", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// titleWords returns lowercase words >3 chars from s, for overlap scoring.
func titleWords(s string) []string {
	var out []string
	for _, w := range strings.Fields(strings.ToLower(s)) {
		if len(w) > 3 {
			out = append(out, w)
		}
	}
	return out
}

// RunITunesHeal is the entry point called by the maintenance plugin op.
//
// Disambiguation pipeline (each layer only runs if the previous one tied):
//  1. DisambiguateMatch — author dir + album title words + track number in filename
//  2. resolveAmbiguousByDB — stored chromaprint byte comparison (no fpcalc);
//     identical audio → MergeBooks + heal
//  3. resolveAmbiguousByBookMeta — DB book title / file-path word overlap vs iTunes
//     Album/Artist; clear winner → heal (no API calls)
//  4. resolveAmbiguousByAcoustID — stored fingerprint (fallback fpcalc) →
//     AcoustID title+artists lookup → metadata match; rate-limited, API key required
//  5. fuzzyFindByAlbum — full index path-content scan for tracks with zero filename
//     matches; finds files in correctly-named book folders with different filenames
func RunITunesHeal(ctx context.Context, store database.Store, reporter sdk.Reporter, params json.RawMessage) error {
	cfg := config.AppConfig.ITunes
	if cfg.LibraryReadPath == "" {
		return fmt.Errorf("itunes.library_read_path not configured")
	}
	rootDir := config.AppConfig.RootDir
	if rootDir == "" {
		return fmt.Errorf("root_dir not configured")
	}

	var cp ITunesHealParams
	if len(params) > 0 {
		_ = json.Unmarshal(params, &cp)
	}

	// AcoustID client for layer 4 — nil when no key configured (layers 1–3 still work).
	var ac *acoustid.Client
	if key := config.AppConfig.AcoustIDAPIKey; key != "" {
		ac = acoustid.NewClient(key)
	}

	log := reporter.Logger()
	log.Info("itunes-heal: parsing iTunes XML", "path", cfg.LibraryReadPath)

	allTracks, err := ParseITunesXML(cfg.LibraryReadPath)
	if err != nil {
		return fmt.Errorf("parse iTunes XML: %w", err)
	}
	log.Info("itunes-heal: parsed tracks", "count", len(allTracks))

	extSet := map[string]bool{
		".m4b": true, ".mp3": true, ".m4a": true,
		".flac": true, ".aac": true, ".ogg": true, ".wma": true,
	}
	var missing []iTunesTrack
	alreadyGood, untranslatable := 0, 0
	for _, t := range allTracks {
		expected := TranslateITunesPath(t.Location, cfg.PathMappings)
		if expected == t.Location {
			untranslatable++
			continue
		}
		if _, err := os.Stat(expected); err == nil {
			alreadyGood++
			continue
		}
		if !extSet[strings.ToLower(filepath.Ext(expected))] {
			continue
		}
		missing = append(missing, t)
	}

	_ = reporter.UpdateProgress(0, 1, fmt.Sprintf(
		"iTunes XML: %d tracks | %d good | %d missing | %d untranslatable — building file index…",
		len(allTracks), alreadyGood, len(missing), untranslatable,
	))
	log.Info("itunes-heal: path check complete",
		"missing", len(missing), "already_good", alreadyGood, "untranslatable", untranslatable)

	if len(missing) == 0 {
		_ = reporter.UpdateProgress(1, 1, fmt.Sprintf(
			"All %d iTunes tracks already present — nothing to heal", len(allTracks),
		))
		return nil
	}

	// Build file index: rootDir + every sibling audio directory under booksRoot.
	// Skip the iTunes source tree (itunes/) and non-audio directories.
	booksRoot := filepath.Dir(rootDir)
	indexDirs := []string{rootDir}
	// itunes/ is intentionally NOT skipped here — original iTunes media files
	// may still live there and are valid heal sources. Only the library scanner
	// must avoid it (to prevent importing iTunes source files as library books).
	skipDirs := map[string]bool{
		rootDir:                                       true,
		filepath.Join(booksRoot, "bkup"):              true,
		filepath.Join(booksRoot, "logs"):              true,
		filepath.Join(booksRoot, "playlists"):         true,
		filepath.Join(booksRoot, "snapshot-list-v1"): true,
	}
	if entries, err := os.ReadDir(booksRoot); err == nil {
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			full := filepath.Join(booksRoot, e.Name())
			if !skipDirs[full] {
				indexDirs = append(indexDirs, full)
			}
		}
	}
	log.Info("itunes-heal: building file index", "dirs", len(indexDirs))
	fileIndex := BuildFileIndex(indexDirs, extSet)
	log.Info("itunes-heal: file index built", "unique_filenames", len(fileIndex))

	startIdx := 0
	if cp.LastProcessedPID != "" {
		for i, t := range missing {
			if t.PersistentID == cp.LastProcessedPID {
				startIdx = i + 1
				break
			}
		}
	}
	slice := missing[startIdx:]

	var (
		healed    atomic.Int64
		ambiguous atomic.Int64
		notFound  atomic.Int64
		merged    atomic.Int64
		healErrs  atomic.Int64
	)
	var resolverFailures resolverFailureCounters

	err = registry.RunItems(ctx, reporter, slice,
		func(ctx context.Context, t iTunesTrack) error {
			expected := TranslateITunesPath(t.Location, cfg.PathMappings)

			if _, err := os.Stat(expected); err == nil {
				healed.Add(1)
				return nil
			}

			candidates := fileIndex[filepath.Base(expected)]

			// Layer 1: metadata scoring (author dir + title words + track number).
			src, _ := DisambiguateMatch(expected, t.Artist, t.Album, t.TrackNumber, candidates)

			// Layer 2: stored fingerprint comparison — no fpcalc.
			if src == "" && len(candidates) > 0 && store != nil {
				var n int
				src, n = resolveAmbiguousByDB(ctx, store, candidates)
				merged.Add(int64(n))
			}

			// Layer 3: DB book title / path word-overlap — no API calls.
			if src == "" && len(candidates) > 0 && store != nil {
				src = resolveAmbiguousByBookMeta(store, t, candidates)
			}

			// Layer 4: AcoustID title lookup — only when API key is configured.
			if src == "" && len(candidates) > 0 && ac != nil {
				src = resolveAmbiguousByAcoustID(ctx, store, ac, t, candidates, &resolverFailures)
			}

			// Layer 5: PID lookup — authoritative for both ambiguous and not-found.
			// The BookFile.ITunesPersistentID index points directly to the current
			// on-disk path, bypassing filename/metadata guessing entirely.
			if src == "" && t.PersistentID != "" && store != nil {
				src = resolveNotFoundByPID(store, t.PersistentID)
			}

			// Layer 6: transcription — Whisper on the first 30s of each candidate's
			// first file; parses "TITLE by AUTHOR. Read by NARRATOR." and picks the
			// candidate whose title/author best matches the iTunes track. Definitive
			// but expensive; only runs for ≤5 still-ambiguous candidates.
			if src == "" && len(candidates) > 0 && store != nil {
				src = resolveAmbiguousByTranscription(ctx, store, t, candidates, &resolverFailures)
			}

			// Layer 7: fuzzy album/artist path scan for zero-candidate not-found tracks.
			if src == "" && len(candidates) == 0 {
				src = fuzzyFindByAlbum(t, fileIndex)
			}

			switch {
			case src == "" && len(candidates) > 0:
				ambiguous.Add(1)
				log.Debug("itunes-heal: ambiguous", "expected", expected, "candidates", len(candidates))
			case src == "":
				notFound.Add(1)
				log.Debug("itunes-heal: not found on disk", "expected", expected)
			default:
				if err := healTrack(expected, src); err != nil {
					healErrs.Add(1)
					log.Warn("itunes-heal: reflink failed", "dst", expected, "src", src, "err", err)
				} else {
					healed.Add(1)
				}
			}
			return nil
		},
		registry.RunItemsOptions{
			Concurrency:    16,
			ErrMode:        registry.ErrModeCollect,
			ProgressOffset: startIdx,
			ProgressTotal:  len(missing),
			Label: func(i, total int) string {
				return fmt.Sprintf("Track %d/%d  healed=%d merged=%d ambig=%d missing=%d err=%d  resolver_fails(fpcalc=%d acoustid=%d whisper=%d)",
					i+1, total, healed.Load(), merged.Load(), ambiguous.Load(), notFound.Load(), healErrs.Load(),
					resolverFailures.fpcalcFailed.Load(), resolverFailures.acoustidLookupFailed.Load(), resolverFailures.whisperFailed.Load())
			},
		},
	)
	if err != nil {
		return err
	}

	result := ITunesHealResult{
		TotalTracks: len(allTracks),
		Missing:     len(missing),
		AlreadyGood: alreadyGood,
		Healed:      int(healed.Load()),
		Ambiguous:   int(ambiguous.Load()),
		NotFound:    int(notFound.Load()),
		Merged:      int(merged.Load()),
		Errors:      int(healErrs.Load()),
	}
	resultJSON, _ := json.Marshal(result)
	log.Info("itunes-heal: complete", "result", string(resultJSON),
		"resolver_fpcalc_failed", resolverFailures.fpcalcFailed.Load(),
		"resolver_acoustid_failed", resolverFailures.acoustidLookupFailed.Load(),
		"resolver_whisper_failed", resolverFailures.whisperFailed.Load(),
	)
	_ = reporter.UpdateProgress(len(missing), len(missing), fmt.Sprintf(
		"Done: healed=%d  merged=%d  ambig=%d  not_found=%d  err=%d",
		result.Healed, result.Merged, result.Ambiguous, result.NotFound, result.Errors,
	))
	return nil
}
