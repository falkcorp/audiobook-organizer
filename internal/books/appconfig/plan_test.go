// file: internal/books/appconfig/plan_test.go
// version: 1.1.1
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
	// only, read and ignored, folded into ai_endpoints), so they keep the base
	// (WithBase). The three ai_backend.parse_batch_* fields are in no list (HEAD's seed does not
	// carry them either); the other five ai_backend fields are kindAI rows. The oauth_* and root fields live inside core lists and are rows
	// of kind fold and roots. A new Config field with no row lands here and fails
	// the test: give it a row or name it.
	want := []string{"ai_backend.parse_batch_size", "ai_backend.parse_batch_timeout_seconds", "ai_backend.parse_batch_workers", "ai_endpoints_routing", "auto_update.window_end", "auto_update.window_start", "database_type"}
	slices.Sort(gaps)
	if !slices.Equal(gaps, want) {
		t.Fatalf("Config fields no row projects = %v, want %v", gaps, want)
	}
}
