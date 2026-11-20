// file: core/ui/manifest_ts.go
// version: 0.1.0
// guid: 7a3d5f08-1e9c-4b62-8d4a-6c2b0e7f9a13
// last-edited: 2026-11-20

package ui

import (
	"fmt"
	"io"
	"strings"
)

// WriteManifestTS prints a module's UI manifest as manifest.gen.ts (§7.4.1):
// a literal object `as const`, so the shell's slot types are keyed by the
// IDs the server declares and a client contribution naming any other ID
// fails typecheck. Every string is a single-quoted TypeScript literal.
func WriteManifestTS(w io.Writer, m ModuleManifest) error {
	var b strings.Builder
	b.WriteString("// generated — do not edit\n")
	b.WriteString("export const manifest = {\n")
	fmt.Fprintf(&b, "  id: %s,\n", tsString(m.ID))
	fmt.Fprintf(&b, "  version: %s,\n", tsString(m.Version))
	fmt.Fprintf(&b, "  displayName: %s,\n", tsString(m.DisplayName))
	for _, f := range []struct {
		name string
		ids  []string
	}{
		{"pages", m.UI.Pages}, {"nav", m.UI.Nav}, {"settingsPanels", m.UI.SettingsPanels},
		{"widgets", m.UI.Widgets}, {"banners", m.UI.Banners}, {"topBarItems", m.UI.TopBarItems},
		{"userPage", m.UI.UserPage}, {"activityPanes", m.UI.ActivityPanes}, {"setupSteps", m.UI.SetupSteps},
		{"permissions", m.Permissions},
	} {
		fmt.Fprintf(&b, "  %s: %s,\n", f.name, tsList(f.ids))
	}
	b.WriteString("} as const;\n")
	_, err := io.WriteString(w, b.String())
	return err
}

// tsList is ['a', 'b'], or [] for none.
func tsList(ids []string) string {
	q := make([]string, len(ids))
	for i, id := range ids {
		q[i] = tsString(id)
	}
	return "[" + strings.Join(q, ", ") + "]"
}

// tsString is s as a single-quoted TypeScript string literal: the backslash,
// the quote, every control character and the two JavaScript line separators
// escaped, so no value can end the literal or the line.
func tsString(s string) string {
	var b strings.Builder
	b.WriteByte('\'')
	for _, r := range s {
		switch {
		case r == '\\':
			b.WriteString(`\\`)
		case r == '\'':
			b.WriteString(`\'`)
		case r == '\n':
			b.WriteString(`\n`)
		case r == '\r':
			b.WriteString(`\r`)
		case r == '\t':
			b.WriteString(`\t`)
		case r < 0x20 || r == 0x7f || r == 0x2028 || r == 0x2029:
			fmt.Fprintf(&b, `\u%04x`, r)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('\'')
	return b.String()
}
