// file: internal/books/boot.go
// version: 1.0.0
// guid: 8d3f6a21-5c4e-4b9a-9e17-2f6c0b8d4a53
// last-edited: 2026-11-08

package books

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/startup"
)

// books' settings boot (06 §6.4.3 "Health, startup steps and diagnostics",
// R4-6, COR4-1, MOD4-17): from release R1 the kernel opens the store at P2,
// and the HEAD steps serve ran over it before app.New run here, in books'
// Register, over the store core opened: the key guard, the encryption init,
// the settings load with the env sync, and config.Validate. Each is Fatal as
// it was in serve (startup.Table): running on defaults would write the
// defaults over the stored settings on the first save (06 §6.9 row "HEAD's
// settings load fails in R1"). R2's config flip replaces them with core
// config and secrets. They run in Register, not in the start hook, because
// NewServer reads the effective config and Register builds the server the
// legacy mount needs before P7 (seams.d/R1-23.md).

// The settings boot's dependencies, variables so tests can fail each step.
var (
	initEncryption    = database.InitEncryption
	loadConfigFromDB  = config.LoadConfigFromDatabase
	syncConfigFromEnv = config.SyncConfigFromEnv
)

// settingsStore is what the settings boot reads: the settings census (the key
// guard and the settings load) and the import paths it logs.
type settingsStore interface {
	database.SettingsStore
	GetAllImportPaths() ([]database.ImportPath, error)
}

// bootSettings runs HEAD's pre-server steps over store, through steps, in
// HEAD's order. stateDir is the secure state directory Options resolved; the
// key's legacy location is the database's own directory.
func bootSettings(store settingsStore, steps *startup.Steps, stateDir string) error {
	legacyKeyDir := filepath.Dir(config.AppConfig.DatabasePath)
	if err := steps.Run("guardAgainstKeyRegeneration", func() error {
		return guardAgainstKeyRegeneration(store, stateDir, legacyKeyDir)
	}); err != nil {
		return err
	}
	// The key lives in the secure state directory, a constant, not beside
	// the database, which may move between pools; the database's directory
	// is passed as the legacy location so an install whose key still sits
	// there keeps working. InitEncryption reads it there and logs where to
	// move it; it never copies the file, and it never generates a
	// replacement for a key it merely failed to read.
	if err := steps.Run("initEncryption", func() error {
		if err := initEncryption(stateDir, legacyKeyDir); err != nil {
			return fmt.Errorf("failed to initialize encryption: %w", err)
		}
		return nil
	}); err != nil {
		return err
	}
	bootLog.Info("settings encryption initialized (state dir: %s)", stateDir)

	// Fatal, not a warning: SaveConfigToDatabase persists the whole config
	// struct, so a server running on defaults would write those defaults
	// over the stored config on its first settings save.
	if err := steps.Run("loadConfigFromDB", func() error {
		if err := loadConfigFromDB(store); err != nil {
			return fmt.Errorf("failed to load config from database: %w", err)
		}
		return nil
	}); err != nil {
		return err
	}
	snap := config.Snapshot()
	// Only whether the key is set: even a suffix is secret material in the
	// startup log (CodeQL go/clear-text-logging).
	openAIKeyState := "(not set)"
	if snap.OpenAIAPIKey != "" {
		openAIKeyState = "set"
	}
	bootLog.Info("configuration loaded from database; root dir (organize output): %s; OpenAI API key: %s", snap.RootDir, openAIKeyState)

	// Env overrides apply over the stored settings (the command line takes
	// precedence over the DB).
	syncConfigFromEnv()

	if err := steps.Run("config.Validate", func() error {
		if err := config.AppConfig.Validate(); err != nil {
			return fmt.Errorf("invalid configuration: %w", err)
		}
		return nil
	}); err != nil {
		return err
	}

	if folders, err := store.GetAllImportPaths(); err == nil {
		bootLog.Info("import paths (scan paths): %d configured", len(folders))
		for _, folder := range folders {
			bootLog.Info("  import path %s (%s)", folder.Name, folder.Path)
		}
	}
	return nil
}

