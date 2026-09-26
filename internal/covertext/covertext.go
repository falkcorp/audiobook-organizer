// file: internal/covertext/covertext.go
// version: 1.0.0
// guid: 6d1e8b37-2f4a-4c59-9e03-7a5b2c8d4f16
// last-edited: 2026-09-26

// Package covertext stores the text a vision model read off a cover image.
//
// It is modelled on how intro transcriptions are kept: the raw reply and the
// parsed fields are stored together with the model that produced them, the
// prompt version, a timestamp and a status, so a failed read is
// distinguishable from one never attempted and a prompt change can be
// re-run selectively.
//
// Two keyspaces, both in the raw KV store (database.RawKVStore, which the
// production ops store forwards, so no capability assertion is involved):
//
//   - cover_text:<sha256>        one Record per distinct IMAGE. Keyed by the
//     SHA-256 of the exact bytes sent to the model, so a cover shared by many
//     books (one embedded image across a series' files, the same folder
//     image in two copies of a book) is read once.
//   - cover_text_book:<book id>  one BookIndex per book: which images the book
//     has (folder, embedded, local) and their hashes. It is what the book
//     detail page and the identification/dedup readers join through.
//
// Reading a cover only STORES text. Nothing here writes book metadata.
package covertext

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// PromptVersion names the prompt and reply schema. Bump it when either
// changes; the backfill op re-reads records written under another version.
const PromptVersion = "cover-text-v1"

const (
	recordKeyPrefix = "cover_text:"
	bookKeyPrefix   = "cover_text_book:"
)

// Status of one read.
type Status string

const (
	// StatusOK: the model replied and the reply parsed.
	StatusOK Status = "ok"
	// StatusError: the read failed (endpoint error, timeout, unparsable
	// reply). Error says why. The backfill retries these.
	StatusError Status = "error"
)

// Source says where a book's cover image came from.
type Source string

const (
	SourceLocal    Source = "local"    // the file cover_url or covers/<id> points at
	SourceEmbedded Source = "embedded" // the first audio file's embedded picture
	SourceFolder   Source = "folder"   // the best image in the book's folder
)

// Text is what the model read. Every field is optional.
type Text struct {
	Title        string   `json:"title,omitempty"`
	Subtitle     string   `json:"subtitle,omitempty"`
	Authors      []string `json:"authors,omitempty"`
	Narrators    []string `json:"narrators,omitempty"`
	Series       string   `json:"series,omitempty"`
	SeriesNumber string   `json:"series_number,omitempty"`
	Publisher    string   `json:"publisher,omitempty"`
	// OtherText is every other visible line: taglines, award banners,
	// "Unabridged", an edition note.
	OtherText []string `json:"other_text,omitempty"`
}

// Empty reports whether the model read nothing at all.
func (t *Text) Empty() bool {
	return t == nil || (t.Title == "" && t.Subtitle == "" && len(t.Authors) == 0 && len(t.Narrators) == 0 &&
		t.Series == "" && t.SeriesNumber == "" && t.Publisher == "" && len(t.OtherText) == 0)
}

// Record is one read of one image.
type Record struct {
	Hash          string    `json:"hash"`
	Status        Status    `json:"status"`
	Text          *Text     `json:"text,omitempty"`
	Raw           string    `json:"raw,omitempty"`
	Error         string    `json:"error,omitempty"`
	Model         string    `json:"model,omitempty"`
	EndpointID    string    `json:"endpoint_id,omitempty"`
	PromptVersion string    `json:"prompt_version"`
	ReadAt        time.Time `json:"read_at"`
	// Attempts counts reads of this hash, successful or not.
	Attempts int `json:"attempts"`
}

// Current reports whether r is a successful read under the current prompt.
func (r *Record) Current() bool {
	return r != nil && r.Status == StatusOK && r.PromptVersion == PromptVersion
}

// ImageRef is one cover image of a book.
type ImageRef struct {
	Hash   string `json:"hash"`
	Source Source `json:"source"`
	// Path is the file the image was read from (the stored cover, the audio
	// file for an embedded picture, or the folder image).
	Path string `json:"path,omitempty"`
}

// BookIndex lists a book's cover images.
type BookIndex struct {
	BookID    string     `json:"book_id"`
	Images    []ImageRef `json:"images"`
	UpdatedAt time.Time  `json:"updated_at"`
}

// KV is the raw key/value surface this package needs. database.RawKVStore
// satisfies it.
type KV interface {
	GetRaw(key string) ([]byte, error)
	SetRaw(key string, value []byte) error
}

func validHash(h string) bool {
	if len(h) != 64 {
		return false
	}
	for _, c := range h {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// RecordKey is the key a record is stored under.
func RecordKey(hash string) string { return recordKeyPrefix + hash }

// BookKey is the key a book index is stored under.
func BookKey(bookID string) string { return bookKeyPrefix + bookID }

// Get returns the record for hash, or nil when none is stored.
func Get(kv KV, hash string) (*Record, error) {
	if !validHash(hash) {
		return nil, fmt.Errorf("cover text: invalid image hash %q", hash)
	}
	raw, err := kv.GetRaw(RecordKey(hash))
	if err != nil || raw == nil {
		return nil, err
	}
	var r Record
	if err := json.Unmarshal(raw, &r); err != nil {
		return nil, fmt.Errorf("cover text %s: %w", hash, err)
	}
	return &r, nil
}

// Put stores r under its hash.
func Put(kv KV, r *Record) error {
	if r == nil || !validHash(r.Hash) {
		return errors.New("cover text: record has no valid hash")
	}
	data, err := json.Marshal(r)
	if err != nil {
		return err
	}
	return kv.SetRaw(RecordKey(r.Hash), data)
}

// GetBook returns the index for bookID, or nil when none is stored.
func GetBook(kv KV, bookID string) (*BookIndex, error) {
	if bookID == "" || strings.ContainsAny(bookID, ":/") {
		return nil, fmt.Errorf("cover text: invalid book id %q", bookID)
	}
	raw, err := kv.GetRaw(BookKey(bookID))
	if err != nil || raw == nil {
		return nil, err
	}
	var idx BookIndex
	if err := json.Unmarshal(raw, &idx); err != nil {
		return nil, fmt.Errorf("cover text index %s: %w", bookID, err)
	}
	return &idx, nil
}

// PutBook stores a book's index.
func PutBook(kv KV, idx *BookIndex) error {
	if idx == nil || idx.BookID == "" || strings.ContainsAny(idx.BookID, ":/") {
		return errors.New("cover text: index has no valid book id")
	}
	data, err := json.Marshal(idx)
	if err != nil {
		return err
	}
	return kv.SetRaw(BookKey(idx.BookID), data)
}

// BookCoverText is one of a book's images joined with its stored read (nil
// when the image has not been read yet).
type BookCoverText struct {
	ImageRef
	Record *Record `json:"record,omitempty"`
}

// ForBook returns every indexed image of a book with its stored read. This is
// the reader the book detail page and the identification/dedup evidence
// (unified.SigCoverText) use. A book never indexed returns nil, nil.
func ForBook(kv KV, bookID string) ([]BookCoverText, error) {
	idx, err := GetBook(kv, bookID)
	if err != nil || idx == nil {
		return nil, err
	}
	out := make([]BookCoverText, 0, len(idx.Images))
	for _, ref := range idx.Images {
		rec, err := Get(kv, ref.Hash)
		if err != nil {
			return nil, err
		}
		out = append(out, BookCoverText{ImageRef: ref, Record: rec})
	}
	return out, nil
}
