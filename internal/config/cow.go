// file: internal/config/cow.go
// version: 1.0.0
// guid: 4756da87-076a-4b06-9c31-a597bc8fa275
// last-edited: 2026-11-21

package config

import (
	"fmt"
	"reflect"
	"sync/atomic"
	"testing"
)

// Copy-on-write config (docs/plans/platform-core/config-cow.md).
//
// The process-wide Config is published as an immutable snapshot behind an
// atomic pointer. Readers call Get and read fields off the pointer it returns;
// they never take a lock and never see a torn or half-written Config. Writers
// (Mutate, and after the F3-07 flip Project) build a NEW *Config under the
// existing write lock and swap the pointer. Nothing ever writes into a
// published snapshot: not its scalars, not its slices, not its maps.
//
// TRANSITIONAL (prototype branch only): the package-global AppConfig still
// exists so the packages the codemod has not reached yet keep compiling.
// Every write through Mutate updates both, and starts from AppConfig so a
// test that still assigns AppConfig directly is folded in at the next write.
// The full sweep deletes AppConfig and with it this paragraph.

// current is the published snapshot. It is never nil (init stores an empty
// Config) so Get needs no nil check at any call site.
var current atomic.Pointer[Config]

func init() { current.Store(new(Config)) }

// Get returns the published config snapshot. It is one atomic load: call it
// per use, or once at the top of a run when the run should see one consistent
// generation from start to finish.
//
// The snapshot is SHARED and IMMUTABLE. Never assign through it
// (config.Get().X = v), never take the address of one of its fields to write
// through, and never append to, sort, or index-assign one of its slices or
// maps. All three compile; internal/arch's TestConfigCOWNoWritesThroughGet
// refuses them, and under `go test` every publish checks that the previous
// snapshot was not changed in place (see seal). To change the config, call
// Mutate (or MutateChecked); to change it in a test, SetForTest.
func Get() *Config { return current.Load() }

// Source is a function that returns the current snapshot. A long-lived object
// that used to keep a *Config (and so kept reading whatever the global held)
// takes a Source instead and calls it once per operation; config.Get is the
// production Source, and a test passes a closure over its own Config.
type Source func() *Config

// Fixed returns a Source that always answers c. For tests and for callers
// that deliberately pin a run to one snapshot.
func Fixed(c *Config) Source { return func() *Config { return c } }

// publishLocked makes next the published snapshot. The caller holds mu for
// writing and bumps generation AFTER this returns (Mutate's deferred Add), so
// a reader that observes the new generation is guaranteed to Get next. next
// must be owned by the caller: nothing else may hold a reference into it.
func publishLocked(next *Config) {
	if testing.Testing() {
		checkSealLocked()
		sealLocked(next)
	}
	current.Store(next)
	// TRANSITIONAL: keep the legacy global in step, as a separate deep copy so
	// a not-yet-converted test that writes into AppConfig's maps or slices can
	// never reach the published snapshot.
	AppConfig = *next.Clone()
}

// mutateCOW is the copy-on-write body of Mutate: deep-copy the base, let fn
// change the copy, deep-copy the result again, publish it. Caller holds mu.
//
// Two copies, deliberately. The first keeps fn's in-place edits (c.M[k] = v,
// c.S[i] = v) off the snapshot readers hold. The second covers fn assigning a
// caller-owned value wholesale (*c = cfg): without it the published snapshot
// would share cfg's maps with a caller that may keep changing them. A write is
// a settings save, a boot step or a test set-up, so two reflective copies per
// write are cheap next to what the write itself costs.
//
// If fn panics nothing is published: readers keep the previous snapshot. (The
// pre-COW Mutate left whatever fn had written before the panic in place.)
func mutateCOW(fn func(*Config)) {
	// TRANSITIONAL: the base is the legacy global, so direct AppConfig writes
	// in unconverted tests are not lost. End state: Get().Clone().
	next := AppConfig.Clone()
	fn(next)
	publishLocked(next.Clone())
}

