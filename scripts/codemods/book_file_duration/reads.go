// file: scripts/codemods/book_file_duration/reads.go
// version: 1.0.1
// guid: 9e1b6f40-27c8-4d35-a0b7-58f3c4d92e16
// last-edited: 2026-11-09

//go:build ignore

// reads is step 2 of the BookFile.Duration int -> *int test rewrite. It was
// run ONCE, AFTER the field became *int, from the repo root:
//
//	go run scripts/codemods/book_file_duration/reads.go          # list only
//	go run scripts/codemods/book_file_duration/reads.go apply    # rewrite
//
// In _test.go files only, for every READ of a BookFile/BookFileCore .Duration:
//
//   - a comparison with an integer constant that means unknown (== 0, <= 0,
//     != 0, > 0, >= 1, < 1) becomes `x.Duration == nil` / `!= nil`;
//   - Equal(t, 0, x.Duration) becomes Nil(t, x.Duration), Zero/NotZero become
//     Nil/NotNil;
//   - any other int-typed use (other comparisons, call arguments, sums,
//     assignments to int variables) is wrapped in durtest.Sec(x.Duration),
//     which reads nil as 0 (internal/testutil/durtest, tests only);
//   - a callee or assignment target that takes *int is left alone.
//
// Uses it cannot place are printed as MANUAL. It is NOT idempotent (a second
// apply wraps `x.Duration == nil`), so apply it once and fix leftovers by hand.
// It leaves new imports to `goimports -local github.com/falkcorp/audiobook-organizer`.
//
// Kept so the rewrite can be audited. Not part of any build (build tag ignore).

package main

import (
	"fmt"
	"go/ast"
	"go/constant"
	"go/token"
	"go/types"
	"os"
	"sort"
	"strings"

	"golang.org/x/tools/go/packages"
)

type edit struct {
	start, end int
	text       string
}

func isBF(t types.Type) bool {
	if t == nil {
		return false
	}
	if p, ok := t.(*types.Pointer); ok {
		t = p.Elem()
	}
	n, ok := t.(*types.Named)
	if !ok {
		return false
	}
	o := n.Obj()
	return o.Pkg() != nil && strings.HasSuffix(o.Pkg().Path(), "/internal/books/store") && (o.Name() == "BookFile" || o.Name() == "BookFileCore")
}

