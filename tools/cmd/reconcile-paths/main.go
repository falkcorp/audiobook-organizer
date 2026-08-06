// file: tools/cmd/reconcile-paths/main.go
// version: 1.2.0
// last-edited: 2026-05-24
//
// reconcile-paths is a READ-ONLY dry-run CLI tool that identifies audiobooks
// whose FilePath no longer exists on disk and finds their candidate location
// via the "Title - Title" doubled-folder pattern, single-file variants, and
// author-root normalized-name fallback.
//
// No DB writes. No file moves. Output is a CSV report only.
//
// Usage:
//
//	reconcile-paths [-api URL] [-key KEY] [-out FILE] [-limit N] [-page-size N] [-page-delay-ms N] [-verbose]
//
// SSH mode (dev box → prod):
//
//	RECONCILE_VIA_SSH=<server> reconcile-paths ...
package main

import (
	"bufio"
	"bytes"
	"crypto/tls"
	"encoding/csv"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
	"unicode"
)

// audioExt lists extensions to check for single-file books.
var audioExt = []string{".m4b", ".mp3", ".m4a", ".ogg", ".flac", ".aac", ".opus"}

// sshBatchSize controls how many paths are checked in a single ssh invocation.
const sshBatchSize = 50

// noiseWords are common audiobook terms stripped during name normalization.
var noiseWords = map[string]bool{
	"the": true, "a": true, "an": true, "of": true, "and": true, "or": true,
	"in": true, "on": true, "at": true, "by": true, "to": true, "for": true,
}

// ----- API response types (mirrors internal/audiobooks types) -----

type book struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	FilePath string `json:"file_path"`
}

type listData struct {
	Items  []book `json:"items"`
	Count  int    `json:"count"`
	Limit  int    `json:"limit"`
	Offset int    `json:"offset"`
}

// listResponse handles both wrapped {"data": {...}} and unwrapped shapes.
type listResponse struct {
	Data *listData `json:"data"`
	// Flat fields (fallback if API returns top-level directly)
	Items  []book `json:"items"`
	Count  int    `json:"count"`
	Limit  int    `json:"limit"`
	Offset int    `json:"offset"`
}

// ----- Result record -----

type matchRecord struct {
	bookID        string
	title         string
	dbPath        string
	candidatePath string
	candidateKind string // "doubled-folder" | "single-file-<ext>"
	audioCount    int    // files in folder (0 for single-file hits)
}

// ----- main -----

