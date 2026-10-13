// file: web/src/components/review/DupesPanel.tsx
// version: 1.15.0
// guid: 1d6f8a03-7c25-4e91-b840-2a5c9e3b7d14
// last-edited: 2026-10-13
//
// The dupes lane's full surface: filter rail, spine, bulk bar, compare drawer.
//
// Assembled here rather than in ReviewWorkspace so the shell keeps its one-line
// lane branch. The metadata lane now has the matching MetadataPanel, so all
// three lanes own their own layout and the shell owns only lane selection and
// the cross-lane chrome.

import { useEffect, useMemo, useState } from 'react';
import { Link as RouterLink, useSearchParams } from 'react-router-dom';
import {
  Alert,
  Box,
  Button,
  Checkbox,
  Chip,
  Dialog,
  DialogActions,
  DialogContent,
  DialogContentText,
  DialogTitle,
  Divider,
  FormControlLabel,
  LinearProgress,
  Link,
  MenuItem,
  Switch,
  Pagination,
  ToggleButton,
  ToggleButtonGroup,
  Stack,
  TextField,
  Tooltip,
  Typography,
} from '@mui/material';
import type { DedupBand } from '../../services/api';
import { CandidateCompareDrawer } from './compare/CandidateCompareDrawer';
import { SelectAllMatchingBanner } from '../common/SelectAllMatchingBanner';
import { DupesSpine } from './spine/DupesSpine';
import { DupesClusterSpine } from './spine/DupesClusterSpine';
import type { SpineViewMode } from './spine/CompareSpine';
import { dupesLane } from './lanes/dupes';
import {
  DEDUP_SHORTCUTS,
  PAGE_SIZE_OPTIONS,
  DEDUP_LAYERS,
  type DedupLayer,
  type DedupStatusFilter,
  type DupesLane,
} from './lanes/useDupesLane';
import { DEFAULT_BULK_APPLY_MAX_ITEMS } from './lanes/useMetadataLane';
import type { DedupSeriesSummary } from '../../services/api';
import { useSeriesPairs } from './lanes/useDupesClusterData';
import { buildClusters } from './lanes/dupesClusters';
import { DupesLabelsView } from './DupesLabelsView';
import { useDupesLabels, type DupesSubView } from './lanes/useDupesLabels';

const BANDS: DedupBand[] = ['CERTAIN', 'HIGH', 'MEDIUM', 'REVIEW'];
const STATUSES: { value: DedupStatusFilter; label: string }[] = [
  { value: 'pending', label: 'Pending' },
  { value: 'merged', label: 'Merged' },
  { value: 'dismissed', label: 'Dismissed' },
  { value: '', label: 'All' },
];

const LAYER_LABELS: Record<DedupLayer, string> = {
  exact: 'Exact',
  acoustid: 'Acoustic',
  embedding: 'Embedding',
  book_signature: 'Book signature',
  metadata_hash_match: 'Metadata hash',
  llm: 'LLM',
  manual: 'Manual',
};

/** A filter-scoped bulk action awaiting confirmation. */
type PendingBulk =
  | 'mergeAllFiltered'
  | 'keepAAllFiltered'
  | 'keepBAllFiltered'
  | 'dismissAllFiltered';

/** Side A is the older record (smaller ULID); B is the newer. */
function keepSideNoun(type: PendingBulk): 'older' | 'newer' {
  return type === 'keepAAllFiltered' ? 'older' : 'newer';
}

/**
 * The By-series list of the Clusters view: one row per series that has pending
 * cluster candidates, each with a link-all that asks first and names its count.
 * Four states: loading, error, empty, populated.
 */
