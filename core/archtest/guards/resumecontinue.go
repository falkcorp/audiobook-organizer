// file: core/archtest/guards/resumecontinue.go
// version: 1.2.0
// guid: 0f9870cc-093b-48cb-82a9-77a7a0753d38
// last-edited: 2026-11-10

package guards

import (
	"fmt"
	"go/ast"
	"go/constant"
	"go/token"
	"go/types"
	"strings"
	"testing"

	"golang.org/x/tools/go/packages"
)

// ResumeFromZeroDirective marks a ResumeContinue Task-shape definition whose
// Run reaches no checkpoint, with the reason a from-zero restart is safe
// (05-tasks §5T.3.6). It goes on the tasks.Task call's line or the line above.
const ResumeFromZeroDirective = "//archtest:resume-from-zero"

// ResumeContinueCheckpoints fails t when a Task-shape definition in the
// packages pkgs names declares Resume: tasks.ResumeContinue and its Run reaches
// neither a .Checkpoint( call on *tasks.RC nor a tasks.RunItems/RunSeq call
// with a Watermark, through same-package functions at any depth, unless the
// definition carries ResumeFromZeroDirective with a reason (05-tasks §5T.3.6,
// successor of ABO's internal/arch/resume_restart_guard_test.go). Batch and
// Fixer pass by construction (the runner's chunk ledger).
//
// fromZero is the caller's shrink-only allowlist, by kind: only a listed kind
// may carry the directive, and its kind must be a constant. A directive on an
// unlisted kind and a listed kind no definition marks both fail, so moving the
// directive from one kind to another is caught, not just a change in the
// count. Core passes nil. ABO's 21-entry allowlist (HEAD's
// restartWithoutCheckpoint) becomes its list, entry by entry, as each kind
// converts with its reason moved onto the definition.
func ResumeContinueCheckpoints(t testing.TB, fromZero []string, pkgs ...string) {
	t.Helper()
	findings, err := resumeContinueFindings(pkgs, fromZero)
	if err != nil {
		t.Fatalf("ResumeContinueCheckpoints: %v", err)
		return
	}
	for _, f := range findings {
		t.Errorf("%s", f)
	}
}