// SetForTest applies fn to a copy of the current snapshot and publishes the
// copy. It is the test replacement for `config.AppConfig.X = v`: fn runs
// under the write lock, so a background reader sees the old snapshot or the
// new one, never a half-written one.
//
// It writes the in-memory snapshot only: with a Facade installed (F3-07) it
// does NOT persist and cannot be refused, exactly as the direct assignment it
// replaces did not. A test that means "save this setting" calls Mutate.
//
// Test-only: internal/arch refuses it in non-test files outside
// internal/testutil.
func SetForTest(fn func(*Config)) {
	mu.Lock()
	defer mu.Unlock()
	defer generation.Add(1)
	mutateCOW(fn)
}

// ReplaceForTest publishes a deep copy of c as the whole config: the test
// replacement for `config.AppConfig = saved`. In-memory only, like SetForTest.
func ReplaceForTest(c Config) {
	SetForTest(func(dst *Config) { *dst = c })
}

// ── seal: catch in-place writes to a published snapshot, under go test ──────

// sealed is the published snapshot and a private deep copy of it, taken at
// publish time. Guarded by mu.
var sealed struct {
	snap *Config
	copy *Config
}

func sealLocked(next *Config) {
	sealed.snap = next
	sealed.copy = next.Clone()
}

// checkSealLocked panics when the snapshot about to be replaced no longer
// equals the copy taken when it was published: someone wrote into an
// immutable snapshot. The panic names the first field that differs; the
// writer is whoever last held that snapshot, not the caller of this publish.
func checkSealLocked() {
	if sealed.snap == nil {
		return
	}
	if path, ok := firstDiff(reflect.ValueOf(sealed.copy).Elem(), reflect.ValueOf(sealed.snap).Elem(), "Config"); !ok {
		panic(fmt.Sprintf("config: a published snapshot was written in place (%s differs from the value it was published with). "+
			"Snapshots from config.Get() are immutable: change the config with Mutate or SetForTest, and copy a slice or map before changing it", path))
	}
}

// CheckSealed reports an in-place write to the current snapshot without
// waiting for the next publish. A test suite that exercises background
// readers calls it at the end (t.Cleanup) to attribute a violation to itself.
func CheckSealed() (err error) {
	mu.RLock()
	defer mu.RUnlock()
	if !testing.Testing() || sealed.snap == nil {
		return nil
	}
	if path, ok := firstDiff(reflect.ValueOf(sealed.copy).Elem(), reflect.ValueOf(sealed.snap).Elem(), "Config"); !ok {
		return fmt.Errorf("config: the published snapshot was written in place: %s changed", path)
	}
	return nil
}

// firstDiff walks a and b (same type) and returns the path of the first
// difference, or ok=true when they are deeply equal. Unexported fields are
// compared with reflect.DeepEqual on the enclosing struct.
func firstDiff(a, b reflect.Value, path string) (string, bool) {
	switch a.Kind() {
	case reflect.Pointer, reflect.Interface:
		if a.IsNil() || b.IsNil() {
			return path, a.IsNil() == b.IsNil()
		}
		return firstDiff(a.Elem(), b.Elem(), path)
	case reflect.Struct:
		for i := 0; i < a.NumField(); i++ {
			if !a.Type().Field(i).IsExported() {
				continue
			}
			if p, ok := firstDiff(a.Field(i), b.Field(i), path+"."+a.Type().Field(i).Name); !ok {
				return p, false
			}
		}
		return path, true
	case reflect.Slice, reflect.Array:
		if a.Kind() == reflect.Slice && a.IsNil() != b.IsNil() {
			return path, false
		}
		if a.Len() != b.Len() {
			return path + " (length)", false
		}
		for i := 0; i < a.Len(); i++ {
			if p, ok := firstDiff(a.Index(i), b.Index(i), fmt.Sprintf("%s[%d]", path, i)); !ok {
				return p, false
			}
		}
		return path, true
	case reflect.Map:
		if a.IsNil() != b.IsNil() || a.Len() != b.Len() {
			return path, false
		}
		iter := a.MapRange()
		for iter.Next() {
			bv := b.MapIndex(iter.Key())
			if !bv.IsValid() {
				return fmt.Sprintf("%s[%v]", path, iter.Key()), false
			}
			if p, ok := firstDiff(iter.Value(), bv, fmt.Sprintf("%s[%v]", path, iter.Key())); !ok {
				return p, false
			}
		}
		return path, true
	default:
		return path, reflect.DeepEqual(a.Interface(), b.Interface())
	}
}
