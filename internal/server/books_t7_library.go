// file: internal/server/books_t7_library.go
// version: 1.0.0
// guid: a3ced322-1920-49c4-bdb7-bbd69ed85636
// last-edited: 2026-11-28

package server

// Group A of the t7 host adapter (see books_t7_adapter.go): the methods the
// library kinds' cores call, and only those.
//
//	library.scan, library.scan-full  PerformScan
//	library.organize                 PerformOrganize
//	library.size-refresh             GetAllImportPaths
//	reconcile.scan                   RunReconcileScan
//
// Every method refuses by name until it is built. A built one reuses the
// server's head* method for the same call and refuses by name when the field
// it reads on *Server is unset.

import (
	"context"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	"github.com/falkcorp/audiobook-organizer/internal/operations"
	"github.com/falkcorp/audiobook-organizer/internal/reconcile"
	"github.com/falkcorp/audiobook-organizer/internal/scanner"
)

func (a *booksT7Store) GetAllImportPaths() ([]database.ImportPath, error) {
	return nil, booksT7NotBuilt("GetAllImportPaths")
}

func (a *booksT7Store) RunReconcileScan(_ context.Context, _ reconcile.SaveResultFunc, _ operations.ProgressReporter, _ reconcile.ScanHooks) error {
	return booksT7NotBuilt("RunReconcileScan")
}

func (a *booksT7Writer) PerformOrganize(_ context.Context, _ *OrganizeRequest, _ logger.Logger) error {
	return booksT7NotBuilt("PerformOrganize")
}

func (a *booksT7Writer) PerformScan(_ context.Context, _ *scanner.ScanRequest, _ logger.Logger) error {
	return booksT7NotBuilt("PerformScan")
}