func main() {
	// No baked-in default: this is a public repo and a hardcoded internal address is
	// exactly the kind of fleet detail that must not ship in it. Mirrors the -key flag
	// below, which already reads from the environment.
	apiURL := flag.String("api", os.Getenv("AUDIOBOOK_API_URL"), "API base URL (or set AUDIOBOOK_API_URL env)")
	outFile := flag.String("out", "/tmp/reconcile_dry_run.csv", "Output CSV path (use '-' for stdout)")
	limit := flag.Int("limit", 0, "Max books to inspect (0 = all)")
	apiKey := flag.String("key", "", "API key (or set AUDIOBOOK_API_KEY env)")
	pageSize := flag.Int("page-size", 200, "Pagination page size")
	pageDelayMs := flag.Int("page-delay-ms", 100, "Delay in milliseconds between page fetches (0 to disable)")
	verbose := flag.Bool("verbose", false, "Verbose progress logging")
	stdout := flag.Bool("stdout", false, "Write CSV to stdout instead of -out file")
	flag.Parse()

	// Resolve API key.
	key := *apiKey
	if key == "" {
		key = os.Getenv("AUDIOBOOK_API_KEY")
	}
	if key == "" {
		fmt.Fprintln(os.Stderr, "reconcile-paths: API key required: use -key flag or AUDIOBOOK_API_KEY env var")
		os.Exit(1)
	}

	sshHost := os.Getenv("RECONCILE_VIA_SSH")

	// Build HTTP client (TLS skip for self-signed cert).
	transport := &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec
	}
	client := &http.Client{Transport: transport}

	// Fetch all books.
	books, err := fetchAllBooks(client, *apiURL, key, *pageSize, *limit, *pageDelayMs, *verbose)
	if err != nil {
		fmt.Fprintf(os.Stderr, "reconcile-paths: fetch books: %v\n", err)
		os.Exit(1)
	}
	fmt.Fprintf(os.Stderr, "Fetched %d books from API\n", len(books))

	// Stat all db paths to find missing ones.
	missing := filterMissing(books, sshHost, *verbose)
	fmt.Fprintf(os.Stderr, "%d books have missing FilePath on disk\n", len(missing))

	// For each missing book, probe candidates.
	var records []matchRecord
	noMatch := 0
	for _, b := range missing {
		rec, found := findCandidate(b, sshHost, *verbose)
		if found {
			records = append(records, rec)
		} else {
			noMatch++
		}
	}

	// Write CSV.
	var w io.Writer
	if *stdout || *outFile == "-" {
		w = os.Stdout
	} else {
		f, err := os.Create(*outFile)
		if err != nil {
			fmt.Fprintf(os.Stderr, "reconcile-paths: create output file: %v\n", err)
			os.Exit(1)
		}
		defer f.Close()
		w = f
		fmt.Fprintf(os.Stderr, "Writing CSV to %s\n", *outFile)
	}
	if err := writeCSV(w, records); err != nil {
		fmt.Fprintf(os.Stderr, "reconcile-paths: write CSV: %v\n", err)
		os.Exit(1)
	}

	// Summary.
	doubledFolderCount := 0
	singleFileCount := 0
	authorRootCount := 0
	for _, r := range records {
		if r.candidateKind == "doubled-folder" {
			doubledFolderCount++
		} else if r.candidateKind == "P3:author_root_match" {
			authorRootCount++
		} else {
			singleFileCount++
		}
	}
	fmt.Fprintf(os.Stderr, "\n=== SUMMARY ===\n")
	fmt.Fprintf(os.Stderr, "Total books inspected:      %d\n", len(books))
	fmt.Fprintf(os.Stderr, "Missing on disk:            %d\n", len(missing))
	fmt.Fprintf(os.Stderr, "Matched (doubled-folder):   %d\n", doubledFolderCount)
	fmt.Fprintf(os.Stderr, "Matched (single-file):      %d\n", singleFileCount)
	fmt.Fprintf(os.Stderr, "Matched (author-root):      %d\n", authorRootCount)
	fmt.Fprintf(os.Stderr, "No match found:             %d\n", noMatch)
}

// ----- API pagination -----

