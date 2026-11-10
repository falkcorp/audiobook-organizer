// file: core/kernel/secrets.go
// version: 1.0.0
// guid: 77d1e3c5-0f7d-4832-9d4c-79a5db3dd150
// last-edited: 2026-11-10

package kernel

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/falkcorp/mediacore/config"
	"github.com/falkcorp/mediacore/health"
	"github.com/falkcorp/mediacore/metrics"
	"github.com/falkcorp/mediacore/secrets"
	"github.com/falkcorp/mediacore/storage"
)

// This file is the kernel's handling of the secret store (05-config Part 2;
// 06 P4b). It is DARK: nothing in boot.go, kernel.go, storage.go or swap.go
// calls openSecrets, storageOwners does not list secretsOwnerSpec, and no boot
// creates secrets.keyring or writes a c/secrets/ row. F3-07 wires it, for
// every app (hello included), with the boot-writer row of 06 §6.5.1 and the
// crash-matrix cells together (09 §9.3):
//
//  1. storageOwners appends secretsOwnerSpec() (P2 Open and P4a Prepare read
//     the owners, so the keyspace exists once Prepare returns);
//  2. openSecrets at the start of P4b, after the P4a checkpoint and before
//     the migration list (06 §6.5.1 "Write rule"), with ResetBoot set on the
//     reset boot of a factory reset, before the KindReset journal is marked
//     applied (DATA7-17); the store becomes module.Importers.Secrets and
//     module.Deps.Secrets (scoped);
//  3. secretsMetrics after P5, when the Provider exists;
//  4. RetireUnused in P5 with the backup service's in-use check (F8-01), then
//     reportSecretsHealth on core.secrets (and again after every change that
//     can move a token: a Put that replaces an unreadable marker, an export).
//
// The offline commands below (rotate-derive, set-install-id) are live: they
// run inside OpenOffline and touch only the keyring file.

// secretsOwnerID is core/secrets' owner in the storage claim ledger: the owner
// of the c/secrets/ keyspace (05-config §2.3).
const secretsOwnerID storage.Owner = "core/secrets"

// The Reasons the kernel raises on core.secrets (05-health; 05-config §2.4,
// §2.5): Degraded, readiness unaffected.
const (
	reasonSecretsUndecryptable = "secrets_undecryptable"
	reasonKeyringNotExported   = "secrets_keyring_not_exported"
	reasonDeriveRetireBlocked  = "derive_key_retire_blocked"
)

// secretsOwnerSpec is the storage claim of core/secrets: the c/secrets/
// keyspace, Serialized as a whole, so every record write carries an Expect
// (Put and Stage read the record with GetForUpdate; c/secrets/meta is written
// expecting it absent). No claims, no migrations. Registering it takes the
// claim ledger's one-time write at P4b: F3-07's change, with the call.
func secretsOwnerSpec() storage.OwnerSpec {
	return storage.OwnerSpec{Owner: secretsOwnerID, Spec: storage.Spec{Keyspace: true, Serialized: []string{""}}, Enabled: true}
}

// secretsInput is what P4b hands openSecrets.
type secretsInput struct {
	DB *storage.DB // the store Prepare returned
	// StateDir is core.paths.state_dir, absolute: the keyring's directory.
	StateDir string
	AppID    string // app.Options.AppID, bound into every record's AAD
	// ResetBoot is set only on the reset boot of a factory reset (the
	// KindReset swap journal reads set_aside, 05-storage §5S.3.15).
	ResetBoot bool
	// KeyFile and AllowKeyRegen are core.secrets.key_file and
	// core.secrets.allow_key_regen (05-config §2.7); "" is the default key
	// file. A dry run passes a key file inside its scratch directory.
	KeyFile       string
	AllowKeyRegen bool
	Logger        *slog.Logger
	Now           func() time.Time
}

// kernelSecrets is the open secret store with the state directory root it
// reads the keyring through; the kernel closes both at shutdown.
type kernelSecrets struct {
	store *secrets.Store
	root  *os.Root
}

// close closes the store (wiping its key material), then the root.
func (s *kernelSecrets) close() error {
	return errors.Join(s.store.Close(), s.root.Close())
}

