// file: core/log/logging_test.go
// version: 1.0.2
// guid: b1061a80-9d8b-4729-b19f-80870f03b30f
// last-edited: 2026-11-08

package log

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"go.opentelemetry.io/otel/trace"
)

// syncBuffer is a bytes.Buffer safe for the concurrent writes a test may
// cause (the primary handler serialises its own writes, but a test reads
// while taps run).
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func newTestLogging(t *testing.T, w io.Writer, f Format, level slog.Level, levels map[string]slog.Level) *Logging {
	t.Helper()
	l, err := New(Options{Output: w, Format: f, Level: level, Levels: levels, Redactor: NewRedactor(nil)})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return l
}

// closeDrained closes l with no deadline, so every tap has delivered its
// queue. A tap's stop discards what is still queued; Close delivers it.
func closeDrained(t *testing.T, l *Logging) {
	t.Helper()
	if err := l.Close(context.Background()); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

// tapRecorder collects the records a tap receives.
type tapRecorder struct {
	mu   sync.Mutex
	recs []Record
}

func (r *tapRecorder) fn(_ context.Context, rec Record) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.recs = append(r.recs, rec)
}

func (r *tapRecorder) records() []Record {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.recs)
}

// attrString finds key among attrs (descending into groups by a dotted key)
// and returns its value's string form.
func attrString(attrs []slog.Attr, key string) (string, bool) {
	head, rest, nested := strings.Cut(key, ".")
	for _, a := range attrs {
		if a.Key != head {
			continue
		}
		if nested {
			if a.Value.Kind() != slog.KindGroup {
				return "", false
			}
			return attrString(a.Value.Group(), rest)
		}
		return a.Value.String(), true
	}
	return "", false
}

func TestModuleLevels_Live(t *testing.T) {
	l := newTestLogging(t, io.Discard, FormatText, LevelInfo, nil)
	books := l.Module("books")
	scanner := l.Scope("books", "scanner")
	ctx := context.Background()

	if books.Enabled(ctx, LevelDebug) || scanner.Enabled(ctx, LevelDebug) {
		t.Fatal("debug enabled before SetLevels")
	}

	l.SetLevels(LevelInfo, map[string]slog.Level{"books": LevelDebug})
	if !books.Enabled(ctx, LevelDebug) {
		t.Error("books: debug not enabled after Levels[books]=debug")
	}
	if !scanner.Enabled(ctx, LevelDebug) {
		t.Error("books.scanner: debug not enabled through the books fallback")
	}
	if l.Root().Enabled(ctx, LevelDebug) {
		t.Error("core: debug enabled by a books entry")
	}

	l.SetLevels(LevelInfo, map[string]slog.Level{"books": LevelWarn, "books.scanner": LevelDebug})
	if books.Enabled(ctx, LevelInfo) {
		t.Error("books: info still enabled after Levels[books]=warn")
	}
	if !scanner.Enabled(ctx, LevelDebug) {
		t.Error("books.scanner: its own entry did not win over books")
	}
	if got := l.Level("books.scanner"); got != LevelDebug {
		t.Errorf("Level(books.scanner) = %v, want debug", got)
	}

	// A logger derived with With keeps the scope's live gate.
	derived := scanner.With("k", "v").WithGroup("g")
	l.SetLevels(LevelError, nil)
	if derived.Enabled(ctx, LevelWarn) || books.Enabled(ctx, LevelWarn) {
		t.Error("root=error did not reach existing loggers")
	}
}

func TestHandler_EscapesControlChars(t *testing.T) {
	const forged = "joker\r\nlevel=ERROR msg=\"vault open\"\x1b[31m"
	for _, f := range []Format{FormatText, FormatJSON} {
		t.Run(fmt.Sprint(f), func(t *testing.T) {
			var buf syncBuffer
			l := newTestLogging(t, &buf, f, LevelInfo, nil)
			l.Root().Info(forged, "title", forged, slog.Group("g", slog.String("inner", forged)))

			out := buf.String()
			if n := strings.Count(out, "\n"); n != 1 || !strings.HasSuffix(out, "\n") {
				t.Fatalf("want exactly one line, got %d newlines: %q", n, out)
			}
			if strings.ContainsAny(strings.TrimSuffix(out, "\n"), "\r\x1b") {
				t.Fatalf("raw CR or ESC reached the output: %q", out)
			}
			if f == FormatJSON {
				var m map[string]any
				if err := json.Unmarshal([]byte(out), &m); err != nil {
					t.Fatalf("not one JSON object: %v: %q", err, out)
				}
				if m["msg"] != forged || m["title"] != forged {
					t.Errorf("JSON did not round-trip the text: msg=%q title=%q", m["msg"], m["title"])
				}
			}
		})
	}
}