function SeriesList({ dupes }: { dupes: DupesLane }) {
  const { summary, linking, linkSeries } = dupes.series;
  const [confirming, setConfirming] = useState<DedupSeriesSummary | null>(null);
  // What a series row's count opens: its pairs, its clusters or its books.
  const [detail, setDetail] = useState<{
    row: DedupSeriesSummary;
    view: 'pairs' | 'clusters' | 'books';
  } | null>(null);
  const detailPairs = useSeriesPairs(
    detail ? detail.row.series_id : null,
    detail !== null && detail.view !== 'pairs'
  );
  const detailClusters = useMemo(
    () => (detailPairs.state === 'ready' ? buildClusters(detailPairs.pairs) : []),
    [detailPairs]
  );

  return (
    <Box sx={{ p: 2 }} data-testid="dupes-series-list">
      {summary.state === 'loading' && <LinearProgress />}
      {summary.state === 'error' && (
        <Alert severity="error" data-testid="dupes-series-error">
          {summary.message}
        </Alert>
      )}
      {summary.state === 'ready' && summary.rows.length === 0 && (
        <Typography color="text.secondary" sx={{ textAlign: 'center', p: 2 }}>
          No series have pending duplicate clusters.
        </Typography>
      )}
      {summary.state === 'ready' && (
        <Stack spacing={1}>
          {summary.rows.map((row) => (
            <Stack
              key={row.series_id}
              direction="row"
              spacing={2}
              data-testid={`dupes-series-${row.series_id}`}
              sx={{
                alignItems: 'center',
                flexWrap: 'wrap',
                p: 1.5,
                border: 1,
                borderRadius: 1,
                borderColor: 'divider',
              }}
            >
              <Typography sx={{ fontWeight: 600, flex: 1, minWidth: 160 }}>
                {row.series_name}
              </Typography>
              {/* Each count opens the pairs it counts. */}
              <Stack direction="row" spacing={0.5} useFlexGap sx={{ flexWrap: 'wrap' }}>
                <Chip
                  size="small"
                  variant="outlined"
                  label={`${row.cluster_count.toLocaleString()} cluster${row.cluster_count === 1 ? '' : 's'}`}
                  onClick={() => setDetail({ row, view: 'clusters' })}
                  data-testid={`dupes-series-clusters-${row.series_id}`}
                />
                <Chip
                  size="small"
                  variant="outlined"
                  label={`${row.book_count.toLocaleString()} book${row.book_count === 1 ? '' : 's'}`}
                  onClick={() => setDetail({ row, view: 'books' })}
                  data-testid={`dupes-series-books-${row.series_id}`}
                />
                <Chip
                  size="small"
                  variant="outlined"
                  label={`${row.candidate_count.toLocaleString()} pending pair${row.candidate_count === 1 ? '' : 's'}`}
                  onClick={() => setDetail({ row, view: 'pairs' })}
                  data-testid={`dupes-series-pairs-${row.series_id}`}
                />
              </Stack>
              <Button
                size="small"
                variant="outlined"
                color="warning"
                disabled={dupes.busy || linking !== null}
                onClick={() => setConfirming(row)}
              >
                Link all pending pairs in this series
              </Button>
            </Stack>
          ))}
        </Stack>
      )}

      <Dialog
        open={detail !== null}
        onClose={() => setDetail(null)}
        fullWidth
        maxWidth="sm"
        data-testid="dupes-series-detail"
      >
        <DialogTitle>
          {detail?.row.series_name}:{' '}
          {detail?.view === 'clusters'
            ? 'clusters'
            : detail?.view === 'books'
              ? 'books'
              : 'pending pairs'}
        </DialogTitle>
        <DialogContent>
          {detailPairs.state === 'loading' && <LinearProgress />}
          {detailPairs.state === 'error' && (
            <Alert severity="error" data-testid="dupes-series-detail-error">
              {detailPairs.message}
            </Alert>
          )}
          {detailPairs.state === 'ready' && detailPairs.pairs.length === 0 && (
            <Typography color="text.secondary">No pending pairs found for this series.</Typography>
          )}
          {detailPairs.state === 'ready' && detailPairs.pairs.length > 0 && (
            <Typography variant="caption" color="text.secondary" data-testid="dupes-series-detail-count">
              {detail?.view === 'clusters'
                ? `${detailClusters.length.toLocaleString()} cluster${detailClusters.length === 1 ? '' : 's'}`
                : detail?.view === 'books'
                  ? `${new Set(detailClusters.flatMap((c) => c.bookIds)).size.toLocaleString()} books`
                  : `Showing ${detailPairs.pairs.length.toLocaleString()} of ${detailPairs.total.toLocaleString()} pending pairs`}
              {detail?.view !== 'pairs' && detailPairs.pairs.length < detailPairs.total
                ? ` (built from the first ${detailPairs.pairs.length.toLocaleString()} of ${detailPairs.total.toLocaleString()} pairs)`
                : ''}
            </Typography>
          )}
          {detailPairs.state === 'ready' && detail?.view === 'clusters' && (
            <Stack component="ul" spacing={1} sx={{ m: 0, pl: 2 }} data-testid="dupes-series-detail-clusters">
              {detailClusters.map((cl) => (
                <li key={cl.key}>
                  {cl.bookIds.length} books:{' '}
                  {cl.members.map((m) => m.book?.title ?? m.bookId).join(' / ')}
                </li>
              ))}
            </Stack>
          )}
          {detailPairs.state === 'ready' && detail?.view === 'books' && (
            <Stack component="ul" spacing={0.5} sx={{ m: 0, pl: 2 }} data-testid="dupes-series-detail-books">
              {detailClusters
                .flatMap((cl) => cl.members)
                .map((m) => (
                  <li key={m.bookId}>
                    <Link component={RouterLink} to={`/library/${m.bookId}`} underline="hover">
                      {m.book?.title ?? m.bookId}
                    </Link>
                  </li>
                ))}
            </Stack>
          )}
          {detailPairs.state === 'ready' && detail?.view === 'pairs' && (
            <Stack component="ul" spacing={0.5} sx={{ m: 0, pl: 2 }} data-testid="dupes-series-detail-pairs">
              {detailPairs.pairs.map((c) => (
                <li key={c.id}>
                  {`${c.book_a?.title ?? c.entity_a_id} / ${c.book_b?.title ?? c.entity_b_id}`}{' '}
                  <Button
                    size="small"
                    onClick={() => {
                      setDetail(null);
                      dupes.setDrawerCandidateId(c.id);
                    }}
                  >
                    Compare
                  </Button>
                </li>
              ))}
            </Stack>
          )}
        </DialogContent>
        <DialogActions>
          {detailPairs.state === 'ready' && detailPairs.loadMore && (
            <Button onClick={detailPairs.loadMore} data-testid="dupes-series-detail-more">
              Load more
            </Button>
          )}
          <Button onClick={() => setDetail(null)}>Close</Button>
        </DialogActions>
      </Dialog>

      <Dialog
        open={confirming !== null}
        onClose={() => setConfirming(null)}
        data-testid="dupes-series-confirm"
      >
        <DialogTitle>
          Link all {confirming?.candidate_count.toLocaleString()} pending pairs in{' '}
          {confirming?.series_name}?
        </DialogTitle>
        <DialogContent>
          <DialogContentText>
            Links {confirming?.cluster_count.toLocaleString()} cluster
            {confirming?.cluster_count === 1 ? '' : 's'} ({confirming?.book_count.toLocaleString()}{' '}
            books) into version groups, letting the server pick each primary. This cannot be undone.
          </DialogContentText>
        </DialogContent>
        <DialogActions>
          <Button onClick={() => setConfirming(null)}>Cancel</Button>
          <Button
            variant="contained"
            color="warning"
            data-testid="dupes-series-confirm-btn"
            onClick={() => {
              const target = confirming;
              setConfirming(null);
              if (target) void linkSeries(target.series_id);
            }}
          >
            Link {confirming?.candidate_count.toLocaleString()} pairs
          </Button>
        </DialogActions>
      </Dialog>
    </Box>
  );
}

