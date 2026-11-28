// file: core/archtest/guards/corescopes_test.go
// version: 1.0.0
// guid: 7e09cfc0-e477-42c8-bd49-659d38447f0f
// last-edited: 2026-11-28

package guards

import (
	"path/filepath"
	"strings"
	"testing"
)

const coreModule = "github.com/falkcorp/mediacore"

func noExemption(string) bool { return false }

// TestCoreScopesOnlyDetection plants a violation in every form the scan claims
// (the fail fixture, one want marker per violation) and the neighbours it must
// leave alone (pass), so a scan that stopped seeing a form fails here instead
// of passing over core. The forms are the review's: an untyped const to Need,
// a constant concatenation, Permission(ns+":x"), Checker.Has, httpapi.Forbidden,
// a Permission-typed struct field, a map key, an import alias, a return and an
// assignment, plus the string parsers, a slice element and a PermissionSpec.
func TestCoreScopesOnlyDetection(t *testing.T) {
	got, checked, err := foreignScopeFindings(coreModule, []string{guardFixture("scopes/fail")}, noExemption)
	if err != nil {
		t.Fatal(err)
	}
	if checked != 1 {
		t.Fatalf("read %d packages, want 1", checked)
	}
	checkWants(t, filepath.Join("testdata", "scopes", "fail"), got)

	got, _, err = foreignScopeFindings(coreModule, []string{guardFixture("scopes/pass")}, noExemption)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range got {
		t.Errorf("the pass fixture was flagged: %s", f)
	}
}

// TestCoreScopesOnlyDocumentedGaps: a scope built at run time has no constant
// value, so the scan reports nothing for it. That is the gap the doc comment
// names (fmt.Sprintf, Permission(variable+":x"), a scope read from a table);
// this pins that it is a gap and not a form the scan half-covers.
func TestCoreScopesOnlyDocumentedGaps(t *testing.T) {
	got, _, err := foreignScopeFindings(coreModule, []string{guardFixture("scopes/gap")}, noExemption)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("the gap fixture was flagged %v: update the doc comment of CoreScopesOnly and the PR's list of gaps", got)
	}
}

// TestCoreScopesOnlyExemptions: an exempt package is loaded and skipped, the
// others are still reported, and a load in which nothing was read is no pass.
func TestCoreScopesOnlyExemptions(t *testing.T) {
	exempt := func(rel string) bool { return strings.HasSuffix(rel, "/exempt") }
	got, checked, err := foreignScopeFindings(coreModule,
		[]string{guardFixture("scopes/fail"), guardFixture("scopes/exempt")}, exempt)
	if err != nil {
		t.Fatal(err)
	}
	if checked != 1 {
		t.Errorf("read %d packages, want 1 (the exempt one skipped)", checked)
	}
	if len(got) == 0 {
		t.Fatal("the fail fixture reported nothing beside an exempt package")
	}
	for _, f := range got {
		if strings.Contains(f.File, "/exempt/") {
			t.Errorf("the exempt package was reported: %s", f)
		}
	}
	if all, n, err := foreignScopeFindings(coreModule, []string{guardFixture("scopes/exempt")}, func(string) bool { return true }); err != nil || n != 0 || len(all) != 0 {
		t.Errorf("an all-exempt load read %d packages with %d findings (err %v), want 0 and 0", n, len(all), err)
	}
	for rel, want := range map[string]bool{
		"coretest": true, "archtest": true, "archtest/guards": true, "rbac/rbactest": true, "tasks/taskstest": true,
		"examples": true, "examples/hello": true,
		"tasks/taskstest/parity": true, "tasks/taskstest/schedule": true, "tasks/taskstest/parity/deeper": true,
		"kernel": false, "httpapi": false, "rbac": false, "internal/audithttp": false, "examplesque": false, "tasks": false, "tasks/engine": false, "tasks/parity": false,
	} {
		if got := coreScopeExempt(rel); got != want {
			t.Errorf("coreScopeExempt(%q) = %v, want %v", rel, got, want)
		}
	}
}

// TestCoreScopesOnlyOverCore: no non-test core code names a scope outside the
// core namespace (W6): the app's scope on a core route is the app's
// RouteCoRequirements, never core's literal.
func TestCoreScopesOnlyOverCore(t *testing.T) {
	CoreScopesOnly(t, coreModule, coreModule+"/...")
}
