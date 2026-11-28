// file: internal/arch/native_fence_guard_test.go
// version: 0.1.0
// guid: 5d0f4e21-8b3a-4c6e-9a17-2f6c8d1b7e53
// last-edited: 2026-11-28

package arch

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// A native kind is run by core's executor, and core's executor never puts a HEAD
// fence on the run's context. HEAD's check, opswriter.FenceFromContext(ctx).Check(),
// answers nil ("always writable") when no HEAD fence is there, so a native kind
// that reads it keeps writing after a cancel, a timeout or a lost lease. A native
// kind is fenced by core: the run context's cancel cause, or the refusal of the
// RC's tasks.Writer (tasks.ErrFenced).
//
// This guard reads the syntax of every non-test Go file of the module and fails
// on any reference to HEAD's fence machinery that is not on a frozen list of the
// files that serve HEAD's own paths. The whole tree is scanned, not only the
// packages that build native kinds today: a kind written tomorrow in any package
// is held to the same rule. It reads syntax rather than the call graph because
// the reachability that matters (is this helper run by a native kind?) turns on
// closures stored in definitions and on shared cores that two shells call, which a
// call graph either over-approximates (every shared core reaches HEAD's helper
// through HEAD's shell) or needs hand-picked roots for. The syntax is what 4b
// deletes, so the list below is also the deletion list.
//
// What counts as a reference (see nativeFenceBanned): the opswriter names that
// build, install, read or classify by HEAD's fence, and pkg/ops's ErrFenced, which
// is the same value as opswriter.ErrFenced. An import of the opswriter package
// under a dot or blank name is refused outright, since the names behind it cannot
// be read.

const opswriterImport = "github.com/falkcorp/audiobook-organizer/internal/operations/opswriter"
const opsImport = "github.com/falkcorp/audiobook-organizer/pkg/ops"

// nativeFenceBanned is the selectors that reach HEAD's fence, by import path.
var nativeFenceBanned = map[string][]string{
	opswriterImport: {"FenceFromContext", "New", "ErrFenced", "WithFence", "NewFence"},
	opsImport:       {"ErrFenced"},
}

// headOnlyFenceUsers is the frozen list: the files that may still name HEAD's
// fence, and the selectors each may name. Every entry serves HEAD's own path and
// is deleted with it by ENGINE-DELETE-OWNER-05 (4b); the list only shrinks. An
// entry that no longer matches a reference is stale and fails the guard, so a
// file that stops using HEAD's fence has to leave the list in the same change.
//
// Dual-use files (a file HEAD and a native kind both reach) are marked DUAL and
// carry the reason the reference cannot go yet.
var headOnlyFenceUsers = map[string][]string{
	// HEAD's batch runner, its v3 adapter and its worker: HEAD's engine, which
	// the switch retires.
	"internal/operations/registry/batch_runner.go": {"opswriter.FenceFromContext", "ops.ErrFenced"},
	"internal/operations/registry/v3_adapter.go":   {"opswriter.FenceFromContext", "opswriter.New"},
	"internal/operations/registry/worker.go":       {"opswriter.NewFence", "opswriter.WithFence"},
	// HEAD's shell of the window-index build (runWindowIndexBuild): the native
	// shell sets nativeFence, core's, in kinds_index.go.
	"internal/plugins/dedup/window_index_build.go": {"opswriter.FenceFromContext"},
	// HEAD's repairs engine: RunApply and applyOne are reached only from the HEAD
	// op (internal/plugins/maintenance/repairs_ops.go); the native kind runs the
	// executor's fixer runner and installs core's fence (kinds.go newRunState).
	"internal/repairs/engine.go": {"opswriter.FenceFromContext", "opswriter.ErrFenced"},
	// DUAL: repairs.NewWriter builds HEAD's journaling opswriter.Writer, and the
	// native repairs kind and two native cores (version-group-primary-repair,
	// asin-backfill) build it too. Its journal, evidence and previous-capture
	// engine has no equivalent in core or in internal/books/writer, so the native
	// path cannot leave it yet (F5-18 commit 1 STOP). Every native caller installs
	// core's fence on it (or runs over a gated store); the writer's own HEAD fence
	// is not read on a native path.
	"internal/repairs/writer.go": {"opswriter.New"},
}

