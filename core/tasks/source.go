// file: core/tasks/source.go
// version: 1.0.0
// guid: 215ba8a1-85e2-40f5-bb7f-72017190f611
// last-edited: 2026-11-10

package tasks

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"sort"

	"github.com/falkcorp/mediacore/entity"
)

// Source yields a Batch's items or a Fixer's candidates. Order is a promise
// the runner relies on for resume (v3 meaning, pkg/ops/batch.go:197-259).
type Source[T any] struct {
	// Items is the full item list, or nil when Pages is used.
	Items []T
	// Pages streams items for sources too large to hold. A Snapshot source
	// that pages is drained once at start (the keys are frozen).
	Pages func(ctx context.Context, after string, limit int) (items []T, next string, err error)
	// Key returns an item's stable, unique id. Required: the snapshot, the
	// ledger and the journal are keyed by it.
	Key func(T) string
	// Order says whether a chunk-ledger resume is sound. Zero = Snapshot.
	Order SourceOrder
	// Total may be Unknown().
	Total Total

	dirty *dirtySpec[T]
}

// SourceOrder is what a source promises about where new items appear.
type SourceOrder uint8

const (
	// Snapshot freezes the item keys at start and chunks that frozen order.
	// Always safe; required with PartitionBy.
	Snapshot SourceOrder = iota + 1
	// AppendOnly: new items only ever sort AFTER existing ones, in ascending
	// key order. The ledger records completed key ranges, not chunk numbers.
	AppendOnly
	// Unordered: items can appear below the cursor. Resume restarts from zero;
	// ResumeContinue is refused at registration.
	Unordered
)

// String names the order ("snapshot", "append_only", "unordered"), or "unset".
func (o SourceOrder) String() string {
	switch o {
	case Snapshot:
		return "snapshot"
	case AppendOnly:
		return "append_only"
	case Unordered:
		return "unordered"
	default:
		return "unset"
	}
}

type dirtySpec[T any] struct {
	prefix string
	load   func(ctx context.Context, ids []string) ([]T, error)
}

// DirtySet is a Source over a durable per-subject dirty set: keys
// <prefix><subjectID> inside the calling module's storage.Keyspace (never a
// raw prefix), whose value begins with an 8-byte big-endian mark epoch. The
// runner drains it a page at a time and, after an item succeeds in a Live
// run, deletes its entry only if the epoch is unchanged, so a re-mark during
// the item is not lost; a Preview run leaves every entry dirty. Order is
// Unordered. load must return exactly one item per id, in the order given.
func DirtySet[T any](prefix string, load func(ctx context.Context, ids []string) ([]T, error)) Source[T] {
	return Source[T]{Order: Unordered, Total: Unknown(), dirty: &dirtySpec[T]{prefix: prefix, load: load}}
}

// Dirty returns a DirtySet source's prefix and loader; ok is false for any
// other source. Accessor coined by F2-01 for the Batch runner.
func (s Source[T]) Dirty() (prefix string, load func(ctx context.Context, ids []string) ([]T, error), ok bool) {
	if s.dirty == nil {
		return "", nil, false
	}
	return s.dirty.prefix, s.dirty.load, true
}

// Total is an item count that may be unknown. Progress shows a rate and no
// percentage for an unknown total instead of a fake 0/0.
type Total struct {
	n     int64
	known bool
}

// Known is a counted total.
func Known(n int64) Total { return Total{n: n, known: true} }

// Unknown is a total the source cannot count cheaply.
func Unknown() Total { return Total{} }

// Value returns the total and whether it is known.
func (t Total) Value() (int64, bool) { return t.n, t.known }

// Summary is what a Batch's Finish receives: the counters folded by the
// chunk ledger. Only completed chunks are in it, so it is the same number a
// resume starts from.
type Summary struct {
	Done, Total, Failed, Skipped int64
	Counters                     map[string]int64
	// Marks are the keys items recorded, by name, from every completed chunk
	// of every attempt (a Fixer's applied rows).
	Marks                   map[string][]string
	ChunksDone, ChunksTotal int
	Canceled                bool
}

// Counter returns a counter (ItemRC.Count) or skipped:<reason>.
func (s Summary) Counter(name string) int64 { return s.Counters[name] }

// Marked returns the keys marked with name, sorted.
func (s Summary) Marked(name string) []string {
	out := slices.Clone(s.Marks[name])
	slices.Sort(out)
	return out
}

// Risk grades a Fixer row for the person approving it.
type Risk uint8

const (
	RiskLow Risk = iota + 1
	RiskMedium
	RiskHigh
)

// Row is what a person approves in a Fixer plan (v3 meaning,
// pkg/ops/fixer.go:20-80), with Subjects as entity references.
type Row struct {
	ID string `json:"id"`
	// Subjects names the entities the row is about. At apply they act as
	// ExclusiveRefs: rows whose subjects overlap never apply at once.
	Subjects []entity.Ref      `json:"subjects,omitempty"`
	Title    string            `json:"title,omitempty"`
	Current  map[string]string `json:"current,omitempty"`
	// Proposed must describe Apply's complete end state, in the same keys and
	// rendering as Current: once Apply has run, a fresh Evaluate of the row
	// returns Current equal to Proposed on every key.
	Proposed  map[string]string `json:"proposed,omitempty"`
	Reason    string            `json:"reason,omitempty"`
	Risk      Risk              `json:"risk,omitempty"`
	Class     string            `json:"class,omitempty"`
	Evidence  []string          `json:"evidence,omitempty"`
	Inputs    map[string]string `json:"inputs,omitempty"`
	Hold      string            `json:"hold,omitempty"`
	Retry     bool              `json:"retry,omitempty"`
	OwnerOnly bool              `json:"owner_only,omitempty"`
	State     json.RawMessage   `json:"state,omitempty"`
}

// Fingerprint hashes the row's Inputs as sorted, canonical JSON, plus the
// Hold unless it is a transient one (Retry). Apply refuses a row whose fresh
// fingerprint differs from the planned one (ErrChangedSincePlan).
func (r Row) Fingerprint() string {
	keys := make([]string, 0, len(r.Inputs))
	for k := range r.Inputs {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	pairs := make([][2]string, 0, len(keys)+1)
	for _, k := range keys {
		pairs = append(pairs, [2]string{k, r.Inputs[k]})
	}
	if r.Hold != "" && !r.Retry {
		pairs = append(pairs, [2]string{"\x00hold", r.Hold})
	}
	b, err := json.Marshal(pairs)
	if err != nil { // strings only; cannot fail, but never hash an empty input
		b = []byte(fmt.Sprint(pairs))
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// Guard returns a reason to skip a Fixer row, or "".
type Guard func(row Row) (skipReason string)
