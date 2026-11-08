// file: internal/books/logging_test.go
// version: 1.1.0
// guid: 5d21b7e8-3f64-4a09-9c1e-7b02e4a86d35
// last-edited: 2026-11-08

package books

import (
	"bytes"
	"context"
	"log"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/activity"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	corelog "github.com/falkcorp/mediacore/log"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestLogSettings: log_level / --log-level becomes the handler level; a level
// core does not know is reported and falls back to info; a state directory
// turns on the file output under it, teeing to the stream.
func TestLogSettings(t *testing.T) {
	for _, tc := range []struct {
		name, level, stateDir string
		wantLevel, wantOutput string
		wantWarn              bool
	}{
		{"empty is info", "", "", "info", "stdout", false},
		{"debug", "debug", "", "debug", "stdout", false},
		{"case is folded", " WARN ", "", "warn", "stdout", false},
		{"unknown falls back and warns", "loud", "", "info", "stdout", true},
		{"state dir opens the file", "error", "state", "error", "file", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, warn := logSettings(tc.level, tc.stateDir)
			assert.Equal(t, tc.wantLevel, s.Level)
			assert.Equal(t, tc.wantOutput, s.Output)
			assert.Equal(t, tc.wantWarn, warn != "", "warning %q", warn)
			if tc.wantOutput == "file" {
				assert.Equal(t, "logs/abo.log", s.File.Path)
				assert.True(t, s.File.TeeStderr, "the stream must still get the lines")
			}
		})
	}
}

// TestActivityLine_MatchesTheTextHandlerLine: the tap renders a record as
// the line HEAD's slog.NewTextHandler over the activity writer wrote for the
// same call (the Activity page's rows are built from that text), with a
// subsystem as the message prefix, and leaves the module off.
func TestActivityLine_MatchesTheTextHandlerLine(t *testing.T) {
	when := time.Date(2026, 11, 8, 10, 0, 0, 123_000_000, time.UTC)
	rec := corelog.Record{
		Time: when, Level: slog.LevelWarn, Module: "books", Subsystem: "operations",
		Message: "run finished",
		Attrs:   []slog.Attr{slog.String("op_id", "op-batcave"), slog.Int("books", 3), slog.String("note", "two words")},
	}
	var want bytes.Buffer
	h := slog.NewTextHandler(&want, &slog.HandlerOptions{Level: slog.LevelInfo})
	r := slog.NewRecord(when, slog.LevelWarn, "operations: run finished", 0)
	r.AddAttrs(slog.String("op_id", "op-batcave"), slog.Int("books", 3), slog.String("note", "two words"))
	require.NoError(t, h.Handle(context.Background(), r))
	assert.Equal(t, want.String(), string(activityLine(rec)))

	rec.Subsystem = ""
	assert.NotContains(t, string(activityLine(rec)), "module=")
	assert.Contains(t, string(activityLine(rec)), `msg="run finished"`)
}

// TestActivityTapKeepsSystemEntries: in serve's composition (the App's
// pipeline is the slog default, the activity feed is a tap on it) a slog
// record from a subsystem still becomes a system row on the Activity page's
// writer, with its operation ID, and the line is printed once, by the
// pipeline: the tap feeds the writer without its stdout tee.
func TestActivityTapKeepsSystemEntries(t *testing.T) {
	c := bootComposed(t, false, nil, nil)
	w := c.srv.ActivityLogWriter()
	require.NotNil(t, w, "the composed server has no activity writer")

	slog.Default().With("subsystem", "operations").Info("starting run", "op_id", "op-batcave", "path", "/lib/cave")

	var e database.ActivityEntry
	select {
	case e = <-w.Chan():
	case <-time.After(5 * time.Second):
		t.Fatal("the slog record never reached the activity writer: the tap is not registered")
	}
	assert.Equal(t, "system", e.Type)
	assert.Equal(t, "op-batcave", e.OperationID)
	assert.Contains(t, e.Summary, "starting run")
	assert.Equal(t, 1, strings.Count(c.newLog.String(), "starting run"), "the pipeline's stream carries the line once")

	// Below the tap's floor: printed (the level gate is info here) only if
	// debug is on, and never an Activity row.
	slog.Default().Debug("batcave debug chatter")
	select {
	case e := <-w.Chan():
		t.Fatalf("a debug record became an Activity row: %+v", e)
	case <-time.After(300 * time.Millisecond):
	}
}

