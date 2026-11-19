// file: internal/books/cutover/m6/refusal_test.go
// version: 1.0.0
// guid: e3a97c52-0d14-4b86-9f2d-18c6a5b0e7f3
// last-edited: 2026-11-19

package m6_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/falkcorp/mediacore/config"
)

func refuseList(path config.Path, err error) func(*rig) {
	return func(r *rig) {
		r.cfg.failFor = map[config.Path]error{path: err}
	}
}

func invalidList(path config.Path) error {
	return &config.FieldError{Path: path, Err: errors.New("a root's path may not overlap another")}
}

// TestConfigListRefusal_QuarantinesWhatFedTheList: core refuses a config list whole
// (config.Importer.Stage returns the *FieldError, which "the step quarantines"),
// so every row and member that fed the list goes aside with its raw bytes under
// one reason, the step completes, nothing is left in books' range, and nothing the
// refused list implied is written (no rootscan: row, no list).
func TestConfigListRefusal_QuarantinesWhatFedTheList(t *testing.T) {
	_, base := runFixture(t)
	baseline := map[string]string{}
	for _, q := range base.Quarantine {
		baseline[q.Key] = q.Reason
	}
	for _, c := range []struct {
		name string
		path config.Path
		want map[string]string
	}{
		{"roots", pathOf("core.library.roots"), map[string]string{
			memberPrefix + "root_dir":       rListRefused,
			"import_path:1":                 rListRefused,
			"import_path:2":                 rListRefused,
			"import_path:3":                 rListRefused,
			memberPrefix + "path_aliases.0": rListRefused,
			// The one mapping that resolved had a library root to resolve against.
			memberPrefix + "itunes.path_mappings.0": "the iTunes mapping's To is under no library root",
		}},
		{"mappings", pathOf("core.library.mappings"), map[string]string{
			memberPrefix + "itunes.path_mappings.0": rListRefused,
		}},
		{"protected paths", pathOf("core.library.protected_paths"), map[string]string{
			memberPrefix + "protected_paths": rListRefused,
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := newRig(t, withRows(headRows(t)), refuseList(c.path, invalidList(c.path)))
			s, err := r.run(t)
			if err != nil {
				t.Fatalf("a refused list failed the boot: %v\nlogs:\n%s", err, r.logs.String())
			}
			if s.State != "done" || len(s.Verify.Failures) != 0 {
				t.Fatalf("state %q, verify %v", s.State, s.Verify.Failures)
			}
			got := map[string]string{}
			vals := map[string]string{}
			for _, q := range s.Quarantine {
				got[q.Key] = q.Reason
				vals[q.Key] = q.Value
			}
			for k, why := range c.want {
				if got[k] != why {
					t.Errorf("%s quarantined as %q, want %q", k, got[k], why)
				}
			}
			for k, why := range baseline {
				if _, replaced := c.want[k]; !replaced && got[k] != why {
					t.Errorf("baseline %s is %q, want %q", k, got[k], why)
				}
			}
			for k := range got {
				if _, in := c.want[k]; !in {
					if _, in := baseline[k]; !in {
						t.Errorf("unexpected quarantine of %s", k)
					}
				}
			}
			if _, has := s.Rows[string(ownerConfig)][string(c.path)]; has {
				t.Errorf("the refused list %s was written", c.path)
			}
			if c.name == "roots" {
				for k := range s.Rows[string(ownerConfig)] {
					if strings.HasPrefix(k, "rootscan:") {
						t.Errorf("a rootscan row %q for a refused root", k)
					}
				}
				if vals["import_path:1"] == "" {
					t.Error("the refused import_path row was quarantined without its raw bytes")
				}
			}
		})
	}
}

func pathOf(s string) config.Path { return config.Path(s) }

// TestConfigListRefusal_OtherErrorsStillFailTheBoot: only the importer's own "this
// value is invalid" is a data problem. Anything else (an unregistered path, a
// closed store) says nothing about the rows, so it fails the boot with every
// source row where it was.
func TestConfigListRefusal_OtherErrorsStillFailTheBoot(t *testing.T) {
	boom := errors.New("config: unknown key")
	r := newRig(t, withRows(headRows(t)), refuseList(pathOf("core.library.roots"), boom))
	if _, err := r.run(t); !errors.Is(err, boom) {
		t.Fatalf("the boot returned %v, want the importer's error", err)
	}
}
