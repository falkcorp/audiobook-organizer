// file: core/app/noglobals_test.go
// version: 1.1.0
// guid: 68d64f45-c7be-4bf0-8c21-6be99047ed03
// last-edited: 2026-11-07

package app

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/falkcorp/mediacore/archtest"
)

// processDefaultsExempt is what app.InstallProcessDefaults (R1-15) may call
// and nothing else in core: the seven process installs 05-logging §10 lists
// (05-metrics §12 names the three otel provider and propagator setters among
// them), plus what its restore needs to put them back: the three otel getters
// it reads the previous values with, and log.SetFlags, because slog.SetDefault
// zeroes log's flags.
func processDefaultsExempt() map[string]bool {
	return map[string]bool{
		"log/slog.SetDefault":                           true,
		"log.SetOutput":                                 true,
		"go.opentelemetry.io/otel.SetLogger":            true,
		"go.opentelemetry.io/otel.SetErrorHandler":      true,
		"go.opentelemetry.io/otel.SetMeterProvider":     true,
		"go.opentelemetry.io/otel.SetTracerProvider":    true,
		"go.opentelemetry.io/otel.SetTextMapPropagator": true,
		// restore's reads and its one extra write
		"go.opentelemetry.io/otel.GetMeterProvider":     true,
		"go.opentelemetry.io/otel.GetTracerProvider":    true,
		"go.opentelemetry.io/otel.GetTextMapPropagator": true,
		"log.SetFlags":                                  true,
	}
}

// bannedGlobal reports whether path.name is a process-global R5 forbids in
// core (03 R5; 05-metrics §12; 05-logging §10), with the reason.
func bannedGlobal(path, name string) (string, bool) {
	switch path {
	case "github.com/prometheus/client_golang/prometheus":
		if slices.Contains([]string{"DefaultRegisterer", "DefaultGatherer", "MustRegister", "Register"}, name) {
			return "the default Prometheus registry; use the App's Provider", true
		}
	case "github.com/prometheus/client_golang/prometheus/promhttp":
		if name == "Handler" {
			return "promhttp.Handler serves the default registry; use Provider.Handler", true
		}
	case "go.opentelemetry.io/otel":
		if strings.HasPrefix(name, "Set") || (strings.HasPrefix(name, "Get") && strings.HasSuffix(name, "Provider")) ||
			name == "Tracer" || name == "Meter" || name == "GetTextMapPropagator" {
			return "the OTel global; use the App's Provider", true
		}
	case "log/slog":
		if name == "SetDefault" {
			return "the process default logger; use Logging", true
		}
	case "log":
		if slices.Contains([]string{"SetOutput", "SetFlags", "SetPrefix"}, name) {
			return "the standard library's global logger", true
		}
	case "net/http":
		if slices.Contains([]string{"DefaultServeMux", "Handle", "HandleFunc"}, name) {
			return "the default ServeMux; use the Router", true
		}
	case "os/signal":
		if name == "Notify" || name == "NotifyContext" {
			return "signal handling belongs to main", true
		}
	}
	return "", false
}

// noGlobalsFindings scans one parsed file. exemptFunc names the one function
// whose calls processDefaultsExempt permits ("" = none in this file).
func noGlobalsFindings(fset *token.FileSet, f *ast.File, exemptFunc string) []string {
	imports := make(map[string]string) // local name -> import path
	for _, is := range f.Imports {
		path, err := strconv.Unquote(is.Path.Value)
		if err != nil {
			continue
		}
		name := path[strings.LastIndex(path, "/")+1:]
		if is.Name != nil {
			name = is.Name.Name
		}
		imports[name] = path
	}
	exempt := processDefaultsExempt()
	var out []string
	for _, decl := range f.Decls {
		inExempt := false
		if fd, ok := decl.(*ast.FuncDecl); ok && fd.Recv == nil && exemptFunc != "" && fd.Name.Name == exemptFunc {
			inExempt = true
		}
		ast.Inspect(decl, func(n ast.Node) bool {
			switch n := n.(type) {
			case *ast.SelectorExpr:
				id, ok := n.X.(*ast.Ident)
				if !ok {
					return true
				}
				path, ok := imports[id.Name]
				if !ok {
					return true
				}
				why, banned := bannedGlobal(path, n.Sel.Name)
				if banned && !(inExempt && exempt[path+"."+n.Sel.Name]) {
					out = append(out, fset.Position(n.Pos()).String()+": "+id.Name+"."+n.Sel.Name+": "+why)
				}
			case *ast.AssignStmt:
				// Mutating http.DefaultClient or http.DefaultTransport (R5).
				for _, lhs := range n.Lhs {
					if root := rootSelector(lhs); root != nil {
						if id, ok := root.X.(*ast.Ident); ok && imports[id.Name] == "net/http" &&
							(root.Sel.Name == "DefaultClient" || root.Sel.Name == "DefaultTransport") {
							out = append(out, fset.Position(lhs.Pos()).String()+": assignment to http."+root.Sel.Name+": a process-global client")
						}
					}
				}
			}
			return true
		})
	}
	return out
}