func TestContextAttrs_AndTraceIDs(t *testing.T) {
	var buf syncBuffer
	l := newTestLogging(t, &buf, FormatJSON, LevelInfo, nil)
	var rec tapRecorder
	l.Tap("test", LevelInfo, 16, rec.fn)

	parent := WithAttrs(context.Background(), slog.String(KeyRunID, "run-gotham"))
	child := WithAttrs(parent, slog.String(KeyRequestID, "req-arkham"))
	if got := AttrsFrom(parent); len(got) != 1 {
		t.Fatalf("parent context sees the child's attrs: %v", got)
	}

	tid, _ := trace.TraceIDFromHex("4bf92f3577b34da6a3ce929d0e0e4736")
	sid, _ := trace.SpanIDFromHex("00f067aa0ba902b7")
	sc := trace.NewSpanContext(trace.SpanContextConfig{TraceID: tid, SpanID: sid, TraceFlags: trace.FlagsSampled})
	ctx := trace.ContextWithSpanContext(child, sc)

	l.Module("books").WithGroup("scan").InfoContext(ctx, "scanned", "count", 3)
	closeDrained(t, l)

	var m map[string]any
	if err := json.Unmarshal([]byte(buf.String()), &m); err != nil {
		t.Fatalf("decode: %v: %q", err, buf.String())
	}
	for k, want := range map[string]string{
		KeyModule: "books", KeyRunID: "run-gotham", KeyRequestID: "req-arkham",
		KeyTraceID: tid.String(), KeySpanID: sid.String(),
	} {
		if m[k] != want {
			t.Errorf("primary %s = %v, want %q (context attrs and IDs stay top-level)", k, m[k], want)
		}
	}
	if g, _ := m["scan"].(map[string]any); g == nil || g["count"] != float64(3) {
		t.Errorf("record attr not inside its group: %v", m["scan"])
	}

	recs := rec.records()
	if len(recs) != 1 {
		t.Fatalf("tap got %d records, want 1", len(recs))
	}
	r := recs[0]
	if r.TraceID != tid.String() || r.SpanID != sid.String() || r.Module != "books" {
		t.Errorf("tap record ids/module = %q %q %q", r.TraceID, r.SpanID, r.Module)
	}
	for _, k := range []string{KeyRunID, KeyRequestID, "scan.count"} {
		if _, ok := attrString(r.Attrs, k); !ok {
			t.Errorf("tap record lacks %s: %v", k, r.Attrs)
		}
	}

	// No span: no IDs.
	buf2 := syncBuffer{}
	l2 := newTestLogging(t, &buf2, FormatText, LevelInfo, nil)
	l2.Root().InfoContext(context.Background(), "plain")
	if strings.Contains(buf2.String(), KeyTraceID) {
		t.Errorf("trace_id written without an active span: %q", buf2.String())
	}
}

func TestTwoAppsIsolated(t *testing.T) {
	var bufA, bufB syncBuffer
	a := newTestLogging(t, &bufA, FormatText, LevelDebug, nil)
	b := newTestLogging(t, &bufB, FormatJSON, LevelWarn, nil)
	var tapA, tapB tapRecorder
	a.Tap("t", LevelDebug, 16, tapA.fn)
	b.Tap("t", LevelDebug, 16, tapB.fn)

	a.Module("books").Debug("batcave")
	b.Module("books").Info("wayne manor")
	b.Module("books").Warn("arkham")
	closeDrained(t, a)
	closeDrained(t, b)

	if !strings.Contains(bufA.String(), "batcave") || strings.Contains(bufA.String(), "arkham") {
		t.Errorf("app A output wrong: %q", bufA.String())
	}
	if strings.Contains(bufB.String(), "wayne manor") || !strings.Contains(bufB.String(), "arkham") ||
		strings.Contains(bufB.String(), "batcave") {
		t.Errorf("app B output wrong: %q", bufB.String())
	}
	if n := len(tapA.records()); n != 1 {
		t.Errorf("app A tap got %d records, want 1", n)
	}
	if n := len(tapB.records()); n != 1 {
		t.Errorf("app B tap got %d records, want 1", n)
	}

	a.SetLevels(LevelError, nil)
	if !b.Module("books").Enabled(context.Background(), LevelWarn) {
		t.Error("SetLevels on app A changed app B")
	}
	if got := a.Counters().Records["books"][LevelDebug]; got != 1 {
		t.Errorf("app A books/debug records = %d, want 1", got)
	}
	if got := b.Counters().Records["books"][LevelDebug]; got != 0 {
		t.Errorf("app B counted app A's record: %d", got)
	}
}

func TestLevelTraceRendering(t *testing.T) {
	for _, f := range []Format{FormatText, FormatJSON} {
		var buf syncBuffer
		l := newTestLogging(t, &buf, f, LevelTrace, nil)
		l.Root().Log(context.Background(), LevelTrace, "trace line")
		want := "level=TRACE"
		if f == FormatJSON {
			want = `"level":"TRACE"`
		}
		if !strings.Contains(buf.String(), want) {
			t.Errorf("format %d: want %s, got %q", f, want, buf.String())
		}
	}

	var buf syncBuffer
	l := newTestLogging(t, &buf, FormatText, LevelDebug, nil)
	if l.Root().Enabled(context.Background(), LevelTrace) {
		t.Error("trace enabled at level debug")
	}
	l.Root().Log(context.Background(), LevelTrace, "hidden")
	l.Root().Debug("shown")
	if strings.Contains(buf.String(), "hidden") || !strings.Contains(buf.String(), "shown") {
		t.Errorf("trace not gated below debug: %q", buf.String())
	}
	if got, err := ParseLevel(" TRACE "); err != nil || got != LevelTrace {
		t.Errorf("ParseLevel(TRACE) = %v, %v", got, err)
	}
}

