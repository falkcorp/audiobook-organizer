// file: core/tasks/kind.go
// version: 1.0.0
// guid: b4521189-003b-4b3b-9172-d383b2c8b214
// last-edited: 2026-11-10

package tasks

import (
	"fmt"
	"strings"
)

// Kind is "<moduleID>.<name>". name is one or more dot-separated segments of
// [a-z][a-z0-9-]*; ':' is never allowed (it is the storage separator, as in
// v2 ValidateOpDef). Example: "books.dedup.full-scan", "core.backup.run".
type Kind string

// ParseKind checks s against the Kind grammar: at least two dot-separated
// segments (the module ID and a name), each matching [a-z][a-z0-9-]*. The
// module segment follows the same grammar here; that it names the
// registering module is checked by the kernel at registration.
func ParseKind(s string) (Kind, error) {
	if s == "" {
		return "", fmt.Errorf("tasks: kind is empty")
	}
	segs := strings.Split(s, ".")
	if len(segs) < 2 {
		return "", fmt.Errorf("tasks: kind %q needs <moduleID>.<name>", s)
	}
	for i, seg := range segs {
		if !kindSegment(seg) {
			return "", fmt.Errorf("tasks: kind %q: segment %d (%q) does not match [a-z][a-z0-9-]*", s, i+1, seg)
		}
	}
	return Kind(s), nil
}

// kindSegment reports whether seg matches [a-z][a-z0-9-]*.
func kindSegment(seg string) bool {
	if seg == "" || seg[0] < 'a' || seg[0] > 'z' {
		return false
	}
	for i := 1; i < len(seg); i++ {
		c := seg[i]
		if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '-' {
			return false
		}
	}
	return true
}

// Module is the text before the first '.' (the owning module's ID, or
// "core").
func (k Kind) Module() string {
	m, _, _ := strings.Cut(string(k), ".")
	return m
}

// Shape is a definition's runner: Task, Batch or Fixer.
type Shape uint8

const (
	ShapeTask  Shape = iota + 1 // one function, runs once
	ShapeBatch                  // source + per-item function; the runner owns the loop
	ShapeFixer                  // plan -> approve -> apply over rows
	// These three are the only shapes (A7): a DAG of definitions is a
	// future additive API change, not a fourth runner.
)

// String names the shape ("task", "batch", "fixer"), or "unset".
func (s Shape) String() string {
	switch s {
	case ShapeTask:
		return "task"
	case ShapeBatch:
		return "batch"
	case ShapeFixer:
		return "fixer"
	default:
		return "unset"
	}
}
