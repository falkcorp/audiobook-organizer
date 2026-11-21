// file: core/log/live_test.go
// version: 0.1.0
// guid: 5d0c7a2e-43b8-4f19-9a66-0e1d3c8b7f52
// last-edited: 2026-11-21

package log

import (
	"context"
	"io"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"testing"
)

// liveLogging is a text Logging over a buffer with its Redactor handed back,
// so a test can hold the second reference the kernel hands to metrics and HTTP.
func liveLogging(t *testing.T, s Settings) (*Logging, *Redactor, *syncBuffer) {
	t.Helper()
	root, per, err := s.parseLevels()
	if err != nil {
		t.Fatal(err)
	}
	r := NewRedactor(s.RedactKeys)
	buf := &syncBuffer{}
	l, err := New(Options{Output: buf, Format: FormatText, Level: root, Levels: per, AddSource: s.AddSource, Redactor: r})
	if err != nil {
		t.Fatal(err)
	}
	return l, r, buf
}

// TestRedactor_SetExtraKeys_ReachesEveryHolder: the Redactor the pipeline
// holds is the one the metrics Provider and the HTTP layer hold, so replacing
// its key table must change what they all redact, not only the log lines.
func TestRedactor_SetExtraKeys_ReachesEveryHolder(t *testing.T) {
	r := NewRedactor(nil)
	l, err := New(Options{Output: io.Discard, Format: FormatText, Level: LevelInfo, Redactor: r})
	if err != nil {
		t.Fatal(err)
	}
	other := l.Redactor() // what a second holder (the span redactor) was given
	if other != r {
		t.Fatal("Logging.Redactor() is not the Redactor New was given")
	}
	const key = "batcave_door_code"
	if got := other.String(key, "1939"); got != "1939" {
		t.Fatalf("before: %q redacted by a key nobody listed", key)
	}
	r.SetExtraKeys([]string{key})
	if got := other.String(key, "1939"); got == "1939" {
		t.Errorf("after SetExtraKeys the second holder still prints %q in clear", key)
	}
	if !other.KeyDenied(key, slog.KindString) {
		t.Error("KeyDenied does not see the new key")
	}
	r.SetExtraKeys(nil)
	if got := other.String(key, "1939"); got != "1939" {
		t.Errorf("after the key left the setting it is still redacted: %q", got)
	}
	if !other.KeyDenied("password", slog.KindString) {
		t.Error("the default deny-list was lost by SetExtraKeys(nil)")
	}
}

// TestRedactor_SetExtraKeys_RaceWithLogging swaps the key table while many
// goroutines log through it; under -race any unsynchronised table access
// fails here.
func TestRedactor_SetExtraKeys_RaceWithLogging(t *testing.T) {
	r := NewRedactor(nil)
	l, err := New(Options{Output: io.Discard, Format: FormatText, Level: LevelInfo, Redactor: r})
	if err != nil {
		t.Fatal(err)
	}
	log := l.Scope("core", "race")
	var wg sync.WaitGroup
	stop := make(chan struct{})
	for range 8 {
		wg.Go(func() {
			for {
				select {
				case <-stop:
					return
				default:
				}
				log.Info("alfred served tea", "batcave_door_code", "1939", "n", 1)
				_ = r.String("batcave_door_code", "1939")
			}
		})
	}
	for i := range 200 {
		if i%2 == 0 {
			r.SetExtraKeys([]string{"batcave_door_code"})
		} else {
			r.SetExtraKeys(nil)
		}
	}
	close(stop)
	wg.Wait()
}