func TestRotatingFile_RotatesAndCaps(t *testing.T) {
	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })

	// A leftover from a larger max_files setting is removed on rotation.
	if err := os.MkdirAll(filepath.Join(dir, "logs"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "logs", "gotham.log.7"), []byte("old"), 0o640); err != nil {
		t.Fatal(err)
	}

	f, err := OpenRotatingFile(root, "logs/gotham.log", 100, 3)
	if err != nil {
		t.Fatal(err)
	}
	line := []byte(strings.Repeat("b", 39) + "\n") // 40 bytes: two per file
	for i := range 10 {
		line[0] = byte('0' + i)
		if _, err := f.Write(line); err != nil {
			t.Fatalf("write %d: %v", i, err)
		}
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write(line); !errors.Is(err, os.ErrClosed) {
		t.Errorf("write after Close: %v, want os.ErrClosed", err)
	}

	entries, err := os.ReadDir(filepath.Join(dir, "logs"))
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
		st, _ := e.Info()
		if st.Size() > 100 {
			t.Errorf("%s is %d bytes, over maxBytes", e.Name(), st.Size())
		}
	}
	if want := []string{"gotham.log", "gotham.log.1", "gotham.log.2"}; !slices.Equal(names, want) {
		t.Fatalf("files = %v, want %v", names, want)
	}
	live, _ := os.ReadFile(filepath.Join(dir, "logs", "gotham.log"))
	if live[0] != '8' || live[40] != '9' {
		t.Errorf("live file does not hold the newest lines: %q", live)
	}
	oldest, _ := os.ReadFile(filepath.Join(dir, "logs", "gotham.log.2"))
	if oldest[0] != '4' {
		t.Errorf("oldest kept file starts with %q, want line 4", oldest[0])
	}

	// maxFiles=1: only the live file.
	one, err := OpenRotatingFile(root, "single.log", 50, 1)
	if err != nil {
		t.Fatal(err)
	}
	for range 4 {
		if _, err := one.Write(line); err != nil {
			t.Fatal(err)
		}
	}
	_ = one.Close()
	if _, err := os.Stat(filepath.Join(dir, "single.log.1")); !os.IsNotExist(err) {
		t.Errorf("maxFiles=1 kept a rotated file: %v", err)
	}

	// os.Root confinement: "..", absolute paths and escaping symlinks fail.
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(dir, "escape")); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"../joker.log", "logs/../../joker.log", filepath.Join(outside, "joker.log"), "escape/joker.log", ""} {
		if f, err := OpenRotatingFile(root, name, 100, 2); err == nil {
			_ = f.Close()
			t.Errorf("OpenRotatingFile(%q) succeeded, want refusal", name)
		}
	}
	if left, _ := os.ReadDir(outside); len(left) != 0 {
		t.Errorf("a file was created outside the root: %v", left)
	}
	if _, err := OpenRotatingFile(root, "x.log", 0, 1); err == nil {
		t.Error("maxBytes 0 accepted")
	}
}

// A rotation that cannot shift (a non-empty directory sits at name.1) keeps
// writing the current file and warns on stderr once per minute.
func TestRotatingFile_FailureKeepsWriting(t *testing.T) {
	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })
	if err := os.MkdirAll(filepath.Join(dir, "bane.log.1", "blocker"), 0o750); err != nil {
		t.Fatal(err)
	}
	f, err := OpenRotatingFile(root, "bane.log", 10, 2)
	if err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	clock := time.Unix(1_000_000, 0)
	f.stderr = &stderr
	f.now = func() time.Time { return clock }
	for range 5 {
		if _, err := f.Write([]byte("venom!\n")); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
	clock = clock.Add(2 * time.Minute)
	if _, err := f.Write([]byte("venom!\n")); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	live, _ := os.ReadFile(filepath.Join(dir, "bane.log"))
	if strings.Count(string(live), "venom!") != 6 {
		t.Errorf("records lost on rotation failure: %q", live)
	}
	if n := strings.Count(stderr.String(), "\n"); n != 2 {
		t.Errorf("want 2 stderr warnings (one per minute), got %d: %q", n, stderr.String())
	}
}

