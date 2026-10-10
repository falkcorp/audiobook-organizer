// file: internal/opsmetrics/promrules_test.go
// version: 1.0.0
// guid: c4e8a1b6-52d9-4730-9f1a-7b3d6e0c8a25
// last-edited: 2026-10-10

package opsmetrics

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// ruleFile is the shape of a Prometheus rule file, enough to check the rules
// this PR adds.
type ruleFile struct {
	Groups []struct {
		Name  string `yaml:"name"`
		Rules []struct {
			Alert  string            `yaml:"alert"`
			Record string            `yaml:"record"`
			Expr   string            `yaml:"expr"`
			For    string            `yaml:"for"`
			Labels map[string]string `yaml:"labels"`
		} `yaml:"rules"`
	} `yaml:"groups"`
}

const promDir = "../../deploy/prometheus"

func loadRuleFile(t *testing.T, name string) ruleFile {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(promDir, name))
	if err != nil {
		t.Fatal(err)
	}
	var f ruleFile
	if err := yaml.Unmarshal(b, &f); err != nil {
		t.Fatalf("%s does not parse as YAML: %v", name, err)
	}
	if len(f.Groups) == 0 {
		t.Fatalf("%s has no groups", name)
	}
	return f
}

// TestPromRules_YAMLParse parses every rule file under deploy/prometheus and
// checks the rules this PR adds. The YAML parse never skips; only the promtool
// pass below does, when the binary is absent.
func TestPromRules_YAMLParse(t *testing.T) {
	alerts := map[string]string{}
	records := map[string]string{}
	for _, name := range []string{"alert-rules.yml", "recording-rules.yml"} {
		f := loadRuleFile(t, name)
		for _, g := range f.Groups {
			for _, r := range g.Rules {
				if r.Expr == "" {
					t.Errorf("%s group %s: a rule has no expr", name, g.Name)
				}
				if r.Alert != "" {
					if _, dup := alerts[r.Alert]; dup {
						t.Errorf("alert %s is defined twice", r.Alert)
					}
					alerts[r.Alert] = r.Expr
				}
				if r.Record != "" {
					records[r.Record] = r.Expr
				}
			}
		}
	}
	// The old rules stay for the soak, unchanged.
	if e := alerts["AudiobookOrganizerOpFailuresHigh"]; !strings.Contains(e, "audiobook_organizer_operations_failed_total") {
		t.Errorf("OpFailuresHigh expr changed: %q", e)
	}
	if e := alerts["AudiobookOrganizerOpStalled"]; !strings.Contains(e, "audiobook_organizer_op_items_processed") {
		t.Errorf("OpStalled expr changed: %q", e)
	}
	v2f, ok := alerts["AudiobookOrganizerOpFailuresHighV2"]
	if !ok || !strings.Contains(v2f, "audiobook_organizer_ops_runs_total") || !strings.Contains(v2f, "timed_out") {
		t.Errorf("OpFailuresHighV2 missing or wrong: %q", v2f)
	}
	v2s, ok := alerts["AudiobookOrganizerOpStalledV2"]
	if !ok || !strings.Contains(v2s, "audiobook_organizer_ops_inflight") || !strings.Contains(v2s, "audiobook_organizer_ops_items_total") ||
		!strings.Contains(v2s, "unless on (def_id)") {
		t.Errorf("OpStalledV2 missing or wrong: %q", v2s)
	}
	if _, ok := records["audiobook_organizer:ops_items_by_op_type:sum"]; !ok {
		t.Errorf("recording rule audiobook_organizer:ops_items_by_op_type:sum is missing")
	}
	// The ops_items family has an "s"; the legacy gauge op_items_total does not.
	// No new rule may read the legacy per-run gauge by accident.
	for name, e := range map[string]string{"OpFailuresHighV2": v2f, "OpStalledV2": v2s} {
		if strings.Contains(e, "audiobook_organizer_op_items") {
			t.Errorf("%s reads the legacy per-run gauge: %q", name, e)
		}
	}
}

// TestPromRules_Promtool runs promtool check rules over every rule file when
// the binary is on PATH.
func TestPromRules_Promtool(t *testing.T) {
	bin, err := exec.LookPath("promtool")
	if err != nil {
		t.Skip("promtool is not on PATH; TestPromRules_YAMLParse still parsed the files")
	}
	files, err := filepath.Glob(filepath.Join(promDir, "*rules.yml"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no rule files under %s (err %v)", promDir, err)
	}
	for _, f := range files {
		out, err := exec.Command(bin, "check", "rules", f).CombinedOutput()
		if err != nil {
			t.Errorf("promtool check rules %s: %v\n%s", filepath.Base(f), err, out)
		}
	}
}
