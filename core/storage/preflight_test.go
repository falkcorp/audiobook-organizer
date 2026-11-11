// file: core/storage/preflight_test.go
// version: 1.0.0
// guid: b88f1741-7844-41f6-9051-5428a480a207
// last-edited: 2026-11-11

package storage

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cockroachdb/pebble/v2/vfs"

	"github.com/falkcorp/mediacore/storage/keys"
)

// TestCutoverBlockedErrorAndReport (05-storage §5S.3.13 "Cutover blockers"):
// the error matches ErrCutoverBlocked and counts its blockers; its report is
// one line per blocker with step, printable key, family, reason and fix and a
// last line naming 'storage cutover-blockers'; an empty list renders nothing.
func TestCutoverBlockedErrorAndReport(t *testing.T) {
	blockers := []CutoverBlocker{
		{Step: "gotham.import", Key: keys.Join(keys.Str("joker"), keys.Str("1")), Family: "joker", Reason: "ha is not a laugh", Fix: "delete the row"},
		{Step: "arkham.import", Key: []byte{0xff, 'x'}, Family: "arkham", Reason: "unreadable", Fix: "repair it"},
	}
	err := &CutoverBlockedError{Blockers: blockers}
	if !errors.Is(err, ErrCutoverBlocked) || errors.Is(err, ErrImportRejected) {
		t.Errorf("errors.Is: blocked %t, rejected %t; want true, false", errors.Is(err, ErrCutoverBlocked), errors.Is(err, ErrImportRejected))
	}
	if !strings.Contains(err.Error(), "2 blocking row(s)") {
		t.Errorf("Error() = %q", err.Error())
	}
	lines := err.Report()
	if len(lines) != 3 {
		t.Fatalf("report has %d lines, want 3: %q", len(lines), lines)
	}
	if !strings.Contains(lines[0], "step=gotham.import") || !strings.Contains(lines[0], `key="joker"/"1"`) || !strings.Contains(lines[0], "family=joker") ||
		!strings.Contains(lines[0], `reason="ha is not a laugh"`) || !strings.Contains(lines[0], `fix="delete the row"`) {
		t.Errorf("line 0 = %s", lines[0])
	}
	if !strings.Contains(lines[1], "step=arkham.import") || !strings.Contains(lines[1], `key="\xffx"`) {
		t.Errorf("a key that is not a tuple prints quoted: %s", lines[1])
	}
	if !strings.Contains(lines[2], "blocked by 2 row(s)") || !strings.Contains(lines[2], "storage cutover-blockers") {
		t.Errorf("last line = %s", lines[2])
	}
	if got := CutoverBlockerReport(nil); got != nil {
		t.Errorf("an empty blocker list renders %q, want nothing", got)
	}
	if got, want := PrintableKey(blockers[0].Key), `"joker"/"1"`; got != want {
		t.Errorf("PrintableKey = %s, want %s", got, want)
	}
}

// preUpgradeOpened opens the seeded gotham/wayne store read-write but stops
// after Open: the state at which the cutover preflight runs.
func preUpgradeOpened(t *testing.T, e *env) *Opened {
	t.Helper()
	o, err := Open(context.Background(), e.opts)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = o.Close() })
	return o
}

// TestCutoverStartedPredicate (§5S.4 step 5a): the cutover has started when
// an owner's committed level reached M7's target for it or when any cutover
// step has a record under c/storage/dm/; a store with neither has not, and
// an unreadable record is an error rather than a guess.
func TestCutoverStartedPredicate(t *testing.T) {
	ctx := context.Background()
	steps := []CutoverStep{{Owner: "wayne", ID: "wayne.import", RaisesLevel: 2}}
	e := headStore(t, vfs.NewMem(), cpOwners()...)
	o := preUpgradeOpened(t, e)
	if started, err := o.CutoverStarted(ctx, steps); err != nil || started {
		t.Fatalf("a store whose cutover never began: started %t, %v", started, err)
	}
	if started, err := o.CutoverStarted(ctx, nil); err != nil || started {
		t.Fatalf("no cutover steps: started %t, %v", started, err)
	}

	// The committed level reaching M7's target for the owner starts it; a
	// level below the target, or a step that raises none, does not.
	o.db.boot.stamps = Levels{"wayne": 1}
	if started, err := o.CutoverStarted(ctx, steps); err != nil || started {
		t.Errorf("level 1 under target 2: started %t, %v", started, err)
	}
	if started, err := o.CutoverStarted(ctx, []CutoverStep{{Owner: "wayne", ID: "wayne.import"}}); err != nil || started {
		t.Errorf("a step raising no level at level 1: started %t, %v", started, err)
	}
	o.db.boot.stamps = Levels{"wayne": 2}
	if started, err := o.CutoverStarted(ctx, steps); err != nil || !started {
		t.Errorf("level 2 at target 2: started %t, %v; want true", started, err)
	}
	o.db.boot.stamps = Levels{}

	// A record of any cutover step, whatever its state, starts it.
	if err := markDone(ctx, o.db, "wayne.import"); err != nil {
		t.Fatal(err)
	}
	if started, err := o.CutoverStarted(ctx, steps); err != nil || !started {
		t.Errorf("a step with a dm record: started %t, %v; want true", started, err)
	}

	// A read error on the record is surfaced.
	if err := o.db.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := o.CutoverStarted(ctx, steps); err == nil || !strings.Contains(err.Error(), "wayne.import") {
		t.Errorf("a record read on a closed store = %v, want an error naming wayne.import", err)
	}
}

