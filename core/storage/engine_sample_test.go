// file: core/storage/engine_sample_test.go
// version: 1.4.0
// guid: 8e2f4a71-3c6b-4d90-a5e1-7b0d9c2f4a68
// last-edited: 2026-11-09

package storage

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"strings"
	"testing"
)

// TestAdminFlushEmptiesTheWAL pins what ABO's census reads: an unflushed
// write shows in EngineSample.WALBytes, and Flush moves it into sstables.
func TestAdminFlushEmptiesTheWAL(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t, OwnerSpec{Owner: "gotham", Enabled: true, Spec: Spec{Claims: gothamClaims()}})
	db := e.open()
	g, _ := db.Legacy("gotham")
	if err := g.Set(ctx, []byte("joker:1"), []byte("ha")); err != nil {
		t.Fatal(err)
	}
	a := db.Admin()
	before, err := a.EngineSample()
	if err != nil {
		t.Fatal(err)
	}
	if before.WALBytes == 0 {
		t.Fatalf("WALBytes = 0 with an unflushed write")
	}
	if err := a.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	after, err := a.EngineSample()
	if err != nil {
		t.Fatal(err)
	}
	if after.WALBytes != 0 {
		t.Errorf("WALBytes = %v after Flush, want 0", after.WALBytes)
	}
	var files float64
	for _, n := range after.LevelFiles {
		files += n
	}
	if files == 0 {
		t.Errorf("no sstable after Flush: LevelFiles = %v", after.LevelFiles)
	}
	if after.DiskUsageBytes == 0 {
		t.Errorf("DiskUsageBytes = 0 on a store with a table")
	}
}

// TestAdminSampleAndFlushRefuseAClosedStore: a closed store is ErrClosed,
// never a zero sample a collector would serve as a measurement.
func TestAdminSampleAndFlushRefuseAClosedStore(t *testing.T) {
	e := newEnv(t, OwnerSpec{Owner: "wayne", Enabled: true, Spec: wayneSpec()})
	db := e.open()
	a := db.Admin()
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := a.EngineSample(); !errors.Is(err, ErrClosed) {
		t.Errorf("EngineSample on a closed store = %v, want ErrClosed", err)
	}
	if err := a.Flush(context.Background()); !errors.Is(err, ErrClosed) {
		t.Errorf("Flush on a closed store = %v, want ErrClosed", err)
	}
}

// TestShadowViolationsCounted: every out-of-claim access is counted by owner,
// op class and mode (the kernel serves the counts, §5S.8): "shadow" when the
// dial allowed it, "refused" under strict claims.
func TestShadowViolationsCounted(t *testing.T) {
	ctx := context.Background()
	for _, shadow := range []bool{false, true} {
		e := newEnv(t, OwnerSpec{Owner: "gotham", Enabled: true, Spec: Spec{Claims: gothamClaims()}})
		e.opts.ClaimsShadow = shadow
		db := e.open()
		if db.ClaimsShadow() != shadow {
			t.Fatalf("ClaimsShadow() = %v, want %v", db.ClaimsShadow(), shadow)
		}
		g, _ := db.Legacy("gotham")
		err := g.Set(ctx, []byte("bane:1"), []byte("venom"))
		_, gerr := g.Get(ctx, []byte("bane:2"))
		switch {
		case shadow:
			if err != nil {
				t.Errorf("shadow: out-of-claim Set = %v, want allowed", err)
			}
			if !errors.Is(gerr, ErrNotFound) {
				t.Errorf("shadow: out-of-claim Get of an absent key = %v, want ErrNotFound", gerr)
			}
			if n := db.ShadowViolations(); n != 2 {
				t.Errorf("shadow: ShadowViolations() = %d, want 2", n)
			}
			want := map[ClaimViolation]uint64{{"gotham", "write", "shadow"}: 1, {"gotham", "read", "shadow"}: 1}
			if got := db.ClaimViolations(); !maps.Equal(got, want) {
				t.Errorf("shadow: ClaimViolations() = %v, want %v", got, want)
			}
		default:
			if !errors.Is(err, ErrOutsideKeyspace) {
				t.Errorf("strict: out-of-claim Set = %v, want ErrOutsideKeyspace", err)
			}
			if n := db.ShadowViolations(); n != 0 {
				t.Errorf("strict: ShadowViolations() = %d, want 0", n)
			}
			want := map[ClaimViolation]uint64{{"gotham", "write", "refused"}: 1, {"gotham", "read", "refused"}: 1}
			if got := db.ClaimViolations(); !maps.Equal(got, want) {
				t.Errorf("strict: ClaimViolations() = %v, want %v", got, want)
			}
		}
	}
}

