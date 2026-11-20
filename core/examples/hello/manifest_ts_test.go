// file: core/examples/hello/manifest_ts_test.go
// version: 0.1.0
// guid: 9f3b6c28-1d4e-4a75-8e09-b7c2d5a1f640
// last-edited: 2026-11-20

package hello

import (
	"bytes"
	"os"
	"testing"

	"github.com/falkcorp/mediacore/ui"
)

// manifestTSPath is hello's generated client manifest (07 §7.4.1).
const manifestTSPath = "web/src/manifest.gen.ts"

// TestManifestTS_Fresh is the freshness check of 07 §7.4.1: web/src/
// manifest.gen.ts is exactly ui.WriteManifestTS over hello's Manifest, so the
// web package's contributions are typed by the IDs the server declares.
// HELLO_WRITE_MANIFEST_TS=1 rewrites the file.
func TestManifestTS_Fresh(t *testing.T) {
	m := New().Manifest()
	var want bytes.Buffer
	err := ui.WriteManifestTS(&want, ui.ModuleManifest{
		ID: string(m.ID), Version: m.Version, DisplayName: m.DisplayName,
		UI: ui.UIIDs{
			Pages: m.UI.Pages, Nav: m.UI.Nav, SettingsPanels: m.UI.SettingsPanels, Widgets: m.UI.Widgets,
			Banners: m.UI.Banners, TopBarItems: m.UI.TopBarItems, UserPage: m.UI.UserPage,
			ActivityPanes: m.UI.ActivityPanes, SetupSteps: m.UI.SetupSteps,
		},
		// Manifest.Permissions has no owning row yet (perms.go), so the
		// generated list is empty until it lands.
	})
	if err != nil {
		t.Fatal(err)
	}
	if os.Getenv("HELLO_WRITE_MANIFEST_TS") == "1" {
		if err := os.WriteFile(manifestTSPath, want.Bytes(), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	got, err := os.ReadFile(manifestTSPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want.Bytes()) {
		t.Fatalf("%s is stale; run HELLO_WRITE_MANIFEST_TS=1 go test -run TestManifestTS_Fresh ./examples/hello\ngot:\n%s\nwant:\n%s", manifestTSPath, got, want.Bytes())
	}
}
