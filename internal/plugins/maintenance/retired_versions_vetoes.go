// file: internal/plugins/maintenance/retired_versions_vetoes.go
// version: 1.1.1
// guid: d3a96b50-1e87-4f2c-8a64-b05c7e19f3d2
// last-edited: 2026-10-28

package maintenance

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/decisions"
	"github.com/falkcorp/audiobook-organizer/internal/merge"
)

// retiredVetoReaderStore is what the owner-veto reader needs of the store: the
// book read the resolvers make and the combine/sibling journal prefix scan the
// merge survivor's follows come from. database.Store carries both.
type retiredVetoReaderStore interface {
	GetBookByID(id string) (*database.Book, error)
	ScanPrefix(prefix string) ([]database.KVPair, error)
}

// retiredBookReader joins the two reads decisions.RetiredBookResolver needs:
// the book (a live one is never redirected) and the retired-version index.
type retiredBookReader struct {
	retiredVetoReaderStore
	database.RetiredBookFileReader
}

// ownerVetoReader answers the retired-versions plan's two questions about the
// owner's rejects: is this shell's proposal rejected (SubjectRejected), and is
// this pair of books vetoed (PairVetoed). It answers from ONE point-in-time
// decisions.Vetoes snapshot, loaded once per reader (so once per plan or
// replan run: OwnerVetoes() hands a fresh one to each run). Every stored id of
// the snapshot is resolved through the book path resolver, and the veto keeps
// the whole path of each end, so a reject naming a book that was merged away,
// retired, absorbed or purged still holds, including against the book it was
// merged or retired into (decisions.Vetoes.PairVetoed explains the rule).
//
// An id the resolver cannot settle (a shell whose retired versions name two
// holders, a merge chain that loops) does not fail the load: the veto that
// holds it is held per id, and a query that touches it gets a
// *decisions.UnresolvableError naming every such id. Unresolved lists them all.
//
// Load can be called up front to refuse a run whose rejects cannot be READ; a
// query before Load loads on demand. A failed load is remembered and every
// query returns it: never "no veto".
type ownerVetoReader struct {
	store   retiredVetoReaderStore
	retired database.RetiredBookFileReader
	kv      decisions.KV
	rows    *decisions.RowCache

	once sync.Once
	v    *decisions.Vetoes
	err  error
}

var _ OwnerVetoReader = (*ownerVetoReader)(nil)

// NewOwnerVetoReader builds the owner-veto reader over store. rows (nil is
// fine) lets the readers a long-lived owner builds share one scan of the
// decision log while no record is written (decisions.RowCache); the ids are
// still resolved afresh by every reader. The result is nil (the fixer then
// refuses to plan) when the store cannot serve the decisions KV or the
// retired-version index, which a non-Pebble backend cannot.
func NewOwnerVetoReader(store database.Store, rows *decisions.RowCache) OwnerVetoReader {
	if store == nil {
		return nil
	}
	kv, ok := database.AsCapability[decisions.KV](store)
	if !ok {
		return nil
	}
	retired, ok := database.AsCapability[database.RetiredBookFileReader](store)
	if !ok {
		return nil
	}
	return &ownerVetoReader{store: store, retired: retired, kv: kv, rows: rows}
}

// Load reads the rejects and the journals the book resolver needs, once. The
// ctx of the first call is the one the read runs under (the plan's).
func (o *ownerVetoReader) Load(ctx context.Context) error {
	o.once.Do(func() { o.v, o.err = o.load(ctx) })
	return o.err
}

func (o *ownerVetoReader) load(ctx context.Context) (*decisions.Vetoes, error) {
	follows, err := merge.FlagHolderFollowsFrom(o.store)
	if err != nil {
		return nil, fmt.Errorf("read the merge journals for the book resolver: %w", err)
	}
	var opts []decisions.Option
	if o.rows != nil {
		opts = append(opts, decisions.WithRowCache(o.rows))
	}
	v, err := decisions.New(o.kv, opts...).LoadPaths(ctx, newBookPathResolver(o.store, o.retired, follows))
	if err != nil {
		return nil, fmt.Errorf("load the owner's rejects: %w", err)
	}
	return v, nil
}

func (o *ownerVetoReader) snapshot() (*decisions.Vetoes, error) {
	if err := o.Load(context.Background()); err != nil {
		return nil, err
	}
	return o.v, nil
}

// PairVetoed is true when the owner rejected joining a and b in family. There
// is one lookup, in the snapshot: a veto whose ends were merged or retired
// into one book is kept with its paths, so it is found against that book
// without a second look at the names the veto was recorded under.
//
// The error is a *decisions.UnresolvableError when a or b cannot be resolved,
// or when a veto that touches them has an end that cannot; the caller holds
// the row for it rather than treating it as "no veto".
func (o *ownerVetoReader) PairVetoed(entityType, a, b, family string) (bool, error) {
	v, err := o.snapshot()
	if err != nil {
		return false, err
	}
	return v.PairVetoed(entityType, a, b, family)
}

