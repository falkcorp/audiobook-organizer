// file: internal/books/cutover/reach_export_test.go
// version: 0.1.0
// guid: 7d091636-e9a3-4307-8aa4-0c01f9f83a6c
// last-edited: 2026-11-22

package cutover

import (
	"maps"
	"slices"
)

// The registered-path harness and its cells live in package cutover_test (the
// external test package, because books imports cutover). The F4-07 obligation
// registry lives here. This file is the one bridge between them: an exported
// name, visible only to this package's tests, through which the external
// package's discharge tests register what they cover.

// RegisterReachDischarge registers the discharge of obligation (a row of
// reachObligations) by the test function named test, which must live in this
// directory, assert something and never skip. covers lists the cases the test
// declares it covers, in the unit the row counts; a row with exact set must be
// covered exactly. Call it from an init in the file that holds the test.
func RegisterReachDischarge(obligation, test string, covers func() []string) {
	reachDischarges = append(reachDischarges, reachDischarge{Obligation: obligation, Test: test, Covers: covers})
}

// ReachPinnedCells are the 11 cells the removed evaluator's handler-admin pin hid:
// route / principal, with HEAD's letter and the router's.
func ReachPinnedCells() []string { return slices.Clone(reachPinnedCells) }

// ReachHandlerRefusalCells are the routes of the eight auth-off cells the router
// admits and R0's handler refused.
func ReachHandlerRefusalCells() []string { return slices.Clone(reachHandlerRefusalCells) }

// ReachWantR2OnlyVerdicts pins what each route R0 never recorded admits at stage
// 13, in the order of verdicts.json's principals.
func ReachWantR2OnlyVerdicts() map[string]string { return maps.Clone(reachWantR2OnlyVerdicts) }
