// file: internal/books/task_ledger.go
// version: 0.1.0
// guid: 5c0e7a14-93d2-4b6f-8e1a-2f7d4b9c6a30
// last-edited: 2026-11-28

package books

import "github.com/falkcorp/mediacore/tasks"

// Manifest is pure and runs before the store opens and before Register, but
// Manifest.TaskLedger must name every kind Register hands Registrar.Tasks (core
// refuses a registered kind the ledger lacks, ProblemTaskLedger). The kinds come
// from bookTaskLists, which cannot be called from Manifest: building the lists
// constructs the repairs registry's fixers, which create the HEAD ops recorder
// (opsmetrics.Default) that Register's opsmetrics.Retire must run before, and
// Manifest runs earlier. So the ledger is a list in source,
// task_ledger_kinds.go, and TestTasksRegister_LedgerEqualsTheListsKinds fails
// the day it differs from the kinds bookTaskLists returns, in either direction.
// A kind a later slice adds to bookTaskLists is added here with
//
//	go test ./internal/books -run TestTasksRegister_LedgerEqualsTheListsKinds -update-task-ledger
//
// which rewrites task_ledger_kinds.go from the lists.

// taskLedger is the kind of every definition bookTaskLists returns, sorted.
func taskLedger() []tasks.Kind { return taskLedgerKinds }