// rootSelector is the innermost pkg.Name selector of an expression like
// pkg.Name.Field or pkg.Name.(*T).Field.
func rootSelector(e ast.Expr) *ast.SelectorExpr {
	for {
		switch x := e.(type) {
		case *ast.SelectorExpr:
			if _, ok := x.X.(*ast.Ident); ok {
				return x
			}
			e = x.X
		case *ast.TypeAssertExpr:
			e = x.X
		case *ast.ParenExpr:
			e = x.X
		case *ast.StarExpr:
			e = x.X
		default:
			return nil
		}
	}
}

// TestNoGlobals is 03 R5's AST scan over every non-test .go file of the core
// module: no default Prometheus registry, no OTel, slog or log globals, no
// default ServeMux, no mutation of the default HTTP client, and no signal
// handling outside a main (05-metrics §12, 05-logging §10, 06 §6.4.5). The
// main packages under core/examples/*/cmd/ are process owners and skipped
// (COR6-11); app.InstallProcessDefaults (R1-15) may make the seven process
// installs and nothing else may.
func TestNoGlobals(t *testing.T) {
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	root, err := archtest.ModuleRoot(wd)
	if err != nil {
		t.Fatal(err)
	}
	var findings []string
	scanned := 0
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			name := d.Name()
			if path != root && (name == "testdata" || name == "node_modules" || strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_") ||
				rel == "web" || rel == "examples/hello/web") {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if err != nil {
			return err
		}
		scanned++
		if f.Name.Name == "main" && underExampleCmd(rel) {
			return nil // a main is a process owner (COR6-11)
		}
		exempt := ""
		if filepath.Dir(rel) == "app" {
			exempt = "InstallProcessDefaults"
		}
		findings = append(findings, noGlobalsFindings(fset, f, exempt)...)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if scanned < 50 {
		t.Fatalf("the walk is broken: scanned %d files, want the whole core module", scanned)
	}
	if len(findings) > 0 {
		t.Errorf("process-global state in core (03 R5):\n  %s", strings.Join(findings, "\n  "))
	}
}

// underExampleCmd reports a path under core/examples/<name>/cmd/.
func underExampleCmd(rel string) bool {
	parts := strings.Split(rel, "/")
	return len(parts) > 3 && parts[0] == "examples" && parts[2] == "cmd"
}

// TestGlobalScan_FindsEachKind proves the scan is not blind: a file holding
// every banned form yields one finding each, and the InstallProcessDefaults
// exemption covers its seven installs only.
func TestGlobalScan_FindsEachKind(t *testing.T) {
	const src = `package wayne

import (
	stdlog "log"
	"log/slog"
	"net/http"
	"os/signal"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.opentelemetry.io/otel"
)

func manor() {
	prometheus.MustRegister(nil)
	_ = prometheus.DefaultGatherer
	_ = promhttp.Handler()
	otel.SetMeterProvider(nil)
	_ = otel.GetTracerProvider()
	_ = otel.Tracer("batmobile")
	slog.SetDefault(nil)
	stdlog.SetOutput(nil)
	http.HandleFunc("/cave", nil)
	_ = http.DefaultServeMux
	http.DefaultClient = nil
	http.DefaultTransport.(*http.Transport).MaxIdleConns = 1
	signal.Notify(nil)
	_, _ = signal.NotifyContext(nil)
}

func InstallProcessDefaults() {
	slog.SetDefault(nil)
	otel.SetTracerProvider(nil)
	_ = promhttp.Handler()
}
`
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "wayne.go", src, parser.SkipObjectResolution)
	if err != nil {
		t.Fatal(err)
	}
	if got := noGlobalsFindings(fset, f, ""); len(got) != 17 {
		t.Fatalf("findings without the exemption = %d, want 17:\n%s", len(got), strings.Join(got, "\n"))
	}
	got := noGlobalsFindings(fset, f, "InstallProcessDefaults")
	if len(got) != 15 || !strings.Contains(got[len(got)-1], "promhttp.Handler") {
		t.Fatalf("findings with the exemption = %d, want 15 (promhttp.Handler stays banned there):\n%s", len(got), strings.Join(got, "\n"))
	}
}
