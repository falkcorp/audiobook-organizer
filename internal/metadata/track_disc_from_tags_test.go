// file: internal/metadata/track_disc_from_tags_test.go
// version: 1.0.0
// guid: d6dd3529-3290-4f06-ab8d-e764f074d54b
// last-edited: 2026-09-12

package metadata

import "testing"

func TestTrackDiscFromTags(t *testing.T) {
	cases := []struct {
		name                string
		tags                map[string]string
		track, tTotal, disc int
		discTotal           int
	}{
		{name: "nil map", tags: nil},
		{name: "no position keys", tags: map[string]string{"TALB": "Book"}},
		{name: "ID3v2.3/2.4 TRCK TPOS", tags: map[string]string{"TRCK": "3/12", "TPOS": "1/2"}, track: 3, tTotal: 12, disc: 1, discTotal: 2},
		{name: "ID3v2.2 TRK TPA", tags: map[string]string{"TRK": "7/9", "TPA": "2/3"}, track: 7, tTotal: 9, disc: 2, discTotal: 3},
		{name: "MP4 trkn disk", tags: map[string]string{"trkn": "4", "disk": "2"}, track: 4, disc: 2},
		{name: "Vorbis lowercase keys", tags: map[string]string{"tracknumber": "5", "discnumber": "1/1"}, track: 5, disc: 1, discTotal: 1},
		{name: "case-insensitive match", tags: map[string]string{"trck": "8/10"}, track: 8, tTotal: 10},
		{name: "non-numeric track", tags: map[string]string{"TRCK": "side A"}},
		{name: "zero track", tags: map[string]string{"TRCK": "0/12"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			track, tTotal, disc, discTotal := TrackDiscFromTags(tc.tags)
			if track != tc.track || tTotal != tc.tTotal || disc != tc.disc || discTotal != tc.discTotal {
				t.Errorf("TrackDiscFromTags(%v) = %d/%d disc %d/%d, want %d/%d disc %d/%d",
					tc.tags, track, tTotal, disc, discTotal, tc.track, tc.tTotal, tc.disc, tc.discTotal)
			}
		})
	}
}
