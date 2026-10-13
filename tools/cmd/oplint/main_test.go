// file: tools/cmd/oplint/main_test.go
// version: 1.0.0
// guid: 2ffa8310-5813-460b-a1e7-fac54313784a
// last-edited: 2026-10-13

package main

import (
	"strings"
	"testing"
)

func depsViolations(t *testing.T, dir string) []string {
	t.Helper()
	var all []string
	if err := walkDir(dir, &all); err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, v := range all {
		if strings.Contains(v, depsStoreRuleID) {
			out = append(out, v)
		}
	}
	return out
}

func TestDepsStoreRule(t *testing.T) {
	bad := depsViolations(t, "testdata/deps/bad")
	want := []string{"Deps.Store ", "Deps.Wide ", "Deps.Wider ", "Deps.Inline "}
	if len(bad) != len(want) {
		t.Fatalf("got %d violations, want %d:\n%s", len(bad), len(want), strings.Join(bad, "\n"))
	}
	for _, w := range want {
		found := false
		for _, v := range bad {
			if strings.Contains(v, w) {
				found = true
			}
		}
		if !found {
			t.Errorf("no violation for %s in:\n%s", w, strings.Join(bad, "\n"))
		}
	}
	if good := depsViolations(t, "testdata/deps/good"); len(good) != 0 {
		t.Fatalf("narrow interfaces flagged:\n%s", strings.Join(good, "\n"))
	}
}
