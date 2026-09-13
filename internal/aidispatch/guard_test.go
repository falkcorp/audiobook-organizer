// file: internal/aidispatch/guard_test.go
// version: 1.0.0
// guid: 978462ae-b706-482d-b878-4b12387747d8
// last-edited: 2026-09-13

package aidispatch

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// TestGuard_NoAIBackendCallsOutsideTheDispatcher is PLAN section 7 item 1: it
// fails when non-test Go code outside the allowlist talks to an AI backend
// without going through this package.
//
// It parses source with go/parser, the pattern of
// internal/database/mock_store_override_test.go, because the rule is
// structural: an import path, a selector chain on a client, a path literal.
// golangci-lint cannot carry it (CI runs it with --enable-only nolintlint).
//
// Rules, any one of which is a bypass:
//  1. importing github.com/openai/openai-go (any major version);
//  2. a selector chain ending .Chat.Completions.New, .Embeddings.New,
//     .Audio.Transcriptions.New or .Files.New, or passing through .Batches.;
//  3. a string literal that is exactly a Whisper-server or Ollama API path.
//     "/api/tags" is included even though it lists models rather than running
//     inference: it is still a direct call to an AI host that bypasses the
//     endpoint list, and PR 2's status API is where it belongs.
var guardPathLiterals = map[string]bool{
	"/transcribe-batch":        true,
	"/transcribe":              true,
	"/v1/audio/transcriptions": true,
	"/api/pull":                true,
	"/api/tags":                true,
	"/api/show":                true,
	"/api/embed":               true,
	"/api/embeddings":          true,
	"/api/chat":                true,
	"/api/generate":            true,
}

var guardSelectorSuffixes = []string{
	".Chat.Completions.New",
	".Embeddings.New",
	".Audio.Transcriptions.New",
	".Files.New",
}

// guardAllowlist is the RATCHET: every file that bypasses the dispatcher on
// the day this guard landed. Each later PR in the plan deletes its own entries
// when it moves those call sites onto the dispatcher, and lowers
// guardAllowlistCeiling to match. PR 8 shrinks the list to the transport
// packages and benchmark code.
//
// Never add an entry. New AI work goes through aidispatch.Call.
var guardAllowlist = map[string]string{
	// Benchmark code, behind the `bench` build tag. Exempt permanently (PLAN
	// decision 8): it measures a raw backend on purpose, never runs in a
	// production build, and routing it through the dispatcher would measure
	// the dispatcher instead.
	"cmd/dedup_bench.go":        "bench build tag: raw-backend benchmark",
	"cmd/dedup_bench_batch.go":  "bench build tag: raw-backend benchmark",
	"cmd/dedup_bench_pass2.go":  "bench build tag: raw-backend benchmark",
	"cmd/dedup_bench_runner.go": "bench build tag: raw-backend benchmark",
	"cmd/dedup_bench_status.go": "bench build tag: raw-backend benchmark",
	"internal/server/bench.go":  "bench build tag: raw-backend benchmark",

	// Chat, embedding and Batch API transports (PRs 4, 5, 6).
	"internal/ai/embedding_batch.go":                    "PR 5: embed.text_batch",
	"internal/ai/embedding_client.go":                   "PR 5: embed.text",
	"internal/ai/metadata_llm_review.go":                "PR 4: llm.metadata_rerank",
	"internal/ai/openai_batch.go":                       "PR 6: batch submit/poll",
	"internal/ai/openai_parser.go":                      "PR 4: chat call sites",
	"internal/ai/retry.go":                              "PR 4: imports openai-go for its error type",
	"internal/server/handlers/aibackends/aibackends.go": "PR 2: /api/tags probe moves to the status API",

	// Whisper transports (PR 3).
	"internal/transcribe/remote.go":  "PR 3: /transcribe and /transcribe-batch",
	"internal/transcribe/whisper.go": "PR 3: whisper-1 cloud fallback for the intro clip",
}

// guardAllowlistCeiling may only go DOWN. Raising it to fit a new entry is the
// exact change this test exists to stop.
const guardAllowlistCeiling = 15

// guardExemptDirs are permanent exemptions, not ratchet entries.
// internal/aidispatch is the dispatcher itself: classify.go imports openai-go
// to recognise the SDK's error type, which is the one place that must.
var guardExemptDirs = []string{"internal/aidispatch/"}

var guardSkipDirs = map[string]bool{
	".git": true, ".worktrees": true, "node_modules": true, "web": true,
	"vendor": true, "testdata": true, ".standards": true, ".claude": true, "dist": true,
}

