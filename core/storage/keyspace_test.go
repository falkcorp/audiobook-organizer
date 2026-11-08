// file: core/storage/keyspace_test.go
// version: 1.0.0
// guid: 33d270b9-deec-461f-a288-8e9c004fa4a1
// last-edited: 2026-11-07

package storage

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"log/slog"
	"slices"
	"strings"
	"testing"
)

// TestKeyspaceWritesCannotEscapePrefix (05-storage §5S.10): relative keys
// always land under Prefix(), Sub included, and DeleteRange bounds stay
// inside the keyspace.
func TestKeyspaceWritesCannotEscapePrefix(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t, OwnerSpec{Owner: "wayne", Spec: wayneSpec(), Enabled: true},
		OwnerSpec{Owner: "robin", Spec: Spec{Keyspace: true}, Enabled: true})
	db := e.open()
	ks, _ := db.Keyspace("wayne")
	robin, _ := db.Keyspace("robin")
	if ks.Prefix() != "m/wayne/" {
		t.Fatalf("prefix = %q", ks.Prefix())
	}
	if err := robin.Set(ctx, []byte("cape"), []byte("red")); err != nil {
		t.Fatal(err)
	}
	// Keys that look like escapes are just relative names.
	for _, k := range []string{"../robin/cape", "/m/robin/cape", "\xff", ""} {
		if err := ks.Set(ctx, []byte(k), []byte("x")); err != nil {
			t.Fatalf("Set(%q): %v", k, err)
		}
	}
	sub := ks.Sub("cave")
	if sub.Prefix() != "m/wayne/cave/" {
		t.Fatalf("sub prefix = %q", sub.Prefix())
	}
	if err := sub.Set(ctx, []byte("batmobile"), []byte("1")); err != nil {
		t.Fatal(err)
	}
	if bad := ks.Sub("Cave/.."); bad.Prefix() != "" {
		t.Fatalf("invalid Sub name gave %q", bad.Prefix())
	}
	if err := ks.Sub("../x").Set(ctx, []byte("k"), nil); !errors.Is(err, ErrInvalidKeyspace) {
		t.Fatalf("zero keyspace Set: %v", err)
	}
	// DeleteRange with nil end clears only the keyspace.
	if err := ks.DeleteRange(ctx, nil, nil); err != nil {
		t.Fatal(err)
	}
	if v, err := robin.Get(ctx, []byte("cape")); err != nil || string(v) != "red" {
		t.Fatalf("robin's key after wayne's DeleteRange: %q, %v", v, err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	for _, k := range rawKeys(t, e.fs, e.path, "m/") {
		if !strings.HasPrefix(k, "m/robin/") {
			t.Errorf("key %q survived wayne's DeleteRange or escaped its prefix", k)
		}
	}
}

// TestLegacyRangeRefusesForeignKeys (05-storage §5S.10): absolute keys are
// checked on reads and writes by longest match, carve-outs belong to their
// owner, and scans are clamped.
func TestLegacyRangeRefusesForeignKeys(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t,
		OwnerSpec{Owner: "gotham", Enabled: true, Spec: Spec{Claims: []Claim{
			{Prefix: "joker:", Mode: ClaimReadWrite, Except: []string{"joker:harley:"}},
		}}},
		OwnerSpec{Owner: "arkham", Enabled: true, Spec: Spec{Claims: []Claim{
			{Prefix: "joker:harley:", Mode: ClaimReadWrite},
		}}},
	)
	e.seedRaw("joker:1", "a", "joker:harley:1", "b", "joker:2", "c", "joker:zed", "z", "penguin:1", "d")
	db := e.open()
	g, _ := db.Legacy("gotham")
	a, _ := db.Legacy("arkham")
	if _, err := g.Get(ctx, []byte("joker:1")); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"joker:harley:1", "penguin:1", "c/storage/install", "m/wayne/x"} {
		if _, err := g.Get(ctx, []byte(k)); !errors.Is(err, ErrOutsideKeyspace) {
			t.Errorf("Get(%q) = %v, want ErrOutsideKeyspace", k, err)
		}
		if err := g.Set(ctx, []byte(k), nil); !errors.Is(err, ErrOutsideKeyspace) {
			t.Errorf("Set(%q) = %v, want ErrOutsideKeyspace", k, err)
		}
	}
	if err := a.Set(ctx, []byte("joker:harley:2"), []byte("e")); err != nil {
		t.Fatal(err)
	}
	var got []string
	for kv, err := range g.Scan(ctx, ScanOptions{}) {
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, string(kv.Key))
	}
	if want := []string{"joker:1", "joker:2", "joker:zed"}; !slices.Equal(got, want) {
		t.Fatalf("clamped scan = %v, want %v", got, want)
	}
	// Reverse and a cursor skip the carve-out too.
	got = nil
	for kv, err := range g.Scan(ctx, ScanOptions{Reverse: true}) {
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, string(kv.Key))
	}
	if want := []string{"joker:zed", "joker:2", "joker:1"}; !slices.Equal(got, want) {
		t.Fatalf("reverse scan = %v, want %v", got, want)
	}
	it, err := g.NewIterator(ctx, ScanOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = it.Close() }()
	if !it.SeekGE([]byte("joker:harley:")) || string(it.Key()) != "joker:zed" {
		t.Fatalf("SeekGE into the carve-out landed on %q", it.Key())
	}
	if !it.SeekLT([]byte("joker:harley:9")) || string(it.Key()) != "joker:2" {
		t.Fatalf("SeekLT into the carve-out landed on %q", it.Key())
	}
	if err := g.DeleteRange(ctx, []byte("joker:"), []byte("joker;")); !errors.Is(err, ErrOutsideKeyspace) {
		t.Fatalf("DeleteRange over the carve-out = %v", err)
	}
}

