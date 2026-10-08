// file: web/src/components/review/ReviewWorkspace.tsx
// version: 1.14.0
// guid: 8e0b4d59-1c76-42a3-95f8-7d2a6b3e0c81
// last-edited: 2026-10-07
//
// The unified review workspace: one screen for dedup, metadata apply, the
// review queue, and library repairs.
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

import { useCallback, useMemo, useState } from 'react';
import { useNavigate, useSearchParams } from 'react-router-dom';
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
import FormatListNumberedIcon from '@mui/icons-material/FormatListNumbered';
import * as api from '../../services/api';
import type { DedupBand } from '../../services/api';
import { useToast } from '../toast/ToastProvider';
import { CoverLightbox } from '../CoverLightbox';
import { coverFullSizeUrl } from '../../utils/coverUrl';
import { CommandBar, type CommandMenu } from './CommandBar';
import { normalizeViewMode, type SpineViewMode } from './spine/CompareSpine';
import { DupesPanel } from './DupesPanel';
import { RegroupPanel } from './RegroupPanel';
import { RepairsPanel } from './RepairsPanel';
import { MetadataPanel } from './MetadataPanel';
import { ReplaceConfirmDialog } from './ReplaceConfirmDialog';
import { useDupesLane } from './lanes/useDupesLane';
import { useMetadataLane } from './lanes/useMetadataLane';
import { useRegroupLane } from './lanes/useRegroupLane';
import { useRepairsLane } from './lanes/useRepairsLane';
import { useDedupPipeline } from './useDedupPipeline';
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
  const navigate = useNavigate();
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

  // useCallback, not an inline arrow in the JSX below.
  //
  // This is load-bearing rather than tidy. DupesSpine memoizes its rows and
  // derives their stable `handlers` object from the individual callbacks in the
  // ctx it is handed. An inline arrow here gets a new identity on every render
  // of this component -- and a dupes checkbox tick re-renders this component,
  // because useDupesLane's state lives here -- so `handlers` would change on
  // every tick and every memoized row would re-render anyway. The memo would be
  // present, correct, and completely inert.
  const toggleDupesExpanded = useCallback(
    (id: number) => setDupesExpandedId((cur) => (cur === id ? null : id)),
    []
  );
  // Rescore-with-apply asks first. See the command pair below.
  const [rescoreConfirmOpen, setRescoreConfirmOpen] = useState(false);
  const [confirmRefetchStale, setConfirmRefetchStale] = useState(false);
  const regroup = useRegroupLane(toast, lane === 'regroup');
  // Gated like the others: no fixer list, rows or trial polls unless visible.
  const repairs = useRepairsLane(toast, lane === 'repairs');

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

  // POST /dedup/rescore is synchronous and returns counts, so it is not a
  // "started, watch the bell" job: the counts ARE the result.
  const rescoreSummary = (r: Partial<api.DedupRescoreResult>) =>
    `${(r.changed ?? 0).toLocaleString()} of ${(r.inspected ?? 0).toLocaleString()} waiting pairs`;
  const previewRescore = async () => {
    try {
      const r = await api.rescoreDedupCandidates(false);
      toast(`Score preview: ${rescoreSummary(r)} would change confidence. Nothing was saved.`, 'info');
    } catch {
      toast('Failed to preview new scores.', 'error');
    }
  };

  // The Dedup menu's one-button run. When it finishes, show its results.
  const refreshDupes = dupes.refresh;
  const showDupes = useCallback(() => {
    setLane('dupes');
    refreshDupes();
  }, [refreshDupes]);
  const dedupPipeline = useDedupPipeline({ toast, onFinished: showDupes });

  const menus: CommandMenu[] = useMemo(
    () => [
      {
        id: 'dedup',
        label: 'Dedup',
        // Owner, 2026-09-27: one button that does everything, then an Advanced
        // section only for people who turned it on. Each description was
        // checked against the op its item enqueues; dedupPipeline.ts has the
        // one-button run's order and why.
        simple: [
          {
            id: 'simple',
            commands: [
              {
                id: 'find-all-duplicates',
                label: 'Find all duplicates',
                scope: 'library',
                primary: true,
                description:
                  'Runs every duplicate check in the right order, then shows what it found in the Dupes tab for you to review. Takes a while on a big library.',
                disabledReason: dedupPipeline.busy
                  ? 'Already running — progress is shown above the list.'
                  : undefined,
                run: () => void dedupPipeline.request('all'),
              },
              {
                // dedup.full-scan, the scan that fills the Dupes tab's queue.
                // It used to start dedup.book-scan, whose groups lived only in
                // a 30-minute server cache shown on the /dedup page; see
                // dedupPipeline.ts.
                id: 'full-rescan',
                label: 'Force full rescan',
                scope: 'library',
                description:
                  'Rechecks every book for copies by identical files, matching titles and similar title and author, and rescores every pair. Only the find step: it does not refresh audio or AI evidence. Results appear in the Dupes tab.',
                disabledReason: dedupPipeline.busy
                  ? 'A duplicate check is already running — progress is shown above the list.'
                  : undefined,
                run: () => void dedupPipeline.request('rescan'),
              },
            ],
          },
        ],
        advanced: [
          {
            id: 'scoring',
            title: 'Find and score',
            commands: [
              {
                id: 'find-duplicates',
                label: 'Find duplicates',
                scope: 'library',
                description:
                  'Compares every book by exact matches and by similar title and author, then scores each possible pair. Identical copies are linked automatically if that is on in Settings → Dedup.',
                run: startJob('Duplicate scan', api.triggerDedupScan),
              },
              // Two commands, because there are two operations. This item used
              // to be plain "Rescore" passing `apply=false`, and answered
              // "Rescore started" while writing nothing.
              {
                id: 'rescore-dry-run',
                label: 'Preview new scores',
                scope: 'library',
                description:
                  'Works out how many waiting pairs would get a different confidence with the latest scoring rules. Changes nothing.',
                run: previewRescore,
              },
              {
                id: 'rescore-apply',
                label: 'Recalculate scores…',
                scope: 'library',
                description:
                  'Saves new confidence scores for every waiting pair from the evidence already collected. Asks first.',
                // The surface this replaced put Apply behind a dialog next to a
                // Dry Run button; one click from a menu would be a downgrade in
                // safety, so the confirm step carries over.
                run: () => setRescoreConfirmOpen(true),
              },
            ],
          },
          {
            id: 'evidence',
            title: 'Collect evidence',
            commands: [
              {
                id: 'embeddings',
                label: 'Build similarity data',
                scope: 'library',
                description:
                  'Creates the AI similarity data used to spot books with alike titles and authors, for every book missing an up-to-date copy. Run it before Find duplicates after adding many books.',
                run: startJob('Embedding scan', api.triggerEmbedScan),
              },
              {
                id: 'acoustic',
                label: 'Compare audio fingerprints',
                scope: 'library',
                description:
                  'Pairs up books whose audio matches, using the fingerprints already stored for their files. Does not create new fingerprints.',
                run: startJob('AcoustID scan', api.triggerDedupAcoustID),
              },
              {
                id: 'ai-review',
                label: 'AI review of unclear pairs',
                scope: 'library',
                description:
                  'Asks the AI to judge pairs whose score is neither clearly a match nor clearly not. May merge pairs it is sure about if AI auto-merge is on in Settings → Dedup.',
                run: startJob('AI review', api.triggerDedupLLM),
              },
            ],
          },
          {
            id: 'maintenance',
            title: 'Maintenance',
            commands: [
              {
                id: 'reconcile',
                label: 'Match missing files',
                scope: 'library',
                description:
                  'Looks for books whose files have gone missing and matches them to untracked files on disk. Review the matches on the Dedup page under Reconcile.',
                run: startJob('Reconcile scan', api.startReconcileScan),
              },
              {
                id: 'manage-labels',
                label: 'Manage labels',
                scope: 'view',
                description:
                  'Opens your past "duplicate / not a duplicate" decisions, which are used to check and tune the scoring.',
                run: () => {
                  // Router navigation: a full page load would kill a
                  // one-button run in progress.
                  navigate('/dedup/labels');
                },
              },
            ],
          },
        ],
      },
      {
        id: 'metadata',
        label: 'Metadata',
        simple: [
          {
            id: 'simple',
            commands: [
              {
                // Id kept from the old "Search providers…" item: same call.
                id: 'search-providers',
                label: 'Find metadata for unmatched books',
                scope: 'library',
                primary: true,
                description:
                  'Searches the metadata providers for every book that has no match yet and lists what they found here for review. Nothing changes on your books until you apply it.',
                run: startJob('Provider search', () =>
                  api.batchFetchCandidates({ selection: { filter: { only_unmatched: true } } })
                ),
              },
            ],
          },
          {
            id: 'selected',
            title: 'Books you have ticked',
            commands: [
              {
                id: 'bulk-search-selected',
                label: 'Search again for selected',
                scope: 'selection',
                description: 'Asks the providers for fresh results for just the books you have ticked.',
                disabledReason:
                  metadata.selectedIds.size === 0 ? 'Select one or more books first.' : undefined,
                run: startJob('Search for selected', () =>
                  api.batchFetchCandidates({ book_ids: [...metadata.selectedIds] })
                ),
              },
              {
                id: 'apply-selected-fields',
                label:
                  metadata.bulkApplyMode === 'replace'
                    ? 'Apply selected fields, replace existing'
                    : 'Apply selected fields',
                scope: 'selection',
                description:
                  metadata.bulkApplyMode === 'replace'
                    ? 'Saves the chosen match onto each ticked book, overwriting details it already has. Asks first unless you turned that off.'
                    : 'Saves the chosen match onto each ticked book, filling in only details it is missing.',
                disabledReason:
                  metadata.applicableSelectedIds.length === 0
                    ? 'Select one or more books with a candidate first.'
                    : undefined,
                run: () =>
                  metadata.dispatch({
                    lane: 'metadata',
                    type: 'applySelected',
                    ids: metadata.applicableSelectedIds,
                  }),
              },
              {
                id: 'apply-all-fields',
                label:
                  metadata.bulkApplyMode === 'replace'
                    ? 'Apply all fields, replace existing'
                    : 'Apply all fields',
                scope: 'selection',
                description:
                  metadata.bulkApplyMode === 'replace'
                    ? 'Saves the match for every undecided row on this page, overwriting existing details. Asks first unless you turned that off.'
                    : 'Saves the match for every undecided row on this page, filling in only missing details.',
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
            ],
          },
        ],
        advanced: [
          {
            id: 'files',
            title: 'Audio files',
            commands: [
              {
                id: 'write-back',
                label: 'Write details into audio files',
                scope: 'selection',
                description:
                  "Copies each ticked book's saved title, author and other details into its audio files' tags. This changes the files themselves and can rename them.",
                disabledReason:
                  metadata.selectedIds.size === 0 ? 'Select one or more books first.' : undefined,
                run: startJob('Tag write-back', () =>
                  api.batchWriteBackMetadata([...metadata.selectedIds])
                ),
              },
            ],
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
    // startJob and previewRescore close over `toast` only, which is stable
    // from the provider. bulkApplyMode was missing here, so the Apply labels
    // could show the previous mode until something else changed.
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [
      metadata.selectedIds,
      metadata.applicableSelectedIds,
      metadata.allVisiblePendingIds,
      metadata.filteredResults,
      metadata.dispatch,
      metadata.bulkApplyMode,
      dedupPipeline.busy,
      dedupPipeline.request,
      navigate,
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
          onChange={(_, v: SpineViewMode | null) => v && setViewMode(normalizeViewMode(v))}
          aria-label="Comparison layout"
          sx={{ ml: 'auto' }}
        >
          <ToggleButton value="compact" aria-label="Compact rows">
            <ViewListIcon fontSize="small" />
          </ToggleButton>
          <ToggleButton value="two-column" aria-label="Two columns">
            <ViewColumnIcon fontSize="small" />
          </ToggleButton>
          {/* Every ranked search candidate per book (was 'auto' until
              2026-10-07). The dupes lane renders it as its compact view. */}
          <ToggleButton value="candidates" aria-label="Candidates">
            <FormatListNumberedIcon fontSize="small" />
          </ToggleButton>
        </ToggleButtonGroup>
      </Box>

      {/* The one-button dedup run's progress / merge prompt / result. */}
      {dedupPipeline.ui}

      {/* Every lane has an explicit branch: the last one falls through to
          metadata, so a lane missing here would silently show metadata. */}
      {lane === 'repairs' ? (
        <RepairsPanel repairs={repairs} />
      ) : lane === 'regroup' ? (
        <RegroupPanel regroup={regroup} />
      ) : lane === 'dupes' ? (
        <DupesPanel
          dupes={dupes}
          viewMode={viewMode}
          expandedId={dupesExpandedId}
          onToggleExpand={toggleDupesExpanded}
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
        <MetadataPanel
          metadata={metadata}
          viewMode={viewMode}
          unmatchedCount={unmatchedCount}
          onRefetchStale={() => setConfirmRefetchStale(true)}
          toast={toast}
        />
      )}

      {/*
        The Replace bulk-apply prompt. The lane parks every Replace
        applySelected dispatch (action bar, group Apply All, the command menu's
        Apply selected / all fields) in `pendingReplace`, so this is the one
        prompt for all of them. Mounted only while open so the "Don't ask me
        again" box starts unticked each time.
      */}
      {metadata.pendingReplace && (
        <ReplaceConfirmDialog
          count={metadata.pendingReplace.ids.length}
          onConfirm={metadata.confirmReplace}
          onCancel={metadata.cancelReplace}
        />
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
              // Synchronous: report what it saved rather than "started".
              void api.rescoreDedupCandidates(true).then(
                (r) => toast(`Scores saved: ${rescoreSummary(r)} changed confidence.`, 'success'),
                () => toast('Failed to recalculate scores.', 'error')
              );
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

        The count is the server summary's `stale`, the same number the chip
        shows, and the POST is {stale: true} so the server resolves the set
        with that count's predicate. This used to be a client-derived id list
        built from the reviewable bucket only: the chip read "3,511 stale"
        while the dialog offered 10.
      */}
      <Dialog open={confirmRefetchStale} onClose={() => setConfirmRefetchStale(false)}>
        <DialogTitle>Refetch {metadata.summary.stale.toLocaleString()} stale books?</DialogTitle>
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
              void metadata.refetchStale();
            }}
          >
            Refetch {metadata.summary.stale.toLocaleString()}
          </Button>
        </DialogActions>
      </Dialog>

      {/* Cover lightbox -- shared by every CompareSpine cover, the "Current"
          and the "Proposed" one alike. This was an inline `maxWidth="sm"`
          Dialog whose image was `max-width: 100%` of a shrink-wrapped Paper;
          CoverLightbox bounds the image in viewport units instead, asks for
          the provider's full-size variant, and says so when the image fails
          to load rather than collapsing to a few pixels. */}
      <CoverLightbox
        open={Boolean(metadata.previewCover)}
        src={metadata.previewCover ? coverFullSizeUrl(metadata.previewCover) : null}
        onClose={() => metadata.setPreviewCover(null)}
      />
    </Box>
  );
}
