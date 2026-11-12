// file: internal/repairs/stores.go
// version: 1.0.0
// guid: 5384bbeb-3b73-4c5d-a69c-7c06059bde7e
// last-edited: 2026-11-12

package repairs

import "context"

// The stores a fixer reads through come from the shell that runs it, not from
// anything the fixer captured when it was built (05-tasks §5T.8.2 conversion
// rule 3). The shell puts them on the context of every Plan, Replan and Apply
// call; the fixer takes them from there.
//
// The set itself is declared by the package that owns the fixers
// (internal/plugins/maintenance.FixerStores): its result types are that
// package's interfaces, and repairs cannot import it. repairs therefore
// carries the set opaquely and type-keyed: WithStores and StoresFrom are
// generic over it, so a caller that asks for the wrong type gets "absent",
// never a value of another type.

// storesKey is the context key of a stores value of type S. Each S has its
// own key, so two stores types never see each other's value.
type storesKey[S any] struct{}

// WithStores returns ctx carrying s as the fixer stores of type S. A nil s
// (a nil interface value) carries nothing: StoresFrom reports it absent.
func WithStores[S any](ctx context.Context, s S) context.Context {
	return context.WithValue(ctx, storesKey[S]{}, s)
}

// StoresFrom returns the fixer stores of type S on ctx. ok is false when none
// was put there (or the value was a nil interface): the caller must refuse,
// never fall back to a store it captured itself.
func StoresFrom[S any](ctx context.Context) (s S, ok bool) {
	if ctx == nil {
		return s, false
	}
	s, ok = ctx.Value(storesKey[S]{}).(S)
	return s, ok
}
