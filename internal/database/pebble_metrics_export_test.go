// file: internal/database/pebble_metrics_export_test.go
// version: 1.1.0
// guid: 7f67b147-94c7-439f-8b61-2ea3da025abe
// last-edited: 2026-10-04

package database

import (
	"fmt"
	"sync"
	"testing"
)

func TestPebbleSampleFromDB_RealStore(t *testing.T) {
	p, err := NewPebbleStoreInMemory(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	for i := 0; i < 1000; i++ {
		if err := p.db.Set([]byte(fmt.Sprintf("a1test:%06d", i)), []byte("value-value-value"), nil); err != nil {
			t.Fatal(err)
		}
	}
	if err := p.db.Flush(); err != nil {
		t.Fatal(err)
	}
	s, ok := PebbleSampleFromDB(p.db)
	if !ok {
		t.Fatal("live store sampled as not ok")
	}
	if s.DiskUsageBytes <= 0 {
		t.Errorf("DiskUsageBytes = %v", s.DiskUsageBytes)
	}
	if s.MemTables < 1 {
		t.Errorf("MemTables = %v", s.MemTables)
	}
	var files float64
	for _, f := range s.LevelFiles {
		files += f
	}
	if files < 1 {
		t.Errorf("sum of LevelFiles = %v", files)
	}
	if s.LevelBytesFlushed[0] <= 0 || s.LevelBytesIn[0] <= 0 {
		t.Errorf("L0 flushed=%v in=%v", s.LevelBytesFlushed[0], s.LevelBytesIn[0])
	}
	if via, ok := p.PebbleMetricsSample(); !ok || via.DiskUsageBytes <= 0 {
		t.Errorf("PebbleMetricsSample = %v ok=%v", via.DiskUsageBytes, ok)
	}
}

func TestPebbleSampleFromDB_ClosedStore(t *testing.T) {
	p, err := NewPebbleStoreInMemory(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
	if _, ok := p.PebbleMetricsSample(); ok {
		t.Fatal("closed store sampled as ok")
	}
}

func TestPebbleSampleFromDB_Nil(t *testing.T) {
	if _, ok := PebbleSampleFromDB(nil); ok {
		t.Fatal("nil db sampled as ok")
	}
}

// Sampling while the store closes must never panic, and must never make Close
// fail (an earlier NewSnapshot-based closed probe could leave a snapshot open
// and Close then reported "leaked snapshots"). Once Close has returned the
// sample is not-ok. Run under -race.
func TestPebbleSampleFromDB_ConcurrentClose(t *testing.T) {
	for round := 0; round < 20; round++ {
		p, err := NewPebbleStoreInMemory(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		stop := make(chan struct{})
		var wg sync.WaitGroup
		panics := make(chan any, 4)
		for g := 0; g < 3; g++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				defer func() {
					if r := recover(); r != nil {
						panics <- r
					}
				}()
				for {
					select {
					case <-stop:
						return
					default:
						p.PebbleMetricsSample()
					}
				}
			}()
		}
		if err := p.Close(); err != nil {
			t.Fatal(err)
		}
		if _, ok := p.PebbleMetricsSample(); ok {
			t.Error("sample after Close returned ok")
		}
		close(stop)
		wg.Wait()
		close(panics)
		for r := range panics {
			t.Fatalf("sampler panicked while the store closed: %v", r)
		}
	}
}