func TestGuard_NoAIBackendCallsOutsideTheDispatcher(t *testing.T) {
	root := repoRoot(t)

	if n := len(guardAllowlist); n > guardAllowlistCeiling {
		t.Fatalf("guard allowlist has %d entries, ceiling %d: the list may only shrink", n, guardAllowlistCeiling)
	}

	scanned := 0
	found := map[string][]string{}
	err := filepath.WalkDir(root, func(path string, de fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if de.IsDir() {
			if path != root && (guardSkipDirs[de.Name()] || strings.HasPrefix(de.Name(), ".")) {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		rel = filepath.ToSlash(rel)
		scanned++
		v, perr := guardViolationsInFile(path)
		if perr != nil {
			t.Errorf("parse %s: %v", rel, perr)
			return nil
		}
		if len(v) > 0 {
			found[rel] = v
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}

	// Positive control: a walker that scanned nothing, or the wrong tree,
	// would report zero violations and pass forever.
	if scanned < 300 {
		t.Fatalf("scanned only %d Go files under %s; the walker is broken, not the code", scanned, root)
	}

	var bad []string
	for rel, v := range found {
		if guardIsExempt(rel) {
			continue
		}
		if _, ok := guardAllowlist[rel]; ok {
			continue
		}
		bad = append(bad, rel+": "+strings.Join(v, "; "))
	}
	sort.Strings(bad)
	if len(bad) > 0 {
		t.Errorf("%d file(s) call an AI backend without internal/aidispatch. Route the work through "+
			"aidispatch.Call with a registered capability; do NOT add to guardAllowlist:\n\t%s",
			len(bad), strings.Join(bad, "\n\t"))
	}

	// Ratchet: an entry whose file no longer bypasses anything must be removed
	// (and the ceiling lowered), or the list stops shrinking.
	var stale []string
	for rel := range guardAllowlist {
		if len(found[rel]) == 0 {
			stale = append(stale, rel)
		}
	}
	sort.Strings(stale)
	if len(stale) > 0 {
		t.Errorf("guardAllowlist entries that no longer bypass the dispatcher (or no longer exist); "+
			"delete them and lower guardAllowlistCeiling:\n\t%s", strings.Join(stale, "\n\t"))
	}
}

// The rules themselves, against synthetic source, so a rule that silently
// stops matching cannot hide behind the allowlist.
func TestGuard_RulesMatch(t *testing.T) {
	cases := map[string]string{
		"import": `package p
import "github.com/openai/openai-go/v3"
var _ = openai.Error{}`,
		"chat": `package p
func f(c client) { c.Chat.Completions.New(nil, nil) }`,
		"embed": `package p
func f(c client) { c.inner.Embeddings.New(nil, nil) }`,
		"batches": `package p
func f(c client) { c.Batches.Get(nil, "id") }`,
		"literal": `package p
var u = base + "/transcribe-batch"`,
	}
	for name, src := range cases {
		f, err := parser.ParseFile(token.NewFileSet(), name+".go", src, 0)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if len(guardViolations(f)) == 0 {
			t.Errorf("%s: rule did not fire on:\n%s", name, src)
		}
	}
	clean := `package p
import "net/http"
var route = "/api/v1/transcribe/status"
func f(c client) { c.Chat.Messages.List(); http.Get(route) }`
	f, err := parser.ParseFile(token.NewFileSet(), "clean.go", clean, 0)
	if err != nil {
		t.Fatal(err)
	}
	if v := guardViolations(f); len(v) > 0 {
		t.Errorf("clean source flagged: %v", v)
	}
}

func guardIsExempt(rel string) bool {
	for _, d := range guardExemptDirs {
		if strings.HasPrefix(rel, d) {
			return true
		}
	}
	return false
}

func guardViolationsInFile(path string) ([]string, error) {
	f, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.SkipObjectResolution)
	if err != nil {
		return nil, err
	}
	return guardViolations(f), nil
}

func guardViolations(f *ast.File) []string {
	seen := map[string]bool{}
	var out []string
	add := func(s string) {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	for _, imp := range f.Imports {
		p, _ := strconv.Unquote(imp.Path.Value)
		if p == "github.com/openai/openai-go" || strings.HasPrefix(p, "github.com/openai/openai-go/") {
			add("imports " + p)
		}
	}
	ast.Inspect(f, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.SelectorExpr:
			chain := selectorChain(x)
			for _, s := range guardSelectorSuffixes {
				if strings.HasSuffix(chain, s) {
					add("calls " + s)
				}
			}
			if strings.Contains(chain+".", ".Batches.") {
				add("uses .Batches.")
			}
		case *ast.BasicLit:
			if x.Kind == token.STRING {
				if v, err := strconv.Unquote(x.Value); err == nil && guardPathLiterals[v] {
					add("path literal " + v)
				}
			}
		}
		return true
	})
	return out
}

// selectorChain renders a.b.c for a selector expression, with "?" standing
// in for any non-identifier base (a call result, an index).
func selectorChain(e ast.Expr) string {
	switch x := e.(type) {
	case *ast.Ident:
		return x.Name
	case *ast.SelectorExpr:
		return selectorChain(x.X) + "." + x.Sel.Name
	default:
		return "?"
	}
}

// repoRoot walks up from the package directory to the directory holding
// go.mod. Failing to find it is fatal: scanning the wrong tree would pass.
func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("no go.mod above the package directory; cannot locate the repo root")
		}
		dir = parent
	}
}
