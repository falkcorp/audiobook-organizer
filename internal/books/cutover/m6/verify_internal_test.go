// file: internal/books/cutover/m6/verify_internal_test.go
// version: 1.0.0
// guid: 5d03b8e7-91c4-4a2f-b6e0-7f18c2a94d65
// last-edited: 2026-11-19

package m6

import (
	"context"
	"iter"
	"strings"
	"testing"

	"github.com/falkcorp/mediacore/storage"
)

// emptyReader is a store with no row in any range.
type emptyReader struct{}

func (emptyReader) Get(context.Context, []byte) ([]byte, error) { return nil, storage.ErrNotFound }
func (emptyReader) Scan(context.Context, storage.ScanOptions) iter.Seq2[storage.KV, error] {
	return func(func(storage.KV, error) bool) {}
}
func (emptyReader) NewIterator(context.Context, storage.ScanOptions) (*storage.Iterator, error) {
	return nil, nil
}

// TestVerify_SeenAgainstHandled: Verify holds the rows this boot read to the rows it
// consumed or quarantined, in both directions. A row read and then neither
// consumed nor quarantined, and a row consumed that was never read, each fail it;
// a balanced boot passes. (The check it replaces compared two counters that were
// always incremented together and could not fail.)
func TestVerify_SeenAgainstHandled(t *testing.T) {
	ctx := context.Background()
	for _, c := range []struct {
		name  string
		run   func(m *Migration)
		fails string
	}{
		{"balanced", func(m *Migration) {
			m.read("aijob:1")
			m.tally("aijob:1", OutcomeJob)
			m.read("aijob:2")
			m.tallyQuarantine("aijob:2")
		}, ""},
		{"read and lost", func(m *Migration) {
			m.read("aijob:1")
			m.tally("aijob:1", OutcomeJob)
			m.read("aijob:2")
		}, "read and neither consumed nor quarantined (aijob:2)"},
		{"consumed unread", func(m *Migration) {
			m.tally("provider_throttle:audible", OutcomeHold)
		}, "consumed that was never read (provider_throttle:audible)"},
		{"member key is not a source row", func(m *Migration) {
			m.read(KeyBlob)
			m.tally(KeyBlob, OutcomeBlob)
			m.tallyQuarantine(KeyBlob + "#itunes.path_mappings.1")
		}, ""},
		{"a source key containing a hash is a source row", func(m *Migration) {
			m.read("import_path:path:/batcave/#1")
			m.tallyQuarantine("import_path:path:/batcave/#1")
		}, ""},
		{"a source key containing a hash, read and lost", func(m *Migration) {
			m.read("import_path:path:/batcave/#1")
		}, "read and neither consumed nor quarantined (import_path:path:/batcave/#1)"},
	} {
		t.Run(c.name, func(t *testing.T) {
			m := newMigration(Config{}, "test")
			c.run(m)
			rep, err := m.Verify(ctx, emptyReader{})
			if err != nil {
				t.Fatal(err)
			}
			if c.fails == "" {
				if len(rep.Failures) != 0 {
					t.Errorf("a balanced boot failed verify: %v", rep.Failures)
				}
				return
			}
			if len(rep.Failures) != 1 || !strings.Contains(rep.Failures[0], c.fails) {
				t.Errorf("failures %v, want one containing %q", rep.Failures, c.fails)
			}
		})
	}
}