func main() {
	apply := len(os.Args) > 1 && os.Args[1] == "apply"
	cfg := &packages.Config{Mode: packages.LoadAllSyntax, Tests: true}
	pkgs, err := packages.Load(cfg, "./internal/...", "./cmd/...", "./tools/...")
	if err != nil {
		panic(err)
	}
	edits := map[string][]edit{}
	seen := map[string]bool{}
	for _, p := range pkgs {
		for _, f := range p.Syntax {
			fname := p.Fset.File(f.Pos()).Name()
			if !strings.HasSuffix(fname, "_test.go") || seen[fname] {
				continue
			}
			seen[fname] = true
			src, _ := os.ReadFile(fname)
			off := func(pos token.Pos) int { return p.Fset.Position(pos).Offset }
			lines := strings.Split(string(src), "\n")
			manual := func(pos token.Pos, why string) {
				ps := p.Fset.Position(pos)
				fmt.Printf("MANUAL %s:%d [%s] %s\n", strings.TrimPrefix(ps.Filename, mustWd()+"/"), ps.Line, why, strings.TrimSpace(lines[ps.Line-1]))
			}
			isBFDur := func(e ast.Expr) bool {
				se, ok := e.(*ast.SelectorExpr)
				return ok && se.Sel.Name == "Duration" && isBF(p.TypesInfo.TypeOf(se.X))
			}
			// collect LHS selectors and kv keys to skip
			skip := map[ast.Node]bool{}
			ast.Inspect(f, func(n ast.Node) bool {
				if as, ok := n.(*ast.AssignStmt); ok {
					for _, l := range as.Lhs {
						skip[l] = true
					}
				}
				return true
			})
			var stack []ast.Node
			needDur := false
			wrapSel := func(se *ast.SelectorExpr) {
				needDur = true
				xs := string(src[off(se.Pos()):off(se.End())])
				edits[fname] = append(edits[fname], edit{off(se.Pos()), off(se.End()), "durtest.Sec(" + xs + ")"})
			}
			ast.Inspect(f, func(n ast.Node) bool {
				if n == nil {
					stack = stack[:len(stack)-1]
					return true
				}
				stack = append(stack, n)
				se, ok := n.(*ast.SelectorExpr)
				if !ok || !isBFDur(se) || skip[se] || len(stack) < 2 {
					return true
				}
				switch par := stack[len(stack)-2].(type) {
				case *ast.BinaryExpr:
					op := par.Op
					var other ast.Expr
					left := par.X == ast.Expr(se)
					if left {
						other = par.Y
					} else {
						other = par.X
					}
					switch op {
					case token.EQL, token.NEQ, token.LSS, token.GTR, token.LEQ, token.GEQ:
					default:
						wrapSel(se)
						return true
					}
					if isBFDur(other) {
						manual(se.Pos(), "ptr-vs-ptr")
						return true
					}
					tv := p.TypesInfo.Types[other]
					if tv.Value == nil || tv.Value.Kind() != constant.Int {
						wrapSel(se)
						return true
					}
					v, _ := constant.Int64Val(tv.Value)
					if !left {
						switch op {
						case token.LSS:
							op = token.GTR
						case token.GTR:
							op = token.LSS
						case token.LEQ:
							op = token.GEQ
						case token.GEQ:
							op = token.LEQ
						}
					}
					xs := string(src[off(se.Pos()):off(se.End())])
					unknown := (op == token.EQL && v == 0) || (op == token.LEQ && v == 0) || (op == token.LSS && v == 1)
					known := (op == token.NEQ && v == 0) || (op == token.GTR && v == 0) || (op == token.GEQ && v == 1)
					switch {
					case unknown:
						edits[fname] = append(edits[fname], edit{off(par.Pos()), off(par.End()), xs + " == nil"})
					case known:
						edits[fname] = append(edits[fname], edit{off(par.Pos()), off(par.End()), xs + " != nil"})
					default:
						wrapSel(se)
					}
				case *ast.CallExpr:
					// find arg index
					idx := -1
					for i, a := range par.Args {
						if a == ast.Expr(se) {
							idx = i
						}
					}
					if idx < 0 {
						manual(se.Pos(), "call-fun")
						return true
					}
					name := ""
					if fs, ok := par.Fun.(*ast.SelectorExpr); ok {
						name = fs.Sel.Name
					}
					fs, _ := par.Fun.(*ast.SelectorExpr)
					if (name == "Equal" || name == "Equalf" || name == "EqualValues") && idx == 2 && len(par.Args) >= 3 {
						tv := p.TypesInfo.Types[par.Args[1]]
						if tv.Value != nil && tv.Value.Kind() == constant.Int {
							if v, _ := constant.Int64Val(tv.Value); v == 0 {
								// Equal(t, 0, X, msg...) -> Nil(t, X, msg...)
								rest := ""
								if len(par.Args) > 3 {
									rest = ", " + string(src[off(par.Args[3].Pos()):off(par.Args[len(par.Args)-1].End())])
								}
								nn := "Nil"
								if name == "Equalf" {
									nn = "Nilf"
								}
								edits[fname] = append(edits[fname], edit{off(fs.Sel.Pos()), off(par.End() - 1), nn + "(" + string(src[off(par.Args[0].Pos()):off(par.Args[0].End())]) + ", " + string(src[off(se.Pos()):off(se.End())]) + rest})
								return true
							}
						}
					}
					if (name == "NotZero" || name == "Zero") && idx == 1 {
						nn := "NotNil"
						if name == "Zero" {
							nn = "Nil"
						}
						edits[fname] = append(edits[fname], edit{off(fs.Sel.Pos()), off(fs.Sel.End()), nn})
						return true
					}
					// param type check
					if sig, ok := p.TypesInfo.TypeOf(par.Fun).(*types.Signature); ok {
						pt := sig.Params()
						var t types.Type
						if idx < pt.Len() {
							t = pt.At(idx).Type()
						} else if sig.Variadic() && pt.Len() > 0 {
							t = pt.At(pt.Len() - 1).Type()
						}
						if sl, ok := t.(*types.Slice); ok && sig.Variadic() && idx >= pt.Len()-1 {
							t = sl.Elem()
						}
						if pp, ok := t.(*types.Pointer); ok {
							if b, ok := pp.Elem().(*types.Basic); ok && b.Kind() == types.Int {
								return true // callee wants *int
							}
						}
					}
					wrapSel(se)
				case *ast.AssignStmt:
					idx := -1
					for i, r := range par.Rhs {
						if r == ast.Expr(se) {
							idx = i
						}
					}
					if idx >= 0 && len(par.Lhs) == len(par.Rhs) {
						lt := p.TypesInfo.TypeOf(par.Lhs[idx])
						if pp, ok := lt.(*types.Pointer); ok {
							if b, ok := pp.Elem().(*types.Basic); ok && b.Kind() == types.Int {
								return true
							}
						}
						wrapSel(se)
					} else {
						manual(se.Pos(), "assign-shape")
					}
				case *ast.ReturnStmt, *ast.IndexExpr, *ast.ParenExpr, *ast.CompositeLit, *ast.UnaryExpr:
					manual(se.Pos(), fmt.Sprintf("%T", par))
				default:
					manual(se.Pos(), fmt.Sprintf("%T", par))
				}
				return true
			})
			if needDur {
				// add import after the database import line, else after first internal/ import
				text := string(src)
				imp := "\"github.com/falkcorp/audiobook-organizer/internal/testutil/durtest\""
				if !strings.Contains(text, imp) {
					ls := strings.Split(text, "\n")
					at := -1
					for i, l := range ls {
						if strings.Contains(l, "falkcorp/audiobook-organizer/internal/") && strings.HasPrefix(strings.TrimSpace(l), "\"") || strings.Contains(l, "falkcorp/audiobook-organizer/internal/") && strings.Contains(l, " \"") {
							at = i
						}
						if strings.HasPrefix(l, "func ") || strings.HasPrefix(l, "type ") {
							break
						}
					}
					if at < 0 {
						manual(f.Pos(), "no import anchor")
					} else {
						importEdits[fname] = at
					}
				}
			}
		}
	}
	if !apply {
		return
	}
	for fname, es := range edits {
		src, _ := os.ReadFile(fname)
		sort.Slice(es, func(i, j int) bool { return es[i].start > es[j].start })
		last := len(src) + 1
		for _, e := range es {
			if e.end > last {
				continue
			}
			src = append(src[:e.start:e.start], append([]byte(e.text), src[e.end:]...)...)
			last = e.start
		}
		os.WriteFile(fname, src, 0o644)
	}
	for fname, at := range importEdits {
		_ = at
		src, _ := os.ReadFile(fname)
		ls := strings.Split(string(src), "\n")
		// recompute anchor: last internal/ import line before first func/type
		anchor := -1
		for i, l := range ls {
			if strings.Contains(l, "falkcorp/audiobook-organizer/internal/") && strings.Contains(l, "\"") && !strings.Contains(l, "(") {
				anchor = i
			}
			if strings.HasPrefix(l, "func ") || strings.HasPrefix(l, "type ") {
				break
			}
		}
		if anchor < 0 {
			fmt.Println("NO ANCHOR", fname)
			continue
		}
		ls = append(ls[:anchor+1], append([]string{"\t\"github.com/falkcorp/audiobook-organizer/internal/testutil/durtest\""}, ls[anchor+1:]...)...)
		os.WriteFile(fname, []byte(strings.Join(ls, "\n")), 0o644)
	}
	fmt.Println("files edited:", len(edits))
}

var importEdits = map[string]int{}

var wd string

func mustWd() string {
	if wd == "" {
		wd, _ = os.Getwd()
	}
	return wd
}
