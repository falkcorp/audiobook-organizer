// file: internal/itunes/writeback_root_test.go
// version: 1.0.0
// guid: 5a9c3e71-2d4b-4f86-a1e0-7b3d9c6f2e15
// last-edited: 2026-10-07

package itunes

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWritebackRootForLibrary(t *testing.T) {
	cases := []struct {
		path string
		want string
	}{
		{"/mnt/bigdata/books/audiobook-organizer/.itunes-writeback/iTunes Library.itl", "audiobook-organizer/.itunes-writeback/"},
		{`W:\audiobook-organizer\.itunes-writeback\iTunes Library.itl`, "audiobook-organizer/.itunes-writeback/"},
		// The Original library: never scoped, always strict.
		{"/mnt/bigdata/books/itunes/iTunes Library.itl", ""},
		// Only the directory the .itl sits in counts, not a deeper or higher one.
		{"/x/audiobook-organizer/.itunes-writeback/sub/iTunes Library.itl", ""},
		{"/.itunes-writeback/iTunes Library.itl", ""},
		{"", ""},
	}
	for _, tc := range cases {
		if got := WritebackRootForLibrary(tc.path); got != tc.want {
			t.Errorf("WritebackRootForLibrary(%q) = %q, want %q", tc.path, got, tc.want)
		}
	}
}

// writebackTracks are synthetic tracks whose media lives under the AO writeback
// library's own root, the shape of every prod track since iTunes was pointed at
// that library.
func writebackTracks(n int) []fxTrack {
	tracks := make([]fxTrack, n)
	for i := range n {
		tracks[i] = fxTrack{
			tid:      uint32((i + 1) * 2),
			name:     "Track",
			location: `W:\audiobook-organizer\.itunes-writeback\iTunes Media\Audiobooks\Synthetic Author\` + itoa(i) + `.m4b`,
		}
	}
	return tracks
}

// TestWriteback_AOLibraryOwnMediaRootAccepted is the 2026-10-07 root-cause
// regression, end to end with the real writers (no hooks): a write to a library
// in ".itunes-writeback/" whose tracks live under that library's own media root
// must pass with NO explicit contract config, on every writer route the
// write-back paths use. The same bytes in any other directory stay strict.
func TestWriteback_AOLibraryOwnMediaRootAccepted(t *testing.T) {
	payload := buildPayloadFromTracks(writebackTracks(30))

	// Strict audit rejects exactly as prod did.
	if v := AuditITL(buildITLFile(t, payload)); v.Pass || !strings.Contains(v.Error(), "staging marker") {
		t.Fatalf("strict AuditITL should reject the AO library's own media root; pass=%v err=%s", v.Pass, v.Error())
	}

	aoDir := filepath.Join(t.TempDir(), "audiobook-organizer", ".itunes-writeback")
	if err := os.MkdirAll(aoDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// The new location is an organized-library path, as the batcher writes: the
	// location writer itself refuses to write a NEW location into
	// ".itunes-writeback/" (CRIT-2, location_pair.go), a separate guard this
	// change leaves alone. What must pass is the contract over the library's 29
	// other, untouched tracks that still live under its own media root.
	newLoc := `W:\audiobook-organizer\Synthetic Author\Moved\moved.m4b`
	pid := pidForTID(payload, 2)
	ops := ITLOperationSet{LocationUpdates: []ITLLocationUpdate{{PersistentID: pid, NewLocation: newLoc}}}

	t.Run("audit with the library's config", func(t *testing.T) {
		path := writeFixtureITL(t, aoDir, "iTunes Library.itl", payload)
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if v := AuditITLWithConfig(data, WritebackContractConfig(path)); !v.Pass {
			t.Fatalf("scoped audit must pass: %s", v.Error())
		}
	})

	t.Run("ApplyITLOperations to a .tmp (the batcher route)", func(t *testing.T) {
		path := writeFixtureITL(t, aoDir, "iTunes Library.itl", payload)
		if _, err := ApplyITLOperations(path, path+".tmp", ops); err != nil {
			t.Fatalf("write to the AO library was rejected: %v", err)
		}
		_ = os.Remove(path + ".tmp")
	})

	t.Run("ApplyITLOperations in place (SafeWriteITL route)", func(t *testing.T) {
		path := writeFixtureITL(t, aoDir, "iTunes Library.itl", payload)
		if _, err := ApplyITLOperations(path, path, ops); err != nil {
			t.Fatalf("in-place write to the AO library was rejected: %v", err)
		}
		lib, err := ParseITL(path)
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, tr := range lib.Tracks {
			if tr.Location == newLoc {
				found = true
			}
		}
		if !found {
			t.Fatal("location update did not land")
		}
	})

	t.Run("UpdateITLLocations to a .tmp (write-back handlers, deferred updates)", func(t *testing.T) {
		path := writeFixtureITL(t, aoDir, "iTunes Library.itl", payload)
		if _, err := UpdateITLLocations(path, path+".tmp", ops.LocationUpdates); err != nil {
			t.Fatalf("UpdateITLLocations to the AO library was rejected: %v", err)
		}
		_ = os.Remove(path + ".tmp")
	})

	t.Run("PinLastKnownGood", func(t *testing.T) {
		path := writeFixtureITL(t, aoDir, "iTunes Library.itl", payload)
		if err := PinLastKnownGood(path); err != nil {
			t.Fatalf("pinning the AO library was refused: %v", err)
		}
	})

	t.Run("same bytes outside .itunes-writeback stay strict", func(t *testing.T) {
		otherDir := filepath.Join(t.TempDir(), "books", "itunes")
		if err := os.MkdirAll(otherDir, 0o755); err != nil {
			t.Fatal(err)
		}
		path := writeFixtureITL(t, otherDir, "iTunes Library.itl", payload)
		_, err := ApplyITLOperations(path, path, ops)
		if err == nil || !strings.Contains(err.Error(), "location-form") {
			t.Fatalf("a non-AO library with .itunes-writeback/ locations must be rejected by location-form, got: %v", err)
		}
	})

	t.Run("a marker outside the library's root is still a leak", func(t *testing.T) {
		leaky := writebackTracks(30)
		leaky[3].location = `W:\elsewhere\.itunes-writeback\iTunes Media\leak.m4b`
		path := writeFixtureITL(t, aoDir, "iTunes Library.itl", buildPayloadFromTracks(leaky))
		_, err := ApplyITLOperations(path, path, ops)
		if err == nil || !strings.Contains(err.Error(), "location-form") {
			t.Fatalf("a staging marker outside the AO root must still be rejected, got: %v", err)
		}
	})
}