// openSecrets is P4b's secrets step (05-config §2.4 step 1; 06 §6.5.1): it
// opens the state directory as an os.Root and calls secrets.Open over the
// c/secrets/ keyspace with the store's install marker, so the keyring header
// carries it (and a reset boot rewrites it). It must run after the P4a
// checkpoint: Open writes c/secrets/meta and, on a first run, the keyring.
func openSecrets(ctx context.Context, in secretsInput) (*kernelSecrets, error) {
	if in.DB == nil {
		return nil, errors.New("kernel: secrets: no storage handle (open the secret store at P4b, after P4a)")
	}
	if in.StateDir == "" {
		return nil, errors.New("kernel: secrets: no state directory (core.paths.state_dir), where the keyring lives")
	}
	ks, ok := in.DB.Keyspace(secretsOwnerID)
	if !ok {
		return nil, fmt.Errorf("kernel: secrets: owner %s has no keyspace: append secretsOwnerSpec() to storageOwners (F3-07)", secretsOwnerID)
	}
	root, err := os.OpenRoot(in.StateDir)
	if err != nil {
		return nil, fmt.Errorf("kernel: secrets: open the state directory: %w", err)
	}
	s, err := secrets.Open(ctx, secrets.Options{
		StateDir: root, KeyFile: in.KeyFile, Keyspace: ks, AppID: in.AppID,
		AllowKeyRegen: in.AllowKeyRegen, ResetBoot: in.ResetBoot,
		InstallID: in.DB.Admin().InstallID(), Logger: in.Logger, Now: in.Now,
	})
	if err != nil {
		return nil, errors.Join(fmt.Errorf("kernel: secrets: %w", err), root.Close())
	}
	return &kernelSecrets{store: s, root: root}, nil
}

// secretsMetrics registers the store's instruments on the core "secrets"
// scope (05-config §2.8) and returns the scope. It runs after P5, when the
// Provider exists; a core scope registers once.
func secretsMetrics(p *metrics.Provider, s *secrets.Store) (*metrics.Scope, error) {
	sc, err := p.Core("secrets", "op", "outcome", "use", "status")
	if err != nil {
		return nil, fmt.Errorf("kernel: secrets metrics scope: %w", err)
	}
	if err := s.Instrument(sc); err != nil {
		return nil, fmt.Errorf("kernel: secrets metrics: %w", err)
	}
	return sc, nil
}

// secretsHealth is core.secrets' Result: Up, or Degraded with one Reason,
// the most urgent of secrets_undecryptable (a stored secret must be
// re-entered, 05-config §2.5), derive_key_retire_blocked (a verify derive key
// past retire_after is still in use, §2.3) and secrets_keyring_not_exported
// (a keyring key the export record does not name, §2.4). The Detail, shown
// only on authenticated endpoints, names every condition found: secret names
// and key IDs, never a value. A Result carries one Reason, so a lower one is
// visible in the Detail only until 05-health's Result grows a list (F8).
func secretsHealth(ctx context.Context, s *secrets.Store) health.Result {
	reason := ""
	var details []string
	raise := func(r, detail string) {
		if reason == "" {
			reason = r
		}
		details = append(details, detail)
	}
	switch bad, err := s.Undecryptable(ctx); {
	case err != nil:
		// The check itself failed: reported, never taken for "all readable".
		raise(reasonSecretsUndecryptable, "the stored secrets could not be checked: "+err.Error())
	case len(bad) > 0:
		names := make([]string, len(bad))
		for i, n := range bad {
			names[i] = string(n)
		}
		raise(reasonSecretsUndecryptable, fmt.Sprintf("%d stored secret(s) cannot be read and must be re-entered: %s", len(bad), strings.Join(names, ", ")))
	}
	if blocked := s.RetireBlocked(); len(blocked) > 0 {
		raise(reasonDeriveRetireBlocked, fmt.Sprintf("derive key(s) %s are past retire_after and kept: a retained archive, undo point, checkpoint or revocation-journal line still names them, or the check failed", strings.Join(blocked, ", ")))
	}
	switch missing, err := s.UnexportedKeys(); {
	case err != nil:
		raise(reasonKeyringNotExported, "the export record cannot be read ("+err.Error()+"); run secrets export-keyring")
	case len(missing) > 0:
		raise(reasonKeyringNotExported, fmt.Sprintf("keyring key(s) %s are in no export; run secrets export-keyring and keep the copy off this host", strings.Join(missing, ", ")))
	}
	if reason == "" {
		return health.Result{Status: health.StatusUp}
	}
	return health.Result{Status: health.StatusDegraded, Reason: reason, Detail: strings.Join(details, "; ")}
}

// reportSecretsHealth pushes secretsHealth to core.secrets' reporter.
func reportSecretsHealth(ctx context.Context, rep health.Reporter, s *secrets.Store) {
	rep.Report(secretsHealth(ctx, s))
}

// The config keys that set rotate-derive's window (05-config §2.3
// "Derive-key rotation"): the backup retention and the longest refresh-shaped
// TTL core declares (05-authn's remember-me session).
const (
	backupMaxAgeKey config.Path = "core.backup.retention.max_age"
	rememberTTLKey  config.Path = "core.auth.session.remember_ttl"
)

