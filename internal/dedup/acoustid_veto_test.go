// file: internal/dedup/acoustid_veto_test.go
// version: 1.0.0
// guid: 4e7c1a92-6d38-45b1-8f0a-3c2e9d514b70
// last-edited: 2026-07-02

package dedup

import (
	"encoding/base64"
	"encoding/binary"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/fingerprint"
)

// makeBookSig builds a valid base64 book_sig_v1 whose 4096 words are all `word`.
// Two all-zero sigs are identical (similarity 1.0); an all-zero vs an
// all-0xFFFFFFFF sig differ in every bit (similarity 0.0).
func makeBookSig(word uint32) string {
	buf := make([]byte, fingerprint.BookSignatureFixedLength*4)
	for i := range fingerprint.BookSignatureFixedLength {
		binary.LittleEndian.PutUint32(buf[i*4:], word)
	}
	return base64.StdEncoding.EncodeToString(buf)
}

func TestAcoustIDSignaturesConflict(t *testing.T) {
	engine, mock, _ := setupTestEngine(t)

	books := map[string]*database.Book{}
	mock.GetBookByIDFunc = func(id string) (*database.Book, error) { return books[id], nil }
	mk := func(id, sig string) *database.Book {
		b := &database.Book{ID: id}
		if sig != "" {
			s := sig
			b.BookSigV1 = &s
		}
		books[id] = b
		return b
	}

	zero := makeBookSig(0)
	ones := makeBookSig(0xFFFFFFFF)

	t.Run("clearly different audio is vetoed", func(t *testing.T) {
		if !engine.acoustIDSignaturesConflict(mk("a1", zero), mk("b1", ones)) {
			t.Error("expected conflict (veto) for opposite signatures")
		}
	})
	t.Run("identical audio is NOT vetoed", func(t *testing.T) {
		if engine.acoustIDSignaturesConflict(mk("a2", zero), mk("b2", zero)) {
			t.Error("identical signatures must never be vetoed (would hide a real duplicate)")
		}
	})
	t.Run("missing signature on one side is not vetoed", func(t *testing.T) {
		if engine.acoustIDSignaturesConflict(mk("a3", zero), mk("b3", "")) {
			t.Error("must not veto when one signature is missing (conservative)")
		}
	})
	t.Run("both signatures missing is not vetoed", func(t *testing.T) {
		if engine.acoustIDSignaturesConflict(mk("a4", ""), mk("b4", "")) {
			t.Error("must not veto when both signatures are missing")
		}
	})
	t.Run("fetch fallback: signature pulled from store when not on the struct", func(t *testing.T) {
		mk("a5", zero)
		mk("b5", ones)
		// Pass stripped structs (no signature); the helper must re-fetch by ID.
		if !engine.acoustIDSignaturesConflict(&database.Book{ID: "a5"}, &database.Book{ID: "b5"}) {
			t.Error("expected conflict via store fetch fallback")
		}
	})
	t.Run("nil books are safe", func(t *testing.T) {
		if engine.acoustIDSignaturesConflict(nil, mk("b6", zero)) {
			t.Error("nil book must not veto")
		}
	})
}