// TestSameOwnerNestedClaimsSelectMode (05-storage §5S.10): nested claims of
// one owner boot without Except, and the longest match selects the child's
// mode, a retired sub-prefix of a read-write parent included.
func TestSameOwnerNestedClaimsSelectMode(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t, OwnerSpec{Owner: "gotham", Enabled: true, Spec: Spec{Claims: gothamClaims()}})
	e.seedRaw("joker:retired:1", "old", "arkham:1", "cell")
	db := e.open()
	g, _ := db.Legacy("gotham")
	if err := g.Set(ctx, []byte("joker:1"), []byte("x")); err != nil {
		t.Fatalf("read-write parent: %v", err)
	}
	if err := g.Set(ctx, []byte("joker:retired:2"), []byte("x")); !errors.Is(err, ErrClaimMode) {
		t.Fatalf("Set in the retired child = %v, want ErrClaimMode", err)
	}
	if err := g.Delete(ctx, []byte("joker:retired:1")); err != nil {
		t.Fatalf("Delete in the retired child: %v", err)
	}
	if err := g.Delete(ctx, []byte("arkham:1")); !errors.Is(err, ErrClaimMode) {
		t.Fatalf("Delete in a read-only claim = %v, want ErrClaimMode", err)
	}
	if v, err := g.Get(ctx, []byte("arkham:1")); err != nil || string(v) != "cell" {
		t.Fatalf("read in a read-only claim: %q, %v", v, err)
	}
}

// TestClaimOverlapFailsBoot (05-storage §5S.10): cross-owner overlaps
// without Except, identical claims and claims under c/ or m/ fail boot, and
// every conflict is listed together.
func TestClaimOverlapFailsBoot(t *testing.T) {
	e := newEnv(t,
		OwnerSpec{Owner: "gotham", Enabled: true, Spec: Spec{Claims: []Claim{
			{Prefix: "joker:", Mode: ClaimReadWrite},
			{Prefix: "m/", Mode: ClaimReadWrite},
		}}},
		OwnerSpec{Owner: "arkham", Enabled: true, Spec: Spec{Claims: []Claim{
			{Prefix: "joker:harley:", Mode: ClaimReadWrite},
			{Key: "bane", Mode: ClaimReadWrite},
		}}},
		OwnerSpec{Owner: "blackgate", Enabled: true, Spec: Spec{Claims: []Claim{
			{Key: "bane", Mode: ClaimReadOnly},
		}}},
	)
	_, err := e.tryOpen()
	var cc *ClaimConflictError
	if !errors.As(err, &cc) || !errors.Is(err, ErrClaimConflict) {
		t.Fatalf("err = %v, want a ClaimConflictError", err)
	}
	if len(cc.Problems) < 3 {
		t.Fatalf("want every conflict listed, got %d: %v", len(cc.Problems), cc.Problems)
	}
	for _, want := range []string{"joker:harley:", `"m/"`, `"bane"`} {
		if !strings.Contains(cc.Error(), want) {
			t.Errorf("conflicts %q do not name %s", cc.Error(), want)
		}
	}
	// The carve-out is accepted.
	ok := newEnv(t,
		OwnerSpec{Owner: "gotham", Enabled: true, Spec: Spec{Claims: []Claim{{Prefix: "joker:", Mode: ClaimReadWrite, Except: []string{"joker:harley:"}}}}},
		OwnerSpec{Owner: "arkham", Enabled: true, Spec: Spec{Claims: []Claim{{Prefix: "joker:harley:", Mode: ClaimReadWrite}}}},
	)
	ok.open()
}

