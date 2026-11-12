// file: core/kernel/audit.go
// version: 1.0.2
// guid: e2c47a96-3b18-4f5d-a07e-8d1b6c9f3a24
// last-edited: 2026-11-12

package kernel

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/falkcorp/mediacore/audit"
	"github.com/falkcorp/mediacore/metrics"
	"github.com/falkcorp/mediacore/storage"
)

// auditOwnerID is core/audit's owner in the storage claim ledger: the owner of
// the c/audit/ keyspace (05-rbac §7.2, 05-storage §5S.3.1).
const auditOwnerID storage.Owner = "core/audit"

// auditSeqName is the name of the audit log's sequence in its keyspace.
const auditSeqName = "seq"

// This file is the kernel's handling of the audit store (05-rbac §5 item 7).
// It is dark: nothing in boot.go, kernel.go, storage.go or swap.go calls it
// yet, so a boot is exactly what it was. F2-16 wires four calls, in the
// order the boot phases give them:
//
//  1. storageOwners appends auditOwnerSpec() (P2 Open and P4a Prepare read the
//     owners, so the keyspace exists once Prepare returns);
//  2. buildAudit(ctx, db, log, now) after P4a, with the *storage.DB Prepare
//     returned; the store is a Kernel field then (the functions below take and
//     return it explicitly so this file adds no field to Kernel);
//  3. auditMetrics(p, s) after P5 (it returns the scope), because the metrics Provider is built at P5,
//     after P4b, so the store cannot take its scope at construction;
//  4. startAudit at Run's step 7 and stopAudit in the shutdown sequence, before
//     the storage handle closes (the writer drains into it).
//
// core.audit.prune is not registered here and no /api/core/v1/audit route
// exists: the tasks flip and F4 add them.

// auditOwnerSpec is the storage claim of core/audit: the c/audit/ keyspace and
// nothing else (no claims, no migrations; the log's rows are written by the
// store). Registering a new owner takes a one-time write in the claim ledger
// at P4b (storage's own bookkeeping): that is F2-16's change and it belongs to
// the flip that appends this spec to storageOwners.
func auditOwnerSpec() storage.OwnerSpec {
	return storage.OwnerSpec{Owner: auditOwnerID, Spec: storage.Spec{Keyspace: true}, Enabled: true}
}

// buildAudit makes the audit store over db's c/audit/ keyspace, after P4a
// (db exists from Prepare on). It writes nothing: the first record takes the
// first sequence lease. A missing keyspace means auditOwnerSpec was not among
// the owners the store opened with, which is F2-16's wiring and is reported by
// name.
func buildAudit(ctx context.Context, db *storage.DB, log *slog.Logger, now func() time.Time) (*audit.Store, error) {
	if db == nil {
		return nil, errors.New("kernel: audit: no storage handle (build the audit store after P4a)")
	}
	ks, ok := db.Keyspace(auditOwnerID)
	if !ok {
		return nil, fmt.Errorf("kernel: audit: owner %s has no keyspace: append auditOwnerSpec() to storageOwners (F2-16)", auditOwnerID)
	}
	s, err := audit.New(ctx, audit.Options{}, audit.Deps{Keyspace: ks, Seq: ks.Sequence(auditSeqName), Log: log, Now: now})
	if err != nil {
		return nil, fmt.Errorf("kernel: audit store: %w", err)
	}
	return s, nil
}

// OpenAudit builds the audit store over db's core/audit keyspace, the way a
// boot does (buildAudit), for a caller that holds a *storage.DB and no boot:
// the offline quarantine purge, which records its audit event in the purge's
// own batch (PurgeQuarantine), and a test reading back the events an offline
// command recorded. It reads the system clock; a nil log discards. It exists so
// such a caller does not restate the keyspace's owner or its sequence's name
// (auditSeqName), which are this file's to define. The store it returns has no
// async writer running, so there is nothing to stop.
func OpenAudit(ctx context.Context, db *storage.DB, log *slog.Logger) (*audit.Store, error) {
	return buildAudit(ctx, db, log, time.Now)
}

// auditMetrics registers the store's instruments on the core "audit" scope
// (05-rbac §11) and returns the scope. It runs after P5, when the Provider
// exists; a core scope registers once, so it is called once.
func auditMetrics(p *metrics.Provider, s *audit.Store) (*metrics.Scope, error) {
	sc, err := p.Core("audit", "namespace", "outcome", "path")
	if err != nil {
		return nil, fmt.Errorf("kernel: audit metrics scope: %w", err)
	}
	if err := s.Instrument(sc); err != nil {
		return nil, fmt.Errorf("kernel: audit metrics: %w", err)
	}
	return sc, nil
}

// startAudit launches the store's async writer. ctx is the context the writer
// runs under, and its end stops the writer as stopAudit does; the kernel
// passes one that Run's cancellation does not reach (startCoreServices), so
// the writer outlives the listener's and the executor's drain.
func startAudit(ctx context.Context, s *audit.Store) error {
	if err := s.Start(ctx); err != nil {
		return fmt.Errorf("kernel: audit writer: %w", err)
	}
	return nil
}

// stopAudit ends the writer: the queue is drained (at most 5 s) and what is
// left is counted as dropped, so it runs while the storage handle is open.
func stopAudit(ctx context.Context, s *audit.Store) error {
	if err := s.Stop(ctx); err != nil {
		return fmt.Errorf("kernel: audit writer stop: %w", err)
	}
	return nil
}
