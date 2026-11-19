// file: core/log/otheruser_test.go
// version: 1.0.0
// guid: 3d8a5c17-b2e4-4f60-9a71-c5e08b4d2f93
// last-edited: 2026-11-19

package log_test

import (
	"log/slog"
	"slices"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/log"
)

// TestPredicateInput pins the contract OtherUserPredicate documents: text
// values, sorted keys, the last value of a repeated key, everything else
// untouched, and the caller's record unmodified.
func TestPredicateInput(t *testing.T) {
	rec := log.Record{
		Time: time.Date(2026, 11, 19, 1, 2, 3, 0, time.UTC), Level: slog.LevelWarn, Module: "core", Subsystem: "scanner",
		Message: "a batarang was thrown", TraceID: "t1", SpanID: "s1",
		Attrs: []slog.Attr{
			slog.String("zeta", "z"), slog.Int("status", 204), slog.Bool("hit", true), slog.Duration("flight", 1500*time.Millisecond),
			slog.String("dup", "first"), slog.Float64("arc", 0.25), slog.String("dup", "last"),
		},
	}
	orig := slices.Clone(rec.Attrs)
	got := log.PredicateInput(rec)

	var seq []string
	for _, a := range got.Attrs {
		if a.Value.Kind() != slog.KindString {
			t.Errorf("%s has kind %v, want String", a.Key, a.Value.Kind())
		}
		seq = append(seq, a.Key+"="+a.Value.String())
	}
	want := []string{"arc=0.25", "dup=last", "flight=1.5s", "hit=true", "status=204", "zeta=z"}
	if !slices.Equal(seq, want) {
		t.Errorf("attrs = %v, want %v", seq, want)
	}
	rest := got
	rest.Attrs = rec.Attrs
	if rest.Time != rec.Time || rest.Level != rec.Level || rest.Module != rec.Module || rest.Subsystem != rec.Subsystem ||
		rest.Message != rec.Message || rest.TraceID != rec.TraceID || rest.SpanID != rec.SpanID {
		t.Errorf("a non-attribute field changed: %+v", got)
	}
	if !slices.EqualFunc(rec.Attrs, orig, slog.Attr.Equal) {
		t.Error("PredicateInput modified the caller's attributes")
	}
	if n := len(log.PredicateInput(log.Record{}).Attrs); n != 0 {
		t.Errorf("an empty record gained %d attributes", n)
	}
}