// TestFirstBootRefusesForeignKeys (05-storage §5S.10): a store without the
// install marker that holds keys under c/ or m/ halts boot with
// ErrForeignKeys and a sample, and nothing is written.
func TestFirstBootRefusesForeignKeys(t *testing.T) {
	e := newEnv(t, OwnerSpec{Owner: "wayne", Spec: wayneSpec(), Enabled: true})
	e.seedRaw("joker:1", "a", "c/rogue", "x", "m/scarecrow/toxin", "y")
	_, err := e.tryOpen()
	mustErrIs(t, err, ErrForeignKeys)
	var fk *ForeignKeysError
	if !errors.As(err, &fk) || len(fk.Sample) != 2 {
		t.Fatalf("sample = %v", err)
	}
	if got := rawKeys(t, e.fs, e.path, "c/storage/"); len(got) != 0 {
		t.Fatalf("refused boot wrote %v", got)
	}
}

// TestReadOnlyOpenRefusesWrites (05-storage §5S.10): Admin.OpenReadOnly
// writes nothing, and every write through its handles fails with
// ErrReadOnly.
func TestReadOnlyOpenRefusesWrites(t *testing.T) {
	ctx := context.Background()
	owners := []OwnerSpec{
		{Owner: "wayne", Spec: wayneSpec(), Enabled: true},
		{Owner: "gotham", Spec: Spec{Claims: gothamClaims()}, Enabled: true},
	}
	src := newEnv(t, owners...)
	src.seedRaw("joker:1", "a")
	db := src.open()
	ks, _ := db.Keyspace("wayne")
	if err := ks.Set(ctx, []byte("cowl"), []byte("black")); err != nil {
		t.Fatal(err)
	}
	other := newEnvFS(t, src.fs, owners...)
	if err := db.Admin().Checkpoint(ctx, other.path); err != nil {
		t.Fatal(err)
	}
	before := rawKeys(t, src.fs, other.path, "")
	ro, err := db.Admin().OpenReadOnly(ctx, other.path)
	if err != nil {
		t.Fatal(err)
	}
	rks, err := ro.Keyspace("wayne")
	if err != nil {
		t.Fatal(err)
	}
	rlr, err := ro.Legacy("gotham")
	if err != nil {
		t.Fatal(err)
	}
	if v, err := rks.Get(ctx, []byte("cowl")); err != nil || string(v) != "black" {
		t.Fatalf("read-only Get: %q, %v", v, err)
	}
	if v, err := rlr.Get(ctx, []byte("joker:1")); err != nil || string(v) != "a" {
		t.Fatalf("read-only legacy Get: %q, %v", v, err)
	}
	writes := map[string]error{
		"Set":              rks.Set(ctx, []byte("k"), nil),
		"Delete":           rks.Delete(ctx, []byte("cowl")),
		"DeleteRange":      rks.DeleteRange(ctx, nil, nil),
		"legacy Set":       rlr.Set(ctx, []byte("joker:2"), nil),
		"legacy Delete":    rlr.Delete(ctx, []byte("joker:1")),
		"legacy Barrier":   rlr.Barrier(ctx),
		"batch Set":        rks.NewBatch().Set([]byte("k"), nil),
		"sequence refill":  func() error { _, err := rks.Sequence("ids").Next(ctx); return err }(),
		"legacy batch Set": rlr.NewBatch().Set([]byte("joker:3"), nil),
	}
	for name, err := range writes {
		if !errors.Is(err, ErrReadOnly) {
			t.Errorf("%s = %v, want ErrReadOnly", name, err)
		}
	}
	if err := ro.Close(); err != nil {
		t.Fatal(err)
	}
	if after := rawKeys(t, src.fs, other.path, ""); !slices.Equal(before, after) {
		t.Fatalf("read-only open changed the store: %v -> %v", before, after)
	}
}