// TestShadowWarnRateLimited (R1-23 review S3): the shadow Warn fires on a
// series' 1st, 2nd, 4th, 8th ... access with the count, while the counter
// sees every access and ClaimViolationKeys keeps the first distinct keys.
func TestShadowWarnRateLimited(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t, OwnerSpec{Owner: "gotham", Enabled: true, Spec: Spec{Claims: gothamClaims()}})
	e.opts.ClaimsShadow = true
	var buf bytes.Buffer
	e.opts.Logger = slog.New(slog.NewTextHandler(&buf, nil))
	db := e.open()
	g, _ := db.Legacy("gotham")
	for i := 1; i <= 9; i++ {
		if err := g.Set(ctx, []byte(fmt.Sprintf("bane:%d", i)), []byte("venom")); err != nil {
			t.Fatal(err)
		}
	}
	warns := strings.Count(buf.String(), "allowed by core.storage.claims.mode=shadow")
	if warns != 4 { // accesses 1, 2, 4 and 8
		t.Errorf("shadow Warns = %d over 9 accesses, want 4: %s", warns, buf.String())
	}
	for _, c := range []string{"count=1\n", "count=2\n", "count=4\n", "count=8\n"} {
		if !strings.Contains(buf.String(), c) {
			t.Errorf("no Warn with %s: %s", c, buf.String())
		}
	}
	v := ClaimViolation{Owner: "gotham", Op: "write", Mode: "shadow"}
	if n := db.ClaimViolations()[v]; n != 9 {
		t.Errorf("counted %d accesses, want 9", n)
	}
	// Nine distinct keys of one length in no claim: the first four, told
	// apart by ordinal, one access each.
	want := []string{"unclaimed key len=6 #1 x1", "unclaimed key len=6 #2 x1", "unclaimed key len=6 #3 x1", "unclaimed key len=6 #4 x1"}
	if got := db.ClaimViolationKeys()[v]; !slices.Equal(got, want) {
		t.Errorf("ClaimViolationKeys = %v, want the first %d distinct keys %v", got, maxViolationKeys, want)
	}
	if strings.Contains(buf.String(), "bane") || strings.Contains(buf.String(), "sha256") {
		t.Errorf("a shadow Warn carries a key's bytes or a hash of them (R1-23 review SF1): %s", buf.String())
	}
}

