// file: core/tasks/window.go
// version: 1.0.0
// guid: cbdfdee8-39cf-4632-b2a0-5d3470f108c2
// last-edited: 2026-11-11

package tasks

import "strings"

// The maintenance windows' one owner (05-tasks 5T.8.2, T4 table row
// maintenance.window: "core/tasks, the one owner: declared at F2 with the
// scheduler"). A window is declared by core config, core.tasks.windows.<name>
// (hours and an enable flag, 5T.10); a kind joins it through its
// ScheduleConfig.Window and runs at its ScheduleConfig.Order. The window
// itself runs as the kind core.window.<name>, which core's scheduler
// (core/internal/scheduler) fires once per opening and whose run starts the
// members in Order. No other package declares a window kind: a member's owner
// (core.storage.compact among them) only schedules into a window. Names
// coined by F2-04.

// WindowNightly is the kind of the nightly maintenance window, the one window
// core declares (HEAD's maintenance.window).
const WindowNightly Kind = "core.window.nightly"

// windowKindPrefix is the namespace of the window kinds.
const windowKindPrefix = "core.window."

// WindowKind is the kind a window runs as: core.window.<name>. A name that
// is not one config segment ([a-z][a-z0-9_]{0,47}) has no window kind, and
// the result fails ParseKind.
func WindowKind(name string) Kind {
	if !configSegment(name) {
		return ""
	}
	// Kind segments allow '-' but not '_': a window name maps to a kind
	// segment with '_' spelled '-', as config segments map to kind segments
	// elsewhere (hello.count, core.storage.compact).
	return Kind(windowKindPrefix + strings.ReplaceAll(name, "_", "-"))
}

// WindowName is the window a window kind runs (WindowKind's inverse); ok is
// false for a kind outside core.window.
func WindowName(k Kind) (name string, ok bool) {
	rest, ok := strings.CutPrefix(string(k), windowKindPrefix)
	if !ok || rest == "" || strings.Contains(rest, ".") {
		return "", false
	}
	name = strings.ReplaceAll(rest, "-", "_")
	if !configSegment(name) {
		return "", false
	}
	return name, true
}

// NightlyWindow is the nightly window's declaration with its default hours
// (core.tasks.windows.nightly.start / .end: 1 and 4, HEAD's maintenance
// window, config.go:3003-3005). The hours operators set live in config; this
// value is the default the kernel's core-section list declares and the
// window kind's name.
func NightlyWindow() Window { return Window{Name: "nightly", Start: 1, End: 4} }
