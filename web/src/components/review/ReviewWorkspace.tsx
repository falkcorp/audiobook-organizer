// file: web/src/components/review/ReviewWorkspace.tsx
// version: 1.5.0
// guid: 8e0b4d59-1c76-42a3-95f8-7d2a6b3e0c81
// last-edited: 2026-08-20
//
// The unified review workspace: one screen for dedup, metadata apply, and the
// review queue.
//
// WHICH LANE OPENS
//
// The URL decides, then `metadata` as the fallback. `LANE_ORDER` opens with
// `dupes` because the switcher lists widest-scope work first, but display order
// is not a default: metadata is the lane with something to show on a library
// nobody has deep-linked into.
//
// This used to be a bare `useState('metadata')`, which quietly broke the dupes
// lane's own entry point. A `?book=` link arrived, the workspace opened on
// metadata, and `useDupesLane(..., lane === 'dupes', ...)` therefore stayed
// inactive -- so the server-side entity filter the link exists to trigger never
// ran. The lane tests all clicked their way in and never saw it.
//
// Seeded once at mount, deliberately NOT mirrored back. A tab click that wrote
// `?lane=` would make lane state derive from a param the click itself sets, and
// the lane's gated fetch would fire twice per switch -- the exact defect just
// removed from DupesPanel. `?lane=` is how you ARRIVE at a lane, not a running
// record of the one you are on.
//
// NO LEGACY TOGGLE
//
// PLAN.md is explicit that no `review_show_legacy` gate is built. One user, no
// migration window: a compatibility flag would be pure cost with nobody to
// protect, and shipping both surfaces indefinitely recreates the fragmentation
// this project exists to remove. The safety net is git until Phase 7 deletes the
// old surfaces, which is gated on docs/port-inventory.md.

import { useMemo, useState } from 'react';
import { useSearchParams } from 'react-router-dom';
import {
  Alert,
  AlertTitle,
  Box,
  Button,
  Dialog,
  DialogActions,
  DialogContent,
  DialogContentText,
  DialogTitle,
  Link,
  Tab,
  Tabs,
  ToggleButton,
  ToggleButtonGroup,
  Typography,
} from '@mui/material';
import ViewListIcon from '@mui/icons-material/ViewList';
import ViewColumnIcon from '@mui/icons-material/ViewColumn';
import AutoAwesomeMosaicIcon from '@mui/icons-material/AutoAwesomeMosaic';
import * as api from '../../services/api';
import type { DedupBand } from '../../services/api';
import { useToast } from '../toast/ToastProvider';
import { CommandBar, type CommandMenu } from './CommandBar';
import { QueueRail } from './QueueRail';
import { ActionBar } from './ActionBar';
import { CompareSpine, type SpineViewMode } from './spine/CompareSpine';
import { DupesPanel } from './DupesPanel';
import { RegroupPanel } from './RegroupPanel';
import { useDupesLane } from './lanes/useDupesLane';
import { useMetadataLane } from './lanes/useMetadataLane';
import { useRegroupLane } from './lanes/useRegroupLane';
import { LANES, LANE_ORDER } from './lanes';
import type { ReviewLane } from './reviewActions';

/**
 * Where an unported lane's surface still lives, so the panel can point at it.
 *
 * Empty: all three lanes render here now. Kept rather than deleted because it is
 * the mechanism a FOURTH lane would use on the way in, and re-deriving it costs
 * more than the four lines it occupies.
 */
const UNPORTED: Partial<Record<ReviewLane, { where: string; href: string }>> = {};

/**
 * Builds a CSV from the rows currently loaded.
 *
 * There is no server-side export route, and rather than render PLAN.md's
 * "Export CSV" as a dead item this builds the file from what the client already
 * holds. The scope is stated in the toast, because "export" invites the reading
 * "everything" and this is the filtered set.
 */
function exportRowsAsCsv(
  rows: Array<{
    book: { id: string; title?: string };
    candidate?: { title?: string; author?: string; source?: string; score?: number };
    status: string;
  }>
): number {
  const esc = (v: unknown) => `"${String(v ?? '').replace(/"/g, '""')}"`;
  const lines = [
    ['book_id', 'book_title', 'status', 'candidate_title', 'candidate_author', 'source', 'score']
      .map(esc)
      .join(','),
    ...rows.map((r) =>
      [
        r.book.id,
        r.book.title,
        r.status,
        r.candidate?.title,
        r.candidate?.author,
        r.candidate?.source,
        r.candidate?.score,
      ]
        .map(esc)
        .join(',')
    ),
  ];
  const blob = new Blob([lines.join('\n')], { type: 'text/csv' });
  const url = URL.createObjectURL(blob);
  const a = document.createElement('a');
  a.href = url;
  a.download = 'review-export.csv';
  a.click();
  URL.revokeObjectURL(url);
  return rows.length;
}

