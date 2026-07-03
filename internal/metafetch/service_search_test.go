// file: internal/metafetch/service_search_test.go
// version: 1.0.0
// guid: a7e89f3c-5d21-4b8a-9c4e-1f2a3b4c5d6e
// last-edited: 2026-07-03

package metafetch

import (
	"testing"
)

func TestFilterCoverlessCandidates(t *testing.T) {
	tests := []struct {
		name        string
		input       []MetadataCandidate
		wantIndices map[int]bool // indices that should be in the output
		wantCount   int            // expected count of results
	}{
		{
			name: "ASIN candidate survives",
			input: []MetadataCandidate{
				{
					Title:     "Cover-less ASIN",
					CoverURL:  "",
					Source:    "Audnexus (Audible)",
					Score:     1.0,
				},
				{
					Title:    "With Cover",
					CoverURL: "http://example.com/cover.jpg",
					Source:   "Google Books",
					Score:    0.8,
				},
			},
			wantIndices: map[int]bool{0: true, 1: true},
			wantCount:   2,
		},
		{
			name: "Transcription-boosted candidate survives",
			input: []MetadataCandidate{
				{
					Title:                "Cover-less but Transcription Boosted",
					CoverURL:             "",
					Source:               "Google Books",
					Score:                0.7,
					TranscriptionBoosted: true,
				},
				{
					Title:    "With Cover",
					CoverURL: "http://example.com/cover.jpg",
					Source:   "Apple Books",
					Score:    0.9,
				},
			},
			wantIndices: map[int]bool{0: true, 1: true},
			wantCount:   2,
		},
		{
			name: "Top-scored cover-less candidate survives",
			input: []MetadataCandidate{
				{
					Title:    "Cover-less but top-scored",
					CoverURL: "",
					Source:   "Google Books",
					Score:    0.95,
				},
				{
					Title:    "With Cover",
					CoverURL: "http://example.com/cover.jpg",
					Source:   "Apple Books",
					Score:    0.85,
				},
			},
			wantIndices: map[int]bool{0: true, 1: true},
			wantCount:   2,
		},
		{
			name: "Ordinary cover-less non-top candidate is dropped",
			input: []MetadataCandidate{
				{
					Title:    "With Cover (mid-score)",
					CoverURL: "http://example.com/cover1.jpg",
					Source:   "Google Books",
					Score:    0.7,
				},
				{
					Title:    "Cover-less top-scored",
					CoverURL: "",
					Source:   "Apple Books",
					Score:    0.9,
				},
				{
					Title:    "Cover-less low-scored",
					CoverURL: "",
					Source:   "OpenLibrary",
					Score:    0.5,
				},
			},
			wantIndices: map[int]bool{0: true, 1: true}, // 0 and 1 survive, 2 is dropped
			wantCount:   2,
		},
		{
			name: "All-coverless input is returned unchanged",
			input: []MetadataCandidate{
				{
					Title:    "No Cover 1",
					CoverURL: "",
					Source:   "Google Books",
					Score:    0.8,
				},
				{
					Title:    "No Cover 2",
					CoverURL: "",
					Source:   "Apple Books",
					Score:    0.6,
				},
			},
			wantIndices: map[int]bool{0: true, 1: true},
			wantCount:   2,
		},
		{
			name:        "Empty input returns empty",
			input:       []MetadataCandidate{},
			wantIndices: map[int]bool{},
			wantCount:   0,
		},
		{
			name: "All candidates with covers are returned unchanged",
			input: []MetadataCandidate{
				{
					Title:    "With Cover 1",
					CoverURL: "http://example.com/cover1.jpg",
					Source:   "Google Books",
					Score:    0.8,
				},
				{
					Title:    "With Cover 2",
					CoverURL: "http://example.com/cover2.jpg",
					Source:   "Apple Books",
					Score:    0.7,
				},
			},
			wantIndices: map[int]bool{0: true, 1: true},
			wantCount:   2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := filterCoverlessCandidates(tt.input)

			if len(result) != tt.wantCount {
				t.Errorf("got %d results, want %d", len(result), tt.wantCount)
			}

			// Check that the correct indices are present
			resultMap := make(map[string]MetadataCandidate)
			for _, c := range result {
				resultMap[c.Title] = c
			}

			for idx := range tt.input {
				if tt.wantIndices[idx] {
					if _, ok := resultMap[tt.input[idx].Title]; !ok {
						t.Errorf("expected candidate at index %d (title: %q) to be in result, but it wasn't", idx, tt.input[idx].Title)
					}
				} else {
					if _, ok := resultMap[tt.input[idx].Title]; ok {
						t.Errorf("expected candidate at index %d (title: %q) to be filtered out, but it wasn't", idx, tt.input[idx].Title)
					}
				}
			}
		})
	}
}
