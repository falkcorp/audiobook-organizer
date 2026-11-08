// file: internal/books/logging.go
// version: 1.0.0
// guid: bed10864-095d-4c93-8263-f11300b314b9
// last-edited: 2026-11-08

package books

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"

	"github.com/falkcorp/audiobook-organizer/internal/activity"
	"github.com/falkcorp/mediacore/app"
	corelog "github.com/falkcorp/mediacore/log"
)

// activityTapName is the activity feed's tap on the App's log pipeline: the
// name its drops are counted under (mediacore_log_dropped_total{sink}).
const activityTapName = "activity"

// activityTapQueue is the tap's queue, in records. A full queue drops the
// record and counts it; the pipeline never waits on the feed.
const activityTapQueue = 4096

// logFilePath is core.log.file.path for ABO, relative to the state directory:
// the spec's default, logs/<app-id>.log (05-logging section 8). HEAD wrote
// logs/audiobook-organizer-<date>.log in the working directory, never rotated;
// that path is not reproduced.
const logFilePath = "logs/" + appID + ".log"

// logSettings is ABO's core.log section for release R1, which has no config
// layer to read it from (04 section 8.1): the defaults, with the level from
// log_level / --log-level (05-logging section 11 item 6: it now sets the
// handler level) and, when the state directory is known, the file output
// under it. level "" is info. A level core does not know falls back to info
// and is reported in warn, since a stored typo must not stop serve booting
// before core's own config validation exists (F3), and must not be silent.
// Everything else is the defaults: text, 100 MiB x 7 files, tee to the log
// stream.
func logSettings(level, stateDir string) (s corelog.Settings, warn string) {
	s = corelog.DefaultSettings()
	if lv := strings.TrimSpace(level); lv != "" {
		if _, err := corelog.ParseLevel(lv); err != nil {
			warn = fmt.Sprintf("log_level %q is not trace, debug, info, warn or error; using info", level)
		} else {
			s.Level = strings.ToLower(lv)
		}
	}
	// The stream HEAD's lines went to: its activity writer teed to stdout.
	s.Output = "stdout"
	if stateDir != "" {
		s.Output = "file"
		s.File.Path = logFilePath
	}
	return s, warn
}

// logStream is the stream the log pipeline prints to (or tees to, beside the
// file): in.LogOut, stdout by default.
func logStream(w io.Writer) io.Writer {
	if w == nil {
		return os.Stdout
	}
	return w
}

// tapActivity registers the activity feed on a's log pipeline: every record
// at info or above is rendered as the slog text line HEAD's second default
// logger wrote into the activity writer, and fed to the writer's line parser,
// so the Activity page's system entries are unchanged (05-logging section 11
// item 1). Nothing to tap when the App has no pipeline of its own.
func tapActivity(a *app.App, w *activity.Writer) {
	if w == nil {
		return
	}
	if lg := a.Logging(); lg != nil {
		lg.Tap(activityTapName, slog.LevelInfo, activityTapQueue, activityTapFunc(w.Feed))
	}
}

// activityTapFunc is the tap's consumer: render, then feed.
func activityTapFunc(feed func([]byte)) func(context.Context, corelog.Record) {
	return func(_ context.Context, r corelog.Record) { feed(activityLine(r)) }
}

// activityLine renders r as the line HEAD's slog.NewTextHandler(activityWriter,
// info) wrote for the same call: time, level, msg, then the attributes in call
// order, one line. The record is already redacted. The module and the trace
// and span IDs are left off, as they never were on HEAD's lines (module is
// not a structural key of the line parser, so it would land in every
// summary). A record from a subsystem carries it as HEAD's logger shim did,
// as the message's "subsystem: " prefix, not as an attribute, so the row's
// summary and tags are the ones HEAD produced.
func activityLine(r corelog.Record) []byte {
	var buf bytes.Buffer
	h := slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})
	msg := r.Message
	if r.Subsystem != "" {
		msg = r.Subsystem + ": " + msg
	}
	out := slog.NewRecord(r.Time, r.Level, msg, 0)
	out.AddAttrs(r.Attrs...)
	// A handler error is a failed write to a bytes.Buffer: it has none.
	_ = h.Handle(context.Background(), out)
	return buf.Bytes()
}
