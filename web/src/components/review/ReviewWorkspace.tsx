// file: web/src/components/review/ReviewWorkspace.tsx
// version: 1.2.0
// guid: 8e0b4d59-1c76-42a3-95f8-7d2a6b3e0c81
// last-edited: 2026-08-20
//
// The unified review workspace: one screen for dedup, metadata apply, and the
// review queue.
//
// LANE DEFAULT
//
// `LANE_ORDER` opens with `dupes` because the lane switcher lists widest-scope
// work first, but the spine's renderers are metadata-shaped and the dedup lane
// is not ported yet. Defaulting to `LANE_ORDER[0]` would therefore land `/review`
// on a lane that cannot render anything -- a blank screen on the first paint of
// the feature. So the default is `metadata`, and the two unported lanes render
// an explicit panel saying where their surface still lives rather than an empty
// spine. `lanes/index.ts` already makes the argument: a fallback lane is a blank
// screen with no explanation.
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
  Dialog,
  DialogContent,
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
import { useDupesLane } from './lanes/useDupesLane';
import { useMetadataLane } from './lanes/useMetadataLane';
import { LANES, LANE_ORDER } from './lanes';
import type { ReviewLane } from './reviewActions';

/** Where each unported lane's surface still lives, so the panel can point at it. */
const UNPORTED: Partial<Record<ReviewLane, { where: string; href: string }>> = {
  regroup: { where: 'the Dedup page', href: '/dedup' },
};

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

export function ReviewWorkspace() {
  const { toast } = useToast();
  // NOT LANE_ORDER[0] -- see the note at the top of the file.
  const [lane, setLane] = useState<ReviewLane>('metadata');
  const [viewMode, setViewMode] = useState<SpineViewMode>('compact');

  const metadata = useMetadataLane(toast, lane === 'metadata');
  // Both lanes fetch only while they are the visible one, so switching lanes
  // does not leave three requests in flight or a stray window key listener.
  // Read here, not in DupesPanel, so the hook has the URL-owned filters on its
  // FIRST render. Passing them down beats letting the panel sync them up: an
  // effect-based sync made every ?book= deep link fetch the whole unfiltered
  // set before correcting itself.
  const [dupesSearchParams] = useSearchParams();
  const dupesUrlFilters = useMemo(
    () => ({
      band: dupesSearchParams.get('band') as DedupBand | null,
      entityId: dupesSearchParams.get('book'),
    }),
    [dupesSearchParams],
  );
  const dupes = useDupesLane(toast, lane === 'dupes', dupesUrlFilters);
  // Expansion is a view concern and the two lanes key it on different id types,
  // so it is not shared state.
  const [dupesExpandedId, setDupesExpandedId] = useState<number | null>(null);

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
          {
            id: 'rescore',
            label: 'Rescore',
            scope: 'library',
            run: startJob('Rescore', () => api.rescoreDedupCandidates(false)),
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

      {lane === 'dupes' ? (
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
