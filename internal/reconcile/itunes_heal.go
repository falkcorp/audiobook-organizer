// file: internal/reconcile/itunes_heal.go
// version: 1.0.0
// guid: 7f3a1b2c-4d5e-6f7a-8b9c-0d1e2f3a4b5c
// last-edited: 2026-06-25

package reconcile

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
	"howett.net/plist"
)

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
	// Strip scheme and URL-decode.
	s := strings.TrimPrefix(location, "file://localhost")
	s = strings.ReplaceAll(s, "\\", "/")
	if decoded, err := url.PathUnescape(s); err == nil {
		s = decoded
	}
	// s is now /W:/path/... or W:/path/... depending on URL encoding

	// Normalise to no-leading-slash form for prefix matching.
	stripped := strings.TrimPrefix(s, "/")

	// 1. Try configured PathMappings (From = Windows path, To = Linux path).
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

	// 2. Hardcoded W:\ → /mnt/bigdata/books/ (production constant).
	if strings.HasPrefix(stripped, "W:/") {
		return "/mnt/bigdata/books/" + stripped[3:]
	}

	return location // unchanged — caller treats as untranslatable
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
// Returns (path, confidence) where confidence is "high" (≥10), "medium" (>0), "".
// Returns ("", "") when no unique winner can be determined.
func DisambiguateMatch(expectedPath, artist, album string, trackNum int, candidates []string) (string, string) {
	if len(candidates) == 0 {
		return "", ""
	}
	if len(candidates) == 1 {
		return candidates[0], "high"
	}

	// Extract the author directory from the expected iTunes path.
	// Expected form: .../Audiobooks/<AUTHOR>/filename
	parts := strings.Split(expectedPath, "/")
	var expectedAuthor string
	for i, p := range parts {
		if p == "Audiobooks" && i+1 < len(parts) {
			expectedAuthor = strings.ToLower(parts[i+1])
			break
		}
	}

	albumWords := func(s string) []string {
		var words []string
		for _, w := range strings.Fields(strings.ToLower(s)) {
			if len(w) > 3 {
				words = append(words, w)
			}
		}
		return words
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
		for _, w := range albumWords(album) {
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

	// Find max and second-max.
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
	return "", "" // tied — cannot pick safely
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

// RunITunesHeal is the entry point called by the maintenance plugin op.
//
// It parses the iTunes XML, builds a filename index of the organized library
// and import directories, then fans out healing across missing tracks using
// RunItems with 16 workers. Total runtime is dominated by the index walk
// (~10–30s for 138K files) and is otherwise I/O-free per track (map lookup
// + ZFS reflink).
func RunITunesHeal(ctx context.Context, reporter sdk.Reporter, params json.RawMessage) error {
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

	log := reporter.Logger()
	log.Info("itunes-heal: parsing iTunes XML", "path", cfg.LibraryReadPath)

	// Step 1: Parse iTunes XML.
	allTracks, err := ParseITunesXML(cfg.LibraryReadPath)
	if err != nil {
		return fmt.Errorf("parse iTunes XML: %w", err)
	}
	log.Info("itunes-heal: parsed tracks", "count", len(allTracks))

	// Step 2: Translate paths and separate missing from already-present.
	extSet := map[string]bool{
		".m4b": true, ".mp3": true, ".m4a": true,
		".flac": true, ".aac": true, ".ogg": true, ".wma": true,
	}
	var missing []iTunesTrack
	alreadyGood := 0
	untranslatable := 0
	for _, t := range allTracks {
		expected := TranslateITunesPath(t.Location, cfg.PathMappings)
		if expected == t.Location {
			untranslatable++ // translation failed — skip silently
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

	// Step 3: Build filename index of organized library + newbooks.
	indexDirs := []string{rootDir}
	newbooks := filepath.Join(filepath.Dir(rootDir), "newbooks")
	if _, err := os.Stat(newbooks); err == nil {
		indexDirs = append(indexDirs, newbooks)
	}
	log.Info("itunes-heal: building file index", "dirs", indexDirs)
	fileIndex := BuildFileIndex(indexDirs, extSet)
	log.Info("itunes-heal: file index built", "unique_filenames", len(fileIndex))

	// Step 4: Skip to checkpoint if resuming.
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

	// Step 5: Fan out healing — 16 workers, ErrModeCollect (non-fatal per item).
	var (
		healed    atomic.Int64
		ambiguous atomic.Int64
		notFound  atomic.Int64
		healErrs  atomic.Int64
	)

	err = registry.RunItems(ctx, reporter, slice,
		func(ctx context.Context, t iTunesTrack) error {
			expected := TranslateITunesPath(t.Location, cfg.PathMappings)

			// Re-check in case a parallel worker already healed this path.
			if _, err := os.Stat(expected); err == nil {
				healed.Add(1)
				return nil
			}

			candidates := fileIndex[filepath.Base(expected)]
			src, _ := DisambiguateMatch(expected, t.Artist, t.Album, t.TrackNumber, candidates)

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
			return nil // all outcomes non-fatal; keep processing
		},
		registry.RunItemsOptions{
			Concurrency:    16,
			ErrMode:        registry.ErrModeCollect,
			ProgressOffset: startIdx,
			ProgressTotal:  len(missing),
			Label: func(i, total int) string {
				return fmt.Sprintf("Track %d/%d  healed=%d ambig=%d missing=%d err=%d",
					i+1, total, healed.Load(), ambiguous.Load(), notFound.Load(), healErrs.Load())
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
		Errors:      int(healErrs.Load()),
	}
	resultJSON, _ := json.Marshal(result)
	log.Info("itunes-heal: complete", "result", string(resultJSON))
	_ = reporter.UpdateProgress(len(missing), len(missing), fmt.Sprintf(
		"Done: healed=%d  ambig=%d  not_found=%d  err=%d",
		result.Healed, result.Ambiguous, result.NotFound, result.Errors,
	))
	return nil
}
