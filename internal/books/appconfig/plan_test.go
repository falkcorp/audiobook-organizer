// file: internal/books/appconfig/plan_test.go
// version: 1.0.0
// guid: 5a0e7c31-8b94-4d26-a3f7-1c6d9e2b4f08
// last-edited: 2026-11-19

package appconfig

import (
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/config"
)

func TestPlan_ConfigFieldsNoRowProjects(t *testing.T) {
	rows := plan(New(newFake(), nil).tab, true)
	covered, below := map[string]bool{}, map[string]bool{}
	for _, r := range rows {
		covered[r.row.Head] = true
		for i := 1; i < len(r.segs); i++ {
			below[strings.Join(r.segs[:i], ".")] = true
		}
	}
	var b config.Config
	bv := reflect.ValueOf(&b).Elem()
	var gaps []string
	var walk func(v reflect.Value, path []string)
	walk = func(v reflect.Value, path []string) {
		for i := range v.NumField() {
			n := fieldName(v.Type().Field(i))
			if n == "" {
				continue
			}
			p := append(append([]string(nil), path...), n)
			d := strings.Join(p, ".")
			switch {
			case covered[d]:
			case v.Field(i).Kind() == reflect.Struct && below[d]:
				walk(v.Field(i), p)
			default:
				gaps = append(gaps, d)
			}
		}
	}
	walk(bv, nil)
	// The fields no row projects, and why: the table drops them (class x: Pebble
	// only, read and ignored, folded into ai_endpoints) or folds them into core's
	// OIDC provider list and library roots (projectFolds fills them, TestEngine_FoldsProject). A new Config field with no
	// row lands here and fails the test: give it a row or name it.
	want := []string{"ai_backend", "ai_endpoints_routing", "auto_update.window_end", "auto_update.window_start",
		"database_type", "oauth_default_role", "oauth_enabled", "oauth_github_client_id", "oauth_google_client_id",
		"path_aliases", "root_dir"}
	slices.Sort(gaps)
	if !slices.Equal(gaps, want) {
		t.Fatalf("Config fields no row projects = %v, want %v", gaps, want)
	}
}
