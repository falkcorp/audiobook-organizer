// file: internal/fingerprint/backfill_utils_test.go
// version: 1.1.0
// guid: a6b7c8d9-e0f1-2a3b-4c5d-6e7f8a9b0c1d

package fingerprint

import (
	"testing"
)

func TestIsFingerprintableWithValidExtensions(t *testing.T) {
	tests := []struct {
		path   string
		expect bool
	}{
		{"/path/to/file.mp3", true},
		{"/path/to/file.m4b", true},
		{"/path/to/file.flac", true},
		{"/path/to/file.MP3", true}, // case insensitive
		{"/path/to/file.txt", false},
		{"/path/to/file.pdf", false},
		{"", false},
	}

	for _, tt := range tests {
		result := IsFingerprintable(tt.path)
		if result != tt.expect {
			t.Errorf("IsFingerprintable(%q) = %v, want %v", tt.path, result, tt.expect)
		}
	}
}

func TestFileExistsForNonexistentFile(t *testing.T) {
	result := FileExists("/nonexistent/path/file.mp3")
	if result {
		t.Error("FileExists should return false for nonexistent file")
	}
}

func TestFingerprintableExtensionsHasExpectedKeys(t *testing.T) {
	expectedExts := []string{".mp3", ".m4b", ".m4a", ".flac", ".opus"}
	for _, ext := range expectedExts {
		if _, ok := FingerprintableExtensions[ext]; !ok {
			t.Errorf("FingerprintableExtensions missing expected key: %s", ext)
		}
	}
}
