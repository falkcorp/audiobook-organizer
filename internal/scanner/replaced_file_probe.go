// file: internal/scanner/replaced_file_probe.go
// version: 1.4.0
// guid: 6b2e9f41-8c37-4d05-a9e1-3f7d2c5b8a64
// last-edited: 2026-11-04

package scanner

import (
	"github.com/falkcorp/audiobook-organizer/internal/bookfileaudio"
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
// or only a filesize estimate), bf keeps a nil Duration and the merge drops the
// audio-derived fields: unverified data about a file that did change is not
// kept. That outcome is logged at Warn (sampled) and counted in the scan
// summary, because with ffprobe missing it happens to every replaced file and
// silently discards fingerprints and transcripts the backfills must rebuild.
//
// The outcome is also written to bf.Scan.DurationOK, classified like every
// other probe writer (bookfileaudio.RecordProbeFailure): a failed read of the
// present file is an attempt on the replacement's own record, which starts at
// zero because the bytes are new; a missing ffprobe or absent file is not.
// bf.Scan carries only that stage, and the upsert merge restores the stored
// row's other stages around it.
func probeReplacedFileAudio(bf *database.BookFile, filePath string, scanLog logger.Logger) {
	mi, err := mediainfo.Extract(filePath)
	if err != nil || mi == nil {
		warnSampled(&replacedProbeNoAudioCount, scanLog,
			"replaced-file probe failed for %s: %v (its fingerprint and transcript are dropped; the scan summary counts these)",
			logger.SanitizeLogValue(filePath), err)
		bookfileaudio.RecordProbeFailure(bf, filePath, bookfileaudio.ProbeError(mi, err), scanLog)
		return
	}
	if mi.Codec != "" {
		bf.Codec = mi.Codec
	}
	if mi.Duration > 0 && !mi.DurationEstimated {
		if err := bf.SetMeasuredDuration(mi.Duration); err == nil {
			return
		}
	}
	warnSampled(&replacedProbeNoAudioCount, scanLog,
		"replaced-file probe of %s gave no real duration (estimated=%v; is ffprobe installed?): its fingerprint and transcript are dropped",
		logger.SanitizeLogValue(filePath), mi.DurationEstimated)
	bookfileaudio.RecordProbeFailure(bf, filePath, bookfileaudio.ProbeError(mi, nil), scanLog)
}

// seedDurationStage carries the stored row's duration stage onto bf, a row about
// to be probed again for a file with the SAME bytes, so a failed probe adds one
// attempt to the stored count instead of starting over at one. The upsert merge
// never sums attempts (a fresh failure is indistinguishable there from a copy
// that merely carries the stored count), so without this a file that keeps
// failing never reaches database.DeepScanMaxAttempts through a rescan.
//
// A stored hash that differs from bf's is a replaced file: its old record
// describes other bytes, so bf starts at zero (the merge resets the stored
// stage when the audio changed). An unknown hash on either side is not
// "changed" (judgeBookFileContent), so it seeds.
func seedDurationStage(bf, stored *database.BookFile) {
	if bf == nil || stored == nil {
		return
	}
	if hashKnownToDiffer(bf.FileHash, stored.FileHash) {
		return
	}
	bf.Scan.DurationOK = stored.Scan.DurationOK
}

// hashKnownToDiffer is true when both hashes are known and unequal: a replaced
// file. An unknown (empty) hash on either side is never "changed", the same
// rule database.judgeBookFileContent applies in the merge, so every scanner
// decision that has to agree with the merge asks it here.
func hashKnownToDiffer(a, b string) bool {
	return a != "" && b != "" && a != b
}

// storedBookFileAt is the stored LIVE row that owns filePath, the row the
// scanner's upsert will merge with, or nil when there is none. It is a point
// lookup on the book_file_path index. The caller reads its FileHash (was the
// file replaced?) and its Duration (would the merge keep a stored duration
// anyway, making a probe wasted work?).
//
// Live-only on purpose, unlike the other path lookups that decide ownership: a
// retired version's row is never what an upsert merges with (UpsertBookFile
// treats it as no match and mints a live row), so the row whose hash and
// duration answer "will the merge keep it?" is the live one or none. A file
// only a retired row claims never gets here as a new row anyway: the scan skips
// it first (checkFileOwnership, lookupScanExisting).
func storedBookFileAt(filePath string, scanLog logger.Logger) *database.BookFile {
	row, err := getStore().GetBookFileByPath(filePath)
	if err != nil {
		// No stored row means no replaced-file probe. The merge then drops
		// audio-derived data on a changed hash, the safe direction.
		scanLog.Debug("stored book file lookup failed for %s: %v (not probed)", logger.SanitizeLogValue(filePath), err)
		return nil
	}
	return row
}