// nativeFenceProblems judges the non-test Go files of the module (path relative
// to the repository root, slash-separated, to source).
func nativeFenceProblems(files map[string][]byte, allow map[string][]string) []string {
	var out []string
	paths := make([]string, 0, len(files))
	for p := range files {
		paths = append(paths, p)
	}
	slices.Sort(paths)
	fset := token.NewFileSet()
	seen := map[string]map[string]bool{} // file -> "pkg.Sel" -> referenced
	for _, p := range paths {
		if strings.HasPrefix(p, "internal/operations/opswriter/") {
			continue // the package's own files
		}
		f, err := parser.ParseFile(fset, p, files[p], parser.SkipObjectResolution)
		if err != nil {
			out = append(out, fmt.Sprintf("%s: unparsable: %v", p, err))
			continue
		}
		// local name -> import path, for the packages the guard watches.
		local := map[string]string{}
		for _, imp := range f.Imports {
			path, _ := strconv.Unquote(imp.Path.Value)
			if _, watched := nativeFenceBanned[path]; !watched {
				continue
			}
			switch {
			case imp.Name != nil && (imp.Name.Name == "." || imp.Name.Name == "_"):
				if path == opswriterImport {
					out = append(out, fmt.Sprintf("%s:%d: imports opswriter as %q, which hides the names it uses from this guard",
						p, fset.Position(imp.Pos()).Line, imp.Name.Name))
				}
			case imp.Name != nil:
				local[imp.Name.Name] = path
			default:
				local[path[strings.LastIndex(path, "/")+1:]] = path
			}
		}
		if len(local) == 0 {
			continue
		}
		ast.Inspect(f, func(n ast.Node) bool {
			sel, ok := n.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			x, ok := sel.X.(*ast.Ident)
			if !ok {
				return true
			}
			path, ok := local[x.Name]
			if !ok || !slices.Contains(nativeFenceBanned[path], sel.Sel.Name) {
				return true
			}
			name := path[strings.LastIndex(path, "/")+1:] + "." + sel.Sel.Name
			if seen[p] == nil {
				seen[p] = map[string]bool{}
			}
			seen[p][name] = true
			if !slices.Contains(allow[p], name) {
				out = append(out, fmt.Sprintf("%s:%d: names %s: core's executor puts no HEAD fence on a run's context, so on a native kind this check answers \"always writable\"; fence by the run context's cancel cause or the RC writer's tasks.ErrFenced",
					p, fset.Position(sel.Pos()).Line, name))
			}
			return true
		})
	}
	// A listed file that no longer names what it is listed for is stale.
	allowed := make([]string, 0, len(allow))
	for p := range allow {
		allowed = append(allowed, p)
	}
	sort.Strings(allowed)
	for _, p := range allowed {
		if _, ok := files[p]; !ok {
			out = append(out, fmt.Sprintf("%s: is on the HEAD-only list but is gone: remove the entry", p))
			continue
		}
		for _, name := range allow[p] {
			if !seen[p][name] {
				out = append(out, fmt.Sprintf("%s: is on the HEAD-only list for %s but no longer names it: remove the entry", p, name))
			}
		}
	}
	return out
}

// TestNativeKindsAreFencedByCore pins the rule over the real tree.
func TestNativeKindsAreFencedByCore(t *testing.T) {
	files := productionGoFiles(t)
	if probs := nativeFenceProblems(files, headOnlyFenceUsers); len(probs) > 0 {
		t.Fatalf("the native-fence guard found %d problem(s):\n%s", len(probs), strings.Join(probs, "\n"))
	}
}

