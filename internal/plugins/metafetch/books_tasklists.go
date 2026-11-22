// file: internal/plugins/metafetch/books_tasklists.go
// version: 0.1.0
// guid: 9d2f6b18-4a73-4e05-b1c8-7a0e53d9f246
// last-edited: 2026-11-22

package metafetch

import (
	"context"
	"fmt"
	"strings"

	"github.com/falkcorp/mediacore/tasks"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/books/writer"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/pkg/ops"
)

// The books module's own KindDeps for the metafetch kinds (F5-18 A6), dark:
// nothing outside tests calls BooksTaskLists until the flip's registration
// commit. The writer is the gated one: every method is ONE writer.Gate call
// stamped with the method's whole row, a method with no row is refused, and the
// raw key families are told apart by key prefix (a key in no family is
// refused, so a new raw write fails closed).

type (
	// CandidateReads is the persisted candidate-cache read calibrate-scoring takes
	// (metafetch.Service.GetCachedCandidates).
	CandidateReads = calibrateCandidates
	// AudibleSearcher is the Audible identity client asin-backfill builds per book.
	AudibleSearcher = audibleIdentitySearcher
)

// kindSourceWrites is the four store writes the three cores reach, through the
// gate only.
type kindSourceWrites interface {
	ModifyBook(id string, fn func(*database.Book) error) (*database.Book, error)
	RecordMetadataChange(record *database.MetadataChangeRecord) error
	SetRaw(key string, value []byte) error
	DeleteRawBatch(keys []string) error
}

// KindSource is the store the builder reads and writes through: the reads of
// the three kinds and the four writes, nothing else.
type KindSource interface {
	kindStore
	kindSourceWrites
}

// BooksTaskInputs is everything BooksTaskLists builds the metafetch lists from.
type BooksTaskInputs struct {
	// Source is the library store.
	Source KindSource
	// Candidates is the persisted candidate-cache read (calibrate-scoring).
	Candidates CandidateReads
	// Scoring is the process's metadata-scoring config (calibrate-scoring).
	Scoring func() config.MetadataScoringConfig
	// NewAudible builds the Audible client per book (asin-backfill).
	NewAudible func() AudibleSearcher
	// BuildSourceChain installs the configured Audible budget; optional.
	BuildSourceChain func()
	// Schedules is books' schedule hook.
	Schedules func(tasks.Kind) *tasks.Schedule
}