// TestNoSecondDefaultLogger: with serve's composition booted the slog default
// is the App's pipeline and the stdlib log goes to it too; nothing installed
// a text handler or a log output over the activity writer (05-logging
// section 11 item 1).
func TestNoSecondDefaultLogger(t *testing.T) {
	c := bootComposed(t, false, nil, nil)
	require.NotNil(t, c.srv.ActivityLogWriter())

	slog.Info("batman default sentinel")
	log.Print("robin stdlib sentinel")
	out := c.newLog.String()
	assert.Contains(t, out, "batman default sentinel", "slog.Default is not the App's pipeline")
	assert.Contains(t, out, "robin stdlib sentinel", "the stdlib log is not the App's pipeline")
	assert.Contains(t, out, "module=core", "the default is not the pipeline's core root")

	// The writer is only a tap consumer: it has no stdout copy of these
	// lines to print twice.
	assert.NotSame(t, log.Writer(), c.srv.ActivityLogWriter())
}

// TestFileOutputUnderStateDir: with serve's state directory the log is
// written under it, at logs/abo.log, and the stream still gets the lines;
// without one there is no file.
func TestFileOutputUnderStateDir(t *testing.T) {
	state := t.TempDir()
	c := bootComposedIn(t, false, state, nil, nil)
	slog.Info("joker file sentinel")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	require.NoError(t, c.app.Logging().Close(ctx))

	b, err := os.ReadFile(filepath.Join(state, "logs", "abo.log"))
	require.NoError(t, err, "the log file is not under the state directory")
	assert.Contains(t, string(b), "joker file sentinel")
	assert.Contains(t, c.newLog.String(), "joker file sentinel", "the stream lost the line")
}

// TestLogLevelFlagApplies: log_level (what --log-level sets) is the pipeline's
// level: at debug a debug record prints, at info it does not, at warn an
// info record does not print either.
func TestLogLevelFlagApplies(t *testing.T) {
	for _, tc := range []struct {
		level                    string
		wantDebug, wantInfoShown bool
	}{
		{"debug", true, true},
		{"info", false, true},
		{"warn", false, false},
	} {
		t.Run(tc.level, func(t *testing.T) {
			c := bootComposed(t, false, func(cfg *config.Config) { cfg.LogLevel = tc.level }, nil)
			slog.Debug("riddler debug line")
			slog.Info("riddler info line")
			slog.Warn("riddler warn line")
			out := c.newLog.String()
			assert.Equal(t, tc.wantDebug, strings.Contains(out, "riddler debug line"), out)
			assert.Equal(t, tc.wantInfoShown, strings.Contains(out, "riddler info line"), out)
			assert.Contains(t, out, "riddler warn line")
		})
	}
}

// TestLogMetricsServedByBooks: the pipeline's counters are served through
// the App's /metrics handler under ABO's namespace (05-logging section 9): a
// record, a redaction and the always-present primary sink each have a sample.
func TestLogMetricsServedByBooks(t *testing.T) {
	c := bootComposed(t, false, nil, nil)
	slog.Info("robin metrics sentinel", "password", "batmobile-secret")

	w := c.serve(http.MethodGet, "/metrics", nil)
	require.Equal(t, http.StatusOK, w.Code)
	body := w.Body.String()
	for _, fam := range []string{
		"audiobook_organizer_log_records_total",
		"audiobook_organizer_log_dropped_total",
		"audiobook_organizer_log_redactions_total",
	} {
		assert.Contains(t, body, fam+"{", "family %s not served", fam)
	}
	assert.NotContains(t, body, "batmobile-secret")
}

