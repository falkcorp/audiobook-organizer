// file: internal/books/cutover/reach_export_test.go
// version: 0.1.0
// guid: 7d091636-e9a3-4307-8aa4-0c01f9f83a6c
// last-edited: 2026-11-22

package cutover

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
