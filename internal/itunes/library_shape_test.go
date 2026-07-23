// file: internal/itunes/library_shape_test.go
// version: 1.0.0
// guid: 7d2b9e04-3a61-4c85-9f18-6c0e1a5b3d72
// last-edited: 2026-07-23

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

func TestShapeFromLibraryAndGuard(t *testing.T) {
	music := ITLTrack{Kind: "MPEG audio file", Genre: "Rock", Location: `W:\Music\x.mp3`}
	book := ITLTrack{Kind: "Audiobook", Location: `W:\itunes\iTunes Media\Audiobooks\a.m4b`}

	// Disposable prototype: audiobook-only, few playlists → NOT real.
	proto := &ITLLibrary{Tracks: []ITLTrack{book, book, book}, Playlists: make([]ITLPlaylist, 5)}
	if s := shapeFromLibrary(proto); s.LooksReal {
		t.Errorf("prototype should not look real: %+v", s)
	}

	// Real library: many non-audiobook tracks → real.
	tracks := make([]ITLTrack, 0, 1200)
	for range 1100 {
		tracks = append(tracks, music)
	}
	real1 := &ITLLibrary{Tracks: tracks, Playlists: make([]ITLPlaylist, 10)}
	s := shapeFromLibrary(real1)
	if !s.LooksReal || s.NonAudiobookTracks != 1100 {
		t.Errorf("real-by-nonaudiobook: %+v, want LooksReal + 1100 non-audiobook", s)
	}

	// Real by playlist count alone (many playlists, few tracks).
	real2 := &ITLLibrary{Tracks: []ITLTrack{book}, Playlists: make([]ITLPlaylist, 60)}
	if !shapeFromLibrary(real2).LooksReal {
		t.Error("60 playlists should look real")
	}
}
