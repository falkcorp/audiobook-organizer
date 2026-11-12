// file: core/archtest/ai_sdk.go
// version: 1.0.0
// guid: 5b0d7e92-3c41-4a86-9f1e-d2a8c6b04713
// last-edited: 2026-11-12

package archtest

import (
	"fmt"
	"go/parser"
	"go/token"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// aiSDKImport is the import path root of the OpenAI Go SDK (v1, v2, v3 and
// every sub-package such as /option, /packages/param, /shared).
const aiSDKImport = "github.com/openai/openai-go"

// aiAdapterDir is the one directory, slash-relative to the module root, whose
// non-test files may import the SDK.
const aiAdapterDir = "integrations/ai/openai"

// NoAISDKOutsideAdapters fails t when a non-test Go file of the module rooted
// at dir imports the OpenAI SDK from outside integrations/ai/openai. The
// adapter is the only place SDK types may appear: the Backend contract in
// integrations/ai speaks the core's own types, so a second importer would
// leak the SDK's wire shapes (and its version) into every caller (05-ai
// section 9). It reads import declarations only and resolves by import path,
// so an alias, a blank import and a dot import are all caught. It walks the
// module's directories (moduleSourceFiles), so a file behind any build tag is
// read too. It fails on a vacuous scan, and fails when the adapter directory
// holds Go files and none of them imports the SDK, because then the scan has
// stopped seeing the import it guards.
func NoAISDKOutsideAdapters(t testing.TB, dir string) {
	t.Helper()
	root, err := resolveRoot(dir)
	if err != nil {
		t.Fatalf("NoAISDKOutsideAdapters: %v", err)
	}
	files, err := moduleSourceFiles(root, nil)
	if err != nil {
		t.Fatalf("NoAISDKOutsideAdapters: %v", err)
	}
	if len(files) == 0 {
		t.Fatalf("NoAISDKOutsideAdapters: no non-test Go file under %s; the check would be vacuous", dir)
	}
	inside, outside, err := AISDKImports(root, files)
	if err != nil {
		t.Fatalf("NoAISDKOutsideAdapters: %v", err)
	}
	if len(outside) > 0 {
		t.Errorf("%d import(s) of the OpenAI SDK outside %s; the SDK's types stay in the adapter (05-ai section 9):\n  %s",
			len(outside), aiAdapterDir, strings.Join(outside, "\n  "))
	}
	if adapterFiles(root, files) > 0 && len(inside) == 0 {
		t.Errorf("NoAISDKOutsideAdapters: %s has Go files and none imports the SDK; the scan does not see the import it guards", aiAdapterDir)
	}
}

// AISDKImports parses the import declarations of files (absolute paths under
// root) and returns "rel:line: imports path" for every import of the OpenAI
// SDK, split into the ones inside the adapter directory and the ones outside
// it. Both lists are sorted.
func AISDKImports(root string, files []string) (inside, outside []string, err error) {
	fset := token.NewFileSet()
	for _, f := range uniq(append([]string(nil), files...)) {
		af, perr := parser.ParseFile(fset, f, nil, parser.ImportsOnly|parser.SkipObjectResolution)
		if perr != nil {
			return nil, nil, fmt.Errorf("parse %s: %v", f, perr)
		}
		rel, rerr := filepath.Rel(root, f)
		if rerr != nil {
			return nil, nil, rerr
		}
		rel = filepath.ToSlash(rel)
		for _, imp := range af.Imports {
			p, uerr := strconv.Unquote(imp.Path.Value)
			if uerr != nil || !isAISDKPath(p) {
				continue
			}
			line := fmt.Sprintf("%s:%d: imports %s", rel, fset.Position(imp.Pos()).Line, p)
			if inAdapterDir(rel) {
				inside = append(inside, line)
			} else {
				outside = append(outside, line)
			}
		}
	}
	sort.Strings(inside)
	sort.Strings(outside)
	return inside, outside, nil
}

// isAISDKPath reports whether p is the SDK's import path or a sub-package of
// it. github.com/openai/openai-gox is a different module and does not match.
func isAISDKPath(p string) bool { return p == aiSDKImport || strings.HasPrefix(p, aiSDKImport+"/") }

// inAdapterDir reports whether the slash path rel (relative to the module
// root) is a file inside the adapter directory.
func inAdapterDir(rel string) bool { return strings.HasPrefix(rel, aiAdapterDir+"/") }

// adapterFiles counts the files that sit in the adapter directory.
func adapterFiles(root string, files []string) int {
	n := 0
	for _, f := range files {
		if rel, err := filepath.Rel(root, f); err == nil && inAdapterDir(filepath.ToSlash(rel)) {
			n++
		}
	}
	return n
}
