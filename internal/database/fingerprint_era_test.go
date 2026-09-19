// file: internal/database/fingerprint_era_test.go
// version: 1.1.0
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

// TestClearBookSignature_RemovesInlineLegacySig: a row written before the
// sidecar migration carries its signature inline and hydrateBookSig falls back
// to it. Deleting only the sidecar left it in place (re-synthesised and
// re-cleared every night); the clear must also rewrite the row without it.
func TestClearBookSignature_RemovesInlineLegacySig(t *testing.T) {
	env := newBookSigEnv(t)
	const id = "inline-legacy-clear"
	migrateSeedLegacyRow(t, env.store, id, "inline", nil)
	before, err := env.store.GetBookByID(id)
	if err != nil || before == nil || before.BookSigV1 == nil {
		t.Fatalf("fixture: inline signature not visible before clear: %v %+v", err, before)
	}
	if err := env.store.ClearBookSignature(id); err != nil {
		t.Fatalf("clear: %v", err)
	}
	after, err := env.store.GetBookByID(id)
	if err != nil || after == nil {
		t.Fatalf("reload: %v", err)
	}
	if after.BookSigV1 != nil || after.BookSigBuiltAt != nil {
		t.Fatalf("inline legacy signature survived ClearBookSignature: v1=%v", *after.BookSigV1)
	}
	if after.Title != before.Title {
		t.Fatalf("clear rewrote other fields: title %q -> %q", before.Title, after.Title)
	}
}

// TestMergeBookFile_PrintAndVersionMoveTogether: a write that carries no
// print (memdb-stripped) but version 1, over a stored LEGACY print, must not
// end up as legacy bytes under version 1. The version follows the bytes.
func TestMergeBookFile_PrintAndVersionMoveTogether(t *testing.T) {
	stored := &BookFile{ID: "f", AcoustIDFingerprint: []byte{1, 2, 3, 4}, AcoustIDFPVersion: 0}
	incoming := &BookFile{ID: "f", AcoustIDFPVersion: fingerprint.PrintEncodingVersion}
	mergeBookFileFromStored(incoming, stored, bookFileWriteReplace, presenceNotScanned)
	if len(incoming.AcoustIDFingerprint) == 0 {
		t.Fatal("fixture: stored print was not restored")
	}
	if incoming.AcoustIDFPVersion != 0 {
		t.Fatalf("restored legacy print written under version %d: garbage certified current", incoming.AcoustIDFPVersion)
	}

	// A caller that brings its own fresh print keeps its own version.
	fresh := &BookFile{ID: "f", AcoustIDFingerprint: []byte{9, 9, 9, 9}, AcoustIDFPVersion: fingerprint.PrintEncodingVersion}
	mergeBookFileFromStored(fresh, stored, bookFileWriteReplace, presenceNotScanned)
	if fresh.AcoustIDFPVersion != fingerprint.PrintEncodingVersion {
		t.Fatalf("fresh print lost its version: %d", fresh.AcoustIDFPVersion)
	}
}
