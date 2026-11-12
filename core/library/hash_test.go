// file: core/library/hash_test.go
// version: 1.1.0
// guid: efbd1161-5225-460f-a129-0d983b1cf9f2
// last-edited: 2026-11-11

package library

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/internal/libfs"
)

func TestHashSameDescriptorSizeAndDigest(t *testing.T) {
	r, _ := transferRig(t, rigOpts{})
	// Three chunks and a bit, so the digest spans reads.
	data := bytes.Repeat([]byte("the Bat-Signal over Gotham. "), (3*libfs.ChunkSize+777)/27+1)
	p := r.put("books-library", "Gotham/long.m4b", string(data))
	d, err := r.m.Hash(ctxbg(), p)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	if d.SHA256 != hex.EncodeToString(sum[:]) || d.Size != int64(len(data)) {
		t.Errorf("Hash = %+v, want %x and %d bytes", d, sum, len(data))
	}
	if d2, err := r.m.Hash(ctxbg(), r.put("books-library", "empty.m4b", "")); err != nil || d2.Size != 0 || d2.SHA256 != hex.EncodeToString(func() []byte { s := sha256.Sum256(nil); return s[:] }()) {
		t.Errorf("Hash of an empty file = %+v, %v", d2, err)
	}
	// The hashed bytes are counted per root.
	r.m.ctr.mu.Lock()
	got := r.m.ctr.hashBytes["books-library"]
	r.m.ctr.mu.Unlock()
	if got != uint64(len(data)) {
		t.Errorf("hash bytes counted = %d, want %d", got, len(data))
	}
}

func TestHashRefusals(t *testing.T) {
	r, _ := transferRig(t, rigOpts{})
	r.put("books-library", "Gotham/a.m4b", "bytes")
	if _, err := r.m.Hash(ctxbg(), Path{Root: "books-library"}); !errors.Is(err, ErrBadTransfer) {
		t.Errorf("Hash of a root = %v, want ErrBadTransfer", err)
	}
	if _, err := r.m.Hash(ctxbg(), Path{Root: "books-library", Rel: "Gotham"}); !errors.Is(err, ErrNotSupported) {
		t.Errorf("Hash of a directory = %v, want ErrNotSupported", err)
	}
	if _, err := r.m.Hash(ctxbg(), Path{Root: "nope", Rel: "a"}); !errors.Is(err, ErrUnknownRoot) {
		t.Errorf("Hash in an unknown root = %v, want ErrUnknownRoot", err)
	}
	if _, err := r.m.Hash(ctxbg(), Path{Root: "books-library", Rel: "missing"}); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("Hash of a missing file = %v, want not-exist", err)
	}
	if _, err := r.m.Hash(ctxbg(), Path{Root: "books-library", Rel: "../escape"}); err == nil {
		t.Error("Hash of an escaping path succeeded")
	}
}

// Hashing stops at a chunk boundary when its context ends, and the shared rate
// limit makes every reader wait for its share of the bytes.
func TestHashThrottleAndCancel(t *testing.T) {
	var slept []time.Duration
	base := time.Unix(1_700_000_000, 0)
	var cancel context.CancelFunc
	r, _ := transferRig(t, rigOpts{seams: seams{
		now: func() time.Time { return base },
		sleep: func(ctx context.Context, d time.Duration) error {
			slept = append(slept, d)
			if cancel != nil && len(slept) == 2 {
				cancel()
				return ctx.Err()
			}
			return nil
		},
	}})
	p := r.put("books-library", "big.m4b", string(bytes.Repeat([]byte{'x'}, 3*libfs.ChunkSize)))
	set := defaultSettings()
	set.roots = r.specs
	set.hashRate = libfs.ChunkSize // one chunk per second
	if err := r.m.apply(ctxbg(), set); err != nil {
		t.Fatal(err)
	}
	// A frozen clock: the nth chunk waits n-1 seconds for the bytes before it.
	if _, err := r.m.Hash(ctxbg(), p); err != nil {
		t.Fatal(err)
	}
	want := []time.Duration{time.Second, 2 * time.Second}
	if len(slept) != len(want) || slept[0] != want[0] || slept[1] != want[1] {
		t.Errorf("waits = %v, want %v", slept, want)
	}
	// Cancelled while waiting: the hash stops with the context's error.
	slept = nil
	r.m.lim.mu.Lock()
	r.m.lim.next = time.Time{}
	r.m.lim.mu.Unlock()
	ctx, c := context.WithCancel(ctxbg())
	cancel = c
	if _, err := r.m.Hash(ctx, p); !errors.Is(err, context.Canceled) {
		t.Errorf("Hash cancelled mid-way = %v, want context.Canceled", err)
	}
	// Unlimited (0) never waits.
	set.hashRate = 0
	if err := r.m.apply(ctxbg(), set); err != nil {
		t.Fatal(err)
	}
	slept, cancel = nil, nil
	if _, err := r.m.Hash(ctxbg(), p); err != nil || len(slept) != 0 {
		t.Errorf("unlimited Hash = %v, waited %v", err, slept)
	}
	// An already-cancelled context stops before the first chunk.
	dead, stop := context.WithCancel(ctxbg())
	stop()
	if _, err := r.m.Hash(dead, p); !errors.Is(err, context.Canceled) {
		t.Errorf("Hash with a dead context = %v, want context.Canceled", err)
	}
}

// Hash takes one of a fixed number of slots (the CPU count): callers fanning out
// over a library queue behind the readers already running, and a caller whose
// context ends while queued gives up without reading a byte.
func TestHashIsBoundedByTheHashSlots(t *testing.T) {
	r, _ := transferRig(t, rigOpts{})
	p := r.put("books-library", "Gotham/01.m4b", "the bytes of chapter one")
	slots := r.m.hashSlots
	for range cap(slots.ch) {
		if err := slots.acquire(ctxbg()); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithTimeout(ctxbg(), 100*time.Millisecond)
	defer cancel()
	if _, err := r.m.Hash(ctx, p); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Hash with every slot taken = %v, want the context's deadline", err)
	}
	slots.release()
	d, err := r.m.Hash(ctxbg(), p)
	if err != nil || d.SHA256 != sum("the bytes of chapter one") {
		t.Fatalf("Hash with a slot free = %+v, %v", d, err)
	}
	if len(slots.ch) != cap(slots.ch)-1 {
		t.Errorf("Hash left %d slots taken, want %d (the ones this test holds)", len(slots.ch), cap(slots.ch)-1)
	}
}
