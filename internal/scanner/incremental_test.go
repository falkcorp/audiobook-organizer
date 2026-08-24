// file: internal/scanner/incremental_test.go
// version: 1.1.0
// guid: e9f0a1b2-c3d4-5e6f-7a8b-9c0d1e2f3a4b

package scanner

import (
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

func TestShouldSkipFile_MatchingCache(t *testing.T) {
	cache := map[string]database.ScanCacheEntry{
		"/fake/path/book.mp3": {Mtime: 1234567890, Size: 1048576, NeedsRescan: false},
	}
	if !skipOnly("/fake/path/book.mp3", 1234567890, 1048576, cache) {
		t.Error("expected skip when mtime+size match cache")
	}
}

func TestShouldSkipFile_MtimeChanged(t *testing.T) {
	cache := map[string]database.ScanCacheEntry{
		"/fake/path/book.mp3": {Mtime: 1234567890, Size: 1048576, NeedsRescan: false},
	}
	if skipOnly("/fake/path/book.mp3", 1234567891, 1048576, cache) {
		t.Error("expected process when mtime changed")
	}
}

func TestShouldSkipFile_SizeChanged(t *testing.T) {
	cache := map[string]database.ScanCacheEntry{
		"/fake/path/book.mp3": {Mtime: 1234567890, Size: 1048576, NeedsRescan: false},
	}
	if skipOnly("/fake/path/book.mp3", 1234567890, 2097152, cache) {
		t.Error("expected process when size changed")
	}
}

func TestShouldSkipFile_NotInCache(t *testing.T) {
	cache := map[string]database.ScanCacheEntry{
		"/fake/path/book.mp3": {Mtime: 1234567890, Size: 1048576, NeedsRescan: false},
	}
	if skipOnly("/fake/path/other.mp3", 1234567890, 1048576, cache) {
		t.Error("expected process when not in cache")
	}
}

func TestShouldSkipFile_NeedsRescan(t *testing.T) {
	cache := map[string]database.ScanCacheEntry{
		"/fake/path/dirty.mp3": {Mtime: 1234567890, Size: 1048576, NeedsRescan: true},
	}
	if skipOnly("/fake/path/dirty.mp3", 1234567890, 1048576, cache) {
		t.Error("expected process when needs_rescan is true")
	}
}

func TestShouldSkipFile_NilCache(t *testing.T) {
	if skipOnly("/fake/path/book.mp3", 1234567890, 1048576, nil) {
		t.Error("expected process when cache is nil")
	}
}
