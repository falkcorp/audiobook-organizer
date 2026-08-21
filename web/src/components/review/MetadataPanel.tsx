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

import { useCallback, useState } from 'react';
import { Box } from '@mui/material';

import * as api from '../../services/api';
import type { Book } from '../../services/api';
import { MetadataSearchDialog } from '../audiobooks/MetadataSearchDialog';
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
  /** Surface errors and confirmations; also handed to the search dialog. */
  toast: (
    message: string,
    severity?: 'success' | 'error' | 'warning' | 'info',
    action?: { label: string; onClick: () => void }
  ) => void;
}

export function MetadataPanel({
  metadata,
  viewMode,
  unmatchedCount,
  onRefetchStale,
  toast,
}: MetadataPanelProps) {
  // The rail carries CandidateBookInfo, which is not the full Book the search
  // dialog edits, so opening the dialog needs a fetch. Held as the book itself
  // rather than an id so the dialog never renders against a half-loaded row.
  const [searchBook, setSearchBook] = useState<Book | null>(null);

  const openSearch = useCallback(
    (bookId: string) => {
      void (async () => {
        try {
          setSearchBook(await api.getBook(bookId));
        } catch (err) {
          toast(
            err instanceof Error ? err.message : 'Could not load that book',
            'error'
          );
        }
      })();
    },
    [toast]
  );

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
          onSearchRow={openSearch}
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

      {/*
        The manual-search escape hatch. Automatic fetching keys off a book's own
        tags, so it cannot rescue a book whose tags are the problem -- and the
        library has plenty: author fields holding a release-group tag, a studio
        name, or the book's own title. Those rows sit at no_match forever
        because every automatic retry asks the same wrong question. Until this
        was wired the only way to type a corrected query was a dialog on a
        different screen.
      */}
      {searchBook && (
        <MetadataSearchDialog
          open
          book={searchBook}
          onClose={() => setSearchBook(null)}
          onApplied={() => {
            setSearchBook(null);
            // The row's status and candidate both changed server-side; refresh
            // rather than patching one row, so the summary counts stay true.
            metadata.refresh();
          }}
          toast={toast}
        />
      )}
    </>
  );
}
