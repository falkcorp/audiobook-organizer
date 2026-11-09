// file: internal/bookfileaudio/probe_outcome.go
// version: 1.0.2
// guid: f73c3b52-b052-4d0f-9243-cf0b7a98bffc
// last-edited: 2026-11-09

package bookfileaudio

import (
	"context"
	"errors"
	"io/fs"
	"os/exec"
	"strings"
	"sync"
	"syscall"

	"github.com/falkcorp/audiobook-organizer/internal/audioutil"
	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	"github.com/falkcorp/audiobook-organizer/internal/mediainfo"
	"github.com/falkcorp/mediacore/library"
)

// FailureKind classifies why a duration probe produced no duration.
type FailureKind int

const (
	// FailureFile: the probe ran on a present file and it failed: a non-zero
	// exit, unparseable output, 0 s, no media info, or only an estimate. Counted
	// as an attempt on the row (database.BookFile.RecordDurationProbeFailure).
	FailureFile FailureKind = iota
	// FailureTimeout: the probe did not finish in time. Counted as an attempt,
	// with database.DurationProbeTimeoutPrefix on LastError so a reader can
	// surface timeouts only at the attempt ceiling (a stalled mount must not
	// flag a good file).
	FailureTimeout
	// FailureEnvironment: the probe could not run, or the file could not be
	// reached: ffprobe is not installed, the run was cancelled, the file or its
	// directory is not readable by this process (fs.ErrPermission), or a network
	// mount went away (ESTALE, ENOTCONN). Says nothing about the file: the row
	// stays unprobed, no attempt is counted, and the cause is warned about once.
	FailureEnvironment
	// FailureAbsent: the file is not there (a stale row). Not counted: the row
	// stays unprobed, and a repoint fixes it, not a retry.
	FailureAbsent
	// FailureUnsupported: the extension is not an audio extension the library
	// knows (mediainfo.ErrUnsupportedFormat) and the probe could not read it
	// either. Not counted: the library does not expect to play such a file, so it
	// must not end up flagged as an unreadable audio file. A KNOWN audio extension
	// that ffprobe cannot read is FailureFile.
	FailureUnsupported
)

// CountsAsAttempt reports whether a failure of this kind is recorded on the row.
func (k FailureKind) CountsAsAttempt() bool {
	return k == FailureFile || k == FailureTimeout
}

// String names the kind for logs and test failures.
func (k FailureKind) String() string {
	switch k {
	case FailureFile:
		return "file"
	case FailureTimeout:
		return "timeout"
	case FailureEnvironment:
		return "environment"
	case FailureAbsent:
		return "absent"
	case FailureUnsupported:
		return "unsupported"
	}
	return "unknown"
}

// lastErrorMaxRunes bounds a stored DurationOK.LastError: it is served to the
// UI, and an ffprobe error can carry a whole stderr.
const lastErrorMaxRunes = 240

// ClassifyProbeError says what an error from a duration probe means, and the
// text to store as the attempt's LastError (meaningful only when
// kind.CountsAsAttempt()). It is the ONE classifier every probe writer uses
// (EnsureDuration, the scanner's replaced-file probe, the duration backfill),
// so they cannot disagree about which failures count.
//
// err is the error chain of mediainfo.Extract or MediaInfo.DurationErr. Order
// matters: a failure that says nothing about the file is recognised first.
func ClassifyProbeError(err error) (kind FailureKind, lastError string) {
	switch {
	case err == nil:
		return FailureFile, "probe failed (no detail)"
	case errors.Is(err, exec.ErrNotFound), errors.Is(err, audioutil.ErrFFprobeNotAvailable),
		errors.Is(err, context.Canceled),
		errors.Is(err, fs.ErrPermission), errors.Is(err, syscall.ESTALE), errors.Is(err, syscall.ENOTCONN):
		return FailureEnvironment, tidyProbeError(err)
	case errors.Is(err, fs.ErrNotExist):
		return FailureAbsent, tidyProbeError(err)
	case errors.Is(err, mediainfo.ErrUnsupportedFormat):
		return FailureUnsupported, tidyProbeError(err)
	case errors.Is(err, ErrProbeTimeout), errors.Is(err, library.ErrTimeout), errors.Is(err, context.DeadlineExceeded):
		return FailureTimeout, database.DurationProbeTimeoutPrefix + tidyProbeError(err)
	}
	return FailureFile, tidyProbeError(err)
}