// allowKeyRegenEnv lets an operator proceed past guardAgainstKeyRegeneration.
// It exists so a recoverable situation cannot become an unbootable one: if the
// census itself is broken, or the operator has genuinely accepted the loss and
// wants to start over, there has to be a way forward that is not "edit the
// source". It does not get past unreadable settings rows, where it cannot help
// and would only mint a key that later costs the stored secrets; see
// guardAgainstKeyRegeneration.
const allowKeyRegenEnv = "ABK_ALLOW_ENCRYPTION_KEY_REGEN"

// settingsCensus is what guardAgainstKeyRegeneration reads: the settings rows.
type settingsCensus interface {
	GetAllSettings() ([]database.Setting, error)
}

// guardAgainstKeyRegeneration refuses to start when there is no encryption key
// anywhere AND the database already holds encrypted secrets.
//
// That combination is not recoverable by starting up. InitEncryption would
// generate a fresh key, and LoadConfigFromDatabase would then fail to decrypt
// every existing secret, re-encrypt the four it can recover from the config file
// and DeleteSetting the rest. The secrets are gone before anyone sees a warning,
// and the app looks healthy afterwards. Refusing to boot is the only point at
// which the operator can still put the key file back.
//
// Fails CLOSED on a census error, and names the override in the message: a
// store we cannot read is exactly the case where we must not gamble that there
// was nothing to lose.
//
// Order matters. A key file found anywhere returns first, so a normal boot runs
// no census. Otherwise the census runs BEFORE allowKeyRegenEnv is honoured,
// because one census result is refused even with the override set: an
// unreadable settings row (*database.UnreadableSettingsError). The override
// cannot get past it -- loadConfigFromDB fails on the same rows next -- and
// honouring it would let InitEncryption write a NEW key file first. Once the
// row is repaired, the following boot would find that key, skip this guard,
// fail to decrypt every stored secret, and LoadConfigFromDatabase would delete
// them. So that message names the rows and the repair and does not offer the
// override. Any other census error (I/O, iterator, close) still yields to the
// override, which is the escape hatch it exists for.
func guardAgainstKeyRegeneration(store settingsCensus, stateDir, legacyKeyDir string) error {
	if found := database.FindEncryptionKey(stateDir, legacyKeyDir); found != "" {
		return nil // a key exists; InitEncryption will load it, not generate one
	}

	settings, err := store.GetAllSettings()
	if unreadable, ok := errors.AsType[*database.UnreadableSettingsError](err); ok {
		// Refused regardless of allowKeyRegenEnv; see the doc comment.
		return fmt.Errorf("settings row(s) %s cannot be read; %s. No encryption "+
			"key was found in %q or %q, and whether encrypted settings exist "+
			"cannot be checked until those rows are readable: %w",
			unreadable.QuotedKeys(), unreadable.Remedy(), stateDir, legacyKeyDir, err)
	}
	if os.Getenv(allowKeyRegenEnv) != "" {
		return nil // operator accepted the loss (or the census is broken)
	}
	if err != nil {
		return fmt.Errorf("cannot determine whether encrypted settings exist, and no "+
			"encryption key was found in %q or %q — refusing to start, because "+
			"generating a new key would permanently discard any secrets already "+
			"stored. Restore the key file, or set %s=1 to accept the loss: %w",
			stateDir, legacyKeyDir, allowKeyRegenEnv, err)
	}
	if database.HasEncryptedSettings(settings) {
		return fmt.Errorf("no encryption key found in %q or %q, but the database "+
			"already holds encrypted settings — refusing to start. Starting would "+
			"generate a new key and permanently delete every secret that is not "+
			"also present in the config file. Put %s back, or set %s=1 to accept "+
			"the loss and re-enter those credentials",
			stateDir, legacyKeyDir, database.EncryptionKeyPath(stateDir), allowKeyRegenEnv)
	}
	return nil // first run: nothing encrypted yet, generating is correct
}
