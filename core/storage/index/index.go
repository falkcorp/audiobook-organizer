// file: core/storage/index/index.go
// version: 1.0.1
// guid: a0a113b9-331b-46fd-beef-f6cb060b8e0c
// last-edited: 2026-11-11

// Package index is core's full-text index contract (05-storage §5S.3.8): the
// declaration of a named index a module owns, the engine-neutral query
// constructors, the durable dirty set that makes an entity write and its
// re-index mark one commit (G4), and the Opener a module receives through
// module.StorageHandles.
//
// It is L1: it imports storage, and storage never imports it. No search-engine
// type appears in its exported API (I5); core/internal/bleveindex is the
// implementation.
package index

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/storage/keys"
)

// Spec declares one named index owned by a module.
type Spec struct {
	Name           string // "books"; on disk <db parent>/index/<module>/<name>.bleve
	MappingVersion string // a change triggers drop + rebuild (today's bookMappingVersion)
	Fields         []Field
	Rebuild        func(ctx context.Context, w RebuildWriter) error        // full rebuild from the module's storage
	Document       func(ctx context.Context, id string) (Doc, bool, error) // reconciler: load one doc, false = delete
}

// FieldKind is a field's type.
type FieldKind uint8

const (
	FieldInvalid FieldKind = iota // the zero value is rejected
	Text                          // analysed free text
	Keyword                       // one exact term
	Number                        // int64 or float64
	Date                          // time.Time
	Bool
)

// String names the kind.
func (k FieldKind) String() string {
	switch k {
	case Text:
		return "text"
	case Keyword:
		return "keyword"
	case Number:
		return "number"
	case Date:
		return "date"
	case Bool:
		return "bool"
	default:
		return "invalid"
	}
}

// Field declares one indexed field.
type Field struct {
	Name     string
	Kind     FieldKind // Text, Keyword, Number, Date, Bool
	Analyzer string    // "standard", "keyword", or a module-registered analyzer name; "" = standard for Text, keyword for Keyword
	Store    bool      // returned in Hit.Fields
	Facet    bool      // countable through Index.Facets (Text, Keyword or Bool only)
}

// Doc is one document: field name to value (string, int64, float64,
// time.Time, bool, or a slice of them for a multi-valued field).
type Doc map[string]any

// Opener opens a module's named index. The kernel hands a module-scoped
// Opener to module.StorageHandles (F7-09 wires it).
type Opener interface {
	Open(ctx context.Context, s Spec) (Index, error)
}

// Index is one open index.
type Index interface {
	Search(ctx context.Context, q Query, page Page) (Result, error)
	Count(ctx context.Context, q Query) (uint64, error)
	Facets(ctx context.Context, q Query, fields []string, size int) (map[string][]FacetValue, error)
	// MarkDirty records id in the durable dirty set inside b, the same batch as
	// the entity write, and touches the index's storage generation (§5S.3.9).
	// It is the only write path modules use (G4: the index never depends on an
	// in-memory event).
	//
	// The mark is a read-modify-write under a precondition (the dirty keyspace is
	// Serialized): a concurrent mark of the same id, or the reconciler clearing
	// the previous mark, can fail the batch's Commit with storage.ErrConflict.
	// That is the price of never losing a mark, and the caller's answer is to
	// rebuild the batch and commit again: wrap the whole write (the entity
	// change, MarkDirty and Commit) in storage.RetryOnConflict.
	//
	// Every write to an indexed entity must call it in the write's own batch,
	// DeleteRange and bulk import included (once per id they touch): a write that
	// does not is a document the index never learns about.
	MarkDirty(b *storage.Batch, id string) error
	State() State // Ready, Rebuilding, Degraded (open failed; callers fall back)
	Close() error
}

// Page selects one page of results. SortBy names fields, "-" prefixed for
// descending, and "_score" and "_id" are the two engine fields; empty means
// best score first, then ID.
type Page struct {
	From, Size int
	SortBy     []string
}

// Result is one page and the total match count.
type Result struct {
	Total uint64
	Hits  []Hit
}

// Hit is one match; Fields holds the Store fields.
type Hit struct {
	ID     string
	Score  float64
	Fields map[string]any
}

// FacetValue is one term and the number of matching documents holding it.
type FacetValue struct {
	Term  string
	Count int
}

// RebuildWriter receives the documents of a full rebuild. Put is safe for
// concurrent use, so a rebuild may scan its storage in parallel; it returns the
// run's cancellation error once the rebuild is cancelled.
type RebuildWriter interface{ Put(id string, d Doc) error }

// State is an index's serving state.
type State uint8

const (
	StateInvalid State = iota
	Ready              // complete and serving
	Rebuilding         // does not yet cover the library: a miss is not "no such document"
	Degraded           // open failed; callers fall back and Search returns ErrDegraded
)

// String names the state.
func (s State) String() string {
	switch s {
	case Ready:
		return "ready"
	case Rebuilding:
		return "rebuilding"
	case Degraded:
		return "degraded"
	default:
		return "invalid"
	}
}

var (
	// ErrInvalidSpec: the Spec cannot declare an index.
	ErrInvalidSpec = errors.New("index: invalid spec")
	// ErrInvalidQuery: the query is nil, malformed, or names a field the index
	// does not have.
	ErrInvalidQuery = errors.New("index: invalid query")
	// ErrDegraded: the index failed to open; the caller serves its fallback.
	ErrDegraded = errors.New("index: degraded (open failed)")
	// ErrClosed: the index was closed.
	ErrClosed = errors.New("index: closed")
)

// Validate reports why s cannot declare an index, or nil. The engine runs it
// before it creates anything on disk.
func (s Spec) Validate() error {
	var probs []string
	if err := keys.ValidName(s.Name); err != nil {
		probs = append(probs, err.Error())
	}
	if strings.TrimSpace(s.MappingVersion) == "" || strings.ContainsAny(s.MappingVersion, "\r\n") {
		probs = append(probs, "MappingVersion must be a non-empty single line")
	}
	if s.Rebuild == nil {
		probs = append(probs, "Rebuild is required")
	}
	if s.Document == nil {
		probs = append(probs, "Document is required")
	}
	if len(s.Fields) == 0 {
		probs = append(probs, "at least one Field is required")
	}
	seen := map[string]bool{}
	for _, f := range s.Fields {
		switch {
		case f.Name == "" || strings.HasPrefix(f.Name, "_"):
			probs = append(probs, fmt.Sprintf("field %q: a name is required and may not start with _ (_id and _score are the engine's)", f.Name))
		case seen[f.Name]:
			probs = append(probs, fmt.Sprintf("field %q declared twice", f.Name))
		}
		seen[f.Name] = true
		switch f.Kind {
		case Text, Keyword, Number, Date, Bool:
		default:
			probs = append(probs, fmt.Sprintf("field %q: kind %s", f.Name, f.Kind))
		}
		if f.Facet && (f.Kind == Number || f.Kind == Date) {
			probs = append(probs, fmt.Sprintf("field %q: only Text, Keyword and Bool fields can be facets", f.Name))
		}
		if f.Analyzer != "" && f.Kind != Text && f.Kind != Keyword {
			probs = append(probs, fmt.Sprintf("field %q: an analyzer applies to Text and Keyword fields only", f.Name))
		}
	}
	if len(probs) > 0 {
		return fmt.Errorf("%w %q: %s", ErrInvalidSpec, s.Name, strings.Join(probs, "; "))
	}
	return nil
}
