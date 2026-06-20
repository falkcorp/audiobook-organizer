// file: internal/metadata/track_disc_test.go
// version: 1.0.0
// guid: c4f8a1d2-7b39-4e60-8a51-2f6c9b0e3d74
// last-edited: 2026-06-20

package metadata

import "testing"

func TestParseSlashPair(t *testing.T) {
	cases := []struct {
		in            string
		number, total int
	}{
		{"15", 15, 0},
		{"15/47", 15, 47},
		{" 7 / 26 ", 7, 26},
		{"0", 0, 0},
		{"", 0, 0},
		{"x", 0, 0},
		{"3/", 3, 0},
		{"-1/9", 0, 0},
	}
	for _, c := range cases {
		n, total := parseSlashPair(c.in)
		if n != c.number || total != c.total {
			t.Errorf("parseSlashPair(%q) = (%d,%d), want (%d,%d)", c.in, n, total, c.number, c.total)
		}
	}
}

func TestBuildMetadataFromTaglibMap_TrackDisc(t *testing.T) {
	tags := map[string][]string{
		"ALBUM":       {"Cage of Souls"},
		"ARTIST":      {"Adrian Tchaikovsky"},
		"COMPOSER":    {"David Thorpe"}, // narrator — must NOT become the author
		"TRACKNUMBER": {"15/47"},
		"DISCNUMBER":  {"1/1"},
		"ASIN":        {"B0BTDSTWG9"},
	}
	md := BuildMetadataFromTaglibMap(tags, "/x/47.mp3", nil)
	if md.TrackNumber != 15 || md.TrackTotal != 47 {
		t.Errorf("track = %d/%d, want 15/47", md.TrackNumber, md.TrackTotal)
	}
	if md.DiscNumber != 1 || md.DiscTotal != 1 {
		t.Errorf("disc = %d/%d, want 1/1", md.DiscNumber, md.DiscTotal)
	}
	if md.Album != "Cage of Souls" || md.Artist != "Adrian Tchaikovsky" {
		t.Errorf("identity = %q/%q, want Cage of Souls/Adrian Tchaikovsky", md.Album, md.Artist)
	}
	if md.ASIN != "B0BTDSTWG9" {
		t.Errorf("ASIN = %q, want B0BTDSTWG9", md.ASIN)
	}
}