func fetchAllBooks(client *http.Client, apiURL, key string, pageSize, limit, pageDelayMs int, verbose bool) ([]book, error) {
	var all []book
	offset := 0
	for {
		url := fmt.Sprintf("%s/api/v1/audiobooks?page_size=%d&offset=%d", apiURL, pageSize, offset)
		req, err := http.NewRequest("GET", url, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+key)

		resp, err := client.Do(req)
		if err != nil {
			return nil, fmt.Errorf("GET %s: %w", url, err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("API returned %d: %s", resp.StatusCode, body)
		}

		var lr listResponse
		if err := json.Unmarshal(body, &lr); err != nil {
			return nil, fmt.Errorf("decode response: %w", err)
		}
		// Unwrap data envelope if present.
		items := lr.Items
		count := lr.Count
		if lr.Data != nil {
			items = lr.Data.Items
			count = lr.Data.Count
		}
		if verbose {
			fmt.Fprintf(os.Stderr, "Page offset=%d: got %d items (total count=%d)\n", offset, len(items), count)
		}
		all = append(all, items...)

		if limit > 0 && len(all) >= limit {
			all = all[:limit]
			break
		}
		// Server may cap page size below requested value; trust count and stop only when empty or count reached.
		if len(items) == 0 || (count > 0 && len(all) >= count) {
			break
		}
		offset += len(items)

		// Rate-limit subsequent page fetches to avoid overwhelming the server.
		if pageDelayMs > 0 {
			time.Sleep(time.Duration(pageDelayMs) * time.Millisecond)
		}
	}
	return all, nil
}

// ----- Stat helpers -----

// filterMissing returns books whose FilePath does not exist on disk.
func filterMissing(books []book, sshHost string, verbose bool) []book {
	if sshHost != "" {
		return filterMissingSSH(books, sshHost, verbose)
	}
	var missing []book
	for _, b := range books {
		if b.FilePath == "" {
			continue
		}
		if _, err := os.Stat(b.FilePath); os.IsNotExist(err) {
			if verbose {
				fmt.Fprintf(os.Stderr, "MISSING: %s\n", b.FilePath)
			}
			missing = append(missing, b)
		}
	}
	return missing
}

// filterMissingSSH batches stat calls over SSH.
func filterMissingSSH(books []book, host string, verbose bool) []book {
	// Build index: path → book.
	pathToBook := make(map[string]book, len(books))
	paths := make([]string, 0, len(books))
	for _, b := range books {
		if b.FilePath == "" {
			continue
		}
		pathToBook[b.FilePath] = b
		paths = append(paths, b.FilePath)
	}

	hits := sshStatBatch(host, paths, verbose)

	var missing []book
	for _, p := range paths {
		if !hits[p] {
			missing = append(missing, pathToBook[p])
		}
	}
	return missing
}

// sshStatBatch checks a list of paths via SSH, returns a map path→exists.
func sshStatBatch(host string, paths []string, verbose bool) map[string]bool {
	result := make(map[string]bool, len(paths))
	for i := 0; i < len(paths); i += sshBatchSize {
		end := i + sshBatchSize
		if end > len(paths) {
			end = len(paths)
		}
		batch := paths[i:end]
		hits := runSSHBatch(host, batch, verbose)
		for k, v := range hits {
			result[k] = v
		}
	}
	return result
}

// runSSHBatch runs one ssh invocation for a slice of paths.
func runSSHBatch(host string, paths []string, verbose bool) map[string]bool {
	// Build a shell script: for each path emit HIT:<path> or MISS:<path>.
	var sb strings.Builder
	for _, p := range paths {
		// Quote path for shell safety (single-quote, escape any single quotes).
		safe := strings.ReplaceAll(p, "'", "'\\''")
		fmt.Fprintf(&sb, "[ -e '%s' ] && echo 'HIT:%s' || echo 'MISS:%s'\n", safe, safe, safe)
	}
	script := sb.String()

	if verbose {
		fmt.Fprintf(os.Stderr, "SSH batch (%d paths) to %s\n", len(paths), host)
	}

	cmd := exec.Command("ssh", "-o", "StrictHostKeyChecking=no", "-o", "BatchMode=yes", host, script)
	out, err := cmd.Output()
	if err != nil {
		fmt.Fprintf(os.Stderr, "WARN: ssh batch error: %v (marking paths as unknown/missing)\n", err)
		result := make(map[string]bool, len(paths))
		for _, p := range paths {
			result[p] = false
		}
		return result
	}

	result := make(map[string]bool, len(paths))
	scanner := bufio.NewScanner(bytes.NewReader(out))
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "HIT:") {
			result[strings.TrimPrefix(line, "HIT:")] = true
		} else if strings.HasPrefix(line, "MISS:") {
			result[strings.TrimPrefix(line, "MISS:")] = false
		}
	}
	return result
}

// statExists checks a single path, either locally or via SSH.
func statExists(path, sshHost string) bool {
	if sshHost != "" {
		hits := runSSHBatch(sshHost, []string{path}, false)
		return hits[path]
	}
	_, err := os.Stat(path)
	return err == nil
}

// ----- Candidate probing -----