// rotateDeriveWindow is rotate-derive's window: the longer of
// core.backup.retention.max_age and core.auth.session.remember_ttl, read from
// a config store holding the core sections. Until F3-07 hands the offline
// commands the booted config (its operator layers and DB layer; F3-02 builds
// the env and file layers), that store has no layer, so the values are the
// declared defaults (0 and 168h). RetireUnused remakes the retirement
// decision every boot, so a window shorter than an operator's retention
// still never retires a key a retained archive needs.
func rotateDeriveWindow(ctx context.Context, appID string) (w time.Duration, err error) {
	st, err := config.NewStore(config.Options{AppID: appID, Logger: slog.New(slog.DiscardHandler)})
	if err != nil {
		return 0, fmt.Errorf("kernel: secrets rotate-derive: config store: %w", err)
	}
	defer func() { err = errors.Join(err, st.Close(ctx)) }()
	var secs []any
	for _, sec := range coreSections() {
		if p := pathOf(sec); p != pathsSection && p != modulesSection {
			secs = append(secs, sec)
		}
	}
	if _, err := config.RegisterSections(st.Registry(), secs...); err != nil {
		return 0, fmt.Errorf("kernel: secrets rotate-derive: register the core sections: %w", err)
	}
	for _, p := range []config.Path{backupMaxAgeKey, rememberTTLKey} {
		ks, ok := st.Lookup(p)
		if !ok {
			return 0, fmt.Errorf("kernel: secrets rotate-derive: %s is not declared", p)
		}
		var d config.Duration
		if err := json.Unmarshal(ks.Effective, &d); err != nil {
			return 0, fmt.Errorf("kernel: secrets rotate-derive: %s: %w", p, err)
		}
		w = max(w, time.Duration(d))
	}
	return w, nil
}

// errInstallIDNotStore: set-install-id was given another ID than the
// store's install marker.
var errInstallIDNotStore = errors.New("kernel: the install ID is not the install marker of the store beside the keyring")

// RotateDeriveOffline is secrets rotate-derive (05-config §2.3, SEC2-24;
// 06 §6.4.5): inside OpenOffline (read-only: the store is not written, but
// its lock is held, so a running service refuses it) and only as the state
// directory's owner, it adds d<n+1> as the active derive key of the keyring
// and sets the previous one to verify, retiring after rotateDeriveWindow. It
// uses the default key file (core.secrets.key_file is not readable offline
// until F3-07). The export record no longer covers the keyring, so
// secrets_keyring_not_exported is raised until the operator exports again.
func RotateDeriveOffline(ctx context.Context, c Config, now time.Time) (res secrets.RotateResult, err error) {
	if err := storage.RefuseNotStateDirOwner(ctx, c.StateDir, "secrets rotate-derive"); err != nil {
		return res, err
	}
	o, err := OpenOffline(ctx, c, true)
	if err != nil {
		return res, err
	}
	defer func() { err = errors.Join(err, o.Close()) }()
	window, err := rotateDeriveWindow(ctx, c.AppID)
	if err != nil {
		return res, err
	}
	root, err := os.OpenRoot(c.StateDir)
	if err != nil {
		return res, fmt.Errorf("kernel: secrets rotate-derive: open the state directory: %w", err)
	}
	defer func() { err = errors.Join(err, root.Close()) }()
	return secrets.RotateDerive(root, "", window, now)
}

// SetInstallIDOffline is secrets set-install-id (05-config §2.4, DATA8-12;
// 06 §6.4.5): inside a read-only OpenOffline (the install marker is read, the
// store is not written, a running service refuses it) and only as the state
// directory's owner, it rewrites the keyring header's install_id to id and
// returns the previous value. It refuses an id other than the store's
// c/storage/install marker, so the header can only take the lineage of the
// store beside it, and a missing keyring.
func SetInstallIDOffline(ctx context.Context, c Config, id string) (old string, err error) {
	if err := storage.RefuseNotStateDirOwner(ctx, c.StateDir, "secrets set-install-id"); err != nil {
		return "", err
	}
	o, err := OpenOffline(ctx, c, true)
	if err != nil {
		return "", err
	}
	defer func() { err = errors.Join(err, o.Close()) }()
	if marker := o.db.Admin().InstallID(); id != marker {
		return "", fmt.Errorf("%w: --install-id %q, the store at %s has install ID %q (move the set-aside store back first)", errInstallIDNotStore, id, c.StoragePath, marker)
	}
	root, err := os.OpenRoot(c.StateDir)
	if err != nil {
		return "", fmt.Errorf("kernel: secrets set-install-id: open the state directory: %w", err)
	}
	defer func() { err = errors.Join(err, root.Close()) }()
	return secrets.SetInstallID(root, "", id)
}
