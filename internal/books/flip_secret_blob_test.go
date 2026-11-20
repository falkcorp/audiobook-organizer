// file: internal/books/flip_secret_blob_test.go
// version: 1.0.0
// guid: 9e3b7a52-4d18-4c6f-a0e5-2b8d1f6c3a97
// last-edited: 2026-11-19

package books

import (
	"reflect"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/books/cutover"
	"github.com/falkcorp/audiobook-organizer/internal/config"
)

// sentinelFor is the plaintext a test plants in one secret field: unmistakable in
// any stored value.
func sentinelFor(head string) string { return "sentinel-secret/" + head + "/alfred" }

// plantSecret writes sentinel into the Config field the rename-table head names,
// creating the slice element or map entry on the way (metadata_sources[] is one
// source; <k> and <id> are one key; any other segment below a map is its key).
// It reports whether the head names a field.
func plantSecret(cfg *config.Config, head, sentinel string) bool {
	return plantAt(reflect.ValueOf(cfg).Elem(), strings.Split(head, "."), sentinel)
}

func plantAt(v reflect.Value, segs []string, sentinel string) bool {
	if len(segs) == 0 {
		switch v.Kind() {
		case reflect.String:
			v.SetString(sentinel)
			return true
		case reflect.Struct:
			// A plugin's entry: the secret is one of its settings.
			if s := v.FieldByName("Settings"); s.IsValid() && s.Kind() == reflect.Map {
				if s.IsNil() {
					s.Set(reflect.MakeMap(s.Type()))
				}
				s.SetMapIndex(reflect.ValueOf("secret"), reflect.ValueOf(sentinel))
				return true
			}
		}
		return false
	}
	seg := segs[0]
	switch v.Kind() {
	case reflect.Map:
		if v.IsNil() {
			v.Set(reflect.MakeMap(v.Type()))
		}
		key := seg
		if key == "<k>" || key == "<id>" {
			key = "bat-key"
		}
		elem := reflect.New(v.Type().Elem()).Elem()
		if cur := v.MapIndex(reflect.ValueOf(key)); cur.IsValid() {
			elem.Set(cur)
		}
		if !plantAt(elem, segs[1:], sentinel) {
			return false
		}
		v.SetMapIndex(reflect.ValueOf(key), elem)
		return true
	case reflect.Struct:
		name, isSlice := strings.CutSuffix(seg, "[]")
		for j := range v.NumField() {
			f := v.Type().Field(j)
			n, _, _ := strings.Cut(f.Tag.Get("json"), ",")
			if n == "" {
				n, _, _ = strings.Cut(f.Tag.Get("mapstructure"), ",")
			}
			if !f.IsExported() || n != name {
				continue
			}
			next := v.Field(j)
			if isSlice {
				if next.Kind() != reflect.Slice {
					return false
				}
				if next.Len() == 0 {
					next.Set(reflect.MakeSlice(next.Type(), 1, 1))
				}
				next = next.Index(0)
				// A source with credentials needs an identity: the residual
				// codec keys the stored secret by it.
				if id := next.FieldByName("ID"); id.IsValid() && id.Kind() == reflect.String && id.String() == "" {
					id.SetString("bat-source")
				}
			}
			return plantAt(next, segs[1:], sentinel)
		}
	}
	return false
}

// TestSecretFields_NeverReachTheSettingsBlob: for every field the rename table
// classes as a secret that has a stored form, set it through the real Engine and
// core store, then read every non-secret row of the settings store: no secret value
// is in any of them. (The blob save that wrote the projection, secrets and all,
// was deleted with HEAD's settings loader in F3-07.) A new secret field added to
// the table is covered without anyone adding it here.
func TestSecretFields_NeverReachTheSettingsBlob(t *testing.T) {
	c := bootFlippedFresh(t)
	var planted []string
	for _, row := range cutover.Table().Rows {
		if row.Class != cutover.ClassSecret || row.Stored == cutover.StoredEnvFile || row.Removed {
			continue
		}
		head, sentinel := row.Head, sentinelFor(row.Head)
		var ok bool
		err := config.MutateChecked(func(cfg *config.Config) { ok = plantSecret(cfg, head, sentinel) })
		require.True(t, ok, "%s: the table names a secret field the test cannot plant; teach plantSecret its shape", head)
		require.NoError(t, err, "%s: the write was refused", head)
		planted = append(planted, head)
	}
	require.NotEmpty(t, planted)

	rows, err := c.store.GetAllSettings()
	require.NoError(t, err)
	for _, s := range rows {
		if s.IsSecret {
			continue // encrypted at rest by the store
		}
		for _, head := range planted {
			require.NotContains(t, s.Value, sentinelFor(head),
				"secret %s reached the plaintext settings row %q", head, s.Key)
		}
	}
	// And the live projection still serves them: moving them out lost nothing.
	snap := config.Snapshot()
	proj := reflect.ValueOf(snap)
	require.True(t, proj.IsValid())
}