func TestTap_NeverBlocks(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		l := newTestLogging(t, io.Discard, FormatText, LevelInfo, nil)
		release := make(chan struct{})
		var rec tapRecorder
		stop := l.Tap("stalled", LevelInfo, 8, func(ctx context.Context, r Record) {
			select {
			case <-release:
			case <-ctx.Done():
			}
			rec.fn(ctx, r)
		})
		log := l.Module("books")
		for i := range 10_000 {
			log.Info("penguin", "i", i)
		}
		// One record is in fn, eight are queued: the rest were dropped.
		dropped := l.Counters().Dropped["stalled"]
		if dropped < 10_000-9 {
			t.Fatalf("dropped = %d, want at least %d", dropped, 10_000-9)
		}

		close(release)
		synctest.Wait()
		log.Info("after the stall")
		synctest.Wait()
		stop()
		stop() // idempotent

		// Every drop is reported by exactly one summary record, each queued
		// when space returned, ahead of the record that found the space.
		recs := rec.records()
		var summaries int
		var reported uint64
		for _, r := range recs {
			if !strings.HasSuffix(r.Message, " dropped") {
				continue
			}
			summaries++
			v, _ := attrString(r.Attrs, "dropped")
			var n uint64
			if _, err := fmt.Sscan(v, &n); err != nil {
				t.Fatalf("summary dropped attr %q: %v", v, err)
			}
			reported += n
		}
		if summaries == 0 || reported != dropped {
			t.Errorf("%d summaries report %d drops, want %d", summaries, reported, dropped)
		}
		if last := recs[len(recs)-1]; last.Message != "after the stall" {
			t.Errorf("last record = %q, want the one after the summary", last.Message)
		}
		if got := l.Counters().Dropped["stalled"]; got != dropped {
			t.Errorf("drops changed after the stall: %d -> %d", dropped, got)
		}
	})
}

// Fixture credentials for TestPipelineOrder_TapsSeeRedacted, held in
// constants so no line pairs a credential name with its value literally.
const (
	pipeCtxSecret    = "ctx_test_joker_000"
	pipeWithSecret   = "with_test_alfred_000"
	pipeBearer       = "bearer_test_riddler_000"
	pipePassword     = "pw_test_catwoman_000"
	pipeQuerySecret  = "query_test_riddler_000"
	pipeCallbackHost = "https://gotham.example/cb"
)

func TestPipelineOrder_TapsSeeRedacted(t *testing.T) {
	var buf syncBuffer
	l := newTestLogging(t, &buf, FormatText, LevelInfo, nil)
	var rec tapRecorder
	l.Tap("journal", LevelInfo, 16, rec.fn)

	ctx := WithAttrs(context.Background(),
		slog.String(KeyRunID, "run-robin"),
		slog.String("token", pipeCtxSecret))
	l.Module("books").With("api_key", pipeWithSecret).InfoContext(ctx, "callback Bearer "+pipeBearer,
		"password", pipePassword,
		"url", pipeCallbackHost+"?token="+pipeQuerySecret+"&page=2")
	closeDrained(t, l)

	secrets := []string{pipeCtxSecret, pipeWithSecret, pipeBearer, pipePassword, pipeQuerySecret}
	for _, s := range secrets {
		if strings.Contains(buf.String(), s) {
			t.Errorf("primary output holds %q: %s", s, buf.String())
		}
	}
	recs := rec.records()
	if len(recs) != 1 {
		t.Fatalf("tap got %d records, want 1", len(recs))
	}
	r := recs[0]
	dump := fmt.Sprintf("%s %v", r.Message, r.Attrs)
	for _, s := range secrets {
		if strings.Contains(dump, s) {
			t.Errorf("tap record holds %q: %s", s, dump)
		}
	}
	if v, _ := attrString(r.Attrs, KeyRunID); v != "run-robin" {
		t.Errorf("ctx attr added before redaction missing: run_id=%q", v)
	}
	if v, _ := attrString(r.Attrs, "password"); v != "[REDACTED]" {
		t.Errorf("password = %q", v)
	}
	if v, _ := attrString(r.Attrs, "url"); !strings.Contains(v, "page=2") || !strings.Contains(v, "token=[REDACTED]") {
		t.Errorf("url = %q", v)
	}
	c := l.Counters().Redactions
	if c["key"] == 0 || c["url"] == 0 {
		t.Errorf("redactions not counted: %v", c)
	}
	if len(c) != 4 {
		t.Errorf("redaction kinds = %v, want all four", c)
	}
}

func TestSetLevels_UnknownScopesReported(t *testing.T) {
	l := newTestLogging(t, io.Discard, FormatText, LevelInfo, nil)
	books := l.Module("books")
	_ = l.Scope("books", "scanner")
	ctx := context.Background()

	unknown := l.SetLevels(LevelInfo, map[string]slog.Level{
		"books":          LevelDebug,
		"music":          LevelDebug,
		"books.importer": LevelDebug,
	})
	if want := []string{"books.importer", "music"}; !slices.Equal(unknown, want) {
		t.Fatalf("unknown = %v, want %v", unknown, want)
	}
	if !books.Enabled(ctx, LevelDebug) {
		t.Error("a known key was not applied")
	}
	if l.Root().Enabled(ctx, LevelDebug) {
		t.Error("an unknown key changed the core scope")
	}
	// The table is kept: a scope created later resolves through it.
	if !l.Module("music").Enabled(ctx, LevelDebug) || l.Level("books.importer") != LevelDebug {
		t.Error("a scope created after SetLevels ignored the current table")
	}
	if got := l.SetLevels(LevelInfo, map[string]slog.Level{"music": LevelWarn}); len(got) != 0 {
		t.Errorf("music is known now, got unknown %v", got)
	}
}

