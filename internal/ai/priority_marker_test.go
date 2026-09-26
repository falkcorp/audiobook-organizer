// file: internal/ai/priority_marker_test.go
// version: 1.4.0
// guid: 8f370c63-462a-4dfa-b899-a5e715e210b0

package ai

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestNoUnmarkedChatCompletionCallers enforces that every function calling
// client.Chat.Completions.New is either in the allow-list (sync callers
// during migration phases) or carries a // PRIORITY: Interactive marker
// directly above the func declaration.
func TestNoUnmarkedChatCompletionCallers(t *testing.T) {
	// Allow-list: functions that currently call Chat.Completions.New synchronously.
	// Each entry notes which Task will remove it during migration.
	allowListedSyncCallers := map[string]string{
		"ParseBatch":          "Task 2.2", // TODO: migrate to aijobs — requires scanner refactor (see TODO.md AI-BATCH-1)
		"ParseCoverArt":       "Task 2.3", // TODO: defer to follow-up — resolveProductionAuthor loop coupling (see TODO.md AI-BATCH-2)
		"reviewAuthorBatch":   "Task 2.3", // Out-of-scope — existing author-dedup flow
		"discoverAuthorBatch": "Task 2.3", // Out-of-scope — existing author-dedup flow
		"scoreMetadataBatch":  "",         // PRIORITY: Interactive — user-waiting metadata search, stays sync
		"ReadCoverText":       "",         // background: maintenance.cover-text-read (PriorityLow), bounded by the pool's llm.cover_art_vision slots
	}

	// Walk the current directory (package ai) for .go files.
	pkgDir, err := os.Getwd()
	if err != nil {
		t.Fatalf("failed to get current directory: %v", err)
	}

	entries, err := os.ReadDir(pkgDir)
	if err != nil {
		t.Fatalf("failed to read package directory: %v", err)
	}

	var offenders []string

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		filename := entry.Name()
		// Skip test files and non-Go files.
		if !strings.HasSuffix(filename, ".go") || strings.HasSuffix(filename, "_test.go") {
			continue
		}

		filepath := filepath.Join(pkgDir, filename)
		content, err := os.ReadFile(filepath)
		if err != nil {
			t.Fatalf("failed to read file %s: %v", filepath, err)
		}

		lines := strings.Split(string(content), "\n")

		// Find all Chat.Completions.New call sites.
		chatPattern := regexp.MustCompile(`client\.Chat\.Completions\.New\(`)

		for i, line := range lines {
			if !chatPattern.MatchString(line) {
				continue
			}

			lineNum := i + 1 // 1-indexed line number for output

			// Find enclosing function by scanning backward from this line.
			var enclosingFunc string

			for j := i; j >= 0; j-- {
				scanLine := lines[j]
				// Match: func (receiver type) funcName(...) or func funcName(...)
				// Pattern: optional whitespace, "func", optional receiver, function name, (
				funcPattern := regexp.MustCompile(`^\s*func\s+(?:\(\s*\w+\s+\*?\w+\s*\)\s+)?(\w+)\s*\(`)
				if submatches := funcPattern.FindStringSubmatch(scanLine); len(submatches) > 1 {
					enclosingFunc = submatches[1]
					break
				}
			}

			if enclosingFunc == "" {
				offenders = append(offenders, fmt.Sprintf(
					"%s:%d: could not determine enclosing function for Chat.Completions.New call",
					filename, lineNum,
				))
				continue
			}

			// Check allow-list.
			if _, isAllowListed := allowListedSyncCallers[enclosingFunc]; isAllowListed {
				continue
			}

			// Check for PRIORITY: Interactive marker on the line immediately before func.
			// Scan backward from the func declaration to find the marker.
			hasMarker := false
			for j := i; j >= 0; j-- {
				scanLine := lines[j]
				// First, check if this is the func declaration line.
				if regexp.MustCompile(`^\s*func\s+(?:\(\s*\w+\s+\*?\w+\s*\)\s+)?` + enclosingFunc + `\s*\(`).MatchString(scanLine) {
					// Found func declaration. Check the previous line for the marker.
					if j > 0 {
						prevLine := lines[j-1]
						if strings.Contains(prevLine, "PRIORITY:") && strings.Contains(prevLine, "Interactive") {
							hasMarker = true
						}
					}
					break
				}
			}

			if !hasMarker {
				offenders = append(offenders, fmt.Sprintf(
					"%s:%d: function %q calls Chat.Completions.New but is not allow-listed and lacks // PRIORITY: Interactive marker",
					filename, lineNum, enclosingFunc,
				))
			}
		}
	}

	if len(offenders) > 0 {
		t.Errorf("Found %d unmarked Chat.Completions.New call sites:\n%s", len(offenders), strings.Join(offenders, "\n"))
	}
}