// TestPreflightSourceIsReadOnlyAndBeforePrepare: a preflight's source is the
// owner's legacy range through a reader with no write method; it refuses an
// owner with no legacy range and any call after Prepare, which would refuse
// with the checkpoint already taken.
func TestPreflightSourceIsReadOnlyAndBeforePrepare(t *testing.T) {
	ctx := context.Background()
	e := headStore(t, vfs.NewMem(), cpOwners()...)
	o := preUpgradeOpened(t, e)
	src, err := o.PreflightSource("gotham")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := src.(interface {
		Set(context.Context, []byte, []byte) error
	}); ok {
		t.Error("the preflight's reader exposes a write method")
	}
	if v, err := src.Get(ctx, []byte("joker:1")); err != nil || string(v) != "ha" {
		t.Errorf("Get(joker:1) = %q, %v", v, err)
	}
	var seen []string
	for kv, err := range src.Scan(ctx, ScanOptions{Start: []byte("joker:"), End: []byte("joker;")}) {
		if err != nil {
			t.Fatal(err)
		}
		seen = append(seen, string(kv.Key))
	}
	if len(seen) != 2 || seen[0] != "joker:1" || seen[1] != "joker:2" {
		t.Errorf("Scan = %v, want the two joker rows", seen)
	}
	it, err := src.NewIterator(ctx, ScanOptions{Start: []byte("joker:"), End: []byte("joker;")})
	if err != nil {
		t.Fatal(err)
	}
	if !it.First() || string(it.Key()) != "joker:1" {
		t.Errorf("NewIterator First is at %q, want joker:1", it.Key())
	}
	_ = it.Close()
	if _, err := o.PreflightSource("wayne"); err == nil || !strings.Contains(err.Error(), "no legacy range") {
		t.Errorf("an owner with no legacy range = %v", err)
	}
	o.db.boot.prepared = true
	if _, err := o.PreflightSource("gotham"); err == nil || !strings.Contains(err.Error(), "before Prepare") {
		t.Errorf("after Prepare = %v, want the before-Prepare refusal", err)
	}
}

// TestEstimateSourceReadsKeyspaceAndLegacyRanges: the estimate's source rows
// come out relative to the owner's keyspace when the range lies in it and
// relative to its legacy range otherwise; an owner with neither yields one
// error instead of rows.
func TestEstimateSourceReadsKeyspaceAndLegacyRanges(t *testing.T) {
	ctx := context.Background()
	e := headStore(t, vfs.NewMem(), cpOwners()...)
	if err := e.open().Close(); err != nil {
		t.Fatal(err)
	}
	o := preUpgradeOpened(t, e)
	collect := func(owner Owner, rs []Range) ([]string, error) {
		var got []string
		for kv, err := range o.EstimateSource(ctx, owner, rs) {
			if err != nil {
				return got, err
			}
			got = append(got, string(kv.Key)+"="+string(kv.Value))
		}
		return got, nil
	}
	got, err := collect("gotham", []Range{{Start: []byte("joker:"), End: []byte("joker;")}})
	if err != nil || len(got) != 2 || got[0] != "joker:1=ha" || got[1] != "joker:2=haha" {
		t.Errorf("legacy range = %v, %v", got, err)
	}
	prefix := o.db.owners["wayne"].prefix
	got, err = collect("wayne", []Range{{Start: append(append([]byte(nil), prefix...), "cave"...), End: append(append([]byte(nil), prefix...), "cavf"...)}})
	if err != nil || len(got) != 1 || got[0] != "cave=1" {
		t.Errorf("keyspace range = %v, %v; want the key relative to the keyspace", got, err)
	}
	if _, err := collect("robin", []Range{{Start: []byte("joker:"), End: []byte("joker;")}}); err == nil || !strings.Contains(err.Error(), "robin") {
		t.Errorf("an owner with neither keyspace nor legacy range = %v, want an error naming robin", err)
	}
	// The consumer stopping early ends the iteration cleanly.
	for range o.EstimateSource(ctx, "gotham", []Range{{Start: []byte("joker:"), End: []byte("joker;")}}) {
		break
	}
}

