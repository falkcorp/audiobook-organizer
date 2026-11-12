// file: core/library/metrics_test.go
// version: 1.1.0
// guid: fa77b784-221e-4881-bda2-c8d0709a3569
// last-edited: 2026-11-11

package library

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/metrics"
	"github.com/falkcorp/mediacore/metrics/metricstest"
)

// gathered is every sample the provider serves, keyed "family label=value ...".
func gathered(t *testing.T, p *metrics.Provider) map[string]float64 {
	t.Helper()
	mfs, err := p.Gatherer().Gather()
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]float64{}
	for _, mf := range mfs {
		for _, m := range mf.GetMetric() {
			key := mf.GetName()
			for _, lp := range m.GetLabel() {
				key += " " + lp.GetName() + "=" + lp.GetValue()
			}
			switch {
			case m.GetCounter() != nil:
				out[key] = m.GetCounter().GetValue()
			case m.GetGauge() != nil:
				out[key] = m.GetGauge().GetValue()
			case m.GetHistogram() != nil:
				out[key+" count"] = float64(m.GetHistogram().GetSampleCount())
			}
		}
	}
	return out
}

// TestLibrarySeries_Golden: the library scope serves exactly the families of
// testdata/series.golden, and each moves with what it counts.
func TestLibrarySeries_Golden(t *testing.T) {
	ctx := context.Background()
	p, err := metrics.New(ctx, metrics.Options{AppID: "gotham", Prometheus: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.Shutdown(context.Background()) })
	sc, err := p.Core("library", MetricLabels()...)
	if err != nil {
		t.Fatal(err)
	}
	poll := spec("poll-1", books, RoleImport)
	poll.Watch = WatchPoll
	r := newRig(t, newStore(t), rigOpts{metrics: sc, seams: seams{usage: func(string) (Usage, error) { return Usage{Total: 1000, Avail: 500}, nil }}}, spec("books-library", books, RoleLibrary), poll)
	set := defaultSettings()
	set.roots = r.specs
	set.pollInterval = 5 * time.Millisecond
	if err := r.m.apply(ctx, set); err != nil {
		t.Fatal(err)
	}

	src := r.put("poll-1", "Arkham/a.m4b", "twelve bytes")
	dst := Path{Root: "books-library", Rel: "Gotham/a.m4b"}
	if _, err := r.m.Transfer(ctx, books, src, dst, copyOpts()); err != nil {
		t.Fatal(err)
	}
	if _, err := r.m.Transfer(ctx, books, src, dst, copyOpts()); !errors.Is(err, fs.ErrExist) { // refused: the name exists
		t.Fatalf("second copy = %v, want fs.ErrExist", err)
	}
	if _, err := r.m.Hash(ctx, dst); err != nil {
		t.Fatal(err)
	}
	if _, err := r.m.Quarantine(ctx, books, Path{Root: "poll-1", Rel: "Arkham/a.m4b"}, "x"); err != nil {
		t.Fatal(err)
	}
	wctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	seq, err := r.m.Watch(wctx, "poll-1", WatchOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for batch := range seq { // the first poll tick is a rescan
		if len(batch) == 0 || batch[0].Kind != EventRescan {
			t.Fatalf("batch = %+v", batch)
		}
		break
	}

	metricstest.AssertSeries(t, p, sc, "testdata/series.golden")
	metricstest.AssertNoOverflow(t, p)

	got := gathered(t, p)
	want := map[string]float64{
		"mediacore_library_transfers_total did=copy mode=copy outcome=ok":      1,
		"mediacore_library_transfers_total did=none mode=copy outcome=refused": 1,
		"mediacore_library_transfer_bytes_total did=copy":                      12,
		"mediacore_library_transfer_duration_seconds did=copy count":           1,
		"mediacore_library_hash_bytes_total root=books-library":                12,
		"mediacore_library_refusals_total reason=exists":                       1,
		"mediacore_library_watch_rescans_total reason=poll root=poll-1":        1,
		"mediacore_library_watch_events_total kind=rescan root=poll-1":         1,
		"mediacore_library_quarantined owner=books":                            1,
		"mediacore_library_root_state root=books-library state=ok":             1,
		"mediacore_library_root_state root=books-library state=disabled":       0,
		"mediacore_library_root_state root=poll-1 state=ok":                    1,
	}
	for k, v := range want {
		if g, ok := got[k]; !ok || g != v {
			t.Errorf("%s = %v (present %v), want %v", k, g, ok, v)
		}
	}
	if g := got["mediacore_library_root_avail_bytes root=books-library"]; g != 500 {
		t.Errorf("root_avail_bytes = %v, want the 500 the filesystem reports", g)
	}
	// A root whose sentinel disappears reports unmounted.
	if err := removeSentinel(r, "books-library"); err != nil {
		t.Fatal(err)
	}
	if got := gathered(t, p); got["mediacore_library_root_state root=books-library state=unmounted"] != 1 || got["mediacore_library_root_state root=books-library state=ok"] != 0 {
		t.Errorf("a root without its sentinel: %v", got)
	}
}

func removeSentinel(r *rig, id RootID) error {
	return os.Remove(r.abs(id, DefaultAppDir+"/root.json"))
}

// A refusal is counted wherever the library makes one, not only in Transfer:
// Quarantine, Release and Hash count theirs, and a reserved area is a protected
// path (5L.8's label set has no other word for it).
func TestRefusalsAreCountedAtEveryEntryPoint(t *testing.T) {
	r := reservedRig(t)
	good := r.put("books-library", "Gotham/01.m4b", "audio")
	refusals := func() map[string]uint64 { return r.m.ctr.snapshot().refusals }
	expect := func(step string, want map[string]uint64) {
		t.Helper()
		got := refusals()
		for _, reason := range []string{reasonEscape, reasonExists, reasonProtected, reasonSpace, reasonUnmounted} {
			if got[reason] != want[reason] {
				t.Errorf("after %s: refusals[%s] = %d, want %d (all: %v)", step, reason, got[reason], want[reason], got)
			}
		}
	}
	expect("setup", nil)

	// Transfer into a reserved area.
	cp := NewTransferOptions(Copy, PermLibrary, VerifySize)
	if _, err := r.m.Transfer(ctxbg(), books, good, Path{Root: "books-library", Rel: DefaultAppDir + "/x"}, cp); !errors.Is(err, ErrReservedPath) {
		t.Fatalf("Transfer into the AppDir = %v", err)
	}
	expect("a Transfer into the AppDir", map[string]uint64{reasonProtected: 1})

	// Quarantine of a reserved area, and of a path that escapes the root.
	if _, err := r.m.Quarantine(ctxbg(), books, Path{Root: "books-library", Rel: DefaultAppDir}, "x"); !errors.Is(err, ErrReservedPath) {
		t.Fatalf("Quarantine of the AppDir = %v", err)
	}
	expect("a Quarantine of the AppDir", map[string]uint64{reasonProtected: 2})
	if _, err := r.m.Quarantine(ctxbg(), books, Path{Root: "books-library", Rel: "../outside"}, "x"); !errors.Is(err, ErrEscapesRoot) {
		t.Fatalf("Quarantine of an escaping path = %v", err)
	}
	expect("a Quarantine of an escaping path", map[string]uint64{reasonProtected: 2, reasonEscape: 1})

	// Hash of a path that escapes the root.
	if _, err := r.m.Hash(ctxbg(), Path{Root: "books-library", Rel: "../outside"}); !errors.Is(err, ErrEscapesRoot) {
		t.Fatalf("Hash of an escaping path = %v", err)
	}
	expect("a Hash of an escaping path", map[string]uint64{reasonProtected: 2, reasonEscape: 2})

	// Release onto a name that now exists, and into a reserved area.
	id, err := r.m.Quarantine(ctxbg(), books, good, "corrupt")
	if err != nil {
		t.Fatal(err)
	}
	expect("a good Quarantine", map[string]uint64{reasonProtected: 2, reasonEscape: 2})
	r.put("books-library", good.Rel, "a new edition took the name")
	if _, err := r.m.Release(ctxbg(), books, id, nil); !errors.Is(err, fs.ErrExist) {
		t.Fatalf("Release onto a taken name = %v", err)
	}
	expect("a Release onto a taken name", map[string]uint64{reasonProtected: 2, reasonEscape: 2, reasonExists: 1})
	if _, err := r.m.Release(ctxbg(), books, id, &Path{Root: "books-library", Rel: DefaultAppDir + "/y"}); !errors.Is(err, ErrReservedPath) {
		t.Fatalf("Release into the AppDir = %v", err)
	}
	expect("a Release into the AppDir", map[string]uint64{reasonProtected: 3, reasonEscape: 2, reasonExists: 1})

	// Successes and non-refusal errors count nothing.
	if _, err := r.m.Hash(ctxbg(), good); err != nil {
		t.Fatal(err)
	}
	if _, err := r.m.Hash(ctxbg(), Path{Root: "books-library", Rel: "Gotham/missing.m4b"}); err == nil {
		t.Fatal("Hash of a missing file succeeded")
	}
	if _, err := r.m.Release(ctxbg(), books, "no-such-record", nil); err == nil {
		t.Fatal("Release of an unknown record succeeded")
	}
	expect("successes and plain errors", map[string]uint64{reasonProtected: 3, reasonEscape: 2, reasonExists: 1})
}

// refusalReason maps every refusal 5L.8 names, wrapped or joined, and nothing
// else.
func TestRefusalReasonTable(t *testing.T) {
	for _, c := range []struct {
		err  error
		want string
	}{
		{nil, ""},
		{ErrEscapesRoot, reasonEscape},
		{fmt.Errorf("x: %w", fs.ErrExist), reasonExists},
		{ErrSourceProtected, reasonProtected},
		{errors.Join(errors.New("oracle"), ErrReservedPath), reasonProtected},
		{ErrInsufficientSpace, reasonSpace},
		{ErrNotMounted, reasonUnmounted},
		{ErrCrossDevice, ""},
		{context.Canceled, ""},
		{errors.New("disk on fire"), ""},
	} {
		if got := refusalReason(c.err); got != c.want {
			t.Errorf("refusalReason(%v) = %q, want %q", c.err, got, c.want)
		}
	}
}

// ValidateRel: the rels that leave the root (absolute, "..", "../x") are escapes
// as well as bad rels, so the refusal is counted as an escape; every other
// malformed rel is a bad rel only, and a valid rel is accepted.
func TestValidateRelClassifiesEscapes(t *testing.T) {
	for _, c := range []struct {
		rel               string
		bad, escape, okay bool
	}{
		{"Gotham/01.m4b", false, false, true},
		{"..", true, true, false},
		{"../Arkham", true, true, false},
		{"/etc/passwd", true, true, false},
		{"", true, false, false},
		{"a\x00b", true, false, false},
		{"a//b", true, false, false},
		{"a/./b", true, false, false},
		{"a/../b", true, false, false},
		{".", true, false, false},
		{"a/", true, false, false},
		{"..hidden", false, false, true}, // a name that starts with dots is not "..".
	} {
		err := ValidateRel(c.rel)
		if (err == nil) != c.okay {
			t.Errorf("ValidateRel(%q) = %v, want ok=%v", c.rel, err, c.okay)
			continue
		}
		if got := errors.Is(err, ErrBadRel); got != c.bad {
			t.Errorf("ValidateRel(%q): ErrBadRel = %v, want %v", c.rel, got, c.bad)
		}
		if got := errors.Is(err, ErrEscapesRoot); got != c.escape {
			t.Errorf("ValidateRel(%q): ErrEscapesRoot = %v, want %v", c.rel, got, c.escape)
		}
	}
}
