// file: internal/books/cutover/config_cutover_blobseed_test.go
// version: 1.3.0
// guid: aee11d6f-716a-4548-9d59-27fef2b342e7
// last-edited: 2026-11-12

package cutover

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/falkcorp/mediacore/storage/datamigrate"
)

// HEAD decodes the settings blob onto Snapshot() (LoadConfigFromDatabase), which
// is viper's view with config.yaml read in, so a member the blob lacks keeps the
// file's value. M2 mirrors that seeding (planner.gapFill): the file's value of
// a viper-read key the blob lacks is staged like a blob member, and only then is
// the line safe for the strip to remove. Before, the strip deleted a value
// nothing stored (the review's TestR3BlobLacksKey).

// bootAndStripBlobSeed boots r, reads the store as M2 left it, and runs the
// strip over r's config.yaml with the real holder in the same boot.
func bootAndStripBlobSeed(t *testing.T, r *rig) (snap *snapshot, rep StripReport, fileAfter string, fileExists bool) {
	t.Helper()
	mem := r.newMem(t)
	err := r.boot(context.Background(), mem, []datamigrate.DataMigration{r.migration()}, func(ctx context.Context, e *bootEnv) error {
		var serr error
		if snap, serr = read(ctx, e); serr != nil {
			return serr
		}
		rep, serr = StripGapFill(ctx, nil, r.confFile, realHolder(e))
		return serr
	})
	if err != nil {
		t.Fatalf("boot: %v", err)
	}
	raw, rerr := os.ReadFile(r.confFile)
	return snap, rep, string(raw), rerr == nil
}

