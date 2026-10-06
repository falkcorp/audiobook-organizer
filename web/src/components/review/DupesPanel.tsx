// file: web/src/components/review/DupesPanel.tsx
// version: 1.7.0
// guid: 1d6f8a03-7c25-4e91-b840-2a5c9e3b7d14
// last-edited: 2026-10-06
//
// The dupes lane's full surface: filter rail, spine, bulk bar, compare drawer.
//
// Assembled here rather than in ReviewWorkspace so the shell keeps its one-line
// lane branch. The metadata lane now has the matching MetadataPanel, so all
// three lanes own their own layout and the shell owns only lane selection and
// the cross-lane chrome.

import { useEffect, useState } from 'react';
import { useSearchParams } from 'react-router-dom';
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
  MenuItem,
  Pagination,
  Stack,
  TextField,
  Tooltip,
  Typography,
} from '@mui/material';
import type { DedupBand } from '../../services/api';
import { CandidateCompareDrawer } from '../dedup/CandidateCompareDrawer';
import { SelectAllMatchingBanner } from '../common/SelectAllMatchingBanner';
import { DupesSpine } from './spine/DupesSpine';
import type { SpineViewMode } from './spine/CompareSpine';
import { dupesLane } from './lanes/dupes';
import {
  DEDUP_SHORTCUTS,
  PAGE_SIZE_OPTIONS,
  type DedupStatusFilter,
  type DupesLane,
} from './lanes/useDupesLane';
import { DEFAULT_BULK_APPLY_MAX_ITEMS } from './lanes/useMetadataLane';

const BANDS: DedupBand[] = ['CERTAIN', 'HIGH', 'MEDIUM', 'REVIEW'];
const STATUSES: { value: DedupStatusFilter; label: string }[] = [
  { value: 'pending', label: 'Pending' },
  { value: 'merged', label: 'Merged' },
  { value: 'dismissed', label: 'Dismissed' },
  { value: '', label: 'All' },
];

/** A filter-scoped bulk action awaiting confirmation. */
type PendingBulk = 'mergeAllFiltered' | 'dismissAllFiltered';

export interface DupesPanelProps {
  dupes: DupesLane;
  viewMode: SpineViewMode;
  expandedId: number | null;
  onToggleExpand: (id: number) => void;
}

export function DupesPanel({ dupes, viewMode, expandedId, onToggleExpand }: DupesPanelProps) {
  const [searchParams, setSearchParams] = useSearchParams();
  const bookParam = searchParams.get('book');

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
                totalMatching={
                  dupes.matchingCount.state === 'ready' ? dupes.matchingCount.n : dupes.total
                }
                countState={matchingCountState}
                noun="pairs"
                unavailableReason={dupes.selectAllMatchingDisabledReason}
                testIdPrefix="dupes-select-all"
              />
            </Box>
          )}

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
            : `Merge all ${bulkCountLabel}matching pairs?`}
        </DialogTitle>
        <DialogContent>
          <DialogContentText>
            {pendingBulk === 'dismissAllFiltered'
              ? 'Every pending pair matching the current filter, on every page, is marked "not a duplicate".'
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
            {pendingBulk === 'dismissAllFiltered' ? 'Dismiss' : 'Merge'}
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
