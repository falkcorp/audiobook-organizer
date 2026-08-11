// file: internal/itunes/xml_library_test.go
// version: 1.0.0
// guid: 5d1a9e64-27bf-4c38-a0e5-93b6c8f21a70
// last-edited: 2026-08-10

package itunes

import (
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
)

const miniXML = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
  <key>Application Version</key><string>12.13.10.3</string>
  <key>Playlists</key>
  <array>
    <dict>
      <key>Name</key><string>Smart One</string>
      <key>Playlist Persistent ID</key><string>A1B2C3D4E5F60718</string>
      <key>Smart Info</key><data>AQID</data>
      <key>Smart Criteria</key><data>U0xzdAABAAE=</data>
      <key>Playlist Items</key>
      <array><dict><key>Track ID</key><integer>11</integer></dict>
             <dict><key>Track ID</key><integer>22</integer></dict></array>
    </dict>
    <dict>
      <key>Name</key><string>Plain One</string>
      <key>Playlist Persistent ID</key><string>0011223344556677</string>
    </dict>
    <dict>
      <key>Name</key><string>A Folder</string>
      <key>Playlist Persistent ID</key><string>8899AABBCCDDEEFF</string>
      <key>Folder</key><true/>
    </dict>
    <dict>
      <key>Name</key><string>Bad PID</string>
      <key>Playlist Persistent ID</key><string>NOTHEX</string>
      <key>Smart Criteria</key><data>U0xzdA==</data>
    </dict>
  </array>
</dict></plist>`

func writeTemp(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "iTunes Library.xml")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	return p
}

func TestParseXMLLibraryPlaylists_ReadsSmartCriteriaAndPID(t *testing.T) {
	lib, err := ParseXMLLibraryPlaylists(writeTemp(t, miniXML))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if lib.Version != "12.13.10.3" {
		t.Errorf("version = %q", lib.Version)
	}
	// "Bad PID" must be dropped, not imported under a zero PID — the importer
	// keys idempotency on the PID, so a zero would make every such playlist
	// collide and all but the first report "already imported".
	if len(lib.Playlists) != 3 {
		t.Fatalf("got %d playlists, want 3 (Bad PID must be skipped)", len(lib.Playlists))
	}

	var smart *ITLPlaylist
	for i := range lib.Playlists {
		if lib.Playlists[i].Title == "Smart One" {
			smart = &lib.Playlists[i]
		}
	}
	if smart == nil {
		t.Fatal("Smart One missing")
	}
	if !smart.IsSmart {
		t.Error("Smart One should be smart — it carries a criteria blob")
	}
	if got := string(smart.SmartCriteria); got != "SLst\x00\x01\x00\x01" {
		t.Errorf("criteria = %q", got)
	}
	if got := hex.EncodeToString(smart.PersistentID[:]); got != "a1b2c3d4e5f60718" {
		t.Errorf("pid = %s", got)
	}
	if len(smart.Items) != 2 || smart.Items[0] != 11 || smart.Items[1] != 22 {
		t.Errorf("items = %v", smart.Items)
	}

	for _, p := range lib.Playlists {
		switch p.Title {
		case "Plain One":
			if p.IsSmart {
				t.Error("Plain One has no criteria blob and must not be smart")
			}
		case "A Folder":
			if !p.IsFolder {
				t.Error("A Folder should be a folder")
			}
		}
	}
}

// Against the owner's real 153 MB export, if present. This is the case the
// .itl parser gets wrong: it reports 0 smart playlists for the same library.
func TestParseXMLLibraryPlaylists_RealExport(t *testing.T) {
	path := os.Getenv("ITUNES_XML")
	if path == "" {
		t.Skip("ITUNES_XML not set")
	}
	lib, err := ParseXMLLibraryPlaylists(path)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	smart, withCriteria := 0, 0
	for _, p := range lib.Playlists {
		if p.IsSmart {
			smart++
		}
		if len(p.SmartCriteria) > 0 {
			withCriteria++
		}
	}
	t.Logf("REAL_XML playlists=%d smart=%d with_criteria=%d version=%s",
		len(lib.Playlists), smart, withCriteria, lib.Version)
	if smart == 0 {
		t.Fatal("no smart playlists found in the real export — the whole point of this reader")
	}
}