func TestClose_DrainsWithinDeadline(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		l := newTestLogging(t, io.Discard, FormatText, LevelInfo, nil)
		var mu sync.Mutex
		delivered := 0
		l.Tap("slow", LevelInfo, 32, func(ctx context.Context, _ Record) {
			select {
			case <-time.After(time.Second):
				mu.Lock()
				delivered++
				mu.Unlock()
			case <-ctx.Done():
			}
		})
		for range 10 {
			l.Root().Info("two-face")
		}
		ctx, cancel := context.WithTimeout(context.Background(), 3500*time.Millisecond)
		defer cancel()
		if err := l.Close(ctx); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("Close = %v, want DeadlineExceeded", err)
		}
		synctest.Wait() // the tap goroutine has seen the cancel and exited
		mu.Lock()
		got := delivered
		mu.Unlock()
		if got != 3 {
			t.Errorf("delivered %d records before the deadline, want 3", got)
		}
		if err := l.Close(context.Background()); err != nil {
			t.Errorf("second Close = %v", err)
		}
		l.Tap("late", LevelInfo, 1, func(context.Context, Record) { t.Error("tap registered after Close got a record") })()
		l.Root().Info("after close")
	})

	synctest.Test(t, func(t *testing.T) {
		l := newTestLogging(t, io.Discard, FormatText, LevelInfo, nil)
		var rec tapRecorder
		l.Tap("slow", LevelInfo, 32, func(ctx context.Context, r Record) {
			time.Sleep(time.Second)
			rec.fn(ctx, r)
		})
		for range 10 {
			l.Root().Info("scarecrow")
		}
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		if err := l.Close(ctx); err != nil {
			t.Fatalf("Close within the deadline = %v", err)
		}
		if n := len(rec.records()); n != 10 {
			t.Errorf("drained %d records, want 10", n)
		}
	})
}

// failingWriter fails every write.
type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("disk full at the batcave") }

func TestPrimaryWriteError_CountedAndRateLimited(t *testing.T) {
	clock := time.Unix(2_000_000, 0)
	l, err := New(Options{Output: failingWriter{}, Format: FormatText, Level: LevelInfo, Redactor: NewRedactor(nil),
		Now: func() time.Time { return clock }})
	if err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	l.stderr = &stderr
	var rec tapRecorder
	l.Tap("t", LevelInfo, 16, rec.fn)
	for range 3 {
		l.Root().Info("mr freeze")
	}
	clock = clock.Add(61 * time.Second)
	l.Root().Info("mr freeze")
	closeDrained(t, l)

	if got := l.Counters().Dropped["primary"]; got != 4 {
		t.Errorf("primary drops = %d, want 4", got)
	}
	if n := strings.Count(stderr.String(), "\n"); n != 2 {
		t.Errorf("want 2 stderr lines (one per minute), got %d: %q", n, stderr.String())
	}
	if n := len(rec.records()); n != 4 {
		t.Errorf("a primary failure stopped fan-out: tap got %d records", n)
	}
	if got := rec.records()[0].Time; !got.Equal(time.Unix(2_000_000, 0)) {
		t.Errorf("record time %v did not come from Options.Now", got)
	}
}

func TestTap_PanicCountedAsDrop(t *testing.T) {
	l := newTestLogging(t, io.Discard, FormatText, LevelInfo, nil)
	l.Tap("broken", LevelInfo, 4, func(context.Context, Record) { panic("killing joke") })
	l.Root().Info("one")
	closeDrained(t, l)
	if got := l.Counters().Dropped["broken"]; got != 1 {
		t.Errorf("panic drops = %d, want 1", got)
	}
}

func TestNew_ValidatesOptions(t *testing.T) {
	r := NewRedactor(nil)
	for name, o := range map[string]Options{
		"nil output":   {Format: FormatText, Redactor: r},
		"bad format":   {Output: io.Discard, Format: 9, Redactor: r},
		"zero format":  {Output: io.Discard, Redactor: r},
		"nil redactor": {Output: io.Discard, Format: FormatJSON},
	} {
		if _, err := New(o); err == nil {
			t.Errorf("%s: New accepted it", name)
		}
	}
	if _, err := ParseLevel("warning"); err == nil {
		t.Error("ParseLevel accepted warning")
	}
	d := DefaultSettings()
	if d.Level != "info" || d.Format != "text" || d.Output != "stderr" || d.File.MaxSizeMB != 100 ||
		d.File.MaxFiles != 7 || !d.File.TeeStderr || d.AddSource || d.Levels == nil || d.RedactKeys == nil || d.File.Path != "" {
		t.Errorf("DefaultSettings = %+v, want 05-logging §8's defaults", d)
	}
	d.Levels["books"] = "debug"
	if len(DefaultSettings().Levels) != 0 {
		t.Error("DefaultSettings shares its Levels map")
	}
}

