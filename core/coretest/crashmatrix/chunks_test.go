// file: core/coretest/crashmatrix/chunks_test.go
// version: 1.0.0
// guid: 4499d593-7cb3-408c-bfa0-5a58852c8320
// last-edited: 2026-11-10

package crashmatrix

import (
	"context"
	"fmt"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/cockroachdb/pebble/v2/vfs"

	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/tasks"
)

// smallCutover is a SyntheticCutover small enough for -short: 240 rows in
// chunks of 30 over two shards.
func smallCutover() SyntheticCutover {
	return SyntheticCutover{Owner: "gotham", Rows: 240, ChunkItems: 30, Parallelism: tasks.Fixed(2, "test: two shards")}
}

// TestRunChunksBreaksEveryChunk is the harness's self-test: RunChunks
// clones at the labelled writers, at every chunk's first item and inside
// each chunk, and ObservedChunks reports the three writers' labels.
func TestRunChunksBreaksEveryChunk(t *testing.T) {
	c := smallCutover()
	s := c.Scenario()
	s.Inside = true
	rep := RunChunks(t, s)
	if rep.Kills == 0 || rep.ChunkStarts < c.Rows/c.ChunkItems || rep.Inside == 0 {
		t.Fatalf("report = %+v, want kill points, at least %d chunk starts and interiors", rep, c.Rows/c.ChunkItems)
	}
	observed := ObservedChunks(t, c.Scenario())
	for _, l := range DataMigrationLabels() {
		if !observed[l] || !rep.Observed[l] {
			t.Errorf("label %s not observed (census %v, run %v)", l, observed[l], rep.Observed[l])
		}
	}
}

// recorder is a testing.TB that records Errorf and stops at Fatalf, so a
// test can assert that an oracle fails a broken store.
type recorder struct {
	testing.TB
	mu   sync.Mutex
	errs []string
}

func (r *recorder) Helper() {}

func (r *recorder) Errorf(format string, args ...any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.errs = append(r.errs, fmt.Sprintf(format, args...))
}

func (r *recorder) Fatalf(format string, args ...any) {
	r.Errorf(format, args...)
	runtime.Goexit()
}

func (r *recorder) Fatal(args ...any) { r.Fatalf("%s", fmt.Sprint(args...)) }

// check runs c.Check on fs against a recorder and returns what it reported.
func check(c SyntheticCutover, fs vfs.FS) []string {
	r := &recorder{}
	done := make(chan struct{})
	go func() {
		defer close(done)
		c.Check(r, fs)
	}()
	<-done
	return r.errs
}

// TestSyntheticCheckFailsALostOrDoubledRow: the harness's oracle passes a
// clean cutover and fails a store with a row lost (its import gone) or
// doubled (imported and quarantined), so a cell that passes proves the
// commit unit.
func TestSyntheticCheckFailsALostOrDoubledRow(t *testing.T) {
	c := smallCutover()
	mem := vfs.NewCrashableMem()
	mem.TestingSetDiskUsage(diskUsage())
	c.Seed(t, mem)
	ctx := context.Background()
	if err := c.Boot(ctx, mem); err != nil {
		t.Fatal(err)
	}
	if errs := check(c, mem); len(errs) != 0 {
		t.Fatalf("the oracle failed a clean cutover: %q", errs)
	}
	o, err := storage.Open(ctx, storage.OpenOptions{Path: SyntheticPath, FS: storage.FSOf(mem), Owners: c.owners(storage.ClaimRetired),
		Build: "batcomputer-2", FreeSpaceMargin: 1, Clock: Now})
	if err != nil {
		t.Fatal(err)
	}
	db, err := o.Prepare(ctx)
	if err == nil {
		err = db.Migrate(ctx)
	}
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	ks, _ := db.Keyspace(c.Owner)
	sks, _ := db.Keyspace("core/storage")
	_, six := SyntheticRow(6)
	if err := ks.Delete(ctx, []byte("case/00005")); err != nil {
		t.Fatal(err)
	}
	if err := sks.Set(ctx, quarantineKey(SyntheticID(c.Owner), []byte("case:00006")), six); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	errs := check(c, mem)
	for _, want := range []string{"row 5 lost", "row 6 both imported and quarantined"} {
		if !slices.ContainsFunc(errs, func(e string) bool { return strings.Contains(e, want) }) {
			t.Errorf("the oracle's reports lack %q: %q", want, errs)
		}
	}
}