func findCandidate(b book, sshHost string, verbose bool) (matchRecord, bool) {
	parent := filepath.Dir(b.FilePath)
	title := b.Title

	// 1. Doubled-folder pattern: parent/<title> - <title>/
	doubled := filepath.Join(parent, title+" - "+title)
	if statExists(doubled, sshHost) {
		count := countAudioFiles(doubled, sshHost)
		if verbose {
			fmt.Fprintf(os.Stderr, "MATCH doubled-folder: %s %q → %s (%d audio files)\n", b.ID, title, doubled, count)
		}
		return matchRecord{
			bookID:        b.ID,
			title:         title,
			dbPath:        b.FilePath,
			candidatePath: doubled,
			candidateKind: "doubled-folder",
			audioCount:    count,
		}, true
	}

	// 2. Single-file variants: parent/<title>.<ext>
	for _, ext := range audioExt {
		candidate := filepath.Join(parent, title+ext)
		if statExists(candidate, sshHost) {
			if verbose {
				fmt.Fprintf(os.Stderr, "MATCH single-file: %s %q → %s\n", b.ID, title, candidate)
			}
			return matchRecord{
				bookID:        b.ID,
				title:         title,
				dbPath:        b.FilePath,
				candidatePath: candidate,
				candidateKind: "single-file" + ext,
				audioCount:    1,
			}, true
		}
	}

	// 3. Author-root fallback: list author root directory and match by normalized name.
	authorRoot := filepath.Dir(parent)
	if authorRoot != parent { // Ensure we have a parent level to recurse into.
		rec := findAuthorRootMatch(b, authorRoot, sshHost, verbose)
		if rec != nil {
			return *rec, true
		}
	}

	if verbose {
		fmt.Fprintf(os.Stderr, "NO MATCH: %s %q (db_path=%s)\n", b.ID, title, b.FilePath)
	}
	return matchRecord{}, false
}

// findAuthorRootMatch scans the author root directory for a normalized-name match.
func findAuthorRootMatch(b book, authorRoot, sshHost string, verbose bool) *matchRecord {
	title := b.Title
	expectedLeaf := filepath.Base(filepath.Dir(b.FilePath))
	expectedNorm := normalizeName(expectedLeaf)

	// List children of author root.
	children := listDirChildren(authorRoot, sshHost, verbose)
	if len(children) == 0 {
		return nil
	}

	for _, child := range children {
		childNorm := normalizeName(child)

		// Check for exact or strong substring match (80%+ character overlap).
		if childNorm == expectedNorm || isStrongMatch(childNorm, expectedNorm) {
			candidatePath := filepath.Join(authorRoot, child)
			audioCount := countAudioFilesRecursive(candidatePath, sshHost)

			if verbose {
				fmt.Fprintf(os.Stderr, "MATCH P3:author_root_match: %s %q → %s (%d audio files)\n",
					b.ID, title, candidatePath, audioCount)
			}
			return &matchRecord{
				bookID:        b.ID,
				title:         title,
				dbPath:        b.FilePath,
				candidatePath: candidatePath,
				candidateKind: "P3:author_root_match",
				audioCount:    audioCount,
			}
		}
	}

	return nil
}

// normalizeName strips case, collapses whitespace, removes punctuation, and drops noise words.
func normalizeName(name string) string {
	// Convert to lowercase.
	name = strings.ToLower(name)

	// Replace punctuation with spaces (keep only letters, digits, spaces).
	var sb strings.Builder
	for _, r := range name {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			sb.WriteRune(r)
		} else {
			sb.WriteRune(' ')
		}
	}
	name = sb.String()

	// Collapse multiple spaces.
	name = strings.Join(strings.Fields(name), " ")

	// Remove noise words.
	words := strings.Fields(name)
	var filtered []string
	for _, w := range words {
		if !noiseWords[w] {
			filtered = append(filtered, w)
		}
	}
	return strings.Join(filtered, " ")
}

// isStrongMatch returns true if one string contains the other with 80%+ char overlap.
func isStrongMatch(a, b string) bool {
	if a == b {
		return true
	}
	// Check containment.
	if strings.Contains(a, b) || strings.Contains(b, a) {
		shorter := a
		if len(b) < len(a) {
			shorter = b
		}
		if len(shorter) == 0 {
			return false
		}
		overlapRatio := float64(len(shorter)) / float64(len(a)+len(b)-len(shorter))
		return overlapRatio >= 0.8
	}
	return false
}

// listDirChildren returns immediate child folder names in a directory.
func listDirChildren(dir, sshHost string, verbose bool) []string {
	if sshHost != "" {
		return listDirChildrenSSH(dir, sshHost, verbose)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		if verbose {
			fmt.Fprintf(os.Stderr, "WARN: cannot list %s: %v\n", dir, err)
		}
		return nil
	}

	var children []string
	for _, e := range entries {
		if e.IsDir() {
			children = append(children, e.Name())
		}
	}
	return children
}

