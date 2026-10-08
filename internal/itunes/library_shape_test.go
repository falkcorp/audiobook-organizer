// file: internal/itunes/library_shape_test.go
// version: 1.1.0
// guid: 7d2b9e04-3a61-4c85-9f18-6c0e1a5b3d72
// last-edited: 2026-10-07

package itunes

import "testing"

func TestIsAudiobookITL(t *testing.T) {
	cases := []struct {
		name string
		t    ITLTrack
		want bool
	}{
		{"kind audiobook", ITLTrack{Kind: "Audiobook"}, true},
		{"kind spoken word", ITLTrack{Kind: "Purchased spoken word"}, true},
		{"genre audiobook", ITLTrack{Genre: "Audiobook"}, true},
		{"location audiobooks", ITLTrack{Location: `W:\itunes\iTunes Media\Audiobooks\A\1.m4b`}, true},
		{"music track", ITLTrack{Kind: "MPEG audio file", Genre: "Rock", Location: `W:\Music\B\2.mp3`}, false},
		{"podcast", ITLTrack{Kind: "Podcast", Genre: "News", Location: `W:\Podcasts\C\3.mp3`}, false},
	}
	for _, c := range cases {
		if got := isAudiobookITL(&c.t); got != c.want {
			t.Errorf("%s: isAudiobookITL = %v, want %v", c.name, got, c.want)
		}
	}
}
