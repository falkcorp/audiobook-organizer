// file: internal/books/operator_config.go
// version: 0.1.0
// guid: 2c7e4a91-6f38-4b05-8d1a-9e3f5c0b7d26
// last-edited: 2026-11-20

package books

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/falkcorp/audiobook-organizer/internal/books/appconfig"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/mediacore/app"
	"github.com/falkcorp/mediacore/module"
)

// OperatorAppConfig is HEAD's flat Config as boot's operator layers alone give
// it (F3-07 slice 3): ABO's compiled defaults, the operator file, the
// environment and boot.Flags, loaded by core as a boot loads them
// (app.OperatorConfig) and projected through the rename table by the same
// Engine the books module installs, with no stored value. It opens no store,
// reads no boot record and creates no directory.
//
// It is what config.AppConfig holds before serve's Facade is installed and in
// every command that never boots the App (cmd's initConfig projects it). The
// fields only the store holds (the class-i residual: library roots, metadata
// sources, AI endpoints, plugins) are zero here, except a residual field an
// operator layer sets through its passthrough name (the Deluge connection)
// and the library root an operator layer names in core.library.roots
// (ABO_CORE_LIBRARY_ROOTS, which the projection overlays on root_dir and
// path_aliases), which are the operator's values, as the booted Facade has
// them. A retired name
// that would refuse the boot refuses here, naming it, and so do the HEAD-era
// settings files ResolveBootPaths refuses.
func OperatorAppConfig(ctx context.Context, mod module.Module, boot BootInputs) (config.Config, error) {
	if err := refuseHEADHomeFile(boot.ConfigFile); err != nil {
		return config.Config{}, err
	}
	if err := refuseHEADKeyedConfigFile(boot.ConfigFile); err != nil {
		return config.Config{}, err
	}
	opts := bootOptions(boot)
	opts.CoreDefaults = coreDefaults
	acc, err := app.OperatorConfig(ctx, opts, []module.Module{mod})
	if err != nil {
		return config.Config{}, err
	}
	defer func() { _ = acc.Close(context.WithoutCancel(ctx)) }()
	var dbPath string
	if ks, ok := acc.Lookup(storagePathKey); ok && len(ks.Effective) > 0 {
		if err := json.Unmarshal(ks.Effective, &dbPath); err != nil {
			return config.Config{}, fmt.Errorf("books: %s: %w", storagePathKey, err)
		}
	}
	eng := appconfig.New(acc, nil,
		appconfig.WithBase(func() config.Config { return appConfigBase(dbPath) }),
		appconfig.WithResidual(operatorResidual{}))
	if err := eng.Rebuild(ctx); err != nil {
		return config.Config{}, fmt.Errorf("books: projecting the operator settings: %w", err)
	}
	return eng.Snapshot(), nil
}

// storagePathKey is core.storage.path, the store the operator layers name ("" =
// step 0's default under the state directory, which this does not resolve:
// resolving creates the directory).
const storagePathKey = "core.storage.path"

// errOperatorResidualReadOnly is operatorResidual's refusal of a write.
var errOperatorResidualReadOnly = errors.New("books: the operator settings hold no stored values; a write needs the booted App")

// operatorResidual is the empty class-i store of an operator-only projection:
// nothing is stored, so every residual field is unset and the Engine overlays
// the operator values of the ones that have a passthrough name.
type operatorResidual struct{}

func (operatorResidual) Load(context.Context, []string) (map[string]json.RawMessage, error) {
	return map[string]json.RawMessage{}, nil
}

func (operatorResidual) Save(context.Context, map[string]json.RawMessage, []string) error {
	return errOperatorResidualReadOnly
}

func (operatorResidual) Redact(_ string, v json.RawMessage) json.RawMessage { return v }
