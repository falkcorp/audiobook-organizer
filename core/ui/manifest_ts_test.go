// file: core/ui/manifest_ts_test.go
// version: 0.1.0
// guid: 8b5e2d17-3f46-4a09-9c71-d0e6a4b8f2c3
// last-edited: 2026-11-20

package ui

import (
	"strings"
	"testing"
)

// TestWriteManifestTS_SpecShape: the output is 07 §7.4.1's literal for the
// spec's hello manifest, byte for byte.
func TestWriteManifestTS_SpecShape(t *testing.T) {
	var b strings.Builder
	err := WriteManifestTS(&b, ModuleManifest{
		ID: "hello", Version: "1.0.0", DisplayName: "Hello",
		UI:          UIIDs{Pages: []string{"greeting"}, Nav: []string{"hello"}, SettingsPanels: []string{"hello"}},
		Permissions: []string{"hello:read", "hello:admin"},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := `// generated — do not edit
export const manifest = {
  id: 'hello',
  version: '1.0.0',
  displayName: 'Hello',
  pages: ['greeting'],
  nav: ['hello'],
  settingsPanels: ['hello'],
  widgets: [],
  banners: [],
  topBarItems: [],
  userPage: [],
  activityPanes: [],
  setupSteps: [],
  permissions: ['hello:read', 'hello:admin'],
} as const;
`
	if b.String() != want {
		t.Fatalf("got\n%s\nwant\n%s", b.String(), want)
	}
}

// TestWriteManifestTS_EscapesLiterals: no value can end its literal or its
// line: quotes, backslashes, newlines and the JS line separators are escaped.
func TestWriteManifestTS_EscapesLiterals(t *testing.T) {
	var b strings.Builder
	if err := WriteManifestTS(&b, ModuleManifest{ID: "joker", DisplayName: "Ha'ha\\\n" + string(rune(0x2028)) + "</script>"}); err != nil {
		t.Fatal(err)
	}
	if want := `displayName: 'Ha\'ha\\\n` + `\` + "u2028</script>',"; !strings.Contains(b.String(), want) {
		t.Fatalf("displayName not escaped: want %q in\n%q", want, b.String())
	}
	if strings.Count(b.String(), "\n") != 16 {
		t.Fatalf("output has %d lines, want 16", strings.Count(b.String(), "\n"))
	}
}