export interface DupesPanelProps {
  dupes: DupesLane;
  viewMode: SpineViewMode;
  expandedId: number | null;
  onToggleExpand: (id: number) => void;
  /**
   * Which surface of the lane shows: the pair Queue, or the Gold Labels
   * (Labels) / Suspicious sub-views. Owned by the workspace, which seeds it from
   * `?view=` once and lets the "Manage labels" command set it; never written
   * back to the URL.
   */
  subView: DupesSubView;
  onSubViewChange: (view: DupesSubView) => void;
}

const SUB_VIEW_OPTIONS: { value: DupesSubView; label: string }[] = [
  { value: 'queue', label: 'Queue' },
  { value: 'labels', label: 'Labels' },
  { value: 'suspicious', label: 'Suspicious' },
];

export function DupesPanel({
  dupes,
  viewMode,
  expandedId,
  onToggleExpand,
  subView,
  onSubViewChange,
}: DupesPanelProps) {
  const [searchParams, setSearchParams] = useSearchParams();
  const bookParam = searchParams.get('book');
  // Fetches only while Labels or Suspicious is showing.
  const labels = useDupesLabels(subView);

  // The URL is the source of truth for `book` and `band`, so a deep link from
  // FingerprintVisualsColumn lands on the right filter and a copied address bar
  // reproduces the view. The workspace reads them and passes them into
  // useDupesLane; this panel only WRITES them (the band chips below). It used
  // to also mirror them into lane state via an effect, which meant a deep link
  // rendered once unfiltered and fetched the whole pending set before
  // correcting itself.

  const selectedIds = [...dupes.selectedIds];
  const sel = dupes.selection;
  // "Select all N matching" is a selection of the FILTER: its bulk actions go
  // to the filter-scoped endpoints (bulk-link / bulk-reject), which re-resolve
  // the filter server-side and report what they did. Anything else acts on ids.
  const crossPage = sel.allMatching;
  const selectedCount = sel.selectedCount;
  // How far the cross-page number can be trusted: the lane swaps in the
  // server's bulk count once it arrives (the same number the confirm dialog
  // shows); until then nothing is shown, and on a failed count the list's
  // total is labelled as approximate.
  const matchingCountState =
    dupes.matchingCount.state === 'ready'
      ? undefined
      : dupes.matchingCount.state === 'error'
        ? ('approximate' as const)
        : ('counting' as const);
  // The server's count of the filter once it has arrived, the list's `total`
  // (a paging hint) until then. One value for the select-all banner and the
  // "(all N)" buttons so the two cannot name different numbers.
  const matchingN = dupes.matchingCount.state === 'ready' ? dupes.matchingCount.n : dupes.total;
  const selectedLabel = !crossPage
    ? selectedCount.toLocaleString()
    : matchingCountState === 'counting'
      ? '…'
      : `${matchingCountState === 'approximate' ? '~' : ''}${selectedCount.toLocaleString()}`;
  const [pendingBulk, setPendingBulk] = useState<PendingBulk | null>(null);
  // The confirmation's count comes from the SERVER (dupes.countMatching: the
  // same function the bulk endpoints re-evaluate), never from the list's
  // `total`, which is a paging hint. It is sent back as expected_total, so the
  // number the reviewer confirms is the number the server checks. Four states:
  // counting, failed, ready (possibly 0).
  const [bulkCount, setBulkCount] = useState<
    { state: 'loading' } | { state: 'error'; message: string } | { state: 'ready'; n: number }
  >({ state: 'loading' });
  const { countMatching } = dupes;
  useEffect(() => {
    if (pendingBulk === null) return;
    const ctrl = new AbortController();
    setBulkCount({ state: 'loading' });
    countMatching(ctrl.signal)
      .then((n) => {
        if (!ctrl.signal.aborted) setBulkCount({ state: 'ready', n });
      })
      .catch((err: unknown) => {
        if (ctrl.signal.aborted) return;
        setBulkCount({
          state: 'error',
          message: err instanceof Error ? err.message : 'Could not count the matching pairs',
        });
      });
    return () => ctrl.abort();
  }, [pendingBulk, countMatching]);
  const confirmedN = bulkCount.state === 'ready' ? bulkCount.n : null;
  const bulkCountLabel = confirmedN !== null ? `${confirmedN.toLocaleString()} ` : '';
  const overCap = confirmedN !== null && confirmedN > DEFAULT_BULK_APPLY_MAX_ITEMS;

  const runMergeSelected = () => {
    if (crossPage) setPendingBulk('mergeAllFiltered');
    else dupes.dispatch({ lane: 'dupes', type: 'mergeSelected', ids: selectedIds });
  };
  const runDismissSelected = () => {
    if (crossPage) setPendingBulk('dismissAllFiltered');
    else dupes.dispatch({ lane: 'dupes', type: 'dismissSelected', ids: selectedIds });
  };

  const subViewControl = (
    <Box sx={{ px: 2, py: 1, borderBottom: 1, borderColor: 'divider' }}>
      <ToggleButtonGroup
        size="small"
        exclusive
        value={subView}
        onChange={(_, v: DupesSubView | null) => v && onSubViewChange(v)}
        aria-label="Duplicates view"
      >
        {SUB_VIEW_OPTIONS.map(({ value, label }) => (
          <ToggleButton key={value} value={value} data-testid={`dupes-subview-${value}`}>
            {label}
          </ToggleButton>
        ))}
      </ToggleButtonGroup>
    </Box>
  );

  if (subView !== 'queue') {
    return (
      <>
        {subViewControl}
        <DupesLabelsView view={subView} labels={labels} onSelectView={onSubViewChange} />
      </>
    );
  }

  return (
    <>
      {subViewControl}
      <Box
        sx={{
          flex: 1,
          minHeight: 0,
          display: 'grid',
          gridTemplateColumns: { xs: '1fr', md: '320px 1fr' },
        }}
      >
        {/* ---------------- filter rail ---------------- */}
        <Box
          data-testid="dupes-rail"
          sx={{ borderRight: 1, borderColor: 'divider', p: 2, overflowY: 'auto' }}
        >
          <Stack spacing={2}>
            <TextField
              select
              size="small"
              label="Status"
              value={dupes.filters.status}
              onChange={(e) => dupes.setFilters({ status: e.target.value as DedupStatusFilter })}
            >
              {STATUSES.map((s) => (
                <MenuItem key={s.value} value={s.value}>
                  {s.label}
                </MenuItem>
              ))}
            </TextField>

            <Box>
              <Typography variant="overline" color="text.secondary">
                Band
              </Typography>
              <Stack direction="row" spacing={0.5} useFlexGap sx={{ flexWrap: 'wrap', mt: 0.5 }}>
                {BANDS.map((b) => (
                  <Chip
                    key={b}
                    label={b}
                    size="small"
                    data-testid={`band-chip-${b}`}
                    color={dupes.filters.band === b ? 'primary' : 'default'}
                    variant={dupes.filters.band === b ? 'filled' : 'outlined'}
                    onClick={() => {
                      const next = new URLSearchParams(searchParams);
                      if (dupes.filters.band === b) next.delete('band');
                      else next.set('band', b);
                      setSearchParams(next, { replace: true });
                    }}
                  />
                ))}
              </Stack>
            </Box>

            <Box>
              <Typography variant="overline" color="text.secondary">
                Layer
              </Typography>
              <Stack direction="row" spacing={0.5} useFlexGap sx={{ flexWrap: 'wrap', mt: 0.5 }}>
                <Chip
                  label={`All layers (${dupes.layerTotal.toLocaleString()})`}
                  size="small"
                  data-testid="layer-chip-all"
                  color={dupes.filters.layer === null ? 'primary' : 'default'}
                  variant={dupes.filters.layer === null ? 'filled' : 'outlined'}
                  // A no-op while already active: setFilters resets the page and
                  // the selection, which clicking the chip you are on must not.
                  onClick={() => {
                    if (dupes.filters.layer !== null) dupes.setFilters({ layer: null });
                  }}
                />
                {DEDUP_LAYERS.map((l) => (
                  // The count is from the stats the lane already fetched, at the
                  // status filter in force, so clicking the chip opens exactly
                  // the rows it counts; clicking the active chip clears the
                  // filter.
                  <Chip
                    key={l}
                    label={`${LAYER_LABELS[l]} (${dupes.countByLayer[l].toLocaleString()})`}
                    size="small"
                    data-testid={`layer-chip-${l}`}
                    color={dupes.filters.layer === l ? 'primary' : 'default'}
                    variant={dupes.filters.layer === l ? 'filled' : 'outlined'}
                    onClick={() => dupes.setFilters({ layer: dupes.filters.layer === l ? null : l })}
                  />
                ))}
              </Stack>
            </Box>

            <FormControlLabel
              control={
                <Checkbox
                  size="small"
                  checked={dupes.filters.bothUnmatched}
                  onChange={(e) => dupes.setFilters({ bothUnmatched: e.target.checked })}
                />
              }
              label={
                <Tooltip title="Pairs where neither book has matched metadata — the manual-matching triage view. Bulk merge is unavailable while this is on, because the bulk endpoint cannot express this filter.">
                  <span>Both unmatched</span>
                </Tooltip>
              }
            />

            <TextField
              size="small"
              // No longer "Search this page". The term round-trips to the
              // server like every other filter in this rail, so an empty
              // result means the queue holds no match -- not that the match
              // was on a page the reviewer had not reached. The old label was
              // accurate when written and became a lie the moment `q` landed.
              label="Search"
              placeholder="Title, author, path…"
              value={dupes.filters.search}
              onChange={(e) => dupes.setFilters({ search: e.target.value })}
            />

            <TextField
              select
              size="small"
              label="Rows per page"
              value={dupes.pageSize}
              onChange={(e) => dupes.setPageSize(Number(e.target.value))}
            >
              {PAGE_SIZE_OPTIONS.map((n) => (
                <MenuItem key={n} value={n}>
                  {n}
                </MenuItem>
              ))}
            </TextField>

            <Divider />

            <Typography variant="caption" color="text.secondary" data-testid="dupes-total">
              {dupes.total} candidate{dupes.total === 1 ? '' : 's'} match this filter
              {dupes.pendingTotal > 0 && ` — ${dupes.pendingTotal} pending in total`}
            </Typography>

            {dupes.totalPages > 1 && (
              <Pagination
                size="small"
                count={dupes.totalPages}
                page={dupes.page}
                onChange={(_, p) => dupes.setPage(p)}
              />
            )}

            <Button size="small" onClick={() => dupes.setShortcutHelpOpen(true)}>
              Keyboard shortcuts (?)
            </Button>
          </Stack>
        </Box>

        {/* ---------------- spine ---------------- */}
        <Box sx={{ minWidth: 0, overflowY: 'auto' }}>
          {dupes.loading && <LinearProgress />}
          {dupes.error && (
            <Alert severity="error" sx={{ m: 2 }}>
              {dupes.error}
            </Alert>
          )}

          {bookParam && (
            <Alert
              severity="info"
              sx={{ m: 2 }}
              data-testid="dupes-deeplink-banner"
              action={
                <Button
                  size="small"
                  onClick={() => {
                    const next = new URLSearchParams(searchParams);
                    next.delete('book');
                    setSearchParams(next, { replace: true });
                  }}
                >
                  Clear
                </Button>
              }
            >
              Showing duplicate candidates for one book.
            </Alert>
          )}

          {/*
            `ctx` is an object literal on purpose, and it is safe to leave it
            one: DupesSpine does not compare `ctx`, it lifts the four CALLBACKS
            out of it into a useMemo and resolves the per-row values itself. So
            what has to hold still is each callback, not this object.

            Which means: do NOT replace any of the four with an inline arrow.
            `onToggleSelect`, `onAction` and `onOpenCompare` come straight from
            useDupesLane (a useCallback and a setState setter), and
            `onToggleExpand` is a useCallback in ReviewWorkspace. An arrow in
            any of those four positions leaves the row memo present and inert.
            `isSelected` is exempt -- it is CALLED during DupesSpine's render
            and never held.
          */}
          {dupes.candidates.length > 0 && (
            <Box sx={{ px: 2, pt: 1 }}>
              <Stack direction="row" spacing={1} sx={{ alignItems: 'center' }}>
                <Checkbox
                  size="small"
                  checked={sel.header.checked}
                  indeterminate={sel.header.indeterminate}
                  disabled={sel.header.disabled}
                  onChange={sel.togglePage}
                  slotProps={{
                    input: { 'aria-label': `Select all ${dupes.candidates.length} on this page` },
                  }}
                />
                <Typography variant="caption" color="text.secondary">
                  Select page · Shift-click a checkbox to select a range
                </Typography>
              </Stack>
              <SelectAllMatchingBanner
                selection={sel}
                pageCount={dupes.candidates.length}
                totalMatching={matchingN}
                countState={matchingCountState}
                noun="pairs"
                unavailableReason={dupes.selectAllMatchingDisabledReason}
                testIdPrefix="dupes-select-all"
              />
            </Box>
          )}

          {viewMode === 'candidates' && (
            <Box sx={{ px: 2, pt: 1 }}>
              <FormControlLabel
                control={
                  <Switch
                    size="small"
                    checked={dupes.bySeries}
                    onChange={(e) => dupes.setBySeries(e.target.checked)}
                    slotProps={{ input: { 'aria-label': 'By series' } }}
                  />
                }
                label="By series"
              />
            </Box>
          )}

          {viewMode === 'candidates' && dupes.bySeries ? (
            <SeriesList dupes={dupes} />
          ) : viewMode === 'candidates' ? (
            <DupesClusterSpine
              clusters={dupes.clusters}
              candidates={dupes.candidates}
              overflow={dupes.clusterOverflow}
              emptyMessage={dupesLane.emptyMessage}
              deepLinkedBookId={bookParam}
              busy={dupes.busy}
              isSelected={sel.isSelected}
              onSetPairsSelected={dupes.setPairsSelected}
              onAction={dupes.dispatch}
              onOpenCompare={dupes.setDrawerCandidateId}
              bookLinkQuery={
                (dupes.filters.layer ? `&layer=${encodeURIComponent(dupes.filters.layer)}` : '') +
                (dupes.filters.band ? `&band=${encodeURIComponent(dupes.filters.band)}` : '')
              }
            />
          ) : (
            <DupesSpine
              candidates={dupes.candidates}
              viewMode={viewMode}
              emptyMessage={dupesLane.emptyMessage}
              deepLinkedBookId={bookParam}
              ctx={{
                isSelected: sel.isSelected,
                onToggleSelect: dupes.toggleSelect,
                onAction: dupes.dispatch,
                focusedId: dupes.candidates[dupes.focusedIndex]?.id ?? null,
                expandedId,
                onToggleExpand,
                onOpenCompare: dupes.setDrawerCandidateId,
              }}
            />
          )}
        </Box>
      </Box>

      {/* ---------------- bulk bar ---------------- */}
      <Box
        data-testid="dupes-action-bar"
        sx={{
          borderTop: 1,
          borderColor: 'divider',
          p: 1.5,
          display: 'flex',
          gap: 1,
          alignItems: 'center',
          flexWrap: 'wrap',
        }}
      >
        <Typography variant="body2" color="text.secondary" data-testid="dupes-selected-count">
          {selectedLabel} selected{crossPage ? ' (every page)' : ''}
        </Typography>
        <Button
          size="small"
          variant="contained"
          data-testid="merge-selected"
          disabled={selectedCount === 0 || dupes.busy}
          onClick={runMergeSelected}
        >
          {dupes.verbs.mergeSelected} ({selectedLabel})
        </Button>
        <Button
          size="small"
          data-testid="dismiss-selected"
          disabled={selectedCount === 0 || dupes.busy}
          onClick={runDismissSelected}
        >
          {dupes.verbs.dismissSelected} ({selectedLabel})
        </Button>
        {selectedCount > 0 && (
          <Button size="small" onClick={sel.clear} data-testid="dupes-clear-selection">
            Clear
          </Button>
        )}

        {/* Bulk keep-side over the whole filter. Pending only: the buttons are
            absent under any other status because the server refuses them, and
            the dispatch guard refuses too. A is the older record. */}
        {dupes.filters.status === 'pending' &&
          (['keepAAllFiltered', 'keepBAllFiltered'] as const).map((type) => (
            <Tooltip key={type} title={dupes.mergeAllFilteredDisabledReason ?? ''}>
              <span>
                <Button
                  size="small"
                  color="warning"
                  data-testid={type === 'keepAAllFiltered' ? 'keep-older-all' : 'keep-newer-all'}
                  disabled={Boolean(dupes.mergeAllFilteredDisabledReason) || dupes.busy}
                  onClick={() => setPendingBulk(type)}
                >
                  {dupes.verbs[type]} (all {matchingN.toLocaleString()})
                </Button>
              </span>
            </Tooltip>
          ))}

        <Box sx={{ ml: 'auto' }}>
          {/* Disabled controls do not fire pointer events, so the tooltip needs
              a wrapper to have something to attach to -- same pattern the
              command menu uses for its disabled items. */}
          <Tooltip title={dupes.mergeAllFilteredDisabledReason ?? ''}>
            <span>
              <Button
                size="small"
                color="warning"
                data-testid="merge-all-filtered"
                disabled={Boolean(dupes.mergeAllFilteredDisabledReason) || dupes.busy}
                onClick={() => setPendingBulk('mergeAllFiltered')}
              >
                {dupes.verbs.mergeAllFiltered}
              </Button>
            </span>
          </Tooltip>
        </Box>
      </Box>

      {/* Confirmation for every filter-scoped bulk action. It shows the count
          the reviewer is looking at and says the server re-checks the filter,
          because the server's own count is what the result toast reports. */}
      <Dialog
        open={pendingBulk !== null}
        onClose={() => setPendingBulk(null)}
        data-testid="dupes-bulk-confirm"
      >
        <DialogTitle>
          {pendingBulk === 'dismissAllFiltered'
            ? `Dismiss all ${bulkCountLabel}matching pairs?`
            : pendingBulk === 'keepAAllFiltered' || pendingBulk === 'keepBAllFiltered'
              ? `Keep the ${keepSideNoun(pendingBulk)} record of all ${bulkCountLabel}matching pairs?`
              : `Merge all ${bulkCountLabel}matching pairs?`}
        </DialogTitle>
        <DialogContent>
          <DialogContentText>
            {pendingBulk === 'dismissAllFiltered'
              ? 'Every pending pair matching the current filter, on every page, is marked "not a duplicate".'
              : pendingBulk === 'keepAAllFiltered' || pendingBulk === 'keepBAllFiltered'
                ? `Keeps the ${keepSideNoun(pendingBulk)} record of every one of the ${confirmedN !== null ? confirmedN.toLocaleString() : '…'} matching pairs and links the other as a version. This cannot be undone.`
                : 'Every pair matching the current filter, on every page, is linked into a version group. This cannot be undone.'}{' '}
            The server re-evaluates the filter when it runs and reports how many it actually
            changed; hand-pinned pairs are skipped and listed as failures. If the number of matching
            pairs changes before it runs, nothing is changed and you are asked again.
            {bulkCount.state === 'loading' && ' Counting the matching pairs…'}
            {confirmedN === 0 && ' Nothing matches the filter now.'}
            {overCap &&
              ` ${(confirmedN ?? 0).toLocaleString()} is over the server's bulk limit (bulk_apply_max_items, ${DEFAULT_BULK_APPLY_MAX_ITEMS.toLocaleString()} by default), so it will refuse and change nothing. Narrow the filter first.`}
          </DialogContentText>
          {bulkCount.state === 'error' && (
            <Alert severity="error" sx={{ mt: 2 }} data-testid="dupes-bulk-count-error">
              {bulkCount.message}
            </Alert>
          )}
        </DialogContent>
        <DialogActions>
          <Button onClick={() => setPendingBulk(null)}>Cancel</Button>
          <Button
            color="warning"
            variant="contained"
            data-testid="dupes-bulk-confirm-btn"
            disabled={confirmedN === null || confirmedN === 0}
            onClick={() => {
              const type = pendingBulk;
              setPendingBulk(null);
              if (type && confirmedN !== null) {
                dupes.dispatch({ lane: 'dupes', type, expectedTotal: confirmedN });
              }
            }}
          >
            {pendingBulk === 'dismissAllFiltered'
              ? 'Dismiss'
              : pendingBulk === 'keepAAllFiltered'
                ? 'Keep older'
                : pendingBulk === 'keepBAllFiltered'
                  ? 'Keep newer'
                  : 'Merge'}
            {confirmedN !== null ? ` ${confirmedN.toLocaleString()}` : ''}
          </Button>
        </DialogActions>
      </Dialog>

      <CandidateCompareDrawer
        candidateId={dupes.drawerCandidateId}
        onClose={() => dupes.setDrawerCandidateId(null)}
      />

      <Dialog
        open={dupes.shortcutHelpOpen}
        onClose={() => dupes.setShortcutHelpOpen(false)}
        data-testid="dupes-shortcut-help"
      >
        <DialogTitle>Keyboard shortcuts</DialogTitle>
        <DialogContent>
          <Stack spacing={1} sx={{ minWidth: 280 }}>
            {DEDUP_SHORTCUTS.map((s) => (
              <Stack
                key={s.keys}
                direction="row"
                spacing={2}
                sx={{ justifyContent: 'space-between' }}
              >
                <Chip label={s.keys} size="small" variant="outlined" />
                <Typography variant="body2">{s.action}</Typography>
              </Stack>
            ))}
          </Stack>
        </DialogContent>
      </Dialog>
    </>
  );
}