// TestApplySettings_Live applies each live key to a running pipeline and
// checks each takes effect on loggers that already exist.
func TestApplySettings_Live(t *testing.T) {
	ctx := context.Background()
	base := DefaultSettings()
	l, r, buf := liveLogging(t, base)
	books := l.Scope("books", "scanner")

	// level
	if books.Enabled(ctx, LevelDebug) {
		t.Fatal("debug enabled by the defaults")
	}
	s := base
	s.Level = "debug"
	if _, err := l.ApplySettings(s); err != nil {
		t.Fatal(err)
	}
	if !books.Enabled(ctx, LevelDebug) {
		t.Error("level=debug did not reach an existing logger")
	}

	// levels (per scope), with an unknown scope reported and not an error
	s = base
	s.Levels = map[string]string{"books.scanner": "error", "nowhere": "debug"}
	unknown, err := l.ApplySettings(s)
	if err != nil {
		t.Fatal(err)
	}
	if books.Enabled(ctx, LevelWarn) {
		t.Error("levels[books.scanner]=error did not reach the existing logger")
	}
	if !slices.Equal(unknown, []string{"nowhere"}) {
		t.Errorf("unknown = %v, want [nowhere]", unknown)
	}

	// redact_keys
	s = base
	s.RedactKeys = []string{"batcave_door_code"}
	if _, err := l.ApplySettings(s); err != nil {
		t.Fatal(err)
	}
	l.Scope("core", "t").Info("door", "batcave_door_code", "1939")
	if strings.Contains(buf.String(), "1939") {
		t.Errorf("redact_keys did not apply live: %s", buf.String())
	}
	if r.String("batcave_door_code", "1939") == "1939" {
		t.Error("the shared Redactor did not change")
	}

	// add_source: off, then on, then off again, on the same pipeline.
	emit := func() string {
		buf.b.Reset()
		l.Scope("core", "t").Info("source probe")
		return buf.String()
	}
	if strings.Contains(emit(), "source=") {
		t.Error("source printed with add_source=false")
	}
	s = base
	s.AddSource = true
	if _, err := l.ApplySettings(s); err != nil {
		t.Fatal(err)
	}
	if out := emit(); !strings.Contains(out, "source=") || !strings.Contains(out, "live_test.go") {
		t.Errorf("add_source=true did not apply live: %s", out)
	}
	if _, err := l.ApplySettings(base); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(emit(), "source=") {
		t.Error("add_source=false did not apply live")
	}
}

// TestApplySettings_RefusedChangesNothing: a bad value is refused whole, so
// the pipeline keeps its previous levels, add_source and redaction.
func TestApplySettings_RefusedChangesNothing(t *testing.T) {
	ctx := context.Background()
	good := DefaultSettings()
	good.Level = "warn"
	good.Levels = map[string]string{"books": "debug"}
	good.RedactKeys = []string{"batcave_door_code"}
	l, r, _ := liveLogging(t, good)
	books := l.Module("books")

	bad := DefaultSettings()
	bad.Level = "error"
	bad.AddSource = true
	bad.RedactKeys = nil // would drop the redaction if half-applied
	bad.Levels = map[string]string{"books": "loud"}
	if _, err := l.ApplySettings(bad); err == nil {
		t.Fatal("a levels value that is not a level was accepted")
	}
	if l.Level("core") != LevelWarn || !books.Enabled(ctx, LevelDebug) {
		t.Error("levels changed although the settings were refused")
	}
	if l.addSource.Load() {
		t.Error("add_source changed although the settings were refused")
	}
	if r.String("batcave_door_code", "1939") == "1939" {
		t.Error("redact_keys changed although the settings were refused")
	}
}

// TestSettings_Validate lists what config must refuse.
func TestSettings_Validate(t *testing.T) {
	cases := map[string]struct {
		mut  func(*Settings)
		want string // "" = valid
	}{
		"defaults":           {func(*Settings) {}, ""},
		"bad level":          {func(s *Settings) { s.Level = "loud" }, "core.log.level"},
		"empty level":        {func(s *Settings) { s.Level = "" }, "core.log.level"},
		"bad scope level":    {func(s *Settings) { s.Levels = map[string]string{"books": "loud"} }, "core.log.levels.books"},
		"empty scope name":   {func(s *Settings) { s.Levels = map[string]string{" ": "debug"} }, "scope name is empty"},
		"separator-only key": {func(s *Settings) { s.RedactKeys = []string{"api_key", "-_."} }, "redact_keys[1]"},
		"good everything": {func(s *Settings) {
			s.Level = "TRACE"
			s.Levels = map[string]string{"books.scanner": "warn"}
			s.RedactKeys = []string{"batcave_door_code"}
		}, ""},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			s := DefaultSettings()
			tc.mut(&s)
			err := s.Validate()
			switch {
			case tc.want == "" && err != nil:
				t.Errorf("Validate = %v, want nil", err)
			case tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)):
				t.Errorf("Validate = %v, want one naming %q", err, tc.want)
			}
		})
	}
}