// jsonOf is the compact JSON of v, the form a staged value is compared in.
func jsonOf(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestConfigCutover_BlobLacksAViperKey(t *testing.T) {
	for _, c := range []struct {
		key     string
		file    string // the config.yaml line, off HEAD's default
		want    any    // what the file's value stages
		blobVal any    // a different value the blob itself may hold
	}{
		{"playlist_dir", "playlist_dir: /mnt/gotham-playlists", "/mnt/gotham-playlists", "/mnt/batcave-playlists"},
		{"setup_complete", "setup_complete: true", true, false},
		{"organization_strategy", "organization_strategy: copy", "copy", "move"},
		{"scan_on_startup", "scan_on_startup: true", true, false},
		{"auto_organize", "auto_organize: false", false, true},
		{"folder_naming_pattern", "folder_naming_pattern: '{author}/{title}'", "{author}/{title}", "{title}"},
		{"file_naming_pattern", "file_naming_pattern: '{title} - bat'", "{title} - bat", "{title}"},
		{"enable_ai_parsing", "enable_ai_parsing: false", false, true},
		{"concurrent_scans", "concurrent_scans: 3", 3.0, 7.0},
		{"log_level", "log_level: debug", "debug", "warn"},
	} {
		t.Run(c.key, func(t *testing.T) {
			row, ok := Table().Row(c.key)
			if !ok || len(row.Targets) == 0 {
				t.Fatalf("no table row for %s", c.key)
			}
			path := row.Targets[0].Path
			lacks := func(tree map[string]any) { delete(tree, c.key) }

			t.Run("the file's value is staged, then the strip may remove the line", func(t *testing.T) {
				r := newRig(t, "stage1", mutateBlob(lacks), withFile(c.file+"\n"))
				snap, rep, after, exists := bootAndStripBlobSeed(t, r)
				if snap.State != "done" {
					t.Fatalf("M2's state is %q, want done", snap.State)
				}
				got, _ := snap.Effective(path)
				if string(got) != jsonOf(t, c.want) {
					t.Errorf("%s = %s, want the file's %s", path, got, jsonOf(t, c.want))
				}
				if v, ok := stagedValue(snap, path); !ok || v != jsonOf(t, c.want) {
					t.Errorf("staged %s = %q (staged %v), want %s", path, v, ok, jsonOf(t, c.want))
				}
				if exists && strings.Contains(strings.ToLower(after), c.key+":") {
					t.Errorf("the strip left %s in the file:\n%s", c.key, after)
				}
				if len(rep.Kept) != 0 {
					t.Errorf("the strip kept %v, want nothing (a viper key is stripped once stored)", rep.Kept)
				}
			})

			t.Run("a value the blob holds wins over the file's", func(t *testing.T) {
				r := newRig(t, "stage1", mutateBlob(func(tree map[string]any) { tree[c.key] = c.blobVal }), withFile(c.file+"\n"))
				snap, err := r.run(t)
				if err != nil {
					t.Fatalf("M2: %v", err)
				}
				got, _ := snap.Effective(path)
				if string(got) != jsonOf(t, c.blobVal) {
					t.Errorf("%s = %s, want the blob's %s", path, got, jsonOf(t, c.blobVal))
				}
			})

			t.Run("a null blob member lacks the key too", func(t *testing.T) {
				r := newRig(t, "stage1", mutateBlob(func(tree map[string]any) { tree[c.key] = nil }), withFile(c.file+"\n"))
				snap, err := r.run(t)
				if err != nil {
					t.Fatalf("M2: %v", err)
				}
				got, _ := snap.Effective(path)
				if string(got) != jsonOf(t, c.want) {
					t.Errorf("%s = %s, want the file's %s", path, got, jsonOf(t, c.want))
				}
			})
		})
	}
}

// A file value equal to HEAD's default is seeded like any other: the key stage
// stages a blob member at its default, and the registry (the double here, the
// real importer in F3-07) skips a default-equal value, so the staging call is
// made and nothing is stored. The same value a blob holds takes the same path.
// A value spelled another way that viper reads as the default is the same.
func TestConfigCutover_BlobLacksAViperKey_DefaultsAreStagedForTheRegistryToSkip(t *testing.T) {
	for _, c := range []struct {
		name, key, file string
		want            any // what the file's value is cast to
	}{
		{"organization_strategy at its default", "organization_strategy", "organization_strategy: auto", "auto"},
		{"log_level at its default", "log_level", "log_level: info", "info"},
		{"concurrent_scans at the host's default", "concurrent_scans", "concurrent_scans: 10", 10.0},
		{"concurrent_scans as a quoted default", "concurrent_scans", "concurrent_scans: \"10\"", 10.0},
		{"concurrent_scans as 10.0", "concurrent_scans", "concurrent_scans: 10.0", 10.0},
		{"enable_ai_parsing true", "enable_ai_parsing", "enable_ai_parsing: true", true},
		{"auto_organize true", "auto_organize", "auto_organize: true", true},
		{"scan_on_startup yes, which ParseBool refuses", "scan_on_startup", "scan_on_startup: yes", false},
		{"playlist_dir at the flag default", "playlist_dir", "playlist_dir: playlists", "playlists"},
	} {
		t.Run(c.name, func(t *testing.T) {
			row, _ := Table().Row(c.key)
			path := row.Targets[0].Path
			r := newRig(t, "stage1", mutateBlob(func(tree map[string]any) { delete(tree, c.key) }), withFile(c.file+"\n"))
			snap, err := r.run(t)
			if err != nil {
				t.Fatalf("M2: %v", err)
			}
			if v, ok := stagedValue(snap, path); !ok || v != jsonOf(t, c.want) {
				t.Errorf("staged %s = %q (staged %v), want the file's %s offered to the registry", path, v, ok, jsonOf(t, c.want))
			}
			if v, ok := snap.Config[path]; ok {
				t.Errorf("%s was stored as %s, a value equal to HEAD's default the registry skips", path, v)
			}
		})
	}
}

// Core's default for a host-dependent key is not a recorded constant, so a file
// value that equals HEAD's default on one host is a pin on another. The staged
// value is the file's either way; the registry keeps it where it differs from
// the default it compares against. Here the host has 4 CPUs, so a file's 10 (the
// recorded default) is no default and is stored, as a blob member of 10 would be.
func TestConfigCutover_BlobLacksAViperKey_APinSurvivesWhereTheHostDefaultDiffers(t *testing.T) {
	pinHostCPUs(t, 4)
	row, _ := Table().Row("concurrent_scans")
	path := row.Targets[0].Path
	r := newRig(t, "stage1", mutateBlob(func(tree map[string]any) { delete(tree, "concurrent_scans") }), withFile("concurrent_scans: 10\n"))
	snap, err := r.run(t)
	if err != nil {
		t.Fatalf("M2: %v", err)
	}
	if v, ok := snap.Config[path]; !ok || string(v) != "10" {
		t.Errorf("%s = %s (stored %v), want the file's 10 kept on a 4-CPU host", path, v, ok)
	}
}

// The file's value is cast the way viper's typed getter reads it: a quoted int
// is an int, and "true" is a bool.
func TestConfigCutover_BlobLacksAViperKey_ValuesAreCastLikeViper(t *testing.T) {
	for _, c := range []struct {
		key, file string
		want      any
	}{
		{"concurrent_scans", "concurrent_scans: \"3\"", 3.0},
		{"concurrent_scans", "concurrent_scans: 3.0", 3.0},
		{"scan_on_startup", "scan_on_startup: \"true\"", true},
		{"log_level", "log_level: 7", "7"},
	} {
		t.Run(fmt.Sprintf("%s %s", c.key, c.file), func(t *testing.T) {
			row, _ := Table().Row(c.key)
			path := row.Targets[0].Path
			r := newRig(t, "stage1", mutateBlob(func(tree map[string]any) { delete(tree, c.key) }), withFile(c.file+"\n"))
			snap, err := r.run(t)
			if err != nil {
				t.Fatalf("M2: %v", err)
			}
			if v, ok := stagedValue(snap, path); !ok || v != jsonOf(t, c.want) {
				t.Errorf("staged %s = %q (%v), want %s", path, v, ok, jsonOf(t, c.want))
			}
		})
	}
}

// planner.gapFill, called on its own: an explicit null member of the tree lacks
// the key as an absent one does (json.Unmarshal leaves a field alone for either),
// a held member is left as it is, and the file's value is planted cast to the
// key's type, a value equal to HEAD's default included (the registry skips it).
func TestPlannerGapFill_SeedsOnlyWhatTheTreeLacks(t *testing.T) {
	tree := map[string]any{
		"log_level":             nil,        // null: lacks
		"organization_strategy": "move",     // held: the blob wins
		"auto_organize":         false,      // held at a value that differs from the file's
		"playlist_dir":          "",         // held as an explicit empty string: the blob wins
		"scan_on_startup":       nil,        // null, and the file's value is HEAD's default: still planted
		"setup_complete":        true,       // held
		"file_naming_pattern":   "{title}",  // held
		"folder_naming_pattern": "{author}", // held
	}
	pl := &planner{in: &m2Input{FileSettings: map[string]any{
		"log_level":             "debug",
		"organization_strategy": "copy",
		"auto_organize":         true,
		"playlist_dir":          "/mnt/gotham-playlists",
		"scan_on_startup":       false,
		"concurrent_scans":      float64(3),
		"enable_ai_parsing":     false,
	}}, tree: tree}
	pl.gapFill()
	want := map[string]any{
		"log_level":             "debug",
		"organization_strategy": "move",
		"auto_organize":         false,
		"playlist_dir":          "",
		"scan_on_startup":       false,
		"setup_complete":        true,
		"file_naming_pattern":   "{title}",
		"folder_naming_pattern": "{author}",
		"concurrent_scans":      float64(3),
		"enable_ai_parsing":     false,
	}
	if jsonOf(t, tree) != jsonOf(t, want) {
		t.Errorf("tree after gapFill = %s, want %s", jsonOf(t, tree), jsonOf(t, want))
	}
}
