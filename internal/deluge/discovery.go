// file: internal/deluge/discovery.go
// version: 1.1.1
// guid: a1b2c3d4-e5f6-7890-abcd-ef1234567890
// last-edited: 2026-07-16
//
// Four-tier matching to decide if a labeled Deluge torrent is already in
// the library — run in order, stop on first hit:
//
//  1. Hash    — GetBookVersionByTorrentHash: exact, works regardless of where
//               the file moved after organize.
//  2. Path    — filepath.Join(save_path, name) is a prefix of Book.FilePath:
//               catches books still sitting in the download directory.
//  3. Title   — parse the torrent name into candidate title strings, check
//               against a normalised set of Book.Title values. When a title
//               match fires, Tier 4 SHA-verifies the actual files.
//  4. SHA256  — SHA256 each audio file found under content_path, query
//               GetBookByFileHash for each. A hash hit confirms the fuzzy
//               title match; a miss means it's a different edition/version.
//
// A torrent that passes all four tiers is returned as a discovery candidate.

package deluge

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"unicode"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/fingerprint"
)

// DiscoveredTorrent is a Deluge torrent not yet tracked in the library.
type DiscoveredTorrent struct {
	Hash        string  `json:"hash"`
	Name        string  `json:"name"`
	SavePath    string  `json:"save_path"`
	ContentPath string  `json:"content_path"` // filepath.Join(save_path, name) — import this
	Label       string  `json:"label"`
	State       string  `json:"state"`
	Progress    float64 `json:"progress"`
	TotalSize   int64   `json:"total_size"`
	MatchTier   string  `json:"match_tier,omitempty"` // debug: which tier matched (empty = new)
}

// LibraryIndex is a pre-built lookup structure used by all match tiers.
type LibraryIndex struct {
	// Tier 2: normalised current file paths → present
	Paths map[string]struct{}
	// Tier 3: normalised book titles → present
	Titles map[string]struct{}
}

// BookLister is the narrow interface needed to build a library index.
type BookLister interface {
	GetAllBooksCore(limit, offset int) ([]database.BookCore, error)
}

// BuildLibraryIndex builds a LibraryIndex from all books in the store.
func BuildLibraryIndex(store BookLister) LibraryIndex {
	idx := LibraryIndex{
		Paths:  make(map[string]struct{}),
		Titles: make(map[string]struct{}),
	}
	books, err := store.GetAllBooksCore(0, 0)
	if err != nil {
		slog.Warn("deluge discovery failed to load books", "err", err)
		return idx
	}
	for _, b := range books {
		if b.FilePath != "" {
			idx.Paths[b.FilePath] = struct{}{}
		}
		if b.Title != "" {
			idx.Titles[NormalizeTitle(b.Title)] = struct{}{}
		}
	}
	return idx
}

// ContentFingerprintStore is the subset of database.Store needed for
// content fingerprint / hash lookups.
type ContentFingerprintStore interface {
	GetBookByFileHash(hash string) (*database.Book, error)
	GetBookFileByAcoustID(fp string) (*database.BookFile, error)
	GetBookFileByAcoustIDFuzzy(fp string, minSim float64) (*database.BookFile, error)
}

// DiscoveryStore is the subset of database.Store needed by DiscoverUnimported.
type DiscoveryStore interface {
	BookLister
	GetBookVersionByTorrentHash(hash string) (*database.BookVersion, error)
	ContentFingerprintStore
}

// DiscoverUnimported fetches labeled torrents and returns those not already
// in the library according to the four-tier matching strategy.
func DiscoverUnimported(store DiscoveryStore, client *Client, label string) ([]DiscoveredTorrent, error) {
	torrents, err := client.ListTorrentsByLabel(label)
	if err != nil {
		return nil, err
	}
	if len(torrents) == 0 {
		return []DiscoveredTorrent{}, nil
	}

	idx := BuildLibraryIndex(store)

	var unimported []DiscoveredTorrent
	for _, t := range torrents {
		// Tier 1: torrent hash lookup (O(1), authoritative).
		if t.Hash != "" {
			if ver, _ := store.GetBookVersionByTorrentHash(t.Hash); ver != nil {
				continue // already tracked
			}
		}

		// Tier 2: content path prefix against current file paths.
		contentPath := filepath.Join(t.SavePath, t.Name)
		if IsPathTracked(contentPath, idx.Paths) {
			continue
		}

		// Tier 3: torrent name → title candidates against known titles.
		// When a title match fires, Tier 4 verifies actual file content.
		if IsTitleTracked(t.Name, idx.Titles) {
			if IsContentFingerprintTracked(store, contentPath) {
				continue // same audio stream — already in library
			}
			// Title matched but audio differs → different edition, surface it.
		}

		unimported = append(unimported, DiscoveredTorrent{
			Hash:        t.Hash,
			Name:        t.Name,
			SavePath:    t.SavePath,
			ContentPath: contentPath,
			Label:       t.Label,
			State:       t.State,
			Progress:    t.Progress,
			TotalSize:   t.TotalSize,
		})
	}
	return unimported, nil
}

