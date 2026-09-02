// file: internal/metadata/filename_track_test.go
// version: 1.0.0
// guid: 8b9c0d1e-2f3a-4b5c-6d7e-8f9a0b1c2d3e

package metadata

import (
	"fmt"
	"testing"
)

func TestExtractTrackInfoFromFilename(t *testing.T) {
	tests := []struct {
		name      string
		filePath  string
		wantTrack *int
		wantTotal *int
		wantDisk  *int
	}{
		{"Part X of Y", "/books/Part 3 of 12.mp3", new(3), new(12), nil},
		{"XX of YY", "/books/03 of 12.mp3", new(3), new(12), nil},
		{"Track XX", "/books/Track 05.mp3", new(5), nil, nil},
		{"Chapter XX", "/books/Chapter 07.m4b", new(7), nil, nil},
		{"Leading number", "/books/01 - Introduction.mp3", new(1), nil, nil},
		{"Leading number underscore", "/books/02_Chapter One.mp3", new(2), nil, nil},
		{"No track info", "/books/Introduction.mp3", nil, nil, nil},
		{"Disk in name", "/books/Disk 2 of 3 - Track 05.mp3", new(2), new(3), new(2)},
		{"CD prefix no track", "/books/CD1 - Opening.mp3", nil, nil, new(1)},
		{"Track after CD", "/books/CD2 Track 03.mp3", new(3), nil, new(2)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			info := ExtractTrackInfoFromFilename(tt.filePath)
			if !intPtrEq(info.TrackNumber, tt.wantTrack) {
				t.Errorf("TrackNumber = %v, want %v", derefInt(info.TrackNumber), derefInt(tt.wantTrack))
			}
			if !intPtrEq(info.TotalTracks, tt.wantTotal) {
				t.Errorf("TotalTracks = %v, want %v", derefInt(info.TotalTracks), derefInt(tt.wantTotal))
			}
			if !intPtrEq(info.DiskNumber, tt.wantDisk) {
				t.Errorf("DiskNumber = %v, want %v", derefInt(info.DiskNumber), derefInt(tt.wantDisk))
			}
		})
	}
}

func TestExtractTrackInfoBatch(t *testing.T) {
	paths := []string{
		"/books/01 - Chapter One.mp3",
		"/books/02 - Chapter Two.mp3",
		"/books/03 - Chapter Three.mp3",
	}
	results := ExtractTrackInfoBatch(paths)
	if len(results) != 3 {
		t.Fatalf("got %d results, want 3", len(results))
	}
	// All should have track numbers
	for i, r := range results {
		if r.TrackNumber == nil || *r.TrackNumber != i+1 {
			t.Errorf("results[%d].TrackNumber = %v, want %d", i, derefInt(r.TrackNumber), i+1)
		}
		// Total should be extrapolated to 3
		if r.TotalTracks == nil || *r.TotalTracks != 3 {
			t.Errorf("results[%d].TotalTracks = %v, want 3", i, derefInt(r.TotalTracks))
		}
	}
}

func intPtrEq(a, b *int) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	return *a == *b
}

func derefInt(p *int) string {
	if p == nil {
		return "<nil>"
	}
	return fmt.Sprintf("%d", *p)
}
