// file: internal/plugins/acoustid/signature_lost_update_test.go
// version: 1.0.0
// guid: d4ae5494-c39a-4c29-8456-9a40033177cc
// last-edited: 2026-09-14

package acoustid

import (
	"encoding/base64"
	"encoding/binary"
	"errors"
	"math/rand"
	"sync"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// sigLostUpdateStore is a one-row stateful book store for the lost-update
// race: GetBookByID returns a copy, UpdateBook stores a copy, ModifyBook is a
// locked read-modify-write, and the OTHER writer commits Duration=4242 right
// after every raw GetBookByID and right before every ModifyBook takes the
// lock. A site that reads the row and writes the WHOLE row back reverts it;
// ModifyBook keeps it.
type sigLostUpdateStore struct {
	*database.MockStore
	mu  sync.Mutex
	row *database.Book
}

func (s *sigLostUpdateStore) landDurationLocked() {
	d := 4242
	s.row.Duration = &d
}

func (s *sigLostUpdateStore) GetBookByID(id string) (*database.Book, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.row == nil || s.row.ID != id {
		return nil, nil
	}
	cp := *s.row
	s.landDurationLocked()
	return &cp, nil
}

func (s *sigLostUpdateStore) UpdateBook(id string, b *database.Book) (*database.Book, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := *b
	s.row = &cp
	return &cp, nil
}

func (s *sigLostUpdateStore) ModifyBook(id string, fn func(*database.Book) error) (*database.Book, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.row == nil || s.row.ID != id {
		return nil, nil
	}
	s.landDurationLocked()
	cp := *s.row
	if err := fn(&cp); err != nil {
		if errors.Is(err, database.ErrSkipBookWrite) {
			return &cp, nil
		}
		return nil, err
	}
	stored := cp
	s.row = &stored
	return &cp, nil
}

func (s *sigLostUpdateStore) stored() *database.Book {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.row
}

// lostUpdateSegment is a decodable segment fingerprint: base64 of
// little-endian uint32 words, the encoding fingerprint.SynthesizePartialBookSignature reads.
func lostUpdateSegment(rng *rand.Rand, words int) string {
	buf := make([]byte, words*4)
	for i := 0; i < words; i++ {
		binary.LittleEndian.PutUint32(buf[i*4:], rng.Uint32())
	}
	return base64.StdEncoding.EncodeToString(buf)
}

// TestSynthesizeBookSignatureForBook_DoesNotRevertConcurrentColumns pins the
// lost-update fix on the book-signature write (audit A1#15): it must set only
// the five BookSig* columns, so a Duration another writer commits between its
// read and its write survives. Against the old GetBookByID -> UpdateBook(whole
// row) it fails with "Duration reverted".
func TestSynthesizeBookSignatureForBook_DoesNotRevertConcurrentColumns(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	mkFile := func(id string, track int) database.BookFile {
		return database.BookFile{
			ID: id, BookID: "b1", TrackNumber: track, OriginalFilename: id + ".mp3",
			AcoustIDSeg0: lostUpdateSegment(rng, 500), AcoustIDSeg1: lostUpdateSegment(rng, 500),
			AcoustIDSeg2: lostUpdateSegment(rng, 500), AcoustIDSeg3: lostUpdateSegment(rng, 500),
			AcoustIDSeg4: lostUpdateSegment(rng, 500), AcoustIDSeg5: lostUpdateSegment(rng, 500),
			AcoustIDSeg6: lostUpdateSegment(rng, 500),
		}
	}
	files := []database.BookFile{mkFile("f1", 1), mkFile("f2", 2)}
	store := &sigLostUpdateStore{
		MockStore: &database.MockStore{
			GetBookFilesFunc: func(string) ([]database.BookFile, error) { return files, nil },
		},
		row: &database.Book{ID: "b1", Title: "Book"},
	}

	if err := synthesizeBookSignatureForBook(store, "b1"); err != nil {
		t.Fatalf("synthesizeBookSignatureForBook: %v", err)
	}
	got := store.stored()
	if got == nil || got.BookSigV1 == nil || *got.BookSigV1 == "" || got.BookSigBuiltAt == nil {
		t.Fatalf("signature not written (the fixture must synthesize a real signature or this test is vacuous): %+v", got)
	}
	if got.Duration == nil || *got.Duration != 4242 {
		t.Fatalf("Duration reverted by the signature write: got %v, want 4242 (the concurrent writer's value)", got.Duration)
	}
}
