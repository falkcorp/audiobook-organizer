// file: core/tasks/taskstest/parity/cases.go
// version: 1.0.0
// guid: 96ed053d-7591-4c1c-bda8-27ea1998cd6f
// last-edited: 2026-11-10

// Package parity ports HEAD's resume, cancel and shutdown test suites as
// behaviour oracles for core's task executor. Each Case names the HEAD test
// it descends from; Excluded names the ones with no core equivalent.
package parity

import "testing"

// Case is one ported HEAD test.
type Case struct {
	Name     string
	Head     string // "file.go:TestFunc"
	Setup    string
	Action   string
	Expected string
	Run      func(t *testing.T, k *Kit)
}

// Cases returns every ported case, in HEAD's file order.
func Cases() []Case {
	var out []Case
	out = append(out, resumeCases()...)
	out = append(out, supersedeCases()...)
	out = append(out, itemsCases()...)
	out = append(out, cancelCases()...)
	out = append(out, shutdownCases()...)
	return out
}
