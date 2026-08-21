// file: web/src/components/review/MetadataPanel.tsx
// version: 1.0.0
// guid: 3f9a2c07-5b41-4e86-9d02-7c1e8b503a64
// last-edited: 2026-08-21
//
// The metadata lane's full surface: queue rail, comparison spine, action bar.
//
// This is the symmetry DupesPanel left owing. Both other lanes were extracted
// when they were ported, so the shell's lane branch was one line for regroup,
// one line for dupes, and sixty for metadata -- the oldest lane was the only
// one still assembled inside the shell. Lifting it means ReviewWorkspace owns
// lane selection and cross-lane chrome, and each lane owns its own layout.
//
// The stale-refetch CONFIRMATION stays in the shell. Refetching every stale row
// is thousands of external provider calls, and the dialog that guards it is
// cross-lane chrome sitting alongside the rescore dialog; this panel raises the
// intent and the shell decides how to ask. A single-row refetch needs no dialog
// and is handled here.

import { Box } from '@mui/material';

import { QueueRail } from './QueueRail';
import { CompareSpine, type SpineViewMode } from './spine/CompareSpine';
import { ActionBar } from './ActionBar';
import { LANES } from './lanes';
import type { MetadataLane } from './lanes/useMetadataLane';

export interface MetadataPanelProps {
  metadata: MetadataLane;
  viewMode: SpineViewMode;
  /** Rows with no candidate at all; shown in the action bar's summary. */
  unmatchedCount: number;
  /**
   * Raised when the reviewer asks to refetch every stale row. Undefined when
   * nothing is stale, which is what hides the control -- the rail treats an
   * absent handler as "not offered" rather than rendering a dead button.
   */
  onRefetchStale?: () => void;
}

export function MetadataPanel({
  metadata,
  viewMode,
  unmatchedCount,
  onRefetchStale,
}: MetadataPanelProps) {
  return (
    <>
      <Box
        sx={{
          flex: 1,
          minHeight: 0,
          display: 'grid',
          gridTemplateColumns: { xs: '1fr', md: '320px 1fr' },
        }}
      >
        <QueueRail
          loading={metadata.loading}
          rows={metadata.pageResults}
          summary={metadata.summary}
          sourceCounts={metadata.sourceCounts}
          filters={metadata.filters}
          setFilters={metadata.setFilters}
          strictPreset={metadata.strictPreset}
          setStrictPreset={metadata.setStrictPreset}
          page={metadata.page}
          totalPages={metadata.totalPages}
          pageSize={metadata.pageSize}
          setPage={metadata.setPage}
          setPageSize={metadata.setPageSize}
          filteredCount={metadata.filteredResults.length}
          rowState={metadata.spineCtx.rowState}
          isSelected={metadata.spineCtx.isSelected}
          onToggleSelect={metadata.spineCtx.onToggleSelect}
          onRefresh={metadata.refresh}
          refetching={metadata.refetching}
          onRefetchStale={metadata.staleIds.length ? onRefetchStale : undefined}
          onRefetchRow={(bookId) => {
            // One row goes straight through. The confirm in the shell exists
            // because a bulk refetch is thousands of calls to external
            // metadata providers; a single book is not worth a dialog.
            void metadata.refetchBooks([bookId]);
          }}
        />

        <Box sx={{ minWidth: 0, overflowY: 'auto' }}>
          <CompareSpine
            rows={metadata.rows}
            groups={metadata.groups}
            viewMode={viewMode}
            ctx={metadata.spineCtx}
            emptyMessage={LANES.metadata.emptyMessage}
          />
        </Box>
      </Box>

      <ActionBar
        selectedIds={metadata.selectedIds}
        highConfidenceIds={metadata.highConfidenceIds}
        allVisiblePendingIds={metadata.allVisiblePendingIds}
        unmatchedCount={unmatchedCount}
        applying={metadata.applying}
        dispatch={metadata.dispatch}
        confirm={(message) => Promise.resolve(window.confirm(message))}
      />
    </>
  );
}
