// file: internal/scanner/test_seams_test.go
// version: 1.0.0
// guid: 9e730461-420b-4319-99ae-b84b53fc5585
// last-edited: 2026-10-11

package scanner

import "github.com/falkcorp/audiobook-organizer/internal/logger"

// SetScanner overrides the default scanner implementation for tests. Pass nil
// to restore the default. Production code never installs a scanner override.
func SetScanner(s Scanner) {
	activeScanner = s
}

// createBookFilesForBook is createBookFilesForBookWithAudio with the default
// file ceiling and no known media info or run counters. Tests call it to
// drive the live implementation without threading those arguments.
func createBookFilesForBook(bookFilePath string, segmentFiles []string, scanLog logger.Logger, normalizeBookPath bool, knownHashes ...map[string]string) string {
	var hashes map[string]string
	if len(knownHashes) > 0 {
		hashes = knownHashes[0]
	}
	return createBookFilesForBookWithAudio(bookFilePath, segmentFiles, scanLog, normalizeBookPath, hashes, nil, maxDirectoryBookFiles, nil)
}

// resolveAuthorID is resolveAuthorIDs' primary author alone, nil for none.
func resolveAuthorID(authorName string) (*int, error) {
	ids, err := resolveAuthorIDs(authorName)
	if err != nil || len(ids) == 0 {
		return nil, err
	}
	return &ids[0], nil
}
