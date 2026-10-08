// file: internal/itunes/library_shape.go
// version: 2.0.0
// guid: 2b7e9c14-6a05-4d38-9f27-8c1b3a0e5d62
// last-edited: 2026-10-07
//
// isAudiobookITL classifies a binary .itl track as an audiobook. The cross-type
// PID census uses it. The target-shape guard that also lived here protected
// the rebuild writers, which were removed with iTunes write-back on 2026-10-07.

package itunes

import (
	"strings"
)

// isAudiobookITL classifies a binary ITLTrack as an audiobook, mirroring the core
// of IsAudiobook (which takes the XML *Track): Kind, Genre, and Location signals.
func isAudiobookITL(t *ITLTrack) bool {
	if t == nil {
		return false
	}
	kind := strings.ToLower(t.Kind)
	if strings.Contains(kind, "audiobook") || strings.Contains(kind, "spoken word") {
		return true
	}
	genre := strings.ToLower(t.Genre)
	if strings.Contains(genre, "audiobook") || strings.Contains(genre, "spoken") {
		return true
	}
	loc := strings.ToLower(t.Location)
	return strings.Contains(loc, "audiobooks")
}