// TestKeyRefCarriesNoKeyBytes (R1-23 review SF1, CodeQL clear-text logging):
// keyRef describes a key by the declared claim that holds it and its length,
// never by any of its bytes or a hash of them, so a session token in a key
// cannot reach a log or an error through storage (R1-23 review SF1; CodeQL
// go/clear-text-logging, go/weak-sensitive-data-hashing). A key in no claim
// is described by its length alone.
func TestKeyRefCarriesNoKeyBytes(t *testing.T) {
	const token = "the-batcave-door-code"
	e := newEnv(t,
		OwnerSpec{Owner: "gotham", Enabled: true, Spec: Spec{Claims: gothamClaims()}},
		OwnerSpec{Owner: "penguin", Enabled: true, Spec: Spec{Claims: []Claim{{Prefix: "umbrella:", Mode: ClaimReadWrite}}}},
	)
	db := e.open()
	cases := []struct{ key, where string }{
		{"joker:" + token, `key under claim "joker:"`},
		{"joker:retired:" + token, `key under claim "joker:retired:"`}, // longest match
		{"joker_counter", `claimed key "joker_counter"`},               // an exact claim's key is its declaration
		{"umbrella:" + token, `key under claim "umbrella:"`},           // another owner's claim
		{"c/" + token, `key under reserved "c/"`},
		{"m/" + token, `key under reserved "m/"`},
		// In no claim: nothing of the key but its length, not even what looks
		// like its family.
		{"sess:" + token, "unclaimed key"},
		{token, "unclaimed key"},
	}
	for _, c := range cases {
		ref := db.keyRef([]byte(c.key))
		if want := fmt.Sprintf("%s len=%d", c.where, len(c.key)); ref != want {
			t.Errorf("keyRef of a %d-byte key = %s, want %s", len(c.key), ref, want)
		}
		if strings.Contains(ref, token[:8]) || strings.Contains(ref, "sha256") {
			t.Errorf("keyRef of a %d-byte key = %s, carries the key's bytes or a hash", len(c.key), ref)
		}
	}
	if ref := db.keyRef([]byte("sess:" + token)); strings.Contains(ref, "sess") {
		t.Errorf("keyRef of sess:<token> = %s, shows part of an unclaimed key", ref)
	}
	// The refusal error describes the key the same way: an unclaimed key and
	// a key in another owner's claim.
	g, _ := db.Legacy("gotham")
	for _, k := range []string{"sess:" + token, "umbrella:" + token} {
		_, err := g.Get(context.Background(), []byte(k))
		if !errors.Is(err, ErrOutsideKeyspace) || strings.Contains(err.Error(), token[:8]) || !strings.Contains(err.Error(), db.keyRef([]byte(k))) {
			t.Errorf("refused Get = %v, want ErrOutsideKeyspace naming %s and none of the key", err, db.keyRef([]byte(k)))
		}
	}
}

// TestNumberShared: an ordinal goes on every description two or more
// entries share, in order, and on no other.
func TestNumberShared(t *testing.T) {
	got := numberShared([]string{"unclaimed key len=6", `key under claim "joker:" len=8`, "unclaimed key len=6", "unclaimed key len=7"})
	want := []string{"unclaimed key len=6 #1", `key under claim "joker:" len=8`, "unclaimed key len=6 #2", "unclaimed key len=7"}
	if !slices.Equal(got, want) {
		t.Errorf("numberShared = %q, want %q", got, want)
	}
}

// TestClaimViolationKeysUnderStrict: a refused access records its key too
// (the series a caller that swallows ErrOutsideKeyspace leaves behind), once
// per distinct key.
func TestClaimViolationKeysUnderStrict(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t, OwnerSpec{Owner: "gotham", Enabled: true, Spec: Spec{Claims: gothamClaims()}})
	db := e.open()
	g, _ := db.Legacy("gotham")
	// penguin:1 three times, then penguin:2 (another key of the same length)
	// and penguin:10 once each: the set is keyed by the key itself, so the
	// two same-length keys stay two entries.
	for _, k := range []string{"penguin:1", "penguin:1", "penguin:1", "penguin:2", "penguin:10"} {
		if _, err := g.Get(ctx, []byte(k)); !errors.Is(err, ErrOutsideKeyspace) {
			t.Fatalf("strict Get = %v", err)
		}
	}
	v := ClaimViolation{Owner: "gotham", Op: "read", Mode: "refused"}
	want := []string{"unclaimed key len=9 #1 x3", "unclaimed key len=9 #2 x1", "unclaimed key len=10 x1"}
	if got := db.ClaimViolationKeys()[v]; !slices.Equal(got, want) {
		t.Errorf("ClaimViolationKeys = %q, want %q", got, want)
	}
	if n := db.ClaimViolations()[v]; n != 5 {
		t.Errorf("counted %d, want 5", n)
	}
	// The admin surface reads the same counters, after Close too.
	a := db.Admin()
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if n := a.ClaimViolations()[v]; n != 5 {
		t.Errorf("Admin.ClaimViolations after Close = %d, want 5", n)
	}
	if got := a.ClaimViolationKeys()[v]; !slices.Equal(got, want) {
		t.Errorf("Admin.ClaimViolationKeys after Close = %q, want %q", got, want)
	}
}
