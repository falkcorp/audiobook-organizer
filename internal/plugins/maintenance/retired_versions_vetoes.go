// file: internal/plugins/maintenance/retired_versions_vetoes.go
// version: 1.0.0
// guid: d3a96b50-1e87-4f2c-8a64-b05c7e19f3d2
// last-edited: 2026-10-23

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
// replan run: OwnerVetoes() hands a fresh one to each run), whose stored ids
// are resolved through the book resolver, so a reject naming a book that was
// merged away, retired or purged still holds.
//
// Load can be called up front to refuse a run whose rejects cannot be read; a
// query before Load loads on demand. A failed load is remembered and every
// query returns it: never "no veto".
type ownerVetoReader struct {
	store   retiredVetoReaderStore
	retired database.RetiredBookFileReader
	// exact reads the rejects with NO id resolution: the pair as named, which
	// a resolved snapshot cannot answer once the ends resolve to one book.
	exact *decisions.Store
	kv    decisions.KV

	once sync.Once
	v    *decisions.Vetoes
	err  error
}

var _ OwnerVetoReader = (*ownerVetoReader)(nil)

// NewOwnerVetoReader builds the owner-veto reader over store. It is nil (the
// fixer then refuses to plan) when the store cannot serve the decisions KV or
// the retired-version index, which a non-Pebble backend cannot.
func NewOwnerVetoReader(store database.Store) OwnerVetoReader {
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
	return &ownerVetoReader{store: store, retired: retired, exact: decisions.New(kv), kv: kv}
}

// Load reads the rejects and the journals the book resolver needs, once.
func (o *ownerVetoReader) Load(ctx context.Context) error {
	o.once.Do(func() { o.v, o.err = o.load(ctx) })
	return o.err
}

func (o *ownerVetoReader) load(ctx context.Context) (*decisions.Vetoes, error) {
	follows, err := merge.FlagHolderFollowsFrom(o.store)
	if err != nil {
		return nil, fmt.Errorf("read the merge journals for the book resolver: %w", err)
	}
	v, err := decisions.New(o.kv).Load(ctx, newBookIDResolver(o.store, o.retired, follows))
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

// PairVetoed is true when the owner rejected joining a and b in family, either
// as the ids are named (the unresolved lookup) or after both ends are resolved
// to the book that survives (the snapshot).
//
// BOTH are asked on purpose. A shell that was merged into its target resolves
// to that target, and the snapshot then sees one book, not a pair, and answers
// false ("merged anyway"). The retire of that shell onto that target is
// exactly the join the owner rejected, so the exact lookup stands as well: a
// veto on the very pair held the row before the resolver existed, and still
// does.
func (o *ownerVetoReader) PairVetoed(entityType, a, b, family string) (bool, error) {
	v, err := o.snapshot()
	if err != nil {
		return false, err
	}
	hit, err := v.PairVetoed(entityType, a, b, family)
	if err != nil || hit {
		return hit, err
	}
	return o.exact.PairVetoed(entityType, a, b, family)
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

// newBookIDResolver is the IDResolver decisions requires for book ids: the
// retired-version index (a shell resolves to the book that holds its files),
// then the merge survivor, over and over until the id stops moving. Safe for
// concurrent use (a snapshot is queried by every plan worker), and memoised
// per resolver, so each distinct id costs its reads once per snapshot.
func newBookIDResolver(store retiredVetoReaderStore, retired database.RetiredBookFileReader, follows map[string]merge.FlagFollow) decisions.IDResolver {
	chain := decisions.ChainResolvers(
		decisions.RetiredBookResolver(retiredBookReader{store, retired}),
		mergeSurvivorResolver(store, follows),
	)
	var memo sync.Map // entityType + "\x00" + id -> string
	return func(entityType, id string) (string, error) {
		k := entityType + "\x00" + id
		if got, ok := memo.Load(k); ok {
			return got.(string), nil
		}
		out, err := chain(entityType, id)
		if err != nil {
			return "", err
		}
		memo.Store(k, out)
		return out, nil
	}
}

// mergeSurvivorResolver resolves a book to the live book its work went into
// (merge.ResolveMergeSurvivor). A book with no live survivor is NOT an error
// here: a trashed shell that was never merged, and a purged id with no trace,
// are ordinary and resolve to themselves. When the full chain ends nowhere
// live (merged into a book that was then purged) the one direct hop of
// MergedIntoBookID is taken, so the chain can go on from that book through the
// retired-version index. A read error propagates.
func mergeSurvivorResolver(store retiredVetoReaderStore, follows map[string]merge.FlagFollow) decisions.IDResolver {
	return func(entityType, id string) (string, error) {
		if entityType != retiredEntityBook {
			return id, nil
		}
		surv, err := merge.ResolveMergeSurvivor(store, id, follows)
		if err == nil {
			return surv, nil
		}
		if !errors.Is(err, merge.ErrNoLiveSurvivor) {
			return "", fmt.Errorf("resolve book %s: %w", id, err)
		}
		b, err := store.GetBookByID(id)
		if err != nil {
			return "", fmt.Errorf("resolve book %s: %w", id, err)
		}
		if b != nil && b.MergedIntoBookID != nil && *b.MergedIntoBookID != "" {
			return *b.MergedIntoBookID, nil
		}
		return id, nil
	}
}

// recordRetiredVersionApplied writes the `applied` decision record of one
// retired-versions row. The Apply calls it after the row's retire has
// committed, with the store it already holds (fixer.p.deps.OpsStore()). The
// record is a label: it vetoes nothing. A retry that finds the same record
// already there writes nothing (decisions.RecordRetiredApplied).
func recordRetiredVersionApplied(store any, ev decisions.RetiredVersionsEvent) (*decisions.Record, error) {
	rec, err := retiredDecisionRecorder(store)
	if err != nil {
		return nil, err
	}
	return decisions.RecordRetiredApplied(rec, ev)
}

// recordRetiredVersionUndone writes the `undone` record of a retired-versions
// row that a revert took back (a label, not a veto: the shell stays plannable).
// The revert path calls it once per reverted row.
func recordRetiredVersionUndone(store any, ev decisions.RetiredVersionsEvent) (*decisions.Record, error) {
	rec, err := retiredDecisionRecorder(store)
	if err != nil {
		return nil, err
	}
	return decisions.RecordRetiredUndone(rec, ev)
}

func retiredDecisionRecorder(store any) (decisions.Recorder, error) {
	kv, ok := database.AsCapability[decisions.KV](store)
	if !ok {
		return nil, fmt.Errorf("%s: the store cannot serve the decisions log", retiredVersionsFixerID)
	}
	return decisions.New(kv), nil
}
