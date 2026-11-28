// file: internal/books/cutover/retired_env_list_test.go
// version: 0.1.0
// guid: 226a7043-afdf-49b8-bded-e05a1b7acefd
// last-edited: 2026-11-19

package cutover

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/falkcorp/mediacore/config"
)

// deploy/retired-env-names.txt is what `make deploy`'s deploy-env-check
// (scripts/deploy_env_check.py) reads to refuse a deploy/local.conf that sets
// an env name the binary being deployed would not read. It is generated from
// the rename table, the one place the retired names live, so the list cannot
// drift from it: TestRetiredEnvList_InSync fails when it does.
//
// Whether the binary ignores these names depends on whether the app hands
// RetiredConfigNames to core. Until it does (F3-07), the binary still reads
// every one of them, and refusing a deploy for setting one would block every
// deploy; the list therefore carries `# enforced: false`. The marker is not
// edited by hand: it is derived from the source (retiredNamesWired), so the PR
// that wires RetiredConfigNames fails TestRetiredEnvList_InSync until the list
// is regenerated, and the regenerated list enforces.

const retiredEnvListPath = "deploy/retired-env-names.txt"

const (
	retiredEnvListVersion = "0.1.0"
	retiredEnvListGUID    = "6f8d1a52-7c0e-4b39-9a14-2d5e8c3b7f60"
	retiredEnvListEdited  = "2026-11-19"
)

// retiredEnvListText renders the list: every env name (not file key) of the
// table whose verdict is not "kept", sorted, with its verdict and the
// canonical env name that replaces it ("-" when it became no key).
func retiredEnvListText(t testing.TB, enforced bool) string {
	t.Helper()
	type entry struct{ verdict, canonical string }
	byName := map[string]entry{}
	for _, r := range Table().Rows {
		for _, e := range r.Retired {
			if e.File || e.Verdict == VerdictKept {
				continue
			}
			canon := "-"
			if e.Canonical != "" {
				canon = CanonicalEnv(e.Canonical)
			}
			cur, seen := byName[e.Name]
			// A name two rows retire is refused if either refuses.
			if seen && cur.verdict == string(VerdictRefuse) {
				continue
			}
			byName[e.Name] = entry{string(e.Verdict), canon}
		}
	}
	// The refused operator settings (RefusedOperatorPaths): a canonical env name
	// the new binary no longer reads and refuses a host for setting. A `*` in the
	// name stands for one or more characters (the metadata credentials family
	// has any source id and any key). The replacement is the setting to use.
	for _, r := range RefusedOperatorPaths() {
		byName[r.EnvPattern()] = entry{string(VerdictRefuse), envInstead(r)}
	}
	names := make([]string, 0, len(byName))
	for n := range byName {
		names = append(names, n)
	}
	sort.Strings(names)
	var b strings.Builder
	fmt.Fprintf(&b, "# file: %s\n# version: %s\n# guid: %s\n# last-edited: %s\n#\n", retiredEnvListPath, retiredEnvListVersion, retiredEnvListGUID, retiredEnvListEdited)
	b.WriteString("# The env names the rename table retires (05-config §1.3.6), generated from it by\n")
	b.WriteString("# internal/books/cutover/retired_env_list_test.go. scripts/deploy_env_check.py reads\n")
	b.WriteString("# this file and refuses a deploy whose deploy/local.conf sets one of them: a binary\n")
	b.WriteString("# that has the flip never reads it (verdict warn: ignored with a warning) or refuses\n")
	b.WriteString("# to boot on it (verdict refuse).\n")
	b.WriteString("# A name with a `*` is a pattern (one or more characters): the refused operator\n")
	b.WriteString("# settings of RefusedOperatorPaths, whose canonical env names a host may still set.\n")
	b.WriteString("# Regenerate: go test ./internal/books/cutover -run TestRetiredEnvList -update\n")
	b.WriteString("#\n")
	b.WriteString("# enforced says whether the app hands these names to core (F3-07). While it is\n")
	b.WriteString("# false the binary still reads them and the check only reports; it is derived from\n")
	b.WriteString("# the source, never edited by hand.\n")
	fmt.Fprintf(&b, "# enforced: %v\n", enforced)
	b.WriteString("#\n# NAME<TAB>VERDICT<TAB>REPLACEMENT\n")
	for _, n := range names {
		fmt.Fprintf(&b, "%s\t%s\t%s\n", n, byName[n].verdict, byName[n].canonical)
	}
	return b.String()
}

// retiredNamesWired reports whether any non-test Go file outside this package
// uses cutover.RetiredConfigNames: the app handing the table's retired names
// to core. Found by import resolution, so a renamed import still counts and
// the field of the same name on app.Options does not.
func retiredNamesWired(t testing.TB, root string) bool {
	t.Helper()
	skip := map[string]bool{".git": true, ".worktrees": true, "node_modules": true, "web": true, "vendor": true, ".standards": true}
	own := filepath.Join("internal", "books", "cutover")
	fset := token.NewFileSet()
	wired := false
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		if d.IsDir() {
			if rel == own || (skip[d.Name()] && rel == d.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		f, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if err != nil {
			return nil // the compiler reports a file that does not parse
		}
		local := ""
		for _, imp := range f.Imports {
			p := strings.Trim(imp.Path.Value, `"`)
			if p == "internal/books/cutover" || strings.HasSuffix(p, "/internal/books/cutover") {
				local = "cutover"
				if imp.Name != nil {
					local = imp.Name.Name
				}
			}
		}
		if local == "" {
			return nil
		}
		ast.Inspect(f, func(n ast.Node) bool {
			if sel, ok := n.(*ast.SelectorExpr); ok && sel.Sel.Name == "RetiredConfigNames" {
				if x, ok := sel.X.(*ast.Ident); ok && x.Name == local {
					wired = true
				}
			}
			return !wired
		})
		return nil
	})
	if err != nil {
		t.Fatalf("scan for the RetiredConfigNames wiring: %v", err)
	}
	return wired
}

