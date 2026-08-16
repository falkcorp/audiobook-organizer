// file: internal/server/path_format_test.go
// version: 1.0.0
// guid: b8c4d2e3-f5a6-7890-bcde-f01234567890

package server

import (
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/organizer"
	"github.com/stretchr/testify/require"
)

func TestFormatSegmentTitle(t *testing.T) {
	tests := []struct {
		name     string
		format   string
		title    string
		track    int
		total    int
		expected string
	}{
		{"default format", "{title} - {track}_{total_tracks}", "Leviathan Falls", 15, 51, "Leviathan Falls - 15_51"},
		{"of format", "{title} - {track} of {total_tracks}", "Leviathan Falls", 15, 51, "Leviathan Falls - 15 of 51"},
		{"part format", "{title} - Part {track}", "Leviathan Falls", 15, 51, "Leviathan Falls - Part 15"},
		{"zero-padded", "{track:02d} - {title}", "Leviathan Falls", 3, 51, "03 - Leviathan Falls"},
		{"three-digit pad", "{track:03d} - {title}", "Leviathan Falls", 3, 200, "003 - Leviathan Falls"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := organizer.FormatSegmentTitle(tt.format, tt.title, tt.track, tt.total)
			require.Equal(t, tt.expected, result)
		})
	}
}

func TestSanitizePathComponent(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"James S.A. Corey", "James S.A. Corey"},
		{"Title: Subtitle", "Title - Subtitle"},
		{"No/Slash/Here", "No Slash Here"},
		{"What?", "What"},
		{"File*Name", "FileName"},
		{"  extra  spaces  ", "extra spaces"},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			require.Equal(t, tt.expected, organizer.SanitizePathComponent(tt.input))
		})
	}
}

func TestCollapseEmptySegments(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"a//b", "a/b"},
		{"a..b", "a.b"},
		{"a/./b", "a/b"},
		{"a/b/", "a/b"},
		{"/a/b", "a/b"},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			require.Equal(t, tt.expected, organizer.CollapseEmptySegments(tt.input))
		})
	}
}
