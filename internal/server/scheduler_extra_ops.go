// file: internal/server/scheduler_extra_ops.go
// version: 2.1.0
// guid: f1e2d3c4-b5a6-7890-fedc-ba9876543210
// last-edited: 2026-09-07

// scheduler_extra_ops is a thin shim that wires the 13 ExtraOpsRegistrar
// methods (now living in internal/scheduler/extra_ops.go) into the server
// package's addOpRegistrar mechanism.
//
// The actual OperationDef logic has been extracted to
// internal/scheduler.ExtraOpsRegistrar as part of SERVER-THIN-RESIDUAL.

package server

import opsregistry "github.com/falkcorp/audiobook-organizer/internal/operations/registry"

// The server package's own schedulerExtraOpParams was deleted on 2026-09-07.
// It existed solely so server_lifecycle.go's v1 resume sweep could re-enqueue a
// resumed operation carrying its legacy op ID; that sweep is gone, superseded by
// Registry.resumeAfterStartup. internal/scheduler declares its own identically
// named type, which is unrelated and still in use.

func init() {
	// All 13 Register* methods have moved to internal/scheduler.ExtraOpsRegistrar
	// (SERVER-THIN-RESIDUAL). These shims keep the addOpRegistrar contract intact
	// while delegating to s.extraOpsRegistrar which is constructed in NewServer.
	addOpRegistrar(func(s *Server, reg *opsregistry.Registry) error {
		return s.extraOpsRegistrar.RegisterDedupLLMReviewOp(reg)
	})
	addOpRegistrar(func(s *Server, reg *opsregistry.Registry) error {
		return s.extraOpsRegistrar.RegisterTrashCleanupOp(reg)
	})
	addOpRegistrar(func(s *Server, reg *opsregistry.Registry) error {
		return s.extraOpsRegistrar.RegisterArchiveSweepOp(reg)
	})
	addOpRegistrar(func(s *Server, reg *opsregistry.Registry) error {
		return s.extraOpsRegistrar.RegisterMetadataUpgradeOp(reg)
	})
	addOpRegistrar(func(s *Server, reg *opsregistry.Registry) error {
		return s.extraOpsRegistrar.RegisterAuthorSplitScanOp(reg)
	})
	addOpRegistrar(func(s *Server, reg *opsregistry.Registry) error {
		return s.extraOpsRegistrar.RegisterDBOptimizeOp(reg)
	})
	addOpRegistrar(func(s *Server, reg *opsregistry.Registry) error {
		return s.extraOpsRegistrar.RegisterCleanupOldBackupsOp(reg)
	})
	addOpRegistrar(func(s *Server, reg *opsregistry.Registry) error {
		return s.extraOpsRegistrar.RegisterISBNEnrichmentOp(reg)
	})
	addOpRegistrar(func(s *Server, reg *opsregistry.Registry) error {
		return s.extraOpsRegistrar.RegisterTempFileCleanupOp(reg)
	})
	addOpRegistrar(func(s *Server, reg *opsregistry.Registry) error {
		return s.extraOpsRegistrar.RegisterPurgeDeletedOp(reg)
	})
	addOpRegistrar(func(s *Server, reg *opsregistry.Registry) error {
		return s.extraOpsRegistrar.RegisterTombstoneCleanupOp(reg)
	})
	addOpRegistrar(func(s *Server, reg *opsregistry.Registry) error {
		return s.extraOpsRegistrar.RegisterResolveProductionAuthorsOp(reg)
	})
	addOpRegistrar(func(s *Server, reg *opsregistry.Registry) error {
		return s.extraOpsRegistrar.RegisterMetadataRefreshOp(reg)
	})
}
