// file: core/module/settings.go
// version: 1.1.0
// guid: 5b0f4d8e-2c79-4a6e-9d13-8e7b6f1a0c42
// last-edited: 2026-11-19

package module

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/falkcorp/mediacore/config"
	"github.com/falkcorp/mediacore/secrets"
	"github.com/falkcorp/mediacore/storage"
)

// SettingsSection wraps a config.Section[T] so a Manifest can list sections
// of different T (06 §6.4.2). The kernel registers each of an enabled
// module's sections with config before the store loads (06 §6.5.1 P3).
type SettingsSection struct {
	path     config.Path
	register func(r config.Registry) (any, error)
}

// Settings wraps a declared section for Manifest.Settings.
func Settings[T any](s config.Section[T]) SettingsSection {
	return SettingsSection{
		path: s.Path(),
		register: func(r config.Registry) (any, error) {
			h, err := config.Register(r, s)
			if err != nil {
				return nil, err
			}
			return h, nil
		},
	}
}

// Path is the section's declared path.
func (s SettingsSection) Path() config.Path { return s.path }

// Register registers the section with r and returns its *config.Handle[T]
// as an any. Kernel-only: P3 calls it for every section of an enabled
// module, before Store.Load. The zero SettingsSection (not built by
// Settings) fails.
func (s SettingsSection) Register(r config.Registry) (any, error) {
	if s.register == nil {
		return nil, errors.New("module: Register of a SettingsSection not built by module.Settings")
	}
	return s.register(r)
}

// settingsInNamespace reports whether p is "modules.<id>" or below it.
func settingsInNamespace(id ID, p config.Path) bool {
	root := "modules." + string(id)
	return string(p) == root || strings.HasPrefix(string(p), root+".")
}

// SettingsProblems is P1's check of the sections m declares (06 §6.5.1 P1,
// §6.4.2): every path is "modules.<ID>" or below
// (ProblemSettingsNamespace), a section is built by Settings, and no path
// is declared twice by the module. It is the manifest's static half; the
// cross-module overlap is config.Register's refusal at P3 (ErrDuplicate).
// Kernel-only.
func SettingsProblems(m Manifest) []Problem {
	var probs []Problem
	seen := make(map[config.Path]bool, len(m.Settings))
	for i, s := range m.Settings {
		switch {
		case s.register == nil:
			probs = append(probs, Problem{Module: m.ID, Kind: ProblemSettingsNamespace,
				Detail: fmt.Sprintf("Manifest.Settings[%d] was not built by module.Settings", i)})
		case !settingsInNamespace(m.ID, s.path):
			probs = append(probs, Problem{Module: m.ID, Kind: ProblemSettingsNamespace,
				Detail: fmt.Sprintf("settings section %q is outside modules.%s (a section of module %s lives at modules.%s or below, 05-config §1.3.2)", s.path, m.ID, m.ID, m.ID)})
		case seen[s.path]:
			probs = append(probs, Problem{Module: m.ID, Kind: ProblemSettingsNamespace,
				Detail: fmt.Sprintf("settings section %q is declared twice", s.path)})
		}
		seen[s.path] = true
	}
	return probs
}

// Setting returns the live handle of a section this module declared in its
// Manifest, to be read in Register (06 §6.4.2). It is never nil:
//   - the section is declared and the kernel registered it: the live handle;
//   - the section is declared but no live handle exists (app.Describe, which
//     builds no config store, or a boot without core config): a handle
//     holding the section's defaults;
//   - the section is not in the module's Manifest.Settings: it records
//     ProblemSettingsUndeclared and returns a handle holding the defaults.
//
// A handle from a registered section of another T than s is the same
// problem.
func Setting[T any](r *Registrar, s config.Section[T]) *config.Handle[T] {
	var live any
	declared := false
	r.record("Setting", func() {
		live, declared = r.settings[s.Path()]
		if !declared {
			r.problem(ProblemSettingsUndeclared, "settings section %q is not in Manifest.Settings", s.Path())
		}
	})
	if h, ok := live.(*config.Handle[T]); ok && h != nil {
		return h
	}
	if live != nil {
		r.record("Setting", func() {
			r.problem(ProblemSettingsUndeclared, "settings section %q is registered with a different type than %T", s.Path(), s)
		})
	}
	return defaultsHandle(s)
}

// defaultsHandle is a handle holding s's defaults: the section registered
// and loaded in a store of its own, with no layer, and closed. It reads no
// environment and no file, so it cannot differ between hosts. A registration
// that fails (an invalid section) panics: the section is the module's own
// declaration, and the same Register fails the boot at P3 with the error.
func defaultsHandle[T any](s config.Section[T]) *config.Handle[T] {
	ctx := context.Background()
	st, err := config.NewStore(config.Options{AppID: "defaults", Logger: slog.New(slog.DiscardHandler)})
	if err != nil {
		panic(fmt.Sprintf("module: defaults store: %v", err))
	}
	h, err := config.Register(st.Registry(), s)
	if err != nil {
		panic(fmt.Sprintf("module: settings section %q: %v", s.Path(), err))
	}
	if err := st.Load(ctx); err != nil {
		panic(fmt.Sprintf("module: settings section %q: load: %v", s.Path(), err))
	}
	if err := st.Close(ctx); err != nil {
		panic(fmt.Sprintf("module: settings section %q: close: %v", s.Path(), err))
	}
	return h
}

// Importers are core's importers (03 §3.3 "Store interfaces and importers"),
// each bound by the kernel to its subsystem's keyspace and staging into the
// CALLER's batch (06 §6.4.2). A field exists from the R2 flip that lands its
// importer; the config flip (F3) lands Config and Secrets.
//
// The values a Cutover func receives are bound late: the kernel builds the
// migration list at P2, the importers' stores open at P4b, so each is a
// forwarding value that refuses (ErrImporterNotBound) until the kernel binds
// it at the start of P4b, and a migration that runs before then fails
// instead of writing anywhere.
type Importers struct {
	Config  config.Importer  // F3
	Secrets secrets.Importer // F3; encrypts with the keyring in the state directory

	// Legacy hands owner's legacy-range reader, the rows its cutover steps plan
	// from. The runner steps a step once per source key and gives it no range,
	// so a step that plans from more than its own source (books' M2 reads the
	// whole settings family) reads it here. It returns nil until the kernel
	// binds it (after Prepare, at the start of P4b) and for an owner that
	// declared no claims; a step treats nil as "the store is not open yet" and
	// fails. Never nil as a func. Added by F3-07 slice d for M2; removed with
	// M2 by the cleanup that deletes the cutover steps (seams.d/F3-07.md).
	Legacy func(owner storage.Owner) storage.Reader
	// AllowKeyRegen reports core.secrets.allow_key_regen as the layers resolved
	// it for this boot: true lets M2 go on without HEAD's settings key and mark
	// the rows it cannot decrypt unreadable. False until bound, and false on a
	// boot whose config store is not wired. Never nil as a func. Added and
	// removed with Legacy.
	AllowKeyRegen func() bool
}

// ErrImporterNotBound is what a late-bound importer returns before the
// kernel bound it.
var ErrImporterNotBound = errors.New("module: the importer is not bound yet (it is bound at the start of P4b)")
