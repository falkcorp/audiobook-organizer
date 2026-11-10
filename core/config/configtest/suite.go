// file: core/config/configtest/suite.go
// version: 1.0.0
// guid: 2005576f-0163-4986-b5d4-c76e75ef5513
// last-edited: 2026-11-10

package configtest

import (
	"bytes"
	"context"
	"encoding/json"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/config"
	"github.com/falkcorp/mediacore/storage"
)

// Harness is what OverrideStoreSuite needs from an implementation. Each
// subtest asks for a fresh one over an empty store.
type Harness struct {
	// Store is the implementation under test.
	Store config.OverrideStore
	// Committer commits a config.Commit through Store (the kernel's, for the
	// native store).
	Committer config.Committer
	// Commit runs stage in a fresh batch per attempt and commits it,
	// retrying on storage.ErrConflict as the Committer does.
	Commit func(ctx context.Context, stage func(ctx context.Context, b *storage.Batch) error) error
	// Rows returns the raw stored row of every path.
	Rows func(ctx context.Context) (map[config.Path][]byte, error)
	// Corrupt replaces p's row with raw, behind the store's back.
	Corrupt func(ctx context.Context, p config.Path, raw []byte) error
}

// OverrideStoreSuite is the contract of config.OverrideStore (05-config
// §1.11): Load is read-only; Stage changes exactly the staged keys (the
// others byte-equal); an unset removes the row; two concurrent Stages on one
// row both commit through the retry; an unparsable row quarantines its
// section at Attach and is never overwritten by a write of another key.
func OverrideStoreSuite(t *testing.T, newHarness func(t *testing.T) Harness) {
	t.Run("LoadIsReadOnlyAndRoundTrips", func(t *testing.T) { suiteLoad(t, newHarness(t)) })
	t.Run("StageChangesOnlyStagedKeys", func(t *testing.T) { suiteOnlyStaged(t, newHarness(t)) })
	t.Run("ConcurrentStagesBothCommit", func(t *testing.T) { suiteConcurrent(t, newHarness(t)) })
	t.Run("UnparsableRowQuarantinedNeverOverwritten", func(t *testing.T) { suiteUnparsable(t, newHarness(t)) })
}

// The suite's keys: scalars, a list, a map value and a restart key beside a
// boot key.
const (
	keyLevel    config.Path = BatcavePath + ".level"
	keyCount    config.Path = BatcavePath + ".gadget.count"
	keyWorkers  config.Path = BatcavePath + ".workers"
	keyVillains config.Path = WatchPath + ".villains"
	keyMax      config.Path = WatchPath + ".max"
)

func stage(ctx context.Context, h Harness, set []config.Override, unset ...config.Path) error {
	return h.Commit(ctx, func(ctx context.Context, b *storage.Batch) error { return h.Store.Stage(ctx, b, set, unset) })
}

func load(t *testing.T, ctx context.Context, h Harness) map[config.Path]config.Override {
	t.Helper()
	out := map[config.Path]config.Override{}
	for ov, err := range h.Store.Load(ctx) {
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if _, dup := out[ov.Path]; dup {
			t.Fatalf("Load yielded %s twice", ov.Path)
		}
		out[ov.Path] = ov
	}
	return out
}

func rows(t *testing.T, ctx context.Context, h Harness) map[config.Path][]byte {
	t.Helper()
	r, err := h.Rows(ctx)
	if err != nil {
		t.Fatalf("Rows: %v", err)
	}
	return r
}

func jsonEqual(a, b []byte) bool {
	var x, y any
	if json.Unmarshal(a, &x) != nil || json.Unmarshal(b, &y) != nil {
		return false
	}
	xb, err1 := json.Marshal(x)
	yb, err2 := json.Marshal(y)
	return err1 == nil && err2 == nil && bytes.Equal(xb, yb)
}

func ov(p config.Path, v string) config.Override {
	return config.Override{Path: p, Value: json.RawMessage(v), Gen: 7, At: time.Date(2026, 11, 10, 8, 0, 0, 0, time.UTC), By: "alfred"}
}

func suiteLoad(t *testing.T, h Harness) {
	ctx := context.Background()
	want := []config.Override{
		ov(keyLevel, `"debug"`), ov(keyCount, `7`), ov(keyWorkers, `8`),
		ov(keyVillains, `["riddler","penguin"]`), ov(WatchPath+".rogues", `{"arkham":"joker"}`),
	}
	if err := stage(ctx, h, want); err != nil {
		t.Fatalf("stage: %v", err)
	}
	before := rows(t, ctx, h)
	if len(before) != len(want) {
		t.Fatalf("%d rows after staging %d keys, want one row per key: %v", len(before), len(want), before)
	}
	first := load(t, ctx, h)
	second := load(t, ctx, h)
	after := rows(t, ctx, h)
	if len(after) != len(before) {
		t.Fatalf("Load changed the row count: %d -> %d", len(before), len(after))
	}
	for p, raw := range before {
		if !bytes.Equal(after[p], raw) {
			t.Errorf("Load rewrote %s: %q -> %q", p, raw, after[p])
		}
	}
	for _, w := range want {
		for i, got := range []map[config.Path]config.Override{first, second} {
			g, ok := got[w.Path]
			switch {
			case !ok:
				t.Errorf("load %d: %s missing", i+1, w.Path)
			case !jsonEqual(g.Value, w.Value):
				t.Errorf("load %d: %s = %s, want %s", i+1, w.Path, g.Value, w.Value)
			case g.Gen != w.Gen || g.By != w.By || !g.At.Equal(w.At):
				t.Errorf("load %d: %s = gen %d by %q at %v, want gen %d by %q at %v", i+1, w.Path, g.Gen, g.By, g.At, w.Gen, w.By, w.At)
			}
		}
	}
}

