// file: core/kernel/identity.go
// version: 0.1.0
// guid: b99ab8e0-7b93-4938-99c1-1ece504234d8
// last-edited: 2026-11-20

package kernel

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/falkcorp/mediacore/audit"
	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/auth/password"
	"github.com/falkcorp/mediacore/backup/journal"
	"github.com/falkcorp/mediacore/internal/authstore"
	"github.com/falkcorp/mediacore/internal/authz"
	"github.com/falkcorp/mediacore/internal/rbacstore"
	"github.com/falkcorp/mediacore/metrics"
	"github.com/falkcorp/mediacore/rbac"
)

// This file is the kernel's identity assembly (05-rbac §5, 05-authn §3.2,
// 06 §6.5.1 P5): the rbac service over core's role store, its catalog
// declared and frozen, and the auth service over core's identity store. It is
// dark: nothing calls buildIdentity before F4-08's boot wiring (B3), which
// calls it in P5 once the app declares Options.Auth.
//
// It takes every dependency as a value and opens nothing: the role and
// identity stores come from P4c, the audit store from the one buildAudit made
// before P4b (storage.go), and the revocation journal component from P4d
// step 2. The kernel binds the journal elsewhere (journalfs, seams.d/
// F4-08-PB.md); no file of this package imports core/internal/journalfs
// until that wiring lands (TestJournalRowsAreUnboundUntilF408).
//
// What it does not do, and B3 does: CommitSeeds (P7, the seed-role rows),
// registering compat methods (P7), auth.BootWrites (P4d step 4), the restore
// hooks (P4d step 3, restore_hooks.go over the same two stores), stage 12's
// Bind and the events bus behind OnRoleChange.

// identityModule is one module's RBAC declarations, as 06 §6.4.2's
// Manifest.Permissions, Roles and ImportedRoleIDs will carry them. Those
// manifest fields are not in core/module yet (F4-08 B1 adds them), so the
// boot copies them into this struct.
type identityModule struct {
	ID              string
	Permissions     []rbac.PermissionSpec
	Roles           []rbac.RoleSeed
	ImportedRoleIDs []rbac.RoleName
}

// identityInputs is everything buildIdentity takes. Required: Users, Roles,
// Audit, Revocations and Settings. The two stores must be the P4c instances
// over one storage.DB (seams.d/F4-02.md: a batch that spans two DBs is
// poisoned), and the same instances the restore hooks and M3 use; NewBatch
// for each service is taken from its store, so the services and the Holders
// adapter cannot end up on different instances.
type identityInputs struct {
	// BinaryID is app.BinaryID, stamped on every revocation line rbac writes
	// (rbac.Options.Source).
	BinaryID string
	// RBACCacheSize is core.rbac.cache_size (0 is rbac's default).
	RBACCacheSize int
	// Modules are the declarations fed to rbac's Declarer, in order. Which
	// manifests are in it (enabled only, or disabled ones too) is the
	// caller's: 06 §6.5.1 P5 says disabled ones are declared, 05-rbac §5 item
	// 3 says they declare nothing.
	Modules []identityModule

	Users *authstore.Store
	Roles *rbacstore.Store
	// Audit is the boot's one audit store (buildAudit, before P4b); a
	// concrete pointer, so a nil store is caught here and not passed on as
	// a non-nil audit.Logger.
	Audit *audit.Store
	// Revocations is the journal component, an interface so tests can
	// inject one. A typed-nil component passes the check below: B3 passes
	// the bound *journal.Component or refuses to serve first.
	Revocations journal.RevocationJournal

	// AuthOptions is app.Options.Auth; Authenticators is
	// app.Options.Authenticators.
	AuthOptions    auth.Options
	Authenticators []auth.Extension
	// Settings is the live core.auth reader (authSettingsReader).
	Settings func() auth.Settings
	// OnRoleChange publishes core.rbac.changed.v1 (rbac.Deps.OnChange).
	OnRoleChange func(generation uint64)
	// Client, Reach and StateFiles are auth.Deps' (httpapi's resolver, the
	// listeners' reach, the labelled state filesystem).
	Client     func(*http.Request) auth.ClientInfo
	Reach      func() auth.Reach
	StateFiles auth.StateFiles
	// Hasher is nil in production (auth builds argon2id at its production
	// cost); tests pass a cheap one.
	Hasher *password.Hasher
	// Clock is nil in production (time.Now); it drives both services.
	Clock func() time.Time
	// RBACLog and AuthLog are the services' loggers; RBACMetrics and
	// AuthMetrics their scopes (identityMetrics). Nil records nothing.
	RBACLog, AuthLog         *slog.Logger
	RBACMetrics, AuthMetrics *metrics.Scope
}

