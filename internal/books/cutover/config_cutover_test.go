// file: internal/books/cutover/config_cutover_test.go
// version: 1.0.0
// guid: 7c1e9d52-3b64-4f08-a9d1-5e2b8f6c1a06
// last-edited: 2026-11-11

package cutover

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/falkcorp/mediacore/storage"
)

// r0Effective is R0's own answer, GET /api/v1/config on the corpus: the
// effective config as flat HEAD-named values (stored, YAML and env layers
// applied), secrets masked.
func r0Effective(t testing.TB, corpus string) map[string]any {
	t.Helper()
	path := filepath.Join(repoRoot(t), filepath.FromSlash(corpusRel), "answers", "effective_config.json")
	if corpus == "overlay" {
		path = filepath.Join(repoRoot(t), filepath.FromSlash(corpusRel), "answers", "overlay", "effective_config.json")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Exchanges []struct {
			Response struct {
				Body struct {
					Data struct {
						Config map[string]any `json:"config"`
					} `json:"data"`
				} `json:"body"`
			} `json:"response"`
		} `json:"exchanges"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil || len(doc.Exchanges) == 0 {
		t.Fatalf("effective_config.json: %v", err)
	}
	return doc.Exchanges[0].Response.Body.Data.Config
}

// lookupHead finds the dotted HEAD name in R0's effective config.
func lookupHead(eff map[string]any, head string) (any, bool) {
	var cur any = eff
	for _, seg := range strings.Split(head, ".") {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil, false
		}
		if cur, ok = m[seg]; !ok {
			return nil, false
		}
	}
	return cur, true
}

// TestConfigCutover_EffectiveConfigUnchanged is the config oracle (05-config
// §1.12.2): on the corpus, for every key row, R2's effective value with empty
// operator layers equals Convert of R0's effective value from R0's own GET
// /api/v1/config on the same store.
func TestConfigCutover_EffectiveConfigUnchanged(t *testing.T) {
	for _, name := range []string{"stage1", "overlay"} {
		t.Run(name, func(t *testing.T) {
			r := newRig(t, name)
			snap, err := r.run(t)
			if err != nil {
				t.Fatalf("M2: %v", err)
			}
			eff := r0Effective(t, name)
			compared, notStaged, absent := 0, 0, 0
			for _, row := range Table().Rows {
				if row.Class != ClassKey || row.Removed || row.Process || row.Container || strings.ContainsAny(row.Head, "[]<>") {
					continue
				}
				val, ok := lookupHead(eff, row.Head)
				if !ok {
					absent++
					continue
				}
				raw, _ := json.Marshal(val)
				for _, tg := range row.Targets {
					if tg.Kind != TargetKey || !isConcretePath(tg.Path) {
						continue
					}
					want, _, err := Convert(tg.Convert, raw)
					if errors.Is(err, ErrNotStaged) {
						notStaged++
						continue
					}
					if err != nil {
						t.Errorf("row %s: Convert(%q, %s): %v", row.Head, tg.Convert, raw, err)
						continue
					}
					got, zero := snap.Effective(tg.Path)
					compared++
					switch {
					case zero && isZeroJSON(want):
					case string(want) == "null" && isZeroJSON(got):
					case zero:
						t.Errorf("%s: R0's %s = %s converts to %s, R2 has the zero value", tg.Path, row.Head, raw, want)
					case !jsonSame(got, want):
						t.Errorf("%s: R0's %s = %s converts to %s, R2's effective value is %s", tg.Path, row.Head, raw, want, got)
					}
				}
			}
			t.Logf("%s: %d key targets compared, %d not staged by their converter, %d rows R0 does not report", name, compared, notStaged, absent)
		})
	}
}

// ---- the every-key oracle: a blob whose every key is non-default ----

// headTypeAt is the Go type of HEAD's dotted name head in headConfig.
func headTypeAt(head string) (reflect.Type, bool) {
	t := reflect.TypeFor[headConfig]()
	for _, seg := range strings.Split(head, ".") {
		if t.Kind() != reflect.Struct {
			return nil, false
		}
		found := false
		for i := 0; i < t.NumField(); i++ {
			if name, _ := fieldName(t.Field(i)); name == seg {
				t, found = t.Field(i).Type, true
				break
			}
		}
		if !found {
			return nil, false
		}
	}
	return t, true
}

// perturbAt is a value of type t that is not HEAD's default for head: the
// default flipped, shifted or suffixed, a one-element list.
func perturbAt(head string, t reflect.Type) (any, bool) {
	def, _ := headDefault(head)
	switch t.Kind() {
	case reflect.Struct:
		m := map[string]any{}
		for i := 0; i < t.NumField(); i++ {
			sf := t.Field(i)
			name, envOnly := fieldName(sf)
			if name == "" || envOnly {
				continue
			}
			if v, ok := perturbAt(head+"."+name, sf.Type); ok {
				m[name] = v
			}
		}
		return m, len(m) > 0
	case reflect.Bool:
		d, _ := def.(bool)
		return !d, true
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64, reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		d, _ := def.(float64)
		return d + 7, true
	case reflect.Float32, reflect.Float64:
		d, _ := def.(float64)
		return d + 7.5, true
	case reflect.String:
		d, _ := def.(string)
		return d + "-gotham", true
	case reflect.Slice:
		switch t.Elem().Kind() {
		case reflect.String:
			return []any{"gotham"}, true
		case reflect.Float32, reflect.Float64, reflect.Int, reflect.Int64:
			return []any{1.5}, true
		}
	}
	return nil, false
}

// normalized are the rows whose stored value HEAD itself normalizes at load
// (a non-positive log retention, the one-shot maintenance window): the corpus
// oracle covers them, a perturbed value would measure the normalizer.
var normalized = map[string]bool{
	"log_retention_days": true, "chapter_consolidation_threshold_min": true,
	"maintenance.window_start": true, "maintenance.window_end": true, "maintenance.enabled": true,
	// config.yaml's gap-fill values (HEAD fills them when the stored value is empty or false).
	"enable_ai_parsing": true, "root_dir": true,
}

// setAt sets the dotted path in tree, creating the objects on the way.
func setAt(tree map[string]any, head string, v any) {
	segs := strings.Split(head, ".")
	cur := tree
	for _, s := range segs[:len(segs)-1] {
		next, ok := cur[s].(map[string]any)
		if !ok {
			next = map[string]any{}
			cur[s] = next
		}
		cur = next
	}
	cur[segs[len(segs)-1]] = v
}

// rewriteBlob edits the settings blob's JSON in books' legacy range.
func rewriteBlob(t testing.TB, lr storage.LegacyRange, edit func(tree map[string]any)) {
	t.Helper()
	ctx := context.Background()
	raw, err := lr.Get(ctx, settingKey(blobRowName))
	if err != nil {
		t.Fatal(err)
	}
	row, err := decodeSettingRow(raw)
	if err != nil {
		t.Fatal(err)
	}
	var tree map[string]any
	if err := json.Unmarshal([]byte(row.Value), &tree); err != nil {
		t.Fatal(err)
	}
	edit(tree)
	nv, err := json.Marshal(tree)
	if err != nil {
		t.Fatal(err)
	}
	row.Value = string(nv)
	out, err := json.Marshal(row)
	if err != nil {
		t.Fatal(err)
	}
	if err := lr.Set(ctx, settingKey(blobRowName), out); err != nil {
		t.Fatal(err)
	}
}

// TestConfigCutover_EffectiveConfigUnchanged_EveryKeyNonDefault: the corpus
// holds mostly defaults, so a wrong target that happens to share the default
// would pass the oracle above. Here every key row of the table, but the
// handful HEAD normalizes, is stored with a value that is not HEAD's
// default; after M2, R2's effective value of each target is Convert of that
// stored value.
func TestConfigCutover_EffectiveConfigUnchanged_EveryKeyNonDefault(t *testing.T) {
	want := map[string]json.RawMessage{} // canonical path -> Convert(stored)
	stored := map[string]any{}           // head -> perturbed value
	for _, row := range Table().Rows {
		if row.Class != ClassKey || row.Stored != StoredBlob || row.Removed || row.Process || strings.ContainsAny(row.Head, "[]<>") || normalized[row.Head] {
			continue
		}
		typ, ok := headTypeAt(row.Head)
		if !ok {
			continue
		}
		v, ok := perturbAt(row.Head, typ)
		if !ok {
			continue
		}
		if row.Container {
			continue // its leaves are rows of their own
		}
		raw, _ := json.Marshal(v)
		hasKey := false
		for _, tg := range row.Targets {
			if tg.Kind != TargetKey || !isConcretePath(tg.Path) {
				continue
			}
			out, _, err := Convert(tg.Convert, raw)
			if errors.Is(err, ErrNotStaged) {
				continue
			}
			if err != nil {
				t.Fatalf("row %s: Convert(%q, %s): %v", row.Head, tg.Convert, raw, err)
			}
			want[tg.Path] = out
			hasKey = true
		}
		if hasKey {
			stored[row.Head] = v
		}
	}
	if len(want) < 150 {
		t.Fatalf("only %d key targets are exercised; the table has far more", len(want))
	}
	r := newRig(t, "stage1", func(r *rig) {
		r.mutate = func(t testing.TB, lr storage.LegacyRange) {
			rewriteBlob(t, lr, func(tree map[string]any) {
				for _, head := range sortedKeys(stored) {
					setAt(tree, head, stored[head])
				}
			})
		}
	})
	snap, err := r.run(t)
	if err != nil {
		t.Fatalf("M2: %v", err)
	}
	for _, path := range sortedKeys(want) {
		got, zero := snap.Effective(path)
		switch {
		case zero:
			t.Errorf("%s: stored value converts to %s, R2 has no value", path, want[path])
		case !jsonSame(got, want[path]):
			t.Errorf("%s: want %s, R2's effective value is %s", path, want[path], got)
		}
	}
	t.Logf("%d key targets compared, each stored non-default", len(want))
}