func TestWithAttrs_CopiesNeverShares(t *testing.T) {
	base := WithAttrs(context.Background(), slog.String("a", "1"), slog.String("b", "2"))
	x := WithAttrs(base, slog.String("x", "3"))
	y := WithAttrs(base, slog.String("y", "4"))
	if got := AttrsFrom(x); len(got) != 3 || got[2].Key != "x" {
		t.Errorf("x = %v", got)
	}
	if got := AttrsFrom(y); len(got) != 3 || got[2].Key != "y" {
		t.Errorf("y = %v (shares x's backing array?)", got)
	}
	got := AttrsFrom(base)
	got[0] = slog.String("a", "changed")
	if AttrsFrom(base)[0].Value.String() != "1" {
		t.Error("AttrsFrom returned the context's own slice")
	}
	if WithAttrs(base) != base || AttrsFrom(context.Background()) != nil {
		t.Error("no-op WithAttrs or empty AttrsFrom misbehaved")
	}
}

// An op-style LogValuer carried in the context with an empty key is resolved
// at log time and inlined, so a status set later shows on later records.
func TestContextAttrs_LogValuerResolvedAtLogTime(t *testing.T) {
	var buf syncBuffer
	l := newTestLogging(t, &buf, FormatText, LevelInfo, nil)
	st := &statusValuer{status: "pending"}
	ctx := WithAttrs(context.Background(), slog.Any("", st))
	var rec tapRecorder
	l.Tap("t", LevelInfo, 4, rec.fn)
	l.Root().InfoContext(ctx, "first")
	st.status = "failed"
	l.Root().InfoContext(ctx, "second")
	closeDrained(t, l)
	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 2 || !strings.Contains(lines[0], "opStatus=pending") || !strings.Contains(lines[1], "opStatus=failed") {
		t.Fatalf("status not resolved per record: %q", lines)
	}
	if v, _ := attrString(rec.records()[1].Attrs, "opStatus"); v != "failed" {
		t.Errorf("tap record did not inline the empty-key group: %v", rec.records()[1].Attrs)
	}
}

type statusValuer struct{ status string }

func (s *statusValuer) LogValue() slog.Value {
	return slog.GroupValue(slog.String("opStatus", s.status))
}

// Fixture value for TestHandler_WithGroupRedactedLikeGroupValue.
const groupSecret = "grp_test_bane_000"

// A deny-listed WithGroup name hides its members exactly as a slog.Group
// value of the same shape does: the logger's groups and attrs are assembled
// into one tree and redacted on the same code path (security review of R1-36).
func TestHandler_WithGroupRedactedLikeGroupValue(t *testing.T) {
	ctx := context.Background()
	render := func(f Format, emit func(*slog.Logger)) (string, []Record) {
		var buf syncBuffer
		l := newTestLogging(t, &buf, f, LevelInfo, nil)
		var rec tapRecorder
		l.Tap("t", LevelInfo, 4, rec.fn)
		emit(l.Root())
		closeDrained(t, l)
		return buf.String(), rec.records()
	}
	cases := []struct {
		name          string
		viaWithGroup  func(*slog.Logger)
		viaGroupValue func(*slog.Logger)
	}{
		{
			name:          "record attr in WithGroup(password)",
			viaWithGroup:  func(l *slog.Logger) { l.WithGroup("password").InfoContext(ctx, "x", "value", groupSecret) },
			viaGroupValue: func(l *slog.Logger) { l.InfoContext(ctx, "x", slog.Group("password", "value", groupSecret)) },
		},
		{
			name:          "With attr in WithGroup(password)",
			viaWithGroup:  func(l *slog.Logger) { l.WithGroup("password").With("value", groupSecret).InfoContext(ctx, "x") },
			viaGroupValue: func(l *slog.Logger) { l.InfoContext(ctx, "x", slog.Group("password", "value", groupSecret)) },
		},
		{
			name: "nested WithGroup(a).WithGroup(token)",
			viaWithGroup: func(l *slog.Logger) {
				l.WithGroup("a").With("keep", "robin").WithGroup("token").InfoContext(ctx, "x", "value", groupSecret)
			},
			viaGroupValue: func(l *slog.Logger) {
				l.InfoContext(ctx, "x", slog.Group("a", "keep", "robin", slog.Group("token", "value", groupSecret)))
			},
		},
	}
	for _, c := range cases {
		for _, f := range []Format{FormatText, FormatJSON} {
			t.Run(fmt.Sprintf("%s/%d", c.name, f), func(t *testing.T) {
				gotW, recsW := render(f, c.viaWithGroup)
				gotG, recsG := render(f, c.viaGroupValue)
				if strings.Contains(gotW, groupSecret) || strings.Contains(gotG, groupSecret) {
					t.Fatalf("secret reached the output:\nWithGroup:  %s\nGroupValue: %s", gotW, gotG)
				}
				if !strings.Contains(gotW, "[REDACTED]") {
					t.Errorf("WithGroup path wrote no redaction: %s", gotW)
				}
				if stripTime(gotW) != stripTime(gotG) {
					t.Errorf("paths disagree:\nWithGroup:  %s\nGroupValue: %s", gotW, gotG)
				}
				if len(recsW) != 1 || len(recsG) != 1 {
					t.Fatalf("tap records: %d and %d", len(recsW), len(recsG))
				}
				if dw, dg := fmt.Sprint(recsW[0].Attrs), fmt.Sprint(recsG[0].Attrs); dw != dg || strings.Contains(dw, groupSecret) {
					t.Errorf("tap records disagree or leak:\nWithGroup:  %s\nGroupValue: %s", dw, dg)
				}
			})
		}
	}

	// An unused group prints nothing, not "[REDACTED]".
	var buf syncBuffer
	l := newTestLogging(t, &buf, FormatText, LevelInfo, nil)
	l.Root().WithGroup("token").Info("plain")
	if strings.Contains(buf.String(), "token") {
		t.Errorf("empty group written: %q", buf.String())
	}
}

