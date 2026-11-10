// file: core/config/imports_test.go
// version: 1.0.0
// guid: 4a9d2f6b-1e3c-4b78-8c5a-0f7e3d9b2c61
// last-edited: 2026-11-10

package config_test

import (
	"os/exec"
	"slices"
	"strings"
	"testing"
)

const mediacore = "github.com/falkcorp/mediacore/"

// goList runs go list -f format over pkgs and returns its lines.
func goList(t *testing.T, format string, deps bool, pkgs ...string) []string {
	t.Helper()
	args := []string{"list", "-f", format}
	if deps {
		args = append(args, "-deps")
	}
	out, err := exec.Command("go", append(args, pkgs...)...).Output()
	if err != nil {
		var stderr string
		if ee, ok := err.(*exec.ExitError); ok {
			stderr = string(ee.Stderr)
		}
		t.Fatalf("go %s: %v\n%s", strings.Join(args, " "), err, stderr)
	}
	var lines []string
	for l := range strings.SplitSeq(strings.TrimSpace(string(out)), "\n") {
		if l != "" {
			lines = append(lines, l)
		}
	}
	return lines
}

// TestConfigImportsOnlyAllowed (05-config §1.11, §1.3 imports): core/config
// imports only the standard library, yaml, jsonschema-go,
// internal/jsonschemax, secrets, storage and metrics (whose Meter is
// OpenTelemetry's, so otel's metric and attribute packages come with it);
// everything else it reaches comes through those packages' own imports.
// jsonschemax imports only jsonschema-go.
func TestConfigImportsOnlyAllowed(t *testing.T) {
	allowedDirect := []string{
		mediacore + "internal/jsonschemax", mediacore + "secrets", mediacore + "storage", mediacore + "metrics",
		"github.com/google/jsonschema-go/jsonschema", "gopkg.in/yaml.v3", "go.yaml.in/yaml/v3",
		"go.opentelemetry.io/otel/metric", "go.opentelemetry.io/otel/attribute",
	}
	const nonStd = `{{if not .Standard}}{{.ImportPath}}{{end}}`
	for _, imp := range goList(t, `{{range .Imports}}{{.}}{{"\n"}}{{end}}`, false, mediacore+"config") {
		if !strings.Contains(strings.SplitN(imp, "/", 2)[0], ".") {
			continue // standard library
		}
		if !slices.Contains(allowedDirect, imp) {
			t.Errorf("core/config imports %s, which is not on 05-config's allow-list", imp)
		}
	}
	// The transitive closure: every non-standard package config reaches is
	// reached by one of the allowed imports too.
	closure := map[string]bool{mediacore + "config": true}
	for _, p := range goList(t, nonStd, true, allowedDirect[:4]...) {
		closure[p] = true
	}
	for _, p := range allowedDirect[4:] {
		closure[p] = true
	}
	thirdParty := goList(t, nonStd, true, "github.com/google/jsonschema-go/jsonschema", "go.opentelemetry.io/otel/metric", "go.opentelemetry.io/otel/attribute")
	for _, p := range thirdParty {
		closure[p] = true
	}
	for _, p := range goList(t, nonStd, true, mediacore+"config") {
		if !closure[p] {
			t.Errorf("core/config reaches %s outside its allowed imports' closure", p)
		}
		for _, banned := range []string{"kernel", "app", "audit", "rbac", "httpapi", "module", "internal/configstore"} {
			if p == mediacore+banned || strings.HasPrefix(p, mediacore+banned+"/") {
				t.Errorf("core/config reaches %s (L4: config never imports it)", p)
			}
		}
	}
	for _, imp := range goList(t, `{{range .Imports}}{{.}}{{"\n"}}{{end}}`, false, mediacore+"internal/jsonschemax") {
		if strings.Contains(strings.SplitN(imp, "/", 2)[0], ".") && !strings.HasPrefix(imp, "github.com/google/jsonschema-go/") {
			t.Errorf("internal/jsonschemax imports %s; it may import only jsonschema-go", imp)
		}
	}
}
