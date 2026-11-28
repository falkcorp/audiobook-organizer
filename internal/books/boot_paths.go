// file: internal/books/boot_paths.go
// version: 1.0.0
// guid: 5e1b7c94-2d38-4a06-9f73-c8a1d40b6e52
// last-edited: 2026-11-20

package books

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/mediacore/app"
	coreconfig "github.com/falkcorp/mediacore/config"
	"github.com/falkcorp/mediacore/module"
)

// BootInputs are the pre-boot layers the command line contributes to core's
// config boot (05-config section 1.12.4 "Flags"): the flags the operator set
// explicitly, by canonical config path (never a flag's default: a default
// would lock the key), and the operator file --config names. cmd builds them
// from the cobra flags; a caller with none (a test, the offline help) passes
// the zero value and gets the environment and the compiled defaults alone.
type BootInputs struct {
	Flags      map[coreconfig.Path]string
	ConfigFile string
}

// headStoreName is the relative path HEAD's --db flag defaulted to. It is read
// only to refuse a boot that would leave that store behind (refuseOrphanedStore).
const headStoreName = "audiobooks.pebble"

// bootOptions is the part of app.Options that decides where the store and the
// state directory are: ABO's identity, the compiled state-directory default
// and the config layers. Options and OfflineOptions both start from it, so
// serve and every offline command resolve through one rule.
func bootOptions(boot BootInputs) app.Options {
	retired, bindings, passthrough := flipConfigNames()
	return app.Options{
		AppID:              AppID,
		StateDir:           config.DefaultSecureStateDir,
		ConfigFile:         boot.ConfigFile,
		ConfigFlags:        boot.Flags,
		RetiredConfigNames: retired,
		ConfigEnvBindings:  bindings,
		ConfigPassthrough:  passthrough,
	}
}

// ConfigCheckOptions is the app.Options core's offline config check runs with:
// ABO's identity, the compiled defaults and the config layers boot names, with
// nothing resolved (the check opens no store and creates no directory).
func ConfigCheckOptions(boot BootInputs) app.Options {
	o := bootOptions(boot)
	o.CoreDefaults = coreDefaults
	return o
}

// ResolveBootPaths is the store path and the state directory a boot with boot
// opens, resolved by core's step 0 over the same layers serve reads (the
// environment, boot.Flags, the operator file, the compiled default), with the
// refusals that keep a layer change from silently pointing the service at
// another store: an orphaned ./audiobooks.pebble and a HEAD-keyed home file
// nothing reads any more. It adopts the directory as the one
// config.SecureStateDir reports, so the key, the bootstrap token and the
// swap slot are looked for where core put the rest of the state. It creates
// the state directory when it is absent, as the boot would.
func ResolveBootPaths(ctx context.Context, mod module.Module, boot BootInputs) (dbPath, stateDir string, err error) {
	if err := refuseHEADHomeFile(boot.ConfigFile); err != nil {
		return "", "", err
	}
	if err := refuseHEADKeyedConfigFile(boot.ConfigFile); err != nil {
		return "", "", err
	}
	if err := RefuseRetiredOperatorSettings(boot.ConfigFile, os.Environ()); err != nil {
		return "", "", err
	}
	dbPath, stateDir, err = app.ResolveStorage(ctx, bootOptions(boot), []module.Module{mod}, "")
	if err != nil {
		return "", "", err
	}
	if err := refuseOrphanedStore(dbPath, stateDir); err != nil {
		return "", "", err
	}
	config.AdoptStateDir(stateDir)
	return dbPath, stateDir, nil
}

