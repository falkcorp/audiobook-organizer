// file: internal/books/resources_test.go
// version: 1.0.0
// guid: 5c9a2d3e-81b4-4f07-b6a1-3e7d0c42f958
// last-edited: 2026-11-18

package books

import (
	"context"
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"slices"
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/maintenance/jobs"
	"github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/internal/repairs"
	"github.com/falkcorp/audiobook-organizer/pkg/ops"
	"github.com/falkcorp/mediacore/module"
	"github.com/falkcorp/mediacore/tasks"
)

// opsResourceVocabulary is every pkg/ops Resource constant's value, read from
// the source of pkg/ops/effects.go (the package keeps no list of its own), so
// a constant added there is seen here without anyone remembering to add it.
func opsResourceVocabulary(t *testing.T) []string {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "../../pkg/ops/effects.go", nil, 0)
	require.NoError(t, err)
	var out []string
	for _, decl := range f.Decls {
		gd, ok := decl.(*ast.GenDecl)
		if !ok || gd.Tok != token.CONST {
			continue
		}
		for _, sp := range gd.Specs {
			vs := sp.(*ast.ValueSpec)
			if id, ok := vs.Type.(*ast.Ident); !ok || id.Name != "Resource" {
				continue
			}
			for _, v := range vs.Values {
				lit, ok := v.(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					continue
				}
				name, err := strconv.Unquote(lit.Value)
				require.NoError(t, err)
				out = append(out, name)
			}
		}
	}
	slices.Sort(out)
	require.Contains(t, out, string(ops.ResBooks), "the scan found no Resource constants: the path or the parse is broken")
	return out
}

// registeredResources runs books' Register on a Registrar with no storage and
// returns what it recorded with Registrar.Resources, after checking the
// registrar took every name (an empty or repeated kind is a
// ProblemInvalidRegistration that would refuse every production boot at P7).
// With no legacy range, Register returns before it builds a store or a server,
// so this is the declaration alone.
func registeredResources(t *testing.T) []string {
	t.Helper()
	withConfig(t, config.Config{})
	m := Module().(*booksModule)
	r := module.NewRegistrar(module.RegistrarConfig{Context: t.Context(), Deps: module.Deps{Module: ID}})
	require.NoError(t, m.Register(r))
	require.NoError(t, r.Err())
	got := slices.Clone(r.Close().Resources)
	slices.Sort(got)
	return got
}

// TestRegister_DeclaresEveryResourceKind: books declares, with
// Registrar.Resources, exactly the pkg/ops resource vocabulary. Every native
// kind's Effects name only pkg/ops constants (internal/plugins/maintenance
// TestT6Kinds_EffectsUseOnlyOpsResources compares by value over all six
// converted groups, and TestT6G1Kinds_EffectsNamesAreConstantsNotLiterals scans
// the package for a literal), so a declared set equal to the vocabulary is a
// declared set that contains every kind's names. A constant added to pkg/ops
// fails here until books declares it; a name declared that pkg/ops does not
// define fails here too, so the list cannot grow a name no kind may use
// (`aux_stores` was one).
func TestRegister_DeclaresEveryResourceKind(t *testing.T) {
	require.Equal(t, opsResourceVocabulary(t), registeredResources(t))
}

// TestTaskResourceKinds_ArePlainAndUnique: no empty and no repeated name,
// stated on the list itself (Register's own check is the registrar's).
func TestTaskResourceKinds_ArePlainAndUnique(t *testing.T) {
	kinds := taskResourceKinds()
	require.NotEmpty(t, kinds)
	seen := map[string]bool{}
	for _, k := range kinds {
		require.NotEmpty(t, k)
		require.False(t, seen[k], "resource kind %q is listed twice", k)
		seen[k] = true
	}
}

// requireEffectsDeclared is the kernel's P7 rule (core/kernel tasks_checks.go
// moduleTaskProblems) read directly over a kind list: every resource a kind
// reads or writes in module books is declared, and every write is to books. It
// is the predicate the kernel applies, not a boot of the kernel: a boot also
// needs stores, a ledger, permissions and schedules that are the flip's. It
// returns how many write refs it saw, so a caller can refuse a list that
// declares none.
func requireEffectsDeclared(t *testing.T, declared []string, defs []tasks.Definition) (writes int) {
	t.Helper()
	require.NotEmpty(t, defs, "the kind list is empty: the check would run over nothing")
	for _, d := range defs {
		e := tasks.CommonOf(d).Effects
		for _, ref := range e.ReadSet() {
			if ref.Module == string(ID) {
				require.Contains(t, declared, ref.Kind, "%s reads resource kind %q, which books did not declare with Registrar.Resources", d.Kind(), ref.Kind)
			}
		}
		for _, ref := range e.WriteSet() {
			writes++
			require.Equal(t, string(ID), ref.Module, "%s writes %s/%s, another module's resource", d.Kind(), ref.Module, ref.Kind)
			require.Contains(t, declared, ref.Kind, "%s writes resource kind %q, which books did not declare with Registrar.Resources", d.Kind(), ref.Kind)
		}
	}
	return writes
}

// TestRegister_JobKindsEffectsAreDeclared: every kind of jobs.Kinds, an
// exported kind list, passes the kernel's rule against what Register declared.
func TestRegister_JobKindsEffectsAreDeclared(t *testing.T) {
	defs, err := jobs.Kinds(jobs.KindDeps{}, nil)
	require.NoError(t, err)
	require.NotZero(t, requireEffectsDeclared(t, registeredResources(t), defs), "no jobs kind writes anything")
}

// batmanFixer is a repairs.Fixer that plans and applies nothing: the repairs
// shell declares one Effects value for every fixer (books and series,
// internal/repairs/kinds.go common), so any fixer shows what a native fixer
// kind declares.
type batmanFixer struct{}

func (batmanFixer) ID() string          { return "mend-the-batsignal" }
func (batmanFixer) Title() string       { return "Mend the bat signal" }
func (batmanFixer) Description() string { return "Replaces a cracked lens." }
func (batmanFixer) Meta() repairs.Meta  { return repairs.Meta{} }
func (batmanFixer) Plan(context.Context, json.RawMessage, registry.Reporter) ([]repairs.Row, error) {
	return nil, nil
}
func (batmanFixer) Replan(context.Context, json.RawMessage, repairs.Row, registry.Reporter) (repairs.Row, error) {
	return repairs.Row{}, nil
}
func (batmanFixer) Apply(context.Context, *repairs.Writer, repairs.Row) error { return nil }

// TestRegister_FixerKindsEffectsAreDeclared: the same rule over repairs.Kinds,
// the other exported kind list (F5-02's seam note: the definitions write
// books/books and books/series, which books must declare).
func TestRegister_FixerKindsEffectsAreDeclared(t *testing.T) {
	defs := repairs.Kinds([]repairs.Fixer{batmanFixer{}}, repairs.KindDeps{})
	require.NotZero(t, requireEffectsDeclared(t, registeredResources(t), defs), "no fixer kind writes anything")
}