// IsContentFingerprintTracked walks contentPath for the first audio file,
// fingerprints it with fpcalc, and checks the library via exact then fuzzy
// AcoustID match. Returns true if the audio stream is already tracked.
//
// Falls back to SHA-256 walking when fpcalc is not installed so the pipeline
// is never blocked by a missing dependency.
func IsContentFingerprintTracked(store ContentFingerprintStore, contentPath string) bool {
	if !fingerprint.Available() {
		// fpcalc not installed — fall back to SHA-256 content walk.
		hashLookup := func(hash string) bool {
			b, _ := store.GetBookByFileHash(hash)
			return b != nil
		}
		return IsContentHashTracked(contentPath, hashLookup)
	}

	// Find the first audio file under contentPath to fingerprint.
	var firstAudio string
	_ = filepath.Walk(contentPath, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || firstAudio != "" {
			return nil
		}
		if _, ok := AudioExtensions[strings.ToLower(filepath.Ext(path))]; ok {
			firstAudio = path
		}
		return nil
	})
	if firstAudio == "" {
		return false
	}

	segs, err := fingerprint.FileSegments(firstAudio, 0)
	if err != nil {
		slog.Warn("deluge discovery fingerprint", "firstAudio", firstAudio, "err", err)
		return false
	}
	// Use the intro segment (seg[0]) for exact and fuzzy lookups.
	introFP := segs[0]
	if introFP == "" {
		return false
	}

	// Exact match first (O(1) index lookup).
	if f, _ := store.GetBookFileByAcoustID(introFP); f != nil {
		return true
	}

	// Fuzzy fallback — catches minor encoding variations.
	f, _ := store.GetBookFileByAcoustIDFuzzy(introFP, fingerprint.FuzzyMinSimilarity)
	return f != nil
}

// IsPathTracked returns true if contentPath is a prefix of any known file path.
//
// Callers MUST pass filepath.Join(save_path, torrent_name) — NOT save_path
// alone. A shared download directory is a prefix of everything in it, so
// using raw save_path would mark every torrent as tracked once any file from
// that directory exists in the DB.
func IsPathTracked(contentPath string, known map[string]struct{}) bool {
	if contentPath == "" {
		return false
	}
	prefix := strings.TrimRight(contentPath, "/") + "/"
	for p := range known {
		if strings.HasPrefix(p, prefix) || p == contentPath {
			return true
		}
	}
	return false
}

// IsTitleTracked parses a torrent name into candidate titles and checks each
// against the normalised DB title set.
func IsTitleTracked(torrentName string, titles map[string]struct{}) bool {
	for _, candidate := range ParseTorrentNameCandidates(torrentName) {
		if _, ok := titles[candidate]; ok {
			return true
		}
	}
	return false
}

// AudioExtensions is the set of file extensions we hash for content matching.
var AudioExtensions = map[string]struct{}{
	".m4b": {}, ".m4a": {}, ".mp3": {}, ".flac": {}, ".aax": {},
	".aac": {}, ".ogg": {}, ".opus": {}, ".wav": {},
}

// IsContentHashTracked walks contentPath, SHA256s each audio file, and calls
// lookup for each hash. Returns true as soon as any hash is found in the DB.
func IsContentHashTracked(contentPath string, lookup func(string) bool) bool {
	found := false
	_ = filepath.Walk(contentPath, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || found {
			return nil
		}
		if _, ok := AudioExtensions[strings.ToLower(filepath.Ext(path))]; !ok {
			return nil
		}
		hash, hashErr := SHA256File(path)
		if hashErr != nil {
			slog.Warn("deluge discovery sha256", "path", path, "hashErr", hashErr)
			return nil
		}
		if lookup(hash) {
			found = true
		}
		return nil
	})
	return found
}

// SHA256File returns the hex-encoded SHA256 of a file's contents.
func SHA256File(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// stripBrackets removes common annotation brackets: [Unabridged], {MP3}, (2023).
var stripBrackets = regexp.MustCompile(`[\[\]{}(][^\[\]{}()]*[\]\})]`)

// metaSuffixes are noise tokens appended to torrent names.
var metaSuffixes = regexp.MustCompile(`(?i)\b(unabridged|abridged|m4b|mp3|aax|flac|aac|ogg|retail|repack|\d{4})\b.*$`)

// ParseTorrentNameCandidates returns a set of normalised title strings derived
// from a torrent name. The typical formats handled:
//
//	"Author - Title"              → ["title", "author"]
//	"Title - Author"              → ["title", "author"]
//	"Title by Author [M4B]"       → ["title"]
//	"Author.Title.Year.M4B"       → ["author title"] (dots-as-spaces)
//	"Title (Author) [Unabridged]" → ["title"]
func ParseTorrentNameCandidates(name string) []string {
	seen := make(map[string]struct{})
	add := func(s string) {
		s = NormalizeTitle(s)
		if len(s) >= 3 {
			seen[s] = struct{}{}
		}
	}

	// Strip bracket annotations first.
	clean := stripBrackets.ReplaceAllString(name, " ")
	// Remove file-format / year suffixes.
	clean = metaSuffixes.ReplaceAllString(clean, "")
	clean = strings.TrimSpace(clean)

	// Try dash-separated "Author - Title" or "Title - Author".
	if parts := strings.SplitN(clean, " - ", 2); len(parts) == 2 {
		add(parts[0])
		add(parts[1])
	}

	// Try "Title by Author".
	if idx := strings.Index(strings.ToLower(clean), " by "); idx > 0 {
		add(clean[:idx])
	}

	// Try dot-separated names (common for scene releases).
	if strings.ContainsRune(clean, '.') && !strings.ContainsRune(clean, ' ') {
		add(strings.ReplaceAll(clean, ".", " "))
	}

	// Always include the whole cleaned name as a fallback candidate.
	add(clean)

	out := make([]string, 0, len(seen))
	for s := range seen {
		out = append(out, s)
	}
	return out
}

// NormalizeTitle lowercases, strips punctuation, and collapses whitespace so
// that "The Way of Kings" and "the way of kings!" both normalise to the same
// string for comparison.
func NormalizeTitle(s string) string {
	var b strings.Builder
	prevSpace := false
	for _, r := range strings.ToLower(s) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
			prevSpace = false
		} else if !prevSpace {
			b.WriteRune(' ')
			prevSpace = true
		}
	}
	return strings.TrimSpace(b.String())
}