// TestLegacyDeleteRangeStaysInClaims: a legacy DeleteRange is allowed only
// when every byte of [start, end) lies in the owner's claims. A range that
// runs into another owner's claim, into keys no claim holds, or over a
// reserved root is refused with ErrOutsideKeyspace and deletes nothing, in
// strict mode and under the shadow dial alike; a range that is exactly an
// owned interval is allowed.
func TestLegacyDeleteRangeStaysInClaims(t *testing.T) {
	ctx := context.Background()
	for _, shadow := range []bool{false, true} {
		t.Run(fmt.Sprintf("shadow=%v", shadow), func(t *testing.T) {
			e := newEnv(t,
				OwnerSpec{Owner: "gotham", Enabled: true, Spec: Spec{Claims: []Claim{
					{Prefix: "joker:", Mode: ClaimReadWrite, Except: []string{"joker:harley:"}},
					{Prefix: "bane:", Mode: ClaimReadWrite},
				}}},
				OwnerSpec{Owner: "arkham", Enabled: true, Spec: Spec{Claims: []Claim{
					{Prefix: "joker:harley:", Mode: ClaimReadWrite},
				}}},
				OwnerSpec{Owner: "wayne", Enabled: true, Spec: wayneSpec()},
			)
			e.opts.ClaimsShadow = shadow
			e.seedRaw("joker:1", "a", "joker:harley:1", "b", "joker:zed", "z", "jokes", "gap", "bane:1", "c")
			db := e.open()
			ks, _ := db.Keyspace("wayne")
			if err := ks.Set(ctx, []byte("cowl"), []byte("black")); err != nil {
				t.Fatal(err)
			}
			g, _ := db.Legacy("gotham")
			for name, r := range map[string][2]string{
				"into a foreign claim": {"joker:1", "joker:i"},
				"into a gap":           {"joker:zed", "jokes~"},
				"over a reserved root": {"bane:", "n"},
				"over the core root":   {"bane:", "c1"},
			} {
				if err := g.DeleteRange(ctx, []byte(r[0]), []byte(r[1])); !errors.Is(err, ErrOutsideKeyspace) {
					t.Errorf("%s: DeleteRange(%q, %q) = %v, want ErrOutsideKeyspace", name, r[0], r[1], err)
				}
			}
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			got := rawKeys(t, e.fs, e.path, "")
			for _, k := range []string{"joker:1", "joker:harley:1", "joker:zed", "jokes", "bane:1", "m/wayne/cowl"} {
				if !slices.Contains(got, k) {
					t.Errorf("a refused DeleteRange removed %q", k)
				}
			}
			db = e.open()
			g, _ = db.Legacy("gotham")
			if err := g.DeleteRange(ctx, []byte("bane:"), []byte("bane;")); err != nil {
				t.Fatalf("DeleteRange of an owned interval: %v", err)
			}
			if _, err := g.Get(ctx, []byte("bane:1")); !errors.Is(err, ErrNotFound) {
				t.Fatalf("owned interval not deleted: %v", err)
			}
		})
	}
}

// TestScanLimitErrorAndStop covers yieldScan, the one loop under keyspace
// and snapshot scans: Limit in both directions; an iteration error (the ctx
// cancelled mid-scan) yielded once, as the last pair; and a consumer that
// stops early is never yielded to again (range-over-func panics if it is).
func TestScanLimitErrorAndStop(t *testing.T) {
	ctx := t.Context()
	e := newEnv(t, OwnerSpec{Owner: "robin", Spec: Spec{Keyspace: true}, Enabled: true})
	db := e.open()
	ks, _ := db.Keyspace("robin")
	for _, k := range []string{"a", "b", "c", "d"} {
		if err := ks.Set(ctx, []byte(k), []byte("nightwing")); err != nil {
			t.Fatal(err)
		}
	}
	snap, err := ks.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := snap.Close(); err != nil {
			t.Error(err)
		}
	}()
	scans := map[string]func(context.Context, ScanOptions) iter.Seq2[KV, error]{"keyspace": ks.Scan, "snapshot": snap.Scan}
	for name, scan := range scans {
		t.Run(name, func(t *testing.T) {
			collect := func(ctx context.Context, o ScanOptions, after func()) (keys []string, errs []error) {
				for kv, err := range scan(ctx, o) {
					if err != nil {
						errs = append(errs, err)
						continue
					}
					keys = append(keys, string(kv.Key))
					after()
				}
				return keys, errs
			}
			for _, tc := range []struct {
				opts ScanOptions
				want []string
			}{
				{ScanOptions{Limit: 2}, []string{"a", "b"}},
				{ScanOptions{Limit: 2, Reverse: true}, []string{"d", "c"}},
				{ScanOptions{Limit: 9}, []string{"a", "b", "c", "d"}},
			} {
				if got, errs := collect(ctx, tc.opts, func() {}); !slices.Equal(got, tc.want) || len(errs) != 0 {
					t.Errorf("scan %+v = %v, %v; want %v and no error", tc.opts, got, errs, tc.want)
				}
			}
			cctx, cancel := context.WithCancel(ctx)
			got, errs := collect(cctx, ScanOptions{}, cancel)
			cancel()
			if !slices.Equal(got, []string{"a"}) || len(errs) != 1 || !errors.Is(errs[0], context.Canceled) {
				t.Errorf("scan cancelled after the first key = %v, %v; want [a] then one context.Canceled", got, errs)
			}
			n := 0
			for _, err := range scan(ctx, ScanOptions{}) {
				if err != nil {
					t.Fatal(err)
				}
				n++
				break
			}
			if n != 1 {
				t.Errorf("a consumer that broke after one pair saw %d", n)
			}
		})
	}
}

