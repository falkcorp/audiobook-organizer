// file: internal/database/pebble_gate_bench_test.go
// version: 1.0.0
// guid: e4a96c15-7d20-4b83-a1f5-83c0d9b27e64
// last-edited: 2026-10-11

package database

import (
	"fmt"
	"testing"

	"github.com/cockroachdb/pebble/v2"
	"github.com/cockroachdb/pebble/v2/vfs"
)

// The gate's cost on the read hot path: the same Get / NewIter+First+Close
// against the raw engine and through the gate, serial and parallel. Run with
//
//	go test -run '^$' -bench 'BenchmarkGate' -benchmem -cpu 1,10 ./internal/database/
func benchGate(b *testing.B) (*pebble.DB, *GatedDB) {
	b.Helper()
	db, err := pebble.Open("db", &pebble.Options{FS: vfs.NewMem()})
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = db.Close() })
	for i := range 256 {
		if err := db.Set([]byte(fmt.Sprintf("bench:%04d", i)), []byte("value"), pebble.NoSync); err != nil {
			b.Fatal(err)
		}
	}
	return db, NewGatedDB(db)
}

var benchKey = []byte("bench:0128")

func BenchmarkGateGet(b *testing.B) {
	db, g := benchGate(b)
	b.Run("raw", func(b *testing.B) {
		b.ReportAllocs()
		b.RunParallel(func(pb *testing.PB) {
			for pb.Next() {
				_, c, err := db.Get(benchKey)
				if err != nil {
					b.Fatal(err)
				}
				_ = c.Close()
			}
		})
	})
	b.Run("gated", func(b *testing.B) {
		b.ReportAllocs()
		b.RunParallel(func(pb *testing.PB) {
			for pb.Next() {
				_, c, err := g.Get(benchKey)
				if err != nil {
					b.Fatal(err)
				}
				_ = c.Close()
			}
		})
	})
}

func BenchmarkGateIter(b *testing.B) {
	db, g := benchGate(b)
	opts := &pebble.IterOptions{LowerBound: []byte("bench:"), UpperBound: []byte("bench;")}
	b.Run("raw", func(b *testing.B) {
		b.ReportAllocs()
		b.RunParallel(func(pb *testing.PB) {
			for pb.Next() {
				it, err := db.NewIter(opts)
				if err != nil {
					b.Fatal(err)
				}
				it.First()
				_ = it.Close()
			}
		})
	})
	b.Run("gated", func(b *testing.B) {
		b.ReportAllocs()
		b.RunParallel(func(pb *testing.PB) {
			for pb.Next() {
				it, err := g.NewIter(opts)
				if err != nil {
					b.Fatal(err)
				}
				it.First()
				_ = it.Close()
			}
		})
	})
}
