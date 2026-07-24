// file: internal/config/itunes_libraries_test.go
// version: 1.0.0
// guid: 8a1c4e70-2d63-4b95-9f28-5c0e7a3b1d46
// last-edited: 2026-07-23

package config

import (
	"strings"
	"testing"
)

func TestLibrarySetResolve(t *testing.T) {
	const aoITL = "/mnt/bigdata/books/audiobook-organizer/.itunes-writeback/iTunes Library.itl"
	const origXML = "/mnt/bigdata/books/itunes/iTunes Library.xml"

	t.Run("unconfigured is a no-op (legacy paths preserved)", func(t *testing.T) {
		c := &ITunesConfig{LibraryReadPath: "/legacy/read.xml", LibraryWritePath: "/legacy/write.itl"}
		c.Resolve()
		if c.LibraryReadPath != "/legacy/read.xml" || c.LibraryWritePath != "/legacy/write.itl" {
			t.Fatalf("unconfigured Resolve mutated legacy paths: read=%q write=%q", c.LibraryReadPath, c.LibraryWritePath)
		}
	})

	t.Run("import_source=ao reads + writes the AO itl", func(t *testing.T) {
		c := &ITunesConfig{Libraries: LibrarySet{
			AO:           LibraryRef{ITLPath: aoITL},
			Original:     LibraryRef{XMLPath: origXML},
			ImportSource: "ao",
		}}
		c.Resolve()
		if c.LibraryWritePath != aoITL {
			t.Errorf("write path = %q, want AO itl", c.LibraryWritePath)
		}
		if c.LibraryReadPath != aoITL {
			t.Errorf("read path = %q, want AO itl (import_source=ao)", c.LibraryReadPath)
		}
	})

	t.Run("import_source=original reads the Original xml, still writes AO itl", func(t *testing.T) {
		c := &ITunesConfig{Libraries: LibrarySet{
			AO:           LibraryRef{ITLPath: aoITL},
			Original:     LibraryRef{XMLPath: origXML},
			ImportSource: "original",
		}}
		c.Resolve()
		if c.LibraryWritePath != aoITL {
			t.Errorf("write path = %q, want AO itl (write target never changes)", c.LibraryWritePath)
		}
		if c.LibraryReadPath != origXML {
			t.Errorf("read path = %q, want Original xml", c.LibraryReadPath)
		}
	})
}

func TestValidateLibraries(t *testing.T) {
	const origITL = "/mnt/bigdata/books/itunes/iTunes Library.itl"
	const aoITL = "/mnt/bigdata/books/audiobook-organizer/.itunes-writeback/iTunes Library.itl"
	protected := []string{"/mnt/bigdata/books/itunes/"}

	good := func() *ITunesConfig {
		return &ITunesConfig{
			WriteBackEnabled: true,
			Libraries: LibrarySet{
				Original:     LibraryRef{ITLPath: origITL, Frozen: true},
				AO:           LibraryRef{ITLPath: aoITL},
				PointedAt:    "ao",
				ImportSource: "ao",
			},
		}
	}

	t.Run("unconfigured returns no errors (back-compat)", func(t *testing.T) {
		if errs := (&ITunesConfig{WriteBackEnabled: true}).ValidateLibraries(protected); errs != nil {
			t.Fatalf("unconfigured should be inert, got %v", errs)
		}
	})

	t.Run("valid config passes", func(t *testing.T) {
		if errs := good().ValidateLibraries(protected); len(errs) != 0 {
			t.Fatalf("valid config should pass, got %v", errs)
		}
	})

	t.Run("original not protected -> error", func(t *testing.T) {
		errs := good().ValidateLibraries(nil) // no protected paths
		if !hasErr(errs, "not covered by any protected_paths") {
			t.Fatalf("expected protected-paths error, got %v", errs)
		}
	})

	t.Run("AO under books/itunes -> error", func(t *testing.T) {
		c := good()
		c.Libraries.AO.ITLPath = "/mnt/bigdata/books/itunes/iTunes Library.itl"
		errs := c.ValidateLibraries(protected)
		if !hasErr(errs, "under books/itunes") {
			t.Fatalf("expected books/itunes error, got %v", errs)
		}
	})

	t.Run("original not frozen while pointed_at=ao -> error", func(t *testing.T) {
		c := good()
		c.Libraries.Original.Frozen = false
		errs := c.ValidateLibraries(protected)
		if !hasErr(errs, "frozen must be true") {
			t.Fatalf("expected frozen error, got %v", errs)
		}
	})

	t.Run("no AO write target while enabled -> error", func(t *testing.T) {
		c := good()
		c.Libraries.AO.ITLPath = ""
		errs := c.ValidateLibraries(protected)
		if !hasErr(errs, "must be set when itunes sync") {
			t.Fatalf("expected zero-value-target error, got %v", errs)
		}
	})
}

func hasErr(errs []string, substr string) bool {
	for _, e := range errs {
		if strings.Contains(e, substr) {
			return true
		}
	}
	return false
}
