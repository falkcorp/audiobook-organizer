// file: internal/database/fingerprint_era_test.go
// version: 1.0.0
// guid: e4a0c7d2-5b18-4f63-8a9e-1d7b2c6f0e45
// last-edited: 2026-09-19

package database

import (
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/fingerprint"
)

// TestBookSigVersion_RoundTripsAndClearDeletes: the signature version is
// persisted in the book_sig: sidecar (so HasCurrentBookSig survives a
// reload), and ClearBookSignature really deletes the signature — nil fields
// through UpdateBook would be restored by the preserve-on-nil guard.
func TestBookSigVersion_RoundTripsAndClearDeletes(t *testing.T) {
	store, cleanup := setupPebbleTestDB(t)
	defer cleanup()
	created, err := store.CreateBook(&Book{Title: "Sig Book", FilePath: "/lib/sig"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	sig, v := "c2ln", fingerprint.BookSignatureVersion
	if _, err := store.ModifyBook(created.ID, func(b *Book) error {
		b.BookSigV1, b.BookSigVersion = &sig, &v
		return nil
	}); err != nil {
		t.Fatalf("modify: %v", err)
	}
	got, _ := store.GetBookByID(created.ID)
	if !got.HasCurrentBookSig() {
		t.Fatalf("version did not round-trip: %+v", got.BookSigVersion)
	}
	if err := store.ClearBookSignature(created.ID); err != nil {
		t.Fatalf("clear: %v", err)
	}
	got, _ = store.GetBookByID(created.ID)
	if got.BookSigV1 != nil || got.BookSigVersion != nil {
		t.Fatalf("signature survived ClearBookSignature: v1=%v version=%v", got.BookSigV1, got.BookSigVersion)
	}
}
