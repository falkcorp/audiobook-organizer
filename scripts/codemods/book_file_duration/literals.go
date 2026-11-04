// file: scripts/codemods/book_file_duration/literals.go
// version: 1.0.0
// guid: 4a7d2c19-8e53-4b06-91fa-d3c6e0b85a27
// last-edited: 2026-11-04

//go:build ignore

// literals is step 1 of the BookFile.Duration int -> *int test-literal
// rewrite (PLAN: docs of the book_file duration work, PR 3). It was run ONCE,
// BEFORE the field's type changed, from the repo root:
//
//	go run scripts/codemods/book_file_duration/literals.go
//
// with a temporary stub in package database so the tree type-checked:
//
//	func Secs(sec int) *int { return &sec }
//	func unmarshalBookFile(data []byte, f *BookFile) error { return json.Unmarshal(data, f) }
//
// It rewrites, in _test.go files only, every keyed `Duration: <int expr>` in a
// database.BookFile / BookFileCore literal to `database.Secs(<expr>)` (a
// literal 0 is dropped: the field is omitted, i.e. not measured) and every
// `x.Duration = <int expr>` assignment the same way. Book.Duration and other
// structs' Duration fields are untouched because it matches on the
// operand's type, not the field name. Then run gofmt. reads.go is step 2.
//
// Kept so the rewrite can be audited and re-run on a branch that added more
// literals before it merged. Not part of any build (build tag ignore).

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
	return o.Pkg() != nil && strings.HasSuffix(o.Pkg().Path(), "/internal/database") && (o.Name() == "BookFile" || o.Name() == "BookFileCore")
}

func main() {
	cfg := &packages.Config{Mode: packages.LoadAllSyntax, Tests: true}
	pkgs, err := packages.Load(cfg, "./internal/...", "./cmd/...", "./tools/...", "./web/...")
	if err != nil {
		panic(err)
	}
	edits := map[string][]edit{}
	seen := map[string]bool{}
	manual := []string{}
	for _, p := range pkgs {
		for _, f := range p.Syntax {
			fname := p.Fset.File(f.Pos()).Name()
			if !strings.HasSuffix(fname, "_test.go") {
				continue
			}
			key := fname
			if seen[key] {
				continue
			}
			seen[key] = true
			src, _ := os.ReadFile(fname)
			// qualifier
			qual := ""
			if !strings.HasSuffix(p.PkgPath, "/internal/database") && !strings.HasSuffix(strings.TrimSuffix(p.PkgPath, "_test"), "/internal/database") {
				qual = "?"
				for _, im := range f.Imports {
					if strings.HasSuffix(strings.Trim(im.Path.Value, `"`), "/internal/database") {
						if im.Name != nil {
							qual = im.Name.Name + "."
						} else {
							qual = "database."
						}
					}
				}
			} else if strings.HasSuffix(p.PkgPath, "_test") {
				qual = "database."
				for _, im := range f.Imports {
					if strings.HasSuffix(strings.Trim(im.Path.Value, `"`), "/internal/database") && im.Name != nil {
						qual = im.Name.Name + "."
					}
				}
			}
			off := func(pos token.Pos) int { return p.Fset.Position(pos).Offset }
			wrap := func(e ast.Expr) string {
				tv := p.TypesInfo.Types[e]
				if tv.Value != nil && tv.Value.Kind() == constant.Int {
					if v, _ := constant.Int64Val(tv.Value); v == 0 {
						return "nil"
					}
				}
				if qual == "?" {
					manual = append(manual, fname+": needs database import")
					qual = "database."
				}
				return qual + "Secs(" + string(src[off(e.Pos()):off(e.End())]) + ")"
			}
			isInt := func(e ast.Expr) bool {
				t := p.TypesInfo.TypeOf(e)
				if t == nil {
					return false
				}
				b, ok := t.Underlying().(*types.Basic)
				return ok && b.Info()&types.IsInteger != 0
			}
			ast.Inspect(f, func(n ast.Node) bool {
				switch x := n.(type) {
				case *ast.CompositeLit:
					if !isBF(p.TypesInfo.TypeOf(x)) {
						return true
					}
					for _, el := range x.Elts {
						kv, ok := el.(*ast.KeyValueExpr)
						if !ok {
							continue
						}
						k, ok := kv.Key.(*ast.Ident)
						if !ok || k.Name != "Duration" || !isInt(kv.Value) {
							continue
						}
						tv := p.TypesInfo.Types[kv.Value]
						zero := false
						if tv.Value != nil && tv.Value.Kind() == constant.Int {
							if v, _ := constant.Int64Val(tv.Value); v == 0 {
								zero = true
							}
						}
						if zero {
							// remove element incl. trailing comma and spacing
							s, e := off(kv.Pos()), off(kv.End())
							if e < len(src) && src[e] == ',' {
								e++
							}
							// extend start back over line indentation if sole on line
							ls := s
							for ls > 0 && (src[ls-1] == ' ' || src[ls-1] == '\t') {
								ls--
							}
							le := e
							for le < len(src) && (src[le] == ' ' || src[le] == '\t') {
								le++
							}
							if ls > 0 && src[ls-1] == '\n' && le < len(src) && src[le] == '\n' {
								edits[fname] = append(edits[fname], edit{ls, le + 1, ""})
							} else {
								edits[fname] = append(edits[fname], edit{s, le, ""})
							}
						} else {
							edits[fname] = append(edits[fname], edit{off(kv.Value.Pos()), off(kv.Value.End()), wrap(kv.Value)})
						}
					}
				case *ast.AssignStmt:
					if x.Tok != token.ASSIGN || len(x.Lhs) != len(x.Rhs) {
						return true
					}
					for i, l := range x.Lhs {
						se, ok := l.(*ast.SelectorExpr)
						if !ok || se.Sel.Name != "Duration" || !isBF(p.TypesInfo.TypeOf(se.X)) || !isInt(x.Rhs[i]) {
							continue
						}
						edits[fname] = append(edits[fname], edit{off(x.Rhs[i].Pos()), off(x.Rhs[i].End()), wrap(x.Rhs[i])})
					}
				}
				return true
			})
		}
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
	fmt.Println("files edited:", len(edits))
	for _, m := range manual {
		fmt.Println(m)
	}
}
