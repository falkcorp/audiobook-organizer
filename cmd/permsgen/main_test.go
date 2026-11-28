// file: cmd/permsgen/main_test.go
// version: 0.1.0
// guid: 2d7104cc-8401-4516-8cb4-f92774773447
// last-edited: 2026-11-21

package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/books"
)

// TestGenerate_EveryAtomScopeIsEmitted: a scope the atom table names reaches
// the generated file under its object, and an API-key scope list carries it.
func TestGenerate_EveryAtomScopeIsEmitted(t *testing.T) {
	out := string(generate(books.AtomTable()))
	for _, a := range books.AtomTable() {
		for _, s := range a.Scopes {
			if got := strings.Count(out, "'"+string(s)+"'"); got != 2 {
				t.Errorf("scope %s appears %d times (want 2: its object and API_KEY_SCOPES)", s, got)
			}
		}
	}
	for _, want := range []string{
		"BOOKS_PERMISSIONS = {",
		"CORE_PERMISSIONS = {",
		"  ORGANIZE: 'books:organize',",
		"  SCAN: 'books:scan',",
		"  DELETE: 'books:delete',",
		"  COLLECTIONS_MANAGE: 'books:collections.manage',",
		"  REQUEST_APPROVE: 'books:request.approve',",
		"  USERS_MANAGE: 'core:users.manage',",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("generated file lacks %q", want)
		}
	}
}

// TestGenerate_IsDeterministic: -check byte-compares, so two runs must agree.
func TestGenerate_IsDeterministic(t *testing.T) {
	first := generate(books.AtomTable())
	second := generate(books.AtomTable())
	if !bytes.Equal(first, second) {
		t.Fatal("two runs differ")
	}
}

// TestGenerate_NoLegacyAtomNames: the file speaks the native vocabulary only; a
// HEAD atom such as library.organize in it would put the old strings back.
func TestGenerate_NoLegacyAtomNames(t *testing.T) {
	out := string(generate(books.AtomTable()))
	for _, a := range books.AtomTable() {
		if strings.Contains(out, "'"+a.Name+"'") {
			t.Errorf("legacy atom name %q in the generated file", a.Name)
		}
	}
}

// TestCommittedFileIsFresh: the committed web/src/generated/permissions.ts is
// what the generator writes (the same comparison `make permissions-check` runs).
func TestCommittedFileIsFresh(t *testing.T) {
	got, err := os.ReadFile(filepath.Join("..", "..", defaultOut))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(generate(books.AtomTable())) {
		t.Fatal("web/src/generated/permissions.ts is stale; run `make generate-permissions`")
	}
}
