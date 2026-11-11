// file: core/internal/storagemigrate/cutover_test.go
// version: 1.0.1
// guid: 13f484bd-b5ea-4a6c-9a4b-5f5c3b2727e3
// last-edited: 2026-11-10

package storagemigrate_test

import (
	"context"
	"errors"
	"testing"

	"github.com/cockroachdb/pebble/v2/vfs"

	"github.com/falkcorp/mediacore/coretest/crashmatrix"
	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/storage/datamigrate"
	"github.com/falkcorp/mediacore/tasks"
)

// gothamCutover is the harness's synthetic Cutover-shaped migration over
// the module gotham: a few thousand case files under a retired claim,
// four disjoint shards (05-storage §5S.3.13; F2-06's brief).
func gothamCutover() crashmatrix.SyntheticCutover {
	return crashmatrix.SyntheticCutover{Owner: "gotham", Spec: storage.Spec{Keyspace: true, Migrations: []storage.Migration{seed()}},
		Parallelism: tasks.Fixed(4, "test: four disjoint shards of the fixture, so every shard's chunks are broken")}
}

// TestCutoverCrashAtEveryChunk is the chunk-boundary crash matrix over the
// synthetic cutover: a kill before every durable operation of the plan,
// every chunk commit of every shard and every state record, and a point
// inside every chunk, each rebooted to completion with no row lost or
// doubled (05-storage §5S.3.13 test list).
func TestCutoverCrashAtEveryChunk(t *testing.T) {
	s := gothamCutover().Scenario()
	s.Inside = true
	rep := crashmatrix.RunChunks(t, s)
	// Four shards over 2 400 rows in chunks of 150: 16 chunks at least,
	// each broken at its boundary and inside.
	if rep.ChunkStarts < 16 || rep.Inside < 16 || rep.Kills == 0 {
		t.Fatalf("broke %d kill points, %d chunk boundaries and %d chunk interiors; want every chunk's (>= 16)", rep.Kills, rep.ChunkStarts, rep.Inside)
	}
}

// TestStepWriter_QuarantineInChunkBatch: the quarantine record and the
// source row's delete commit together with the chunk or not at all (a kill
// at each chunk commit and inside each chunk; Check finds every row exactly
// once), and with no quarantine writer the step fails and the row stays.
func TestStepWriter_QuarantineInChunkBatch(t *testing.T) {
	t.Run("crash at the chunk commit", func(t *testing.T) {
		c := gothamCutover()
		c.Rows = 1200
		s := c.Scenario()
		s.Labels, s.Inside = []string{crashmatrix.DataMigrationLabels()[1]}, true
		crashmatrix.RunChunks(t, s)
	})
	t.Run("no writer", func(t *testing.T) {
		c := gothamCutover()
		c.NoQuarantineWriter, c.Rows, c.Parallelism = true, 300, sequential()
		mem := vfs.NewMem()
		mem.TestingSetDiskUsage(plenty())
		c.Seed(t, mem)
		err := c.Boot(context.Background(), mem)
		var se *storage.StepError
		if !errors.Is(err, datamigrate.ErrQuarantineNotWired) || !errors.As(err, &se) || se.Step.DataMigration != crashmatrix.SyntheticID("gotham") {
			t.Fatalf("boot = %v, want the step's ErrQuarantineNotWired", err)
		}
		db := readOnly(t, mem, c)
		lr, _ := db.Legacy("gotham")
		k, v := crashmatrix.SyntheticRow(97)
		if got, err := lr.Get(context.Background(), k); err != nil || string(got) != string(v) {
			t.Fatalf("row 97 after the refused quarantine = %q, %v; want it untouched", got, err)
		}
		sks, _ := db.Keyspace("core/storage")
		n := 0
		for _, err := range sks.Scan(context.Background(), storage.ScanOptions{Start: []byte("quarantine/"), End: []byte("quarantine0")}) {
			if err != nil {
				t.Fatal(err)
			}
			n++
		}
		if n != 0 {
			t.Fatalf("%d quarantine records without a writer", n)
		}
	})
}