// valueFails is a scanCursor over keys whose value read fails at bad: Value
// returns nil and Err reports the failure, as *Iterator does when Pebble's
// ValueAndErr fails (a blob file read, for one).
type valueFails struct {
	keys []string
	bad  string
	i    int
	err  error
}

var errMadHatter = errors.New("mad hatter: blob read failed")

func (c *valueFails) First() bool { c.i = 0; return len(c.keys) > 0 }
func (c *valueFails) Last() bool  { c.i = len(c.keys) - 1; return len(c.keys) > 0 }
func (c *valueFails) Next() bool  { c.i++; return c.i < len(c.keys) }
func (c *valueFails) Prev() bool  { c.i--; return c.i >= 0 }
func (c *valueFails) Key() []byte { return []byte(c.keys[c.i]) }
func (c *valueFails) Value() []byte {
	if c.keys[c.i] == c.bad {
		c.err = errMadHatter
		return nil
	}
	return []byte("value of " + c.keys[c.i])
}
func (c *valueFails) Err() error   { return c.err }
func (c *valueFails) Close() error { return nil }

// TestScanValueErrorYieldsNoNilValue (review MINOR-2): when a value read
// fails mid-scan, the scan yields the error, once, and never the key with a
// nil value that looks like a real (empty) row.
func TestScanValueErrorYieldsNoNilValue(t *testing.T) {
	for _, reverse := range []bool{false, true} {
		var kvs []KV
		var errs []error
		cur := &valueFails{keys: []string{"alfred", "bane", "catwoman"}, bad: "bane"}
		yieldScan(cur, ScanOptions{Reverse: reverse}, slog.New(slog.DiscardHandler), func(kv KV, err error) bool {
			if err != nil {
				errs = append(errs, err)
				return true
			}
			kvs = append(kvs, kv.Clone())
			return true
		})
		want := "alfred"
		if reverse {
			want = "catwoman"
		}
		if len(kvs) != 1 || string(kvs[0].Key) != want || kvs[0].Value == nil {
			t.Errorf("reverse=%v: pairs = %v, want only %s with its value", reverse, kvs, want)
		}
		if len(errs) != 1 || !errors.Is(errs[0], errMadHatter) {
			t.Errorf("reverse=%v: errors = %v, want the value read's error once", reverse, errs)
		}
	}
}

// TestClaimPrefixShape (05-storage §5S.3.4 Claim.Prefix; review MINOR-4): a
// claim prefix ends in a separator byte, so "joker" cannot quietly claim
// "jokerized:"; a prefix of only 0xff bytes has no PrefixEnd, so its span
// would be broken. Both fail boot, every problem listed. An exact Key needs
// no separator.
func TestClaimPrefixShape(t *testing.T) {
	e := newEnv(t, OwnerSpec{Owner: "gotham", Enabled: true, Spec: Spec{Claims: []Claim{
		{Prefix: "joker", Mode: ClaimReadWrite},
		{Prefix: "\xff\xff", Mode: ClaimReadOnly},
		{Prefix: "arkham-", Mode: ClaimReadWrite},
	}}})
	_, err := e.tryOpen()
	if err == nil {
		t.Fatal("bare and all-0xff claim prefixes were accepted")
	}
	for _, want := range []string{`"joker"`, `"\xff\xff"`, `"arkham-"`} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal %q does not name %s", err, want)
		}
	}
	for _, sep := range []string{":", "/", "_", "."} {
		ok := newEnv(t, OwnerSpec{Owner: "gotham", Enabled: true, Spec: Spec{Claims: []Claim{
			{Prefix: "joker" + sep, Mode: ClaimReadWrite},
			{Key: "joker_counter", Mode: ClaimReadWrite, Serialized: true},
			{Key: "penguin", Mode: ClaimReadWrite},
		}}})
		if _, err := ok.tryOpen(); err != nil {
			t.Errorf("prefix %q with a separator: %v", "joker"+sep, err)
		}
	}
}
