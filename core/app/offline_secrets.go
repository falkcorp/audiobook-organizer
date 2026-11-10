// file: core/app/offline_secrets.go
// version: 1.0.0
// guid: 8c1d4e7a-2b95-4f30-a6e8-d07c3b9f5a14
// last-edited: 2026-11-10

package app

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/falkcorp/mediacore/kernel"
	"github.com/falkcorp/mediacore/module"
	"github.com/falkcorp/mediacore/secrets"
	"github.com/falkcorp/mediacore/storage"
)

// The three secrets commands (05-config §2.4 "How an app's CLI reaches these
// commands"; 06 §6.4.5). Each uses the default keyring file,
// secrets.keyring in the state directory: core.secrets.key_file is not
// readable offline until the config flip hands these commands the app's
// config (F3-07).

const exportKeyringLong = `Copies the secrets keyring (secrets.keyring in the state directory) to
--out, a new file only its owner can read (mode 0600), and records the key
IDs it carried and the time in secrets.keyring.exported beside the keyring
(no key material). The copy is the install's only key artifact: keep it off
this host. Whoever holds it can decrypt the stored secrets and the
install's encrypted backups. storage restore-offline --keyring puts it back.

It opens no store, so it runs with the service stopped or while it serves.
It refuses an --out that exists with a mode looser than 0600, that is not a
regular file, or that is the keyring itself.

It runs only as the state directory's owner, the user the service runs as
(sudo -u <that user>): the record it writes must stay readable by the
service, whose health reports secrets_keyring_not_exported until every key
is in an export.`

const rotateDeriveLong = `Rotates the derive key: adds a new active derive key and keeps the previous
one for verification only (status verify) until now plus the longer of the
backup retention and the remember-me session TTL (168h by default). What was
signed or encrypted under the old key (backup MACs, archives, sign-in state)
still verifies; a later start retires the old key once nothing retained
needs it. Stored secrets are not re-encrypted: data keys do not change.

It runs only with the service stopped (it refuses while the service holds
the store) and only as the state directory's owner. The export no longer
covers the keyring afterwards: run secrets export-keyring again.`

const setInstallIDLong = `Sets the install ID in the secrets keyring's header back to --install-id
and prints the old and the new value. It is the last step of undoing a
factory reset by hand: once the set-aside store is moved back, the keyring's
header names the reset's fresh store, and --install-id is the ID the reset
journal's set_aside record names. Only the header changes: the keys, and so
the export, stay valid.

It runs only with the service stopped (it refuses while the service holds
the store) and only as the state directory's owner. It refuses an ID that is
not the install ID of the store at core.storage.path, and a state directory
without a keyring.`

// exportKeyringCommand is secrets export-keyring: secrets.ExportKeyring
// over the app's state directory, opening no store (SEC10-9).
func exportKeyringCommand(opts Options, mods []module.Module) Command {
	return Command{
		Name:  "secrets export-keyring",
		Short: "Copy the secrets keyring to a file to keep off this host",
		Long:  exportKeyringLong,
		Run: func(ctx context.Context, args []string, stdout io.Writer) error {
			fl := flag.NewFlagSet("secrets export-keyring", flag.ContinueOnError)
			out := fl.String("out", "", "the file to write (created with mode 0600)")
			if help, err := parseFlags(fl, args, stdout, "secrets export-keyring --out <file>", exportKeyringLong); help || err != nil {
				return err
			}
			if strings.TrimSpace(*out) == "" {
				return errors.New("secrets export-keyring: --out is required")
			}
			dbPath, stateDir, err := storagePath(opts, mods)
			if err != nil {
				return err
			}
			if dbPath == "" {
				return errors.New("secrets export-keyring: no module of this app declares storage, so it has no secret store and no keyring")
			}
			if err := storage.RefuseNotStateDirOwner(ctx, stateDir, "secrets export-keyring"); err != nil {
				return err
			}
			root, err := os.OpenRoot(stateDir)
			if err != nil {
				return fmt.Errorf("secrets export-keyring: open the state directory: %w", err)
			}
			res, err := secrets.ExportKeyring(root, "", *out, time.Now())
			if cerr := root.Close(); cerr != nil {
				err = errors.Join(err, cerr)
			}
			if err != nil {
				return fmt.Errorf("secrets export-keyring: %w", err)
			}
			var report strings.Builder
			fmt.Fprintf(&report, "Exported keys %s to %s.\n", strings.Join(res.KeyIDs, ", "), res.Out)
			if res.InstallID == "" {
				report.WriteString("The keyring header carries no install ID yet; export again after the service has started once.\n")
			} else {
				fmt.Fprintf(&report, "Install ID %s.\n", res.InstallID)
			}
			report.WriteString("Keep the file off this host: whoever holds it can decrypt the stored secrets and the encrypted backups.\n")
			_, err = io.WriteString(stdout, report.String())
			return err
		},
	}
}

// rotateDeriveCommand is secrets rotate-derive, through kernel.OpenOffline.
func rotateDeriveCommand(opts Options, mods []module.Module) Command {
	return Command{
		Name:  "secrets rotate-derive",
		Short: "Rotate the keyring's derive key with the service stopped",
		Long:  rotateDeriveLong,
		Run: func(ctx context.Context, args []string, stdout io.Writer) error {
			fl := flag.NewFlagSet("secrets rotate-derive", flag.ContinueOnError)
			if help, err := parseFlags(fl, args, stdout, "secrets rotate-derive", rotateDeriveLong); help || err != nil {
				return err
			}
			c, err := offlineConfig(opts, mods)
			if err != nil {
				return err
			}
			res, err := kernel.RotateDeriveOffline(ctx, c, time.Now())
			if err != nil {
				return fmt.Errorf("secrets rotate-derive: %w", err)
			}
			_, err = fmt.Fprintf(stdout, "Derive key %s is active; %s verifies until %s and retires once nothing retained needs it.\n"+
				"Run secrets export-keyring again: the last export does not hold %s.\n",
				res.New, res.Old, res.RetireAfter.Format(time.RFC3339), res.New)
			return err
		},
	}
}

// setInstallIDCommand is secrets set-install-id, through a read-only
// kernel.OpenOffline.
func setInstallIDCommand(opts Options, mods []module.Module) Command {
	return Command{
		Name:  "secrets set-install-id",
		Short: "Set the keyring header's install ID back after a factory-reset undo, with the service stopped",
		Long:  setInstallIDLong,
		Run: func(ctx context.Context, args []string, stdout io.Writer) error {
			fl := flag.NewFlagSet("secrets set-install-id", flag.ContinueOnError)
			id := fl.String("install-id", "", "the install ID the reset journal's set_aside record names")
			if help, err := parseFlags(fl, args, stdout, "secrets set-install-id --install-id <id>", setInstallIDLong); help || err != nil {
				return err
			}
			if strings.TrimSpace(*id) == "" {
				return errors.New("secrets set-install-id: --install-id is required")
			}
			c, err := offlineConfig(opts, mods)
			if err != nil {
				return err
			}
			old, err := kernel.SetInstallIDOffline(ctx, c, *id)
			if err != nil {
				return fmt.Errorf("secrets set-install-id: %w", err)
			}
			if old == "" {
				old = "(none)"
			}
			_, err = fmt.Fprintf(stdout, "install ID: %s -> %s\n", old, *id)
			return err
		},
	}
}
