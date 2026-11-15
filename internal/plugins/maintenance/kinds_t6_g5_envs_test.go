// file: internal/plugins/maintenance/kinds_t6_g5_envs_test.go
// version: 1.0.0
// guid: 5d0b2c6e-8f1a-4c37-9e52-7a41c3b0d6f8
// last-edited: 2026-11-13

package maintenance

import (
	"context"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
)

// Env builders for the tests that drive a core over a fixture store of their
// own. Each builds the env HEAD's shell would for the same reporter (the
// reporter-derived base, HEAD's pool and, where the core takes one, HEAD's
// scan stand-down over the scan controller the test passes), so a test calls the
// real core and not a wrapper around it. They hold no logic of their own.

func t6g5MarkEnv(ctx context.Context, live bool, scan ScanController, rep sdk.Reporter) markMissingFilesEnv {
	return markMissingFilesEnv{
		t6g5Base: t6g5ReporterBase(ctx, rep, live),
		Items:    t6g5HeadItems(rep),
		Lease:    t6g5HeadLeaseOn(scan, rep),
	}
}

func t6g5RepointEnv(ctx context.Context, live bool, scan ScanController, rep sdk.Reporter) missingFileRepointEnv {
	return missingFileRepointEnv{
		t6g5Base: t6g5ReporterBase(ctx, rep, live),
		Items:    t6g5HeadItems(rep),
		Lease:    t6g5HeadLeaseOn(scan, rep),
	}
}

// t6g5RepointAll lists every book_file row of the store, retired included, as
// HEAD's shell hands the repoint core.
func t6g5RepointAll(store repointStore) func() ([]database.BookFileCore, error) {
	return func() ([]database.BookFileCore, error) { return database.AllBookFilesCoreIncludingRetired(store) }
}

func t6g5MergeEnv(ctx context.Context, live bool, rep sdk.Reporter) mergeSamePathDupesEnv {
	return mergeSamePathDupesEnv{t6g5Base: t6g5ReporterBase(ctx, rep, live), Items: t6g5HeadItems(rep)}
}

func t6g5ReapEnv(ctx context.Context, live bool, rep sdk.Reporter) metadataCacheReapEnv {
	return metadataCacheReapEnv{t6g5Base: t6g5ReporterBase(ctx, rep, live), Items: t6g5HeadItems(rep)}
}

func t6g5AuditEnv(ctx context.Context, rep sdk.Reporter) missingFileAuditEnv {
	return missingFileAuditEnv{t6g5Base: t6g5ReporterBase(ctx, rep, false), Items: t6g5HeadItems(rep)}
}

func t6g5RepairEnv(ctx context.Context, rep sdk.Reporter) missingFileRepairEnv {
	return missingFileRepairEnv{t6g5Base: t6g5ReporterBase(ctx, rep, false), Items: t6g5HeadItems(rep)}
}

// t6g5RgReporter is the regroup core's reporter over an sdk.Reporter, as HEAD's
// shell builds it.
func t6g5RgReporter(ctx context.Context, rep sdk.Reporter) regroupReporter {
	return regroupEnv{t6g5Base: t6g5ReporterBase(ctx, rep, true)}.reporter()
}
