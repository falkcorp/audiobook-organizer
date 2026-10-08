// file: web/src/components/review/MetadataPanel.tsx
// version: 1.9.1
// guid: 3f9a2c07-5b41-4e86-9d02-7c1e8b503a64
// last-edited: 2026-10-07
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

import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { Alert, Box, Button } from '@mui/material';

import * as api from '../../services/api';
import type { Book, MetadataCandidate } from '../../services/api';
import { MetadataSearchDialog } from '../audiobooks/MetadataSearchDialog';
import type { CandidatesContext } from './spine/CandidatesCard';
import {
  CANDIDATE_APPLY_CONCURRENCY,
  CandidateLoader,
  applyCandidateToBook,
  createLimiter,
} from './spine/candidateLoader';
import { QueueRail } from './QueueRail';
import { CompareSpine, type SpineViewMode } from './spine/CompareSpine';
import { ActionBar } from './ActionBar';
import { SelectionBar } from './SelectionBar';
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

/** How long MetadataPanel waits for more apply completions before reloading. */
const APPLIED_REFRESH_DEBOUNCE_MS = 1500;

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

  // Background applies finish one by one while the reviewer keeps working.
  // Each completion asks for a lane refresh, and a refresh reloads the rail;
  // coalesce completions that land close together into one reload so the
  // list is not yanked once per book.
  const refreshTimer = useRef<ReturnType<typeof setTimeout> | null>(null);
  const { refresh } = metadata;
  const refreshSoon = useCallback(() => {
    if (refreshTimer.current) clearTimeout(refreshTimer.current);
    refreshTimer.current = setTimeout(() => {
      refreshTimer.current = null;
      refresh();
    }, APPLIED_REFRESH_DEBOUNCE_MS);
  }, [refresh]);
  useEffect(
    () => () => {
      if (refreshTimer.current) clearTimeout(refreshTimer.current);
    },
    []
  );

  // The candidates view. One loader for the panel (its cap of 4 searches in
  // flight is per page, not per card) and one apply limiter (4 background
  // applies at a time). Both live as long as the panel, so a book's answer is
  // still there when the reviewer scrolls back to it.
  const candidateLoader = useMemo(
    () =>
      new CandidateLoader((bookId, q) =>
        api
          .searchMetadataForBook(bookId, q.title, q.author || undefined)
          .then((resp) => resp.results ?? [])
      ),
    []
  );
  const applyLimiter = useMemo(() => createLimiter(CANDIDATE_APPLY_CONCURRENCY), []);
  // Read at click time, so the context below stays stable (it is a prop of
  // every memoized card) while the toggle still decides each apply.
  const applyModeRef = useRef(metadata.bulkApplyMode);
  useEffect(() => {
    applyModeRef.current = metadata.bulkApplyMode;
  }, [metadata.bulkApplyMode]);

  // One apply per book at a time: two queued applies of the same book refuse
  // each other (StagedPick), so a second click waits for the first to settle.
  const applyingBooks = useRef(new Set<string>());
  const applyCandidate = useCallback(
    async (bookId: string, candidate: MetadataCandidate) => {
      if (applyingBooks.current.has(bookId)) {
        toast('An apply for this book is already running; wait for it to finish.', 'warning');
        return;
      }
      applyingBooks.current.add(bookId);
      try {
        await applyLimiter(() =>
          applyCandidateToBook({
            bookId,
            candidate,
            mode: applyModeRef.current,
            toast,
            onApplied: refreshSoon,
          })
        );
      } finally {
        applyingBooks.current.delete(bookId);
      }
    },
    [applyLimiter, toast, refreshSoon]
  );
  const candidatesCtx: CandidatesContext = useMemo(
    () => ({ loader: candidateLoader, apply: applyCandidate }),
    [candidateLoader, applyCandidate]
  );

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
          titleFilterError={metadata.titleFilterError}
          reviewLevel={metadata.reviewLevel}
          setReviewLevel={metadata.setReviewLevel}
          levelCustomised={metadata.levelCustomised}
          runtimeHiddenCount={metadata.runtimeHiddenCount}
          chipFilter={metadata.chipFilter}
          onToggleChip={metadata.toggleChipFilter}
          onClearChip={metadata.clearChipFilter}
          unreviewableLoading={metadata.unreviewableLoading}
          unreviewableError={metadata.unreviewableError}
          page={metadata.page}
          totalPages={metadata.totalPages}
          pageSize={metadata.pageSize}
          setPage={metadata.setPage}
          setPageSize={metadata.setPageSize}
          filteredCount={metadata.filteredResults.length}
          rowState={metadata.spineCtx.rowState}
          isSelected={metadata.spineCtx.isSelected}
          onToggleSelect={metadata.spineCtx.onToggleSelect}
          onSelectPage={metadata.setSelection}
          onSelectAllMatching={metadata.selectAllMatching}
          onClearSelection={metadata.clearSelection}
          allMatchingSelected={metadata.allMatchingSelected}
          selectedCount={metadata.selectedIds.size}
          onRefresh={metadata.refresh}
          refetching={metadata.refetching}
          // Gated on the server's count, the same number the chip shows: the
          // set is resolved on the server ({stale: true}), so there is no
          // client-side list to check for emptiness.
          onRefetchStale={metadata.summary.stale > 0 ? onRefetchStale : undefined}
          onRefetchRow={(bookId) => {
            // One row goes straight through. The confirm in the shell exists
            // because a bulk refetch is thousands of calls to external
            // metadata providers; a single book is not worth a dialog.
            void metadata.refetchBooks([bookId]);
          }}
          onSearchRow={openSearch}
        />

        <Box sx={{ minWidth: 0, overflowY: 'auto' }}>
          {/*
            Always on screen: every view mode, chip or no chip, zero rows or
            thousands. Sticky inside this scroll container so it stays put while
            the spine scrolls; the opaque background keeps rows from showing
            through it.
          */}
          <SelectionBar
            testIdPrefix="main-"
            pageIds={metadata.pageResults.map((r) => r.book.id)}
            matchingCount={metadata.filteredResults.length}
            selectedCount={metadata.selectedIds.size}
            allMatchingSelected={metadata.allMatchingSelected}
            isSelected={metadata.spineCtx.isSelected}
            onSelectPage={metadata.setSelection}
            onSelectAllMatching={metadata.selectAllMatching}
            onClearSelection={metadata.clearSelection}
            disabled={metadata.loading}
            sx={{
              position: 'sticky',
              top: 0,
              zIndex: 2,
              bgcolor: 'background.paper',
              borderBottom: 1,
              borderColor: 'divider',
              px: 2,
              py: 1,
            }}
          />
          {/*
            Mirrors the Alert DupesPanel and RegroupPanel already render. Retry
            is wired to the lane's existing `refresh` -- the same one the rail's
            refresh button uses -- so a failed load has a way forward that does
            not require the reviewer to reload the page.
          */}
          {metadata.error && (
            <Alert
              severity="error"
              sx={{ m: 2 }}
              data-testid="metadata-error"
              action={
                <Button color="inherit" size="small" onClick={metadata.refresh}>
                  Retry
                </Button>
              }
            >
              {metadata.error}
            </Alert>
          )}
          <CompareSpine
            rows={metadata.rows}
            groups={metadata.groups}
            viewMode={viewMode}
            ctx={metadata.spineCtx}
            emptyMessage={LANES.metadata.emptyMessage}
            loading={metadata.loading}
            errored={!!metadata.error}
            candidates={candidatesCtx}
          />
        </Box>
      </Box>

      <ActionBar
        selectedIds={metadata.selectedIds}
        applicableSelectedIds={metadata.applicableSelectedIds}
        searching={metadata.searching}
        bulkProgress={metadata.bulkProgress}
        onClearSelection={metadata.clearSelection}
        onSkipSelected={metadata.skipSelected}
        onRejectSelected={() => void metadata.rejectSelected()}
        onSearchSelected={(ids) =>
          void metadata.searchAgain(ids, (message) => Promise.resolve(window.confirm(message)))
        }
        highConfidenceIds={metadata.highConfidenceIds}
        allVisiblePendingIds={metadata.allVisiblePendingIds}
        unmatchedCount={unmatchedCount}
        applying={metadata.applying}
        dispatch={metadata.dispatch}
        confirm={(message) => Promise.resolve(window.confirm(message))}
        bulkApplyMode={metadata.bulkApplyMode}
        onBulkApplyModeChange={metadata.setBulkApplyMode}
        replaceConfirmSkipped={metadata.skipReplaceConfirm}
        onResetReplaceConfirm={metadata.resetReplaceConfirm}
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
            // Fires when the background apply FINISHES, which is after the
            // dialog closed and possibly after the reviewer opened another
            // book's search -- so it must not touch searchBook (it used to
            // close the dialog here, which would now close the next book's).
            // The row's status and candidate both changed server-side; refresh
            // rather than patching one row, so the summary counts stay true.
            refreshSoon();
          }}
          toast={toast}
        />
      )}
    </>
  );
}
