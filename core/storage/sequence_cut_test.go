// file: core/storage/sequence_cut_test.go
// version: 0.1.0
// guid: 3b7e51c4-92d8-4a60-b1f3-5c0e8a7d2916
// last-edited: 2026-11-22

package storage

import (
	"context"
	"testing"

	"github.com/cockroachdb/pebble/v2/vfs"
)

// TestSequenceCut (05-backup §5B.3.5 step 4): after Cut returns B, every ID
// Next hands out is greater than B, and every ID handed out before it is at
// most B, though the live lease still held unissued IDs below the stored
// high-water mark. A boundary read from the stored mark alone would put the
// next ID of that lease below it.
func TestSequenceCut(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t, OwnerSpec{Owner: "wayne", Spec: wayneSpec(), Enabled: true})
	e.opts.SequenceLease = 8
	db := e.open()
	ks, _ := db.Keyspace("wayne")
	seq := ks.Sequence("ids")

	// A fresh sequence has issued nothing: boundary 0, and the first ID is above it.
	b0, err := seq.Cut(ctx)
	if err != nil || b0 != 0 {
		t.Fatalf("Cut of a fresh sequence = %d, %v; want 0", b0, err)
	}

	var before []uint64
	for range 3 { // a live lease with unissued IDs left in it
		id, err := seq.Next(ctx)
		if err != nil {
			t.Fatal(err)
		}
		before = append(before, id)
	}
	b, err := seq.Cut(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range before {
		if id > b {
			t.Fatalf("id %d was issued before Cut but is above its boundary %d", id, b)
		}
	}
	for range 40 { // spans several lease refills
		id, err := seq.Next(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if id <= b {
			t.Fatalf("id %d was issued after Cut but is not above its boundary %d (the stored mark was %d)", id, b, b+1)
		}
	}
}

// TestSequenceCutSurvivesACrash: the IDs a restarted process hands out are
// above the boundary a Cut returned, because the cut is a durable commit.
func TestSequenceCutSurvivesACrash(t *testing.T) {
	ctx := context.Background()
	fs := vfs.NewCrashableMem()
	owners := []OwnerSpec{{Owner: "wayne", Spec: wayneSpec(), Enabled: true}}
	e := newEnvFS(t, fs, owners...)
	e.opts.SequenceLease = 8
	db := e.open()
	ks, _ := db.Keyspace("wayne")
	seq := ks.Sequence("ids")
	if _, err := seq.Next(ctx); err != nil {
		t.Fatal(err)
	}
	b, err := seq.Cut(ctx)
	if err != nil {
		t.Fatal(err)
	}
	after := newEnvFS(t, fs.CrashClone(vfs.CrashCloneCfg{}), owners...)
	after.path, after.opts.Path = e.path, e.path
	db2 := after.open()
	ks2, _ := db2.Keyspace("wayne")
	if id, err := ks2.Sequence("ids").Next(ctx); err != nil || id <= b {
		t.Fatalf("id after a crash = %d, %v; want above the boundary %d", id, err, b)
	}
}

// TestSequenceCutRacesNext: a Next that loaded the lease before Cut swapped it
// takes no ID from it; the seam runs Cut exactly between Next's load and its
// Add, as the AdvancePast race test does.
func TestSequenceCutRacesNext(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t, OwnerSpec{Owner: "wayne", Spec: wayneSpec(), Enabled: true})
	e.opts.SequenceLease = 8
	var (
		seq   *Sequence
		armed bool
		cut   uint64
	)
	e.opts.hooks.seqLoaded = func() {
		if armed {
			armed = false
			var err error
			if cut, err = seq.Cut(ctx); err != nil {
				t.Error(err)
			}
		}
	}
	db := e.open()
	ks, _ := db.Keyspace("wayne")
	seq = ks.Sequence("ids")
	if _, err := seq.Next(ctx); err != nil {
		t.Fatal(err)
	}
	armed = true
	id, err := seq.Next(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if id <= cut {
		t.Fatalf("id %d taken from the lease Cut ended (boundary %d)", id, cut)
	}
}