// activityRef feeds line to a fresh activity writer, the parse HEAD's text
// handler line got, and returns the entry it built.
func activityRef(t *testing.T, c *composed, line string) database.ActivityEntry {
	t.Helper()
	ref := activity.NewWriter(nil, 8)
	ref.Feed([]byte(line))
	select {
	case e := <-ref.Chan():
		return e
	case <-time.After(5 * time.Second):
		t.Fatalf("no entry parsed from %q", line)
	}
	return database.ActivityEntry{}
}

// nextActivity is the next entry on the composed server's activity writer.
func nextActivity(t *testing.T, c *composed) database.ActivityEntry {
	t.Helper()
	select {
	case e := <-c.srv.ActivityLogWriter().Chan():
		return e
	case <-time.After(5 * time.Second):
		t.Fatal("no Activity entry arrived: the tap is not registered")
	}
	return database.ActivityEntry{}
}

// TestActivityTap_EntriesMatchHEADAndCarryNoModule: for the same log call the
// Activity entry the tap makes has the summary, details, source, level and
// operation of the entry HEAD's text handler line made, and neither carries a
// module attribute. HEAD's line is built with slog.NewTextHandler at info,
// the subsystem as the message prefix, as HEAD's logger shim wrote it.
func TestActivityTap_EntriesMatchHEADAndCarryNoModule(t *testing.T) {
	c := bootComposed(t, false, nil, nil)

	headLine := func(msg string, attrs ...slog.Attr) string {
		var buf bytes.Buffer
		h := slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo})
		r := slog.NewRecord(time.Now(), slog.LevelWarn, msg, 0)
		r.AddAttrs(attrs...)
		require.NoError(t, h.Handle(context.Background(), r))
		return buf.String()
	}
	for _, tc := range []struct {
		name string
		log  func()
		head string
	}{
		{"plain slog", func() {
			slog.Warn("scan finished", "op_id", "op-batcave", "books", 3, "path", "/lib/cave dir")
		}, headLine("scan finished", slog.String("op_id", "op-batcave"), slog.Int("books", 3), slog.String("path", "/lib/cave dir"))},
		{"subsystem logger", func() {
			logger.New("scheduler").Warn("tick %d", 7)
		}, headLine("scheduler: tick 7")},
		{"subsystem attribute", func() {
			slog.Default().With("subsystem", "operations").Warn("run finished", "opID", "op-gotham")
		}, headLine("operations: run finished", slog.String("opID", "op-gotham"))},
	} {
		t.Run(tc.name, func(t *testing.T) {
			want := activityRef(t, c, tc.head)
			tc.log()
			got := nextActivity(t, c)
			assert.Equal(t, want.Type, got.Type)
			assert.Equal(t, want.Tier, got.Tier)
			assert.Equal(t, want.Level, got.Level)
			assert.Equal(t, want.Source, got.Source)
			assert.Equal(t, want.OperationID, got.OperationID)
			assert.Equal(t, want.Summary, got.Summary)
			assert.Equal(t, want.Details, got.Details)
			assert.Equal(t, want.Tags, got.Tags)
			assert.NotContains(t, got.Summary, "module=")
			for k := range got.Details {
				assert.NotEqual(t, "module", k, "details carry a module attribute")
			}
		})
	}
}

// TestActivityTap_CoreOwnRecordsMakeNoEntries: records from core's own
// loggers (kernel, metrics, http, otel) and from a module's logger are not
// Activity rows: HEAD's feed only ever saw what went through slog.Default
// (the root logger here, which is the process default).
// The marker record, logged last, is the first entry that arrives: the tap's
// queue is FIFO.
func TestActivityTap_CoreOwnRecordsMakeNoEntries(t *testing.T) {
	c := bootComposed(t, false, nil, nil)
	lg := c.app.Logging()
	lg.Scope("core", "kernel").Info("kernel: listening", "addr", "cave:8080")
	lg.Scope("core", "metrics").Warn("metrics: family taken")
	lg.Scope("core", "http").Error("http: serve failed")
	lg.Scope("core", "otel").Info("otel: exporter built")
	lg.Module("books").Info("books module line")
	slog.Info("alfred marker")

	e := nextActivity(t, c)
	assert.Contains(t, e.Summary, "alfred marker", "a core-owned record became an Activity entry: %+v", e)
}
