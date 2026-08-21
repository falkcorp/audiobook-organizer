// file: internal/autorevertprobe/probe.go
// version: 1.0.0
// guid: 7c1e9a44-3b52-4f18-9d6a-2e0c5f8b71a3
// last-edited: 2026-08-20

// Package autorevertprobe exists only to prove that auto-revert fires on a red
// main. It contains a deliberate compile error and is expected to be reverted
// automatically within minutes of landing. If you are reading this on main,
// auto-revert did NOT work — delete this package by hand and say so in #2649.
package autorevertprobe

// DeliberateCompileError does not compile: it declares a string return and
// returns an int. `go build ./...` fails here, which is the whole point.
func DeliberateCompileError() string {
	return 42
}
