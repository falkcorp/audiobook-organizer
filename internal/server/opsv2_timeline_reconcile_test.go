// file: internal/server/opsv2_timeline_reconcile_test.go
// version: 1.1.0
// guid: 1aadeb25-3ece-4be2-88a9-5f21fd2900ac
// last-edited: 2026-10-04

package server

import (
	"context"
	"testing"
	"time"

	"github.com/oklog/ulid/v2"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// TestIndexedStoreExposesOpsV2TimelineReconcile runs the production
// resolution path through the indexedStore decorator: a bare assertion misses
// the *PebbleStore behind it, and then the timeline index would never be
// trusted (only a warning would say so). Before the reconcile the store is
// untrusted (scan); after it, trusted (index).
func TestIndexedStoreExposesOpsV2TimelineReconcile(t *testing.T) {
	inner, err := database.NewPebbleStoreInMemory(t.TempDir())
	if err != nil {
		t.Fatalf("open pebble: %v", err)
	}
	t.Cleanup(func() { _ = inner.Close() })
	inner.WaitForWarmup()

	var wrapped database.Store = &indexedStore{Store: inner, server: nil}
	if _, ok := wrapped.(opsV2TimelineReconciler); ok {
		t.Fatal("a bare assertion now resolves opsV2TimelineReconciler through the " +
			"decorator; this test no longer reproduces the production shape")
	}
	r, ok := resolveOpsV2TimelineReconciler(wrapped)
	if !ok {
		t.Fatal("resolveOpsV2TimelineReconciler through indexedStore failed; the " +
			"timeline index would never be used")
	}

	now := time.Now().UTC()
	id := ulid.Make().String()
	if err := inner.InsertOperationV2(database.OperationV2Row{ID: id, Status: "completed", QueuedAt: now.Add(-time.Hour), CompletedAt: &now}); err != nil {
		t.Fatal(err)
	}
	if inner.OpsV2TimelineIndexTrusted() {
		t.Fatal("index trusted before this boot's reconcile")
	}
	res, err := r.ReconcileOpsV2TimelineIndex(context.Background())
	if err != nil {
		t.Fatalf("reconcile through the decorator: %v", err)
	}
	if res.Rows != 1 {
		t.Fatalf("reconcile = %+v, want 1 row", res)
	}
	if !inner.OpsV2TimelineIndexTrusted() {
		t.Fatal("index not trusted after a completed reconcile")
	}
	rows, err := inner.ListOperationsV2Since(now.Add(-10*time.Minute), 10)
	if err != nil || len(rows) != 1 || rows[0].ID != id {
		t.Fatalf("indexed timeline read: %v, %v", rows, err)
	}
}
