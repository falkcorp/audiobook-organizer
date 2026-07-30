// file: internal/audioutil/drm.go
// version: 1.0.0
// guid: 6ba10228-85ba-455d-a6a0-33ee1169bfbc
// last-edited: 2026-07-30

package audioutil

import (
	"path/filepath"
	"strings"
)

// DRM reason strings returned by DetectDRM. Kept as plain strings (not an
// exported type) so callers can log/serialize them without a conversion --
// mirrors how audioutil.Chapter's Title is a plain string, not an enum.
const (
	ReasonAudibleAAX  = "audible-aax"
	ReasonAudibleAAXC = "audible-aaxc"
)

// DetectDRM reports whether filePath is a DRM-protected audiobook format
// this app cannot decode or play, and why.
//
// Detection is extension-only (case-insensitive) and deliberately does NOT
// probe the container or codec: ffprobe/ffmpeg ship an unrelated demuxer
// literally named "aax" (CRIWARE AAX, a game-audio codec, nothing to do
// with Audible), and Audible's own AAX/AAXC container is otherwise a
// standard MPEG-4 box structure that ffprobe reads happily -- only the
// audio ESSENCE is encrypted, so there is no verified, repo-testable
// codec/format signature to check without a real Audible sample file
// (none exists in testdata/ and none should be fabricated). This means a
// renamed .aax -> .m4b file defeats detection; that is a known,
// documented limitation, not an oversight.
//
// Every extension in config.AppConfig's default SupportedExtensions list
// is otherwise treated as NOT protected -- this function must never flag a
// legitimate m4b/m4a/mp3/flac/etc. file.
func DetectDRM(filePath string) (protected bool, reason string) {
	ext := strings.ToLower(filepath.Ext(filePath))
	switch ext {
	case ".aax":
		return true, ReasonAudibleAAX
	case ".aaxc":
		return true, ReasonAudibleAAXC
	default:
		return false, ""
	}
}