// identityServices is what buildIdentity returns.
type identityServices struct {
	RBAC *rbac.Service
	Auth *auth.Service
}

// buildIdentity builds rbac and auth in the spec's order: rbac.New over the
// role store with authz.Compile and the Holders adapter over the identity
// store, every module's declarations, Freeze, then auth.New over the frozen
// rbac service. It writes nothing (rbac.New, Freeze and auth.New write
// nothing; auth.New reads whether any user exists). Any error fails the boot.
func buildIdentity(ctx context.Context, in identityInputs) (identityServices, error) {
	var missing []string
	if in.Users == nil {
		missing = append(missing, "Users")
	}
	if in.Roles == nil {
		missing = append(missing, "Roles")
	}
	if in.Audit == nil {
		missing = append(missing, "Audit")
	}
	if in.Revocations == nil {
		missing = append(missing, "Revocations")
	}
	if in.Settings == nil {
		missing = append(missing, "Settings")
	}
	if len(missing) > 0 {
		return identityServices{}, fmt.Errorf("kernel: identity: missing inputs %v", missing)
	}
	rb, err := rbac.New(ctx, rbac.Options{CacheSize: in.RBACCacheSize, Now: in.Clock, Source: in.BinaryID}, rbac.Deps{
		Store:       in.Roles,
		Compile:     authz.Compile,
		Audit:       in.Audit,
		OnChange:    in.OnRoleChange,
		Revocations: in.Revocations,
		Log:         in.RBACLog,
		Metrics:     in.RBACMetrics,
		NewBatch:    in.Roles.NewBatch,
		Holders:     newAuthHolders(in.Users),
	})
	if err != nil {
		return identityServices{}, fmt.Errorf("kernel: identity: rbac: %w", err)
	}
	if err := declareAndFreeze(ctx, rb, in.Modules); err != nil {
		return identityServices{}, err
	}
	as, err := auth.New(ctx, in.AuthOptions, auth.Deps{
		Store:       in.Users,
		NewBatch:    in.Users.NewBatch,
		RBAC:        rb,
		Audit:       in.Audit,
		Revocations: in.Revocations,
		Settings:    in.Settings,
		Clock:       in.Clock,
		Log:         in.AuthLog,
		Metrics:     in.AuthMetrics,
		Extensions:  in.Authenticators,
		Hasher:      in.Hasher,
		StateFiles:  in.StateFiles,
		Client:      in.Client,
		Reach:       in.Reach,
	})
	if err != nil {
		return identityServices{}, fmt.Errorf("kernel: identity: auth: %w", err)
	}
	return identityServices{RBAC: rb, Auth: as}, nil
}

// declareAndFreeze is P5's catalog step (05-rbac §5 items 3 and 4): each
// module's permissions and seed roles through its Declarer, with its
// ImportedRoleIDs, then Freeze, which adds core's fixed scopes, validates
// every declaration again and loads the stored roles. Every problem is
// returned joined; the service stays unfrozen on any of them.
func declareAndFreeze(ctx context.Context, rb *rbac.Service, mods []identityModule) error {
	var errs []error
	for _, m := range mods {
		d := rb.Declarer(m.ID, m.ImportedRoleIDs...)
		if err := d.Permissions(m.Permissions...); err != nil {
			errs = append(errs, fmt.Errorf("module %s permissions: %w", m.ID, err))
		}
		if err := d.Roles(m.Roles...); err != nil {
			errs = append(errs, fmt.Errorf("module %s roles: %w", m.ID, err))
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("kernel: identity: declare: %w", errors.Join(errs...))
	}
	if err := rb.Freeze(ctx); err != nil {
		return fmt.Errorf("kernel: identity: %w", err)
	}
	return nil
}

// identityMetrics builds the core "rbac" and "auth" scopes on p (05-metrics).
// The rbac label keys are seams.d/F4-01.md's; the auth ones are auth's
// families' plus "extension" (seams.d/F4-03.md: the open-labelled
// mediacore_auth_extension_errors_total). A core scope registers once, so the
// boot calls this once, after P5 builds the Provider.
func identityMetrics(p *metrics.Provider) (rbacScope, authScope *metrics.Scope, err error) {
	if rbacScope, err = p.Core("rbac", "owner", "scope", "result", "origin"); err != nil {
		return nil, nil, fmt.Errorf("kernel: rbac metrics scope: %w", err)
	}
	if authScope, err = p.Core("auth", "method", "outcome", "surface", "imported", "reason", "extension"); err != nil {
		return nil, nil, fmt.Errorf("kernel: auth metrics scope: %w", err)
	}
	return rbacScope, authScope, nil
}
