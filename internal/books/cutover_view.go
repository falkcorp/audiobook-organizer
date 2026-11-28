// file: internal/books/cutover_view.go
// version: 1.0.0
// guid: 2e9c5a74-1b38-4f60-8d27-a4c1e7b09d53
// last-edited: 2026-11-28

package books

import (
	"fmt"
	"slices"
	"strings"
	"sync"

	"github.com/falkcorp/audiobook-organizer/internal/auth"
	"github.com/falkcorp/audiobook-organizer/internal/books/cutover/kindrename"
	"github.com/falkcorp/audiobook-organizer/internal/plugins/maintenance"
	"github.com/falkcorp/audiobook-organizer/internal/repairs"

	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/tasks"
)

// fixerLookup is the Repairs registry's read side (opaccess.FixerLookup).
type fixerLookup interface {
	Get(id string) (repairs.Fixer, bool)
}

// importedKindView is M4's ViewOf: HEAD's read gate for the runs of an imported
// kind, in the rule core's View takes (F5-18 reach R3; m4.ViewOfFunc).
//
//   - core.*: not stamped. Core registers those kinds and its boot upsert owns
//     their snapshots.
//   - books.repairs.<fixer>: opaccess.Reader.fixerPermitted
//     (internal/server/handlers/opaccess/repairs.go). A fixer this build
//     registers needs every permission repairs.RequiredPermissions names; a
//     retired one keeps repairs.RetiredFixerPermissions'; any other fixer id
//     has an unknown scope and needs every permission there is (auth.All()).
//     Each atom is resolved to its native scopes through books' atom table, and
//     the kind's rule is AllOf the lot, which is "holds every one of the atoms".
//   - every other kind (books.<id>, books.retired.<id>): HEAD's MayRead is true
//     for an operation that is not a Repairs one, so the kind takes core's
//     default View (core:tasks.view, which library.view carries).
//
// A Repairs atom that the table does not resolve is an error, not a guess: the
// step fails naming the kind, so the owner is told what HEAD's gate cannot be
// translated for.
func importedKindView(fixers func() fixerLookup) func(k tasks.Kind) (tasks.KindView, bool, error) {
	return func(k tasks.Kind) (tasks.KindView, bool, error) {
		name := string(k)
		switch {
		case strings.HasPrefix(name, "core."):
			return tasks.KindView{}, false, nil
		case strings.HasPrefix(name, kindrename.RepairsKindPrefix):
			req, err := fixerView(fixers(), strings.TrimPrefix(name, kindrename.RepairsKindPrefix))
			if err != nil {
				return tasks.KindView{}, false, err
			}
			return tasks.KindView{Requires: req}, true, nil
		default:
			return tasks.KindView{}, true, nil
		}
	}
}

// fixerView is fixerPermitted's rule for fixer id as a requirement.
func fixerView(fixers fixerLookup, id string) (rbac.Requirement, error) {
	var atoms []auth.Permission
	if f, ok := fixers.Get(id); ok && id != "" {
		atoms = repairs.RequiredPermissions(f)
	} else if retired, isRetired := repairs.RetiredFixerPermissions(id); isRetired {
		atoms = retired
	} else {
		atoms = auth.All()
	}
	var scopes []rbac.Permission
	for _, a := range atoms {
		ss, ok := atomScopes(a)
		if !ok {
			return rbac.Requirement{}, fmt.Errorf("fixer %q needs the atom %q, which books' atom table does not resolve", id, a)
		}
		for _, s := range ss {
			if !slices.Contains(scopes, s) {
				scopes = append(scopes, s)
			}
		}
	}
	// rbac.AllOf of no scope, or of one that does not parse, is the zero
	// Requirement, which a stamp would record as "unrestricted": the opposite of
	// what an unresolved gate must do.
	req := rbac.AllOf(scopes...)
	if len(scopes) == 0 || req.IsZero() {
		return rbac.Requirement{}, fmt.Errorf("fixer %q: the scopes %v do not make a requirement", id, scopes)
	}
	return req, nil
}

// headFixers is the fixers HEAD's Repairs lane registers, the registry the
// opaccess reader asks, built on first use (the step list is built by Describe
// and the offline plans too, and NewRegistry puts a gauge on the meter). A bare
// maintenance plugin is enough: a fixer's ID and RequiredPermissions read
// nothing from the plugin's dependencies, and the fixers are the ones
// Plugin.Repairs registers, so the list cannot drift from HEAD's.
var headRegistry = sync.OnceValue(func() *repairs.Registry { return maintenance.New(nil).Repairs() })

func headFixers() fixerLookup { return headRegistry() }