// SubjectRejected is true when a live reject of the proposal (opKind,
// subjectKey) blocks it (materialHash "" is the sticky ask: any live reject).
func (o *ownerVetoReader) SubjectRejected(opKind, subjectKey, materialHash string) (bool, error) {
	v, err := o.snapshot()
	if err != nil {
		return false, err
	}
	return v.SubjectRejected(opKind, subjectKey, materialHash)
}

// Unresolved lists every stored id the snapshot could not resolve, for the
// plan to report once. It is empty before Load, and after a failed one.
func (o *ownerVetoReader) Unresolved() []decisions.UnresolvedID {
	if o.v == nil {
		return nil
	}
	return o.v.Unresolved()
}

// newBookPathResolver is the decisions.PathResolver for book ids: the retired-
// version index (a shell resolves to the book that holds its files), then the
// merge survivor, over and over until the id stops moving. Safe for concurrent
// use (a snapshot is queried by every plan worker). It memoises successes only,
// per resolver, so each distinct id costs its reads once per snapshot; a
// failure is asked again, so a read that failed once is not remembered as the
// answer. The paths it returns are shared and must not be modified.
func newBookPathResolver(store retiredVetoReaderStore, retired database.RetiredBookFileReader, follows map[string]merge.FlagFollow) decisions.PathResolver {
	chain := decisions.ChainPathResolvers(
		decisions.PathOf(decisions.RetiredBookResolver(retiredBookReader{store, retired})),
		mergeSurvivorPathResolver(store, follows),
	)
	var memo sync.Map // entityType + "\x00" + id -> []string
	return func(entityType, id string) ([]string, error) {
		k := entityType + "\x00" + id
		if got, ok := memo.Load(k); ok {
			return got.([]string), nil
		}
		path, err := chain(entityType, id)
		if err != nil {
			return nil, err
		}
		memo.Store(k, path)
		return path, nil
	}
}

// mergeSurvivorPathResolver resolves a book to the live book its work went
// into, with the books it went through (merge.ResolveMergeSurvivorPath). A
// book with no live survivor is NOT an error here: a trashed shell that was
// never merged, and a purged id with no trace, are ordinary and resolve to
// themselves. When the full chain ends nowhere live (merged into a book that
// was then purged) the one direct hop of MergedIntoBookID is taken, so the
// chain can go on from that book through the retired-version index. A read
// error propagates.
func mergeSurvivorPathResolver(store retiredVetoReaderStore, follows map[string]merge.FlagFollow) decisions.PathResolver {
	return func(entityType, id string) ([]string, error) {
		if entityType != retiredEntityBook {
			return []string{id}, nil
		}
		path, err := merge.ResolveMergeSurvivorPath(store, id, follows)
		if err == nil {
			return path, nil
		}
		if !errors.Is(err, merge.ErrNoLiveSurvivor) {
			return nil, fmt.Errorf("resolve book %s: %w", id, err)
		}
		b, err := store.GetBookByID(id)
		if err != nil {
			return nil, fmt.Errorf("resolve book %s: %w", id, err)
		}
		if b != nil && b.MergedIntoBookID != nil && *b.MergedIntoBookID != "" && *b.MergedIntoBookID != id {
			return []string{id, *b.MergedIntoBookID}, nil
		}
		return []string{id}, nil
	}
}

// recordRetiredVersionApplied writes the `applied` decision record of one
// retired-versions row. The Apply calls it (recordApplied) after the row's
// retire has committed, with the store it already holds
// (fixer.p.deps.OpsStore()). The revert writes the `undone` record itself
// (audiobooks.RevertService.recordRetireUndone), as the undo does not reach
// into this package. The record is a label: it vetoes nothing. A retry that finds the same record
// already there writes nothing (decisions.RecordRetiredApplied).
func recordRetiredVersionApplied(store any, ev decisions.RetiredVersionsEvent) (*decisions.Record, error) {
	rec, err := retiredDecisionRecorder(store)
	if err != nil {
		return nil, err
	}
	return decisions.RecordRetiredApplied(rec, ev)
}

func retiredDecisionRecorder(store any) (decisions.Recorder, error) {
	kv, ok := database.AsCapability[decisions.KV](store)
	if !ok {
		return nil, fmt.Errorf("%s: the store cannot serve the decisions log", retiredVersionsFixerID)
	}
	return decisions.New(kv), nil
}
