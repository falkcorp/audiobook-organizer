// file: core/module/resetplan.go
// version: 1.0.0
// guid: 403f7dd9-fea5-4e57-be9a-de05213b4002
// last-edited: 2026-11-08

package module

import "context"

// resetPlanKey carries the reset hook's storage into ResetHook.Plan.
type resetPlanKey struct{}

// resetKeptKey carries rule (4) of the reset hook into ResetHook.Plan.
type resetKeptKey struct{}

// ResetPlanStorage is the storage of the module that declares
// Manifest.ResetHook, opened read-only, during its ResetHook.Plan in the
// offline storage reset (05-storage §5S.3.15 "Stage"): Plan reads the
// settings it plans from (ABO's books: root_dir and the Open Library dump
// directory) here, because the offline command boots no module. ok is false
// when the store did not open read-only (a store the guard refuses, one with
// pending migration steps, an empty one) and in every other context; Plan
// then plans from what it has, and the kernel refuses a library wipe it
// cannot confirm. Release R1 only: from F4-05 the route stages from the
// running module, whose Plan reads its own Deps.Storage.
func ResetPlanStorage(ctx context.Context) (h StorageHandles, ok bool) {
	h, ok = ctx.Value(resetPlanKey{}).(StorageHandles)
	return h, ok
}

// WithResetPlanStorage is the kernel's: the context the offline reset calls
// ResetHook.Plan with. An app never calls it.
func WithResetPlanStorage(ctx context.Context, h StorageHandles) context.Context {
	return context.WithValue(ctx, resetPlanKey{}, h)
}

// ResetPlanKept is rule (4) of the reset hook (05-storage §5S.3.15), during
// its ResetHook.Plan in the offline storage reset: kept(p) reports a path
// the confined filesystem refuses to remove at Run (storage.ErrKeptPath:
// one that is or holds <db parent> or the state directory, or is, holds or
// lies inside the swap's directories or the live store and its sidecars).
// A preflight leaves those out of the count it confirms, so the count is
// what the wipe removes. The kernel sets it whether or not the store
// opened. ok is false in every other context; Plan then counts every entry.
// Release R1 only: from F4-05 the route sets it for the running module.
func ResetPlanKept(ctx context.Context) (kept func(path string) bool, ok bool) {
	kept, ok = ctx.Value(resetKeptKey{}).(func(path string) bool)
	return kept, ok && kept != nil
}

// WithResetPlanKept is the kernel's: the context the offline reset calls
// ResetHook.Plan with carries its rule (4). An app never calls it.
func WithResetPlanKept(ctx context.Context, kept func(path string) bool) context.Context {
	return context.WithValue(ctx, resetKeptKey{}, kept)
}