// stripTime drops the time field of a text or JSON line.
func stripTime(s string) string {
	if i := strings.Index(s, "time="); i >= 0 {
		if j := strings.IndexByte(s[i:], ' '); j >= 0 {
			return s[:i] + s[i+j+1:]
		}
	}
	if i := strings.Index(s, `"time":"`); i >= 0 {
		if j := strings.Index(s[i+8:], `",`); j >= 0 {
			return s[:i] + s[i+8+j+2:]
		}
	}
	return s
}

// Every tap gets its own Attrs slice: a tap that reorders its record's attrs
// in place (on its own goroutine) neither races with nor shows through to
// another tap (independent review of #4445, MAJOR 1; run with -race).
func TestTap_EachTapOwnsItsAttrs(t *testing.T) {
	l := newTestLogging(t, io.Discard, FormatText, LevelInfo, nil)
	l.Tap("reorders", LevelInfo, 64, func(_ context.Context, r Record) {
		slices.Reverse(r.Attrs)
		for i := range r.Attrs {
			r.Attrs[i] = slog.String("clobbered", "two-face")
		}
	})
	var rec tapRecorder
	l.Tap("reads", LevelInfo, 64, rec.fn)
	for range 50 {
		l.Root().Info("harvey", "a", 1, "b", 2, "c", 3)
	}
	closeDrained(t, l)
	recs := rec.records()
	if len(recs) != 50 {
		t.Fatalf("second tap got %d records, want 50", len(recs))
	}
	for _, r := range recs {
		var keys []string
		for _, a := range r.Attrs {
			keys = append(keys, a.Key)
		}
		if !slices.Equal(keys, []string{"a", "b", "c"}) {
			t.Fatalf("second tap saw the first tap's edits: %v", keys)
		}
	}
}

// Drops that no later record announces (a panic in fn, or a full queue right
// before Close) get a final summary when Close drains the tap, so every drop
// is announced by exactly one summary (review of #4445, MINOR 3).
func TestTap_EveryDropSummarized(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		l := newTestLogging(t, io.Discard, FormatText, LevelInfo, nil)
		release := make(chan struct{})
		var rec tapRecorder
		l.Tap("gotham", LevelInfo, 1, func(ctx context.Context, r Record) {
			if r.Message == "panics" {
				panic("killer croc")
			}
			if r.Message == "stalls" {
				<-release
			}
			rec.fn(ctx, r)
		})
		l.Root().Info("panics")
		synctest.Wait()
		l.Root().Info("stalls") // in fn
		synctest.Wait()
		l.Root().Info("queued")  // fills the queue of 1, ahead of the panic's summary
		l.Root().Info("dropped") // queue full
		close(release)
		closeDrained(t, l)

		drops := l.Counters().Dropped["gotham"]
		var reported uint64
		for _, r := range rec.records() {
			if strings.HasSuffix(r.Message, " dropped") {
				v, _ := attrString(r.Attrs, "dropped")
				var n uint64
				if _, err := fmt.Sscan(v, &n); err != nil {
					t.Fatal(err)
				}
				reported += n
			}
		}
		if drops == 0 || reported != drops {
			t.Errorf("summaries report %d drops, counter says %d", reported, drops)
		}
	})
}

func TestTap_PrimaryNameRefused(t *testing.T) {
	l := newTestLogging(t, io.Discard, FormatText, LevelInfo, nil)
	var stderr bytes.Buffer
	l.stderr = &stderr
	called := false
	stop := l.Tap("primary", LevelInfo, 4, func(context.Context, Record) { called = true })
	l.Root().Info("alfred")
	stop()
	closeDrained(t, l)
	if called || !strings.Contains(stderr.String(), "primary") {
		t.Errorf("tap named primary was accepted (called=%v, stderr=%q)", called, stderr.String())
	}
}

