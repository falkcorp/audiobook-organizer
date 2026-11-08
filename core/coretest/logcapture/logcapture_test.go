// file: core/coretest/logcapture/logcapture_test.go
// version: 1.1.0
// guid: 04b9335e-790b-47be-8262-702ff011f5ff
// last-edited: 2026-11-07

package logcapture

import (
	"log/slog"
	"slices"
	"sync"
	"testing"
)

func TestLogCapture_RecordsWithoutDefault(t *testing.T) {
	before := slog.Default()
	c := New(t)
	l := c.Logger()

	var wg sync.WaitGroup
	for _, msg := range []string{"batman patrols", "robin follows"} {
		wg.Go(func() { l.Info(msg) })
	}
	wg.Wait()

	got := c.Messages()
	slices.Sort(got)
	if want := []string{"batman patrols", "robin follows"}; !slices.Equal(got, want) {
		t.Fatalf("Messages() = %v, want %v", got, want)
	}
	if n := len(c.Records()); n != 2 {
		t.Fatalf("Records() holds %d records, want 2", n)
	}
	if slog.Default() != before {
		t.Fatal("slog.Default() changed: the capture must never replace it")
	}
}

func TestLogCapture_DerivedLoggersShareCapture(t *testing.T) {
	c := New(t)
	c.Logger().With("subsystem", "cave").WithGroup("g").Warn("signal lit", "who", "gordon")

	recs := c.Records()
	if len(recs) != 1 {
		t.Fatalf("got %d records, want 1", len(recs))
	}
	got := map[string]string{}
	recs[0].Attrs(func(a slog.Attr) bool {
		if a.Value.Kind() == slog.KindGroup {
			for _, g := range a.Value.Group() {
				got[a.Key+"."+g.Key] = g.Value.String()
			}
		} else {
			got[a.Key] = a.Value.String()
		}
		return true
	})
	if got["subsystem"] != "cave" || got["g.who"] != "gordon" {
		t.Fatalf("attrs = %v, want subsystem=cave and g.who=gordon", got)
	}
}
