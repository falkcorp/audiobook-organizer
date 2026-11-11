// file: core/backup/journal/lines_test.go
// version: 1.0.0
// guid: a4d27e60-3b19-4c85-9e1f-72c0d8b5a346
// last-edited: 2026-11-11

package journal

import (
	"context"
	"slices"
	"testing"
	"time"
)

// TestLines_RoundTripThroughTheChain: the bookkeeping lines survive the file
// (encode, MAC, decode) with their Detail layouts, and AbortedRestores names the
// restore lines a restore_aborted line closes and those whose restore journal
// swapped back.
func TestLines_RoundTripThroughTheChain(t *testing.T) {
	ctx := context.Background()
	fx := newFixture(t)
	j := fx.open(t)
	at := time.Date(2026, 11, 10, 5, 0, 0, 0, time.UTC)
	fx.commitRevoke(t, j, "joker") // 1
	for _, r := range []Revocation{
		RestoreLineOf(RestoreInfo{Kind: RestoreArchive, Source: "arch-1", Boundary: 1, RestoreID: "r-1"}, at),  // 2
		RestoreLineOf(RestoreInfo{Kind: RestoreExternal, Boundary: 1}, at),                                     // 3
		RestoreLineOf(RestoreInfo{Kind: RestoreCheckpoint, Source: "cp-1", Boundary: 2, RestoreID: "r-3"}, at), // 4
		AbortedLine(2, at), // 5
		GapLine(at, GapOtherInstall),
	} {
		if _, err := j.AppendLine(ctx, r); err != nil {
			t.Fatal(err)
		}
	}
	c := fx.chain(t)
	if c.Break != nil || len(c.Lines) != 6 {
		t.Fatalf("chain break %v, %d lines", c.Break, len(c.Lines))
	}
	if info, ok := c.Lines[1].RestoreInfo(); !ok || info != (RestoreInfo{Kind: "archive", Source: "arch-1", Boundary: 1, RestoreID: "r-1"}) {
		t.Errorf("restore line 2 = %+v, %v", info, ok)
	}
	if info, ok := c.Lines[2].RestoreInfo(); !ok || info.Kind != "external" || info.RestoreID != "" || info.Boundary != 1 {
		t.Errorf("external restore line = %+v, %v", info, ok)
	}
	if n, ok := c.Lines[4].AbortedSeq(); !ok || n != 2 {
		t.Errorf("aborted seq = %d, %v", n, ok)
	}
	if since, ok := gapSince(c.Lines[5].Revocation); !ok || !since.Equal(at) || c.Lines[5].Detail[1] != GapOtherInstall {
		t.Errorf("gap line = %+v", c.Lines[5])
	}
	if _, ok := c.Lines[0].RestoreInfo(); ok {
		t.Error("a revocation line read as a restore line")
	}
	if _, ok := c.Lines[1].AbortedSeq(); ok {
		t.Error("a restore line read as restore_aborted")
	}
	got := c.AbortedRestores(func(id string) bool { return id == "r-3" })
	if !got[2] || !got[4] || got[3] || len(got) != 2 {
		t.Errorf("AbortedRestores = %v, want seq 2 (restore_aborted) and 4 (swapped back)", got)
	}
	if got := c.AbortedRestores(nil); !got[2] || got[4] {
		t.Errorf("AbortedRestores(nil) = %v", got)
	}

	for _, bad := range []Revocation{
		{Kind: Restore, Detail: []string{"archive", "a"}},
		{Kind: Restore, Detail: []string{"archive", "a", "not-a-number"}},
		{Kind: RestoreAborted},
		{Kind: RestoreAborted, Detail: []string{"x"}},
	} {
		if _, ok := bad.RestoreInfo(); ok && bad.Kind == Restore {
			t.Errorf("%+v read as a restore line", bad)
		}
		if _, ok := bad.AbortedSeq(); ok {
			t.Errorf("%+v read as an aborted seq", bad)
		}
	}
}

// TestHooked_MarkAndRead: a restore line is hooked once its record is written;
// the record is per seq.
func TestHooked_MarkAndRead(t *testing.T) {
	ctx := context.Background()
	fx := newFixture(t)
	if ok, err := Hooked(ctx, fx.ks, 5); ok || err != nil {
		t.Fatalf("Hooked before = %v, %v", ok, err)
	}
	must(t, MarkHooked(ctx, fx.ks, 5, time.Date(2026, 11, 10, 5, 0, 0, 0, time.UTC), map[string]int{"api_keys_revoked": 2}))
	for seq, want := range map[uint64]bool{5: true, 4: false, 6: false} {
		if ok, err := Hooked(ctx, fx.ks, seq); err != nil || ok != want {
			t.Errorf("Hooked(%d) = %v, %v; want %v", seq, ok, err, want)
		}
	}
	keys := []string{}
	for kv, err := range fx.ks.ScanPrefix(ctx, []byte(hookedPrefix)) {
		must(t, err)
		keys = append(keys, string(kv.Key))
	}
	if !slices.Equal(keys, []string{"restore_hooked/00000000000000000005"}) {
		t.Errorf("hooked keys = %v", keys)
	}
}
