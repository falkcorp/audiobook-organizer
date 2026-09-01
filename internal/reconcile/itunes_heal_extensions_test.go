// file: internal/reconcile/itunes_heal_extensions_test.go
// version: 1.0.0
// guid: 7bb15541-4dab-42aa-b32e-64edefef0aab
// last-edited: 2026-09-01

package reconcile

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/config"
)

// writeITunesXML builds a minimal iTunes Library.xml holding one track per
// supplied filename. Built at run time rather than checked in: a fixture file
// would be a Git LFS pointer in CI, and this repo has been bitten three times
// by tests that passed against a 129-byte pointer.
func writeITunesXML(t *testing.T, names ...string) string {
	t.Helper()
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + "\n")
	b.WriteString(`<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">` + "\n")
	b.WriteString(`<plist version="1.0"><dict><key>Tracks</key><dict>` + "\n")
	for i, n := range names {
		fmt.Fprintf(&b, `<key>%d</key><dict>`+
			`<key>Name</key><string>Chapter %d</string>`+
			`<key>Artist</key><string>An Author</string>`+
			`<key>Album</key><string>A Book</string>`+
			`<key>Location</key><string>file://localhost/X:/library/%s</string>`+
			`<key>Persistent ID</key><string>PID%08d</string>`+
			`<key>Track Number</key><integer>%d</integer>`+
			`</dict>`+"\n", 100+i, i+1, n, i, i+1)
	}
	b.WriteString(`</dict></dict></plist>` + "\n")

	path := filepath.Join(t.TempDir(), "iTunes Library.xml")
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func trackNames(tracks []iTunesTrack) []string {
	out := make([]string, 0, len(tracks))
	for _, tr := range tracks {
		out = append(out, filepath.Base(tr.Location))
	}
	sort.Strings(out)
	return out
}

// ParseITunesXML held a private 7-entry extension list. Every track below in a
// container outside that list was dropped before the heal run ever saw it — so
// an .aax or .wav book could not be healed, and the run reported a track count
// that simply did not include it.
func TestParseITunesXMLFollowsTheConfiguredExtensions(t *testing.T) {
	prev := config.Snapshot()
	config.Mutate(func(c *config.Config) { c.SupportedExtensions = nil })
	t.Cleanup(func() { config.Mutate(func(c *config.Config) { *c = prev }) })

	path := writeITunesXML(t,
		"Book.m4b",  // the old list knew this
		"Book.aax",  // it did not
		"Book.aaxc", // nor this
		"Book.aiff", // nor this
		"Book.mka",  // nor this
		"Book.oga",  // nor this
		"Book.wav",  // nor this
		"cover.jpg", // never audio
	)

	tracks, err := ParseITunesXML(path)
	if err != nil {
		t.Fatalf("ParseITunesXML: %v", err)
	}
	got := trackNames(tracks)
	want := []string{"Book.aax", "Book.aaxc", "Book.aiff", "Book.m4b", "Book.mka", "Book.oga", "Book.wav"}
	if len(got) != len(want) {
		t.Fatalf("parsed %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("parsed %v, want %v", got, want)
		}
	}
}

func TestParseITunesXMLNarrowsWithTheConfig(t *testing.T) {
	prev := config.Snapshot()
	config.Mutate(func(c *config.Config) { c.SupportedExtensions = []string{".m4b"} })
	t.Cleanup(func() { config.Mutate(func(c *config.Config) { *c = prev }) })

	tracks, err := ParseITunesXML(writeITunesXML(t, "Book.m4b", "Book.aax"))
	if err != nil {
		t.Fatalf("ParseITunesXML: %v", err)
	}
	if got := trackNames(tracks); len(got) != 1 || got[0] != "Book.m4b" {
		t.Fatalf("parsed %v, want only the configured .m4b", got)
	}
}