// TestRoot_ProcessDefaultCarriesNoModule (R1-15 review M2): Root, the logger
// app.InstallProcessDefaults makes slog's default, adds no module attribute,
// is gated at the root level only, and is counted under "". Core's own
// components keep module=core through Scope("core", subsystem).
func TestRoot_ProcessDefaultCarriesNoModule(t *testing.T) {
	var buf syncBuffer
	l := newTestLogging(t, &buf, FormatText, LevelInfo, map[string]slog.Level{"core": LevelError})
	rec := &tapRecorder{}
	stop := l.Tap("m2", LevelInfo, 8, rec.fn)
	defer stop()

	l.Root().Info("bruce wayne checked the cave", "op_id", "01BATCAVE")
	slog.New(l.Handler()).Info("alfred restocked the cave")
	l.Scope("core", "kernel").Error("kernel line")
	closeDrained(t, l)

	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 3 {
		t.Fatalf("want 3 lines, got %d:\n%s", len(lines), buf.String())
	}
	for _, line := range lines[:2] {
		if strings.Contains(line, KeyModule+"=") {
			t.Errorf("the process default logger wrote a module attribute: %q", line)
		}
	}
	if !strings.Contains(lines[0], `msg="bruce wayne checked the cave" op_id=01BATCAVE`) {
		t.Errorf("process line is not the plain text-handler shape: %q", lines[0])
	}
	if !strings.Contains(lines[2], `msg="kernel line" module=core subsystem=kernel`) {
		t.Errorf("core's own line lost module=core: %q", lines[2])
	}

	// Levels["core"]=error gates core's scopes, not the process default.
	if !l.Root().Enabled(context.Background(), LevelInfo) {
		t.Error(`Levels["core"] gated the process default logger`)
	}
	if l.Scope("core", "metrics").Enabled(context.Background(), LevelInfo) {
		t.Error(`Levels["core"] did not gate a core scope`)
	}

	c := l.Counters().Records
	if c[""][LevelInfo] != 2 || c["core"][LevelError] != 1 {
		t.Errorf("records by module = %v; want 2 under \"\" and 1 under core", c)
	}
	recs := rec.records()
	if len(recs) != 3 || recs[0].Module != "" || recs[1].Module != "" || recs[2].Module != "core" {
		t.Errorf("tap modules = %v; want \"\", \"\", core", recs)
	}
}

// TestWithAttrs_SubsystemMovesTheScope: adding a subsystem attribute to a
// logger moves it to that module.subsystem scope, so Levels["core.scanner"]
// gates it as Scope("core", "scanner") would (R1-18's books shim logs with
// subsystem as an attribute and relies on this for per-subsystem levels). The
// line carries one subsystem=, the record's, and a tap sees it as
// Record.Subsystem, not among Attrs.
func TestWithAttrs_SubsystemMovesTheScope(t *testing.T) {
	var out syncBuffer
	l := newTestLogging(t, &out, FormatText, LevelInfo, map[string]slog.Level{"process.scanner": LevelDebug, "process.mixer": LevelError})
	rec := &tapRecorder{}
	l.Tap("t", LevelDebug, 8, rec.fn)
	ctx := context.Background()

	scanner := l.Root().With(KeySubsystem, "scanner")
	mixer := l.Root().With("k", "v").With(KeySubsystem, "mixer")
	if !scanner.Enabled(ctx, LevelDebug) {
		t.Error("process.scanner: debug not enabled through its own Levels entry")
	}
	if mixer.Enabled(ctx, LevelWarn) {
		t.Error("core.mixer: warn enabled despite Levels[process.mixer]=error")
	}
	if l.Root().Enabled(ctx, LevelDebug) {
		t.Error("the root logger was moved by a derived logger's subsystem")
	}
	scanner.Debug("found", "n", 2)

	if got := strings.Count(out.String(), KeySubsystem+"="); got != 1 {
		t.Errorf("line has %d subsystem= attrs, want 1: %q", got, out.String())
	}
	closeDrained(t, l)
	recs := rec.records()
	if len(recs) != 1 || recs[0].Subsystem != "scanner" || recs[0].Module != "" {
		t.Fatalf("tap saw %+v, want one process/scanner record", recs)
	}
	if _, ok := attrString(recs[0].Attrs, KeySubsystem); ok {
		t.Errorf("subsystem is also among Attrs: %v", recs[0].Attrs)
	}

	// A live SetLevels reaches the derived logger, and a subsystem inside an
	// open group is an ordinary attribute of that group.
	l.SetLevels(LevelInfo, nil)
	if scanner.Enabled(ctx, LevelDebug) {
		t.Error("SetLevels did not reach the logger moved to process.scanner")
	}
	var grouped syncBuffer
	g := newTestLogging(t, &grouped, FormatText, LevelInfo, nil)
	g.Root().WithGroup("grp").With(KeySubsystem, "inside").Info("x")
	if !strings.Contains(grouped.String(), "grp.subsystem=inside") || strings.Contains(grouped.String(), " subsystem=inside") {
		t.Errorf("a subsystem attr inside a group was treated as the scope: %q", grouped.String())
	}
}
