// file: internal/scanner/replaced_file_probe.go
// version: 1.1.0
// guid: 6b2e9f41-8c37-4d05-a9e1-3f7d2c5b8a64
// last-edited: 2026-09-13

package scanner

import (
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	"github.com/falkcorp/audiobook-organizer/internal/mediainfo"
)

// probeReplacedFileAudio puts the file's real duration and codec on bf, for a
// file whose content hash differs from the stored row's.
//
// The book_file merge keeps a row's audio-derived data (fingerprint, intro
// transcript, duration, stream info) across a hash change only when the
// incoming row proves the audio is the same: a Duration within one second of
// the stored one and no differing codec. The scanner's rows otherwise carry
// neither, so without this every real replacement kept the old recording's
// fingerprint. A changed hash is rare (our own tag writes record the new
// digest), so one probe per such file is cheap.
//
// When the probe yields no trustworthy duration (ffprobe missing or failing,
// or only a filesize estimate), bf keeps Duration zero and the merge drops the
// audio-derived fields: unverified data about a file that did change is not
// kept. That outcome is logged at Warn (sampled) and counted in the scan
// summary, because with ffprobe missing it happens to every replaced file and
// silently discards fingerprints and transcripts the backfills must rebuild.
func probeReplacedFileAudio(bf *database.BookFile, filePath string, scanLog logger.Logger) {
	mi, err := mediainfo.Extract(filePath)
	if err != nil || mi == nil {
		warnSampled(&replacedProbeNoAudioCount, scanLog,
			"replaced-file probe failed for %s: %v (its fingerprint and transcript are dropped; the scan summary counts these)",
			logger.SanitizeLogValue(filePath), err)
		return
	}
	if mi.Codec != "" {
		bf.Codec = mi.Codec
	}
	if mi.Duration > 0 && !mi.DurationEstimated {
		bf.Duration = mi.Duration
		return
	}
	warnSampled(&replacedProbeNoAudioCount, scanLog,
		"replaced-file probe of %s gave no real duration (estimated=%v; is ffprobe installed?): its fingerprint and transcript are dropped",
		logger.SanitizeLogValue(filePath), mi.DurationEstimated)
}

// storedFileHashAt is the FileHash of the stored row that owns filePath, the
// row the scanner's upsert will merge with, or "" when there is none. It is a
// point lookup on the book_file_path index.
func storedFileHashAt(filePath string, scanLog logger.Logger) string {
	row, err := getStore().GetBookFileByPath(filePath)
	if err != nil {
		// No stored hash means no probe. The merge then drops audio-derived
		// data on a changed hash, the safe direction.
		scanLog.Debug("stored book file lookup failed for %s: %v (not probed)", logger.SanitizeLogValue(filePath), err)
		return ""
	}
	if row == nil {
		return ""
	}
	return row.FileHash
}
