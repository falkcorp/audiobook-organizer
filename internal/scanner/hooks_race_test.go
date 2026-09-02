// file: internal/scanner/hooks_race_test.go
// version: 1.0.1
// guid: 9c4e1a70-6b83-4d52-bf19-0a7c35d8e264
// last-edited: 2026-09-02

// Pins the scanHooks global as safe for concurrent use.
//
// The shape this reproduces is the one CI hit in
// TestAddImportPathFallbackScan on 2026-08-18: a test's deferred cleanup
// calls SetScanHooks(nil) while a scan started by an async operation is
// still running and reading the hooks from saveBookToDatabase. Those two
// goroutines genuinely overlap -- the operations registry runs the scan in
// the background -- so the write and the read race.
//
// This test only means anything under -race. Without the RWMutex in
// hooks.go it reports a DATA RACE between SetScanHooks and
// currentScanHooks; with it, it is clean. Verified both ways.

package scanner

import (
	"sync"
	"testing"
)

type noopScanHooks struct{}

func (noopScanHooks) OnBookScanned(string, string) {}
func (noopScanHooks) OnImportDedup(string)         {}

func TestScanHooksConcurrentSetAndRead(t *testing.T) {
	t.Cleanup(func() { SetScanHooks(nil) })

	const workers = 8
	const iterations = 500

	var wg sync.WaitGroup
	for range workers {
		wg.Add(2)
		// Writers: the teardown path, installing and clearing hooks.
		go func() {
			defer wg.Done()
			for j := range iterations {
				if j%2 == 0 {
					SetScanHooks(noopScanHooks{})
				} else {
					SetScanHooks(nil)
				}
			}
		}()
		// Readers: the scan worker path. Read through currentScanHooks and
		// use the returned value, exactly as saveBookToDatabase does -- a nil
		// check followed by a second read of the global would be a TOCTOU
		// even with the lock.
		go func() {
			defer wg.Done()
			for range iterations {
				if hooks := currentScanHooks(); hooks != nil {
					hooks.OnBookScanned("book-1", "Title")
					hooks.OnImportDedup("book-1")
				}
			}
		}()
	}
	wg.Wait()
}
