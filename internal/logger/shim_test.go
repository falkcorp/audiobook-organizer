// file: internal/logger/shim_test.go
// version: 1.0.2
// guid: 8a14c3d7-52e9-4b60-a7f1-0c3d9e5b2a46
// last-edited: 2026-11-08

package logger

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"

	corelog "github.com/falkcorp/mediacore/log"
)

// shimPipeline is a core pipeline over buf at info with levels per scope.
func shimPipeline(t *testing.T, buf *bytes.Buffer, levels map[string]slog.Level) *corelog.Logging {
	t.Helper()
	l, err := corelog.New(corelog.Options{
		Output:   buf,
		Format:   corelog.FormatText,
		Level:    slog.LevelInfo,
		Levels:   levels,
		Redactor: corelog.NewRedactor(nil),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close(context.Background()) })
	return l
}

// TestShim_SubsystemIsAttribute (05-logging §11 item 4): the printf methods
// format their message, and the subsystem reaches the pipeline as an
// attribute of the record, not as text in the message, so a per-subsystem
// level applies to it. Nested With names are dotted.
func TestShim_SubsystemIsAttribute(t *testing.T) {
	var buf bytes.Buffer
	lg := shimPipeline(t, &buf, map[string]slog.Level{"process.quiet": slog.LevelError})
	l := FromSlog(lg.Root())

	l.With("loud").Info("x %d", 1)
	l.With("quiet").Info("hidden %d", 2)
	l.With("quiet").Error("shown %s", "err")
	l.With("outer").With("inner").Warn("nested")
	l.Info("bare")

	records := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(records) != 4 {
		t.Fatalf("want 4 records (the info of subsystem quiet is below its level), got %d:\n%s", len(records), buf.String())
	}
	for i, want := range []struct{ msg, sub string }{
		{`msg="x 1"`, "subsystem=loud"},
		{`msg="shown err"`, "subsystem=quiet"},
		{`msg=nested`, "subsystem=outer.inner"},
		{`msg=bare`, ""},
	} {
		if !strings.Contains(records[i], want.msg) {
			t.Errorf("record %d = %q, want %s", i, records[i], want.msg)
		}
		if want.sub != "" && !strings.Contains(records[i], want.sub) {
			t.Errorf("record %d = %q, want the attribute %s", i, records[i], want.sub)
		}
		if strings.Contains(records[i], "loud:") || strings.Contains(records[i], "quiet:") {
			t.Errorf("record %d = %q: the subsystem is baked into the message", i, records[i])
		}
	}
	if strings.Contains(records[3], "subsystem=") {
		t.Errorf("a logger without a subsystem wrote one: %q", records[3])
	}
}

// TestShim_DefaultIsResolvedAtCallTime: a logger built before the App's
// pipeline becomes the default (package variables are) writes to the
// pipeline once it is, and follows a later default.
func TestShim_DefaultIsResolvedAtCallTime(t *testing.T) {
	prev := slog.Default()
	t.Cleanup(func() { slog.SetDefault(prev) })

	early := New("early")
	var a, b bytes.Buffer
	slog.SetDefault(shimPipeline(t, &a, nil).Root())
	early.Info("first %s", "pipeline")
	slog.SetDefault(shimPipeline(t, &b, nil).Root())
	early.Info("second %s", "pipeline")

	if !strings.Contains(a.String(), `msg="first pipeline"`) || !strings.Contains(a.String(), "subsystem=early") {
		t.Errorf("first default got %q", a.String())
	}
	if strings.Contains(a.String(), "second") || !strings.Contains(b.String(), `msg="second pipeline"`) {
		t.Errorf("the logger did not follow the new default: %q / %q", a.String(), b.String())
	}
}

// TestShim_SanitisesTheMessage: the formatted message keeps its sanitising
// barrier whatever the handler.
func TestShim_SanitisesTheMessage(t *testing.T) {
	var buf bytes.Buffer
	l := FromSlog(slog.New(slog.NewTextHandler(&buf, nil)))
	l.Info("riddle\nlevel=ERROR msg=%s", "forged\r")
	if got := strings.Count(buf.String(), "\n"); got != 1 {
		t.Errorf("want one line, got %d: %q", got, buf.String())
	}
}

// TestShim_TraceReachesATraceGate: with the minimum lowered to trace, a
// trace call is a pipeline record at core's trace level.
func TestShim_TraceReachesATraceGate(t *testing.T) {
	var buf bytes.Buffer
	lg := shimPipeline(t, &buf, map[string]slog.Level{"process.deep": corelog.LevelTrace})
	l := &StandardLogger{minStdout: LevelTrace, base: lg.Root()}
	l.With("deep").Trace("down %d", 3)
	l.With("shallow").Trace("up")
	if !strings.Contains(buf.String(), `msg="down 3"`) || strings.Contains(buf.String(), "up") {
		t.Errorf("trace routing wrong: %q", buf.String())
	}
}