func TestRetiredEnvList_InSync(t *testing.T) {
	root := repoRoot(t)
	path := filepath.Join(root, retiredEnvListPath)
	got := retiredEnvListText(t, retiredNamesWired(t, root))
	if *update {
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v (run with -update to create it)", retiredEnvListPath, err)
	}
	if string(want) != got {
		t.Fatalf("%s differs from the rename table (or from whether the app wires RetiredConfigNames): regenerate it with go test ./internal/books/cutover -run TestRetiredEnvList -update\n%s", retiredEnvListPath, firstDiff(string(want), got))
	}
}

// The marker is part of the contract with the deploy check: a list that says
// enforced: true while the app does not hand the names to core would refuse
// every deploy that sets a name the binary still reads, and one that says false
// after the flip would let a deploy ship names the binary ignores. Pinned apart
// from the byte comparison so the failure names the cause.
func TestRetiredEnvList_EnforcedMatchesWhetherTheAppWiresRetiredNames(t *testing.T) {
	root := repoRoot(t)
	raw, err := os.ReadFile(filepath.Join(root, retiredEnvListPath))
	if err != nil {
		t.Fatalf("read %s: %v", retiredEnvListPath, err)
	}
	enforced := strings.Contains(string(raw), "\n# enforced: true\n")
	if !enforced && !strings.Contains(string(raw), "\n# enforced: false\n") {
		t.Fatalf("%s has no `# enforced: true|false` line", retiredEnvListPath)
	}
	if wired := retiredNamesWired(t, root); wired != enforced {
		t.Fatalf("the app wires cutover.RetiredConfigNames: %v, but %s says enforced: %v. Regenerate it (go test ./internal/books/cutover -run TestRetiredEnvList -update) so `make deploy` refuses a deploy/local.conf that sets a name the new binary ignores", wired, retiredEnvListPath, enforced)
	}
}

func TestRetiredEnvList_HoldsTheNamesTheDeployFilesMustNotSet(t *testing.T) {
	text := retiredEnvListText(t, false)
	for _, name := range []string{
		"WHISPER_REMOTE_URL", "WHISPER_ENDPOINTS", "SMTP_FROM", "ABS_AUTH_PROBE",
		"CF_ACCOUNT_ID", "CF_GUESTS_POLICY_ID", "CF_GUEST_SERVICE_TOKENS", "CF_SERVICE_AUTH_POLICY_ID",
	} {
		if !strings.Contains(text, "\n"+name+"\twarn\t") {
			t.Errorf("%s is not listed as a warn name", name)
		}
	}
	for _, kept := range []string{"OTEL_EXPORTER_OTLP_ENDPOINT", "OPENAI_API_KEY"} {
		if strings.Contains(text, "\n"+kept+"\t") {
			t.Errorf("%s is an ecosystem binding, not retired, and is listed", kept)
		}
	}
	if !strings.Contains(text, "\nDATABASE_PATH\trefuse\tABO_CORE_STORAGE_PATH\n") {
		t.Errorf("DATABASE_PATH is not listed with its verdict and replacement")
	}
}

// TestRetiredEnvList_HoldsTheRefusedOperatorSettings pins the exact canonical
// spellings the deploy pre-flight refuses (the F3-07 prod census names them):
// the old openai path's env name and the metadata credentials family as a pattern.
func TestRetiredEnvList_HoldsTheRefusedOperatorSettings(t *testing.T) {
	text := retiredEnvListText(t, true)
	for _, row := range []string{
		"ABO_INTEGRATIONS_AI_OPENAI_BASE_URL\trefuse\tABO_INTEGRATIONS_AI_OPENAI_URL\n",
		"ABO_INTEGRATIONS_METADATA_*_CREDENTIALS_*\trefuse\tthe credential as a stored secret",
	} {
		if !strings.Contains(text, "\n"+row) {
			t.Errorf("the list lacks the row %q", row)
		}
	}
	// The new canonical name is no retired name: a host may set it.
	if strings.Contains(text, "\nABO_INTEGRATIONS_AI_OPENAI_URL\t") {
		t.Error("ABO_INTEGRATIONS_AI_OPENAI_URL is listed as refused; it is the setting to use")
	}
}

// envInstead is what the deploy check tells a host to set in place of a refused
// operator setting: the canonical env name of Instead when that is a path, and
// the phrase as it stands otherwise. Only the generated list needs it, so it is
// test code and not production code.
func envInstead(r RefusedOperatorPath) string {
	if strings.ContainsAny(r.Instead, " \t") {
		return r.Instead
	}
	return CanonicalEnv(config.Path(r.Instead))
}