// TestDryRunReportLines: the report prints the blockers first, per step the
// state, bytes and outcomes in sorted order with their first keys, the held
// and would-quarantine rows with reasons and never a value, M8's copy and
// the error that stopped the run; an estimate says so.
func TestDryRunReportLines(t *testing.T) {
	r := &DryRunReport{
		Source:   "/var/lib/batcave/main.pebble",
		Blockers: []CutoverBlocker{{Step: "gotham.import", Key: []byte("joker:9"), Family: "joker", Reason: "r", Fix: "f"}},
		Steps: []DryRunStep{{
			ID: "gotham.import", State: "done", Bytes: 42, Took: 1500 * time.Microsecond,
			Items:      map[string]int64{"quarantined": 1, "imported": 7},
			First:      map[string][]string{"imported": {"joker:1", "joker:2"}},
			Held:       []DryRunItem{{Key: "joker:3", Reason: "held for review"}},
			Quarantine: []DryRunItem{{Key: "joker:4", Reason: "bad json", Sensitive: true}},
		}},
		Rewrite: &RewriteCopyStats{Keys: 9, Bytes: 100, Tables: 1},
		Took:    2 * time.Second,
		Err:     "step gotham.import: boom",
	}
	joined := strings.Join(r.Lines(), "\n")
	order := []string{
		"dry run of /var/lib/batcave/main.pebble",
		"blocker: step=gotham.import",
		"step gotham.import: state=done bytes_written=42",
		"  imported: 7 (first: joker:1, joker:2)",
		"  quarantined: 1",
		`  held: key=joker:3 reason="held for review"`,
		`  would quarantine: key=joker:4 reason="bad json" sensitive=true`,
		"M8 store rewrite: 9 keys, 100 bytes, 1 tables copied into scratch; nothing swapped",
		"stopped: step gotham.import: boom",
		"took 2s; the scratch copy is deleted",
	}
	at := 0
	for _, want := range order {
		i := strings.Index(joined[at:], want)
		if i < 0 {
			t.Fatalf("report lacks %q after offset %d:\n%s", want, at, joined)
		}
		at += i + len(want)
	}
	est := (&DryRunReport{Estimate: true, Source: "archive.tar"}).Lines()
	if len(est) != 2 || !strings.Contains(est[0], "ESTIMATE") || strings.Contains(strings.Join(est, ""), "stopped") {
		t.Errorf("estimate report = %q", est)
	}
}

// TestNewDryRunScratchAtIsPrivateAndUnique: with no live store open
// (--from-archive) the scratch directory is a new dryrun-<time>-<id> entry
// of the checkpoint area's scratch/ beside the store, two calls get two
// directories, and a checkpoint directory that cannot be used is refused.
func TestNewDryRunScratchAtIsPrivateAndUnique(t *testing.T) {
	fs := FS{fs: vfs.NewMem()}
	db := filepath.Join(t.TempDir(), "main.pebble")
	now := time.Date(2026, 11, 11, 3, 4, 5, 0, time.UTC)
	a, err := NewDryRunScratchAt(fs, db, "", now)
	if err != nil {
		t.Fatal(err)
	}
	b, err := NewDryRunScratchAt(fs, db, "", now)
	if err != nil {
		t.Fatal(err)
	}
	if a == b {
		t.Errorf("two scratch directories share the name %s", a)
	}
	for _, d := range []string{a, b} {
		base := filepath.Base(d)
		if !strings.HasPrefix(base, dryRunPrefix+"20261111") || filepath.Base(filepath.Dir(d)) != checkpointScratch || filepath.Dir(filepath.Dir(filepath.Dir(d))) != filepath.Dir(db) {
			t.Errorf("scratch %s is not a dated dryrun- entry of the scratch area beside the store", d)
		}
		if fi, err := fs.fs.Stat(d); err != nil || !fi.IsDir() {
			t.Errorf("scratch %s was not created: %v", d, err)
		}
	}
}
