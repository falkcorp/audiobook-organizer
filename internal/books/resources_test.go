// file: internal/books/resources_test.go
// version: 1.0.0
// guid: 5c9a2d3e-81b4-4f07-b6a1-3e7d0c42f958
// last-edited: 2026-11-18

package books

import (
	"go/ast"
	"go/parser"
	"go/token"
	"slices"
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/maintenance/jobs"
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

// TestRegister_JobKindsEffectsAreDeclared: the kernel's P7 rule (core/kernel
// tasks_checks.go moduleTaskProblems) read directly over jobs.Kinds, the one
// exported kind list: every resource a kind reads or writes in module books is
// declared, and every write is to books. This is the predicate the kernel
// applies, not a boot of the kernel: a boot also needs stores, a ledger,
// permissions and schedules that are the flip's. The six converted maintenance
// groups' lists are unexported with their fake dependencies in test files, so
// they are covered by the vocabulary equality above.
func TestRegister_JobKindsEffectsAreDeclared(t *testing.T) {
	declared := registeredResources(t)
	defs, err := jobs.Kinds(jobs.KindDeps{}, nil)
	require.NoError(t, err)
	require.NotEmpty(t, defs, "jobs.Kinds lists no kind: the maintenance jobs are not registered")
	var wrote int
	for _, d := range defs {
		e := tasks.CommonOf(d).Effects
		for _, ref := range e.ReadSet() {
			if ref.Module == string(ID) {
				require.Contains(t, declared, ref.Kind, "%s reads resource kind %q, which books did not declare with Registrar.Resources", d.Kind(), ref.Kind)
			}
		}
		for _, ref := range e.WriteSet() {
			wrote++
			require.Equal(t, string(ID), ref.Module, "%s writes %s/%s, another module's resource", d.Kind(), ref.Module, ref.Kind)
			require.Contains(t, declared, ref.Kind, "%s writes resource kind %q, which books did not declare with Registrar.Resources", d.Kind(), ref.Kind)
		}
	}
	require.NotZero(t, wrote, "no jobs kind writes anything: the check ran over nothing")
}
