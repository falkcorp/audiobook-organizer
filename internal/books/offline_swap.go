// file: internal/books/offline_swap.go
// version: 1.0.0
// guid: 5f2a9d14-7e3c-4b08-a6d1-c94e0b37f812
// last-edited: 2026-11-09

package books

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// hnswSnapshotDir and hnswCleanStopName are where the R1-15 follow-up
// (#4506) writes its HNSW clean-stop marker: hnsw.clean-stop in the HNSW
// snapshot directory "hnsw" beside the store, the directory the server
// persists the vector graph to (internal/server's hnswPersistDir;
// database.HNSWSnapshotDir once #4506 merges).
const (
	hnswSnapshotDir   = "hnsw"
	hnswCleanStopName = "hnsw.clean-stop"
)

// removeHNSWCleanStop is ABO's app.Options.R1BeforeSwap (kernel.Config
// BeforeSwap): storage restore-offline and storage reset call it with
// core.storage.path after every check passed and before they stage or move
// anything. The clean-stop marker vouches that the snapshot beside the
// store is the graph of the store's rows; the swap replaces those rows, so
// the boot after it must reconcile the snapshot instead of trusting it
// (seams.d/R1-15.md, the cross-seam row on the P0a swap). The marker is
// removed and the directory synced, so the removal survives a crash; no
// marker is nothing to do. An error stops the command with nothing staged
// or moved. The offline commands run with the service stopped (the kernel
// proved it by the store's lock), so no live HNSW store can write the
// marker again after this.
func removeHNSWCleanStop(dbPath string) error {
	dir := filepath.Join(filepath.Dir(dbPath), hnswSnapshotDir)
	p := filepath.Join(dir, hnswCleanStopName)
	if err := os.Remove(p); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("books: remove the HNSW clean-stop marker %s, so the next boot reconciles the vector graph: %w", p, err)
	}
	d, err := os.Open(dir)
	if err != nil {
		return fmt.Errorf("books: sync %s after removing the HNSW clean-stop marker: %w", dir, err)
	}
	if err := errors.Join(d.Sync(), d.Close()); err != nil {
		return fmt.Errorf("books: sync %s after removing the HNSW clean-stop marker: %w", dir, err)
	}
	return nil
}
