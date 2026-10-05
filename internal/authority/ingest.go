// file: internal/authority/ingest.go
// version: 1.0.0
// guid: c21642cf-8b47-4c22-a6d1-debe423b7a8e
// last-edited: 2026-10-04

package authority

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/metadata"
)

// CatalogRawPrefix is the author catalog's raw-payload keyspace
// (internal/database/catalog_entry_store.go, cat_raw:<entry id>): the
// Audible product JSON exactly as received, the only lossless source of
// contributor lists with narrator ASINs. The catalog store writes it on the
// shared main Pebble DB, so the raw KV surface reads it; a test proves that
// against a real store.
const CatalogRawPrefix = "cat_raw:"

// MaxLibraryExportBytes caps an owner library export read. audible-cli
// writes about 8 KB per product; 256 MiB is far beyond any real library.
const MaxLibraryExportBytes = 256 << 20

// AddRawProduct decodes one Audible product payload (a cat_raw: value or a
// library-export item) and ingests it. A payload the decoder refuses is
// counted as undecodable and returned as an error.
func (b *Builder) AddRawProduct(source string, raw []byte) error {
	p, err := metadata.DecodeAudibleProduct(raw)
	if err != nil {
		b.NoteUndecodable(source)
		return err
	}
	b.AddProduct(source, p)
	return nil
}

// ScanCatalogRaw hands every cat_raw: payload to fn, a page at a time.
// Memory is bounded by pageSize. An empty catalog calls fn zero times.
func ScanCatalogRaw(ctx context.Context, kv Scanner, pageSize int, fn func([]database.KVPair) error) error {
	after := ""
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		pairs, next, err := kv.ScanPrefixPage(CatalogRawPrefix, after, max(1, pageSize))
		if err != nil {
			return fmt.Errorf("scan %s: %w", CatalogRawPrefix, err)
		}
		if len(pairs) > 0 {
			if err := fn(pairs); err != nil {
				return err
			}
		}
		if next == "" {
			return nil
		}
		after = next
	}
}

// ReadLibraryExport reads an owner library export: a JSON list of Audible
// product objects (audible-cli `library export --format json`), or an object
// with an "items" list. Each item is returned undecoded. More than limit
// bytes is an error, never a silent truncation.
func ReadLibraryExport(r io.Reader, limit int64) ([]json.RawMessage, error) {
	data, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return nil, fmt.Errorf("read library export: %w", err)
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("library export is larger than %d bytes", limit)
	}
	data = bytes.TrimSpace(data)
	if len(data) == 0 {
		return nil, errors.New("library export is empty")
	}
	var items []json.RawMessage
	if data[0] == '{' {
		var wrapper struct {
			Items []json.RawMessage `json:"items"`
		}
		if err := json.Unmarshal(data, &wrapper); err != nil {
			return nil, fmt.Errorf("decode library export: %w", err)
		}
		if wrapper.Items == nil {
			return nil, errors.New(`library export object has no "items" list`)
		}
		items = wrapper.Items
	} else if err := json.Unmarshal(data, &items); err != nil {
		return nil, fmt.Errorf("decode library export: %w", err)
	}
	return items, nil
}
