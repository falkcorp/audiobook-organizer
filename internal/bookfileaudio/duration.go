// file: internal/bookfileaudio/duration.go
// version: 1.0.0
// guid: 81298124-2825-49f4-b115-ae8aa117cd68
// last-edited: 2026-09-25

// Package bookfileaudio fills a book_file row's audio facts (duration, codec)
// at the moment the row is created, so no writer stores a readable file with
// Duration 0.
//
// Why it exists: ABS reports a book's duration as the SUM of its book_file
// durations (internal/server/handlers/abs/mapper.go), so a row created with
// Duration 0 makes the app show the book as "0" even when the file is fine and
// book.Duration is already right. Before this package every creation site built
// its own row and most of them never set Duration: the scanner's new-book path,
// the organizer's CreateOrganizedVersion copy (which propagated a 0 verbatim),
// the importer, server.ensureSingleFileBookFile, and two maintenance jobs. The
// fix is one rule in one place, applied by every creation site.
package bookfileaudio

import (
	"errors"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	"github.com/falkcorp/audiobook-organizer/internal/mediainfo"
)

// Source says where EnsureDuration took the row's duration from.
type Source int

const (
	// SourceNone: the row still has Duration 0 (no trustworthy value found).
	SourceNone Source = iota
	// SourceAlreadySet: the row arrived with Duration > 0 and was left alone.
	SourceAlreadySet
	// SourceCaller: the caller's already-read media info for this file.
	SourceCaller
	// SourceBook: the owning single-file book's Duration.
	SourceBook
	// SourceProbe: a header read of the file (mediainfo.Extract).
	SourceProbe
)

// String names the source for logs and test failures.
func (s Source) String() string {
	switch s {
	case SourceAlreadySet:
		return "already-set"
	case SourceCaller:
		return "caller"
	case SourceBook:
		return "book"
	case SourceProbe:
		return "probe"
	default:
		return "none"
	}
}

// Known is what the caller already has in hand about the row's audio. The zero
// value means "nothing": EnsureDuration then probes the file.
type Known struct {
	// Info is media info the caller ALREADY READ from this row's FilePath. When
	// non-nil it is final: its real duration is used, and when that duration is
	// only an estimate (or absent) the row stays 0. There is no fall back to
	// BookDurationSec and no re-probe, because for a scanned single-file book
	// book.Duration was copied from this very Info — estimate included — so the
	// fall back would write the estimate as real by the back door.
	Info *mediainfo.MediaInfo

	// BookDurationSec is the owning book's Duration in seconds, 0 when unknown.
	// It is used only when SingleFileBook is true: for a multi-file book the
	// book total says nothing about one file.
	BookDurationSec int

	// SingleFileBook is true when this row is the book's only file, so the book
	// duration IS the file duration.
	SingleFileBook bool
}

// ProbeTimeout bounds one header read. mediainfo.Extract calls tag.ReadFrom
// and ffprobe, which contain blocking syscalls that ignore cancellation; a
// malformed container once stalled a scan on one file for three days. On
// timeout the probe goroutine is abandoned (it unblocks when the kernel does)
// and the row keeps Duration 0.
const ProbeTimeout = 30 * time.Second

// errProbeTimeout is returned by boundedProbe when the probe outlives ProbeTimeout.
var errProbeTimeout = errors.New("media probe timed out")

// probe reads a file's media info. A package variable so tests can inject a
// fake without real audio or ffprobe.
var probe = mediainfo.Extract

// probeTimeout is ProbeTimeout, as a variable so the timeout path is testable.
var probeTimeout = ProbeTimeout

var defaultLog = logger.New("bookfileaudio")

// EnsureDuration fills bf.Duration (and bf.Codec when empty) before the row is
// written. The precedence is:
//
//  1. bf.Duration > 0 already: left alone.
//  2. known.Info (the caller already read this file): its duration when real,
//     otherwise 0. Never followed by 2 or 3.
//  3. known.SingleFileBook with known.BookDurationSec > 0: the book duration,
//     normalized through database.NormalizeDurationSec like relink_unlinked.
//  4. A bounded header probe of bf.FilePath.
//
// An ESTIMATED duration (mediainfo.MediaInfo.DurationEstimated, a fileSize ÷
// bitrate guess) is never written as real; the row stays 0, the same rule
// maintenance.duration-backfill applies.
//
// Only Codec is copied from media info, and only into an empty field. Bitrate,
// sample rate and channels are NOT: mediainfo fills them with hard-coded
// defaults (128 or 192 kbps, 44100 Hz, 2 channels) when the tag lacks them, and
// a default written into the row would read as a measurement. The scanner's
// probeReplacedFileAudio copies the same two fields for the same reason.
//
// A probe failure never fails the caller: it is logged at Warn with the path
// and the row keeps Duration 0. log may be nil.
func EnsureDuration(bf *database.BookFile, known Known, log logger.LevelLogger) Source {
	if bf == nil {
		return SourceNone
	}
	if log == nil {
		log = defaultLog
	}
	if bf.Duration > 0 {
		return SourceAlreadySet
	}
	if known.Info != nil {
		fillCodec(bf, known.Info)
		if d := realDuration(known.Info); d > 0 {
			bf.Duration = d
			return SourceCaller
		}
		return SourceNone
	}
	if known.SingleFileBook && known.BookDurationSec > 0 {
		bf.Duration = database.NormalizeDurationSec(bf.FileSize, known.BookDurationSec)
		return SourceBook
	}
	if bf.FilePath == "" {
		return SourceNone
	}
	info, err := boundedProbe(bf.FilePath)
	if err != nil || info == nil {
		log.Warn("book file %s: duration probe failed, row keeps duration 0: %v",
			logger.SanitizeLogValue(bf.FilePath), err)
		return SourceNone
	}
	fillCodec(bf, info)
	if d := realDuration(info); d > 0 {
		bf.Duration = d
		return SourceProbe
	}
	log.Warn("book file %s: probe gave no real duration (estimated=%v; is ffprobe installed?), row keeps duration 0",
		logger.SanitizeLogValue(bf.FilePath), info.DurationEstimated)
	return SourceNone
}

// realDuration is info's duration when it was read from the audio stream, 0
// when it is absent or only an estimate.
func realDuration(info *mediainfo.MediaInfo) int {
	if info == nil || info.DurationEstimated || info.Duration <= 0 {
		return 0
	}
	return info.Duration
}

func fillCodec(bf *database.BookFile, info *mediainfo.MediaInfo) {
	if bf.Codec == "" && info != nil && info.Codec != "" {
		bf.Codec = info.Codec
	}
}

// SetProbeForTesting replaces the file probe and returns a func restoring the
// previous one. For tests in other packages (organizer, scanner) that must
// prove a creation site fills Duration without real audio or ffprobe. Not safe
// alongside parallel tests that also probe.
func SetProbeForTesting(fn func(path string) (*mediainfo.MediaInfo, error)) (restore func()) {
	prev := probe
	probe = fn
	return func() { probe = prev }
}

// boundedProbe runs probe(path) under probeTimeout.
func boundedProbe(path string) (*mediainfo.MediaInfo, error) {
	type result struct {
		info *mediainfo.MediaInfo
		err  error
	}
	fn := probe
	ch := make(chan result, 1) // buffered: an abandoned probe must not block forever on send
	go func() {
		info, err := fn(path)
		ch <- result{info, err}
	}()
	timer := time.NewTimer(probeTimeout)
	defer timer.Stop()
	select {
	case r := <-ch:
		return r.info, r.err
	case <-timer.C:
		return nil, errProbeTimeout
	}
}
