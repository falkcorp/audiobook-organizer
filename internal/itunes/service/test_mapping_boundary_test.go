// file: internal/itunes/service/test_mapping_boundary_test.go
// version: 1.0.0
// guid: 4b8d2e60-7c1a-4f95-b3e8-06a9f5c2d71e
// last-edited: 2026-09-12

package itunesservice

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeMappingLibrary(t *testing.T, locations ...string) string {
	t.Helper()
	var tracks strings.Builder
	for i, loc := range locations {
		id := 100 + i
		fmt.Fprintf(&tracks, `
		<key>%d</key>
		<dict>
			<key>Track ID</key><integer>%d</integer>
			<key>Persistent ID</key><string>ABCD%012d</string>
			<key>Name</key><string>Book %d</string>
			<key>Kind</key><string>Audiobook</string>
			<key>Genre</key><string>Audiobook</string>
			<key>Location</key><string>%s</string>
		</dict>`, id, id, id, i, loc)
	}
	xml := `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple Computer//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Major Version</key><integer>1</integer>
	<key>Minor Version</key><integer>1</integer>
	<key>Tracks</key>
	<dict>` + tracks.String() + `
	</dict>
	<key>Playlists</key>
	<array>
	</array>
</dict>
</plist>
`
	p := filepath.Join(t.TempDir(), "Library.xml")
	if err := os.WriteFile(p, []byte(xml), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// The preview samples only tracks under From. A track in a sibling directory
// ("C:/lib2" next to "C:/lib") is not under it, and RemapPath would not map it,
// so it must not be sampled as a "not found" example.
func TestTestMapping_SamplesOnlyTracksUnderFrom(t *testing.T) {
	lib := writeMappingLibrary(t,
		"file://localhost/C:/lib/a.m4b",
		"file://localhost/C:/lib2/b.m4b",
	)
	resp, err := TestMapping(TestMappingRequest{LibraryPath: lib, From: "file://localhost/C:/lib", To: t.TempDir()})
	if err != nil {
		t.Fatalf("TestMapping: %v", err)
	}
	if resp.Tested != 1 {
		t.Errorf("Tested = %d, want 1: the C:/lib2 track is not under From", resp.Tested)
	}

	// Trailing-separator From: the same single track.
	resp, err = TestMapping(TestMappingRequest{LibraryPath: lib, From: "file://localhost/C:/lib/", To: t.TempDir()})
	if err != nil {
		t.Fatalf("TestMapping: %v", err)
	}
	if resp.Tested != 1 {
		t.Errorf("trailing-separator From: Tested = %d, want 1", resp.Tested)
	}

	// Empty From keeps its old meaning: every audiobook track is sampled.
	resp, err = TestMapping(TestMappingRequest{LibraryPath: lib, From: "", To: t.TempDir()})
	if err != nil {
		t.Fatalf("TestMapping: %v", err)
	}
	if resp.Tested != 2 {
		t.Errorf("empty From: Tested = %d, want 2", resp.Tested)
	}
}