// TestNativeFenceGuard_CatchesEachReference: the guard fails closed on each way
// to name HEAD's fence, so a change that loosens it is caught here. A new native
// reference is the first case.
func TestNativeFenceGuard_CatchesEachReference(t *testing.T) {
	const head = "internal/repairs/engine.go"
	allow := map[string][]string{head: {"opswriter.FenceFromContext"}}
	headFile := `package repairs
import "github.com/falkcorp/audiobook-organizer/internal/operations/opswriter"
func f(ctx context.Context) error { return opswriter.FenceFromContext(ctx).Check() }
`
	cases := map[string]struct {
		files map[string][]byte
		want  string // "" = clean; else a substring of a problem
	}{
		"a listed HEAD-only file is clean": {files: map[string][]byte{head: []byte(headFile)}},
		"a new native kind reading the fence": {
			files: map[string][]byte{head: []byte(headFile),
				"internal/plugins/newkind/kinds.go": []byte("package newkind\nimport \"github.com/falkcorp/audiobook-organizer/internal/operations/opswriter\"\nfunc f(ctx context.Context) error { return opswriter.FenceFromContext(ctx).Check() }\n")},
			want: "internal/plugins/newkind/kinds.go:3: names opswriter.FenceFromContext",
		},
		"the same name under an alias": {
			files: map[string][]byte{head: []byte(headFile),
				"internal/plugins/newkind/kinds.go": []byte("package newkind\nimport ow \"github.com/falkcorp/audiobook-organizer/internal/operations/opswriter\"\nvar _ = ow.FenceFromContext\n")},
			want: "names opswriter.FenceFromContext",
		},
		"a sentinel check": {
			files: map[string][]byte{head: []byte(headFile),
				"internal/plugins/newkind/kinds.go": []byte("package newkind\nimport \"github.com/falkcorp/audiobook-organizer/internal/operations/opswriter\"\nfunc f(err error) bool { return errors.Is(err, opswriter.ErrFenced) }\n")},
			want: "names opswriter.ErrFenced",
		},
		"the same sentinel by pkg/ops": {
			files: map[string][]byte{head: []byte(headFile),
				"internal/plugins/newkind/kinds.go": []byte("package newkind\nimport \"github.com/falkcorp/audiobook-organizer/pkg/ops\"\nfunc f(err error) bool { return errors.Is(err, ops.ErrFenced) }\n")},
			want: "names ops.ErrFenced",
		},
		"building a HEAD writer": {
			files: map[string][]byte{head: []byte(headFile),
				"internal/plugins/newkind/kinds.go": []byte("package newkind\nimport \"github.com/falkcorp/audiobook-organizer/internal/operations/opswriter\"\nvar _ = opswriter.New(opswriter.Config{})\n")},
			want: "names opswriter.New",
		},
		"installing a HEAD fence on a context": {
			files: map[string][]byte{head: []byte(headFile),
				"internal/plugins/newkind/kinds.go": []byte("package newkind\nimport \"github.com/falkcorp/audiobook-organizer/internal/operations/opswriter\"\nvar _ = opswriter.WithFence\n")},
			want: "names opswriter.WithFence",
		},
		"a dot import hides the names": {
			files: map[string][]byte{head: []byte(headFile),
				"internal/plugins/newkind/kinds.go": []byte("package newkind\nimport . \"github.com/falkcorp/audiobook-organizer/internal/operations/opswriter\"\n")},
			want: "hides the names",
		},
		"a listed file naming a second thing": {
			files: map[string][]byte{head: []byte(headFile + "var _ = opswriter.ErrFenced\n")},
			want:  "internal/repairs/engine.go:4: names opswriter.ErrFenced",
		},
		"a listed file that stopped naming it is stale": {
			files: map[string][]byte{head: []byte("package repairs\n")},
			want:  "no longer names it",
		},
		"a listed file that is gone is stale": {
			files: map[string][]byte{"internal/other/x.go": []byte("package other\n")},
			want:  "is gone",
		},
		"a local variable named like the package is not the package": {
			files: map[string][]byte{head: []byte(headFile),
				"internal/other/x.go": []byte("package other\nfunc f(opswriter struct{ New int }) int { return opswriter.New }\n")},
		},
		"an unwatched package's New is fine": {
			files: map[string][]byte{head: []byte(headFile),
				"internal/other/x.go": []byte("package other\nimport \"example.com/x/opswriter\"\nvar _ = opswriter.New\n")},
		},
		"the opswriter package's own files are not scanned": {
			files: map[string][]byte{head: []byte(headFile),
				"internal/operations/opswriter/fence.go": []byte("package opswriter\nvar _ = ErrFenced\n")},
		},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			probs := nativeFenceProblems(c.files, allow)
			if c.want == "" {
				if len(probs) != 0 {
					t.Fatalf("clean tree flagged: %v", probs)
				}
				return
			}
			if !slices.ContainsFunc(probs, func(p string) bool { return strings.Contains(p, c.want) }) {
				t.Fatalf("problems %v, want one containing %q", probs, c.want)
			}
		})
	}
}