// tidyProbeError is err's text on one line, bounded to lastErrorMaxRunes.
func tidyProbeError(err error) string {
	msg := strings.Join(strings.Fields(err.Error()), " ")
	if r := []rune(msg); len(r) > lastErrorMaxRunes {
		msg = string(r[:lastErrorMaxRunes]) + "…"
	}
	return msg
}

// ProbeError is the error for a probe that gave no real duration, built from
// what the probe returned: err when it failed outright, else info's
// DurationErr, else a description of what info lacks. nil when info carries a
// real (non-estimated, positive) duration. The estimate case carries the
// underlying ffprobe error when there is one, so a missing ffprobe is still
// classified as the environment failure it is.
func ProbeError(info *mediainfo.MediaInfo, err error) error {
	switch {
	case err != nil:
		return err
	case info == nil:
		return errors.New("probe returned no media info")
	case realDuration(info) > 0:
		return nil
	case info.DurationErr != nil:
		return info.DurationErr
	case info.DurationEstimated:
		return errors.New("only an estimated duration (no stream duration)")
	}
	return errors.New("probe returned 0s")
}

// RecordProbeFailure applies a failed duration probe of bf to the row:
//
//   - FailureFile / FailureTimeout: bf.RecordDurationProbeFailure; returns
//     SourceProbeFailed.
//   - FailureEnvironment / FailureAbsent / FailureUnsupported: nothing is written, the row stays
//     unprobed; returns SourceProbeUnavailable. The first of each cause in the
//     process is logged at Warn, later ones at Debug.
//
// bf is untouched (and SourceNone returned) when err is nil. path is for the
// log line only. log may be nil.
func RecordProbeFailure(bf *database.BookFile, path string, err error, log logger.LevelLogger) Source {
	if bf == nil || err == nil {
		return SourceNone
	}
	if log == nil {
		log = defaultLog
	}
	kind, msg := ClassifyProbeError(err)
	if !kind.CountsAsAttempt() {
		warnUnavailableOnce(kind, log, path, msg)
		return SourceProbeUnavailable
	}
	bf.RecordDurationProbeFailure(msg)
	log.Warn("book file %s: duration probe failed (%s, attempt %d): %s",
		logger.SanitizeLogValue(path), kind, bf.Scan.DurationOK.Attempts, logger.SanitizeLogValue(msg))
	return SourceProbeFailed
}

// unavailableWarned holds the causes already warned about. A missing ffprobe
// fails every probe in a run and a stale row's file is absent on every scan, so
// a per-file Warn would be tens of thousands of lines.
var unavailableWarned sync.Map

// warnDemoter is implemented by a logger that sends Warn lines to a quieter
// level (a dry run's). Its lines must not use up the once-per-cause warning, or
// the real run that follows in the same process would never warn.
type warnDemoter interface{ DemotesWarn() bool }

func warnUnavailableOnce(kind FailureKind, log logger.LevelLogger, path, msg string) {
	if d, ok := log.(warnDemoter); ok && d.DemotesWarn() {
		log.Warn("book file %s: duration probe could not run (%s): %s; row stays unprobed",
			logger.SanitizeLogValue(path), kind, logger.SanitizeLogValue(msg))
		return
	}
	if _, seen := unavailableWarned.LoadOrStore(kind, struct{}{}); seen {
		log.Debug("book file %s: duration probe could not run (%s): %s; row stays unprobed",
			logger.SanitizeLogValue(path), kind, logger.SanitizeLogValue(msg))
		return
	}
	log.Warn("book file %s: duration probe could not run (%s): %s; the row stays unprobed and no attempt is counted (logged once per cause, later ones at debug)",
		logger.SanitizeLogValue(path), kind, logger.SanitizeLogValue(msg))
}
