// file: core/archtest/tasklint/testdata/src/github.com/falkcorp/mediacore/tasks/tasks.go
// version: 2.0.0
// guid: 58dce002-9f63-42ed-8cdc-46602a9b7216
// last-edited: 2026-11-10

// Package tasks is a minimal stub of core/tasks for the tasklint
// analysistest fixtures: the same import path and the same names the
// analyzer resolves (05-tasks §5T.3.1).
package tasks

type Kind string

type Shape uint8

type Definition interface {
	Kind() Kind
	Shape() Shape
}

type def struct{ kind Kind }

func (d def) Kind() Kind   { return d.kind }
func (d def) Shape() Shape { return 1 }

type Resource string

type Effects struct{ writes, deletes []Resource }

func ReadOnly(reads ...Resource) Effects { return Effects{} }

func Writes(ws ...Resource) Effects { return Effects{writes: ws} }

func (e Effects) AlsoDeletes(rs ...Resource) Effects { e.deletes = rs; return e }

type ResumePolicy uint8

const (
	ResumeContinue ResumePolicy = iota + 1
	ResumeFromZero
)

type Common struct {
	Title   string
	Help    string
	Effects Effects
	Resume  ResumePolicy
}

type Parallelism struct {
	n      int
	reason string
}

func CPU() Parallelism                       { return Parallelism{n: 1} }
func Fixed(n int, reason string) Parallelism { return Parallelism{n: n, reason: reason} }
func Sequential(reason string) Parallelism   { return Parallelism{n: 1, reason: reason} }

type Writer interface{ Do(name string) error }

type RC struct{}

func (rc *RC) Writer() Writer         { return nil }
func (rc *RC) Checkpoint(v any) error { return nil }

type ItemRC struct{}

func (ic *ItemRC) Writer() Writer { return nil }

type Row struct{ ID string }

type Summary struct{}

type Source[T any] interface{ Next() (T, bool) }

type TaskSpec[P, R any] struct {
	Common
	Run func(rc *RC, p P) (R, error)
}

type BatchSpec[P, T, R any] struct {
	Common
	Source      func(rc *RC, p P) (Source[T], error)
	Item        func(ic *ItemRC, p P, item T) error
	Finish      func(rc *RC, p P, s Summary) (R, error)
	Parallelism Parallelism
}

type FixerSpec[P, C any] struct {
	Common
	Candidates  func(rc *RC, p P) (Source[C], error)
	Evaluate    func(ic *ItemRC, p P, c C) (Row, error)
	Apply       func(ic *ItemRC, w Writer, p P, row Row) error
	AfterApply  func(rc *RC, applied []string) error
	Parallelism Parallelism
}

func Task[P, R any](kind Kind, s TaskSpec[P, R]) Definition { return def{kind} }

func Batch[P, T, R any](kind Kind, s BatchSpec[P, T, R]) Definition { return def{kind} }

func Fixer[P, C any](kind Kind, s FixerSpec[P, C]) Definition { return def{kind} }

type NoParams struct{}

type NoResult struct{}
