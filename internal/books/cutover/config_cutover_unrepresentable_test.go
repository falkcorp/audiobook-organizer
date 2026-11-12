// file: internal/books/cutover/config_cutover_unrepresentable_test.go
// version: 1.0.0
// guid: 43c89a58-0dda-4151-bdb2-b215c2ae4818
// last-edited: 2026-11-12

package cutover

import (
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/spf13/viper"
)

// A file value M2 cannot carry exactly is HEAD's effective setting when the
// blob lacks the key, and the strip would delete the line. It is therefore
// refused by name (Preflight, and the planner for a resumed cutover), never
// read as absent. A key the blob holds is the blob's: the file's value is
// shadowed and nothing is lost.
//
// concurrent_scans is the only int key. Each literal below is one the plan's
// float64 tree cannot hold: a number that is not finite, or beyond 2^53, or
// one a platform converts to an int by its own rule.
var unrepresentableInts = []string{
	".inf", "-.inf", ".nan",
	"9007199254740993", `"9007199254740993"`, "-9007199254740993",
	"18446744073709551615", "1e30",
}

func TestConfigCutover_UnrepresentableFileValueIsRefusedNotRead(t *testing.T) {
	row, _ := Table().Row("concurrent_scans")
	path := row.Targets[0].Path
	for _, lit := range unrepresentableInts {
		file := "concurrent_scans: " + lit + "\n"
		t.Run(lit, func(t *testing.T) {
			t.Run("no blob: Preflight refuses, naming the key", func(t *testing.T) {
				r := newRig(t, "stage1", dropSources(), withFile(file))
				report := requireBlocked(t, r)
				if !strings.Contains(report, "concurrent_scans") {
					t.Errorf("the refusal does not name concurrent_scans:\n%s", report)
				}
				assertFileWhole(t, r, file)
			})

			t.Run("a blob without the key: Preflight refuses, naming the key", func(t *testing.T) {
				r := newRig(t, "stage1", mutateBlob(func(tree map[string]any) { delete(tree, "concurrent_scans") }), withFile(file))
				report := requireBlocked(t, r)
				for _, want := range []string{"concurrent_scans", "cannot carry over exactly", "plain value"} {
					if !strings.Contains(report, want) {
						t.Errorf("the refusal does not say %q:\n%s", want, report)
					}
				}
				assertFileWhole(t, r, file)
			})

			t.Run("a blob holding the key: the blob wins, nothing is refused, the line goes", func(t *testing.T) {
				r := newRig(t, "stage1", mutateBlob(func(tree map[string]any) { tree["concurrent_scans"] = 7.0 }), withFile(file))
				snap, rep, after, exists := bootAndStripBlobSeed(t, r)
				if snap.State != "done" {
					t.Fatalf("M2's state is %q, want done", snap.State)
				}
				if v, ok := stagedValue(snap, path); !ok || v != "7" {
					t.Errorf("staged %s = %q (%v), want the blob's 7", path, v, ok)
				}
				if exists && strings.Contains(after, "concurrent_scans") {
					t.Errorf("the strip left the shadowed line:\n%s", after)
				}
				if len(rep.Kept) != 0 {
					t.Errorf("the strip kept %v, want nothing", rep.Kept)
				}
			})
		})
	}

	t.Run("a null blob member lacks the key too", func(t *testing.T) {
		r := newRig(t, "stage1", mutateBlob(func(tree map[string]any) { tree["concurrent_scans"] = nil }), withFile("concurrent_scans: .inf\n"))
		if report := requireBlocked(t, r); !strings.Contains(report, "concurrent_scans") {
			t.Errorf("the refusal does not name concurrent_scans:\n%s", report)
		}
	})

	t.Run("a planner reached without Preflight (a resumed cutover) refuses too, naming the key", func(t *testing.T) {
		in := &m2Input{Blob: settingRow(blobRowName, "{}", "json", false), FileUnrepresentable: map[string]bool{"concurrent_scans": true, "log_level": false}}
		_, err := buildPlan(in)
		var ue *unrepresentableError
		if !errors.As(err, &ue) || len(ue.Keys) != 1 || ue.Keys[0] != "concurrent_scans" {
			t.Fatalf("buildPlan = %v, want an unrepresentableError naming concurrent_scans only", err)
		}
	})

	t.Run("2^53 is the last number carried", func(t *testing.T) {
		r := newRig(t, "stage1", mutateBlob(func(tree map[string]any) { delete(tree, "concurrent_scans") }), withFile("concurrent_scans: 9007199254740992\n"))
		snap, err := r.run(t)
		if err != nil {
			t.Fatalf("M2 refused 2^53: %v", err)
		}
		if v, ok := stagedValue(snap, path); !ok || v != "9007199254740992" {
			t.Errorf("staged %s = %q (%v), want 9007199254740992 exactly", path, v, ok)
		}
	})
}

// viperString is what the old build's v.GetString reads for key from doc.
func viperString(t *testing.T, doc, key string) string {
	t.Helper()
	v := viper.New()
	v.SetConfigType("yaml")
	if err := v.ReadConfig(strings.NewReader(doc)); err != nil {
		t.Fatalf("viper cannot read %q: %v", doc, err)
	}
	return v.GetString(key)
}

// A YAML timestamp on a string key reads as time.Time, and viper's GetString
// gives that type's String form, not the text the operator wrote. The seed is
// the form the old build used, and the same goes for a number cast to a
// string.
func TestConfigCutover_BlobLacksAViperKey_StringKeysKeepViperSForm(t *testing.T) {
	for _, c := range []struct{ key, lit string }{
		{"folder_naming_pattern", "2001-12-14"},
		{"file_naming_pattern", "2001-12-14T21:59:43.10-05:00"},
		{"log_level", ".inf"},
		{"organization_strategy", "1e3"},
	} {
		t.Run(c.key+": "+c.lit, func(t *testing.T) {
			doc := c.key + ": " + c.lit + "\n"
			want := viperString(t, doc, c.key)
			row, _ := Table().Row(c.key)
			path := row.Targets[0].Path
			r := newRig(t, "stage1", mutateBlob(func(tree map[string]any) { delete(tree, c.key) }), withFile(doc))
			snap, err := r.run(t)
			if err != nil {
				t.Fatalf("M2: %v", err)
			}
			if v, ok := stagedValue(snap, path); !ok || v != jsonOf(t, want) {
				t.Errorf("staged %s = %q (%v), want viper's %s", path, v, ok, jsonOf(t, want))
			}
			if strings.Contains(c.lit, "2001") && want == c.lit {
				t.Errorf("viper's form of %s equals the text, so the cell proves nothing", c.lit)
			}
		})
	}
}

// assertFileWhole checks the strip never ran: the file is as the operator left it.
func assertFileWhole(t *testing.T, r *rig, want string) {
	t.Helper()
	raw, err := os.ReadFile(r.confFile)
	if err != nil || string(raw) != want {
		t.Errorf("config.yaml changed under a refused upgrade (err %v)", err)
	}
}
