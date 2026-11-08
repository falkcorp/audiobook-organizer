// file: core/storage/engine_sample_test.go
// version: 1.1.0
// guid: 8e2f4a71-3c6b-4d90-a5e1-7b0d9c2f4a68
// last-edited: 2026-11-08

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
	want := []string{`"bane:1"`, `"bane:2"`, `"bane:3"`, `"bane:4"`}
	if got := db.ClaimViolationKeys()[v]; !slices.Equal(got, want) {
		t.Errorf("ClaimViolationKeys = %v, want the first %d distinct keys %v", got, maxViolationKeys, want)
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
	for range 3 {
		if _, err := g.Get(ctx, []byte("penguin:1")); !errors.Is(err, ErrOutsideKeyspace) {
			t.Fatalf("strict Get = %v", err)
		}
	}
	v := ClaimViolation{Owner: "gotham", Op: "read", Mode: "refused"}
	if got := db.ClaimViolationKeys()[v]; !slices.Equal(got, []string{`"penguin:1"`}) {
		t.Errorf("ClaimViolationKeys = %v, want one distinct key", got)
	}
	if n := db.ClaimViolations()[v]; n != 3 {
		t.Errorf("counted %d, want 3", n)
	}
	// The admin surface reads the same counters, after Close too.
	a := db.Admin()
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if n := a.ClaimViolations()[v]; n != 3 {
		t.Errorf("Admin.ClaimViolations after Close = %d, want 3", n)
	}
	if got := a.ClaimViolationKeys()[v]; !slices.Equal(got, []string{`"penguin:1"`}) {
		t.Errorf("Admin.ClaimViolationKeys after Close = %v", got)
	}
}
