// file: internal/books/cutover/m6/effective_operator_export_test.go
// version: 1.0.0
// guid: 3b7f1c52-9d04-4e68-a1b3-5c2e8f60d417
// last-edited: 2026-11-28

package m6

import "github.com/falkcorp/mediacore/config"

// OperatorField is operatorField for the pin tests in m6_test (which import
// cutover and so cannot be internal tests): the ref and settings field an
// operator path lands in.
func OperatorField(p config.Path) (ref, field string, ok bool) {
	r, field, ok := operatorField(p)
	return r.String(), field, ok
}

// AccountedFor reports whether the builder either maps p or lists it as
// deliberately not a URL setting.
func AccountedFor(p config.Path) bool {
	if _, ok := operatorURLPaths[p]; ok {
		return true
	}
	_, ok := notURLPaths[p]
	return ok
}

// URLPaths are the mapped operator URL paths.
func URLPaths() []config.Path {
	var out []config.Path
	for p := range operatorURLPaths {
		out = append(out, p)
	}
	return out
}

// URLPathsAndNotURLPaths are both tables' paths, for asserting each is a real
// passthrough path.
func URLPathsAndNotURLPaths() []config.Path {
	var out []config.Path
	for p := range operatorURLPaths {
		out = append(out, p)
	}
	for p := range notURLPaths {
		out = append(out, p)
	}
	return out
}