// refuseOrphanedStore refuses the one resolution that can make a boot open a
// new, empty store beside a populated one: no layer named a store (the path is
// core's default under the state directory) while the store HEAD's --db
// default named exists in the working directory. HEAD opened it; the
// flipped boot would not, and would serve an empty library looking healthy.
func refuseOrphanedStore(dbPath, stateDir string) error {
	if dbPath != filepath.Join(stateDir, "storage", "main.pebble") {
		return nil
	}
	legacy, err := filepath.Abs(headStoreName)
	if err != nil {
		return nil
	}
	if _, err := os.Stat(legacy); err != nil {
		return nil
	}
	return fmt.Errorf("books: no store path is configured, and %s exists in the working directory: HEAD opened it by default, "+
		"this release would open a new empty store at %s instead; set %s=%s (or pass --db %s) to keep using it, "+
		"or set %s to %s to start a new one deliberately",
		legacy, dbPath, storagePathEnv, legacy, legacy, storagePathEnv, dbPath)
}

// storagePathEnv is the environment name of core.storage.path.
const storagePathEnv = "ABO_CORE_STORAGE_PATH"

// headHomeFiles are the files HEAD's viper read when --config was not given.
var headHomeFiles = []string{".audiobook-organizer.yaml", ".audiobook-organizer.yml"}

// refuseHEADHomeFile refuses to boot while a HEAD-keyed settings file sits at
// the place HEAD read one by default. Nothing reads it now (the operator file
// is --config or ABO_CONFIG, with canonical keys) and the cutover does not
// import it (M2 gap-fills from the config.yaml beside the database only), so
// leaving it would drop its settings without a word. A --config file is core's
// to judge: it reads it, and refuses a retired key at a protected or secret
// path, warning on the rest (05-config section 1.3.6).
func refuseHEADHomeFile(configFile string) error {
	if configFile != "" {
		return nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	for _, name := range headHomeFiles {
		p := filepath.Join(home, name)
		if _, err := os.Stat(p); err == nil {
			return fmt.Errorf("books: %s is a settings file HEAD read by default and nothing reads it any more, and the cutover does not import it: "+
				"move its settings to ABO_* environment names or to an operator file with canonical keys (--config <file> or ABO_CONFIG), "+
				"then delete it", p)
		}
	}
	return nil
}

// refuseHEADKeyedConfigFile refuses a --config file (or ABO_CONFIG's) that holds
// HEAD's flat keys. HEAD's viper read it as settings; the operator file is
// core's now and reads canonical keys only, and the cutover imports only the
// config.yaml beside the database (05-config section 1.12.2), never this file.
// Core refuses a retired key at a protected or secret path and merely warns for
// the rest (section 1.3.6), so the rest would be dropped on a log line: this
// refuses by file and key name, never a value, so a HEAD-era file cannot lose
// settings silently. A file this cannot parse as a mapping is core's to judge.
func refuseHEADKeyedConfigFile(configFile string) error {
	if configFile == "" {
		return nil
	}
	raw, err := os.ReadFile(configFile)
	if err != nil {
		return nil
	}
	var top map[string]any
	if yaml.Unmarshal(raw, &top) != nil {
		return nil
	}
	retired := map[string]bool{}
	for _, r := range flipRetiredFileKeys() {
		retired[r] = true
	}
	var found []string
	for k := range top {
		if retired[k] {
			found = append(found, k)
		}
	}
	if len(found) == 0 {
		return nil
	}
	slices.Sort(found)
	return fmt.Errorf("books: %s holds settings under HEAD's flat key names (%s), which nothing reads any more and the cutover does not import from a --config file: "+
		"move them to canonical keys in the operator file or to ABO_* environment names (the R2 release note lists every rename), "+
		"or remove them; the config.yaml beside the database is imported once by the cutover and needs no change (%w)",
		configFile, strings.Join(found, ", "), coreconfig.ErrRetiredName)
}

// flipRetiredFileKeys are the top-level operator-file keys HEAD read that the
// flip retires, from the same list core's boot is given.
func flipRetiredFileKeys() []string {
	retired, _, _ := flipConfigNames()
	var out []string
	for _, r := range retired {
		if r.File {
			out = append(out, r.Name)
		}
	}
	return out
}
