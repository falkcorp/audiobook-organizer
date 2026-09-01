// file: internal/fingerprint/backfill_utils.go
// version: 1.1.0
// guid: e5f6a7b8-c9d0-e1f2-a3b4-c5d6e7f8a9b0

package fingerprint

import (
	"os"
	"path/filepath"
	"strings"
)

// FingerprintableExtensions are the containers fpcalc/ffmpeg can DECODE for
// this package. It is deliberately NOT the library's supported_extensions and
// must not be widened to match it: .aax and .aaxc are library extensions and
// are DRM-encrypted (see internal/audioutil.DetectDRM — this application has
// no activation-bytes path and cannot decode them at all), so including them
// would only buy a decode failure per file. The narrowness is the point, and
// the name now says so.
//
// internal/config imports this package, so a config-driven variant is not even
// expressible here without an import cycle. That is a useful accident: it
// keeps the capability question and the membership question apart.
var FingerprintableExtensions = map[string]bool{
	".mp3":  true,
	".m4b":  true,
	".m4a":  true,
	".aac":  true,
	".flac": true,
	".opus": true,
	".ogg":  true,
	".wma":  true,
	".wav":  true,
}

// IsFingerprintable reports whether filePath is in a container this package
// can decode. It answers a capability question, not "is this file part of the
// library?" — for that, call config.SupportedExtensionSet().MatchPath.
func IsFingerprintable(filePath string) bool {
	if filePath == "" {
		return false
	}
	ext := strings.ToLower(filepath.Ext(filePath))
	_, ok := FingerprintableExtensions[ext]
	return ok
}

// FileExists checks if a file exists on disk.
func FileExists(filePath string) bool {
	_, err := os.Stat(filePath)
	return err == nil
}