// listDirChildrenSSH retrieves directory children via SSH.
func listDirChildrenSSH(dir, host string, verbose bool) []string {
	safe := strings.ReplaceAll(dir, "'", "'\\''")
	script := fmt.Sprintf("ls -1d '%s'/*/ 2>/dev/null | xargs -I {} basename {}", safe)
	cmd := exec.Command("ssh", "-o", "StrictHostKeyChecking=no", "-o", "BatchMode=yes", host, script)
	out, err := cmd.Output()
	if err != nil {
		if verbose {
			fmt.Fprintf(os.Stderr, "WARN: ssh list dir error for %s: %v\n", dir, err)
		}
		return nil
	}

	var children []string
	scanner := bufio.NewScanner(bytes.NewReader(out))
	for scanner.Scan() {
		name := strings.TrimSpace(scanner.Text())
		if name != "" {
			children = append(children, name)
		}
	}
	return children
}

// countAudioFilesRecursive counts audio files recursively in a directory tree.
func countAudioFilesRecursive(dir, sshHost string) int {
	if sshHost != "" {
		return countAudioFilesRecursiveSSH(dir, sshHost)
	}

	count := 0
	filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			return nil
		}
		ext := strings.ToLower(filepath.Ext(path))
		for _, ae := range audioExt {
			if ext == ae {
				count++
				break
			}
		}
		return nil
	})
	return count
}

// countAudioFilesRecursiveSSH counts audio files recursively via SSH.
func countAudioFilesRecursiveSSH(dir, host string) int {
	safe := strings.ReplaceAll(dir, "'", "'\\''")
	script := fmt.Sprintf("find '%s' -type f \\( -iname '*.m4b' -o -iname '*.mp3' -o -iname '*.m4a' -o -iname '*.ogg' -o -iname '*.flac' -o -iname '*.aac' -o -iname '*.opus' \\) 2>/dev/null | wc -l", safe)
	cmd := exec.Command("ssh", "-o", "StrictHostKeyChecking=no", "-o", "BatchMode=yes", host, script)
	out, err := cmd.Output()
	if err != nil {
		return 0
	}
	n := 0
	fmt.Sscanf(strings.TrimSpace(string(out)), "%d", &n)
	return n
}

// countAudioFiles counts audio files in a directory (local or SSH).
func countAudioFiles(dir, sshHost string) int {
	if sshHost != "" {
		return countAudioFilesSSH(dir, sshHost)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}
	count := 0
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		ext := strings.ToLower(filepath.Ext(e.Name()))
		for _, ae := range audioExt {
			if ext == ae {
				count++
				break
			}
		}
	}
	return count
}

func countAudioFilesSSH(dir, host string) int {
	safe := strings.ReplaceAll(dir, "'", "'\\''")
	script := fmt.Sprintf("ls -1 '%s' 2>/dev/null | grep -cEi '\\.(m4b|mp3|m4a|ogg|flac|aac|opus)$' || echo 0", safe)
	cmd := exec.Command("ssh", "-o", "StrictHostKeyChecking=no", "-o", "BatchMode=yes", host, script)
	out, err := cmd.Output()
	if err != nil {
		return 0
	}
	n := 0
	fmt.Sscanf(strings.TrimSpace(string(out)), "%d", &n)
	return n
}

// ----- CSV output -----

func writeCSV(w io.Writer, records []matchRecord) error {
	cw := csv.NewWriter(w)
	// Header.
	if err := cw.Write([]string{
		"book_id", "title", "db_path", "candidate_path", "candidate_kind", "audio_file_count",
	}); err != nil {
		return err
	}
	for _, r := range records {
		if err := cw.Write([]string{
			r.bookID,
			r.title,
			r.dbPath,
			r.candidatePath,
			r.candidateKind,
			fmt.Sprintf("%d", r.audioCount),
		}); err != nil {
			return err
		}
	}
	cw.Flush()
	return cw.Error()
}