func resumeContinueFindings(patterns, fromZero []string) ([]GuardFinding, error) {
	pkgs, err := loadTyped(patterns)
	if err != nil {
		return nil, err
	}
	idx := indexFuncDecls(pkgs)
	var out []GuardFinding
	listed := map[string]bool{}
	for _, k := range fromZero {
		if listed[k] {
			out = append(out, GuardFinding{File: "(fromZero)", Msg: fmt.Sprintf("%s is listed twice", k)})
		}
		listed[k] = true
	}
	marks := map[string]bool{}
	for _, p := range pkgs {
		if underTasks(p.PkgPath) {
			continue
		}
		files := nonTestFiles(p)
		dirs := directives(p.Fset, files, ResumeFromZeroDirective)
		for _, f := range files {
			ast.Inspect(f, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok || tasksFuncName(p.TypesInfo, call) != "Task" || len(call.Args) != 2 {
					return true
				}
				pos := p.Fset.Position(call.Pos())
				report := func(format string, a ...any) {
					out = append(out, GuardFinding{File: pos.Filename, Line: pos.Line, Msg: fmt.Sprintf(format, a...)})
				}
				kind := kindText(p.TypesInfo, call.Args[0])
				spec, ok := ast.Unparen(call.Args[1]).(*ast.CompositeLit)
				if !ok {
					report("%s: the TaskSpec is not a literal, so its Resume policy cannot be read; build it inline", kind)
					return true
				}
				fields := keyed(spec)
				cont, known := resumeIsContinue(p, idx, fields["Common"])
				if r, ok := fields["Resume"]; ok {
					// Common's fields set as promoted fields (Go 1.27):
					// TaskSpec{Title: ..., Resume: ...} with no Common key.
					cont, known = isResumeContinue(p.TypesInfo, r), true
				}
				if !known {
					report("%s: Resume cannot be read from Common (build Common as a literal, a package variable or a same-package function returning one)", kind)
					return true
				}
				if !cont {
					return true
				}
				reason, marked := dirs[pos.Filename][pos.Line]
				if !marked {
					reason, marked = dirs[pos.Filename][pos.Line-1]
				}
				reaches := false
				if run, ok := fields["Run"]; ok {
					reaches = reachesCheckpoint(p, idx, run)
				}
				constKind, isConst := constString(p.TypesInfo, call.Args[0])
				if marked && isConst {
					marks[constKind] = true
				}
				switch {
				case reaches && marked:
					report("%s checkpoints; delete its %s directive", kind, ResumeFromZeroDirective)
				case reaches:
				case marked && reason == "":
					report("%s: %s needs the reason a from-zero restart is safe", kind, ResumeFromZeroDirective)
				case marked && !isConst:
					report("%s: %s needs a constant kind, so the fromZero list can name it", kind, ResumeFromZeroDirective)
				case marked && !listed[constKind]:
					report("%s carries %s but is not in the fromZero list; the list may only shrink, so checkpoint it instead (a directive does not move between kinds)", kind, ResumeFromZeroDirective)
				case marked:
				default:
					report("%s declares tasks.ResumeContinue but its Run reaches no rc.Checkpoint call and no RunItems/RunSeq with a Watermark, so a resume starts from zero; checkpoint, pick another Resume policy, or add %s <reason>", kind, ResumeFromZeroDirective)
				}
				return true
			})
		}
	}
	for _, k := range fromZero {
		if !marks[k] {
			out = append(out, GuardFinding{File: "(fromZero)", Msg: fmt.Sprintf("%s is in the fromZero list but no definition of that kind carries %s; delete the entry", k, ResumeFromZeroDirective)})
			marks[k] = true // one finding per entry, even when listed twice
		}
	}
	sortFindings(out)
	return out, nil
}

func kindText(info *types.Info, e ast.Expr) string {
	if k, ok := constString(info, e); ok {
		return k
	}
	return "a Task definition"
}

// constString returns e's value when it is a constant string.
func constString(info *types.Info, e ast.Expr) (string, bool) {
	if tv, ok := info.Types[e]; ok && tv.Value != nil && tv.Value.Kind() == constant.String {
		return constant.StringVal(tv.Value), true
	}
	return "", false
}

// resumeIsContinue reads Common's Resume: from a tasks.Common literal, a
// package variable initialised with one, or a same-package function whose
// return statements build one. known is false when none of those applies.
func resumeIsContinue(p *packages.Package, idx funcIndex, common ast.Expr) (cont, known bool) {
	if common == nil {
		return false, true // zero Common: Resume unset, refused at registration
	}
	var lits []*ast.CompositeLit
	switch x := ast.Unparen(common).(type) {
	case *ast.CompositeLit:
		lits = append(lits, x)
	case *ast.Ident:
		if v, ok := p.TypesInfo.Uses[x].(*types.Var); ok && v.Parent() == p.Types.Scope() {
			lits = commonLitsInit(p, v)
		}
	case *ast.CallExpr:
		if fn := funcOf(p.TypesInfo, x); fn != nil {
			if d, ok := idx[fn]; ok {
				lits = returnedCommonLits(d)
			}
		}
	}
	if len(lits) == 0 {
		return false, false
	}
	for _, lit := range lits {
		if r, ok := keyed(lit)["Resume"]; ok && isResumeContinue(p.TypesInfo, r) {
			return true, true
		}
	}
	return false, true
}