// BooksTaskLists returns every metafetch kind with its schedule, over the books
// module's own dependency values.
func BooksTaskLists(in BooksTaskInputs) ([]tasks.Definition, error) {
	var missing []string
	for name, set := range map[string]bool{
		"Source": in.Source != nil, "Candidates": in.Candidates != nil, "Scoring": in.Scoring != nil,
		"NewAudible": in.NewAudible != nil, "Schedules": in.Schedules != nil,
	} {
		if !set {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("metafetch: BooksTaskLists: missing input(s): %v", missing)
	}
	deps, err := newBooksKindDeps(in)
	if err != nil {
		return nil, err
	}
	return (&Plugin{}).Kinds(deps, in.Schedules), nil
}

// booksKindRows is the rows of the writer's methods: the shared table's
// ModifyBook, the apply table's RecordMetadataChange, and one row for each of
// the raw families the cores write (the answer cache under metadata_cache, the
// no-match markers under asin_misses).
func booksKindRows() (writer.Rows, error) {
	shared := writer.SharedRows()
	apply := writer.MetafetchApplyRows()
	rows := writer.Rows{
		"SetRawASINMiss": {ops.ResASINMisses},
	}
	for _, m := range []struct {
		method string
		from   writer.Rows
	}{{"ModifyBook", shared}, {"RecordMetadataChange", apply}} {
		row, ok := m.from[m.method]
		if !ok {
			return nil, fmt.Errorf("metafetch: no resource row for %s", m.method)
		}
		rows[m.method] = row
	}
	raw := writer.RawRows()
	for method, qualified := range map[string]string{
		"SetRawPCache":   writer.RawMethod("SetRaw", pcacheRawPrefix),
		"DeleteRawBatch": writer.RawMethod("DeleteRawBatch", pcacheRawPrefix),
	} {
		row, ok := raw[qualified]
		if !ok {
			return nil, fmt.Errorf("metafetch: no resource row for %s", qualified)
		}
		rows[method] = row
	}
	return rows, nil
}

// pcacheRawPrefix is the answer cache's key family.
const pcacheRawPrefix = "pcache:"

// asinMissPrefixes are the no-match marker families asin-backfill writes.
var asinMissPrefixes = []string{asinMissKeyPrefix, "asinbackfill:isbnmiss:"}

func newBooksKindDeps(in BooksTaskInputs) (KindDeps, error) {
	rows, err := booksKindRows()
	if err != nil {
		return KindDeps{}, err
	}
	src := in.Source
	return KindDeps{
		Store:      src,
		Candidates: in.Candidates,
		Scoring:    in.Scoring,
		NewAudible: in.NewAudible,
		NewWriter: func(w tasks.Writer) kindWriter {
			return &booksKindWriter{src: src, gate: writer.ForRun(w, rows)}
		},
		BuildSourceChain: in.BuildSourceChain,
	}, nil
}

// booksKindWriter is one run's gated writes. A ReadOnly run has no writer
// (w is nil): every method then refuses instead of dereferencing nil.
type booksKindWriter struct {
	src  kindSourceWrites
	gate *writer.Gate
}

func (w *booksKindWriter) ModifyBook(id string, fn func(*database.Book) error) (*database.Book, error) {
	return writer.Value(context.Background(), w.gate, "ModifyBook", writer.Ref(string(ops.ResBooks), id), func() (*database.Book, error) {
		return w.src.ModifyBook(id, fn)
	})
}

func (w *booksKindWriter) RecordMetadataChange(record *database.MetadataChangeRecord) error {
	id := ""
	if record != nil {
		id = record.BookID
	}
	return w.gate.Err(context.Background(), "RecordMetadataChange", writer.Ref(string(ops.ResBooks), id), func() error {
		return w.src.RecordMetadataChange(record)
	})
}

func (w *booksKindWriter) SetRawASINMiss(key string, value []byte) error {
	if !hasAnyPrefix(key, asinMissPrefixes) {
		return refuseKey("SetRawASINMiss", key)
	}
	return w.gate.Err(context.Background(), "SetRawASINMiss", writer.Ref(string(ops.ResASINMisses), key), func() error {
		return w.src.SetRaw(key, value)
	})
}

func (w *booksKindWriter) SetRawPCache(key string, value []byte) error {
	if !strings.HasPrefix(key, pcacheRawPrefix) {
		return refuseKey("SetRawPCache", key)
	}
	return w.gate.Err(context.Background(), "SetRawPCache", writer.Ref(string(ops.ResMetadataCache), key), func() error {
		return w.src.SetRaw(key, value)
	})
}

// DeleteRawBatch is one store call, so its keys must all be answer-cache keys.
func (w *booksKindWriter) DeleteRawBatch(keys []string) error {
	for _, k := range keys {
		if !strings.HasPrefix(k, pcacheRawPrefix) {
			return refuseKey("DeleteRawBatch", k)
		}
	}
	return w.gate.Err(context.Background(), "DeleteRawBatch", writer.Ref(string(ops.ResMetadataCache), ""), func() error {
		return w.src.DeleteRawBatch(keys)
	})
}

func hasAnyPrefix(key string, prefixes []string) bool {
	for _, p := range prefixes {
		if strings.HasPrefix(key, p) {
			return true
		}
	}
	return false
}

// refuseKey is the refusal of a raw write on a key in the method's no family.
func refuseKey(method, key string) error {
	return fmt.Errorf("%w: %w: %s on key %q is in no raw family of this writer", tasks.ErrOutsideEffects, writer.ErrNoRow, method, key)
}