/**
 * The lane to open on, read from the URL once at mount.
 *
 * `?lane=` is explicit and wins. `?book=` and `?band=` are the dupes lane's own
 * filters, so a link carrying either is a link to that lane even when it does
 * not say so -- which is what makes the deep link from a book's status alert
 * work without every producer having to spell the lane out.
 *
 * An unrecognised `?lane=` falls back rather than throwing: a stale bookmark
 * should land somewhere useful, not on a blank screen.
 */
function initialLaneFrom(params: URLSearchParams): ReviewLane {
  const named = params.get('lane');
  if (named && (LANE_ORDER as string[]).includes(named)) return named as ReviewLane;
  if (params.get('book') || params.get('band')) return 'dupes';
  return 'metadata';
}

export function ReviewWorkspace() {
  const { toast } = useToast();
  const [searchParams] = useSearchParams();
  // Seeded from the URL, not synced to it -- see the note at the top of the file.
  const [lane, setLane] = useState<ReviewLane>(() => initialLaneFrom(searchParams));
  const [viewMode, setViewMode] = useState<SpineViewMode>('compact');

  const metadata = useMetadataLane(toast, lane === 'metadata');
  // Both lanes fetch only while they are the visible one, so switching lanes
  // does not leave three requests in flight or a stray window key listener.
  // Read here, not in DupesPanel, so the hook has the URL-owned filters on its
  // FIRST render. Passing them down beats letting the panel sync them up: an
  // effect-based sync made every ?book= deep link fetch the whole unfiltered
  // set before correcting itself.
  const dupesUrlFilters = useMemo(
    () => ({
      band: searchParams.get('band') as DedupBand | null,
      entityId: searchParams.get('book'),
    }),
    [searchParams],
  );
  const dupes = useDupesLane(toast, lane === 'dupes', dupesUrlFilters);
  // Expansion is a view concern and the two lanes key it on different id types,
  // so it is not shared state.
  const [dupesExpandedId, setDupesExpandedId] = useState<number | null>(null);
  // Rescore-with-apply asks first. See the command pair below.
  const [rescoreConfirmOpen, setRescoreConfirmOpen] = useState(false);
  const [confirmRefetchStale, setConfirmRefetchStale] = useState(false);
  const regroup = useRegroupLane(toast, lane === 'regroup');

  const unmatchedCount = useMemo(
    () => metadata.results.filter((r) => r.status === 'no_match' || r.status === 'error').length,
    [metadata.results]
  );

  // Every command starts a background job and reports through the bell, so the
  // handler shape is uniform: fire, toast, let OperationsIndicator own progress.
  const startJob = (label: string, fn: () => Promise<unknown>) => async () => {
    try {
      await fn();
      toast(`${label} started — watch the bell for progress.`, 'success');
    } catch {
      toast(`Failed to start ${label.toLowerCase()}.`, 'error');
    }
  };

  const menus: CommandMenu[] = useMemo(
    () => [
      {
        id: 'dedup',
        label: 'Dedup',
        commands: [
          {
            id: 'find-duplicates',
            label: 'Find duplicates',
            scope: 'library',
            run: startJob('Duplicate scan', api.triggerDedupScan),
          },
          // Two commands, because there are two operations and they were being
          // reported as one. This menu item passed `apply=false` while calling
          // itself plain "Rescore", so it answered "Rescore started" and then
          // wrote nothing -- a dry run announced as the real thing.
          {
            id: 'rescore-dry-run',
            label: 'Rescore (dry run)',
            scope: 'library',
            run: startJob('Rescore dry run', () => api.rescoreDedupCandidates(false)),
          },
          {
            id: 'rescore-apply',
            label: 'Rescore and apply…',
            scope: 'library',
            // The surface this replaces put Apply behind a dialog, in warning
            // colour, next to a Dry Run button. Reachable in one click from a
            // menu would be a downgrade in safety, not a port, so the confirm
            // step carries over even though nothing else in this menu confirms.
            run: () => setRescoreConfirmOpen(true),
          },
          {
            id: 'full-rescan',
            label: 'Force full rescan…',
            scope: 'library',
            run: startJob('Full rescan', api.scanBookDuplicates),
          },
          {
            id: 'embeddings',
            label: 'Embeddings',
            scope: 'library',
            startsGroup: true,
            run: startJob('Embedding scan', api.triggerEmbedScan),
          },
          {
            id: 'acoustic',
            label: 'Acoustic',
            scope: 'library',
            run: startJob('AcoustID scan', api.triggerDedupAcoustID),
          },
          {
            id: 'ai-review',
            label: 'AI review',
            scope: 'library',
            run: startJob('AI review', api.triggerDedupLLM),
          },
          {
            id: 'reconcile',
            label: 'Reconcile…',
            scope: 'library',
            startsGroup: true,
            run: startJob('Reconcile scan', api.startReconcileScan),
          },
          {
            id: 'manage-labels',
            label: 'Manage labels…',
            scope: 'view',
            run: () => {
              window.location.assign('/dedup/labels');
            },
          },
        ],
      },
      {
        id: 'metadata',
        label: 'Metadata',
        commands: [
          {
            id: 'search-providers',
            label: 'Search providers…',
            scope: 'library',
            run: startJob('Provider search', () => api.batchFetchCandidates({})),
          },
          {
            id: 'bulk-search-selected',
            label: 'Bulk search selected…',
            scope: 'selection',
            disabledReason:
              metadata.selectedIds.size === 0 ? 'Select one or more books first.' : undefined,
            run: startJob('Search for selected', () =>
              api.batchFetchCandidates({ book_ids: [...metadata.selectedIds] })
            ),
          },
          {
            id: 'apply-selected-fields',
            label: 'Apply selected fields',
            scope: 'selection',
            startsGroup: true,
            disabledReason:
              metadata.selectedIds.size === 0 ? 'Select one or more books first.' : undefined,
            run: () =>
              metadata.dispatch({
                lane: 'metadata',
                type: 'applySelected',
                ids: [...metadata.selectedIds],
              }),
          },
          {
            id: 'apply-all-fields',
            label: 'Apply all fields',
            scope: 'selection',
            disabledReason:
              metadata.allVisiblePendingIds.length === 0
                ? 'No undecided matched rows on this page.'
                : undefined,
            run: () =>
              metadata.dispatch({
                lane: 'metadata',
                type: 'applySelected',
                ids: metadata.allVisiblePendingIds,
              }),
          },
          {
            id: 'write-back',
            label: 'Write back to files…',
            scope: 'selection',
            startsGroup: true,
            disabledReason:
              metadata.selectedIds.size === 0 ? 'Select one or more books first.' : undefined,
            run: startJob('Tag write-back', () =>
              api.batchWriteBackMetadata([...metadata.selectedIds])
            ),
          },
        ],
      },
      {
        id: 'queue',
        label: 'Queue',
        commands: [
          {
            id: 'queue-approve',
            label: 'Approve',
            scope: 'selection',
            disabledReason: 'The review-queue lane is not ported yet — use the Review page.',
            run: () => {},
          },
          {
            id: 'queue-reject',
            label: 'Reject',
            scope: 'selection',
            disabledReason: 'The review-queue lane is not ported yet — use the Review page.',
            run: () => {},
          },
          {
            id: 'queue-bulk',
            label: 'Bulk decide…',
            scope: 'library',
            disabledReason: 'The review-queue lane is not ported yet — use the Review page.',
            run: () => {},
          },
          {
            id: 'export-csv',
            label: 'Export CSV',
            scope: 'view',
            startsGroup: true,
            run: () => {
              const n = exportRowsAsCsv(metadata.filteredResults);
              // Says WHAT was exported: "export" reads as "everything", and this
              // is the filtered set.
              toast(`Exported ${n.toLocaleString()} filtered row(s) to CSV.`, 'success');
            },
          },
          {
            id: 'purge-stale',
            label: 'Purge stale',
            scope: 'library',
            run: startJob('Stale-candidate purge', api.purgeStaleCandidates),
          },
        ],
      },
    ],
    // startJob closes over `toast` only, which is stable from the provider.
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [
      metadata.selectedIds,
      metadata.allVisiblePendingIds,
      metadata.filteredResults,
      metadata.dispatch,
      toast,
    ]
  );

  const unported = UNPORTED[lane];

  return (
    <Box
      data-testid="review-workspace"
      sx={{ display: 'flex', flexDirection: 'column', height: '100%', minHeight: 0 }}
    >
      {/* Header: lane switcher + commands + view mode. */}
      <Box
        sx={{
          display: 'flex',
          alignItems: 'center',
          gap: 2,
          px: 2,
          borderBottom: 1,
          borderColor: 'divider',
          flexWrap: 'wrap',
        }}
      >
        <Tabs
          value={lane}
          onChange={(_, v: ReviewLane) => setLane(v)}
          aria-label="Review lane"
          sx={{ minHeight: 48 }}
        >
          {LANE_ORDER.map((id) => (
            <Tab key={id} value={id} label={LANES[id].label} data-testid={`lane-tab-${id}`} />
          ))}
        </Tabs>

        <CommandBar menus={menus} />

        <ToggleButtonGroup
          size="small"
          exclusive
          value={viewMode}
          onChange={(_, v: SpineViewMode | null) => v && setViewMode(v)}
          aria-label="Comparison layout"
          sx={{ ml: 'auto' }}
        >
          <ToggleButton value="compact" aria-label="Compact rows">
            <ViewListIcon fontSize="small" />
          </ToggleButton>
          <ToggleButton value="two-column" aria-label="Two columns">
            <ViewColumnIcon fontSize="small" />
          </ToggleButton>
          {/* The one addition PLAN.md authorises: collapse on the spine's own
              width rather than the window's. */}
          <ToggleButton value="auto" aria-label="Auto layout">
            <AutoAwesomeMosaicIcon fontSize="small" />
          </ToggleButton>
        </ToggleButtonGroup>
      </Box>

      {lane === 'regroup' ? (
        <RegroupPanel regroup={regroup} />
      ) : lane === 'dupes' ? (
        <DupesPanel
          dupes={dupes}
          viewMode={viewMode}
          expandedId={dupesExpandedId}
          onToggleExpand={(id) => setDupesExpandedId((cur) => (cur === id ? null : id))}
        />
      ) : unported ? (
        <Box sx={{ p: 3 }} data-testid={`lane-unported-${lane}`}>
          <Alert severity="info">
            <AlertTitle>{LANES[lane].label} is not in the workspace yet</AlertTitle>
            <Typography variant="body2">
              The comparison spine renders metadata candidates today; this lane still lives on{' '}
              <Link href={unported.href}>{unported.where}</Link>. It moves here before the old
              surfaces are deleted.
            </Typography>
          </Alert>
        </Box>
      ) : (
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
              onRefetchStale={
                metadata.staleIds.length ? () => setConfirmRefetchStale(true) : undefined
              }
              onRefetchRow={(bookId) => {
                // One row goes straight through. The confirm below exists
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
      )}

      {/* Rescore-and-apply confirmation. */}
      <Dialog open={rescoreConfirmOpen} onClose={() => setRescoreConfirmOpen(false)}>
        <DialogTitle>Rescore and apply?</DialogTitle>
        <DialogContent>
          <DialogContentText>
            Re-runs the unified scoring formula over stored signal sets for every pending
            candidate and writes the new scores. No re-embedding or re-collection happens —
            only candidates that already have stored signals are updated, and older rows are
            counted as skipped.
          </DialogContentText>
        </DialogContent>
        <DialogActions>
          <Button onClick={() => setRescoreConfirmOpen(false)}>Cancel</Button>
          <Button
            variant="contained"
            color="warning"
            data-testid="rescore-apply-confirm"
            onClick={() => {
              setRescoreConfirmOpen(false);
              void startJob('Rescore', () => api.rescoreDedupCandidates(true))();
            }}
          >
            Rescore and apply
          </Button>
        </DialogActions>
      </Dialog>

      {/*
        Refetching every stale row is one click but thousands of calls to
        external metadata providers -- on production 5,771 of 5,774 reviewable
        rows are stale. The count goes in the dialog because "refetch stale"
        reads as a tidy-up until you see the number.
      */}
      <Dialog open={confirmRefetchStale} onClose={() => setConfirmRefetchStale(false)}>
        <DialogTitle>Refetch {metadata.staleIds.length.toLocaleString()} stale books?</DialogTitle>
        <DialogContent>
          <DialogContentText>
            Every one of these was last fetched more than 30 days ago. Refetching queries the
            metadata providers once per book and replaces each cached candidate list, so any
            review decision you have not yet applied to these rows will be re-derived from the
            new results.
          </DialogContentText>
          <DialogContentText sx={{ mt: 2 }}>
            This runs as a background operation — you can keep reviewing while it works, and
            progress shows in the operations list.
          </DialogContentText>
        </DialogContent>
        <DialogActions>
          <Button onClick={() => setConfirmRefetchStale(false)}>Cancel</Button>
          <Button
            variant="contained"
            color="warning"
            data-testid="refetch-stale-confirm"
            onClick={() => {
              setConfirmRefetchStale(false);
              void metadata.refetchBooks(metadata.staleIds);
            }}
          >
            Refetch {metadata.staleIds.length.toLocaleString()}
          </Button>
        </DialogActions>
      </Dialog>

      {/* Cover lightbox. */}
      <Dialog
        open={Boolean(metadata.previewCover)}
        onClose={() => metadata.setPreviewCover(null)}
        maxWidth="sm"
      >
        <DialogContent sx={{ p: 0 }}>
          {metadata.previewCover && (
            <Box
              component="img"
              src={metadata.previewCover}
              alt="Cover preview"
              sx={{ display: 'block', maxWidth: '100%' }}
            />
          )}
        </DialogContent>
      </Dialog>
    </Box>
  );
}