func suiteOnlyStaged(t *testing.T, h Harness) {
	ctx := context.Background()
	if err := stage(ctx, h, []config.Override{ov(keyLevel, `"debug"`), ov(keyCount, `7`), ov(keyVillains, `["bane"]`)}); err != nil {
		t.Fatalf("stage: %v", err)
	}
	before := rows(t, ctx, h)
	if err := stage(ctx, h, []config.Override{ov(keyCount, `9`)}, keyVillains); err != nil {
		t.Fatalf("stage: %v", err)
	}
	after := rows(t, ctx, h)
	if !bytes.Equal(after[keyLevel], before[keyLevel]) {
		t.Errorf("%s changed though it was not staged: %q -> %q", keyLevel, before[keyLevel], after[keyLevel])
	}
	if _, ok := after[keyVillains]; ok {
		t.Errorf("%s survived its unset", keyVillains)
	}
	got := load(t, ctx, h)
	if len(got) != 2 || !jsonEqual(got[keyCount].Value, []byte(`9`)) || !jsonEqual(got[keyLevel].Value, []byte(`"debug"`)) {
		t.Errorf("after set+unset Load = %v, want level debug and count 9 only", got)
	}
	// Unsetting a key with no row is not an error and changes nothing.
	if err := stage(ctx, h, nil, keyMax); err != nil {
		t.Fatalf("unset of an absent row: %v", err)
	}
	if n := len(rows(t, ctx, h)); n != 2 {
		t.Errorf("%d rows after unsetting an absent key, want 2", n)
	}
}

func suiteConcurrent(t *testing.T, h Harness) {
	ctx := context.Background()
	if err := stage(ctx, h, []config.Override{ov(keyLevel, `"warn"`)}); err != nil {
		t.Fatalf("stage: %v", err)
	}
	staged, aDone := make(chan struct{}), make(chan struct{})
	var attempts atomic.Int32
	var bErr error
	var wg sync.WaitGroup
	wg.Go(func() {
		bErr = h.Commit(ctx, func(ctx context.Context, b *storage.Batch) error {
			n := attempts.Add(1)
			if err := h.Store.Stage(ctx, b, []config.Override{ov(keyLevel, `"debug"`)}, nil); err != nil {
				return err
			}
			if n == 1 {
				close(staged)
				<-aDone
			}
			return nil
		})
	})
	<-staged
	aErr := stage(ctx, h, []config.Override{ov(keyLevel, `"info"`)})
	close(aDone)
	wg.Wait()
	if aErr != nil || bErr != nil {
		t.Fatalf("concurrent stages: first %v, second %v; want both to commit", aErr, bErr)
	}
	if n := attempts.Load(); n < 2 {
		t.Errorf("the second stage committed in %d attempt(s): its read of the row was not checked at commit (want a conflict and a retry)", n)
	}
	if got := load(t, ctx, h)[keyLevel]; !jsonEqual(got.Value, []byte(`"debug"`)) {
		t.Errorf("%s = %s after both commits, want the retried writer's \"debug\"", keyLevel, got.Value)
	}
}

func suiteUnparsable(t *testing.T, h Harness) {
	ctx := context.Background()
	bad := []byte("{not json")
	if err := stage(ctx, h, []config.Override{ov(keyLevel, `"debug"`)}); err != nil {
		t.Fatalf("stage: %v", err)
	}
	if err := h.Corrupt(ctx, keyMax, bad); err != nil {
		t.Fatalf("Corrupt: %v", err)
	}
	got := load(t, ctx, h)
	if _, ok := got[keyMax]; !ok {
		t.Fatalf("Load hid the unparsable row of %s; it must yield it so config quarantines its section", keyMax)
	}
	st, err := config.NewStore(config.Options{AppID: "gotham"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := st.Close(context.Background()); err != nil {
			t.Error(err)
		}
	})
	if _, err := config.Register(st.Registry(), BatcaveSection()); err != nil {
		t.Fatal(err)
	}
	watch, err := config.Register(st.Registry(), WatchSection())
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Load(ctx); err != nil {
		t.Fatal(err)
	}
	rep, err := st.Attach(ctx, h.Store, h.Committer)
	if err != nil {
		t.Fatalf("Attach failed on an unparsable row; it must quarantine the section and boot: %v", err)
	}
	if rep.Quarantined[keyMax] == nil {
		t.Errorf("AttachReport.Quarantined = %v, want %s", rep.Quarantined, keyMax)
	}
	if watch.Get().Max != 10 {
		t.Errorf("quarantined section reads max %d, want the default 10", watch.Get().Max)
	}
	admin := config.Actor{Subject: "alfred", Interactive: true, Can: func(string) bool { return true }}
	if _, err := st.Apply(ctx, config.Patch{Set: map[config.Path]json.RawMessage{keyVillains: json.RawMessage(`["two-face"]`)}}, admin); err != nil {
		t.Fatalf("Apply of another key of the quarantined section: %v", err)
	}
	if raw := rows(t, ctx, h)[keyMax]; !bytes.Equal(raw, bad) {
		t.Errorf("the unparsable row of %s was overwritten: %q", keyMax, raw)
	}
}
