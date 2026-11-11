// file: tools/cmd/oplint/testdata/src/github.com/falkcorp/audiobook-organizer/pkg/ops/ops.go
// version: 1.0.0
// guid: 58dce002-9f63-42ed-8cdc-46602a9b7216
// last-edited: 2026-10-16

// Package ops is a minimal stub of pkg/ops for the oplint analysistest
// fixtures: the same import path and the same names the analyzer resolves.
package ops

type Definition interface{ ID() string }

type def struct{ id string }

func (d def) ID() string { return d.id }

type ServiceKey string

type Bundle struct {
	Plugin   string
	Defs     []Definition
	Requires []ServiceKey
}

type Resource string

type Effects struct{ writes []Resource }

func Writes(rs ...Resource) Effects { return Effects{writes: rs} }

func Deletes(rs ...Resource) Resource { return "delete:" }

type Common struct {
	Title      string
	Help       string
	Effects    Effects
	Permission string
}

type Concurrency struct{ reason string }

func Sequential(why string) Concurrency { return Concurrency{reason: why} }

type Writer interface{ Effect() error }

type Deleter interface{ Delete(id string) error }

type RC struct{}

func (rc *RC) Writer() Writer { return nil }

func (rc *RC) Deleter() Deleter { return nil }

type ItemRC struct{ RC }

type NoParams struct{}

type NoResult struct{}

type Row struct{}

type TaskSpec[P any, R any] struct {
	Common
	Run func(rc *RC, p P) (R, error)
}

type BatchSpec[P any, T any, R any] struct {
	Common
	Item        func(rc *ItemRC, p P, item T) error
	Concurrency Concurrency
}

type FixerSpec[P any, C any] struct {
	Common
	Apply      func(rc *ItemRC, w Writer, p P, row Row) error
	AfterApply func(rc *RC, applied []string) error
}

type PipelineSpec[P any] struct{ Common }

func Task[P any, R any](id string, s TaskSpec[P, R]) Definition { return def{id} }

func Batch[P any, T any, R any](id string, s BatchSpec[P, T, R]) Definition { return def{id} }

func Fixer[P any, C any](id string, s FixerSpec[P, C]) Definition { return def{id} }

func Pipeline[P any](id string, s PipelineSpec[P]) Definition { return def{id} }
