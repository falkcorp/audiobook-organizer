// file: internal/database/pebble_metrics_export_test.go
// version: 1.0.0
// guid: 7f67b147-94c7-439f-8b61-2ea3da025abe
// last-edited: 2026-10-04

package database

import (
	"fmt"
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
	db := p.db
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
	if _, ok := PebbleSampleFromDB(db); ok {
		t.Fatal("closed store sampled as ok")
	}
}

func TestPebbleSampleFromDB_Nil(t *testing.T) {
	if _, ok := PebbleSampleFromDB(nil); ok {
		t.Fatal("nil db sampled as ok")
	}
}