func commonLitsInit(p *packages.Package, v *types.Var) []*ast.CompositeLit {
	var out []*ast.CompositeLit
	for _, f := range p.Syntax {
		for _, d := range f.Decls {
			gd, ok := d.(*ast.GenDecl)
			if !ok {
				continue
			}
			for _, s := range gd.Specs {
				vs, ok := s.(*ast.ValueSpec)
				if !ok {
					continue
				}
				for i, n := range vs.Names {
					if p.TypesInfo.Defs[n] == v && i < len(vs.Values) {
						if lit, ok := ast.Unparen(vs.Values[i]).(*ast.CompositeLit); ok {
							out = append(out, lit)
						}
					}
				}
			}
		}
	}
	return out
}

func returnedCommonLits(d declIn) []*ast.CompositeLit {
	var out []*ast.CompositeLit
	ast.Inspect(d.decl.Body, func(n ast.Node) bool {
		if _, ok := n.(*ast.FuncLit); ok {
			return false
		}
		if r, ok := n.(*ast.ReturnStmt); ok && len(r.Results) == 1 {
			if lit, ok := ast.Unparen(r.Results[0]).(*ast.CompositeLit); ok && isTasksNamed(d.pkg.TypesInfo.TypeOf(lit), "Common") {
				out = append(out, lit)
			}
		}
		return true
	})
	return out
}

// isResumeContinue reports an expression whose constant value is
// tasks.ResumeContinue's, of type tasks.ResumePolicy.
func isResumeContinue(info *types.Info, e ast.Expr) bool {
	tv, ok := info.Types[e]
	if !ok || tv.Value == nil || !isTasksNamed(tv.Type, "ResumePolicy") {
		return false
	}
	n := types.Unalias(tv.Type).(*types.Named)
	c, ok := n.Obj().Pkg().Scope().Lookup("ResumeContinue").(*types.Const)
	return ok && constant.Compare(tv.Value, token.EQL, c.Val())
}

// reachesCheckpoint reports whether the Run expression (a func literal or a
// same-package function or method value) reaches rc.Checkpoint or a
// RunItems/RunSeq with a Watermark, following same-package functions it names.
func reachesCheckpoint(p *packages.Package, idx funcIndex, run ast.Expr) bool {
	seen := map[*types.Func]bool{}
	var walk func(n ast.Node, info *types.Info, pkg *types.Package) bool
	walk = func(n ast.Node, info *types.Info, pkg *types.Package) bool {
		found := false
		ast.Inspect(n, func(x ast.Node) bool {
			if found {
				return false
			}
			switch x := x.(type) {
			case *ast.CallExpr:
				if name := tasksFuncName(info, x); (name == "RunItems" || name == "RunSeq") && hasWatermark(info, x) {
					found = true
					return false
				}
			case *ast.SelectorExpr:
				if fn, ok := info.Uses[x.Sel].(*types.Func); ok && fn.Name() == "Checkpoint" {
					if sig := fn.Type().(*types.Signature); sig.Recv() != nil && isTasksNamed(sig.Recv().Type(), "RC") {
						found = true
						return false
					}
				}
			}
			if e, ok := x.(ast.Expr); ok {
				if fn := funcOf(info, e); fn != nil && fn.Pkg() == pkg && !seen[fn] {
					if d, ok := idx[fn]; ok {
						seen[fn] = true
						if walk(d.decl.Body, info, pkg) {
							found = true
							return false
						}
					}
				}
			}
			return true
		})
		return found
	}
	return walk(run, p.TypesInfo, p.Types)
}

// hasWatermark reports a RunItems/RunSeq call with an ItemsOptions literal
// whose Watermark is set and not the constant "".
func hasWatermark(info *types.Info, call *ast.CallExpr) bool {
	for _, a := range call.Args {
		lit, ok := ast.Unparen(a).(*ast.CompositeLit)
		if !ok || !isTasksNamed(info.TypeOf(lit), "ItemsOptions") {
			continue
		}
		w, ok := keyed(lit)["Watermark"]
		if !ok {
			continue
		}
		if tv := info.Types[w]; tv.Value != nil && tv.Value.Kind() == constant.String && strings.TrimSpace(constant.StringVal(tv.Value)) == "" {
			continue
		}
		return true
	}
	return false
}
