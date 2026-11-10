// file: core/backup/journal/imports_test.go
// version: 1.0.0
// guid: 6e1b8d42-7a3c-4f59-9c2d-0b5e8a7f3d16
// last-edited: 2026-11-10

// An external test package: archtest imports rbac, which imports
// backup/journal (rbac.Deps.Revocations is a journal.RevocationJournal,
// 05-rbac §3.4), so an in-package test of journal that imports archtest
// would close an import cycle. The test moved here from file_test.go.
package journal_test

import (
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/falkcorp/mediacore/archtest"
)

// TestJournalImportsOnlyAllowed: backup/journal imports only entity, storage
// and storage/restorejournal of core (and the standard library), so auth ->
// journal and rbac -> journal close no cycle; its transitive closure adds
// nothing beyond what storage itself imports (05-backup §5B.3.2; the brief's
// go list -deps allow-list).
func TestJournalImportsOnlyAllowed(t *testing.T) {
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	root, err := archtest.ModuleRoot(wd)
	if err != nil {
		t.Fatal(err)
	}
	pkgs, err := archtest.Load(archtest.LoadOptions{Dir: root, Patterns: []string{"./backup/journal"}, TagSets: archtest.TagSets()})
	if err != nil {
		t.Fatal(err)
	}
	const mod = "github.com/falkcorp/mediacore/"
	allowed := map[string]bool{mod + "entity": true, mod + "storage": true, mod + "storage/restorejournal": true}
	p := pkgs[mod+"backup/journal"]
	if p == nil {
		t.Fatal("the loader did not return backup/journal")
	}
	for _, imp := range p.Imports {
		if strings.Contains(strings.SplitN(imp, "/", 2)[0], ".") && !allowed[imp] {
			t.Errorf("backup/journal imports %s; only entity, storage, storage/restorejournal and the standard library are allowed", imp)
		}
	}

	deps := func(pkg string) map[string]bool {
		cmd := exec.Command("go", "list", "-deps", "-f", "{{if not .Standard}}{{.ImportPath}}{{end}}", pkg)
		cmd.Dir = root
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("go list -deps %s: %v", pkg, err)
		}
		set := map[string]bool{}
		for l := range strings.FieldsSeq(string(out)) {
			set[l] = true
		}
		return set
	}
	storageDeps := deps("./storage")
	for d := range deps("./backup/journal") {
		if !storageDeps[d] && !allowed[d] && d != mod+"backup/journal" {
			t.Errorf("backup/journal depends on %s, which storage does not import", d)
		}
	}
}
