// file: core/module/module.go
// version: 1.0.1
// guid: 3e79e4b4-4709-49f9-9193-d1023779795d
// last-edited: 2026-11-07

// Package module is what an app's module implements and what the kernel
// hands it (06 §6.4.1-§6.4.4, §6.4.7): the Module interface, its Manifest,
// the Registrar of Register and the module-scoped Deps. It is layer L7.
//
// This is the R1 surface (06 §6.4.10 R1 row) minus the members whose package
// lands later, which are absent, not stubbed: Manifest.Settings (config, F3),
// Manifest.Storage and Manifest.ResetHook (storage, R1-20/R1-25) and
// Deps.Storage (R1-20). Every later member is added additively.
package module

import (
	"regexp"
	"slices"

	"github.com/falkcorp/mediacore/httpapi"
	"github.com/falkcorp/mediacore/log"
	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/ui"
)

// ID is a module identifier: ^[a-z][a-z0-9]{1,15}$ and not in ReservedIDs().
// ID.Valid reports both.
type ID string

// digitsToken is httpapi.ReservedSegments' token for the segment pattern
// ^v[0-9]+$ ("v" followed by digits, 03 §3.6), not a literal segment.
const digitsToken = "v<digits>"

func idPattern() *regexp.Regexp      { return regexp.MustCompile(`^[a-z][a-z0-9]{1,15}$`) }
func versionSegment() *regexp.Regexp { return regexp.MustCompile(`^v[0-9]+$`) }

// Valid reports whether id matches the naming rule and is not reserved.
func (id ID) Valid() bool {
	return idPattern().MatchString(string(id)) && !reserved(string(id), ReservedIDs())
}

// reserved reports whether s is in list, reading the "v<digits>" token as
// its pattern.
func reserved(s string, list []string) bool {
	for _, r := range list {
		if r == s || (r == digitsToken && versionSegment().MatchString(s)) {
			return true
		}
	}
	return false
}

// IsReserved reports whether id is reserved by list, with list's
// "v<digits>" entry read as the pattern ^v[0-9]+$. Kernel-only: P1 checks IDs
// against ReservedIDs() plus the app inputs (06 §6.4.1) with it.
func IsReserved(id ID, list []string) bool { return reserved(string(id), list) }

// ReservedIDs returns the union of httpapi.ReservedSegments (server paths),
// ui.ReservedSegments (shell routes, 07 §7.4.3), rbac.ReservedNamespaces
// ("core", "custom") and log.ReservedModuleIDs ("process") (03 §3.6). It is static, so P1 can evaluate it; the
// kernel adds the two app inputs in P1 (the first segment of every
// manifest's LegacyPrefixes and app.Options.ReservedAPINamespaces, COR2-19,
// FE2-11), and P7 re-checks IDs against every mount's TopSegments and
// SPAShared. "v<digits>" is a token for the pattern ^v[0-9]+$. A fresh slice
// per call (R5).
func ReservedIDs() []string {
	out := httpapi.ReservedSegments()
	out = append(out, ui.ReservedSegments()...)
	out = append(out, rbac.ReservedNamespaces()...)
	out = append(out, log.ReservedModuleIDs()...)
	slices.Sort(out)
	return slices.Compact(out)
}

// Module is what an app hands to app.New. Implementations are plain values
// constructed by the app's main (e.g. hello.New()); they must not do work in
// their constructor beyond capturing options.
type Module interface {
	// Manifest returns the module's static declaration. It must be pure,
	// cheap and deterministic: the kernel and tests may call it any number of
	// times, from any goroutine, before and after Register.
	Manifest() Manifest

	// Register is called exactly once per boot, for an enabled module only.
	// It constructs the module's services from r.Deps() and records routes,
	// hooks and workers. Recording has no effect until every module has
	// registered (P7). r.Context() carries app.New's ctx. Register must be
	// side-effect-free outside the handles in Deps: no file, network or
	// subprocess I/O and no goroutine (work of that kind belongs in a Hook or
	// Worker), so app.Describe can run it (COR2-13). A returned error aborts
	// boot (fail-closed).
	Register(r *Registrar) error
}
